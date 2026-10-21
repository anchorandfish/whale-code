// Package plugin — M20 扩展功能测试
//
// 本文件覆盖 marketplace / signature / upgrade 的核心路径。
//
// ⚠️ 2026-09-15（G-E · B3）：原 `TestPluginSandbox_Noop` 与 `sandbox_integration.go` 一并删除 ——
// 该"插件进程级沙箱"是**广告但未接线**的假隔离（6 个符号零生产调用点，且自身丢弃 preparer 的
// restrict 函数）。拍板 B3 = 删除，把"插件命令走主沙箱"确立为唯一真相。
// 回归守卫见 `audit_char_ge_test.go::TestGE_B3_NoSecondIsolatorInPluginPackage`。
package plugin

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestParseMarketIndex 测试市场索引解析（M20-01）。
func TestParseMarketIndex(t *testing.T) {
	data := []byte(`{
		"version": 1,
		"updated": 1719000000,
		"entries": [
			{"id": "p1", "name": "Plugin One", "version": "1.0.0", "repository": "https://github.com/test/p1.git"},
			{"id": "p2", "name": "Plugin Two", "version": "2.0.0", "repository": "https://github.com/test/p2.git"}
		]
	}`)
	idx, err := ParseMarketIndex(data)
	if err != nil {
		t.Fatalf("ParseMarketIndex error: %v", err)
	}
	if len(idx.Entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(idx.Entries))
	}
	if idx.Entries[0].ID != "p1" {
		t.Errorf("expected first entry ID 'p1', got %q", idx.Entries[0].ID)
	}
}

// TestFormatMarketIndex 测试市场索引格式化（M20-01）。
func TestFormatMarketIndex(t *testing.T) {
	idx := &MarketIndex{Version: 1, Entries: []MarketEntry{{ID: "p1", Name: "P1", Version: "1.0.0"}}}
	s := FormatMarketIndex(idx)
	if s == "" {
		t.Error("expected non-empty format string")
	}

	empty := FormatMarketIndex(nil)
	if empty == "" {
		t.Error("expected non-empty for nil index")
	}
}

// TestSignatureVerifier_Ed25519 测试 ed25519 签名校验（M20-02）。
func TestSignatureVerifier_Ed25519(t *testing.T) {
	// 生成 ed25519 密钥对
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey error: %v", err)
	}
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	verifier := NewSignatureVerifier(SignatureConfig{
		PublicKey: pubB64,
		Required:  true,
		Algorithm: SigAlgEd25519,
	})

	// 签名 manifest
	manifest := []byte(`{"id":"test","name":"Test","version":"1.0.0"}`)
	sig, err := SignManifest(manifest, priv)
	if err != nil {
		t.Fatalf("SignManifest error: %v", err)
	}

	// 验签通过
	if err := verifier.VerifyBytes(manifest, sig); err != nil {
		t.Errorf("VerifyBytes valid sig error: %v", err)
	}

	// 篡改 manifest 后验签失败
	tampered := []byte(`{"id":"test","name":"Tampered","version":"1.0.0"}`)
	if err := verifier.VerifyBytes(tampered, sig); err == nil {
		t.Error("expected verification error for tampered manifest")
	}
}

// TestSignatureVerifier_Noop 测试降级 noop 校验器（M20-02）。
func TestSignatureVerifier_Noop(t *testing.T) {
	v := NewSignatureVerifier(SignatureConfig{PublicKey: "", Required: false})
	if err := v.Verify("/nonexistent"); err != nil {
		t.Errorf("noop verifier should return nil, got %v", err)
	}
	if err := v.VerifyBytes([]byte("any"), "any"); err != nil {
		t.Errorf("noop verifier should return nil, got %v", err)
	}
}

// TestSignatureVerifier_RequiredMissing 测试 Required=true 时无签名文件拒绝（M20-02）。
func TestSignatureVerifier_RequiredMissing(t *testing.T) {
	dir := t.TempDir()
	manifest := Plugin{ID: "t", Name: "T", Version: "1.0.0"}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0644)

	pub, _, _ := ed25519.GenerateKey(nil)
	v := NewSignatureVerifier(SignatureConfig{
		PublicKey: base64.StdEncoding.EncodeToString(pub),
		Required:  true,
	})
	if err := v.Verify(dir); err == nil {
		t.Error("expected error for missing signature when Required=true")
	}
}

// TestCompareSemver 测试 semver 比较（M20-05）。
func TestCompareSemver(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"2.0.0", "1.0.0", 1},
		{"1.0.0", "2.0.0", -1},
		{"1.2.0", "1.1.0", 1},
		{"1.0.3", "1.0.10", -1},
		{"1.0.0-alpha", "1.0.0", 0}, // prerelease 被忽略
	}
	for _, c := range cases {
		got := compareSemver(c.a, c.b)
		if got != c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestHashManifest 测试 manifest 哈希（M20-02 辅助函数）。
func TestHashManifest(t *testing.T) {
	h1 := HashManifest([]byte("test"))
	h2 := HashManifest([]byte("test"))
	h3 := HashManifest([]byte("other"))
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
	if h1 == h3 {
		t.Error("different input should produce different hash")
	}
}

// TestParseSignatureConfig 测试签名配置解析（M20-02）。
func TestParseSignatureConfig(t *testing.T) {
	data := []byte(`{"public_key":"dGVzdA==","required":true}`)
	cfg, err := ParseSignatureConfig(data)
	if err != nil {
		t.Fatalf("ParseSignatureConfig error: %v", err)
	}
	if !cfg.Required {
		t.Error("expected Required=true")
	}
	if cfg.Algorithm != SigAlgEd25519 {
		t.Errorf("expected default algorithm ed25519, got %s", cfg.Algorithm)
	}
}
