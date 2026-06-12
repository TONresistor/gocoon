package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
)

// handleChatCompletions implements an OpenAI-compatible chat passthrough.
// Response shapes, picked from the inbound body:
//   - `tools:[…]`  → request/response are translated; one JSON document
//   - `stream:true` → SSE (text/event-stream). When the upstream proxy
//     streams (UpstreamStream enabled), chunks pass through as they arrive;
//     otherwise the complete answer is converted into a single SSE delta, so
//     UI streaming code works either way.
//   - otherwise    → upstream chunks are collected; one JSON document
//
// Upstream streaming is gated behind GOCOON_UPSTREAM_STREAM=1 until verified
// against the live network: the proxy was previously observed returning
// empty payload chunks for stream:true requests.
//
// CORS preflight (OPTIONS) returns 200 with permissive headers so the
// browser-side fetch passes.
func (cp *ControlPlane) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	cp.applyCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sess, err := cp.pickSession()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8*1024*1024))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "body read: "+err.Error())
		return
	}

	// Extract model from JSON for routing.
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)
	model, err := cp.resolveModel(r.Context(), sess, probe.Model)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if model != probe.Model {
		body, err = rewriteJSONModel(body, model)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "model rewrite: "+err.Error())
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
			writeJSONError(w, http.StatusBadRequest, "tools translate: "+err.Error())
			return
		}
	case upstreamStream:
		// Body already carries stream:true; forward as-is.
	default:
		body, err = forceNonStream(body)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "force non-stream: "+err.Error())
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
		Timeout: 5 * time.Minute,
	}
	stream, err := sess.RunQuery(r.Context(), q)
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "RunQuery: "+err.Error())
		return
	}

	switch {
	case hadTools:
		cp.collectTranslateRespond(w, stream)
	case upstreamStream:
		cp.streamPassthrough(w, stream)
	case wantStream:
		cp.collectRespondAsSSE(w, stream)
	default:
		cp.collectAndRespond(w, stream)
	}
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

// upstreamStreamEnabled gates real proxy-side streaming until it is verified
// against the live network.
func upstreamStreamEnabled() bool {
	switch os.Getenv("GOCOON_UPSTREAM_STREAM") {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// pickSession returns the first Ready session, or an error if none.
func (cp *ControlPlane) pickSession() (*cocoon.Session, error) {
	cli := cp.engine.Client()
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

func (cp *ControlPlane) resolveModel(ctx context.Context, sess *cocoon.Session, requested string) (string, error) {
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

// collectAndRespond collects all chunks into a single JSON response.
func (cp *ControlPlane) collectAndRespond(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	if lastErr != nil && len(buf) == 0 {
		writeJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
}

// collectTranslateRespond collects the upstream response, then runs it
// through translateResponseBody so any <tool_call> blocks become an
// OpenAI-shape `tool_calls` array.
func (cp *ControlPlane) collectTranslateRespond(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	// Translate path requires a complete OpenAI Chat Completions JSON to
	// produce a valid `tool_calls` array. Any upstream error invalidates
	// the buffer, so refuse rather than risk delivering corrupt output.
	if lastErr != nil {
		writeJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	out, _ := translateResponseBody(buf)
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

// streamPassthrough forwards upstream bytes as they arrive. The worker
// (vllm/sglang) already emits SSE frames for stream:true requests, so the
// bytes are relayed verbatim with a flush per chunk. If the upstream turns
// out to answer with a plain JSON document (first byte '{'), the response is
// collected and converted into SSE instead, so clients always get what they
// asked for.
func (cp *ControlPlane) streamPassthrough(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	var pending []byte
	for chunk := range stream {
		if chunk.Err != nil {
			cp.finishSSEWithError(w, pending, chunk.Err)
			return
		}
		pending = append(pending, chunk.Bytes...)
		if trimmed := bytes.TrimLeft(pending, " \t\r\n"); len(trimmed) > 0 {
			if trimmed[0] == '{' {
				// Upstream ignored stream:true → collect fully, convert once.
				rest, lastErr := collectChunks(stream)
				pending = append(pending, rest...)
				if lastErr != nil {
					cp.finishSSEWithError(w, nil, lastErr)
					return
				}
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
func (cp *ControlPlane) collectRespondAsSSE(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	buf, lastErr := collectChunks(stream)
	if lastErr != nil && len(buf) == 0 {
		writeJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	writeSSEFromCompletion(w, buf)
}

// finishSSEWithError reports an upstream error. If nothing was written yet,
// a plain JSON error response is used; otherwise an SSE error event.
func (cp *ControlPlane) finishSSEWithError(w http.ResponseWriter, pending []byte, err error) {
	if len(bytes.TrimSpace(pending)) == 0 {
		writeJSONError(w, http.StatusBadGateway, err.Error())
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

// handleModels lists models available on the connected proxies.
func (cp *ControlPlane) handleModels(w http.ResponseWriter, r *http.Request) {
	cp.applyCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sess, err := cp.pickSession()
	if err != nil {
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	models, err := sess.WorkerTypes(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusBadGateway, "models: "+err.Error())
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
	resp := map[string]any{
		"object": "list",
		"data":   data,
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (cp *ControlPlane) applyCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
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
