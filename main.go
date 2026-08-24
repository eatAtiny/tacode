package main

import (
	"bufio"
	"context"
	"flag"
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
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui"
	"agentic/internal/ui/bubble"
	"agentic/internal/ui/text"
)

// loadEnvFile 从 .env 文件加载环境变量。
// 只设置当前未定义的变量，已有的环境变量不会被覆盖。
// 文件格式：每行 KEY=VALUE，支持 # 注释和引号包裹的值。
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// 去除引号
		if len(value) >= 2 &&
			((value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		// 环境变量已设置时不覆盖
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

// main 是程序入口。
//
// 初始化调用链（按顺序）：
//
//	步骤 1: 解析命令行参数（sessions 目录、session ID、env 文件路径）
//	   -one-shot "<任务>": 一次性运行单次查询（headless），输出最终答案后退出
//	步骤 2: 加载 .env 文件（不覆盖已有环境变量）
//	步骤 3: 创建 LLM 客户端（从环境变量读取 API Key 和配置）
//	步骤 4: 初始化会话管理器（加载 manifest.json 或创建默认会话）
//	步骤 5: 如果指定了 -session flag，切换到该会话
//	步骤 6: 初始化三层记忆存储（L1 HistoryStore / L2 SummaryStore / L3 MemoryStore）
//	步骤 7: 初始化全量事件日志（EventStore）
//	步骤 8: 初始化记忆提取器（Extractor，使用 LLM 从对话提取摘要和记忆）
//	步骤 9: 初始化记忆检索器（Retriever，构建上下文 + 自动压缩）
//	步骤 10: 注册内置工具（Shell、File）
//	步骤 10b: 连接 -mcp-server 指定的外部 MCP server，注册其工具
//	步骤 11: 创建 UI 实例（REPL 模式 BubbleUI / one-shot 模式 TextUI）
//	步骤 12: 创建 Runner 并执行（REPL 循环或 one-shot 单次查询）
func main() {
	// ── 步骤 1: 解析命令行参数 ──
	sessionsDir := flag.String("sessions", "./data/sessions", "sessions directory path")
	sessionID := flag.String("session", "", "resume a specific session by ID (optional)")
	envFile := flag.String("env", ".env", "env file path")
	oneShot := flag.String("one-shot", "", "run a single query and exit (headless, prints final answer)")
	sandboxMode := flag.String("sandbox", "off", "sandbox mode: on (sandbox shell commands) / off (default)")
	configPath := flag.String("config", "", "config file path (yaml), optional")
	// 可重复 flag：-mcp-server "npx -y @modelcontextprotocol/server-fetch"
	// 或带名称：-mcp-server "fetch@npx -y @modelcontextprotocol/server-fetch"（工具前缀用 "fetch"）。
	var mcpServers mcpServerFlags
	flag.Var(&mcpServers, "mcp-server", "MCP server command to connect (repeatable, e.g. \"npx -y @modelcontextprotocol/server-fetch\")")
	flag.Parse()

	// ── 步骤 2: 加载 .env 文件 ──
	// 尝试从 .env 文件加载环境变量（不覆盖已有值）。
	if err := loadEnvFile(*envFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: load env file failed: %v\n", err)
	}

	// ── 步骤 2b: 加载 config 文件（可选） ──
	// 优先级：config 文件 > 环境变量（OPENAI_CONTEXT_LIMIT 等）> 代码默认。
	// 未指定 -config 时全部使用默认值，行为与旧版完全一致。
	cfg := config.Default()
	if *configPath != "" {
		loaded, err := config.Load(*configPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "warning: config file %s not found, using defaults\n", *configPath)
			} else {
				fmt.Fprintf(os.Stderr, "load config failed: %v\n", err)
				os.Exit(1)
			}
		} else {
			cfg = loaded.Apply(config.Default())
			fmt.Fprintf(os.Stderr, "config loaded from %s\n", *configPath)
		}
	}

	// ── 步骤 3: 创建 LLM 客户端 ──
	// 从环境变量创建 OpenAI 客户端（OPENAI_API_KEY 必需）。
	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init llm client failed: %v\n", err)
		os.Exit(1)
	}
	// config 的 ContextLimit / Temperature 覆盖环境推断值。
	client.SetConfig(cfg)

	// ── 步骤 4: 初始化会话管理器 ──
	// 加载 manifest.json（首次运行时创建默认会话）。
	sessions, err := session.NewSessionManager(*sessionsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init session manager failed: %v\n", err)
		os.Exit(1)
	}

	// ── 步骤 5: 恢复指定会话（可选） ──
	// 如果指定了 -session flag，切换到该会话。
	if *sessionID != "" {
		if err := sessions.Switch(*sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "switch session failed: %v\n", err)
			os.Exit(1)
		}
	}

	// ── 步骤 6: 初始化三层记忆存储 ──
	// 所有 store 指向当前活跃会话的子目录，通过 SetPath 支持后续会话切换。
	activeDir := sessions.ActiveSessionDir()

	// L1: 原始对话日志（history.jsonl），最多保留 50 轮。
	history, err := memory.NewHistoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init history store failed: %v\n", err)
		os.Exit(1)
	}

	// L2: 对话摘要（summaries.jsonl），LLM 提取 + 自动压缩合并。
	summary, err := memory.NewSummaryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init summary store failed: %v\n", err)
		os.Exit(1)
	}

	// L3: 结构化记忆（memory/*.md），frontmatter + 正文格式。
	memStore, err := memory.NewMemoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init memory store failed: %v\n", err)
		os.Exit(1)
	}

	// ── 步骤 6b: 初始化全局 + 项目级记忆（三级记忆的外两层） ──
	// 数据目录（相对 sessions 的上级，即 data/ 下）：
	//   - data/global-memory/memory/ — 全局记忆（user 类，跨项目）
	//   - data/project-memory/memory/ — 项目级记忆（project/reference 类，跨会话）
	// 会话级不再落 L3（对话细节靠 L2 摘要 + EventStore）。
	dataRoot := filepath.Join(*sessionsDir, "..")
	globalMem, err := memory.NewMemoryStore(filepath.Join(dataRoot, "global-memory"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "init global memory store failed: %v\n", err)
		os.Exit(1)
	}
	projectMem, err := memory.NewMemoryStore(filepath.Join(dataRoot, "project-memory"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "init project memory store failed: %v\n", err)
		os.Exit(1)
	}

	// ── 步骤 6c: 迁移旧记忆文件（幂等） ──
	// 把旧版散落在会话目录 / 旧全局目录的 L3 记忆按 type 归位到全局/项目级。
	if n := memory.MigrateLegacyMemory(globalMem, projectMem, *sessionsDir); n > 0 {
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
	// Extractor 使用 LLM 从每轮对话中提取 L2 摘要和 L3 记忆操作。
	extractor := memory.NewExtractor(client)

	// ── 步骤 9: 初始化记忆检索器 ──
	// Retriever 组合三层存储，在每轮查询前构建上下文（项目指令 + L3 记忆 + L2 摘要 + 降级 L1）。
	// 同时负责自动压缩：当上下文 token 用量超过模型窗口 80% 时触发 L2 摘要合并。
	retriever := memory.NewRetriever(history, summary, memStore, events)

	// 注入 config 的压缩阈值（无显式设置时保持默认 0.8）。
	if cfg.CompressThreshold != nil {
		retriever.SetCompressThreshold(*cfg.CompressThreshold)
	}

	// 注入全局 + 项目级记忆 store：BuildContext 合并对应层级记忆。
	retriever.SetGlobalMemory(globalMem)
	retriever.SetProjectMemory(projectMem)

	// 加载项目指令（AGENTS.md），失败只警告不退出（无指令文件时正常启动）。
	if err := retriever.LoadProjectInstructions(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: load project instructions failed: %v\n", err)
	}

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
	if *sandboxMode == "on" {
		sb = sandbox.NewSandbox(sandbox.Config{
			AllowNetwork: false, // 默认拒绝网络（防外发），network:true 需确认
			WorkDir:      "",    // 空 = cwd
		})
	}
	// 注意：sandbox.IsActive(nil) 对 nil 接口的类型断言返回 ok=false → 返回 true，
	// 因此必须先判空再调用，避免 nil 接口方法调用 panic。
	if sb != nil && sandbox.IsActive(sb) {
		defer sb.Close() // 释放临时 profile（macOS seatbelt 文件）
		tools.Register(tool.NewShellToolWithSandbox(sb))
	} else if *sandboxMode == "on" {
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
	defer mcpMgr.Close()

	// ── 步骤 11: 创建 UI 实例 ──
	// 按模式分支：
	//   - 默认：BubbleUI（终端美化 UI，lipgloss 样式 + glamour Markdown 渲染 + ANSI 光标控制）
	//   - one-shot：TextUI（headless，无终端输出，权限默认放行）
	var uiInstance ui.UI
	if *oneShot != "" {
		uiInstance = text.NewTextUI()
	} else {
		uiInstance = bubble.NewBubbleUI()
	}
	defer uiInstance.Close() // 确保退出时恢复终端状态

	// ── 步骤 12: 创建 Runner 并执行 ──
	// Runner 是顶层编排器，组合所有组件，驱动 REPL 交互循环。
	runner := agent.NewRunner(client, history, summary, memStore, events, extractor, retriever, tools, sessions, uiInstance)
	// 注入运行时配置（maxIterations / resultLimit / compressThreshold 等）。
	runner.SetConfig(cfg)
	// 注入全局 + 项目级记忆 store：extractMemory 按类型分流写入对应目录。
	runner.SetMemoryStores(globalMem, projectMem)

	// one-shot 模式：同步执行单次查询后退出。
	// 成功 → 输出答案到 stdout，return（让 defer Close() 执行）。
	// 失败 → 输出错误到 stderr，退出码 1。
	if *oneShot != "" {
		answer, err := runner.RunOnce(context.Background(), *oneShot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "one-shot 执行失败: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(answer)
		return
	}

	// REPL 模式：Run() 阻塞直到用户输入 exit 或发生致命错误。
	if err := runner.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed: %v\n", err)
		// 不用 os.Exit(1)，让 defer Close() 执行以恢复终端状态
	}
}

// mcpServerFlags 可重复的 -mcp-server flag 值收集器。
type mcpServerFlags []string

func (f *mcpServerFlags) String() string { return strings.Join(*f, ",") }

func (f *mcpServerFlags) Set(v string) error {
	*f = append(*f, v)
	return nil
}
