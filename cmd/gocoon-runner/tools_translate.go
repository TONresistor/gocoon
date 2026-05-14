package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Translates OpenAI tool-calling (request `tools` array, response
// `tool_calls`) to and from the XML tag prompt format the upstream proxy
// accepts. The proxy validator rejects `tools` and `tool_choice`, so tool
// definitions go into the system prompt as `<tools>…</tools>` and the model
// emits `<tool_call>{…}</tool_call>` blocks that we parse out.

const (
	toolPromptPreamble = "\n\n# Tools\n\n" +
		"You may call one or more functions to assist with the user query.\n\n" +
		"You are provided with function signatures within <tools></tools> XML tags:\n" +
		"<tools>\n"

	toolPromptPostamble = "</tools>\n\n" +
		"For each function call, return a json object with function name and arguments " +
		"within <tool_call></tool_call> XML tags:\n" +
		"<tool_call>\n" +
		`{"name": <function-name>, "arguments": <args-json-object>}` + "\n" +
		"</tool_call>"

	toolCallOpenTag      = "<tool_call>"
	toolCallCloseTag     = "</tool_call>"
	toolResponseOpenTag  = "<tool_response>"
	toolResponseCloseTag = "</tool_response>"
	thinkOpenTag         = "<think>"
	thinkCloseTag        = "</think>"

	defaultPresencePenalty = 1.5
)

// unsupportedFields are body keys the proxy validator rejects as
// "unknown option". We strip them when translation activates.
var unsupportedFields = []string{
	"tools",
	"tool_choice",
	"parallel_tool_calls",
	"store",
	"web_search_options",
	"safety_identifier",
	"service_tier",
	"metadata",
	"modalities",
	"prompt_cache_key",
	"stream_options",
}

// requestHasTools reports whether the body contains a non-empty `tools` array.
func requestHasTools(body []byte) bool {
	var probe struct {
		Tools []json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Tools) > 0
}

// messageContentAsString flattens an OpenAI Chat Completions message
// `content` field into a single string. Accepts the legacy `string` form
// and the modern `[]any` parts form (e.g. [{"type":"text","text":"..."}]).
// Non-text parts are skipped: the proxy is text-only, so image/audio
// inputs cannot be forwarded; preserving them would only mislead the
// caller. Unknown shapes return "".
func messageContentAsString(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, part := range c {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := p["type"].(string); t != "text" {
				continue
			}
			text, _ := p["text"].(string)
			if text == "" {
				continue
			}
			if sb.Len() > 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(text)
		}
		return sb.String()
	default:
		return ""
	}
}

// translateRequestBody applies all request-side transforms in one pass:
//   - inject tool definitions into the system message (creating one if absent)
//   - rewrite history: assistant tool_calls → text with <tool_call> blocks;
//     `role:tool` messages → `role:user` with <tool_response><![CDATA[…]]>
//   - strip fields the proxy rejects
//   - force `stream:false`
//   - default `presence_penalty:1.5` if absent (mitigates known repeat loops)
//
// Returns the rewritten JSON body. If `tools` is missing or empty, returns
// the input unchanged.
func translateRequestBody(body []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}

	toolsAny, ok := obj["tools"]
	if !ok {
		return body, nil
	}
	toolsArr, ok := toolsAny.([]any)
	if !ok || len(toolsArr) == 0 {
		delete(obj, "tools")
		delete(obj, "tool_choice")
		return json.Marshal(obj)
	}

	injection, err := buildToolInjection(toolsArr)
	if err != nil {
		return nil, fmt.Errorf("build tool injection: %w", err)
	}

	msgsAny, _ := obj["messages"].([]any)
	rewritten, sysFound := rewriteHistoryMessages(msgsAny, injection)
	if !sysFound {
		sysMsg := map[string]any{
			"role":    "system",
			"content": strings.TrimLeft(injection, "\n"),
		}
		rewritten = append([]any{sysMsg}, rewritten...)
	}
	obj["messages"] = rewritten

	for _, f := range unsupportedFields {
		delete(obj, f)
	}

	obj["stream"] = false

	if _, ok := obj["presence_penalty"]; !ok {
		obj["presence_penalty"] = defaultPresencePenalty
	}

	return json.Marshal(obj)
}

// buildToolInjection returns the system-prompt addition: preamble + one JSON
// line per tool spec + postamble.
func buildToolInjection(toolsArr []any) (string, error) {
	var sb strings.Builder
	sb.WriteString(toolPromptPreamble)
	for _, t := range toolsArr {
		line, err := json.Marshal(t)
		if err != nil {
			return "", err
		}
		sb.Write(line)
		sb.WriteByte('\n')
	}
	sb.WriteString(toolPromptPostamble)
	return sb.String(), nil
}

// rewriteHistoryMessages walks the messages array and rewrites:
//   - the first system message: appends the injection to its content
//   - assistant messages with tool_calls: serializes calls as <tool_call> text
//   - tool messages: rewrites to user with <tool_response> CDATA wrap
//
// Returns (newMessages, sysFound). Other roles are passed through unchanged.
func rewriteHistoryMessages(msgs []any, injection string) ([]any, bool) {
	out := make([]any, 0, len(msgs))
	sysFound := false
	for _, m := range msgs {
		obj, ok := m.(map[string]any)
		if !ok {
			out = append(out, m)
			continue
		}
		role, _ := obj["role"].(string)
		switch {
		case role == "system" && !sysFound:
			sysFound = true
			existing := messageContentAsString(obj["content"])
			obj["content"] = existing + injection
			out = append(out, obj)
		case role == "assistant":
			if rewritten, ok := rewriteAssistantToolCalls(obj); ok {
				out = append(out, rewritten)
			} else {
				out = append(out, obj)
			}
		case role == "tool":
			out = append(out, rewriteToolMessage(obj))
		default:
			out = append(out, obj)
		}
	}
	return out, sysFound
}

// rewriteAssistantToolCalls converts assistant.tool_calls into inline
// <tool_call> blocks appended to the message content. Returns (msg, true) if
// rewritten, (nil, false) if the message has no tool_calls.
func rewriteAssistantToolCalls(msg map[string]any) (map[string]any, bool) {
	tcAny, ok := msg["tool_calls"].([]any)
	if !ok || len(tcAny) == 0 {
		return nil, false
	}
	existing := messageContentAsString(msg["content"])
	var sb strings.Builder
	if existing != "" {
		sb.WriteString(existing)
	}
	for _, tc := range tcAny {
		tcObj, ok := tc.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tcObj["function"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := fn["name"].(string)
		argsStr, _ := fn["arguments"].(string)

		callObj := map[string]any{"name": name}
		var argsParsed any
		if json.Unmarshal([]byte(argsStr), &argsParsed) == nil {
			callObj["arguments"] = argsParsed
		} else {
			callObj["arguments"] = argsStr
		}
		callJSON, err := json.Marshal(callObj)
		if err != nil {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(toolCallOpenTag)
		sb.WriteByte('\n')
		sb.Write(callJSON)
		sb.WriteByte('\n')
		sb.WriteString(toolCallCloseTag)
	}
	msg["content"] = sb.String()
	delete(msg, "tool_calls")
	return msg, true
}

// rewriteToolMessage converts a `role:tool` message to `role:user` whose
// content wraps the tool result in <tool_response> CDATA tags.
func rewriteToolMessage(msg map[string]any) map[string]any {
	content := messageContentAsString(msg["content"])
	safe := strings.ReplaceAll(content, "]]>", "]]]]><![CDATA[>")
	wrapped := toolResponseOpenTag + "\n<![CDATA[" + safe + "]]>\n" + toolResponseCloseTag
	return map[string]any{
		"role":    "user",
		"content": wrapped,
	}
}

// extractedCall is one tool call recovered from the model's text response.
type extractedCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON string, ready for OpenAI tool_calls[].function.arguments
}

// parseToolCallsFromText extracts <tool_call>{…}</tool_call> blocks. Any
// <think> blocks are stripped first. Inside each tag pair we find the first
// '{' and consume a balanced JSON object. Malformed blocks are silently
// skipped.
func parseToolCallsFromText(text string) []extractedCall {
	cleaned := stripThinkBlocks(text)
	var calls []extractedCall
	searchFrom := 0
	for {
		open := strings.Index(cleaned[searchFrom:], toolCallOpenTag)
		if open < 0 {
			break
		}
		open += searchFrom
		contentStart := open + len(toolCallOpenTag)
		closeIdx := strings.Index(cleaned[contentStart:], toolCallCloseTag)
		if closeIdx < 0 {
			break
		}
		closeIdx += contentStart

		braceRel := strings.Index(cleaned[contentStart:closeIdx], "{")
		if braceRel < 0 {
			searchFrom = closeIdx + len(toolCallCloseTag)
			continue
		}
		braceStart := contentStart + braceRel

		raw, ok := extractJSONObject(cleaned, braceStart)
		if !ok {
			searchFrom = closeIdx + len(toolCallCloseTag)
			continue
		}

		var parsed struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil || parsed.Name == "" {
			searchFrom = closeIdx + len(toolCallCloseTag)
			continue
		}
		args := strings.TrimSpace(string(parsed.Arguments))
		if args == "" || args == "null" {
			args = "{}"
		}
		calls = append(calls, extractedCall{
			ID:        generateToolCallID(),
			Name:      parsed.Name,
			Arguments: args,
		})
		searchFrom = closeIdx + len(toolCallCloseTag)
	}
	return calls
}

// extractJSONObject reads a balanced JSON object starting at startIdx.
// Returns the substring (including the outer braces) and a success flag.
// Honors string boundaries and escape sequences.
func extractJSONObject(s string, startIdx int) (string, bool) {
	if startIdx >= len(s) || s[startIdx] != '{' {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := startIdx; i < len(s); i++ {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inString {
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[startIdx : i+1], true
			}
		}
	}
	return "", false
}

// stripThinkBlocks removes all <think>…</think> spans (non-greedy match).
// An unclosed <think> drops everything from the open tag onward.
func stripThinkBlocks(text string) string {
	var sb strings.Builder
	cursor := 0
	for {
		open := strings.Index(text[cursor:], thinkOpenTag)
		if open < 0 {
			sb.WriteString(text[cursor:])
			break
		}
		open += cursor
		sb.WriteString(text[cursor:open])
		closeRel := strings.Index(text[open:], thinkCloseTag)
		if closeRel < 0 {
			break
		}
		cursor = open + closeRel + len(thinkCloseTag)
	}
	return sb.String()
}

// stripThinkAndToolCalls returns the visible response text: think and
// tool_call blocks removed, surrounding whitespace trimmed.
func stripThinkAndToolCalls(text string) string {
	text = stripThinkBlocks(text)
	var sb strings.Builder
	cursor := 0
	for {
		open := strings.Index(text[cursor:], toolCallOpenTag)
		if open < 0 {
			sb.WriteString(text[cursor:])
			break
		}
		open += cursor
		sb.WriteString(text[cursor:open])
		closeRel := strings.Index(text[open:], toolCallCloseTag)
		if closeRel < 0 {
			break
		}
		cursor = open + closeRel + len(toolCallCloseTag)
	}
	return strings.TrimSpace(sb.String())
}

// translateResponseBody rewrites a non-stream OpenAI Chat Completions JSON
// response so that any <tool_call> blocks in choices[0].message.content
// become a `tool_calls` array. If at least one tool call is recovered,
// finish_reason becomes "tool_calls". The visible content is always cleaned
// of think and tool_call blocks.
//
// If the body is not a JSON object or has no choices, it is returned
// unchanged.
func translateResponseBody(body []byte) ([]byte, error) {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return body, nil
	}
	choices, ok := obj["choices"].([]any)
	if !ok || len(choices) == 0 {
		return body, nil
	}
	choice0, ok := choices[0].(map[string]any)
	if !ok {
		return body, nil
	}
	msg, ok := choice0["message"].(map[string]any)
	if !ok {
		return body, nil
	}
	content, _ := msg["content"].(string)
	calls := parseToolCallsFromText(content)

	if len(calls) == 0 {
		msg["content"] = stripThinkBlocks(content)
		choice0["message"] = msg
		choices[0] = choice0
		obj["choices"] = choices
		return json.Marshal(obj)
	}

	tcArr := make([]any, len(calls))
	for i, c := range calls {
		tcArr[i] = map[string]any{
			"id":   c.ID,
			"type": "function",
			"function": map[string]any{
				"name":      c.Name,
				"arguments": c.Arguments,
			},
		}
	}
	msg["tool_calls"] = tcArr
	msg["content"] = stripThinkAndToolCalls(content)
	choice0["message"] = msg
	choice0["finish_reason"] = "tool_calls"
	choices[0] = choice0
	obj["choices"] = choices
	return json.Marshal(obj)
}

// generateToolCallID returns a 9-byte URL-safe random ID prefixed with "tc_".
func generateToolCallID() string {
	var b [9]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "tc_fallback"
	}
	return "tc_" + base64.RawURLEncoding.EncodeToString(b[:])
}
