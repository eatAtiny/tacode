# DeepSeek 账户余额查询设计

日期: 2026-08-26

## 背景

当前 agent 每轮结束仅展示 token 统计（`⚡ 本轮 N tokens`），无法直观看到账户剩余额度。经调研 DeepSeek 官方 API 文档：

- 官方唯一的计费相关接口是 `GET https://api.deepseek.com/user/balance`，返回**账户余额**（总余额 / 充值 / 赠金，CNY/USD）。
- 官方**没有**按请求返回消费明细的接口，因此「每轮花费金额」无法通过接口获取，本项目不做本地金额换算。
- go-openai 库**不内置**该接口，需自行实现 HTTP 调用。

目标：**每轮对话结束展示剩余额度**，并提供 `/balance` 命令手动刷新。

## 需求

1. 每轮对话结束（Final 事件渲染完成后）展示账户余额。
2. `/balance` 命令手动查询余额。
3. 余额查询默认关闭（避免所有用户每轮多一次 HTTP 请求），首次 `/balance` 查询成功后开启每轮展示。
4. 每轮查询失败**静默**（不打扰）；`/balance` 手动查询失败**提示原因**。
5. 兼容代理网关：复用 `OPENAI_BASE_URL` 推导 API host；仅当 host 为官方地址或含 `deepseek` 时自动查询。

## 非目标

- 不做本地金额换算（每轮花费 / 单价 / 缓存命中拆分）。
- 不做余额缓存、不做重试（每轮一次请求，量极小）。
- 不修改 token 统计展示。

## 架构

### 新增文件: `internal/llm/balance.go`（~80 行）

```go
// BalanceInfo 单货币余额信息。
type BalanceInfo struct {
    Currency        string // "CNY" | "USD"
    TotalBalance    string // 总可用余额（含赠金与充值）
    GrantedBalance  string // 未过期赠金余额
    ToppedUpBalance string // 充值余额
}

// BalanceResponse /user/balance 响应。
type BalanceResponse struct {
    IsAvailable  bool          // 账户是否有可用余额
    BalanceInfos []BalanceInfo
}

// FetchBalance 查询账户余额。
// baseURL 为空时使用官方地址 https://api.deepseek.com；
// 否则推导 scheme://host[:port]（剥离 /v1 等路径前缀）。
// 鉴权: Bearer API key。超时 5s。
func FetchBalance(ctx context.Context, apiKey, baseURL string) (*BalanceResponse, error)
```

实现要点：

- host 推导逻辑（与 OpenAI client 的 BaseURL 一致）：
  - `baseURL` 为空 → `https://api.deepseek.com`
  - 非空 → 取 `scheme://host[:port]`，剥离路径（含 `/v1`）
- `GET {host}/user/balance`，Header: `Authorization: Bearer <apiKey>`
- 5s 超时（`http.Client{Timeout: 5 * time.Second}`）
- 错误信息可读：非 200 返回状态码 + body 截断（错误原因如鉴权失败/欠费）
- JSON 解析字段与官方响应一致（`is_available`, `balance_infos[].currency/total_balance/granted_balance/topped_up_balance`）

### 修改 `internal/llm/openai.go`

- 暴露 API key 访问器：`func (c *OpenAIClient) APIKey() string`（当前 key 仅存于 go-openai client 内部，balance 查询需要复用）

### 修改 `internal/ui/ui.go`

- 接口增加 `ShowBalance(line string)`（1 行）

### 修改 `internal/ui/bubble/bubble.go`

- 实现 `ShowBalance`：cyan 样式输出 `💰 余额: ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）`，多货币则逐行输出；不支持的 host 场景由调用方处理
- 金额符号：CNY → `¥`，USD → `$`，其他货币显示原始 code

### 修改 `internal/ui/text/text.go`

- 实现 `ShowBalance`：经 `OnEvent` 转发，`t.OnEvent("balance", line)`

### 修改 `internal/agent/query_engine.go`

- `Runner` 新增字段 `showBalance bool`（默认 false）
- Final 事件渲染完成后：`if r.showBalance { go r.queryBalance() }`
  - 成功 → `r.ui.ShowBalance(...)`
  - 失败 → 静默

### 修改 `internal/agent/runner.go`

- 新增 REPL 命令 `/balance`：
  - 同步调用 `FetchBalance`（复用 `r.llm` 的 API key + baseURL 推导）
  - 成功 → `UI.ShowBalance(...)` 且 `r.showBalance = true`
  - 失败 → `UI.OnMessage("⚠️ 余额查询失败: <原因>")`，不开启每轮展示
- host 非 DeepSeek（不支持自动查询）时：`/balance` 仍可查询（余额接口属于 DeepSeek 官方，代理网关若透传则可用，否则错误提示）

## 数据流

```
用户输入 → queryLoop → Final 事件 → queryEngine:
    showBalance == true → goroutine FetchBalance → 成功: UI.ShowBalance() / 失败: 静默

/balance 输入 → Runner 命令分支:
    FetchBalance → 成功: UI.ShowBalance() + showBalance = true（开启每轮）
                  失败: UI.OnMessage("⚠️ 余额查询失败: <原因>")
```

## 测试

- `internal/llm/balance_test.go`：
  - host 推导：空 baseURL → 官方地址；`https://api.deepseek.com/v1` → 剥离 `/v1`；自定义端口
  - JSON 解析：httptest 模拟 `is_available: true` + 双货币 `balance_infos`，验证字段映射
  - 非 200 错误返回可读错误
- 手动验证：
  - `go run .` → `/balance` → 显示余额，随后每轮结束展示余额
  - 关闭 OPENAI_API_KEY 场景（不适用，启动必需）

## 影响文件清单

| 文件 | 改动 |
|------|------|
| `internal/llm/balance.go` | 新增 |
| `internal/llm/openai.go` | +`APIKey()` 访问器 |
| `internal/ui/ui.go` | +`ShowBalance(line string)` |
| `internal/ui/bubble/bubble.go` | 实现 `ShowBalance` |
| `internal/ui/text/text.go` | 实现 `ShowBalance`（OnEvent 转发） |
| `internal/agent/query_engine.go` | Final 后按 `showBalance` 查询 |
| `internal/agent/runner.go` | `/balance` 命令 + `showBalance` 字段 |
| `internal/agent/query_loop.go` | 无改动 |
