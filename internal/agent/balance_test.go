package agent

import (
	"testing"

	"agentic/internal/llm"
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
