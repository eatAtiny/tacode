package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"agentic/internal/llm"
	"agentic/internal/memory"
	"agentic/internal/prompt"

	"github.com/chzyer/readline"
)

// Runner 负责驱动单个 Agent 的循环执行。
type Runner struct {
	llm    *llm.OpenAIClient
	memory *memory.Store
}

// NewRunner 构造一个最小可运行的 Agent 执行器。
func NewRunner(client *llm.OpenAIClient, store *memory.Store) *Runner {
	return &Runner{
		llm:    client,
		memory: store,
	}
}

// newReadline 创建一个 readline 实例，用于逐行读取输入。
// readline 库原生支持 UTF-8，中文输入和删除均正常工作。
func newReadline(prompt string) (*readline.Instance, error) {
	return readline.NewEx(&readline.Config{
		Prompt: prompt,
	})
}

// Run 进入交互循环：读用户输入 -> 组装 Prompt -> 调 LLM -> 保存记忆。
// 输入 exit 可退出。
func (r *Runner) Run(ctx context.Context) error {
	fmt.Println("Agent started.")
	fmt.Println("Input your task each round. Type 'exit' to stop.")

	rl, err := newReadline("")
	if err != nil {
		return fmt.Errorf("init readline failed: %w", err)
	}
	defer rl.Close()

	for round := 1; ; round++ {
		// 设置本轮 prompt 并读取用户输入。
		rl.SetPrompt(fmt.Sprintf("[Round %d] You> ", round))
		input, err := rl.Readline()
		if err == readline.ErrInterrupt || err == io.EOF {
			fmt.Println("\nAgent stopped by user.")
			return nil
		}
		if err != nil {
			return fmt.Errorf("read input failed: %w", err)
		}

		input = strings.TrimSpace(input)
		if input == "" {
			fmt.Println("Input is empty, skip this round.")
			continue
		}
		if strings.EqualFold(input, "exit") {
			fmt.Println("Agent stopped by user.")
			return nil
		}

		// 将最近 20 轮记忆压缩成摘要，拼到本轮 prompt。
		digest := r.memory.Digest(20)
		roundPrompt := prompt.BuildRoundPrompt(round, digest, input)

		// 调用 LLM 获取本轮回答。
		output, err := r.llm.Chat(ctx, roundPrompt.System, roundPrompt.User)
		if err != nil {
			return fmt.Errorf("llm call failed at round %d: %w", round, err)
		}

		fmt.Printf("[Round %d] Agent> %s\n", round, output)

		// 每轮结束后都落盘记忆，并自动保留最近 20 轮。
		if err := r.memory.Append(round, input, output); err != nil {
			return fmt.Errorf("save memory failed at round %d: %w", round, err)
		}
	}
}
