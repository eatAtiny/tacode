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

// main 是程序入口：初始化 LLM、Memory 和 Agent Runner。
func main() {
	// memory 参数用于指定记忆文件路径，默认写到 data 目录。
	memoryPath := flag.String("memory", "./data/memory.jsonl", "memory file path")
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

	// 初始化记忆存储（JSONL 文件）。
	store, err := memory.NewStore(*memoryPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init memory store failed: %v\n", err)
		os.Exit(1)
	}

	// 启动最小 agent 循环。
	runner := agent.NewRunner(client, store)
	if err := runner.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "agent run failed: %v\n", err)
		os.Exit(1)
	}
}
