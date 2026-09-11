package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"tacode/internal/memory"
	"tacode/internal/tool"
	"tacode/internal/ui/text"
)

// newTestSSEServer 启动一个返回固定 SSE 流的 mock server。
func newTestSSEServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// ──────────────────────────────────────────────────────────
// 跨轮消息累积集成测试
//
// 用 httptest mock LLM 驱动真实 queryEngine，跑两轮：
//   轮 1: 输入 "第一个任务" → mock 返回文本答案（无工具调用）
//   轮 2: 输入 "第二个任务" → 断言请求的 messages 包含轮 1 的
//         assistant 回复（跨轮累积生效）
//
// 同时验证前缀缓存友好的消息结构：
//   [0] system（全静态）
//   [1] preamble（<system-reminder> 记忆，仅首轮注入）
//   [2..] 累积对话（轮 1 的 assistant）
//   [last] 轮次 + 用户任务
// ──────────────────────────────────────────────────────────

func TestQueryEngine_CrossRoundAccumulation(t *testing.T) {
	handler := &captureHandler{}
	srv := newTestSSEServer(t, handler)
	defer srv.Close()
	client := newLLMClientViaEnv(t, srv.URL)

	// 组装 Runner 所需的最小依赖。
	dir := t.TempDir()
	history := memory.NewHistoryStore(dir)
	summary := memory.NewSummaryStore(dir)
	memStore := memory.NewMemoryStore(dir)
	events := memory.NewEventStore(dir)
	extractor := memory.NewExtractor(client)
	retriever := memory.NewRetriever(history, summary, memStore, events)
	tools := tool.NewRegistry()
	tools.Register(tool.NewListTool())

	runner := NewRunner(client, history, summary, memStore, events, extractor, retriever, tools, nil, text.NewTextUI())
	compactor := NewCompactor(client, dir+"/transcripts", dir+"/tool-results")
	runner.SetCompactor(compactor)

	ctx := context.Background()

	// ── 轮 1 ──
	answer1, msgs1, err := runner.queryEngine(ctx, 1, "第一个任务：列出当前目录文件", nil, nil)
	if err != nil {
		t.Fatalf("round 1 failed: %v", err)
	}
	if !strings.Contains(answer1, "final answer") {
		t.Fatalf("round 1 answer mismatch: %q", answer1)
	}
	if len(msgs1) < 3 {
		t.Fatalf("round 1 should produce >=3 messages, got %d", len(msgs1))
	}

	// ── 轮 2：传入轮 1 的累积消息 ──
	answer2, msgs2, err := runner.queryEngine(ctx, 2, "第二个任务：继续", nil, msgs1)
	if err != nil {
		t.Fatalf("round 2 failed: %v", err)
	}
	if !strings.Contains(answer2, "final answer") {
		t.Fatalf("round 2 answer mismatch: %q", answer2)
	}
	_ = msgs2

	// ── 断言：轮 2 的请求 messages 包含轮 1 的 assistant 回复 ──
	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.bodies) < 2 {
		t.Fatalf("expected >=2 requests, got %d", len(handler.bodies))
	}
	req2 := handler.bodies[1] // 轮 2 的请求
	msgsAny, ok := req2["messages"].([]any)
	if !ok {
		t.Fatalf("messages should be array, got %T", req2["messages"])
	}

	// 消息结构断言：system 在 [0]；若注入 preamble 则 [1] 是 <system-reminder>；
	// 累积对话在 [2..]；用户任务在末尾。
	if len(msgsAny) < 3 {
		t.Fatalf("round 2 should have system+accumulated+task, got %d messages", len(msgsAny))
	}
	first := msgsAny[0].(map[string]any)
	if first["role"] != "system" {
		t.Fatalf("messages[0] should be system, got %v", first["role"])
	}
	// 若有 preamble，必须带 <system-reminder> 标签。
	if len(msgsAny) >= 4 {
		second := msgsAny[1].(map[string]any)
		if second["role"] == "user" && strings.Contains(second["content"].(string), "<system-reminder>") {
			t.Logf("memory preamble present at messages[1]")
		}
	}
	// 累积对话：轮 1 的 assistant 回复应在 messages 中。
	foundRound1Answer := false
	var lastTask string
	for _, m := range msgsAny {
		mm := m.(map[string]any)
		if mm["role"] == "assistant" {
			if strings.Contains(contentOf(mm), "final answer") {
				foundRound1Answer = true
			}
		}
		if mm["role"] == "user" && strings.Contains(contentOf(mm), "用户任务") {
			lastTask = contentOf(mm)
		}
	}
	if !foundRound1Answer {
		t.Fatalf("round 2 request must include round 1's assistant reply (cross-round accumulation broken)")
	}
	if !strings.Contains(lastTask, "第二个任务") {
		t.Fatalf("round 2 task should be last user message, got %q", lastTask)
	}
	// 轮次号在末尾，不在开头（前缀缓存友好）。
	if strings.Contains(first["content"].(string), "轮次: 2") || strings.Contains(first["content"].(string), "轮次: 1") {
		t.Fatalf("system prompt must not contain round number")
	}
}

// contentOf 提取消息 content（字符串或 JSON 字符串）。
func contentOf(m map[string]any) string {
	switch v := m["content"].(type) {
	case string:
		return v
	case []any:
		var sb strings.Builder
		for _, part := range v {
			if s, ok := part.(string); ok {
				sb.WriteString(s)
			}
		}
		return sb.String()
	default:
		return ""
	}
}

// captureHandler 捕获所有请求体（跨轮断言用）。
type captureHandler struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (h *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// 余额查询请求（每轮自动更新触发的 /user/balance）：
	// 返回 500 让余额查询失败静默，不捕获进 bodies（避免污染 LLM 请求断言）。
	if r.URL.Path == "/user/balance" {
		http.Error(w, `{"error":"mock balance unavailable"}`, http.StatusInternalServerError)
		return
	}

	body, _ := io.ReadAll(r.Body)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	h.mu.Lock()
	h.bodies = append(h.bodies, parsed)
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, stopResponseSSE())
}

// /stop 主动取消后，queryEngine 收到的流式错误（receive stream failed）应静默：
// 不 OnError、不返回 error（取消的预期副作用，非真实失败）。
func TestQueryEngine_CancelledSilencesError(t *testing.T) {
	handler := &captureHandler{}
	srv := newTestSSEServer(t, handler)
	defer srv.Close()
	client := newLLMClientViaEnv(t, srv.URL)

	dir := t.TempDir()
	history := memory.NewHistoryStore(dir)
	summary := memory.NewSummaryStore(dir)
	memStore := memory.NewMemoryStore(dir)
	events := memory.NewEventStore(dir)
	extractor := memory.NewExtractor(client)
	retriever := memory.NewRetriever(history, summary, memStore, events)
	tools := tool.NewRegistry()
	tools.Register(tool.NewListTool())

	runner := NewRunner(client, history, summary, memStore, events, extractor, retriever, tools, nil, text.NewTextUI())
	compactor := NewCompactor(client, dir+"/transcripts", dir+"/tool-results")
	runner.SetCompactor(compactor)

	// 取消的 context（模拟 /stop）。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := runner.queryEngine(ctx, 1, "任务", nil, nil)
	if err != nil {
		t.Errorf("取消后 queryEngine 不应返回 error（静默），got: %v", err)
	}
}
