package service

import (
	"encoding/json"
	"testing"

	"github.com/kleinai/backend/internal/model"
)

func TestNormalizeGrokPlanType(t *testing.T) {
	cases := map[string]string{
		"":                 "basic",
		"free":             "basic",
		"FREE":             "basic",
		"unknown":          "basic",
		"basic":            "basic",
		"super":            "super",
		"super_grok":       "super",
		"SUPER_GROK":       "super",
		"team":             "super",
		"heavy":            "heavy",
		"super_grok_heavy": "heavy",
		"garbage":          "",
	}
	for in, want := range cases {
		if got := normalizeGrokPlanType(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAccountSupportsGrokPlan_SuperGrokCanRunVideo(t *testing.T) {
	meta := map[string]any{"plan_type": "super_grok"}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderGROK, OAuthMeta: &s}
	if !accountSupportsGrokPlan(acc, "super") {
		t.Fatalf("super_grok account should satisfy super requirement (grok-imagine-video)")
	}
	if !accountSupportsGrokPlan(acc, "basic") {
		t.Fatalf("super_grok account should also satisfy basic requirement")
	}
	if accountSupportsGrokPlan(acc, "heavy") {
		t.Fatalf("super_grok account should NOT satisfy heavy requirement")
	}
}

func TestAccountSupportsGrokPlan_HeavyCanRunEverything(t *testing.T) {
	meta := map[string]any{"plan_type": "super_grok_heavy"}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderGROK, OAuthMeta: &s}
	for _, plan := range []string{"basic", "super", "heavy"} {
		if !accountSupportsGrokPlan(acc, plan) {
			t.Errorf("super_grok_heavy account should satisfy %q", plan)
		}
	}
}

func TestAccountSupportsGrokPlan_FreeBlocksVideo(t *testing.T) {
	meta := map[string]any{"plan_type": "free"}
	raw, _ := json.Marshal(meta)
	s := string(raw)
	acc := &model.Account{Provider: model.ProviderGROK, OAuthMeta: &s}
	if accountSupportsGrokPlan(acc, "super") {
		t.Fatalf("free account should NOT satisfy super requirement")
	}
	if !accountSupportsGrokPlan(acc, "basic") {
		t.Fatalf("free account should satisfy basic requirement")
	}
}
