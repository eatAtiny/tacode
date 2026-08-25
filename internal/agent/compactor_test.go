package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentic/internal/llm"
)

// newTestCompactor 构造使用临时目录的 Compactor（llmClient = nil，跳过摘要）。
func newTestCompactor(t *testing.T) *Compactor {
	t.Helper()
	dir := t.TempDir()
	return NewCompactor(nil, filepath.Join(dir, "transcripts"), filepath.Join(dir, "tool-results"))
}

// msg 快捷构造消息。
func msg(role, content string) llm.ChatMessage {
	return llm.ChatMessage{Role: role, Content: content}
}

// assistantWithTools 构造带工具调用的 assistant 消息。
func assistantWithTools(id string, names ...string) llm.ChatMessage {
	var tcs []llm.ToolCall
	for i, n := range names {
		tcs = append(tcs, llm.ToolCall{ID: id, Name: n, Arguments: "{}"})
		_ = i
	}
	return llm.ChatMessage{Role: "assistant", Content: "调用工具", ToolCalls: tcs}
}

// toolResult 构造工具结果消息。
func toolResult(id, content string) llm.ChatMessage {
	return llm.ChatMessage{Role: "tool", Content: content, ToolCallID: id}
}

// makeConversation 构造 system + 累积对话（assistant/tool 配对）。
func makeConversation(rounds int, toolContent string) []llm.ChatMessage {
	msgs := []llm.ChatMessage{msg("system", "你是一个 AI 助手。")}
	for i := 0; i < rounds; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, assistantWithTools(id, "file"))
		msgs = append(msgs, toolResult(id, toolContent))
	}
	msgs = append(msgs, msg("user", "轮次: 1\n\n用户任务:\n你好"))
	return msgs
}

// ── toolResultBudget ──

func TestToolResultBudget_PersistsLargeResults(t *testing.T) {
	c := newTestCompactor(t)
	big := strings.Repeat("x", largeResultCharLimit+1000) // >30K
	small := strings.Repeat("y", 1000)

	// 最后一批：一个 assistant 带 7 个 tool_calls + 7 条大结果，总量 >200K。
	// Go 模型：同一批次的所有 tool 结果是连续的 [tool, tool, ...]。
	// toolResultBudget 只处理"最新一批"（最后一个 assistant(ToolCalls) 之后）。
	msgs := []llm.ChatMessage{msg("system", "s")}
	// 前置一个普通小批次（非最新，不受影响）。
	msgs = append(msgs, assistantWithTools("prev", "file"))
	msgs = append(msgs, toolResult("prev", small))
	// 最新一批：7 条大结果。
	ids := make([]string, 7)
	for i := 0; i < 7; i++ {
		ids[i] = fmt.Sprintf("big%d", i)
	}
	msgs = append(msgs, assistantWithTools("batch1", ids...))
	for _, id := range ids {
		msgs = append(msgs, toolResult(id, big))
	}

	out := c.toolResultBudget(msgs)
	// 最新一批的大结果被转存；前置的小结果保留。
	persisted := 0
	intact := 0
	for _, m := range out {
		if m.Role != "tool" {
			continue
		}
		if strings.Contains(m.Content, "<persisted-output>") {
			persisted++
		} else if m.Content == small {
			intact++
		}
	}
	if persisted == 0 {
		t.Fatalf("expected some large results persisted, got 0")
	}
	if intact != 1 {
		t.Fatalf("small result should stay intact")
	}
	// 校验占位符指向的文件存在。
	for _, m := range out {
		if m.Role == "tool" && strings.Contains(m.Content, "<persisted-output>") {
			path := c.persistedOutputPath(m.Content)
			if path == "" || !fileExists(path) {
				t.Fatalf("persisted file should exist: %s", path)
			}
		}
	}
}

func TestToolResultBudget_UnderLimitNoChange(t *testing.T) {
	c := newTestCompactor(t)
	msgs := []llm.ChatMessage{
		msg("system", "s"),
		assistantWithTools("a1", "file"),
		toolResult("a1", strings.Repeat("y", 1000)),
	}
	out := c.toolResultBudget(msgs)
	if !strings.Contains(out[2].Content, "y") {
		t.Fatalf("under-limit results should stay intact")
	}
}

// ── snipCompact ──

func TestSnipCompact_ArchivesMiddle(t *testing.T) {
	c := newTestCompactor(t)
	// 60 轮 → 121 条消息，远超 50。
	msgs := makeConversation(60, strings.Repeat("z", 100))
	if len(msgs) <= snipMaxMessages {
		t.Fatalf("test setup wrong: %d messages", len(msgs))
	}
	out := c.snipCompact(msgs)
	// 配对保护可能使保留数略超 50（保护优先于严格数量），但应远小于原始。
	if len(out) > snipMaxMessages+2 {
		t.Fatalf("snipCompact should cap near %d, got %d", snipMaxMessages, len(out))
	}
	// 找到归档标记。
	foundMarker := false
	for _, m := range out {
		if m.Role == "user" && strings.Contains(m.Content, "messages archived at") {
			foundMarker = true
			// 从 marker 提取 transcript 路径（数字由实际归档数决定）。
			marker := m.Content
			open := strings.Index(marker, " at ")
			if open < 0 {
				t.Fatalf("marker malformed: %s", marker)
			}
			path := strings.TrimSuffix(marker[open+4:], "]")
			if !fileExists(path) {
				t.Fatalf("transcript file should exist: %s", path)
			}
		}
	}
	if !foundMarker {
		t.Fatalf("archive marker not found")
	}
}

func TestSnipCompact_PairingProtection(t *testing.T) {
	c := newTestCompactor(t)
	// 60 轮完整配对。
	msgs := makeConversation(60, strings.Repeat("z", 50))
	// 构造：在 index 1（第一个 assistant）处安排多 tool_call 批次。
	// 原 messages[1] = assistant(c0, 1 tool_call)，messages[2] = tool(c0)。
	// 改为 2 个 tool_calls + 2 个 tool 结果（配对完整）。
	msgs[1] = assistantWithTools("batch-x", "file", "grep")
	msgs[2] = toolResult("batch-x", "第一个结果")
	msgs = append(msgs[:3], append([]llm.ChatMessage{toolResult("batch-x", "第二个结果")}, msgs[3:]...)...)

	out := c.snipCompact(msgs)
	// 校验：任何 assistant 带 ToolCalls 的消息后必须紧跟其全部 tool 结果（配对完整）。
	for i, m := range out {
		if hasToolCalls(m) {
			expected := len(m.ToolCalls)
			got := 0
			for j := i + 1; j < len(out) && out[j].Role == "tool"; j++ {
				got++
			}
			if got != expected {
				t.Fatalf("pairing broken at index %d: assistant has %d tool_calls but %d tool messages follow", i, expected, got)
			}
		}
	}
}

// ── microCompact ──

func TestMicroCompact_ShortensConsumedResults(t *testing.T) {
	c := newTestCompactor(t)
	long := strings.Repeat("L", 500)
	short := strings.Repeat("S", 50)
	// 5 轮：每轮 assistant + tool（长内容）。末尾追加一个未闭合 assistant，
	// 使最后 0 条 tool 为未读（全部已消费）。
	msgs := makeConversation(5, long)
	msgs = append(msgs, assistantWithTools("open-x", "grep")) // 未闭合批次，无 tool 跟随
	unseen := trailingUnseenToolIDs(msgs)
	if len(unseen) != 0 {
		t.Fatalf("expected 0 unseen, got %d", len(unseen))
	}
	// 把最后一条 tool 变成 short（<120 不动）。
	msgs[len(msgs)-2] = toolResult(msgs[len(msgs)-2].ToolCallID, short)

	target := 1000 // 低目标，强制压缩
	out := c.microCompact(msgs, target)
	// 保留最近 3 条已消费；更早的长内容被替换。
	replaced := 0
	for _, m := range out {
		if m.Role == "tool" && strings.HasPrefix(m.Content, "[Earlier tool result saved at ") {
			replaced++
		}
	}
	if replaced < 1 {
		t.Fatalf("expected >=1 shortened results, got %d", replaced)
	}
}

func TestMicroCompact_KeepsRecentResults(t *testing.T) {
	c := newTestCompactor(t)
	long := strings.Repeat("L", 500)
	msgs := makeConversation(6, long) // 6 条已消费
	// 目标足够大，只压缩最早的部分。
	target := c.limit() // 不触发缩短
	out := c.microCompact(msgs, target)
	changed := false
	for _, m := range out {
		if strings.HasPrefix(m.Content, "[Earlier tool result saved at ") {
			changed = true
		}
	}
	if changed {
		t.Fatalf("with large target, no results should be shortened")
	}
	// 最近 3 条绝不缩短（即使目标很小）。
	msgs2 := makeConversation(6, long)
	out2 := c.microCompact(msgs2, 1)
	lastThree := out2[len(out2)-4 : len(out2)-1] // 排除末尾 user 任务
	for _, m := range lastThree {
		if strings.HasPrefix(m.Content, "[Earlier tool result saved at ") {
			t.Fatalf("recent results must be kept: %s", m.Content)
		}
	}
}

// ── fitToolResults ──

func TestFitToolResults_LargestFirst(t *testing.T) {
	c := newTestCompactor(t)
	huge := strings.Repeat("H", 20000)
	medium := strings.Repeat("M", 5000)
	msgs := []llm.ChatMessage{
		msg("system", "s"),
		assistantWithTools("x1", "file"),
		toolResult("x1", medium),
		assistantWithTools("x2", "file"),
		toolResult("x2", huge),
	}
	out := c.fitToolResults(msgs, 1) // 目标极小，全压
	// 最大优先：huge 应先被转存。
	hugeIdx, mediumIdx := -1, -1
	for i, m := range out {
		if m.ToolCallID == "x2" {
			hugeIdx = i
		}
		if m.ToolCallID == "x1" {
			mediumIdx = i
		}
	}
	if hugeIdx == -1 || mediumIdx == -1 {
		t.Fatalf("tool messages missing")
	}
	if !strings.Contains(out[hugeIdx].Content, "<persisted-output>") {
		t.Fatalf("huge result should be persisted")
	}
	// 占位符不应变长。
	if len(out[hugeIdx].Content) >= len(huge) {
		t.Fatalf("replacement should be shorter than original")
	}
}

// ── compactHistory / reactiveCompact（llmClient=nil 时摘要不可用但结构正确） ──

func TestCompactHistory_PreservesSystemAndSummarizes(t *testing.T) {
	c := newTestCompactor(t)
	msgs := makeConversation(3, strings.Repeat("z", 100))
	out := c.compactHistory(msgs, "测试请求")
	if len(out) != 2 {
		t.Fatalf("compactHistory should return [system, summary], got %d", len(out))
	}
	if out[0].Role != "system" {
		t.Fatalf("system must be preserved")
	}
	if out[1].Role != "user" || !strings.Contains(out[1].Content, "[Compacted]") {
		t.Fatalf("summary message should be [Compacted], got: %.80s", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "Current user request:\n测试请求") {
		t.Fatalf("active request must be injected")
	}
}

func TestReactiveCompact_KeepsLast5(t *testing.T) {
	c := newTestCompactor(t)
	msgs := makeConversation(10, strings.Repeat("z", 50)) // 21 条
	out := c.reactiveCompact(msgs, "当前请求")
	// out = [system, summary, ...tail]
	if out[0].Role != "system" {
		t.Fatalf("system must be first")
	}
	if !strings.Contains(out[1].Content, "[Reactive compact]") {
		t.Fatalf("summary message should be [Reactive compact]")
	}
	if len(out) > 7 { // system + summary + 最近 5 条
		t.Fatalf("reactiveCompact should keep ~5 tail messages, got %d", len(out))
	}
	// 最近一条 user 任务保留。
	last := out[len(out)-1]
	if !strings.Contains(last.Content, "用户任务") {
		t.Fatalf("latest user task should be preserved, got: %.60s", last.Content)
	}
}

// ── Prepare 顺序与幂等 ──

func TestPrepare_UnderLimitNoChange(t *testing.T) {
	c := newTestCompactor(t)
	msgs := makeConversation(2, strings.Repeat("z", 100))
	before := estimateChars(msgs)
	out := c.Prepare(msgs, "测试")
	after := estimateChars(out)
	if before != after {
		t.Fatalf("under-limit messages should not change: %d -> %d", before, after)
	}
}

// ── 辅助 ──

func TestIsToolPairBoundaryHelpers(t *testing.T) {
	if !hasToolCalls(assistantWithTools("a", "file")) {
		t.Fatalf("assistant with tool calls should be detected")
	}
	if hasToolCalls(msg("user", "hi")) {
		t.Fatalf("user message should not be a tool pair start")
	}
}

func TestIsTooLongError(t *testing.T) {
	if !isTooLongError(os.ErrNotExist) {
		// 普通错误不是 too long
	}
	if !isTooLongError(errTooLong) {
		t.Fatalf("prompt_too_long should be detected")
	}
	if isTooLongError(nil) {
		t.Fatalf("nil should not be too long")
	}
}

var errTooLong = &apiTooLongError{}

type apiTooLongError struct{}

func (e *apiTooLongError) Error() string { return "Error 400: prompt_too_long" }

// fileExists 判断文件是否存在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
