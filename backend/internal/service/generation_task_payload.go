package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/internal/provider"
)

const (
	WebhookEventSucceeded = "generation.succeeded"
	WebhookEventFailed    = "generation.failed"
)

func callbackURLFromParams(params map[string]any) string {
	if len(params) == 0 {
		return ""
	}
	for _, key := range []string{"callback_url", "webhook_url", "webhook"} {
		raw, _ := params[key].(string)
		raw = strings.TrimSpace(raw)
		if raw != "" {
			return raw
		}
	}
	return ""
}

func parseTaskParams(raw string) map[string]any {
	out := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func webhookEventForTask(t *model.GenerationTask) string {
	if t == nil {
		return "generation.updated"
	}
	switch t.Status {
	case model.GenStatusSucceeded:
		return WebhookEventSucceeded
	case model.GenStatusFailed, model.GenStatusRefunded:
		return WebhookEventFailed
	default:
		return "generation.updated"
	}
}

func taskStatusName(status int8) string {
	switch status {
	case model.GenStatusPending:
		return "queued"
	case model.GenStatusRunning:
		return "running"
	case model.GenStatusSucceeded:
		return "succeeded"
	case model.GenStatusFailed:
		return "failed"
	case model.GenStatusRefunded:
		return "refunded"
	default:
		return "unknown"
	}
}

func buildGenerationWebhookBody(ctx context.Context, cfg *SystemConfigService, t *model.GenerationTask, results []*model.GenerationResult) ([]byte, string, error) {
	if t == nil {
		return nil, "", nil
	}
	event := webhookEventForTask(t)
	payload := map[string]any{
		"event":    event,
		"id":       t.TaskID,
		"task_id":  t.TaskID,
		"object":   t.Kind + ".generation.task",
		"status":   taskStatusName(t.Status),
		"progress": webhookProgressForTask(t),
		"created":  t.CreatedAt.Unix(),
		"model":    t.ModelCode,
		"kind":     t.Kind,
		"mode":     t.Mode,
		"usage": map[string]any{
			"total_cost":   t.CostPoints,
			"total_points": float64(t.CostPoints) / 100,
		},
		"error": nil,
	}
	if t.Error != nil && strings.TrimSpace(*t.Error) != "" {
		payload["error"] = map[string]any{"message": strings.TrimSpace(*t.Error)}
	}
	if len(results) > 0 {
		if t.Kind == string(provider.KindVideo) {
			payload["result"] = buildVideoWebhookResult(ctx, cfg, t, results)
		} else {
			payload["result"] = buildImageWebhookResult(ctx, cfg, t, results)
		}
	}
	body, err := json.Marshal(payload)
	return body, event, err
}

func webhookProgressForTask(t *model.GenerationTask) int8 {
	if t == nil {
		return 0
	}
	if t.Status == model.GenStatusSucceeded {
		return 100
	}
	return t.Progress
}

func buildImageWebhookResult(ctx context.Context, cfg *SystemConfigService, t *model.GenerationTask, results []*model.GenerationResult) map[string]any {
	data := make([]map[string]any, 0, len(results))
	for _, r := range results {
		if r == nil {
			continue
		}
		row := map[string]any{"url": resolveWebhookResultURL(ctx, cfg, r.URL)}
		if r.Width != nil {
			row["width"] = *r.Width
		}
		if r.Height != nil {
			row["height"] = *r.Height
		}
		data = append(data, row)
	}
	return map[string]any{
		"created": t.CreatedAt.Unix(),
		"data":    data,
		"task_id": t.TaskID,
		"usage": map[string]any{
			"total_cost":   t.CostPoints,
			"total_points": float64(t.CostPoints) / 100,
		},
	}
}

func buildVideoWebhookResult(ctx context.Context, cfg *SystemConfigService, t *model.GenerationTask, results []*model.GenerationResult) map[string]any {
	data := make([]map[string]any, 0, len(results))
	for _, r := range results {
		if r == nil {
			continue
		}
		row := map[string]any{"url": resolveWebhookResultURL(ctx, cfg, r.URL)}
		if r.ThumbURL != nil && strings.TrimSpace(*r.ThumbURL) != "" {
			row["cover_url"] = resolveWebhookResultURL(ctx, cfg, *r.ThumbURL)
		}
		if r.DurationMs != nil {
			row["duration_ms"] = *r.DurationMs
		}
		if r.Width != nil {
			row["width"] = *r.Width
		}
		if r.Height != nil {
			row["height"] = *r.Height
		}
		data = append(data, row)
	}
	return map[string]any{
		"id":      t.TaskID,
		"object":  "video.generation",
		"created": t.CreatedAt.Unix(),
		"model":   t.ModelCode,
		"data":    data,
		"usage": map[string]any{
			"total_cost":   t.CostPoints,
			"total_points": float64(t.CostPoints) / 100,
		},
	}
}

func resolveWebhookResultURL(ctx context.Context, cfg *SystemConfigService, raw string) string {
	raw = normalizeWebhookResultURL(raw)
	return AbsolutizeMediaURL(ctx, cfg, "", raw)
}

func normalizeWebhookResultURL(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") ||
		strings.HasPrefix(v, "/api/") || strings.HasPrefix(v, "data:") {
		return v
	}
	return "https://assets.grok.com/" + strings.TrimLeft(v, "/")
}
