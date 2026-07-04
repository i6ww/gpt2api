package service

import (
	"testing"
	"time"

	"github.com/kleinai/backend/internal/model"
)

func TestApplyProbeRecoveryRestoresBrokenAccountOnSuccess(t *testing.T) {
	until := time.Now().UTC().Add(10 * time.Minute)
	lastError := "rate limit"
	acc := &model.Account{
		Status:        model.AccountStatusBroken,
		CooldownUntil: &until,
		LastError:     &lastError,
	}
	updates := map[string]any{
		"last_test_status": model.AccountTestOK,
	}

	applyProbeRecovery(updates, acc, true)

	if got := updates["status"]; got != model.AccountStatusEnabled {
		t.Fatalf("status = %v, want %d", got, model.AccountStatusEnabled)
	}
	if got, ok := updates["cooldown_until"]; !ok || got != nil {
		t.Fatalf("cooldown_until = %#v, want nil", got)
	}
	if got, ok := updates["last_error"]; !ok || got != nil {
		t.Fatalf("last_error = %#v, want nil", got)
	}
	if got := updates["error_count"]; got != 0 {
		t.Fatalf("error_count = %v, want 0", got)
	}
}

func TestApplyProbeRecoverySkipsFailedProbe(t *testing.T) {
	until := time.Now().UTC().Add(10 * time.Minute)
	lastError := "rate limit"
	acc := &model.Account{
		Status:        model.AccountStatusBroken,
		CooldownUntil: &until,
		LastError:     &lastError,
	}
	updates := map[string]any{
		"last_test_status": model.AccountTestFail,
	}

	applyProbeRecovery(updates, acc, false)

	if _, ok := updates["status"]; ok {
		t.Fatal("status should not be restored on failed probe")
	}
	if _, ok := updates["cooldown_until"]; ok {
		t.Fatal("cooldown_until should not be changed on failed probe")
	}
	if _, ok := updates["last_error"]; ok {
		t.Fatal("last_error should not be changed on failed probe")
	}
	if _, ok := updates["error_count"]; ok {
		t.Fatal("error_count should not be reset on failed probe")
	}
}
