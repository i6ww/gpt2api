package service

import (
	"context"
	"testing"
	"time"
)

func TestOpenAIAdmissionMaxInflightDefault(t *testing.T) {
	cfg := &SystemConfigService{
		cache: map[string]string{
			SettingOpenAISyncWaitMax: "200",
		},
		loaded: time.Now(),
		ttl:    time.Hour,
	}
	if got := cfg.OpenAIAdmissionMaxInflight(context.Background()); got != 200 {
		t.Fatalf("expected default admission 200, got %d", got)
	}
}

func TestOpenAIAdmissionMaxInflightExplicitZero(t *testing.T) {
	cfg := &SystemConfigService{
		cache: map[string]string{
			SettingOpenAIAdmissionMaxInflight: "0",
			SettingOpenAISyncWaitMax:          "200",
		},
		loaded: time.Now(),
		ttl:    time.Hour,
	}
	if got := cfg.OpenAIAdmissionMaxInflight(context.Background()); got != 0 {
		t.Fatalf("expected unlimited admission, got %d", got)
	}
}

func TestOpenAIAdmissionMaxInflightExplicitOverride(t *testing.T) {
	cfg := &SystemConfigService{
		cache: map[string]string{
			SettingOpenAIAdmissionMaxInflight: "150",
			SettingOpenAISyncWaitMax:          "200",
		},
		loaded: time.Now(),
		ttl:    time.Hour,
	}
	if got := cfg.OpenAIAdmissionMaxInflight(context.Background()); got != 150 {
		t.Fatalf("expected admission override 150, got %d", got)
	}
}
