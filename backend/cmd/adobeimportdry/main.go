// adobeimportdry 是一次性的 dry-run 工具：拿用户提供的真实文件丢给
// service.ParseAdobeImportText，确认计数 / placeholder 邮箱派生符合预期。
//
// 使用：
//
//	go run ./cmd/adobeimportdry "<file1>" "<file2>" ...
//
// 本工具不写库、不发请求；只是把解析结果摘要打到 stdout，供运营人工核对。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kleinai/backend/internal/service"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: adobeimportdry <file> [file...]")
		os.Exit(2)
	}
	for _, p := range os.Args[1:] {
		raw, err := os.ReadFile(p)
		if err != nil {
			fmt.Printf("[ERR] %s : %v\n", p, err)
			continue
		}
		items, errs := service.ParseAdobeImportText(string(raw))
		placeholders := 0
		realEmails := 0
		for _, it := range items {
			if strings.HasPrefix(it.Email, "adobe-cookie-") {
				placeholders++
			} else {
				realEmails++
			}
		}
		fmt.Printf("=== %s\n", filepath.Base(p))
		fmt.Printf("  items=%d  real-email=%d  placeholder=%d  errors=%d\n",
			len(items), realEmails, placeholders, len(errs))
		if len(items) > 0 {
			fmt.Printf("  sample[0].email=%s\n", items[0].Email)
			fmt.Printf("  sample[0].cookie_len=%d\n", len(items[0].Cookie))
		}
		if len(errs) > 0 && len(errs) <= 3 {
			for _, e := range errs {
				fmt.Printf("  err: %s\n", e)
			}
		} else if len(errs) > 3 {
			fmt.Printf("  err sample: %s ... (%d total)\n", errs[0], len(errs))
		}
	}
}
