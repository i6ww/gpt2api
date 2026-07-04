package handler

import "testing"

func TestStripMediaTokenSuffix(t *testing.T) {
	// 真实 token 形如 "<b64payload>.<b64sig>"（base64url，仅含 A-Za-z0-9-_，唯一的点是分隔符）。
	const tok = "eyJ0IjoiNDk5ZDQ2NGIwZGYzNDJhZDkyYzU5YWI1ZDIiLCJxIjowfQ.bnyceiUjPa5sKeaeT74JpGXFU-EpERJZDdqvRj6JQDw"
	cases := map[string]string{
		tok:            tok, // 无后缀：原样返回（不能误删签名分隔符）
		tok + ".png":   tok,
		tok + ".PNG":   tok,
		tok + ".jpg":   tok,
		tok + ".jpeg":  tok,
		tok + ".webp":  tok,
		tok + ".mp4":   tok,
		tok + ".webm":  tok,
		tok + ".mov":   tok,
		tok + ".txt":   tok + ".txt", // 未知后缀不剥离
		"":             "",
	}
	for in, want := range cases {
		if got := stripMediaTokenSuffix(in); got != want {
			t.Fatalf("stripMediaTokenSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
