package agent

import (
	"context"
	"testing"
)

// handleInput 查询运行中的普通输入应排队（不阻塞），而非转发 inputForward。
//
// Bug 2 回归：旧实现 `inputForward <- input` 在无读者时第 2 条输入即阻塞
// 冻结主循环。新实现用 pendingInputs 排队。
//
// 权限答案已不在此路径：确认由 UI 层自行取得（模态弹层 + 私有 channel），
// 不经过文本输入流，因此主循环无须判断某行输入是否为权限答案。
func TestHandleInput_QueryRunningQueuesInput(t *testing.T) {
	r := newTestRunner(t)
	ctx := context.Background()

	var (
		queryResultCh <-chan queryResult
		queryCancel   context.CancelFunc
		queryRunning  bool
		round         int
		currentInput  string
		inputForward  chan string
		pendingInputs []string
	)

	// 模拟查询运行中。
	queryRunning = true
	inputForward = make(chan string, 1)

	// 第 1 条输入：应排队（不转发、不阻塞）。
	if exit := r.handleInput("第一条", ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if len(pendingInputs) != 1 || pendingInputs[0] != "第一条" {
		t.Errorf("pendingInputs = %v, want [第一条]", pendingInputs)
	}

	// 第 2 条输入：仍排队（旧实现此处阻塞冻结，新实现应返回）。
	if exit := r.handleInput("第二条", ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if len(pendingInputs) != 2 {
		t.Errorf("pendingInputs len = %d, want 2（第 2 条应排队不阻塞）", len(pendingInputs))
	}
	// inputForward 缓冲应仍为空（无转发）。
	select {
	case got := <-inputForward:
		t.Errorf("inputForward 不应收到输入（无权限确认），got %q", got)
	default:
	}
}

// handleInput 查询运行中（非权限确认）输入 /interrupt 应转发 inputForward，
// 而非排队 pendingInputs——queryLoop 的 peekInterrupt 在串行工具执行间隙
// 从 inputForward 读中断命令，排队会让 mid-loop 中断不可达。
func TestHandleInput_QueryRunningForwardsInterrupt(t *testing.T) {
	r := newTestRunner(t)
	ctx := context.Background()

	var (
		queryResultCh <-chan queryResult
		queryCancel   context.CancelFunc
		queryRunning  bool
		round         int
		currentInput  string
		inputForward  chan string
		pendingInputs []string
	)

	// 模拟查询运行中、无权限确认（permWaiting 默认 false）。
	queryRunning = true
	inputForward = make(chan string, 1)

	if exit := r.handleInput("/interrupt", ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if len(pendingInputs) != 0 {
		t.Errorf("/interrupt 不应排队，pendingInputs = %v", pendingInputs)
	}
	select {
	case got := <-inputForward:
		if got != "/interrupt" {
			t.Errorf("inputForward = %q, want /interrupt", got)
		}
	default:
		t.Error("inputForward 应收到 /interrupt（mid-loop 中断转发，而非排队）")
	}
}

// handleInput 查询运行中输入 /retry 已不再被当作控制命令：它和普通消息一样
// 排队，查询结束后作为下一轮输入发送（此前会被转发进 inputForward 后被
// peekInterrupt 消费掉，既没重试也没排队——消息静默消失）。
func TestHandleInput_QueryRunningQueuesRetry(t *testing.T) {
	r := newTestRunner(t)
	ctx := context.Background()

	var (
		queryResultCh <-chan queryResult
		queryCancel   context.CancelFunc
		queryRunning  bool
		round         int
		currentInput  string
		inputForward  chan string
		pendingInputs []string
	)

	queryRunning = true
	inputForward = make(chan string, 1)

	retryCmd := `/retry {"command":"ls"}`
	if exit := r.handleInput(retryCmd, ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if len(pendingInputs) != 1 || pendingInputs[0] != retryCmd {
		t.Errorf("/retry 应排队，pendingInputs = %v", pendingInputs)
	}
	select {
	case got := <-inputForward:
		t.Errorf("/retry 不应写入 inputForward，却收到 %q", got)
	default:
		// 预期：inputForward 保持为空。
	}
}

// handleInput /stop 应取消查询并复位状态。
func TestHandleInput_Stop(t *testing.T) {
	r := newTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())

	var (
		queryResultCh <-chan queryResult
		queryCancel   = cancel
		queryRunning  bool
		round         int
		currentInput  string
		inputForward  chan string
		pendingInputs []string
	)

	queryRunning = true
	inputForward = make(chan string, 1)

	if exit := r.handleInput("/stop", ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if queryRunning {
		t.Error("/stop 后 queryRunning 应为 false")
	}
	if inputForward != nil {
		t.Error("/stop 后 inputForward 应为 nil")
	}
}
