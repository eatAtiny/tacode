package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"agentic/internal/agent"
	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/session"
	"agentic/internal/tool"
	"agentic/internal/ui/bubble"
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
//	步骤 2: 加载 .env 文件（不覆盖已有环境变量）
//	步骤 3: 创建 LLM 客户端（从环境变量读取 API Key 和配置）
//	步骤 4: 初始化会话管理器（加载 manifest.json 或创建默认会话）
//	步骤 5: 如果指定了 -session flag，切换到该会话
//	步骤 6: 初始化三层记忆存储（L1 HistoryStore / L2 SummaryStore / L3 MemoryStore）
//	步骤 7: 初始化全量事件日志（EventStore）
//	步骤 8: 初始化记忆提取器（Extractor，使用 LLM 从对话提取摘要和记忆）
//	步骤 9: 初始化记忆检索器（Retriever，构建上下文 + 自动压缩）
//	步骤 10: 注册内置工具（Shell、File）
//	步骤 11: 创建 UI 实例（BubbleUI 终端美化 UI）
//	步骤 12: 创建 Runner 并启动 REPL 循环
func main() {
	// ── 步骤 1: 解析命令行参数 ──
	sessionsDir := flag.String("sessions", "./data/sessions", "sessions directory path")
	sessionID := flag.String("session", "", "resume a specific session by ID (optional)")
	envFile := flag.String("env", ".env", "env file path")
	flag.Parse()

	// ── 步骤 2: 加载 .env 文件 ──
	// 尝试从 .env 文件加载环境变量（不覆盖已有值）。
	if err := loadEnvFile(*envFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: load env file failed: %v\n", err)
	}

	// ── 步骤 3: 创建 LLM 客户端 ──
	// 从环境变量创建 OpenAI 客户端（OPENAI_API_KEY 必需）。
	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init llm client failed: %v\n", err)
		os.Exit(1)
	}

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

	// ── 步骤 7: 初始化全量事件日志 ──
	// EventStore（events.jsonl）是追加写入的完整事件流，永不截断，作为真相源。
	events := memory.NewEventStore(activeDir)

	// ── 步骤 8: 初始化记忆提取器 ──
	// Extractor 使用 LLM 从每轮对话中提取 L2 摘要和 L3 记忆操作。
	extractor := memory.NewExtractor(client)

	// ── 步骤 9: 初始化记忆检索器 ──
	// Retriever 组合三层存储，在每轮查询前构建上下文（L3 记忆 + L2 摘要 + 降级 L1）。
	// 同时负责自动压缩：当上下文 token 用量超过模型窗口 80% 时触发 L2 摘要合并。
	retriever := memory.NewRetriever(history, summary, memStore, events)

	// ── 步骤 10: 注册内置工具 ──
	// Shell: 执行 bash 命令（30s 超时），危险命令需用户确认。
	// File: 读写文件（带行号），写操作需用户确认。
	// Edit: search-and-replace 编辑，唯一性校验 + diff 输出。
	// Grep: 结构化文本搜索，跳过 .git/ + 二进制文件。
	// List: 结构化目录列表，深度控制 + 排序输出。
	// Git: 结构化 git 操作（status/diff/log/show/branch 只读 + add/commit/stash/checkout 需确认）。
	tools := tool.NewRegistry()
	tools.Register(tool.NewShellTool())
	tools.Register(tool.NewFileTool())
	tools.Register(tool.NewEditTool())
	tools.Register(tool.NewGrepTool())
	tools.Register(tool.NewListTool())
	tools.Register(tool.NewGitTool())

	// ── 步骤 11: 创建 UI 实例 ──
	// BubbleUI: 终端美化 UI（lipgloss 样式 + glamour Markdown 渲染 + ANSI 光标控制）。
	uiInstance := bubble.NewBubbleUI()
	defer uiInstance.Close() // 确保退出时恢复终端状态

	// ── 步骤 12: 创建 Runner 并启动 REPL 循环 ──
	// Runner 是顶层编排器，组合所有组件，驱动 REPL 交互循环。
	// Run() 阻塞直到用户输入 exit 或发生致命错误。
	runner := agent.NewRunner(client, history, summary, memStore, events, extractor, retriever, tools, sessions, uiInstance)
	if err := runner.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed: %v\n", err)
		// 不用 os.Exit(1)，让 defer Close() 执行以恢复终端状态
	}
}
