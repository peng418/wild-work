// body.go 构造 agent_chat_generation 请求体。
// 以 qoder2api baseprompt.json 模板为准（区别于 qoderwork 渠道的精简体）：
//   - session_type: "qoder"（qoderwork 是 "qodercli"）
//   - 额外字段：is_retry/code_language/source/version/chat_prompt/parameters/task_id
//   - model_config 全字段（key/display_name/format/is_vl/api_key/url/source/max_input_tokens）
//   - aliyun_user_type 用账号实测 userType
package qodercn

import (
	"encoding/json"
	"time"
)

// buildAgentBody 构造请求体。
//   - messages：客户端原始消息列表（可含 system/assistant/tool 多轮）
//   - mc：model_config 条目（来自动态模型表；nil 时用 auto 兜底）
//   - tools：客户端传来的 OpenAI tools 数组；为空则不注入 tools 字段
//   - enableReasoning：是否启用思考模式
//   - maxTokens：客户端请求的 max_tokens，<=0 时用模板默认 32768
//
// 注意：developer 角色必须改写为 system。
func buildAgentBody(messages []map[string]any, mc *ModelEntry, tools []any, enableReasoning bool, maxTokens int, userType string) ([]byte, error) {
	// developer → system（浅拷贝消息避免污染调用方数据）
	msgs := make([]map[string]any, len(messages))
	for i, m := range messages {
		cp := make(map[string]any, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		if r, _ := cp["role"].(string); r == "developer" {
			cp["role"] = "system"
		}
		msgs[i] = cp
	}

	// 最后一条 user 消息文本（chat_context.text 上游协议要求必填）
	prompt := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" {
			if c, ok := msgs[i]["content"].(string); ok && c != "" {
				prompt = c
				break
			}
		}
	}

	if maxTokens <= 0 {
		maxTokens = 32768 // qoder2api baseprompt.json parameters.max_tokens 默认
	}
	if userType == "" {
		userType = "personal_standard"
	}
	modelCfg := modelConfigFrom(mc, enableReasoning)

	now := time.Now()
	newUUID := uuid4()

	base := map[string]any{
		"request_id":       newUUID,
		"chat_record_id":   newUUID,
		"request_set_id":   uuid4(),
		"session_id":       uuid4(),
		"stream":           true,
		"aliyun_user_type": userType,
		"agent_id":         "agent_common",
		"chat_task":        "FREE_INPUT",
		"is_reply":         true,
		// qoder2api baseprompt.json 模板独有字段
		"is_retry":      false,
		"code_language": "",
		"source":        1,
		"version":       "3",
		"chat_prompt":   "",
		"task_id":       "common",
		"parameters":    map[string]any{"max_tokens": maxTokens},
		"session_type":  "qoder", // qoderwork 渠道是 "qodercli"
		"model_config":  modelCfg,
		"chat_context": map[string]any{
			"chatPrompt": "",
			"text":       map[string]any{"type": "text", "text": prompt},
			"extra": map[string]any{
				"context":         []any{},
				"modelConfig":     copyModelConfigLite(modelCfg),
				"originalContent": map[string]any{"type": "text", "text": prompt},
			},
			"features":  []any{},
			"imageUrls": nil,
		},
		"messages": msgs,
		"business": map[string]any{
			"id":       uuid4(),
			"begin_at": now.UnixMilli(),
			"name":     truncateRunes(prompt, 30),
		},
	}

	if len(tools) > 0 {
		base["tools"] = tools
	}

	return json.Marshal(base)
}

// ModelEntry 动态模型表条目（models.go 解析自上游）。
type ModelEntry struct {
	Key            string  `json:"key"`
	DisplayName    string  `json:"display_name"`
	Enable         bool    `json:"enable"`
	IsDefault      bool    `json:"is_default"`
	IsReasoning    bool    `json:"is_reasoning"`
	IsVL           bool    `json:"is_vl"`
	MaxInputTokens int64   `json:"max_input_tokens"`
	PriceFactor    float64 `json:"price_factor"`
	ContextWindow  int64   `json:"-"` // context_config.token_count 解析结果
}

// modelConfigFrom 构造 model_config（qoder2api baseprompt.json 全字段形态）。
func modelConfigFrom(m *ModelEntry, enableReasoning bool) map[string]any {
	if m == nil || m.Key == "" {
		return map[string]any{
			"key": "auto", "display_name": "Auto", "model": "", "format": "openai",
			"is_vl": false, "is_reasoning": enableReasoning, "api_key": "", "url": "",
			"source": "system", "max_input_tokens": 180000,
		}
	}
	format := "openai" // baseprompt 默认；上游未下发 format 字段时兜底
	maxIn := m.MaxInputTokens
	if maxIn <= 0 {
		maxIn = 180000
	}
	return map[string]any{
		"key": m.Key, "display_name": m.DisplayName, "model": "", "format": format,
		"is_vl": m.IsVL, "is_reasoning": enableReasoning, "api_key": "", "url": "",
		"source": "system", "max_input_tokens": maxIn,
	}
}

// copyModelConfigLite chat_context.extra.modelConfig 仅需 key/is_reasoning（模板形态）。
func copyModelConfigLite(mc map[string]any) map[string]any {
	return map[string]any{"key": mc["key"], "is_reasoning": mc["is_reasoning"]}
}

// truncateRunes 截断到 n 个 rune。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
