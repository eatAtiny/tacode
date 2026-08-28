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
	"time"

	"agentic/internal/llm"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// P0①: generateFinalSummary 不再向 LLM 传递工具定义
//
// 回归背景：generateFinalSummary 是 maxIter 耗尽后的兜底回答。
// 旧实现仍传入 lc.tools，LLM 可能再次返回 tool_calls，而该函数的
// 事件循环忽略 done 事件里的 toolCalls → finalContent 为空 → 空答案。
//
// 修复后要求：兜底调用不带任何工具定义，从根上杜绝再次调工具。
// 本测试用 httptest.Server 捕获请求体，断言 tools 字段为空。
// ──────────────────────────────────────────────────────────

// sseHandler 处理流式请求：捕获请求体并返回固定 SSE 流（stop finish）。
type sseHandler struct {
	mu       sync.Mutex
	bodies   []map[string]any
	response string
}

func (h *sseHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)

	h.mu.Lock()
	h.bodies = append(h.bodies, parsed)
	h.mu.Unlock()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, h.response)
}

// stopResponseSSE 生成一个 finish_reason=stop 的流式响应（带 usage）。
func stopResponseSSE() string {
	return "data: " + `{"id":"chatcmpl-test","object":"chat.completion.chunk","created":1,` +
		`"model":"gpt-4o-mini","choices":[{"index":0,` +
		`"delta":{"content":"final answer","role":"assistant"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":5}}` + "\n\n" +
		"data: [DONE]\n\n"
}

// newLLMClientViaEnv 通过环境变量构造 LLM 客户端（走公共 API），
// 把 OPENAI_BASE_URL 指向 mock server。
func newLLMClientViaEnv(t *testing.T, serverURL string) *llm.OpenAIClient {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", serverURL)
	t.Setenv("OPENAI_MODEL", "gpt-4o-mini")
	t.Setenv("OPENAI_CONTEXT_LIMIT", "128000")
	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		t.Fatalf("NewOpenAIClientFromEnv failed: %v", err)
	}
	return client
}

// runGenerateFinalSummary 运行 generateFinalSummary，返回最终答案文本。
// generateFinalSummary 是同步调用（事件 channel 不会被关闭），
// 因此用带超时的非阻塞读取收集事件。
//
// withTools 控制是否预置工具定义：旧实现（传 lc.tools）在有工具定义时
// 会把 tools 带上请求 → 测试应失败；修复后（传 nil）→ 测试通过。
func runGenerateFinalSummary(t *testing.T, client *llm.OpenAIClient, messages []llm.ChatMessage, withTools bool) string {
	t.Helper()
	events := make(chan QueryEvent, 16)
	lc := &queryLoopContext{
		ctx:       context.Background(),
		llmClient: client,
		messages:  messages,
		maxIter:   10,
		events:    events,
	}
	if withTools {
		lc.tools = []openai.Tool{
			{
				Type: openai.ToolTypeFunction,
				Function: &openai.FunctionDefinition{
					Name:        "shell",
					Description: "run a shell command",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		}
	}
	lc.generateFinalSummary()

	var finalContent string
	drained := 0
	for {
		select {
		case evt := <-events:
			// 钉住 Think yield 修复（2d591d7）：总结路径首个事件必须是
			// QueryEventThink（inline UI 依赖它重置 streamed，删除该 yield
			// 会导致 final 的 glamour 重印分支被跳过、总结文本不上屏）。
			if drained == 0 && evt.Type != QueryEventThink {
				t.Fatalf("首个排空的事件应为 QueryEventThink（总结轮重置流式状态），实际 %v", evt.Type)
			}
			drained++
			if evt.Type == QueryEventFinal {
				finalContent = evt.Content
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for QueryEventFinal")
		default:
			return finalContent
		}
	}
}

func TestGenerateFinalSummary_NoTools(t *testing.T) {
	handler := &sseHandler{response: stopResponseSSE()}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newLLMClientViaEnv(t, srv.URL)
	finalContent := runGenerateFinalSummary(t, client, []llm.ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "user"},
	}, true)

	if finalContent != "final answer" {
		t.Errorf("final answer mismatch: got %q, want %q", finalContent, "final answer")
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.bodies) != 1 {
		t.Fatalf("expected exactly 1 request, got %d", len(handler.bodies))
	}
	reqBody := handler.bodies[0]

	toolsField, hasTools := reqBody["tools"]
	if hasTools {
		if toolsField != nil {
			t.Errorf("request must not carry tool definitions, got tools=%v", toolsField)
		}
		// tools 字段存在但为 null：等价于未传，可以接受。
		t.Logf("tools field present but null (acceptable)")
	}
}

func TestGenerateFinalSummary_AppendsStopPrompt(t *testing.T) {
	handler := &sseHandler{response: stopResponseSSE()}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newLLMClientViaEnv(t, srv.URL)
	runGenerateFinalSummary(t, client, []llm.ChatMessage{
		{Role: "system", Content: "system"},
		{Role: "user", Content: "user"},
	}, true)

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.bodies) != 1 {
		t.Fatalf("expected 1 request, got %d", len(handler.bodies))
	}
	reqBody := handler.bodies[0]

	msgsAny, ok := reqBody["messages"].([]any)
	if !ok {
		t.Fatalf("messages should be an array, got %T", reqBody["messages"])
	}
	if len(msgsAny) != 3 {
		t.Fatalf("expected 3 messages (system+user+stop prompt), got %d", len(msgsAny))
	}
	last := msgsAny[len(msgsAny)-1].(map[string]any)
	lastContent, _ := last["content"].(string)
	if !strings.Contains(lastContent, "不要再调用工具") {
		t.Errorf("last message should be the stop prompt, got %q", lastContent)
	}
}

// ──────────────────────────────────────────────────────────
// 流式请求的 token 精确统计
//
// 背景：ChatWithToolsStream 若未设置 stream_options.include_usage，
// OpenAI 流式响应不返回 usage 字段 → InputTokens/OutputTokens 恒 0，
// 累计 token 统计恒为空。
// 本测试断言流式请求必须带 include_usage=true。
// ──────────────────────────────────────────────────────────

func TestStreamRequest_IncludesUsage(t *testing.T) {
	handler := &sseHandler{response: stopResponseSSE()}
	srv := httptest.NewServer(handler)
	defer srv.Close()

	client := newLLMClientViaEnv(t, srv.URL)

	// 触发一次流式请求（走 ChatWithToolsStream）。
	events := make(chan QueryEvent, 16)
	lc := &queryLoopContext{
		ctx:       context.Background(),
		llmClient: client,
		messages: []llm.ChatMessage{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "u"},
		},
		maxIter: 10,
		events:  events,
	}
	// 用 callLLMStream 走流式路径。
	go func() {
		lc.callLLMStream(0)
		close(events)
	}()
	for range events {
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	if len(handler.bodies) != 1 {
		t.Fatalf("expected 1 request, got %d", len(handler.bodies))
	}
	reqBody := handler.bodies[0]

	streamOpts, ok := reqBody["stream_options"].(map[string]any)
	if !ok {
		t.Fatalf("stream_options should be present, got %v", reqBody["stream_options"])
	}
	includeUsage, _ := streamOpts["include_usage"].(bool)
	if !includeUsage {
		t.Error("stream_options.include_usage must be true for token tracking")
	}

	// 响应侧断言：mock 响应携带 usage.prompt_tokens=10，drainStream 应把它
	// 精确落地到 lc.lastInputTokens（值不符说明落地逻辑有误）。
	if lc.lastInputTokens != 10 {
		t.Errorf("lastInputTokens must equal stream usage prompt_tokens (10), got %d", lc.lastInputTokens)
	}
}

func TestPrepareIfNeeded_NoopWithoutCompactor(t *testing.T) {
	// compactor 为 nil 时 prepareIfNeeded 是 no-op（保持旧行为）。
	lc := &queryLoopContext{
		messages: []llm.ChatMessage{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "u"},
		},
		events: make(chan QueryEvent, 16),
	}
	if !lc.prepareIfNeeded(0) {
		t.Fatal("expected no-op to return true")
	}
	if len(lc.messages) != 2 {
		t.Errorf("messages should be unchanged without compactor")
	}
}

func TestPrepareIfNeeded_NoopForTinyMessages(t *testing.T) {
	// 注入 compactor 但消息很小（未超限）→ prepare 返回不变。
	lc := &queryLoopContext{
		compactor: newTestCompactor(t),
		messages: []llm.ChatMessage{
			{Role: "system", Content: "s"},
			{Role: "user", Content: "u"},
		},
		events: make(chan QueryEvent, 16),
	}
	if !lc.prepareIfNeeded(0) {
		t.Fatal("expected prepare to return true")
	}
	if len(lc.messages) != 2 {
		t.Errorf("tiny messages should not be compressed, got %d", len(lc.messages))
	}
}

func TestPrepareIfNeeded_CompactsOverLimit(t *testing.T) {
	// 消息超限（字符数 > contextCharLimit）→ prepare 触发压缩（消息数减少）。
	lc := &queryLoopContext{
		compactor: newTestCompactor(t),
		messages:  makeConversation(200, strings.Repeat("z", 2000)), // 大量长结果，远超 50K
		events:    make(chan QueryEvent, 16),
	}
	if !lc.prepareIfNeeded(0) {
		t.Fatal("expected prepare to return true")
	}
	if len(lc.messages) > snipMaxMessages+2 {
		t.Errorf("messages should be compacted near %d, got %d", snipMaxMessages, len(lc.messages))
	}
}
