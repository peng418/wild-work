# QoderCOM（国际版）渠道抓包分析与接入计划

> 状态：**已实施并验证**（2026-09-21，渠道 `qodercom`）
> 抓包：`ref/qodercom_ide_auth_20260921.saz`（登录授权，196 会话）+
> `ref/qodercom_ide_misc_20260921.saz`（全局代理下的业务流量，7 会话）
> 对象：Qoder 国际版 IDE 0.3.4（UA: `Qoder/0.3.4 ... Electron/43.1.1`，注意与 CN 版 UA 前缀 `QoderCN/0.3.4` 不同）

---

## 1. ★ 已提取并实测可用的凭据

来源：`misc` 抓包 sid=5，`GET https://openapi.qoder.sh/api/v1/userinfo` 请求头。

| 字段 | 值 |
|---|---|
| access token | `dt-DEMO0000000000000000000` |
| uid | `01a0c3ea-0000-7000-8000-000000000002` |
| 账号 | demo-qodercom / demo-qodercom@outlook.example（GitHub SSO，`source=sso.github`） |
| 套餐 | Pro Trial（`personal_professional_trial`，300 credits / 14 天） |
| 余额（实测） | 300/300 未消费 |

### 实测验证（openapi.qoder.sh，全部 200）

| 端点 | 结果 |
|---|---|
| `GET /api/v1/userinfo` | ✅ name=demo-qodercom, email=demo-qodercom@outlook.example |
| `GET /api/v2/user/plan` | ✅ Pro Trial，窗口 2026-09-21 → 10-04 |
| `GET /api/v2/quota/usage` | ✅ 300/300 credits（userQuota） |
| `GET /sash/api/v1/me/campaigns` | ✅ 今日活动 `act-20260921-308` 已被 IDE 领取（CLAIMED，100 credits/30 天） |
| `GET /api/v3/user/status` | ✅（Orchids 用的端点，含 whitelistStatus=PASS） |
| `GET /sash/api/v1/me/daily-check-in/status` | ❌ **404 —— 国际版无 legacy 签到系统** |
| `POST /algo/api/v2/model/list`（api1/api2/openapi 均试） | 403 `Signature invalid` —— **COSY 签名必需**（dt- Bearer 不够） |
| `POST /api/v1/me/jobToken`（空 body） | 400（CN 版此端点 200；国际版 body 格式不同或路径不同，Orchids 用的是 `center.qoder.sh/algo/api/v3/user/jobToken`） |

**结论：QoderCOM 的 dt- 与 CN 版同构同协议，业务 API/签到/推理全链路可用。**

---

## 2. 与 QoderCN 渠道的差异全景

| 项 | QoderCN（.com.cn） | QoderCOM（.sh） |
|---|---|---|
| 业务 API | `openapi.qoder.com.cn` | `openapi.qoder.sh` |
| 推理网关 | `gateway.qoder.com.cn` | `api1.qoder.sh`（chat）/ `api2.qoder.sh`（model list） |
| JobToken | `gateway.../algo/api/v3/user/jobToken` | `center.qoder.sh/algo/api/v3/user/jobToken` |
| 授权页 | `qoder.com.cn/device/selectAccounts` | `qoder.com/device/selectAccounts` |
| IDE OAuth client_id | `732aef47-9cf2-46a2-95fe-4cebb5d0d1fa` | 同 `732aef47-...`（**同一 IDE 客户端，双区共用**，见 §3） |
| IDE redirect | 无（CN 走 `device/redirect` 返回 auth 参数） | `redirect_uri=qoder-app://`（deep-link 回 IDE） |
| 登录方式 | 阿里云 SSO（`provider=aliyun_account_sso`） | GitHub SSO（`provider=github`），实测登录走 web 会话 cookie + `sso/callback` |
| 签到 daily-check-in | 存在但 legacy DISABLED（404 兜底 campaigns） | **端点不存在（404）**，仅有 campaigns 活动 |
| campaigns 活动 | ✅ `act-YYYYMMDD-xxx` | ✅ 同构（`act-20260921-308`），文案 detailUrl 指向 `docs.qoder.com` |
| userinfo | ✅ | ✅ 多 `email` 字段（CN 版也有 name） |
| `/api/v3/user/status` | 未验证 | ✅ 200（Orchids 用它做配额） |
| IDE 机器头 | cosy-clienttype=10 + 7 个机器头 | 同构（machineid/machinetoken/machinetype/machinecode/machineos/machinehostname） |
| UA | `QoderCN/0.3.4 ... Electron/43.1.1` | `Qoder/0.3.4 ... Electron/43.1.1` |

**协议主体（COSY 签名、QoderEncoding、嵌套 SSE、请求体模板）与 CN 版完全同源** —— qoder2api 的双区实现已证明（同一套代码只换域名表）。

---

## 3. 登录流程差异（auth 抓包还原）

COM 版 IDE 登录是**双向流**：

```
IDE → GET openapi.qoder.sh/api/v1/deviceToken/poll?nonce&verifier（404 轮询，sid=001/002）
IDE 内嵌浏览器 → GET qoder.com/device/selectAccounts?challenge&nonce&client_id=732aef47
                  &machine_id&provider=github&redirect_uri=qoder-app://&directLogin=true
浏览器 → GitHub OAuth（sso/login/github → github.com/login/oauth/authorize → sso/callback/github）
浏览器 → POST /sso/signup/confirm（首次注册确认）
浏览器 → GET qoder.com/device/redirect?...&redirect_uri=qoder-app://&directLogin=true
       ← 200 {"redirect_url":"qoder-app:?auth=<obfuscated>&nonce=<nonce>&tokenString=<10字符>"}
IDE 收到 auth/tokenString 后本地换 dt（此步不走代理，poll 成功帧未被抓到）
```

与 CN 版对比：CN 抓包里 **poll 成功帧可见**（591），说明 CN 的 IDE 走 poll 轮询拿 token；
COM 版 redirect 响应直接带 `auth` 参数回传 IDE（`qoder-app://` deep-link），**无需轮询**。
但两种方式最终都产出 `dt-` —— 本项目轮询式（qoder2api 形态）在 COM 区**同样可行**
（client_id `e883ade2` 双区共用，Orchids 已在 COM 区跑通该 client_id 的设备流）。

## 4. 凭据不通用（此前已实测）

CN dt- 在 `.sh` 报 `401 TOKEN_EXPIRE`；COM dt- 在 `.com.cn` 同理（双向隔离）。
**必须独立渠道、独立凭据文件。**

---

## 5. 接入计划（新建 `internal/qodercom/`，代码级复制 `internal/qodercn/`）—— 已全部完成

| # | 项 | 内容 | 状态 |
|---|---|---|---|
| 1 | Kind | `provider.QoderCOM = "qodercom"`（模型前缀 `qodercom/<model>`） | ✅ |
| 2 | `internal/qodercom/constants.go` | Base=`openapi.qoder.sh`、Gateway=`api1.qoder.sh`、**ModelsBase=`api2.qoder.sh`**（双域名分离）；**删除 EpCheckinSt/EpCheckinCl** | ✅ |
| 3 | `internal/qodercom/checkin.go` | 仅 campaigns 路径（无 daily-check-in 分支）；`cosy-clienttype: 10` 同款 | ✅ |
| 4 | `internal/qodercom/cosy.go` | 与 qodercn 同参（cosyVersion 1.0.10 / 18 头）；identity userType 实测回填 | ✅ |
| 5 | `internal/qodercom/models.go` | 动态拉取（ModelsBase 域，COSY 签名）+ 上次成功缓存；场景三级回退 | ✅ |
| 6 | `internal/login_qodercom/` | 授权页 `qoder.com`、poll `openapi.qoder.sh`、client_id `e883ade2-...`；凭据 `qodercom-<uid>.json`（domain=qoder.com） | ✅ |
| 7 | `internal/auth/auth.go` | `LoadQoderCOMDir`（glob `qodercom-*.json`）；`LoadQoderDir` 排除 `qodercn-`/`qodercom-` 两前缀 | ✅ |
| 8 | 装配 main.go | `state-qodercom.json` / qcmPool / qcmUp / qcmSch（签到时段=全局） | ✅ |
| 9 | app.go | StartLoginFor / poll / completeQoderCOMLogin（含 FilePath 回填 + 显示名回填）/ reload / fees | ✅ |
| 10 | Web UI | 「＋ QoderCOM」在行2列2（金色 `#b45309` 与 CN 紫区分）；CH_LABEL/CH_CLASS/CHANNEL_PRESETS/btnAdd 全补 | ✅ |
| 11 | 单测 | `qodercom_test.go`：campaigns 领取/幂等/401/404 无回退/常量断言/模板形态 | ✅ |

### 实施说明（与计划的偏差）

1. **模型列表双域名**：`constants.go` 新增 `ModelsBase = api2.qoder.sh`（推理 api1 与模型 api2 分离，qoder2api Global 表同构），`models.go` 的 `fetchModels` 走 `c.ModelsBase`。
2. **测试凭据**：misc 抓包的 dt- 已写入 `dist/auths/qodercom-01a0c3ea-...json`（nickname=demo-qodercom，domain=qoder.com）。**refreshToken 为空**（IDE 本地换 token 不走代理，drt- 未捕获），已置 `pending-relogin` 占位——scheduler 的签到前置检查只判非空，`dt-` 还有 30 天有效期，短期内无影响；**dt- 过期前需面板重新登录获取 drt-**。

### 端到端验证（2026-09-21 20:54-20:56）

```
loaded accounts: ... qodercn=1, qodercom=1 ...
/v1/models → qodercom/auto|ultimate|performance|efficient|qwen3.8-max|... （动态拉取成功）
R1 qodercom/glm-5.3  「记住代号 Bravo-9」→ 正确，credits=0.0407
R2 追问代号 → 「Bravo-9」（多轮上下文 OK）
手动签到 → ok=true msg=ok（今日 IDE 已领，幂等「已签到」）
余额    → 400（300 userQuota + 100 赠送池，UserResourceDetail 分桶正确）
```

### 已知风险 / 后续决议

1. **client_id**：用 Orchids 验证过的 `e883ade2`（与 login_qodercn 同源，双区可用）；IDE 同款 `732aef47` 需 GitHub SSO 网页交互，后续可切换。
2. **dr t- 缺失**：测试凭据无 refresh token，30 天后过期需重登；正式用户走面板登录会拿到完整 dt-/drt-。
3. **`/api/v3/user/status`**（Orchids 用于配额）：未接，quota/usage 已够用；后续需要 whitelistStatus 时再补。
4. **`/api/v1/me/jobToken`**：COM 区空 body 400，与 CN 行为不同；本项目用不到，无影响。
