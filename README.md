# wild-work

> 多渠道账号聚合桌面工具——把 WorkBuddy(CodeBuddy) 国内版/国际版、TraeWork、QoderCN、QoderCOM（国际版）、千问办公的多个账号聚合成一个 OpenAI 兼容 API，双击启动，浏览器管理。

[![GitHub](https://img.shields.io/badge/GitHub-rockswang%2Fworkbuddy--wild-blue)](https://github.com/rockswang/workbuddy-wild)

## 功能

- **三接口协议兼容**：`/v1/chat/completions`（OpenAI Chat）+ `/v1/responses`（OpenAI Responses）+ `/v1/messages`（Anthropic Messages，含 `count_tokens`），可直接接入 `codex` CLI 与 `claude` CLI
- **请求体指纹脱敏**：自动清除 Claude Code / Codex CLI 注入的模板句，防止上游 11128 内容拦截
- **OpenAI 兼容代理**：`/v1/chat/completions`、`/v1/models`，支持流式/非流式，模型前缀路由
- **错误分类精细化**：区分「请求问题」与「账号问题」——内容拦截/上下文超限不罚号，限流/风控/账号故障分级冷却，429 不再误判余额耗尽
- **六渠道聚合**：WorkBuddyCN(CodeBuddy) + WorkBuddyAI（国际版） + TraeWork + QoderCN + QoderCOM（国际版） + 千问办公，模型前缀路由，粘性路由优先复用账号以提升会话缓存利用率，临期积分优先消耗；旧 Qoder（`qoder/*`）路由保留但已从界面下线
- **自动签到**：每日定时签到领额度，token 保活，冷却状态机
- **自动领日活奖励**（WorkBuddy 国际版）：定时自动用免费模型对话保活，自动领取每日活跃奖励，无需手动签到
- **Web 管理面板**：账号管理（添加/签到/刷新/停用/删除）、积分明细、模型列表和费率、API 配置
- **系统托盘**：常驻右下角，双击打开面板，右键菜单操作
- **跨平台**：Windows（完整支持）、macOS（代码已就绪，CI 构建）、Linux（无头模式）
- **developer→system 角色转换**：自动将下游 Agent 发送的 `<developer>` 角色改写为 `<system>`，避免上游触发内容过滤
- **上游协议头仿真**：出站自动注入官方客户端指纹头（`X-CodeBuddy-Request`、`X-Machine-ID` 等），降低风控误判风险

## 项目渊源

本项目最初算法来源于 [Sliverkiss/](https://github.com/Sliverkiss/) 大佬的 xxx2api 系列项目，本项目针对多渠道进行了聚合，针对 Windows 环境进行了适配，降低了使用门槛，并提供跨平台 Web 管理界面。

## 使用方式

### Windows

1. 从 [Releases](https://github.com/rockswang/workbuddy-wild/releases) 下载 `wild-work.exe`
2. 放到任意目录，双击启动
3. 右下角出现 W 图标，**双击托盘图标** → 浏览器打开 Web 管理面板
4. 在面板中点击「+ WorkBuddy」/「+ WorkBuddy 国际版」/「+ TraeWork」/「+ Qoder」/「+ 千问办公」添加账号
5. 根据下方配置说明接入你的 AI 客户端

### 托盘菜单

- **打开主界面**：在浏览器中打开管理面板
- **查看日志**：用记事本打开运行日志
- **退出**：退出程序

### 无头模式（Linux 服务器）

```bash
./wild-work --no-tray
```

启动后打印 API 地址、Key 等信息，阻塞运行，Ctrl+C 退出。

## 配置 AI 客户端

### 1. 获取模型列表

wild-work 的 `/v1/models` 端点返回当前所有可用模型。在终端中执行：

```bash
curl -s -H "Authorization: Bearer WildWorkAPI" http://127.0.0.1:7863/v1/models
```

### 2. 配置 Pi（models.json）

Pi 不支持自动拉取模型列表，需要手动编辑 `~/.pi/agent/models.json`（Windows 路径 `C:\Users\<用户名>\.pi\agent\models.json`），在 `providers` 中加入 wild-work 配置：

```json
{
  "providers": {
    "wild-work": {
      "name": "wild-work",
      "api": "openai-completions",
      "baseUrl": "http://127.0.0.1:7863/v1",
      "apiKey": "WildWorkAPI",
      "models": [
        {
          "id": "workbuddy/auto",
          "name": "自动路由 (WorkBuddy)",
          "reasoning": false,
          "input": ["text"],
          "contextWindow": 168000,
          "maxTokens": 32000,
          "compat": { "maxTokensField": "max_tokens" }
        },
        {
          "id": "workbuddy/deepseek-v4-pro",
          "name": "DeepSeek V4 Pro (WorkBuddy)",
          "reasoning": true,
          "input": ["text"],
          "contextWindow": 1000000,
          "maxTokens": 50000,
          "compat": {
            "thinkingFormat": "deepseek",
            "supportsReasoningEffort": true,
            "maxTokensField": "max_tokens"
          }
        },
        {
          "id": "workbuddy/glm-5.3",
          "name": "GLM-5.3 (WorkBuddy)",
          "reasoning": true,
          "input": ["text"],
          "contextWindow": 1000000,
          "maxTokens": 48000,
          "compat": { "maxTokensField": "max_tokens" }
        },
        {
          "id": "traework/DeepSeek-V4-Pro",
          "name": "DeepSeek V4 Pro (TraeWork)",
          "reasoning": true,
          "input": ["text"],
          "contextWindow": 1000000,
          "maxTokens": 50000,
          "compat": {
            "thinkingFormat": "deepseek",
            "supportsReasoningEffort": true,
            "maxTokensField": "max_tokens"
          }
        },
        {
          "id": "traework/glm-5.2",
          "name": "GLM-5.2 (TraeWork)",
          "reasoning": true,
          "input": ["text"],
          "contextWindow": 1000000,
          "maxTokens": 48000,
          "compat": { "maxTokensField": "max_tokens" }
        },
        {
          "id": "workbuddyai/deepseek-v4.1-flash",
          "name": "DeepSeek V4.1 Flash (WorkBuddy 国际版)",
          "reasoning": true,
          "input": ["text", "image"],
          "contextWindow": 1000000,
          "maxTokens": 128000,
          "compat": {
            "thinkingFormat": "deepseek",
            "supportsReasoningEffort": true,
            "maxTokensField": "max_tokens"
          }
        }
      ]
    }
  }
}
```

> 上面只列出了部分常用模型，完整列表请通过 `/v1/models` 端点获取后自行添加。

### 3. 其他客户端

支持 OpenAI 兼容 API 的客户端均可接入：

```
Base URL: http://127.0.0.1:7863/v1
API Key:  WildWorkAPI
```

模型 ID 需带渠道前缀：`workbuddy/<model>`、`workbuddyai/<model>`、`traework/<model>`、`qodercn/<model>`、`qodercom/<model>`、`qwenwork/<model>`（旧 `qoder/<model>` 仍可用，已从界面下线）。

### 4. 三种接口协议

除 OpenAI Chat Completions 外，同一端口还兼容 **OpenAI Responses** 与 **Anthropic Messages**，
可用官方 CLI 直接接入（无需改代码）：

| 端点 | 协议 | 适用客户端 |
|------|------|-----------|
| `POST /v1/chat/completions` | OpenAI Chat | 大多数第三方客户端、`openai` SDK |
| `POST /v1/responses` | OpenAI Responses | `codex` CLI、`openai` SDK 的 Responses API |
| `POST /v1/messages` | Anthropic Messages | `claude` CLI、`anthropic` SDK |
| `POST /v1/messages/count_tokens` | Anthropic count_tokens | Claude Code 上下文预算用 |

鉴权：OpenAI 侧用 `Authorization: Bearer <API Key>`；Anthropic 侧 `x-api-key: <API Key>`
或 `Authorization: Bearer` 均可。

**模型名可省略渠道前缀**，在 `config.json` 的 `compat` 段配置映射：

```json
"compat": {
  "default_channel": "workbuddy",
  "max_tokens_cap": 32000,
  "model_map": {
    "claude-*": "workbuddy/glm-5.2",
    "gpt-5*": "workbuddy/deepseek-v4-pro"
  }
}
```

- 带 `channel/` 前缀的模型名始终优先（如 `workbuddy/hy3`），行为与旧版一致
- 无前缀时依次尝试：`model_map` 精确匹配 → 通配匹配（key 以 `*` 结尾）→ `default_channel`
- `max_tokens_cap` 用于封顶客户端的 `max_tokens`（Anthropic 客户端常发 64000，
  超出部分上游会直接 400）；设 `0` 表示不限制

**Codex CLI 配置示例**（`~/.codex/config.toml`）：

```toml
model = "traework/glm-5.2"
model_provider = "wildwork"

[model_providers.wildwork]
name = "wildwork"
base_url = "http://127.0.0.1:7863/v1"
wire_api = "responses"
env_key = "WILDWORK_KEY"
```

**Claude Code 配置示例**（环境变量）：

```bash
export ANTHROPIC_BASE_URL="http://127.0.0.1:7863"
export ANTHROPIC_AUTH_TOKEN="WildWorkAPI"
export ANTHROPIC_MODEL="traework/glm-5.2"
```

> ⚠️ **已知限制**：Responses 接口是无状态实现，不支持 `previous_response_id`
> （会返回 400 并提示）。客户端需在 `input` 中携带完整历史（Codex CLI 默认如此）。
> 另外 WorkBuddy 国内版/国际版上游对某些 agent 系统提示词（如 Codex CLI 自带的那份）
> 会触发内容策略拦截（上游返回 `code=11128 Illegal API invocation from an unapproved channel`），
> 实测 `traework/*`、`qoder/*`、部分 `workbuddy/*` 模型不受影响；如遇拦截请更换渠道模型。

## 面板操作指南

所有操作在 Web 管理面板中完成，按直觉操作即可：

- **添加账号**：点击渠道按钮 → 确认对话框 → 浏览器窗口登录 → 自动完成
- **账号管理**：卡片显示积分、签到状态（WorkBuddy 国际版显示「自动领日活奖励」）；图标按钮操作（签到 ✓ / 刷新 ↻ / 停用 ⏸ / 删除 ✕）
  - 积分显示为「可用积分」；若该账号还有本工具用不了的额度（如 TraeWork 官方客户端专用池），会追加显示 `/ N 不可用`
- **积分明细**：鼠标悬停积分数字显示套餐明细（含有效期与可用/不可用小计），条目多时用底部 `‹ ›` 翻页
- **刷新积分**：面板顶部按钮，批量刷新全部账号余额
- **模型列表和费率**：点击「刷新」从上游拉取最新模型定价；上游未返回定价的模型显示 `unknown`，免费模型高亮为 `Free`
- **API 配置**：点击页面顶部 API 地址或 Key 修改

## 常见问题

### 1. Windows 提示“未知发布者”或报毒？

本工具没有数字签名，因此可能会被 Windows 安全中心或杀毒软件拦截。如果报毒，请将本工具添加到排除项，方法如下：

>设置 → 更新和安全 → Windows 安全中心 → 打开 Windows 安全中心 → 病毒和威胁防护 → “病毒和威胁防护”设置/管理设置 → 排除项/添加或删除排除项 → 添加排除项（文件或文件夹），把 exe 或所在目录加进去。

如果对本仓库发布包不放心，可以克隆到本地让 Agent 帮你审查一遍，然后自行基于源码构建（见 [DEVELOPMENT.md](DEVELOPMENT.md)）。

### 2. TraeWork 积分里的「不可用」是什么？

TraeWork 的额度分两个池，由上游 `available_endpoint` 字段区分：

- `ep=0`（通用池）：本工具能消耗，显示为**可用积分**
- `ep=1`（官方客户端专用池）：只有 Trae 官方客户端能用，本工具消耗不了，显示为**不可用**

所以 TraeWork 卡片会出现类似 `1828可用积分/4400不可用` 的显示——后者再多也帮不上 API 转发。
本工具的可消耗余额只算 `ep=0`，不会因为专用池额度高而误判账号可用。

> 注：早期版本把所有额度混成一个数字（且用 `group_type` 误判可用性），会让 TraeWork 账号看起来余额充足；
> v2.2.1 起修正为按 `available_endpoint` 拆分统计。

### 3. TraeWork DeepSeek V4 Flash 模型响应慢

在官方客户端里这个模型也会排队，我的办法是 `ds4f` 用 WorkBuddy 的，`ds4p` 用 TraeWork 的。

### 4. 如何绑定多个 WorkBuddy 国际版账号？

添加新账号时，因为默认使用当前已登录的 github/Google 等账号自动登录，因此无法快速添加新的账号。
这里给出我的方案，在浏览器右上角菜单选择“新建无痕窗口”，在窗口地址栏输入`http://127.0.0.1:7863`打开 WildWork 主界面，即可在干净环境内新增账号。
也可以打开开发者控制台，单独清除 github.com 和 workbuddy.ai 的 Cookies。

### 5. WorkBuddy 国际版登录后积分显示为0，刷新积分报错
注册 WorkBuddy 国际版新账号时，需要选择地区后奖励积分才发放。

## 交流群

微信扫码加入「野活儿老白蹬之家」，有问题欢迎在群里反馈：

<img src="wechat_group.jpg" alt="野活儿老白蹬之家 微信群二维码" width="320">

## License

MIT — 仅供个人学习使用，请遵守各上游平台服务条款。
