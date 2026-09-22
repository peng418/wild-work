// handler_model_isolation_test.go 回归测试：并发请求下 model 字段不得串号。
//
// 历史缺陷：qoder 渠道曾用 Client 上的全局 lastModel 记录上次请求的模型名，
// ChatStream 写入、Stream/Aggregate 再读出——两条语句之间是竞态窗口，
// 并发请求 A/B 会让 A 的响应带上 B 的模型名。修复方式是把 model 作为显式入参
// 从 HTTP 层一路透传到 Stream/Aggregate（见 provider.Upstream 注释）。
//
// 本测试锁定该契约：N 个 goroutine 各发不同模型名，断言每个响应回填自己的名字。
// 建议配合 -race 运行：go test -race ./internal/server/
package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"wild-work/internal/auth"
	"wild-work/internal/pool"
	"wild-work/internal/provider"
)

// echoUpstream 假上游：ChatStream 返回固定 SSE；Stream/Aggregate 把入参 model 回填，
// 便于断言「三层（HTTP → Upstream → 响应）的 model 是同一个值」。
type echoUpstream struct{}

func (echoUpstream) RefreshToken(*auth.Auth) error { return nil }

func (echoUpstream) ChatStream(*auth.Auth, []byte) (io.ReadCloser, int, []byte, error) {
	const sse = `data: {"id":"c1","object":"chat.completion.chunk","model":"upstream-auto",` +
		`"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n" +
		`data: {"id":"c1","object":"chat.completion.chunk","model":"upstream-auto",` +
		`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
		"data: [DONE]\n\n"
	return io.NopCloser(strings.NewReader(sse)), 200, nil, nil
}

func (echoUpstream) FetchModels(*auth.Auth) ([]provider.ModelInfo, error) { return nil, nil }
func (echoUpstream) FetchModelPricing(*auth.Auth) ([]provider.ModelPricing, error) {
	return nil, nil
}
func (echoUpstream) UserResource(*auth.Auth) (int64, error) { return 1, nil }
func (echoUpstream) UserResourceDetail(*auth.Auth) (int64, []provider.ResourceItem, error) {
	return 0, nil, nil
}
func (echoUpstream) DailyCheckin(*auth.Auth) error { return nil }
func (echoUpstream) Classify(int, string) provider.ErrKind {
	return provider.ErrNone
}

// Stream 把入参 model 写进每个 chunk，模拟「上游返回 auto、网关回填客户端名」。
func (echoUpstream) Stream(w http.ResponseWriter, r io.Reader, model string) error {
	w.Header().Set("Content-Type", "text/event-stream")
	if f, ok := w.(http.Flusher); ok {
		defer f.Flush()
	}
	body, _ := io.ReadAll(r)
	for _, line := range strings.Split(string(body), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
			continue
		}
		chunk["model"] = model // 关键：model 来自入参而非共享状态
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", raw)
	}
	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	return nil
}

func (echoUpstream) Aggregate(r io.Reader, model string) (map[string]any, error) {
	raw, _ := io.ReadAll(r)
	content := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) == nil && len(chunk.Choices) > 0 {
			content += chunk.Choices[0].Delta.Content
		}
	}
	return map[string]any{
		"id": "c1", "object": "chat.completion", "created": time.Now().Unix(),
		"model": model, // 关键：回填客户端请求名
		"choices": []any{map[string]any{
			"index": 0, "message": map[string]any{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
	}, nil
}

// newEchoHandler 构造单渠道 Handler + Pool（含一个永不过期的账号）。
func newEchoHandler() *Handler {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "t", ExpiresAt: time.Now().Add(24 * time.Hour).Unix()})
	rt := &Runtime{Kind: provider.Qoder, Pool: p, Upstream: echoUpstream{}}
	return NewHandler(Config{Runtimes: map[provider.Kind]*Runtime{provider.Qoder: rt}})
}

// TestModelNotCrossedAcrossConcurrentRequests 并发请求不同模型，响应 model 必须各自正确。
func TestModelNotCrossedAcrossConcurrentRequests(t *testing.T) {
	h := newEchoHandler()
	const n = 32

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("qoder/model-%d", i)
			body := fmt.Sprintf(`{"model":%q,"stream":false,"messages":[{"role":"user","content":"hi"}]}`, model)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("model=%s status=%d body=%s", model, rec.Code, rec.Body.String())
				return
			}
			var resp struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				errs <- fmt.Sprintf("model=%s unmarshal: %v", model, err)
				return
			}
			if resp.Model != model {
				errs <- fmt.Sprintf("串号：请求 %s，响应 %s", model, resp.Model)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// TestModelNotCrossedInStreaming 流式路径同样不得串号（chunk.model 即客户端请求名）。
func TestModelNotCrossedInStreaming(t *testing.T) {
	h := newEchoHandler()
	const n = 32

	var wg sync.WaitGroup
	errs := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			model := fmt.Sprintf("qoder/stream-model-%d", i)
			body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				errs <- fmt.Sprintf("model=%s status=%d", model, rec.Code)
				return
			}
			for _, line := range strings.Split(rec.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: ") || strings.Contains(line, "[DONE]") {
					continue
				}
				var chunk struct {
					Model string `json:"model"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) != nil {
					continue
				}
				if chunk.Model != model {
					errs <- fmt.Sprintf("流式串号：请求 %s，chunk %s", model, chunk.Model)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
