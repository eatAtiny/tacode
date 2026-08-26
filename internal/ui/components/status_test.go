package components

import (
	"strings"
	"testing"
)

func TestStatusModel_Balance(t *testing.T) {
	m := NewStatusModel()
	m.SetSession("demo")
	m.SetModel("deepseek-v4-flash")
	m.SetTokens(120, 45)
	m.SetBalance("💰 ¥110.00")

	v := m.View()
	for _, want := range []string{"agentic", "session: demo", "deepseek-v4-flash", "↑ 120", "↓ 45", "💰 ¥110.00"} {
		if !strings.Contains(v, want) {
			t.Errorf("View() 缺少 %q，实际:\n%s", want, v)
		}
	}
}

func TestStatusModel_BalanceEmpty(t *testing.T) {
	m := NewStatusModel()
	m.SetSession("demo")
	m.SetModel("m")
	m.SetTokens(1, 1)
	// 余额为空时不显示余额段。
	if strings.Contains(m.View(), "💰") {
		t.Error("余额为空时不应显示 💰 段")
	}
}
