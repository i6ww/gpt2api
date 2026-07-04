package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/kleinai/backend/pkg/config"
	"github.com/kleinai/backend/pkg/logger"
	"github.com/kleinai/backend/pkg/webhookurl"
)

const WebhookTaskNotify = "webhook:notify"

// WebhookNotifyJob asynq / inline delivery payload.
type WebhookNotifyJob struct {
	DeliveryID string          `json:"delivery_id"`
	TaskID     string          `json:"task_id"`
	Event      string          `json:"event"`
	URL        string          `json:"url"`
	Body       json.RawMessage `json:"body"`
}

// WebhookService delivers generation task webhooks with retry.
type WebhookService struct {
	asynqClient *asynq.Client
	sysCfg      *SystemConfigService
	jwtSecret   string
	httpClient  *http.Client
}

func NewWebhookService(cfg *config.Config, rdb *redis.Client, sysCfg *SystemConfigService) *WebhookService {
	w := &WebhookService{
		sysCfg:    sysCfg,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
	if cfg != nil {
		w.jwtSecret = strings.TrimSpace(cfg.JWT.Secret)
	}
	if rdb != nil && cfg != nil && cfg.Redis.Addr != "" {
		w.asynqClient = asynq.NewClient(asynq.RedisClientOpt{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
	}
	return w
}

func (w *WebhookService) Close() error {
	if w == nil || w.asynqClient == nil {
		return nil
	}
	return w.asynqClient.Close()
}

// Enqueue posts a webhook asynchronously (asynq when Redis is available).
func (w *WebhookService) Enqueue(ctx context.Context, job WebhookNotifyJob) error {
	if w == nil {
		return nil
	}
	if strings.TrimSpace(job.URL) == "" || len(job.Body) == 0 {
		return nil
	}
	if job.DeliveryID == "" {
		job.DeliveryID = uuid.NewString()
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return err
	}
	if w.asynqClient != nil {
		task := asynq.NewTask(WebhookTaskNotify, payload,
			asynq.MaxRetry(5),
			asynq.Queue("default"),
			asynq.Timeout(30*time.Second),
		)
		_, err = w.asynqClient.EnqueueContext(ctx, task)
		return err
	}
	go func() {
		_ = w.deliverWithRetry(context.Background(), job)
	}()
	return nil
}

// HandleAsynqTask implements worker consumer for WebhookTaskNotify.
func (w *WebhookService) HandleAsynqTask(ctx context.Context, t *asynq.Task) error {
	if w == nil {
		return nil
	}
	var job WebhookNotifyJob
	if err := json.Unmarshal(t.Payload(), &job); err != nil {
		return fmt.Errorf("decode webhook job: %w", err)
	}
	return w.deliver(ctx, job)
}

func (w *WebhookService) deliverWithRetry(ctx context.Context, job WebhookNotifyJob) error {
	delays := []time.Duration{0, 2 * time.Second, 8 * time.Second, 30 * time.Second}
	var lastErr error
	for i, delay := range delays {
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := w.deliver(ctx, job); err == nil {
			return nil
		} else {
			lastErr = err
			logger.FromCtx(ctx).Warn("webhook.deliver_retry",
				zap.String("task", job.TaskID),
				zap.String("delivery", job.DeliveryID),
				zap.Int("attempt", i+1),
				zap.Error(err))
		}
	}
	return lastErr
}

func (w *WebhookService) deliver(ctx context.Context, job WebhookNotifyJob) error {
	url, err := webhookurl.ValidateReachable(ctx, job.URL)
	if err != nil {
		return fmt.Errorf("callback url rejected: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(job.Body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Klein-Webhook/1.0")
	req.Header.Set("X-Klein-Event", job.Event)
	req.Header.Set("X-Klein-Delivery", job.DeliveryID)
	req.Header.Set("X-Klein-Task-Id", job.TaskID)
	if sig := w.sign(job.Body); sig != "" {
		req.Header.Set("X-Klein-Signature", sig)
	}

	resp, err := w.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logger.FromCtx(ctx).Info("webhook.delivered",
			zap.String("task", job.TaskID),
			zap.String("delivery", job.DeliveryID),
			zap.String("event", job.Event),
			zap.Int("status", resp.StatusCode))
		return nil
	}
	return fmt.Errorf("webhook HTTP %d", resp.StatusCode)
}

func (w *WebhookService) sign(body []byte) string {
	secret := w.webhookSecret()
	if secret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (w *WebhookService) webhookSecret() string {
	if w == nil {
		return ""
	}
	if w.sysCfg != nil {
		if v := strings.TrimSpace(w.sysCfg.GetString(context.Background(), "generation.webhook_secret", "")); v != "" {
			return v
		}
	}
	return strings.TrimSpace(w.jwtSecret)
}
