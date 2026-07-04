package xai

import (
	"strings"
	"testing"
)

func TestBuildResponsesRequestMapsRolesAndInstructions(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "be terse"},
			map[string]any{"role": "user", "content": "hello"},
			map[string]any{"role": "assistant", "content": "hi"},
			map[string]any{"role": "user", "content": "bye"},
		},
		"max_tokens": float64(128),
	}
	req := buildResponsesRequest("grok-4", body)
	if req["instructions"] != "be terse" {
		t.Fatalf("instructions = %v", req["instructions"])
	}
	if req["model"] != "grok-4" {
		t.Fatalf("model = %v", req["model"])
	}
	if req["max_output_tokens"] != float64(128) {
		t.Fatalf("max_output_tokens = %v", req["max_output_tokens"])
	}
	input, ok := req["input"].([]map[string]any)
	if !ok || len(input) != 3 {
		t.Fatalf("input = %v", req["input"])
	}
	if input[0]["role"] != "user" || input[1]["role"] != "assistant" {
		t.Fatalf("unexpected roles: %v", input)
	}
}

func TestContentToTextMultimodal(t *testing.T) {
	parts := []any{
		map[string]any{"type": "text", "text": "a"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "x"}},
		map[string]any{"type": "input_text", "text": "b"},
	}
	if got := contentToText(parts); got != "a\nb" {
		t.Fatalf("contentToText = %q", got)
	}
	if got := contentToText("plain"); got != "plain" {
		t.Fatalf("contentToText string = %q", got)
	}
}

func TestParseResponsesSSECompleted(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"partial"}`,
		`data: {"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"final answer"}]}],"usage":{"input_tokens":10,"output_tokens":5}}}`,
		`data: [DONE]`,
	}, "\n")
	text, usage, err := parseResponsesSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	if text != "final answer" {
		t.Fatalf("text = %q", text)
	}
	if usage == nil || usage.PromptTokens != 10 || usage.CompletionTokens != 5 || usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestParseResponsesSSEFallbackToDelta(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hel"}`,
		`data: {"type":"response.output_text.delta","delta":"lo"}`,
		`data: {"type":"response.completed","response":{"output":[]}}`,
	}, "\n")
	text, _, err := parseResponsesSSE(strings.NewReader(sse))
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
}

func TestModelRouting(t *testing.T) {
	if !IsChatModel("xai/grok-4.3") || !IsChatModel("xai/grok-4") {
		t.Fatal("expected chat model match for xai/ prefix")
	}
	// 裸 grok-* 必须留给 grok web 通道，不能被官方 xAI 劫持。
	if IsChatModel("grok-4") {
		t.Fatal("bare grok-4 should NOT be xai chat (belongs to grok web)")
	}
	if IsChatModel("gpt-4o") {
		t.Fatal("gpt-4o should not be xai chat")
	}
	if UpstreamModel("xai/grok-4") != "grok-4" {
		t.Fatalf("UpstreamModel = %q", UpstreamModel("xai/grok-4"))
	}
	if !IsVideoModel("grok-video") || !IsVideoModel("xai/grok-imagine-video") {
		t.Fatal("expected video model match")
	}
}
