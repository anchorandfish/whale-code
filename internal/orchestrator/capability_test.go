// capability_test.go — 桶 4 CapabilityCache 反例测试（地基层 §2.7.4）。
// 5 个反例测试覆盖：severity 映射 / 路径穿越拒绝 / 文件不存在 Load / Snapshot 只读 / FALLBACK 声明。

package orchestrator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/doctor"
)

// TestCapabilityCache_SeverityMapping mock doctor 返回 OK/Warn/Error/Fatal，验证映射为 true/true/false/false。
// 验证目标（02 §2.7.4）：severity → bool 映射规则正确。
func TestCapabilityCache_SeverityMapping(t *testing.T) {
	results := []doctor.CheckResult{
		{Category: "Env", Name: "go", Severity: doctor.SeverityOK, Message: "go 1.25 installed"},
		{Category: "Env", Name: "git", Severity: doctor.SeverityWarn, Message: "git 2.30 (recommended 2.40+)"},
		{Category: "Deps", Name: "gcc", Severity: doctor.SeverityError, Message: "gcc not found"},
		{Category: "Platform", Name: "dlp", Severity: doctor.SeverityFatal, Message: "Esafenet DLP blocking"},
	}

	items := applyDoctorResults(results)

	cases := []struct {
		key      string
		expected bool
	}{
		{"env.Env.go", true},        // OK → true
		{"env.Env.git", true},       // Warn → true
		{"env.Deps.gcc", false},     // Error → false
		{"env.Platform.dlp", false}, // Fatal → false
	}
	for _, c := range cases {
		v, ok := items[c.key]
		if !ok {
			t.Errorf("key %q not in items (should be mapped)", c.key)
			continue
		}
		if v != c.expected {
			t.Errorf("key %q: expected %v, got %v", c.key, c.expected, v)
		}
	}
}

// TestCapabilityCache_PathTraversal_Rejected 致命反例：构造 path 含 ".." 或 key 含 "/"，验证返回 error。
// 验证目标（地基层 §2.4.2 安全要求）：路径穿越与 key 注入零容忍。
func TestCapabilityCache_PathTraversal_Rejected(t *testing.T) {
	// 致命反例 1：path 含 ".."（用字符串拼接构造，filepath.Join 会 Clean 掉 ..）
	evilPath := t.TempDir() + string(filepath.Separator) + ".." + string(filepath.Separator) + "evil.json"
	_, err := NewCapabilityCache(evilPath)
	if err == nil {
		t.Fatal("NewCapabilityCache with '..' in path must return error (致命反例：路径穿越)")
	}
	if !strings.Contains(err.Error(), "..") {
		t.Errorf("error should mention '..', got: %v", err)
	}

	// 致命反例 2：GetCapability key 含 "/"
	c, err := NewCapabilityCache(filepath.Join(t.TempDir(), "cap.json"))
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}
	if _, err := c.GetCapability("env/foo"); err == nil {
		t.Fatal("GetCapability with '/' in key must return error (致命反例：key 注入)")
	}

	// 致命反例 3：GetCapability key 含 "\"
	if _, err := c.GetCapability("env\\foo"); err == nil {
		t.Fatal("GetCapability with '\\' in key must return error")
	}

	// 致命反例 4：GetCapability key 含 ".."
	if _, err := c.GetCapability("env..foo"); err == nil {
		t.Fatal("GetCapability with '..' in key must return error")
	}

	// 致命反例 5：GetCapability 空 key
	if _, err := c.GetCapability(""); err == nil {
		t.Fatal("GetCapability with empty key must return error")
	}
}

// TestCapabilityCache_Load_NonExistent 文件不存在时 Load 返回空 cache 不报错。
// 验证目标（地基层 §2.7.4）：冷启动文件缺失不阻塞，返回空 cache。
func TestCapabilityCache_Load_NonExistent(t *testing.T) {
	c, err := NewCapabilityCache(filepath.Join(t.TempDir(), "nonexistent.json"))
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	// Load 不存在的文件应返回 nil error + 空 cache
	if err := c.Load(); err != nil {
		t.Fatalf("Load non-existent file must not error, got: %v", err)
	}

	// cache 应为空（无 items）
	if got := c.CheckedAt(); got != 0 {
		t.Errorf("expected CheckedAt=0 for empty cache, got %d", got)
	}

	// GetCapability 任意 key 应返回 not found error
	if _, err := c.GetCapability("env.Env.go"); err == nil {
		t.Fatal("GetCapability on empty cache must return not-found error")
	}
}

// TestCapabilityCache_Snapshot_ReadOnly Snapshot 返回的 map 修改不影响内部状态（copy）。
// 验证目标（02 §2.7.1）：子任务只读 snapshot，修改不污染编排器状态。
func TestCapabilityCache_Snapshot_ReadOnly(t *testing.T) {
	c, err := NewCapabilityCache(filepath.Join(t.TempDir(), "cap.json"))
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	// 用 applyDoctorResults 填充内部 items（绕过 ProbeViaDoctor 的 d.Run 调用）
	c.mu.Lock()
	c.items["env.Env.go"] = true
	c.items["env.Deps.gcc"] = false
	c.mu.Unlock()

	// 取 snapshot 并修改
	snap := c.Snapshot()
	snap["env.Env.go"] = false   // 修改副本
	snap["env.Hack.evil"] = true // 添加副本项
	delete(snap, "env.Deps.gcc") // 删除副本项

	// 内部状态应不受影响
	got, err := c.GetCapability("env.Env.go")
	if err != nil {
		t.Fatalf("GetCapability: %v", err)
	}
	if !got {
		t.Error("Snapshot modification leaked to internal state: env.Env.go should still be true")
	}

	if _, err := c.GetCapability("env.Hack.evil"); err == nil {
		t.Error("Snapshot addition leaked to internal state: env.Hack.evil should not exist")
	}

	got2, err := c.GetCapability("env.Deps.gcc")
	if err != nil {
		t.Fatalf("GetCapability gcc: %v", err)
	}
	if got2 {
		t.Error("Snapshot deletion leaked to internal state: env.Deps.gcc should still be false")
	}
}

// TestCapabilityCache_Fallback_Declared the constant must declare that the
// component is a fallback, with its trade-offs and retirement condition.
func TestCapabilityCache_Fallback_Declared(t *testing.T) {
	// 验证 CapabilityCacheFallbackDecl 常量含关键声明
	decl := CapabilityCacheFallbackDecl
	requiredKeywords := []string{
		"FALLBACK",
		"trust_level",
		"Retirement condition",
		"GetCapability",
		"2026-12-31",
	}
	for _, kw := range requiredKeywords {
		if !strings.Contains(decl, kw) {
			t.Errorf("CapabilityCacheFallbackDecl must contain %q", kw)
		}
	}
}

// TestCapabilityCache_SaveLoad_RoundTrip Save 后 Load 应恢复相同状态（补充测试）。
func TestCapabilityCache_SaveLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cap.json")
	c1, err := NewCapabilityCache(path)
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	// 填充数据
	c1.mu.Lock()
	c1.items["env.Env.go"] = true
	c1.items["env.Deps.gcc"] = false
	c1.checkedAt = 1234567890
	c1.mu.Unlock()

	if err := c1.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 新建实例 Load
	c2, err := NewCapabilityCache(path)
	if err != nil {
		t.Fatalf("NewCapabilityCache c2: %v", err)
	}
	if err := c2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 验证恢复
	if got := c2.CheckedAt(); got != 1234567890 {
		t.Errorf("expected CheckedAt=1234567890, got %d", got)
	}
	v, err := c2.GetCapability("env.Env.go")
	if err != nil || !v {
		t.Errorf("env.Env.go should be true after Load, got v=%v err=%v", v, err)
	}
	v, err = c2.GetCapability("env.Deps.gcc")
	if err != nil || v {
		t.Errorf("env.Deps.gcc should be false after Load, got v=%v err=%v", v, err)
	}
}

// TestCapabilityCache_ProbeViaDoctor_RealDoctor 用真实 Doctor 验证 ProbeViaDoctor 不崩溃（补充测试）。
// 注意：此测试会调用真实 doctor.Run()，可能较慢，但验证端到端流程。
func TestCapabilityCache_ProbeViaDoctor_RealDoctor(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real doctor test in short mode")
	}

	path := filepath.Join(t.TempDir(), "cap.json")
	c, err := NewCapabilityCache(path)
	if err != nil {
		t.Fatalf("NewCapabilityCache: %v", err)
	}

	// 构造真实 Doctor（cfg/aiClient 可为 nil，Run 不会崩溃）
	d := doctor.NewDoctor(nil, nil, t.TempDir(), "")
	if err := c.ProbeViaDoctor(context.Background(), d); err != nil {
		t.Fatalf("ProbeViaDoctor failed: %v", err)
	}

	// 验证 cache 文件已生成
	if err := c.Load(); err != nil {
		t.Fatalf("Load after Probe: %v", err)
	}
	// 不验证具体 key（环境相关），只验证 CheckedAt 已更新
	if got := c.CheckedAt(); got == 0 {
		t.Error("expected CheckedAt > 0 after Probe")
	}
}
