# Qoder 渠道签到补齐改造方案

> 状态：**方案阶段（未改代码）**；§8 已用真实凭据实测验证签到链路可用
> 上游参考：`ref/qoder2api`（CN 分支，签到实现最完整）、`ref/qoderwork2api`（本项目 qoder 渠道的来源仓库）
> 结论先行：本项目 qoder 渠道与 qoderwork2api **同源同协议同 endpoint**；签到功能属**预留但未落地**。
> **实测结论（2026-09-21）**：`dist/auths/` 中的 legacy CN 凭据可直接用于签到，已成功领取 100 Credits。

---

## 1. 现状盘点

| 项 | 现状 | 位置 |
|----|------|------|
| 签到端点常量 | ✅ 已定义但**无引用** | `internal/qoder/constants.go:18-19` |
| `Client.DailyCheckin` | ❌ 直接返回错误 `"qoder 暂无签到活动"` | `internal/qoder/client.go:331` |
| 调度器签到时段 | ❌ `CheckinMinutes: nil`，只做 token keepalive | `cmd/wild-work/main.go:147` |
| 手动签到入口 | ❌ `noExplicitCheckin()` 把 Qoder 屏蔽 | `internal/app/app.go:172` |
| 登录后首次签到 | ❌ 无 | `internal/app/app.go:568` |

已定义但未用的常量：

```go
EpCheckinSt  = "/sash/api/v1/me/daily-check-in/status"
EpCheckinCl  = "/sash/api/v1/me/daily-check-in/claim"
```

---

## 2. 上游签到协议（抓包还原）

### 2.1 认证要求（与推理链路不同！）

签到走**业务 API**（`openapi.qoder.com.cn`），**不需要 COSY 签名**，仅需：

```
Authorization:   Bearer dt-<access_token>
accept:          application/json
content-type:    application/json
accept-language: zh-CN
user-agent:      Qoder
cosy-clienttype: 10          ← 桌面端标识（推理链路用的是 5）
origin:          https://openapi.qoder.com.cn   （仅 POST）
```

> ⚠️ **`cosy-clienttype: 10` 是本改造最容易漏的点**。qoder2api 的 `scripts/auto_checkin.py` 明确注释「关键：桌面端标识（抓包确认 cosy-clienttype=10）」。现有 `internal/qoder/client.go` 的 `billingHeaders()` 只设了 Authorization/Accept/Content-Type，需为签到单独补头。

### 2.2 主路径 vs 兜底路径（**实测已反转优先顺序**）

> ⚠️ **实测修正**：2026-09-21 实测该账号 `daily-check-in/status` 返回 `status:"DISABLED"`，
> 而 `campaigns` 返回可领取的 `act-20260920-549`（100 Credits）。
> 即**当前生效的是 campaigns 路径**，daily-check-in 已成为 history。
> 实现上仍建议两条都写（照 qoder2api 顺序：先 daily-check-in，不通则回退 campaigns），
> 因为上游运营策略会变，两种形态都可能在某时期成为主路径。

#### A. daily-check-in 简化端点（当前 DISABLED，保留兼容）

```
Step 1  GET  /sash/api/v1/me/daily-check-in/status
        → {"status":"CLAIMABLE"|"CLAIMED"|"DISABLED",
           "campaignKey":"cn_daily_check_in_legacy",
           "rewardCredits":100,
           "currentStreakDays":N,"totalClaimDays":N,"totalRewardCredits":N}

Step 2  POST /sash/api/v1/me/daily-check-in/claim   （空 body / "{}"）
        → 200 {"success":true,"rewardCredits":100,"result":"CLAIMED",...}
        → 409 {"errorCode":"AlreadyExists"}   已领取（幂等）
```

**实测**：`status=DISABLED`，`claim` 凭据不足时 401，重复领 409。

#### B. campaigns 活动端点（★ 当前实际生效路径）

```
GET  /sash/api/v1/me/campaigns
     → {"claimable":true,"campaigns":[
          {"campaignId":"01a0bf8b-...","campaignKey":"act-20260920-549",
           "actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE"|"CLAIMED",
           "benefit":{"kind":"CREDITS","amount":100,"validity":{"mode":"RELATIVE_DAYS","days":30}}}]}
POST /sash/api/v1/me/campaigns/{campaignId}/claim   （空 body + origin 头）
     → 200 {"status":"CLAIMED","replayed":bool,"benefit":{"amount":100},"expiresAt":"..."}
```

**实测**：真实领取成功，`replayed=false`，`benefit.amount=100`。

**匹配条件**：`actionType == "CLAIM_BENEFIT" && claimStatus == "CLAIMABLE"`。
活动 `campaignKey` **每日携带日期变化**，不可硬编码。

### 2.4 幂等与状态映射

| 情形 | HTTP | 处理 |
|------|------|------|
| 领取成功 | 200 | `claimed`，OK=true，记录金额 |
| 今日已领 | 409 | `already_claimed`，**OK=true**（对用户是成功） |
| 无活动 | status=DISABLED 且 campaigns 无 CLAIMABLE | `no_campaign`，OK=true（不算失败） |
| token 失效 | 401 | ErrSessionDead → 走既有 refresh 自愈 |
| 端点不存在 | 404/501 | 回退 campaigns |

---

## 3. 改造点清单

### 3.1 `internal/qoder/checkin.go`（**新建**）

新增签到实现，建议结构（对齐 qoder2api）：

```go
// CheckinStatus 签到状态（嵌套在 provider 结果里展示）
type CheckinStatus struct {
    Status             string // CLAIMABLE | CLAIMED | DISABLED
    RewardCredits      int64
    CurrentStreakDays  int64
    TotalClaimDays     int64
    TotalRewardCredits int64
}

// CheckinInfo DailyCheckin 的返回（供日志/UI 展示）
type CheckinInfo struct {
    Claimed bool  // 本次是否新领取
    Already bool  // 今日是否已领
    Amount  int64
    Streak, TotalDays int64
}

// 签到专用头（★ cosy-clienttype: 10 是关键）
func checkinHeaders(req *http.Request, dt string)

// doCheckin(method, path, dt string, body any) (status int, raw []byte, err error)

// status → claim 主路径；404/非 200/DISABLED → (info, handled=false) 回退
func (c *Client) dailyCheckinFlow(a *auth.Auth) (CheckinInfo, bool, error)

// campaigns 兜底
func (c *Client) campaignsCheckinFlow(a *auth.Auth) (CheckinInfo, error)
```

### 3.2 `internal/qoder/client.go`（**修改**）

替换 `DailyCheckin` 桩实现：

```go
// DailyCheckin 每日签到领积分。
// 幂等：今日已领（409）与无活动均视为成功，使调度器不报错重试。
func (c *Client) DailyCheckin(a *auth.Auth) error {
    info, err := c.Checkin(a)   // 主路径+兜底
    if err != nil { return err }
    if info.Already { return errAlreadyCheckin }  // 见 3.5
    return nil
}
```

> 保留一个 `Checkin(a) (CheckinInfo, error)` 公开方法，供 scheduler/UI 拿签到金额与连续天数。

### 3.3 `internal/scheduler/scheduler.go`（**小改**）

`isAlready()` 增补 Qoder 语义（当前匹配「已签到 / already check / code=9095」）：

```go
strings.Contains(s, "already_claimed") || strings.Contains(s, "已领取")
```

`CheckinResult` 结构可选扩展字段（`Amount`/`Streak`），**非必需**——
若只求「能签到」，沿用现有 `OK/Msg` 即可，签到金额可只写日志。

### 3.4 `cmd/wild-work/main.go`（**修改**）

```go
// 改前
qdSch := scheduler.New(scheduler.Config{Pool: qdPool, Upstream: qdUp, Name: "qoder",
    CheckinMinutes: nil, KeepaliveHours: cfg.Schedule.KeepaliveHours})
// 改后：Qoder 支持每日签到，沿用全局签到时段
qdSch := scheduler.New(scheduler.Config{Pool: qdPool, Upstream: qdUp, Name: "qoder",
    CheckinMinutes: checkinMinutes, KeepaliveHours: cfg.Schedule.KeepaliveHours})
```

### 3.5 `internal/app/app.go`（**修改**）

1. `noExplicitCheckin()` 移除 `provider.Qoder`（见 §4 决议点）。可简化为：

```go
func noExplicitCheckin(k provider.Kind) bool {
    return k == provider.WorkBuddyAI || k == provider.QwenWork
}
```

2. `completeQoderLogin()` 末尾追加首次签到（对齐 workbuddy/traework 登录后行为）。

3. 更新 `noExplicitCheckinKinds` 上方注释（去掉「Qoder 无签到活动」表述）。

### 3.6 `internal/qoder/client.go` 错误分类（**可选**）

若新增 `errAlreadyCheckin` 哨兵错误，需确认 `Classify()` 不把它归为 hard/soft 冷却（签到路径不经 `Classify`，仅 `DailyCheckin` 返回给 scheduler，风险低）。

---

## 4. 需要决议的点（请在实施前确认）

| # | 问题 | 建议 |
|---|------|------|
| D1 | **是否为 Qoder 开放手动签到按钮？** | 建议**开放**（签到是用户可感知的领积分行为，与 workbuddy 一致）。若开放，需同步 `internal/server` 的 `/api/account/checkin` 路由（该路由已通用，只要 `noExplicitCheckin` 放行即可） |
| D2 | **签到时段** | 建议沿用全局 `checkinMinutes`（默认 09:00/21:00），与 workbuddy/traework 一致 |
| D3 | **是否实现 campaigns 兜底？** | 建议**实现**。上游 legacy 系统可能 DISABLED，只实现主路径会在活动切换期集体失败 |
| D4 | **是否做本地签到历史补算连续天数？** | 建议**不做**（qoder2api 做是因为它要展示 streak；本项目 UI 无此需求）。仅记录当日是否已领，避免引入 `checkin_history.json` 新状态文件 |
| D5 | **签到金额展示** | 建议仅写日志（`checkin credits ... amount=100`），不改 `CheckinResult` 结构，零 UI 改动 |
| D6 | **KeepaliveHours 是否保留** | 保留。签到与 token 保活是两回事，Qoder token ~30 天，仍需 nightly refresh |

---

## 5. 实施步骤（建议顺序）

1. **新建 `internal/qoder/checkin.go`**：实现 status/claim + campaigns 兜底 + 专用头。
2. **改 `client.go`**：`DailyCheckin` 调 `Checkin()`，新增 `errAlreadyCheckin`。
3. **改 `scheduler.go`**：`isAlready()` 增补匹配词。
4. **改 `main.go`**：Qoder 调度器接 `checkinMinutes`。
5. **改 `app.go`**：`noExplicitCheckin` 放行 Qoder；登录后首次签到；改注释。
6. **单测**（参照 `qoder_test.go`）：用 `httptest` mock 上游，覆盖
   - status=CLAIMABLE → claim 200 → 成功
   - claim 409 → already
   - status=DISABLED → campaigns 有 CLAIMABLE → 成功
   - 401 → 返回 ErrSessionDead 错误（供 scheduler 自愈）
7. `go build ./... && go vet ./... && go test ./...` 全绿。
8. **重建 `dist/wild-work.exe`**（§6 第 0 条硬性约束）。
9. 更新文档：`docs/upstream-reverse-engineering.md` §3 Qoder 补签到端点；
   `AGENTS.md` §5 渠道说明去掉「Qoder 无签到活动」。

---

## 6. 风险与注意事项

1. **`cosy-clienttype` 必须为 10**：用推理链路的 5 会被上游拒（或返回无活动）。
2. **绝对不能走 COSY 签名**：签到是业务 API 直连，套签名会 401。
3. **成功语义要放宽**：409（已领）与 DISABLED+无活动都应视为成功，否则调度器每天报错、UI 显示红叉。
4. **遵循 401 自愈（AGENTS.md 不变式 19）**：`DailyCheckin` 返回 `ErrSessionDead` 时，
   scheduler 现有逻辑会自动 refresh 重试——不要吞成普通错误。
5. **零 token 泄漏（不变式 2）**：签到日志不得打印 dt-/drt-。
6. **改动后必须重建 `dist/wild-work.exe`**（不变式 0），CI 不产出该文件。
7. **端点可能再次变更**：签到活动是**周期性运营活动**，上游随时可能下线或改路径。
   建议 status 非 200 时**宽容回退**而非直接报错，最坏情况退化为「无签到」静默处理。

---

## 7. 三个参考仓库取舍结论

| 仓库 | endpoint | 是否可用作参考 |
|------|----------|----------------|
| **qoder2api** | CN `openapi.qoder.com.cn`（另支持 Global `.sh`） | ✅ **主参考**：签到实现最完整（主路径+兜底+幂等+统计） |
| **qoderwork2api** | CN，与本项目**同源同协议** | ✅ 次参考：签到实现简单版，可对照本项目移植风格 |
| **Orchids-2api** | **仅国际 `.sh`**（`openapi.qoder.sh`/`api2.qoder.sh`） | ❌ 不同区；且**无签到实现**，不可参考签到 |

---

## 8. 实测验证记录（2026-09-21，真实凭据）

### 8.1 测试凭据

`dist/auths/qoder-01a00f81-0000-7000-8000-000000000003.json`（本项目原生格式，legacy 凭据）：

```json
{
  "account": { "nickname": "", "uid": "01a00f81-...", "enterpriseId": "" },
  "auth": {
    "accessToken": "dt-...",            // 27 字符
    "refreshToken": "drt-...",           // 28 字符
    "domain": "qoder.com.cn",
    "expiresAt": 1792504803,
    "machineId": "...", "machineToken": "...", "machineType": "..."
  }
}
```

### 8.2 凭证可用性结论

| 结论 | 证据 |
|------|------|
| ✅ **legacy CN 凭据可直接用于签到** | `dt-` 直连 `openapi.qoder.com.cn/sash/...` 全部 200，成功领取 |
| ✅ **签到不需 COSY 签名** | 仅 `Bearer dt-` + `cosy-clienttype: 10`，无签名头即成功 |
| ✅ **`cosy-clienttype: 10` 是必需的** | 用该头全部 200；qoder2api 脚本注释亦确认 |
| ❌ **CN 凭据不能用于国际 `.sh`** | `.sh` 全部返回 `401 TOKEN_EXPIRE "token is not active"` |
| ⚠️ **`daily-check-in` legacy 系统已 DISABLED** | status 返回 `status:"DISABLED"`，`streak/totalDays` 恒 0 |
| ✅ **真实活动走 campaigns 路径** | `GET /sash/api/v1/me/campaigns` 返回 `act-20260920-549` / `CLAIMABLE` / 100 Credits |

### 8.3 实测各端点返回（CN，带凭据）

```
GET  /sash/api/v1/me/daily-check-in/status   → 200 {"campaignKey":"cn_daily_check_in_legacy","status":"DISABLED",...}
GET  /sash/api/v1/me/campaigns               → 200 {"claimable":true,"campaigns":[{"campaignKey":"act-20260920-549","actionType":"CLAIM_BENEFIT","claimStatus":"CLAIMABLE","benefit":{"kind":"CREDITS","amount":100,"validity":{"mode":"RELATIVE_DAYS","days":30}}}]}
POST /sash/api/v1/me/campaigns/{id}/claim    → 200 {"status":"CLAIMED","replayed":false,"benefit":{"amount":100},"expiresAt":"2026-10-21T..."}   ★ 真实领取成功
POST /sash/api/v1/me/daily-check-in/claim    → 409 AlreadyExists（重放，幂等）
GET  /api/v2/quota/usage                     → 200 addOnQuota.remaining 从 0 → 100，isQuotaExceeded true → false
GET  /api/v1/userinfo                        → 200 name=demo-legacy@aliyun.example source=sso.aliyun
```

> 领取后 `addOnQuota: {total:100, remaining:100}`，主 `userQuota` 仍为 0——**积分落在赠送额度池**，与现有 `UserResourceDetail` 的 `赠送额度` 条目口径一致。

### 8.4 活动元信息（上游下发）

- 活动 key：`act-20260920-549`（**按日滚动**，key 含日期）
- 奖励：100 Credits，`ALL_MODELS` 全模型可用，领取后 **30 天有效**（`RELATIVE_DAYS`）
- 刷新：**每日 10:00 (UTC+8)**
- 官方说明：`https://docs.qoder.cn/events/100credits`
- 活动页 iframe：`https://openapi.qoder.com.cn/growth-page/activity-iframe`
- `campaignKey` 每日变化 → **不可硬编码**，必须每次动态查 campaigns 匹配 `actionType==CLAIM_BENEFIT && claimStatus==CLAIMABLE`

---

## 9. 关于 Qoder 国际版 / 网页版地址

### 9.1 国际版（与 CN 是两套独立部署）

| 用途 | CN（本项目） | 国际 |
|------|------|------|
| 授权登录页 | `https://qoder.com.cn/device/selectAccounts` | `https://qoder.com/device/selectAccounts` |
| 业务 API | `https://openapi.qoder.com.cn` | `https://openapi.qoder.sh` |
| 推理网关 | `https://gateway.qoder.com.cn` | `https://api1.qoder.sh` |
| 模型列表 | `https://gateway.qoder.com.cn/algo/...` | `https://api2.qoder.sh/algo/...` |
| JobToken | `https://gateway.qoder.com.cn/algo/api/v3/user/jobToken` | `https://center.qoder.sh/algo/api/v3/user/jobToken` |
| OAuth client_id | `1c5e33e1-364d-4ce6-b02c-acaa81274a5c` | `e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb` |
| 签到 | 支持（`/sash/...`） | 端点存在（401），活动未知 |

**凭据不通用**（实测 CN dt- 在 `.sh` 报 `TOKEN_EXPIRE`）。国际版要单独 OAuth 登录（不同 client_id）。

### 9.2 网页版/控制台地址

| 地址 | 说明 | 实测 |
|------|------|------|
| `https://qoder.com.cn/` | CN 官网/控制台 | 200 |
| `https://www.qoder.com.cn/` | 同上（308 跳转） | 308 |
| `https://qoder.com/` | 国际站 | 200 |
| `https://openapi.qoder.com.cn/growth-page/activity-iframe` | 签到活动页 iframe | 上游下发 |
| `https://docs.qoder.cn/events/100credits` | 签到活动官方说明 | 上游下发 |
| qoder2api 控制台 | `http://127.0.0.1:3588`（CN）/ `3589`（Global） | 其自身 Web UI |

### 9.3 本项目是否需要支持国际版

**当前不需要**。本项目 qoder 渠道定位 CN（`domain: qoder.com.cn`）。若将来要支持国际版，需独立加渠道（类比 workbuddy / workbuddyai 双渠道模式），工作量与新增渠道相当，**不在本次签到改造范围内**。

### 9.4 Web 凭据实测（`qoder.com/agents`，2026-09-21）

> 用户提供了一份从 `https://qoder.com/agents/session/new` 抓包的 **国际站 Web 会话 cookie**。
> 结论：**与本项目渠道无关，不能用于签到/推理，但揭示了一个独立的 Remote Agents 子系统**。

**关键字段**：

```
qoder_session_cookie=<base64 blob>      ← 核心会话凭据（cookie，非 dt- token）
qoderuid=01a0c1c6-0000-7000-8000-000000000004   ← 与 CN 凭据的 uid 不同（另一账号）
qoder_visitor_id=468cbb3c-...           ← 匿名访客 ID
```

**实测结果**：

| 测试 | 结果 | 说明 |
|------|------|------|
| `GET qoder.com/api/v1/remote/sessions` | ✅ 200 | cookie 认证通过，返回会话列表 |
| `GET qoder.com/api/v1/remote/environments` | ✅ 200 | 返回 cloud 环境 + 模型表（auto=0.5x / ultimate=2x / performance…） |
| `GET qoder.com/api/v1/remote/employees` | ✅ 200（空） | |
| `POST qoder.com/api/v1/remote/sessions` | 400 `MissingSessionTarget` | **认证通过**，仅缺参数 |
| `GET qoder.com/api/v1/userinfo` | ❌ 404 | Web 站无此路径 |
| `GET qoder.com/api/v2/quota/usage` | ❌ 404 | 同上 |
| `GET openapi.qoder.sh/api/v1/userinfo`（带 cookie） | ❌ 401 `TOKEN_INVALID` | **cookie 不能跨子系统** |
| `GET openapi.qoder.sh/sash/api/v1/me/campaigns`（带 cookie） | ❌ 401 | **不能用于签到** |

**结论**：

1. **Web cookie 与 dt- device token 是两套完全独立的凭据体系**：
   - cookie → `qoder.com/api/v1/remote/*`（Remote Agents 会话管理）
   - dt- → `openapi.qoder.sh` / `openapi.qoder.com.cn`（业务 API + 推理 + 签到）
   - 用 cookie 冒充 Bearer 会被拒（`invalid token`）
2. **不能用于本项目**：本项目走 dt- + COSY 签名的推理通道，Web cookie 既不能签到也不能推理。
3. **CSRF 要求**：`qoder.com` 的写操作需 CSRF 令牌（`POST /api/v1/deviceToken` 等返回 `CSRFInvalid`），无 cookie jar + CSRF 无法自动化。
4. `deviceToken/*` 路径在 web 站返回 **CSRFInvalid 而非 404**，推测存在 **web → device token 互换**的可能（即用浏览器登录态换取 dt-），但目前被 CSRF 阻断，**未验证成功**。若将来要支持「用网页登录态自动获取 dt-」，这是需逆向的方向，但优先级低。
5. 该账号 uid（`01a0c1c6-...`）与 CN 测试凭据（`01a00f81-...`）**是不同账号**，说明用户同时持有两个站的账号。
