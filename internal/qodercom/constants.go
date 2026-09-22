// Package qodercom 封装 QoderCOM（国际版，qoder.com / qoder.sh）上游协议：
// COSY 签名、QoderEncoding 编码、嵌套 SSE 解析，并实现 provider.Upstream 接口。
// 独立渠道：代码级复制自 internal/qodercn 后改域名表（qoder2api 双区同码已验证协议同源）。
// 与 QoderCN 差异：业务 API openapi.qoder.sh、推理 api1.qoder.sh、模型表 api2.qoder.sh；
// 无 legacy daily-check-in 签到系统（实测 404），仅 campaigns 活动路径。
package qodercom

import (
	"wild-work/internal/provider"
)

// 上游域名与端点（COM only）。
const (
	OpenAPIBase = "https://openapi.qoder.sh"  // 业务 API（dt- Bearer，无签名）
	GatewayBase = "https://api1.qoder.sh"     // 推理网关（COSY 签名）
	ModelsBase  = "https://api2.qoder.sh"     // 模型列表（COSY 签名）

	EpQuotaUsage = "/api/v2/quota/usage"
	EpCampaigns  = "/sash/api/v1/me/campaigns" // 活动列表+领取（COM 唯一签到路径；无 daily-check-in）
	EpPlan       = "/api/v2/user/plan"
	EpUserInfo   = "/api/v1/userinfo"
	EpDTRefresh  = "/api/v1/deviceToken/refresh"
	EpModels     = "/algo/api/v2/model/list?Encode=1"
	EpChat       = "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"

	clientUA = "Go-http-client/2.0"
)

// StaticModels 返回空的静态兜底（QoderCOM 无静态表，与 qodercn 一致）。
func StaticModels() []provider.ModelInfo {
	return nil
}
