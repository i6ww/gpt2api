package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/kleinai/backend/internal/model"
)

func TestBuildGenerationWebhookBodySucceeded(t *testing.T) {
	errMsg := ""
	tk := &model.GenerationTask{
		TaskID:     "01TASK",
		Kind:       "image",
		Mode:       "t2i",
		ModelCode:  "gpt-image-2",
		Status:     model.GenStatusSucceeded,
		Progress:   100,
		CostPoints: 800,
	}
	w := 1024
	h := 1536
	results := []*model.GenerationResult{{
		TaskID: tk.TaskID,
		URL:    "/api/v1/gen/cached/foo.png",
		Width:  &w,
		Height: &h,
	}}
	body, event, err := buildGenerationWebhookBody(context.Background(), nil, tk, results)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event != WebhookEventSucceeded {
		t.Fatalf("event=%q", event)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["status"] != "succeeded" {
		t.Fatalf("status=%v", payload["status"])
	}
	if payload["event"] != WebhookEventSucceeded {
		t.Fatalf("payload event=%v", payload["event"])
	}
	_ = errMsg
}

func TestCallbackURLFromParams(t *testing.T) {
	got := callbackURLFromParams(map[string]any{"callback_url": " https://example.com/hook "})
	if got != "https://example.com/hook" {
		t.Fatalf("got %q", got)
	}
}
