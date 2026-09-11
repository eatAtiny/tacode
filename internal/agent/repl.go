// repl.go REPL 主循环：select 模型 + 输入排队/转发协议。

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"tacode/internal/memory"
)

// Run 进入交互循环：读用户输入 -> QueryEngine -> 保存记忆。
//
// 主循环流程（channel select 模式）：
//
//	循环开始
//	  ├─ case input, ok := <-inputCh         ← 收到用户输入
//	  │    ├─ "exit"  → return（退出）
//	  │    ├─ "/stop" → queryCancel()（中断正在运行的查询）
//	  │    ├─ "/xxx"  → handleSessionCommand()（会话管理命令）
//	  │    └─ 其他     → runQueryAsync()（启动异步查询）
//	  │                   ├─ 记录 EventUser
//	  │                   ├─ 重置 token 计数
//	  │                   ├─ 创建 queryCtx + cancel
//	  │                   └─ 启动 goroutine 调用 queryEngine()
//	  │
//	  └─ case result, ok := <-queryResultCh  ← 收到查询结果
//	       ├─ 记录 EventAssistant
//	       ├─ go history.Append()             ← 后台保存 L1
//	       ├─ go extractMemory()              ← 后台提取 L3 记忆
//	       └─ go ensurePersisted()            ← 首次对话后落盘临时会话
//
// 启动时创建临时会话，只有真正对话后才落盘到 manifest。
func (r *Runner) Run(ctx context.Context) error {
	// ── 启动聊天 TUI（tea 程序） ──
	// 聊天界面（BubbleUI）后台启动 tea.Program，Runner 主循环不阻塞：
	//   - 事件方法（OnThink/OnDelta/...）→ Program.Send → ChatModel 渲染对话区
	//   - 输入从 ChatModel 的 textarea 提交 channel（ReadInputChan）读取
	// 必须在 Welcome 之前启动：send 在 program 为 nil 时丢弃消息，未启动就
	// 调事件方法会丢欢迎信息。
	// one-shot / 子 agent 模式（TextUI 等）chatUI 为 nil，走原路径不受影响。
	if r.chatUI != nil {
		if err := r.chatUI.Start(); err != nil {
			r.ui.OnError(fmt.Errorf("聊天界面启动失败: %v", err))
			return err
		}
		defer r.chatUI.Close()
	}

	// ── 启动临时会话 ──
	// 临时会话不在 manifest 中，首次对话后通过 ensurePersisted 落盘。
	if err := r.initTempSession(); err != nil {
		return err
	}

	// 显示欢迎信息。
	r.ui.Welcome(r.llm.Model())

	// ── 启动状态初始化（footer 状态栏立即有数据） ──
	// 1. 上下文占用：初始为空（0 字符），显示 "上下文 0/50K (0%)"。
	// 2. 余额：自动查询一次（无需手动 /balance），成功显示在 footer；
	//    失败静默（启动不打扰，后续 /balance 可手动查询）。
	if r.compactor != nil {
		r.ui.UpdateContext(0, r.compactor.Limit())
	}
	go r.queryBalance()

	// ── 启动异步输入读取 ──
	// 追加式模型：统一走 UI 的 raw 输入通道（ReadInputChan），
	// 输入即流末尾的提示符行，无 tea 输入桥接。
	var inputCh <-chan string
	inputCh = r.ui.ReadInputChan()

	// ── 查询状态变量 ──
	var queryResultCh <-chan queryResult // 查询结果 channel（nil 表示无运行中的查询）
	var queryCancel context.CancelFunc   // 取消函数（用于 /stop）
	var queryRunning bool                // 是否有查询正在运行
	var round int                        // 当前轮次号
	var currentInput string              // 当前查询的用户输入，用于保存记忆
	var inputForward chan string         // 权限确认输入与控制命令（/interrupt）转发通道

	// 排队输入重放 channel：查询结束时把 pendingInputs 弹出一条投递到此，
	// 与 inputCh 走同一套分支 A 处理逻辑（空输入/exit/命令/查询）。
	pendingReplayCh := make(chan string, 4)

	// 显示初始提示符（立即 flush 确保在用户输入前显示）。
	r.printPrompt()

	for {
		select {
		// ──────────────────────────────────────────
		// 分支 A: 收到用户输入（主输入通道 或 排队重放）
		// ──────────────────────────────────────────
		case input, ok := <-inputCh:
			if !ok {
				// EOF，退出。
				if queryRunning {
					queryCancel()
				}
				return nil
			}
			// 排队输入重放与主输入走同一处理逻辑。
			if exit := r.handleInput(input, ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &r.pendingInputs); exit {
				return nil
			}

		// 分支 A2: 排队输入重放（查询结束后自动处理用户查询中输入的下一条）。
		case input := <-pendingReplayCh:
			if exit := r.handleInput(input, ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &r.pendingInputs); exit {
				return nil
			}

		// ──────────────────────────────────────────
		// 分支 B: 收到查询结果
		// ──────────────────────────────────────────
		case result, ok := <-queryResultCh:
			if !ok {
				queryResultCh = nil
				continue
			}

			// 清理查询状态。
			queryRunning = false
			queryResultCh = nil
			inputForward = nil

			if result.err != nil {
				r.ui.OnError(result.err)
			} else {
				// 跨轮累积：查询成功后保存本次完整消息数组。
				// 失败/取消时不更新（保留上一轮累积），避免部分轮次消息进入下轮。
				if result.messages != nil {
					r.messages = result.messages
				}

				// 记录助手回答事件。
				r.events.Append(memory.Event{
					Type:    memory.EventAssistant,
					Round:   round,
					Content: result.answer,
					Model:   r.llm.Model(),
				})

				// ── 后台保存记忆 ──
				// 不阻塞主循环，在独立 goroutine 中执行：
				//   1. 保存 L1 原始对话记录
				//   2. 提取 L3 记忆（L2 摘要仅在压缩时生成——当前未持久化到
				//      summaries.jsonl，对话细节由跨轮累积 messages + EventStore 承载）
				//   3. 首次对话后临时会话落盘
				go func(round int, input, answer string) {
					if err := r.history.Append(round, input, answer); err != nil {
						r.ui.OnError(fmt.Errorf("save history failed at round %d: %w", round, err))
						return
					}
					r.extractMemory(ctx, round, input, answer)
					if r.isTemporary {
						if err := r.ensurePersisted(input); err != nil {
							r.ui.OnError(fmt.Errorf("保存会话失败: %v", err))
						}
					}
				}(round, currentInput, result.answer)
			}

			r.printPrompt()

			// 查询结束：自动处理查询期间排队的输入（Bug 2 修复——用户查询中
			// 输入的下一条消息在回答完成后自动作为下一轮输入发送）。
			// 只重放第一条，剩余继续排队（每轮查询结束处理一条，保持顺序）。
			if len(r.pendingInputs) > 0 {
				next := r.pendingInputs[0]
				r.pendingInputs = r.pendingInputs[1:]
				pendingReplayCh <- next
			}
		}
	}
}

// handleInput 处理一条用户输入（主输入通道与排队重放共用）。
//
// 参数（指针，runInput 状态由 Run() 主循环持有，本方法修改后由主循环继续使用）：
//   - queryResultCh: 查询结果 channel（启动查询时写入，nil 表示无运行中的查询）
//   - queryCancel:   取消函数（/stop 用）
//   - queryRunning:  是否有查询正在运行
//   - round:         当前轮次号
//   - currentInput:  当前查询的用户输入（保存记忆用）
//   - inputForward:  权限确认输入与控制命令（/interrupt）转发通道
//   - pendingInputs: 查询运行中排队的输入
//
// 返回 true 表示退出（exit 命令或 EOF）。
func (r *Runner) handleInput(
	input string,
	ctx context.Context,
	queryResultCh *<-chan queryResult,
	queryCancel *context.CancelFunc,
	queryRunning *bool,
	round *int,
	currentInput *string,
	inputForward *chan string,
	pendingInputs *[]string,
) bool {
	if input == "" {
		if !*queryRunning {
			r.printPrompt()
		}
		return false
	}

	// "exit" 退出程序。
	if strings.EqualFold(input, "exit") {
		if *queryRunning {
			(*queryCancel)()
		}
		return true
	}

	// ── 查询运行中 ──
	// 输入处理策略：
	//   - /stop                 → 取消当前查询
	//   - 权限确认在等           → 转发给 query 侧（ConfirmPermission 阻塞读 inputForward）
	//   - /interrupt             → 控制命令，转发给 queryLoop（peekInterrupt 读取）
	//   - 其他                   → 排队（pendingInputs），查询结束后自动作为下一轮输入
	// 旧实现无条件 `inputForward <- input`：无权限确认时 channel 无人读，
	// 第 2 条输入即阻塞冻结主循环（Bug 2 根因）。
	if *queryRunning {
		if input == "/stop" {
			(*queryCancel)()
			*queryRunning = false
			*inputForward = nil
			r.ui.OnMessage("⏹️  已停止")
			*round++
			r.printPrompt()
		} else if r.permWaiting.Load() {
			// 权限确认：转发给 ConfirmPermission（阻塞读 inputForward）。
			// 非阻塞发送避免 channel 满时冻结主循环；缓冲 1 一般有空位。
			select {
			case *inputForward <- input:
			default:
				// 缓冲满（异常）：丢弃，避免阻塞。
			}
		} else if isControlCommand(input) {
			// 控制命令：转发给 queryLoop（peekInterrupt 在串行工具执行间隙
			// 非阻塞读取，中止剩余工具并注入中断提示让 LLM 调整策略）。
			// 非阻塞发送：缓冲满（上一条未被消费）时丢弃，不冻结主循环。
			select {
			case *inputForward <- input:
			default:
				// 缓冲满（上一条命令未被消费）：丢弃，避免阻塞。
			}
		} else {
			// 普通查询运行中：排队，查询结束后自动处理。
			*pendingInputs = append(*pendingInputs, input)
			r.ui.OnMessage("💬 输入已排队，当前查询结束后自动发送")
		}
		return false
	}

	// ── 空闲状态，处理输入 ──
	if strings.HasPrefix(input, "/") {
		// 处理会话管理命令。
		newRound, handled := r.handleSessionCommand(input)
		if handled {
			if newRound > 0 {
				*round = newRound - 1
			}
		} else {
			r.ui.OnError(fmt.Errorf("未知命令，可用: /new, /list, /switch, /delete, /rename, /current, /compress, /memory, /reload, /balance"))
		}
		*round++
		r.printPrompt()
		return false
	}

	// ── 普通输入，启动异步查询 ──
	*round++

	// 记录用户输入事件。
	r.events.Append(memory.Event{
		Type:    memory.EventUser,
		Round:   *round,
		Content: input,
	})

	// 创建可取消的 context（用于 /stop）。
	queryCtx, cancel := context.WithCancel(ctx)
	*queryCancel = cancel
	*queryRunning = true
	*currentInput = input
	*inputForward = make(chan string, 1)

	// 启动后台 goroutine 执行查询。
	// queryResultCh 收到结果后触发分支 B。
	*queryResultCh = r.runQueryAsync(queryCtx, *round, input, *inputForward)
	return false
}

// isControlCommand 判断输入是否为查询控制命令。
// 与 tool_exec.go peekInterrupt 的识别规则一致：TrimSpace 后精确匹配 /interrupt。
//
// 历史上这里还接受 "/retry" 前缀，但 /retry 从未实现——它被 peekInterrupt 消费后
// 只是把命令文本拼进给 LLM 的中断提示，没有任何代码重新执行工具，也没有代码
// 解析其参数。行为与 /interrupt 完全相同，却让用户以为存在重试语义，故移除。
// 现在 /retry 是普通输入 → 排队，查询结束后作为下一轮消息发送。
func isControlCommand(input string) bool {
	return strings.TrimSpace(input) == "/interrupt"
}

// printPrompt 打印主屏输入提示符 "> "（追加式模型：输入框即流末尾的提示符）。
// 聊天 TUI 模式：提示符由 textarea 渲染，跳过（写 os.Stdout 会打乱活区渲染）。
func (r *Runner) printPrompt() {
	if r.isChatUI() {
		return
	}
	fmt.Print("> ")
	os.Stdout.Sync()
}

// isChatUI 判断是否聊天 TUI 模式（BubbleUI inline 聊天界面）。
// 聊天界面下活区由 tea 原地重绘，直接写 os.Stdout 会打乱活区渲染
// （非 alt screen 原因——本界面不进 alt screen），相关输出一律跳过。
func (r *Runner) isChatUI() bool { return r.chatUI != nil }
