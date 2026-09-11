package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"tacode/internal/llm"
	"tacode/internal/memory"
	"tacode/internal/ui/text"
)

// ──────────────────────────────────────────────────────────
// P2⑧: 跨会话记忆分流
//
// extractMemory 处理 LLM 提取结果时，按类型分流：
//   - project / reference → 全局 store（跨会话共享）
//   - user / feedback     → 当前会话 store
// ──────────────────────────────────────────────────────────

// mockExtractorLLM 构造返回固定 ExtractionResult JSON 的 LLM 客户端。
func mockExtractorLLM(t *testing.T, result *memory.ExtractionResult) *llm.OpenAIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(result)
		bodyStr, _ := json.Marshal(string(body))
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":`+string(bodyStr)+`},"finish_reason":"stop"}],
			"usage":{"prompt_tokens":10,"completion_tokens":5}
		}`)
	}))
	t.Cleanup(srv.Close)

	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_MODEL", "gpt-4o-mini")
	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		t.Fatalf("NewOpenAIClientFromEnv failed: %v", err)
	}
	return client
}

func TestExtractMemory_RoutesGlobalAndProject(t *testing.T) {
	// 三个独立目录：会话 store + 全局 store（user 类）+ 项目 store（project 类）。
	sessionDir := t.TempDir()
	globalDir := t.TempDir()
	projectDir := t.TempDir()

	history := memory.NewHistoryStore(sessionDir)
	summary := memory.NewSummaryStore(sessionDir)
	sessionMem := memory.NewMemoryStore(sessionDir)
	globalMem := memory.NewMemoryStore(globalDir)
	projectMem := memory.NewMemoryStore(projectDir)
	events := memory.NewEventStore(sessionDir)
	retriever := memory.NewRetriever(history, summary, sessionMem, events)

	client := mockExtractorLLM(t, &memory.ExtractionResult{
		Summary: "本轮对话摘要",
		Memories: []memory.MemoryAction{
			{Action: "create", Name: "project-convention", Type: "project", Importance: 4, Content: "用中文注释"},
			{Action: "create", Name: "user-pref", Type: "user", Importance: 3, Content: "喜欢简洁回答"},
		},
	})

	r := &Runner{
		llm:        client,
		history:    history,
		summary:    summary,
		memStore:   sessionMem,
		globalMem:  globalMem,
		projectMem: projectMem,
		events:     events,
		retriever:  retriever,
		extractor:  memory.NewExtractor(client),
		ui:         text.NewTextUI(),
	}

	r.extractMemory(context.Background(), 1, "帮我写代码", "好的")

	// 项目级记忆 → 项目 store。
	if entry := projectMem.GetEntry("project-convention"); entry == nil {
		t.Error("project memory should be saved to project store")
	}
	// user 级记忆 → 全局 store。
	if entry := globalMem.GetEntry("user-pref"); entry == nil {
		t.Error("user memory should be saved to global store")
	}
	// 交叉泄漏检查。
	if entry := globalMem.GetEntry("project-convention"); entry != nil {
		t.Error("project memory should NOT leak to global store")
	}
	if entry := projectMem.GetEntry("user-pref"); entry != nil {
		t.Error("user memory should NOT leak to project store")
	}
	if entry := sessionMem.GetEntry("user-pref"); entry != nil {
		t.Error("user memory should NOT be in session store")
	}
}

func TestExtractMemory_NoStores_AllInSession(t *testing.T) {
	// 未启用全局/项目 store：所有记忆写入会话 store（保持旧行为）。
	sessionDir := t.TempDir()
	history := memory.NewHistoryStore(sessionDir)
	summary := memory.NewSummaryStore(sessionDir)
	sessionMem := memory.NewMemoryStore(sessionDir)
	events := memory.NewEventStore(sessionDir)
	retriever := memory.NewRetriever(history, summary, sessionMem, events)

	client := mockExtractorLLM(t, &memory.ExtractionResult{
		Summary: "摘要",
		Memories: []memory.MemoryAction{
			{Action: "create", Name: "proj-a", Type: "project", Importance: 4, Content: "x"},
			{Action: "create", Name: "user-a", Type: "user", Importance: 3, Content: "y"},
		},
	})

	r := &Runner{
		llm:       client,
		history:   history,
		summary:   summary,
		memStore:  sessionMem,
		events:    events,
		retriever: retriever,
		extractor: memory.NewExtractor(client),
		ui:        text.NewTextUI(),
	}

	r.extractMemory(context.Background(), 1, "hi", "ok")

	if entry := sessionMem.GetEntry("proj-a"); entry == nil {
		t.Error("without project store, project memory should fall back to session store")
	}
	if entry := sessionMem.GetEntry("user-a"); entry == nil {
		t.Error("without global store, user memory should fall back to session store")
	}
}
