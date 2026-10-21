//go:build darwin

// Package sandbox — macOS Seatbelt workspace-write profile 单元测试（P1-SEC）。
//
// 测试目标：buildSeatbeltProfile 生成的 SBPL 必须是 workspace-write 姿态：
//   - (deny default) 起手（default-deny，未显式允许一律拒绝）
//   - (deny network*)（网络全断）
//   - 工作区可写（allow file-write* subpath workspace）
//   - .git/hooks 不可写且规则位于工作区放行之后（SBPL last-match-wins，后置 deny 才能生效）
//   - 敏感目录（~/.ssh 等）不可读且规则位于 (allow file-read*) 之后
package sandbox

import (
	"os/exec"
	"strings"
	"testing"
)

// TestBuildSeatbeltProfile_DefaultDenyPosture 验证 profile 是 default-deny 姿态。
func TestBuildSeatbeltProfile_DefaultDenyPosture(t *testing.T) {
	profile, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace: "/Users/test/proj",
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile failed: %v", err)
	}
	for _, want := range []string{
		"(deny default)",
		"(deny network*)",
		"(allow file-read*)",
		`(allow file-write* (subpath "/Users/test/proj"))`,
		`(deny file-write* (subpath "/Users/test/proj/.git/hooks"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("profile missing rule %q\nprofile:\n%s", want, profile)
		}
	}
}

// TestBuildSeatbeltProfile_AllowNetwork 门 3 网络开关（结果断言）：
// AllowNetwork=true 时 profile 必须显式 (allow network*)——(deny default) 下
// 仅省略 deny 规则网络仍被拒（开关变静默空操作），显式 allow 才真放行；
// 其余规则不变（文件/进程隔离姿态不受网络开关影响）。
func TestBuildSeatbeltProfile_AllowNetwork(t *testing.T) {
	profile, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace:    "/Users/test/proj",
		AllowNetwork: true,
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile failed: %v", err)
	}
	if strings.Contains(profile, "(deny network*)") {
		t.Errorf("AllowNetwork=true profile must omit (deny network*):\n%s", profile)
	}
	if !strings.Contains(profile, "(allow network*)") {
		t.Errorf("AllowNetwork=true profile must contain explicit (allow network*) (deny default posture):\n%s", profile)
	}
	// 网络开关不得影响其余隔离规则
	for _, want := range []string{
		"(deny default)",
		"(allow file-read*)",
		`(allow file-write* (subpath "/Users/test/proj"))`,
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("AllowNetwork profile missing rule %q:\n%s", want, profile)
		}
	}

	// 默认（AllowNetwork=false）：deny network* 保持
	def, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace: "/Users/test/proj",
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile failed: %v", err)
	}
	if !strings.Contains(def, "(deny network*)") {
		t.Errorf("default profile must keep (deny network*):\n%s", def)
	}
}

// TestRestrictAllowNetworkWiredFromProductionEnv 接线钉子（对抗式审查 #2）：
// 生产链路 env → NewProductionSandboxManager → NewManager → Enter → Restrict
// 后，cmd.Args 内嵌 profile 必须含 (allow network*)。
// Restrict 中 opts.AllowNetwork = d.cfg.AllowNetwork 透传行被误删时本测试红
// （其余 4 个门 3 测试全部绕过该写入点，误删时依然绿——生产静默断网而
// banner 宣称 network=on）。
func TestRestrictAllowNetworkWiredFromProductionEnv(t *testing.T) {
	skipIfNoSandboxExec(t)
	t.Setenv("WHALE_SANDBOX_ALLOW_NETWORK", "1")
	mgr := NewProductionSandboxManager(t.TempDir())
	m, ok := mgr.(*manager)
	if !ok {
		t.Fatal("NewProductionSandboxManager must return *manager")
	}
	_, cleanup, err := m.osSandbox.Enter()
	if err != nil {
		t.Fatalf("Enter failed: %v", err)
	}
	defer cleanup()

	cmd := exec.Command("/bin/echo", "hi")
	cmd.Dir = t.TempDir()
	if err := m.osSandbox.Restrict(cmd); err != nil {
		t.Fatalf("Restrict failed: %v", err)
	}
	// 生产写入点断言：profile 经 sandbox-exec -p（可能再被 rlimit helper
	// 外层包装）注入 cmd.Args——搜索任一 arg 含 (allow network*)
	found := false
	for _, a := range cmd.Args {
		if strings.Contains(a, "(allow network*)") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Restrict must embed (allow network*) in cmd args (production write point), got: %v", cmd.Args)
	}
}

// TestBuildSeatbeltProfile_RuleOrderingDenyWins 验证 deny 规则位于对应 allow 之后。
//
// SBPL 语义：同一条操作命中多条规则时，最后一条生效（last-match-wins）。
// 因此 deny .git/hooks 必须在 allow workspace 之后，deny 敏感读必须在 allow file-read* 之后。
func TestBuildSeatbeltProfile_RuleOrderingDenyWins(t *testing.T) {
	profile, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace:         "/Users/test/proj",
		SensitiveReadDirs: []string{"/Users/test/.ssh"},
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile failed: %v", err)
	}

	allowWS := strings.Index(profile, `(allow file-write* (subpath "/Users/test/proj"))`)
	denyHooks := strings.Index(profile, `(deny file-write* (subpath "/Users/test/proj/.git/hooks"))`)
	if allowWS < 0 || denyHooks < 0 {
		t.Fatalf("workspace allow/deny-hooks rules missing:\n%s", profile)
	}
	if denyHooks < allowWS {
		t.Errorf("deny .git/hooks rule (idx %d) must come AFTER allow workspace rule (idx %d); otherwise the deny never wins", denyHooks, allowWS)
	}

	allowRead := strings.Index(profile, "(allow file-read*)")
	denySSH := strings.Index(profile, `(deny file-read* (subpath "/Users/test/.ssh"))`)
	if allowRead < 0 || denySSH < 0 {
		t.Fatalf("file-read allow / sensitive-deny rules missing:\n%s", profile)
	}
	if denySSH < allowRead {
		t.Errorf("deny sensitive read rule (idx %d) must come AFTER (allow file-read*) (idx %d)", denySSH, allowRead)
	}
}

// TestBuildSeatbeltProfile_ExtraWriteDirs 验证额外可写根（tmp/cache）出现在 profile 中。
func TestBuildSeatbeltProfile_ExtraWriteDirs(t *testing.T) {
	profile, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace:      "/Users/test/proj",
		ExtraWriteDirs: []string{"/private/var/folders/xx/T", "/Users/test/Library/Caches"},
	})
	if err != nil {
		t.Fatalf("buildSeatbeltProfile failed: %v", err)
	}
	for _, d := range []string{"/private/var/folders/xx/T", "/Users/test/Library/Caches"} {
		if !strings.Contains(profile, `(subpath "`+d+`")`) {
			t.Errorf("profile missing extra write dir %q:\n%s", d, profile)
		}
	}
}

// TestBuildSeatbeltProfile_RejectsPathInjection 验证路径含 SBPL 特殊字符时 fail-closed。
//
// SBPL 字符串字面量中 " 和 \ 是转义字符；路径包含它们可能注入额外规则
// （CVE 形态：profile 注入）。必须返回错误而非生成被篡改的 profile。
func TestBuildSeatbeltProfile_RejectsPathInjection(t *testing.T) {
	bad := []string{
		`/Users/test/pro"j)`,              // 引号逃逸 + 规则提前闭合
		`/Users/test/pro\j`,               // 反斜杠转义
		"/Users/test/pro\n(deny default)", // 换行注入规则
		"/Users/test/pro\x00",             // 控制字符
	}
	for _, p := range bad {
		if _, err := buildSeatbeltProfile(seatbeltProfileOptions{Workspace: p}); err == nil {
			t.Errorf("workspace path %q should be rejected (injection risk), but profile was built", p)
		}
	}
	// 敏感目录同样校验
	if _, err := buildSeatbeltProfile(seatbeltProfileOptions{
		Workspace:         "/Users/test/proj",
		SensitiveReadDirs: []string{`/Users/x/.ssh") (allow network*`},
	}); err == nil {
		t.Error("sensitive dir with quote injection should be rejected")
	}
}

// TestDefaultDarwinProfileOptions 验证默认选项包含 tmp/cache 可写根与敏感读目录。
func TestDefaultDarwinProfileOptions(t *testing.T) {
	opts, err := defaultDarwinProfileOptions("/Users/test/proj")
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions failed: %v", err)
	}
	if opts.Workspace == "" {
		t.Fatal("workspace should not be empty")
	}
	if len(opts.ExtraWriteDirs) == 0 {
		t.Error("extra write dirs should include temp dir (commands need TMPDIR writes)")
	}
	foundSensitive := false
	for _, d := range opts.SensitiveReadDirs {
		if strings.HasSuffix(d, "/.ssh") {
			foundSensitive = true
		}
	}
	if !foundSensitive {
		t.Errorf("sensitive read dirs should include ~/.ssh, got %v", opts.SensitiveReadDirs)
	}
}
