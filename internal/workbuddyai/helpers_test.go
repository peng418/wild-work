package workbuddyai

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"wild-work/internal/auth"
)

// testAuth 构造最小可用凭据（无需真实 token）。
func testAuth() *auth.Auth {
	return &auth.Auth{
		Kind: "workbuddyai", AccessToken: "test-token",
		RefreshToken: "test-refresh", UID: "test-uid", Domain: "www.workbuddy.ai",
	}
}

// testServer 起一个本地 httptest 服务，handler 的 n 为第几次调用（从 1 开始）。
// 返回的 URL 可作为 Client.Base，避免测试触网。
func testServer(handler func(w http.ResponseWriter, r *http.Request, n int)) *httptest.Server {
	var n int64
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, int(atomic.AddInt64(&n, 1)))
	}))
}
