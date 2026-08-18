package llm

import (
	"context"
	"errors"
	"math/rand"
	"net"
	"net/http"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// defaultMaxRetries 默认最大重试次数（共 maxRetries+1 次尝试）。
const defaultMaxRetries = 3

// backoffBase 指数退避基数。
const backoffBase = 500 * time.Millisecond

// backoff 计算第 attempt 次重试前的等待时长（指数退避 + 单向抖动）。
// attempt 从 1 开始（第一次重试）。抖动范围 [0, base/5)，总等待 [base, 1.2*base)，
// 使多个同时重试的请求错开（避免惊群）。
func backoff(attempt int) time.Duration {
	base := backoffBase * time.Duration(1<<uint(attempt-1))
	jitter := time.Duration(rand.Int63n(int64(base) / 5)) // ±20%
	return base + jitter
}

// isRetryableError 判断错误是否可重试。
//
// 可重试：429 限流、5xx 服务端错误、网络/超时错误。
// 不可重试：401/403（认证）、400（参数错误）——重试无意义，直接失败。
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

// withRetry 对 fn 执行重试：可重试错误时指数退避后重试，最多 maxRetries 次。
//
// 退避等待尊重 ctx 取消（取消时立即返回 ctx.Err()）。
// 重试耗尽后返回最后一次原始错误（不包装，保持 APIError 类型）。
func withRetry[T any](ctx context.Context, maxRetries int, fn func() (T, error)) (T, error) {
	var zero T
	if maxRetries < 0 {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := backoff(attempt)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return zero, ctx.Err()
			case <-timer.C:
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
