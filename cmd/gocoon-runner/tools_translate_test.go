package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── requestHasTools ─────────────────────────────────────────────────────

func TestRequestHasTools(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"absent", `{"model":"x","messages":[]}`, false},
		{"empty array", `{"model":"x","tools":[]}`, false},
		{"one tool", `{"model":"x","tools":[{"type":"function","function":{"name":"a"}}]}`, true},
		{"three tools", `{"model":"x","tools":[{},{},{}]}`, true},
		{"malformed json", `{not json`, false},
		{"null tools", `{"tools":null}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := requestHasTools([]byte(tc.body)); got != tc.want {
				t.Fatalf("requestHasTools(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}

// ─── translateRequestBody (full request transform) ───────────────────────

func TestTranslateRequestBody_InjectIntoExistingSystem(t *testing.T) {
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"system","content":"You are helpful."},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"now","description":"clock","parameters":{}}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)

	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Fatalf("first msg should be system, got %v", sys["role"])
	}
	content := sys["content"].(string)
	if !strings.HasPrefix(content, "You are helpful.") {
		t.Errorf("system content lost original prefix: %q", content)
	}
	if !strings.Contains(content, "<tools>") || !strings.Contains(content, "</tools>") {
		t.Errorf("system content missing tool tags: %q", content)
	}
	if !strings.Contains(content, `"name":"now"`) {
		t.Errorf("system content missing tool name: %q", content)
	}
}

func TestTranslateRequestBody_CreateSystemIfAbsent(t *testing.T) {
	in := `{
		"model":"qwen",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"now"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	msgs := parsed["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages (synthetic system + user), got %d", len(msgs))
	}
	sys := msgs[0].(map[string]any)
	if sys["role"] != "system" {
		t.Errorf("expected synthetic system as first msg, got role=%v", sys["role"])
	}
	content := sys["content"].(string)
	if !strings.Contains(content, "# Tools") {
		t.Errorf("synthetic system content has no Tools header: %q", content)
	}
}

func TestTranslateRequestBody_RewriteAssistantToolCalls(t *testing.T) {
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":"","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"now","arguments":"{\"tz\":\"UTC\"}"}}
			]}
		],
		"tools":[{"type":"function","function":{"name":"now"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	msgs := parsed["messages"].([]any)
	// system, user, assistant
	if len(msgs) != 3 {
		t.Fatalf("expected 3 msgs, got %d", len(msgs))
	}
	asst := msgs[2].(map[string]any)
	if asst["role"] != "assistant" {
		t.Fatalf("third msg should be assistant, got %v", asst["role"])
	}
	if _, has := asst["tool_calls"]; has {
		t.Errorf("assistant.tool_calls should have been removed")
	}
	content := asst["content"].(string)
	if !strings.Contains(content, "<tool_call>") || !strings.Contains(content, "</tool_call>") {
		t.Errorf("assistant content missing <tool_call> tags: %q", content)
	}
	if !strings.Contains(content, `"name":"now"`) {
		t.Errorf("assistant content missing tool name: %q", content)
	}
	if !strings.Contains(content, `"tz":"UTC"`) {
		t.Errorf("assistant content missing tool args: %q", content)
	}
}

func TestTranslateRequestBody_RewriteToolMessageToUser(t *testing.T) {
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"user","content":"q"},
			{"role":"tool","tool_call_id":"call_1","content":"2026-05-08T12:00Z"}
		],
		"tools":[{"type":"function","function":{"name":"now"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	msgs := parsed["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("tool message should become user, got %v", last["role"])
	}
	content := last["content"].(string)
	if !strings.Contains(content, "<tool_response>") || !strings.Contains(content, "</tool_response>") {
		t.Errorf("missing <tool_response> tags: %q", content)
	}
	if !strings.Contains(content, "<![CDATA[2026-05-08T12:00Z]]>") {
		t.Errorf("missing CDATA payload: %q", content)
	}
}

func TestTranslateRequestBody_RewriteToolMessage_CDATAEscape(t *testing.T) {
	// Content containing the CDATA terminator must be safely escaped.
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"tool","tool_call_id":"x","content":"prefix]]>suffix"}
		],
		"tools":[{"type":"function","function":{"name":"x"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	msgs := parsed["messages"].([]any)
	last := msgs[len(msgs)-1].(map[string]any)
	content := last["content"].(string)
	if strings.Contains(content, "prefix]]>suffix") {
		t.Errorf("CDATA terminator not escaped: %q", content)
	}
	if !strings.Contains(content, "]]]]><![CDATA[>") {
		t.Errorf("expected escape sequence missing: %q", content)
	}
}

func TestTranslateRequestBody_SystemContentArray(t *testing.T) {
	// OpenAI accepts `content` as an array of parts. Original text must
	// be preserved when injecting the tool prompt.
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"system","content":[{"type":"text","text":"You are helpful."}]},
			{"role":"user","content":"hi"}
		],
		"tools":[{"type":"function","function":{"name":"now"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	sys := parsed["messages"].([]any)[0].(map[string]any)
	content := sys["content"].(string)
	if !strings.HasPrefix(content, "You are helpful.") {
		t.Errorf("system array content lost: %q", content)
	}
	if !strings.Contains(content, "<tools>") {
		t.Errorf("injection missing: %q", content)
	}
}

func TestTranslateRequestBody_AssistantContentArray(t *testing.T) {
	// Assistant message with array content + tool_calls. Original text
	// must precede the rewritten <tool_call> blocks.
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":[{"type":"text","text":"thinking out loud"}],"tool_calls":[
				{"id":"c1","type":"function","function":{"name":"now","arguments":"{}"}}
			]}
		],
		"tools":[{"type":"function","function":{"name":"now"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	asst := parsed["messages"].([]any)[2].(map[string]any)
	content := asst["content"].(string)
	if !strings.HasPrefix(content, "thinking out loud") {
		t.Errorf("assistant array content lost: %q", content)
	}
	if !strings.Contains(content, "<tool_call>") {
		t.Errorf("tool_call rewrite missing: %q", content)
	}
}

func TestTranslateRequestBody_ToolMessageContentArray(t *testing.T) {
	// Tool result delivered as array-of-parts must be wrapped intact.
	in := `{
		"model":"qwen",
		"messages":[
			{"role":"user","content":"q"},
			{"role":"tool","tool_call_id":"c1","content":[{"type":"text","text":"42"}]}
		],
		"tools":[{"type":"function","function":{"name":"x"}}]
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	last := parsed["messages"].([]any)[len(parsed["messages"].([]any))-1].(map[string]any)
	content := last["content"].(string)
	if !strings.Contains(content, "<![CDATA[42]]>") {
		t.Errorf("tool array content lost: %q", content)
	}
}

func TestMessageContentAsString(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string", "hi", "hi"},
		{"empty string", "", ""},
		{"single text part", []any{map[string]any{"type": "text", "text": "hi"}}, "hi"},
		{"two text parts", []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "text", "text": "b"},
		}, "a\nb"},
		{"skip non-text part", []any{
			map[string]any{"type": "text", "text": "a"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
			map[string]any{"type": "text", "text": "b"},
		}, "a\nb"},
		{"empty array", []any{}, ""},
		{"nil", nil, ""},
		{"unknown shape", 42, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := messageContentAsString(tc.in); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTranslateRequestBody_StripUnsupportedFields(t *testing.T) {
	in := `{
		"model":"qwen",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function","function":{"name":"x"}}],
		"tool_choice":"auto",
		"parallel_tool_calls":true,
		"store":true,
		"metadata":{"k":"v"},
		"modalities":["text"],
		"web_search_options":{},
		"safety_identifier":"u",
		"service_tier":"default",
		"prompt_cache_key":"k",
		"stream_options":{"include_usage":true}
	}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	for _, f := range unsupportedFields {
		if _, has := parsed[f]; has {
			t.Errorf("field %q should have been stripped", f)
		}
	}
}

func TestTranslateRequestBody_DefaultsAndStream(t *testing.T) {
	t.Run("forces stream=false when stream=true", func(t *testing.T) {
		in := `{
			"model":"qwen",
			"messages":[{"role":"user","content":"hi"}],
			"stream":true,
			"tools":[{"type":"function","function":{"name":"x"}}]
		}`
		out, _ := translateRequestBody([]byte(in))
		var parsed map[string]any
		mustJSON(t, out, &parsed)
		if parsed["stream"] != false {
			t.Errorf("stream should be false, got %v", parsed["stream"])
		}
	})
	t.Run("defaults presence_penalty when absent", func(t *testing.T) {
		in := `{
			"model":"qwen",
			"messages":[{"role":"user","content":"hi"}],
			"tools":[{"type":"function","function":{"name":"x"}}]
		}`
		out, _ := translateRequestBody([]byte(in))
		var parsed map[string]any
		mustJSON(t, out, &parsed)
		pp, ok := parsed["presence_penalty"].(float64)
		if !ok || pp != defaultPresencePenalty {
			t.Errorf("presence_penalty should default to %v, got %v", defaultPresencePenalty, parsed["presence_penalty"])
		}
	})
	t.Run("preserves explicit presence_penalty", func(t *testing.T) {
		in := `{
			"model":"qwen",
			"messages":[{"role":"user","content":"hi"}],
			"presence_penalty":0.3,
			"tools":[{"type":"function","function":{"name":"x"}}]
		}`
		out, _ := translateRequestBody([]byte(in))
		var parsed map[string]any
		mustJSON(t, out, &parsed)
		pp := parsed["presence_penalty"].(float64)
		if pp != 0.3 {
			t.Errorf("explicit presence_penalty must be preserved, got %v", pp)
		}
	})
}

func TestTranslateRequestBody_NoToolsPassthrough(t *testing.T) {
	in := `{"model":"qwen","messages":[{"role":"user","content":"hi"}]}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != in {
		t.Errorf("body without tools must pass through unchanged.\nin:  %s\nout: %s", in, string(out))
	}
}

func TestTranslateRequestBody_EmptyToolsArrayJustStrips(t *testing.T) {
	in := `{"model":"qwen","messages":[{"role":"user","content":"hi"}],"tools":[],"tool_choice":"auto"}`
	out, err := translateRequestBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	if _, has := parsed["tools"]; has {
		t.Errorf("empty tools must be stripped")
	}
	if _, has := parsed["tool_choice"]; has {
		t.Errorf("tool_choice must be stripped when tools is empty")
	}
	// stream / presence_penalty NOT touched in this branch, bypass injection.
	if _, has := parsed["stream"]; has {
		t.Errorf("empty-tools branch should not force stream=false")
	}
}

// ─── parseToolCallsFromText / extractJSONObject ──────────────────────────

func TestParseToolCallsFromText_Simple(t *testing.T) {
	text := `Sure. <tool_call>
{"name":"now","arguments":{"tz":"UTC"}}
</tool_call>`
	calls := parseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Name != "now" {
		t.Errorf("name = %q", calls[0].Name)
	}
	if !strings.Contains(calls[0].Arguments, `"tz":"UTC"`) {
		t.Errorf("args = %q", calls[0].Arguments)
	}
	if !strings.HasPrefix(calls[0].ID, "tc_") {
		t.Errorf("id = %q (want tc_ prefix)", calls[0].ID)
	}
}

func TestParseToolCallsFromText_NestedJSON(t *testing.T) {
	text := `<tool_call>{"name":"f","arguments":{"q":{"a":1,"b":[2,3,{"c":"}"}]},"s":"]"}}</tool_call>`
	calls := parseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d (text=%q)", len(calls), text)
	}
	if !strings.Contains(calls[0].Arguments, `"a":1`) {
		t.Errorf("nested args lost: %q", calls[0].Arguments)
	}
	// Brace inside string must not break the parser.
	if !strings.Contains(calls[0].Arguments, `"c":"}"`) {
		t.Errorf("string-escaped brace lost: %q", calls[0].Arguments)
	}
}

func TestParseToolCallsFromText_MultipleCalls(t *testing.T) {
	text := `<tool_call>{"name":"a","arguments":{}}</tool_call>
some text
<tool_call>{"name":"b","arguments":{"x":1}}</tool_call>`
	calls := parseToolCallsFromText(text)
	if len(calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(calls))
	}
	if calls[0].Name != "a" || calls[1].Name != "b" {
		t.Errorf("names = %q, %q", calls[0].Name, calls[1].Name)
	}
	if calls[0].ID == calls[1].ID {
		t.Errorf("ids should differ, got %q twice", calls[0].ID)
	}
}

func TestParseToolCallsFromText_MalformedSkipped(t *testing.T) {
	cases := []struct {
		name string
		text string
		want int
	}{
		{"no json", `<tool_call>nothing</tool_call>`, 0},
		{"unclosed brace", `<tool_call>{"name":"a"</tool_call>`, 0},
		{"missing name", `<tool_call>{"arguments":{}}</tool_call>`, 0},
		{"unclosed tag", `<tool_call>{"name":"a","arguments":{}}`, 0},
		{"good after bad", `<tool_call>broken</tool_call><tool_call>{"name":"x","arguments":{}}</tool_call>`, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := len(parseToolCallsFromText(tc.text))
			if got != tc.want {
				t.Errorf("got %d calls, want %d", got, tc.want)
			}
		})
	}
}

func TestParseToolCallsFromText_StripsThinkFirst(t *testing.T) {
	text := `<think>I'll call the tool. <tool_call>fake</tool_call> just kidding</think>
<tool_call>{"name":"real","arguments":{}}</tool_call>`
	calls := parseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call (only the real one), got %d", len(calls))
	}
	if calls[0].Name != "real" {
		t.Errorf("expected real, got %s", calls[0].Name)
	}
}

func TestParseToolCallsFromText_NullArgsNormalized(t *testing.T) {
	text := `<tool_call>{"name":"a","arguments":null}</tool_call>`
	calls := parseToolCallsFromText(text)
	if len(calls) != 1 {
		t.Fatalf("got %d", len(calls))
	}
	if calls[0].Arguments != "{}" {
		t.Errorf("null args should normalize to '{}', got %q", calls[0].Arguments)
	}
}

func TestExtractJSONObject(t *testing.T) {
	cases := []struct {
		name  string
		input string
		start int
		want  string
		ok    bool
	}{
		{"flat", `prefix {"a":1} suffix`, 7, `{"a":1}`, true},
		{"nested", `{"a":{"b":2}}`, 0, `{"a":{"b":2}}`, true},
		{"with strings containing braces", `{"s":"{}{}"}`, 0, `{"s":"{}{}"}`, true},
		{"escaped quote in string", `{"s":"a\"b"}`, 0, `{"s":"a\"b"}`, true},
		{"unclosed", `{"a":1`, 0, "", false},
		{"not at brace", `prefix`, 0, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractJSONObject(tc.input, tc.start)
			if ok != tc.ok || got != tc.want {
				t.Errorf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

// ─── stripThinkBlocks / stripThinkAndToolCalls ───────────────────────────

func TestStripThinkBlocks(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello", "hello"},
		{"<think>internal</think>visible", "visible"},
		{"a<think>x</think>b<think>y</think>c", "abc"},
		{"<think>unclosed everything", ""},
		{"<think></think>", ""},
	}
	for _, tc := range cases {
		got := stripThinkBlocks(tc.in)
		if got != tc.want {
			t.Errorf("stripThinkBlocks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripThinkAndToolCalls(t *testing.T) {
	in := "Sure!<think>plan</think> Calling.\n<tool_call>{\"name\":\"a\"}</tool_call>\nDone."
	got := stripThinkAndToolCalls(in)
	want := "Sure! Calling.\n\nDone."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ─── translateResponseBody ───────────────────────────────────────────────

func TestTranslateResponseBody_NoToolCalls(t *testing.T) {
	in := `{
		"choices":[{"index":0,"message":{"role":"assistant","content":"<think>hm</think>hi there"},"finish_reason":"stop"}]
	}`
	out, err := translateResponseBody([]byte(in))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	choices := parsed["choices"].([]any)
	c0 := choices[0].(map[string]any)
	msg := c0["message"].(map[string]any)
	if msg["content"] != "hi there" {
		t.Errorf("content not cleaned: %q", msg["content"])
	}
	if c0["finish_reason"] != "stop" {
		t.Errorf("finish_reason should remain stop, got %v", c0["finish_reason"])
	}
	if _, has := msg["tool_calls"]; has {
		t.Errorf("tool_calls must be absent when no calls extracted")
	}
}

func TestTranslateResponseBody_WithToolCalls(t *testing.T) {
	content := `Looking up the time. <tool_call>{"name":"now","arguments":{"tz":"UTC"}}</tool_call>`
	body := map[string]any{
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	}
	raw, _ := json.Marshal(body)
	out, err := translateResponseBody(raw)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	var parsed map[string]any
	mustJSON(t, out, &parsed)
	c0 := parsed["choices"].([]any)[0].(map[string]any)
	if c0["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason should be tool_calls, got %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]any)
	tc := msg["tool_calls"].([]any)
	if len(tc) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tc))
	}
	tc0 := tc[0].(map[string]any)
	if tc0["type"] != "function" {
		t.Errorf("type = %v", tc0["type"])
	}
	fn := tc0["function"].(map[string]any)
	if fn["name"] != "now" {
		t.Errorf("name = %v", fn["name"])
	}
	args := fn["arguments"].(string)
	if !strings.Contains(args, `"tz":"UTC"`) {
		t.Errorf("args lost: %q", args)
	}
	if msg["content"] != "Looking up the time." {
		t.Errorf("visible content not cleaned: %q", msg["content"])
	}
}

func TestTranslateResponseBody_NotJSONPassthrough(t *testing.T) {
	in := []byte(`not json at all`)
	out, err := translateResponseBody(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("non-JSON body should pass through unchanged")
	}
}

func TestTranslateResponseBody_NoChoicesPassthrough(t *testing.T) {
	in := []byte(`{"id":"x","object":"chat.completion"}`)
	out, err := translateResponseBody(in)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if string(out) != string(in) {
		t.Errorf("body without choices should pass through unchanged")
	}
}

// ─── round-trip: request + mock proxy response ───────────────────────────

func TestTranslateRoundTrip(t *testing.T) {
	clientReq := `{
		"model":"qwen",
		"messages":[
			{"role":"system","content":"Be concise."},
			{"role":"user","content":"What's the time in Tokyo?"}
		],
		"tools":[{
			"type":"function",
			"function":{
				"name":"datetime_now",
				"description":"current datetime",
				"parameters":{"type":"object","properties":{"timezone":{"type":"string"}}}
			}
		}]
	}`
	upstreamReq, err := translateRequestBody([]byte(clientReq))
	if err != nil {
		t.Fatalf("translate request: %v", err)
	}
	var upstream map[string]any
	mustJSON(t, upstreamReq, &upstream)
	if upstream["stream"] != false {
		t.Errorf("upstream req should have stream=false")
	}
	if _, has := upstream["tools"]; has {
		t.Errorf("upstream req should not have tools field")
	}
	upstreamMsgs := upstream["messages"].([]any)
	sys := upstreamMsgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "Be concise.") || !strings.Contains(sys, "datetime_now") {
		t.Errorf("system prompt malformed: %q", sys)
	}

	// Simulate proxy/model response
	mockResp := `{
		"id":"chatcmpl-x",
		"object":"chat.completion",
		"choices":[{
			"index":0,
			"message":{
				"role":"assistant",
				"content":"<think>I should call it.</think>Let me check.\n<tool_call>{\"name\":\"datetime_now\",\"arguments\":{\"timezone\":\"Asia/Tokyo\"}}</tool_call>"
			},
			"finish_reason":"stop"
		}]
	}`
	clientResp, err := translateResponseBody([]byte(mockResp))
	if err != nil {
		t.Fatalf("translate response: %v", err)
	}
	var clientObj map[string]any
	mustJSON(t, clientResp, &clientObj)
	c0 := clientObj["choices"].([]any)[0].(map[string]any)
	if c0["finish_reason"] != "tool_calls" {
		t.Errorf("finish_reason mismatch: %v", c0["finish_reason"])
	}
	msg := c0["message"].(map[string]any)
	tc := msg["tool_calls"].([]any)
	if len(tc) != 1 {
		t.Fatalf("expected 1 tool_call, got %d", len(tc))
	}
	fn := tc[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "datetime_now" {
		t.Errorf("tool name mismatch: %v", fn["name"])
	}
	if !strings.Contains(fn["arguments"].(string), "Asia/Tokyo") {
		t.Errorf("tool args lost: %v", fn["arguments"])
	}
	if msg["content"] != "Let me check." {
		t.Errorf("visible content unexpected: %q", msg["content"])
	}
}

// ─── helpers ─────────────────────────────────────────────────────────────

func mustJSON(t *testing.T, raw []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, string(raw))
	}
}
