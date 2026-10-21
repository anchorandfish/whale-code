// ask_settings_test.go — T3 S5 提问配置读取单元测试。
// 覆盖 LoadAskSettings 的默认值/clamp 高/clamp 低/合法值 4 类路径。
package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// writeSettingsJSON 在本测试独占的状态目录写入 settings.json（含 interaction.ask_threshold 字段）。
//
// ★ 逐测试隔离（WHALE_HOME → t.TempDir）：不要写包级共享 HOME。共享目录一旦被上一个用例
// 留下 ask_threshold=0，后续所有新建 Model 的用例都会因「阈值 0 = 禁用提问」而集体假红。
func writeSettingsJSON(t *testing.T, threshold int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(whalehome.EnvVar, dir)
	data := map[string]interface{}{
		"interaction": map[string]interface{}{
			"ask_threshold": threshold,
		},
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
}

// TestLoadAskSettings_Default 验证 settings.json 不存在时返回默认值 1（REF-T3-S5-新增）。
func TestLoadAskSettings_Default(t *testing.T) {
	// TestMain 已隔离 HOME 到临时目录，不写 settings.json 即视为缺失
	s := LoadAskSettings()
	if s == nil {
		t.Fatalf("LoadAskSettings should never return nil")
	}
	if s.AskThreshold != AskThresholdDefault {
		t.Errorf("default AskThreshold should be %d, got %d", AskThresholdDefault, s.AskThreshold)
	}
}

// TestLoadAskSettings_Valid 验证合法值（0-3 范围内）原样返回（REF-T3-S5-新增）。
func TestLoadAskSettings_Valid(t *testing.T) {
	writeSettingsJSON(t, 2)
	s := LoadAskSettings()
	if s.AskThreshold != 2 {
		t.Errorf("valid AskThreshold should be 2, got %d", s.AskThreshold)
	}
}

// TestLoadAskSettings_ValidZero 验证 0 是合法值（完全禁用日常对话提问，REF-T3-S5-新增）。
func TestLoadAskSettings_ValidZero(t *testing.T) {
	writeSettingsJSON(t, 0)
	s := LoadAskSettings()
	if s.AskThreshold != 0 {
		t.Errorf("AskThreshold 0 should be preserved (not defaulted), got %d", s.AskThreshold)
	}
}

// TestLoadAskSettings_ClampHigh 验证 >3 的值 clamp 到 3（REF-降级策略-优化4，REF-T3-S5-新增）。
func TestLoadAskSettings_ClampHigh(t *testing.T) {
	writeSettingsJSON(t, 99)
	s := LoadAskSettings()
	if s.AskThreshold != AskThresholdMax {
		t.Errorf("AskThreshold >3 should clamp to %d, got %d", AskThresholdMax, s.AskThreshold)
	}
}

// TestLoadAskSettings_ClampLow 验证 <0 的值 clamp 到 0（REF-降级策略-优化4，REF-T3-S5-新增）。
func TestLoadAskSettings_ClampLow(t *testing.T) {
	writeSettingsJSON(t, -5)
	s := LoadAskSettings()
	if s.AskThreshold != AskThresholdMin {
		t.Errorf("AskThreshold <0 should clamp to %d, got %d", AskThresholdMin, s.AskThreshold)
	}
}

// TestLoadAskSettings_InvalidJSON 验证 JSON 解析失败时降级为默认值（REF-T3-S5-新增）。
func TestLoadAskSettings_InvalidJSON(t *testing.T) {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, ".whale", "settings.json")
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	_ = os.WriteFile(path, []byte("not a json"), 0644)

	s := LoadAskSettings()
	if s.AskThreshold != AskThresholdDefault {
		t.Errorf("invalid JSON should fallback to default %d, got %d", AskThresholdDefault, s.AskThreshold)
	}
}

// TestDefaultAskSettings 验证 DefaultAskSettings 返回默认值（REF-T3-S5-新增）。
func TestDefaultAskSettings(t *testing.T) {
	s := DefaultAskSettings()
	if s == nil || s.AskThreshold != AskThresholdDefault {
		t.Errorf("DefaultAskSettings should return threshold %d", AskThresholdDefault)
	}
}
