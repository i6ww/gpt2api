// Command xaitest 直接调用 xai provider 的 Generate，验证官方 xAI API 视频生成端到端（创建→轮询→解析）。
// 用法：go run ./cmd/xaitest -cred <access_token> [-model grok-imagine-video] [-prompt "..."] [-proxy http://...]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/kleinai/backend/internal/provider"
	"github.com/kleinai/backend/internal/provider/xai"
)

func main() {
	cred := flag.String("cred", "", "xAI access_token")
	credFile := flag.String("credfile", "", "从登录产物 JSON 读取 access_token")
	model := flag.String("model", "grok-imagine-video", "upstream 模型名")
	prompt := flag.String("prompt", "a corgi puppy running on a sunny beach, cinematic", "提示词")
	proxy := flag.String("proxy", "", "出口代理")
	flag.Parse()

	token := *cred
	if token == "" && *credFile != "" {
		b, err := os.ReadFile(*credFile)
		if err != nil {
			fmt.Println("read credfile:", err)
			os.Exit(1)
		}
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		if v, ok := m["access_token"].(string); ok {
			token = v
		}
	}
	if token == "" {
		fmt.Println("missing -cred / -credfile")
		os.Exit(1)
	}

	p := xai.New("")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	start := time.Now()
	res, err := p.Generate(ctx, &provider.Request{
		TaskID:     "xaitest-" + fmt.Sprint(time.Now().Unix()),
		Kind:       provider.KindVideo,
		ModelCode:  *model,
		Prompt:     *prompt,
		Credential: token,
		ProxyURL:   *proxy,
		Count:      1,
	})
	if err != nil {
		fmt.Println("GENERATE FAILED:", err)
		os.Exit(2)
	}
	fmt.Printf("OK in %s, assets=%d\n", time.Since(start), len(res.Assets))
	for i, a := range res.Assets {
		fmt.Printf("  [%d] url=%s mime=%s %dx%d dur=%dms\n", i, a.URL, a.Mime, a.Width, a.Height, a.DurationMs)
	}
}
