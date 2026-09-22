# wild-work v2.2.0 — 稳健性大版

> 吸收上游 [Sliverkiss/workbuddy2api](https://github.com/Sliverkiss/workbuddy2api) 近期 100+ 提交中的关键修复，
> 聚焦「Codex / Claude Code 能跑通」「错误分类不误伤好号」「Token 竞态与连接半死不传染」三类问题。

## 新增功能

### Claude Code / Codex 请求体指纹脱敏 (`internal/sanitize/`)
- 出站前自动清除 Claude Code（`You are Claude Code...`、`x-anthropic-billing-header`、`Main branch (...)`、`github.com/anthropics/issues`、`11128` 裸码）与 Codex CLI（`Codex CLI, a terminal-based coding assistant.`）的模板指纹
- 覆盖 `messages[].content`（string / 多模态 array 的 `text` part）、`reasoning_content`、`tool_calls[].function.arguments` 三处
- 预检不命中时零分配原样通过，不影响正常请求；默认开启，未来可通过配置关闭（escape hatch）

### 模型多模态能力透传 (`/v1/models`)
- 上游目录的 `supportsImages`/`supportsReasoning`/`supportsToolCall` → `provider.ModelInfo` 能力字段 → `/v1/models` 输出 `architecture.input_modalities`（OpenRouter / llama.cpp 通行形状，`["text"]` 或 `["text","image"]`）
- WorkBuddy 国内版/国际版：解析上游目录接口的 `supportsImages` × `disabledMultimodal` 联合判定
- Qoder：`IsVL` → `SupportsImages`、`IsReasoning` → `SupportsReasoning`
- TraeWork：上游模型列表不返回能力字段，回退不声明
- 静态兜底表/extraModels：无上游数据，不声明任何能力；**原则是不自造数据、不入为纠正上游与实际不符的声明**
- Web UI 费率表：模型 ID 后增加 👁🧠🔧 能力图标 + tooltip；措辞用「上游未声明」而非「不支持」

### 错误分类精细化
- 新增 5 个 `ErrKind`：`ErrContentBlocked`（内容拦截，不罚号）、`ErrPromptTooLong`（上下文超限，请求级错误不轮转）、`ErrWafBlock`（WAF 403，账号软冷却）、`ErrAccountFault`（11140/14017，冷却轮换）、`ErrModelBlocked`（11102，后续可做模型级负缓存）
- 三渠道 `Classify` 全部修复：**429 判定移到 hardRule 之前**（限流 body 带 "quota exceeded" 不再误判余额耗尽 12h）
- 新增非 429 状态码软限流词表（`rate limit` / `too many requests` / `usage limit` / `请求过于频繁`）
- 账号级故障词表（`request illegal` / `trial not activated`）

### 出站协议头补齐
- `X-CodeBuddy-Request: 1` —— 官方客户端风控闸门头，所有 API 请求生效
- `Accept-Language` —— CN → `zh-CN`，global → `en-US`
- `X-Machine-ID` / `X-Session-ID` —— 按 `sha256("wb2a:"+purpose+":"+uid)` 稳定派生，跨重启固定
- `X-Auth-Refresh-Source: plugin` —— 对齐官方客户端 refresh 标识

### 请求体改写增强
- `max_completion_tokens → max_tokens` 翻译 —— 新客户端（o-series / DeepSeek Harness）只发别名时不再被上游忽略而输出截断
- `stream_options: {include_usage: true}` 缺省注入 —— 保障上游末帧返回 `usage`，客户端用量统计不再缺失

## Bug 修复

### Token 并发读写竞态
- `auth.Auth` 新增 `AccessTokenValue()` / `RefreshTokenValue()` 锁内快照
- 三渠道出站请求头全部改为锁快照读取，消除与 keepalive 刷新写回的 `-race` 数据竞争

### `Aggregate` SSE 聚合修复（5 项）
- 空 content 帧不再置 latch（阻止后续 message 帧合并）
- message 回退分支补 gotAnyContent 守卫（避免正文重复拼接）
- `tool_calls` 缺 index 时按「id 优先 / lastIdx 兜底 / 跳号分配」分派（不再一律归 0 导致数据污染）
- EOF / `length` 截断时丢弃残缺 `tool_calls.arguments`（不把非法 JSON 交给客户端）
- `usage.total_tokens` 缺时用 `prompt+completion` 合成补齐

### `Stream` 空流检测
- 上游返回 200 但 0 有效帧时不再合成空 content 假成功，改为报 `upstream_parse` 错误

### Transport 连接层加固
- **真禁 h2**：`TLSNextProto: make(map...)`（`ForceAttemptHTTP2=false` 实测无效）
- Dial 超时 10s + keepalive 15s + TLS 握手超时 10s + ResponseHeaderTimeout 60s
- IdleConnTimeout 从 90s 收到 30s
- WorkBuddy 国内版与国际版统一使用同一加固 Transport

### TraeWork 流式/聚合 model 回填
- `Aggregate` / `Stream` 拆分出 `WithModel` 变体，上游 SOLO 事件不带的模型名由调用方回填（对齐 R14）
- qoder 多模态能力也一并跟上（`IsVL → SupportsImages`、`IsReasoning → SupportsReasoning`）

### `truncate` 防 panic 守卫
- 补 `n<=0` 守卫（负数不再 panic）

## 交互体验

### Web UI
- 费率表加 👁🧠🔧 能力图标（图像输入 / 思考模式 / 工具调用）
- API 配置弹窗增加 API-Key 可见性切换按钮
- 新增 FAQ 常见问题说明
- 帮助面板新增微信群二维码

## 文档更新
- AGENTS.md：补 v2.2.0 渊源 + Classify 新不变量 + sanitize 逃生门
- DEVELOPMENT.md：补 `internal/sanitize/` 包说明 + Transport/Classify 新不变量
- README.md：功能描述补脱敏/错误分类/协议头仿真 + 版本号更新
- 新增完整 FAQ

---

**全量校验**：`go build ./... && go vet ./... && go test ./...` 全绿。

**构建**：`GOOS=windows CGO_ENABLED=0 go build -ldflags "-H windowsgui" -o dist/wild-work.exe ./cmd/wild-work`

**该版本无配置格式变更**：config.json 与 state-*.json 兼容 v2.0.x / v2.1.x。