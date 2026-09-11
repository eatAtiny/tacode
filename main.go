package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"tacode/internal/agent"
	"tacode/internal/config"
	"tacode/internal/llm"
	"tacode/internal/session"
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
// 初始化调用链（按顺序；步骤 6-11 的装配细节在 bootstrap.go 的三个 build 函数内）：
//
//	步骤 1: 解析命令行参数（sessions 目录、session ID、env 文件路径）
//	   -one-shot "<任务>": 一次性运行单次查询（headless），输出最终答案后退出
//	步骤 2: 加载 .env 文件（不覆盖已有环境变量）与 config 文件（可选）
//	步骤 3: 创建 LLM 客户端（从环境变量读取 API Key 和配置）
//	步骤 4: 初始化会话管理器（加载 manifest.json 或创建默认会话）
//	步骤 5: 如果指定了 -session flag，切换到该会话
//	步骤 6-9: buildMemoryStack（bootstrap.go）——三层记忆存储（L1/L2/L3）+
//	   全局/项目记忆 store + 旧记忆迁移 + EventStore/Extractor/Retriever
//	步骤 10-10b: buildTools（bootstrap.go）——注册内置工具 + 沙箱 + MCP server
//	   连接，返回工具注册表与清理函数
//	步骤 11: buildUI（bootstrap.go）——UI 实例（REPL 模式 BubbleUI / one-shot 模式 TextUI）
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
				fatal("load config failed", err)
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
		fatal("init llm client failed", err)
	}
	// config 的 ContextLimit / Temperature 覆盖环境推断值。
	client.SetConfig(cfg)

	// ── 步骤 4: 初始化会话管理器 ──
	// 加载 manifest.json（首次运行时创建默认会话）。
	sessions, err := session.NewSessionManager(*sessionsDir)
	if err != nil {
		fatal("init session manager failed", err)
	}

	// ── 步骤 5: 恢复指定会话（可选） ──
	// 如果指定了 -session flag，切换到该会话。
	if *sessionID != "" {
		if err := sessions.Switch(*sessionID); err != nil {
			fatal("switch session failed", err)
		}
	}

	// ── 步骤 6-9: 装配记忆体系（buildMemoryStack，bootstrap.go） ──
	// 活跃会话目录：记忆 store 与下方 Compactor 的 transcripts/tool-results 都挂在它下面。
	activeDir := sessions.ActiveSessionDir()
	mem := buildMemoryStack(client, cfg, *sessionsDir, activeDir)

	// ── 步骤 10-10b: 注册工具 + 沙箱 + MCP 连接（buildTools，bootstrap.go） ──
	// 清理函数关闭 MCP 连接与沙箱临时 profile（顺序与原 defer 链的 LIFO 语义一致）。
	tools, cleanupTools := buildTools(*sandboxMode, mcpServers)
	defer cleanupTools()

	// ── 步骤 11: 创建 UI 实例（buildUI，bootstrap.go） ──
	uiInstance := buildUI(*oneShot != "")
	defer uiInstance.Close() // 确保退出时恢复终端状态

	// ── 步骤 12: 创建 Runner 并执行 ──
	// Runner 是顶层编排器，组合所有组件，驱动 REPL 交互循环。
	runner := agent.NewRunner(client, mem.history, mem.summary, mem.memStore, mem.events, mem.extractor, mem.retriever, tools, sessions, uiInstance)
	// 注入运行时配置（maxIterations / resultLimit / compressThreshold 等）。
	runner.SetConfig(cfg)
	// 注入全局 + 项目级记忆 store：extractMemory 按类型分流写入对应目录。
	runner.SetMemoryStores(mem.globalMem, mem.projectMem)
	// 注入 s08 压缩管线（Prepare 四步无损 + compactHistory LLM 摘要；
	// transcript/tool-results 目录按会话隔离）。
	compactor := agent.NewCompactor(client, filepath.Join(activeDir, "transcripts"), filepath.Join(activeDir, "tool-results"))
	if cfg.ContextCharLimit != nil {
		compactor.SetContextCharLimit(*cfg.ContextCharLimit)
	}
	runner.SetCompactor(compactor)

	// one-shot 模式：同步执行单次查询后退出。
	// 成功 → 输出答案到 stdout，return（让 defer Close() 执行）。
	// 失败 → 输出错误到 stderr，退出码 1。
	if *oneShot != "" {
		answer, err := runner.RunOnce(context.Background(), *oneShot)
		if err != nil {
			fatal("one-shot 执行失败", err)
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
