package agentguard

import (
	"os"
	"testing"
)

// P0-1/P0-2/P0-3/P0-5 默认值
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxTurns != 40 {
		t.Errorf("MaxTurns = %d, want 40（P0-1 默认）", cfg.MaxTurns)
	}
	if cfg.MaxConsecutiveToolFailures != 3 {
		t.Errorf("MaxConsecutiveToolFailures = %d, want 3（P0-2 默认）", cfg.MaxConsecutiveToolFailures)
	}
	if cfg.MaxReviewRounds != 8 {
		t.Errorf("MaxReviewRounds = %d, want 8（P0-3 软提醒间隔默认；旧值 3 是停机阈值，已降级）", cfg.MaxReviewRounds)
	}
	if cfg.Autonomy != "" {
		t.Errorf("Autonomy = %q, want 空（P0-5 默认关闭）", cfg.Autonomy)
	}
	if cfg.MaxSegments != 3 {
		t.Errorf("MaxSegments = %d, want 3（P0-4 续跑段上限）", cfg.MaxSegments)
	}
}

// P0-5 WHALE_AUTONOMY 插口：full 开启，其他值/未设置关闭
func TestConfigFromEnvAutonomy(t *testing.T) {
	orig, had := os.LookupEnv("WHALE_AUTONOMY")
	defer func() {
		if had {
			os.Setenv("WHALE_AUTONOMY", orig)
		} else {
			os.Unsetenv("WHALE_AUTONOMY")
		}
	}()

	cases := []struct {
		env      string
		expected bool
	}{
		{"", false},
		{"off", false},
		{"0", false},
		{"full", true},
	}
	for _, tc := range cases {
		if tc.env == "" {
			os.Unsetenv("WHALE_AUTONOMY")
		} else {
			os.Setenv("WHALE_AUTONOMY", tc.env)
		}
		cfg := ConfigFromEnv(DefaultConfig())
		if cfg.AutonomyFull() != tc.expected {
			t.Errorf("WHALE_AUTONOMY=%q: AutonomyFull = %v, want %v", tc.env, cfg.AutonomyFull(), tc.expected)
		}
	}
}

// AutonomyFull 对非法值的容错（默认关闭）
func TestAutonomyFullInvalidValues(t *testing.T) {
	for _, v := range []string{"FULL", "Full", "yes", "on", "true"} {
		cfg := DefaultConfig()
		cfg.Autonomy = v
		if cfg.AutonomyFull() {
			t.Errorf("Autonomy = %q 不应视为 full（只认小写 full）", v)
		}
	}
}
