package memory

import "testing"

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"ascii", "hello world this is a test"},
		{"pure cjk", "中文内容测试"},
		{"mixed", "hello 世界，this is a mixed 中文 test"},
		{"emoji", "hello 👋 world 🌍"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := EstimateTokens(tt.text)
			if got < 0 {
				t.Fatalf("EstimateTokens(%q) = %d, should be >= 0", tt.text, got)
			}
			// 空串应为 0。
			if tt.text == "" && got != 0 {
				t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
			}
		})
	}
}

func TestEstimateTokens_AsciiRatio(t *testing.T) {
	// 纯 ASCII：约 4 字符/token，20 字符 → 5 token。
	if got := EstimateTokens("abcdefghijklmnopqrst"); got != 5 {
		t.Errorf("20 ascii chars should be ~5 tokens, got %d", got)
	}
}

func TestEstimateTokens_CjkRatio(t *testing.T) {
	// 纯中文：CJK 约 1 字符/token（保守可 1.5）。
	// 10 个中文 → 应在 [6, 10] 区间（取整到 1.5 字符/token ≈ 7）。
	got := EstimateTokens("一二三四五六七八九十")
	if got < 6 || got > 10 {
		t.Errorf("10 CJK chars should be ~7 tokens, got %d", got)
	}
}

func TestEstimateTokens_EmojiHeavierThanCjk(t *testing.T) {
	// emoji 等非 ASCII 非 CJK 字符按 2 字符/token 估算（比 CJK 更重）。
	// 8 个 emoji → 4 token（若误按 CJK 1 字符/token 会得到 8）。
	got := EstimateTokens("😀😀😀😀😀😀😀😀")
	if got > 6 {
		t.Errorf("8 emoji should be estimated ~4 tokens (non-CJK rate), got %d", got)
	}
}
