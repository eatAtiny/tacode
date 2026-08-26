# Bubble Tea 全量渲染 UI 重构 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把终端 UI 从「追加式输出」改为 Claude Code 式渲染模型：对话区在上、常驻输入框在底、底部状态栏（session/model/token/余额）常驻。

**Architecture:** 三阶段渐进。阶段 1 用 ANSI 光标控制在现有追加式输出上维护底部状态栏；阶段 2 引入 tea.Program 渲染输入框常驻；阶段 3 全量 Bubble Tea（对话区滚动 + /list 融合 + 权限弹层）。

**Tech Stack:** Go 1.26.4、bubbletea、lipgloss、glamour。

**Spec:** `docs/superpowers/specs/2026-08-26-bubbletea-ui-design.md`

**分支:** `refactor/bubbletea-ui`

---

## 阶段 1：底部状态栏常驻（ANSI 光标控制）

### Task 1: StatusModel 加余额字段 + 渲染

**Files:**
- Modify: `internal/ui/components/status.go`
- Test: `internal/ui/components/status_test.go`（新建）

- [ ] **Step 1: 写失败测试**

创建 `internal/ui/components/status_test.go`：

```go
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/components/ -run TestStatusModel -v`
Expected: FAIL（`SetBalance` 未定义）

- [ ] **Step 3: 实现**

`internal/ui/components/status.go`：
- struct 加字段 `balance string // 账户余额展示文本（空=不显示）`
- 加方法：

```go
// SetBalance 设置账户余额展示文本（空字符串表示不显示）。
func (m *StatusModel) SetBalance(balance string) {
	m.balance = balance
}
```

- `View()` 的 `tokenText` 后追加余额段（`balance` 非空时）：

```go
	// 余额段：非空时追加。
	right := tokenText
	if m.balance != "" {
		right += sepStyle.Render(" │ ") + m.balance
	}
```

并把 `bar := statusStyle.Render(left + tokenText)` 改为 `bar := statusStyle.Render(left + right)`。

- 更新 `View()` 顶部注释的格式说明（加余额段）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/ui/components/ -run TestStatusModel -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/components/status.go internal/ui/components/status_test.go
git commit -m "feat(ui): StatusModel 加余额字段（SetBalance + 渲染）"
```

### Task 2: BubbleUI 维护底部状态栏（阶段 1 核心）

**Files:**
- Modify: `internal/ui/bubble/bubble.go`
- Modify: `internal/ui/bubble/bubble.go`（StatusBar 渲染辅助）
- Test: `internal/ui/bubble/statusbar_test.go`（新建）

- [ ] **Step 1: 写失败测试**

创建 `internal/ui/bubble/statusbar_test.go`（捕获 stdout 验证状态栏渲染）：

```go
package bubble

import (
	"os"
	"strings"
	"testing"
)

// renderStatusBar 输出的状态栏文本应包含 session/model/token 信息。
func TestRenderStatusBar(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("deepseek-v4-flash")
	b.UpdateTokens(120, 45)

	line := b.statusBarText()
	for _, want := range []string{"session: demo", "deepseek-v4-flash", "↑ 120", "↓ 45"} {
		if !strings.Contains(line, want) {
			t.Errorf("statusBarText 缺少 %q，实际: %q", want, line)
		}
	}
}

// 余额设置后状态栏包含余额文本。
func TestRenderStatusBar_Balance(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")
	b.SetBalanceText("💰 ¥110.00")

	if !strings.Contains(b.statusBarText(), "💰 ¥110.00") {
		t.Errorf("statusBarText 应包含余额，实际: %q", b.statusBarText())
	}
}

// 辅助：stderr/stdout 捕获（阶段 1 的渲染辅助是纯文本函数，不真正写终端）。
func captureStdout(f func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	f()
	w.Close()
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}
```

> 注：阶段 1 的状态栏渲染辅助设计为纯文本函数 `statusBarText()`（返回 lipgloss 渲染后的单行文本），`renderStatusBar()` 负责真正写终端（`fmt.Println`）。测试只测 `statusBarText()`，避免终端 IO 复杂度。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/bubble/ -run TestRenderStatusBar -v`
Expected: FAIL（`SetBalanceText`/`statusBarText` 未定义）

- [ ] **Step 3: 实现**

`internal/ui/bubble/bubble.go`：

a) struct 加字段：

```go
	// balanceText 账户余额展示文本（/balance 成功后由 Runner 设置）
	balanceText string
	// statusVisible 状态栏是否启用（阶段 1：默认启用；可通过 SetStatusBarVisible 控制）
	statusVisible bool
```

b) `NewBubbleUI` 中 `statusVisible: true`。

c) 加方法：

```go
// SetBalanceText 设置账户余额展示文本（空=不显示余额段）。
func (b *BubbleUI) SetBalanceText(text string) {
	b.balanceText = text
}

// SetStatusBarVisible 控制状态栏是否渲染（阶段 3 全量模式接管后此开关用于过渡）。
func (b *BubbleUI) SetStatusBarVisible(visible bool) {
	b.statusVisible = visible
}

// statusBarText 渲染状态栏单行文本（供测试与 renderStatusBar 共用）。
// 复用 components.StatusModel 的渲染逻辑：同步 session/model/token/余额后调 View()。
func (b *BubbleUI) statusBarText() string {
	if !b.statusVisible {
		return ""
	}
	st := b.status
	st.SetSession(b.sessionName)
	st.SetModel(b.model)
	st.SetTokens(b.inputTokens, b.outputTokens)
	st.SetBalance(b.balanceText)
	return st.View()
}
```

d) 加状态栏维护辅助（在输出点前后调用）：

```go
// clearStatusBar 清除状态栏所在行（将光标移到该行并清空）。
// 状态栏渲染在输入提示符上方一行，追加输出前需先清除，输出完再重绘。
func (b *BubbleUI) clearStatusBar() {
	if !b.statusVisible {
		return
	}
	// 状态栏行在最后一行上方：光标上移 1 行、清行、下移回来。
	fmt.Print("\033[1A\033[2K\r")
	os.Stdout.Sync()
}

// renderStatusBar 在输入提示符上方渲染状态栏。
// 前提：调用时光标位于输入提示符行首。
func (b *BubbleUI) renderStatusBar() {
	if !b.statusVisible {
		return
	}
	line := b.statusBarText()
	if line == "" {
		return
	}
	// 上移一行（输入提示符上一行）、输出状态栏、下移回输入行。
	fmt.Printf("\033[1A%s\n", line)
	os.Stdout.Sync()
}
```

e) 在 `Run()` 的输出路径接入：由于 `Run()` 在 runner.go（agent 包）里调用 UI 方法，BubbleUI 侧需在以下输出点维护状态栏：
- `OnThink`：输出前 `clearStatusBar()`；`OnFinal`/`OnMessage` 等输出后 `renderStatusBar()`
- 具体：`OnThink` 开头 `b.clearStatusBar()`；`OnFinal` 末尾（token 统计后）`b.renderStatusBar()`；`OnMessage` 末尾 `b.renderStatusBar()`；`OnError` 末尾 `b.renderStatusBar()`
- 说明：阶段 1 中 `renderStatusBar()` 假设光标在输入提示符行首——对追加式输出（内容一路往下打，最后一行是提示符）成立。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/ui/bubble/ -run TestRenderStatusBar -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add internal/ui/bubble/bubble.go internal/ui/bubble/statusbar_test.go
git commit -m "feat(ui): BubbleUI 底部状态栏常驻（ANSI 光标维护）"
```

### Task 3: Runner 接线余额到状态栏

**Files:**
- Modify: `internal/agent/query_engine.go`（Final 后余额展示改为 SetBalanceText）
- Modify: `internal/agent/balance.go`（queryBalance 成功时同步状态栏）
- Modify: `internal/ui/bubble/bubble.go`（`ShowBalance` 改为更新状态栏 + 重绘）

- [ ] **Step 1: ShowBalance 改为状态栏更新**

`internal/ui/bubble/bubble.go` 的 `ShowBalance` 改为：

```go
// ShowBalance 展示账户余额（阶段 1：更新状态栏并重绘）。
// line 是已格式化的余额文本，如 "💰 ¥110.00（充值 ¥100.00 / 赠金 ¥10.00）"。
func (b *BubbleUI) ShowBalance(line string) {
	b.SetBalanceText(line)
	if b.statusVisible {
		b.clearStatusBar()
		b.renderStatusBar()
	}
}
```

> 说明：阶段 1 状态栏常驻后，`ShowBalance` 不再单独打印一行，而是把余额塞进状态栏。每轮 `queryBalance` 成功后调用 `ShowBalance` → 更新状态栏余额段。TextUI 的 `ShowBalance` 不变（OnEvent 转发）。

- [ ] **Step 2: 测试**

`internal/ui/bubble/statusbar_test.go` 追加：

```go
// ShowBalance 更新状态栏余额段（不单独打印）。
func TestShowBalance_UpdatesStatusBar(t *testing.T) {
	b := NewBubbleUI()
	b.SetSessionName("demo")
	b.SetModel("m")

	b.ShowBalance("💰 ¥110.00")
	if b.balanceText != "💰 ¥110.00" {
		t.Errorf("balanceText = %q, want 已设置", b.balanceText)
	}
	if !strings.Contains(b.statusBarText(), "💰 ¥110.00") {
		t.Errorf("状态栏应含余额，实际: %q", b.statusBarText())
	}
}
```

- [ ] **Step 3: 跑测试 + 全量验证**

Run: `go test ./internal/ui/... ./internal/agent/ && go build ./...`
Expected: PASS（现有 balance_test 中依赖 ShowBalance 的断言需检查——阶段 1 后 ShowBalance 不再打印，相关测试若断言 stdout 需更新。检查 `TestQueryBalancePerRound_ThresholdHint` 用的是 TextUI，不受影响）

- [ ] **Step 4: Commit**

```bash
git add internal/ui/bubble/bubble.go internal/ui/bubble/statusbar_test.go
git commit -m "feat(ui): ShowBalance 改为更新状态栏（每轮余额常驻）"
```

### Task 4: 阶段 1 全量验证

- [ ] **Step 1: 全量测试**

Run: `go test ./... && go build -o /tmp/agentic-ui-phase1 .`
Expected: 全部 PASS + 构建成功

- [ ] **Step 2: 手动冒烟（可选，需 API key）**

`go run .` → 观察：启动后状态栏常驻底部（session/model/token）；对话输出时状态栏被清除重绘；`/balance` 后余额出现在状态栏；每轮结束 token 更新。

- [ ] **Step 3: 更新文档**

`CLAUDE.md` 的 UI System 章节加一句：阶段 1 后 BubbleUI 底部有常驻状态栏（session/model/token/余额）。

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: 底部状态栏常驻说明"
```

---

## 阶段 2：输入框常驻（textinput 接管）

> 阶段 2 引入 tea.Program 渲染输入区。此阶段是架构切换的关键，改动集中在 bubble.go 与 runner.go。**在开始前需确认 tea.Program 与现有 rawInputLoop 的终端控制协调策略**（见 spec 风险表）。

### Task 5: 引入 tea.Program 渲染输入框（对话区仍追加式）

**Files:**
- Modify: `internal/ui/bubble/bubble.go`（新增 TeaInputModel、View 拼接输入框）
- Modify: `internal/agent/runner.go`（Run() 的输入读取从 ReadInputChan 改为 tea 消息驱动）
- Test: `internal/ui/bubble/teainput_test.go`（新建）

> **注意**：Task 5 是阶段 2 的架构核心，涉及主循环改造。实现前需仔细阅读现有 `rawInputLoop`/`ReadInputChan` 与 `runner.go` 的 select 循环，确定：tea.Program 接管后输入如何进入 Runner（建议：tea 消息 → channel → 复用现有 inputCh 语义）。若发现改动面超出预期，及时 BLOCKED 上报，由 controller 决策是否拆分。

- [ ] **Step 1: 写失败测试**

创建 `internal/ui/bubble/teainput_test.go`：

```go
package bubble

import (
	"strings"
	"testing"
)

// TeaInputModel 的 View 应包含输入框（> 提示符 + 占位符）。
func TestTeaInputModel_View(t *testing.T) {
	m := newTeaInputModel()
	v := m.View()
	if !strings.Contains(v, ">") || !strings.Contains(v, "输入任务开始对话") {
		t.Errorf("View 应包含输入提示符和占位符，实际: %q", v)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/ui/bubble/ -run TestTeaInputModel -v`
Expected: FAIL（`newTeaInputModel` 未定义）

- [ ] **Step 3: 实现（骨架）**

`internal/ui/bubble/bubble.go` 新增：

```go
// ──────────────────────────────────────────────────────────
// 阶段 2：Bubble Tea 输入框模型
// ──────────────────────────────────────────────────────────

// teaInputMsg 输入框提交消息（用户按下 Enter）。
type teaInputMsg struct {
	value string
}

// teaInputModel 常驻输入框的 Bubble Tea 模型（阶段 2 起启用）。
type teaInputModel struct {
	// input 复用 components.InputModel（textinput 封装）。
	input components.InputModel
	// width 终端宽度（用于输入框宽度自适应）。
	width int
}

// newTeaInputModel 创建输入框模型。
func newTeaInputModel() *teaInputModel {
	return &teaInputModel{
		input: components.NewInputModel(),
	}
}

// Init 返回输入框初始命令（光标闪烁）。
func (m *teaInputModel) Init() tea.Cmd {
	return m.input.Init()
}

// Update 处理按键消息：回车提交、其他转发给 textinput。
func (m *teaInputModel) Update(msg tea.Msg) (tea.Cmd, error) {
	switch v := msg.(type) {
	case tea.KeyMsg:
		if v.Type == tea.KeyEnter {
			value := m.input.Value()
			if value == "" {
				return nil, nil // 空输入不提交
			}
			m.input.SetValue("")
			return func() tea.Msg { return teaInputMsg{value: value} }, nil
		}
	case tea.WindowSizeMsg:
		m.width = v.Width
		m.input.SetWidth(v.Width)
	}
	var cmd tea.Cmd
	m.input.Update(msg) // 转给 textinput
	return cmd, nil
}

// View 渲染输入框（分隔线 + > 提示符 + 输入区）。
func (m *teaInputModel) View() string {
	m.input.SetWidth(m.width)
	return m.input.View()
}
```

> **实现提示**：上述骨架依赖 `components.InputModel` 的 `Update` 签名与 tea.Msg 兼容。`components.InputModel.Update(msg tea.Msg) (InputModel, tea.Cmd)` 返回值是值类型，需在 teaInputModel 内持值调用并回写。若 `textinput.Model.Update` 返回的 cmd 处理复杂，可将 `input` 改为值拷贝调用。以实际编译为准微调。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/ui/bubble/ -run TestTeaInputModel -v && go build ./...`
Expected: PASS + 编译通过

- [ ] **Step 5: Commit**

```bash
git add internal/ui/bubble/bubble.go internal/ui/bubble/teainput_test.go
git commit -m "feat(ui): 阶段2 引入 tea 输入框模型（textinput 接管骨架）"
```

### Task 6: runner.go 适配 tea.Program 输入驱动

> **依赖 Task 5**。此任务把 `Run()` 的输入读取从 `ReadInputChan` 切换到 tea 消息驱动。**风险**：rawInputLoop 的终端控制（MakeRaw/OPOST）与 tea.Program 冲突——需协调（方案：阶段 2 输入仍走 rawInputLoop，tea.Program 只做渲染层，输入通过 channel 桥接）。

- [ ] **Step 1: 明确输入桥接方案**

阅读 `runner.go` 的 `Run()` select 循环与 `bubble.go` 的 `ReadInputChan`/`rawInputLoop`。实现输入桥接：tea.Program 的 `Update` 收到 `teaInputMsg` 后，写入现有 `inputChan`（复用 `ReadInputChan` 语义），Runner 的 select 循环不变。**关键**：tea.Program 只负责渲染输入框，不直接接管 stdin（rawInputLoop 继续从 stdin 读真实按键并回显）。

> 这是阶段 2 的核心决策，若实现中发现 tea.Program 渲染与 rawInputLoop 回显冲突（双写终端），需上报 BLOCKED，controller 决定是「tea 全接管输入」还是「保持 raw 输入 + tea 渲染」。

- [ ] **Step 2: 实现**

`bubble.go`：
- `NewBubbleUI` 创建 `tea.Program`（挂 `teaInputModel`）
- `View()`（阶段 2 对话区仍是追加式，tea 只渲染输入框）
- 提供 `SubmitInput(value string)`：把用户提交写入 inputChan（供 teaInputMsg 触发）

`runner.go`：
- `Run()` 中 `tea.Run()` 启动渲染循环（goroutine）；select 循环继续监听 inputCh

- [ ] **Step 3: 测试 + 构建**

Run: `go build ./... && go test ./internal/ui/... ./internal/agent/`
Expected: 编译通过 + 既有测试不回归

- [ ] **Step 4: Commit**

```bash
git add internal/ui/bubble/bubble.go internal/agent/runner.go
git commit -m "feat(ui): 阶段2 runner 接入 tea 渲染循环（输入桥接）"
```

---

## 阶段 3：全量 Bubble Tea（对话区 + 选择器融合）

> 阶段 3 涉及 queryEngine 事件消费改为消息驱动、/list 视图融合、权限确认弹层。**改动面大，需在阶段 2 完成后进一步评估**。此部分计划为框架级，具体代码在阶段 2 完成后细化。

### Task 7: 对话区接入 ConversationModel

- [ ] **Step 1**: ConversationModel 接线：queryEngine 事件（think/delta/final/tool）→ ConversationModel.Add* 方法
- [ ] **Step 2**: 全量 View() = ConversationModel + 输入框 + 状态栏
- [ ] **Step 3**: 测试：mock LLM 驱动真实 queryEngine → 断言 View 含对话内容
- [ ] **Step 4**: Commit

### Task 8: /list 选择器融合主循环

- [ ] **Step 1**: 视图状态机（对话视图 / 选择器视图）
- [ ] **Step 2**: `handleListCommand` 改为切换视图（不再另起 tea.Program）
- [ ] **Step 3**: 测试：视图切换
- [ ] **Step 4**: Commit

### Task 9: 权限确认弹层 + /stop 适配

- [ ] **Step 1**: ToolViewModel.renderConfirm 接入渲染循环（y/N → PermissionMsg）
- [ ] **Step 2**: /stop 消息路径（StopMsg → queryCancel）
- [ ] **Step 3**: 测试 + 全量回归
- [ ] **Step 4**: Commit

### Task 10: 阶段 3 全量验证 + 文档

- [ ] **Step 1**: `go test ./... && go test -race ./internal/...`
- [ ] **Step 2**: 手动冒烟：完整交互（对话/工具/权限/会话切换/余额常驻）
- [ ] **Step 3**: 更新 CLAUDE.md（UI System 章节全量描述）
- [ ] **Step 4**: Commit

---

## Self-Review

**Spec 覆盖：**
- 阶段 1（状态栏常驻）→ Task 1/2/3/4
- 阶段 2（输入框常驻）→ Task 5/6
- 阶段 3（全量 Bubble Tea）→ Task 7/8/9/10
- 余额进状态栏 → Task 1（SetBalance）+ Task 3（ShowBalance 改状态栏）
- /list 融合 → Task 8
- 权限确认 → Task 9
- 保留 /stop 语义 → Task 9

**类型一致性：**
- `StatusModel.SetBalance(string)` 在 Task 1 定义，Task 2 的 `statusBarText` 使用
- `BubbleUI.SetBalanceText/statusBarText/clearStatusBar/renderStatusBar` 在 Task 2 定义，Task 3 使用
- `teaInputModel` 在 Task 5 定义，Task 6 使用

**已知风险（实现时上报 BLOCKED）：**
- Task 6 的 tea.Program 与 rawInputLoop 终端控制冲突（阶段 2 核心决策）
- Task 3 的 `ShowBalance` 行为变更可能影响既有测试（需检查断言）
- Task 7-10 阶段 3 代码需在阶段 2 完成后细化
