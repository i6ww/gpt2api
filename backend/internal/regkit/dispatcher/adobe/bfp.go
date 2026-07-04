// BFP（Browser Fingerprint Profile）请求体构造。
//
// schema 来源：抓取真机一次成功 SUSI 的 HAR
// （c:\Users\Administrator\Desktop\newwork\cn.bing.com_2026_05_07_17_28_43.har）
// 中 https://idg.adobe.com/v1/api/bfp_capture 的 200 请求体。
//
// 注意：Adobe 在 2026 年初更新了 BFP schema，与 newwork python 项目里
// build_bfp_payload 老版本相比有四处大改：
//
//  1. headless 从布尔 → 嵌套对象（chromium / likeHeadless / headless / stealth）
//  2. math 从浮点字符串（"1.4474..."）→ 32 字符 hex（"f22a94013fc94e90..."）
//  3. webGlBasics / browserDetails / userAgentData 字段全变了
//  4. 顶层新增 mode / fontBatchSizeNonBlocking / version 三字段
//
// 旧 payload 在新后端会直接 400 → 没拿到 genuine_token → captcha 难度被推到不可解。
package adobe

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/kleinai/backend/internal/regkit/browser"
)

const imsClientIDForBFP = "clio-playground-web"

// 字体池（Windows 系统自带；与 HAR 中真机字体高度重合）。
var bfpFonts = []string{
	"Arial", "Arial Narrow", "Bahnschrift", "Cambria", "Cambria Math", "Calibri",
	"Calibri Light", "Candara", "Comic Sans MS", "Consolas", "Constantia",
	"Corbel", "Courier", "Courier New", "Ebrima", "Franklin Gothic Medium",
	"Gabriola", "Gadugi", "Georgia", "Helvetica", "HoloLens MDL2 Assets",
	"Impact", "Ink Free", "Javanese Text", "Leelawadee UI",
	"Lucida Console", "Lucida Sans Unicode", "Malgun Gothic",
	"Marlett", "Microsoft Himalaya", "Microsoft JhengHei",
	"Microsoft New Tai Lue", "Microsoft PhagsPa", "Microsoft Sans Serif",
	"Microsoft Tai Le", "Microsoft YaHei", "Microsoft Yi Baiti",
	"MingLiU-ExtB", "Mongolian Baiti", "MS Gothic", "MS PGothic", "MS UI Gothic",
	"MT Extra", "Myanmar Text", "Nirmala UI", "Palatino Linotype",
	"PMingLiU-ExtB", "Segoe Fluent Icons", "Segoe MDL2 Assets",
	"Segoe Print", "Segoe UI", "Segoe UI Emoji", "Segoe UI Historic",
	"Segoe UI Light", "Segoe UI Symbol", "SimHei", "SimSun", "SimSun-ExtB",
	"Sitka Banner", "Sitka Display", "Sitka Heading", "Sitka Small",
	"Sitka Subheading", "Sitka Text", "Sylfaen", "Symbol", "Tahoma",
	"Times New Roman", "Trebuchet MS", "Verdana", "Webdings", "Wingdings",
	"Yu Gothic", "Yu Gothic Light", "Yu Gothic Medium", "Yu Gothic UI",
	"Yu Gothic UI Light", "Yu Gothic UI Semibold", "Yu Gothic UI Semilight",
}

// 真机的 WebGL 渲染器。SwiftShader 是 Edge headless 模式默认 fallback，
// 与 HAR 中 vendor=WebKit / renderer=WebKit WebGL 的"前端值"保持一致。
//
// vendor / renderer 是 WebGL_DEBUG_RENDERER_INFO 后看到的"unmasked"值；
// vendor / renderer（前端值）是被浏览器抹掉后给页面看到的固定值。
var bfpWebGLProfiles = []struct {
	VendorUnmasked   string
	RendererUnmasked string
}{
	{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) UHD Graphics 630 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) Iris(R) Xe Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (Intel)", "ANGLE (Intel, Intel(R) UHD Graphics 770 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce GTX 1660 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (NVIDIA)", "ANGLE (NVIDIA, NVIDIA GeForce RTX 4060 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (AMD)", "ANGLE (AMD, AMD Radeon RX 6700 XT Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (AMD)", "ANGLE (AMD, AMD Radeon RX 7600 Direct3D11 vs_5_0 ps_5_0, D3D11)"},
	{"Google Inc. (Google)", "ANGLE (Google, Vulkan 1.3.0 (SwiftShader Device (Subzero) (0x0000C0DE)), SwiftShader driver)"},
}

var bfpTimezones = []string{
	"America/New_York",
	"America/Chicago",
	"America/Los_Angeles",
	"America/Denver",
	"America/Phoenix",
}

// buildBFPPayload 构造 /v1/api/bfp_capture 的请求 body。
//
// idgTokenLs 由调用方维护（一次注册流程内固定），通过 metaData.privateMeta.idgTokenLs 上报。
// 与 UA 一致的 sec-ch-ua 关键词从 bc.Profile.SecChUA 解析；如解析失败默认使用 Chrome 主流。
func buildBFPPayload(idgTokenLs string, prof browser.Profile) map[string]any {
	major := extractChromeMajor(prof.SecChUA)
	if major == 0 {
		major = 131
	}
	browserBrand, browserName, detailedName := detectBrowserBrand(prof.UserAgent, prof.SecChUA)
	hw := bfpWebGLProfiles[randIntn(len(bfpWebGLProfiles))]
	tz := bfpTimezones[randIntn(len(bfpTimezones))]
	canvasGeom := randomHex(16)
	canvasText := randomHex(16)
	// math 是 32 字符 hex（HAR 实测：固定长度 32，看似 md5）。
	mathHash := randomHex(16)
	// audio：HAR 是 124.04347527516074 这个常数；用真实值，不再随机化。
	// Adobe 后端会拿这个值与历史样本比对，乱跳的浮点反而更可疑。
	audioVal := 124.04347527516074
	cores := []int{8, 12, 16, 32}[randIntn(4)]
	mem := 8
	width, height := 1920, 1080
	if randIntn(3) == 0 {
		width, height = 2560, 1440
	}
	// fontPreferences 用 HAR 实测的 Edge 真机常数（不再 ±offset 随机），
	// 这些数值是浏览器 layout engine 计算的，几乎与设备无关。
	const fpDefault = 101.14583587646484
	const fpSerif = 110.14583587646484
	const fpMono = 79.33333587646484
	const fpMin = 6.3229169845581055
	const fpSystem = 107.53125

	totalTime := 250 + randIntn(120)

	return map[string]any{
		"metaData": map[string]any{
			"duration": map[string]any{
				"fonts": 61, "fontPreferences": 50, "audio": 1, "screenFrame": 0,
				"canvas": 64, "osCpu": 0, "languages": 0, "colorDepth": 0,
				"deviceMemory": 0, "screenResolution": 0, "hardwareConcurrency": 0,
				"timezone": 0, "sessionStorage": 0, "localStorage": 0, "indexedDB": 0,
				"openDatabase": 0, "cpuClass": 0, "platform": 0, "plugins": 19,
				"headless": 35, "touchSupport": 0, "vendor": 0, "vendorFlavors": 0,
				"cookiesEnabled": 0, "colorGamut": 0, "invertedColors": 0,
				"forcedColors": 0, "monochrome": 0, "contrast": 0, "reducedMotion": 0,
				"reducedTransparency": 0, "hdr": 0, "math": 1, "pdfViewerEnabled": 0,
				"architecture": 0, "applePay": 0, "privateClickMeasurement": 0,
				"userAgentData": 30, "mathml": 40, "clientRect": 39, "idgTokenLs": 0,
				"webGlBasics": 4, "webGlExtensions": 22, "browserDetails": 6, "speech": 165,
			},
			"totalTime":   totalTime,
			"privateMeta": map[string]any{"idgTokenLs": idgTokenLs},
			"meta":        map[string]any{"clientId": imsClientIDForBFP},
		},
		"fonts": bfpFonts,
		"fontPreferences": map[string]any{
			"default": fpDefault,
			"apple":   fpDefault,
			"serif":   fpSerif,
			"sans":    fpDefault,
			"mono":    fpMono,
			"min":     fpMin,
			"system":  fpSystem,
		},
		"audio":               audioVal,
		"screenFrame":         []int{0, 0, 50, 0},
		"canvas":              map[string]any{"winding": true, "geometry": canvasGeom, "text": canvasText},
		"languages":           map[string]any{"default": guessLanguageList(prof.Locale)},
		"colorDepth":          24,
		"deviceMemory":        mem,
		"screenResolution":    []int{width, height},
		"hardwareConcurrency": cores,
		"timezone":            tz,
		"sessionStorage":      true,
		"localStorage":        true,
		"indexedDB":           true,
		"openDatabase":        false,
		"platform":            "Win32",
		"plugins": []map[string]any{
			plugin("PDF Viewer"), plugin("Chrome PDF Viewer"),
			plugin("Chromium PDF Viewer"), plugin("Microsoft Edge PDF Viewer"),
			plugin("WebKit built-in PDF"),
		},
		// 2026 schema：headless 是嵌套对象，不能传 bool。
		"headless": map[string]any{
			"chromium": true,
			"likeHeadless": map[string]any{
				"noChrome":             false,
				"hasPermissionsBug":    false,
				"noPlugins":            false,
				"noMimeTypes":          false,
				"notificationIsDenied": false,
				"prefersLightColor":    true,
				"uaDataIsBlank":        false,
				"pdfIsDisabled":        false,
				"noTaskbar":            false,
				"hasVvpScreenRes":      false,
				"noWebShare":           false,
				"noContentIndex":       true,
				"noContactsManager":    true,
				"noDownlinkMax":        true,
			},
			"headless": map[string]any{
				"webDriverIsOn": false,
				"hasHeadlessUA": false,
			},
			"stealth": map[string]any{
				"hasIframeProxy":      false,
				"hasHighChromeIndex":  false,
				"hasBadChromeRuntime": false,
			},
		},
		"touchSupport":            map[string]any{"maxTouchPoints": 0, "touchEvent": false, "touchStart": false},
		"vendor":                  "Google Inc.",
		"vendorFlavors":           []string{"chrome"},
		"cookiesEnabled":          true,
		"colorGamut":              "srgb",
		"forcedColors":            false,
		"monochrome":              0,
		"contrast":                0,
		"reducedMotion":           false,
		"reducedTransparency":     false,
		"hdr":                     false,
		"math":                    mathHash,
		"pdfViewerEnabled": true,
		// architecture 是个 bitmask 整数；HAR 里实测是 255（一个普通 Win64 桌面版 Chromium 的值）。
		"architecture": 255,
		"applePay":     -1,
		// 不发 privateClickMeasurement：HAR 里没有这个键，Spring 后端在 JSON 里看到 null 会按
		// "未知字段"或 type 不符报 400。
		// 2026 schema：userAgentData.brands 是 ["Microsoft Edge"] 这种纯字符串数组，
		// 不再是 [{brand,version}] 对象数组；同时新增 brandsVersion 字段。
		"userAgentData": map[string]any{
			"architecture":    "x86",
			"bitness":         "64",
			"brands":          []string{browserBrand},
			"brandsVersion":   []string{fmt.Sprintf("%s %d", browserBrand, major)},
			"mobile":          false,
			"model":           "",
			"platform":        "Windows",
			"platformVersion": "19.0.0",
			"uaFullVersion":   fmt.Sprintf("%d.0.0.0", major),
		},
		// 2026 schema：mathml / clientRect 是 DOMRect 对象，不是 bool / array。
		// 真请求里这些值是浏览器对一组隐藏 DOM 节点跑 getBoundingClientRect 得到的实测尺寸；
		// 我们用一组合理的常量 + ±0.5 抖动还原 layout engine 的 sub-pixel 输出。
		"mathml":     buildMathmlRect(),
		"clientRect": buildClientRect(),
		// 2026 schema：webGlBasics 6 字段（version / vendor / vendorUnmasked / renderer / rendererUnmasked / shadingLanguageVersion）。
		"webGlBasics": map[string]any{
			"version":                "WebGL 1.0 (OpenGL ES 2.0 Chromium)",
			"vendor":                 "WebKit",
			"vendorUnmasked":         hw.VendorUnmasked,
			"renderer":               "WebKit WebGL",
			"rendererUnmasked":       hw.RendererUnmasked,
			"shadingLanguageVersion": "WebGL GLSL ES 1.0 (OpenGL ES GLSL ES 1.0 Chromium)",
		},
		// 2026 schema：webGlExtensions 是 32 字符 hex（猜测是 md5(joined)）；
		// list 形式直接 400。
		"webGlExtensions": hashWebGLExtensions(defaultWebGLExtensions()),
		// 2026 schema：browserDetails 5 字段。
		"browserDetails": map[string]any{
			"private":      false,
			"name":         browserName,
			"version":      fmt.Sprintf("%d.0.0.0", major),
			"platform":     "win",
			"detailedName": detailedName,
		},
		// 2026 schema：speech 是 {numVoices, voices: [...]}；裸 list 直接 400。
		"speech": map[string]any{
			"numVoices": len(defaultSpeechVoices()),
			"voices":    defaultSpeechVoices(),
		},
		// 2026 schema 新增三个顶层字段。
		"mode":                     "kBlocking",
		"fontBatchSizeNonBlocking": 3,
		"version":                  "1.0.12",
	}
}

// buildMathmlRect 构造 mathml 字段的 DOMRect，与 HAR 里的真值同量级（width≈2080, height≈333）。
//
// 这是浏览器渲染一段 <math> 元素后 getBoundingClientRect() 的产出，
// 含 x/y/width/height/top/right/bottom/left + font。
func buildMathmlRect() map[string]any {
	width := 2080.0 + randomFloat(0, 8)
	height := 333.0 + randomFloat(0, 1)
	x, y := 8.0, 8.0
	return map[string]any{
		"x": x, "y": y,
		"width": width, "height": height,
		"top": y, "right": x + width,
		"bottom": y + height, "left": x,
		"font": "math",
	}
}

// buildClientRect 构造 clientRect 子对象集合，与 HAR 里 button/progress/selection/summary/
// table/simpletext/transformedText 7 个 sub-rectangle 一一对应。
//
// 这些尺寸在不同 viewport / DPR 下会浮动，但同一台机器上稳定。我们用 HAR 实测值附近 ±随机
// 抖动来近似 sub-pixel layout 输出，避免后端拿到一组完全相同的浮点数当作"明显伪造"特征。
func buildClientRect() map[string]any {
	mk := func(x, y, w, h float64) map[string]any {
		return map[string]any{
			"x": x, "y": y,
			"width": w, "height": h,
			"top":    y,
			"right":  x + w,
			"bottom": y + h,
			"left":   x,
		}
	}
	jx := func(base float64) float64 { return base + randomFloat(-0.4, 0.4) }
	return map[string]any{
		"selection":       mk(jx(22.55), jx(-342.54), jx(167.33), jx(21.33)),
		"progress":        mk(jx(262.55), jx(382.75), 164, 20),
		"summary":         mk(jx(262.55), jx(15.46), jx(74.86), 74),
		"button":          mk(jx(262.55), jx(-149.54), 20, 10),
		"table":           mk(jx(64.52), 8, jx(98.92), 158),
		"simpletext":      mk(jx(20.11), jx(456.67), jx(264.55), jx(36.625)),
		"transformedText": mk(8, jx(306.78), jx(276.67), jx(209.10)),
	}
}

// hashWebGLExtensions 把 extension 列表用 "," 拼起来后取 md5，返回 32 字符 hex。
// 真 BFP 的字段值就是这个形态（见 HAR：'f8422ac056e877b3b13086020ae25e5e'）。
func hashWebGLExtensions(exts []string) string {
	sum := md5.Sum([]byte(strings.Join(exts, ",")))
	return hex.EncodeToString(sum[:])
}

func plugin(name string) map[string]any {
	return map[string]any{
		"name":        name,
		"description": "Portable Document Format",
		"mimeTypes": []map[string]any{
			{"type": "application/pdf", "suffixes": "pdf"},
			{"type": "text/pdf", "suffixes": "pdf"},
		},
	}
}

func defaultWebGLExtensions() []string {
	return []string{
		"ANGLE_instanced_arrays", "EXT_blend_minmax", "EXT_clip_control",
		"EXT_color_buffer_half_float", "EXT_depth_clamp", "EXT_disjoint_timer_query",
		"EXT_float_blend", "EXT_frag_depth", "EXT_polygon_offset_clamp",
		"EXT_shader_texture_lod", "EXT_texture_compression_bptc",
		"EXT_texture_compression_rgtc", "EXT_texture_filter_anisotropic",
		"EXT_texture_mirror_clamp_to_edge", "EXT_sRGB", "OES_element_index_uint",
		"OES_fbo_render_mipmap", "OES_standard_derivatives", "OES_texture_float",
		"OES_texture_float_linear", "OES_texture_half_float",
		"OES_texture_half_float_linear", "OES_vertex_array_object",
		"WEBGL_blend_func_extended", "WEBGL_color_buffer_float",
		"WEBGL_compressed_texture_s3tc", "WEBGL_compressed_texture_s3tc_srgb",
		"WEBGL_debug_renderer_info", "WEBGL_debug_shaders",
		"WEBGL_depth_texture", "WEBGL_draw_buffers", "WEBGL_lose_context",
		"WEBGL_multi_draw", "WEBGL_polygon_mode",
	}
}

// defaultSpeechVoices 真机 Edge 在 Win11 上 speechSynthesis.getVoices() 通常会报 250-330 个语音。
//
// HAR 里实测是 328 个，包含 Microsoft 在线/离线 + Google + Microsoft Natural。Adobe 后端可能
// 对 numVoices < 某个阈值或 voices 列表"过短"判定为 headless/bot。这里给一份接近真机的列表。
func defaultSpeechVoices() []string {
	// 偏移生成器，避免每次都返回完全一样的列表（不同设备 voices 列表可能略有不同）。
	base := baseSpeechVoices
	out := make([]string, 0, len(base))
	out = append(out, base...)
	return out
}

// detectBrowserBrand 从 UA + sec-ch-ua 推断浏览器品牌名（Edge / Chrome）。
//
// 返回三元组：
//
//	brand:        "Microsoft Edge" / "Google Chrome"
//	name:         "Edge" / "Chrome"
//	detailedName: "edg" / "chrome"
//
// 这三个名字之间在 BFP 后端会做一致性校验，不能 hardcode 单一值。
func detectBrowserBrand(ua, secChUA string) (string, string, string) {
	uaL := strings.ToLower(ua)
	if strings.Contains(uaL, "edg/") || strings.Contains(uaL, "edge/") ||
		strings.Contains(strings.ToLower(secChUA), "microsoft edge") {
		return "Microsoft Edge", "Edge", "edg"
	}
	return "Google Chrome", "Chrome", "chrome"
}

func parseSecChUA(s string) []map[string]any {
	out := []map[string]any{}
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.SplitN(raw, ";", 2)
		brand := strings.Trim(parts[0], `" `)
		ver := ""
		if len(parts) == 2 {
			v := strings.TrimSpace(parts[1])
			v = strings.TrimPrefix(v, "v=")
			ver = strings.Trim(v, `"`)
		}
		if brand == "" {
			continue
		}
		out = append(out, map[string]any{"brand": brand, "version": ver})
	}
	return out
}

// extractChromeMajor 从 sec-ch-ua 字符串里抠出 Chrome / Chromium / Edge 主版本号。
func extractChromeMajor(secChUA string) int {
	for _, b := range parseSecChUA(secChUA) {
		brand, _ := b["brand"].(string)
		l := strings.ToLower(brand)
		if strings.Contains(l, "chrome") || strings.Contains(l, "edge") {
			ver, _ := b["version"].(string)
			if v, err := strconv.Atoi(ver); err == nil {
				return v
			}
		}
	}
	return 0
}

func guessLanguageList(locale string) []string {
	if locale == "" {
		return []string{"en-US"}
	}
	first := strings.SplitN(locale, ",", 2)[0]
	first = strings.SplitN(first, ";", 2)[0]
	first = strings.TrimSpace(first)
	if first == "" {
		first = "en-US"
	}
	return []string{first}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// randomFloat 返回 [lo, hi) 之间均匀分布的 float64（用 crypto/rand 取 53 位精度，避免再依赖 math/rand）。
func randomFloat(lo, hi float64) float64 {
	if hi <= lo {
		return lo
	}
	const denom = 1 << 53
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(denom)))
	r := float64(v.Int64()) / float64(denom)
	return lo + r*(hi-lo)
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}
