package gpt

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebBootstrap403ContinuesWithDeviceID(t *testing.T) {
	p := &Provider{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "oai-did", Value: "device-from-cookie"})
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<html><head><meta name=\"viewport\""))
	}))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	fp := newWebFP()
	warn, err := p.webBootstrap(context.Background(), client, srv.URL, &fp)
	if err != nil {
		t.Fatalf("bootstrap 403 should not fail: %v", err)
	}
	if warn == "" || !strings.Contains(warn, "403") {
		t.Fatalf("expected bootstrap warn, got %q", warn)
	}
	if fp.DeviceID != "device-from-cookie" {
		t.Fatalf("device id = %q", fp.DeviceID)
	}
}

func TestWebBootstrap500Fails(t *testing.T) {
	p := &Provider{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	fp := newWebFP()
	_, err := p.webBootstrap(context.Background(), srv.Client(), srv.URL, &fp)
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("expected 502 bootstrap error, got %v", err)
	}
}

func TestIsRetriableWebStepErr(t *testing.T) {
	if !isRetriableWebStepErr(errString("gpt image2 web requirements 403: blocked")) {
		t.Fatal("expected requirements 403 to be retriable")
	}
	if isRetriableWebStepErr(errString("gpt image2 web requirements 400: bad request")) {
		t.Fatal("expected 400 to be non-retriable")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
