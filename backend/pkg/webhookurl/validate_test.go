package webhookurl

import "testing"

func TestValidateAllowsPublicHTTPS(t *testing.T) {
	got, err := Validate("https://example.com/hook")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://example.com/hook" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateBlocksPrivate(t *testing.T) {
	cases := []string{
		"http://127.0.0.1/hook",
		"http://localhost/hook",
		"http://10.0.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data",
	}
	for _, raw := range cases {
		if _, err := Validate(raw); err == nil {
			t.Fatalf("expected error for %q", raw)
		}
	}
}
