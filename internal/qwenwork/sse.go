// sse.go 处理千问办公嵌套 SSE：每行 data:{"headers":{...},"body":"<json-string>",
// "statusCodeValue":200,"statusCode":"OK"}，body 字段需二次解析得到标准 OpenAI chunk。
//
// 关键差异：网关对错误（如缺 request_id）也返回 HTTP 200，
// 错误信息包在 envelope 的 statusCodeValue>=400 里 —— 必须剥壳判错，
// 不能只看 HTTP 状态码（Buddy2api 的 envelope_error() 即为此设计）。
package qwenwork

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// envelope SSE 外层信封。
type envelope struct {
	Headers         map[string]any `json:"headers"`
	Body            json.RawMessage `json:"body"` // 可能是字符串（内层 JSON 文本）或对象
	StatusCodeValue int            `json:"statusCodeValue"`
	StatusCode      string         `json:"statusCode"`
}

// errText 从 envelope 提取错误文案；非错误返回空串。
func (e *envelope) errText() string {
	if e.StatusCodeValue < 400 {
		return ""
	}
	// body 可能是 {"code":"400","message":"..."} 或纯字符串
	var withMsg struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if err := json.Unmarshal(e.Body, &withMsg); err == nil && withMsg.Message != "" {
		return withMsg.Message
	}
	return truncate(string(e.Body), 200)
}

// innerChunk 解出内层 OpenAI chunk；无有效内容（[DONE]/{}）返回 nil。
func (e *envelope) innerChunk() (map[string]any, error) {
	if len(e.Body) == 0 {
		return nil, nil
	}
	// body 为字符串形式（最常见）：先解字符串再解 JSON
	var s string
	if err := json.Unmarshal(e.Body, &s); err == nil {
		t := strings.TrimSpace(s)
		if t == "" || t == "[DONE]" || t == "{}" {
			return nil, nil
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(t), &chunk); err != nil {
			return nil, nil // 跳过无法解析的行（不致命）
		}
		return chunk, nil
	}
	// body 为对象形式
	var chunk map[string]any
	if err := json.Unmarshal(e.Body, &chunk); err != nil {
		return nil, nil
	}
	if len(chunk) == 0 {
		return nil, nil
	}
	return chunk, nil
}

// parseNestedSSE 逐行解析嵌套 SSE：
//   - envelope statusCodeValue >= 400 → 返回错误（上游错误以 HTTP 200 包裹）
//   - body == "[DONE]"/"{}" → 正常结束
//   - body 二次解析失败 → 跳过该行（不致命）
func parseNestedSSE(r io.Reader, onChunk func(map[string]any) error) error {
	br := bufio.NewReaderSize(r, 256*1024)
	for {
		line, err := br.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "data:") {
			payload := strings.TrimPrefix(line, "data:")
			var env envelope
			if json.Unmarshal([]byte(payload), &env) == nil && env.StatusCodeValue != 0 {
				if msg := env.errText(); msg != "" {
					return fmt.Errorf("upstream %d: %s", env.StatusCodeValue, msg)
				}
			}
			if json.Unmarshal([]byte(payload), &env) == nil {
				chunk, _ := env.innerChunk()
				if chunk != nil {
					if err := onChunk(chunk); err != nil {
						return err
					}
				}
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}

// aggregate 聚合完整 SSE 为单个 OpenAI chat.completion 响应。
// model 参数覆盖响应中的 model 字段（上游回显的是档位 key）。
func aggregate(r io.Reader, model string) (map[string]any, error) {
	var (
		id           string
		created      float64
		content      strings.Builder
		reasoning    strings.Builder
		role         = "assistant"
		finishReason = "stop"
		usage        map[string]any
		toolCalls    = map[int]map[string]any{}
		toolOrder    []int
	)
	err := parseNestedSSE(r, func(chunk map[string]any) error {
		if v, ok := chunk["id"].(string); ok && id == "" {
			id = v
		}
		if v, ok := chunk["created"].(float64); ok && created == 0 {
			created = v
		}
		if u, ok := chunk["usage"].(map[string]any); ok {
			usage = u
		}
		ch, _ := chunk["choices"].([]any)
		for _, ci := range ch {
			c, _ := ci.(map[string]any)
			if c == nil {
				continue
			}
			if fr, ok := c["finish_reason"].(string); ok && fr != "" {
				finishReason = fr
			}
			delta, ok := c["delta"].(map[string]any)
			if !ok {
				continue
			}
			if r2, ok := delta["role"].(string); ok && r2 != "" {
				role = r2
			}
			if txt, ok := delta["content"].(string); ok {
				content.WriteString(txt)
			}
			if rc, ok := delta["reasoning_content"].(string); ok {
				reasoning.WriteString(rc)
			}
			if tcs, ok := delta["tool_calls"].([]any); ok {
				for _, tc := range tcs {
					call, ok := tc.(map[string]any)
					if !ok {
						continue
					}
					idx := 0
					if v, ok := call["index"].(float64); ok {
						idx = int(v)
					}
					merged, seen := toolCalls[idx]
					if !seen {
						merged = map[string]any{"index": idx}
						toolCalls[idx] = merged
						toolOrder = append(toolOrder, idx)
					}
					mergeToolCallDelta(merged, call)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if id == "" {
		id = fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	}
	if created == 0 {
		created = float64(time.Now().Unix())
	}
	message := map[string]any{
		"role":    role,
		"content": content.String(),
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolOrder) > 0 {
		sortInts(toolOrder)
		calls := make([]map[string]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			calls = append(calls, toolCalls[idx])
		}
		message["tool_calls"] = calls
	}
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": int64(created),
		"model":   model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	return resp, nil
}

// mergeToolCallDelta 把流式 tool_call 片段合并到累计对象：
// id/type/function.name 直覆盖，function.arguments 拼接。
func mergeToolCallDelta(merged, delta map[string]any) {
	if v, ok := delta["id"].(string); ok && v != "" {
		merged["id"] = v
	}
	if v, ok := delta["type"].(string); ok && v != "" {
		merged["type"] = v
	}
	df, _ := delta["function"].(map[string]any)
	if df == nil {
		return
	}
	mf, _ := merged["function"].(map[string]any)
	if mf == nil {
		mf = map[string]any{}
		merged["function"] = mf
	}
	if v, ok := df["name"].(string); ok && v != "" {
		mf["name"] = v
	}
	if v, ok := df["arguments"].(string); ok && v != "" {
		if prev, _ := mf["arguments"].(string); prev != "" {
			mf["arguments"] = prev + v
		} else {
			mf["arguments"] = v
		}
	}
}

// sortInts 升序排序。
func sortInts(a []int) {
	for i := 0; i < len(a)-1; i++ {
		for j := i + 1; j < len(a); j++ {
			if a[j] < a[i] {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}

// streamAsOpenAI 把嵌套 SSE 流边读边转写为标准 OpenAI SSE 给客户端。
// 每个 chunk 重写 model 字段为客户端模型名；末尾补 data: [DONE]。
func streamAsOpenAI(w io.Writer, r io.Reader, model string, flush func()) error {
	sawDone := false
	err := parseNestedSSE(r, func(chunk map[string]any) error {
		chunk["model"] = model
		raw, _ := json.Marshal(chunk)
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !sawDone {
		if _, err := io.WriteString(w, "data: [DONE]\n\n"); err != nil {
			return err
		}
		if flush != nil {
			flush()
		}
	}
	return nil
}

// Stream 实现 provider.Upstream：嵌套 SSE → 标准 OpenAI SSE 透传。
func Stream(w http.ResponseWriter, r io.Reader, model string) error {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	fl, _ := w.(http.Flusher)
	flush := func() {
		if fl != nil {
			fl.Flush()
		}
	}
	return streamAsOpenAI(w, r, model, flush)
}

// truncate 字符串截断（rune 安全）。
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}
