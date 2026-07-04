// Package webhookurl validates outbound webhook callback URLs (SSRF guard).
package webhookurl

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
)

var (
	ErrEmpty     = errors.New("callback url is empty")
	ErrTooLong   = errors.New("callback url is too long")
	ErrScheme    = errors.New("callback url must use http or https")
	ErrHost      = errors.New("callback url host is invalid")
	ErrUserinfo  = errors.New("callback url must not contain credentials")
	ErrPrivate   = errors.New("callback url must not target private or local addresses")
	ErrNoAddress = errors.New("callback url host did not resolve")
)

const maxCallbackURLLen = 2048

// Validate checks scheme/host shape. Call ValidateReachable before delivery.
func Validate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ErrEmpty
	}
	if len(raw) > maxCallbackURLLen {
		return "", ErrTooLong
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrScheme
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", ErrHost
	}
	if u.User != nil {
		return "", ErrUserinfo
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if host == "" {
		return "", ErrHost
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "metadata.google.internal" {
		return "", ErrPrivate
	}
	if ip := net.ParseIP(host); ip != nil && isBlockedIP(ip) {
		return "", ErrPrivate
	}
	u.Fragment = ""
	return u.String(), nil
}

// ValidateReachable resolves the host and rejects private targets.
func ValidateReachable(ctx context.Context, raw string) (string, error) {
	normalized, err := Validate(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isBlockedIP(ip) {
			return "", ErrPrivate
		}
		return normalized, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil || len(addrs) == 0 {
		return "", ErrNoAddress
	}
	for _, addr := range addrs {
		if isBlockedIP(addr.IP) {
			return "", ErrPrivate
		}
	}
	return normalized, nil
}

func isBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	if ip.IsMulticast() {
		return true
	}
	// AWS / GCP metadata
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
	}
	return false
}
