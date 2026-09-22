# wild-work v2.4.1 — Qoder 体系重构：新增 QoderCN / QoderCOM 双渠道，旧 Qoder 界面下线

> 本版围绕 Qoder 渠道体系做了一次彻底重整：**新增 QoderCN（国内）与 QoderCOM（国际）两个独立渠道**，
> 并把上一代 Qoder（QoderWork，`qoder/*`）从界面上线撤下（代码保留、路由仍可用，平滑过渡）。
> 另外包含用量归属请求头注入、对话活跃上报、渠道更名等一批改进。
> 同时支持每日自动签到（**每日 +100 Credits**，实测已到账）。

## 新增

### QoderCN 渠道（`qodercn/*`）

基于 qoder2api（CN 分支）的抓包参数全新实现的独立渠道，与旧 Qoder 渠道并存：

- **OAuth 设备流登录**（PKCE+S256，client_id `e883ade2-...`），面板点「＋ QoderCN」弹出浏览器授权，回调自动绑号；
- **每日签到领 100 Credits**：campaigns 活动主路径（`act-YYYYMMDD-xxx` 动态匹配）+ daily-check-in 兜底双路径；
  幂等语义（409/CLAIMED/已领/无活动均视为成功），401 自动刷新重试；实测领取成功到账；
- **动态模型表**（含 `context_config` 上下文窗口解析、`is_vl`/`is_reasoning` 能力位），
  上次成功结果作进程内缓存，无静态兜底（模型名映射走本项目统一的兼容层机制）；
- 实测 `12345×54321` 算术题各模型推理全对（glm-5.3 思考 582 tokens）——思考链路工作正常；
- 紫色渠道主题；多轮对话、流式/聚合、余额分桶（套餐 + 赠送额度）全链路验证通过。

### QoderCOM 渠道（`qodercom/*`，国际版）

Qoder 国际版（`qoder.com` / `openapi.qoder.sh`），凭据与 CN 区完全隔离（已实测双向 401）：

- 复用 QoderCN 的协议实现（COSY 签名/QoderEncoding/嵌套 SSE 同源），**三域名分离**：
  业务 `openapi.qoder.sh`、推理 `api1.qoder.sh`、模型表 `api2.qoder.sh`；
- 签到仅 campaigns 活动路径（国际版无 legacy daily-check-in 系统，实测 404）；
- 橙金色渠道主题；GitHub SSO 账号实测：多轮对话、模型列表、签到幂等、余额分桶（300 套餐 + 100 赠送）全部通过。

### 请求头注入（WorkBuddyAI）

- 用量归属头：`X-Agent-Purpose: conversation`、`X-IDE-Name/Type: WorkBuddy`（对齐官方 IDE 形态）；
- 会话头族：`X-Conversation-Request-ID` / `X-Conversation-Message-ID` / `X-Request-ID` / `X-Root-Request-ID`（每请求生成）。

### 对话活跃上报（WorkBuddyAI）

新增 `ReportChatActivity`：一条 `chat_request_send` 事件（`/v2/report`）即可点亮
growth 连登与领养前置任务，无需真实对话，风控口径每号每天 1 次。

## 变更

### 渠道界面重整

- 「＋ WorkBuddy」→「＋ **WorkBuddyCN**」；「＋ WorkBuddy 国际版」→「＋ **WorkBuddyAI**」（显示名同步）；
- **旧 Qoder（`qoder/*`）从界面撤下**：删除添加入口，不再出现在渠道引导/兼容层预设中；
  代码与路由保留，存量账号照常显示与使用，可平滑迁移到 QoderCN；
- 添加按钮布局固化为：行1 `WorkBuddyCN | QoderCN | TraeWork`，行2 `WorkBuddyAI | QoderCOM | 千问办公`；
- QoderCN 紫色（`#6d28d9`）、QoderCOM 橙金（`#b45309`）主题贯穿徽标/按钮/费率表。

### Qoder 系签到定时

QoderCN / QoderCOM 自动签到固定每日 **10:15**（上游活动 10:00 UTC+8 刷新后的安全时点），
不再跟随全局签到时间配置。

## 修复

- QoderCN 登录后昵称不回填：设备流响应不含 nickname，改为登录后从 `/api/v1/userinfo`
  拉取显示名并写回凭据文件（修复首次绑定后面板显示 UID 的问题）；
- auth 凭据 glob 边界：`qoder*.json` 会吞掉 `qodercn-`/`qodercom-` 前缀文件，
  旧渠道加载器已显式排除，避免账号双计。

## 升级说明

- 旧 `qoder/*` 模型映射仍可用（后端保留），建议尽快切换到 `qodercn/*`；
- QoderCN/QoderCOM 凭据文件分别为 `auths/qodercn-<uid>.json`、`auths/qodercom-<uid>.json`，
  与旧渠道互不兼容（上游凭据区隔离，CN 的 dt- 在国际端点返回 401）；
- 若从 v2.3.x 升级：配置文件无破坏性变更，直接替换二进制即可。
