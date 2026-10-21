// observability_test.go — 钩子可观测性测试（/ T1-P2-G5-M16Hook可观测性）。
//
// 测试覆盖：
// - 默认配置禁用验证
// - StatsCollector 统计收集
// - 慢钩子告警触发
// - 慢钩子告警去重（5s TTL）
// - ringBuffer 环形覆盖
// - RenderTrace 渲染输出
// - 并发安全（10 goroutine -race，子审查优化 7 吸纳）
package hook

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestObservability_DefaultDisabled 默认配置不收集 trace
func TestObservability_DefaultDisabled(t *testing.T) {
	m := NewManager(t.TempDir())
	if m.obs != nil {
		t.Fatal("obs should be nil by default")
	}
	// RenderTrace 应返回禁用提示
	if got := m.RenderTrace(); got != "[hook trace] observability disabled" {
		t.Fatalf("expected disabled msg, got: %s", got)
	}
	// Stats 应返回 nil
	if got := m.Stats(); got != nil {
		t.Fatalf("expected nil Stats, got: %v", got)
	}
}

// TestObservability_StatsCollector 统计收集
func TestObservability_StatsCollector(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableStats:       true,
		SlowHookThreshold: 50 * time.Millisecond,
	})
	// 模拟 3 次 hook 执行（2 次成功 + 1 次失败 + 1 次慢）
	m.obs.recordSpan(HookSpan{HookID: "h1", Duration: 10 * time.Millisecond, Action: ActionAllow})
	m.obs.recordSpan(HookSpan{HookID: "h1", Duration: 20 * time.Millisecond, Action: ActionAllow})
	m.obs.recordSpan(HookSpan{HookID: "h1", Duration: 60 * time.Millisecond, Action: ActionBlock, Error: errors.New("foo")})
	snaps := m.Stats()
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d", len(snaps))
	}
	s := snaps[0]
	if s.HookID != "h1" {
		t.Errorf("HookID=%s, want h1", s.HookID)
	}
	if s.ExecCount != 3 {
		t.Errorf("ExecCount=%d, want 3", s.ExecCount)
	}
	if s.FailCount != 1 {
		t.Errorf("FailCount=%d, want 1", s.FailCount)
	}
	if s.SlowCount != 1 {
		t.Errorf("SlowCount=%d, want 1", s.SlowCount)
	}
	if s.MaxDuration != 60*time.Millisecond {
		t.Errorf("MaxDuration=%s, want 60ms", s.MaxDuration)
	}
	// 平均耗时 = (10+20+60)/3 = 30ms
	if s.AvgDuration() != 30*time.Millisecond {
		t.Errorf("AvgDuration=%s, want 30ms", s.AvgDuration())
	}
}

// TestObservability_SlowHookAlert 慢钩子告警触发
func TestObservability_SlowHookAlert(t *testing.T) {
	alerted := make(chan HookSpan, 10)
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableStats:       true,
		EnableTrace:       true,
		SlowHookThreshold: 50 * time.Millisecond,
		SlowHookAlert:     &chanAlert{ch: alerted},
	})
	m.obs.recordSpan(HookSpan{HookID: "slow1", Duration: 100 * time.Millisecond})
	select {
	case sp := <-alerted:
		if sp.HookID != "slow1" {
			t.Errorf("alerted HookID=%s, want slow1", sp.HookID)
		}
	case <-time.After(time.Second):
		t.Fatal("slow hook alert not received")
	}
}

// TestObservability_AlertDedup 慢钩子告警去重（5s TTL）
func TestObservability_AlertDedup(t *testing.T) {
	alertCount := 0
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableStats:       true,
		SlowHookThreshold: 10 * time.Millisecond,
		SlowHookAlert:     &countingAlert{count: &alertCount},
	})
	// 同一 hook 连续 5 次慢调用，应只告警 1 次（5s 内）
	for i := 0; i < 5; i++ {
		m.obs.recordSpan(HookSpan{HookID: "slow2", Duration: 50 * time.Millisecond})
	}
	if alertCount != 1 {
		t.Errorf("alertCount=%d, want 1 (dedup)", alertCount)
	}
}

// TestObservability_RingBuffer 环形缓冲区覆盖
func TestObservability_RingBuffer(t *testing.T) {
	rb := newRingBuffer(3)
	for i := 0; i < 5; i++ {
		rb.Add(HookSpan{HookID: string(rune('a' + i))})
	}
	all := rb.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 spans, got %d", len(all))
	}
	// 应保留最近 3 条（c, d, e）
	if all[0].HookID != "c" || all[2].HookID != "e" {
		t.Errorf("FIFO order wrong: %v", all)
	}
}

// TestObservability_RingBufferDefaultSize 零值时使用默认大小
func TestObservability_RingBufferDefaultSize(t *testing.T) {
	rb := newRingBuffer(0)
	if len(rb.buf) != DefaultMaxTraceBuffer {
		t.Errorf("expected default size %d, got %d", DefaultMaxTraceBuffer, len(rb.buf))
	}
}

// TestObservability_RenderTrace 渲染输出
func TestObservability_RenderTrace(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableTrace:       true,
		EnableStats:       true,
		SlowHookThreshold: 50 * time.Millisecond,
		MaxTraceBuffer:    10,
	})
	m.obs.recordSpan(HookSpan{
		HookID:    "h1",
		EventType: EventPreToolUse,
		HookType:  HookTypeCommand,
		Duration:  5 * time.Millisecond,
		Action:    ActionAllow,
		Output:    "ok",
	})
	m.obs.recordSpan(HookSpan{
		HookID:    "h2",
		EventType: EventPreToolUse,
		HookType:  HookTypeHTTP,
		Duration:  60 * time.Millisecond,
		Action:    ActionAllow,
	})
	out := m.RenderTrace()
	if !strings.Contains(out, "hook trace") {
		t.Errorf("RenderTrace missing 'hook trace': %s", out)
	}
	if !strings.Contains(out, "h1") || !strings.Contains(out, "h2") {
		t.Errorf("RenderTrace missing hook IDs: %s", out)
	}
	if !strings.Contains(out, "slow") {
		t.Errorf("RenderTrace missing slow marker: %s", out)
	}
}

// TestObservability_RenderTraceOutputTruncate output 截断验证
func TestObservability_RenderTraceOutputTruncate(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableTrace:       true,
		EnableStats:       false,
		SlowHookThreshold: 1 * time.Hour,
		MaxTraceBuffer:    5,
	})
	longOutput := strings.Repeat("x", 1000)
	m.obs.recordSpan(HookSpan{
		HookID:   "h1",
		Duration: 1 * time.Millisecond,
		Action:   ActionAllow,
		Output:   longOutput,
	})
	out := m.RenderTrace()
	// 应包含截断标记
	if !strings.Contains(out, "truncated") {
		t.Errorf("expected truncated marker, got: %s", out)
	}
	// 输出不应包含完整的 1000 字符
	if strings.Contains(out, longOutput) {
		t.Errorf("output not truncated, contains full 1000 chars")
	}
}

// TestObservability_Concurrent 并发安全测试（子审查优化 7 吸纳，10 goroutine -race）
func TestObservability_Concurrent(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableTrace:       true,
		EnableStats:       true,
		SlowHookThreshold: 10 * time.Millisecond,
		SlowHookAlert:     &noopAlert{},
		MaxTraceBuffer:    50,
	})
	var wg sync.WaitGroup
	// 10 goroutine × 100 hooks = 1000 次 recordSpan
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.obs.recordSpan(HookSpan{
					HookID:   "h" + string(rune('A'+id)),
					Duration: 5 * time.Millisecond,
					Action:   ActionAllow,
				})
			}
		}(i)
	}
	wg.Wait()
	// 验证不崩溃 + 数据合理
	snaps := m.Stats()
	totalExec := int64(0)
	for _, s := range snaps {
		totalExec += s.ExecCount
	}
	if totalExec != 1000 {
		t.Errorf("totalExec=%d, want 1000", totalExec)
	}
	// 验证 RenderTrace 在并发后仍可用（不应 panic）
	_ = m.RenderTrace()
}

// TestObservability_DefaultConfig 默认配置测试
func TestObservability_DefaultConfig(t *testing.T) {
	cfg := DefaultObservabilityConfig()
	if cfg.EnableTrace != false {
		t.Errorf("EnableTrace should be false by default")
	}
	if cfg.EnableStats != true {
		t.Errorf("EnableStats should be true by default")
	}
	if cfg.SlowHookThreshold != DefaultSlowHookThreshold {
		t.Errorf("SlowHookThreshold=%s, want %s", cfg.SlowHookThreshold, DefaultSlowHookThreshold)
	}
	if cfg.MaxTraceBuffer != DefaultMaxTraceBuffer {
		t.Errorf("MaxTraceBuffer=%d, want %d", cfg.MaxTraceBuffer, DefaultMaxTraceBuffer)
	}
}

// TestObservability_ZeroThresholdConfig 零值阈值修正为默认值
func TestObservability_ZeroThresholdConfig(t *testing.T) {
	m := NewManager(t.TempDir())
	// SlowHookThreshold=0 和 MaxTraceBuffer=0 应被修正为默认值
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableStats:       true,
		SlowHookThreshold: 0,
		MaxTraceBuffer:    0,
	})
	if m.obs.cfg.SlowHookThreshold != DefaultSlowHookThreshold {
		t.Errorf("SlowHookThreshold not corrected: %s", m.obs.cfg.SlowHookThreshold)
	}
	if m.obs.cfg.MaxTraceBuffer != DefaultMaxTraceBuffer {
		t.Errorf("MaxTraceBuffer not corrected: %d", m.obs.cfg.MaxTraceBuffer)
	}
}

// TestObservability_StatsEmptyBeforeRecord 启用 stats 但未记录时返回空切片
func TestObservability_StatsEmptyBeforeRecord(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableStats:       true,
		SlowHookThreshold: 50 * time.Millisecond,
	})
	snaps := m.Stats()
	if len(snaps) != 0 {
		t.Errorf("expected empty snapshots, got %d", len(snaps))
	}
}

// TestObservability_RenderTraceEmpty 启用 trace 但无 span 时返回空提示
func TestObservability_RenderTraceEmpty(t *testing.T) {
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableTrace:       true,
		SlowHookThreshold: 50 * time.Millisecond,
	})
	out := m.RenderTrace()
	if !strings.Contains(out, "no spans recorded") {
		t.Errorf("expected 'no spans recorded', got: %s", out)
	}
}

// TestObservability_SinkInjection sink 注入测试
func TestObservability_SinkInjection(t *testing.T) {
	traceSinks := make(chan HookSpan, 10)
	statsObs := make(chan statsObserved, 10)
	m := NewManager(t.TempDir())
	m.SetObservabilityConfig(ObservabilityConfig{
		EnableTrace:       true,
		EnableStats:       true,
		SlowHookThreshold: 1 * time.Hour,
		TraceSink:         &chanTraceSink{ch: traceSinks},
		StatsSink:         &chanStatsSink{ch: statsObs},
	})
	m.obs.recordSpan(HookSpan{HookID: "h1", Duration: 5 * time.Millisecond, Action: ActionAllow})
	select {
	case sp := <-traceSinks:
		if sp.HookID != "h1" {
			t.Errorf("TraceSink got HookID=%s, want h1", sp.HookID)
		}
	case <-time.After(time.Second):
		t.Fatal("TraceSink not called")
	}
	select {
	case obs := <-statsObs:
		if obs.hookID != "h1" {
			t.Errorf("StatsSink got hookID=%s, want h1", obs.hookID)
		}
	case <-time.After(time.Second):
		t.Fatal("StatsSink not called")
	}
}

// 辅助实现

type chanAlert struct{ ch chan<- HookSpan }

func (c *chanAlert) OnSlowHook(span HookSpan) { c.ch <- span }

type countingAlert struct{ count *int }

func (c *countingAlert) OnSlowHook(_ HookSpan) { *c.count++ }

type noopAlert struct{}

func (noopAlert) OnSlowHook(_ HookSpan) {}

type chanTraceSink struct{ ch chan<- HookSpan }

func (s *chanTraceSink) Record(span HookSpan) { s.ch <- span }

type statsObserved struct {
	hookID   string
	duration time.Duration
	failed   bool
	slow     bool
}

type chanStatsSink struct{ ch chan<- statsObserved }

func (s *chanStatsSink) Observe(hookID string, duration time.Duration, failed, slow bool) {
	s.ch <- statsObserved{hookID: hookID, duration: duration, failed: failed, slow: slow}
}
