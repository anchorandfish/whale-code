// commands_test.go — H11/N27 Phase B B6 测试。
// 含致命反例：
//   - TestCommands_probe_add_h2_blocked（H2 校验拦截 "rm -rf /"）
//   - TestCommands_registration_via_command_list（RegisterCommands 注入 r.OnMemory 回调）
//   - TestCapability_force_project_scope（PIT-001 重现：trust_level=capability 写 _user/ 被拒）
//
// 测试覆盖：
//   - TestCommands_confirm（致命反例）：unverified→verified
//   - TestCommands_confirm_already_verified：verified→verified 无效流转
//   - TestCommands_confirm_not_found：name 不存在
//   - TestCommands_confirm_empty_name：空 name 返回 Usage
//   - TestCommands_probe_add：注册成功
//   - TestCommands_probe_add_h2_blocked（致命反例）：H2 拦截
//   - TestCommands_probe_add_empty_args：空 args 返回 Usage
//   - TestCommands_probe_no_subcommand：probe 单独返回 Usage
//   - TestCommands_invalidate：调用成功
//   - TestCommands_invalidate_not_found：name 不存在
//   - TestCommands_invalidate_empty_name：空 name 返回 Usage
//   - TestCommands_dispatch_fallback：未匹配子命令回退 N12
//   - TestCommands_registration_via_command_list（致命反例）：RegisterCommands 注入回调
//   - TestCommands_register_commands_nil_registry：nil registry 返回 error
//   - TestCapability_force_project_scope（致命反例，PIT-001 重现）：capability 写 _user/ 被拒
//   - TestCapability_in_project_scope_success：capability 写 project scope 成功（正向对照）

package memory

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/command"
)

// setupCommandsTest 创建测试用 MergeLayer + 清理 capabilityStore + 清理 probeRegistry。
// 复用 setupTestMergeLayer（已注入 globalMergeLayer + t.Cleanup）。
func setupCommandsTest(t *testing.T, workDir string) (baseDir string, ml *MergeLayer) {
	t.Helper()
	baseDir, ml = setupTestMergeLayer(t, workDir)
	// 清理 capabilityStore（HandleConfirm/HandleInvalidate 会临时注入）
	t.Cleanup(ResetCapabilityRegistryForTest)
	// 清理 h2Checker（默认 nil，走 basicDangerCheck；某些测试会临时注入）
	t.Cleanup(func() {
		h2Checker = nil
	})
	return baseDir, ml
}

// writeUnverifiedEntryForCmd 写入 unverified 记忆（commands 测试专用，避免与 invalidate_test 的辅助函数重名）。
func writeUnverifiedEntryForCmd(t *testing.T, store *MemoryStore, name string) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "unverified entry for commands test",
		Type:         MemoryTypeFeedback,
		CreatedAt:    time.Now(),
		Content:      "unverified content",
		TrustLevel:   TrustProcedural,
		Verification: VerificationUnverified,
		Importance:   60,
		Source:       SourceReflection,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write unverified entry %q 失败: %v", name, err)
	}
}

// writeVerifiedEntryForCmd 写入 verified 记忆。
func writeVerifiedEntryForCmd(t *testing.T, store *MemoryStore, name string) {
	t.Helper()
	entry := &MemoryEntry{
		Name:         name,
		Description:  "verified entry for commands test",
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      "verified content",
		TrustLevel:   TrustStable,
		Verification: VerificationVerified,
		Importance:   80,
		Source:       SourceUser,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write verified entry %q 失败: %v", name, err)
	}
}

// cleanupProbeKey 测试后清理注册的 ProbeFn（避免跨测试污染 globalRegistry）。
func cleanupProbeKey(t *testing.T, key string) {
	t.Helper()
	t.Cleanup(func() {
		_ = UnregisterProbe(key)
	})
}

// TestCommands_confirm 致命反例（unverified→verified）：
// /memory confirm foo → verification=verified + 持久化到文件。
func TestCommands_confirm(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cmd-confirm")
	writeUnverifiedEntryForCmd(t, ml.userStore, "cmd-confirm-entry")

	result, err := HandleConfirm("cmd-confirm-entry")
	if err != nil {
		t.Fatalf("HandleConfirm 失败: %v", err)
	}
	if !strings.Contains(result, "confirmed") {
		t.Errorf("result 不含 confirmed: %q", result)
	}
	if !strings.Contains(result, VerificationVerified) {
		t.Errorf("result 不含 %q: %q", VerificationVerified, result)
	}

	// 验证文件已持久化
	readEntry, err := ml.userStore.Read("cmd-confirm-entry")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationVerified {
		t.Errorf("致命反例失效: verification = %q, 期望 %q", readEntry.Verification, VerificationVerified)
	}
}

// TestCommands_confirm_already_verified 验证 verified→verified 无效流转返回 error。
func TestCommands_confirm_already_verified(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cmd-confirm-verified")
	writeVerifiedEntryForCmd(t, ml.userStore, "already-verified")

	_, err := HandleConfirm("already-verified")
	if err == nil {
		t.Fatal("HandleConfirm(already-verified) 应返回 error（verified→verified 无效流转）, 实际 nil")
	}
	if !errors.Is(err, ErrInvalidTransition) && !strings.Contains(err.Error(), ErrInvalidTransition.Error()) {
		t.Errorf("error 应含 ErrInvalidTransition: %v", err)
	}
}

// TestCommands_confirm_not_found 验证 name 不存在返回 error。
func TestCommands_confirm_not_found(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-confirm-not-found")

	_, err := HandleConfirm("nonexistent")
	if err == nil {
		t.Fatal("HandleConfirm(nonexistent) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}
}

// TestCommands_confirm_empty_name 验证空 name 返回 Usage。
func TestCommands_confirm_empty_name(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-confirm-empty")

	result, err := HandleConfirm("")
	if err != nil {
		t.Fatalf("HandleConfirm(空) 不应返回 error: %v", err)
	}
	if !strings.Contains(result, "Usage") {
		t.Errorf("result 应含 Usage: %q", result)
	}
}

// TestCommands_probe_add 验证注册成功。
func TestCommands_probe_add(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-probe-add")
	cleanupProbeKey(t, "gcc-version")

	// H2 未注入，走 basicDangerCheck，"gcc --version" 是安全的
	result, err := HandleProbeAdd("gcc-version gcc --version")
	if err != nil {
		t.Fatalf("HandleProbeAdd 失败: %v", err)
	}
	if !strings.Contains(result, "registered") {
		t.Errorf("result 不含 registered: %q", result)
	}
	if !strings.Contains(result, "gcc-version") {
		t.Errorf("result 不含 key: %q", result)
	}
	if !strings.Contains(result, "gcc --version") {
		t.Errorf("result 不含 command: %q", result)
	}
}

// TestCommands_probe_add_h2_blocked 致命反例（H2 校验拦截）：
// /memory probe add rm_root "rm -rf /" → H2 黑名单拦截，拒绝注册。
func TestCommands_probe_add_h2_blocked(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-probe-h2")
	// 显式设置 h2Checker = nil，走 basicDangerCheck 降级（basicDangerCheck 能拦截 "rm -rf /"）
	h2Checker = nil

	_, err := HandleProbeAdd("rm_root rm -rf /")
	if err == nil {
		t.Fatal("致命反例失效: HandleProbeAdd 应拒绝 rm -rf /, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrProbeH2Blocked.Error()) {
		t.Errorf("error 应含 ErrProbeH2Blocked: %v", err)
	}

	// 验证 ProbeFn 未被注册
	_, existsErr := executeProbeForTest("rm_root")
	if existsErr == nil {
		t.Errorf("致命反例失效: rm_root 被注册到 globalRegistry, 期望拒绝")
	}
}

// executeProbeForTest 测试用执行 ProbeFn（验证是否注册）。
// 复用 executeProbe 但不实际执行 shell（仅检查是否注册）。
func executeProbeForTest(key string) (interface{}, error) {
	globalRegistry.mu.RLock()
	_, ok := globalRegistry.probes[key]
	globalRegistry.mu.RUnlock()
	if !ok {
		return nil, errors.New("probe not registered")
	}
	return nil, nil
}

// TestCommands_probe_add_empty_args 验证空 args 返回 Usage。
func TestCommands_probe_add_empty_args(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-probe-empty")

	cases := []struct {
		args, desc string
	}{
		{"", "完全空"},
		{"   ", "纯空格"},
		{"only-key", "仅有 key 无 command"},
		{"only-key ", "key + 单空格"},
	}
	for _, c := range cases {
		result, err := HandleProbeAdd(c.args)
		if err != nil {
			t.Errorf("HandleProbeAdd(%q) 不应返回 error (%s): %v", c.args, c.desc, err)
			continue
		}
		if !strings.Contains(result, "Usage") {
			t.Errorf("HandleProbeAdd(%q) 应返回 Usage (%s), 实际: %q", c.args, c.desc, result)
		}
	}
}

// TestCommands_probe_no_subcommand 验证 probe 单独返回 Usage。
func TestCommands_probe_no_subcommand(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-probe-noop")

	cases := []struct {
		args, desc string
	}{
		{"", "完全空"},
		{"unknown", "未知子命令"},
	}
	for _, c := range cases {
		result, err := HandleProbe(c.args)
		if err != nil {
			t.Errorf("HandleProbe(%q) 不应返回 error (%s): %v", c.args, c.desc, err)
			continue
		}
		if !strings.Contains(result, "Usage") {
			t.Errorf("HandleProbe(%q) 应返回 Usage (%s), 实际: %q", c.args, c.desc, result)
		}
	}
}

// TestCommands_invalidate 验证 /memory invalidate 调用成功。
func TestCommands_invalidate(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cmd-invalidate")
	writeVerifiedEntryForCmd(t, ml.userStore, "cmd-invalidate-entry")

	result, err := HandleInvalidate("cmd-invalidate-entry outdated library")
	if err != nil {
		t.Fatalf("HandleInvalidate 失败: %v", err)
	}
	if !strings.Contains(result, "invalidated") {
		t.Errorf("result 不含 invalidated: %q", result)
	}
	if !strings.Contains(result, "cmd-invalidate-entry") {
		t.Errorf("result 不含 name: %q", result)
	}
	if !strings.Contains(result, "outdated library") {
		t.Errorf("result 不含 reason: %q", result)
	}

	// 验证文件已写入
	readEntry, err := ml.userStore.Read("cmd-invalidate-entry")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.Verification != VerificationContradicted {
		t.Errorf("verification = %q, 期望 %q", readEntry.Verification, VerificationContradicted)
	}
}

// TestCommands_invalidate_not_found 验证 name 不存在返回 error。
func TestCommands_invalidate_not_found(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-invalidate-not-found")

	_, err := HandleInvalidate("nonexistent some reason")
	if err == nil {
		t.Fatal("HandleInvalidate(nonexistent) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), ErrEntryNotFound.Error()) {
		t.Errorf("error 应含 ErrEntryNotFound: %v", err)
	}
}

// TestCommands_invalidate_empty_name 验证空 name 返回 Usage。
func TestCommands_invalidate_empty_name(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-invalidate-empty")

	result, err := HandleInvalidate("")
	if err != nil {
		t.Fatalf("HandleInvalidate(空) 不应返回 error: %v", err)
	}
	if !strings.Contains(result, "Usage") {
		t.Errorf("result 应含 Usage: %q", result)
	}
}

// TestCommands_dispatch_fallback 验证未匹配子命令时 handled=false（回退 N12）。
func TestCommands_dispatch_fallback(t *testing.T) {
	_, _ = setupCommandsTest(t, "/test/work/cmd-dispatch")

	cases := []struct {
		args, desc string
	}{
		{"list", "list 子命令回退 N12"},
		{"edit foo.md", "edit 子命令回退 N12"},
		{"show foo", "show 子命令回退 N12"},
		{"add", "add 子命令回退 N12"},
		{"delete foo", "delete 子命令回退 N12"},
		{"unknown-subcommand", "未知子命令回退 N12"},
		{"", "空 args handled=false"},
	}
	for _, c := range cases {
		handled, _, _ := DispatchMemorySubcommand(c.args)
		if handled {
			t.Errorf("DispatchMemorySubcommand(%q) (%s): handled=true, 期望 false（回退 N12）", c.args, c.desc)
		}
	}
}

// TestCommands_dispatch_handled 验证 H11/N27 子命令 handled=true。
func TestCommands_dispatch_handled(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cmd-dispatch-handled")
	writeUnverifiedEntryForCmd(t, ml.userStore, "dispatch-test")

	cases := []struct {
		args, desc string
	}{
		{"confirm dispatch-test", "confirm handled=true"},
		{"invalidate dispatch-test test", "invalidate handled=true"},
	}
	for _, c := range cases {
		handled, _, err := DispatchMemorySubcommand(c.args)
		if !handled {
			t.Errorf("DispatchMemorySubcommand(%q) (%s): handled=false, 期望 true, err=%v", c.args, c.desc, err)
		}
	}

	// probe 子命令也应 handled=true（即使参数不全返回 Usage）
	handled, _, _ := DispatchMemorySubcommand("probe")
	if !handled {
		t.Error("DispatchMemorySubcommand(probe) handled=false, 期望 true")
	}
}

// TestCommands_registration_via_command_list 致命反例（RegisterCommands 注入回调）：
// 验证 RegisterCommands 注入 r.OnMemory 回调，confirm/probe/invalidate 子命令可路由。
func TestCommands_registration_via_command_list(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cmd-registration")
	writeUnverifiedEntryForCmd(t, ml.userStore, "reg-test-entry")

	// 构造 Registry + 注入 N12 模拟 OnMemory（返回 "not connected" 表示 N12 未处理）
	// 注：Registry 通过嵌入 HandlerCallbacks 暴露 OnMemory 字段，struct literal 不能直接赋值嵌入字段
	r := &command.Registry{}
	r.OnMemory = func(args string) (string, error) {
		return "N12 fallback: " + args, nil
	}

	// 注册 H11/N27 子命令
	if err := RegisterCommands(r); err != nil {
		t.Fatalf("RegisterCommands 失败: %v", err)
	}

	// 验证 r.OnMemory 已被包装（不再是原函数）
	if r.OnMemory == nil {
		t.Fatal("致命反例失效: r.OnMemory 未被注入")
	}

	// 验证 confirm 子命令被路由到 H11/N27（不返回 N12 fallback）
	result, err := r.OnMemory("confirm reg-test-entry")
	if err != nil {
		t.Fatalf("r.OnMemory(confirm) 失败: %v", err)
	}
	if strings.Contains(result, "N12 fallback") {
		t.Errorf("致命反例失效: confirm 被回退到 N12, 实际: %q", result)
	}
	if !strings.Contains(result, "confirmed") {
		t.Errorf("致命反例失效: result 不含 confirmed: %q", result)
	}

	// 验证未匹配子命令回退到 N12
	result, err = r.OnMemory("list")
	if err != nil {
		t.Fatalf("r.OnMemory(list) 失败: %v", err)
	}
	if !strings.Contains(result, "N12 fallback") {
		t.Errorf("致命反例失效: list 未回退到 N12, 实际: %q", result)
	}
}

// TestCommands_register_commands_nil_registry 验证 nil registry 返回 error。
func TestCommands_register_commands_nil_registry(t *testing.T) {
	err := RegisterCommands(nil)
	if err == nil {
		t.Fatal("RegisterCommands(nil) 应返回 error, 实际 nil")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error 应含 nil: %v", err)
	}
}

// TestCapability_force_project_scope 致命反例（PIT-001 重现）：
// trust_level=capability 的记忆写入 user scope 必须被拒绝。
// 理由：capability 记录项目特定能力探测（gcc/git 可用性），写入 _user/ 会在换机器/换项目后失效被当真（PIT-001）。
func TestCapability_force_project_scope(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cap-pit001")

	// capability 类型记忆尝试写入 user scope，必须被拒绝
	capEntry := &MemoryEntry{
		Name:         "gcc-available",
		Description:  "gcc 编译器可用性探测（项目特定）",
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      "gcc --version 输出 13.2.0",
		TrustLevel:   TrustCapability, // ← 关键：capability 类型
		Verification: VerificationVerified,
		ProbeFn:      BuiltinProbeGccAvailable,
		Importance:   90,
		Source:       SourceProbeFn,
	}
	err := ml.userStore.Write(capEntry)
	if err == nil {
		t.Fatal("致命反例失效: trust_level=capability 写 user scope 应被拒绝 (PIT-001 防护), 实际成功")
	}
	if !errors.Is(err, ErrCapabilityMustBeProjectScope) {
		t.Errorf("error 应含 ErrCapabilityMustBeProjectScope: %v", err)
	}

	// 验证文件未被写入（拒绝前不应有副作用）
	_, readErr := ml.userStore.Read("gcc-available")
	if readErr == nil {
		t.Errorf("致命反例失效: 拒绝后文件不应存在, 实际写入成功")
	}
}

// TestCapability_in_project_scope_success 正向对照：
// trust_level=capability 的记忆写入 project scope 应成功（workDir hash 隔离不同项目）。
func TestCapability_in_project_scope_success(t *testing.T) {
	_, ml := setupCommandsTest(t, "/test/work/cap-project-ok")

	capEntry := &MemoryEntry{
		Name:         "git-available",
		Description:  "git 命令可用性探测（项目特定）",
		Type:         MemoryTypeUser,
		CreatedAt:    time.Now(),
		Content:      "git version 2.43.0",
		TrustLevel:   TrustCapability,
		Verification: VerificationVerified,
		ProbeFn:      BuiltinProbeGitAvailable,
		Importance:   90,
		Source:       SourceProbeFn,
	}
	if err := ml.projectStore.Write(capEntry); err != nil {
		t.Fatalf("capability 写 project scope 应成功, 实际失败: %v", err)
	}

	// 验证可读回
	readEntry, err := ml.projectStore.Read("git-available")
	if err != nil {
		t.Fatalf("Read 失败: %v", err)
	}
	if readEntry.TrustLevel != TrustCapability {
		t.Errorf("TrustLevel = %q, 期望 %q", readEntry.TrustLevel, TrustCapability)
	}
}
