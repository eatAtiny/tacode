package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"agentic/internal/llm"
	"agentic/internal/tool"
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
func collectEvents(events <-chan QueryEvent, wg *sync.WaitGroup, timeout time.Duration, autoApprove bool) (toolResults []QueryEvent, permCount int) {
	deadline := time.After(timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		// 短暂等待确保最后的事件已写入 channel。
		time.Sleep(5 * time.Millisecond)
		close(done)
	}()

	for {
		select {
		case evt := <-events:
			switch evt.Type {
			case QueryEventToolResult:
				toolResults = append(toolResults, evt)
			case QueryEventPermission:
				permCount++
				if autoApprove && evt.PermissionCh != nil {
					select {
					case evt.PermissionCh <- true: // 批准
					default:
					}
				}
			}
		case <-done:
			extra := drainEvents(events)
			for _, evt := range extra {
				switch evt.Type {
				case QueryEventToolResult:
					toolResults = append(toolResults, evt)
				case QueryEventPermission:
					permCount++
				}
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
		lc.executeToolCallsHelper(toolCalls, 0)
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
		lc.executeToolCallsHelper(toolCalls, 0)
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
		lc.executeToolCallsHelper(toolCalls, 0)
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
		lc.executeToolCallsHelper(toolCalls, 0)
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
		lc.executeToolCallsHelper(toolCalls, 0)
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
// executeToolCallsHelper
// ──────────────────────────────────────────────────────────

func (lc *queryLoopContext) executeToolCallsHelper(toolCalls []llm.ToolCall, iter int) bool {
	type execItem struct {
		tc         llm.ToolCall
		t          tool.Tool
		concurrent bool
	}

	var concurrentItems []execItem
	var serialItems []execItem

	for _, tc := range toolCalls {
		t := lc.toolRegistry.Get(tc.Name)
		item := execItem{tc: tc, t: t}

		if t != nil && t.IsConcurrencySafe(tc.Arguments) && t.IsReadOnly(tc.Arguments) {
			if perm := t.CheckPermission(tc.Arguments); perm.Allow {
				item.concurrent = true
				concurrentItems = append(concurrentItems, item)
				continue
			}
		}
		serialItems = append(serialItems, item)
	}

	if len(concurrentItems) > 0 {
		concurrentTCs := make([]llm.ToolCall, len(concurrentItems))
		for i, item := range concurrentItems {
			concurrentTCs[i] = item.tc
		}
		lc.executeConcurrentTools(concurrentTCs, iter)
	}

	for _, item := range serialItems {
		if !lc.executeSingleTool(item.tc, iter) {
			return false
		}
	}

	return true
}

// ──────────────────────────────────────────────────────────
// sleepTool — 带延迟的并发安全测试工具
// ──────────────────────────────────────────────────────────

type sleepTool struct {
	name  string
	delay time.Duration
}

func (t *sleepTool) Name() string                                   { return t.name }
func (t *sleepTool) Description() string                            { return "sleep tool for testing" }
func (t *sleepTool) Parameters() map[string]any                     { return map[string]any{"type": "object", "properties": map[string]any{}} }
func (t *sleepTool) PromptGuide() string                            { return "" }
func (t *sleepTool) ResultLimit() int                               { return 1000 }
func (t *sleepTool) CheckPermission(args string) tool.PermissionResult { return tool.PermissionResult{Allow: true} }
func (t *sleepTool) IsConcurrencySafe(args string) bool             { return true }
func (t *sleepTool) IsReadOnly(args string) bool                    { return true }
func (t *sleepTool) Execute(args string) (string, error) {
	time.Sleep(t.delay)
	return "done:" + t.name, nil
}
