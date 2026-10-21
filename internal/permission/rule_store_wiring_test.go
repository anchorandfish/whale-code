package permission

import (
	"os"
	"path/filepath"
	"testing"
)

// TestNewRuleStoreFromPaths_MapsSourcesToSettingsFiles verifies the factory maps
// each persistent source to the settings.json path convention shared with the
// config package (config.go discoverSources). This is the R1 wiring assertion at
// the unit level: without it, Load/LoadAll always see empty paths and load nothing.
func TestNewRuleStoreFromPaths_MapsSourcesToSettingsFiles(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	// user settings 路径从 whalehome.Dir() 解析，测试经 WHALE_HOME 注入
	t.Setenv("WHALE_HOME", filepath.Join(home, ".whale"))
	work := filepath.Join(t.TempDir(), "work")

	store := NewRuleStoreFromPaths(home, work)

	want := map[RuleSource]string{
		SourceUserSettings:    filepath.Join(home, ".whale", "settings.json"),
		SourceProjectSettings: filepath.Join(work, ".whale", "settings.json"),
		SourceLocalSettings:   filepath.Join(work, ".whale", "settings.local.json"),
	}
	for source, expected := range want {
		got := store.GetPathForSource(source)
		if got != expected {
			t.Errorf("path for %s = %q, want %q", source, got, expected)
		}
	}

	// Runtime-only sources must stay pathless.
	for _, source := range []RuleSource{SourceSession, SourceCLIArg} {
		if got := store.GetPathForSource(source); got != "" {
			t.Errorf("path for %s = %q, want empty (runtime-only)", source, got)
		}
	}
}

// TestRuleStoreWiring_DenyRuleFromSettingsBlocksPolicy is the production-entry
// red/green test: a deny rule written to the project settings.json must be loaded
// by LoadAll and, once set on a Policy via SetRuleSet, must actually block the
// tool — i.e. the user-visible behavior "configured deny takes effect after a
// restart" that the P0 fix is meant to deliver.
func TestRuleStoreWiring_DenyRuleFromSettingsBlocksPolicy(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	projectSettings := filepath.Join(work, ".whale", "settings.json")
	writeSettingsFile(t, projectSettings, PermissionSettings{
		Deny: []string{"bash(rm:*)"},
	})

	store := NewRuleStoreFromPaths(home, work)

	loaded, err := store.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll failed: %v", err)
	}
	if loaded.Count() != 1 {
		t.Fatalf("loaded rule count = %d, want 1 (deny rule must be loaded from settings.json)", loaded.Count())
	}

	p := NewPolicy(LevelWorkspaceWrite)
	p.SetRuleSet(loaded)

	allowed, needConfirm := p.Check("bash", `{"command":"rm /tmp/victim.txt"}`)
	if allowed {
		t.Errorf("deny rule from settings.json was not enforced: allowed=true, want false")
	}
	if needConfirm {
		t.Errorf("deny rule from settings.json still asks for confirmation: needConfirm=true, want false")
	}
}

// TestRuleStoreWiring_SaveFiltersBySource guards against cross-source pollution:
// once wired, Save(source, mergedSet) must persist only that source's rules, not
// the merged set (which handlePermissionsReset/Remove pass in). Without this
// filter, resetting session rules would dump user/session rules into the project
// settings file.
func TestRuleStoreWiring_SaveFiltersBySource(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	store := NewRuleStoreFromPaths(home, work)

	merged := NewRuleSet()
	projectRule, _ := ParseRuleString("bash(rm:*)", RuleDeny, SourceProjectSettings)
	userRule, _ := ParseRuleString("bash(git push*)", RuleAllow, SourceUserSettings)
	merged.Add(*projectRule)
	merged.Add(*userRule)

	if err := store.Save(SourceProjectSettings, merged); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	projectSettings := filepath.Join(work, ".whale", "settings.json")
	perms := readPermissionsFromFile(t, projectSettings)
	if len(perms.Allow) != 0 {
		t.Errorf("project settings leaked user allow rules: %v", perms.Allow)
	}
	if len(perms.Deny) != 1 || perms.Deny[0] != "bash(rm:*)" {
		t.Errorf("project settings should contain only the project deny rule, got deny=%v", perms.Deny)
	}
}

// TestWireRuleStore_LoadsAndAttachesAtStartup is the closest unit proxy for the
// production entry point. It calls the exact method the two production entry
// points (lifecycle.go NewModel, runner_core.go NewRunnerCore) will invoke, and
// asserts the user-visible outcome: a deny rule already present in settings.json
// is active on the Policy right after wiring (no manual /permissions reload).
func TestWireRuleStore_LoadsAndAttachesAtStartup(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	writeSettingsFile(t, filepath.Join(work, ".whale", "settings.json"), PermissionSettings{
		Deny: []string{"bash(rm:*)"},
	})

	p := NewPolicy(LevelWorkspaceWrite)
	if err := p.WireRuleStore(home, work); err != nil {
		t.Fatalf("WireRuleStore failed: %v", err)
	}

	if p.RuleStore() == nil {
		t.Fatal("WireRuleStore did not attach a rule store")
	}

	allowed, needConfirm := p.Check("bash", `{"command":"rm /tmp/victim.txt"}`)
	if allowed {
		t.Errorf("deny rule not loaded at startup: allowed=true, want false")
	}
	if needConfirm {
		t.Errorf("deny rule not loaded at startup: needConfirm=true, want false")
	}
}

// TestRuleStoreWiring_AddRulePersistsToProjectByDefault covers the second half of
// P0: once the store is wired, AddRule with a non-session source must round-trip
// to disk so a restart reloads it (no silent in-memory-only loss).
func TestRuleStoreWiring_AddRulePersistsToProjectByDefault(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	store := NewRuleStoreFromPaths(home, work)
	p := NewPolicy(LevelWorkspaceWrite)
	p.SetRuleStore(store)

	rule, err := ParseRuleString("bash(rm:*)", RuleDeny, SourceProjectSettings)
	if err != nil {
		t.Fatalf("ParseRuleString failed: %v", err)
	}
	if err := p.AddRule(*rule); err != nil {
		t.Fatalf("AddRule failed: %v", err)
	}

	projectSettings := filepath.Join(work, ".whale", "settings.json")
	if _, err := os.Stat(projectSettings); err != nil {
		t.Fatalf("AddRule did not persist project settings.json: %v", err)
	}

	// Simulate a restart: fresh store + policy, load from disk only.
	store2 := NewRuleStoreFromPaths(home, work)
	loaded, err := store2.LoadAll()
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	p2 := NewPolicy(LevelWorkspaceWrite)
	p2.SetRuleSet(loaded)

	allowed, _ := p2.Check("bash", `{"command":"rm /tmp/victim.txt"}`)
	if allowed {
		t.Errorf("after restart, deny rule was not reloaded: allowed=true, want false")
	}
}
