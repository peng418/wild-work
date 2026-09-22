# qoder2api 端点覆盖度对比（对照本项目 qoder 渠道）

> 目的：确认 qoder2api 是否包含本项目渠道所需的**全部**端点（模型列表/模型能力/用户余额/登录授权等）。
> 结论：**端点层面几乎完全覆盖，且 qoder2api 更全**（多出 PAT 交换、jobToken、国际区、CSRF 探测）；
> 但有几个本项目**已实现而 qoder2api 未实现**的能力（详见 §3）。

---

## 1. 本项目 qoder 渠道当前使用的端点

| 端点 | 域名 | 用途 | 本项目是否使用 |
|------|------|------|----------------|
| `POST /api/v1/deviceToken/poll` | `openapi.qoder.com.cn` | OAuth 设备流换 dt-/drt- | ✅ login_qoder |
| `POST /api/v1/deviceToken/refresh` | 同上 | dt- 轮换 | ✅ RefreshToken |
| `GET /api/v1/userinfo` | 同上 | 账号信息 | ❌ 定义了 `EpUserInfo` 但**未使用** |
| `GET /api/v2/user/plan` | 同上 | 套餐名 | ❌ 定义了 `EpPlan` 但**未使用** |
| `GET /api/v2/quota/usage` | 同上 | 余额 | ✅ UserResource / UserResourceDetail |
| `GET /algo/api/v2/model/list?Encode=1` | `gateway.qoder.com.cn` | 模型列表+能力 | ✅ FetchModels |
| `POST /algo/api/v2/service/pro/sse/agent_chat_generation?...` | 同上 | 推理 SSE | ✅ ChatStream |
| `GET /sash/api/v1/me/daily-check-in/status` | `openapi.qoder.com.cn` | 签到状态 | ❌ 定义了常量但未用 |
| `POST /sash/api/v1/me/daily-check-in/claim` | 同上 | 签到领取 | ❌ 同上 |
| —— | —— | 活动 `GET /sash/api/v1/me/campaigns` | ❌ **未定义**（实测当前主路径） |

---

## 2. qoder2api 端点全集（CN + Global）

| 端点 | qoder2api 字段 | 用途 | 本项目对应 |
|------|---------------|------|-----------|
| `/device/selectAccounts`（页面） | `DeviceLoginBase` | OAuth 授权页 | ✅ login_qoder（URL 拼接） |
| `/api/v1/deviceToken/poll` | `PollEndpoint` | 换 dt-/drt- | ✅ |
| `/api/v1/userinfo` | `UserinfoBase` | 账号信息 | ⚠️ 定义未用 |
| `/api/v2/user/plan` | `PlanEndpoint` | 套餐 | ⚠️ 定义未用 |
| `/api/v2/quota/usage` | `QuotaEndpoint` | 余额 | ✅ |
| `/algo/api/v2/service/pro/sse/agent_chat_generation` | `ChatStreamURL` | 推理 SSE | ✅ |
| `/algo/api/v2/model/list?Encode=1` | `ModelListURL` | 模型列表 | ✅ |
| `/algo/api/v3/user/jobToken?Encode=1` | `JobTokenURL` | **PAT → session token 交换** | ❌ **本项目无** |
| `/sash/api/v1/me/daily-check-in/status` | （硬编码） | 签到状态 | ⚠️ 定义未用 |
| `/sash/api/v1/me/daily-check-in/claim` | （硬编码） | 签到领取 | ⚠️ 定义未用 |
| `/sash/api/v1/me/campaigns` | （硬编码） | **活动列表（当前主路径）** | ❌ **本项目无** |
| `/sash/api/v1/me/campaigns/{id}/claim` | （硬编码） | **活动领取（当前主路径）** | ❌ **本项目无** |
| `/api/v1/deviceToken/refresh` | —— | dt- 轮换 | ✅（qoder2api 走 `ExchangeJobToken` 统一处理） |

**qoder2api 独有**：
- `JobTokenURL`（PAT 交换）—— 本项目只支持 Device Flow OAuth，不支持 PAT
- 双区（`.sh` / `.com.cn`）—— 本项目仅 CN
- campaigns 两个端点 —— **签到当前主路径**
- `daily-check-in` 两个端点 —— 本项目已定义常量但实现为空

---

## 3. 模型能力（模型列表）覆盖度对比

两者都调 **同一个上游** `GET /algo/api/v2/model/list?Encode=1`（COSY 签名），但解析深度不同：

| 能力字段 | 上游字段 | qoder2api | 本项目 |
|----------|----------|-----------|--------|
| 模型 key | `key` | ✅ | ✅ |
| 显示名 | `display_name` | ✅ | ✅ |
| 是否启用 | `enable` | ✅ | ✅ |
| 是否默认 | `is_default` | ✅ | ❌ 未解析 |
| **思考模式** | `is_reasoning` | ✅ | ✅ `SupportsReasoning` |
| **视觉能力** | `is_vl` | ⚠️ 未解析 | ✅ `SupportsImages` |
| 最大输入 | `max_input_tokens` | ✅ | ✅ `ContextFromAPI` |
| **上下文窗口** | `context_config.*.token_count` | ✅ 优先读 | ⚠️ **仅回退用 max_input_tokens** |
| **最大输出** | 推导（reasoning 32768 / 否则 16384） | ✅ | ❌ 未设置 |
| 价格倍率 | `price_factor` | ✅ | ✅（另走 FetchModelPricing） |
| 场景分类 | `assistant`/`developer`/`chat` 多级回退 | ✅ 三级回退 | ⚠️ **仅 `chat`** |

### 3.1 本项目模型能力的两处可改进点

1. **`context_config.token_count` 未解析**：本项目 `internal/qoder/models.go:100` 直接用 `max_input_tokens` 作 ContextWindow。qoder2api 优先读嵌套的 `context_config`（默认档 `is_default`）的 `token_count`，仅在缺失时回退 `max_input_tokens`。
   → 若上游两者不等，本项目会显示偏小的上下文窗口。
2. **模型分类仅取 `chat`**：本项目 `internal/qoder/models.go:62` 只读 `apiResp["chat"]`，而 qoder2api 按 `assistant` → `developer` → `chat` 三级回退。
   → 上游若把模型挪到 `assistant` 场景，本项目会报 `no chat scene`。

> **注**：本项目采用 `chat` 场景是刻意的（对应 `AgentId=agent_common` 推理通道），但缺少回退会导致上游调整时硬失败（违反 AGENTS.md §「Fail Early」例外——此处更宜容忍回退）。

---

## 4. 用户余额 / 登录授权覆盖度

| 能力 | qoder2api | 本项目 | 结论 |
|------|-----------|--------|------|
| OAuth Device Flow（PKCE+S256） | ✅ | ✅ | 等价 |
| PAT 登录（明文 token） | ✅ `AddAccountByPAT` | ❌ | **qoder2api 更全** |
| jobToken 交换 | ✅ | ❌ | qoder2api 独有 |
| dt- 刷新（轮换） | ✅ | ✅ | 等价 |
| 余额（quota/usage） | ✅ | ✅ | 等价 |
| 余额分桶（userQuota + addOnQuota） | ✅ | ✅ | 等价（本项目 `赠送额度` 条目） |
| 套餐名（plan） | ✅ 展示 | ⚠️ 定义未用 | 本项目可选补 |
| 账号信息（userinfo） | ✅ 展示 | ⚠️ 定义未用 | 本项目可选补 |
| 签到 | ✅ 双路径 | ❌ | **qoder2api 更全**（本次要补） |
| 模型列表动态拉取 | ✅ | ✅ | 等价 |
| 多协议（OpenAI/Anthropic/Codex） | ✅ bridge 层 | 本项目另有 gateway 层 | 各自实现 |
| 双区（CN/Global） | ✅ | ❌ | qoder2api 更全 |
| 定时签到 + 自动刷新 + 解冻 | ✅ | ⚠️ 部分（有调度器无签到） | 本次补 |
| Web 控制台 | ✅ | 本项目另有 Web UI | 各自实现 |

---

## 5. 最终回答

### Q：qoder2api 是否包含本项目渠道的所有必要端点？

**是，且更全。** 逐项对照：

| 类别 | 是否覆盖 | 说明 |
|------|----------|------|
| **模型列表** | ✅ 完全覆盖 | 同一 `/algo/api/v2/model/list`，qoder2api 解析更深（多场景回退） |
| **模型能力** | ✅ 覆盖且更强 | qoder2api 额外解析 `context_config.token_count`、`max_output_tokens`、`is_default`；本项目独有 `is_vl` 视觉标记 |
| **用户余额** | ✅ 完全覆盖 | 同一 `/api/v2/quota/usage`，分桶口径一致 |
| **用户登录授权** | ✅ 覆盖且更强 | 同一 Device Flow；qoder2api **额外支持 PAT + jobToken 交换** |
| **推理** | ✅ 完全覆盖 | 同一 chat SSE 端点 + COSY 签名 + QoderEncoding |
| **签到** | ✅ 覆盖（本项目缺失） | qoder2api 有完整双路径；本项目常量已留但未实现 |
| **国际区** | ✅ qoder2api 独有 | 本项目仅 CN |

### 结论

1. **qoder2api 可以作为本项目 qoder 渠道的完整端点参考**，无需担心遗漏必要端点。
2. **qoder2api 在 3 处比本项目更全**，值得借鉴：
   - **签到双路径**（`daily-check-in` + `campaigns`）—— 本次改造核心
   - **PAT + jobToken 交换** —— 本项目仅有 OAuth，若想支持用户粘贴 PAT 可补
   - **模型分类三级回退 + `context_config` 解析** —— 提升健壮性与上下文窗口准确度
3. **本项目在 1 处比 qoder2api 更全**：`is_vl`（视觉能力）解析与 `SupportsImages` 暴露。
4. **国际区**（`.sh`）本项目不需要，若将来要做，应作为**独立渠道**（类比 workbuddy/workbuddyai），而非在 qoder 渠道内做 region 分支。

### 建议的补充改造项（除签到外）

| 优先级 | 改造 | 理由 |
|--------|------|------|
| 中 | `campaigns` 兜底 | 签到必备（见签到方案 §3.1） |
| 中 | 模型分类 `assistant`/`developer`/`chat` 三级回退 | 防上游调整导致硬失败 |
| 低 | `context_config.token_count` 优先解析 | 提升上下文窗口准确度 |
| 低 | 启用 `EpPlan` / `EpUserInfo` | 面板可展示套餐名与账号名 |
| 低 | PAT 登录 + jobToken | 提供 Device Flow 之外的备选登录方式 |
