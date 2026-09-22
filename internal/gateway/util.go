// util.go 协议转换共用的取值/赋值小工具。
//
// 设计取舍：三接口的请求体字段类型在客户端之间并不统一
// （同一个语义可能是 string / number / bool / null），
// 因此统一走「宽容取值」而非严格结构体解码，避免为兼容个别客户端而层层 fallback。
package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// asString 宽容取字符串：string 原样；number/bool 转字面量；nil 与其它返回空串。
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case float64:
		return trimFloat(t)
	case bool:
		if t {
			return "true"
		}
		return "false"
	}
	return ""
}

// asBool 宽容取布尔。
func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(strings.TrimSpace(t), "true")
	}
	return false
}

// asInt 宽容取整数（json 解码后数字为 float64）。
func asInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return int(n), true
		}
	case string:
		var n int
		if err := json.Unmarshal([]byte(t), &n); err == nil {
			return n, true
		}
	}
	return 0, false
}

// copyNumber 把 src 中的数值字段原样复制到 dst（键名可不同）。
func copyNumber(dst, src map[string]any, srcKey, dstKey string) {
	switch v := src[srcKey].(type) {
	case float64, int, int64, json.Number:
		dst[dstKey] = v
	}
}

// isEmptyValue 判定 null/空串/空数组/空对象。
func isEmptyValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	case []any:
		return len(t) == 0
	case map[string]any:
		return len(t) == 0
	}
	return false
}

// orEmptyObject 参数为 nil 时返回空 JSON Schema 对象。
// 上游普遍要求 tools[].function.parameters 存在，缺失会直接 400。
func orEmptyObject(v any) any {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		return m
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// firstNonEmpty 返回首个非空串。
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// stringifyOutput 把 function_call_output 的 output 转成字符串。
// 官方允许 string 或 ContentBlock 数组；数组时取其中的文本块。
func stringifyOutput(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any:
		if s := flattenText(t); strings.TrimSpace(s) != "" {
			return s
		}
	}
	if raw, err := json.Marshal(v); err == nil {
		return string(raw)
	}
	return ""
}

// clampMaxTokens 对 max_tokens 封顶；cap<=0 表示不限制。
func clampMaxTokens(v, capTokens int) int {
	if capTokens <= 0 || v <= 0 || v <= capTokens {
		return v
	}
	return capTokens
}

// randSuffix 生成 12 位十六进制随机后缀，用于拼装 item id。
func randSuffix() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil { // 随机源不可用时退化为时间戳，不影响正确性
		return trimFloat(float64(nowUnix()))
	}
	return hex.EncodeToString(b[:])
}

// responseID 生成 Responses 的响应 id：优先沿用上游 chatcmpl id 的随机部分。
func responseID(upstreamID string) string {
	if s := strings.TrimSpace(upstreamID); s != "" {
		if i := strings.Index(s, "-"); i >= 0 && i+1 < len(s) {
			return "resp_" + s[i+1:]
		}
		return "resp_" + s
	}
	return "resp_" + randSuffix()
}

// trimFloat 把 float64 转成无多余小数的字符串（1.0 → "1"）。
func trimFloat(f float64) string {
	if f == float64(int64(f)) {
		return json.Number(intToStr(int64(f))).String()
	}
	raw, _ := json.Marshal(f)
	return string(raw)
}

func intToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [24]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
