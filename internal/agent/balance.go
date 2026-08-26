package agent

import (
	"context"
	"fmt"

	"agentic/internal/llm"
)

// showBalance 是否每轮结束展示余额（/balance 成功后开启）。
// 字段加在 runner.go 的 Runner struct 中（messages 字段附近）。

// formatBalanceLine 格式化余额展示行。
// 多货币逐行输出；货币符号: CNY→¥, USD→$。
func formatBalanceLine(resp *llm.BalanceResponse) string {
	if resp == nil || !resp.IsAvailable {
		return ""
	}
	lines := []string{}
	for _, b := range resp.BalanceInfos {
		symbol := map[string]string{"CNY": "¥", "USD": "$"}[b.Currency]
		if symbol == "" {
			symbol = b.Currency + " "
		}
		line := fmt.Sprintf("💰 余额: %s%s", symbol, b.TotalBalance)
		if b.ToppedUpBalance != "" || b.GrantedBalance != "" {
			line += fmt.Sprintf("（充值 %s%s / 赠金 %s%s）",
				symbol, b.ToppedUpBalance, symbol, b.GrantedBalance)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "💰 余额: 账户无可用余额"
	}
	return joinLines(lines)
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out[:len(out)-1]
}

// queryBalance 查询余额并展示到 UI。
// 返回 false 表示失败（每轮场景静默，/balance 场景由调用方提示原因）。
func (r *Runner) queryBalance() bool {
	resp, err := llm.FetchBalance(context.Background(), r.llm.APIKey(), balanceBaseURL(r.llm))
	if err != nil {
		return false
	}
	line := formatBalanceLine(resp)
	if line == "" {
		return false
	}
	r.ui.ShowBalance(line)
	return true
}

// balanceBaseURL 获取余额查询使用的 baseURL（复用 OpenAI client 的 BaseURL）。
func balanceBaseURL(c *llm.OpenAIClient) string {
	return c.BaseURL()
}

// handleBalanceCommand 处理 /balance 命令。
// 成功：展示余额并开启每轮展示；失败：提示原因。
func (r *Runner) handleBalanceCommand() {
	if !r.queryBalance() {
		r.ui.OnMessage("⚠️ 余额查询失败（每轮自动展示未开启）")
		return
	}
	r.showBalance = true
}
