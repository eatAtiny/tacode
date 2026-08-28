// bootstrap.go 承担依赖装配：记忆体系（buildMemoryStack）、工具注册与沙箱/MCP
// 连接（buildTools）、UI 实例（buildUI）三个构造函数，以及 fatal 错误退出
// helper。main.go 只保留 flag 解析、配置加载与模式分支。
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agentic/internal/agent"
	"agentic/internal/config"
	"agentic/internal/llm"
	"agentic/internal/mcp"
	"agentic/internal/memory"
	"agentic/internal/sandbox"
	"agentic/internal/tool"
	"agentic/internal/ui"
	"agentic/internal/ui/bubble"
	"agentic/internal/ui/text"
)

// memoryStack 是 buildMemoryStack 的装配结果：三层记忆体系 + 全局/项目记忆
// store + 事件日志 + 提取/检索组件，供 main 组装 Runner 时注入。
type memoryStack struct {
	history    *memory.HistoryStore // L1 原始对话日志
	summary    *memory.SummaryStore // L2 对话摘要
	memStore   *memory.MemoryStore  // L3 会话级结构化记忆
	globalMem  *memory.MemoryStore  // 全局记忆（跨项目）
	projectMem *memory.MemoryStore  // 项目级记忆（跨会话）
	events     *memory.EventStore   // 全量事件日志（真相源）
	extractor  *memory.Extractor    // L3 记忆提取器
	retriever  *memory.Retriever    // 三层检索 + 记忆 preamble
}

// buildMemoryStack 装配记忆体系（原 main 步骤 6-9）：三层会话存储、全局/项目
// 记忆 store、旧记忆迁移、EventStore、Extractor 与 Retriever。
// 无 fatal 路径：项目指令加载失败只警告不退出，旧记忆迁移仅输出计数提示。
func buildMemoryStack(client *llm.OpenAIClient, cfg *config.Config, sessionsDir, activeDir string) *memoryStack {
	// ── 步骤 6: 初始化三层记忆存储 ──
	// 所有 store 指向当前活跃会话的子目录，通过 SetPath 支持后续会话切换。
	// L1: 原始对话日志（history.jsonl），最多保留 50 轮。
	history := memory.NewHistoryStore(activeDir)

	// L2: 对话摘要（summaries.jsonl），LLM 提取 + 自动压缩合并。
	summary := memory.NewSummaryStore(activeDir)

	// L3: 结构化记忆（memory/*.md），frontmatter + 正文格式。
	memStore := memory.NewMemoryStore(activeDir)

	// ── 步骤 6b: 初始化全局 + 项目级记忆（三级记忆的外两层） ──
	// 数据目录（相对 sessions 的上级，即 data/ 下）：
	//   - data/global-memory/memory/ — 全局记忆（user 类，跨项目）
	//   - data/project-memory/memory/ — 项目级记忆（project/reference 类，跨会话）
	// 会话级不再落 L3（对话细节靠跨轮累积消息 + EventStore）。
	dataRoot := filepath.Join(sessionsDir, "..")
	globalMem := memory.NewMemoryStore(filepath.Join(dataRoot, "global-memory"))
	projectMem := memory.NewMemoryStore(filepath.Join(dataRoot, "project-memory"))

	// ── 步骤 6c: 迁移旧记忆文件（幂等） ──
	// 把旧版散落在会话目录 / 旧全局目录的 L3 记忆按 type 归位到全局/项目级。
	if n := memory.MigrateLegacyMemory(globalMem, projectMem, sessionsDir); n > 0 {
		plural := "ies"
		if n == 1 {
			plural = "y"
		}
		fmt.Fprintf(os.Stderr, "migrated %d legacy memory entr%s to global/project stores\n", n, plural)
	}

	// ── 步骤 7: 初始化全量事件日志 ──
	// EventStore（events.jsonl）是追加写入的完整事件流，永不截断，作为真相源。
	events := memory.NewEventStore(activeDir)

	// ── 步骤 8: 初始化记忆提取器 ──
	// Extractor 使用 LLM 从每轮对话中提取 L3 记忆操作。
	extractor := memory.NewExtractor(client)

	// ── 步骤 9: 初始化记忆检索器 ──
	// Retriever 组合三层存储，仅首轮/会话切换时注入记忆 preamble；
	// 上下文压缩由 Compactor 字符管线负责（s08 五步）。
	retriever := memory.NewRetriever(history, summary, memStore, events)

	// 注入 L2 压缩阈值（预留路径，当前无生产调用）。
	if cfg.CompressThreshold != nil {
		retriever.SetCompressThreshold(*cfg.CompressThreshold)
	}

	// 注入全局 + 项目级记忆 store：BuildContext 合并对应层级记忆。
	retriever.SetGlobalMemory(globalMem)
	retriever.SetProjectMemory(projectMem)

	// 加载项目指令（CLAUDE.md/AGENTS.md/AGENTS/.cursorrules，CLAUDE.md 优先），
	// 失败只警告不退出（无指令文件时正常启动）。
	if err := retriever.LoadProjectInstructions(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: load project instructions failed: %v\n", err)
	}

	return &memoryStack{
		history:    history,
		summary:    summary,
		memStore:   memStore,
		globalMem:  globalMem,
		projectMem: projectMem,
		events:     events,
		extractor:  extractor,
		retriever:  retriever,
	}
}

// buildTools 注册内置工具、按需启用沙箱并连接 -mcp-server 指定的 MCP server
// （原 main 步骤 10-10b）。返回工具注册表与清理函数：清理函数必须由调用方在
// 退出时 defer 执行，内部先关 MCP 连接、再释放沙箱临时 profile（与原 main
// defer 链的 LIFO 语义一致）。
func buildTools(sandboxMode string, mcpServers mcpServerFlags) (*tool.Registry, func()) {
	// ── 步骤 10: 注册内置工具 ──
	// Shell: 执行 bash 命令（30s 超时），危险命令需用户确认。
	// File: 读写文件（带行号），写操作需用户确认。
	// Edit: search-and-replace 编辑，唯一性校验 + diff 输出。
	// Grep: 结构化文本搜索，跳过 .git/ + 二进制文件。
	// List: 结构化目录列表，深度控制 + 排序输出。
	// Git: 结构化 git 操作（status/diff/log/show/branch 只读 + add/commit/stash/checkout 需确认）。
	tools := tool.NewRegistry()
	// shell 工具：默认无沙箱；-sandbox on 时启用平台沙箱（网络/文件系统隔离）。
	var sb sandbox.Sandbox
	if sandboxMode == "on" {
		sb = sandbox.NewSandbox(sandbox.Config{
			AllowNetwork: false, // 默认拒绝网络（防外发），network:true 需确认
			WorkDir:      "",    // 空 = cwd
		})
	}
	var closeSandbox func() // 沙箱激活时释放临时 profile，随清理函数退出时执行
	// 注意：sandbox.IsActive(nil) 对 nil 接口的类型断言返回 ok=false → 返回 true，
	// 因此必须先判空再调用，避免 nil 接口方法调用 panic。
	if sb != nil && sandbox.IsActive(sb) {
		closeSandbox = func() { sb.Close() } // 释放临时 profile（macOS seatbelt 文件）
		tools.Register(tool.NewShellToolWithSandbox(sb))
	} else if sandboxMode == "on" {
		fmt.Fprintf(os.Stderr, "warning: sandbox requested but not supported on %s, continuing without sandbox\n", runtime.GOOS)
		tools.Register(tool.NewShellTool())
	} else {
		tools.Register(tool.NewShellTool())
	}
	tools.Register(tool.NewFileTool())
	tools.Register(tool.NewEditTool())
	tools.Register(tool.NewGrepTool())
	tools.Register(tool.NewListTool())
	tools.Register(tool.NewGitTool())
	// webfetch 工具：抓取网页（HTML→Markdown + 提取元信息），仅 GET，默认放行。
	tools.Register(tool.NewWebFetchTool())
	// compact 工具：模型主动请求压缩（整批工具执行完后对已闭合回合做历史摘要）。
	tools.Register(&agent.CompactTool{})

	// ── 步骤 10b: MCP server 连接 ──
	// -mcp-server flag 指定的外部 MCP server（如 mcp-server-fetch 提供 WebFetch）。
	// 每个 server 的工具注册进 Registry；单个失败警告跳过，不阻断启动。
	mcpMgr := mcp.New()
	for _, spec := range mcpServers {
		parts := strings.Fields(spec)
		if len(parts) == 0 {
			continue
		}
		// 支持 "name@command args..." 语法：@ 前的部分是 server 名，
		// 作为工具名前缀（如 "fetch@npx -y ..." → 工具名 "fetch_fetch"）。
		// 仅当首个 token 内含 @ 时按 name@command 解析；
		// 否则整个命令保持原样（scoped 包名 @scope/pkg 中的 @ 不误判），
		// server 名回退到 "mcp"。
		name := "mcp"
		command := parts[0]
		if idx := strings.Index(parts[0], "@"); idx > 0 {
			name = parts[0][:idx]
			command = parts[0][idx+1:]
		}
		cfg := mcp.ServerConfig{
			Name:    name,
			Command: command,
			Args:    parts[1:],
		}
		connectCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		serverTools, err := mcpMgr.Connect(connectCtx, cfg)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: mcp server %q connect failed: %v\n", cfg.Command, err)
			continue
		}
		// 只注册本次连接的 server 的工具（Connect 返回该 server 的工具）。
		for _, t := range serverTools {
			tools.Register(t)
		}
		fmt.Fprintf(os.Stderr, "mcp server %q connected, %d tool(s) registered\n", cfg.Name, len(serverTools))
	}

	return tools, func() {
		mcpMgr.Close()
		if closeSandbox != nil {
			closeSandbox()
		}
	}
}

// buildUI 创建 UI 实例（原 main 步骤 11）。按模式分支：
//   - 默认：BubbleUI（终端美化 UI，lipgloss 样式 + glamour Markdown 渲染 + ANSI 光标控制）
//   - one-shot：TextUI（headless，无终端输出，权限默认放行）
//
// 返回的实例由调用方 defer Close（确保退出时恢复终端状态）。
func buildUI(oneShotMode bool) ui.UI {
	if oneShotMode {
		return text.NewTextUI()
	}
	return bubble.NewBubbleUI()
}

// fatal 输出错误信息到 stderr 并以退出码 1 终止程序。
// 注意：os.Exit 不执行 defer（如 MCP/沙箱/UI 的清理），与原内联
// 「Fprintf + os.Exit(1)」写法行为一致，仅用于无需清理的失败路径。
// 例外：main 中 one-shot 失败点在 defer 注册之后调用 fatal——为保持
// 原 os.Exit 行为有意跳过已注册的清理，勿在新代码模仿。
func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", msg, err)
	os.Exit(1)
}
