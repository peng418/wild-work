// report.go 对话活跃上报接口：POST {billingBase}/v2/report。
// 一条 chat_request_send 事件同时点亮 growth 连登 + 解锁领养前置任务。
// 不需真实对话，纯事件上报，风控口径每号每天 1 次。
package upstream

import (
	"bytes"
	"encoding/json"
	"net/http"
	"time"

	"wild-work/internal/auth"
)

const reportPath = "/v2/report"

// chatRequestEvent 与官方桌面端 chat_request_send 事件同构。
type chatRequestEvent struct {
	EventCode             string `json:"eventCode"`
	Timestamp             int64  `json:"timestamp"`
	Mode                  string `json:"mode"`
	ConversationID        string `json:"conversationId"`
	RequestID             string `json:"requestId"`
	InputLength           int    `json:"inputLength"`
	RequestModelID        string `json:"requestModelId"`
	RequestModelName      string `json:"requestModelName"`
	IsPlan                bool   `json:"isPlan"`
	IsAutoExecuteTerminal bool   `json:"isAutoExecuteTerminal"`
	IsAutoModify          bool   `json:"isAutoModify"`
	CodebaseEnable        bool   `json:"codebaseEnable"`
	MaxToken              int    `json:"maxToken"`
	Temperature           int    `json:"temperature"`
	MentionContexts       []any  `json:"mentionContexts"`
	KnowledgeID           []any  `json:"knowledgeId"`
	KnowledgeName         []any  `json:"knowledgeName"`
	Command               string `json:"command"`
	PresentAt             int64  `json:"presentAt"`
	RootRequestID         string `json:"rootRequestId"`
	AgentName             string `json:"agentName"`
	AgentType             string `json:"agentType"`
	UserID                string `json:"userId"`
}

// doJSONBilling 发 billing 域请求的通用方法（公之于包内）。
// billingJSON 用 billingBase + BillingHeaders 发送。
func (c *Client) billingReq(a *auth.Auth, method, path string, body json.RawMessage) (json.RawMessage, error) {
	for name, host := range map[string]string{"billingBaseCN": c.BillingBaseCN, "billingBaseGlobal": c.BillingBaseGlob} {
		_ = name
		_ = host
	}
	return c.billingJSON(a, method, path, body)
}

// billingJSON 发 billing 域请求并解信封；body 为 nil 时无请求体。
func (c *Client) billingJSON(a *auth.Auth, method, path string, body json.RawMessage) (json.RawMessage, error) {
	url := c.billingBase(a) + path
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		return nil, err
	}
	if len(body) > 0 {
		var rdr = bytes.NewReader(body)
		req, err = http.NewRequest(method, url, rdr)
		if err != nil {
			return nil, err
		}
	}
	BillingHeaders(req, a)
	return c.doJSONBilling(req)
}

// ReportChatActivity 向上游发送一条对话活跃上报（chat_request_send）。
// conversationID 由调用方生成（如 "wb2a-<uid8>-<ms>"），无需真实会话。
func (c *Client) ReportChatActivity(a *auth.Auth, conversationID, requestID string) error {
	if requestID == "" {
		requestID = conversationID
	}
	now := time.Now().UnixMilli()
	ev := chatRequestEvent{
		EventCode:       "chat_request_send",
		Timestamp:       now,
		Mode:            "craft",
		ConversationID:  conversationID,
		RequestID:       requestID,
		InputLength:     12,
		RequestModelID:  "deepseek-v4-flash",
		RequestModelName:"DeepSeek V4 Flash",
		MentionContexts: []any{},
		KnowledgeID:     []any{},
		KnowledgeName:   []any{},
		Command:         "",
		PresentAt:       now,
		RootRequestID:   conversationID,
		AgentName:       "default",
		AgentType:       "conversation",
		UserID:          a.UID,
	}
	raw, _ := json.Marshal([]chatRequestEvent{ev})
	req, err := http.NewRequest(http.MethodPost, c.billingBase(a)+reportPath, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	BillingHeaders(req, a)
	_, err = c.doJSONBilling(req)
	return err
}