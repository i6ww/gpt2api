package service

import (
	"testing"

	"github.com/kleinai/backend/internal/model"
)

func TestTaskPollRetryAfter(t *testing.T) {
	cases := []struct {
		status int8
		stored int8
		want   int
	}{
		{model.GenStatusRunning, 5, 5},
		{model.GenStatusRunning, 2, 2},
		{model.GenStatusRunning, 0, 2},
		{model.GenStatusPending, 0, 1},
		{model.GenStatusSucceeded, 2, 0},
	}
	for _, tc := range cases {
		got := TaskPollRetryAfter(&model.GenerationTask{Status: tc.status, PollRetryAfter: tc.stored})
		if got != tc.want {
			t.Fatalf("status=%d stored=%d: got %d want %d", tc.status, tc.stored, got, tc.want)
		}
	}
}
