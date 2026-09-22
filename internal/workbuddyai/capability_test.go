// capability_test.go 能力字段（图像/推理/工具）的透传逻辑测试。
//
// 原则：只透传上游模型列表接口真实返回的能力字段，不自造数据。
//   - 上游返回 supportsImages=true 且未 disabledMultimodal → 声明图像
//   - 上游未返回该字段 → 不声明（回退 [text]）
//   - 上游声明与实测行为不符（如 glm-5.2 声明 true 却拒图）→ 仍按上游声明透传，
//     不在本项目内做纠正（上游的不一致由上游负责）
package workbuddyai

import (
	"encoding/json"
	"testing"
)

// TestImageOKRespectsUpstreamDeclaration 上游声明 supportsImages 时采纳。
func TestImageOKRespectsUpstreamDeclaration(t *testing.T) {
	if !(catalogModel{ID: "m", SupportsImages: true}).imageOK() {
		t.Fatal("上游声明 supportsImages=true 时应判定支持图像")
	}
}

// TestImageOKRespectsDisabledMultimodal 上游 disabledMultimodal=true 时否决图像。
// 这是上游自己的字段，属透传而非自造。
func TestImageOKRespectsDisabledMultimodal(t *testing.T) {
	m := catalogModel{ID: "m", SupportsImages: true, DisabledMultimodal: true}
	if m.imageOK() {
		t.Fatal("disabledMultimodal=true 时必须否决图像能力")
	}
}

// TestImageOKDefaultsFalse 字段缺失时保守判定为不支持（回退 [text]）。
func TestImageOKDefaultsFalse(t *testing.T) {
	if (catalogModel{ID: "unknown"}).imageOK() {
		t.Fatal("能力字段缺失时不应声明支持图像")
	}
}

// TestCatalogModelParsesCapabilityFields 目录 JSON 的能力字段应被正确解析。
func TestCatalogModelParsesCapabilityFields(t *testing.T) {
	raw := `{"id":"m1","name":"M1","supportsImages":true,"supportsReasoning":true,
	         "supportsToolCall":true,"disabledMultimodal":false}`
	var m catalogModel
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !m.imageOK() || !m.SupportsReasoning || !m.SupportsToolCall {
		t.Fatalf("能力字段解析错误: %+v", m)
	}
}

// TestHardcodedTablesDeclareNoCapabilities 硬编码表（目录接口不返回的兜底表）
// 无上游能力数据来源，必须不声明任何能力，避免自造数据误导下游。
func TestHardcodedTablesDeclareNoCapabilities(t *testing.T) {
	for _, m := range extraModels {
		if m.SupportsImages || m.SupportsReasoning || m.SupportsTools {
			t.Errorf("extraModels 中 %s 声明了能力，但该表无上游数据来源", m.ID)
		}
	}
	for _, m := range StaticModels() {
		if m.SupportsImages || m.SupportsReasoning || m.SupportsTools {
			t.Errorf("staticModels 中 %s 声明了能力，但该表无上游数据来源", m.ID)
		}
	}
}
