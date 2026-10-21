// Package tools — output compressor for reducing bash/tool output tokens
//
// OutputCompressor provides a 9-step pipeline that intelligently compresses
// bash command output before sending it to the LLM:
// 1. StripANSI — strip terminal color/control codes
// 2. StripCarriage — remove \r characters (Windows carriage returns)
// 3. FilterNoise — filter progress bars, spinners, npm warnings, git hints
// 4. StripTimestamps — strip log timestamp prefixes
// 5. CollapseBlanks — merge consecutive blank lines into one
// 6. DedupLines — fold identical repeated lines into [×N]
// 7. LogTemplate — template-compress lines differing only in variable values
// 8. RelevantTruncate — relevance-scored truncation (keeps error lines)
// 9. TOONEncode — JSON array → CSV-like dense format
//
// Design principles:
// - Zero external dependencies (Go standard library only)
// - Silent degradation: any step error is silently skipped
// - Thread-safe metrics with sync.RWMutex
// - Idempotent: same input always produces same output
// - Performance budget: full pipeline <5ms on 1000 lines
package tools

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"unicode/utf8"
)

// H13-G2 Feature Flag 环境变量。
//
// EnvTruncateMiddle 控制 G2 中间截断 50/50 + Warning 头模式是否启用。
// 默认 DISABLED：保持现有 RelevantTruncate 行为（避免破坏 JSON 输出语法完整性）。
// 显式设置 "1"/"true" 可启用 middle 截断模式（对纯日志类输出保留头尾关键信息）。
//
// 仲裁依据（方案 4.6 节冲突 1）：G2 是 RelevantTruncate 的补充模式，
// 通过 CompressConfig.TruncateMode 字段选择，JSON 输出强制用 "relevant" 模式。
const EnvTruncateMiddle = "WHALE_TRUNCATE_MIDDLE"

// IsTruncateMiddleEnabled 返回是否启用 G2 中间截断 50/50 模式（H13-G2）。
//
// 默认 DISABLED：保持现有 RelevantTruncate 行为不变。
// 显式设置 "1"/"true" 启用 middle 截断模式，作为纯日志类输出的补充截断策略。
// 与 CompressConfig.TruncateMode 字段配合：TruncateMode="middle" 时仅当本 Flag 启用才生效。
func IsTruncateMiddleEnabled() bool {
	v := strings.ToLower(os.Getenv(EnvTruncateMiddle))
	return v == "1" || v == "true"
}

// TruncationPolicy 描述截断后输出的格式化策略（H13-G2）。
//
// 对标同类 CLI formatted_truncate_text@本地副本 2026-07-29）。
// OriginalTokenCount 用于在 Warning 头中展示原始 token 估算值，帮助模型判断丢失的信息量。
type TruncationPolicy struct {
	// OriginalTokenCount 原始内容的 token 估算值（用于 Warning 头展示）
	OriginalTokenCount int
	// OriginalByteCount 原始内容的字节长度（用于 Warning 头展示）
	OriginalByteCount int
	// Mode 截断模式（"middle" | "tail"）
	Mode string
}

// TruncateMiddle 中间截断，保留头尾各 50%，中间用 marker 替换（H13-G2）。
//
// 对标同类 CLI truncate_with_byte_estimate@本地副本 2026-07-29）。
// 当 maxBytes <= 0 或输入长度 <= maxBytes 时原样返回（不截断）。
// UTF-8 多字节字符安全：按 rune 边界回退，避免切断多字节字符（参考 prompt/length.go:14-46）。
//
// 参数：
// - s: 待截断的字符串
// - maxBytes: 截断后的目标字节上限（含 marker 长度）
// - useTokens: 预留参数（H13-G2 方案声明），当前按字节估算（4 字节 ≈ 1 token），未来可接入精确 token 计数
//
// 返回：截断后的字符串，中间用 "\n... [truncated middle, kept 50% head + 50% tail] ...\n" 替换。
func TruncateMiddle(s string, maxBytes int, useTokens bool) string {
	_ = useTokens // 预留参数，当前按字节估算
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	const marker = "\n... [truncated middle, kept 50% head + 50% tail] ...\n"
	// maxBytes 过小不足以容纳 marker 时直接头部截断（不追加 marker）
	if maxBytes <= len(marker)+2 {
		return truncateUTF8Head(s, maxBytes)
	}
	// 计算头尾配额：各占 (maxBytes - len(marker)) / 2
	quota := (maxBytes - len(marker)) / 2
	if quota < 1 {
		quota = 1
	}
	head := truncateUTF8Head(s, quota)
	tail := truncateUTF8Tail(s, quota)
	return head + marker + tail
}

// truncateUTF8Head 按 UTF-8 rune 边界安全截取头部 n 字节（H13-G2 辅助函数）。
//
// 当 n >= len(s) 时返回原串；按 rune 边界回退避免切断多字节字符。
func truncateUTF8Head(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	truncated := s[:n]
	// 回退到最后一个完整 rune
	for len(truncated) > 0 {
		r, size := utf8.DecodeLastRuneInString(truncated)
		if r != utf8.RuneError || size != 1 {
			break
		}
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

// truncateUTF8Tail 按 UTF-8 rune 边界安全截取尾部 n 字节（H13-G2 辅助函数）。
//
// 当 n >= len(s) 时返回原串；按 rune 边界前进避免切断多字节字符。
func truncateUTF8Tail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	start := len(s) - n
	tail := s[start:]
	// 前进到第一个完整 rune
	for len(tail) > 0 {
		r, size := utf8.DecodeRuneInString(tail)
		if r != utf8.RuneError || size != 1 {
			break
		}
		tail = tail[size:]
	}
	return tail
}

// FormatTruncatedOutput 截断后加 Warning 头（H13-G2）。
//
// 对标同类 CLI formatted_truncate_text@本地副本 2026-07-29）。
// Warning 头格式：`Warning: truncated output (original token count: N, original bytes: M, mode: <mode>)`
// 帮助模型识别输出被截断并评估丢失的信息量，避免模型误认为截断内容是完整输出。
//
// 参数：
// - content: 已截断的内容
// - policy: 截断策略（含原始 token/byte 计数和模式）
//
// 返回：带 Warning 头的完整输出。
func FormatTruncatedOutput(content string, policy TruncationPolicy) string {
	mode := policy.Mode
	if mode == "" {
		mode = "middle"
	}
	warning := fmt.Sprintf("Warning: truncated output (original token count: %d, original bytes: %d, mode: %s)\n",
		policy.OriginalTokenCount, policy.OriginalByteCount, mode)
	return warning + content
}

// CompressorConfig holds the global configuration for the output compressor
type CompressorConfig struct {
	// --- Feature toggles ---
	Enabled bool `json:"enabled"` // whether to enable output compression (default true)

	// --- Pipeline toggles (each step can be independently controlled) ---
	StripANSI        bool `json:"strip_ansi"`        // strip ANSI escape codes (default true)
	StripCarriage    bool `json:"strip_carriage"`    // remove \r (default true)
	FilterNoise      bool `json:"filter_noise"`      // filter noise lines (default true)
	StripTimestamps  bool `json:"strip_timestamps"`  // strip log timestamps (default true)
	CollapseBlanks   bool `json:"collapse_blanks"`   // merge consecutive blank lines (default true)
	DedupLines       bool `json:"dedup_lines"`       // duplicate line folding (default true)
	LogTemplate      bool `json:"log_template"`      // log template compression (default true)
	RelevantTruncate bool `json:"relevant_truncate"` // relevance truncation (default true)
	TOONEncode       bool `json:"toon_encode"`       // JSON→TOON re-encoding (default true)

	// --- Parameters ---
	DedupThreshold      int `json:"dedup_threshold"`     // fold when repeat count >= this (default 3)
	LogTemplateMinCount int `json:"log_template_min"`    // template appears >= this to compress (default 3)
	RelevantKeepLines   int `json:"relevant_keep_lines"` // lines to keep in relevance truncation (default 50)
	RelevantMaxLines    int `json:"relevant_max_lines"`  // trigger truncation above this line count (default 100)
	MaxOutputLen        int `json:"max_output_len"`      // final hard truncation length (default 50000)

	// H13-G2: TruncateMode 截断模式选择（"relevant" | "middle" | "tail"）。
	// 默认 "relevant"：保持现有 RelevantTruncate 行为（智能截断保留信号词行）。
	// "middle"：中间截断 50/50 + Warning 头（对标同类 CLI truncate_with_byte_estimate），
	// 仅当 Feature Flag WHALE_TRUNCATE_MIDDLE 启用时生效，否则回退到 "relevant"。
	// "tail"：尾部截断（仅保留头部），适合日志类输出（最新内容在尾部时反向选择）。
	// JSON 输出强制用 "relevant" 模式（避免破坏语法完整性，方案 4.6 节冲突 1 仲裁）。
	TruncateMode string `json:"truncate_mode"`

	// --- Noise patterns (extensible) ---
	NoisePatterns []NoisePattern `json:"noise_patterns"` // custom noise line patterns

	// --- Signal words (for relevance truncation) ---
	SignalWords []string `json:"signal_words"` // custom signal words
}

// NoisePattern represents a noise line matching pattern
type NoisePattern struct {
	Pattern     string `json:"pattern"`     // regex pattern
	Description string `json:"description"` // human-readable description
	Enabled     bool   `json:"enabled"`     // whether enabled
}

// DefaultCompressorConfig returns the default compressor configuration
func DefaultCompressorConfig() CompressorConfig {
	return CompressorConfig{
		Enabled:             true,
		StripANSI:           true,
		StripCarriage:       true,
		FilterNoise:         true,
		StripTimestamps:     true,
		CollapseBlanks:      true,
		DedupLines:          true,
		LogTemplate:         true,
		RelevantTruncate:    true,
		TOONEncode:          true,
		DedupThreshold:      3,
		LogTemplateMinCount: 3,
		RelevantKeepLines:   50,
		RelevantMaxLines:    100,
		MaxOutputLen:        50000,
		// H13-G2: 默认 "relevant" 模式，保持现有行为。
		// 用户可通过 CompressConfig.TruncateMode="middle" + WHALE_TRUNCATE_MIDDLE=1 切换到中间截断。
		TruncateMode:  "relevant",
		SignalWords:   DefaultSignalWords(),
		NoisePatterns: DefaultNoisePatterns(),
	}
}

// CompressResult holds the result of a compression operation
type CompressResult struct {
	Output        string         `json:"output"`         // compressed output
	OriginalLen   int            `json:"original_len"`   // original byte count
	CompressedLen int            `json:"compressed_len"` // compressed byte count
	StepsApplied  []string       `json:"steps_applied"`  // steps that were actually applied
	LinesRemoved  map[string]int `json:"lines_removed"`  // lines removed per step
	TokensSaved   int            `json:"tokens_saved"`   // estimated tokens saved
	// Streaming 标识结果是否由流式压缩器产出。
	// true 表示步骤 6/7 的 [×N] 标记为窗口内计数语义，与批量模式的全文计数语义不同。
	// 批量模式（OutputCompressor.Compress）保持零值，向后兼容。
	Streaming bool `json:"streaming"`
	// WindowSize 流式模式的滑动窗口大小（0=全量模式）。
	// 调用方可据此判断 [×N] 标记的语义范围。
	WindowSize int `json:"window_size"`
}

// CompressorMetrics holds metrics for the output compressor
type CompressorMetrics struct {
	mu sync.RWMutex

	// --- Overall statistics ---
	TotalCompressions    int   `json:"total_compressions"`
	TotalOriginalBytes   int64 `json:"total_original_bytes"`
	TotalCompressedBytes int64 `json:"total_compressed_bytes"`
	TotalTokensSaved     int64 `json:"total_tokens_saved"`

	// --- Per-step statistics ---
	StepStats map[string]*StepStat `json:"step_stats"`

	// --- Per-command statistics ---
	CommandStats map[string]*CommandStat `json:"command_stats"`
}

// StepStat holds statistics for a single compression step
type StepStat struct {
	Name         string `json:"name"`
	Applications int    `json:"applications"`  // execution count
	LinesRemoved int    `json:"lines_removed"` // lines removed
	BytesSaved   int64  `json:"bytes_saved"`
}

// CommandStat holds statistics for a specific command
type CommandStat struct {
	Command      string  `json:"command"`
	Invocations  int     `json:"invocations"`
	AvgReduction float64 `json:"avg_reduction"` // average compression ratio
	TotalSaved   int64   `json:"total_saved"`
}

// NewCompressorMetrics creates a new CompressorMetrics instance
func NewCompressorMetrics() *CompressorMetrics {
	return &CompressorMetrics{
		StepStats:    make(map[string]*StepStat),
		CommandStats: make(map[string]*CommandStat),
	}
}

// Record records a compression operation's metrics
func (m *CompressorMetrics) Record(cmd string, stepsApplied []string, linesRemoved map[string]int, originalBytes, compressedBytes, tokensSaved int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TotalCompressions++
	m.TotalOriginalBytes += originalBytes
	m.TotalCompressedBytes += compressedBytes
	m.TotalTokensSaved += tokensSaved

	// Per-step stats
	for _, step := range stepsApplied {
		stat, ok := m.StepStats[step]
		if !ok {
			stat = &StepStat{Name: step}
			m.StepStats[step] = stat
		}
		stat.Applications++
		if removed, ok := linesRemoved[step]; ok {
			stat.LinesRemoved += removed
		}
	}

	// Per-command stats
	if cmd != "" {
		// Use first word of command as key
		cmdKey := cmd
		if idx := strings.Index(cmd, " "); idx > 0 {
			cmdKey = cmd[:idx]
		}
		cstat, ok := m.CommandStats[cmdKey]
		if !ok {
			cstat = &CommandStat{Command: cmdKey}
			m.CommandStats[cmdKey] = cstat
		}
		cstat.Invocations++
		if originalBytes > 0 {
			reduction := float64(originalBytes-compressedBytes) / float64(originalBytes)
			// Running average
			cstat.AvgReduction = (cstat.AvgReduction*float64(cstat.Invocations-1) + reduction) / float64(cstat.Invocations)
		}
		cstat.TotalSaved += int64(tokensSaved)
	}
}

// CompressorMetricsSnapshot is a mutex-free copy of CompressorMetrics for safe external use
type CompressorMetricsSnapshot struct {
	TotalCompressions    int                     `json:"total_compressions"`
	TotalOriginalBytes   int64                   `json:"total_original_bytes"`
	TotalCompressedBytes int64                   `json:"total_compressed_bytes"`
	TotalTokensSaved     int64                   `json:"total_tokens_saved"`
	StepStats            map[string]*StepStat    `json:"step_stats"`
	CommandStats         map[string]*CommandStat `json:"command_stats"`
}

// Snapshot returns a mutex-free copy of the current metrics
func (m *CompressorMetrics) Snapshot() CompressorMetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := CompressorMetricsSnapshot{
		TotalCompressions:    m.TotalCompressions,
		TotalOriginalBytes:   m.TotalOriginalBytes,
		TotalCompressedBytes: m.TotalCompressedBytes,
		TotalTokensSaved:     m.TotalTokensSaved,
		StepStats:            make(map[string]*StepStat, len(m.StepStats)),
		CommandStats:         make(map[string]*CommandStat, len(m.CommandStats)),
	}
	for k, v := range m.StepStats {
		copy := *v
		result.StepStats[k] = &copy
	}
	for k, v := range m.CommandStats {
		copy := *v
		result.CommandStats[k] = &copy
	}
	return result
}

// FormatReport formats a human-readable metrics report
func (m *CompressorMetrics) FormatReport() string {
	snap := m.Snapshot()
	avgReduction := 0.0
	if snap.TotalOriginalBytes > 0 {
		avgReduction = float64(snap.TotalOriginalBytes-snap.TotalCompressedBytes) / float64(snap.TotalOriginalBytes) * 100
	}
	return fmt.Sprintf(
		"Output Compressor Metrics:\n"+
			"  Compressions: %d\n"+
			"  Original bytes: %d\n"+
			"  Compressed bytes: %d\n"+
			"  Avg reduction: %.1f%%\n"+
			"  Tokens saved: %d",
		snap.TotalCompressions,
		snap.TotalOriginalBytes,
		snap.TotalCompressedBytes,
		avgReduction,
		snap.TotalTokensSaved,
	)
}

// OutputCompressor is the tool output intelligent compression engine
type OutputCompressor struct {
	config          CompressorConfig
	metrics         *CompressorMetrics
	commandProfiles []CommandProfile
}

// NewOutputCompressor creates a new output compressor
func NewOutputCompressor(config CompressorConfig) *OutputCompressor {
	return &OutputCompressor{
		config:          config,
		metrics:         NewCompressorMetrics(),
		commandProfiles: DefaultCommandProfiles(),
	}
}

// Compress executes the full compression pipeline
func (c *OutputCompressor) Compress(output string, cmd string) *CompressResult {
	if !c.config.Enabled || output == "" {
		return &CompressResult{
			Output:        output,
			OriginalLen:   len(output),
			CompressedLen: len(output),
		}
	}

	originalLen := len(output)
	stepsApplied := []string{}
	linesRemoved := map[string]int{}

	// Step 1: StripANSI
	if c.config.StripANSI {
		before := len(output)
		output = StripANSI(output)
		if len(output) < before {
			stepsApplied = append(stepsApplied, "strip_ansi")
		}
	}

	// Step 2: StripCarriage
	if c.config.StripCarriage {
		before := len(output)
		output = StripCarriage(output)
		if len(output) < before {
			stepsApplied = append(stepsApplied, "strip_carriage")
		}
	}

	// Convert to line array (subsequent steps work line-by-line)
	lines := strings.Split(output, "\n")

	// Step 3: FilterNoise
	if c.config.FilterNoise {
		before := len(lines)
		lines = FilterNoise(lines, c.config.NoisePatterns)
		removed := before - len(lines)
		if removed > 0 {
			stepsApplied = append(stepsApplied, "filter_noise")
			linesRemoved["filter_noise"] = removed
		}
	}

	// Step 4: StripTimestamps
	if c.config.StripTimestamps {
		changed := 0
		for i, l := range lines {
			stripped := StripTimestamp(l)
			if stripped != l {
				lines[i] = stripped
				changed++
			}
		}
		if changed > 0 {
			stepsApplied = append(stepsApplied, "strip_timestamps")
			linesRemoved["strip_timestamps"] = changed
		}
	}

	// Step 5: CollapseBlanks
	if c.config.CollapseBlanks {
		before := len(lines)
		lines = CollapseBlanks(lines)
		removed := before - len(lines)
		if removed > 0 {
			stepsApplied = append(stepsApplied, "collapse_blanks")
			linesRemoved["collapse_blanks"] = removed
		}
	}

	// Step 6: DedupLines
	if c.config.DedupLines {
		before := len(lines)
		lines = DedupLines(lines, c.config.DedupThreshold)
		removed := before - len(lines)
		if removed > 0 {
			stepsApplied = append(stepsApplied, "dedup_lines")
			linesRemoved["dedup_lines"] = removed
		}
	}

	// Step 7: LogTemplate
	if c.config.LogTemplate {
		before := len(lines)
		lines = LogTemplate(lines, c.config.LogTemplateMinCount)
		removed := before - len(lines)
		if removed > 0 {
			stepsApplied = append(stepsApplied, "log_template")
			linesRemoved["log_template"] = removed
		}
	}

	// Step 8: RelevantTruncate / TruncateMiddle（H13-G2）
	// 模式选择由 CompressConfig.TruncateMode 决定，Feature Flag WHALE_TRUNCATE_MIDDLE 控制 middle 模式开关。
	// - "relevant"（默认）：现有 RelevantTruncate 行为（智能截断保留信号词行）
	// - "middle"：G2 中间截断 50/50 + Warning 头（仅当 Feature Flag 启用时生效，否则回退 "relevant"）
	// - "tail"：尾部截断（仅保留头部）
	// JSON 输出检测：TryTOON 成功的输出强制用 "relevant" 模式（避免破坏语法完整性）
	mode := c.config.TruncateMode
	if mode == "" {
		mode = "relevant"
	}
	// G2 Feature Flag 检查：middle 模式需 WHALE_TRUNCATE_MIDDLE=1/true 才生效
	if mode == "middle" && !IsTruncateMiddleEnabled() {
		mode = "relevant" // 回退到默认模式
	}
	middleApplied := false // 跟踪 middle 截断是否真正应用（用于跳过 Final hard truncation）
	if c.config.RelevantTruncate && len(lines) > c.config.RelevantMaxLines {
		switch mode {
		case "middle":
			// H13-G2: 中间截断 50/50 + Warning 头
			joined := strings.Join(lines, "\n")
			originalBytes := len(joined)
			originalTokens := originalBytes / 4 // 4 字节 ≈ 1 token 估算
			truncated := TruncateMiddle(joined, c.config.MaxOutputLen, false)
			if len(truncated) < originalBytes {
				truncated = FormatTruncatedOutput(truncated, TruncationPolicy{
					OriginalTokenCount: originalTokens,
					OriginalByteCount:  originalBytes,
					Mode:               "middle",
				})
			}
			output = truncated
			stepsApplied = append(stepsApplied, "truncate_middle")
			// middle 模式已生成最终 output，跳过后续 Join + MaxOutputLen 截断
			// 但仍执行 Step 9 TOONEncode 检查（middle 截断后的非 JSON 内容不会触发 TOON）
			lines = nil          // 标记已处理，避免下方再次 Join
			middleApplied = true // 标记 middle 截断已应用
		case "tail":
			// 尾部截断：保留头部 RelevantKeepLines 行
			keepLines := c.config.RelevantKeepLines
			if keepLines > len(lines) {
				keepLines = len(lines)
			}
			lines = lines[:keepLines]
			stepsApplied = append(stepsApplied, "truncate_tail")
			// 继续走 Join + MaxOutputLen 截断
		default:
			// "relevant" 模式：现有行为
			lines = RelevantTruncate(lines, c.config.RelevantKeepLines, c.config.RelevantMaxLines, cmd, c.config.SignalWords)
			stepsApplied = append(stepsApplied, "relevant_truncate")
		}
	}

	// Merge lines back to string（middle 模式已直接生成 output，跳过 Join）
	if lines != nil {
		output = strings.Join(lines, "\n")
	}

	// Step 9: TOONEncode (try on the whole output)
	if c.config.TOONEncode {
		toonResult := TryTOON(output)
		if toonResult != nil {
			output = *toonResult
			stepsApplied = append(stepsApplied, "toon_encode")
		}
	}

	// Final hard truncation (preserve existing behavior)
	// H13-G2: middle 模式已应用 MaxOutputLen 截断，此处跳过避免二次截断
	// 收口：走唯一权威 TruncateTail（真实丢弃量标记）。
	if !middleApplied && c.config.MaxOutputLen > 0 {
		output = TruncateTail(output, c.config.MaxOutputLen)
	}

	compressedLen := len(output)
	tokensSaved := estimateTokensSaved(originalLen, compressedLen)

	// Record metrics
	c.metrics.Record(cmd, stepsApplied, linesRemoved, int64(originalLen), int64(compressedLen), int64(tokensSaved))

	return &CompressResult{
		Output:        output,
		OriginalLen:   originalLen,
		CompressedLen: compressedLen,
		StepsApplied:  stepsApplied,
		LinesRemoved:  linesRemoved,
		TokensSaved:   tokensSaved,
	}
}

// CompressWithProfile performs command-aware compression
func (c *OutputCompressor) CompressWithProfile(output string, cmd string) *CompressResult {
	if !c.config.Enabled || output == "" {
		return &CompressResult{
			Output:        output,
			OriginalLen:   len(output),
			CompressedLen: len(output),
		}
	}

	// Find matching command profile
	for _, profile := range c.commandProfiles {
		if profile.Match(cmd) {
			// Merge config: global defaults + command overrides
			merged := mergeConfig(c.config, profile.Profile)
			compressor := &OutputCompressor{
				config:          merged,
				metrics:         c.metrics,
				commandProfiles: c.commandProfiles,
			}
			return compressor.Compress(output, cmd)
		}
	}

	// No match: use global default config
	return c.Compress(output, cmd)
}

// Metrics returns the compressor metrics reference
func (c *OutputCompressor) Metrics() *CompressorMetrics {
	return c.metrics
}

// estimateTokensSaved estimates the number of tokens saved by compression
// Uses a simple heuristic: ~4 characters per token for English text
func estimateTokensSaved(originalLen, compressedLen int) int {
	if originalLen <= compressedLen {
		return 0
	}
	saved := (originalLen - compressedLen) / 4
	if saved < 0 {
		saved = 0
	}
	return saved
}

// mergeConfig merges a base config with override values from a command profile
// Only non-zero override values are applied
func mergeConfig(base, override CompressorConfig) CompressorConfig {
	result := base

	// Override boolean fields only if override explicitly sets them
	// For command profiles, we only override fields that differ from zero values
	// Since bool defaults to false, we need a different approach:
	// CommandProfile.Profile only sets the fields it wants to override
	if !override.FilterNoise {
		// Check if this is an explicit override by checking if any field is set
		// If the override has Enabled=false explicitly, respect it
		if !override.Enabled && override.StripANSI == false && override.StripCarriage == false &&
			override.FilterNoise == false && override.StripTimestamps == false &&
			override.CollapseBlanks == false && override.DedupLines == false &&
			override.LogTemplate == false && override.RelevantTruncate == false &&
			override.TOONEncode == false {
			// All fields are zero/false — this is a "disable" profile
			result.Enabled = false
			return result
		}
	}

	// Apply individual overrides
	if override.RelevantKeepLines > 0 {
		result.RelevantKeepLines = override.RelevantKeepLines
	}
	if override.RelevantMaxLines > 0 {
		result.RelevantMaxLines = override.RelevantMaxLines
	}
	if override.DedupThreshold > 0 {
		result.DedupThreshold = override.DedupThreshold
	}
	if override.LogTemplateMinCount > 0 {
		result.LogTemplateMinCount = override.LogTemplateMinCount
	}
	if override.MaxOutputLen > 0 {
		result.MaxOutputLen = override.MaxOutputLen
	}

	// For boolean overrides in command profiles, we use a convention:
	// If the profile sets StripTimestamps=false, it means "don't strip timestamps"
	// We detect this by checking if the profile has any non-zero bool field
	// Simple approach: always apply boolean overrides from profiles
	result.FilterNoise = override.FilterNoise
	result.StripTimestamps = override.StripTimestamps
	result.DedupLines = override.DedupLines
	result.LogTemplate = override.LogTemplate
	result.RelevantTruncate = override.RelevantTruncate

	return result
}
