package llm

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	openai "github.com/sashabaranov/go-openai"
)

// ──────────────────────────────────────────────────────────
// TestIsRetryableError
// ──────────────────────────────────────────────────────────

func TestIsRetryableError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"429 rate limit", &openai.APIError{HTTPStatusCode: http.StatusTooManyRequests}, true},
		{"500 server", &openai.APIError{HTTPStatusCode: http.StatusInternalServerError}, true},
		{"503 unavailable", &openai.APIError{HTTPStatusCode: http.StatusServiceUnavailable}, true},
		{"401 auth", &openai.APIError{HTTPStatusCode: http.StatusUnauthorized}, false},
		{"400 bad request", &openai.APIError{HTTPStatusCode: http.StatusBadRequest}, false},
		{"403 forbidden", &openai.APIError{HTTPStatusCode: http.StatusForbidden}, false},
		{"wrapped 429", &wrapErr{&openai.APIError{HTTPStatusCode: http.StatusTooManyRequests}}, true},
		{"wrapped 401", &wrapErr{&openai.APIError{HTTPStatusCode: http.StatusUnauthorized}}, false},
		{"RequestError 429", &openai.RequestError{HTTPStatusCode: http.StatusTooManyRequests}, true},
		{"RequestError 503", &openai.RequestError{HTTPStatusCode: http.StatusServiceUnavailable}, true},
		{"RequestError 401", &openai.RequestError{HTTPStatusCode: http.StatusUnauthorized}, false},
		{"wrapped RequestError 429", &wrapErr{&openai.RequestError{HTTPStatusCode: http.StatusTooManyRequests}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryableError(c.err); got != c.want {
				t.Errorf("isRetryableError(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsRetryableError_Network(t *testing.T) {
	// 网络错误（net.Error 实现）→ 可重试。
	netErr := &fakeNetError{}
	if !isRetryableError(netErr) {
		t.Error("net.Error should be retryable")
	}
	// 被包装的网络错误。
	if !isRetryableError(&wrapErr{netErr}) {
		t.Error("wrapped net.Error should be retryable")
	}
	// 普通错误 → 不可重试。
	if isRetryableError(errors.New("plain error")) {
		t.Error("plain error should not be retryable")
	}
	// nil → 不可重试。
	if isRetryableError(nil) {
		t.Error("nil should not be retryable")
	}
	// context.DeadlineExceeded → 可重试。
	if !isRetryableError(context.DeadlineExceeded) {
		t.Error("DeadlineExceeded should be retryable")
	}
}

// ──────────────────────────────────────────────────────────
// TestWithRetry
// ──────────────────────────────────────────────────────────

func TestWithRetry_Success(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	fn := func() (int, error) {
		calls++
		if calls == 1 {
			return 0, &openai.APIError{HTTPStatusCode: http.StatusTooManyRequests}
		}
		return 42, nil
	}

	result, err := withRetry[int](context.Background(), 3, fn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
	if calls != 2 {
		t.Errorf("fn should be called 2 times, got %d", calls)
	}
}

func TestWithRetry_Exhausted(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	apiErr := &openai.APIError{HTTPStatusCode: http.StatusInternalServerError}
	fn := func() (string, error) {
		calls++
		return "", apiErr
	}

	_, err := withRetry[string](context.Background(), 3, fn)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if calls != 4 {
		t.Errorf("fn should be called 4 times (1 + 3 retries), got %d", calls)
	}
	// 返回最后一次原始错误（保持 APIError 类型）。
	if err != apiErr {
		t.Errorf("should return last original error, got %v", err)
	}
}

func TestWithRetry_NonRetryable(t *testing.T) {
	withFastBackoff(t)
	calls := 0
	apiErr := &openai.APIError{HTTPStatusCode: http.StatusUnauthorized}
	fn := func() (int, error) {
		calls++
		return 0, apiErr
	}

	_, err := withRetry[int](context.Background(), 3, fn)
	if err != apiErr {
		t.Errorf("should return original error immediately, got %v", err)
	}
	if calls != 1 {
		t.Errorf("fn should be called only once for non-retryable, got %d", calls)
	}
}

func TestWithRetry_CtxCancel(t *testing.T) {
	// 退避等待期间 ctx 取消 → 立即返回 ctx.Err()。
	// 注意：此处不用 1ms 快速退避，而是固定 50ms——确保退避窗口长于
	// 取消触发延迟（10ms），使取消在退避等待期间确定性发生，避免竞态。
	withBackoff(t, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	fn := func() (int, error) {
		calls++
		if calls == 1 {
			// 第一次失败后，取消 ctx（模拟 /stop 期间退避）。
			go func() {
				time.Sleep(10 * time.Millisecond)
				cancel()
			}()
			return 0, &openai.APIError{HTTPStatusCode: http.StatusTooManyRequests}
		}
		return 0, nil
	}

	_, err := withRetry[int](ctx, 3, fn)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("should return ctx.Err() when canceled during backoff, got %v", err)
	}
	if calls != 1 {
		t.Errorf("fn should not be called again after cancel, got %d calls", calls)
	}
}

// ──────────────────────────────────────────────────────────
// 测试辅助
// ──────────────────────────────────────────────────────────

// withFastBackoff 临时用固定短退避替换 backoffFn，测试结束后恢复。
// 避免真实指数退避（500ms 起）拖慢测试。
func withFastBackoff(t *testing.T) {
	t.Helper()
	withBackoff(t, time.Millisecond)
}

// withBackoff 临时用固定退避 d 替换 backoffFn，测试结束后恢复。
func withBackoff(t *testing.T, d time.Duration) {
	t.Helper()
	orig := backoffFn
	backoffFn = func(int) time.Duration { return d }
	t.Cleanup(func() { backoffFn = orig })
}

// fakeNetError 实现 net.Error 接口。
type fakeNetError struct{}

var _ net.Error = &fakeNetError{}

func (e *fakeNetError) Error() string   { return "fake network error" }
func (e *fakeNetError) Timeout() bool   { return true }
func (e *fakeNetError) Temporary() bool { return true }

// wrapErr 包装错误，测试 errors.As 穿透。
type wrapErr struct{ err error }

func (e *wrapErr) Error() string { return "wrapped: " + e.err.Error() }
func (e *wrapErr) Unwrap() error { return e.err }
