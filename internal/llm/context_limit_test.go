package llm

import (
	"testing"
)

func TestInferContextLimit_KnownModels(t *testing.T) {
	tests := []struct {
		model string
		want  int
	}{
		{"gpt-4o-mini", 128_000},
		{"gpt-4o", 128_000},
		{"gpt-4-turbo", 128_000},
		{"gpt-4", 8_192},
		{"gpt-3.5-turbo-16k", 16_384},
		{"gpt-3.5-turbo", 16_384},
		{"claude-sonnet-4", 200_000},
		{"claude-opus-4", 200_000},
		{"claude-3.5-sonnet", 200_000},
		{"claude-3-opus", 200_000},
		{"claude-3-haiku", 200_000},
		{"claude-haiku-4", 200_000},
		{"mimo-v2.5-pro", 1_000_000},
		{"mimo-v2-pro", 1_000_000}, // 不含 "mimo-v2.5" 子串，须独立命中 1M 分支
		{"mimo-v2-omni", 256_000},
		{"mimo-v2-flash", 256_000},
		{"deepseek-v4-flash", 1_000_000},
		{"deepseek-chat", 128_000},
		{"deepseek-reasoner", 128_000},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			if got := inferContextLimit(tt.model); got != tt.want {
				t.Errorf("inferContextLimit(%q) = %d, want %d", tt.model, got, tt.want)
			}
		})
	}
}

func TestInferContextLimit_UnknownModel_Conservative(t *testing.T) {
	// 未识别模型不应默认 128k（乐观值，可能超过真实窗口导致压缩过晚），
	// 应使用保守默认值。
	for _, model := range []string{"unknown-model", "gpt-7-super", "my-custom-llm"} {
		if got := inferContextLimit(model); got != unknownModelContextLimit {
			t.Errorf("inferContextLimit(%q) = %d, want conservative default %d",
				model, got, unknownModelContextLimit)
		}
	}
}

func TestInferContextLimit_EnvOverride(t *testing.T) {
	// OPENAI_CONTEXT_LIMIT 是最优先级，覆盖所有模型推断。
	t.Setenv("OPENAI_CONTEXT_LIMIT", "64000")
	if got := inferContextLimit("gpt-4o-mini"); got != 64000 {
		t.Errorf("env override failed: got %d, want 64000", got)
	}
	if got := inferContextLimit("unknown-model"); got != 64000 {
		t.Errorf("env override failed for unknown model: got %d, want 64000", got)
	}
}

func TestInferContextLimit_EnvUnset(t *testing.T) {
	// 未设置环境变量时，走模型推断路径。
	t.Setenv("OPENAI_CONTEXT_LIMIT", "")
	if got := inferContextLimit("gpt-4o-mini"); got != 128_000 {
		t.Errorf("expected model inference without env, got %d", got)
	}
}
