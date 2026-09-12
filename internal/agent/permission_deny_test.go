package agent

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"tacode/internal/llm"
	"tacode/internal/tool"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// 用户拒绝权限确认 → 终止本次查询 + 留痕
//
// 语义（用户拍板）：拒绝不再「跳过该工具继续」，而是终止整个查询。
// 代价是本轮内 LLM 不能自动重新规划，补偿是拒绝记录留在累积 messages 里，
// 下一轮用户开口时 LLM 看得到它并据此换方案。
// ──────────────────────────────────────────────────────────

// assertToolCallPairsComplete 断言每个 tool_call 都有配对的 role=tool 消息。
//
// API 硬约束：assistant 消息携带整批 tool_calls 后，每个 tool_call_id 都必须
// 有对应的 tool 结果，否则下一次 LLM 调用因配对不全被拒绝（400）。
// 批次中途终止（用户拒绝 / /interrupt）最容易留下这个缺口。
func assertToolCallPairsComplete(t *testing.T, messages []llm.ChatMessage, toolCalls []llm.ToolCall) {
	t.Helper()
	seen := make(map[string]bool, len(messages))
	for _, m := range messages {
		if m.Role == "tool" {
			seen[m.ToolCallID] = true
		}
	}
	for _, tc := range toolCalls {
		if !seen[tc.ID] {
			t.Errorf("tool_call %q 缺少配对的 role=tool 消息（会导致 API 400）", tc.ID)
		}
	}
}

// countToolMessages 统计指定 tool_call ID 的 role=tool 消息内容。
func countToolMessages(messages []llm.ChatMessage, id string) (n int, contents []string) {
	for _, m := range messages {
		if m.Role == "tool" && m.ToolCallID == id {
			n++
			contents = append(contents, m.Content)
		}
	}
	return n, contents
}

// 全局策略拒绝只跳过该工具（不终止查询），其余工具照常执行。
// 这是与「用户拒绝」必须区分开的另一条路径。
func TestExecuteSingleTool_PolicyDenySkipsOnly(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileTool())

	SetPermissionChecker(denyAllChecker{})
	t.Cleanup(func() { SetPermissionChecker(nil) })

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	tmpDir := t.TempDir()
	tc := llm.ToolCall{ID: "c1", Name: "file",
		Arguments: `{"action": "write", "path": "` + tmpDir + `/a.txt", "content": "a"}`}

	if decision := lc.executeSingleTool(tc); decision != permDenyPolicy {
		t.Errorf("全局策略拒绝应返回 permDenyPolicy，got %v", decision)
	}
	if lc.abortTool != "" {
		t.Errorf("策略拒绝不应设置 abortTool，got %q", lc.abortTool)
	}

	// 留痕：仍推入配对的 role=tool 消息（LLM 需要知道该工具没执行）。
	assertToolCallPairsComplete(t, lc.messages, []llm.ToolCall{tc})
	if n, contents := countToolMessages(lc.messages, "c1"); n != 1 ||
		!strings.Contains(contents[0], "全局权限策略拒绝执行") {
		t.Errorf("策略拒绝应留痕，got n=%d contents=%v", n, contents)
	}

	// 未执行：文件不应被创建。
	if fileExists(tmpDir + "/a.txt") {
		t.Error("策略拒绝的写操作不应生效")
	}
}

// ──────────────────────────────────────────────────────────
// 拒绝终止（经 PermissionRequest 事件通道，模拟 UI 返回「拒绝」）
// ──────────────────────────────────────────────────────────

// 用户拒绝 → 终止查询：executeToolCalls 返回 execAbortUser，被拒工具之前与
// 之后的每个 tool_call 都有配对消息，且最终以 Final 收束（不是 error）。
func TestExecuteToolCalls_UserDenyTerminates(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileTool())
	// 不注入全局 checker：走工具自检 → file write 需确认 → 弹 PermissionRequest。

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	tmpDir := t.TempDir()
	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "file", Arguments: `{"action": "write", "path": "` + tmpDir + `/a.txt", "content": "a"}`},
		{ID: "c2", Name: "file", Arguments: `{"action": "write", "path": "` + tmpDir + `/b.txt", "content": "b"}`},
	}

	// 设一个上限，避免 approve 路径下测试挂死。
	done := make(chan execOutcome, 1)
	go func() {
		done <- lc.executeToolCalls(toolCalls)
	}()

	// 消费事件：对第一个权限请求拒绝（后续不会再有权请求，因为已终止）。
	var denied bool
	for e := range events {
		if ev, ok := e.(PermissionRequest); ok && !denied {
			denied = true
			ev.Reply <- false // 用户拒绝
			break
		}
	}

	select {
	case outcome := <-done:
		if outcome != execAbortUser {
			t.Fatalf("用户拒绝应返回 execAbortUser，got %v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeToolCalls 未返回")
	}

	if lc.abortTool != "file" {
		t.Errorf("abortTool 应为被拒工具名 file，got %q", lc.abortTool)
	}
	// 配对完整性：c1 被拒、c2 从未执行，两者都必须有 role=tool 消息。
	assertToolCallPairsComplete(t, lc.messages, toolCalls)

	n1, c1 := countToolMessages(lc.messages, "c1")
	if n1 != 1 || !strings.Contains(c1[0], "用户拒绝执行") {
		t.Errorf("被拒工具 c1 应留痕「用户拒绝执行」，got n=%d contents=%v", n1, c1)
	}
	n2, c2 := countToolMessages(lc.messages, "c2")
	if n2 != 1 || !strings.Contains(c2[0], "用户终止了本次查询") {
		t.Errorf("未执行的 c2 应回填占位，got n=%d contents=%v", n2, c2)
	}

	// 被拒的工具真的没执行：文件不应存在。
	if fileExists(tmpDir + "/a.txt") {
		t.Error("被拒的写操作不应生效")
	}
}

// 批准路径回归：用户批准后行为不变（工具执行、批次正常收束为 execContinue）。
func TestExecuteToolCalls_UserApproveContinues(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewFileTool())

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	tmpDir := t.TempDir()
	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "file", Arguments: `{"action": "write", "path": "` + tmpDir + `/a.txt", "content": "a"}`},
	}

	done := make(chan execOutcome, 1)
	go func() { done <- lc.executeToolCalls(toolCalls) }()

	for e := range events {
		if ev, ok := e.(PermissionRequest); ok {
			ev.Reply <- true
			break
		}
	}

	select {
	case outcome := <-done:
		if outcome != execContinue {
			t.Fatalf("批准后应返回 execContinue，got %v", outcome)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("executeToolCalls 未返回")
	}

	assertToolCallPairsComplete(t, lc.messages, toolCalls)
	if lc.abortTool != "" {
		t.Errorf("批准路径不应设置 abortTool，got %q", lc.abortTool)
	}
	if !fileExists(tmpDir + "/a.txt") {
		t.Error("批准的写操作应生效")
	}
}

// ──────────────────────────────────────────────────────────
// 端到端：真实 queryLoop + mock LLM
//
// 上半段的测试直接调 executeToolCalls，覆盖不到 runLoop 的收束分支——
// 而「用 final 而非 error 收束」正是留痕能否活到下一轮的关键（repl.go 只在
// err == nil 时累积 messages）。这里用 httptest mock 一个 tool_calls 响应
// 驱动完整循环，断言终止提示既进事件流（上屏 + EventStore）又进 lc.messages。
// ──────────────────────────────────────────────────────────

// toolCallSSE 生成一个 finish_reason=tool_calls 的流式响应（单个 file write）。
func toolCallSSE(id, args string) string {
	return "data: " + `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,` +
		`"model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant",` +
		`"tool_calls":[{"index":0,"id":"` + id + `","type":"function",` +
		`"function":{"name":"file","arguments":` + strconv.Quote(args) + `}}]},` +
		`"finish_reason":null}]}` + "\n\n" +
		"data: " + `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,` +
		`"model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
}

// 跑完整 queryLoop，对权限请求回复 reject。
func TestQueryLoop_UserDenyYieldsFinalAndKeepsRecord(t *testing.T) {
	tmpDir := t.TempDir()
	writePath := tmpDir + "/a.txt"

	args, _ := json.Marshal(map[string]string{"action": "write", "path": writePath, "content": "x"})
	handler := &sseHandler{response: toolCallSSE("c1", string(args))}
	srv := newTestSSEServer(t, handler)
	client := newLLMClientViaEnv(t, srv.URL)

	reg := tool.NewRegistry()
	reg.Register(tool.NewFileTool())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := queryLoop(
		ctx, client,
		[]llm.ChatMessage{{Role: "user", Content: "写个文件"}},
		[]openai.Tool{{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name: "file", Description: "文件操作",
				Parameters: map[string]any{"type": "object"},
			},
		}},
		reg, 3,
	)

	var finalText string
	var gotFinal, gotPermission bool
	var sawToolResultError bool
	for ev := range events {
		switch e := ev.(type) {
		case PermissionRequest:
			gotPermission = true
			e.Reply <- false // 用户拒绝
		case ToolResultEvent:
			if e.IsError {
				sawToolResultError = true
			}
		case FinalEvent:
			if finalText != "" {
				t.Errorf("终止后不应再有第二个 FinalEvent，got %q", e.Content)
			}
			finalText = e.Content
			gotFinal = true
		}
	}

	if !gotPermission {
		t.Fatal("file write 应触发权限确认")
	}
	if !gotFinal {
		t.Fatal("拒绝终止必须用 FinalEvent 收束（error 路径会丢掉累积 messages）")
	}
	if !strings.Contains(finalText, "本次查询已终止") {
		t.Errorf("终止提示文案不符，got %q", finalText)
	}
	if !sawToolResultError {
		t.Error("被拒工具应 yield 一条 IsError 的 ToolResultEvent（对话区 + EventStore 留痕）")
	}

	// 被拒的写操作真的没落地。
	if fileExists(writePath) {
		t.Error("被拒的写操作不应生效")
	}

	// 只应发生一次 LLM 调用：拒绝即终止，不在本轮内继续迭代。
	handler.mu.Lock()
	n := len(handler.bodies)
	handler.mu.Unlock()
	if n != 1 {
		t.Errorf("拒绝后不应再调用 LLM，got %d 次请求", n)
	}
}
