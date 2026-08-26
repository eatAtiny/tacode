package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BalanceInfo 单货币余额信息。
type BalanceInfo struct {
	Currency        string `json:"currency"`          // "CNY" | "USD"
	TotalBalance    string `json:"total_balance"`     // 总可用余额（含赠金与充值）
	GrantedBalance  string `json:"granted_balance"`   // 未过期赠金余额
	ToppedUpBalance string `json:"topped_up_balance"` // 充值余额
}

// BalanceResponse /user/balance 响应。
type BalanceResponse struct {
	IsAvailable  bool          `json:"is_available"` // 账户是否有可用余额
	BalanceInfos []BalanceInfo `json:"balance_infos"`
}

// balanceClient 余额查询专用 HTTP 客户端（5s 超时）。
var balanceClient = &http.Client{Timeout: 5 * time.Second}

// balanceHost 从 OPENAI_BASE_URL 推导余额接口 host。
// 规则：仅保留 scheme://host[:port]，剥离路径（含 /v1）；
// baseURL 为空时使用 DeepSeek 官方地址。
func balanceHost(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return "https://api.deepseek.com"
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "https://api.deepseek.com"
	}
	return u.Scheme + "://" + u.Host
}

// FetchBalance 查询 DeepSeek 账户余额。
// baseURL 为空时使用官方地址；否则复用 OPENAI_BASE_URL 推导的 host。
// 鉴权：Bearer API key。非 200 返回可读错误。
func FetchBalance(ctx context.Context, apiKey, baseURL string) (*BalanceResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		balanceHost(baseURL)+"/user/balance", nil)
	if err != nil {
		return nil, fmt.Errorf("build balance request failed: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := balanceClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("balance request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB 上限
	if err != nil {
		return nil, fmt.Errorf("read balance response failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("balance query failed: status=%d body=%s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var result BalanceResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse balance response failed: %w", err)
	}
	return &result, nil
}
