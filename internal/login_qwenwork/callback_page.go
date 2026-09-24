// callback_page.go 统一的本机回调响应页（login_trae 与 login_qwenwork 共用视觉规范）。
//
// 行为契约（5 渠道统一后的约定，见备忘 §9.8）：
//   - 成功：显示 ✅ 登录成功 → 1.5s 后自动 window.close()（脚本 window.close 对
//     非 window.open 打开的页面会被浏览器拦截，故同时提供「手动关闭」按钮兜底）；
//   - 失败：显示具体错误，不自动关（用户可能想截图反馈）。
//   - 品牌色与面板渠道色一致（青蓝 #0e7490），无外部依赖（无 CDN 字体/图标）。
//
// 实现说明：仅 HTML 模板一个字符串，两个 login 包各自内联一份，
// 避免为一段 HTML 建立共享包（工具的包边界原则：渠道间零耦合）。
package login_qwenwork

import (
	"fmt"
	"html"
)

// callbackPage 渲染统一回调页。
// ok 为 true 时展示成功态（自动关闭）；false 时展示失败态（msg 为错误文案，不自动关）。
func callbackPage(ok bool, msg string) string {
	if ok {
		return fmt.Sprintf(callbackPageBase, "ok", "✓", "登录成功",
			"凭证已保存，请回到 wild-work 面板查看账号。<br>本页面即将自动关闭…",
			"true", 1500, 2500)
	}
	return fmt.Sprintf(callbackPageBase, "err", "✕", "登录失败",
		`<span class="msg">`+html.EscapeString(msg)+`</span>`,
		"false", 0, 800)
}

// callbackPageBase 统一模板：成功/失败态共用骨架，仅 icon/配色/文案不同。
// 注意 %% 转义（fmt 占位符）。
const callbackPageBase = `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>wild-work 登录回调</title>
<style>
  :root { color-scheme: dark; }
  * { margin: 0; padding: 0; box-sizing: border-box; }
  html, body { height: 100%%; }
  body {
    display: flex; align-items: center; justify-content: center;
    font-family: -apple-system, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    background: #0a0e17;
    background-image:
      radial-gradient(ellipse at 30%% 40%%, rgba(59,130,246,.08) 0%%, transparent 50%%),
      radial-gradient(ellipse at 70%% 60%%, rgba(139,92,246,.06) 0%%, transparent 50%%);
  }
  .card {
    width: 380px; padding: 40px 32px 28px; text-align: center;
    background: rgba(15,23,42,.85); backdrop-filter: blur(20px);
    border: 1px solid rgba(148,163,184,.12);
    border-radius: 20px;
    box-shadow: 0 8px 32px rgba(0,0,0,.4);
  }
  .icon {
    width: 72px; height: 72px; margin: 0 auto 20px; border-radius: 50%%;
    display: flex; align-items: center; justify-content: center;
    font-size: 36px; color: #fff;
  }
  .ok .icon { background: linear-gradient(135deg,#3b82f6,#2563eb); box-shadow: 0 4px 16px rgba(59,130,246,.35); }
  .err .icon { background: linear-gradient(135deg,#ef4444,#dc2626); box-shadow: 0 4px 16px rgba(239,68,68,.3); }
  h1 { font-size: 22px; color: #f1f5f9; margin-bottom: 10px; font-weight: 700; letter-spacing: -.3px; }
  p { font-size: 14px; color: #94a3b8; line-height: 1.6; margin-bottom: 24px; word-break: break-all; }
  .msg { font-size: 13px; color: #64748b; }
  .close-btn {
    display: inline-block; padding: 10px 40px; border: none; border-radius: 10px;
    font-size: 14px; font-weight: 500; color: #fff; cursor: pointer; transition: all .15s;
  }
  .ok .close-btn { background: linear-gradient(135deg,#3b82f6,#2563eb); }
  .err .close-btn { background: linear-gradient(135deg,#ef4444,#dc2626); }
  .ok .close-btn:hover { background: linear-gradient(135deg,#60a5fa,#3b82f6); box-shadow: 0 4px 16px rgba(59,130,246,.3); }
  .err .close-btn:hover { background: linear-gradient(135deg,#f87171,#ef4444); box-shadow: 0 4px 16px rgba(239,68,68,.3); }
  .brand { margin-top: 20px; font-size: 12px; color: #475569; }
</style>
</head>
<body>
  <div class="card %s">
    <div class="icon">%s</div>
    <h1>%s</h1>
    <p>%s</p>
    <button class="close-btn" onclick="tryClose()">立即关闭</button>
    <div class="brand">wild-work · 本页面由本机回调服务生成，可安全关闭</div>
  </div>
<script>
var autoClose = %s;
function tryClose() { window.close(); }
if (autoClose) { setTimeout(tryClose, %d); }
setTimeout(function () {
  var b = document.querySelector('.close-btn');
  if (!window.closed && b) { b.textContent = '关闭此标签页'; }
}, %d);
</script>
</body>
</html>`
