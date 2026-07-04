package service

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// parseAdobeEntitlements 是 admin 列表渲染权益徽章的唯一翻译层。
// 它必须把 DB 里两条 key（no_<tier> + ok_<tier>）合并成单态：ok / blocked / unknown，
// 并把"用哪个时间戳给前端"这个决定在后端做完——前端只渲染。

// 1. 完全没 meta → 不渲染任何徽章（返回 nil）。
func TestParseAdobeEntitlementsNilWhenEmpty(t *testing.T) {
	if got := parseAdobeEntitlements(nil); got != nil {
		t.Fatalf("expected nil for empty raw, got %+v", got)
	}
	empty := "   "
	if got := parseAdobeEntitlements(&empty); got != nil {
		t.Fatalf("expected nil for whitespace raw, got %+v", got)
	}
	junk := "not json"
	if got := parseAdobeEntitlements(&junk); got != nil {
		t.Fatalf("expected nil for invalid json, got %+v", got)
	}
}

// 2. 只有 ok_<tier> 且在 TTL 内 → 三态返回 "ok"，前端渲染绿色徽章。
//    这是用户最关心的场景："我刚跑通了 2K，请把徽章涂绿"。
func TestParseAdobeEntitlementsOKWinsWhenFresh(t *testing.T) {
	now := time.Now().UTC().Unix()
	raw := mustJSON(t, map[string]any{
		"ok_2k":            true,
		"ok_2k_checked_at": now - 60,
	})
	got := parseAdobeEntitlements(&raw)
	if got == nil {
		t.Fatal("expected non-nil entitlements")
	}
	if got.Image2K != "ok" {
		t.Fatalf("expected image_2k=ok, got %q", got.Image2K)
	}
	if got.Image1K != "unknown" || got.Image4K != "unknown" {
		t.Fatalf("expected other tiers to remain unknown, got %+v", got)
	}
	// checked_at 必须是毫秒（前端 Date.now() 配套）
	if got.Image2KCheckedAt < 1_700_000_000_000 {
		t.Fatalf("expected ok_2k_checked_at in ms, got %d", got.Image2KCheckedAt)
	}
}

// 3. 同时存在 no_<tier> + ok_<tier>，ok 更新 → 翻成 "ok"。
//    场景：先撞过 not_entitled，运营后来升级了 Premium，一次成功跑通 → 应该绿。
func TestParseAdobeEntitlementsConflictNewerOKWins(t *testing.T) {
	now := time.Now().UTC().Unix()
	raw := mustJSON(t, map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now - 3*24*3600, // 3 天前
		"ok_4k":            true,
		"ok_4k_checked_at": now - 60, // 1 分钟前
	})
	got := parseAdobeEntitlements(&raw)
	if got == nil || got.Image4K != "ok" {
		t.Fatalf("expected newer ok_4k to win, got %+v", got)
	}
	if got.Image4KCheckedAt/1000 != now-60 {
		t.Fatalf("expected checked_at to reflect winner timestamp")
	}
}

// 4. 同时存在 no + ok，但 no 更新（账号被回收了）→ blocked。
func TestParseAdobeEntitlementsConflictNewerNoWins(t *testing.T) {
	now := time.Now().UTC().Unix()
	raw := mustJSON(t, map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now - 60,
		"ok_4k":            true,
		"ok_4k_checked_at": now - 3*24*3600,
	})
	got := parseAdobeEntitlements(&raw)
	if got == nil || got.Image4K != "blocked" {
		t.Fatalf("expected newer no_4k to win, got %+v", got)
	}
}

// 5. ok_<tier> 过期（> 7 天）+ 没 no_<tier> → 退回 unknown，让任务再去验证一次。
func TestParseAdobeEntitlementsOKExpiredReturnsUnknown(t *testing.T) {
	now := time.Now().UTC().Unix()
	raw := mustJSON(t, map[string]any{
		"ok_2k":            true,
		"ok_2k_checked_at": now - 8*24*3600,
	})
	got := parseAdobeEntitlements(&raw)
	if got == nil {
		t.Fatal("expected non-nil entitlements")
	}
	if got.Image2K != "unknown" {
		t.Fatalf("expected image_2k=unknown after TTL, got %q", got.Image2K)
	}
}

// 6. 只有 no_<tier> 且新鲜 → blocked，与之前的快路径行为保持一致。
func TestParseAdobeEntitlementsOnlyBlockedWhenNoIsFresh(t *testing.T) {
	now := time.Now().UTC().Unix()
	raw := mustJSON(t, map[string]any{
		"no_4k":            true,
		"no_4k_checked_at": now - 60,
	})
	got := parseAdobeEntitlements(&raw)
	if got == nil || got.Image4K != "blocked" {
		t.Fatalf("expected image_4k=blocked, got %+v", got)
	}
}

func mustJSON(t *testing.T, v map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

// === ParseAdobeImportText ===
//
// Import 的纯解析层负责吃下三类历史/新增格式：
//   1. cookies (3).json：JSON Array，元素 {"cookie":"...","name":"邮箱"}
//   2. 50个/100个/200个 adobe.txt：每行 {"cookie":"...."}，没有 email
//   3. 老格式：email----password[----access_token[----cookie]]
//
// 这些 case 很容易回归（一个 typo 就让 200 个号一个都进不来），所以用单测锁死。

// 1. cookies (3).json 形态 —— `name` 当 email 别名识别。
func TestParseAdobeImportArrayWithNameField(t *testing.T) {
	text := `[
	  {"cookie":"foo=1; ims_sid=AAA","name":"alice@example.com"},
	  {"cookie":"foo=2; ims_sid=BBB","name":"bob@EXAMPLE.com"}
	]`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Email != "alice@example.com" {
		t.Errorf("alice email = %q", items[0].Email)
	}
	// email 必须 lowercase
	if items[1].Email != "bob@example.com" {
		t.Errorf("bob email = %q (want lowercase)", items[1].Email)
	}
	// cookie 透传
	if !strings.Contains(items[0].Cookie, "ims_sid=AAA") {
		t.Errorf("cookie not preserved: %q", items[0].Cookie)
	}
	// Name 字段不应该残留到下游
	if items[0].Name != "" {
		t.Errorf("Name should be cleared after fold-into-email, got %q", items[0].Name)
	}
}

// 2. 纯 cookie .txt 形态 —— 没有 email/name，应该派生稳定占位邮箱。
func TestParseAdobeImportCookieOnlyJSONLDerivesPlaceholder(t *testing.T) {
	text := `{"cookie":"ftrset=976; ims_sid=XYZ; aux_sid=PQR"}
{"cookie":"ftrset=125; ims_sid=AAA; aux_sid=BBB"}`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	for i, it := range items {
		if !strings.HasSuffix(it.Email, "@token.local") {
			t.Errorf("item[%d].Email %q missing token.local", i, it.Email)
		}
		if !strings.HasPrefix(it.Email, "adobe-cookie-") {
			t.Errorf("item[%d].Email %q missing adobe-cookie- prefix", i, it.Email)
		}
		if it.Cookie == "" {
			t.Errorf("item[%d].Cookie empty", i)
		}
	}
	if items[0].Email == items[1].Email {
		t.Errorf("two distinct cookies should derive distinct emails, got both = %q", items[0].Email)
	}
}

// 3. 占位邮箱必须稳定 —— 同一 cookie 重复导入要走 upsert 而不是建新行。
func TestParseAdobeImportPlaceholderIsStableAcrossRuns(t *testing.T) {
	cookie := `ftrset=976; ims_sid=AAA; aux_sid=BBB`
	a := placeholderAdobeEmailFromCookie(cookie)
	b := placeholderAdobeEmailFromCookie(cookie)
	if a != b {
		t.Fatalf("placeholder changed between calls: %q vs %q", a, b)
	}
	// trim 不影响结果
	if c := placeholderAdobeEmailFromCookie("  " + cookie + "\n"); c != a {
		t.Fatalf("placeholder not whitespace-stable: %q vs %q", c, a)
	}
	// 不同 cookie 不同结果（防止意外坍缩）
	if d := placeholderAdobeEmailFromCookie(cookie + "x"); d == a {
		t.Fatalf("two different cookies hashed to same email: %q", d)
	}
}

// 4. 老格式：email----password[----token[----cookie]] 必须继续工作（避免回归）。
func TestParseAdobeImportLegacyDelimiters(t *testing.T) {
	text := `# 注释行应忽略
foo@bar.com----secret123----eyJhbGciOiJ----c=v
bar@baz.com,pwd-only`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Email != "foo@bar.com" || items[0].Password != "secret123" ||
		items[0].AccessToken != "eyJhbGciOiJ" || items[0].Cookie != "c=v" {
		t.Errorf("legacy 4-tuple parse wrong: %+v", items[0])
	}
	if items[1].Email != "bar@baz.com" || items[1].Password != "pwd-only" {
		t.Errorf("legacy 2-tuple parse wrong: %+v", items[1])
	}
}

// 5. 缺三样东西（email/name/cookie 全空）→ 走错误分支，不产出 item。
func TestParseAdobeImportSkipsRowWithNoIdentity(t *testing.T) {
	text := `{"password":"x"}`
	items, errs := ParseAdobeImportText(text)
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 err, got %d: %v", len(errs), errs)
	}
	if !strings.Contains(errs[0], "缺少 email/name/cookie") {
		t.Errorf("err msg should mention identity hint, got %q", errs[0])
	}
}

// 6. JSON Array 整体非法 → 立即把错误透传到调用方（而不是吞掉）。
func TestParseAdobeImportInvalidJSONArray(t *testing.T) {
	text := `[ {"cookie":"x"  ` // 故意不闭合
	items, errs := ParseAdobeImportText(text)
	if len(items) != 0 {
		t.Fatalf("expected 0 items on parse fail, got %d", len(items))
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "JSON Array 解析失败") {
		t.Fatalf("expected JSON Array 解析失败 error, got %v", errs)
	}
}

// 8. adobe_items_*.json 包装格式：{"items": [{cookie, name, email, password}, ...]}
//    每条带 email + password + cookie，verify parser 走 wrapped 分支正确摊平到 items
//    并归一化 email、保留 cookie/password。
func TestParseAdobeImportWrappedItemsFormat(t *testing.T) {
	text := `{
  "items": [
    {"cookie": "ims_sid=AAA; relay=R1", "name": "alice@indevs.in", "email": "alice@indevs.in", "password": "Pass!1"},
    {"cookie": "ims_sid=BBB; relay=R2", "name": "BOB@INDEVS.IN", "email": "BOB@INDEVS.IN", "password": "Pass!2"}
  ]
}`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Email != "alice@indevs.in" {
		t.Errorf("expected lowercased email, got %q", items[0].Email)
	}
	if items[1].Email != "bob@indevs.in" {
		t.Errorf("expected lowercased email, got %q", items[1].Email)
	}
	if items[0].Password != "Pass!1" {
		t.Errorf("password lost, got %q", items[0].Password)
	}
	if items[1].Cookie != "ims_sid=BBB; relay=R2" {
		t.Errorf("cookie lost, got %q", items[1].Cookie)
	}
}

// 9. {"items":[...]} 包装但解析失败（items 不是数组）→ 应当给出明确错误而不是
//    被当成 JSONL 单行误判。
func TestParseAdobeImportWrappedItemsInvalid(t *testing.T) {
	text := `{"items": "not-an-array"}`
	items, errs := ParseAdobeImportText(text)
	if len(items) != 0 {
		t.Fatalf("expected 0 items on parse fail, got %d", len(items))
	}
	if len(errs) == 0 {
		t.Fatalf("expected at least one error, got none")
	}
}

// 10. 单个 JSON 对象（多行美化）+ 字段别名 cookies/user_id —— 浏览器插件导出的
//     单号格式。之前会被逐行解析当成乱码全部失败；现在整体当一条，且 cookies→cookie、
//     user_id→adobe_user_id 正确回填。
func TestParseAdobeImportSingleObjectWithAliases(t *testing.T) {
	text := `{
  "email": "zssrhggr@gocryptomail.com",
  "display_name": "David Smith",
  "user_id": "780983A86A1F4D2C0A495C46@AdobeID",
  "client_id": "clio-playground-web",
  "cookies": "ARID=abc; relay=545ba06b; ims_sid=AYbDI7CW; aux_sid=AHkTPg; sso_sid=ACQmalr",
  "create_time": "2026-06-06 05:30:33"
}`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %d", len(items))
	}
	it := items[0]
	if it.Email != "zssrhggr@gocryptomail.com" {
		t.Errorf("email = %q", it.Email)
	}
	if it.DisplayName != "David Smith" {
		t.Errorf("display_name = %q", it.DisplayName)
	}
	if it.AdobeUserID != "780983A86A1F4D2C0A495C46@AdobeID" {
		t.Errorf("user_id alias not mapped to adobe_user_id, got %q", it.AdobeUserID)
	}
	if !strings.Contains(it.Cookie, "ims_sid=AYbDI7CW") || !strings.Contains(it.Cookie, "sso_sid=ACQmalr") {
		t.Errorf("cookies alias not mapped to cookie, got %q", it.Cookie)
	}
}

// 11. cookies 别名在 JSON Array / JSONL 里也要生效，且不覆盖显式 cookie。
func TestParseAdobeImportCookiesAliasInArrayAndExplicitWins(t *testing.T) {
	text := `[
	  {"name":"a@x.com","cookies":"ims_sid=FROM_ALIAS"},
	  {"name":"b@x.com","cookie":"ims_sid=EXPLICIT","cookies":"ims_sid=IGNORED"}
	]`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !strings.Contains(items[0].Cookie, "FROM_ALIAS") {
		t.Errorf("alias cookies not applied, got %q", items[0].Cookie)
	}
	if !strings.Contains(items[1].Cookie, "EXPLICIT") || strings.Contains(items[1].Cookie, "IGNORED") {
		t.Errorf("explicit cookie should win over alias, got %q", items[1].Cookie)
	}
}

// 7. cookies (3).json 真实样本里 name 是邮箱、cookie 是浏览器导出原样的字符串
//    （包含分号、=、空格）。这里用裁切过的真实样本验证 JSON 解析+大小写归一+
//    cookie 完整保留。
func TestParseAdobeImportRealSampleCookiesJSON(t *testing.T) {
	text := `[
  {"cookie": "fg=2M4JP67OFLM5ADEKFAQVIHAACI======", "name": "Susanthompson9263@dfsb.eu.cc"},
  {"cookie": "ims_sid=AYio; aux_sid=AAC; relay=3b30; ftrset=109", "name": "barbarataylor9870@skyubvn.indevs.in"}
]`
	items, errs := ParseAdobeImportText(text)
	if len(errs) != 0 {
		t.Fatalf("unexpected errs: %v", errs)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if items[0].Email != "susanthompson9263@dfsb.eu.cc" {
		t.Errorf("expected lowercased email, got %q", items[0].Email)
	}
	if !strings.Contains(items[1].Cookie, "ims_sid=AYio") || !strings.Contains(items[1].Cookie, "ftrset=109") {
		t.Errorf("cookie not preserved verbatim: %q", items[1].Cookie)
	}
}
