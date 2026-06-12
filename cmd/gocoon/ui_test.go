package main

import (
	"strings"
	"testing"
)

func TestParseUIChatResponseSplitsThinkAndUsage(t *testing.T) {
	raw := []byte(`{
	  "choices": [{
	    "message": {
	      "content": "<think>\nOkay, the user sent \"Hello\". I need to respond appropriately.\n</think>\n\nHello! How can I assist you today?"
	    }
	  }],
	  "usage": {
	    "prompt_tokens": 34,
	    "completion_tokens": 100,
	    "total_tokens": 134,
	    "prompt_tokens_details": {"cached_tokens": 11},
	    "completion_tokens_details": {"reasoning_tokens": 10},
	    "prompt_total_cost": 230,
	    "completion_total_cost": 1200,
	    "total_cost": 1430
	  }
	}`)

	got := parseUIChatResponse(raw)
	if got.Content != "Hello! How can I assist you today?" {
		t.Fatalf("Content = %q", got.Content)
	}
	if len(got.Thinking) != 1 || !strings.Contains(got.Thinking[0], `the user sent "Hello"`) {
		t.Fatalf("Thinking = %#v", got.Thinking)
	}
	if got.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if got.Usage.PromptTokens != 34 || got.Usage.CachedTokens != 11 || got.Usage.CompletionTokens != 100 ||
		got.Usage.ReasoningTokens != 10 || got.Usage.TotalTokens != 134 {
		t.Fatalf("Usage = %#v", got.Usage)
	}
	if got.Usage.TotalCostNano != "1430" || got.Usage.TotalCostTON != "0.00000143" {
		t.Fatalf("Usage cost = %#v", got.Usage)
	}
	if got.Spend == nil || got.Spend.Label != "0.00000143 TON / 134 tokens" {
		t.Fatalf("Spend = %#v", got.Spend)
	}
}

func TestSplitThinkBlocksMultipleAndUnclosed(t *testing.T) {
	visible, thinking := splitThinkBlocks("A<think>one</think>B<think>two</think>C")
	if visible != "ABC" || len(thinking) != 2 || thinking[0] != "one" || thinking[1] != "two" {
		t.Fatalf("visible=%q thinking=%#v", visible, thinking)
	}

	visible, thinking = splitThinkBlocks("<think>unfinished")
	if visible != "" || len(thinking) != 1 || thinking[0] != "unfinished" {
		t.Fatalf("visible=%q thinking=%#v", visible, thinking)
	}
}

func TestApplyRunnerSpendDeltaKeepsUsageCostPrimary(t *testing.T) {
	resp := &uiChatResponse{
		Usage: &uiChatUsage{
			TotalTokens:   12,
			TotalCostNano: "900",
			TotalCostTON:  "0.0000009",
			HasCost:       true,
		},
	}
	before := []byte(`{"proxies":[{"tokens_charged":100,"tokens_payed":20}]}`)
	after := []byte(`{"proxies":[{"tokens_charged":115,"tokens_payed":20}]}`)
	applyRunnerSpendDelta(resp, before, after)

	if resp.Spend == nil {
		t.Fatal("Spend is nil")
	}
	if resp.Spend.Label != "0.0000009 TON / 12 tokens" {
		t.Fatalf("Label = %q", resp.Spend.Label)
	}
	if resp.Spend.TokensChargedDelta != 15 {
		t.Fatalf("TokensChargedDelta = %d", resp.Spend.TokensChargedDelta)
	}
}
