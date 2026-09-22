// anthropic_stream.go Anthropic Messages API 流式（SSE）转换：Chat chunk → Anthropic 事件。
//
// 官方事件序列：
//
//	message_start
//	content_block_start (text)          ← 有文本时
//	content_block_delta (text_delta)    ×N
//	content_block_stop
//	content_block_start (tool_use)      ← 有工具调用时，每个调用一个块
//	content_block_delta (input_json_delta, partial_json) ×N
//	content_block_stop
//	message_delta (stop_reason + usage)
//	message_stop
//
// 与 tokligence-gateway 实现的差异（修掉了它的三处瑕疵）：
//  1. tool_use 的 arguments 以 input_json_delta 增量下发，而不是在 content_block_start
//     里一次性给完整 input —— 后者会让等待 partial_json 的客户端卡住。
//  2. stop_reason 按 finish_reason 如实映射（length→max_tokens、tool_calls→tool_use），
//     而不是恒为 end_turn。
//  3. usage 使用上游真实值（缺失时按文本长度估算并标注来源），而不是恒定 0。
package gateway

import (
	"net/http"
	"strings"
)

// streamAnthropic 把内层 Chat SSE 转成 Anthropic SSE 写回客户端。
func (g *Gateway) streamAnthropic(w http.ResponseWriter, r *http.Request, res *innerResult, clientModel string, req chatRequest) {
	defer res.Close()

	if res.Status >= 400 {
		raw, _ := res.ReadAll()
		writeUpstreamError(w, res.Status, raw, true)
		return
	}

	flush := sseHeader(w)
	msgID := "msg_" + randSuffix()

	// 请求侧是否带 tools：决定 message_start 里 content 的初始形态（保持为空数组即可）
	// 思考块必须先于文本块输出（Anthropic 要求块按 index 递增且成对出现），
	// 因此思考块固定占 index 0，文本块占 1，工具块从 2 起。
	const (
		idxThinking = 0
		idxText     = 1
		idxToolBase = 2
	)
	var (
		thinkingOpen bool
		thinkingSent bool
		textOpen     bool
		textSent     bool
		finish       string
		usage        map[string]any
		tools        = newToolCallAccumulator()
	)

	emit := func(event string, payload map[string]any) error {
		return writeSSEEvent(w, flush, event, payload)
	}

	startPayload := map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": msgID, "type": "message", "role": "assistant",
			"model": clientModel, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}
	if err := emit("message_start", startPayload); err != nil {
		return
	}

	// 思考链（上游 reasoning_content）→ thinking 内容块。
	// 部分思考模型只输出 reasoning_content 而无 content，不下发此块会导致客户端收到空消息。
	openThinking := func() error {
		if thinkingOpen {
			return nil
		}
		if textOpen { // 上游顺序异常（文本先于思考）：丢弃乱序的思考增量，保证块顺序合法
			return nil
		}
		thinkingOpen = true
		return emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": idxThinking,
			"content_block": map[string]any{"type": "thinking", "thinking": ""},
		})
	}
	closeThinking := func() error {
		if !thinkingOpen || thinkingSent {
			return nil
		}
		thinkingSent = true
		return emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": idxThinking})
	}
	// 文本块惰性开启：只有真的来文本才发 content_block_start
	openText := func() error {
		if textOpen {
			return nil
		}
		if err := closeThinking(); err != nil { // 思考块必须先关闭才能开文本块
			return err
		}
		textOpen = true
		return emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": idxText,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
	}
	closeText := func() error {
		if !textOpen || textSent {
			return nil
		}
		textSent = true
		return emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": idxText})
	}

	err := iterateChatSSE(res.Body, func(c chatChunk) error {
		if c.Usage != nil {
			usage = c.Usage
		}
		if c.FinishReason != "" {
			finish = c.FinishReason
		}
		if c.Reasoning != "" && !textOpen {
			if err := openThinking(); err != nil {
				return err
			}
			if thinkingOpen {
				if err := emit("content_block_delta", map[string]any{
					"type": "content_block_delta", "index": idxThinking,
					"delta": map[string]any{"type": "thinking_delta", "thinking": c.Reasoning},
				}); err != nil {
					return err
				}
			}
		}
		if c.Content != "" {
			if err := openText(); err != nil {
				return err
			}
			if err := emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idxText,
				"delta": map[string]any{"type": "text_delta", "text": c.Content},
			}); err != nil {
				return err
			}
		}
		for _, tc := range c.ToolCalls {
			tools.Add(tc)
		}
		return nil
	})
	if err != nil {
		// 流中途失败：补一个收尾事件，避免客户端永久等待
		_ = emit("error", map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": err.Error()}})
		_ = closeThinking()
		_ = closeText()
		_ = emit("message_stop", map[string]any{"type": "message_stop"})
		return
	}

	// 块必须先关后开（Anthropic 严格要求块按 index 顺序成对出现）
	if err := closeThinking(); err != nil {
		return
	}
	if err := closeText(); err != nil {
		return
	}

	// 工具调用：每个调用一个 tool_use 块，arguments 走 input_json_delta
	toolIdx := idxToolBase
	for _, tc := range tools.List() {
		idx := toolIdx
		toolIdx++
		callID := tc.ID
		if strings.TrimSpace(callID) == "" {
			callID = "toolu_" + randSuffix()
		}
		if err := emit("content_block_start", map[string]any{
			"type": "content_block_start", "index": idx,
			"content_block": map[string]any{
				"type": "tool_use", "id": callID, "name": tc.Name, "input": map[string]any{},
			},
		}); err != nil {
			return
		}
		if args := tc.Arguments; strings.TrimSpace(args) != "" {
			if err := emit("content_block_delta", map[string]any{
				"type": "content_block_delta", "index": idx,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
			}); err != nil {
				return
			}
		}
		if err := emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": idx}); err != nil {
			return
		}
	}

	// message_delta：携带 stop_reason 与最终 usage
	stopReason := anthropicStopReason(finish, tools.Len() > 0)
	delta := map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}}
	if u := anthropicUsage(usage); u != nil {
		delta["usage"] = u
	}
	if err := emit("message_delta", delta); err != nil {
		return
	}
	_ = emit("message_stop", map[string]any{"type": "message_stop"})
}

// handleCountTokens 处理 POST /v1/messages/count_tokens。
//
// 本项目无 tokenizer 依赖，采用字符启发式估算：中文按 1 字 ≈ 1 token、
// 其余按 4 字符 ≈ 1 token，再对消息条数与块数取下限。
// Claude Code 用它做上下文预算，数值偏保守（略高）比偏低更安全。
func (g *Gateway) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	body, ok := parseJSONBody(w, r, true)
	if !ok {
		return
	}
	tokens := estimateAnthropicTokens(body)
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": tokens})
}

// estimateAnthropicTokens 估算 Anthropic 请求的输入 token 数。
func estimateAnthropicTokens(body map[string]any) int {
	var cjk, other, blocks int
	countText := func(s string) {
		for _, r := range s {
			if r > 0x2E80 { // CJK 及全角符号：约 1 字 1 token
				cjk++
			} else {
				other++
			}
		}
	}
	var walk func(v any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			countText(t)
		case []any:
			for _, x := range t {
				walk(x)
			}
		case map[string]any:
			if typ, has := t["type"]; has {
				// 只统计内容类字段，避免把 name/id 等短标识符重复计入
				if s, ok := typ.(string); ok && (s == "text" || s == "tool_result" || s == "input_text" || s == "output_text") {
					blocks++
				}
			}
			for k, x := range t {
				switch k {
				case "text", "content", "input", "system", "tools", "name", "description":
					walk(x)
				}
			}
		}
	}
	walk(body["system"])
	walk(body["messages"])
	walk(body["tools"])

	tokens := cjk + other/4 + 1
	if min := blocks * 2; tokens < min { // 每个内容块至少有结构开销
		tokens = min
	}
	if msgs, ok := body["messages"].([]any); ok {
		if min := len(msgs) * 2; tokens < min {
			tokens = min
		}
	}
	return tokens
}
