package adobe

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kleinai/backend/internal/regkit/browser"
)

// TestBFPPayloadShape 校验 build 出来的 payload 在 JSON 序列化后包含 2026 年新 schema
// 必须的字段（headless 嵌套对象、math hex、新增 mode/version/fontBatchSizeNonBlocking 等）。
//
// 这些是当前 idg.adobe.com/v1/api/bfp_capture 200 响应的硬性前置；缺一项就 400。
func TestBFPPayloadShape(t *testing.T) {
	prof := browser.Profile{
		UserAgent: "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		SecChUA:   `"Google Chrome";v="131", "Chromium";v="131", "Not_A Brand";v="24"`,
		Locale:    "en-US,en;q=0.9",
	}
	body, err := json.Marshal(buildBFPPayload("90e8265e-f4d0-491c-9a30-8a8e749f8b6b", prof))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	checks := []string{
		`"mode":"kBlocking"`,
		`"version":"1.0.12"`,
		`"fontBatchSizeNonBlocking":3`,
		`"headless":{`,        // 嵌套对象，不能是布尔
		`"likeHeadless":{`,    // 必备子对象
		`"hasIframeProxy":false`,
		`"chromium":true`,
		`"webGlBasics":{`,
		`"vendorUnmasked":`,
		`"shadingLanguageVersion"`,
		`"browserDetails":{`,
		`"detailedName":`,
		`"userAgentData":{`,
		`"brandsVersion":`,
		`"clientId":"clio-playground-web"`,
		`"idgTokenLs":"90e8265e-f4d0-491c-9a30-8a8e749f8b6b"`,
		`"applePay":-1`,
	}
	for _, want := range checks {
		if !strings.Contains(string(body), want) {
			head := body
			if len(head) > 600 {
				head = head[:600]
			}
			t.Errorf("payload missing %q\n--- payload (first 600) ---\n%s", want, head)
		}
	}

	// math 必须是 32 字符 hex
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	mathHash, _ := parsed["math"].(string)
	if len(mathHash) != 32 {
		t.Errorf("math hash should be 32 chars, got %d: %q", len(mathHash), mathHash)
	}
	for _, c := range mathHash {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("math hash should be lowercase hex, got %q", mathHash)
			break
		}
	}

	// userAgentData.brands 现在是 string 数组而不是对象数组
	uad, _ := parsed["userAgentData"].(map[string]any)
	brands, _ := uad["brands"].([]any)
	if len(brands) == 0 {
		t.Fatalf("brands is empty")
	}
	if _, ok := brands[0].(string); !ok {
		t.Errorf("brands[0] should be string, got %T (%v)", brands[0], brands[0])
	}
}
