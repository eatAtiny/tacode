// Package tool 定义工具接口和注册表。
//
// 调用链中的角色：
//
//	main.go → Registry.Register(shellTool, fileTool)    ← 注册工具
//	QueryEngine → Registry.FunctionDefinitions()         ← 生成 OpenAI 工具定义
//	QueryEngine → Registry.Descriptions()                ← 生成 prompt 中的工具描述
//	queryLoop.executeSingleTool() → Registry.Get(name)    ← 按名称查找工具
//	queryLoop.executeSingleTool() → tool.Execute(args)   ← 执行工具
//
// 添加新工具的步骤：
//  1. 实现 Tool 接口（Name、Description、Parameters、Execute）
//  2. 在 main.go 中调用 Registry.Register(newTool)
package tool

import (
	"encoding/json"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// parseArgs 将 JSON 字符串参数解析到目标结构体。
// 工具实现中使用此函数解析 Execute(args) 的 args 参数。
func parseArgs(raw string, dst any) error {
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return fmt.Errorf("invalid JSON args: %w", err)
	}
	return nil
}

// ──────────────────────────────────────────────────────────
// Tool 接口
// ──────────────────────────────────────────────────────────

// Tool 定义一个可被 Agent 调用的工具。
//
// 这是项目中两个核心接口之一（另一个是 ui.UI）。
// 所有工具（内置的 shell/file 以及未来扩展的工具）都需实现此接口。
//
// 实现者需要提供：
//   - 名称和描述（告诉 LLM 工具能做什么）
//   - 参数 JSON Schema（告诉 LLM 如何传参）
//   - 执行逻辑（实际操作）
//
// Execute 的 args 是 LLM 传递的 JSON 字符串，工具实现需自行解析。
type Tool interface {
	// Name 返回工具名称，作为 function call 的标识。
	// 如 "shell"、"file"。
	// 名称在 Registry 中必须唯一。
	Name() string

	// Description 返回工具描述，告诉 LLM 这个工具能做什么。
	// 描述越清晰，LLM 越能正确选择工具。
	// 如 "执行一个 bash 命令并返回输出结果。"
	Description() string

	// Parameters 返回参数的 JSON Schema。
	// 格式为 map[string]any，会被序列化为 JSON 并嵌入 OpenAI function definition。
	// 如 {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}
	Parameters() map[string]any

	// Execute 执行工具，返回结果文本或错误。
	// args 是 LLM 传递的 JSON 字符串参数（与 Parameters 定义的 schema 对应）。
	//
	// 返回值：
	//   - 成功：返回结果文本（如 shell 命令输出、文件内容）
	//   - 失败：返回错误（queryLoop 会将错误信息作为 tool 消息反馈给 LLM）
	//
	// 注意：Execute 出错不会终止 Agent 运行。错误信息会被注入到消息历史中，
	// LLM 看到后会尝试其他方案。
	Execute(args string) (string, error)
}

// ──────────────────────────────────────────────────────────
// Registry — 工具注册表
// ──────────────────────────────────────────────────────────

// Registry 管理所有已注册的工具。
//
// 职责：
//   - 存储工具实例（map[name]Tool）
//   - 生成 OpenAI function calling 格式的定义（用于 LLM API 调用）
//   - 生成可读的工具描述文本（用于 system prompt）
//   - 按名称查找工具（用于执行）
type Registry struct {
	tools map[string]Tool
}

// NewRegistry 创建一个空的工具注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register 注册一个工具。如果同名工具已存在，后者覆盖前者。
func (r *Registry) Register(t Tool) {
	r.tools[t.Name()] = t
}

// Get 按名称获取工具，不存在返回 nil。
func (r *Registry) Get(name string) Tool {
	return r.tools[name]
}

// FunctionDefinitions 生成 OpenAI function calling 所需的工具定义列表。
//
// 调用时机：QueryEngine 步骤 3（构建 prompt 时）。
// 生成的 []openai.Tool 作为 ChatWithToolsStream 的 tools 参数传入。
//
// 每个工具定义包含：
//   - Type: "function"
//   - Function.Name: 工具名称
//   - Function.Description: 工具描述
//   - Function.Parameters: JSON Schema 参数定义
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
//
// 调用时机：BuildReActSystemPrompt（生成 system prompt 中的 "## 可用工具" 部分）。
//
// 输出格式：
//
//	- shell: 执行 bash 命令并返回输出结果
//	  参数: {"type":"object","properties":{"command":{"type":"string"}},...}
//	- file: 读取或写入文件
//	  参数: {"type":"object","properties":{"action":{"type":"string"},...},...}
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
