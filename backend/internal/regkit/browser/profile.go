package browser

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// Chrome 大版本池。Chrome120 / 131 都是当前 utls 支持且 Adobe / Grok 接受的版本。
var chromeMajorVersions = []int{120, 122, 124, 126, 128, 130, 131}

// 平台分布（注册一般用 Windows / macOS）。
var platforms = []struct {
	UAPlatform   string
	SecChPlatform string
}{
	{"Windows NT 10.0; Win64; x64", `"Windows"`},
	{"Macintosh; Intel Mac OS X 10_15_7", `"macOS"`},
}

// 接受语言池。
var locales = []string{
	"en-US,en;q=0.9",
	"en-GB,en;q=0.9",
	"en-US,en;q=0.9,zh-CN;q=0.8",
}

func randIntn(n int) int {
	if n <= 0 {
		return 0
	}
	v, _ := rand.Int(rand.Reader, big.NewInt(int64(n)))
	return int(v.Int64())
}

// RandomProfile 生成一份随机浏览器指纹元数据。
func RandomProfile() Profile {
	major := chromeMajorVersions[randIntn(len(chromeMajorVersions))]
	plat := platforms[randIntn(len(platforms))]
	build := 6000 + randIntn(900)
	patch := 100 + randIntn(200)
	ua := fmt.Sprintf(
		"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.%d.%d Safari/537.36",
		plat.UAPlatform, major, build, patch,
	)
	secCh := fmt.Sprintf(`"Not:A-Brand";v="99", "Google Chrome";v="%d", "Chromium";v="%d"`, major, major)
	return Profile{
		UserAgent:       ua,
		SecChUA:         secCh,
		SecChUAPlatform: plat.SecChPlatform,
		Locale:          locales[randIntn(len(locales))],
	}
}
