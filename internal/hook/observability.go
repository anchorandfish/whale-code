// observability.go — 钩子可观测性（/ T1-P2-G5-M16Hook可观测性）。
//
// 提供钩子执行链可视化（M16-01，对标 VSCode Task 输出）与执行统计/慢钩子告警（M16-03）。
// 默认零开销：obs=nil 时全部 noop；EnableStats 默认 true 但用 atomic.Int64；EnableTrace 默认 false。
//
// 设计原则（候选 C，）：
// - CGO_ENABLED=0 严格合规（纯 Go 标准库，sync/atomic + time + strings）
// - 默认零开销：obs=nil 时全部 noop
// - 慢钩子阈值可配置 + SlowHookAlert 回调接口 + 5s TTL 去重
// - ringBuffer 100 条 FIFO 覆盖（防止内存膨胀）
// - StatsSink / TraceSink 接口可注入 Prometheus / log / OTel adapter
//
// 官方源：
// - VSCode Task 输出: https://code.visualstudio.com/docs/editor/tasks @2026-07-21
// - Go sync/atomic: https://pkg.go.dev/sync/atomic @2026-07-21
// - Go container/ring: https://pkg.go.dev/container/ring @2026-07-21
// - Prometheus alerting: https://prometheus.io/docs/prometheus/latest/configuration/alerting_rules/ @2026-07-21
package hook

import (
	"sync"
	"sync/atomic"
	"time"
)

// 默认配置常量（子审查优化 1 吸纳：避免魔法数字）
const (
	// DefaultSlowHookThreshold 是慢钩子默认阈值（5s）。
	// 超过此阈值的 hook 执行触发 SlowHookAlert 回调。
	DefaultSlowHookThreshold = 5 * time.Second

	// DefaultMaxTraceBuffer 是 trace 环形缓冲区默认容量（100 条 span）。
	// FIFO 覆盖：满 100 条后新增会覆盖最旧条目。
	DefaultMaxTraceBuffer = 100

	// defaultAlertDedupTTL 是慢钩子告警去重 TTL（5s）。
	// 同一 hook ID 在 5s 内只触发一次 SlowHookAlert 回调，防止告警风暴。
	defaultAlertDedupTTL = 5 * time.Second

	// defaultOutputTruncateLen 是 RenderTrace 中单 span output 截断长度（256 字节）。
	// 超长 output 截断后追加 "... (truncated)" 标记。
	defaultOutputTruncateLen = 256
)

// HookSpan 单个钩子执行的 span 记录（trace 单元）。
//
// 字段全部为值类型（非指针），可安全在 goroutine 间传递；
// Children 是值切片（深拷贝时需要单独处理，但本实现仅记录单层 span，Children 留作扩展）。
type HookSpan struct {
	HookID    string        // 钩子唯一 ID
	HookType  HookType      // 钩子类型（command/prompt/agent/http/callback）
	EventType HookEventType // 触发的事件类型（PreToolUse/PostToolUse 等）
	ToolName  string        // 触发钩子的工具名
	Start     time.Time     // 钩子执行开始时间
	Duration  time.Duration // 钩子执行总耗时
	Action    Action        // 钩子返回的动作（allow/block/ask/warn）
	Error     error         // 钩子执行错误（nil 表示无错误）
	Output    string        // 钩子输出（stdout / response body），渲染时截断
	Children  []HookSpan    // 子 span（预留：HTTP 钩子的子请求/命令钩子的子进程）
}

// TraceSink 是 trace 输出 sink 接口（可注入 Prometheus / log / OTel adapter）。
//
// 实现约定：
// - Record 应为非阻塞调用（内部异步或快速同步）
// - Record 不应 panic（observabilityState.recordSpan 会 recover 保护，但实现方仍应自律）
type TraceSink interface {
	Record(span HookSpan)
}

// StatsSink 是统计输出 sink 接口（可注入 Prometheus / log）。
//
// 实现约定：
// - Observe 应为非阻塞调用
// - 参数 slow 由调用方依据 SlowHookThreshold 判定后传入
type StatsSink interface {
	Observe(hookID string, duration time.Duration, failed bool, slow bool)
}

// SlowHookAlert 是慢钩子告警回调接口。
//
// 当 span.Duration >= ObservabilityConfig.SlowHookThreshold 时触发；
// 同一 hook ID 在 defaultAlertDedupTTL (5s) 内只触发一次（去重）。
// 实现方应处理 panic（observabilityState 中已 recover 保护，但仍建议自律）。
type SlowHookAlert interface {
	OnSlowHook(span HookSpan)
}

// ObservabilityConfig 是可观测性配置。
//
// 零值时全部禁用（与 obs=nil 等效）；推荐用 DefaultObservabilityConfig() 初始化。
type ObservabilityConfig struct {
	// EnableTrace 是否收集 span 到 ringBuffer（默认 false，零开销）。
	EnableTrace bool
	// EnableStats 是否收集统计（默认 true，用 atomic.Int64，轻量）。
	EnableStats bool
	// SlowHookThreshold 慢钩子阈值（0 表示用 DefaultSlowHookThreshold）。
	SlowHookThreshold time.Duration
	// TraceSink trace 输出 sink（nil 表示仅存内存 ringBuffer）。
	TraceSink TraceSink
	// StatsSink 统计输出 sink（nil 表示仅存内存供 Stats() 查询）。
	StatsSink StatsSink
	// SlowHookAlert 慢钩子告警回调（nil 表示不触发回调）。
	SlowHookAlert SlowHookAlert
	// MaxTraceBuffer ringBuffer 容量上限（0 表示用 DefaultMaxTraceBuffer）。
	MaxTraceBuffer int
}

// DefaultObservabilityConfig 返回默认配置。
//
// 默认值：
// - EnableTrace=false（零开销）
// - EnableStats=true（轻量统计）
// - SlowHookThreshold=DefaultSlowHookThreshold (5s)
// - MaxTraceBuffer=DefaultMaxTraceBuffer (100)
func DefaultObservabilityConfig() ObservabilityConfig {
	return ObservabilityConfig{
		EnableTrace:       false,
		EnableStats:       true,
		SlowHookThreshold: DefaultSlowHookThreshold,
		MaxTraceBuffer:    DefaultMaxTraceBuffer,
	}
}

// HookStatsSnapshot 是单个 hook ID 的统计快照（值类型，避免内部状态泄漏）。
type HookStatsSnapshot struct {
	HookID        string
	ExecCount     int64
	FailCount     int64
	SlowCount     int64
	TotalDuration time.Duration
	MaxDuration   time.Duration
}

// AvgDuration 返回平均耗时（零除保护）。
func (s HookStatsSnapshot) AvgDuration() time.Duration {
	if s.ExecCount == 0 {
		return 0
	}
	return s.TotalDuration / time.Duration(s.ExecCount)
}

// statsEntry 单个 hook 的统计条目（并发安全，子审查优化 4 吸纳：用 atomic 而非 mutex）。
//
// 字段全部 atomic 类型，可被多 goroutine 并发读写；
// statsEntry 本身是值类型，但内部 atomic 字段按指针语义生效。
type statsEntry struct {
	execCount       atomic.Int64 // 累计执行次数
	failCount       atomic.Int64 // 累计失败次数（Error != nil 或 Action=Block）
	slowCount       atomic.Int64 // 累计慢调用次数（Duration >= SlowHookThreshold）
	totalDurationNs atomic.Int64 // 累计总耗时（纳秒）
	maxDurationNs   atomic.Int64 // 最大单次耗时（纳秒）
}

// StatsCollector 钩子执行统计收集器（并发安全）。
//
// 内部维护 map[hookID]*statsEntry，用 RWMutex 保护 map 操作；
// 单个 statsEntry 的字段更新用 atomic，避免锁竞争。
type StatsCollector struct {
	mu      sync.RWMutex
	entries map[string]*statsEntry
}

// NewStatsCollector 创建统计收集器。
func NewStatsCollector() *StatsCollector {
	return &StatsCollector{entries: make(map[string]*statsEntry)}
}

// Observe 记录一次钩子执行。
//
// 参数：
// - hookID: 钩子唯一 ID
// - duration: 本次执行耗时
// - failed: 是否失败（Error != nil 或 Action=Block）
// - slow: 是否慢调用（Duration >= SlowHookThreshold，由调用方判定）
func (sc *StatsCollector) Observe(hookID string, duration time.Duration, failed, slow bool) {
	// 双检锁模式：先 RLock 读，未命中再 Lock 写
	sc.mu.RLock()
	e, ok := sc.entries[hookID]
	sc.mu.RUnlock()
	if !ok {
		sc.mu.Lock()
		if e, ok = sc.entries[hookID]; !ok {
			e = &statsEntry{}
			sc.entries[hookID] = e
		}
		sc.mu.Unlock()
	}
	e.execCount.Add(1)
	if failed {
		e.failCount.Add(1)
	}
	if slow {
		e.slowCount.Add(1)
	}
	ns := int64(duration)
	e.totalDurationNs.Add(ns)
	// CAS 循环更新 maxDurationNs
	for {
		cur := e.maxDurationNs.Load()
		if ns <= cur {
			break
		}
		if e.maxDurationNs.CompareAndSwap(cur, ns) {
			break
		}
	}
}

// Snapshot 返回所有 hook ID 的统计快照（按 hook ID 维度）。
//
// 返回切片为副本，调用方可安全修改。
func (sc *StatsCollector) Snapshot() []HookStatsSnapshot {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	out := make([]HookStatsSnapshot, 0, len(sc.entries))
	for id, e := range sc.entries {
		out = append(out, HookStatsSnapshot{
			HookID:        id,
			ExecCount:     e.execCount.Load(),
			FailCount:     e.failCount.Load(),
			SlowCount:     e.slowCount.Load(),
			TotalDuration: time.Duration(e.totalDurationNs.Load()),
			MaxDuration:   time.Duration(e.maxDurationNs.Load()),
		})
	}
	return out
}

// ringBuffer 环形缓冲区（子审查优化 2 吸纳：FIFO 覆盖防止内存膨胀）。
//
// 容量固定，满后新增覆盖最旧条目；All() 返回按时间顺序的副本。
type ringBuffer struct {
	mu   sync.Mutex
	buf  []HookSpan
	next int // 下一个写入位置
	size int // 当前有效条数（<= len(buf)）
}

// newRingBuffer 创建环形缓冲区。
//
// 参数 size<=0 时用 DefaultMaxTraceBuffer。
func newRingBuffer(size int) *ringBuffer {
	if size <= 0 {
		size = DefaultMaxTraceBuffer
	}
	return &ringBuffer{buf: make([]HookSpan, size)}
}

// Add 添加一个 span（满后覆盖最旧）。
func (rb *ringBuffer) Add(span HookSpan) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.buf[rb.next] = span
	rb.next = (rb.next + 1) % len(rb.buf)
	if rb.size < len(rb.buf) {
		rb.size++
	}
}

// All 返回所有 span（按时间顺序）。
func (rb *ringBuffer) All() []HookSpan {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]HookSpan, 0, rb.size)
	// 计算起始位置（最旧条目）
	start := (rb.next - rb.size + len(rb.buf)) % len(rb.buf)
	for i := 0; i < rb.size; i++ {
		out = append(out, rb.buf[(start+i)%len(rb.buf)])
	}
	return out
}

// observabilityState 是 Manager 持有的可观测性状态。
//
// 由 SetObservabilityConfig 创建；obs=nil 时全 noop。
type observabilityState struct {
	cfg        ObservabilityConfig
	stats      *StatsCollector
	traceBuf   *ringBuffer
	alertMu    sync.Mutex
	alertDedup map[string]time.Time // hook ID -> 上次告警时间（去重用）
}

// newObservabilityState 创建可观测性状态。
func newObservabilityState(cfg ObservabilityConfig) *observabilityState {
	// 修正零值
	if cfg.SlowHookThreshold <= 0 {
		cfg.SlowHookThreshold = DefaultSlowHookThreshold
	}
	if cfg.MaxTraceBuffer <= 0 {
		cfg.MaxTraceBuffer = DefaultMaxTraceBuffer
	}
	return &observabilityState{
		cfg:        cfg,
		stats:      NewStatsCollector(),
		traceBuf:   newRingBuffer(cfg.MaxTraceBuffer),
		alertDedup: make(map[string]time.Time),
	}
}

// recordSpan 记录 span 到 sink + ringBuffer + 触发慢钩子告警。
//
// 子审查优化 3 吸纳：alert 回调用 recover 包裹，避免 panic 影响主流程。
func (o *observabilityState) recordSpan(span HookSpan) {
	if o.cfg.EnableTrace {
		o.traceBuf.Add(span)
		if o.cfg.TraceSink != nil {
			o.cfg.TraceSink.Record(span)
		}
	}
	if o.cfg.EnableStats {
		slow := span.Duration >= o.cfg.SlowHookThreshold
		failed := span.Error != nil || span.Action == ActionBlock
		o.stats.Observe(span.HookID, span.Duration, failed, slow)
		if o.cfg.StatsSink != nil {
			o.cfg.StatsSink.Observe(span.HookID, span.Duration, failed, slow)
		}
		if slow && o.cfg.SlowHookAlert != nil {
			o.maybeAlert(span)
		}
	}
}

// maybeAlert 慢钩子告警去重（子审查优化 6 吸纳：5s TTL）。
//
// 同一 hook ID 在 defaultAlertDedupTTL (5s) 内只触发一次回调；
// 懒清理：每次访问时检查过期条目并清理。
func (o *observabilityState) maybeAlert(span HookSpan) {
	o.alertMu.Lock()
	now := time.Now()
	// 懒清理过期条目
	for id, t := range o.alertDedup {
		if now.Sub(t) >= defaultAlertDedupTTL {
			delete(o.alertDedup, id)
		}
	}
	if last, exists := o.alertDedup[span.HookID]; exists && now.Sub(last) < defaultAlertDedupTTL {
		o.alertMu.Unlock()
		return
	}
	o.alertDedup[span.HookID] = now
	o.alertMu.Unlock()

	// recover 保护：alert 回调 panic 不影响主流程
	defer func() { _ = recover() }()
	o.cfg.SlowHookAlert.OnSlowHook(span)
}
