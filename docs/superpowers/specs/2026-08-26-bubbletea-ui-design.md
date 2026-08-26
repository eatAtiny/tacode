# Bubble Tea 全量渲染 UI 重构设计

日期: 2026-08-26
分支: refactor/bubbletea-ui

## 背景与目标

当前终端 UI（BubbleUI）是「追加式输出」：主循环 `bufio.Scanner`/raw mode 逐行读输入，`fmt.Println` 一路往下打印，`> ` 提示符固定在终端最底部。流式渲染（OnThink/OnDelta/OnFinal 的光标控制）与输入提示符是两个互不相干的层。

目标：改为 **Claude Code 式渲染模型** —— 对话区在上、**常驻输入框在底**，流式渲染与输入框同时存在、互不干扰；底部状态栏常驻展示 session / model / token 统计 / 账户余额。

## 非目标

- 不改 TextUI（headless 模式继续走 OnEvent 回调）
- 不改 agent 核心逻辑（queryLoop / compactor / memory 等），只改 UI 渲染与 Runner↔UI 交互方式
- 不做 Web/桌面端 UI

## 需求（已确认）

1. **全量切换 Bubble Tea 渲染模型**：主循环从 select 循环改为 tea.Program 消息驱动，`View()` 每帧重绘整屏（对话区 + 输入框 + 状态栏）。
2. **输入框常驻**：textinput 接管输入（保留中文多字节退格处理能力），常驻在对话区下方，流式渲染时不动。
3. **底部状态栏**：完整展示 `session | model | ↑token ↓token | 💰余额`，每轮刷新，余额常驻。
4. **查询交互保留现有语义**：`/stop` 取消当前查询；查询运行时输入转发给权限确认；不引入输入队列。
5. **/list 会话选择器融入主循环**：不再另起独立 Bubble Tea 程序，作为主渲染循环的内部视图。
6. **权限确认**：主渲染循环内弹确认层（ToolViewModel 的 renderConfirm），y/N 走消息。
7. **分阶段渐进实施**：三阶段，每阶段独立提交、可验证、可回退。

## 架构

```
main.go → runner.Run() 创建 tea.Program（替代现有 select 主循环）
                │ msg 流
                ▼
        TeaModel（新增，bubble.go 内）
         ├─ ConversationModel ── 对话区（滚动视口）
         ├─ InputModel ──────── 常驻输入框（textinput）
         ├─ StatusModel ─────── 底部状态栏（session/model/token/余额）
         └─ ToolViewModel ───── 工具调用弹窗 + 权限确认层
```

### 消息流

- **用户输入** → `tea.KeyMsg` → Update 更新 textinput → Enter 提交 → 包装为 `UserInputMsg` 发给 Runner
- **queryEngine 事件** → 包装为 `QueryEventMsg{Type, Content, ...}` → Update 更新 ConversationModel → 触发重绘
- **异步结果** → `tea.Cmd`（goroutine 返回消息）注入
- **权限确认** → `PermissionMsg{Allow bool}` 写回 channel，queryLoop 解除阻塞

### 阶段拆解

#### 阶段 1: 底部状态栏常驻（轻量，ANSI 光标控制）

- **不动主循环架构**。用 ANSI 光标控制把 `StatusModel`（session/model/token/余额）固定渲染在终端底部区域，每轮刷新。
- BubbleUI 维护「状态栏占位」：在最后一行（输入提示符上方）渲染状态栏；追加输出时先清除状态栏区域、输出内容、再重绘状态栏。
- **验证点**：余额/token 常驻可见，追加式输出不受影响，输入提示符仍最底。

改动文件：
- `internal/ui/components/status.go`：+`SetBalance(string)`、+余额渲染（`💰 ¥110.00`）
- `internal/ui/bubble/bubble.go`：状态栏渲染/清除逻辑（`renderStatusBar()` / `clearStatusBar()`），在 OnThink/OnFinal/OnMessage 等输出点前后维护
- `internal/agent/query_engine.go`：Final 后更新余额到状态栏（替代现在的 ShowBalance 单行打印，或并存）

#### 阶段 2: 输入框常驻（textinput 接管）

- 引入 tea.Program 渲染输入区；对话区仍追加式输出。
- `InputModel` 接入真实使用（textinput），输入框常驻，流式输出时不动。
- **验证点**：流式输出时输入框纹丝不动，输入/退格/中文正常。

改动文件：
- `internal/ui/bubble/bubble.go`：主渲染循环切换为 tea.Program；`View()` = 对话区（当前输出）+ 输入框
- `internal/ui/components/input.go`：可能需要微调（宽度自适应、占位文案）
- `internal/agent/runner.go`：`Run()` 适配 tea.Program 消息循环（读输入从 `ReadInputChan` 改为 tea msg）

#### 阶段 3: 全量 Bubble Tea（对话区 + 选择器融合）

- 对话区接入 `ConversationModel`（滚动视口，AddUserInput/AddDelta/AddFinal 等已有方法全接上）。
- `/list` 选择器作为主循环内部视图（复用 `RunSessionPicker` 的模型逻辑，内嵌为视图切换）。
- 权限确认在渲染循环内弹层；`/stop` 适配。
- **验证点**：完整 Claude Code 式 UI；所有功能回归（会话管理、工具调用、记忆、压缩、余额）。

改动文件：
- `internal/ui/bubble/bubble.go`：TeaModel 完整化（视图状态机：对话视图 / 选择器视图 / 确认层）
- `internal/ui/components/conversation.go`：接入真实使用（AddUserInput/AddFinal 等接线）
- `internal/agent/runner.go` / `query_engine.go`：事件消费改为发消息；权限确认改为消息
- `internal/agent/session.go`：`handleListCommand` 改为切换视图而非独立程序

### Runner↔UI 交互反转

现状：Runner select 循环直接调 `b.ui.OnXxx()`（同步阻塞式）。
改造：queryEngine 事件消费不再直接调 UI 方法，而是通过 channel 把事件发往 TeaModel（或保留 UI 接口但由 TeaModel 内部转发）。`Run()` 变为：
1. 创建 tea.Program（传入 UI 消息 channel）
2. tea.Run() 驱动消息循环
3. 查询结果通过 tea.Cmd 回传

## 风险点与对策

| 风险 | 对策 |
|------|------|
| raw mode 与 Bubble Tea 终端接管冲突 | 阶段 2 引入 tea.Program 时，raw mode 只保留输入侧；Bubble Tea 自带终端控制，需协调 MakeRaw 时机 |
| `/stop` 阻塞语义 | 保留 queryCancel 路径，通过消息传递 `StopMsg` |
| 权限确认阻塞 | 确认层走消息循环（非阻塞轮询 channel），保持 queryLoop 的 channel 语义 |
| glamour 渲染时机 | ConversationModel 已有 glamour，AddFinal 时渲染，View 内直接拼接 |
| 会话切换视图刷新 | 切换时重置 ConversationModel + 更新 StatusModel |
| TextUI 回归 | 不动 TextUI 路径，headless 测试覆盖 |

## 测试计划

- **阶段 1**：status 组件单测（SetBalance/渲染格式）；BubbleUI 状态栏渲染逻辑（可用捕获 stdout 的测试）
- **阶段 2**：InputModel 单测（输入/退格/中文）；tea.Program 冒烟（模拟 KeyMsg）
- **阶段 3**：ConversationModel 接线集成测试（mock LLM 驱动真实 queryEngine → 断言 View 含对话内容）；/list 视图切换测试；全量 `go test ./...` + `go test -race ./internal/...`
- 手动冒烟：`go run .` 完整交互（对话/工具/权限/会话切换/余额常驻）

## 影响文件清单

| 文件 | 改动 |
|------|------|
| `internal/ui/bubble/bubble.go` | 核心重写：TeaModel、Update/View、消息类型、状态栏渲染 |
| `internal/ui/components/status.go` | +`SetBalance`、余额渲染 |
| `internal/ui/components/conversation.go` | 接线真实使用（阶段 3） |
| `internal/ui/components/input.go` | 微调（阶段 2） |
| `internal/ui/components/toolview.go` | 确认层复用（阶段 3） |
| `internal/ui/ui.go` | 可能微调接口 |
| `internal/agent/runner.go` | `Run()` 适配 tea.Program（阶段 2/3） |
| `internal/agent/query_engine.go` | 事件消费改发消息（阶段 3） |
| `internal/agent/session.go` | `/list` 视图切换（阶段 3） |
| `internal/ui/text/text.go` | 无改动 |
