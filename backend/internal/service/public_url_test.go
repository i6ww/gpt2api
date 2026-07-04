package service

import (
	"context"
	"testing"
)

func TestAbsolutizeMediaURLKeepsAbsolute(t *testing.T) {
	got := AbsolutizeMediaURL(context.Background(), nil, "", "https://cdn.example.com/a.png")
	if got != "https://cdn.example.com/a.png" {
		t.Fatalf("got %q", got)
	}
}

func TestAbsolutizeMediaURLFallbackOrigin(t *testing.T) {
	got := AbsolutizeMediaURL(context.Background(), nil, "https://hook.example.com", "/api/v1/gen/cached/x.png")
	if got != "https://hook.example.com/api/v1/gen/cached/x.png" {
		t.Fatalf("got %q", got)
	}
}

func TestMediaPublicBaseFromCORS(t *testing.T) {
	t.Setenv("KLEIN_CORS_ORIGINS", "http://localhost:9000,https://klein.example.com")
	got := MediaPublicBase(context.Background(), nil, "")
	if got != "https://klein.example.com" {
		t.Fatalf("got %q", got)
	}
}
