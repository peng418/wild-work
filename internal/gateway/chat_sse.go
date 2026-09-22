// chat_sse.go 内层 Chat SSE 的解析：把上游 OpenAI chunk 流归一化后交给 visitor。
//
// Responses 与 Anthropic 两个转换器都需要「逐 chunk 读 + 增量 tool_calls 按 index 合并」，
// 抽到此处避免两份重复实现。
//
// 上游 chunk 的关键特性（内层渠道差异已由内层抹平）：
//   - delta.content / delta.reasoning_content 增量
//   - delta.tool_calls 分片到达：首片带 id/type/function.name，后续只带 arguments 片段
//   - finish_reason 在最后一个带 choices 的 chunk 上
//   - usage 可能出现在任意 chunk（OpenAI 官方在末 chunk，部分渠道在首 chunk）
package gateway

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// chatChunk 归一化后的单个流式分片。
type chatChunk struct {
	ID      string
	Model   string
	Content string
	// Reasoning 思考链增量（部分渠道的 reasoning_content 字段）。
	Reasoning string
	// ToolCalls 本分片新增的工具调用片段（未合并）。
	ToolCalls []toolCallDelta
	// FinishReason 非空表示流结束原因（stop / length / tool_calls / content_filter）。
	FinishReason string
	// Usage 上游用量（部分渠道在末 chunk 提供；为 nil 表示未提供）。
	Usage map[string]any
}

// toolCallDelta 单个工具调用的增量片段。
type toolCallDelta struct {
	Index     int
	ID        string
	Name      string
	Arguments string
}

// iterateChatSSE 逐 chunk 解析内层 SSE，对每个有效分片调用 visit。
// visit 返回 error 时中断并返回该 error。
//
// 容错策略：无法解析的行（注释、event:、非法 JSON）直接跳过，
// 只有 IO 错误与 visit 返回的错误才终止——与内层各家渠道的宽容度保持一致。
func iterateChatSSE(r io.Reader, visit func(chatChunk) error) error {
	br := bufio.NewReaderSize(r, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if chunk, ok := parseChatSSELine(line); ok {
				if verr := visit(chunk); verr != nil {
					return verr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}

// parseChatSSELine 解析一行 SSE；非 data 行 / [DONE] / 非法 JSON 返回 ok=false。
func parseChatSSELine(line string) (chatChunk, bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(trimmed, "data:") {
		return chatChunk{}, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	if payload == "" || payload == "[DONE]" {
		return chatChunk{}, false
	}
	var raw struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Usage   map[string]any
		Choices []struct {
			Delta struct {
				Content          string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
				ToolCalls        []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal([]byte(payload), &raw) != nil {
		return chatChunk{}, false
	}

	c := chatChunk{ID: raw.ID, Model: raw.Model, Usage: raw.Usage}
	if len(raw.Choices) > 0 {
		d := raw.Choices[0].Delta
		c.Content = d.Content
		c.Reasoning = d.ReasoningContent
		c.FinishReason = raw.Choices[0].FinishReason
		for _, tc := range d.ToolCalls {
			c.ToolCalls = append(c.ToolCalls, toolCallDelta{
				Index: tc.Index, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments,
			})
		}
	}
	return c, true
}

// toolCallAccumulator 按 index 累积流式 tool_call 片段。
// arguments 拼接；id/name 取首次出现的非空值（后续分片通常缺省或重复）。
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*toolCallDelta
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: map[int]*toolCallDelta{}}
}

// Add 合并一个片段。
func (a *toolCallAccumulator) Add(d toolCallDelta) {
	cur, ok := a.byIdx[d.Index]
	if !ok {
		cur = &toolCallDelta{Index: d.Index}
		a.byIdx[d.Index] = cur
		a.order = append(a.order, d.Index)
	}
	if d.ID != "" {
		cur.ID = d.ID
	}
	if d.Name != "" {
		cur.Name = d.Name
	}
	cur.Arguments += d.Arguments
}

// Len 已累积的工具调用数量。
func (a *toolCallAccumulator) Len() int { return len(a.order) }

// List 按 index 升序返回累积结果。
func (a *toolCallAccumulator) List() []toolCallDelta {
	sorted := append([]int(nil), a.order...)
	for i := 0; i < len(sorted)-1; i++ { // 数量少，避免为此引入 sort 包
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j] < sorted[i] {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}
	out := make([]toolCallDelta, 0, len(sorted))
	for _, idx := range sorted {
		out = append(out, *a.byIdx[idx])
	}
	return out
}

// usageInt 从上游 usage map 取整数字段，兼容 int/float64/json.Number 三种解码结果。
func usageInt(usage map[string]any, keys ...string) int {
	for _, k := range keys {
		switch v := usage[k].(type) {
		case float64:
			return int(v)
		case int:
			return v
		case int64:
			return int(v)
		}
	}
	return 0
}
