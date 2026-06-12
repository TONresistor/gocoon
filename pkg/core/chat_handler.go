package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
)

// UsageInfo is the parsed usage block of a completed request, delivered to
// OpenAIProxy.OnUsage for metering.
type UsageInfo struct {
	Model            string
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	// TotalCostNano is the proxy-reported cost in nanoTON (empty if absent).
	TotalCostNano string
}

// OpenAIProxy serves OpenAI-compatible endpoints backed by a cocoon Engine.
// Shared by the local runner control plane and the public gateway.
//
// Response shapes for /v1/chat/completions, picked from the inbound body:
//   - `tools:[…]`  → request/response are translated; one JSON document
//   - `stream:true` → SSE (text/event-stream). When the upstream proxy
//     streams (GOCOON_UPSTREAM_STREAM=1), chunks pass through as they
//     arrive; otherwise the complete answer is converted into a single SSE
//     delta, so UI streaming code works either way. (Verified live
//     2026-06-12: the network proxy currently delivers EMPTY payload chunks
//     for stream:true, so conversion stays the default.)
//   - otherwise    → upstream chunks are collected; one JSON document
type OpenAIProxy struct {
	Engine *Engine
	Logger *slog.Logger
	// OnUsage, when set, is called after every completed request.
	OnUsage func(u UsageInfo)
	// MaxTokens / MaxCoefficient cap every query (0 = engine defaults).
	MaxTokens      int
	MaxCoefficient int
}

// HandleChatCompletions implements POST /v1/chat/completions.
func (p *OpenAIProxy) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	ApplyCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sess, err := p.pickSession()
	if err != nil {
		WriteJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err != nil {
		WriteJSONError(w, http.StatusBadRequest, "body read: "+err.Error())
		return
	}

	// Extract model from JSON for routing.
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)
	model, err := p.resolveModel(r.Context(), sess, probe.Model)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if model != probe.Model {
		body, err = rewriteJSONModel(body, model)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "model rewrite: "+err.Error())
			return
		}
	}

	hadTools := requestHasTools(body)
	wantStream := requestWantsStream(body) && !hadTools
	upstreamStream := wantStream && upstreamStreamEnabled()

	switch {
	case hadTools:
		body, err = translateRequestBody(body)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "tools translate: "+err.Error())
			return
		}
	case upstreamStream:
		// Body already carries stream:true; forward as-is.
	default:
		body, err = forceNonStream(body)
		if err != nil {
			WriteJSONError(w, http.StatusBadRequest, "force non-stream: "+err.Error())
			return
		}
	}

	q := cocoon.Query{
		Model: model,
		Body:  body,
		Path:  r.URL.RequestURI(),
		Headers: map[string]string{
			"Content-Type": r.Header.Get("Content-Type"),
		},
		Timeout:        5 * time.Minute,
		MaxTokens:      p.MaxTokens,
		MaxCoefficient: p.MaxCoefficient,
	}
	stream, err := sess.RunQuery(r.Context(), q)
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "RunQuery: "+err.Error())
		return
	}

	switch {
	case hadTools:
		p.collectTranslateRespond(w, model, stream)
	case upstreamStream:
		p.streamPassthrough(w, model, stream)
	case wantStream:
		p.collectRespondAsSSE(w, model, stream)
	default:
		p.collectAndRespond(w, model, stream)
	}
}

// HandleModels implements GET /v1/models.
func (p *OpenAIProxy) HandleModels(w http.ResponseWriter, r *http.Request) {
	ApplyCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, err := p.pickSession()
	if err != nil {
		WriteJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	models, err := sess.WorkerTypes(r.Context())
	if err != nil {
		WriteJSONError(w, http.StatusBadGateway, "models: "+err.Error())
		return
	}
	data := make([]any, 0, len(models))
	for _, model := range models {
		workers := make([]any, 0, len(model.Workers))
		for _, worker := range model.Workers {
			workers = append(workers, map[string]any{
				"coefficient":          worker.Coefficient,
				"running_requests":     worker.ActiveRequests,
				"max_running_requests": worker.MaxActiveRequests,
			})
		}
		data = append(data, map[string]any{
			"id":       model.Name,
			"object":   "model",
			"created":  0,
			"owned_by": "?",
			"workers":  workers,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// forceNonStream rewrites the body to set "stream": false.
func forceNonStream(body []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	obj["stream"] = false
	return json.Marshal(obj)
}

// requestWantsStream reports whether the inbound body asked for stream:true.
func requestWantsStream(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
}

// upstreamStreamEnabled gates real proxy-side streaming; see OpenAIProxy doc.
func upstreamStreamEnabled() bool {
	switch os.Getenv("GOCOON_UPSTREAM_STREAM") {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// pickSession returns the first Ready session, or an error if none.
func (p *OpenAIProxy) pickSession() (*cocoon.Session, error) {
	cli := p.Engine.Client()
	if cli == nil {
		return nil, errors.New("client not initialized")
	}
	for _, s := range cli.Sessions() {
		if s.Status() == cocoon.SessionReady {
			return s, nil
		}
	}
	return nil, errors.New("no ready proxy session yet")
}

func (p *OpenAIProxy) resolveModel(ctx context.Context, sess *cocoon.Session, requested string) (string, error) {
	models, err := sess.WorkerTypes(ctx)
	if err != nil {
		return "", fmt.Errorf("models: %w", err)
	}
	var fallback string
	for _, model := range models {
		if len(model.Workers) == 0 {
			continue
		}
		if fallback == "" {
			fallback = model.Name
		}
		if requested != "" && requested != "default" && requested == model.Name {
			return requested, nil
		}
	}
	if fallback == "" {
		return "", errors.New("no ready model workers advertised by proxy")
	}
	if requested == "" || requested == "default" {
		return fallback, nil
	}
	return "", fmt.Errorf("unknown model %q; available model: %s", requested, fallback)
}

func rewriteJSONModel(body []byte, model string) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, err
	}
	obj["model"] = model
	return json.Marshal(obj)
}

// reportUsage parses the usage block of a complete chat.completion document
// and forwards it to OnUsage.
func (p *OpenAIProxy) reportUsage(model string, completion []byte) {
	if p.OnUsage == nil {
		return
	}
	var doc struct {
		Usage struct {
			PromptTokens     int64           `json:"prompt_tokens"`
			CompletionTokens int64           `json:"completion_tokens"`
			TotalTokens      int64           `json:"total_tokens"`
			TotalCost        json.RawMessage `json:"total_cost"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(completion, &doc); err != nil {
		return
	}
	u := UsageInfo{
		Model:            model,
		PromptTokens:     doc.Usage.PromptTokens,
		CompletionTokens: doc.Usage.CompletionTokens,
		TotalTokens:      doc.Usage.TotalTokens,
	}
	if u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	if len(doc.Usage.TotalCost) > 0 {
		u.TotalCostNano = string(bytes.Trim(doc.Usage.TotalCost, `"`))
	}
	if u.TotalTokens == 0 && u.TotalCostNano == "" {
		return
	}
	p.OnUsage(u)
}

// collectAndRespond collects all chunks into a single JSON response.
func (p *OpenAIProxy) collectAndRespond(w http.ResponseWriter, model string, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	if lastErr != nil && len(buf) == 0 {
		WriteJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	p.reportUsage(model, buf)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// collectTranslateRespond collects the upstream response, then runs it
// through translateResponseBody so any <tool_call> blocks become an
// OpenAI-shape `tool_calls` array.
func (p *OpenAIProxy) collectTranslateRespond(w http.ResponseWriter, model string, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	// Translate path requires a complete OpenAI Chat Completions JSON to
	// produce a valid `tool_calls` array. Any upstream error invalidates
	// the buffer, so refuse rather than risk delivering corrupt output.
	if lastErr != nil {
		WriteJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	out, _ := translateResponseBody(buf)
	p.reportUsage(model, out)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

func collectChunks(stream <-chan cocoon.Chunk) ([]byte, error) {
	var buf []byte
	var lastErr error
	for chunk := range stream {
		if chunk.Err != nil {
			lastErr = chunk.Err
		}
		buf = append(buf, chunk.Bytes...)
	}
	return buf, lastErr
}

// streamPassthrough forwards upstream bytes as they arrive. If the upstream
// answers with a plain JSON document (first byte '{') instead of SSE, the
// response is collected and converted, so clients always get what they
// asked for.
func (p *OpenAIProxy) streamPassthrough(w http.ResponseWriter, model string, stream <-chan cocoon.Chunk) {
	var pending []byte
	for chunk := range stream {
		if chunk.Err != nil {
			p.finishSSEWithError(w, pending, chunk.Err)
			return
		}
		pending = append(pending, chunk.Bytes...)
		if trimmed := bytes.TrimLeft(pending, " \t\r\n"); len(trimmed) > 0 {
			if trimmed[0] == '{' {
				// Upstream ignored stream:true → collect fully, convert once.
				rest, lastErr := collectChunks(stream)
				pending = append(pending, rest...)
				if lastErr != nil {
					p.finishSSEWithError(w, nil, lastErr)
					return
				}
				p.reportUsage(model, pending)
				writeSSEFromCompletion(w, pending)
				return
			}
			break
		}
	}

	// Genuine SSE bytes (or stream ended while still whitespace-only).
	flusher, _ := w.(http.Flusher)
	startSSE(w)
	if len(pending) > 0 {
		_, _ = w.Write(pending)
		if flusher != nil {
			flusher.Flush()
		}
	}
	for chunk := range stream {
		if chunk.Err != nil {
			writeSSEError(w, chunk.Err)
			if flusher != nil {
				flusher.Flush()
			}
			return
		}
		if len(chunk.Bytes) > 0 {
			_, _ = w.Write(chunk.Bytes)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// collectRespondAsSSE serves an SSE response built from a non-streamed
// upstream answer: the full completion arrives, then is emitted as a single
// delta chunk plus [DONE]. UIs keep one streaming code path either way.
func (p *OpenAIProxy) collectRespondAsSSE(w http.ResponseWriter, model string, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	if lastErr != nil && len(buf) == 0 {
		WriteJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	p.reportUsage(model, buf)
	writeSSEFromCompletion(w, buf)
}

// finishSSEWithError reports an upstream error. If nothing was written yet,
// a plain JSON error response is used; otherwise an SSE error event.
func (p *OpenAIProxy) finishSSEWithError(w http.ResponseWriter, pending []byte, err error) {
	if len(bytes.TrimSpace(pending)) == 0 {
		WriteJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	startSSE(w)
	_, _ = w.Write(pending)
	writeSSEError(w, err)
}

func startSSE(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream; charset=utf-8")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
}

func writeSSEError(w http.ResponseWriter, err error) {
	payload, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": err.Error(),
			"type":    "gocoon_runner_error",
		},
	})
	fmt.Fprintf(w, "data: %s\n\n", payload)
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// writeSSEFromCompletion converts a complete chat.completion JSON document
// into a single chat.completion.chunk SSE event followed by [DONE].
func writeSSEFromCompletion(w http.ResponseWriter, completion []byte) {
	chunk, ok := completionToChunk(completion)
	startSSE(w)
	flusher, _ := w.(http.Flusher)
	if !ok {
		// Not parseable as a completion — relay raw payload as one event.
		fmt.Fprintf(w, "data: %s\n\n", bytes.TrimSpace(completion))
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", chunk)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// completionToChunk rewrites an OpenAI chat.completion document into the
// equivalent single chat.completion.chunk: message → delta, object renamed,
// usage preserved.
func completionToChunk(completion []byte) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(completion, &doc); err != nil {
		return nil, false
	}
	choices, ok := doc["choices"].([]any)
	if !ok {
		return nil, false
	}
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if message, ok := choice["message"].(map[string]any); ok {
			choice["delta"] = message
			delete(choice, "message")
		}
	}
	doc["object"] = "chat.completion.chunk"
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, false
	}
	return out, true
}

// ApplyCORS sets permissive CORS headers (the API is auth-gated by key, not
// origin; local runner endpoints are loopback-only).
func ApplyCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// WriteJSONError writes an OpenAI-style error body.
func WriteJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    "gocoon_runner_error",
		},
	}
	_ = json.NewEncoder(w).Encode(body)
}
