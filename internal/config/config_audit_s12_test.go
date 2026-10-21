// config_audit_s12_test.go — S12 配置加载 / settings 持久化 审计 characterization 测试
//
// 目的：用测试钉死 whale-code 在 I2（fail-closed）/ I6（原子 & 安全持久化）上的
// *当前* 行为。本文件只新增测试、不改源码；所有用例针对 current source 必须 GREEN。
// 一旦进入 Fix 阶段修改行为，对应用例应随之翻转（红→绿）。
//
// 变更记录：
// - S12 Fix PR#1：G6（迁移备份 0644→0600）+ G5b（APIKey omitempty 透传）。
// - S12 Fix PR#2（2026-09-14，Q1/Q4/Q5=A）：G1（损坏配置拒启/报错）、G2（Load 纯只读，
// 恢复改显式 RestoreFromBackup）、G7（env 仅收紧不得放宽）。对应用例已随之翻转。
//
// 对应不变量：
// - I2 默认拒绝：配置损坏 / 缺失 / 含非法字段时，是否 fail-closed。
// - I6 持久化原子 + Fail-Safe：落盘是否原子（temp+rename）、权限 0600、
// 损坏/无效配置是否回退到最严而非最松。
package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearConfigEnv 清空所有 WHALE_ 环境变量，避免 applyEnvironmentOverrides 干扰断言。
func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, e := range os.Environ() {
		if i := strings.IndexByte(e, '='); i > 0 {
			if strings.HasPrefix(e[:i], "WHALE_") {
				os.Unsetenv(e[:i])
			}
		}
	}
}

// G1 (FIXED — PR#2, Q1=A)：损坏的 settings.json（非法 JSON）必须让 Load **返回 error**
// （ErrCorruptConfig），绝不静默回退到宽松默认（对齐业内既有实现 拒启 / 同类 CLI exit(1)）。
// 反例守卫：若把 Load 改回「损坏即 continue + 返回 nil」，本用例立即变红。
func TestS12_Load_CorruptedConfigRefusesWithError(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 写一份非法 JSON
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"),
		[]byte("{ this is not valid json ,,, "), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err == nil {
		t.Fatalf("损坏配置必须让 Load 返回 error（fail-closed），但返回了 nil；cfg=%+v", cfg)
	}
	if !errors.Is(err, ErrCorruptConfig) {
		t.Errorf("error 应为 ErrCorruptConfig，实际：%v", err)
	}
	if cfg != nil {
		t.Errorf("配置损坏时 Load 不应返回可用配置，实际非 nil：%+v", cfg)
	}
}

// G1' 反例守卫（判别性）：**文件缺失**不得被当作损坏 —— 全新安装必须能正常 Load 成功。
// 若把「缺失」也当作错误，本用例变红。
func TestS12_Load_MissingConfigIsNotCorruption(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user") // 目录不存在任何配置文件

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("配置文件缺失不应报错（全新安装合法），实际：%v", err)
	}
	if cfg == nil {
		t.Fatal("缺失配置时应返回默认配置")
	}
}

// G2 (FIXED — PR#2, Q4=A)：Load 是**纯只读**。即便存在合法 .bak，Load 也**不得**改写源文件、
// 不得消费 .bak —— 它只应报「损坏」并交由显式恢复流程（whale doctor --repair → RestoreFromBackup）。
// 反例守卫：若 Load 重新引入写副作用（回填源 + 删 .bak），本用例立即变红。
func TestS12_Load_CorruptedConfigWithValidBackup_IsReadOnlyAndErrors(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	bak := src + ".20260101-000000.bak"
	corrupt := "{ broken "
	if err := os.WriteFile(src, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bak, []byte(`{"permission_mode":"read-only"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err == nil {
		t.Fatalf("损坏配置即使有备份，Load 也应返回 error（恢复须显式），实际 nil；cfg=%+v", cfg)
	}
	if !errors.Is(err, ErrCorruptConfig) {
		t.Errorf("error 应为 ErrCorruptConfig，实际：%v", err)
	}
	// 源文件必须保持损坏（Load 未改写）
	got, rerr := os.ReadFile(src)
	if rerr != nil {
		t.Fatalf("read source: %v", rerr)
	}
	if string(got) != corrupt {
		t.Errorf("Load 不得改写源文件（纯只读）；实际被改为 %q", string(got))
	}
	// .bak 必须仍存在（未被消费）
	if _, serr := os.Stat(bak); serr != nil {
		t.Errorf("Load 不得消费 .bak（应留给显式恢复流程）；实际已被删除：%v", serr)
	}
}

// G2' 显式恢复（PR#2 新增能力）：RestoreFromBackup 是**显式**恢复入口，含写副作用
// （原子改写源文件 + 消费已用的 .bak）。它是 Q4=A 中「自动恢复改显式流程」的落地机制。
func TestS12_RestoreFromBackup_ExplicitlyRestores(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	bak := src + ".20260101-000000.bak"
	if err := os.WriteFile(src, []byte("{ broken "), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bak, []byte(`{"permission_mode":"read-only"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	ok, err := l.RestoreFromBackup(src)
	if err != nil {
		t.Fatalf("RestoreFromBackup: %v", err)
	}
	if !ok {
		t.Fatal("RestoreFromBackup 应报告恢复成功（true）")
	}
	// 源文件被备份内容原子改写为有效 JSON
	got, rerr := os.ReadFile(src)
	if rerr != nil {
		t.Fatalf("read restored source: %v", rerr)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(got, &raw); err != nil {
		t.Fatalf("恢复后源文件不是合法 JSON：%v", err)
	}
	if raw["permission_mode"] != "read-only" {
		t.Errorf("恢复后 permission_mode=%v, want read-only", raw["permission_mode"])
	}
	// 已用的 .bak 被消费
	if _, serr := os.Stat(bak); !os.IsNotExist(serr) {
		t.Errorf("恢复成功后 .bak 应被消费（删除），实际仍存在")
	}
}

// G2” 反例守卫：无任何 .bak 时 RestoreFromBackup 必须报告 (false, nil)，不得报错、不得改盘。
func TestS12_RestoreFromBackup_NoBackupReturnsFalse(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	corrupt := "{ broken "
	if err := os.WriteFile(src, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	ok, err := l.RestoreFromBackup(src)
	if err != nil {
		t.Fatalf("无备份不应报错，实际：%v", err)
	}
	if ok {
		t.Error("无备份时 RestoreFromBackup 应返回 false")
	}
	if got, _ := os.ReadFile(src); string(got) != corrupt {
		t.Errorf("无备份时不得改盘；源被改为 %q", string(got))
	}
}

// CheckIntegrity（PR#2 新增）：启动前置**只读**完整性预检 —— 损坏即报 ErrCorruptConfig，
// 且绝不改盘、不消费 .bak。它是「在 onboarding 向导写盘之前先 fail-closed」的依据。
func TestS12_CheckIntegrity_ReadOnlyPrecheck(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(whaleDir, "settings.json")
	bak := src + ".20260101-000000.bak"
	corrupt := "{ broken "
	if err := os.WriteFile(src, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bak, []byte(`{"permission_mode":"read-only"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	if err := l.CheckIntegrity(); !errors.Is(err, ErrCorruptConfig) {
		t.Fatalf("CheckIntegrity 应报 ErrCorruptConfig，实际：%v", err)
	}
	// 纯只读：源仍损坏、.bak 仍在
	if got, _ := os.ReadFile(src); string(got) != corrupt {
		t.Errorf("CheckIntegrity 不得改盘；源被改为 %q", string(got))
	}
	if _, serr := os.Stat(bak); serr != nil {
		t.Errorf("CheckIntegrity 不得消费 .bak；实际已删除：%v", serr)
	}

	// 反例守卫：配置正常（或缺失）时必须返回 nil
	if err := os.WriteFile(src, []byte(`{"permission_mode":"workspace-write"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := l.CheckIntegrity(); err != nil {
		t.Errorf("合法配置时 CheckIntegrity 应返回 nil，实际：%v", err)
	}
}

// G3: 非法 permission_mode（如 "bogus"）在 Validate 中被回退到最严的 read-only，
//
//	且 Load 仍返回 nil error（校验仅 log，不向上传播）。
func TestS12_Load_InvalidPermissionMode_FailClosedToReadOnly(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"),
		[]byte(`{"permission_mode":"bogus"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("expected nil error (validation only logged), got %v", err)
	}
	if cfg.PermissionMode != PermReadOnly {
		t.Errorf("invalid permission_mode should fail-closed to read-only, got %s", cfg.PermissionMode)
	}
}

// G4 (good path): Save 使用原子写（temp+rename）、文件权限 0600、并在「覆盖已有文件」时产生 0600 的 .bak 备份，
//
//	且 api_key 等敏感字段被持久化。
//
// 注：.bak 仅在一次 Save 覆盖已存在文件时生成（首次写入不备份），故此处做两次 Save。
func TestS12_Save_AtomicWriteAndPerms(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)

	cfg := DefaultConfig()
	cfg.APIKey = "super-secret-token"

	l := NewLoader(userDir, "")
	// 第一次 Save：创建文件（不生成 .bak）
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// 第二次 Save：覆盖已有文件（生成 .bak）
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	settingsPath := filepath.Join(whaleDir, "settings.json")
	fi, err := os.Stat(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0600 {
		t.Errorf("settings.json perm = %o, want 0600", mode)
	}
	data, _ := os.ReadFile(settingsPath)
	if !strings.Contains(string(data), "super-secret-token") {
		t.Errorf("api_key not persisted to settings.json")
	}
	// 覆盖写入后应存在 .bak，且权限 0600
	matches, _ := filepath.Glob(settingsPath + ".*.bak")
	if len(matches) == 0 {
		t.Fatalf("expected at least one .bak backup after overwrite")
	}
	bi, berr := os.Stat(matches[0])
	if berr != nil {
		t.Fatalf("stat backup: %v", berr)
	}
	if mode := bi.Mode().Perm(); mode != 0600 {
		t.Errorf("backup perm = %o, want 0600", mode)
	}
}

// G5a (good path): Save 时若新配置未携带「非结构体字段」的 auth 标记（如 oauth_token），
//
//	应透传磁盘上已有的该字段，避免保存操作丢失登录态。
func TestS12_Save_PreservesExistingOAuthToken(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	// 收口后 Loader.Save 的目标目录从 whalehome.Dir()（$WHALE_HOME > $HOME/.whale）解析，
	// 不再取 l.userDir ⇒ 只传 NewLoader(userDir, "") 不再构成隔离。
	// 缺这行时本用例会写到宿主的**真实** ~/.whale/settings.json（实测覆写用户 model / api_key）。
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 预置一份含 oauth_token 的 settings.json（oauth_token 不是 Config 结构体字段）
	existing := `{"oauth_token":"tok-123","permission_mode":"workspace-write"}`
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	// 新配置不含 oauth_token（Config 结构体无此字段；模拟一次普通配置保存）
	cfg := DefaultConfig()
	cfg.PermissionMode = PermWorkspaceWrite

	l := NewLoader(userDir, "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(whaleDir, "settings.json"))
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved settings invalid: %v", err)
	}
	if raw["oauth_token"] != "tok-123" {
		t.Errorf("oauth_token not preserved: %v", raw["oauth_token"])
	}
}

// G5b (FIXED — S12 Fix PR#1): Save 时若内存 cfg.APIKey 为空字符串，因 APIKey 的 json tag
//
//	带 omitempty，空值不被序列化，从而触发 preserveAuthFields 把磁盘上已有的 api_key
//	回填 → 不再被空串清空。
//	修复前（Gap 形态）：磁盘 api_key 被覆盖为 ""（一次无关保存即丢密钥）。
//	修复后（本用例断言）：磁盘 api_key 保留 old-secret。
func TestS12_Save_PreservesStructFieldAPIKeyWhenEmpty(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	// 同上 —— 无 WHALE_HOME 时 Save 会落到宿主真实 ~/.whale/settings.json。
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	// 预置一份含真实 api_key 的 settings.json
	existing := `{"api_key":"old-secret","permission_mode":"workspace-write"}`
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	// 模拟 `/config set permission_mode X`：用 DefaultConfig 仅改一项后保存
	cfg := DefaultConfig()
	cfg.PermissionMode = PermWorkspaceWrite
	// cfg.APIKey 此时为空（默认）——修复后序列化不含 api_key，走透传保留磁盘值

	l := NewLoader(userDir, "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(whaleDir, "settings.json"))
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved settings invalid: %v", err)
	}
	if raw["api_key"] != "old-secret" {
		t.Errorf("api_key not preserved: got %v, want old-secret (S12-G5b fix)", raw["api_key"])
	}
}

// G5b 反例（判别性守卫 — S12 Fix PR#1）：修复不得变成「无条件保留旧值」。
//
//	当内存里存在一个新的非空 api_key 时，Save 必须写入新值（覆盖磁盘旧值），
//	否则该修复会把用户更新密钥的操作也吞掉。此用例注入反例，确保守卫可被打红。
func TestS12_Save_NewNonEmptyAPIKeyOverwritesOld(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	existing := `{"api_key":"old-secret","permission_mode":"workspace-write"}`
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"), []byte(existing), 0600); err != nil {
		t.Fatal(err)
	}

	cfg := DefaultConfig()
	cfg.APIKey = "new-secret" // 显式更新密钥

	l := NewLoader(userDir, "")
	if err := l.Save(&cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(whaleDir, "settings.json"))
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("saved settings invalid: %v", err)
	}
	if raw["api_key"] != "new-secret" {
		t.Errorf("Save must overwrite with new non-empty api_key: got %v, want new-secret", raw["api_key"])
	}
}

// G6 (FIXED — S12 Fix PR#1): migrateWithBackup 写入的备份权限为 0600（与 Save 路径一致），
//
//	避免含 api_key / oauth_token / bridge.jwt_secret 的备份被同机其他用户读取。
//	修复前：0644（Gap）；修复后（本用例断言）：0600，且备份内容保持原样。
func TestS12_MigrateWithBackup_WritesBackupAt0600(t *testing.T) {
	clearConfigEnv(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "settings.json")
	content := `{"permission_mode":"workspace-write","api_key":"topsecret"}`
	if err := os.WriteFile(src, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	bakPath, err := migrateWithBackup(src)
	if err != nil {
		t.Fatalf("migrateWithBackup: %v", err)
	}
	if bakPath == "" {
		t.Fatal("expected a backup path")
	}
	bi, berr := os.Stat(bakPath)
	if berr != nil {
		t.Fatalf("stat backup: %v", berr)
	}
	if mode := bi.Mode().Perm(); mode != 0600 {
		t.Errorf("migrate backup perm = %o, want 0600 (S12-G6 fix)", mode)
	}
	// 改用原子写后，备份内容必须与源一致（不得因 temp+rename 而损坏/截断）
	got, rerr := os.ReadFile(bakPath)
	if rerr != nil {
		t.Fatalf("read backup: %v", rerr)
	}
	if string(got) != content {
		t.Errorf("backup content changed: got %q, want %q", string(got), content)
	}
}

// G7 (FIXED — PR#2, Q5=A)：env WHALE_PERMISSION_MODE **只能收紧或持平**，不得凌驾 settings
// 放宽到更松的模式。对齐业内既有实现/同类 CLI：放宽权限必须走显式 CLI flag，env 不是放宽入口。
// 反例守卫：若 env 重新变为「无条件覆盖」，settings=read-only + env=danger 会变红。
func TestS12_Load_EnvPermissionModeCannotLoosen(t *testing.T) {
	clearConfigEnv(t)
	os.Setenv("WHALE_PERMISSION_MODE", "danger-full-access")
	defer os.Unsetenv("WHALE_PERMISSION_MODE")

	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"),
		[]byte(`{"permission_mode":"read-only"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PermissionMode != PermReadOnly {
		t.Errorf("env 不得放宽权限：settings=read-only + env=danger 应保持 read-only，实际 %s",
			cfg.PermissionMode)
	}
}

// G7' env 可以「收紧」：settings=danger-full-access + env=read-only → read-only。
func TestS12_Load_EnvPermissionModeCanTighten(t *testing.T) {
	clearConfigEnv(t)
	os.Setenv("WHALE_PERMISSION_MODE", "read-only")
	defer os.Unsetenv("WHALE_PERMISSION_MODE")

	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	// Load 也从 whalehome.Dir() 读 settings.json ⇒ 无此行时本用例读的是宿主真实配置，
	// 夹具被忽略（假通过）。注入后夹具才真正参与判定。
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"),
		[]byte(`{"permission_mode":"danger-full-access"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PermissionMode != PermReadOnly {
		t.Errorf("env 应可收紧：settings=danger + env=read-only → read-only，实际 %s", cfg.PermissionMode)
	}
}

// G7” env 非法值应被**忽略**（不写入），交由 Validate 保持/回退，绝不放宽。
func TestS12_Load_EnvPermissionModeInvalidIsIgnored(t *testing.T) {
	clearConfigEnv(t)
	os.Setenv("WHALE_PERMISSION_MODE", "definitely-bogus")
	defer os.Unsetenv("WHALE_PERMISSION_MODE")

	dir := t.TempDir()
	userDir := filepath.Join(dir, "user")
	whaleDir := filepath.Join(userDir, ".whale")
	// 同上 —— 无此行时 Load 读宿主真实 settings.json，夹具被忽略（假通过）。
	t.Setenv("WHALE_HOME", whaleDir)
	if err := os.MkdirAll(whaleDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(whaleDir, "settings.json"),
		[]byte(`{"permission_mode":"workspace-write"}`), 0600); err != nil {
		t.Fatal(err)
	}

	l := NewLoader(userDir, "")
	cfg, err := l.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PermissionMode != PermWorkspaceWrite {
		t.Errorf("非法 env 应被忽略，settings 保持 workspace-write，实际 %s", cfg.PermissionMode)
	}
}
