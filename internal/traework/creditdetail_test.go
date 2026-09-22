package traework

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"wild-work/internal/auth"
	"wild-work/internal/provider"
)

// entUsageBody 构造一份 web_user_ent_usage 响应：
//   - ep=0 每月登录（可用，500 限额已用 40）
//   - ep=0 每日签到（可用，200 限额未用）
//   - ep=1 每日签到（不可用：官方客户端专用池，本工具扣不到）
//   - ep=0 限额 0 的免费包（应被明细过滤掉）
//
// expire_time 用固定值便于断言日期格式化。
const entUsageBody = `{"is_credits_billing":true,"user_entitlement_pack_list":[
 {"display_desc":"每月登录赠送","group_name":"每月登录积分","group_type":3,"expire_time":1790783999,
  "entitlement_base_info":{"available_endpoint":0,"quota":{"credits_limit":500}},
  "usage":{"credits_amount":40}},
 {"display_desc":"签到奖励","group_name":"每日签到","group_type":1,"expire_time":1789779374,
  "entitlement_base_info":{"available_endpoint":0,"quota":{"credits_limit":200}},
  "usage":{}},
 {"display_desc":"签到奖励","group_name":"每日签到","group_type":1,"expire_time":1789779374,
  "entitlement_base_info":{"available_endpoint":1,"quota":{"credits_limit":300}},
  "usage":{}},
 {"display_desc":"免费","group_type":0,"expire_time":1790783999,
  "entitlement_base_info":{"available_endpoint":0,"quota":{"credits_limit":0}},
  "usage":{}}
]}`

func entUsageClient(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != EpEntUsage {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.HTTP = srv.Client()
	c.UgHost = srv.URL
	return c
}

// TestUserResourceExcludesOfficialClientPool 可消耗余额只算 available_endpoint==0，
// ep=1（官方客户端专用池）必须排除——否则 pool 会按虚高余额选号。
func TestUserResourceExcludesOfficialClientPool(t *testing.T) {
	c := entUsageClient(t, entUsageBody)
	remain, err := c.UserResource(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatalf("UserResource: %v", err)
	}
	// 460 (每月登录) + 200 (ep=0 签到) = 660；不含 ep=1 的 300。
	if remain != 660 {
		t.Errorf("remain=%d want 660 (ep=1 不应计入)", remain)
	}
}

// TestUserResourceDetailMarksUsableAndExpiry 明细须带可用性标记与到期日。
func TestUserResourceDetailMarksUsableAndExpiry(t *testing.T) {
	c := entUsageClient(t, entUsageBody)
	remain, items, err := c.UserResourceDetail(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if remain != 660 {
		t.Errorf("remain=%d want 660", remain)
	}
	// 限额 0 的免费包应被过滤，剩 3 条
	if len(items) != 3 {
		t.Fatalf("items=%d want 3（限额 0 的包应被过滤）: %+v", len(items), items)
	}

	usable, unusable := provider.Summarize(items)
	if usable != 660 || unusable != 300 {
		t.Errorf("usable=%d unusable=%d, want 660/300", usable, unusable)
	}

	byName := map[string][]item{}
	for _, it := range items {
		byName[it.Name] = append(byName[it.Name], item{it.Remain, it.ExpireAt, it.Usable})
	}
	month := byName["每月登录积分"]
	if len(month) != 1 || month[0].remain != 460 || !month[0].usable {
		t.Errorf("每月登录积分应为 460 且可用: %+v", month)
	}
	if month[0].expire != "2026-09-30" {
		t.Errorf("每月登录积分到期日=%q want 2026-09-30", month[0].expire)
	}
	// 同名条目按 ep 区分可用性：ep=0 可用 / ep=1 不可用
	checkins := byName["每日签到"]
	if len(checkins) != 2 {
		t.Fatalf("每日签到应有 2 条: %+v", checkins)
	}
	var avail, unavail bool
	for _, ci := range checkins {
		if ci.usable && ci.remain == 200 {
			avail = true
		}
		if !ci.usable && ci.remain == 300 {
			unavail = true
		}
	}
	if !avail || !unavail {
		t.Errorf("每日签到应区分可用(200)/不可用(300): %+v", checkins)
	}
}

// item 测试内使用的明细投影。
type item struct {
	remain int64
	expire string
	usable bool
}

// TestUserResourceDetailNoExpiryWhenUpstreamOmits 上游未下发 expire_time 时，
// ExpireAt 必须为空串——前端据此隐藏有效期列，不得用零值冒充「永不过期」。
func TestUserResourceDetailNoExpiryWhenUpstreamOmits(t *testing.T) {
	c := entUsageClient(t, `{"user_entitlement_pack_list":[
	 {"group_name":"无到期包","group_type":4,
	  "entitlement_base_info":{"available_endpoint":0,"quota":{"credits_limit":100}},
	  "usage":{"credits_amount":10}}
	]}`)
	_, items, err := c.UserResourceDetail(&auth.Auth{AccessToken: "at"})
	if err != nil {
		t.Fatalf("UserResourceDetail: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items=%d want 1", len(items))
	}
	if items[0].ExpireAt != "" {
		t.Errorf("ExpireAt=%q want 空串（上游未下发）", items[0].ExpireAt)
	}
	if items[0].Remain != 90 {
		t.Errorf("Remain=%d want 90", items[0].Remain)
	}
}

// TestPackRemainClampsNegative 上游脏数据（used > limit）剩余应钳 0，不得为负。
func TestPackRemainClampsNegative(t *testing.T) {
	p := entPackage{}
	p.EntitlementBaseInfo.Quota.CreditsLimit = 100
	p.Usage.CreditsAmount = 150
	if got := packRemain(p); got != 0 {
		t.Errorf("packRemain=%d want 0 (钳负)", got)
	}
}
