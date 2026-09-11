package agent

import (
	"sync"
	"testing"

	"tacode/internal/ui/text"
)

// ──────────────────────────────────────────────────────────
// 每轮对话后的 token 统计展示
//
// 1. yieldFinal 的 FinalEvent 携带累计 input/output/total tokens
// 2. TextUI.OnFinal 把 token 转发给 OnEvent（headless/子 agent 可用）
// ──────────────────────────────────────────────────────────

func TestYieldFinal_CarriesTokens(t *testing.T) {
	events := make(chan QueryEvent, 8)
	lc := &queryLoopContext{
		events:            events,
		totalInputTokens:  100,
		totalOutputTokens: 50,
	}
	lc.yieldFinal("final answer", 3)

	var evt QueryEvent
	var ok bool
	select {
	case evt = <-events:
		ok = true
	default:
	}
	if !ok {
		t.Fatal("expected a FinalEvent event")
	}
	fe, isFinal := evt.(FinalEvent)
	if !isFinal {
		t.Fatalf("expected final event, got %T", evt)
	}
	if fe.InputTokens != 100 {
		t.Errorf("input tokens = %d, want 100", fe.InputTokens)
	}
	if fe.OutputTokens != 50 {
		t.Errorf("output tokens = %d, want 50", fe.OutputTokens)
	}
	if fe.TotalTokens != 150 {
		t.Errorf("total tokens = %d, want 150", fe.TotalTokens)
	}
}

func TestTextUI_OnFinal_ForwardsTokens(t *testing.T) {
	var mu sync.Mutex
	var got map[string]any
	ui := text.NewTextUI()
	ui.OnEvent = func(event string, data any) {
		mu.Lock()
		defer mu.Unlock()
		if event == "final" {
			got = data.(map[string]any)
		}
	}

	ui.OnFinal("answer text", 100, 50, 150)

	mu.Lock()
	defer mu.Unlock()
	if got == nil {
		t.Fatal("OnEvent should receive final event")
	}
	if got["answer"] != "answer text" {
		t.Errorf("answer = %v, want 'answer text'", got["answer"])
	}
	if got["total_tokens"] != 150 {
		t.Errorf("total_tokens = %v, want 150", got["total_tokens"])
	}
	if got["input_tokens"] != 100 || got["output_tokens"] != 50 {
		t.Errorf("token fields wrong: %v %v", got["input_tokens"], got["output_tokens"])
	}
}
