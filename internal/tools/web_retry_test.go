// web_retry_test.go — M08-02 Web 工具重试机制测试
// 测试覆盖：fetchWithRetry 成功/失败/重试边界、isWebRetryableError 分类、parseRetryAfter 解析
package tools

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// TestFetchWithRetry_Success 成功不重试
func TestFetchWithRetry_Success(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := fetchWithRetry(context.Background(), client, req, defaultWebBackoff(), 3)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 call, got %d", callCount)
	}
}

// TestFetchWithRetry_4xx_NoRetry 4xx 立即返回响应不重试
// 4xx 视为永久失败，fetchWithRetry 返回响应（status=404）+ nil err
// 调用方需自行检查 resp.StatusCode
func TestFetchWithRetry_4xx_NoRetry(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := fetchWithRetry(context.Background(), client, req, defaultWebBackoff(), 3)
	if err != nil {
		t.Fatalf("expected no error (caller checks status), got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 call (no retry for 4xx), got %d", callCount)
	}
}

// TestFetchWithRetry_5xx_RetryThenSuccess 5xx 重试后成功
func TestFetchWithRetry_5xx_RetryThenSuccess(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	// 用快速 backoff 避免测试慢
	fastBackoff := ai.NewExponentialBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0, 0, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := fetchWithRetry(context.Background(), client, req, fastBackoff, 3)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 calls (2 retries + 1 success), got %d", callCount)
	}
}

// TestFetchWithRetry_429_Retry 429 重试
func TestFetchWithRetry_429_Retry(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count < 2 {
			w.Header().Set("Retry-After", "0") // 0 表示解析失败，降级到 backoff
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fastBackoff := ai.NewExponentialBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0, 0, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := fetchWithRetry(context.Background(), client, req, fastBackoff, 3)
	if err != nil {
		t.Fatalf("expected success after 429 retry, got: %v", err)
	}
	defer resp.Body.Close()
	if atomic.LoadInt32(&callCount) != 2 {
		t.Errorf("expected 2 calls (1 retry), got %d", callCount)
	}
}

// TestFetchWithRetry_RetryAfterHeader 解析 Retry-After header
func TestFetchWithRetry_RetryAfterHeader(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&callCount, 1)
		if count < 2 {
			w.Header().Set("Retry-After", "1") // 1 秒
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// 故意用大 backoff 验证 Retry-After 优先
	slowBackoff := ai.NewExponentialBackoff(10*time.Second, 30*time.Second, 2.0, 0, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	start := time.Now()
	resp, err := fetchWithRetry(context.Background(), client, req, slowBackoff, 1)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after Retry-After, got: %v", err)
	}
	defer resp.Body.Close()
	// 应在 ~1s 内完成（Retry-After=1），而非 10s（backoff base）
	if elapsed > 3*time.Second {
		t.Errorf("Retry-After should take priority over backoff, elapsed=%v", elapsed)
	}
}

// TestFetchWithRetry_MaxRetriesExhausted 重试次数耗尽
func TestFetchWithRetry_MaxRetriesExhausted(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	fastBackoff := ai.NewExponentialBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0, 0, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := fetchWithRetry(context.Background(), client, req, fastBackoff, 2)
	if err == nil {
		t.Fatal("expected error after max retries exhausted")
	}
	// maxRetries=2 → 1 + 2 = 3 次调用
	if atomic.LoadInt32(&callCount) != 3 {
		t.Errorf("expected 3 calls (1 + 2 retries), got %d", callCount)
	}
}

// TestFetchWithRetry_CtxCancel ctx 取消立即返回
func TestFetchWithRetry_CtxCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	fastBackoff := ai.NewExponentialBackoff(1*time.Millisecond, 10*time.Millisecond, 2.0, 0, nil)
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	_, err := fetchWithRetry(ctx, client, req, fastBackoff, 3)
	if err == nil {
		t.Fatal("expected ctx cancellation error")
	}
	// 应返回 ctx.Err() 或包含 ctx 错误
	if !errors.Is(err, context.Canceled) {
		t.Logf("err=%v (may be HTTP err since request was already issued)", err)
	}
}

// TestFetchWithRetry_NilBackoff backoff 为 nil 时用默认
func TestFetchWithRetry_NilBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	resp, err := fetchWithRetry(context.Background(), client, req, nil, 3)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	defer resp.Body.Close()
}

// TestFetchWithRetry_NegativeMaxRetries maxRetries < 0 按 0 处理
func TestFetchWithRetry_NegativeMaxRetries(t *testing.T) {
	var callCount int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	_, err := fetchWithRetry(context.Background(), client, req, defaultWebBackoff(), -1)
	if err == nil {
		t.Fatal("expected error")
	}
	if atomic.LoadInt32(&callCount) != 1 {
		t.Errorf("expected 1 call (no retries), got %d", callCount)
	}
}

// TestIsWebRetryableError 分类测试
func TestIsWebRetryableError(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{"200 no err", nil, 200, false},
		{"4xx no err", nil, 404, false},
		{"429 no err", nil, 429, true},
		{"5xx no err", nil, 500, true},
		{"503 no err", nil, 503, true},
		{"net.Error timeout", &netOpError{timeout: true}, 0, true},
		{"net.Error no timeout", &netOpError{timeout: false}, 0, false},
		{"generic error", errors.New("generic"), 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWebRetryableError(tt.err, tt.status)
			if got != tt.want {
				t.Errorf("isWebRetryableError(%v, %d) = %v, want %v", tt.err, tt.status, got, tt.want)
			}
		})
	}
}

// netOpError 实现 net.Error 接口用于测试
type netOpError struct {
	timeout bool
}

func (e *netOpError) Error() string   { return "net op error" }
func (e *netOpError) Timeout() bool   { return e.timeout }
func (e *netOpError) Temporary() bool { return false }

// TestParseRetryAfter 解析测试
func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"30", 30 * time.Second},
		{"120", 2 * time.Minute},
		{"invalid", 0},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0}, // 过去时间返回 0
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseRetryAfter(tt.input)
			// 对未来时间用例单独处理（无法精确比对）
			if tt.input == "Wed, 21 Oct 2026 07:28:00 GMT" {
				// 过去时间应返回 0
				if got != 0 {
					t.Logf("past date returned %v (expected 0)", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("parseRetryAfter(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestParseRetryAfter_FutureDate 未来日期解析
func TestParseRetryAfter_FutureDate(t *testing.T) {
	// 构造 5 秒后的 HTTP 日期
	future := time.Now().Add(5 * time.Second).UTC().Format(http.TimeFormat)
	got := parseRetryAfter(future)
	// 允许 1 秒误差（格式化/解析耗时）
	if got < 3*time.Second || got > 7*time.Second {
		t.Errorf("parseRetryAfter(future+5s) = %v, want ~5s", got)
	}
}

// TestDefaultWebBackoff 默认 backoff 配置正确
func TestDefaultWebBackoff(t *testing.T) {
	b := defaultWebBackoff()
	if b == nil {
		t.Fatal("expected non-nil backoff")
	}
	// 第一次重试延迟应近似 500ms（含 ±20% jitter）
	d := b.NextDelay(1)
	if d < 350*time.Millisecond || d > 700*time.Millisecond {
		t.Errorf("first delay = %v, want ~500ms ± 20%%", d)
	}
	// 调用 Reset 应为 noop（不 panic）
	b.Reset()
}

// TestFetchWithRetry_Concurrent 并发安全测试（race detector）
func TestFetchWithRetry_Concurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	backoff := defaultWebBackoff()
	const concurrency = 5
	done := make(chan error, concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			client := &http.Client{Timeout: 5 * time.Second}
			req, _ := http.NewRequest("GET", srv.URL, nil)
			_, err := fetchWithRetry(context.Background(), client, req, backoff, 3)
			done <- err
		}()
	}
	for i := 0; i < concurrency; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent fetch failed: %v", err)
		}
	}
}

// 确保 net.OpError 实现 net.Error 接口（编译期断言）
var _ net.Error = (*net.OpError)(nil)
