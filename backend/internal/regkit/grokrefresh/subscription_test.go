package grokrefresh

import (
	"encoding/json"
	"testing"
	"time"
)

// 模拟几个可能的 /rest/subscriptions 响应形状（grok 没公开 schema，我们靠多 key 兜底）。
//
// 当真实字段名暴露后（首次部署后看日志 raw_keys），可以把对应 case 留到测试里
// 防止以后 grok 改名/移除时悄悄退化成 unknown / nil。
func TestFlattenSubscription_Variants(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool // 期望能拍到一个非 nil map
	}{
		{"empty_array", `[]`, false},
		{"empty_object", `{}`, false},
		{"array_of_one", `[{"tier":"super_grok","status":"active"}]`, true},
		{"wrapped_subscriptions", `{"subscriptions":[{"tier":"super_grok_heavy"}]}`, true},
		{"wrapped_data", `{"data":[{"plan":"team"}]}`, true},
		{"wrapped_items_empty", `{"items":[]}`, false},
		{"flat_object", `{"subscriptionTier":"super_grok","currentPeriodEnd":1800000000}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(c.body), &v); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got := flattenSubscription(v)
			if (got != nil) != c.want {
				t.Fatalf("flattenSubscription got=%v want_non_nil=%v", got, c.want)
			}
		})
	}
}

func TestNormalizeSubscriptionTier(t *testing.T) {
	cases := map[string]string{
		"":                  AccountTypeFree,
		"free":              AccountTypeFree,
		"basic":             AccountTypeFree,
		"Super Grok":        AccountTypeSuperGrok,
		"SUPER_GROK_HEAVY":  AccountTypeSuperGrokHeavy,
		"team":              AccountTypeSuperGrok,
		"PremiumPlus":       AccountTypeSuperGrok,
		"grok-heavy":        AccountTypeSuperGrokHeavy,
		"something-unknown": AccountTypeUnknown,
	}
	for in, want := range cases {
		if got := normalizeSubscriptionTier(in); got != want {
			t.Errorf("normalize(%q)=%q want %q", in, got, want)
		}
	}
}

// TestProbeSubscription_RealSchema 用 2026-05-14 实测到的 grok /rest/subscriptions
// 真实响应字段集 — 确保我们 (a) 不再退化 (b) 关键字段 (tier/expires_at/status) 都能拍准。
//
// 真实 raw_keys 见 docker logs：[stripe xaiUserId tier status createTime
// billingInterval billingPeriodEnd modTime cancelAtPeriodEnd]
func TestProbeSubscription_RealSchema(t *testing.T) {
	// billingPeriodEnd 既可能是 unix 秒，也可能是毫秒（grok 没公开 schema）。两种都测。
	cases := []struct {
		name       string
		body       string
		wantTier   string
		wantStatus string
		wantUnix   int64
	}{
		{
			"wrapped_subscriptions_unix_sec",
			`{"subscriptions":[{"stripe":{"customerId":"cus_x"},"xaiUserId":"u1","tier":"super_grok","status":"subscription_status_active","billingInterval":"monthly","billingPeriodEnd":1800000000,"createTime":1790000000,"modTime":1790000000,"cancelAtPeriodEnd":false}]}`,
			AccountTypeSuperGrok, "active", 1800000000,
		},
		{
			"flat_object_unix_ms",
			`{"tier":"super_grok_heavy","status":"SUBSCRIPTION_STATUS_ACTIVE","billingPeriodEnd":1800000000000,"cancelAtPeriodEnd":true}`,
			AccountTypeSuperGrokHeavy, "active", 1800000000,
		},
		{
			"empty_wrapper_treated_as_free",
			`{"subscriptions":[]}`,
			AccountTypeFree, "", 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var data any
			if err := json.Unmarshal([]byte(c.body), &data); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			first := flattenSubscription(data)
			var (
				tier, status string
				expiresUnix  int64
			)
			if first == nil {
				tier = AccountTypeFree
			} else {
				tier = normalizeSubscriptionTier(pickString(first, "subscriptionTier", "tier", "plan", "planName", "accountType", "type", "level"))
				status = normalizeSubscriptionStatus(pickString(first, "status", "subscriptionStatus", "state"))
				if t := pickTime(first, "billingPeriodEnd", "billing_period_end", "currentPeriodEnd", "current_period_end", "expirationDate", "expiresAt", "expiresAtMs", "expires_at", "endsAt", "endDate", "nextBillingDate", "nextRenewalDate", "renewsAt", "validUntil"); t != nil {
					expiresUnix = t.Unix()
				}
			}
			if tier != c.wantTier {
				t.Errorf("tier=%q want %q", tier, c.wantTier)
			}
			if status != c.wantStatus {
				t.Errorf("status=%q want %q", status, c.wantStatus)
			}
			if expiresUnix != c.wantUnix {
				t.Errorf("expires unix=%d want %d", expiresUnix, c.wantUnix)
			}
		})
	}
}

func TestNormalizeSubscriptionStatus(t *testing.T) {
	cases := map[string]string{
		"subscription_status_active":   "active",
		"SUBSCRIPTION_STATUS_ACTIVE":   "active",
		"subscription_status_past_due": "past_due",
		"active":                       "active",
		"":                             "",
	}
	for in, want := range cases {
		if got := normalizeSubscriptionStatus(in); got != want {
			t.Errorf("normalizeStatus(%q)=%q want %q", in, got, want)
		}
	}
}

func TestPickTime_AcceptsUnixAndRFC3339(t *testing.T) {
	t.Run("unix_sec", func(t *testing.T) {
		m := map[string]any{"currentPeriodEnd": float64(1800000000)}
		got := pickTime(m, "currentPeriodEnd")
		if got == nil || got.Unix() != 1800000000 {
			t.Fatalf("unix sec: %v", got)
		}
	})
	t.Run("unix_ms", func(t *testing.T) {
		m := map[string]any{"expiresAtMs": float64(1800000000000)}
		got := pickTime(m, "expiresAtMs")
		if got == nil || got.UnixMilli() != 1800000000000 {
			t.Fatalf("unix ms: %v", got)
		}
	})
	t.Run("rfc3339", func(t *testing.T) {
		m := map[string]any{"expirationDate": "2026-07-01T08:00:00Z"}
		got := pickTime(m, "expirationDate")
		if got == nil || !got.Equal(time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)) {
			t.Fatalf("rfc3339: %v", got)
		}
	})
	t.Run("first_non_empty_wins", func(t *testing.T) {
		m := map[string]any{"nope": nil, "expirationDate": "2026-07-01T08:00:00Z", "currentPeriodEnd": float64(1800000000)}
		got := pickTime(m, "currentPeriodEnd", "expirationDate")
		// currentPeriodEnd 优先
		if got == nil || got.Unix() != 1800000000 {
			t.Fatalf("priority: %v", got)
		}
	})
}
