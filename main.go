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

// main 是程序入口：初始化 LLM、Memory 三层存储和 Agent Runner。
func main() {
	// sessions 参数用于指定会话目录，默认写到 data/sessions。
	sessionsDir := flag.String("sessions", "./data/sessions", "sessions directory path")
	sessionID := flag.String("session", "", "resume a specific session by ID (optional)")
	envFile := flag.String("env", ".env", "env file path")
	flag.Parse()

	// 尝试从 .env 文件加载环境变量（不覆盖已有值）。
	if err := loadEnvFile(*envFile); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "warning: load env file failed: %v\n", err)
	}

	// 从环境变量创建 OpenAI 客户端。
	client, err := llm.NewOpenAIClientFromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "init llm client failed: %v\n", err)
		os.Exit(1)
	}

	// 初始化会话管理器。
	sessions, err := session.NewSessionManager(*sessionsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init session manager failed: %v\n", err)
		os.Exit(1)
	}

	// 如果指定了 -session flag，切换到该会话。
	if *sessionID != "" {
		if err := sessions.Switch(*sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "switch session failed: %v\n", err)
			os.Exit(1)
		}
	}

	// 初始化三层记忆存储。
	activeDir := sessions.ActiveSessionDir()

	history, err := memory.NewHistoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init history store failed: %v\n", err)
		os.Exit(1)
	}

	summary, err := memory.NewSummaryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init summary store failed: %v\n", err)
		os.Exit(1)
	}

	memStore, err := memory.NewMemoryStore(activeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init memory store failed: %v\n", err)
		os.Exit(1)
	}

	// 初始化 LLM 记忆提取器。
	extractor := memory.NewExtractor(client)

	// 初始化记忆检索器。
	retriever := memory.NewRetriever(history, summary, memStore)

	// 注册内置工具。
	tools := tool.NewRegistry()
	tools.Register(tool.NewShellTool())
	tools.Register(tool.NewFileTool())

	// 启动 ReAct agent 循环。
	runner := agent.NewRunner(client, history, summary, memStore, extractor, retriever, tools, sessions)
	if err := runner.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed: %v\n", err)
		os.Exit(1)
	}
}
