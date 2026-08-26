package agent

import (
	"sync"
	"testing"

	"agentic/internal/llm"
	"agentic/internal/ui/text"
)

func TestFormatBalanceLine_CNY(t *testing.T) {
	resp := &llm.BalanceResponse{
		IsAvailable: true,
		BalanceInfos: []llm.BalanceInfo{
			{Currency: "CNY", TotalBalance: "110.00", GrantedBalance: "10.00", ToppedUpBalance: "100.00"},
		},
	}
	line := formatBalanceLine(resp)
	want := "💰 余额: ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）"
	if line != want {
		t.Errorf("formatBalanceLine = %q, want %q", line, want)
	}
}

func TestFormatBalanceLine_MultiCurrency(t *testing.T) {
	resp := &llm.BalanceResponse{
		IsAvailable: true,
		BalanceInfos: []llm.BalanceInfo{
			{Currency: "CNY", TotalBalance: "110.00"},
			{Currency: "USD", TotalBalance: "5.00"},
		},
	}
	line := formatBalanceLine(resp)
	if line != "💰 余额: ¥110.00\n💰 余额: $5.00" {
		t.Errorf("formatBalanceLine = %q, want two lines", line)
	}
}

func TestFormatBalanceLine_Unavailable(t *testing.T) {
	if line := formatBalanceLine(nil); line != "" {
		t.Errorf("nil line = %q, want empty", line)
	}
	if line := formatBalanceLine(&llm.BalanceResponse{IsAvailable: false}); line != "💰 余额: 账户无可用余额" {
		t.Errorf("unavailable line = %q, want 账户无可用余额", line)
	}
	if line := formatBalanceLine(&llm.BalanceResponse{IsAvailable: true}); line != "💰 余额: 账户无可用余额" {
		t.Errorf("empty infos line = %q, want 账户无可用余额", line)
	}
}

func TestFormatBalanceLine_UnknownCurrency(t *testing.T) {
	resp := &llm.BalanceResponse{
		IsAvailable: true,
		BalanceInfos: []llm.BalanceInfo{
			{Currency: "EUR", TotalBalance: "100.00"},
		},
	}
	line := formatBalanceLine(resp)
	want := "💰 余额: EUR 100.00"
	if line != want {
		t.Errorf("formatBalanceLine = %q, want %q", line, want)
	}
}

func TestFormatBalanceLine_GrantedOnly(t *testing.T) {
	resp := &llm.BalanceResponse{
		IsAvailable: true,
		BalanceInfos: []llm.BalanceInfo{
			{Currency: "USD", TotalBalance: "5.00", GrantedBalance: "2.00"},
		},
	}
	line := formatBalanceLine(resp)
	// 充值段为空时省略，仅展示赠金段。
	want := "💰 余额: $5.00（赠金 $2.00）"
	if line != want {
		t.Errorf("formatBalanceLine = %q, want %q", line, want)
	}
}

func TestFormatBalanceLine_ToppedUpOnly(t *testing.T) {
	resp := &llm.BalanceResponse{
		IsAvailable: true,
		BalanceInfos: []llm.BalanceInfo{
			{Currency: "CNY", TotalBalance: "100.00", ToppedUpBalance: "100.00"},
		},
	}
	line := formatBalanceLine(resp)
	// 赠金段为空时省略，仅展示充值段。
	want := "💰 余额: ¥100.00（充值 ¥100.00）"
	if line != want {
		t.Errorf("formatBalanceLine = %q, want %q", line, want)
	}
}

// TestQueryBalancePerRound_ThresholdHint 验证每轮余额查询的失败静默 + 连续失败阈值提示。
//
// 用注入的查询桩（balanceQueryFunc）模拟失败/成功，避免真实网络依赖：
//   - 前 balanceFailThreshold 次失败 → 静默，不触发任何 UI 输出
//   - 第 balanceFailThreshold 次失败 → 触发一次提示（OnMessage）
//   - 成功一次 → 失败计数复位，后续失败重新从 1 计数
//
// UI 用 TextUI 捕获 OnMessage 事件。
func TestQueryBalancePerRound_ThresholdHint(t *testing.T) {
	var (
		mu       sync.Mutex
		messages []string
	)

	ui := text.NewTextUI()
	ui.OnEvent = func(name string, data any) {
		if name == "message" {
			mu.Lock()
			messages = append(messages, data.(string))
			mu.Unlock()
		}
	}

	r := newTestRunner(t)
	r.ui = ui

	// 桩：永远失败（返回 ("网络错误", false)）。
	failQuery := func() (string, bool) { return "网络错误", false }

	// 前 balanceFailThreshold 次失败：应静默（0 条提示）。
	for i := 0; i < balanceFailThreshold; i++ {
		r.queryBalanceWith(failQuery)
	}
	mu.Lock()
	got := len(messages)
	mu.Unlock()
	if got != 1 {
		t.Fatalf("连续 %d 次失败后提示数 = %d, want 1", balanceFailThreshold, got)
	}
	if messages[0] != "⚠️ 余额查询连续失败 3 次，请检查网络或 API key" {
		t.Errorf("提示内容 = %q, want 连续失败提示", messages[0])
	}

	// 第 4 次失败：已过阈值，不再重复提示（仍是 1 条）。
	r.queryBalanceWith(failQuery)
	mu.Lock()
	got = len(messages)
	mu.Unlock()
	if got != 1 {
		t.Errorf("超过阈值后继续失败，提示数 = %d, want 仍为 1", got)
	}

	// 成功一次 → 计数复位；再连续失败 2 次（未达阈值）→ 仍静默。
	okQuery := func() (string, bool) { return "💰 余额: ¥10.00", true }
	r.queryBalanceWith(okQuery)
	for i := 0; i < balanceFailThreshold-1; i++ {
		r.queryBalanceWith(failQuery)
	}
	mu.Lock()
	got = len(messages)
	mu.Unlock()
	if got != 1 {
		t.Errorf("复位后连续失败 %d 次，提示数 = %d, want 仍为 1", balanceFailThreshold-1, got)
	}
}
