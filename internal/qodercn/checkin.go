// checkin.go QoderCN 每日签到：双路径实现（参考 qoder2api checkin.go）。
//   - 路径 A：GET /sash/api/v1/me/daily-check-in/status → POST .../claim
//   - 路径 B：GET /sash/api/v1/me/campaigns → POST /sash/api/v1/me/campaigns/{id}/claim
//
// 2026-09-21 实测：路径 A 的 legacy 系统已全局 DISABLED（status=DISABLED，streak 恒 0），
// 当前实际生效的是路径 B（活动 key 形如 act-20260920-549，每日变化，不可硬编码）。
// 故实现顺序为 B 优先、A 兜底（与 qoder2api 相反，按实测为准）。
// 签到认证：仅需 Bearer dt- + cosy-clienttype:10（桌面端标识），无需 COSY 签名。
// 幂等语义：已签到（409 / CLAIMED / replayed）与无活动均视为成功（返回 nil）。
package qodercn

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

// checkinHost 签到 API 域名（抓包确认 openapi.qoder.com.cn，与业务 API 同域）。
// var 而非 const：测试用 httptest 覆盖。
var checkinHost = OpenAPIBase

// checkinHeaders 构造桌面端签到请求头（qoder2api 抓包确认的必需头）。
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

// checkin 执行签到：campaigns 主路径 → daily-check-in 兜底。
// 调用方（scheduler）已处理 401 自愈（ErrSessionDead），本函数直接透传。
func checkin(a *auth.Auth) error {
	dt := a.JWT()
	if dt == "" {
		return fmt.Errorf("no dt- available")
	}
	// 路径 B：campaigns（实测主路径）
	res, handled, err := tryCampaigns(dt)
	if handled {
		log.Printf("qodercn checkin campaigns uid=%s ok=%t claimed=%t amount=%d msg=%s", a.UID, res.OK, res.Claimed, res.Amount, res.Msg)
		if res.OK {
			return nil
		}
		if err != nil {
			return err // 401 等需向 scheduler 透传（自愈重试）
		}
		return fmt.Errorf("%s", res.Msg)
	}
	// 路径 A：daily-check-in（legacy DISABLED 时兜底）
	if err != nil {
		log.Printf("qodercn checkin campaigns error uid=%s err=%v, fallback daily-check-in", a.UID, err)
	}
	res, handled, err = tryDailyCheckin(dt)
	if handled {
		log.Printf("qodercn checkin daily uid=%s ok=%t claimed=%t amount=%d msg=%s", a.UID, res.OK, res.Claimed, res.Amount, res.Msg)
		if res.OK {
			return nil
		}
		return fmt.Errorf("%s", res.Msg)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("qodercn checkin: both paths unavailable")
}

// tryCampaigns 路径 B：查活动列表 → 领取 CLAIMABLE 的 CLAIM_BENEFIT。
// 返回 (结果, handled, err)；handled=false 表示路径不可用（网络/格式异常），可回退。
func tryCampaigns(dt string) (checkinResult, bool, error) {
	res := checkinResult{}
	status, raw, err := doCheckinRequest(http.MethodGet, EpCampaigns, dt, nil)
	if err != nil {
		return res, false, err
	}
	// 401 透传给上层自愈（session dead）；其余非 200 回退另一路径
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

// tryDailyCheckin 路径 A：daily-check-in 简化端点（legacy，实测 DISABLED）。
// 返回 (结果, handled, err)；handled=false 表示端点不可用，可回退。
func tryDailyCheckin(dt string) (checkinResult, bool, error) {
	res := checkinResult{}
	status, raw, err := doCheckinRequest(http.MethodGet, EpCheckinSt, dt, nil)
	if err != nil {
		return res, false, err
	}
	// 404/501 = 端点不存在 → 回退
	if status == http.StatusNotFound || status == http.StatusNotImplemented {
		return res, false, nil
	}
	if status == http.StatusUnauthorized {
		return res, true, checkinResult{Msg: fmt.Sprintf("http 401: %s", truncate(string(raw), 120))}.toErr()
	}
	if status != http.StatusOK {
		return res, false, fmt.Errorf("checkin status http %d: %s", status, truncate(string(raw), 200))
	}
	var st struct {
		Status        string `json:"status"` // CLAIMABLE | CLAIMED | DISABLED
		RewardCredits int64  `json:"rewardCredits"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || st.Status == "" {
		return res, false, fmt.Errorf("checkin status parse: %w", err)
	}
	if st.Status == "DISABLED" {
		// legacy 全局停用：按无活动成功处理（对用户透明，不再走 claim）
		return checkinResult{OK: true, Msg: "无可用签到活动"}, true, nil
	}

	// 幂等领取：status=CLAIMED 也直接 claim，靠 409 判定已领
	status, raw, err = doCheckinRequest(http.MethodPost, EpCheckinCl, dt, map[string]any{})
	if err != nil {
		return res, true, err
	}
	if status == http.StatusConflict {
		return checkinResult{OK: true, Msg: "已签到"}, true, nil
	}
	if status == http.StatusUnauthorized {
		return res, true, checkinResult{Msg: fmt.Sprintf("http 401: %s", truncate(string(raw), 120))}.toErr()
	}
	if status == http.StatusOK {
		var claim struct {
			Success       bool  `json:"success"`
			RewardCredits int64 `json:"rewardCredits"`
		}
		_ = json.Unmarshal(raw, &claim)
		amount := claim.RewardCredits
		if amount == 0 {
			amount = st.RewardCredits
		}
		return checkinResult{OK: true, Claimed: true, Amount: amount, Msg: fmt.Sprintf("签到成功 +%d", amount)}, true, nil
	}
	return checkinResult{OK: false, Msg: fmt.Sprintf("领取失败 http %d: %s", status, truncate(string(raw), 150))}, true, nil
}

// toErr 把失败结果转为 error（保持 scheduler isAlready 兼容文案）。
func (r checkinResult) toErr() error { return fmt.Errorf("%s", r.Msg) }
