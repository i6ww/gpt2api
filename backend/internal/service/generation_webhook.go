package service

import (
	"context"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/kleinai/backend/internal/model"
	"github.com/kleinai/backend/pkg/logger"
	"github.com/kleinai/backend/pkg/webhookurl"
)

func (s *GenerationService) SetWebhookService(w *WebhookService) {
	if s == nil {
		return
	}
	s.webhooks = w
}

func (s *GenerationService) dispatchTaskWebhook(ctx context.Context, taskID string) {
	if s == nil || s.webhooks == nil || s.repo == nil || taskID == "" {
		return
	}
	t, err := s.repo.GetByTaskID(ctx, taskID)
	if err != nil || t == nil {
		return
	}
	switch t.Status {
	case model.GenStatusSucceeded, model.GenStatusFailed, model.GenStatusRefunded:
	default:
		return
	}
	results, _ := s.repo.ListResultsByTask(ctx, taskID)
	s.notifyTaskWebhook(ctx, t, results)
}

func (s *GenerationService) notifyTaskWebhook(ctx context.Context, t *model.GenerationTask, results []*model.GenerationResult) {
	if s == nil || s.webhooks == nil || t == nil {
		return
	}
	params := parseTaskParams(t.Params)
	callbackURL := callbackURLFromParams(params)
	if callbackURL == "" {
		return
	}
	validated, err := webhookurl.Validate(callbackURL)
	if err != nil {
		logger.FromCtx(ctx).Warn("webhook.callback_invalid",
			zap.String("task", t.TaskID),
			zap.Error(err))
		return
	}
	body, event, err := buildGenerationWebhookBody(ctx, s.cfg, t, results)
	if err != nil || len(body) == 0 {
		logger.FromCtx(ctx).Warn("webhook.payload_build_failed",
			zap.String("task", t.TaskID),
			zap.Error(err))
		return
	}
	job := WebhookNotifyJob{
		DeliveryID: uuid.NewString(),
		TaskID:     t.TaskID,
		Event:      event,
		URL:        validated,
		Body:       body,
	}
	if err := s.webhooks.Enqueue(ctx, job); err != nil {
		logger.FromCtx(ctx).Warn("webhook.enqueue_failed",
			zap.String("task", t.TaskID),
			zap.Error(err))
	}
}
