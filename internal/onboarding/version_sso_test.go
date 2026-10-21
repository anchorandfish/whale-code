package onboarding

import (
	"strings"
	"testing"

	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// TestRenderHeader_UsesVersionPackage 保证 Onboarding 首屏版本号与 --version 同源。
// 判别性：把 internal/version.Version 改成哨兵值后头部必须跟着变；修复前这里
// 读 debug.ReadBuildInfo()，源码构建恒为 dev，哨兵值不会出现 → 断言红。
func TestRenderHeader_UsesVersionPackage(t *testing.T) {
	orig := versionpkg.Version
	versionpkg.Version = "9.9.9-sso"
	defer func() { versionpkg.Version = orig }()

	header := renderHeader()
	if !strings.Contains(header, "v9.9.9-sso") {
		t.Fatalf("onboarding 头部未跟随 internal/version.Version：%q", header)
	}
	if strings.Contains(header, "vdev") {
		t.Fatalf("onboarding 头部仍在用 build info 的 dev 版本：%q", header)
	}
}
