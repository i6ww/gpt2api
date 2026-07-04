// Package browser 注册流程使用的"浏览器化"HTTP client。
//
// 提供：
//
//   - utls Chrome131 TLS 指纹（绕过 Cloudflare / Akamai 的 TLS 指纹检测）
//   - 自动 Cookie jar（跨重定向累积 Set-Cookie）
//   - 代理（http/https/socks5）
//   - 每个 client 一份随机 User-Agent + sec-ch-ua
//
// 依赖：github.com/refraction-networking/utls
package browser

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// Profile 一个 browser 会话的指纹元数据。
type Profile struct {
	UserAgent       string
	SecChUA         string
	SecChUAPlatform string
	Locale          string
}

// Client 包装 net/http.Client，挂上 utls TLS 指纹与 cookie jar。
type Client struct {
	HTTP    *http.Client
	Profile Profile
	Jar     *cookiejar.Jar
}

// Options 创建参数。
type Options struct {
	ProxyURL string        // 形如 http://user:pass@host:port，留空走直连
	Timeout  time.Duration // 整个请求超时，默认 60s
}

// New 创建一个 Client。返回 error 通常是代理 URL 解析失败。
func New(opts Options) (*Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{})
	if err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	transport, err := newTransport(opts.ProxyURL, timeout)
	if err != nil {
		return nil, err
	}
	prof := RandomProfile()
	hc := &http.Client{
		Transport: transport,
		Jar:       jar,
		Timeout:   timeout,
	}
	return &Client{
		HTTP:    hc,
		Profile: prof,
		Jar:     jar,
	}, nil
}

// Do 包装：自动注入 UA 与基础头，再交给底层 http.Do。调用方仍可覆盖。
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", c.Profile.UserAgent)
	}
	if req.Header.Get("sec-ch-ua") == "" {
		req.Header.Set("sec-ch-ua", c.Profile.SecChUA)
	}
	if req.Header.Get("sec-ch-ua-mobile") == "" {
		req.Header.Set("sec-ch-ua-mobile", "?0")
	}
	if req.Header.Get("sec-ch-ua-platform") == "" {
		req.Header.Set("sec-ch-ua-platform", c.Profile.SecChUAPlatform)
	}
	if req.Header.Get("Accept-Language") == "" {
		req.Header.Set("Accept-Language", c.Profile.Locale)
	}
	return c.HTTP.Do(req)
}

// NewRequest 便利方法，自带 Context。
func (c *Client) NewRequest(ctx context.Context, method, url string, body interface{ Read(p []byte) (n int, err error) }) (*http.Request, error) {
	if body == nil {
		return http.NewRequestWithContext(ctx, method, url, nil)
	}
	return http.NewRequestWithContext(ctx, method, url, body)
}

// CookiesFor 返回某 host 的 cookie 切片（便于诊断 / 抓 sso）。
func (c *Client) CookiesFor(rawURL string) []*http.Cookie {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	return c.Jar.Cookies(u)
}

// === 内部：utls Transport（h2-aware，对齐 Chrome 真实 ALPN） ===
//
// Cloudflare 在敏感接口上会比对 ClientHello 的 ALPN 列表与真实 Chrome 模板，
// 如果只声明 http/1.1 但伪装成 Chrome120，会被 1020/1010 直接 firewall block。
// 所以这里完整复用 outbound 包的 h2 协商策略：
//
//   - ALPN 同时声明 ["h2","http/1.1"]，跟 Chrome 真实指纹保持一致
//   - 协商出 h2  → 用 golang.org/x/net/http2 在该 conn 上跑 client conn
//   - 协商出 h1  → 直接 buffer read response（不再走标准 net/http.Transport，
//     因为标准 transport 不能接管由我们自定义的 utls conn）
//
// 代价：失去连接复用。注册流程每个会话也就 5~10 个请求，影响可忽略。
func newTransport(proxyURL string, timeout time.Duration) (http.RoundTripper, error) {
	t := &uTLSTransport{timeout: timeout}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(u.Scheme) {
		case "socks5", "socks5h", "http", "https":
			t.proxyURL = u
		default:
			return nil, errors.New("不支持的代理协议：" + u.Scheme)
		}
	}
	return t, nil
}

type uTLSTransport struct {
	proxyURL *url.URL
	timeout  time.Duration
}

func (t *uTLSTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("missing request URL")
	}
	// 非 https 直接走标准 transport（极少触发，sign-up 流程全 https）。
	if req.URL.Scheme != "https" {
		std := &http.Transport{
			Proxy: func(*http.Request) (*url.URL, error) {
				if t.proxyURL == nil || strings.HasPrefix(strings.ToLower(t.proxyURL.Scheme), "socks") {
					return nil, nil
				}
				return t.proxyURL, nil
			},
		}
		return std.RoundTrip(req)
	}

	conn, err := t.dialTarget(req.Context(), req.URL)
	if err != nil {
		return nil, err
	}
	closeOnErr := true
	defer func() {
		if closeOnErr {
			_ = conn.Close()
		}
	}()

	host := req.URL.Hostname()
	tlsConn := utls.UClient(conn, &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: false,
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"h2", "http/1.1"},
	}, utls.HelloChrome_133)
	if err := tlsConn.HandshakeContext(req.Context()); err != nil {
		return nil, fmt.Errorf("tls handshake to %s failed: %w", req.URL.Host, err)
	}

	if tlsConn.ConnectionState().NegotiatedProtocol == "h2" {
		h2Transport := &http2.Transport{}
		cc, err := h2Transport.NewClientConn(tlsConn)
		if err != nil {
			return nil, fmt.Errorf("create http2 client failed: %w", err)
		}
		resp, err := cc.RoundTrip(req)
		if err != nil {
			return nil, fmt.Errorf("http2 request failed: %w", err)
		}
		resp.Body = &connReadCloser{Reader: resp.Body, closer: tlsConn}
		closeOnErr = false
		return resp, nil
	}

	if err := writeRequest(tlsConn, req); err != nil {
		return nil, fmt.Errorf("write request failed: %w", err)
	}
	br := bufio.NewReader(tlsConn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		return nil, fmt.Errorf("read response failed: %w", err)
	}
	resp.Body = &connReadCloser{Reader: resp.Body, closer: tlsConn}
	closeOnErr = false
	return resp, nil
}

func (t *uTLSTransport) dialTarget(ctx context.Context, target *url.URL) (net.Conn, error) {
	addr := canonicalAddr(target)
	if t.proxyURL == nil {
		return (&net.Dialer{Timeout: t.timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", addr)
	}
	switch strings.ToLower(t.proxyURL.Scheme) {
	case "http", "https":
		return t.dialHTTPProxy(ctx, addr)
	case "socks5", "socks5h":
		return t.dialSOCKS5(ctx, addr)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", t.proxyURL.Scheme)
	}
}

func (t *uTLSTransport) dialHTTPProxy(ctx context.Context, targetAddr string) (net.Conn, error) {
	proxyAddr := t.proxyURL.Host
	conn, err := (&net.Dialer{Timeout: t.timeout, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", proxyAddr)
	if err != nil {
		return nil, fmt.Errorf("connect proxy %s failed: %w", proxyAddr, err)
	}
	closeOnErr := true
	defer func() {
		if closeOnErr {
			_ = conn.Close()
		}
	}()

	var proxyConn net.Conn = conn
	if strings.EqualFold(t.proxyURL.Scheme, "https") {
		host := t.proxyURL.Hostname()
		tlsProxy := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if err := tlsProxy.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("tls handshake to proxy %s failed: %w", proxyAddr, err)
		}
		proxyConn = tlsProxy
	}

	connectReq := "CONNECT " + targetAddr + " HTTP/1.1\r\nHost: " + targetAddr + "\r\nProxy-Connection: Keep-Alive\r\n"
	if t.proxyURL.User != nil {
		pw, _ := t.proxyURL.User.Password()
		token := base64.StdEncoding.EncodeToString([]byte(t.proxyURL.User.Username() + ":" + pw))
		connectReq += "Proxy-Authorization: Basic " + token + "\r\n"
	}
	connectReq += "\r\n"
	if _, err := io.WriteString(proxyConn, connectReq); err != nil {
		return nil, fmt.Errorf("write CONNECT to proxy failed: %w", err)
	}
	br := bufio.NewReader(proxyConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return nil, fmt.Errorf("read CONNECT response from proxy failed: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("proxy CONNECT %s returned HTTP %d", targetAddr, resp.StatusCode)
	}
	closeOnErr = false
	return proxyConn, nil
}

func (t *uTLSTransport) dialSOCKS5(ctx context.Context, targetAddr string) (net.Conn, error) {
	var auth *proxy.Auth
	if t.proxyURL.User != nil {
		pw, _ := t.proxyURL.User.Password()
		auth = &proxy.Auth{User: t.proxyURL.User.Username(), Password: pw}
	}
	dialer, err := proxy.SOCKS5("tcp", t.proxyURL.Host, auth, &net.Dialer{
		Timeout:   t.timeout,
		KeepAlive: 30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("build socks5 dialer: %w", err)
	}
	ctxDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("socks5 dialer does not support context")
	}
	return ctxDialer.DialContext(ctx, "tcp", targetAddr)
}

func writeRequest(w io.Writer, req *http.Request) error {
	out := req.Clone(req.Context())
	out.RequestURI = ""
	out.URL = cloneURL(req.URL)
	out.Header = req.Header.Clone()
	if out.Header.Get("Host") == "" && req.Host != "" {
		out.Host = req.Host
	}
	return out.Write(w)
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return nil
	}
	cp := *u
	return &cp
}

func canonicalAddr(u *url.URL) string {
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return net.JoinHostPort(host, port)
}

type connReadCloser struct {
	io.Reader
	closer io.Closer
}

func (c *connReadCloser) Close() error {
	return c.closer.Close()
}
