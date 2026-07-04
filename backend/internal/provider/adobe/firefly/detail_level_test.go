package firefly

import "testing"

// TestGPTImageDetailLevel 锁住客户端画质级别 → 上游 detailLevel 的映射：
// low→1、high→3、original→5；未提交/其它值→3（默认中等）。与 billing 价格无关。
func TestGPTImageDetailLevel(t *testing.T) {
	cases := map[string]int{
		"low":      1,
		"LOW":      1,
		" low ":    1,
		"original": 5,
		"ORIGINAL": 5,
		"high":     3,
		"medium":   3,
		"auto":     3,
		"":         3,
		"1k":       3,
		"2k":       3,
		"4k":       3,
	}
	for in, want := range cases {
		if got := gptImageDetailLevel(in); got != want {
			t.Errorf("gptImageDetailLevel(%q)=%d, want %d", in, got, want)
		}
	}
}
