// maildbg: 邮箱池单封诊断工具。
//
// 用法：
//
//	cd backend && go run ./scripts/maildbg
//	cd backend && go run ./scripts/maildbg -email user@outlook.com
//	cd backend && go run ./scripts/maildbg -dsn ... -aes-key ...
//
// 默认连本机 127.0.0.1:23306（dev-full compose 暴露端口），AES key 用 dev
// 的默认 32 字节明文。脚本一定不写库，只是按 mail_pool 一行：
//
//  1. AES 解密 refresh_token
//  2. 调 https://login.microsoftonline.com/.../token 换 access_token
//  3. 调 Graph API 列 Inbox + JunkEmail 最近 10 封
//
// 输出全部 subject / from / receivedDateTime / bodyPreview 前 80 字符。
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/kleinai/backend/pkg/crypto"
)

var proxyURL string

func main() {
	emailFlag := flag.String("email", "", "指定邮箱地址；为空则随机拿一行 status=available")
	dsnFlag := flag.String("dsn", envOr("KLEIN_DB_DSN", "klein:klein123@tcp(127.0.0.1:23306)/klein_ai?charset=utf8mb4&parseTime=True&loc=Local"), "mysql DSN")
	keyFlag := flag.String("aes-key", envOr("KLEIN_AES_KEY", "00000000000000000000000000000000"), "AES-256-GCM key（32 字节明文 / 64 hex）")
	proxyFlag := flag.String("proxy", "", "可选：HTTP 代理（如 http://user:pass@host:port），用于模拟 dispatcher 走代理调 Graph 的场景")
	flag.Parse()
	proxyURL = strings.TrimSpace(*proxyFlag)
	if proxyURL != "" {
		fmt.Printf("⚠️  使用代理：%s\n", maskProxy(proxyURL))
	} else {
		fmt.Println("（无代理，直连）")
	}

	aesKey, err := decodeAESKey(*keyFlag)
	if err != nil {
		log.Fatalf("AES key 不合法：%v", err)
	}
	aes, err := crypto.NewAESGCM(aesKey)
	if err != nil {
		log.Fatalf("AES 初始化失败：%v", err)
	}

	db, err := sql.Open("mysql", *dsnFlag)
	if err != nil {
		log.Fatalf("连 MySQL 失败：%v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		log.Fatalf("MySQL ping 失败：%v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	q := `SELECT id, email, client_id, refresh_token_enc, mode, status, failure_count, IFNULL(last_error, '') 
	      FROM mail_pool WHERE deleted_at IS NULL`
	args := []any{}
	if *emailFlag != "" {
		q += " AND email = ?"
		args = append(args, strings.ToLower(strings.TrimSpace(*emailFlag)))
	} else {
		q += " AND status='available' AND mode IN ('outlook_graph','outlook_imap')"
	}
	q += " ORDER BY failure_count ASC, imported_at ASC LIMIT 1"

	var (
		id, failCnt          uint64
		email, clientID      string
		mode, status, lastEr string
		rtEnc                []byte
	)
	if err := db.QueryRowContext(ctx, q, args...).Scan(&id, &email, &clientID, &rtEnc, &mode, &status, &failCnt, &lastEr); err != nil {
		if err == sql.ErrNoRows {
			log.Fatalf("没有匹配的邮箱（条件: %s）", q)
		}
		log.Fatalf("查询 mail_pool 失败：%v", err)
	}
	fmt.Printf("\n=== mail_pool 行 ===\n")
	fmt.Printf("  id=%d email=%s mode=%s status=%s failure_count=%d\n", id, email, mode, status, failCnt)
	fmt.Printf("  client_id=%s\n", clientID)
	if lastEr != "" {
		fmt.Printf("  last_error=%s\n", lastEr)
	}

	rt, err := aes.Decrypt(rtEnc)
	if err != nil {
		log.Fatalf("AES 解密 refresh_token 失败（key 是否对？）：%v", err)
	}
	rtStr := string(rt)
	fmt.Printf("\n=== refresh_token (前 32) ===\n  %s…\n", safeHead(rtStr, 32))

	// step 1: 换 access_token
	fmt.Printf("\n=== Step 1: refresh_token → access_token ===\n")
	scope := "https://graph.microsoft.com/Mail.Read offline_access"
	t0 := time.Now()
	at, err := refreshAccessToken(ctx, clientID, rtStr, scope)
	if err != nil {
		log.Fatalf("  ❌ 换 access_token 失败（refresh_token 已过期或失效）：%v", err)
	}
	fmt.Printf("  ✅ access_token 已拿到 len=%d 用时 %s\n", len(at), time.Since(t0))

	// step 2: 拉 Inbox + JunkEmail
	for _, folder := range []string{"Inbox", "JunkEmail"} {
		fmt.Printf("\n=== Step 2: 拉文件夹 %s 最近 10 封 ===\n", folder)
		t1 := time.Now()
		list, err := graphList(ctx, at, folder)
		if err != nil {
			fmt.Printf("  ❌ 拉取失败：%v\n", err)
			continue
		}
		fmt.Printf("  Graph API 返回 %d 封  用时 %s\n", len(list), time.Since(t1))
		if len(list) == 0 {
			fmt.Println("  （空）")
			continue
		}
		for i, m := range list {
			subj, _ := m["subject"].(string)
			from := ""
			if f, ok := m["from"].(map[string]any); ok {
				if ea, ok := f["emailAddress"].(map[string]any); ok {
					from, _ = ea["address"].(string)
				}
			}
			rt, _ := m["receivedDateTime"].(string)
			pv, _ := m["bodyPreview"].(string)
			pv = strings.ReplaceAll(strings.ReplaceAll(pv, "\r", " "), "\n", " ")
			pv = strings.TrimSpace(pv)
			if len(pv) > 80 {
				pv = pv[:80] + "…"
			}
			fmt.Printf("  [%d] %s ← %s\n      Subj: %s\n      Body: %s\n", i+1, rt, from, subj, pv)
		}
	}

	fmt.Println("\n=== 测试完成 ===")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func decodeAESKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 32 {
		return []byte(raw), nil
	}
	// 兼容 hex 64 字符
	out := make([]byte, 32)
	n, err := fmt.Sscanf(raw, "%64x", &out)
	if err == nil && n == 1 {
		return out, nil
	}
	return nil, fmt.Errorf("AES key 需要 32 字节明文或 64 hex 字符（实际长度 %d）", len(raw))
}

func safeHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func httpClient(timeout time.Duration) *http.Client {
	c := &http.Client{Timeout: timeout}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err == nil {
			c.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
		}
	}
	return c
}

func maskProxy(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	colon := strings.Index(raw[:at], ":")
	if colon < 0 {
		return raw
	}
	scheme := strings.Index(raw[:colon], "://")
	if scheme < 0 {
		return raw[:colon] + ":***" + raw[at:]
	}
	return raw[:colon+3] + raw[scheme+3:colon] + ":***" + raw[at:]
}

func refreshAccessToken(ctx context.Context, clientID, refreshToken, scope string) (string, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")
	form.Set("scope", scope)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://login.microsoftonline.com/common/oauth2/v2.0/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := httpClient(30 * time.Second).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", fmt.Errorf("响应非 JSON: %s", string(raw))
	}
	at, _ := m["access_token"].(string)
	if at == "" {
		desc, _ := m["error_description"].(string)
		return "", fmt.Errorf("响应缺 access_token: %s", desc)
	}
	return at, nil
}

func graphList(ctx context.Context, accessToken, folder string) ([]map[string]any, error) {
	u := fmt.Sprintf("https://graph.microsoft.com/v1.0/me/mailFolders('%s')/messages?$top=10&$orderby=receivedDateTime desc&$select=subject,from,receivedDateTime,body,bodyPreview,id",
		folder)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var body struct {
		Value []map[string]any `json:"value"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, err
	}
	return body.Value, nil
}
