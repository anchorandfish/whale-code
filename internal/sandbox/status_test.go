// Package sandbox — 门 2 降级可见性测试（TDD：失败测试先行）。
//
// 分层验证链覆盖：
// 1. 存在：LogDegradation 记录 reason、LogStatus 不计入降级
// 2. 已接线：IsolationStatusOf 能从生产 Manager 读出实际隔离状态
// 3. 防护真的发生：降级（Landlock 不支持等）出现在用户可见的 banner 文本中
package sandbox

import (
	"context"
	"strings"
	"testing"
)

// TestAuditLog_DegradeReasonsTracked LogDegradation 的 reason 必须可查询（结果断言：
// 追踪的 reason 与写入的一致，而非只断言 count>0）。
func TestAuditLog_DegradeReasonsTracked(t *testing.T) {
	al := &AuditLog{} // 无 logDir：只验证内存级追踪
	al.LogDegradation(SandboxLevelIsolated, SandboxLevelIsolated,
		"linux landlock unsupported by kernel (< 5.13?), filesystem isolation limited to namespaces")
	al.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict, "second degrade")

	reasons := al.DegradeReasons()
	if len(reasons) != 2 {
		t.Fatalf("DegradeReasons() = %v, want 2 reasons", reasons)
	}
	want0 := "linux landlock unsupported by kernel (< 5.13?), filesystem isolation limited to namespaces"
	if reasons[0] != want0 {
		t.Errorf("reasons[0] = %q, want %q", reasons[0], want0)
	}
}

// TestAuditLog_StatusNotCountedAsDegradation LogStatus（信息性记录，如 "namespaces enabled"）
// 不得计入降级：否则启动 banner 会把正常启用误报为降级。
func TestAuditLog_StatusNotCountedAsDegradation(t *testing.T) {
	al := &AuditLog{}
	al.LogStatus("linux namespaces enabled: pid,net,uts (cloneflags=0x100000)")
	if al.ContainsDegradation() {
		t.Error("LogStatus must not set ContainsDegradation()")
	}
	if got := al.DegradationCount(); got != 0 {
		t.Errorf("DegradationCount() = %d, want 0", got)
	}
	if got := al.DegradeReasons(); len(got) != 0 {
		t.Errorf("DegradeReasons() = %v, want empty", got)
	}
}

// TestAuditLog_DegradeReasonsCapped reason 上限防护（防止长会话无限增长）。
func TestAuditLog_DegradeReasonsCapped(t *testing.T) {
	al := &AuditLog{}
	for i := 0; i < maxDegradeReasons+10; i++ {
		al.LogDegradation(SandboxLevelIsolated, SandboxLevelStrict, "x")
	}
	if got := len(al.DegradeReasons()); got != maxDegradeReasons {
		t.Errorf("len(DegradeReasons()) = %d, want cap %d", got, maxDegradeReasons)
	}
}

// TestIsolationStatusOf_DegradedManager 零值配置 → degraded 沙箱：
// Platform=degraded、无 Features（结果断言：用户看到的是"无 OS 隔离"而非空白）。
func TestIsolationStatusOf_DegradedManager(t *testing.T) {
	mgr := NewManager(&SandboxConfig{}, SandboxLevelStrict)
	st := IsolationStatusOf(mgr)
	if st.Platform != "degraded" {
		t.Errorf("Platform = %q, want \"degraded\"", st.Platform)
	}
	if len(st.Features) != 0 {
		t.Errorf("Features = %v, want empty for degraded sandbox", st.Features)
	}
	if st.Level != SandboxLevelStrict {
		t.Errorf("Level = %v, want Strict", st.Level)
	}
}

// TestIsolationStatusOf_ProductionManager 生产工厂（本机平台）：状态查询走生产构造路径，
// 且**宣称与平台一致性**成立（结果断言，跨平台可编译）。
//
// ★ 2026-10-09（接线）：原写法硬编码"必须非 degraded"——那是**主机事实**，
// 不是 code 不变式：在 `sandbox_apply` 被拒的机器（内嵌终端 / MDM / 受限上下文）上，
// 接线后"如实降级"会被它判成错。本文件**无平台构建标签**（须处处可编译），
// 故这里只钉**可移植**的那条关系：
//
//	非 degraded ⇒ 必须列出生效特性；
//	degraded ⇒ 必须**不列**任何特性（口径一致：降级不得同时宣称特性）。
//
// "探针不过就必须 degraded"（更强、更贴）由 darwin 专属判别器
// `.go:TestN013_ProductionManagerStatusAgreesWithProbe` 钉死。
func TestIsolationStatusOf_ProductionManager(t *testing.T) {
	mgr := NewProductionSandboxManager(t.TempDir())
	st := IsolationStatusOf(mgr)

	if st.Platform == "" {
		t.Fatal("production manager Platform 为空（平台实现选择失败）")
	}
	if st.Platform == "degraded" {
		if len(st.Features) != 0 {
			t.Errorf("degraded 沙箱不得列出任何生效特性（否则仍是谎报）: %v", st.Features)
		}
		return
	}
	if len(st.Features) == 0 {
		t.Errorf("Features empty for platform %q, want non-empty", st.Platform)
	}
}

// TestIsolationStatusOf_NilAndForeignManager nil/非 *manager 实现：
// 返回 unknown 状态而非 panic（降级路径有可观测出口）。
func TestIsolationStatusOf_NilAndForeignManager(t *testing.T) {
	if st := IsolationStatusOf(nil); st.Platform != "unknown" {
		t.Errorf("IsolationStatusOf(nil).Platform = %q, want \"unknown\"", st.Platform)
	}
	foreign := foreignMgr{}
	if st := IsolationStatusOf(foreign); st.Platform != "unknown" {
		t.Errorf("IsolationStatusOf(foreign).Platform = %q, want \"unknown\"", st.Platform)
	}
}

// TestFormatIsolationBanner banner 格式（门 2 核心：降级必须出现在用户可见文本）：
// - 正常：一行，含平台与特性
// - 降级：额外逐行列出降级原因
func TestFormatIsolationBanner(t *testing.T) {
	normal := FormatIsolationBanner(IsolationStatus{
		Level:    SandboxLevelStrict,
		Platform: "linux-namespaces",
		Features: []string{"namespaces(pid,net,uts,mount)", "landlock"},
	})
	if !strings.Contains(normal, "linux-namespaces") {
		t.Errorf("banner missing platform: %q", normal)
	}
	if !strings.Contains(normal, "landlock") {
		t.Errorf("banner missing features: %q", normal)
	}
	if strings.Contains(normal, "degraded") {
		t.Errorf("normal banner must not mention degrade: %q", normal)
	}

	degraded := FormatIsolationBanner(IsolationStatus{
		Level:        SandboxLevelStrict,
		Platform:     "linux-namespaces",
		Features:     []string{"namespaces(pid,net,uts,mount)"},
		Degradations: []string{"linux landlock unsupported by kernel (< 5.13?), filesystem isolation limited to namespaces"},
	})
	if !strings.Contains(degraded, "linux landlock unsupported") {
		t.Errorf("degraded banner missing degradation reason: %q", degraded)
	}
}

// TestLinuxLandlockDegradationVisibleOnBanner 门 2 主用例（Linux）：
// 配置开启 Landlock 时，banner 必须出现 landlock——内核支持则列为特性，
// 不支持则给出降级原因（结果断言：用户可见文本覆盖两种内核现实，不依赖审计旁路）。
// 非 Linux 平台跳过（landlock 仅 Linux 存在）。
func TestLinuxLandlockDegradationVisibleOnBanner(t *testing.T) {
	if !isLinuxLandlockBuild() {
		t.Skip("landlock degrade path is linux-only")
	}
	cfg := DefaultOSLevelConfig()
	cfg.Linux.UseNamespaces = true
	cfg.Linux.Namespaces = []string{"pid", "net", "uts", "mount"}
	cfg.Linux.UseLandlock = true
	mgr := NewManager(&SandboxConfig{OSLevel: cfg}, SandboxLevelStrict)
	st := IsolationStatusOf(mgr)
	banner := FormatIsolationBanner(st)
	if !strings.Contains(banner, "landlock") {
		t.Errorf("landlock neither in features nor degradations, banner: %q", banner)
	}
	// 口径一致性：特性宣称与降级宣告不得同时出现
	inFeatures := false
	for _, f := range st.Features {
		if strings.Contains(f, "landlock") {
			inFeatures = true
		}
	}
	if inFeatures {
		for _, d := range st.Degradations {
			if strings.Contains(d, "landlock") {
				t.Errorf("landlock claimed as feature and degraded simultaneously: %v / %v", st.Features, st.Degradations)
			}
		}
	}
}

// foreignMgr 是 Manager 接口的外来实现（验证 IsolationStatusOf 类型断言降级路径）。
type foreignMgr struct{}

func (foreignMgr) Prepare(_ context.Context, _ SandboxLevel, _ string) (SandboxContext, func(), error) {
	return SandboxContext{}, nil, nil
}
func (foreignMgr) ValidateCommand(_ context.Context, _ string, _ *SandboxConfig) (*ValidationResult, error) {
	return nil, nil
}
func (foreignMgr) ValidatePath(_ context.Context, _ string, _ *SandboxConfig) error { return nil }
func (foreignMgr) Level() SandboxLevel                                              { return SandboxLevelStrict }
