# LLM 调用重试设计文档

- 日期：2026-08-18
- 状态：已批准
- 范围：`internal/llm/retry.go`（新增重试辅助）+ `internal/llm/openai.go`（3 个调用点包裹）+ `internal/llm/retry_test.go`（测试）

## 背景

`internal/llm/openai.go` 的 `Chat`（:78）、`ChatWithTools`（:188）、`ChatWithToolsStream`（:291）对 API 错误零重试。网络抖动、429 限流、5xx 直接导致整轮查询失败。用户实测撞过 401——认证错误重试无意义，但 429/5xx/网络错误是瞬时的，重试能救回整轮。

## 决策

| 决策点 | 结论 |
|--------|------|
| 可重试错误 | 429（限流）、5xx（服务端）、网络/超时错误 |
| 不可重试 | 401/403（认证）、400（参数错误）——直接失败 |
| 重试参数 | 最多 3 次（共 4 次尝试），指数退避 500ms×2ⁿ + 随机抖动 |
| 流式边界 | 只重试 stream 创建前（`CreateChatCompletionStream`）；`Recv` 中断不重试（避免重复 tool_calls 副作用） |
| ctx 尊重 | 退避等待尊重 ctx 取消（`/stop` 立即退出，不傻等） |
| 错误包装 | `withRetry` 返回最后一次原始 error（保持 APIError 类型），不吞状态码 |

## 架构（方案 A + C）

```
internal/llm/retry.go（新增）
  ├─ isRetryableError(err) bool   // 429/5xx/网络/超时 → true
  ├─ backoff(attempt) time.Duration // 500ms × 2^n + 抖动
  └─ withRetry[T](ctx, maxRetries, fn) (T, error) // 泛型通用重试

internal/llm/openai.go（3 处调用点包裹）
  ├─ Chat:              resp, err := withRetry(ctx, 3, func() (*openai.ChatCompletionResponse, error) {...})
  ├─ ChatWithTools:     同上
  └─ ChatWithToolsStream: goroutine 内部包 CreateChatCompletionStream（Recv 不重试）
```

## 涉及文件

| 文件 | 改动 |
|------|------|
| `internal/llm/retry.go` | 新增：`isRetryableError` / `backoff` / `withRetry` |
| `internal/llm/openai.go` | 3 个调用点用 `withRetry` 包裹（`Chat`:78、`ChatWithTools`:188、`ChatWithToolsStream`:291） |
| `internal/llm/retry_test.go` | 新增：6 个测试 |

## 可重试判定

```go
// isRetryableError 判断错误是否可重试。
// 可重试：429 限流、5xx 服务端错误、网络/超时错误。
// 不可重试：401/403（认证）、400（参数）——重试无意义，直接失败。
func isRetryableError(err error) bool {
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) {
		return apiErr.HTTPStatusCode == http.StatusTooManyRequests ||
			apiErr.HTTPStatusCode >= 500
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded)
}
```

## withRetry

```go
// defaultMaxRetries 默认最大重试次数（共 maxRetries+1 次尝试）。
const defaultMaxRetries = 3

// backoffBase 指数退避基数。
const backoffBase = 500 * time.Millisecond

// withRetry 对 fn 执行重试：可重试错误时指数退避后重试，最多 maxRetries 次。
// 退避等待尊重 ctx 取消（取消时立即返回 ctx.Err()）。
// 重试耗尽后返回最后一次原始错误（不包装，保持 APIError 类型）。
func withRetry[T any](ctx context.Context, maxRetries int, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 退避等待（尊重 ctx 取消）。
			delay := backoff(attempt)
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(delay):
			}
		}
		result, err := fn()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableError(err) {
			return zero, err
		}
	}
	return zero, lastErr
}
```

## 流式接口包裹

`ChatWithToolsStream` 的 goroutine 内（`openai.go:291`）：

```go
	// ── 步骤 3: 创建流式连接（带重试） ──
	stream, err := withRetry(ctx, defaultMaxRetries, func() (*openai.ChatCompletionStream, error) {
		return c.client.CreateChatCompletionStream(ctx, req)
	})
	if err != nil {
		events <- StreamEvent{
			Type:  StreamEventError,
			Error: fmt.Errorf("create stream failed: %w", err),
		}
		return
	}
```

`Recv` 循环（`openai.go:308`）不重试——流已建立后的中断走既有 `StreamEventError` 路径。理由：重试已发送的 tool_calls 有重复执行写工具的副作用风险，ReAct 循环会让 LLM 在下一轮处理。

## 测试计划（`internal/llm/retry_test.go`）

纯逻辑测试，不碰真实 API：

| 测试 | 覆盖点 |
|------|--------|
| `TestIsRetryableError` | APIError 429/500/503 → true；401/400/403 → false |
| `TestIsRetryableError_Network` | net.Error（假实现）→ true；普通 error → false |
| `TestWithRetry_Success` | 第 2 次成功 → 返回结果，fn 调用 2 次 |
| `TestWithRetry_Exhausted` | 一直失败 → 重试 3 次后返回最后错误，fn 调用 4 次 |
| `TestWithRetry_NonRetryable` | 401 → 只调用 1 次，立即返回 |
| `TestWithRetry_CtxCancel` | 退避中 ctx 取消 → 立即返回 ctx.Err() |

## 集成

- 完成标准：`go build ./...` + `go test ./...` 全绿
- 无需改 main.go / CLI（重试参数暂用常量 `defaultMaxRetries=3`，不引入配置）
