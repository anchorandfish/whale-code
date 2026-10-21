package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestConfig_HasNoEffortLevelField 结构面锁：`/effort` 整条命令已删（C 批 C1，2026-10-05），
// config 侧随之删除 `EffortLevel` 字段与 `effort_level` 键。
//
// ★ 为什么必须有这条：字段存在性检查（reflect）比「JSON 里没有 effort_level」更强 ——
// 若有人把字段加回去却从未赋值，序列化时它照样出现（omitempty 只在零值时省略）；
// 而这里无论如何都会红。防止"只写不读"的半死字段以「看起来还在配」的形式复活。
func TestConfig_HasNoEffortLevelField(t *testing.T) {
	if f, ok := reflect.TypeOf(Config{}).FieldByName("EffortLevel"); ok {
		t.Errorf("config.Config 不应再有 EffortLevel 字段（C1 已删），但发现它，json tag=%q", f.Tag.Get("json"))
	}
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag != "" && strings.Split(tag, ",")[0] == "effort_level" {
			t.Errorf("config.Config 不应再有 json:\"effort_level\" 字段，发现 %s", rt.Field(i).Name)
		}
	}
}

// TestEffortLevel_RemovedFromConfig 老 settings.json 写了 effort_level 必须仍能解析：
// 未知键被静默忽略，不得变成「加载失败」。口径同 TestCustomPricing_RemovedFromConfig。
//
// ★ 为什么必须有这条：用户升级前的 settings.json 里有 effort_level 是既成事实；
// 删字段若把老配置变成读不了，就是拿"清理死码"换来了"启动崩"。
func TestEffortLevel_RemovedFromConfig(t *testing.T) {
	oldJSON := `{"effort_level": "high", "fast_mode_enabled": true}`

	var cfg Config
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatalf("含已废弃 effort_level 的配置应仍能解析（未知键应被忽略）: %v", err)
	}
	// 反向守卫：删除 effort_level 不得连带影响相邻保留字段 fast_mode_enabled 的解析
	if !cfg.FastModeEnabled {
		t.Error("effort_level 删除不应连带破坏 fast_mode_enabled 的解析")
	}
}
