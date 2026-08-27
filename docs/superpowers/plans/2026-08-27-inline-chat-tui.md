# Inline 聊天界面实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将 bubble 聊天 UI 从 alt screen 全屏渲染改为 inline 渲染（活区 + `tea.Println` 定稿），恢复终端原生复制粘贴/滚轮/退出留存。

**Architecture:** ChatModel 删除 viewport，`View()` 只渲染底部活区（状态行 + 权限弹层 + textarea + footer，≤5 行）；对话内容在 Update 各事件分支经 `commit()` 定稿（记入 `m.lines` 转录 + 返回单次 `tea.Println`，多段合并保序）。程序选项删除鼠标捕获，bubbletea v1.3.10 默认 inline 渲染。

**Tech Stack:** Go 1.26、bubbletea v1.3.10（`tea.Println`、默认 bracketed paste）、bubbles v1.0.0（textarea）、lipgloss/glamour。

**Spec:** `docs/superpowers/specs/2026-08-27-inline-chat-tui-design.md`

**关键 API 事实（已对照 v1.3.10 / v1.0.0 源码核实，勿凭记忆改）：**

- `tea.Println(args ...any) Cmd` — 打印在活区上方，内容不受后续重绘影响（定稿机制）
- `tea.Batch` 里的 Cmd **并发执行、不保序** → 一次 Update 的多段定稿必须合并为**单次** `tea.Println`（`strings.Join(parts, "\n")`）
- 没有独立的 Paste 消息类型：bracketed paste 默认开启，粘贴 = `tea.KeyMsg{Type: tea.KeyRunes, Runes, Paste: true}`，textarea 自带多行插入（sanitizer 保留 `\n`）
- `textarea.Model` 有 `InsertString/SetWidth/Value/Reset`，无 viewport 相关依赖

---

### Task 1: 程序选项与 Init（去鼠标捕获、去 alt screen）

**Files:**
- Modify: `internal/ui/bubble/bubble.go:88-89`
- Modify: `internal/ui/bubble/chat.go:183-186`

- [ ] **Step 1: 修改 bubble.go 的 tea.NewProgram 选项**

把（bubble.go:88-89）：

```go
	// 启用鼠标（CellMotion：滚轮/移动事件），让 viewport 对话区支持滚轮滚动历史。
	p := tea.NewProgram(b.chat, append([]tea.ProgramOption{tea.WithMouseCellMotion()}, opts...)...)
```

改为：

```go
	// 不开鼠标捕获、不进 alt screen（inline 渲染）：
	// 终端原生选择/复制/滚轮滚动全部保留，对话经 tea.Println 流入原生 scrollback。
	p := tea.NewProgram(b.chat, opts...)
```

- [ ] **Step 2: 修改 chat.go 的 Init**

把（chat.go:183-186）：

```go
// Init 初始命令：进入 alt screen（全屏渲染），并启动光标闪烁。
func (m *ChatModel) Init() tea.Cmd {
	return tea.Batch(tea.EnterAltScreen, textarea.Blink)
}
```

改为：

```go
// Init 初始命令：启动光标闪烁。
// 不进 alt screen——inline 渲染，对话定稿后留在终端原生 scrollback。
func (m *ChatModel) Init() tea.Cmd {
	return textarea.Blink
}
```

- [ ] **Step 3: 验证编译与既有测试全绿**

Run: `go build ./... && go test ./internal/ui/bubble/`
Expected: 编译通过，所有测试 PASS（选项变更不影响生命周期/欢迎/footer 测试）

- [ ] **Step 4: Commit**

```bash
git add internal/ui/bubble/bubble.go internal/ui/bubble/chat.go
git commit -m "feat(ui): 去除鼠标捕获与 alt screen（inline 渲染基础）"
```

---

### Task 2: ChatModel 去 viewport 化（活区 View + 结构改造）

**Files:**
- Modify: `internal/ui/bubble/chat.go`（结构体、NewChatModel、View、WindowSizeMsg、删 refresh/scrollBottom 及全部调用点）
- Test: `internal/ui/bubble/chat_test.go`

- [ ] **Step 1: 先写失败的测试（View 不含对话内容）**

在 `chat_test.go` 追加：

```go
// 活区 View 不含对话内容（对话经 tea.Println 定稿进 scrollback，View 只有活区）。
func TestChatModel_ViewExcludesConversation(t *testing.T) {
	m := NewChatModel()
	m.Update(chatFinalMsg{content: "答案正文内容", totalTokens: 0})
	if v := m.View(); strings.Contains(v, "答案正文内容") {
		t.Errorf("View 不应包含对话内容（活区只有输入框/footer），实际:\n%s", v)
	}
	// 定稿内容记入转录 m.lines。
	if len(m.lines) != 1 || !strings.Contains(m.lines[0].text, "答案正文内容") {
		t.Errorf("final 内容应记入 m.lines，实际: %+v", m.lines)
	}
}
```

并改造既有用例（viewport 断言 → 活区/转录断言）：

`TestChatModel_View` 不变（仍断言 placeholder + footer）。
`TestChatModel_WindowSize` 改为：

```go
// WindowSizeMsg 更新布局（textarea 宽度；viewport 已删除）。
func TestChatModel_WindowSize(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if m.width != 80 || m.height != 24 {
		t.Errorf("size = %dx%d, want 80x24", m.width, m.height)
	}
	if m.textarea.Width() != 80 {
		t.Errorf("textarea 宽度 = %d, want 80", m.textarea.Width())
	}
}
```

`TestChatModel_Events` 中 `content := m.viewport.View()` 及后续 4 个 `content` 断言改为遍历 `m.lines`：

```go
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "思考中") {
		t.Errorf("转录应含思考行，实际:\n%s", all)
	}
	if !strings.Contains(all, "bold") {
		t.Errorf("转录应含渲染后的 final 文本，实际:\n%s", all)
	}
	if !strings.Contains(all, "150 tokens") {
		t.Errorf("转录应含 token 统计行，实际:\n%s", all)
	}
	if !strings.Contains(all, "Error") {
		t.Errorf("转录应含错误行，实际:\n%s", all)
	}
```

`TestChatModel_WelcomeGotoTop` 替换为：

```go
// 欢迎界面（chatWelcomeMsg）记入转录（首条），无滚动语义。
func TestChatModel_WelcomePrinted(t *testing.T) {
	m := NewChatModel()
	m.Update(chatWelcomeMsg{content: welcomeBanner("deepseek-v4-flash", "v0.1", "/tmp")})
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "agentic") {
		t.Errorf("欢迎行应含 logo，实际: %q", m.lines[0].text)
	}
}
```

`TestChatModel_HistoryGotoBottom` 替换为：

```go
// 历史加载（chatHistoryMsg）清空重建转录，顺序保留。
func TestChatModel_HistoryAppended(t *testing.T) {
	m := NewChatModel()
	var evts []chatHistoryEvent
	for i := 0; i < 30; i++ {
		evts = append(evts, chatHistoryEvent{text: fmt.Sprintf("历史第 %d 行", i)})
	}
	m.Update(chatHistoryMsg{events: evts})
	if len(m.lines) != 30 {
		t.Fatalf("lines = %d, want 30", len(m.lines))
	}
	if !strings.Contains(m.lines[29].text, "历史第 29 行") {
		t.Errorf("末条应为最后一行历史，实际: %q", m.lines[29].text)
	}
}
```

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/ui/bubble/`
Expected: FAIL——`TestChatModel_ViewExcludesConversation`（当前 View 含 viewport 对话区）、`TestChatModel_WindowSize`（textarea.Width 不是 80，编译错 `m.viewport` 已不存在的断言先改造）等

- [ ] **Step 3: 实现——删除 viewport 与滚动辅助**

chat.go 逐项修改：

1. 结构体删除 `viewport viewport.Model` 字段（注释同步删）
2. import 删除 `"github.com/charmbracelet/bubbles/viewport"`
3. `NewChatModel` 删除 `vp := viewport.New(50, 10)` 与 `viewport: vp`
4. `View()` 的 `parts := []string{m.viewport.View()}` 改为：

```go
	parts := []string{}
```

（后续 `permLayer`/`textarea`/`footer` 追加逻辑不变）

5. `WindowSizeMsg` 分支改为：

```go
		case tea.WindowSizeMsg:
			m.width = v.Width
			m.height = v.Height
			m.textarea.SetWidth(v.Width)
```

6. 删除 `scrollBottom()`、`refresh()` 两个方法及**全部**调用点（submit 分支、appendStreaming、think/final/toolcall/toolresult/continue/error/message/welcome/history 各 case 里的 `m.scrollBottom()`、`m.refresh()`、`m.viewport.GotoTop()`、`m.viewport.GotoBottom()`）。各 case 保留 `m.lines = append(...)` 逻辑本身
7. `Update` 中删除 `m.viewport, cmd = m.viewport.Update(msg)` 两行
8. `m.textarea.Height()` 的引用处（原 viewport 高度计算）随第 5 步一并消失

- [ ] **Step 4: 运行包测试确认通过**

Run: `go test ./internal/ui/bubble/`
Expected: PASS（全部用例，含未改造的 Submit/Streaming/Tool/Picker 系列）

- [ ] **Step 5: Commit**

```bash
git add internal/ui/bubble/chat.go internal/ui/bubble/chat_test.go
git commit -m "refactor(ui): ChatModel 去 viewport 化，View 只渲染底部活区"
```

---

### Task 3: 定稿打印管线（commit + tea.Println）

**Files:**
- Modify: `internal/ui/bubble/chat.go`
- Test: `internal/ui/bubble/chat_test.go`（既有断言不变，本任务为行为接线）

**说明:** `tea.Println` 返回的 `tea.Cmd` 是闭包、无法单测；定稿行为的可测面是转录 `m.lines`（Task 2 已覆盖），打印接线经编译 + 全量测试 + 手动验收覆盖。本任务是重构接线任务。

- [ ] **Step 1: 新增 commit 辅助方法**

在 chat.go 追加（`closeStreaming` 附近）：

```go
// commit 定稿若干段：逐段记入转录 m.lines，并返回单次 tea.Println
// （多段合并为一个打印命令——tea.Batch 内的 Cmd 并发执行不保序，
// 单次 Println 用 \n 连接保证段落顺序）。
// 空串段照记（保留段落间空行语义）；整次调用无段时返回 nil。
func (m *ChatModel) commit(parts ...string) tea.Cmd {
	if len(parts) == 0 {
		return nil
	}
	for _, p := range parts {
		m.lines = append(m.lines, chatLine{text: p})
	}
	return tea.Println(strings.Join(parts, "\n"))
}
```

- [ ] **Step 2: 各事件分支改为经 commit 定稿**

chat.go `Update` 内逐个改造（模式：原来的 `m.lines = append(m.lines, chatLine{text: X})` 改为 `cmds = append(cmds, m.commit(X))`）：

1. submit 分支（Enter 提交）：`m.lines = append(m.lines, chatLine{text: m.renderUser(value)})` 改为 `cmds = append(cmds, m.commit(m.renderUser(value)))`
2. `chatFinalMsg`：整个 case 替换为下面的过渡实现（Task 4 会再重构为冲刷模型）。注意被替换的流式行**不能用 commit**（commit 会再追加一条转录，导致双份）——替换用直接赋值，打印用裸 `tea.Println`：

```go
		case chatFinalMsg:
			// 过渡实现：保留「final 原地替换流式行」语义（Task 4 重构为冲刷模型）。
			var text string
			if v.content != "" {
				text = m.renderAssistant() + finalAnswerText(m, v.content)
			}
			if len(m.lines) > 0 && m.lines[len(m.lines)-1].streaming && text != "" {
				m.lines[len(m.lines)-1] = chatLine{text: text}
			} else if text != "" {
				m.lines = append(m.lines, chatLine{text: text})
			}
			if text != "" {
				cmds = append(cmds, tea.Println(text))
			}
			if v.totalTokens > 0 {
				cmds = append(cmds, m.commit(styleMuted.Render(fmt.Sprintf(
					"  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）",
					v.totalTokens, v.inputTokens, v.outputTokens,
				))))
			}
```
3. `chatToolCallMsg`：`m.lines = append(m.lines, chatLine{text: toolCallBox(v.name, v.args)})` → `cmds = append(cmds, m.commit(toolCallBox(v.name, v.args)))`
4. `chatToolResultMsg`：同上，`toolResultBox(v.name, v.result, v.isError)`
5. `chatContinueMsg`：append 行改 commit
6. `chatErrorMsg`：`styleError.Render(...)` 行改 commit
7. `chatMessageMsg`：`v.content` 改 commit
8. `chatWelcomeMsg`：`v.content` 改 commit
9. `chatHistoryMsg`：循环内逐条 append 改为收集 `var parts []string`，循环后 `cmds = append(cmds, m.commit(parts...))`（单次打印整段历史，保序）

- [ ] **Step 3: 运行包测试与编译**

Run: `go build ./... && go test ./internal/ui/bubble/`
Expected: PASS（既有用例对 m.lines 的断言不受影响——commit 同样写入 m.lines）

- [ ] **Step 4: Commit**

```bash
git add internal/ui/bubble/chat.go
git commit -m "feat(ui): 对话内容经 tea.Println 定稿（打印于活区上方入 scrollback）"
```

---

### Task 4: 流式逐段冲刷 + final 不重印

**Files:**
- Modify: `internal/ui/bubble/chat.go`
- Test: `internal/ui/bubble/chat_test.go`

- [ ] **Step 1: 写失败的测试**

替换 `TestChatModel_StreamingMerge` 为以下三个用例：

```go
// 流式增量遇换行定稿：完整段落进 m.lines，残余留 streamBuf。
func TestChatModel_StreamingFlushOnNewline(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "第一段"})
	m.Update(chatDeltaMsg{content: "收尾\n第二段开头"})
	// "…第一段收尾" 已定稿为一条转录。
	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（换行前内容应定稿一条）", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "第一段收尾") {
		t.Errorf("定稿段应含完整段落，实际: %q", m.lines[0].text)
	}
	if !strings.Contains(m.lines[0].text, "助手") {
		t.Errorf("首个流式段应带助手前缀，实际: %q", m.lines[0].text)
	}
	// 残余未定稿。
	if m.streamBuf != "第二段开头" {
		t.Errorf("streamBuf = %q, want 第二段开头", m.streamBuf)
	}

	// final 冲刷残余 + 补 token 行。
	m.Update(chatFinalMsg{content: "第二段开头", totalTokens: 100})
	if m.streamBuf != "" {
		t.Errorf("final 后 streamBuf 应清空，实际: %q", m.streamBuf)
	}
	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if !strings.Contains(all, "第二段开头") || !strings.Contains(all, "100 tokens") {
		t.Errorf("final 应冲刷残余并补统计行，实际:\n%s", all)
	}
}

// final 不重印：有流式内容时 final 不再打印全文（避免双份显示）。
func TestChatModel_FinalNoReprint(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatDeltaMsg{content: "答案内容完毕\n"})
	m.Update(chatFinalMsg{content: "答案内容完毕", totalTokens: 0})

	all := ""
	for _, l := range m.lines {
		all += l.text + "\n"
	}
	if got := strings.Count(all, "答案内容完毕"); got != 1 {
		t.Errorf("答案应只出现 1 次（流式已定稿，final 不重印），实际 %d 次:\n%s", got, all)
	}
}

// 本轮无流式内容（模型直接答）时 final 打印 glamour 渲染全文。
func TestChatModel_FinalWithoutStreamPrintsRendered(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatFinalMsg{content: "**加粗**回答", totalTokens: 0})

	if len(m.lines) != 1 {
		t.Fatalf("lines = %d, want 1（无 token 行）", len(m.lines))
	}
	if !strings.Contains(m.lines[0].text, "加粗") || !strings.Contains(m.lines[0].text, "助手") {
		t.Errorf("final 应打印渲染全文 + 助手前缀，实际: %q", m.lines[0].text)
	}
}
```

注意：`TestChatModel_FinalNoTokens` 在本步骤**必须**改写（think 行仍在转录里，行数断言会踩）：

```go
// totalTokens 为 0（API 未返回 usage）时不应展示 token 统计行。
// 断言末条转录内容而非总行数（think 行在 Task 5 才移除，行数会变）。
func TestChatModel_FinalNoTokens(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.Update(chatFinalMsg{content: "ok", totalTokens: 0})

	last := m.lines[len(m.lines)-1]
	if strings.Contains(last.text, "tokens") {
		t.Errorf("totalTokens=0 不应渲染 token 行，实际: %q", last.text)
	}
	if !strings.Contains(last.text, "ok") {
		t.Errorf("final 行应含回答内容，实际: %q", last.text)
	}
}
```

`TestChatModel_Events`、`TestChatModel_SubmitTwice` 行数断言核对：Events 序列（think+final+error，无 delta）转录仍为 4 条（think/final/token/error，不变）；SubmitTwice `len(m.lines) < 3` 下限断言仍成立（think + 冲刷段 + token = 3 条）。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/ui/bubble/`
Expected: FAIL——`streamBuf` 字段不存在（编译错）驱动实现

- [ ] **Step 3: 实现冲刷模型**

chat.go 修改：

1. 结构体加字段（`lines` 附近）：

```go
	// streamBuf 未定稿的流式缓冲（无换行的增量累积）。
	streamBuf string
	// streamed 自最近一次 think 起是否有流式内容（final 判断是否重印全文）。
	streamed bool
```

2. 删除 `appendStreaming`、`closeStreaming`，替换为：

```go
// appendStreaming 追加流式增量，返回可定稿段落（遇 \n 切段，保序）。
// 本轮首个增量前注入助手前缀（进 streamBuf，随首次冲刷一起定稿）。
func (m *ChatModel) appendStreaming(content string) []string {
	if !m.streamed {
		m.streamBuf += m.renderAssistant()
	}
	m.streamBuf += content
	m.streamed = true
	var segs []string
	for {
		i := strings.IndexByte(m.streamBuf, '\n')
		if i < 0 {
			break
		}
		segs = append(segs, m.streamBuf[:i])
		m.streamBuf = m.streamBuf[i+1:]
	}
	return segs
}

// flushBuf 取出残余缓冲（无残余返回 nil）。
func (m *ChatModel) flushBuf() []string {
	if m.streamBuf == "" {
		return nil
	}
	s := m.streamBuf
	m.streamBuf = ""
	return []string{s}
}
```

3. `chatThinkMsg` 分支加 `m.streamed = false`（保留行追加，Task 5 移除）
4. `chatDeltaMsg` 分支改为：

```go
		case chatDeltaMsg:
			if segs := m.appendStreaming(v.content); len(segs) > 0 {
				cmds = append(cmds, m.commit(segs...))
			}
```

5. `chatFinalMsg` 分支整体替换为：

```go
		case chatFinalMsg:
			// 冲刷残余段落；有流式内容时 final 不重印全文（增量与最终回答同源）。
			parts := m.flushBuf()
			if !m.streamed && v.content != "" {
				parts = append(parts, m.renderAssistant()+finalAnswerText(m, v.content))
			}
			if v.totalTokens > 0 {
				parts = append(parts, styleMuted.Render(fmt.Sprintf(
					"  ⚡ 本轮 %d tokens（输入 %d / 输出 %d）",
					v.totalTokens, v.inputTokens, v.outputTokens,
				)))
			}
			if len(parts) > 0 {
				cmds = append(cmds, m.commit(parts...))
			}
			m.streamed = false
```

6. `chatToolCallMsg` / `chatToolResultMsg` 分支改为（先冲刷再接框线，同一次打印保序）：

```go
		case chatToolCallMsg:
			parts := append(m.flushBuf(), toolCallBox(v.name, v.args))
			cmds = append(cmds, m.commit(parts...))
		case chatToolResultMsg:
			parts := append(m.flushBuf(), toolResultBox(v.name, v.result, v.isError))
			cmds = append(cmds, m.commit(parts...))
```

7. `chatHistoryMsg` 分支开头补 `parts = append(m.flushBuf(), parts...)`（防御性冲刷，正常为空）
8. `chatFinalMsg` 内旧的 `closeStreaming()`/原地替换逻辑全部删除（已被上述模型取代）

- [ ] **Step 4: 运行包测试确认通过**

Run: `go test ./internal/ui/bubble/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/ui/bubble/chat.go internal/ui/bubble/chat_test.go
git commit -m "feat(ui): 流式逐段定稿（遇换行冲刷），final 不重印全文"
```

---

### Task 5: 查询状态行（活区）

**Files:**
- Modify: `internal/ui/bubble/chat.go`
- Test: `internal/ui/bubble/chat_test.go`

- [ ] **Step 1: 写失败的测试**

在 `chat_test.go` 追加：

```go
// 状态行：查询中显示于活区，final/error/message 清空；think 不再进对话区。
func TestChatModel_StatusLine(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	if !strings.Contains(m.View(), "思考中") {
		t.Errorf("活区应显示思考状态，实际:\n%s", m.View())
	}
	if len(m.lines) != 0 {
		t.Errorf("think 不应再进对话区，实际 %d 行", len(m.lines))
	}

	m.Update(chatDeltaMsg{content: "流式文本"})
	if !strings.Contains(m.View(), "输出中") {
		t.Errorf("活区应显示输出状态，实际:\n%s", m.View())
	}

	m.Update(chatToolCallMsg{name: "shell", args: "{}"})
	if !strings.Contains(m.View(), "执行工具: shell") {
		t.Errorf("活区应显示工具执行状态，实际:\n%s", m.View())
	}

	m.Update(chatFinalMsg{content: "答", totalTokens: 0})
	if strings.Contains(m.View(), "执行工具") || strings.Contains(m.View(), "输出中") {
		t.Errorf("final 后状态行应清空，实际:\n%s", m.View())
	}
}

// 新一轮提交重置状态行。
func TestChatModel_StatusResetOnSubmit(t *testing.T) {
	m := NewChatModel()
	m.Update(chatThinkMsg{iteration: 1})
	m.textarea.SetValue("继续")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "思考中") {
		t.Errorf("提交后状态行应重置，实际:\n%s", m.View())
	}
}
```

同时 `TestChatModel_Events` 的行数断言调整：think/continue 行不再进转录（事件序列 think+final+error = **3** 条转录行：final、token、error），`all` 断言去掉 `"思考中"` 一项。

- [ ] **Step 2: 运行测试确认失败**

Run: `go test ./internal/ui/bubble/`
Expected: FAIL（状态行不存在、think 仍进对话区）

- [ ] **Step 3: 实现状态行**

chat.go 修改：

1. 结构体加字段：

```go
	// status 活区状态行文本（查询中显示：思考/输出/工具执行；空=不渲染）。
	status string
```

2. `View()` 的 parts 构造改为（状态行在弹层之上）：

```go
	parts := []string{}
	if m.status != "" {
		parts = append(parts, styleThink.Render(m.status+"▌"))
	}
```

3. submit 分支（Enter 提交内）加 `m.status = ""`
4. `chatThinkMsg` 分支改为（删行追加）：

```go
		case chatThinkMsg:
			m.streamed = false
			m.status = "⏳ 思考中"
```

5. `chatDeltaMsg` 分支加 `m.status = "📝 输出中"`
6. `chatToolCallMsg` 分支加 `m.status = "🔧 执行工具: " + v.name`
7. `chatContinueMsg` 分支改为（删行追加）：

```go
		case chatContinueMsg:
			m.status = fmt.Sprintf("🔄 继续推理 (iter %d)", v.iteration)
```

8. `chatFinalMsg`、`chatErrorMsg`、`chatMessageMsg` 分支各加 `m.status = ""`（兜底清空，含 /stop 的「⏹️ 已停止」消息路径）

- [ ] **Step 4: 运行包测试确认通过**

Run: `go test ./internal/ui/bubble/`
Expected: PASS（`TestChatModel_Events` 行数断言已同步调整）

- [ ] **Step 5: Commit**

```bash
git add internal/ui/bubble/chat.go internal/ui/bubble/chat_test.go
git commit -m "feat(ui): 查询状态行（活区常驻，替代逐条打印的思考/继续提示）"
```

---

### Task 6: 多行粘贴回归测试（无实现改动）

**Files:**
- Test: `internal/ui/bubble/chat_test.go`

- [ ] **Step 1: 写回归测试（预期直接通过）**

```go
// 多行粘贴回归：bracketed paste（v1.3.10 默认开启）以 KeyMsg{Paste:true}
// 到达，textarea 多行插入，换行不触发 Enter 提交。
func TestChatModel_PasteMultiline(t *testing.T) {
	m := NewChatModel()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("第一行\n第二行"), Paste: true})

	if !strings.Contains(m.textarea.Value(), "\n") {
		t.Errorf("多行粘贴应保留换行，实际: %q", m.textarea.Value())
	}
	select {
	case got := <-m.SubmitCh():
		t.Errorf("粘贴不应触发提交，got %q", got)
	default:
	}
}
```

- [ ] **Step 2: 运行测试**

Run: `go test ./internal/ui/bubble/ -run TestChatModel_PasteMultiline -v`
Expected: PASS（已核实 textarea default 分支 → insertRunesFromUserInput 保留 \n）。
**若 FAIL：不要改实现凑测试**——按 systematic-debugging 排查（可能是 Enter 拦截顺序或 sanitize 行为），找到根因再动代码。

- [ ] **Step 3: Commit**

```bash
git add internal/ui/bubble/chat_test.go
git commit -m "test(ui): 多行粘贴回归测试（钉住 v1.3.10 默认 bracketed paste 行为）"
```

---

### Task 7: 文档更新与全量验证

**Files:**
- Modify: `internal/ui/bubble/bubble.go`（包注释 1-29 行）
- Modify: `internal/ui/bubble/chat.go`（包注释 1-11 行）
- Modify: `CLAUDE.md`（UI System 节 + File Map bubble 条目）

- [ ] **Step 1: 更新包注释**

bubble.go 包注释中「主渲染：ChatModel（chat.go）用 viewport + textarea + footer 全屏渲染」等描述改为 inline 模型描述（活区 + tea.Println 定稿、无鼠标捕获、无 alt screen）。chat.go 包注释同步（删 viewport 描述，改为「活区 = 状态行 + 弹层 + textarea + footer；对话经 commit→tea.Println 定稿」）。

- [ ] **Step 2: 更新 CLAUDE.md**

UI System 节的 BubbleUI 描述改为：inline 聊天界面（viewport 已删除）——底部活区（输入框+footer+状态行）原地重绘，对话定稿经 `tea.Println` 流入终端原生 scrollback；无 alt screen、无鼠标捕获（原生选择/复制/滚轮）；流式逐段定稿（遇换行冲刷）。File Map 的 `bubble/chat.go`、`bubble/bubble.go` 条目同步。

- [ ] **Step 3: 全量验证**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 编译、vet、全部测试 PASS

- [ ] **Step 4: 手动验收（需要用户终端，`OPENAI_API_KEY` 需可用）**

Run: `go run .`，按 spec 验收清单逐项检查：
拖选复制 / 滚轮滚 scrollback / 上滚输入框暂时离屏打字回底 / ctrl+c 退出后对话留存 / 流式逐段流出无双份 / 权限弹层 / `/list` 选择器 / `/switch` 历史加载

- [ ] **Step 5: Commit**

```bash
git add CLAUDE.md internal/ui/bubble/bubble.go internal/ui/bubble/chat.go
git commit -m "docs: inline 聊天界面文档更新（CLAUDE.md/包注释）"
```

---

## 自审记录

- **Spec 覆盖**：鼠标捕获删除（Task 1）、alt screen 删除（Task 1）、活区 View（Task 2）、Println 定稿管线（Task 3）、流式逐段冲刷/final 不重印/无流式 glamour（Task 4）、状态行+生命周期（Task 5）、粘贴回归（Task 6）、文档+验收（Task 7）。WindowSizeMsg 的 glamour 宽度跟随：**未入计划**——当前 `finalAnswerText` 仅在无流式 final 使用，收益边际，按 YAGNI 砍掉（spec 该句视为可选，若手动验收发现换行难看再补）。
- **占位符扫描**：无 TBD/TODO；所有代码步骤含完整代码。
- **类型一致性**：`commit(parts ...string) tea.Cmd`、`appendStreaming(content string) []string`、`flushBuf() []string`、字段 `streamBuf/streamed/status` 各任务间一致。
