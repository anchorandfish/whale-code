package metrics

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// === 用量桥接测试（D 批 2026-10-05：cost 维度已删除，只余 provider 真报的 token）===

// TestRecordUsage_TokenFields 验证 RecordUsage 正确累加 4 个 token 字段。
func TestRecordUsage_TokenFields(t *testing.T) {
	m := &Metrics{}
	m.RecordUsage(UsageRecord{InputTokens: 1000, OutputTokens: 500, CacheCreation: 200, CacheRead: 100})

	if m.InputTokens.Load() != 1000 {
		t.Errorf("InputTokens = %d, want 1000", m.InputTokens.Load())
	}
	if m.OutputTokens.Load() != 500 {
		t.Errorf("OutputTokens = %d, want 500", m.OutputTokens.Load())
	}
	if m.CacheCreationTokens.Load() != 200 {
		t.Errorf("CacheCreationTokens = %d, want 200", m.CacheCreationTokens.Load())
	}
	if m.CacheReadTokens.Load() != 100 {
		t.Errorf("CacheReadTokens = %d, want 100", m.CacheReadTokens.Load())
	}
}

// TestRecordUsage_Accumulate 验证多次调用累加。
func TestRecordUsage_Accumulate(t *testing.T) {
	m := &Metrics{}
	m.RecordUsage(UsageRecord{InputTokens: 100, OutputTokens: 50})
	m.RecordUsage(UsageRecord{InputTokens: 200, OutputTokens: 100})

	if m.InputTokens.Load() != 300 {
		t.Errorf("InputTokens = %d, want 300（累加）", m.InputTokens.Load())
	}
	if m.OutputTokens.Load() != 150 {
		t.Errorf("OutputTokens = %d, want 150（累加）", m.OutputTokens.Load())
	}
}

// TestSnapshot_ContainsTokenFields 验证 Snapshot 含 cache token 字段。
func TestSnapshot_ContainsTokenFields(t *testing.T) {
	m := &Metrics{}
	m.RecordUsage(UsageRecord{InputTokens: 100, OutputTokens: 50, CacheCreation: 30, CacheRead: 20})

	snap := m.Snapshot()

	requiredKeys := []string{"cache_creation_tokens", "cache_read_tokens"}
	for _, key := range requiredKeys {
		if _, ok := snap[key]; !ok {
			t.Errorf("Snapshot 缺少字段 %q", key)
		}
	}
}

// TestSnapshot_HasNoCostField 结构判据：成本维度已删（D 批 2026-10-05）。
//
// ★ 为什么必须有这条：删掉一个对外可见的字段，若没有判据钉住，日后有人"顺手补回来"
// 或误以为只是漏了，就会重新引入"我们自己估的价"。此判据钉住的是 Snapshot 出口；
// 端点出口（原 /metrics 的 whale_cost_total_cents）由 daemon 侧另行删除。
func TestSnapshot_HasNoCostField(t *testing.T) {
	m := &Metrics{}
	m.RecordUsage(UsageRecord{InputTokens: 100, OutputTokens: 50})

	snap := m.Snapshot()
	if _, ok := snap["total_cost"]; ok {
		t.Errorf("Snapshot 不应再有 total_cost 字段（成本线已删）：%v", snap["total_cost"])
	}
}

// === N2 反例测试（审查 prompt §2.1-2.2 + §2.4-2.5）===

// TestUsageRecord_Fields 验证 UsageRecord 字段集（反例：少字段直接编译失败）。
// D 批删掉了 CostCents：金额不是 provider 上报的事实，是我们按价格表
// 估的、且混币种，属"我们自己编的数"，已随成本线移除。余 7 字段皆为真实记录项。
func TestUsageRecord_Fields(t *testing.T) {
	r := UsageRecord{
		AgentID:       "agent-001",
		Model:         "gpt-4",
		InputTokens:   100,
		OutputTokens:  50,
		CacheCreation: 200,
		CacheRead:     300,
		Timestamp:     time.Now(),
	}
	// 逐字段校验，任一缺失或赋值失败则失败
	if r.AgentID != "agent-001" || r.Model != "gpt-4" ||
		r.InputTokens != 100 || r.OutputTokens != 50 ||
		r.CacheCreation != 200 || r.CacheRead != 300 ||
		r.Timestamp.IsZero() {
		t.Fatalf("UsageRecord field missing or mismatched: %+v", r)
	}
}

// TestRecordUsage_TokenAccumulation 验证 RecordUsage(UsageRecord) 累加正确（审查 prompt §2.4）。
// 两次调用：第一次 agent-001 + gpt-4，第二次 compact + gpt-4，
// 累加结果：input=300, output=150, cache_creation=200, cache_read=300。
func TestRecordUsage_TokenAccumulation(t *testing.T) {
	m := &Metrics{}
	m.RecordUsage(UsageRecord{
		AgentID:       "agent-001",
		Model:         "gpt-4",
		InputTokens:   100,
		OutputTokens:  50,
		CacheCreation: 200,
		CacheRead:     300,
		Timestamp:     time.Now(),
	})
	m.RecordUsage(UsageRecord{
		AgentID:      "compact",
		Model:        "gpt-4",
		InputTokens:  200,
		OutputTokens: 100,
		Timestamp:    time.Now(),
	})

	// 累加：input=300, output=150, cache_creation=200, cache_read=300
	if m.InputTokens.Load() != 300 {
		t.Fatalf("input mismatch: %d", m.InputTokens.Load())
	}
	if m.OutputTokens.Load() != 150 {
		t.Fatalf("output mismatch: %d", m.OutputTokens.Load())
	}
	if m.CacheCreationTokens.Load() != 200 {
		t.Fatalf("cache creation mismatch: %d", m.CacheCreationTokens.Load())
	}
	if m.CacheReadTokens.Load() != 300 {
		t.Fatalf("cache read mismatch: %d", m.CacheReadTokens.Load())
	}
}

// TestNoOtherModuleMaintainsTokenField 反例：确认无其他模块自维护 metrics 的 token 计数器（审查 prompt §2.2）。
// 反例：若 internal/ 下除 metrics 包外有其他模块直接调
//
//	metrics.Default.InputTokens.Add(...) 等 atomic.Add
//
// 则绕过唯一入口 RecordUsage（02 §2.1.2 契约）。
//
// 实现方式：扫描 internal/ 下所有 .go 文件（排除 _test.go 和 metrics 包自身），
// 正则匹配 `\.InputTokens\.Add\(|\.OutputTokens\.Add\(|
// \.CacheCreationTokens\.Add\(|\.CacheReadTokens\.Add\(`，断言无匹配。
//
// 此测试为静态检查，运行时无需启动服务，仅依赖源码可读。
func TestNoOtherModuleMaintainsTokenField(t *testing.T) {
	// 反例模式：直接对 metrics 的 atomic 字段调 Add，绕过 RecordUsage
	pattern := regexp.MustCompile(`\.(InputTokens|OutputTokens|CacheCreationTokens|CacheReadTokens)\.Add\(`)

	// 定位 internal/ 目录（测试工作目录为 internal/metrics/，上一级即 internal/）
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd failed: %v", err)
	}
	internalDir := filepath.Join(wd, "..")

	var violations []string
	err = filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 跳过 metrics 包自身（唯一允许直接操作 atomic 字段的包）
			if filepath.Base(path) == "metrics" && path != internalDir {
				return filepath.SkipDir
			}
			return nil
		}
		// 仅扫描 .go 文件，排除 _test.go
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if loc := pattern.FindStringIndex(string(data)); loc != nil {
			rel, _ := filepath.Rel(internalDir, path)
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Walk internal/ failed: %v", err)
	}

	if len(violations) > 0 {
		t.Fatalf("反例违规：以下文件绕过 RecordUsage 直接操作 atomic token 字段（违背 02 §2.1.2 契约）：\n%s",
			strings.Join(violations, "\n"))
	}
}
