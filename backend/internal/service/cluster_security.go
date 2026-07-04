// Package service · cluster security
//
// 集群相关的两个签名工具：
//   1. NodeSigner     主控 ↔ agent 之间所有 HTTP 请求使用 HMAC-SHA256 双向验签。
//   2. DownloadTicket 主控签发的短期下载 ticket，agent 用 node_secret 验证。
//
// 详见 deploy/docs/CLUSTER_OVERVIEW.md §6 安全模型。
package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kleinai/backend/pkg/crypto"
)

// ── 1. HMAC 双向签名 ─────────────────────────────────────────

// SignedRequest 签名输入。
type SignedRequest struct {
	Method string // GET / POST
	Path   string // 含 query, eg /admin/api/v1/cluster/lease?max=5
	Body   []byte
	Ts     int64 // unix seconds; 0 取 now
}

// NodeSigner 用单个 secret 给 HTTP 请求计算签名 / 校验签名。
type NodeSigner struct {
	NodeID string
	Secret []byte
	Skew   time.Duration // 允许时钟漂移；默认 30s
}

// NewNodeSigner 构造（secret 明文，调用方负责密文 → 明文）。
func NewNodeSigner(nodeID string, secret []byte) *NodeSigner {
	return &NodeSigner{NodeID: nodeID, Secret: secret, Skew: 30 * time.Second}
}

// Sign 返回 (header values for X-Klein-Node, X-Klein-Ts, X-Klein-Sig).
func (s *NodeSigner) Sign(req SignedRequest) (node, ts, sig string) {
	if req.Ts == 0 {
		req.Ts = time.Now().Unix()
	}
	bodyHash := sha256.Sum256(req.Body)
	payload := strings.Join([]string{
		strconv.FormatInt(req.Ts, 10),
		strings.ToUpper(req.Method),
		req.Path,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	mac := hmac.New(sha256.New, s.Secret)
	mac.Write([]byte(payload))
	return s.NodeID, strconv.FormatInt(req.Ts, 10), base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 校验。
func (s *NodeSigner) Verify(req SignedRequest, ts, sig string) error {
	tsInt, err := strconv.ParseInt(strings.TrimSpace(ts), 10, 64)
	if err != nil {
		return errors.New("ts invalid")
	}
	now := time.Now().Unix()
	if diff := now - tsInt; diff > int64(s.Skew/time.Second) || diff < -int64(s.Skew/time.Second) {
		return fmt.Errorf("ts skew %ds exceeds limit", diff)
	}
	req.Ts = tsInt
	_, _, expect := s.Sign(req)
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return errors.New("signature mismatch")
	}
	return nil
}

// ── 2. 下载 ticket ─────────────────────────────────────────

// TicketPayload 内部 JSON 体。
type TicketPayload struct {
	Kind   string `json:"k"` // asset_kind, eg "gen"
	Key    string `json:"a"` // asset_key
	Shape  string `json:"s"` // "" / "thumb"
	Exp    int64  `json:"e"` // unix seconds
	Nonce  string `json:"n"` // 6 byte base64url
	UserID uint64 `json:"u,omitempty"`
}

// Ticket = base64url(payload).base64url(hmac).
// 完整 URL 路径形如：https://<node>/d/<node_id>.<ticket>
type Ticket struct {
	NodeID  string
	Payload TicketPayload
	Token   string // <b64payload>.<b64sig>
}

// SignTicket 用 node_secret 给 payload 签名。
func SignTicket(nodeID string, secret []byte, p TicketPayload) (*Ticket, error) {
	if nodeID == "" || len(secret) == 0 {
		return nil, errors.New("node secret missing")
	}
	if p.Exp == 0 {
		p.Exp = time.Now().Add(5 * time.Minute).Unix()
	}
	if p.Nonce == "" {
		b, err := crypto.RandomBytes(6)
		if err != nil {
			return nil, err
		}
		p.Nonce = base64.RawURLEncoding.EncodeToString(b)
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(nodeID + "." + encPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return &Ticket{
		NodeID:  nodeID,
		Payload: p,
		Token:   encPayload + "." + sig,
	}, nil
}

// VerifyTicket 解析 + 校验 + 检查过期。
// ticketStr 形如 "<b64payload>.<b64sig>"，nodeID 由路径前缀提取。
func VerifyTicket(nodeID string, secret []byte, ticketStr string) (*TicketPayload, error) {
	parts := strings.SplitN(ticketStr, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed ticket")
	}
	encPayload, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(nodeID + "." + encPayload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, errors.New("ticket signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return nil, fmt.Errorf("ticket payload: %w", err)
	}
	var p TicketPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("ticket payload json: %w", err)
	}
	if time.Now().Unix() > p.Exp {
		return nil, errors.New("ticket expired")
	}
	return &p, nil
}

// BuildDownloadURL 把签好的 ticket 拼成最终用户态可访问的 URL。
//   publicHost: 节点 public_host (https://hk01.cdn.example)
//   token: ticket.Token
//
// 形如：https://hk01.cdn.example/d/<node_id>.<token>
func BuildDownloadURL(publicHost, nodeID, token string) string {
	host := strings.TrimRight(publicHost, "/")
	return host + "/d/" + url.PathEscape(nodeID) + "." + token
}

// ── 3. Bootstrap token ─────────────────────────────────────
//
// 主控注册节点时一次性发给 agent 的引导 token，包含 node_id + nonce + 签名。
// agent 启动时拿 token 去 /cluster/handshake 换取真正的 hmac_secret。

// BootstrapToken 结构。
type BootstrapPayload struct {
	NodeID string `json:"n"`
	Issued int64  `json:"i"`
	Nonce  string `json:"x"`
}

// SignBootstrapToken bootstrapSecret 是主控全局 KLEIN_CLUSTER_BOOTSTRAP_SECRET。
func SignBootstrapToken(bootstrapSecret []byte, nodeID string) (string, error) {
	if len(bootstrapSecret) == 0 {
		return "", errors.New("bootstrap secret missing")
	}
	nonce, err := crypto.RandomBytes(8)
	if err != nil {
		return "", err
	}
	p := BootstrapPayload{
		NodeID: nodeID,
		Issued: time.Now().Unix(),
		Nonce:  base64.RawURLEncoding.EncodeToString(nonce),
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	encPayload := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, bootstrapSecret)
	mac.Write([]byte(encPayload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encPayload + "." + sig, nil
}

// base64URLNoPad base64 raw url（无 padding）helper，跨包共享。
func base64URLNoPad(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

// VerifyBootstrapToken 校验 token 并返回 payload；默认 60 min 有效期。
func VerifyBootstrapToken(bootstrapSecret []byte, token string, ttl time.Duration) (*BootstrapPayload, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed bootstrap token")
	}
	encPayload, sig := parts[0], parts[1]
	mac := hmac.New(sha256.New, bootstrapSecret)
	mac.Write([]byte(encPayload))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(want), []byte(sig)) {
		return nil, errors.New("bootstrap signature mismatch")
	}
	raw, err := base64.RawURLEncoding.DecodeString(encPayload)
	if err != nil {
		return nil, fmt.Errorf("bootstrap payload: %w", err)
	}
	var p BootstrapPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("bootstrap json: %w", err)
	}
	if ttl <= 0 {
		ttl = 60 * time.Minute
	}
	if time.Since(time.Unix(p.Issued, 0)) > ttl {
		return nil, errors.New("bootstrap token expired")
	}
	return &p, nil
}
