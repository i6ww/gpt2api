// minttoken: 一次性工具——按 deploy/docker-compose.dev-full.yml 里的 KLEIN_JWT_SECRET
// 给指定 uid 颁一个 access token，供 _e2e_size_matrix.ps1 等测试脚本使用。
//
// 不在 cmd/ 主入口里——仅本地 dev 跑测试时用：
//   go run ./cmd/minttoken -uid 1 -hours 24 > backend/scripts/_token.txt
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/kleinai/backend/pkg/jwtx"
)

func main() {
	uid := flag.Uint64("uid", 1, "user id")
	hours := flag.Int("hours", 24, "token TTL in hours")
	secret := flag.String("secret", os.Getenv("KLEIN_JWT_SECRET"), "JWT secret (default $KLEIN_JWT_SECRET)")
	subj := flag.String("subject", "user", "subject: user|admin")
	flag.Parse()

	if *secret == "" {
		*secret = "local-dev-jwt-secret-32bytes-please-change"
	}
	mgr, err := jwtx.New(*secret, *secret+"-refresh", time.Duration(*hours)*time.Hour, 24*time.Hour)
	if err != nil {
		fmt.Fprintln(os.Stderr, "jwtx.New:", err)
		os.Exit(1)
	}
	sub := jwtx.SubjectUser
	roles := []string{"user"}
	if *subj == "admin" {
		sub = jwtx.SubjectAdmin
		roles = []string{"super", "admin"}
	}
	tok, _, err := mgr.IssueAccess(*uid, sub, uuid.NewString(), roles)
	if err != nil {
		fmt.Fprintln(os.Stderr, "IssueAccess:", err)
		os.Exit(1)
	}
	fmt.Print(tok)
}
