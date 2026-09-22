package workbuddyai

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRetryableStatus 仅 5xx 可重试；4xx 一律不重试（风控/余额/参数语义）。
func TestRetryableStatus(t *testing.T) {
	cases := map[int]bool{
		200: false, 201: false,
		400: false, // 参数错误（如 stream:false / tool_choice 对象）
		401: false, // 未授权
		402: false, // 疑似余额不足，不重试
		403: false, // 疑似风控，不重试
		404: false,
		408: false, // 请求超时语义由客户端控制，不重试
		429: false, // 限流：重试会放大，且可能触发风控
		500: true, 502: true, 503: true, 504: true,
		599: true, 600: false,
	}
	for code, want := range cases {
		if got := retryableStatus(code); got != want {
			t.Errorf("retryableStatus(%d) = %v, want %v", code, got, want)
		}
	}
}

// TestRetryableErr 瞬态传输错误重试；证书/协议类不重试。
func TestRetryableErr(t *testing.T) {
	// 可重试
	retryable := []error{
		&net.OpError{Op: "dial", Err: errors.New("connection attempt failed")},
		errors.New("connection reset by peer"),
		errors.New("unexpected EOF"),
		errors.New("dial tcp 1.2.3.4:443: i/o timeout"),
		errors.New("Empty reply from server"),
	}
	for _, e := range retryable {
		if !retryableErr(e) {
			t.Errorf("retryableErr(%v) = false, want true", e)
		}
	}
	// 不可重试
	noRetry := []error{
		nil,
		errors.New("x509: certificate signed by unknown authority"),
		errors.New("tls: unsupported protocol version"),
		errors.New("malformed HTTP response"),
	}
	for _, e := range noRetry {
		if retryableErr(e) {
			t.Errorf("retryableErr(%v) = true, want false", e)
		}
	}
}

// TestBackoffGrows 退避应递增且带抖动（不为固定值）。
func TestBackoffGrows(t *testing.T) {
	// 抖动用随机数，取多次统计区间
	for attempt := 1; attempt <= 3; attempt++ {
		base := retryBaseDelay << (attempt - 1)
		lo, hi := time.Duration(0), time.Duration(0)
		for i := 0; i < 50; i++ {
			d := backoff(attempt)
			if lo == 0 || d < lo {
				lo = d
			}
			if d > hi {
				hi = d
			}
		}
		// 抖动区间约为 base 的 [0.75, 1.25]
		if lo < base*3/4 || hi > base*5/4 {
			t.Errorf("attempt=%d backoff 区间 [%v, %v] 超出预期（base=%v）", attempt, lo, hi, base)
		}
	}
}

// TestChatStreamRetries5xx 5xx 应重试直至成功；且不应把 5xx 当成终态。
func TestChatStreamRetries5xx(t *testing.T) {
	var calls int
	srv := testServer(func(w http.ResponseWriter, r *http.Request, n int) {
		calls = n
		if n < 3 {
			// 前两次模拟网关 504
			w.WriteHeader(http.StatusGatewayTimeout)
			_, _ = w.Write([]byte("<html>504 Gateway Time-out</html>"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"x\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	})
	defer srv.Close()
	c := NewWithBase(srv.URL)
	rc, status, _, err := c.ChatStream(testAuth(), []byte(`{"model":"hy3","messages":[]}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if status != 200 || rc == nil {
		t.Fatalf("want 200 with body after retry, got status=%d rc=%v calls=%d", status, rc, calls)
	}
	rc.Close()
	if calls != 3 {
		t.Fatalf("expect 3 attempts (2 x 5xx + 1 ok), got %d", calls)
	}
}

// TestChatStreamNoRetry4xx 4xx 不应重试（只应有 1 次请求）。
func TestChatStreamNoRetry4xx(t *testing.T) {
	for _, code := range []int{400, 402, 403, 429} {
		var calls int
		srv := testServer(func(w http.ResponseWriter, r *http.Request, n int) {
			calls = n
			w.WriteHeader(code)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"code":%d,"msg":"nope"}`, code)))
		})
		c := NewWithBase(srv.URL)
		_, status, _, err := c.ChatStream(testAuth(), []byte(`{"model":"hy3","messages":[]}`))
		if err != nil {
			t.Fatalf("code=%d err=%v", code, err)
		}
		if status != code {
			t.Fatalf("code=%d got status=%d", code, status)
		}
		if calls != 1 {
			t.Errorf("code=%d 不应重试，但请求了 %d 次", code, calls)
		}
		srv.Close()
	}
}

// TestChatStreamGivesUpAfterMax 5xx 持续失败时应在 maxAttempts 后放弃。
func TestChatStreamGivesUpAfterMax(t *testing.T) {
	var calls int
	srv := testServer(func(w http.ResponseWriter, r *http.Request, n int) {
		calls = n
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502</html>"))
	})
	defer srv.Close()
	c := NewWithBase(srv.URL)
	_, status, body, err := c.ChatStream(testAuth(), []byte(`{"model":"hy3","messages":[]}`))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if status != 502 {
		t.Fatalf("want 502, got %d", status)
	}
	if !strings.Contains(string(body), "502") {
		t.Fatalf("body = %s", body)
	}
	if calls != retryMaxAttempts {
		t.Fatalf("want %d attempts, got %d", retryMaxAttempts, calls)
	}
}
