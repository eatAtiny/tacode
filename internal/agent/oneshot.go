// oneshot.go headless 单次查询执行入口。

package agent

import (
	"context"
	"fmt"
	"strings"

	"tacode/internal/memory"
)

// RunOnce 执行一次查询并返回最终答案（one-shot / headless 模式）。
//
// 与 Run() 的区别：不进入 REPL 循环，同步执行单次查询后返回。
// 行为与 REPL 模式一致：复用 queryEngine（含记忆上下文构建、工具调用、
// 权限确认）、成功后保存 L1 历史并提取 L3 记忆（L2 摘要仅在压缩时生成——
// 当前未持久化到 summaries.jsonl，对话细节由跨轮累积 messages + EventStore
// 承载）、临时会话落盘。
//
// 使用场景：
//   - CLI 一次性运行（go run . -one-shot "任务"）
//   - 脚本 / CI / 子 agent 场景（配合 TextUI）
//
// 权限：one-shot 场景配合 TextUI 使用，ConfirmPermission 默认放行。
// inputForward 传 nil 安全——TextUI.ConfirmPermission 不读该参数。
//
// 注意：每次调用会重新初始化临时会话（分配新 ID），不适合在同一个 Runner
// 实例上连续调用多次。计划用法：CLI one-shot 为独立进程；子 agent 场景
// 每个子 agent 使用独立的 Runner 实例。
//
// 返回：
//   - string: 最终回答文本（LLM 的完整回复）
//   - error: 查询失败或记忆保存失败
func (r *Runner) RunOnce(ctx context.Context, input string) (string, error) {
	// 空输入保护。
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("one-shot 输入为空")
	}

	// ── 启动临时会话（与 Run() 共用） ──
	if err := r.initTempSession(); err != nil {
		return "", err
	}

	// ── 记录用户输入事件（与 Run() 一致） ──
	r.events.Append(memory.Event{
		Type:    memory.EventUser,
		Round:   1,
		Content: input,
	})

	// ── 同步执行单次查询 ──
	// round=1，inputForward=nil（TextUI 权限默认放行，不读此参数）。
	answer, messages, err := r.queryEngine(ctx, 1, input, nil, nil)
	if err != nil {
		return "", fmt.Errorf("one-shot 查询失败: %w", err)
	}
	r.messages = messages

	// ── 记录助手回答事件（与 Run() 一致） ──
	r.events.Append(memory.Event{
		Type:    memory.EventAssistant,
		Round:   1,
		Content: answer,
		Model:   r.llm.Model(),
	})

	// ── 保存记忆（与 Run() 分支 B 一致） ──
	if err := r.history.Append(1, input, answer); err != nil {
		return "", fmt.Errorf("save history failed: %w", err)
	}
	r.extractMemory(ctx, 1, input, answer)
	if r.isTemporary {
		if err := r.ensurePersisted(input); err != nil {
			return "", fmt.Errorf("保存会话失败: %w", err)
		}
	}

	return answer, nil
}
