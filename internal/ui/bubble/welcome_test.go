package bubble

import (
	"strings"
	"testing"
)

// welcomeBanner 应包含 logo、欢迎语、版本、模型、目录、使用提示。
func TestWelcomeBanner(t *testing.T) {
	b := welcomeBanner("deepseek-v4-flash", "v0.1", "/tmp/proj")

	for _, want := range []string{
		"agentic",           // ASCII logo 文本
		"欢迎使用 agentic",      // 欢迎语
		"v0.1",              // 版本
		"deepseek-v4-flash", // 模型
		"/tmp/proj",         // 目录
		"/new",              // 使用提示
		"/balance",          // 使用提示
	} {
		if !strings.Contains(b, want) {
			t.Errorf("welcomeBanner 缺少 %q，实际:\n%s", want, b)
		}
	}
}

// welcomeBanner 空值场景：空 model/ver/cwd 不应 panic，仍含欢迎语。
func TestWelcomeBanner_Empty(t *testing.T) {
	b := welcomeBanner("", "", "")
	if !strings.Contains(b, "欢迎使用 agentic") {
		t.Errorf("空参数时仍应有欢迎语，实际:\n%s", b)
	}
}
