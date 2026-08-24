package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agentic/internal/llm"
)

// ──────────────────────────────────────────────────────────
// P2⑥: compressMessages 保真改进
//
// 旧实现的问题：
//  1. 早期 tool 结果的完整内容被整体丢弃，不可逆（LLM 想回读只能重调工具）
//  2. 成功/失败判断靠 strings.Contains(msg.Content, "出错"/"错误") 猜，
//     工具输出含"错误码说明"字样也会被判失败
//
// 新行为：
//  1. tool 结果压缩为中性占位符（不再猜测状态）
//  2. 压缩时把完整结果持久化到 tool-results 目录，占位符带回读提示
// ──────────────────────────────────────────────────────────

// buildCompressibleMessages 构造触发压缩的消息序列。
// 序列：[system] + [user] + [assistant(tool_calls)] + [tool(result)] + [assistant] + [user] + [assistant(tool_calls)] + [tool(result)]
// 共 8 条，compressEnd = 8-4 = 4 → 索引 1-3 被压缩（user 保留原样，assistant+tool 被占位）。
func buildCompressibleMessages() []llm.ChatMessage {
	return []llm.ChatMessage{
		{Role: "system", Content: "system prompt"},
		{Role: "user", Content: "user round 1"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c1", Name: "shell", Arguments: `{"command":"ls"}`}}},
		{Role: "tool", ToolCallID: "c1", Content: "file1\nfile2"},
		{Role: "assistant", Content: "thinking"},
		{Role: "user", Content: "user round 2"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "c2", Name: "grep", Arguments: `{"pattern":"x"}`}}},
		{Role: "tool", ToolCallID: "c2", Content: "result2"},
	}
}

func TestCompressMessages_NeutralToolStatus(t *testing.T) {
	messages := buildCompressibleMessages()
	compressed := compressMessages(messages, 0)

	if len(compressed) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(compressed))
	}

	// 压缩后的 tool 消息应是中性占位符：不再猜"成功/失败"。
	for i, msg := range compressed {
		if msg.Role != "tool" {
			continue
		}
		if msg.Content == "file1\nfile2" || msg.Content == "result2" {
			// 最近 2 轮 tool 结果保留原样。
			continue
		}
		if strings.Contains(msg.Content, "成功") || strings.Contains(msg.Content, "失败") {
			t.Errorf("compressed tool message[%d] should be neutral, got %q", i, msg.Content)
		}
		if !strings.HasPrefix(msg.Content, "[已压缩]") {
			t.Errorf("compressed tool message[%d] should start with [已压缩], got %q", i, msg.Content)
		}
	}
}

func TestCompressMessages_PreservesRecentRounds(t *testing.T) {
	messages := buildCompressibleMessages()
	compressed := compressMessages(messages, 0)

	// 最近 2 轮（最后 4 条）应完整保留。
	tail := compressed[len(compressed)-4:]
	if tail[0].Content != "thinking" ||
		tail[1].Content != "user round 2" ||
		len(tail[2].ToolCalls) != 1 || tail[2].ToolCalls[0].Name != "grep" ||
		tail[3].Content != "result2" {
		t.Errorf("recent rounds should be preserved verbatim, got %+v", tail)
	}
}

func TestCompressMessages_TooFewMessages(t *testing.T) {
	// 少于 4 条消息不压缩。
	messages := []llm.ChatMessage{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "u"},
		{Role: "assistant", Content: "a"},
	}
	if got := compressMessages(messages, 0); len(got) != 3 {
		t.Errorf("too few messages should not compress, got %d", len(got))
	}
}

// validateToolPairing 校验消息数组满足 OpenAI API 约束：
// 每条 role=tool 的消息前，必须有一条带匹配 ToolCalls 的 assistant 消息。
// 否则 API 返回 400 "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"。
func validateToolPairing(t *testing.T, messages []llm.ChatMessage) {
	t.Helper()
	for i, msg := range messages {
		if msg.Role != "tool" {
			continue
		}
		// 向前找最近的 assistant 消息。
		found := false
		for j := i - 1; j >= 0; j-- {
			prev := messages[j]
			if prev.Role == "assistant" {
				// assistant 必须带 ToolCalls，且包含匹配的 tool_call_id。
				for _, tc := range prev.ToolCalls {
					if tc.ID == msg.ToolCallID {
						found = true
						break
					}
				}
				break // 只看最近一条 assistant
			}
		}
		if !found {
			t.Errorf("message[%d] role=tool (ToolCallID=%s) has no preceding assistant with matching tool_calls\nmessages: %+v",
				i, msg.ToolCallID, messages)
		}
	}
}

func TestCompressMessages_PreservesToolPairing(t *testing.T) {
	// 压缩后所有 tool 消息仍有配对的 assistant（不产生孤儿 → 不触发 API 400）。
	messages := buildCompressibleMessages()
	compressed := compressMessages(messages, 0)
	validateToolPairing(t, compressed)
}

func TestCompressMessagesWithPersistence_PreservesToolPairing(t *testing.T) {
	// 带持久化的压缩路径同样必须保持配对。
	dir := t.TempDir()
	lc := &queryLoopContext{}
	compressed := lc.compressMessagesWithPersistence(buildCompressibleMessages(), 0, dir)
	validateToolPairing(t, compressed)
}

func TestCompressMessages_PersistsToolResults(t *testing.T) {
	// 集成：压缩时把被压缩的 tool 结果持久化到目录，占位符带回读提示。
	dir := t.TempDir()
	lc := &queryLoopContext{}
	compressed := lc.compressMessagesWithPersistence(buildCompressibleMessages(), 0, dir)

	// 早前的 tool 结果（"file1\nfile2"）应被持久化，占位符带文件路径提示。
	var placeholder string
	for _, msg := range compressed {
		if msg.Role == "tool" && strings.HasPrefix(msg.Content, "[已压缩]") {
			placeholder = msg.Content
			break
		}
	}
	if placeholder == "" {
		t.Fatal("expected a compressed tool placeholder")
	}
	if !strings.Contains(placeholder, "完整内容已保存到") {
		t.Errorf("placeholder should hint at persisted file, got %q", placeholder)
	}

	// 持久化文件应存在且内容完整。
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read tool-results dir: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one persisted tool result file")
	}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "file1") {
			t.Logf("✅ 被压缩的工具结果已持久化: %s (%d bytes)", e.Name(), len(data))
			return
		}
	}
	t.Error("persisted file should contain the compressed tool result content")
}
