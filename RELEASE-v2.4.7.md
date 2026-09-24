# wild-work v2.4.7 — 不再静默失败：修复「上游把错误包在 HTTP 200 信封里」的两处吞错

> **定版说明**：本版为 wild-work 的收官版本（后续以新项目重做，见下一节）。
> 本版只做两件事，都是「把已经发生的失败诚实地暴露出来」，不改任何渠道协议。
> 另：本次提交一并把此前未入库的 UI 统一改版（已随 2.4.6 交付给用户）纳入版本控制。

## 背景

千问办公（`qwenwork/*`）渠道自 2026-09-21 起，上游网关在**签名校验之后、模型路由之前**
加了服务端闸门，推理一律返回 **HTTP 200 + 信封错误**：

```
data:{"headers":{...},"body":"{\"code\":\"503\",\"message\":\"Model catalog unavailable\"}",
     "statusCodeValue":503,"statusCode":"Service Unavailable"}
```

上游作者已在 `rockswang/wild-work` **Issue #31** 确认（"估计千问办公改版了"），客户端侧无可修之处。
但 wild-work 自身有两处**吞错**，把这类失败伪装成了正常，导致排查方向被带偏——本版修掉这两处。

## 修复

### 1. 流式分支丢弃上游错误（`internal/server/handler.go`）

原实现：

```go
if peek.Stream {
    _ = rt.Upstream.Stream(w, rc, clientModel)   // ← 上游错误被丢弃
    return
}
```

`Stream()` 会先写好 `Content-Type: text/event-stream` 等响应头，解析器在读到错误信封**第一行**时即返回
error；错误被丢弃后 handler 直接 return，Go 便补一个 **HTTP 200 + `Content-Length: 0`** 的空包。

后果：流式客户端（Studio / Claude Code / 各 OpenAI SDK）拿到空流即认为"没有内容"——
**既不回复也不报错**，肉眼完全看不出上游在报错。非流式才会输出真实错误。

现在：新增 `streamProbe` 包装 `ResponseWriter`，记录是否已写出 body / 状态行；若出错且尚未写出任何
内容，则用 `writeOpenAIError` 返回 `502 upstream_parse` + 上游原话，并落一条
`stream failed platform=... wrote=false：<上游错误>` 日志。

### 2. 面板测速把失败判为可用（`internal/app/app.go` `testOneModel`）

原实现只判 `resp.StatusCode == 200` 就返回 `Status:"ok"`，于是**空流**和**信封错误**都显示为绿色。
2026-09-24 11:47 的全量测速里，已废的千问办公三个模型全部被标成 `ok ≤ 667ms`。

现在：必须收到首个 `content`/`reasoning_content` delta 才算 `ok`；空流、只回 `[DONE]`、
错误帧三种情况分别记为 `error` 并附上区分得开的原因文案。

## 验证证据（本机沙箱实例，QoderCN + 千问办公两套真实凭证）

| 测试 | 修复前 | 修复后 |
|---|---|---|
| 流式 `qwenwork/pro` | HTTP 200 + **0 字节** | HTTP 502 + `upstream 503 "Model catalog unavailable"`，日志 `stream failed ... wrote=false` |
| 面板测速 · qwenwork ×3 | 全 `ok`（假绿） | 全 `error` + 上游原因 |
| 面板测速 · qodercn ×14 | ok | **全 ok（647–5584ms）**，无回归 |

## 已知问题（不在本版解决）

- **千问办公**：上游闸门，见 Issues #31 / 上游逆向社区跟进前不可用。本版起它会明确报错而非静默。
- **QoderCOM**：账号 0 积分，上游回 `code 112` + pricing 链接。
- **WorkBuddyAI**：额度耗尽，`429 Credits exhausted`。
- **TraeWork**：账号可用，但部分模型（如 `deepseek-v4.1-flash`）会长时间挂死，建议按模型健康度过滤。

## 定版与后续

本版交付物：`wildwork-2.4.7.fpk`（fnOS，安装路径见 README）。
wild-work 到此冻结，不再加功能；下一代把可用的渠道与功能体系重做（需求/设计方案/UI 原型另立项目）。
