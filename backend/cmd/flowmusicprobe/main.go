// Command flowmusicprobe 用导出的 FlowMusic 浏览器 cookie 对真实上游跑一次音乐生成，
// 用于端到端验证 flowmusic provider。仅本地诊断用。
//
// 用法：
//
//	go run ./cmd/flowmusicprobe -cookies ../.tools/flowmusic_cookies.json -prompt "写一首轻快的中文流行歌" -model lyria
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/flowmusic"
)

func main() {
	cookiesPath := flag.String("cookies", "../.tools/flowmusic_cookies.json", "浏览器导出的 cookie JSON 数组路径")
	prompt := flag.String("prompt", "写一首轻快温暖的中文流行歌，关于夏天的海边", "音乐生成提示词")
	modelCode := flag.String("model", "lyria", "模型 code")
	proxyURL := flag.String("proxy", "", "可选代理 URL")
	flag.Parse()

	raw, err := os.ReadFile(*cookiesPath)
	if err != nil {
		fmt.Println("read cookies file failed:", err)
		os.Exit(1)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		fmt.Println("parse cookies json failed:", err)
		os.Exit(1)
	}
	var parts []string
	for _, it := range items {
		name, _ := it["name"].(string)
		val, _ := it["value"].(string)
		if name == "" || val == "" {
			continue
		}
		parts = append(parts, name+"="+val)
	}
	cookieHeader := strings.Join(parts, "; ")
	if cookieHeader == "" {
		fmt.Println("no cookies parsed")
		os.Exit(1)
	}
	bundle, _ := json.Marshal(map[string]string{"cookies": cookieHeader})

	prov := flowmusic.New(flowmusic.Config{})

	ctx := context.Background()
	start := time.Now()
	fmt.Printf("== flowmusic probe ==\nmodel=%s\nprompt=%s\n\n", *modelCode, *prompt)

	res, err := prov.Generate(ctx, &provider.Request{
		TaskID:     "probe-" + fmt.Sprint(time.Now().Unix()),
		Kind:       provider.KindMusic,
		Mode:       provider.ModeT2A,
		ModelCode:  *modelCode,
		Prompt:     *prompt,
		Count:      1,
		Credential: string(bundle),
		ProxyURL:   *proxyURL,
		OnPollProgress: func(_ context.Context, percent, _ int) {
			fmt.Printf("  progress: %d%%\n", percent)
		},
		UpstreamLog: func(_ context.Context, e provider.UpstreamLogEntry) {
			if e.Error != "" {
				fmt.Printf("  [stage %s] status=%d err=%s\n", e.Stage, e.StatusCode, e.Error)
			} else {
				fmt.Printf("  [stage %s] status=%d ok\n", e.Stage, e.StatusCode)
			}
		},
	})
	if err != nil {
		fmt.Println("\nGENERATE FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("\n== SUCCESS in %s ==\n", time.Since(start).Round(time.Second))
	for i, a := range res.Assets {
		fmt.Printf("asset[%d]:\n  audio=%s\n  cover=%s\n  duration_ms=%d\n", i, a.URL, a.ThumbURL, a.DurationMs)
		if b, _ := json.MarshalIndent(a.Meta, "  ", "  "); len(b) > 0 {
			fmt.Printf("  meta=%s\n", string(b))
		}
	}
}
