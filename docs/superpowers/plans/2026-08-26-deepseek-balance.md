# DeepSeek 账户余额查询 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 每轮对话结束展示 DeepSeek 账户剩余额度，并提供 `/balance` 命令手动查询；查询默认关闭，首次 `/balance` 成功后开启每轮展示。

**Architecture:** 新增 `internal/llm/balance.go` 封装 `GET /user/balance`（host 从 `OPENAI_BASE_URL` 推导，默认官方地址）；`Runner` 加 `showBalance` 开关字段；`query_engine` 在 Final 事件后按开关异步查询；`/balance` 命令同步查询并开启开关；`UI` 接口加 `ShowBalance(line)`，BubbleUI 样式化输出、TextUI 经 OnEvent 转发。失败策略：每轮静默、手动提示。

**Tech Stack:** Go 1.26.4，net/http，现有 go-openai client 的 apiKey/config 复用。

**Spec:** `docs/superpowers/specs/2026-08-26-deepseek-balance-design.md`

---

### Task 1: `internal/llm/balance.go` — 余额查询客户端（含 host 推导）

**Files:**
- Create: `internal/llm/balance.go`
- Test: `internal/llm/balance_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/llm/balance_test.go`：

```go
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
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/llm/ -run 'TestBalanceHost|TestFetchBalance' -v`
Expected: FAIL（`balanceHost` / `FetchBalance` 未定义）

- [ ] **Step 3: 实现 `balance.go`**

创建 `internal/llm/balance.go`：

```go
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
	Currency        string `json:"currency"`         // "CNY" | "USD"
	TotalBalance    string `json:"total_balance"`    // 总可用余额（含赠金与充值）
	GrantedBalance  string `json:"granted_balance"`  // 未过期赠金余额
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
```

- [ ] **Step 4: 运行测试确认通过**

Run: `go test ./internal/llm/ -run 'TestBalanceHost|TestFetchBalance' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/llm/balance.go internal/llm/balance_test.go
git commit -m "feat(llm): DeepSeek 余额查询客户端（/user/balance + host 推导）"
```

---

### Task 2: UI 接口 + 两种实现

**Files:**
- Modify: `internal/ui/ui.go`（接口声明处，`OnMessage` 附近）
- Modify: `internal/ui/bubble/bubble.go`（新增方法，`OnMessage` 附近）
- Modify: `internal/ui/text/text.go`（新增方法，`OnFinal` 之后）
- Test: `internal/ui/text/text_test.go`（新建）

- [ ] **Step 1: UI 接口加 `ShowBalance`**

在 `internal/ui/ui.go` 中 `OnMessage(msg string)` 声明后新增：

```go
	// ShowBalance 展示账户余额（每轮结束或 /balance 命令触发）。
	// line 是已格式化的单行文本，如 "💰 余额: ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）"。
	ShowBalance(line string)
```

- [ ] **Step 2: BubbleUI 实现**

在 `internal/ui/bubble/bubble.go` 的 `OnMessage` 方法后新增：

```go
// ShowBalance 展示账户余额（青色高亮，与普通消息区分）。
func (b *BubbleUI) ShowBalance(line string) {
	fmt.Println(styleCyan.Render(line))
}
```

`styleCyan` 若未定义，先在文件顶部样式区（`styleMuted` 定义附近）添加：

```go
styleCyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
```

- [ ] **Step 3: TextUI 实现 + 测试**

在 `internal/ui/text/text.go` 的 `OnFinal` 方法后新增：

```go
// ShowBalance 转发余额展示事件。data 为 line（string）。
func (t *TextUI) ShowBalance(line string) {
	if t.OnEvent != nil {
		t.OnEvent("balance", line)
	}
}
```

创建 `internal/ui/text/text_test.go`：

```go
package text

import "testing"

func TestShowBalance_ForwardsEvent(t *testing.T) {
	got := map[string]any{}
	ui := NewTextUI()
	ui.OnEvent = func(name string, data any) {
		got["name"] = name
		got["data"] = data
	}

	ui.ShowBalance("💰 余额: ¥110.00")

	if got["name"] != "balance" {
		t.Errorf("event name = %v, want balance", got["name"])
	}
	if got["data"] != "💰 余额: ¥110.00" {
		t.Errorf("event data = %v, want balance line", got["data"])
	}
}
```

- [ ] **Step 4: 编译 + 跑测试**

Run: `go build ./... && go test ./internal/ui/...`
Expected: 编译通过，全部 PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/ui.go internal/ui/bubble/bubble.go internal/ui/text/text.go internal/ui/text/text_test.go
git commit -m "feat(ui): ShowBalance 接口 + Bubble/Text 实现"
```

---

### Task 3: Runner 加 `showBalance` 开关 + `/balance` 命令

**Files:**
- Modify: `internal/agent/runner.go`（struct 字段区 + 命令错误提示字符串）
- Modify: `internal/agent/session.go`（`handleSessionCommand` switch 加 case）
- Modify: `internal/agent/query_engine.go`（Final 事件后异步查询）
- Create: `internal/agent/balance.go`（`queryBalance` + `handleBalanceCommand` + 格式化）
- Test: `internal/agent/balance_test.go`

- [ ] **Step 1: 写失败测试**

创建 `internal/agent/balance_test.go`：

```go
package agent

import "testing"

// 余额行格式化：单货币 CNY。
func TestFormatBalanceLine(t *testing.T) {
	line := formatBalanceLine(true, []llmBalanceInfo{})
	_ = line // 占位：具体断言在实现后补充，见 Task 3 Step 5
}
```

> 注：Task 3 的测试与实现耦合较紧（需要真实 HTTP 服务器注入到 Runner），放在 Step 4 一起写完整断言。

- [ ] **Step 2: 实现格式化 + 查询 helper**

创建 `internal/agent/balance.go`：

```go
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
```

- [ ] **Step 3: `APIKey()` 与 `BaseURL()` 访问器**

在 `internal/llm/openai.go` 的 `Model()` 方法后新增：

```go
// APIKey 返回 API key（余额查询等场景复用）。
func (c *OpenAIClient) APIKey() string { return c.apiKey }

// BaseURL 返回 API Base URL（余额查询 host 推导用）。
func (c *OpenAIClient) BaseURL() string { return c.baseURL }
```

在 `OpenAIClient` struct（`internal/llm/openai.go:43-48`）加字段并初始化：

```go
	apiKey       string // API key（余额查询复用）
	baseURL      string // API Base URL（余额查询 host 推导）
```

`NewOpenAIClientFromEnv` 中 `model := ...` 之前设置：

```go
	baseURLForBalance := ""
	if baseURL != "" {
		baseURLForBalance = baseURL
	}
```

并在返回值处传入：

```go
	return &OpenAIClient{
		client:       openai.NewClientWithConfig(config),
		model:        model,
		apiKey:       apiKey,
		baseURL:      baseURLForBalance,
		contextLimit: contextLimit,
		temperature:  defaultTemperature,
	}, nil
```

- [ ] **Step 4: 接入 Runner + query_engine**

`internal/agent/runner.go`：
- struct 加字段 `showBalance bool // 每轮结束是否展示余额（/balance 成功后开启）`
- 未知命令错误提示字符串末尾追加 `/balance`：
  `"未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory, /reload, /balance"`

`internal/agent/session.go` 的 `handleSessionCommand` switch，`case "/compress"` 前新增：

```go
	// ── /balance ──
	// 手动查询余额；成功后开启每轮结束展示。
	case "/balance":
		r.handleBalanceCommand()
		return 0, true
```

`internal/agent/query_engine.go` 的 `case QueryEventFinal:` 内，`OnFinal` 调用后新增：

```go
			// 每轮结束展示余额（/balance 开启后生效，失败静默）。
			if r.showBalance {
				go r.queryBalance()
			}
```

- [ ] **Step 5: 补全测试 + 跑测试**

重写 `internal/agent/balance_test.go`（覆盖格式化逻辑，不依赖网络）：

```go
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
	if line := formatBalanceLine(&llm.BalanceResponse{IsAvailable: false}); line != "" {
		t.Errorf("unavailable line = %q, want empty", line)
	}
	if line := formatBalanceLine(nil); line != "" {
		t.Errorf("nil line = %q, want empty", line)
	}
}
```

Run: `go build ./... && go test ./internal/agent/ ./internal/llm/ ./internal/ui/...`
Expected: 编译通过，全部 PASS

- [ ] **Step 6: Commit**

```bash
git add internal/agent/balance.go internal/agent/balance_test.go internal/agent/runner.go internal/agent/session.go internal/agent/query_engine.go internal/llm/openai.go
git commit -m "feat(agent): /balance 命令 + 每轮结束余额展示（失败静默）"
```

---

### Task 4: 文档更新 + 全量验证

**Files:**
- Modify: `CLAUDE.md`（REPL 命令表 + 架构说明）
- Modify: `README.md`（如存在命令表）

- [ ] **Step 1: 更新 CLAUDE.md REPL 命令表**

在 `CLAUDE.md` 的 REPL 命令表 `/compress` 行后新增：

```markdown
| `/balance` | 查询 DeepSeek 账户余额；成功后每轮对话结束自动展示剩余额度 |
```

- [ ] **Step 2: 全量测试 + 构建**

Run: `go test ./... && go build -o /tmp/agentic-balance-check .`
Expected: 全部 PASS，构建成功

- [ ] **Step 3: 手动冒烟（可选，需真实 API key）**

Run: `OPENAI_BASE_URL=https://api.deepseek.com go run . -one-shot "hi"` 后输入 `/balance`
Expected: 显示余额行；后续每轮结束显示余额

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md README.md
git commit -m "docs: /balance 命令 + 每轮余额展示说明"
```

---

## Self-Review

**Spec 覆盖：**
- 每轮结束展示余额 → Task 3 Step 4（`query_engine.go` Final 后异步查询）
- `/balance` 手动查询 → Task 3 Step 4（`handleSessionCommand` case）
- 默认关闭 + 首次 /balance 开启 → Task 3 Step 2（`showBalance` 字段 + `handleBalanceCommand` 置 true）
- 每轮失败静默 / 手动提示 → Task 3 Step 2（`queryBalance` 返回 bool，静默；`handleBalanceCommand` 提示）
- host 推导 + 兼容代理 → Task 1（`balanceHost`）
- UI 接口 → Task 2

**类型一致性：**
- `llm.BalanceResponse` / `llm.BalanceInfo` 在 Task 1 定义，Task 3 测试引用一致
- `UI.ShowBalance(line string)` 在 Task 2 定义，Task 3 通过 `r.ui.ShowBalance` 调用
- `OpenAIClient.APIKey()` / `BaseURL()` 在 Task 3 Step 3 定义，Task 3 Step 2 使用

**已知待办（实现时确认）：**
- `styleCyan` 是否已存在于 bubble.go：若已定义则跳过新增（Task 2 Step 2 已注明）
- `llmBalanceInfo` 占位引用：Task 3 Step 1 的占位测试在 Step 5 整体重写时删除
