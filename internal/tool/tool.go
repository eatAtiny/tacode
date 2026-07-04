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
//  1. 实现 Tool 接口（6 个方法）
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
// PermissionResult — 工具权限自检结果
// ──────────────────────────────────────────────────────────

// PermissionResult 表示工具对自身操作的权限判定。
//
// 每个工具通过 CheckPermission 返回此结果，
// 告诉框架：这个操作是直接放行还是需要用户确认。
//
// Claude Code 对应：checkPermissions(input, context) 的返回值。
type PermissionResult struct {
	Allow  bool   // true=允许直接执行，false=需要用户确认
	Reason string // 需要确认的原因（Allow=false 时展示给用户）
}

// ──────────────────────────────────────────────────────────
// Tool 接口（重构后 — 6 个方法）
// ──────────────────────────────────────────────────────────

// Tool 定义一个可被 Agent 调用的工具。
//
// 这是项目中两个核心接口之一（另一个是 ui.UI）。
// 所有工具都需实现此接口。
//
// 设计参考 Claude Code 的工具契约，融入三个关键模式：
//   - 权限内聚：工具自行判断操作是否需要确认（而非外部按工具名打标签）
//   - Prompt 自引导：工具向 system prompt 注入使用指南
//   - 结果上限：工具声明自己的结果截断长度，框架统一处理
//
// Execute 的 args 是 LLM 传递的 JSON 字符串，工具实现需自行解析。
type Tool interface {
	// ── 基础方法 ──

	// Name 返回工具名称，作为 function call 的标识。
	// 如 "shell"、"file"、"edit"。
	// 名称在 Registry 中必须唯一。
	Name() string

	// Description 返回工具描述，告诉 LLM 这个工具能做什么。
	// 描述越清晰，LLM 越能正确选择工具。
	// 如 "执行一个 bash 命令并返回输出结果。"
	Description() string

	// Parameters 返回参数的 JSON Schema。
	// 格式为 map[string]any，会被序列化为 JSON 并嵌入 OpenAI function definition。
	Parameters() map[string]any

	// Execute 执行工具，返回结果文本或错误。
	// args 是 LLM 传递的 JSON 字符串参数（与 Parameters 定义的 schema 对应）。
	//
	// 返回值：
	//   - 成功：返回结果文本
	//   - 失败：返回错误（queryLoop 将错误信息作为 tool 消息反馈给 LLM）
	//
	// 注意：Execute 出错不会终止 Agent 运行。
	Execute(args string) (string, error)

	// ── 权限内聚（替代外部 DefaultPermissionChecker） ──

	// CheckPermission 工具自检：这个操作可以直接执行，还是需要用户确认？
	//
	// 这是 Claude Code checkPermissions(input, context) 的 Go 化。
	// 每个工具最了解自己的安全语义：
	//   - shell 知道哪些命令危险（rm -rf、sudo…）
	//   - file 知道 read 安全、write 需要确认
	//   - grep/list 始终安全
	//   - edit 始终需要确认
	//
	// queryLoop 在 Execute 之前调用此方法：
	//   Allow=true  → 直接执行
	//   Allow=false → yield Permission 事件，阻塞等待用户 y/N
	CheckPermission(args string) PermissionResult

	// ── Prompt 自引导（来自 Claude Code prompt()） ──

	// PromptGuide 返回工具专属的使用引导文本，注入到 system prompt。
	//
	// 与 Registry.Descriptions()（扁平描述列表）互补：
	//   - Descriptions 告诉 LLM 工具有什么参数
	//   - PromptGuide 告诉 LLM 怎么正确使用工具
	//
	// 返回空字符串表示无额外引导。
	PromptGuide() string

	// ── 结果上限（来自 Claude Code maxResultSizeChars） ──

	// ResultLimit 返回期望的结果字符数上限。
	//
	// 框架在 Execute 返回后统一截断（保留头尾的 head+tail 策略）。
	// 工具自身无需在 Execute 中实现截断逻辑。
	//
	// 返回 0 表示不截断（慎用，可能导致上下文爆炸）。
	ResultLimit() int
}

// ──────────────────────────────────────────────────────────
// TruncateResult — 统一结果截断
// ──────────────────────────────────────────────────────────

// TruncateResult 截断过长结果，保留头部和尾部。
//
// 策略（参考 Claude Code）：
//   - 保留前 70% + 后 30%（扣除截断提示信息）
//   - 明确告知模型内容被截断以及总长度
//   - 模型可据此决定是否用更精细的工具获取完整内容
//
// 保留头尾而非只保留头部：很多输出（编译错误、测试结果）
// 的关键信息在末尾。
func TruncateResult(s string, limit int) string {
	if len(s) <= limit {
		return s
	}

	const headRatio = 0.7
	headLen := int(float64(limit) * headRatio)
	truncMsg := fmt.Sprintf("\n\n…(已截断，共 %d 字符，保留头尾)…\n\n", len(s))
	msgLen := len(truncMsg)
	tailLen := limit - headLen - msgLen
	if tailLen < 0 {
		tailLen = 0
		headLen = limit - msgLen
		if headLen < 0 {
			headLen = 0
		}
	}

	head := s[:headLen]
	tail := s[len(s)-tailLen:]
	return head + truncMsg + tail
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
