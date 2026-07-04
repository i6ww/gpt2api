package service

import "testing"

func TestSanitizeDBTextRemovesMalformedUTF8(t *testing.T) {
	got := sanitizeDBText("正常中文" + string([]byte{0xe5}) + "abc")
	want := "正常中文abc"
	if got != want {
		t.Fatalf("sanitizeDBText() = %q, want %q", got, want)
	}
}
