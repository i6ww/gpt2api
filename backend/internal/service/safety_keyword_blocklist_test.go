package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestKeywordBlocklistValidate(t *testing.T) {
	cfg := testKeywordBlocklistConfig(true, []string{"Unsafe", "违禁词"}, "contains")
	if err := cfg.ValidateKeywordSafe(context.Background(), "this has unsafe content"); err == nil {
		t.Fatal("expected unsafe English keyword to be blocked")
	}
	if err := cfg.ValidateKeywordSafe(context.Background(), "这是一段违禁词内容"); err == nil {
		t.Fatal("expected unsafe Chinese keyword to be blocked")
	}
	if err := cfg.ValidateKeywordSafe(context.Background(), "normal prompt"); err != nil {
		t.Fatalf("expected normal prompt to pass, got %v", err)
	}
}

func TestKeywordBlocklistDisabledAllowsPrompt(t *testing.T) {
	cfg := testKeywordBlocklistConfig(false, []string{"unsafe"}, "contains")
	if err := cfg.ValidateKeywordSafe(context.Background(), "unsafe"); err != nil {
		t.Fatalf("expected disabled blocklist to pass, got %v", err)
	}
}

func TestGenerationCreateBlocksUnsafePromptBeforeDB(t *testing.T) {
	cfg := testKeywordBlocklistConfig(true, []string{"unsafe"}, "contains")
	svc := &GenerationService{cfg: cfg}
	_, err := svc.Create(context.Background(), CreateRequest{Prompt: "unsafe prompt"})
	if err == nil {
		t.Fatal("expected unsafe generation prompt to be blocked")
	}
	if !strings.Contains(err.Error(), UnsafeKeywordMessage) {
		t.Fatalf("expected unsafe keyword message, got %v", err)
	}
}

func TestGenerationCreateBlocksUnsafeNegativePromptBeforeDB(t *testing.T) {
	cfg := testKeywordBlocklistConfig(true, []string{"unsafe"}, "contains")
	svc := &GenerationService{cfg: cfg}
	_, err := svc.Create(context.Background(), CreateRequest{Prompt: "normal", NegPrompt: "unsafe negative"})
	if err == nil {
		t.Fatal("expected unsafe negative prompt to be blocked")
	}
	if !strings.Contains(err.Error(), UnsafeKeywordMessage) {
		t.Fatalf("expected unsafe keyword message, got %v", err)
	}
}

func TestChatCompleteBlocksUnsafePromptBeforeDB(t *testing.T) {
	cfg := testKeywordBlocklistConfig(true, []string{"unsafe"}, "contains")
	svc := &ChatService{cfg: cfg}
	_, status, err := svc.Complete(context.Background(), ChatCallRequest{Body: map[string]any{
		"model": "gpt-4o-mini",
		"messages": []any{
			map[string]any{"role": "user", "content": "unsafe chat"},
		},
	}})
	if err == nil {
		t.Fatal("expected unsafe chat prompt to be blocked")
	}
	if status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
	if !strings.Contains(err.Error(), UnsafeKeywordMessage) {
		t.Fatalf("expected unsafe keyword message, got %v", err)
	}
}

func testKeywordBlocklistConfig(enabled bool, words []string, mode string) *SystemConfigService {
	rawWords := `[]`
	if len(words) > 0 {
		parts := make([]string, 0, len(words))
		for _, word := range words {
			parts = append(parts, `"`+strings.ReplaceAll(word, `"`, `\"`)+`"`)
		}
		rawWords = `[` + strings.Join(parts, ",") + `]`
	}
	enabledRaw := "false"
	if enabled {
		enabledRaw = "true"
	}
	return &SystemConfigService{
		cache: map[string]string{
			SettingSafetyKeywordBlocklistEnabled: enabledRaw,
			SettingSafetyKeywordBlocklistWords:   rawWords,
			SettingSafetyKeywordBlocklistMode:    `"` + mode + `"`,
		},
		loaded: time.Now(),
		ttl:    time.Hour,
	}
}
