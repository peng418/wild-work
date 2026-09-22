# wild-work v2.2.2 — 会话自愈安全网 + 国际版登录适配 + macOS .app 打包

> 本版重点解决两类问题：**坏会话把整条对话搞死**（客户端工具执行失败后，
> 每条消息都被上游 400 拒绝）与 **region=global 实例的登录/费率接口适配**。
> 另外带来 macOS 可双击的 .app 打包形态。

## 新增

### 会话自愈：孤儿 tool_call / tool 结果对称裁剪（重要）

**现象**：客户端（Codex / Claude Code 等）工具执行失败时，会把 `tool_calls`
持久化进历史却写不回对应结果。此后这条会话的**每一条**新消息都会被上游以
HTTP 400（`11148 tool_call_sequence_broken`）拒绝——对话报废，只能新建会话。

**修复**：请求送出前做双侧对称安全网处理：

- `repackToolResultBlocks`：把插在 tool 结果中间的非 tool 消息（如 Codex 的
  `image_resize_notice`）挪到结果块之后，修复「被判定断裂」的假阳性；
- `cleanupOrphanToolCalls`：按 id 双侧对称裁剪——无法配对的 `tool_calls` 与
  无法配对的 tool 结果一并剔除。旧的「整批齐才保留」策略会造出
  「无 tool_calls 的 assistant + 孤儿 tool」半截配对，照样 400，已弃用；
- 带 13 个回归用例（含真实会话卡死形态）。

**已知限制**：此安全网目前挂在 workbuddy(CN) 渠道的预处理管线；
workbuddyai / traework 渠道有各自的预处理入口，尚未接入，后续评估下沉共用层。

### normalizeRoles 兼容 role 变体

`developer` 角色归一为 `system` 的判定从精确匹配放宽为忽略大小写/首尾空白
（`"Developer"`、`" developer "` 等变体不再漏网，漏网会命中上游 role
白名单校验 400）。

### 面板登录支持国际版 WorkBuddy 账号（yanghuan，PR #20）

`region=global` 的实例此前点「+ WorkBuddy」仍打开国内版登录页，保存的账号
domain 为 CN，不会被国际版实例加载。现登录流程按 `region` 配置选择端点：

- 新增 `login.Endpoints` / `EndpointsForRegion`，CN 与 global 各自的
  API 基址与 Origin/Referer；
- `Start` / `Poll` / `ResolveAuthURL` 透传端点；
- 国际版 token 响应缺 `domain` 字段时按区域兜底，避免账号被误判为 CN。

### 国际版模型/费率接口路径适配（yanghuan，PR #20）

国际版网关下 `GET /console/enterprises/personal/models` 返回 500，正确路径为
`/v2/enterprises/personal/models`（实测 200，含 credits 定价）。现按账号
region 自动选择路径，CN 保持原路径不变；同时修正 `FetchModels`（动态模型
列表）与 `FetchModelPricing`（费率）两条链路。

### macOS：可双击的 .app 打包（geekxi，PR #25）

- 新增 `build/macos/build-app.sh`：把 `dist/wild-work-darwin-<arch>` 打包成
  `WildWork.app` 并生成 `.dmg`，图标由仓库内 `build/appicon.png` 派生，
  全程 ad-hoc 签名；
- 修复菜单栏（托盘）无图标问题；
- `.app` 形态下数据目录自动改用 `~/Library/Application Support/WildWork`
  （bundle 内容对普通用户只读）；`WILDWORK_HOME` 显式指定时优先。

## 内部改动

- 出站请求体脱敏（剥离上游内容审核黑名单指纹）与 SSE 出站逐帧规范化实现
  从上游参考项目 ref/workbuddy2api 整体拉齐（PR #23 / #24），
  本仓库与参考实现保持同口径，便于后续同步上游修复；
- `max_completion_tokens → max_tokens` 翻译规则注释拉齐（行为不变：
  显式 `max_tokens` 优先、非正数值不翻译、翻译后删别名）；
- 请求 payload 预处理管线顺序：stream 强制 → tokens 别名翻译 →
  stream_options 补全 → tool_choice 归一 → roles 归一 →
  tool 配对安全网 → 出站脱敏 → Marshal。

## 升级说明

- 直接替换二进制即可，`data/` 目录无需任何处理（v2.2.1 的 state 格式向后兼容）；
- macOS 用户可改用 `.app` 形态：首次启动数据目录为
  `~/Library/Application Support/WildWork`，如需沿用旧目录，
  设置环境变量 `WILDWORK_HOME` 指向原目录后启动。
