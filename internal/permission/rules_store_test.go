package permission

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeSettingsFile(t *testing.T, path string, perms PermissionSettings) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	settings := map[string]interface{}{
		"permissions": perms,
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}
}

func readPermissionsFromFile(t *testing.T, path string) PermissionSettings {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	var raw struct {
		Permissions PermissionSettings `json:"permissions"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("failed to unmarshal settings: %v", err)
	}
	return raw.Permissions
}

func TestNewRuleStore(t *testing.T) {
	store := NewRuleStore()
	if store == nil {
		t.Fatal("NewRuleStore returned nil")
	}
	if store.settings == nil {
		t.Error("settings map is nil")
	}
	if store.paths == nil {
		t.Error("paths map is nil")
	}
}

func TestSetPathForSourceAndGetPathForSource(t *testing.T) {
	store := NewRuleStore()

	if got := store.GetPathForSource(SourceLocalSettings); got != "" {
		t.Errorf("expected empty path, got %q", got)
	}

	store.SetPathForSource(SourceLocalSettings, "/tmp/local.json")
	if got := store.GetPathForSource(SourceLocalSettings); got != "/tmp/local.json" {
		t.Errorf("expected /tmp/local.json, got %q", got)
	}

	if got := store.GetPathForSource(SourceUserSettings); got != "" {
		t.Errorf("expected empty path for unset source, got %q", got)
	}

	store.SetPathForSource(SourceLocalSettings, "/tmp/other.json")
	if got := store.GetPathForSource(SourceLocalSettings); got != "/tmp/other.json" {
		t.Errorf("expected /tmp/other.json after overwrite, got %q", got)
	}
}

func TestLoad_NonExistentFile(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	store.SetPathForSource(SourceLocalSettings, filepath.Join(tmpDir, "nonexistent.json"))

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}
	if ruleSet.Count() != 0 {
		t.Errorf("expected empty rule set, got %d rules", ruleSet.Count())
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	store := NewRuleStore()

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}
	if ruleSet.Count() != 0 {
		t.Errorf("expected empty rule set, got %d rules", ruleSet.Count())
	}
}

func TestLoad_ValidSettingsFile(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
		Deny:  []string{"Bash(rm:*)"},
		Ask:   []string{"Bash(git:*)"},
	})

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet.Count() != 3 {
		t.Fatalf("expected 3 rules, got %d", ruleSet.Count())
	}

	rules := ruleSet.List()
	foundAllow := false
	foundDeny := false
	foundAsk := false
	for _, r := range rules {
		if r.Behavior == RuleAllow && r.ToolName == "Bash" && r.RuleContent == "curl:*" {
			foundAllow = true
		}
		if r.Behavior == RuleDeny && r.ToolName == "Bash" && r.RuleContent == "rm:*" {
			foundDeny = true
		}
		if r.Behavior == RuleAsk && r.ToolName == "Bash" && r.RuleContent == "git:*" {
			foundAsk = true
		}
	}
	if !foundAllow {
		t.Error("allow rule not found")
	}
	if !foundDeny {
		t.Error("deny rule not found")
	}
	if !foundAsk {
		t.Error("ask rule not found")
	}
}

func TestLoad_InvalidJSON(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	if err := os.WriteFile(path, []byte("not json at all"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet.Count() != 0 {
		t.Errorf("expected empty rule set for invalid JSON, got %d rules", ruleSet.Count())
	}
}

func TestLoadAll_MergesFromAllSources(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()

	localPath := filepath.Join(tmpDir, "local.json")
	projectPath := filepath.Join(tmpDir, "project.json")
	userPath := filepath.Join(tmpDir, "user.json")

	store.SetPathForSource(SourceLocalSettings, localPath)
	store.SetPathForSource(SourceProjectSettings, projectPath)
	store.SetPathForSource(SourceUserSettings, userPath)

	writeSettingsFile(t, localPath, PermissionSettings{Allow: []string{"Bash(ls)"}})
	writeSettingsFile(t, projectPath, PermissionSettings{Deny: []string{"Bash(rm:*)"}})
	writeSettingsFile(t, userPath, PermissionSettings{Ask: []string{"Bash(git:*)"}})

	ruleSet, err := store.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet.Count() != 3 {
		t.Fatalf("expected 3 merged rules, got %d", ruleSet.Count())
	}
}

func TestSave_CreatesFileWithCorrectFormat(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "subdir", "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings})
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "rm:*", Behavior: RuleDeny, Source: SourceLocalSettings})

	if err := store.Save(SourceLocalSettings, ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Allow) != 1 || perms.Allow[0] != "Bash(curl:*)" {
		t.Errorf("expected allow [Bash(curl:*)], got %v", perms.Allow)
	}
	if len(perms.Deny) != 1 || perms.Deny[0] != "Bash(rm:*)" {
		t.Errorf("expected deny [Bash(rm:*)], got %v", perms.Deny)
	}
	if len(perms.Ask) != 0 {
		t.Errorf("expected empty ask, got %v", perms.Ask)
	}
}

func TestSave_SkipsCLIArg(t *testing.T) {
	store := NewRuleStore()
	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "ls", Behavior: RuleAllow, Source: SourceCLIArg})

	if err := store.Save(SourceCLIArg, ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSave_SkipsSession(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "session.json")
	store.SetPathForSource(SourceSession, path)

	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "ls", Behavior: RuleAllow, Source: SourceSession})

	if err := store.Save(SourceSession, ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("expected no file to be created for Session source")
	}
}

func TestSave_EmptyPath(t *testing.T) {
	store := NewRuleStore()
	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "ls", Behavior: RuleAllow, Source: SourceLocalSettings})

	if err := store.Save(SourceLocalSettings, ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveMerged_SavesBySource(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()

	localPath := filepath.Join(tmpDir, "local.json")
	projectPath := filepath.Join(tmpDir, "project.json")

	store.SetPathForSource(SourceLocalSettings, localPath)
	store.SetPathForSource(SourceProjectSettings, projectPath)

	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings})
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "rm:*", Behavior: RuleDeny, Source: SourceProjectSettings})

	if err := store.SaveMerged(ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	localPerms := readPermissionsFromFile(t, localPath)
	if len(localPerms.Allow) != 1 || localPerms.Allow[0] != "Bash(curl:*)" {
		t.Errorf("local: expected allow [Bash(curl:*)], got %v", localPerms.Allow)
	}

	projectPerms := readPermissionsFromFile(t, projectPath)
	if len(projectPerms.Deny) != 1 || projectPerms.Deny[0] != "Bash(rm:*)" {
		t.Errorf("project: expected deny [Bash(rm:*)], got %v", projectPerms.Deny)
	}
}

func TestSaveMerged_SkipsCLIArgAndSession(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()

	localPath := filepath.Join(tmpDir, "local.json")
	store.SetPathForSource(SourceLocalSettings, localPath)

	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "ls", Behavior: RuleAllow, Source: SourceCLIArg})
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "git:*", Behavior: RuleAsk, Source: SourceSession})
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings})

	if err := store.SaveMerged(ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	localPerms := readPermissionsFromFile(t, localPath)
	if len(localPerms.Allow) != 1 || localPerms.Allow[0] != "Bash(curl:*)" {
		t.Errorf("expected only local allow rule, got %v", localPerms.Allow)
	}
}

func TestAddRuleToSettings_AddsRule(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	})

	rule := PermissionRule{ToolName: "Bash", RuleContent: "ls", Behavior: RuleAllow, Source: SourceLocalSettings}
	if err := store.AddRuleToSettings(SourceLocalSettings, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Allow) != 2 {
		t.Fatalf("expected 2 allow rules, got %d: %v", len(perms.Allow), perms.Allow)
	}

	foundCurl := false
	foundLs := false
	for _, r := range perms.Allow {
		if r == "Bash(curl:*)" {
			foundCurl = true
		}
		if r == "Bash(ls)" {
			foundLs = true
		}
	}
	if !foundCurl {
		t.Error("original rule Bash(curl:*) not found after add")
	}
	if !foundLs {
		t.Error("added rule Bash(ls) not found")
	}
}

func TestAddRuleToSettings_CreatesFileIfNotExists(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	rule := PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings}
	if err := store.AddRuleToSettings(SourceLocalSettings, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Allow) != 1 || perms.Allow[0] != "Bash(curl:*)" {
		t.Errorf("expected [Bash(curl:*)], got %v", perms.Allow)
	}
}

func TestAddRuleToSettings_AddsDenyRule(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceProjectSettings, path)

	rule := PermissionRule{ToolName: "Bash", RuleContent: "rm:*", Behavior: RuleDeny, Source: SourceProjectSettings}
	if err := store.AddRuleToSettings(SourceProjectSettings, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Deny) != 1 || perms.Deny[0] != "Bash(rm:*)" {
		t.Errorf("expected deny [Bash(rm:*)], got %v", perms.Deny)
	}
}

func TestRemoveRuleFromSettings_RemovesRule(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)", "Bash(ls)"},
		Deny:  []string{"Bash(rm:*)"},
	})

	rule := PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings}
	if err := store.RemoveRuleFromSettings(SourceLocalSettings, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Allow) != 1 || perms.Allow[0] != "Bash(ls)" {
		t.Errorf("expected [Bash(ls)] after removal, got %v", perms.Allow)
	}
	if len(perms.Deny) != 1 || perms.Deny[0] != "Bash(rm:*)" {
		t.Errorf("deny rules should be unchanged, got %v", perms.Deny)
	}
}

func TestRemoveRuleFromSettings_NonExistentRule(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	})

	rule := PermissionRule{ToolName: "Bash", RuleContent: "nonexistent", Behavior: RuleAllow, Source: SourceLocalSettings}
	if err := store.RemoveRuleFromSettings(SourceLocalSettings, rule); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	perms := readPermissionsFromFile(t, path)
	if len(perms.Allow) != 1 || perms.Allow[0] != "Bash(curl:*)" {
		t.Errorf("expected original rule to remain, got %v", perms.Allow)
	}
}

func TestAddRuleToSettings_DoesNotDeadlock(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	done := make(chan struct{})
	go func() {
		defer close(done)
		rule := PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings}
		if err := store.AddRuleToSettings(SourceLocalSettings, rule); err != nil {
			t.Errorf("AddRuleToSettings failed: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("AddRuleToSettings deadlocked (CONC-002 regression)")
	}
}

func TestAddRuleToSettings_ConcurrentNoDeadlock(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rule := PermissionRule{
				ToolName:    "Bash",
				RuleContent: "cmd",
				Behavior:    RuleAllow,
				Source:      SourceLocalSettings,
			}
			if err := store.AddRuleToSettings(SourceLocalSettings, rule); err != nil {
				t.Errorf("AddRuleToSettings failed: %v", err)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent AddRuleToSettings deadlocked (CONC-002 regression)")
	}
}

func TestRemoveRuleFromSettings_DoesNotDeadlock(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		rule := PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings}
		if err := store.RemoveRuleFromSettings(SourceLocalSettings, rule); err != nil {
			t.Errorf("RemoveRuleFromSettings failed: %v", err)
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RemoveRuleFromSettings deadlocked")
	}
}

func TestSave_PreservesOtherFields(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	existing := map[string]interface{}{
		"permissions": PermissionSettings{
			Allow: []string{"Bash(ls)"},
		},
		"theme":  "dark",
		"editor": "vim",
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	ruleSet := NewRuleSet()
	ruleSet.Add(PermissionRule{ToolName: "Bash", RuleContent: "curl:*", Behavior: RuleAllow, Source: SourceLocalSettings})

	if err := store.Save(SourceLocalSettings, ruleSet); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result["theme"] != "dark" {
		t.Errorf("expected theme=dark, got %v", result["theme"])
	}
	if result["editor"] != "vim" {
		t.Errorf("expected editor=vim, got %v", result["editor"])
	}
}

// writeSettingsFileWithPriorityMode 写入带 priorityMode 字段的配置文件（R-2 测试辅助）。
// 用于模拟 v1.0+ 新配置文件，区分"无 priorityMode 字段"（旧配置）与"有 priorityMode 字段"（新配置）。
func writeSettingsFileWithPriorityMode(t *testing.T, path string, perms PermissionSettings, priorityMode string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	settings := map[string]interface{}{
		"permissions":  perms,
		"priorityMode": priorityMode,
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal settings: %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		t.Fatalf("failed to write settings: %v", err)
	}
}

// TestR2_OldConfig_NoPriorityModeField_LoadsAsLegacy 反例测试（R-2）：
// 验证旧配置文件（无 priorityMode 字段）加载后保持 Legacy 语义（向后兼容）。
//
// 红队终审 R-2（112-其他未考虑问题与建议）：
// 桶 X4 将 NewRuleSet() 默认从 PriorityLegacy 改为 PriorityPosix，
// 旧配置文件无 priorityMode 字段，加载后默认 Posix 导致规则评估顺序反转。
// 修复：Load 检测 priorityMode 字段缺失时回退 Legacy（向后兼容）。
//
// 反例：若修复失效（Load 未检测字段缺失），ruleSet.PriorityMode() 将是 Posix（NewRuleSet 默认），
// 与旧配置的 Legacy 语义冲突。
func TestR2_OldConfig_NoPriorityModeField_LoadsAsLegacy(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	// 写入旧配置文件（无 priorityMode 字段，模拟 v0.x 升级场景）
	writeSettingsFile(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	})

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}

	// R-2 修复：旧配置文件应保持 Legacy 语义（向后兼容）
	if ruleSet.PriorityMode() != PriorityLegacy {
		t.Errorf("R-2 反例失败: 旧配置文件（无 priorityMode 字段）应保持 Legacy 语义\n"+
			"  期望: PriorityLegacy（向后兼容）\n"+
			"  实际: %s（旧配置升级后语义反转）", ruleSet.PriorityMode())
	}
}

// TestR2_NewConfig_PriorityModePosix_LoadsAsPosix 反例测试（R-2）：
// 验证新配置文件（priorityMode=posix）加载后采用 Posix 语义（向前改进）。
//
// 红队终审 R-2 建议：新配置文件显式声明 priorityMode，按配置加载。
// 用户主动 opt-in Posix 模式后，加载后应是 Posix。
func TestR2_NewConfig_PriorityModePosix_LoadsAsPosix(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	// 写入新配置文件（priorityMode=posix，模拟 v1.0+ 显式 opt-in）
	writeSettingsFileWithPriorityMode(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	}, "posix")

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}

	// R-2 修复：新配置文件 priorityMode=posix 应采用 Posix 语义
	if ruleSet.PriorityMode() != PriorityPosix {
		t.Errorf("R-2 反例失败: 新配置文件 priorityMode=posix 应采用 Posix 语义\n"+
			"  期望: PriorityPosix\n"+
			"  实际: %s", ruleSet.PriorityMode())
	}
}

// TestR2_NewConfig_PriorityModeLegacy_LoadsAsLegacy 反例测试（R-2）：
// 验证新配置文件（priorityMode=legacy）加载后采用 Legacy 语义。
//
// 用户显式 opt-in Legacy 模式时，加载后应是 Legacy。
func TestR2_NewConfig_PriorityModeLegacy_LoadsAsLegacy(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	// 写入新配置文件（priorityMode=legacy，显式 opt-in Legacy）
	writeSettingsFileWithPriorityMode(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	}, "legacy")

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}

	if ruleSet.PriorityMode() != PriorityLegacy {
		t.Errorf("R-2 反例失败: 新配置文件 priorityMode=legacy 应采用 Legacy 语义\n"+
			"  期望: PriorityLegacy\n"+
			"  实际: %s", ruleSet.PriorityMode())
	}
}

// TestR2_NewConfig_PriorityModeInvalid_FallsBackToPosix 反例测试（R-2）：
// 验证新配置文件 priorityMode 为非法值时 fail-closed 回填 Posix。
//
// 非法值（如 "invalid"）应回填 Posix（与 NewRuleSet 默认一致，行业惯例）。
func TestR2_NewConfig_PriorityModeInvalid_FallsBackToPosix(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	// 写入新配置文件（priorityMode=invalid，非法值）
	writeSettingsFileWithPriorityMode(t, path, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	}, "invalid")

	ruleSet, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ruleSet == nil {
		t.Fatal("ruleSet is nil")
	}

	if ruleSet.PriorityMode() != PriorityPosix {
		t.Errorf("R-2 反例失败: 非法 priorityMode 应 fail-closed 回填 Posix\n"+
			"  期望: PriorityPosix\n"+
			"  实际: %s", ruleSet.PriorityMode())
	}
}

// TestR2_LoadAll_MixedConfig_FallsBackToLegacy 反例测试（R-2）：
// 验证 LoadAll 合并多个 source 时，任一 source 是 Legacy（旧配置）→ merged 用 Legacy。
//
// 模拟场景：user 级新配置（priorityMode=posix）+ project 级旧配置（无 priorityMode 字段）。
// 期望：merged 用 Legacy（向后兼容优先，混合场景下保守策略）。
func TestR2_LoadAll_MixedConfig_FallsBackToLegacy(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()

	userPath := filepath.Join(tmpDir, "user.json")
	projectPath := filepath.Join(tmpDir, "project.json")

	// user 级：新配置（priorityMode=posix）
	writeSettingsFileWithPriorityMode(t, userPath, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	}, "posix")
	store.SetPathForSource(SourceUserSettings, userPath)

	// project 级：旧配置（无 priorityMode 字段）
	writeSettingsFile(t, projectPath, PermissionSettings{
		Deny: []string{"Bash(rm:*)"},
	})
	store.SetPathForSource(SourceProjectSettings, projectPath)

	merged, err := store.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged == nil {
		t.Fatal("merged ruleSet is nil")
	}

	// R-2 修复：混合配置（任一 source 是 Legacy）→ merged 用 Legacy
	if merged.PriorityMode() != PriorityLegacy {
		t.Errorf("R-2 反例失败: 混合配置（user=posix + project=legacy）应回退 Legacy\n"+
			"  期望: PriorityLegacy（向后兼容优先）\n"+
			"  实际: %s（混合场景下应保守选择 Legacy）", merged.PriorityMode())
	}
}

// TestR2_LoadAll_AllNewConfig_Posix_LoadsAsPosix 反例测试（R-2）：
// 验证 LoadAll 合并多个 source 时，所有 source 都显式 priorityMode=posix → merged 用 Posix。
//
// 模拟场景：user 级 + project 级都是新配置（priorityMode=posix）。
// 期望：merged 用 Posix（所有 source 显式 opt-in）。
func TestR2_LoadAll_AllNewConfig_Posix_LoadsAsPosix(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()

	userPath := filepath.Join(tmpDir, "user.json")
	projectPath := filepath.Join(tmpDir, "project.json")

	// user 级：新配置（priorityMode=posix）
	writeSettingsFileWithPriorityMode(t, userPath, PermissionSettings{
		Allow: []string{"Bash(curl:*)"},
	}, "posix")
	store.SetPathForSource(SourceUserSettings, userPath)

	// project 级：新配置（priorityMode=posix）
	writeSettingsFileWithPriorityMode(t, projectPath, PermissionSettings{
		Deny: []string{"Bash(rm:*)"},
	}, "posix")
	store.SetPathForSource(SourceProjectSettings, projectPath)

	merged, err := store.LoadAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if merged == nil {
		t.Fatal("merged ruleSet is nil")
	}

	// R-2 修复：所有 source 显式 posix → merged 用 Posix
	if merged.PriorityMode() != PriorityPosix {
		t.Errorf("R-2 反例失败: 所有 source 显式 posix 应采用 Posix\n"+
			"  期望: PriorityPosix\n"+
			"  实际: %s", merged.PriorityMode())
	}
}

// TestR2_Save_PersistsPriorityMode 反例测试（R-2）：
// 验证 Save 保存时写入 priorityMode 字段，下次 Load 时保持一致语义。
//
// 模拟场景：用户通过 SetPriorityMode(Posix) 切换后 Save，下次 Load 应是 Posix。
func TestR2_Save_PersistsPriorityMode(t *testing.T) {
	store := NewRuleStore()
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "settings.json")
	store.SetPathForSource(SourceLocalSettings, path)

	// 创建 ruleSet 并显式设置 Posix
	ruleSet := NewRuleSet()
	ruleSet.SetPriorityMode(PriorityPosix)
	ruleSet.Add(PermissionRule{
		ToolName:    "Bash",
		RuleContent: "curl:*",
		Behavior:    RuleAllow,
		Source:      SourceLocalSettings,
	})

	if err := store.Save(SourceLocalSettings, ruleSet); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// 验证文件中写入了 priorityMode 字段
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if result["priorityMode"] != "posix" {
		t.Errorf("R-2 反例失败: Save 应写入 priorityMode=posix\n"+
			"  期望: priorityMode=posix\n"+
			"  实际: %v", result["priorityMode"])
	}

	// 重新 Load，应保持 Posix 语义
	loaded, err := store.Load(SourceLocalSettings)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.PriorityMode() != PriorityPosix {
		t.Errorf("R-2 反例失败: Save 后 Load 应保持 Posix 语义\n"+
			"  期望: PriorityPosix\n"+
			"  实际: %s", loaded.PriorityMode())
	}
}

// TestR2_NewRuleSet_DefaultStillPosix 反例测试（R-2）：
// 验证显式 NewRuleSet()（无配置文件）默认仍是 Posix（向前改进）。
//
// 红队终审 R-2 推荐：新代码 NewRuleSet() 默认 Posix（向前改进），
// 旧配置文件升级保持 Legacy（向后兼容）。两者不冲突。
func TestR2_NewRuleSet_DefaultStillPosix(t *testing.T) {
	rs := NewRuleSet()
	if rs.PriorityMode() != PriorityPosix {
		t.Errorf("R-2 反例失败: 显式 NewRuleSet() 默认应是 Posix（向前改进）\n"+
			"  期望: PriorityPosix\n"+
			"  实际: %s", rs.PriorityMode())
	}
}
