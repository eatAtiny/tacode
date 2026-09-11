package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"tacode/internal/llm"
	"tacode/internal/tool"
)

// ──────────────────────────────────────────────────────────
// P2⑧: 循环内中断
//
// queryLoopContext 支持 inputForward 通道接收控制命令：
//   - /interrupt → 中止当前批次的工具执行，注入提示让 LLM 调整策略
//
// 历史上还接受 /retry 前缀，但那条路径从未实现（只把命令文本拼进给 LLM 的
// 提示，没有任何代码重新执行工具），已移除——现在 /retry 是普通输入。
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

	lc.executeToolCalls(toolCalls)

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

// /retry 已不再是控制命令：peekInterrupt 不认识它，返回空串。
// 注意该值会被消费掉（peekInterrupt 无回填），这是当前的已知行为。
func TestPeekInterrupt_RetryIsNoLongerControlCommand(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "/retry {\"command\":\"ls\"}"
	lc := &queryLoopContext{inputForward: ch}
	if cmd := lc.peekInterrupt(); cmd != "" {
		t.Errorf("/retry 不应被识别为控制命令，got %q", cmd)
	}
	if len(ch) != 0 {
		t.Errorf("peekInterrupt 应已消费该值，channel 长度 = %d", len(ch))
	}
}

// 带前后空白的 /interrupt 仍应被识别（TrimSpace 归一化）。
func TestPeekInterrupt_InterruptWithWhitespace(t *testing.T) {
	ch := make(chan string, 1)
	ch <- "  /interrupt\n"
	lc := &queryLoopContext{inputForward: ch}
	if cmd := lc.peekInterrupt(); cmd != "/interrupt" {
		t.Errorf("应归一化为 /interrupt，got %q", cmd)
	}
}

// isControlCommand 只认 /interrupt；/retry 及其它输入均返回 false。
func TestIsControlCommand(t *testing.T) {
	cases := map[string]bool{
		"/interrupt":              true,
		"  /interrupt  ":          true,
		"/retry":                  false,
		`/retry {"command":"ls"}`: false,
		"/stop":                   false,
		"普通消息":                    false,
	}
	for input, want := range cases {
		if got := isControlCommand(input); got != want {
			t.Errorf("isControlCommand(%q) = %v, want %v", input, got, want)
		}
	}
}
