package doctor

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/config"
	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// TestVersionSSOT_HeaderAndCheckUseVersionPackage 防止 doctor 再次分叉出自己的版本号。
//
// 判别性证明：把 internal/version.Version 改成一个哨兵值，doctor 的头部与
// Environment/Version 检查项都必须跟着变。修复前 doctor 读的是
// debug.ReadBuildInfo().Main.Version，从源码目录构建时恒为 "(devel)" → "dev"，
// 这个断言会直接红。
func TestVersionSSOT_HeaderAndCheckUseVersionPackage(t *testing.T) {
	orig := versionpkg.Version
	versionpkg.Version = "9.9.9-sso"
	defer func() { versionpkg.Version = orig }()

	doc := NewDoctor(config.DefaultConfigPtr(), nil, t.TempDir(), "")

	header := doc.renderHeader()
	if !strings.Contains(header, "v9.9.9-sso") {
		t.Fatalf("doctor 头部未跟随 internal/version.Version：%q", header)
	}
	if strings.Contains(header, "vdev") {
		t.Fatalf("doctor 头部仍在用 build info 的 dev 版本：%q", header)
	}

	got := doc.checkVersion()
	if got.Message != "whale v9.9.9-sso" {
		t.Fatalf("Version 检查项 = %q，期望 %q（必须与 --version 同源）", got.Message, "whale v9.9.9-sso")
	}
}

// TestGetCurrentVersion_StripsVPrefix 保证 --update-check 的比较口径与展示口径一致。
func TestGetCurrentVersion_StripsVPrefix(t *testing.T) {
	orig := versionpkg.Version
	versionpkg.Version = "v1.2.3"
	defer func() { versionpkg.Version = orig }()

	if got := getCurrentVersion(); got != "1.2.3" {
		t.Fatalf("getCurrentVersion() = %q，期望 %q", got, "1.2.3")
	}
}
