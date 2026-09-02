package memory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"tacode/internal/llm"
)

// ──────────────────────────────────────────────────────────
// P2⑨: CompressSummaries / CheckAndCompress 测试
//
// 使用 httptest.Server 模拟 OpenAI 非流式 /chat/completions，
// 避免外部网络依赖。CompressSummaries 内部调用 llmClient.Chat()。
// ──────────────────────────────────────────────────────────

// newMockLLM 构造一个 Chat() 返回固定文本的 LLM 客户端。
// 通过 OPENAI_BASE_URL 指向本地 mock server（不产生真实网络请求）。
func newMockLLM(t *testing.T, reply string) *llm.OpenAIClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		replyJSON, _ := json.Marshal(reply)
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o-mini",
			"choices":[{"index":0,"message":{"role":"assistant","content":`+string(replyJSON)+`},"finish_reason":"stop"}],
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

// addSummaries 向 SummaryStore 追加 n 条摘要。
func addSummaries(t *testing.T, store *SummaryStore, n int) {
	t.Helper()
	for i := 1; i <= n; i++ {
		if err := store.Append(i, "summary "+strings.Repeat("x", i)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompressSummaries_TooFew(t *testing.T) {
	root := t.TempDir()
	summary := NewSummaryStore(root)
	addSummaries(t, summary, 3)

	r := NewRetriever(nil, summary, nil, nil)
	client := newMockLLM(t, "compressed")
	if err := r.CompressSummaries(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// <=3 条不压缩，仍为 3 条。
	all, _ := summary.LoadAll()
	if len(all) != 3 {
		t.Errorf("<=3 summaries should not compress, got %d", len(all))
	}
}

func TestCompressSummaries_MergesOldKeepsRecent(t *testing.T) {
	root := t.TempDir()
	summary := NewSummaryStore(root)
	addSummaries(t, summary, 6) // 6 条：压缩前 3 条，保留后 3 条

	r := NewRetriever(nil, summary, nil, nil)
	client := newMockLLM(t, "merged summary")
	if err := r.CompressSummaries(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	all, _ := summary.LoadAll()
	if len(all) != 4 {
		t.Fatalf("expected 1 compressed + 3 recent = 4, got %d", len(all))
	}
	// 第一条是压缩摘要（带前缀标记），后 3 条是原样保留的最近摘要。
	if !strings.HasPrefix(all[0].Summary, "[压缩摘要]") {
		t.Errorf("first summary should be marked [压缩摘要], got %q", all[0].Summary)
	}
	if !strings.Contains(all[0].Summary, "merged summary") {
		t.Errorf("compressed summary should contain LLM output, got %q", all[0].Summary)
	}
	// 最近 3 条原样保留（轮次 4,5,6）。
	wantRounds := []int{4, 5, 6}
	for i, want := range wantRounds {
		if all[i+1].Round != want {
			t.Errorf("recent[%d].Round = %d, want %d", i, all[i+1].Round, want)
		}
	}
}

func TestCheckAndCompress_Threshold(t *testing.T) {
	root := t.TempDir()
	summary := NewSummaryStore(root)
	addSummaries(t, summary, 6)

	r := NewRetriever(nil, summary, nil, nil)
	client := newMockLLM(t, "merged")

	// tokenLimit=100，usage=50 → 50% < 80%，不压缩。
	ok, err := r.CheckAndCompress(context.Background(), client, 100, 50)
	if err != nil || ok {
		t.Errorf("below threshold: ok=%v err=%v, want false,nil", ok, err)
	}

	// usage=90 → 90% > 80%，触发压缩。
	ok, err = r.CheckAndCompress(context.Background(), client, 100, 90)
	if err != nil {
		t.Fatalf("compress failed: %v", err)
	}
	if !ok {
		t.Error("above threshold should trigger compression")
	}
}

func TestCheckAndCompress_ZeroTokenLimit(t *testing.T) {
	root := t.TempDir()
	summary := NewSummaryStore(root)
	r := NewRetriever(nil, summary, nil, nil)

	ok, err := r.CheckAndCompress(context.Background(), nil, 0, 100)
	if err != nil || ok {
		t.Errorf("zero tokenLimit: ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestCompressSummaries_WithCustomThreshold(t *testing.T) {
	root := t.TempDir()
	summary := NewSummaryStore(root)
	addSummaries(t, summary, 6)

	r := NewRetriever(nil, summary, nil, nil)
	client := newMockLLM(t, "merged")

	// 提高阈值到 0.95：usage/tokenLimit = 0.9 不再触发。
	r.SetCompressThreshold(0.95)
	ok, err := r.CheckAndCompress(context.Background(), client, 100, 90)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("0.9 ratio should not trigger with 0.95 threshold")
	}

	// 降低阈值到 0.5：0.9 ratio 触发。
	r.SetCompressThreshold(0.5)
	ok, err = r.CheckAndCompress(context.Background(), client, 100, 90)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("0.9 ratio should trigger with 0.5 threshold")
	}
}
