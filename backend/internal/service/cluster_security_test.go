package service

import (
	"strings"
	"testing"
	"time"
)

func TestNodeSignerRoundtrip(t *testing.T) {
	s := NewNodeSigner("agent-1", []byte("super-secret-32-bytes-or-w/e"))

	req := SignedRequest{
		Method: "POST",
		Path:   "/admin/api/v1/cluster/lease?max=5",
		Body:   []byte(`{"node_id":"agent-1"}`),
	}

	node, ts, sig := s.Sign(req)
	if node != "agent-1" || sig == "" || ts == "" {
		t.Fatalf("sign empty: node=%q ts=%q sig=%q", node, ts, sig)
	}
	if err := s.Verify(req, ts, sig); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	// 改 body 就该失败
	bad := req
	bad.Body = []byte(`{"node_id":"agent-2"}`)
	if err := s.Verify(bad, ts, sig); err == nil {
		t.Fatalf("expected verify to fail with tampered body")
	}
	// 改 path 也失败
	bad = req
	bad.Path = "/admin/api/v1/cluster/lease?max=999"
	if err := s.Verify(bad, ts, sig); err == nil {
		t.Fatalf("expected verify to fail with tampered path")
	}
}

func TestNodeSignerSkew(t *testing.T) {
	s := NewNodeSigner("agent-1", []byte("k"))
	s.Skew = 5 * time.Second
	req := SignedRequest{Method: "GET", Path: "/x"}
	// 故意签一个 1 小时前的请求
	req.Ts = time.Now().Add(-1 * time.Hour).Unix()
	_, ts, sig := s.Sign(req)
	if err := s.Verify(req, ts, sig); err == nil {
		t.Fatalf("expected ts skew rejection")
	}
}

func TestTicketRoundtrip(t *testing.T) {
	secret := []byte("node-secret-abc")
	tk, err := SignTicket("hk01", secret, TicketPayload{
		Kind: "gen",
		Key:  "01HG.../0",
		Exp:  time.Now().Add(2 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	p, err := VerifyTicket("hk01", secret, tk.Token)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.Key != "01HG.../0" {
		t.Fatalf("payload key mismatch: %q", p.Key)
	}

	// 跨节点签 ticket 应失败
	if _, err := VerifyTicket("sg02", secret, tk.Token); err == nil {
		t.Fatalf("expected cross-node verify to fail")
	}
	// 错 secret 应失败
	if _, err := VerifyTicket("hk01", []byte("wrong"), tk.Token); err == nil {
		t.Fatalf("expected wrong-secret verify to fail")
	}
}

func TestTicketExpired(t *testing.T) {
	secret := []byte("k")
	tk, _ := SignTicket("hk01", secret, TicketPayload{
		Kind: "gen", Key: "x/0",
		Exp: time.Now().Add(-1 * time.Second).Unix(),
	})
	if _, err := VerifyTicket("hk01", secret, tk.Token); err == nil {
		t.Fatalf("expected expired")
	}
}

func TestBootstrapTokenRoundtrip(t *testing.T) {
	secret := []byte("bootstrap-secret")
	tok, err := SignBootstrapToken(secret, "agent-1")
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if !strings.Contains(tok, ".") {
		t.Fatalf("token shape: %q", tok)
	}
	p, err := VerifyBootstrapToken(secret, tok, time.Hour)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if p.NodeID != "agent-1" {
		t.Fatalf("nodeid: %q", p.NodeID)
	}
	if _, err := VerifyBootstrapToken(secret, tok, 0); err != nil {
		t.Fatalf("default ttl rejected fresh token: %v", err)
	}
}

func TestBuildDownloadURL(t *testing.T) {
	u := BuildDownloadURL("https://hk01.cdn.example/", "hk01", "abc.def")
	want := "https://hk01.cdn.example/d/hk01.abc.def"
	if u != want {
		t.Fatalf("url=%q want=%q", u, want)
	}
}
