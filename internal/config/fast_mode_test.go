// Package config — fast mode 状态机单元测试。
package config

import (
	"sync"
	"testing"
	"time"
)

// TestGetFastModeState 验证 fast mode 状态机。
// 对标同类 CLI getFastModeState。
func TestGetFastModeState(t *testing.T) {
	tests := []struct {
		name string
		cfg  FastModeConfig
		want FastModeState
	}{
		{
			name: "未开启 → off",
			cfg:  FastModeConfig{Enabled: false},
			want: FastModeOff,
		},
		{
			name: "已开启，无冷却 → on",
			cfg:  FastModeConfig{Enabled: true, CooldownUntil: time.Time{}},
			want: FastModeOn,
		},
		{
			name: "已开启，冷却未到期 → cooldown",
			cfg:  FastModeConfig{Enabled: true, CooldownUntil: time.Now().Add(30 * time.Second)},
			want: FastModeCooldown,
		},
		{
			name: "已开启，冷却已到期 → 自动恢复 on（M9）",
			cfg:  FastModeConfig{Enabled: true, CooldownUntil: time.Now().Add(-1 * time.Second)},
			want: FastModeOn,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetFastModeState(tt.cfg); got != tt.want {
				t.Errorf("GetFastModeState() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestTriggerCooldown 验证冷却触发。
// 对标同类 CLI triggerFastModeCooldown。
func TestTriggerCooldown(t *testing.T) {
	cfg := FastModeConfig{Enabled: true}

	// 触发冷却
	cfg.TriggerCooldown(60*time.Second, "rate_limit")

	if cfg.CooldownUntil.IsZero() {
		t.Error("TriggerCooldown did not set CooldownUntil")
	}
	if cfg.CooldownReason != "rate_limit" {
		t.Errorf("CooldownReason = %q, want %q", cfg.CooldownReason, "rate_limit")
	}

	// 验证状态为 cooldown
	if state := GetFastModeState(cfg); state != FastModeCooldown {
		t.Errorf("state after trigger = %v, want %v", state, FastModeCooldown)
	}
}

// TestClearCooldown 验证清除冷却。
func TestClearCooldown(t *testing.T) {
	cfg := FastModeConfig{
		Enabled:        true,
		CooldownUntil:  time.Now().Add(30 * time.Second),
		CooldownReason: "overloaded",
	}

	// 验证初始为冷却状态
	if state := GetFastModeState(cfg); state != FastModeCooldown {
		t.Errorf("initial state = %v, want %v", state, FastModeCooldown)
	}

	// 清除冷却
	cfg.ClearCooldown()

	if !cfg.CooldownUntil.IsZero() {
		t.Error("ClearCooldown did not reset CooldownUntil")
	}
	if cfg.CooldownReason != "" {
		t.Errorf("CooldownReason = %q, want empty", cfg.CooldownReason)
	}

	// 验证状态恢复为 on
	if state := GetFastModeState(cfg); state != FastModeOn {
		t.Errorf("state after clear = %v, want %v", state, FastModeOn)
	}
}

// TestGetCurrentModel 验证当前模型获取。
// M13 协同：N2 cost tracker 通过此函数获取当前模型。
func TestGetCurrentModel(t *testing.T) {
	tests := []struct {
		name      string
		cfg       FastModeConfig
		wantModel string
	}{
		{
			name:      "fast on → 返回 FastModel",
			cfg:       FastModeConfig{Enabled: true, FastModel: "opus-4-fast", DefaultModel: "claude-3.5-sonnet"},
			wantModel: "opus-4-fast",
		},
		{
			name:      "fast off → 返回 DefaultModel",
			cfg:       FastModeConfig{Enabled: false, FastModel: "opus-4-fast", DefaultModel: "claude-3.5-sonnet"},
			wantModel: "claude-3.5-sonnet",
		},
		{
			name:      "fast cooldown → 返回 DefaultModel（M13 边界 case）",
			cfg:       FastModeConfig{Enabled: true, FastModel: "opus-4-fast", DefaultModel: "claude-3.5-sonnet", CooldownUntil: time.Now().Add(30 * time.Second)},
			wantModel: "claude-3.5-sonnet",
		},
		{
			name:      "fast on，FastModel 为空 → 返回空",
			cfg:       FastModeConfig{Enabled: true, FastModel: "", DefaultModel: "claude-3.5-sonnet"},
			wantModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetCurrentModel(tt.cfg); got != tt.wantModel {
				t.Errorf("GetCurrentModel() = %q, want %q", got, tt.wantModel)
			}
		})
	}
}

// TestFastModeAutoRecover 验证冷却到期自动恢复。
// M9 修订：冷却到期后自动恢复为 on 状态。
func TestFastModeAutoRecover(t *testing.T) {
	cfg := FastModeConfig{
		Enabled:        true,
		FastModel:      "fast-model",
		DefaultModel:   "default-model",
		CooldownUntil:  time.Now().Add(-1 * time.Second), // 已过期
		CooldownReason: "rate_limit",
	}

	// 验证状态为 on（冷却已到期自动恢复）
	if state := GetFastModeState(cfg); state != FastModeOn {
		t.Errorf("state after cooldown expired = %v, want %v", state, FastModeOn)
	}

	// 验证当前模型为 FastModel
	if model := GetCurrentModel(cfg); model != "fast-model" {
		t.Errorf("model after recovery = %q, want %q", model, "fast-model")
	}
}

// TestFastModeConfig_ConcurrentSafe 反例：FastModeConfig 并发安全（破坏性测试）。
// 对标 N16 TestTurnLimiter_ConcurrentSafe（100 goroutine 标准）。
// FastModeConfig 本身无内部锁（由 Config 读写锁保护，见 fast_mode.go 注释"调用方需在写锁保护下进行"），
// 本测试验证：在 sync.Mutex 保护下，100 goroutine 并发 TriggerCooldown/ClearCooldown/GetFastModeState，
// 若锁被移除，go test -race 会检测到 data race。
func TestFastModeConfig_ConcurrentSafe(t *testing.T) {
	cfg := &FastModeConfig{Enabled: true, FastModel: "fast", DefaultModel: "default"}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			switch i % 3 {
			case 0:
				cfg.TriggerCooldown(60*time.Second, "rate_limit")
			case 1:
				cfg.ClearCooldown()
			case 2:
				_ = GetFastModeState(*cfg)
			}
		}(i)
	}
	wg.Wait()
	// 最终状态合法性校验（TriggerCooldown/ClearCooldown 互为逆操作，最终态为二者之一）
	state := GetFastModeState(*cfg)
	if state != FastModeOn && state != FastModeCooldown {
		t.Errorf("并发后状态不合法: %v（期望 on 或 cooldown）", state)
	}
}
