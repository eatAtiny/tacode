package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefault_Values(t *testing.T) {
	cfg := Default()
	if cfg.Temperature == nil || *cfg.Temperature != 0.2 {
		t.Errorf("expected temperature 0.2, got %v", cfg.Temperature)
	}
	if cfg.MaxIterations == nil || *cfg.MaxIterations != 10 {
		t.Errorf("expected max_iterations 10, got %v", cfg.MaxIterations)
	}
	if cfg.ResultLimit == nil || *cfg.ResultLimit != 8000 {
		t.Errorf("expected result_limit 8000, got %v", cfg.ResultLimit)
	}
	if cfg.CompressThreshold == nil || *cfg.CompressThreshold != 0.8 {
		t.Errorf("expected compress_threshold 0.8, got %v", cfg.CompressThreshold)
	}
	if cfg.ContextLimit != nil {
		t.Errorf("expected context_limit nil by default, got %v", *cfg.ContextLimit)
	}
}

func TestLoad_AllFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := "temperature: 0.7\nmax_iterations: 5\nresult_limit: 4096\ncompress_threshold: 0.9\ncontext_limit: 32000\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Temperature == nil || *cfg.Temperature != 0.7 {
		t.Errorf("temperature: got %v", cfg.Temperature)
	}
	if cfg.MaxIterations == nil || *cfg.MaxIterations != 5 {
		t.Errorf("max_iterations: got %v", cfg.MaxIterations)
	}
	if cfg.ResultLimit == nil || *cfg.ResultLimit != 4096 {
		t.Errorf("result_limit: got %v", cfg.ResultLimit)
	}
	if cfg.CompressThreshold == nil || *cfg.CompressThreshold != 0.9 {
		t.Errorf("compress_threshold: got %v", cfg.CompressThreshold)
	}
	if cfg.ContextLimit == nil || *cfg.ContextLimit != 32000 {
		t.Errorf("context_limit: got %v", cfg.ContextLimit)
	}
}

func TestLoad_PartialFields(t *testing.T) {
	// 只声明部分字段：未出现的保持 nil（沿用默认）。
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("max_iterations: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.MaxIterations == nil || *cfg.MaxIterations != 3 {
		t.Errorf("max_iterations: got %v", cfg.MaxIterations)
	}
	if cfg.Temperature != nil {
		t.Errorf("temperature should be nil (not set), got %v", *cfg.Temperature)
	}
	if cfg.ContextLimit != nil {
		t.Errorf("context_limit should be nil (not set), got %v", *cfg.ContextLimit)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if !os.IsNotExist(err) {
		t.Errorf("expected os.ErrNotExist, got %v", err)
	}
}

func TestLoad_InvalidValue(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"max_iterations zero", "max_iterations: 0\n"},
		{"temperature out of range", "temperature: 3\n"},
		{"compress_threshold boundary", "compress_threshold: 1.0\n"},
		{"context_limit negative", "context_limit: -1\n"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Errorf("expected validation error for %q", tt.content)
			}
		})
	}
}

func TestApply_Overrides(t *testing.T) {
	// 部分字段的 Load 结果叠加到 Default 上：显式字段覆盖，其余保持默认。
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("max_iterations: 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	merged := loaded.Apply(Default())
	if merged.MaxIterations == nil || *merged.MaxIterations != 3 {
		t.Errorf("max_iterations should be overridden to 3, got %v", merged.MaxIterations)
	}
	if merged.Temperature == nil || *merged.Temperature != 0.2 {
		t.Errorf("temperature should stay default 0.2, got %v", merged.Temperature)
	}
	if merged.CompressThreshold == nil || *merged.CompressThreshold != 0.8 {
		t.Errorf("compress_threshold should stay default 0.8, got %v", merged.CompressThreshold)
	}
	// ContextLimit 默认 nil：Apply 后仍为 nil（走 env → 模型推断）。
	if merged.ContextLimit != nil {
		t.Errorf("context_limit should stay nil, got %v", *merged.ContextLimit)
	}
}
