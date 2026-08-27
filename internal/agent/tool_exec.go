// tool_exec.go 工具调用执行子系统：并发/串行分类、权限确认协议、中断注入、read-before-edit 状态维护。

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"agentic/internal/llm"
	"agentic/internal/tool"
)

// executeToolCalls 执行工具调用列表。
//
// 重构后支持并行执行：
//  1. 分类：并发安全工具（IsConcurrencySafe + IsReadOnly + Allow permission）
//     → 用 goroutine 并行执行
//  2. 其余工具 → 串行执行
//
// 并发批内按原始顺序 yield 事件和推入消息（并发批整体先于串行批）。
// 设计参考 Claude Code 的 StreamingToolExecutor。
//
// 返回：
//   - true: 继续执行
//   - false: 发生错误，需要退出
func (lc *queryLoopContext) executeToolCalls(toolCalls []llm.ToolCall, iter int) bool {
	// yield: 工具调用请求（含完整 toolCalls 列表和 token 统计）。
	lc.events <- QueryEvent{
		Type:         QueryEventToolCall,
		ToolCalls:    toolCalls,
		Iteration:    iter + 1,
		InputTokens:  lc.lastInputTokens,
		OutputTokens: lc.lastOutputTokens,
		TotalTokens:  lc.totalInputTokens + lc.totalOutputTokens,
	}

	// ── 分类：并发安全 vs 串行 ──
	//
	// 并发安全条件（以下条件同时满足）：
	//   1. 工具存在
	//   2. IsConcurrencySafe(args) == true
	//   3. IsReadOnly(args) == true
	//   4. CheckPermission(args).Allow == true（避免并发弹窗）
	type execItem struct {
		tc         llm.ToolCall
		t          tool.Tool
		concurrent bool
	}

	var concurrentItems []execItem
	var serialItems []execItem

	for _, tc := range toolCalls {
		t := lc.toolRegistry.Get(tc.Name)
		item := execItem{tc: tc, t: t}

		if t != nil && t.IsConcurrencySafe(tc.Arguments) && t.IsReadOnly(tc.Arguments) {
			if perm := t.CheckPermission(tc.Arguments); perm.Allow {
				item.concurrent = true
				concurrentItems = append(concurrentItems, item)
				continue
			}
		}
		serialItems = append(serialItems, item)
	}

	// ── 阶段 1: 并行执行并发安全工具 ──
	if len(concurrentItems) > 0 {
		concurrentTCs := make([]llm.ToolCall, len(concurrentItems))
		for i, item := range concurrentItems {
			concurrentTCs[i] = item.tc
		}
		lc.executeConcurrentTools(concurrentTCs, iter)
	}

	// ── 阶段 2: 串行执行其余工具 ──
	// 每执行一个工具前检查中断信号（/interrupt 等）：收到则中止后续工具，
	// 注入中断提示让 LLM 知道当前状态，继续循环让 LLM 调整策略。
	for _, item := range serialItems {
		if cmd := lc.peekInterrupt(); cmd != "" {
			lc.injectInterruptNotice(item.tc, iter, cmd)
			return true
		}
		if !lc.executeSingleTool(item.tc, iter) {
			return false
		}
	}

	return true
}

// peekInterrupt 非阻塞检查输入转发通道是否有控制命令（/interrupt、/retry）。
// 返回命令字符串；无命令或通道未启用时返回空字符串。
func (lc *queryLoopContext) peekInterrupt() string {
	if lc.inputForward == nil {
		return ""
	}
	select {
	case cmd := <-lc.inputForward:
		cmd = strings.TrimSpace(cmd)
		if cmd == "/interrupt" || strings.HasPrefix(cmd, "/retry") {
			return cmd
		}
		return ""
	default:
		return ""
	}
}

// injectInterruptNotice 注入中断提示到消息历史。
// 让 LLM 知道用户中断了工具执行，可据此调整策略（或直接回答）。
func (lc *queryLoopContext) injectInterruptNotice(tc llm.ToolCall, iter int, cmd string) {
	notice := fmt.Sprintf("用户已中断工具 %s 的执行（%s）。"+
		"请根据已有信息调整策略：可直接给出回答，或尝试其他方案。", tc.Name, cmd)
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:    "user",
		Content: notice,
	})
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: notice,
		IsError:    true,
		Iteration:  iter + 1,
	}
}

// toolExecResult 工具执行结果（用于并行执行收集）。
type toolExecResult struct {
	result  string
	isError bool
}

// executeConcurrentTools 并行执行一组并发安全工具。
//
// 所有工具并发执行（goroutine + WaitGroup），
// 但结果按原始顺序 yield 和推入消息（保证 LLM 上下文一致性）。
//
// 前置条件：传入的 toolCalls 均已通过并发安全检查
// （IsConcurrencySafe + IsReadOnly + CheckPermission.Allow）。
//
// 线程安全：
//   - 各工具读取独立文件，无竞争
//   - fileReads map 在并行写时由 mu 保护
//   - event channel 只在主 goroutine 写入
func (lc *queryLoopContext) executeConcurrentTools(toolCalls []llm.ToolCall, iter int) {
	// 结果切片（预分配，按索引存储，保持原始顺序）。
	results := make([]toolExecResult, len(toolCalls))

	var wg sync.WaitGroup
	var mu sync.Mutex // 保护 fileReads map

	for i, tc := range toolCalls {
		wg.Add(1)
		go func(idx int, tc llm.ToolCall) {
			defer wg.Done()

			t := lc.toolRegistry.Get(tc.Name)
			if t == nil {
				results[idx] = toolExecResult{
					result:  fmt.Sprintf("未知工具: %s", tc.Name),
					isError: true,
				}
				return
			}

			// 注入读取状态（read-before-edit 内聚）。
			if aware, ok := t.(tool.ReadStateAware); ok {
				aware.SetReadState(lc.fileReads)
			}

			// Pre-tool hooks。
			if hookErr := lc.toolRegistry.BeforeHooks(tc.Name, tc.Arguments); hookErr != nil {
				results[idx] = toolExecResult{
					result:  fmt.Sprintf("工具执行被阻止: %v", hookErr),
					isError: true,
				}
				return
			}

			result, execErr := t.Execute(tc.Arguments)
			if execErr != nil {
				result = fmt.Sprintf("工具执行出错: %v\n请分析错误原因并尝试其他方案。", execErr)
			}

			if strings.TrimSpace(result) == "" {
				result = "(无输出)"
			}

			// 统一截断 + 大结果持久化。
			limit := t.ResultLimit()
			if limit <= 0 {
				limit = lc.resultLimit
			}
			if len(result) > limit {
				fullResult := result
				result = tool.TruncateResult(result, limit)
				if savedPath, err := tool.SaveLargeResult(tool.DefaultToolResultsDir, tc.Name, fullResult); err == nil {
					result += fmt.Sprintf("\n\n💾 完整结果已保存到: %s（可使用 file read 读取）", savedPath)
				}
			}

			// Post-tool hooks。
			lc.toolRegistry.AfterHooks(tc.Name, tc.Arguments, result, execErr)

			// 记录文件 mtime（并行安全：mu 保护）。
			if absPath, ok := extractFilePath(tc.Arguments); ok {
				if info, err := os.Stat(absPath); err == nil {
					mu.Lock()
					lc.fileReads[absPath] = info.ModTime()
					mu.Unlock()
				}
			}

			results[idx] = toolExecResult{
				result:  result,
				isError: execErr != nil,
			}
		}(i, tc)
	}

	wg.Wait()

	// 按原始顺序 yield 事件 + 推入消息。
	for i, tc := range toolCalls {
		r := results[i]

		lc.events <- QueryEvent{
			Type:       QueryEventToolResult,
			ToolName:   tc.Name,
			ToolResult: r.result,
			IsError:    r.isError,
			Iteration:  iter + 1,
		}

		lc.messages = append(lc.messages, llm.ChatMessage{
			Role:       "tool",
			Content:    r.result,
			ToolCallID: tc.ID,
		})
	}
}

// executeSingleTool 执行单个工具调用。
//
// 子流程（按顺序，重构后）：
//  1. 全局禁止列表检查（isToolForbidden → 直接拒绝）
//  2. 工具自检权限（Tool.CheckPermission → Allow/Confirm）
//  3. toolRegistry.Get() → 查找工具实现
//  4. read-before-edit 检测（edit 工具：验证文件已读 + mtime 未变）
//  5. tool.Execute() → 执行工具
//  6. 统一结果截断（Tool.ResultLimit + TruncateResult）
//  7. yield ToolResult 事件
//  8. 记录文件 mtime（用于 read-before-edit）
//  9. 推入 tool 消息到历史
//
// 返回：
//   - true: 继续执行（即使工具执行出错也继续，让 LLM 自行处理错误）
//   - false: 发生致命错误，需要退出
func (lc *queryLoopContext) executeSingleTool(tc llm.ToolCall, iter int) bool {
	// ── 子步骤 1: 全局禁止列表 ──
	if isToolForbidden(tc.Name) {
		errMsg := fmt.Sprintf("工具已被禁止使用: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 2: 查找工具 ──
	t := lc.toolRegistry.Get(tc.Name)
	if t == nil {
		errMsg := fmt.Sprintf("未知工具: %s", tc.Name)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 3: 权限检查（工具自检） ──
	if !lc.checkToolPermission(tc, t, iter) {
		return true // 权限拒绝，但继续执行下一个工具
	}

	// ── 子步骤 4: 注入读取状态（read-before-edit 内聚） ──
	if aware, ok := t.(tool.ReadStateAware); ok {
		aware.SetReadState(lc.fileReads)
	}

	// ── 子步骤 4b: Pre-tool hooks ──
	if hookErr := lc.toolRegistry.BeforeHooks(tc.Name, tc.Arguments); hookErr != nil {
		errMsg := fmt.Sprintf("工具执行被钩子阻止: %v", hookErr)
		lc.yieldToolError(tc, errMsg, iter)
		return true
	}

	// ── 子步骤 5: 执行工具 ──
	result, execErr := t.Execute(tc.Arguments)
	if execErr != nil {
		// 工具执行出错时，将错误信息作为结果返回给 LLM。
		// LLM 会看到错误并尝试其他方案（而非直接失败）。
		result = fmt.Sprintf("工具执行出错: %v\n请分析错误原因并尝试其他方案。", execErr)
	}

	// 空结果保护：命令成功但无输出时（如 mkdir、空 grep），
	// 用 "(无输出)" 占位，避免空 content 导致 API 报错。
	if strings.TrimSpace(result) == "" {
		result = "(无输出)"
	}

	// ── 子步骤 6: 统一结果截断 + 大结果持久化 ──
	// 框架层统一处理截断（head+tail 保留策略），工具无需自行截断。
	// 超过上限时：完整结果写入磁盘 → 模型可后续通过 file read 获取。
	limit := t.ResultLimit()
	if limit <= 0 {
		limit = lc.resultLimit
	}
	if len(result) > limit {
		fullResult := result
		result = tool.TruncateResult(result, limit)
		// 持久化完整结果到磁盘。
		if savedPath, err := tool.SaveLargeResult(tool.DefaultToolResultsDir, tc.Name, fullResult); err == nil {
			result += fmt.Sprintf("\n\n💾 完整结果已保存到: %s（可使用 file read 读取）", savedPath)
		}
	}

	// ── Post-tool hooks ──
	lc.toolRegistry.AfterHooks(tc.Name, tc.Arguments, result, execErr)

	// ── 子步骤 7: yield 工具执行结果 ──
	lc.events <- QueryEvent{
		Type:       QueryEventToolResult,
		ToolName:   tc.Name,
		ToolResult: result,
		IsError:    execErr != nil,
		Iteration:  iter + 1,
	}

	// ── 子步骤 8: 记录文件 mtime（用于 read-before-edit 检测） ──
	lc.recordFileRead(tc.Arguments)

	// ── 子步骤 9: 推入消息历史 ──
	// tool 角色消息包含 ToolCallID，LLM 可以关联到对应的 tool_call。
	lc.messages = append(lc.messages, llm.ChatMessage{
		Role:       "tool",
		Content:    result,
		ToolCallID: tc.ID,
	})

	return true
}

// checkToolPermission 使用工具自身的 CheckPermission 方法检查权限。
//
// 重构后逻辑：
//   - 先检查全局权限注入点（globalPermissionChecker，非 nil 时覆盖）
//   - 否则调用 Tool.CheckPermission(args)
//     → Allow=true → 直接允许
//     → Allow=false → yield Permission 事件 → 阻塞等待用户确认
//
// 阻塞机制：
//
//	queryLoop 创建 PermissionCh channel → yield Permission 事件
//	→ QueryEngine 收到事件 → 调用 UI.ConfirmPermission()
//	→ 用户在终端输入 y/N → 写入 PermissionCh
//	→ queryLoop 从 PermissionCh 读取结果 → 继续或拒绝
func (lc *queryLoopContext) checkToolPermission(tc llm.ToolCall, t tool.Tool, iter int) bool {
	// ── 全局权限注入点（极端定制场景） ──
	if globalPermissionChecker != nil {
		if !globalPermissionChecker.CheckPermission(tc.Name, tc.Arguments) {
			errMsg := "全局权限策略拒绝执行"
			lc.yieldToolError(tc, errMsg, iter)
			return false
		}
		return true
	}

	// ── 工具自检权限 ──
	perm := t.CheckPermission(tc.Arguments)
	if perm.Allow {
		return true
	}

	// ── 需要确认 ──
	ch := make(chan bool, 1)
	lc.events <- QueryEvent{
		Type:             QueryEventPermission,
		PermissionTool:   tc.Name,
		PermissionArgs:   tc.Arguments,
		PermissionReason: perm.Reason,
		PermissionCh:     ch,
		Iteration:        iter + 1,
	}

	// 阻塞等待用户确认。
	approved := <-ch
	if !approved {
		errMsg := "用户拒绝执行"
		lc.yieldToolError(tc, errMsg, iter)
		return false
	}

	// 用户确认通过：若工具支持放行接口（如 ShellTool 的 network 放行），
	// 通知工具记录"已放行"，Execute 时据此重建允许网络/权限的沙箱。
	if allow, ok := t.(interface{ AllowNetworkFor(args string) }); ok {
		allow.AllowNetworkFor(tc.Arguments)
	}

	return true
}

// ──────────────────────────────────────────────────────────
// Read-Before-Edit + mtime 追踪
// ──────────────────────────────────────────────────────────

// extractFilePath 从工具参数中提取文件绝对路径。
//
// 返回绝对路径和是否成功提取。
// 用于 mtime 追踪的集中提取逻辑，避免在多个函数中重复解析。
func extractFilePath(args string) (string, bool) {
	var params struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(args), &params); err != nil || params.Path == "" {
		return "", false
	}
	absPath, err := filepath.Abs(params.Path)
	if err != nil {
		return "", false
	}
	return absPath, true
}

// recordFileRead 从工具参数中提取文件路径，记录其 mtime。
//
// 用于 read-before-edit 检测：编辑类工具（edit、file write）执行前，
// 通过 checkReadBeforeEdit 验证目标文件已被读取且未被外部修改。
//
// 只处理包含 "path" 参数的工具：file(read)、edit、grep（文件模式时）。
func (lc *queryLoopContext) recordFileRead(args string) {
	absPath, ok := extractFilePath(args)
	if !ok {
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return
	}

	lc.fileReads[absPath] = info.ModTime()
}
