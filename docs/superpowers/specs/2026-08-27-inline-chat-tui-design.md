# Inline 聊天界面设计（复制粘贴修复 · 方案 C）

- 日期：2026-08-27
- 分支：`feature/chat-tui-inline`（自 `feature/chat-tui-redesign` 切出）
- 状态：已评审通过，待实现

## 背景与动机

`feature/chat-tui-redesign` 的全 tea 聊天界面（viewport + textarea + footer，alt screen
全屏渲染）存在复制粘贴不可用的问题，根因有二：

1. **鼠标捕获劫持文本选择**：`bubble.go` 的 `tea.WithMouseCellMotion()` 为支持滚轮
   滚动而开启鼠标跟踪，终端从此把拖拽事件发给应用而非启动原生选择。
2. **Alt screen 隔绝回滚缓冲区**：`chat.go` Init 的 `tea.EnterAltScreen` 使对话只存在于
   备用屏幕，退出即丢弃，终端 scrollback 从未包含对话——「运行中选不了」且「退出后没得选」。

附带说明（源码核实，v1.3.10）：bubbletea 默认开启 bracketed paste，粘贴以
`tea.KeyMsg{Type: KeyRunes, Runes, Paste: true}` 整块到达，bubbles textarea 走
default 分支进 `insertRunesFromUserInput`（多行插入逻辑完整，sanitizer 默认保留
`\n`）——**多行粘贴输入当前已可用**，不在本设计修复范围，仅补回归测试钉住。

本设计将渲染模型改为 **inline（内联）**：app 只「拥有」终端底部几行活区，定稿内容经
`tea.Println` 打入原生 scrollback。对话历史交还终端管理，复制/滚轮/选择/退出留存全部
恢复原生行为。

## 目标

- 鼠标拖选复制、滚轮滚动、退出后对话留存于 scrollback——全部原生终端行为
- 输入框 + footer 常驻终端底部（活区原地重绘），交互体验与 alt screen 版一致
- 流式输出逐段自然流出（用户选定策略），长回答不再有活区高度上限风险
- 顺带为多行粘贴补回归测试（v1.3.10 默认 bracketed paste 已可用，防止回退）
- 改动收敛在 `internal/ui/bubble/` 包内；UI 接口、Runner 接线、submitCh/pickerDone/
  权限链路不动

## 非目标

- 不保留 alt screen 版本做运行时切换（git 分支对比即可）
- 不实现应用内滚动（滚动即终端滚 scrollback）
- 不做 OSC 52 剪贴板命令（方案 B，另行评估）

## 渲染模型

```
（原生 scrollback，app 不管，可选中复制）   ← 往上滚是历史
  $ go run .
  欢迎横幅（已打印）
  > 写一个快排
  🧑 好的，快排思路如下…
  🔧 shell …（工具框线）
─────────────────────────────────────────
⏳ 输出中▌                                 ← 状态行（仅查询中）
> 输入框_                                  ← 常驻：textarea
agentic │ 上下文 3.2k/50k │ 💰 … │ ctrl+c 退出 ← 常驻：footer
```

- **活区**（View() 渲染，bubbletea 每帧原地重绘）：状态行（可选）+ 权限弹层（可选）+
  textarea + footer，总高 ≤5 行
- **定稿内容**：Update 返回 `tea.Println(文本)`，打印于活区上方、滚入 scrollback，
  打过即不可改

行为要点：

- 打字/流式时活区钉在底部，新内容从活区上方打印上滚
- 用户主动上滚翻历史时，输入框随屏幕滚出视野（同 shell prompt 行为）；一打字自动回底
- 不进 alt screen、不捕获鼠标；仍不直写 os.Stdout（避免与 tea renderer 冲突，
  runner 的聊天模式跳过直写提示符逻辑保持不变）

## 程序选项变更（bubble.go）

| 现状 | 变更 |
|------|------|
| `tea.WithMouseCellMotion()` | 删除（恢复原生选择/滚轮/复制） |

无新增选项：bubbletea v1.3.10 默认 inline（不传 alt screen 选项）、默认开启
bracketed paste。

## ChatModel 改造（chat.go）

### 结构

- **删除 viewport** 及其全部高度计算；`m.lines` 保留作已提交转录（测试断言 seam）
- 新增字段：`streamBuf string`（未冲刷的流式缓冲）、`streamed bool`（自最近一次
  think 起是否有流式内容）、`status string`（活区状态行文本，空=不渲染）
- `Init()`：删 `tea.EnterAltScreen`，保留 `textarea.Blink`

### View()

按序拼接（全部活区）：状态行（非空时）→ 权限弹层（permLayer 非 nil 时）→ textarea →
footer。picker 模式保持现状（选择器替换活区渲染）。

### 事件 → 打印映射

| 事件 | 行为 |
|------|------|
| chatThinkMsg | status="⏳ 思考中"；`streamed=false`；不打印 |
| chatDeltaMsg | 追加 streamBuf；status="📝 输出中"；**遇 `\n` 冲刷**：完整段落经 `tea.Println` 定稿并记入 m.lines |
| chatFinalMsg | 冲刷 streamBuf 残余段落；`streamed=true` 时只补 `⚡ token` 统计行（不重印全文），`streamed=false`（模型直接答无增量）时 Println glamour 渲染全文 + 统计行；清空 status |
| chatToolCallMsg | 冲刷 streamBuf；status="🔧 执行工具: name"；Println 工具框线（box.go 复用） |
| chatToolResultMsg | Println 结果框线（现有渲染不变） |
| chatContinueMsg | status="🔄 继续推理 (iter N)"；不打印 |
| chatErrorMsg / chatMessageMsg / chatWelcomeMsg | Println（现有渲染不变）；同时清空 status |
| chatHistoryMsg | m.lines 清空重建；逐条 Println（切换会话后旧内容已在 scrollback，无需清除） |
| chatBalanceMsg / chatContextMsg | 进 footer 字段（现状不变） |
| chatPickerMsg / chatPermissionMsg / chatPermissionDoneMsg | 活区渲染（现状不变） |

### 流式冲刷逻辑（逐段提交）

```
onDelta(c):
  streamBuf += c
  while streamBuf 含 '\n':
    段, streamBuf = splitfirst(streamBuf, '\n')
    commit(段)            // m.lines 追加 + 返回 tea.Println(段)
    streamed = true
onFinal:
  if streamBuf != "": commit(streamBuf); streamBuf=""; streamed=true
  if !streamed:      commit(glamour 渲染全文)
  if totalTokens>0:  commit(⚡ 统计行)
```

说明：增量与最终回答同源（现有代码注释已确认），故 `streamed=true` 时 final 不重印，
避免双份显示；流式段落为原文（不做 glamour），这是逐段提交策略的既定代价。

### 粘贴

无实现改动（已可用）。回归测试：`tea.KeyMsg{Type: KeyRunes, Runes: "a\nb", Paste: true}`
进入 textarea 值（含换行）、不写 submitCh。

### WindowSizeMsg

textarea.SetWidth(v.Width)；记录 width/height；glamour wordwrap 宽度随终端宽更新
（cap 100，初始仍 100）。不再计算 viewport 高度。

## 不变的部分

- UI 接口方法集与 BubbleUI 事件转发（Program.Send）
- submitCh / pickerDone / 权限确认 inputForward 链路
- box.go 工具框线、welcome.go 横幅渲染
- footer 状态栏（上下文/余额/退出提示）格式
- Runner 侧一切逻辑（含聊天模式跳过 stdout 直写）

## 测试策略（chat_test.go 改造）

改造（viewport 断言 → lines/View 断言）：

- View：含 placeholder + footer；对话内容**不在** View 中
- WindowSize：textarea 宽度更新（viewport 高度断言删除）
- Events：m.lines 含 think 状态行不再打印的调整后集合
- Welcome/History：断言 Println 定稿（m.lines）而非滚动位置
  （WelcomeGotoTop / HistoryGotoBottom 删除，语义不存在了）

新增：

- 流式段落冲刷：delta 跨 `\n` 分两段定稿，streamBuf 清空
- final 不重印：有流式时 final 后无全文重复行；无流式时 final 打印 glamour 全文
- 状态行生命周期：think 置位、final/error 清空、提交重置
- PasteMsg 场景（`KeyMsg{Paste:true}`）：多行文本进入 textarea 值、不写 submitCh

不变：Submit/SubmitEmpty/SubmitTwice、ToolMessages、picker 双测、
statusbar/welcome/bubble 生命周期测试（`WithoutRenderer` 注入不受选项变化影响）。

## 验收清单（手动）

- [ ] 拖选对话文本可复制（Terminal.app 直接拖选；无需修饰键）
- [ ] 滚轮滚动为终端原生 scrollback；上滚时输入框暂时离屏、打字自动回底
- [ ] ctrl+c 退出后对话仍留在终端 scrollback
- [ ] 多行文本粘贴进输入框：内容完整、不提前提交
- [ ] 流式回答逐段流出，final 无双份显示；长回答（>1 屏）流出过程无错位
- [ ] 权限确认弹层显示/清除正常；/list 选择器正常
- [ ] go test ./... 全绿

## 已知边界（实验阶段接受）

- 终端高度 < 活区高度（约 <5 行）时重绘错位——正常使用不触及
- `/stop` 后状态行可能残留至下一事件：chatMessageMsg（"⏹️ 已停止"）兜底清空 +
  下次提交重置
- 已定稿内容不能原地重写：流式段落为原文，不做 markdown 重渲染
- 窗口 resize 后已定稿行不重排（终端原生行为）
