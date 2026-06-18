package tool

import (
	"encoding/json"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// parseArgs 将 JSON 字符串解析到目标结构体。
func parseArgs(raw string, dst any) error {
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("invalid JSON args: %w", err)
	}
	return nil
}

// Tool 定义一个可被 Agent 调用的工具。
type Tool interface {
	// Name 返回工具名称，作为 function call 的标识。
	Name() string
	// Description 返回工具描述，告诉 LLM 这个工具能做什么。
	Description() string
	// Parameters 返回参数的 JSON Schema。
	Parameters() map[string]any
	// Execute 执行工具，返回结果文本或错误。
	Execute(args string) (string, error)
}

// Registry 管理所有已注册的工具。
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建一个空的工具注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册一个工具。
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get 按名称获取工具，不存在返回 nil。
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// FunctionDefinitions 生成 OpenAI function calling 所需的工具定义列表。
func (r *Registry) FunctionDefinitions() []openai.Tool {
	defs := make([]openai.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.Parameters(),
			},
		})
	}
	return defs
}

// Names 返回所有已注册工具的名称列表。
func (r *Registry) Names() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Descriptions 返回所有工具的可读描述，用于 prompt 拼接。
func (r *Registry) Descriptions() string {
	if len(r.tools) == 0 {
		return "(无可用工具)"
	}
	var s string
	for _, t := range r.tools {
		params, _ := json.Marshal(t.Parameters())
		s += fmt.Sprintf("- %s: %s\n  参数: %s\n", t.Name(), t.Description(), string(params))
	}
	return s
}
