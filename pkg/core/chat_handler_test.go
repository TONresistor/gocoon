package core

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TONresistor/gocoon/pkg/cocoon"
)

func testProxy() *OpenAIProxy {
	return &OpenAIProxy{
		Engine: nil,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func chunkStream(chunks ...cocoon.Chunk) <-chan cocoon.Chunk {
	ch := make(chan cocoon.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch
}

func errAsProxy(msg string) error {
	return errors.New(msg)
}

func TestRequestWantsStream(t *testing.T) {
	if !requestWantsStream([]byte(`{"stream":true}`)) {
		t.Fatal("stream:true not detected")
	}
	if requestWantsStream([]byte(`{"stream":false}`)) || requestWantsStream([]byte(`{}`)) {
		t.Fatal("false positive")
	}
}

func TestCompletionToChunk(t *testing.T) {
	completion := []byte(`{
	  "id":"x","object":"chat.completion","model":"m",
	  "choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],
	  "usage":{"total_tokens":5}
	}`)
	out, ok := completionToChunk(completion)
	if !ok {
		t.Fatal("conversion failed")
	}
	var doc struct {
		Object  string `json:"object"`
		Choices []struct {
			Delta struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"delta"`
			Message *json.RawMessage `json:"message"`
			Finish  string           `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Object != "chat.completion.chunk" {
		t.Fatalf("object = %q", doc.Object)
	}
	if len(doc.Choices) != 1 || doc.Choices[0].Delta.Content != "hi" || doc.Choices[0].Message != nil {
		t.Fatalf("choices = %+v", doc.Choices)
	}
	if doc.Choices[0].Finish != "stop" || doc.Usage.TotalTokens != 5 {
		t.Fatalf("metadata lost: %+v", doc)
	}
}

func TestCollectRespondAsSSE(t *testing.T) {
	p := testProxy()
	rec := httptest.NewRecorder()
	completion := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"hello"}}]}`
	p.collectRespondAsSSE(rec, "m", chunkStream(
		cocoon.Chunk{Bytes: []byte(completion[:20])},
		cocoon.Chunk{Bytes: []byte(completion[20:]), IsFinal: true},
	))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"delta":{`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body = %q", body)
	}
}

func TestCollectRespondAsSSEErrorBeforeData(t *testing.T) {
	p := testProxy()
	rec := httptest.NewRecorder()
	p.collectRespondAsSSE(rec, "m", chunkStream(
		cocoon.Chunk{IsFinal: true, Err: errAsProxy("boom")},
	))
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("body = %q", rec.Body)
	}
}

func TestStreamPassthroughRelaysSSE(t *testing.T) {
	p := testProxy()
	rec := httptest.NewRecorder()
	p.streamPassthrough(rec, "m", chunkStream(
		cocoon.Chunk{Bytes: []byte("data: {\"choices\":[]}\n\n")},
		cocoon.Chunk{Bytes: []byte("data: [DONE]\n\n"), IsFinal: true},
	))
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "data: {\"choices\"") || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body = %q", body)
	}
}

func TestStreamPassthroughConvertsJSONFallback(t *testing.T) {
	p := testProxy()
	rec := httptest.NewRecorder()
	completion := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"plain"}}]}`
	p.streamPassthrough(rec, "m", chunkStream(
		cocoon.Chunk{Bytes: []byte(completion)},
	))
	body := rec.Body.String()
	if !strings.Contains(body, `"object":"chat.completion.chunk"`) || !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("body = %q", body)
	}
}

func TestStreamPassthroughErrorFirst(t *testing.T) {
	p := testProxy()
	rec := httptest.NewRecorder()
	p.streamPassthrough(rec, "m", chunkStream(
		cocoon.Chunk{IsFinal: true, Err: errAsProxy("offline")},
	))
	if rec.Code != 502 {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestOnUsageReported(t *testing.T) {
	var got *UsageInfo
	p := testProxy()
	p.OnUsage = func(u UsageInfo) { got = &u }
	rec := httptest.NewRecorder()
	completion := `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":25,"total_tokens":35,"total_cost":12345}}`
	p.collectAndRespond(rec, "test-model", chunkStream(cocoon.Chunk{Bytes: []byte(completion), IsFinal: true}))
	if got == nil {
		t.Fatal("OnUsage not called")
	}
	if got.Model != "test-model" || got.TotalTokens != 35 || got.PromptTokens != 10 || got.CompletionTokens != 25 {
		t.Fatalf("usage = %+v", got)
	}
	if got.TotalCostNano != "12345" {
		t.Fatalf("cost = %q", got.TotalCostNano)
	}
}
