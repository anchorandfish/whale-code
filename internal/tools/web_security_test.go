package tools

import (
	"testing"
)

// TestValidateWebURL_SSRF 验证 SSRF 防护逻辑生效（TC-007，固定测试用例方案 v3）。
// 直接调用 Executor.validateWebURL，不依赖 AI 行为，确保 100% 可重复执行。
// 源码核查：validateWebURL 签名 func(e *Executor) validateWebURL(rawURL string) error（web.go:161）
// AllowLocalhostForTest=false（零值）时，isIPBlocked 拦截 Loopback/Private/LinkLocal 地址。
func TestValidateWebURL_SSRF(t *testing.T) {
	e := &Executor{} // AllowLocalhostForTest=false（零值）
	cases := []struct {
		url  string
		name string
	}{
		{"http://127.0.0.1:8080/secret", "loopback IPv4"},
		{"http://10.0.0.1/", "private 10.x"},
		{"http://192.168.1.1/", "private 192.168.x"},
		{"http://169.254.169.254/", "AWS metadata"},
		{"http://[::1]/", "loopback IPv6"},
	}
	for _, c := range cases {
		err := e.validateWebURL(c.url)
		if err == nil {
			t.Errorf("%s: expected SSRF block, got nil", c.name)
		}
	}
}

// TestValidateWebURL_Valid 验证合法 URL 通过 SSRF 检查（TC-007 反例）。
// 合法公网 URL 应不被拦截（注意：此测试依赖 DNS，可能因网络问题失败，故仅验证格式）。
func TestValidateWebURL_Valid(t *testing.T) {
	e := &Executor{}
	// 合法公网 URL（不解析 DNS，仅格式校验）
	err := e.validateWebURL("https://example.com/page")
	if err != nil {
		t.Errorf("valid URL should pass, got error: %v", err)
	}
}
