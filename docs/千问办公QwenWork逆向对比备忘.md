# 千问办公（QwenWork）逆向对比 + 实机验证备忘

> 目的：对比两个开源参考项目的千问办公逆向原理是否一致，评估与 wild-work 的契合度，并用真实凭据实机验证。
> 克隆位置（不入版本控制）：`ref/xrl-router-plugin-qwenwork`、`ref/Buddy2api`
> 验证脚本：`temp/qwen/`（含明文凭据，勿提交）
> 编写日期：2026-09（对照 wild-work v2.1.x）

---

## 0. 两个参考项目速览

| 项 | xrl-router-plugin-qwenwork | Buddy2api |
|---|---|---|
| 作者 | 杏仁鹿（krkr@xrl.im） | LBJ_WICM |
| 语言/栈 | TypeScript + Express（xrl-router 插件，WS 注册） | Python + FastAPI + SQLite + 静态 Web 管理页 |
| 版本/最后提交 | 0.1.0 / 2026-08-06 | 2.1.9 / 2026-09-15 |
| 许可 | MIT | MIT |
| 渠道 | qwenwork（默认）+ wukong（钉钉悟空 DEAP） | workbuddy / qclaw / qwenwork / traework |
| 定位 | **纯协议桥接层**：不做密钥池/重试/路由（交给 xrl-router） | **完整网关**：账号库、API Key、额度、Codex `/v1/responses` |
| 逆向文档 | `docs/reverse/QWENWORKCN_REVERSE.md`（极详尽，含失败路径与绕过链） | `docs/design/multi-channel-v2.md` Appendix B（实现要点 + 冻结门闩） |

两者**互相独立**（无代码引用关系），却得出高度一致的结论，互证价值很高。

---

## 1. 逆向原理对比：核心算法字节级同源

### 1.1 完全一致的部分

| 环节 | 两项目实现（逐条一致） |
|---|---|
| 应用真身 | QwenWorkCN 桌面端（Electron），userData = `%APPDATA%\QwenWorkCN`，凭据文件 `auth-v2.dat` |
| 凭据加密 | Electron safeStorage `v10` 头。Windows：`Local State` 的 `os_crypt.encrypted_key`（`DPAPI\0` 前缀）→ DPAPI(CurrentUser, entropy=NULL) → 32B AES key → **AES-256-GCM**（`v10 + 12B nonce + ct + 16B tag`） |
| token 刷新 | `POST https://gateway.qwenwork.cn/api/v1/deviceToken/refresh`，body `{refresh_token, target:"c"}` → 返回 `device_token`(或 `token`) + **轮换的** `refresh_token` + `expires_at` |
| AES 会话密钥 | **16 个 ASCII hex 字符**（`uuid4().hex[:16]`），`key = iv = utf8 字节`；**不是** `os.urandom(16)` |
| info 明文 | `{uid, aid:"", name, email, security_oauth_token:<access>}` → AES-128-CBC(PKCS7) → base64 |
| Cosy-Key | `base64(RSA_PKCS1_v1_5(公钥, 16字符))`，**PKCS1 非 OAEP**（OAEP → 403） |
| authorization | `Bearer COSY.<b64(header)>.<md5>`，header = `{version:"v1", requestId, info, cosyVersion, ideVersion}` |
| 签名串 | `f"{o}\n{cosyKey}\n{ts}\n{body}\n{path}"`；`path` = URL pathname，**去 query + 去 `/algo` 前缀** |
| RSA 公钥 | **同一把 PEM**（1024-bit，modulus 头 `c0f223…`），且与 wild-work `internal/qoder/cosy.go` 的 `serverPubKeyPEM` 逐字节相同 |
| 推理端点 | `POST /algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common` |
| 请求体 | **明文 JSON**（不带 `Encode=1`） |
| 响应格式 | 外层 envelope `data:{"headers":…,"body":"<内层 OpenAI chunk JSON>","statusCodeValue":200}`，需剥一层 |
| 签到 | **无签到活动**（Buddy2api `checkin_supported=False`） |
| 静态头族 | `Cosy-Business-Product: qoder_work`、`Cosy-Business-Type: agent`、`Cosy-ClientType: 6`、`Cosy-Scene: qwork`、`Login-Version: v2`、`x-model-source: system` |

**结论：两者对「千问办公逆向原理」的认知完全一致，且与 wild-work 现有 Qoder COSY 实现同构。**

### 1.2 存在差异的部分（版本漂移 + 工程取舍）

| 维度 | xrl-router-plugin | Buddy2api |
|---|---|---|
| 逆向观测版本 | macOS `QwenWorkCN.app 0.1.3` → Windows 实机验证 | Windows `0.1.8-26081406` |
| `Cosy-Version` | `1.0.47` | `1.1.18`（取自 qoderclicn 常量 `l0A`，设 `COSY_VERSION_FROZEN` 门闩，未冻结拒绝出站） |
| header `cosyVersion`/`ideVersion` | `"1.0.0"` / `"1.0.0"` | `1.1.18` / `0.1.8` |
| `Cosy-MachineOS` / UA | `aarch64_darwin` / `node` | `x86_64_win32` / `qoderwork/0.1.8` |
| `Cosy-MachineId` | 固定 `"unknown"` | 取 `auth-v2.dat` 的 `loginDeviceId` |
| body 策略 | **原样透传** OpenAI chat.completions + 补 `request_id`/`session_id` | **重构造** qoder 原生结构（`chat_context.text` 为字符串、`session_type:"qoder_work"`） |
| token 生命周期 | 三源 fallback（内存 → `auth-v2.dat` → `.env QWEN_KEYS`）+ `fs.watch` 文件监听 + **双向写回**，按需刷新（5min 缓冲，单飞防并发） | 账号入库 SQLite，`pick_account_with_fallback` 按需刷新，刷新成功后写回 `auth-v2.dat`（原子 replace + 首次 `.bak` + 保留未知字段） |
| 平台覆盖 | macOS（Keychain + PBKDF2 1003/saltysalt/AES-128-CBC IV=0x20）+ Windows（DPAPI + GCM） | **仅 Windows**（DPAPI），Linux Docker 明确不支持 |
| 额度查询 | 无 | `GET /api/v1/adapter/user/account-context?include=user,plan,quota` |
| 模型列表 | 静态 4 个 | 静态 + `GET /api/v2/model/list` 动态拉取 |

### 1.3 关键发现

1. **RSA 公钥三方一致**（wild-work qoder / xrl / Buddy2api）→ 该 PEM 是「Qoder 系」共享公钥；也佐证千问办公与 QoderWork 同源（xrl 文档：千问办公内部代号即 **Qoder**，出品方 DingTalk，更新源 `static.qoder.com.cn/qwen-work-cn`）。
2. **明文 body 已三方独立验证**（xrl §6.8、Buddy2api Appendix B、本文 §2 实测）。
3. **`cosy-key` 语义已被修正**：xrl §6.1 曾推测为「RSA-1024 封装的会话对称密钥」，§6.8 自我修正为「RSA_PKCS1(asar 公钥, 16 字符 AES key)」，与另两方一致。
4. **`Cosy-Version` 校验宽松**（本文 §2.4 实测：删除或改成 0.1.43/1.0.47 均可）→ 该头非强校验项。
5. **Buddy2api 把 QwenWork 与 QoderWork CN 明确分家**：QwenWork = `gateway.qwenwork.cn` / clienttype **6** / 明文；QoderWork CN = `gateway.qoder.com.cn` / clienttype **5** / `Encode=1` / `dt-`·`drt-`。但**公钥相同、签名串格式相同**，实为同一 COSY 框架的两个实例。
6. **防重放机制**：签名 `md5` 绑定 `body + path + 时间戳`，每请求必须新生成 `timestamp` + 新 `info`/`cosy-key`。xrl §6.5 记录的 `103 Duplicate request` 死锁，根因是复用抓包的 authorization；自造签名即绕过。

---

## 2. 实机验证（2026-09，真实凭据）

### 2.1 验证环境

- 凭据来源：**千问办公网页版**（`qwenwork.cn`）登录态 —— cookie `token`（HS256 JWT，`iss=qwenwork.cn`，`client_type=desktop`，uid `d0fbfa46-…`）+ `ory_hydra_session`
- 桌面端 COSY 签名：Python 复刻（`temp/qwen/cosy.py`），RSA 公钥取三方一致的 PEM
- 目标：`gateway.qwenwork.cn`（桌面端网关）
- 原则：仅只读 GET + 极短推理（`max_tokens` 8~24），每次间隔 3~4s，不并发

### 2.2 核心验证结果

| # | 验证项 | 结果 |
|---|---|---|
| 1 | 网页 cookie 只读端点 `/user/balance` | **200** `{"balance":2098.6147,"freeze_credit":0}` |
| 2 | 网页 cookie `/user/info` | **200**（nickname=demo-qwenwork，Free 套餐，month_requests=100000） |
| 3 | **COSY 签名 GET `/api/v2/model/list`** | **200**，2420 字节，返回 3 个模型 |
| 4 | `?Encode=1` 对 GET 模型列表 | **200**（与不带 Encode 完全相同，说明该参数在 GET 上无效果） |
| 5 | **COSY 签名 POST 推理（明文 body）** | **200** `x-model-name: qwen3.8-flash` / `x-provider-name: maas` |
| 6 | 积分实际扣减 | 2098.5884 → 2098.587（一次 flash 极短对话 ≈ **0.0014** 积分） |
| 7 | `usage` 字段 | `{prompt_tokens:66, completion_tokens:28, total_tokens:94, reasoning_tokens:24}` |
| 8 | 篡改 md5 签名 | **403** `{"code":"101","message":"Signature invalid"}` |
| 9 | body 改了但签名不重算 | **403** Signature invalid |

**→ 逆向结论完全复现：网页版 JWT 可直接驱动桌面端 COSY 签名，明文 body 可用，无需 Encode。**

### 2.3 模型清单（实测，2026-09）

`GET /api/v2/model/list` 返回 **scene = `qwork`，仅 3 个模型**（`chat`/`developer`/`assistant`/`inline` 等 scene 全为空）：

| key | display_name | price_factor | is_reasoning | is_vl | max_input |
|---|---|---|---|---|---|
| `pro` | 高级（Pro） | 1 | false | true | 180000 |
| `flash` | 标准｜Qwen3.8-Flash | **0.1** | false | true | 180000 |
| `qwen3.8-max-preview` | Qwen3.8-Max | **1.8** | false | true | 180000 |

> 注：`max_input_tokens` 该响应为 180000，但 `/api/chat-modes` 为 1000000（见 §6.4）。
> 该字段随响应被截断时的噪声较大，取费率请以完整响应为准。

**⚠️ 与两个参考项目记载的模型清单已发生代际变化：**

| `x-model-key` | 实测路由结果 | 说明 |
|---|---|---|
| `flash` | `x-model-name: qwen3.8-flash` / `maas` | 新档位 |
| `pro` | **`glm-5.2`** / `maas-glm` | 显示名「高级」，实际后端仍是 GLM-5.2 |
| `qwen3.8-max-preview` | （未测） | 新档位 |
| `qwork-advanced` | **`glm-5.2`** / `maas-glm` | **旧 key 仍被接受**（Buddy2api 静态表用此值，未失效） |
| `qwork-auto` / `qwork-lite` / `qmodel_latest` | 未测 | xrl 记载的旧档位 |

`x-model-name` 是**真实后端模型名**，`x-model-key` 只是应用层档位。`pro` 与 `qwork-advanced` 都路由到 `glm-5.2`，说明档位映射在服务端。

### 2.4 请求头容差矩阵（实测，对实现价值最高）

**结论：所有 `Cosy-*` 业务头、版本头、UA 全部不校验；只有 COSY 签名三件套是强校验。**

| 测试 | 结果 |
|---|---|
| 基线（完整静态头） | 200 |
| 删除 `Cosy-ClientType` | 200 |
| 删除 `Cosy-Business-Product` | 200 |
| 删除 `Cosy-Business-Type` | 200 |
| 删除 `Cosy-Scene` | 200 |
| 删除 `Cosy-MachineOS` | 200 |
| 删除 `Login-Version` | 200 |
| 删除 `x-model-source` | 200 |
| 删除 `User-Agent` | 200 |
| 删除 `Cosy-Version` | 200 |
| **删除 `Cosy-User`** | **403 Signature invalid** ← 签名串依赖它 |
| `Cosy-Version: 1.0.47`（xrl 观测值） | 200 |
| `Cosy-Version: 0.1.43`（Qoder CN 值） | 200 |
| `Cosy-ClientType: 5`（Qoder CN 值） | 200 |
| `Cosy-Business-Product: qoder`（非 `qoder_work`） | 200 |
| 极简头（仅 `Content-Type` + `Accept` + COSY 三件套 + `x-model-key`） | **200** |

> 注：`Cosy-User` 本身参与的是 `info`（AES 加密的 userInfo 内含 uid），签名串里的 `cosyKey` 覆盖它；删掉该头即 403，说明网关会从 header 取值参与校验。

### 2.5 body 必需字段（实测）

| 测试 | 结果 |
|---|---|
| 极简头 + 完整 body | 200 |
| **无 `request_id`** | 外层 200 但 `statusCodeValue:400`，`body:{"code":"400","message":"request_id is required"}` |
| **无 `session_id`** | 同上，`session_id is required` |
| 无 `max_tokens` | 200 |
| 无 `model`（仅靠 `x-model-key`） | 200 |
| 无 `stream` | 200 |
| **仅有 `messages`**（无 model/stream/ids） | **200** ← 说明 `request_id`/`session_id` 由服务端兜底？ |

> ⚠️ 矛盾点：单独删 `request_id` 报 400，但「仅有 messages」却 200。推测服务端有幂等/缓存：同一签名请求重复提交时命中已生成的会话。**实现上仍应始终补 `request_id`/`session_id`**（两参考项目一致做法）。
> **关键：网关对错误返回 HTTP 200 + `statusCodeValue:400` 包在 SSE 外层**，客户端必须剥壳判断，不能只看 HTTP 状态码（Buddy2api 的 `envelope_error()` 正是为此）。

### 2.6 x-model-key 与 body model 的关系（实测）

| 测试 | 结果 |
|---|---|
| `x-model-key: pro` + body `model: flash` | **200，`x-model-name: glm-5.2`** → **header 胜出** |
| 去掉 `x-model-key`，body `model: flash` | 200（模型未知，可能回落到默认 `pro`→glm-5.2） |

### 2.7 端点鉴权矩阵（重要差异）

| 端点 | COSY 签名 | 纯 `Bearer <JWT>` |
|---|---|---|
| `GET /api/v2/model/list`（网关） | **200** | **403 Signature invalid** |
| `GET /api/v1/adapter/user/account-context`（网关） | **401 INVALID_TOKEN** | **200** |
| `POST .../agent_chat_generation`（网关） | **200** | 未测（预期 403） |
| `GET https://qwenwork.cn/user/balance`（网页域） | — | cookie 200 |
| `GET https://qwenwork.cn/user/v2/account-context`（网页域） | — | cookie 200 |

**结论：桌面网关内部存在两类端点 —— COSY 签名端点（推理、模型列表）与纯 Bearer 端点（account-context 等账户 API）。混用必失败。** 这与 Buddy2api 的实现完全吻合（`_account_context` 用纯 Bearer，`chat`/`models` 用 COSY）。

### 2.8 token 生命周期（关键问题，未完全解决）

**⚠️ 网页版凭据不能刷新桌面端 token。**

- 网页版是 **cookie / Ory Hydra 会话**：`ory_hydra_session` + `token`（HS256 JWT，24h exp）
- 从 chat app 的 JS bundle（`/app/assets/index-Di6r_6vz.js`，3.4MB）提取到的端点全部是网页域相对路径：`/auth/oauth/*`、`/auth/dingtalk/sso`、`/auth/qr-login/transactions`、`/user/v2/*` 等
- **未发现 `refresh_token` / `deviceToken` / `ory_rt_` 相关逻辑**（桌面端的 `deviceToken/refresh` 是 Electron 端专属）
- 实测：`account-context` 纯 Bearer 可稳定 200（连续多次），说明当前 JWT 在 24h 内有效

**实现建议**：`deviceToken/refresh` 只对**桌面端 `auth-v2.dat` 的 `ory_rt_` refresh token** 有效；网页版凭据应作为「手工导入的一次性 token」，过期后需重新从浏览器复制，不能自动续期。

---

## 3. 与 wild-work 的契合度

### 3.1 wild-work 现状

已有 **Qoder 渠道**（`internal/qoder`）：

| 项 | 现值 |
|---|---|
| 域名 | Base `openapi.qoder.com.cn` / Gateway `gateway.qoder.com.cn` |
| 鉴权 | `dt-` / `drt-`；COSY 签名（`cosy.go`：RSA_PKCS1 + AES-128-CBC + MD5） |
| RSA 公钥 | 与两个参考项目**逐字节相同** |
| 静态头 | `cosy-clienttype: 5`、`cosy-version: 0.1.43`、`cosy-data-policy: AGREE`、`user-agent: Go-http-client/2.0` |
| body | `buildAgentBody` 重构造（`session_type: "qodercli"`、`chat_context.text` 为 **对象**） |
| 编码 | `Encode=1` + `qoderEncode`（base64 + 自定义字母表 + 三段重排） |
| SSE | `parseNestedSSE` 剥 `body` 外层 |
| 签到 | `DailyCheckin` 返回错误（无活动） |
| 登录 | OAuth Device Flow（`client_id 1c5e33e1-…`、PKCE） |

**尚无 qwenwork 渠道**（`gateway.qwenwork.cn` / clienttype 6 / 明文 body / `auth-v2.dat` safeStorage / `ory_rt_` 刷新 / `qoder_work` 产品头）。

### 3.2 契合度评分

| 层 | 契合度 | 说明 |
|---|---|---|
| 签名算法层 | **95%** | `cosy.go` 结构可直接复用，仅需常量参数化 |
| 协议/传输层 | **90%** | 嵌套 SSE、错误分类、`provider.Upstream` 均已就位 |
| 凭据/存储层 | **50%** | `auth.Auth` 是「文件即凭据」，与 `auth-v2.dat`（safeStorage 加密）模型不同 |
| 架构扩展点 | **90%** | `provider.Kind` + `Load*Dir` + `Runtime` 注册三步扩展点齐备 |
| 调度/保活 | **60%** | 有 `KeepaliveHours`，但 QwenWork 的 refresh **轮换**特性与之冲突 |

综合：**约 85%**。

### 3.3 可直接复用的资产

1. **RSA 公钥**：`internal/qoder/cosy.go` 的 `serverPubKeyPEM` 原样可用（已三方互证）。
2. **签名骨架**：`NewCosySession` / `AuthHeader` / `ApplyHeaders` 只需抽出「静态头集合 + 版本常量」为参数。
3. **SSE 剥壳**：`internal/qoder/sse.go` 的 `parseNestedSSE` 与两参考项目等价，envelope 结构相同 → 直接复用。
4. **编码开关**：`internal/qoder/encoding.go` 的 `qoderEncode` 在 QwenWork 上**不需要**（实测明文 200）。
5. **渠道装配模板**：`internal/workbuddyai`（无签到、`KeepaliveHours=nil`）最贴近 QwenWork。

### 3.4 需要新增的工作

| # | 工作项 | 参考实现 | 备注 |
|---|---|---|---|
| 1 | `auth-v2.dat` 解密（Windows DPAPI + AES-256-GCM） | xrl `auth.ts` / Buddy2api `store.py` | wild-work 为 `CGO_ENABLED=0`，用 `golang.org/x/sys/windows.CryptUnprotectData` 手写绑定（无需 cgo） |
| 2 | macOS Keychain 解密（PBKDF2 1003 + saltysalt + AES-128-CBC IV=0x20） | xrl `auth.ts` | 或先 Windows-only 并显式报错 |
| 3 | 写回 `auth-v2.dat`（对称加密 + 原子 replace + 首次 `.bak` + 保留未知字段） | Buddy2api `store.py::write_refreshed_auth` | 见风险 1 |
| 4 | 常量/静态头参数化 | 两项目 constants | 实测**这些头都可以不传**，可只保留 `x-model-key` + COSY 三件套，降低版本漂移风险 |
| 5 | body 策略 | xrl 透传 / Buddy2api 重构造 | **两者实测均 200**，建议照抄 xrl 透传（最省事） |
| 6 | **必须补 `request_id` / `session_id`** | 两项目一致 | 实测缺失 → `statusCodeValue:400 request_id is required` |
| 7 | **必须剥 SSE 外层判错** | Buddy2api `envelope_error()` | 错误是 HTTP 200 + `statusCodeValue:400` |
| 8 | 模型列表 | Buddy2api `models.py`（COSY GET，body 用空串 `""` 而非 `"{}"`） | 实测 `GET /api/v2/model/list` 200；注意档位已更新为 `pro`/`flash`/`qwen3.8-max-preview` |
| 9 | 额度 | 纯 Bearer `account-context`（**不能用 COSY**） | 实测：COSY 401 / Bearer 200 |
| 10 | 登录 | 可做「手工粘贴 JWT」+「导入 `auth-v2.dat`」双路径 | 两参考项目均不做 OAuth |
| 11 | 注册装配 | `cmd/wild-work/main.go` | `provider.QwenWork` + `LoadQwenWorkDir` + `Runtime` + scheduler |

预估新增代码：**约 400–600 行 Go**（含平台分支），加测试。

### 3.5 关键风险与决策点

1. **⚠️ refresh token 轮换互踩（最高优先级）**
   `deviceToken/refresh` 会**轮换** refresh_token。wild-work daemon 常驻 + `KeepaliveHours` 定时保活 → 与用户同时开着的千问办公 App 互相作废。
   - xrl 解法：按需刷新（5min 缓冲，平均 1h 才刷 1 次）+ 写回 `auth-v2.dat` + `fs.watch` 监听 App 侧刷新。
   - Buddy2api 解法：按需刷新 + 刷新成功后写回 `auth-v2.dat`（带备份）。
   - **建议**：QwenWork 渠道设 `KeepaliveHours = nil`（对齐 workbuddyai），改为「每次 chat 前检查 `ExpiresAt`，临近过期才刷新」，并实现写回 `auth-v2.dat`。
2. **写回安全性**：必须保留 `auth-v2.dat` 中未知字段（`loginDeviceId`、`loginMethod`、`refreshStrategy` 等），先备份再原子替换；解密失败**禁止**写回。
3. **零 token 日志不变量**：新渠道需纳入 wild-work「日志/面板/消息框零 token」约束。
4. **平台限制**：DPAPI 绑定当前 Windows 用户，`auth-v2.dat` + `Local State` 拷贝到其他机器/账户无法解密 → 与跨平台定位冲突，需在 UI/文档明确提示。
5. **`x-model-key` 用 `qwork-*`/新档位，勿用真实后端名**：`x-model-key: glm-5.2` 是错用法（Buddy2api 明令禁止）；实测 header 优先级高于 body `model`。
6. **`Encode` 开关别搞混**：Qoder 走 `qoderEncode`（自定义字母表三段重排），QwenWork 走明文。
7. **签到**：两项目均确认无签到活动 → `DailyCheckin` 返回错误，并把 QwenWork 加入 `noExplicitCheckin` 白名单。
8. **版本头会漂移**：实测所有 `Cosy-*` 业务头/版本头都不校验，因此**建议尽量少传**，避免未来版本升级导致硬编码常量失效。
9. **模型档位代际更新**：`pro`（高级）实测后端仍是 `glm-5.2`，`flash` 是 `qwen3.8-flash`（0.1 倍率，最省积分，适合连通性测试）。静态模型表需支持运行时刷新。

---

## 4. 结论

1. **逆向原理完全一致**：两个开源项目对千问办公的逆向结论逐条吻合，RSA 公钥甚至与 wild-work 现有 Qoder 渠道**逐字节相同**，属同一 COSY 框架的两个实例（QwenWork clienttype 6 / QoderWork CN clienttype 5）。
2. **实机验证全部通过**：网页版 cookie JWT 可直接驱动桌面端 COSY 签名；明文 body 推理返回 200；签名篡改/body 篡改均 403；积分扣减真实发生（≈0.0014/次）。
3. **最重要的新发现**：网关对 `Cosy-*` 业务头**几乎零校验**，只有签名三件套（`Authorization`/`Cosy-Key`/`Cosy-User`）强校验；这大幅简化实现，也降低了版本漂移风险。
4. **两个必须处理的协议细节**：`request_id`/`session_id` 必填；错误以 HTTP 200 + `statusCodeValue:400` 的 SSE 外层返回，必须剥壳判断。
5. **与 wild-work 契合度约 85%**：签名/协议/架构三层可直接复用，主要新增工作是 `auth-v2.dat` 解密与写回、常量参数化、token 轮换策略。
6. **最大风险是 refresh token 轮换互踩**，必须用「按需刷新 + 写回 auth-v2.dat」而非现有定时保活模式。
7. **网页版凭据不可自动续期**（无 `deviceToken/refresh` 等价接口），只能作为手工导入的一次性 token。

---

## 5. 参考资料

- `ref/xrl-router-plugin-qwenwork/docs/reverse/QWENWORKCN_REVERSE.md` — 千问办公逆向全文
- `ref/xrl-router-plugin-qwenwork/docs/specs/qwenwork-{signing,token,forward}.md` — 签名、token、转发规格
- `ref/xrl-router-plugin-qwenwork/docs/DECISIONS.md` D-3 — 为什么改为自行管理 token（轮换互踩的原始记录）
- `ref/Buddy2api/docs/design/multi-channel-v2.md` Appendix A/B/C — 通道隔离设计 + QwenWork COSY 要点 + QoderWork CN 未决项
- `ref/Buddy2api/providers/qwenwork/{cosy,chat,token,store,models}.py` — Python 参考实现
- wild-work `internal/qoder/{cosy,client,sse,body,encoding}.go` — 现有 Qoder COSY 实现
- 验证脚本（含明文凭据，勿提交）：`temp/qwen/{cosy.py, stage2_cosy.py, stage9_headers.py, stage10_minimal.py, stage11.py, stage12.py}`
- 验证结果落盘：`temp/qwen/stage{5,6,7,9,10,11,12}_result.txt`

---

## 6. SAZ 抓包分析（`ref/qwenwork_usage_20260918.saz`）

抓包时长 ≈ 2 分钟（2026-09-18 17:08:35 ~ 17:10:54），共 447 个会话，其中 `qwenwork.cn` 143 个。
outline：`temp/qwen/saz/outline.tsv`（解压目录 `temp/qwen/saz/raw/`）。

### 6.1 请求时间轴

| 时刻 | sid | 事件 |
|---|---|---|
| 17:08:35 | 003 | `CONNECT qwenwork.cn:443` TLS 隧道建立 |
| 17:08:36 | 010 | `GET /signin?return_to=/app/chat/...` → 200，落地登录页 |
| 17:08:36 | 015 | `GET /app/runtime-env.js` |
| 17:08:38 | 087 | **`POST /auth/qr-login/transactions`** → **201**，返回 `transaction_id` + `qr_url` + `expires_at`(10min) + `poll_after_ms:2000` |
| 17:08:42~56 | 092~099 | `POST /auth/qr-login/transactions/{id}/poll` × 8，每次返回 `status:pending`（间隔 ~2s） |
| 17:08:58 | 102 | poll → 200（状态变化，响应 102B） |
| 17:09:00 | 103 | **poll → 200 `status:completed`**，**`Set-Cookie: token=<JWT>`**（`client_type=web`，`Max-Age=172799` = 48h） |
| 17:09:01 | 105 | `GET /oauth/signin?auth_source=session&...` → 200，携带新 `token` cookie |
| 17:09:01 | 120/134 | `GET /user/info`、`GET /biz/user/v1/auth/identities?include_enterprise=true` |
| 17:09:01 | 163 | `GET /user/v2/account-context?include=user` |
| 17:09:02 | 256/270 | **`GET /api/notify-ws`** 与 **`GET /api/chat-ws`** → **101 Switching Protocols**（WebSocket 长连） |
| 17:09:02 | 261/267/268/269/271/272 | `GET /user/plans`、`/user/balance`、`/user/wallets`、`/api/chat-sessions/pinned`、`/recent`、会话详情 |
| 17:09:03 | 282/283/284/285 | `GET /api/chat-modes`（**费率表**）、`/user/integrations`、`/user/connections`、`POST /api/chat-sessions/preferences/batch/get` |
| 17:09:06~19 | 307~427 | 大量 `app/assets/*.js` 懒加载；`POST /api/chat-sessions` → **201**（新建会话） |
| 17:09:37 | 437 | `GET /user/balance`（轮询） |
| 17:09:40~45 | 446~591 | `learn.qwenwork.host` 文档站 + `supabase.co`（文档站第三方后端，与本工具无关） |
| 17:09:56 | 612 | `GET /user/v2/account-context?include=data_sharing` |
| 17:10:14 | 635 | `GET /app/settings/usage` → 200（进入用量页） |
| 17:10:14~15 | 645~691 | `app/assets/usage-*.js` 等；`/user/plans`、`/user/balance`、`/user/wallets`、**`GET /user/billings?source=all`**（账单流水） |
| 17:10:21~54 | 708~767 | 会话轮询、`/api/platform/super-agent/comments`、`GET /api/sandbox/.../info`、`/user/info` |

**未出现任何"领取/签到/任务"类请求** —— 全抓包 `qwenwork.cn` 域的**非 GET 请求只有**：`/auth/qr-login/transactions*`（3 类）、`/api/chat-sessions`（建会话）、`/api/chat-sessions/preferences/batch/get`（读偏好，虽是 POST 但为查询语义）。**不存在 daily-claim / receive-reward / checkin 接口。**

### 6.2 凭证抽取

#### (a) 网页会话 token（QR 登录成功后由服务端下发）

```
Set-Cookie: token=<JWT>; Path=/; Max-Age=172799; HttpOnly; Secure; SameSite=Lax
```

JWT payload（`client_type` 是关键差异字段）：
```json
{"aud":"user","client_type":"web","email":"phone_d0fbfa46-...@phone.local",
 "exp":1789895339,"iat":1789722539,"iss":"qwenwork.cn",
 "sub":"d0fbfa46-0000-4000-8000-000000000005",
 "user_id":"d0fbfa46-0000-4000-8000-000000000005","username":"demo-qwenwork"}
```
- 算法 **HS256**，有效期 **48 小时**（172799s）
- 另有 `ory_hydra_session`（Ory Hydra 会话 cookie，供账户/consent 流程）

#### (b) 登录机密参数（`POST /auth/qr-login/transactions` body）

```json
{"return_to":"/app/chat/<session_id>",
 "bx-ua":"231!GRR3fkmU...",          // 阿里设备指纹（超长，>2KB）
 "bx-umidtoken":"T2gAoppicHEgeltaAcbNphYh5gewnmvU1YB1_fVO_x0_htqjTTF1zUPlwTP74WhbaaE=",
 "bx_et":"gAexIqVlgabDRaW5JKfuv29LRZslr_qVirrBIV0D1zU8jkPmsKDm1N387f0joSYT6r4uWfSqSVgTSlNcq9X30okZ..."}
```

⚠️ **`bx-ua` 每次请求都不同**（实测 087/092/103 三份 body 的 `bx-ua` 互不相同），并由阿里 captcha SDK 在浏览器端动态生成 —— **脚本难以复现，这是 QR 登录自动化的主要障碍**。

#### (c) 桌面端凭据（本工具实际使用的路径）

`%APPDATA%\QwenWorkCN\auth-v2.dat`（safeStorage 加密）中的 `token` + `refreshToken`（`ory_rt_`）。
本文 §2 已实测：该 JWT 可直接驱动 `gateway.qwenwork.cn` 的 COSY 签名，**无需 QR 登录**。

### 6.3 关键业务接口（全部实测 200，2026-09-18）

| 用途 | 端点 | 鉴权 | 实测响应 |
|---|---|---|---|
| **总余额** | `GET /user/balance` | cookie `token` | `{"balance":2094.8294,"freeze_credit":0}` |
| **钱包分账** | `GET /user/wallets` | cookie | `daily_credits.total_balance` / `longterm_credits` / `monthly_credits` / `active_wallets[].valid_to` |
| **账户+套餐+额度** | `GET /user/v2/account-context?include=user,plan,quota` | cookie | `user` + `plan.next_refresh_date` + `quota.remaining` |
| **模型费率表** | `GET /api/chat-modes` | cookie | `qwork[]` 含 `key`/`price_factor`/`is_reasoning`/`max_input_tokens`/`context_config` |
| **积分流水** | `GET /user/billings?source=all` | cookie | 逐笔 `{amount, created_at, type, detail.title, source}` |
| **套餐权益** | `GET /user/plans` | cookie | `daily_trial_credits` / `monthly_credits` / `starter_credits` |
| 用户信息 | `GET /user/info` | cookie | nickname / email / page quota |
| 身份列表 | `GET /biz/user/v1/auth/identities?include_enterprise=true` | cookie | 个人/企业身份 |
| 会话列表 | `GET /api/chat-sessions/recent?offset=0&limit=25` | cookie | 会话列表 |
| 长连接 | `GET /api/chat-ws`、`GET /api/notify-ws` | cookie | 101 WebSocket |

### 6.4 模型费率表（`GET /api/chat-modes`，实测）

```json
{"qwork":[
  {"key":"flash","display_name":"标准","price_factor":0.1,"is_reasoning":true,"is_vl":true,
   "max_input_tokens":1000000,"context_config":{"1M":{"is_default":true},"400K":{},"200K":{}},"enable":true},
  {"key":"pro","display_name":"高级","price_factor":1.0,"is_reasoning":true,"is_vl":false,
   "max_input_tokens":1000000,"is_default":true,"enable":true},
  {"key":"qwen3.8-max-preview","display_name":"Qwen3.8-Max","price_factor":1.8,
   "is_reasoning":true,"is_vl":true,"max_input_tokens":1000000,"is_new":true,"enable":true}]}
```

**✅ 两处来源完全一致**（实测复核，见 §6.8）：`flash=0.1` / `pro=1` / `qwen3.8-max-preview=1.8`，与客户端界面显示的「标准 ×0.1、高级 ×1、前沿 Qwen3.8-Max ×1.8」**完全吻合**。
> 早前版本的本备忘曾记录网关侧 `qwen3.8-max-preview=1.1`，那是**读取被截断响应体导致的误读**，已更正。

唯一差异在 `max_input_tokens`：网关侧 180000，网页侧 1000000（`context_config` 声明支持 200K/400K/1M）。以网页侧为准更贴近实际能力。

另注：`/api/chat-modes` 里 `pro` 的 `is_reasoning=true`，而 §2 实测 `pro` 路由到 `glm-5.2` 且思考为可选 —— 该字段是 UI 档位能力声明，非运行时后端属性。

### 6.5 每日积分机制（重要结论：**被动发放，无领取接口**）

`/user/billings` 中的奖励记录（实测，跨 1 个月）：

| 时间 | 金额 | 类型 | 备注 |
|---|---|---|---|
| 2026-08-20 09:04:55 | **+2000** | 奖励 | 欢迎奖励（注册时） |
| 2026-08-20 00:00:00 | **+100** | 奖励 | 每日奖励 |
| 2026-08-24 00:00:00 | **+100** | 奖励 | 每日奖励 |
| 2026-09-17 00:00:00 | **+100** | 奖励 | 每日奖励 |
| 2026-09-18 00:00:00 | **+100** | 奖励 | 每日奖励 |
| 2026-09-17 23:59:59 | −98.4539 | 过期 | 每日积分过期 |
| 2026-08-24 23:59:59 | −96.8131 | 过期 | 每日积分过期 |
| 2026-08-20 23:59:59 | −99.1092 | 过期 | 每日积分过期 |

**关键证据链：**

1. **发放时刻恒为 `00:00:00+08:00`**，精确到整点，4 次记录完全一致 → 服务端定时任务
2. **抓包起始于 17:08，远晚于当日 00:00** → 当日 100 分在用户上线前就已到账
3. **全抓包不存在任何 claim/receive/checkin 请求**（§6.1）
4. **`/user/balance` 全程稳定在 2100**（17:09:02 = 2100，17:09:04 = 2100，17:09:07 = 2100），未因任何客户端动作跳变
5. 过期时刻为 `23:59:59+08:00`，与发放形成**当日有效、隔日清零**的每日钱包（`daily_credits`）

**结论：**
- 个人免费版**每日 100 分**（`subscription-cn-free` 的 `daily_trial_credits=100`）
- **不需要也不存在"领积分"API** —— 只要账号存在，服务端每天 00:00（UTC+8）自动入账，**无需客户端保持活跃**
- 每日积分**当天有效**，23:59:59 过期，`/user/wallets` 的 `daily_credits.total_balance` 即当日剩余
- 高级版每日额度更高（标准版 200 / 高级版 300 / 企业版 150），但同样是 `daily_trial_credits` 被动发放

> 因此**「寻找令客户端保持活跃的 API」这一需求不成立** —— 不存在保活换积分机制。若要做，只需每天定时调一次 `/user/balance` 刷新展示值（读操作，用于面板），而非"领取"。

### 6.6 账号当前状态（实测快照）

```
plan          : subscription-cn-free（个人免费版）
balance       : 2094.8294（总）
  ├ daily_credits    : 94.8294   valid_to 2026-09-19 00:00（当日剩余，明日清零）
  └ longterm_credits : 2000      valid_to 2026-11-20 09:04:55（欢迎奖励，长期有效）
sessions      : 10（并发会话上限）
permissions   : 100000 次/月请求

⚠️ 该账号已**没有"新用户每日 200 分"限时权益**（那是 qwen-office-plus 的权益）。
   Free 套餐 daily_trial_credits = 100，与实测发放量一致。
```

### 6.7 稳定性告警（实测发现）

**同日实测 `pro` 档位的后端模型发生漂移：**

| 时刻 | `x-model-name`（`x-model-key: pro`） |
|---|---|
| 09-17 | `glm-5.2` / `maas-glm` |
| 09-18（本次） | `glm-5.2` / `maas-glm` |
| 09-18（稍后重测） | **`qwen3.8-max`** / `maas` |

→ 上游**会按时间/负载在 `glm-5.2` 与 `qwen3.8-max` 之间切换** `pro` 档位的后端。因此：
- `provider.ModelInfo.Name` **不能硬编码后端名**，必须取自响应头 `x-model-name`
- 模型列表应支持运行时刷新（对齐 wild-work 现有的"刷新费率/模型"机制）

---

## 7. 评估：现有接口是否足够支撑一个新上游渠道？

### 7.1 分项结论

| 能力 | 接口 | 是否具备 | 说明 |
|---|---|---|---|
| **用户余额** | `GET /user/balance` + `/user/wallets` + `/user/v2/account-context` | ✅ **完全足够** | 三个口径齐全：总额、分账（当日/长期/月度）、账户上下文。比 wild-work 现有渠道更丰富 |
| **模型费率表** | `GET /api/chat-modes`（cookie）+ `GET /api/v2/model/list`（COSY） | ✅ **足够**，但需注意 | 有 `price_factor`。**两处数值不一致**，需明确以哪个为准；`provider.ModelPricing` 可直接映射 `price_factor` |
| **领积分** | —— | ✅ **不需要** | 每日积分**服务端被动发放**，无 claim 接口，也**无需保活**。这是本次最重要的结论 |
| 积分明细 | `GET /user/billings?source=all` | ✅ 有 | 逐笔流水，可映射 `provider.ResourceItem` |
| 套餐权益 | `GET /user/plans` | ✅ 有 | `daily_trial_credits` 等，可用于展示"每日可得" |
| 日志/会话活跃 | `GET /api/chat-sessions/recent` | ✅ 有（次要） | 非必需，可选做"保活"读操作 |
| 长连接 | `GET /api/chat-ws` / `notify-ws` | ⚠️ 有但不必用 | 101 WebSocket，仅供 UI 实时通知；API 网关无需 |
| 签到 | —— | ✅ 无签到活动 | 与两参考项目一致，`DailyCheckin` 返回错误并加入 `no_explicit_checkin` |
| 登录 | `POST /auth/qr-login/transactions` | ❌ **不建议做** | 需 `bx-ua` 动态设备指纹（阿里 captcha SDK），脚本难复现；改用 `auth-v2.dat` 导入（§2 已验证可行） |
| token 续期 | `POST /api/v1/deviceToken/refresh` | ✅ 有（桌面端） | 网页 token 48h 且**无 refresh 接口**；桌面端 `ory_rt_` 可轮换 |

### 7.2 总体判定

> **现有接口对"余额 / 费率 / 积分"三项核心需求已经足够，且"领积分"根本不需要实现。**

**关键澄清 —— 用户原始假设需要修正：**

| 原始假设 | 实测事实 |
|---|---|
| "领积分（个人版每日赠送积分，每天 00:00 刷新）" | ✅ 事实正确：每日 00:00（UTC+8）发放，个人免费版 100 分 |
| "可能需要找到令客户端保持活跃的 API" | ❌ **不成立**。发放完全被动，无需任何客户端动作；不存在 claim/保活接口 |

**唯一真正缺失的是"账号来源"**，而非业务接口：

- 网页版 token（48h，无 refresh）**不适合长期跑**，只能做临时验证/手工导入
- 桌面端 `auth-v2.dat`（`ory_rt_` + `deviceToken/refresh`）**才是可持续路径** —— 且它同时提供了推理所需的 COSY 签名凭据

### 7.3 推荐实现方案（接口层面已无阻塞）

```
┌─ 账号来源（二选一，推荐 A）─────────────────────────────┐
│ A. 导入 %APPDATA%\QwenWorkCN\auth-v2.dat               │  ← 持续可用，可自动刷新
│    (DPAPI + AES-256-GCM 解密 → token + refreshToken)   │
│ B. 手工粘贴网页 cookie token（48h，到期需重贴）          │  ← 仅作备用
└────────────────────────────────────────────────────────┘
                    ↓
┌─ 推理（COSY 签名，已验证 200）──────────────────────────┐
│ POST gateway.qwenwork.cn/algo/api/v2/service/pro/sse/  │
│      agent_chat_generation?FetchKeys=...&AgentId=...   │
│ 明文 body；必填 request_id / session_id                  │
│ 极简头即可（Content-Type + Accept + COSY 三件套）        │
└────────────────────────────────────────────────────────┘
                    ↓
┌─ 面板数据（全部 cookie 只读，已验证 200）────────────────┐
│ 余额  : GET https://qwenwork.cn/user/balance            │  → pool 余额
│ 分账  : GET /user/wallets  (daily/longterm/monthly)     │  → 明细（含过期时间 valid_to）
│ 费率  : GET /api/chat-modes  (price_factor)             │  → FetchModelPricing
│ 模型  : GET gateway /api/v2/model/list  (COSY)          │  → FetchModels
│ 流水  : GET /user/billings?source=all                   │  → 可选
│ 套餐  : GET /user/plans  (daily_trial_credits)          │  → 可选
└────────────────────────────────────────────────────────┘
                    ↓
        每日 00:00 服务端自动 +100，**无需任何客户端动作**
        面板刷新只需定时 GET /user/balance（读操作）
```

### 7.4 实现要点小结

1. **`UserResource`**：直接取 `/user/balance` 的 `balance`（= 所有钱包之和，含当日 + 长期），语义清晰。
2. **`UserResourceDetail`**：映射 `/user/wallets`，天然给出 4 类分账；`valid_to` **正好可填 `provider.ResourceItem.ExpireAt`**（对应本仓 §6 第 18 条"到期时间因渠道而异"的约束 —— 注意 QwenWork 是 `2026-09-19T00:00:00+08:00` 格式，须按 UTC+8 解析）。
3. **`FetchModelPricing`**：取 `/api/chat-modes` 的 `price_factor`；**并记录 `x-model-name` 用于纠正展示名**（后端会漂移）。
4. **`DailyCheckin`**：直接返回错误（无签到活动），加入 `no_explicit_checkin` 名单。
5. **`KeepaliveHours`**：设 `nil`。因每日积分被动发放，**不需要定时保活**；但仍建议定时刷新余额用于面板展示。
6. **`Classify`**：注意网页域错误返回是 `{"code":"...","msg":"..."}`（非网关的 `errorCode`），且网关错误可能包在 SSE `statusCodeValue` 里 —— 需同时兼容。
7. **两套鉴权不可混用**：COSY 端点（推理、`/api/v2/model/list`）用签名；cookie 端点（余额、费率、账单）用 `token` cookie。实测混用分别 403 / 401。
8. **模型 key 白名单**：`flash`(×0.1) / `pro`(×1) / `qwen3.8-max-preview`(×1.8)，**两处接口数值一致**，可直接用于 `FetchModelPricing`；兼容旧 key `qwork-advanced`（仍路由到 glm-5.2）。

### 7.5 尚存的未知 / 风险

| # | 项 | 影响 | 缓解 |
|---|---|---|---|
| 1 | 网页 token 48h 且无 refresh | 网页路径不可持续 | 用 `auth-v2.dat` 路径（§2 已验证） |
| 2 | `bx-ua` 动态指纹 | 无法自动化 QR 登录 | 不做登录，只做凭据导入 |
| 3 | ~~两处 `price_factor` 不一致~~ | ✅ **已排除**（§6.8 复核一致） | —— |
| 4 | `pro` 后端模型漂移（glm-5.2 ↔ qwen3.8-max） | 展示名不稳 | 取响应头 `x-model-name` |
| 5 | 每日积分 23:59:59 过期 | pool 余额呈现"锯齿" | 展示时区分 `daily_credits` 与 `longterm_credits`，路由排序应以**长期+当日合计**为准，并注意当日部分次日归零 |
| 6 | 抓包未见"扣费口径"字段 | 无法精确预估单次消耗 | 用 `x-model-key` 的 `price_factor` × usage.token 估算 |

### 6.8 费率复核：两处来源一致（更正此前误读）

**结论：网关 `/api/v2/model/list` 与网页 `/api/chat-modes` 的 `price_factor` 完全相同，且与客户端界面显示一一对应。**

| key | 客户端界面 | 网关 `model/list` | 网页 `chat-modes` |
|---|---|---|---|
| `flash` | 标准 ×0.1 | 0.1 ✅ | 0.1 ✅ |
| `pro` | 高级 ×1 | 1 ✅ | 1 ✅ |
| `qwen3.8-max-preview` | 前沿 Qwen3.8-Max ×1.8 | **1.8** ✅ | **1.8** ✅ |

> ⚠️ 本备忘早期版本曾记录网关侧 `qwen3.8-max-preview=1.1`，那是**读取被截断响应体导致的误读**（`max_input_tokens=180000` 也是同一噪声），已更正。

**唯一真实差异：`max_input_tokens`**

| 来源 | 值 |
|---|---|
| 网关 `/api/v2/model/list` | 180000 |
| 网页 `/api/chat-modes` | 1000000，且 `context_config` 声明 `200K` / `400K` / `1M`（`1M` 为 default） |

→ **上游实际能力是 1M 上下文**，网关侧的 180000 是过时/降级值。实现时 `provider.ModelInfo.ContextWindow` 建议取 `chat-modes` 的 `max_input_tokens`（或按需让用户在 200K/400K/1M 中选择）。

**实现建议（费率）**

```go
// FetchModelPricing 可直接取 price_factor（两处一致，任选其一）
//   - 网页 /api/chat-modes  : 需要 cookie 鉴权，字段更全（i18n / context_config / is_new）
//   - 网关 /api/v2/model/list : 需要 COSY 签名，字段含 is_default / is_reasoning / is_vl
// 建议：模型发现走网关（与推理同一会话/鉴权，少一套 cookie），
//       上下文长度等 UI 元数据补充自 /api/chat-modes（可选）。
// price_factor 语义 = 相对基准倍率，成本 ≈ price_factor × token 用量（pro 为 1.0 基准）
```

---

## 8. 渠道实施记录（v2.2.2，2026-09-19）

### 8.1 代码落点

| 文件 | 职责 | 行数 |
|---|---|---|
| `internal/qwenwork/constants.go` | 域名/端点/模型映射表/静态模型 | ~100 |
| `internal/qwenwork/cosy.go` | COSY 签名（RSA_PKCS1 + AES-128-CBC + MD5），5 字段 info | ~150 |
| `internal/qwenwork/sse.go` | 嵌套 SSE 剥壳（含 envelope statusCodeValue 判错）+ Stream/Aggregate | ~300 |
| `internal/qwenwork/client.go` | provider.Upstream 实现：ChatStream/RefreshToken/FetchModels/FetchModelPricing/UserResource*/DailyCheckin/Classify | ~550 |
| `internal/qwenwork/qwenwork_test.go` | 12 个单测（签名/改写/SSE/解析/分类/透传） | ~300 |
| `internal/auth/auth.go` | `LoadQwenWorkDir`（qwenwork-*.json 前缀） | +24 |
| `internal/provider/provider.go` | `Kind = "qwenwork"` | +1 |
| `internal/app/app.go` | noExplicitCheckin/firstRuntime/费率渠道清单/登录拦截 | ~6 |
| `cmd/wild-work/main.go` | 装配：pool/scheduler/runtime/observer/credit-refresh | ~20 |
| `cmd/wild-work/web/*` | 渠道标签/添加按钮（青蓝色 #0e7490）/签到文案/模型映射预设 | ~12 |

### 8.2 实现关键决策

1. **双轨鉴权**：COSY（gateway 域：推理、模型列表）+ 纯 Bearer（qwenwork.cn 域：余额/费率/钱包）。实测混用分别 403/401（备忘 §2.7）。
2. **最小头集**：只发 `Authorization/Cosy-Key/Cosy-User/Cosy-Date` 四件套 + `Content-Type/Accept`。实测全部业务头可省（备忘 §2.4），规避版本漂移。
3. **`x-model-key` 必须显式**：缺省 → envelope 403 "Model is not available for this user"（HTTP 仍 200，实测 §8.4）。
4. **envelope 判错**：网关错误以 HTTP 200 + `statusCodeValue>=400` 包裹（实测缺 request_id 时），`parseNestedSSE` 内联剥壳转 error。
5. **余额精度**：上游浮点积分（如 2094.8294）×100 转厘（int64），四舍五入；`ResourceItem` 按钱包分类：每日奖励（带 `valid_to` 到期）/长期积分/月度积分，均 `Usable=true`。
6. **`KeepaliveHours=nil` + `CheckinMinutes=nil`**：无签到、无保活需求；仅靠 `StartCreditAutoRefresh` 循环刷新余额（顺带 401 自愈）。
7. **model 兼容映射**：`auto`/`qwork-advanced`/`qwork-auto`→`pro`；`qwork-lite`→`flash`；`qmodel_latest`→`qwen3.8-max-preview`；防御性剥离 `qwenwork/` 前缀。
8. **developer 角色不改写**：实测可接受（与 qoder 渠道不同），但仍强制 `stream=true`（SSE-only 端点）。
9. **登录暂缺**：QR 登录依赖阿里 `bx-ua` 动态指纹（每次不同，无法脚本化）；Web UI「添加千问办公」给出导入指引（`auths/qwenwork-<uid>.json`）。
10. **token 双形态支持**：桌面端（`ory_rt_` 可刷新 48h+）与网页版（48h 一次性）均写入同一 auth 格式；`RefreshToken` 无 refresh token 时报错但不阻塞其他账号。

### 8.3 实机联调结果（2026-09-19 00:25，wild-work.exe --no-tray）

| 验证 | 结果 |
|---|---|
| 凭证加载 | `qwenwork=1 from ./auths` ✅ |
| `POST /v1/chat/completions`（非流式，flash） | **HTTP 200**，content="成功"，usage 完整（435 tokens） ✅ |
| `POST /v1/chat/completions`（流式，pro） | 标准 OpenAI SSE，`x-model-name: glm-5.2`，末尾 `[DONE]`，raw_usage 含 `router_strategy_name: qwork_advanced` ✅ |
| `GET /v1/models` | `qwenwork/pro`、`qwenwork/flash`、`qwenwork/qwen3.8-max-preview` ✅ |
| `GET /api/fees` | 三档 rate=0.1/1/1.8，与客户端界面一致 ✅ |
| `GET /api/state` | credits=209999 厘（2100.00 正确 ×100） ✅ |
| `go vet ./...` / `go test ./...` | 全绿 ✅ |

### 8.4 联调中发现并修复的问题

1. **内联匿名 struct 标签丢失**：`struct{ TotalBalance float64 } \`json:"daily_credits"\`` 只给外层字段打了标签，
   内层 `total_balance` 无标签 → 解析恒 0（回归测试 `TestParseWalletTotals` 固化）。
   → 改为具名 `walletTotals struct{ TotalBalance float64 \`json:"total_balance"\` }`。
2. **带前缀 model 防御**：e2e 手工测试直接传 `qwenwork/flash` 时（正常 server 层已剥前缀），
   `ModelKey` 查不到 → 原 key 透传 → 上游 403。→ `prepareChatBody` 增加 `LastIndex("/")` 剥离。
3. **flash 偶发 403 "Model is not available"**：非账号问题 —— 是**缺省模型被拒**（Python 对照测试：
   无 `x-model-key` 头必 403；带任意合法 key 均 200）。Go 实现恒发该头，不受影响。

### 8.5 遗留事项

| # | 事项 | 优先级 |
|---|---|---|
| 1 | 桌面端 `auth-v2.dat` 一键导入（DPAPI + AES-256-GCM 解密，`x/sys/windows.CryptUnprotectData`） | 高——目前只能手工构造 JSON |
| 2 | Web UI「添加千问办公」改为凭据粘贴对话框（当前提示手工导入） | 中 |
| 3 | `qwen3.8-max-preview` 档位实测（联调只测了 flash/pro；路由预期 `qwen3.8-max`） | 低 |
| 4 | 网关 `max_input_tokens=180000` 与 `chat-modes` 1000000 的差异——静态表取 1M，动态表随上游 | 低 |

### 8.6 余额单位修正（2026-09-19）

**问题**：早期实现把上游余额 ×100（2094.8294 → 209483 厘），面板显示虚高 100 倍。

**根因**：wild-work 全链路积分单位 = 上游原始整数（如 qoder 的 `int64(Remaining)`、workbuddy 的积分整数），
面板直接显示，**没有 ×100 厘的约定**。千问办公 `/user/balance` 下发的浮点数（2090.38）就是界面显示的同源值。

**修正**：`roundCents`（×100）→ `truncCredits`（int64 直接截断小数），与其他渠道口径一致；
`TestRoundCents` → `TestTruncCredits` 回归固化（含 2090.38 → 2090 断言）。
截断后对话零头（如 -0.0014）不可见，可接受 —— 与界面显示一致。

---

## 9. IDE（桌面版）登录链路逆向（`ref/qwenwork_auth_20260919.saz`）

抓包时长 ≈ 30s（12:37:02 ~ 12:37:33），`qwenwork.cn` 44 个会话 + gateway callback 1 个。
这是**桌面 IDE 内嵌浏览器**发起的登录（UA：`QwenWorkCN/1.0.6 Chrome/138 Electron/37.10.3`）。

### 9.1 完整时序（PKCE + Ory Hydra + QR 登录三合一）

| 步 | sid | 请求 | 说明 |
|---|---|---|---|
| 0 | — | 桌面端生成 PKCE（`code_challenge=vFaD53...`，S256）+ `adapter_state` + `umid_token` + `x_mini_wua`（阿里 SecurityGuard） | **state 是 base64 JSON**，含 adapter_state/umid_token/wua |
| 1 | 007 | `GET /oauth2/auth?client_id=qwenwork-desktop-app&code_challenge=...&redirect_uri=https://gateway.qwenwork.cn/oauth/callback&response_type=code&scope=openid+profile+email+offline_access+qwen_work&state=<b64>` | Hydra 授权起点 → **302** `/biz/signin?login_challenge=...`，下发 `ory_hydra_login_csrf_*` cookie |
| 2 | 019 | `GET /auth/oauth/login-request-url?login_challenge=...` → `{request_url}`（同一授权 URL，前端取用） | |
| 3 | 140 | `POST /auth/qr-login/transactions` body `{"return_to":"/app","login_challenge":"...","bx-ua":"...","bx-umidtoken":"...","bx_et":"..."}` → **201** `{transaction_id, qr_url, poll_after_ms}` | **QR 事务绑定 login_challenge**（与网页版唯一差异：网页版 body 无 login_challenge） |
| 4 | 149~152 | `POST .../{id}/poll` ×4 → `status:pending` → `completed` | **poll 成功时 Set-Cookie: token=<JWT client_type=desktop>**（48h） |
| 5 | 154 | 浏览器跳 `redirect_to` → `/oauth/signin?...login_challenge=...` | session 已建立（token cookie 生效，`GET /user/info` 200） |
| 6 | 216 | 带 `identity_receipt` 重跳 `/oauth/signin?...` | 前端身份确认 |
| 7 | 261 | `POST /auth/oauth/accept-login` body `{"login_challenge","user_id","bx-ua","bx-umidtoken","bx_et"}` → **200** `{redirect_to: /oauth2/auth?...}` | 以 session 身份接受 Hydra login challenge |
| 8 | 266/309 | Hydra 重跳 → `/consent?consent_challenge=...` → `GET /auth/oauth/consent-check?consent_challenge=...` → `{redirect_to}` | 自动同意（scope: openid profile email offline_access qwen_work） |
| 9 | 309 | `GET /oauth2/auth?...consent_verifier=...` → **303** `Location: https://gateway.qwenwork.cn/oauth/callback?code=ory_ac_...&scope=...&state=<原 b64>` | **code 由 gateway 域接收** |
| 10 | 315 | `GET gateway.qwenwork.cn/oauth/callback?code=...` → 200「登录成功」页（含 `qwenwork-cn://` deep link） | **code 兑换 token 在桌面进程内完成**（抓包外，TLS 直连） |

### 9.2 与网页版登录的关键差异

| 项 | 网页版（usage 抓包） | IDE 版（本次） |
|---|---|---|
| QR 事务 body | `{"return_to","bx-ua","bx-umidtoken","bx_et"}` | 多一个 **`login_challenge`** |
| poll 成功 Set-Cookie | `token`（`client_type=web`） | `token`（**`client_type=desktop`**） |
| 后续 | 直接进 /app | 继续走 Hydra：accept-login → consent → **302 到 gateway callback 带 `code`** |
| 最终凭据 | cookie `token`（48h，无 refresh） | 桌面进程用 `code + code_verifier` 兑换 → **access_token + refresh_token（ory_rt_）** 落 auth-v2.dat |

### 9.3 自动化可行性评估

| 环节 | 可脚本化 | 说明 |
|---|---|---|
| 生成 PKCE + state | ✅ | state 里的 `adapter_state`/`umid_token`/`x_mini_wua` 由桌面端 SecurityGuard 生成——**但 wild-work 可以自造 state**（服务端只透传 state，兑换时才校验 code_verifier） |
| 打开 `/oauth2/auth` 拿 `login_challenge` | ✅ | 302 Location 直接提取 |
| `POST /auth/qr-login/transactions` | ⚠️ | 需要 `bx-ua`/`bx-umidtoken`/`bx_et`（阿里设备指纹）——**实测网页版同样需要**， wild-work 无法自造 |
| **复用已有登录态跳过 QR** | ✅✅ | **关键发现：若浏览器 session 已登录（token cookie 有效），/biz/signin 页会直接走 154→261 accept-login，无需 QR**。wild-work 可引导用户在系统浏览器先登录网页版一次，之后「添加账号」只需走 Hydra 自动链 |
| accept-login | ⚠️ | body 需要 `user_id`（session 可取）+ `bx-ua` 族——**实测该接口在已有 session 下仍要求 bx 指纹**（本次抓包带了）；是否强校验待验证 |
| consent-check → code | ✅ | 全自动 302 链 |
| code 兑换 | ✅ | `POST gateway/api/v1/deviceToken/refresh` 之外的兑换端点需确认（抓包外）；Buddy2api/xrl 均未实现登录，无参考 |

**结论：**
1. **「系统浏览器先登录 + 自动 Hydra 链」是唯一可对齐其它渠道的登录方式**——用户在浏览器登录一次（cookie 有效期内），wild-work 打开 `/oauth2/auth` 后可全自动走到 `gateway callback?code=...`；
2. **QR 全自动不可行**（bx-ua 指纹），但可以把 QR 二维码**展示给用户手机扫**（浏览器打开 /biz/signin 页即可，无需脚本化 bx）；
3. **code 兑换端点未在抓包中出现**（桌面进程内 TLS 直连）——需要从 qoderclicn 二进制或 auth-v2.dat 写回逻辑推断，或实测 `/oauth2/token`（Hydra 标准 token endpoint，client `qwenwork-desktop-app` 为 public client，PKCE 无 secret）。

### 9.4 推荐实现（对齐其它渠道的 login_<channel> 编排）

```
Start()
  1. 生成 PKCE verifier/challenge
  2. GET {gw}/oauth2/auth?client_id=qwenwork-desktop-app&code_challenge=...&S256
     &redirect_uri=http://127.0.0.1:<port>/callback&response_type=code
     &scope=openid+profile+email+offline_access+qwen_work&state=<自造 b64>
     → 302 → 提取 login_challenge（或直接把整个 302 URL 交给系统浏览器）
  3. 用系统浏览器打开授权 URL（对齐 workbuddy/traework 的「浏览器登录」模式）
     - 用户在浏览器里可能需要扫一次 QR（若 session 未登录）
     - 若浏览器已登录 → 全自动跳转
  4. redirect_uri 改为本机 http 回调（需实测网关是否允许自定义 redirect_uri；
     若强制 https://gateway.qwenwork.cn/oauth/callback 则改用轮询/拦截方式）
Poll()
  5. 本机回调收 code → POST Hydra token endpoint（待确认）
     {grant_type:authorization_code, code, redirect_uri, client_id, code_verifier}
  6. → {access_token, refresh_token(ory_rt_), expires_in} → SaveAuth
```

**未决问题（实现前必须实测）：**
- `redirect_uri` 是否强制 `https://gateway.qwenwork.cn/oauth/callback`（Hydra client 注册白名单）；
- token endpoint 路径与 body（Hydra 标准 `/oauth2/token` vs 自定义）；
- `bx-ua` 在 accept-login/consent 是否强校验（若强校验且无 session，则纯自动化登录不可行）。

### 9.5 登录实现落地（2026-09-19）

新增 `internal/login_qwenwork`，与其它渠道完全对齐的 `Start/Poll/SaveAuth/NewClient` 契约：

| 项 | 实现 |
|---|---|
| 协议 | PKCE(S256) + Ory Hydra 授权码流，`client_id=qwenwork-desktop-app`（public client，无 secret） |
| 回调 | **本机 HTTP 回调**（`http://127.0.0.1:<随机端口>/callback`）——实测 Hydra 接受自定义 redirect_uri 与自造 state（§9.3 测 1） |
| 兑换 | `POST /oauth2/token`（`grant_type=authorization_code` + `code_verifier`），在 wild-work 本机完成（桌面官方是进程内兑换） |
| 身份 | 从 JWT payload 解 `user_id`/`username`（token endpoint 不返回 userinfo） |
| 凭据落盘 | `auths/qwenwork-<uid>.json`（嵌套形，含 `refreshToken` ory_rt_ 与 `apiHost`） |
| 资源清理 | 回调 server 在 Poll 成功/失败/超时（5min）/CancelLogin 四路径统一 `Shutdown()` |
| 用户交互 | 系统浏览器打开授权 URL；已登录 session → 全自动跳回；未登录 → 浏览器内扫 QR（脚本不碰 bx-ua 指纹） |

**实机联调**（`temp/qwen/login_e2e.go`）：
- `Start` 生成授权 URL + 本机回调监听 ✅
- 无 cookie 访问授权 URL → Hydra 302 `/biz/signin?login_challenge=...` ✅
- 回调探针（`?error=access_denied`）→ Poll 正确退出并透出上游错误文案 ✅
- 完整 code 兑换需真人浏览器登录，留待用户自测

**接入点**：`app.StartLoginFor/pollLogin/completeQwenWorkLogin/CancelLogin` 四处 + Web UI 提示文案更新。

### 9.6 登录接线缺失排查与修复（2026-09-19）

**现象**：面板点「添加千问办公」返回「暂不支持在线登录」，无法发起本机回调。

**根因（两处，均为 `app.go` 的 switch 分支遗漏，系实施时 edit 序列报错后重发遗漏所致）**：
1. `StartLoginFor` 第一处 switch 的 `case provider.QwenWork: return error` 残留 —— 直接拦截了登录发起；
2. `StartLoginFor` 第二处 switch（分配 `loginClient`）与第三处 switch（调用 `Start`）均缺 `case provider.QwenWork` —— 即使过了第一关也会走 workbuddy 的 default 分支。
   加上构建时 `dist/wild-work.exe` 被运行中进程占用导致 **build 静默失败**（copy error），daemon 一直跑旧版 —— 三层叠加。

**修复**：
- 删除拦截分支，`provider.QwenWork` 加入已编排渠道白名单；
- `loginClient = loginqwenwork.NewClient()`；`authURL, err = loginqwenwork.Start(...)`；
- 最终 `provider.QwenWork` 在 app.go 共 9 处接线（白名单/client/Start/poll/complete/cancel-shutdown/firstRuntime/noExplicitCheckin/credit-refresh）。

**daemon 实机回归**：
- `POST /api/login/start {channel:"qwenwork"}` → **200**，返回有效授权 URL（`/oauth2/auth?...redirect_uri=http://127.0.0.1:55157/callback...`）✅
- daemon 内回调 server 随 login/start 启动，探针 `?error=probe` 命中回调页 ✅
- `login/cancel` → 回调 server 关闭 ✅

**经验**：构建前必须确认旧 daemon 已退出（Windows 文件锁会让 `go build -o dist/...` 失败但被忽略）；
多段 edit 序列中断后应 `rg` 全量核对接线点，而不是依赖记忆。

### 9.7 扫码登录后账号不出现——第二处 bug（2026-09-20）

**现象**：浏览器扫码、回调页显示成功，但面板无 qwenwork 账号。

**日志证据**：`login poll panic: strings: negative Repeat count`

**根因**：`parseJWTIdentity` 用 `p += strings.Repeat("=", -len(p)%4)` 补 base64 填充。
Go 的 `%` 保留被除数符号：当 payload 长度非 4 的倍数时 `-len%4` 为**负数**，`strings.Repeat` 直接 panic。
panic 发生在 `Poll` 内（token 兑换已成功、JWT 已到手），`completeQwenWorkLogin` 未执行 →
auth 文件未写、`reloadAccounts` 未触发 → 账号不出现。

**修复**：
1. 填充改为 `if r := len(p)%4; r != 0 { p += strings.Repeat("=", 4-r) }`；
2. `reloadAccounts` 补 qwenwork 分支（此前另一处接线遗漏：登录后池不同步）。

**教训**：跨语言移植 base64 填充惯用法时注意 Go `%` 的符号语义（Python 的 `%` 恒非负，Go/C 不是）。

### 9.8 回调页统一（2026-09-20）

5 渠道回调页行为盘点与统一：

| 渠道 | 回调方式 | 原行为 | 统一后 |
|---|---|---|---|
| workbuddy / workbuddyai / qoder | 轮询上游 state 端点 | 无回调页 | 不涉及 |
| traework | 本机回调 | 静态一行字，不自动关 | **统一回调页** |
| qwenwork | 本机回调 | 一行小字 + 2~3s 自动关 | **统一回调页** |

统一回调页（`callbackPage`，两 login 包各内联一份，包间零耦合）：
- 成功态：✓ 图标卡片 + 「登录成功」+ 1.5s 自动 `window.close()`；被浏览器拦截时按钮变「关闭此标签页」兜底
- 失败态：✕ 红色图标 + 具体错误文案（HTML 转义防 XSS），不自动关（便于截图反馈）
- 品牌色与面板渠道色一致（青蓝 #0e7490）；零外部依赖
- 单测 `TestCallbackPageStructure`：占位符替换完整性 + XSS 转义

### 9.9 布局与初始积分修正（2026-09-20）

1. **账号管理按钮布局**：改为水平两行、共用 5 列 grid 轨道（CSS grid + `justify-content:end`）：
   第一行 `刷新积分(左对齐) / +WorkBuddy / +国际版 / +Qoder / +TraeWork`，
   第二行 `全部签到(左对齐) / +千问办公`（对齐上一行国际版位置）+ 末列留白。
2. **新账号初始积分 0**：`completeQwenWorkLogin` 缺「登录后立即拉余额」步骤
   （`completeWbaiLogin` 有，是各渠道的对齐范例）→ 补 `safeGo(RefreshCredits(uid))`。
   注：`StartCreditAutoRefresh` 启动即刷一次，但 daemon 启动早于登录，新增账号只能靠登录后主动刷。

### 9.10 nickname 显示 hex id——OAuth token 与 cookie token 的差异（2026-09-20）

**现象**：扫码登录成功、积分正常，但面板显示 hex uid 而非昵称。

**根因**：两条 token 签发链的 payload 不同——
- 网页 cookie token（`aud=user`）与 deviceToken/refresh 换出的 token：**带 `username`**
- OAuth `/oauth2/token` 兑换的 access token（`aud=oauth_app`）：**偶发无 `username`**
  （首登时序：Poll 兑换的原始 token 解出 nickname 空 → credits refresh 触发 401 →
  session-dead 自动 refresh 换出的新 token 才有 username）

**修复**：
1. `completeQwenWorkLogin` 增加 nickname 兜底：JWT 解出为空时用 token 调 `/user/info`
   拉昵称（`FetchNickname`），写回 auth 文件并同步池；
2. 现有账号的 auth 文件已手动补齐（dist/auths/qwenwork-*.json nickname=demo-qwenwork）。

**教训**：OAuth 标准允许 access token 不携带用户claims；昵称应以 userinfo 端点为准，
JWT 仅作快照优化。

### 9.11 全渠道「登录后初始积分 0」盘点与修复（2026-09-20）

**现象**：新添加的账号面板显示 0 积分，手动「刷新积分」才正确。用户反馈 workbuddy 国际版与千问办公均有此问题。

**根因盘点**（登录完成回调的初始化动作差异）：

| 渠道 | 登录后动作 | 结果 |
|---|---|---|
| workbuddy | CheckinAccount（签到流程含余额查询→ReenableIfCredits 写入 credits） | ✅ 签到成功即正确；**签到/余额查询失败则 0** |
| traework | 同上 | ✅ 同上 |
| workbuddyai | RefreshCredits | ✅ |
| **qoder** | **无任何动作** | ❌ **恒 0，直到下个 auto-refresh 周期** |
| qwenwork | RefreshCredits | ✅ |

**修复**（统一约定：登录完成后显式 RefreshCredits 一次，幂等多刷无害）：
- qoder：补 RefreshCredits（真正缺失的渠道）
- workbuddy / traework：签到流程之后追加 RefreshCredits 兜底（覆盖签到失败路径）
- workbuddyai / qwenwork：已有，不变

**另一台电脑显示 uid 的补充说明**：除 §9.10 的 token 差异外，旧 exe 也可能未含昵称兜底逻辑——
分发前请确认 exe 为最新构建（含 `FetchNickname` 与兜底时序修复：先 RefreshCredits 修 token，再拉昵称）。

### 6.9 flash 上下文容量实测（2026-09-20，真实计费二分探测）

**问题**：网关 `model/list` 声明 `max_input_tokens=180000`，网页 `chat-modes` 声明 `1M`（`context_config`: 200K/400K/1M），应以哪个为准？

**实测**（flash 档，`max_tokens=1`，真实扣费二分探测）：

| 输入（字≈tokens） | 结果 |
|---|---|
| 50,061 | ✅ 200 |
| 114,561 | ✅ 200 |
| ~146K / ~163K / ~171K / ~175K | ✅ 200 |
| 179,000 | ❌ **424 `All models failed`**（两次复验一致） |

**结论**：
1. **真实单次输入上限 ≈ 180K，与网关声明吻合**；超过即 424 熔断（所有后端拒绝）。
2. 网页 `chat-modes` 的 1M 是**前端档位选择器的最大可选值**（官方客户端按 context_config 分块处理），并非单次请求真实能力。
3. **wild-work 应采用网关 180K 口径**（已改：动态取上游值 + 缺失兜底 180K；静态表同步改 1M→180K）。

**计费附带发现**：
- `/user/balance` 异步结算（§2.2 已知），大请求扣费延迟到账，实时差值不可用于计费验证。
- 失败请求（424）也扣费（179K 探测失败仍扣 ~5）——上游已消耗推理资源。
- 扣费数值与 tokens 非线性对应，存在按次结算/折扣波动，精确计费模型未逆向。
- 本轮探测总消耗 ≈ 40 积分。

### 10. 渠道故障诊断（2026-09-21）：503 Model catalog unavailable

**现象**：千问办公渠道推理全部返回 `HTTP 200 + envelope 503 "Model catalog unavailable"`；
官方客户端同账号正常。模型列表、余额、refresh 均正常。

**排除矩阵**（8 轮实验，客户端可控维度全部穷尽）：

| 维度 | 实验 | 结果 |
|---|---|---|
| 签名 | 极简头/完整头 | 通过（非 403） |
| token | OAuth 原始 / refresh 后 | 均 503 |
| 机器指纹 | 加 Cosy-MachineId/Token | 503 |
| 档位 | flash / pro / qwen3.8-max-preview | 全 503 |
| body 结构 | OpenAI 透传 / Buddy2api 原生 / qoder 风格 | 全 503 |
| 版本头 | cosyVersion 1.0.0 / 0.1.43 / 1.1.18 | 全 503 |
| UA | node / qoderwork/0.1.8 / 官方 Electron | 全 503 |
| Buddy2api 22 头完整克隆（含 X-QwenWork-* 族） | E7/E8/E9 | 全 503 |
| info 字段 | 加 client_type=desktop | 503 |
| query | FetchKeys/AgentId 变体 | 503（裸路径 400 agent_id required 说明路由正常） |
| info 变体 | uid/aid/name/email/security_oauth_token 精确复刻 | 503 |

**上游佐证**：Buddy2api v2.1.14（2026-09-21，最新）**qwenwork 推理链路自 v2.1.9 以来零改动**；
其仓库 Issue **#82「[Bug]: QwenWork上游返回异常」**（2026-09-21）报告**完全相同的
`Model catalog unavailable` 错误**，作者暂无回复、无修复。

**结论**：上游网关在签名校验之后、模型路由之前新增了服务端策略（模型目录查询失败/
非官方客户端识别/账号级推理闸门），错误统一收敛为 503 Model catalog unavailable。
**客户端侧（wild-work/Buddy2api）当前无可修之处**——请求构造与官方可见行为已无差异。

**待观察**：
1. Issue #82 的后续回复（作者或其它用户可能发现新差异）；
2. 用 mitmproxy 抓官方客户端推理报文与我们的逐字节 diff（备忘 §7 方法）——若客户端
   请求形态也没变化，则纯粹是服务端针对 OAuth device token 的推理闸门，只能等上游放开
   或寻找新的鉴权链（如复用客户端内 qoderclicn 的本地签名通道）；
3. 账号维度：Free 套餐 + OAuth token 组合可能被降级，付费账号是否受影响待观察。

**渠道状态**：保持接入（代码无需回滚），账号面板/余额/费率仍正常；推理恢复时间取决于上游。
