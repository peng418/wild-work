// handler_models_test.go 校验 /v1/models 的输出契约：
// 满足 OpenAI 官方字段，并按 OpenRouter / llama.cpp 惯例透传输入模态。
package server

import (
	"encoding/json"
	"testing"

	"wild-work/internal/provider"
)

// TestModelEntryContract 单条模型条目应含 OpenAI 官方字段 + architecture 模态声明。
func TestModelEntryContract(t *testing.T) {
	mi := provider.ModelInfo{
		ID: "vision-model", Name: "Vision", ContextWindow: 128000, MaxTokens: 8192,
		ContextFromAPI: true, SupportsImages: true,
	}
	entry := buildModelEntry(provider.WorkBuddyAI, mi)

	// OpenAI 官方规定字段
	for _, k := range []string{"id", "object", "created", "owned_by"} {
		if _, ok := entry[k]; !ok {
			t.Errorf("缺少 OpenAI 官方字段 %q", k)
		}
	}
	if entry["object"] != "model" {
		t.Errorf("object 应为 model，得到 %v", entry["object"])
	}
	if entry["id"] != "workbuddyai/vision-model" {
		t.Errorf("id 应带渠道前缀，得到 %v", entry["id"])
	}

	// 模态声明：OpenRouter / llama.cpp 通行形状
	arch, ok := entry["architecture"].(map[string]any)
	if !ok {
		t.Fatalf("缺少 architecture 对象，得到 %T", entry["architecture"])
	}
	mods, ok := arch["input_modalities"].([]string)
	if !ok || len(mods) != 2 || mods[0] != "text" || mods[1] != "image" {
		t.Errorf("input_modalities 应为 [text image]，得到 %v", arch["input_modalities"])
	}
	if arch["modality"] != "text+image->text" {
		t.Errorf("modality 错误：%v", arch["modality"])
	}

	// 不应存在自造的并行编码（收敛为单一惯例）
	for _, k := range []string{"input_modalities", "capabilities"} {
		if _, has := entry[k]; has {
			t.Errorf("不应输出冗余字段 %q（统一用 architecture）", k)
		}
	}

	// 可 JSON 序列化（下游按 JSON 解析）
	if _, err := json.Marshal(entry); err != nil {
		t.Fatalf("条目无法序列化为 JSON: %v", err)
	}
}

// TestModelEntryTextOnly 不支持图像的模型只声明 text 模态。
func TestModelEntryTextOnly(t *testing.T) {
	mi := provider.ModelInfo{ID: "text-model", ContextWindow: 1000}
	entry := buildModelEntry(provider.WorkBuddy, mi)

	arch, _ := entry["architecture"].(map[string]any)
	mods, ok := arch["input_modalities"].([]string)
	if !ok || len(mods) != 1 || mods[0] != "text" {
		t.Errorf("不支持图像时应为 [text]，得到 %v", arch["input_modalities"])
	}
	if arch["modality"] != "text->text" {
		t.Errorf("modality 应为 text->text，得到 %v", arch["modality"])
	}
	// 容量字段
	if entry["context_length"] != int64(1000) {
		t.Errorf("context_length 应透传，得到 %v", entry["context_length"])
	}
	if _, has := entry["max_output_tokens"]; has {
		t.Error("MaxTokens 为 0 时不应输出 max_output_tokens")
	}
}
