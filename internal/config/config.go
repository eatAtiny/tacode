package config

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config 是可配置的运行时参数。
//
// 优先级（从高到低）：
//  1. config 文件（-config <path>）
//  2. 环境变量（OPENAI_CONTEXT_LIMIT 等，保留向后兼容）
//  3. 代码默认（Default()）
//
// 所有字段均为指针（*int / *float64），以区分"未设置"（沿用默认）与
// "显式设置为零值"。YAML 中缺省字段保持 nil → 使用默认值。
type Config struct {
	// Temperature LLM 采样温度（默认 0.2）。
	Temperature *float64 `yaml:"temperature"`

	// MaxIterations ReAct 最大循环次数（默认 10）。
	MaxIterations *int `yaml:"max_iterations"`

	// ResultLimit 工具结果截断上限，字符数（默认 8000）。
	ResultLimit *int `yaml:"result_limit"`

	// CompressThreshold token 使用率超过该阈值触发上下文压缩（默认 0.8）。
	CompressThreshold *float64 `yaml:"compress_threshold"`

	// ContextLimit 模型上下文窗口大小（token 数）。
	// nil 时回退到 OPENAI_CONTEXT_LIMIT 环境变量 → 模型名推断。
	ContextLimit *int `yaml:"context_limit"`

	// ContextCharLimit 上下文字符上限，超过时触发压缩管线的 micro/fit/compact 步骤。
	// 镜像 s08 的 CONTEXT_CHAR_LIMIT；nil 时由 Compactor 兜底（真实默认见 agent/compactor.go）。
	ContextCharLimit *int `yaml:"context_char_limit"`
}

// Default 返回全部采用默认值的配置。
func Default() *Config {
	temperature := 0.2
	maxIterations := 10
	resultLimit := 8000
	compressThreshold := 0.8
	return &Config{
		Temperature:       &temperature,
		MaxIterations:     &maxIterations,
		ResultLimit:       &resultLimit,
		CompressThreshold: &compressThreshold,
		ContextLimit:      nil, // 未显式设置：走 env → 模型推断
		ContextCharLimit:  nil, // 真实默认 50000 定义在 agent/compactor.go 的 contextCharLimit（双源，改动需同步）
	}
}

// Load 从文件加载配置。
// 文件内容只覆盖显式声明的字段；未出现的字段保持 nil（沿用默认）。
// 文件不存在时返回 os.ErrNotExist（由调用方决定是否忽略）。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return cfg, nil
}

// Validate 校验字段取值是否合理。
func (c *Config) Validate() error {
	if c.MaxIterations != nil && *c.MaxIterations < 1 {
		return errors.New("max_iterations must be >= 1")
	}
	if c.ResultLimit != nil && *c.ResultLimit < 0 {
		return errors.New("result_limit must be >= 0")
	}
	if c.Temperature != nil && (*c.Temperature < 0 || *c.Temperature > 2) {
		return errors.New("temperature must be in [0, 2]")
	}
	if c.CompressThreshold != nil && (*c.CompressThreshold <= 0 || *c.CompressThreshold >= 1) {
		return errors.New("compress_threshold must be in (0, 1)")
	}
	if c.ContextLimit != nil && *c.ContextLimit <= 0 {
		return errors.New("context_limit must be > 0")
	}
	if c.ContextCharLimit != nil && *c.ContextCharLimit <= 0 {
		return errors.New("context_char_limit must be > 0")
	}
	return nil
}

// Apply 将 c 中显式设置（非 nil）的字段覆盖到 base 的默认值上，返回合并结果。
// 不修改接收者；base 为 nil 时回退 Default()。
// 用于 config 文件（已 Load）叠加到 Default() 之上。
func (c *Config) Apply(base *Config) *Config {
	if base == nil {
		base = Default()
	}
	out := *base // 浅拷贝结构体（指针字段共享默认值）
	if c.Temperature != nil {
		v := *c.Temperature
		out.Temperature = &v
	}
	if c.MaxIterations != nil {
		v := *c.MaxIterations
		out.MaxIterations = &v
	}
	if c.ResultLimit != nil {
		v := *c.ResultLimit
		out.ResultLimit = &v
	}
	if c.CompressThreshold != nil {
		v := *c.CompressThreshold
		out.CompressThreshold = &v
	}
	if c.ContextLimit != nil {
		v := *c.ContextLimit
		out.ContextLimit = &v
	}
	if c.ContextCharLimit != nil {
		v := *c.ContextCharLimit
		out.ContextCharLimit = &v
	}
	return &out
}
