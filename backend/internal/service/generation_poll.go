package service

import "github.com/kleinai/backend/internal/model"

// TaskPollRetryAfter 返回建议下游轮询间隔（秒），与上游 Adobe Retry-After 对齐。
func TaskPollRetryAfter(t *model.GenerationTask) int {
	if t == nil {
		return 2
	}
	switch t.Status {
	case model.GenStatusSucceeded, model.GenStatusFailed, model.GenStatusRefunded:
		return 0
	}
	if t.PollRetryAfter > 0 {
		return int(t.PollRetryAfter)
	}
	if t.Status == model.GenStatusPending {
		return 1
	}
	return 2
}
