# QoderCN IDE 抓包分析备忘（qodercn_ide_auth_20260921.saz）

> 抓包时间：2026-09-21 16:09-16:20（677 会话）
> 对象：QoderCN IDE 0.3.4（Electron 43 / Chrome 150）登录授权 + 网页活动页
> 说明：IDE 推理/业务流量（gateway.qoder.com.cn COSY 签名请求）**不走系统代理**，未被抓到；
> 本抓包覆盖：OAuth 设备流登录全过程 + 登录后的业务 API + 网页端活动页（campaigns iframe）。

---

## 1. ★ 已提取并实测可用的用户凭据

来源：sid=591，`GET https://openapi.qoder.com.cn/api/v1/deviceToken/poll` 的 200 响应（设备流成功帧）。

| 字段 | 值 |
|---|---|
| access token | `dt-DEMO0000000000000000000` |
| refresh token | `drt-DEMO0000000000000000000` |
| uid | `01a0c304-0000-7000-8000-000000000001` |
| 账号 | demo-qodercn@aliyun.example（SSO aliyun 登录，`source=sso.aliyun`） |
| user_type | `personal_professional_trial`（Pro Trial，来自 /api/v2/user/plan） |
| dt 有效期 | 2026-10-21（30 天，`expires_in=2591999999` ms） |
| drt 有效期 | 2027-09-16（约 1 年，`refresh_token_expires_in=31103999999` ms） |

### 实测验证（2026-09-21，全部通过）

| 端点 | 结果 |
|---|---|
| `GET /api/v1/userinfo` | ✅ 200，demo-qodercn@aliyun.example |
| `GET /api/v2/user/plan` | ✅ 200，Pro Trial（300 额度窗口 2026-09-21 → 10-21） |
| `GET /api/v2/quota/usage` | ✅ 200，**293/300 credits**（userQuota，已用 7） |
| `GET /sash/api/v1/me/campaigns` | ✅ 200，`claimable=false`（今日已被 IDE 领过） |
| `GET /sash/api/v1/me/daily-check-in/status` | ✅ 200，`DISABLED`（legacy 停用，与 qoder2api 结论一致） |
| `POST .../campaigns/01a05bbf-.../claim` 重放 | ✅ 200 `replayed=true`（幂等语义与本项目实现一致） |
| `POST /api/v1/deviceToken/refresh`（无效 drt） | 401 `Unauthorized`（错误形态确认，自愈判定可用） |

**结论：IDE 设备流签发的 dt-/drt- 与 qoder2api / 本项目渠道完全兼容，可直接入库使用。**

---

## 2. ★ 关键发现：IDE 的 OAuth 参数与 qoder2api / qoderwork 都不同

三个已知 OAuth client_id 并存（同站多客户端）：

| 来源 | client_id | 授权页域名 | redirect |
|---|---|---|---|
| qoderwork2api（旧 qoder 渠道） | `1c5e33e1-364d-4ce6-b02c-acaa81274a5c` | `qoder.com.cn` | `qoder-work-cn://` |
| qoder2api（新 qodercn 渠道当前采用） | `e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb` | `qoder.com.cn` | 无（纯轮询） |
| **QoderCN IDE 0.3.4（本抓包）** | **`732aef47-9cf2-46a2-95fe-4cebb5d0d1fa`** | **`qoder.cn`**（短域） | 无（`directLogin=true`） |

IDE 授权 URL 实录（sid=515）：

```
https://qoder.cn/device/selectAccounts?action=signup
  &challenge=B2qkvIbpJ7KaM7tyWVZgz0hmB4KJgdRwNvmIJeFIHtQ
  &challenge_method=S256
  &client_id=732aef47-9cf2-46a2-95fe-4cebb5d0d1fa
  &machine_id=af256f52-0000-4000-8000-000000000000
  &nonce=24fbb9b3-0000-4000-8000-000000000000
  &provider=aliyun_account_sso
```

要点：
- 授权页用 **`qoder.cn`**（不是 qoder.com.cn）；登录方式 `provider=aliyun_account_sso`（阿里云 SSO）
- **`machine_id` 随授权 URL 传入**（IDE 的机器指纹在授权时即上报）
- 轮询端点相同：`openapi.qoder.com.cn/api/v1/deviceToken/poll?nonce=&verifier=&challenge_method=S256`
- 404=未授权继续轮询；200 返回 token/refresh_token/user_id（响应字段与本项目 `login_qodercn.Poll` 解析一致）

> 影响：本项目 qodercn 渠道当前用 qoder2api 的 `e883ade2` client_id，签发的凭据同样有效（§1 实测即用 IDE 凭据，但三种 client_id 签发的 dt 在业务 API 侧无差别）。若想完全模拟 IDE，可把 `login_qodercn` 的 client_id/授权页换成 `732aef47` + `qoder.cn`（另行决议）。

---

## 3. IDE 请求头形态（登录后业务 API）

### 3.1 简单查询（userinfo / plan / jobToken）

```
authorization: Bearer dt-...
accept: application/json
user-agent: Mozilla/5.0 ... QoderCN/0.3.4 Chrome/150.0.7871.114 Electron/43.1.1 Safari/537.36
```

仅需 Bearer，无 COSY 签名、无 cosy-clienttype —— 与本项目 `billingHeaders` 一致。

### 3.2 campaigns / claim（网页活动 iframe 内）

```
authorization: Bearer dt-...
Cosy-ClientType: 10
Cosy-Version: 0.3.4
Cosy-MachineOS: x86_64_win32
Cosy-MachineHostname: DESKTOP-FLHS79P
Cosy-MachineId: af256f52-0000-4000-8000-000000000000
Cosy-MachineToken: P1gAz9WE6uzNc1rEtJFtAUm3MgG8Tm88WVa6aPL-8Ppq3j4e5rAl4FuDnKVnBxSEkqAEhLyrDr8nJ1edjpsmbv4o
Cosy-MachineCode: cb4ad5bc763e34b0c2
Cosy-MachineType: 1dcb54ea14324b4f6f
origin/referer: https://openapi.qoder.com.cn/growth-page/activity-iframe
```

- **`Cosy-ClientType: 10` 再次确认**（与 qoder2api 抓包结论一致，本项目 checkin.go 已用）
- IDE 额外带 8 个 `Cosy-*` 机器头；本项目仅发 `cosy-clienttype: 10` 实测也能通过（§1 + 此前 CN 凭据验证），说明机器头非必需
- claim 空 body + `origin` 头，与本项目实现一致

### 3.3 jobToken 端点（sid=598，IDE 启动时调用）

```
POST /api/v1/me/jobToken   body: {"...51 bytes..."}
→ 200 {"token":"jt-...","expires_in":86400000(24h),"refresh_token":"jrt-...","refresh_token_expires_in":172800000(48h)}
```

这是 IDE 运行时短效作业令牌（jt-/jrt-，24h/48h）。qoder2api 的 PAT 交换走的是
`/algo/api/v3/user/jobToken`（gateway 域），与此端点（openapi 域 + dt- 认证）不同。
本项目暂不需要。

---

## 4. IDE 登录时序（从抓包还原）

```
16:09:2x  GET qoder.cn/device/selectAccounts?...  （IDE 内嵌浏览器打开授权页）
          ↳ 轮询 openapi.../api/v1/deviceToken/poll × 40+（404 = 未授权）
16:10:2x  （用户完成阿里云 SSO 登录 + 授权）
          GET qoder.cn/device/redirect?...&directLogin=true → 200
16:10:30  poll → 200 {dt-, drt-, user_id}          ← sid=591
16:10:31  GET /api/v1/userinfo × 2                  （账号信息）
16:10:31  POST /api/v1/me/jobToken                  （运行时令牌 jt-）
16:10:31  GET /api/v2/user/plan × 多次              （套餐/权益）
16:10:31  GET /api/v1/me/partner_plans
16:10:32  GET /sash/api/v1/me/campaigns             （活动列表）
16:10:52  POST /sash/api/v1/me/campaigns/{id}/claim → CLAIMED（IDE 自动领活动）
          ↳ 之后 gateway.qoder.com.cn 推理流量不走代理，未捕获
```

---

## 5. 对本项目 qodercn 渠道的结论

1. **凭据体系验证通过**：IDE 签发的 dt-/drt- 可直接用于本项目 qodercn 渠道
   （业务 API + 签到 + 预期中的 gateway 推理），无需任何转换。
2. **签到实现无需改动**：IDE 与本项目（qoder2api 形态）的 campaigns 链路一致；
   `cosy-clienttype: 10` 必需、机器头可选，均与现有实现吻合。
3. **登录 client_id 差异**：IDE 用 `732aef47` + `qoder.cn` 授权页。
   当前渠道用 qoder2api 的 `e883ade2` 已验证可用；是否切换为 IDE 参数待决议（纯模拟 IDE 更隐蔽，但 IDE 走阿里云 SSO，`provider=aliyun_account_sso` 参数也需带上）。
4. **userType 实测**：该账号 `personal_professional_trial`（Pro Trial），
   与旧 qoder 渠道硬编码值相同——但 qodercn 渠道按设计走 `/api/v2/user/plan`/userinfo 实测，正确。
5. **额度观察**：Pro Trial 的 300 credits 落在 `userQuota`（主池）；
   签到活动的 100 credits 落在 `addOnQuota`（赠送池）——与 `UserResourceDetail` 的两条目口径一致。
6. 余额快照（2026-09-21）：293/300 credits + 已领今日活动。
