package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/TONresistor/gocoon/pkg/cocoon"
)

// handleChatCompletions implements an OpenAI-compatible chat passthrough.
// The body is forwarded as-is to the upstream proxy via Session.RunQuery,
// and the proxy's streaming response is reframed as Server-Sent Events.
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

	if cp.cli == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "client not initialized")
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

	// Extract model + stream flag from JSON for routing.
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
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

	if probe.Stream {
		cp.streamSSE(w, stream)
		return
	}
	cp.collectAndRespond(w, stream)
}

// pickSession returns the first Ready session, or an error if none.
func (cp *ControlPlane) pickSession() (*cocoon.Session, error) {
	for _, s := range cp.cli.Sessions() {
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

// streamSSE forwards chunks as Server-Sent Events.
func (cp *ControlPlane) streamSSE(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	flusher, ok := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for chunk := range stream {
		if chunk.Err != nil {
			fmt.Fprintf(w, "data: %s\n\n", jsonEncode(map[string]any{
				"error": chunk.Err.Error(),
			}))
		} else {
			// The bytes the proxy sent are the upstream model output. We
			// pass them through as one SSE data event.
			if len(chunk.Bytes) > 0 {
				fmt.Fprintf(w, "data: %s\n\n", chunk.Bytes)
			}
		}
		if ok {
			flusher.Flush()
		}
		if chunk.IsFinal {
			break
		}
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if ok {
		flusher.Flush()
	}
}

// collectAndRespond collects all chunks into a single JSON response.
func (cp *ControlPlane) collectAndRespond(w http.ResponseWriter, stream <-chan cocoon.Chunk) {
	var buf []byte
	var lastErr error
	for chunk := range stream {
		if chunk.Err != nil {
			lastErr = chunk.Err
		}
		buf = append(buf, chunk.Bytes...)
	}
	if lastErr != nil && len(buf) == 0 {
		writeJSONError(w, http.StatusBadGateway, lastErr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf)
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
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
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

func jsonEncode(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return `{"error":"encode failed"}`
	}
	return strings.ReplaceAll(string(b), "\n", " ")
}
