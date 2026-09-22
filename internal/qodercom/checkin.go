// checkin.go QoderCOM 每日签到：仅 campaigns 活动路径。
// 代码级复制自 internal/qodercn/checkin.go 后裁剪：
// COM 区无 legacy daily-check-in 系统（实测 GET .../daily-check-in/status 404），
// 故只有 campaigns 链路，无回退分支。
// 参考证据：misc 抓包 sid=3（IDE 国际版同端点，cosy-clienttype=10 + 机器头）。
// 认证：仅需 Bearer dt- + cosy-clienttype:10（桌面端标识），无需 COSY 签名。
// 幂等语义：已签到（409 / CLAIMED / replayed）与无活动均视为成功（返回 nil）。
package qodercom

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"wild-work/internal/auth"
)

// checkinHost 签到 API 域名（与业务 API 同域 openapi.qoder.sh）。
// var 而非 const：测试用 httptest 覆盖。
var checkinHost = OpenAPIBase

// checkinHeaders 构造桌面端签到请求头（与 qodercn 同款；抓包确认必需头）。
// ★ cosy-clienttype 必须为 10（桌面端标识），推理链路用的是 5，两者不可混用。
func checkinHeaders(req *http.Request, dt string) {
	req.Header.Set("authorization", "Bearer "+dt)
	req.Header.Set("accept", "application/json")
	req.Header.Set("accept-language", "zh-CN")
	req.Header.Set("user-agent", "Qoder")
	req.Header.Set("cosy-clienttype", "10")
}

// doCheckinRequest 发送签到请求，返回 (httpStatus, rawBody)。
// reqBody 为 nil 时发送空 body（抓包确认 campaigns/claim 即空 body）。
func doCheckinRequest(method, path, dt string, reqBody any) (int, []byte, error) {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, nil, err
		}
		body = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, checkinHost+path, body)
	if err != nil {
		return 0, nil, err
	}
	checkinHeaders(req, dt)
	if method == http.MethodPost {
		req.Header.Set("origin", checkinHost)
		if reqBody == nil {
			req.ContentLength = 0
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, raw, nil
}

// checkinResult 单次签到结果。
type checkinResult struct {
	OK      bool   // 视为成功（含已签到）
	Msg     string // 展示文案（含金额）
	Claimed bool   // 本次是否新领取（false=已签/无活动）
	Amount  int64
}

// checkin 执行签到：campaigns 活动路径（COM 唯一路径）。
// 调用方（scheduler）已处理 401 自愈（ErrSessionDead），本函数直接透传。
func checkin(a *auth.Auth) error {
	dt := a.JWT()
	if dt == "" {
		return fmt.Errorf("no dt- available")
	}
	res, handled, err := tryCampaigns(dt)
	if handled {
		log.Printf("qodercom checkin campaigns uid=%s ok=%t claimed=%t amount=%d msg=%s", a.UID, res.OK, res.Claimed, res.Amount, res.Msg)
		if res.OK {
			return nil
		}
		if err != nil {
			return err // 401 等需向 scheduler 透传（自愈重试）
		}
		return fmt.Errorf("%s", res.Msg)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("qodercom checkin: campaigns unavailable")
}

// tryCampaigns 查活动列表 → 领取 CLAIMABLE 的 CLAIM_BENEFIT。
// 返回 (结果, handled, err)；handled=false 表示路径不可用（网络/格式异常）。
func tryCampaigns(dt string) (checkinResult, bool, error) {
	res := checkinResult{}
	status, raw, err := doCheckinRequest(http.MethodGet, EpCampaigns, dt, nil)
	if err != nil {
		return res, false, err
	}
	// 401 透传给上层自愈（session dead）
	if status == http.StatusUnauthorized {
		return res, true, checkinResult{Msg: fmt.Sprintf("http 401: %s", truncate(string(raw), 120))}.toErr()
	}
	if status != http.StatusOK {
		return res, false, fmt.Errorf("campaigns http %d: %s", status, truncate(string(raw), 200))
	}
	var list struct {
		Campaigns []struct {
			CampaignID  string `json:"campaignId"`
			CampaignKey string `json:"campaignKey"`
			ActionType  string `json:"actionType"`
			ClaimStatus string `json:"claimStatus"`
		} `json:"campaigns"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return res, false, fmt.Errorf("campaigns parse: %w", err)
	}
	var targetID string
	already := false
	for _, cp := range list.Campaigns {
		if cp.ActionType != "CLAIM_BENEFIT" {
			continue
		}
		switch cp.ClaimStatus {
		case "CLAIMABLE":
			targetID = cp.CampaignID
		case "CLAIMED":
			already = true
		}
	}
	if targetID == "" {
		if already {
			return checkinResult{OK: true, Msg: "已签到"}, true, nil
		}
		return checkinResult{OK: true, Msg: "无可用签到活动"}, true, nil
	}

	// 领取（空 body，抓包确认）
	status, raw, err = doCheckinRequest(http.MethodPost, EpCampaigns+"/"+targetID+"/claim", dt, nil)
	if err != nil {
		return res, true, err
	}
	if status == http.StatusConflict { // 409 幂等：今日已领
		return checkinResult{OK: true, Msg: "已签到"}, true, nil
	}
	if status == http.StatusUnauthorized {
		return res, true, checkinResult{Msg: fmt.Sprintf("http 401: %s", truncate(string(raw), 120))}.toErr()
	}
	if status != http.StatusOK {
		return checkinResult{OK: false, Msg: fmt.Sprintf("领取失败 http %d: %s", status, truncate(string(raw), 150))}, true, nil
	}
	var claim struct {
		Status   string `json:"status"`
		Replayed bool   `json:"replayed"`
		Benefit  *struct {
			Amount int64 `json:"amount"`
		} `json:"benefit"`
	}
	if err := json.Unmarshal(raw, &claim); err != nil {
		return res, true, fmt.Errorf("claim parse: %w", err)
	}
	if claim.Status == "CLAIMED" {
		if claim.Replayed {
			return checkinResult{OK: true, Msg: "已签到"}, true, nil
		}
		amount := int64(0)
		if claim.Benefit != nil {
			amount = claim.Benefit.Amount
		}
		return checkinResult{OK: true, Claimed: true, Amount: amount, Msg: fmt.Sprintf("签到成功 +%d", amount)}, true, nil
	}
	return checkinResult{OK: false, Msg: fmt.Sprintf("未知状态 %s", claim.Status)}, true, nil
}

// toErr 把失败结果转为 error（保持 scheduler isAlready 兼容文案）。
func (r checkinResult) toErr() error { return fmt.Errorf("%s", r.Msg) }
