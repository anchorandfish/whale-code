package sandbox

// Manager/EnvConfig/AuditLog 测试（B1-B11 验证）

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSandboxLevel_String 验证 SandboxLevel.String()。
func TestSandboxLevel_String(t *testing.T) {
	tests := []struct {
		level SandboxLevel
		str   string
	}{
		{SandboxLevelDisabled, "disabled"},
		{SandboxLevelRelaxed, "relaxed"},
		{SandboxLevelStrict, "strict"},
		{SandboxLevelIsolated, "isolated"},
		{SandboxLevel(99), "unknown(99)"},
	}
	for _, tc := range tests {
		if got := tc.level.String(); got != tc.str {
			t.Errorf("level %d: expected %q, got %q", int(tc.level), tc.str, got)
		}
	}
}

// TestParseSandboxMode 验证 ParseSandboxMode。
func TestParseSandboxMode(t *testing.T) {
	tests := []struct {
		input   string
		level   SandboxLevel
		wantErr bool
	}{
		{"disabled", SandboxLevelDisabled, false},
		{"off", SandboxLevelDisabled, false},
		{"none", SandboxLevelDisabled, false},
		{"", SandboxLevelDisabled, false},
		{"relaxed", SandboxLevelRelaxed, false},
		{"strict", SandboxLevelStrict, false},
		{"isolated", SandboxLevelIsolated, false},
		{"RELAXED", SandboxLevelRelaxed, false}, // case insensitive
		{"unknown", SandboxLevelDisabled, true},
	}
	for _, tc := range tests {
		level, err := ParseSandboxMode(tc.input)
		if tc.wantErr && err == nil {
			t.Errorf("expected error for %q", tc.input)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("unexpected error for %q: %v", tc.input, err)
		}
		if !tc.wantErr && level != tc.level {
			t.Errorf("for %q: expected level %d, got %d", tc.input, int(tc.level), int(level))
		}
	}
}

// TestDecrement 验证 Decrement 带下限保护。
func TestDecrement(t *testing.T) {
	if Decrement(SandboxLevelIsolated) != SandboxLevelStrict {
		t.Error("Isolated-1 should be Strict")
	}
	if Decrement(SandboxLevelRelaxed) != SandboxLevelDisabled {
		t.Error("Relaxed-1 should be Disabled")
	}
	// 下限保护：Disabled-1 应仍为 Disabled
	if Decrement(SandboxLevelDisabled) != SandboxLevelDisabled {
		t.Error("Disabled-1 should stay Disabled (lower bound)")
	}
}

// TestManager_Prepare 验证 Manager.Prepare 返回有效上下文。
func TestManager_Prepare(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelRelaxed)

	ctx := context.Background()
	sbCtx, cleanup, err := mgr.Prepare(ctx, SandboxLevelRelaxed, dir)
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer cleanup()

	if sbCtx.WorkDir == "" {
		t.Error("WorkDir should not be empty")
	}
	if sbCtx.TempDir == "" {
		t.Error("TempDir should not be empty")
	}
	if len(sbCtx.Env) == 0 {
		t.Error("Env should not be empty")
	}
	if sbCtx.CancelFunc == nil {
		t.Error("CancelFunc should not be nil")
	}
	if sbCtx.EffectiveLevel() != SandboxLevelRelaxed {
		t.Errorf("expected level Relaxed, got %s", sbCtx.EffectiveLevel())
	}
}

// TestManager_Prepare_Disabled 验证 Disabled 级别调用 Prepare 返回 error。
func TestManager_Prepare_Disabled(t *testing.T) {
	mgr := NewManager(nil, SandboxLevelDisabled)
	_, _, err := mgr.Prepare(context.Background(), SandboxLevelDisabled, "/tmp")
	if err == nil {
		t.Error("expected error when Prepare with Disabled level")
	}
}

// TestManager_Prepare_EmptyWorkDir 验证空 workDir 返回 error。
func TestManager_Prepare_EmptyWorkDir(t *testing.T) {
	mgr := NewManager(nil, SandboxLevelRelaxed)
	_, _, err := mgr.Prepare(context.Background(), SandboxLevelRelaxed, "")
	if err == nil {
		t.Error("expected error for empty workDir")
	}
}

// TestManager_Prepare_FailHard 验证 failHard 模式返回 error（B11）。
func TestManager_Prepare_FailHard(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelStrict, WithFailHard())
	_, _, err := mgr.Prepare(context.Background(), SandboxLevelStrict, dir)
	if err == nil {
		t.Error("expected error in FailHard mode (P0 has no OS sandbox)")
	}
}

// TestManager_ValidateCommand_Blocked 验证 ValidateCommand 返回 Blocked。
func TestManager_ValidateCommand_Blocked(t *testing.T) {
	mgr := NewManager(&SandboxConfig{
		WorkDir:      "/tmp/test",
		DenyCommands: []string{"rm"},
	}, SandboxLevelRelaxed)

	ctx := context.Background()
	result, err := mgr.ValidateCommand(ctx, "rm -rf /", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Blocked {
		t.Error("rm should be blocked")
	}
}

// TestManager_ValidateCommand_Allowed 验证安全命令不被阻止。
func TestManager_ValidateCommand_Allowed(t *testing.T) {
	mgr := NewManager(&SandboxConfig{
		WorkDir: "/tmp/test",
	}, SandboxLevelRelaxed)

	ctx := context.Background()
	result, err := mgr.ValidateCommand(ctx, "ls -la", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Blocked {
		t.Error("ls should not be blocked")
	}
}

// TestManager_ValidateCommand_AllowPriority 验证 deny-wins 安全铁律（R9 修复，对齐 sandbox.go 已修复版）。
// 当命令同时命中 allow 和 deny 时，deny 必须优先（curl 同时在 AllowCommands 和 DenyCommands 中应被拒绝）。
func TestManager_ValidateCommand_AllowPriority(t *testing.T) {
	mgr := NewManager(&SandboxConfig{
		WorkDir:       "/tmp/test",
		AllowCommands: []string{"curl"},
		DenyCommands:  []string{"curl"},
	}, SandboxLevelRelaxed)

	ctx := context.Background()
	result, err := mgr.ValidateCommand(ctx, "curl http://example.com", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// deny-wins：deny 命中后必须存在 DENYLIST 违规，无论 allow 是否命中
	denyHit := false
	for _, v := range result.Violations {
		if v.CheckID == "DENYLIST" {
			denyHit = true
			break
		}
	}
	if !denyHit {
		t.Error("deny-wins violated: curl in DenyCommands should produce DENYLIST violation even if also in AllowCommands (R9)")
	}
}

// TestManager_ValidatePath_WithinWorkDir 验证工作目录内路径放行。
func TestManager_ValidatePath_WithinWorkDir(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelRelaxed)

	err := mgr.ValidatePath(context.Background(), filepath.Join(dir, "test.txt"), nil)
	if err != nil {
		t.Errorf("path within workdir should be allowed: %v", err)
	}
}

// TestManager_ValidatePath_OutsideWorkDir 验证工作目录外路径拒绝。
func TestManager_ValidatePath_OutsideWorkDir(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelRelaxed)

	err := mgr.ValidatePath(context.Background(), "/etc/passwd", nil)
	if err == nil {
		t.Error("path outside workdir should be denied")
	}
}

// TestDefaultEnvConfig 验证默认环境变量配置（C15/B9）。
func TestDefaultEnvConfig(t *testing.T) {
	cfg := DefaultEnvConfig()

	expectedAllow := []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "TERM"}
	for _, name := range expectedAllow {
		found := false
		for _, a := range cfg.AllowList {
			if a == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("AllowList should contain %s", name)
		}
	}

	expectedDenyGlobs := []string{"*_API_KEY", "*_TOKEN", "*_SECRET", "AWS_*", "OPENAI_*", "ANTHROPIC_*"}
	for _, glob := range expectedDenyGlobs {
		found := false
		for _, d := range cfg.DenyList {
			if d == glob {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DenyList should contain %s", glob)
		}
	}

	if cfg.Set == nil {
		t.Error("Set should not be nil")
	}
}

// TestBuildEnv_Filtering 验证 BuildEnv 过滤敏感变量。
func TestBuildEnv_Filtering(t *testing.T) {
	// t.Setenv 由 testing 框架在测试结束时自动恢复原值。
	// 修复记录（P1-SEC）：修复前用 os.Setenv 且 defer 未恢复 PATH/HOME，
	// 进程级污染同包后续测试（macOS 上 /usr/bin 无 cat → e2e 对照组 exit 127，
	// darwin profile 的 HOME 敏感目录解析也被带偏）。
	t.Setenv("WHALE_TEST_API_KEY", "secret123")
	t.Setenv("WHALE_TEST_TOKEN", "tok456")
	t.Setenv("WHALE_TEST_SECRET", "sec789")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIATEST")
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("ANTHROPIC_API_KEY", "ant-test")
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("HOME", "/tmp/test")

	cfg := DefaultEnvConfig()
	env := BuildEnv(cfg)

	// Win10 真机 E2E 教训：Windows 上 os.Environ() 返回环境块的原始大小写
	//（PATH 变量为 "Path"），生产 BuildEnv 保留宿主大小写是正确行为
	//（Windows env 名大小写不敏感，改写反而破坏约定）。测试断言侧镜像
	// 生产的 envFoldCase 语义：仅 Windows 折叠为大写再查找；Unix 保持
	// 大小写敏感（捕获生产侧错误放行小写变体的回归）。
	envMap := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			name := parts[0]
			if runtime.GOOS == "windows" {
				name = strings.ToUpper(name)
			}
			envMap[name] = parts[1]
		}
	}

	// 允许的变量应在
	if _, ok := envMap["PATH"]; !ok {
		t.Error("PATH should be in env")
	}
	if _, ok := envMap["HOME"]; !ok {
		t.Error("HOME should be in env")
	}

	// 敏感变量不应在
	sensitive := []string{"WHALE_TEST_API_KEY", "WHALE_TEST_TOKEN", "WHALE_TEST_SECRET",
		"AWS_ACCESS_KEY_ID", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"}
	for _, name := range sensitive {
		if _, ok := envMap[name]; ok {
			t.Errorf("sensitive variable %s should NOT be in env", name)
		}
	}
}

// TestBuildEnv_SetOverride 验证 BuildEnv 的 Set 覆盖。
func TestBuildEnv_SetOverride(t *testing.T) {
	cfg := &EnvConfig{
		AllowList: []string{"PATH"},
		Set: map[string]string{
			"WHALE_SANDBOX": "true",
			"PATH":          "/sandbox/bin", // 覆盖宿主 PATH
		},
	}
	env := BuildEnv(cfg)

	envMap := make(map[string]string)
	for _, kv := range env {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	if envMap["WHALE_SANDBOX"] != "true" {
		t.Error("WHALE_SANDBOX should be set")
	}
	// 注意：Set 叠加在末尾，os.Environ 的 PATH 也在其中
	// 最后一项生效（exec.Cmd.Env 后出现的覆盖前面的）
	lastPath := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			lastPath = strings.TrimPrefix(kv, "PATH=")
		}
	}
	if lastPath != "/sandbox/bin" {
		t.Errorf("PATH should be overridden to /sandbox/bin, got %s", lastPath)
	}
}

// TestGlobMatch 验证 glob 匹配。
func TestGlobMatch(t *testing.T) {
	tests := []struct {
		pattern string
		name    string
		match   bool
	}{
		{"*_API_KEY", "OPENAI_API_KEY", true},
		{"*_TOKEN", "ACCESS_TOKEN", true},
		{"*_SECRET", "DB_SECRET", true},
		{"AWS_*", "AWS_ACCESS_KEY_ID", true},
		{"AWS_*", "OPENAI_API_KEY", false},
		{"PATH", "PATH", true},
		{"PATH", "PATHOLOGICAL", false},
		{"*", "ANYTHING", true},
	}
	for _, tc := range tests {
		got := globMatch(tc.pattern, tc.name)
		if got != tc.match {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.name, got, tc.match)
		}
	}
}

// TestAuditLog_Rotation 验证审计日志写入与轮转。
func TestAuditLog_Rotation(t *testing.T) {
	// 使用临时目录
	tmpDir := t.TempDir()
	al := &AuditLog{logDir: tmpDir}
	defer al.Close()

	// 写入若干条违规
	violations := []SecurityViolation{
		{CheckID: "TEST", Message: "test violation", Severity: SeverityHigh},
	}
	al.LogViolations("test cmd", violations, true)

	// 验证文件创建
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("read dir failed: %v", err)
	}
	if len(entries) == 0 {
		t.Error("audit log file should be created")
	}

	// 验证文件名格式
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "sandbox-audit-") && strings.HasSuffix(entry.Name(), ".log") {
			found = true
			break
		}
	}
	if !found {
		t.Error("audit log file name should match sandbox-audit-*.log pattern")
	}
}

// TestAuditLog_NilSafe 验证 nil AuditLog 不 panic。
func TestAuditLog_NilSafe(t *testing.T) {
	var al *AuditLog
	// 不应 panic
	al.LogViolations("test", nil, false)
	al.LogPathViolation("/etc/passwd", "/etc/passwd")
	al.LogDegradation(SandboxLevelStrict, SandboxLevelRelaxed, "test")
	al.Close()
}

// TestSandboxContext_Context 验证 SandboxContext.Context() 返回有效 context。
func TestSandboxContext_Context(t *testing.T) {
	// 零值 SandboxContext 返回 context.Background()
	var sc SandboxContext
	ctx := sc.Context()
	if ctx == nil {
		t.Error("Context() should not return nil")
	}

	// 带 _ctx 的返回派生 context
	dir := t.TempDir()
	mgr := NewManager(&SandboxConfig{WorkDir: dir}, SandboxLevelRelaxed)
	sbCtx, cleanup, err := mgr.Prepare(context.Background(), SandboxLevelRelaxed, dir)
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	defer cleanup()

	ctx2 := sbCtx.Context()
	if ctx2 == nil {
		t.Error("Context() should not return nil after Prepare")
	}
}
