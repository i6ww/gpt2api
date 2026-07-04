package gpt

import (
	"net/http"
	"testing"
)

func TestIsCodexChatModel(t *testing.T) {
	cases := map[string]bool{
		"gpt-5.4":             true,
		"GPT-5.4":             true,
		"gpt-5.4-mini":        true,
		"gpt-5.3-codex":       true,
		"gpt-5.3-codex-high":  true,
		"gpt-5.3-codex-spark": true,
		"gpt-4o-mini":         false,
		"gpt-image-2":         false,
		"grok-4.20-fast":      false,
	}
	for model, want := range cases {
		if got := IsCodexChatModel(model); got != want {
			t.Fatalf("IsCodexChatModel(%q)=%v want %v", model, got, want)
		}
	}
}

func TestCodexChatHeadersUseOfficialCLIIdentity(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	setCodexChatHeaders(req, "token", "session")
	if got := req.Header.Get("User-Agent"); got != codexCLIUserAgent {
		t.Fatalf("User-Agent=%q", got)
	}
	if got := req.Header.Get("originator"); got != "codex_cli_rs" {
		t.Fatalf("originator=%q", got)
	}
	if got := req.Header.Get("version"); got != codexCLIVersion {
		t.Fatalf("version=%q", got)
	}
}

func TestChatCompletionsToCodexResponses(t *testing.T) {
	body := map[string]any{
		"model": "gpt-5.4",
		"messages": []any{
			map[string]any{"role": "system", "content": "Be concise."},
			map[string]any{"role": "user", "content": "Hello"},
		},
		"temperature": 0.2,
	}
	out, err := chatCompletionsToCodexResponses(body, "gpt-5.4")
	if err != nil {
		t.Fatal(err)
	}
	if out["model"] != "gpt-5.4" {
		t.Fatalf("model=%v", out["model"])
	}
	if out["store"] != false || out["stream"] != true {
		t.Fatalf("store/stream=%v/%v", out["store"], out["stream"])
	}
	if _, ok := out["temperature"]; ok {
		t.Fatal("temperature should be stripped")
	}
	if out["instructions"] != "Be concise." {
		t.Fatalf("instructions=%v", out["instructions"])
	}
	input, ok := out["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%T len=%d", out["input"], len(input))
	}
	if input[0]["role"] != "user" {
		t.Fatalf("role=%v", input[0]["role"])
	}
}

func TestChatCompletionsToCodexResponsesMapSlice(t *testing.T) {
	body := map[string]any{
		"model": "gpt-5.4-mini",
		"messages": []map[string]any{
			{"role": "user", "content": "Hello"},
		},
	}
	out, err := chatCompletionsToCodexResponses(body, "gpt-5.4-mini")
	if err != nil {
		t.Fatal(err)
	}
	input, ok := out["input"].([]map[string]any)
	if !ok || len(input) != 1 {
		t.Fatalf("input=%T len=%d", out["input"], len(input))
	}
}
