package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"agentic/internal/llm"
)

// currencySymbol 货币符号映射（未知货币回退到币种代码）。
var currencySymbol = map[string]string{"CNY": "¥", "USD": "$"}

// formatBalanceLine 格式化余额展示行。
// 多货币逐行输出；货币符号: CNY→¥, USD→$，未知货币用币种代码。
// 充值/赠金两段仅在对应余额非空时显示，用 " / " 连接，空段省略。
// nil 返回空串（视为无响应）；is_available=false 与空 balance_infos 均显示"账户无可用余额"。
func formatBalanceLine(resp *llm.BalanceResponse) string {
	if resp == nil {
		return ""
	}
	lines := []string{}
	if resp.IsAvailable {
		for _, b := range resp.BalanceInfos {
			symbol := currencySymbol[b.Currency]
			if symbol == "" {
				symbol = b.Currency + " "
			}
			line := fmt.Sprintf("💰 余额: %s%s", symbol, b.TotalBalance)

			// 充值/赠金明细：仅展示非空字段，两段用 " / " 连接，空段省略。
			segments := []string{}
			if b.ToppedUpBalance != "" {
				segments = append(segments, fmt.Sprintf("充值 %s%s", symbol, b.ToppedUpBalance))
			}
			if b.GrantedBalance != "" {
				segments = append(segments, fmt.Sprintf("赠金 %s%s", symbol, b.GrantedBalance))
			}
			if len(segments) > 0 {
				line += "（" + strings.Join(segments, " / ") + "）"
			}
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return "💰 余额: 账户无可用余额"
	}
	return strings.Join(lines, "\n")
}

// queryBalance 查询余额并展示到 UI。
// 返回 (展示行, 是否成功)：成功时展示并返回 (line, true)；
// 失败返回 (错误原因, false)，空行/不可用返回 ("", false)。
// 5s 超时 context 与 balanceClient 超时一致，避免网络异常挂死。
func (r *Runner) queryBalance() (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := llm.FetchBalance(ctx, r.llm.APIKey(), balanceBaseURL(r.llm))
	if err != nil {
		return err.Error(), false
	}
	line := formatBalanceLine(resp)
	if line == "" {
		return "", false
	}
	r.ui.ShowBalance(line)
	return line, true
}

// balanceQueryFunc 查询余额的签名，便于测试注入桩实现。
type balanceQueryFunc func() (string, bool)

// balanceFailThreshold 连续失败达到该次数时提示一次原因，避免用户永远不知道余额展示已停止。
const balanceFailThreshold = 3

// queryBalanceWith 用指定的查询函数执行每轮余额查询（生产走 queryBalance，测试注入桩）。
// balanceFailCount 用 atomic 保护：每轮查询在独立 goroutine 运行，可能并发读写。
func (r *Runner) queryBalanceWith(query balanceQueryFunc) {
	if _, ok := query(); !ok {
		if r.balanceFailCount.Add(1) == balanceFailThreshold {
			r.ui.OnMessage(fmt.Sprintf("⚠️ 余额查询连续失败 %d 次，请检查网络或 API key", balanceFailThreshold))
		}
		return
	}
	r.balanceFailCount.Store(0)
}

// balanceBaseURL 获取余额查询使用的 baseURL（复用 OpenAI client 的 BaseURL）。
func balanceBaseURL(c *llm.OpenAIClient) string {
	return c.BaseURL()
}

// handleBalanceCommand 处理 /balance 命令。
// 手动查询余额并展示（每轮已自动更新，此命令用于立即刷新/排查）。
func (r *Runner) handleBalanceCommand() {
	line, ok := r.queryBalance()
	if !ok {
		r.ui.OnMessage(fmt.Sprintf("⚠️ 余额查询失败: %s", line))
	}
}
