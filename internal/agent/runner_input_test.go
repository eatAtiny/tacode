package agent

import (
	"context"
	"testing"
)

// handleInput 查询运行中的普通输入应排队（不阻塞），而非转发 inputForward。
//
// Bug 2 回归：旧实现 `inputForward <- input` 在无权限确认读者时第 2 条输入
// 阻塞冻结主循环。新实现用 pendingInputs 排队 + permWaiting 区分权限确认。
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

// handleInput 查询运行中输入 /retry 前缀命令（含参数）同样转发 inputForward。
func TestHandleInput_QueryRunningForwardsRetry(t *testing.T) {
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
	if len(pendingInputs) != 0 {
		t.Errorf("/retry 不应排队，pendingInputs = %v", pendingInputs)
	}
	select {
	case got := <-inputForward:
		if got != retryCmd {
			t.Errorf("inputForward = %q, want %q", got, retryCmd)
		}
	default:
		t.Error("inputForward 应收到 /retry 命令（mid-loop 转发，而非排队）")
	}
}

// handleInput 权限确认在等（permWaiting=true）时应转发 inputForward（非阻塞）。
func TestHandleInput_PermissionForwardsInput(t *testing.T) {
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
	r.permWaiting.Store(true) // 模拟权限确认在等

	if exit := r.handleInput("y", ctx, &queryResultCh, &queryCancel, &queryRunning, &round, &currentInput, &inputForward, &pendingInputs); exit {
		t.Fatal("不应退出")
	}
	if len(pendingInputs) != 0 {
		t.Errorf("权限确认时不应排队，pendingInputs = %v", pendingInputs)
	}
	select {
	case got := <-inputForward:
		if got != "y" {
			t.Errorf("inputForward = %q, want y", got)
		}
	default:
		t.Error("inputForward 应收到 y（权限确认转发）")
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
