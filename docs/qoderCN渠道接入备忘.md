# QoderCN 渠道接入备忘

> 日期：2026-09-21
> 状态：已实现（渠道 `qodercn`，版本 2.3.1+）

## 1. 渠道定位

QoderCN 是**独立渠道**（`provider.QoderCN`，模型前缀 `qodercn/<model>`），与现有
Qoder（`provider.Qoder`，QoderWork 产品线，移植自 qoderwork2api）**并存，互不影响**。
两者上游域名相同（`openapi.qoder.com.cn` + `gateway.qoder.com.cn`），但协议参数不同。
稳定运行后可评估替换旧 `qoder` 渠道（另行决议）。

## 2. 与旧 qoder（QoderWork）渠道的协议差异（来源 ref/qoder2api）

| 项 | qoder（QoderWork） | qodercn（QoderCN） |
|---|---|---|
| cosyVersion | `0.1.43` | `1.0.10` |
| COSY 头 | 15 头（含 cosy-clientip） | 18 头：**多** `cosy-scene:assistant`、`cosy-business-product:ide`、`cosy-business-type:agent`、`login-version:v2`；**无** cosy-clientip；data-policy 小写 `agree` |
| identity.user_type | 硬编码 `personal_professional_trial` | `/api/v1/userinfo` **实测**（登录后 FetchUserInfo 填充，缺省 `personal_standard`） |
| 请求体 session_type | `qodercli` | `qoder` |
| 请求体模板 | 精简 | qoder2api baseprompt.json 全字段：`is_retry`/`code_language`/`source:1`/`version:"3"`/`chat_prompt`/`task_id:common`/`parameters.max_tokens`(默认 32768)/`model_config` 全字段 |
| OAuth client_id | `1c5e33e1-...`（带 redirect `qoder-work-cn://`） | `e883ade2-...`（qoder2api CN/Global 共用，无 redirect，纯轮询） |
| 模型表 | 动态 + 静态兜底表 | **动态 + 上次成功缓存（无静态兜底）** |
| 模型场景解析 | 仅 `chat` | `assistant`→`developer`→`chat` 三级回退 |
| 上下文窗口 | max_input_tokens | `context_config.token_count` 优先 → max_input_tokens 回退 |
| 签到 | 无 | **双路径**（campaigns 主 + daily-check-in 兜底，见 §4） |
| accept 头 | 恒 text/event-stream | 按请求类型（GET=application/json / SSE=text/event-stream） |

## 3. 代码地图

| 文件 | 说明 |
|---|---|
| `internal/qodercn/` | 渠道实现（constants/cosy/encoding/body/models/sse/client/checkin） |
| `internal/login_qodercn/` | OAuth 设备流登录（client_id `e883ade2-...`，无 redirect_uri） |
| `internal/auth/auth.go` | `LoadQoderCNDir`（glob `qodercn-*.json`）；`LoadQoderDir` 已排除该前缀防双计 |
| `internal/provider/provider.go` | `QoderCN Kind = "qodercn"` |
| `cmd/wild-work/main.go` | 装配：`state-qodercn.json` / `qcnPool` / `qcnUp` / `qcnSch`（签到时段=全局） |
| `internal/app/app.go` | `StartLoginFor` / `Poll` 分支 / `completeQoderCNLogin` / reload / noExplicitCheckin 放行 / fees |
| `cmd/wild-work/web/` | 「＋ QoderCN」按钮、CH_LABEL/CH_CLASS、CHANNEL_PRESETS |

凭据文件：`auths/qodercn-<uid>.json`（嵌套形，domain=qoder.com.cn，含机器指纹）。

## 4. 签到实现（qoder2api 双路径，实测修正优先顺序）

**2026-09-21 实测**：`daily-check-in` legacy 系统已全局 DISABLED（status=DISABLED，streak 恒 0）；
实际生效的是 **campaigns 活动路径**（key 形如 `act-20260920-549`，每日变化，不可硬编码）。
实现顺序：**campaigns 优先**，不可用（404/非 200）时回退 daily-check-in。

- 认证：`Bearer dt-` + **`cosy-clienttype: 10`（桌面端标识，★ 不可用推理链路的 5）**，无 COSY 签名
- 匹配条件：`actionType=="CLAIM_BENEFIT" && claimStatus=="CLAIMABLE"`
- 幂等：409 / `replayed:true` / CLAIMED / DISABLED / 无活动 → 均视为成功（`errAlready` 文案「已签到」）
- 401 → 透传 ErrSessionDead，scheduler 现有自愈（refresh+落盘+重试）接管
- 每日 10:00 (UTC+8) 刷新；奖励 100 Credits，30 天有效；落账在 `addOnQuota`（赠送额度池）

## 5. 模型策略（按用户决议）

1. **无静态兜底表**：`StaticModels()` 返回 nil。
2. **上次成功缓存**：`fetchModels` 成功后 `setCache()`（内存态：模型表 + 客户端名→key 映射）；
   拉取失败回退缓存，连缓存都没有才报错。
3. **不引入 qoder2api 的 MapModel 模糊映射**：客户端模型名映射由本项目统一的
   渠道抽象映射机制（compat model_map）承担；`modelKey` 未命中时原样透传。
4. 能力解析补齐 qoder2api：`is_default`、`context_config.token_count`（ContextWindow 优先源）。

## 6. 已验证

- `go build ./... && go vet ./... && go test ./...` 全绿
- `internal/qodercn/qodercn_test.go`：编码回路、场景三级回退、缓存语义（无静态兜底）、
  上下文窗口优先级、签到 5 场景（领取/已领/回退/DISABLED/401 透传）、请求体模板形态、429 优先分类
- `dist/wild-work.exe` 已重建，二进制含 qodercn 标识

## 7. 已知限制 / 后续

- 登录采用纯轮询式设备流（无 redirect scheme），与 qoder 渠道并存时二选一操作，面板 UI 已区分。
- 首次启动在拉到模型前 `/v1/models` 该渠道为空（无静态兜底的代价）；登录后会立即 afterAccountAdded→RefreshPricing 预热。
- qoder2api 的 PAT→jobToken 交换未引入（如需粘贴 PAT 登录再评估）。
- 稳定运行后是否替换旧 `qoder` 渠道：另行决议（决议前两条渠道并存）。
