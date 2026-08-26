package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBalanceHost(t *testing.T) {
	cases := []struct {
		baseURL string
		want    string
	}{
		{"", "https://api.deepseek.com"},
		{"https://api.deepseek.com", "https://api.deepseek.com"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com"},
		{"http://localhost:8080", "http://localhost:8080"},
		{"http://localhost:8080/v1", "http://localhost:8080"},
	}
	for _, c := range cases {
		got := balanceHost(c.baseURL)
		if got != c.want {
			t.Errorf("balanceHost(%q) = %q, want %q", c.baseURL, got, c.want)
		}
	}
}

func TestFetchBalance_JSON(t *testing.T) {
	// 官方响应示例：双货币。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/balance" {
			t.Errorf("path = %q, want /user/balance", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"is_available": true,
			"balance_infos": []map[string]any{
				{"currency": "CNY", "total_balance": "110.00", "granted_balance": "10.00", "topped_up_balance": "100.00"},
				{"currency": "USD", "total_balance": "5.00", "granted_balance": "0.00", "topped_up_balance": "5.00"},
			},
		})
	}))
	defer srv.Close()

	resp, err := FetchBalance(context.Background(), "test-key", srv.URL)
	if err != nil {
		t.Fatalf("FetchBalance failed: %v", err)
	}
	if !resp.IsAvailable {
		t.Error("IsAvailable = false, want true")
	}
	if len(resp.BalanceInfos) != 2 {
		t.Fatalf("BalanceInfos len = %d, want 2", len(resp.BalanceInfos))
	}
	cny := resp.BalanceInfos[0]
	if cny.Currency != "CNY" || cny.TotalBalance != "110.00" || cny.GrantedBalance != "10.00" || cny.ToppedUpBalance != "100.00" {
		t.Errorf("CNY balance = %+v, want 110.00/10.00/100.00", cny)
	}
}

func TestFetchBalance_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := FetchBalance(context.Background(), "bad-key", srv.URL)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if len(msg) == 0 {
		t.Error("error message empty")
	}
}
