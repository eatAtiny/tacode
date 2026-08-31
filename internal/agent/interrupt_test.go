package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"agentic/internal/llm"
	"agentic/internal/tool"
)

// ──────────────────────────────────────────────────────────
// P2⑧: 循环内中断
//
// queryLoopContext 支持 inputForward 通道接收控制命令：
//   - /interrupt → 中止当前批次的工具执行，注入提示让 LLM 调整策略
//   - /retry     → 同样中止（带新参数的 /retry <args> 由上层处理）
// ──────────────────────────────────────────────────────────

// alwaysAllowChecker 放行所有权限的测试用 checker。
type alwaysAllowChecker struct{}

func (alwaysAllowChecker) CheckPermission(string, string) bool { return true }

func TestExecuteToolCalls_InterruptSkipsRemaining(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileTool())

	// 放行所有权限（避免 file write 的权限确认阻塞测试）。
	SetPermissionChecker(alwaysAllowChecker{})
	t.Cleanup(func() { SetPermissionChecker(nil) })

	events := make(chan QueryEvent, 100)
	inputForward := make(chan string, 1)
	inputForward <- "/interrupt"

	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
		inputForward: inputForward,
	}

	// file write 是串行 + 需权限 → 走串行路径。
	tmpDir := t.TempDir()
	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "file", Arguments: `{"action": "write", "path": "` + tmpDir + `/a.txt", "content": "a"}`},
		{ID: "c2", Name: "file", Arguments: `{"action": "write", "path": "` + tmpDir + `/b.txt", "content": "b"}`},
	}

	lc.executeToolCalls(toolCalls, 0)

	// 两个工具都被跳过（中断提示代替执行）。
	if len(lc.messages) != 1 {
		t.Fatalf("expected 1 interrupt notice message, got %d: %+v", len(lc.messages), lc.messages)
	}
	if !strings.Contains(lc.messages[0].Content, "用户已中断") {
		t.Errorf("message should be interrupt notice, got %q", lc.messages[0].Content)
	}
}

func TestPeekInterrupt_NoChannel(t *testing.T) {
	lc := &queryLoopContext{}
	if cmd := lc.peekInterrupt(); cmd != "" {
		t.Errorf("no inputForward should return empty, got %q", cmd)
	}
}

func TestPeekInterrupt_NoCommand(t *testing.T) {
	ch := make(chan string, 1)
	lc := &queryLoopContext{inputForward: ch}
	// 通道空 → 返回空。
	if cmd := lc.peekInterrupt(); cmd != "" {
		t.Errorf("empty channel should return empty, got %q", cmd)
	}
}

func TestPeekInterrupt_RetryCommand(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "/retry {\"command\":\"ls\"}"
	lc := &queryLoopContext{inputForward: ch}
	if cmd := lc.peekInterrupt(); cmd != "/retry {\"command\":\"ls\"}" {
		t.Errorf("retry command should pass through, got %q", cmd)
	}
}
