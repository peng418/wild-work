package login_qwenwork

import (
	"html"
	"os"
	"strings"
	"testing"
)

// TestCallbackPageStructure 验证模板占位符全部被替换（无残留 %!x(MISSING)）与 XSS 转义。
func TestCallbackPageStructure(t *testing.T) {
	okPage := callbackPage(true, "")
	if strings.Contains(okPage, "%!") {
		t.Fatal("成功页含未替换占位符")
	}
	if !strings.Contains(okPage, "登录成功") || !strings.Contains(okPage, "autoClose = true") {
		t.Fatal("成功页缺少成功文案或自动关闭脚本")
	}
	errPage := callbackPage(false, `<script>alert(1)</script> & "quotes"`)
	if strings.Contains(errPage, "%!") {
		t.Fatal("失败页含未替换占位符")
	}
	// XSS 转义验证
	if !strings.Contains(errPage, html.EscapeString("<script>")) {
		t.Fatal("错误文案未做 HTML 转义")
	}
	if strings.Contains(errPage, "<script>alert") {
		t.Fatal("错误文案被原样嵌入，存在 XSS")
	}
	// 输出预览文件供人工查看（temp/ 已 gitignore）
	_ = os.WriteFile("../../temp/qwen/cb_success.html", []byte(okPage), 0o644)
	_ = os.WriteFile("../../temp/qwen/cb_error.html", []byte(errPage), 0o644)
}
