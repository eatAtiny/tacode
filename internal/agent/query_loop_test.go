package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"tacode/internal/llm"
	"tacode/internal/tool"
)

// drainEvents 排空 channel 中剩余的事件（非阻塞）。
func drainEvents(events <-chan QueryEvent) []QueryEvent {
	var remaining []QueryEvent
	for {
		select {
		case evt := <-events:
			remaining = append(remaining, evt)
		default:
			return remaining
		}
	}
}

// collectEvents 消费事件 channel 直到执行完成。
// autoApprove=true 时自动批准权限请求。
func collectEvents(events <-chan QueryEvent, wg *sync.WaitGroup, timeout time.Duration, autoApprove bool) (toolResults []ToolResultEvent, permCount int) {
	deadline := time.After(timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		// 短暂等待确保最后的事件已写入 channel。
		time.Sleep(5 * time.Millisecond)
		close(done)
	}()

	// consume 处理单个事件：收集工具结果、统计权限请求并按需自动批准。
	consume := func(evt QueryEvent, approve bool) {
		switch e := evt.(type) {
		case ToolResultEvent:
			toolResults = append(toolResults, e)
		case PermissionRequest:
			permCount++
			if approve && e.Reply != nil {
				select {
				case e.Reply <- true: // 批准
				default:
				}
			}
		}
	}

	for {
		select {
		case evt := <-events:
			consume(evt, autoApprove)
		case <-done:
			for _, evt := range drainEvents(events) {
				consume(evt, false)
			}
			return
		case <-deadline:
			return
		}
	}
}

// ──────────────────────────────────────────────────────────
// P0: 并行工具执行集成测试
// ──────────────────────────────────────────────────────────

func TestParallelToolExecution(t *testing.T) {
	tmpDir := t.TempDir()

	reg := tool.NewRegistry()
	reg.Register(tool.NewGrepTool())
	reg.Register(tool.NewListTool())
	reg.Register(tool.NewFileTool())

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	toolCalls := []llm.ToolCall{
		{ID: "call_1", Name: "grep", Arguments: `{"pattern": "test", "path": "` + tmpDir + `"}`},
		{ID: "call_2", Name: "list", Arguments: `{"path": "` + tmpDir + `"}`},
		{ID: "call_3", Name: "file", Arguments: `{"action": "read", "path": "` + tmpDir + `/nonexistent.txt"}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	results, _ := collectEvents(events, &wg, 2*time.Second, false)

	if len(results) != 3 {
		t.Fatalf("expected 3 tool results, got %d", len(results))
	}

	expectedOrder := []string{"grep", "list", "file"}
	for i, r := range results {
		if r.ToolName != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], r.ToolName)
		}
	}

	if len(lc.messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(lc.messages))
	}
	expectedIDs := []string{"call_1", "call_2", "call_3"}
	for i, msg := range lc.messages {
		if msg.ToolCallID != expectedIDs[i] {
			t.Errorf("message[%d]: expected ToolCallID=%s, got %s", i, expectedIDs[i], msg.ToolCallID)
		}
		if msg.Role != "tool" {
			t.Errorf("message[%d]: expected role=tool, got %s", i, msg.Role)
		}
	}

	t.Logf("✅ 并行执行成功: 3 个工具全部执行，结果顺序=%v", expectedOrder)
}

// ──────────────────────────────────────────────────────────
// P0: 并行执行时间验证
// ──────────────────────────────────────────────────────────

func TestParallelExecutionIsFaster(t *testing.T) {
	reg := tool.NewRegistry()

	const delay = 100 * time.Millisecond
	reg.Register(&sleepTool{name: "slow1", delay: delay})
	reg.Register(&sleepTool{name: "slow2", delay: delay})
	reg.Register(&sleepTool{name: "slow3", delay: delay})

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "slow1", Arguments: `{}`},
		{ID: "c2", Name: "slow2", Arguments: `{}`},
		{ID: "c3", Name: "slow3", Arguments: `{}`},
	}

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	collectEvents(events, &wg, 2*time.Second, false)
	elapsed := time.Since(start)

	if elapsed > delay*2 {
		t.Errorf("并行执行太慢: %v (expected ~%v, serial would be ~%v)",
			elapsed, delay, delay*3)
	} else {
		t.Logf("✅ 并行执行时间: %v (串行需要 ~%v, 加速 ~%.1fx)",
			elapsed, delay*3, float64(delay*3)/float64(elapsed))
	}

	if len(lc.messages) != 3 {
		t.Errorf("expected 3 messages, got %d", len(lc.messages))
	}
}

// ──────────────────────────────────────────────────────────
// P0: 混合调用（并发 + 串行）测试
// ──────────────────────────────────────────────────────────

func TestMixedConcurrentAndSerial(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewGrepTool()) // 并发安全
	reg.Register(tool.NewListTool()) // 并发安全
	reg.Register(tool.NewFileTool()) // file write → 串行 + 需权限确认

	tmpDir := t.TempDir()

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	dstFile := tmpDir + "/out.txt"
	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "grep", Arguments: `{"pattern": "test", "path": "` + tmpDir + `"}`},
		{ID: "c2", Name: "list", Arguments: `{"path": "` + tmpDir + `"}`},
		{ID: "c3", Name: "file", Arguments: `{"action": "write", "path": "` + dstFile + `", "content": "hello"}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	// autoApprove=true: 统一事件处理器自动批准权限 → file write 不会阻塞。
	results, permCount := collectEvents(events, &wg, 2*time.Second, true)

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// file write 应该触发了权限检查。
	if permCount < 1 {
		t.Error("expected at least 1 permission check for file write")
	}

	expectedOrder := []string{"grep", "list", "file"}
	for i, r := range results {
		if r.ToolName != expectedOrder[i] {
			t.Errorf("result[%d]: expected %s, got %s", i, expectedOrder[i], r.ToolName)
		}
	}

	t.Logf("✅ 混合执行成功: 2 并发 + 1 串行(含权限确认)，结果顺序=%v，权限检查=%d次", expectedOrder, permCount)
}

// ──────────────────────────────────────────────────────────
// P1: 大结果持久化端到端测试
// ──────────────────────────────────────────────────────────

func TestLargeResultPersistence(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewListTool())

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "list", Arguments: `{"path": "../..", "depth": 3, "max_entries": 200}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	results, _ := collectEvents(events, &wg, 2*time.Second, false)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if strings.Contains(r.ToolResult, "💾 完整结果已保存到") {
		t.Logf("✅ 大结果已持久化到磁盘 (len=%d)", len(r.ToolResult))
	} else {
		t.Logf("结果未超过上限（无需持久化）: len=%d", len(r.ToolResult))
	}
}

// ──────────────────────────────────────────────────────────
// P0: 权限检查阻止并发执行测试
// ──────────────────────────────────────────────────────────

func TestPermissionBlocksConcurrent(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(tool.NewShellTool())

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	// rm 命令 — IsConcurrencySafe=false → 进入串行路径 → 触发权限检查。
	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "shell", Arguments: `{"command": "rm -rf /tmp/test_nonexistent"}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	// autoApprove=true 解除阻塞，同时记录权限事件。
	_, permCount := collectEvents(events, &wg, 2*time.Second, true)

	if permCount >= 1 {
		t.Logf("✅ 危险命令正确触发权限检查（%d 次），进入串行执行路径", permCount)
	} else {
		t.Error("危险命令未触发权限检查 — 可能被误分类为并发安全")
	}
}

// ──────────────────────────────────────────────────────────
// P0: 并发分类必须尊重全局禁止列表与全局权限检查器
// ──────────────────────────────────────────────────────────

// denyAllChecker 拒绝所有权限的测试用 checker。
type denyAllChecker struct{}

func (denyAllChecker) CheckPermission(string, string) bool { return false }

// TestForbiddenToolBlocksConcurrentClassification 验证：
// 自报 IsConcurrencySafe+IsReadOnly+CheckPermission.Allow 的工具被列入
// ForbiddenTools 后，必须落入串行路径（被禁止检查拦截），而非并发执行。
//
// 直接调用生产 executeToolCalls，确保覆盖真实分类逻辑（而非测试副本）。
func TestForbiddenToolBlocksConcurrentClassification(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&sleepTool{name: "forbidden_safe", delay: 0})

	// 追加到全局禁止列表（测试后恢复，避免污染其他测试）。
	origForbidden := ForbiddenTools
	ForbiddenTools = append(ForbiddenTools, "forbidden_safe")
	t.Cleanup(func() { ForbiddenTools = origForbidden })

	events := make(chan QueryEvent, 100)
	lc := &queryLoopContext{
		ctx:          context.Background(),
		toolRegistry: reg,
		messages:     make([]llm.ChatMessage, 0),
		events:       events,
		fileReads:    make(map[string]time.Time),
	}

	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "forbidden_safe", Arguments: `{}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	results, _ := collectEvents(events, &wg, 2*time.Second, false)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected forbidden tool to be blocked (IsError=true), got: %+v", results[0])
	}
	if !strings.Contains(results[0].ToolResult, "工具已被禁止使用") {
		t.Errorf("expected forbidden error message, got: %q", results[0].ToolResult)
	}
	if len(lc.messages) != 1 || !strings.Contains(lc.messages[0].Content, "工具已被禁止使用") {
		t.Errorf("expected 1 forbidden error message in history, got %d: %+v", len(lc.messages), lc.messages)
	}
}

// TestGlobalPermissionCheckerBlocksConcurrentClassification 验证：
// 全局权限检查器拒绝时，即使工具自报并发安全+只读+自身权限放行，
// 也必须落入串行路径（被全局检查器拦截），而非并发执行。
func TestGlobalPermissionCheckerBlocksConcurrentClassification(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&sleepTool{name: "global_blocked", delay: 0})

	// 注入全局权限检查器：一律拒绝（测试后恢复 nil）。
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

	toolCalls := []llm.ToolCall{
		{ID: "c1", Name: "global_blocked", Arguments: `{}`},
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lc.executeToolCalls(toolCalls, 0)
	}()

	results, _ := collectEvents(events, &wg, 2*time.Second, false)

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].IsError {
		t.Fatalf("expected global checker to block (IsError=true), got: %+v", results[0])
	}
	if !strings.Contains(results[0].ToolResult, "全局权限策略拒绝执行") {
		t.Errorf("expected global permission error message, got: %q", results[0].ToolResult)
	}
	if len(lc.messages) != 1 || !strings.Contains(lc.messages[0].Content, "全局权限策略拒绝执行") {
		t.Errorf("expected 1 global-permission error message in history, got %d: %+v", len(lc.messages), lc.messages)
	}
}

// ──────────────────────────────────────────────────────────
// sleepTool — 带延迟的并发安全测试工具
// ──────────────────────────────────────────────────────────

type sleepTool struct {
	name  string
	delay time.Duration
}

func (t *sleepTool) Name() string        { return t.name }
func (t *sleepTool) Aliases() []string   { return nil }
func (t *sleepTool) Description() string { return "sleep tool for testing" }
func (t *sleepTool) Parameters() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{}}
}
func (t *sleepTool) PromptGuide() string { return "" }
func (t *sleepTool) ResultLimit() int    { return 1000 }
func (t *sleepTool) CheckPermission(args string) tool.PermissionResult {
	return tool.PermissionResult{Allow: true}
}
func (t *sleepTool) IsConcurrencySafe(args string) bool { return true }
func (t *sleepTool) IsReadOnly(args string) bool        { return true }
func (t *sleepTool) Execute(args string) (string, error) {
	time.Sleep(t.delay)
	return "done:" + t.name, nil
}
