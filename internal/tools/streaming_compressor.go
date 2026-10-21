// Package tools — 流式压缩器（/ T1-P1-B5）
//
// StreamingCompressor 提供按行边界的增量压缩能力，对标同类 CLI 流式压缩架构。
//
// 与现有 OutputCompressor.Compress 的区别：
// - 批量 Compress：一次性 Split 全量入内存，O(N) 峰值内存
// - 流式 StreamingCompressor：按 chunk 写入，行边界增量处理，O(window) 峰值内存
//
// 流式管线（与 Compress 9 步对齐）：
// 1. StripANSI — 流式，复用 compressor_steps.go 的 StripANSI
// 2. StripCarriage — 流式，复用 StripCarriage
// 3. FilterNoise — 流式，复用 IsNoiseLine
// 4. StripTimestamps — 流式，复用 StripTimestamp
// 5. CollapseBlanks — 流式，用 lastWasBlank 状态判断
// 6. DedupLines — 流式，滑动窗口（lineWindow）维护窗口内计数
// 7. LogTemplate — 流式，滑动窗口（key=normalizeLine）
// 8. RelevantTruncate — flush 阶段，复用 RelevantTruncate
// 9. TOONEncode — flush 阶段，复用 TryTOON
//
// 内存复杂度：O(windowSize × 平均行长)，默认 256 行 ≈ 32KB
// 单行处理延迟：< 100μs（步骤 1-5 均为 O(L)）
//
// 语义偏离说明：
//
//	流式模式下步骤 6/7 的 [×N] 标记表示"窗口内重复 N 次"，
//	与 OutputCompressor.Compress 的"全文重复 N 次"语义不同；
//	通过 CompressResult.Streaming=true / WindowSize 字段标注，调用方可感知。
package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ErrCompressorClosed 流式压缩器已 Flush，不能再写入。
var ErrCompressorClosed = errors.New("streaming compressor already closed")

// 默认滑动窗口大小（行数）。
// 取 256 行 ≈ 32KB 内存（按平均行长 128 字节估算），与同类 CLI code 对标。
const defaultStreamingWindowSize = 256

// 最小窗口大小，避免配置过小导致去重/模板化失效。
// 设为 4 以支持小窗口测试场景（如驱逐测试），同时仍防止 size=1/2 的退化配置。
const minStreamingWindowSize = 4

// StreamingCompressorOption 流式压缩器配置选项（函数式选项模式）。
type StreamingCompressorOption func(*StreamingCompressor)

// WithWindowSize 设置滑动窗口大小（默认 256，最小 16）。
// 小于 minStreamingWindowSize 的值会被自动钳制到最小值。
func WithWindowSize(n int) StreamingCompressorOption {
	return func(s *StreamingCompressor) {
		if n < minStreamingWindowSize {
			n = minStreamingWindowSize
		}
		s.windowSize = n
	}
}

// WithFlushTruncate 是否在 Flush 时启用步骤 8（RelevantTruncate），默认 true。
// 设为 false 时纯流式输出，适合超大日志场景（牺牲相关性截断）。
func WithFlushTruncate(enabled bool) StreamingCompressorOption {
	return func(s *StreamingCompressor) {
		s.config.RelevantTruncate = enabled
	}
}

// WithFlushTOON 是否在 Flush 时启用步骤 9（TOONEncode），默认 true。
// 设为 false 时跳过 JSON→TOON 重编码。
func WithFlushTOON(enabled bool) StreamingCompressorOption {
	return func(s *StreamingCompressor) {
		s.config.TOONEncode = enabled
	}
}

// WithMetrics 注入共享的 CompressorMetrics（默认新建独立 metrics）。
// 多个 StreamingCompressor 实例可共享同一 metrics 聚合统计。
func WithMetrics(m *CompressorMetrics) StreamingCompressorOption {
	return func(s *StreamingCompressor) {
		if m != nil {
			s.metrics = m
		}
	}
}

// StreamingCompressor 流式压缩器，按行边界增量处理工具输出。
//
// 设计要点：
// - 行边界识别：维护 partialLine 跨 chunk 拼接，每个完整行立即进入管线
// - 步骤 1-5 流式：StripANSI/StripCarriage/FilterNoise/StripTimestamps/CollapseBlanks
// - 步骤 6-7 滑动窗口：DedupLines/LogTemplate 用 windowSize 行窗口维护计数
// - 步骤 8-9 flush 阶段：RelevantTruncate/TOONEncode 仅在 Flush() 时触发
// - 对接 BackpressurePipe：ProcessChannel 直接复用 T1-C 资产
//
// 并发安全：
// - Write/Flush 单 producer 模式，不并发安全
// - ProcessChannel 内部用 mu sync.Mutex 保护，可并发读取 out channel
//
// 不修改现有 OutputCompressor.API，向后兼容。
type StreamingCompressor struct {
	config  CompressorConfig
	metrics *CompressorMetrics
	cmd     string

	// 行边界状态：跨 chunk 的不完整行缓冲
	partialLine []byte

	// 步骤 5 状态：CollapseBlanks 的 lastWasBlank 标记
	lastWasBlank bool

	// 步骤 6 状态：DedupLines 滑动窗口（key = line 本身）
	dedupWindow *lineWindow

	// 步骤 7 状态：LogTemplate 滑动窗口（key = normalizeLine(line)）
	templateWindow *lineWindow

	// flush 阶段缓冲：累积已压缩行（仅当 enableFlush=true 时使用）
	// 注意：这里累积的是经过步骤 1-7 压缩后的行，而非原始行
	flushBuffer []string

	// windowSize 滑动窗口大小（行数）
	windowSize int

	// 统计
	inBytes      int64
	outBytes     int64
	stepsApplied map[string]bool
	linesRemoved map[string]int
	lineCount    int

	mu     sync.Mutex
	closed bool
}

// NewStreamingCompressor 创建流式压缩器。
//
// 参数：
// - config: 压缩配置（与 OutputCompressor 共用 CompressorConfig）
// - cmd: 触发压缩的命令字符串（用于 RelevantTruncate 的 query terms 提取）
// - opts: 函数式选项（WithWindowSize / WithFlushTruncate / WithFlushTOON / WithMetrics）
//
// 返回的 StreamingCompressor 初始状态为 open，可重复调用 Write，最终调用 Flush 收尾。
// Flush 后再调用 Write 返回 ErrCompressorClosed。
func NewStreamingCompressor(config CompressorConfig, cmd string, opts ...StreamingCompressorOption) *StreamingCompressor {
	s := &StreamingCompressor{
		config:       config,
		metrics:      NewCompressorMetrics(),
		cmd:          cmd,
		windowSize:   defaultStreamingWindowSize,
		stepsApplied: make(map[string]bool),
		linesRemoved: make(map[string]int),
		flushBuffer:  make([]string, 0, 256),
		partialLine:  make([]byte, 0, 4096),
	}
	for _, opt := range opts {
		opt(s)
	}
	// 用最终 windowSize 初始化滑动窗口
	if config.DedupLines {
		s.dedupWindow = newLineWindow(s.windowSize, config.DedupThreshold)
	}
	if config.LogTemplate {
		s.templateWindow = newLineWindow(s.windowSize, config.LogTemplateMinCount)
	}
	return s
}

// Write 写入一个 chunk，返回已压缩的可输出部分。
//
// 处理流程：
// 1. 把 p 追加到 partialLine
// 2. 按 \n（或独立 \r）切分行，每个完整行进入 processLine
// 3. 最后一行（无换行符）保留在 partialLine，等待下次 Write 或 Flush
//
// 跨 chunk 行边界处理：
// - \r\n 跨 chunk：chunk1 末尾 "abc\r" + chunk2 开头 "\n" → 视为一行 "abc"
// （\r 在 StripCarriage 步骤中被剥离，不参与行切分判定，避免双切）
// - \n 跨 chunk：chunk1 末尾 "abc" + chunk2 开头 "\n" → 视为一行 "abc"
// - 独立 \r（老 Mac 风格）：按 \r 也切行
// - 空行 \n\n 跨 chunk：正常处理
// - 超长行（> 64KB）跨多 chunk：累积到 partialLine 直到看到换行符
//
// 返回值：
// - string: 本次 Write 产出的已压缩内容（可能为空，例如行未完成）
// - error: 仅在压缩器已 Flush 后返回 ErrCompressorClosed
func (s *StreamingCompressor) Write(p []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", ErrCompressorClosed
	}
	if len(p) == 0 {
		return "", nil
	}

	s.inBytes += int64(len(p))
	s.partialLine = append(s.partialLine, p...)

	var out strings.Builder
	for {
		// 查找下一个换行符（\n 或独立 \r）
		idx, sepLen := findLineEnd(s.partialLine)
		if idx < 0 {
			// 没有完整行，等待下次 Write
			break
		}
		// 提取一行（不含换行符）
		line := string(s.partialLine[:idx])
		// 移除已处理的行 + 换行符
		s.partialLine = s.partialLine[idx+sepLen:]

		compressed, emit := s.processLine(line)
		if emit {
			out.WriteString(compressed)
			out.WriteByte('\n')
			s.outBytes += int64(len(compressed) + 1)
			// 仅当 flush 阶段启用时累积已压缩行，供步骤 8/9 使用
			// 注意：累积的是已压缩行（非原始行），内存远小于原始输入
			if s.config.RelevantTruncate || s.config.TOONEncode {
				s.flushBuffer = append(s.flushBuffer, compressed)
			}
		}
		s.lineCount++
	}

	return out.String(), nil
}

// findLineEnd 在 buf 中查找下一个行结束位置。
// 返回 (lineEndIndex, separatorLength)：
// - 找到 \n：返回 (\n 的 index, 1)
// - 找到独立 \r（即 \r 不后接 \n）：返回 (\r 的 index, 1)
// - 找到 \r\n：返回 (\r 的 index, 2)（视为一个分隔符）
// - 未找到：返回 (-1, 0)
//
// 该函数处理跨 chunk 边界的关键：当 buf 末尾是 \r 时，不立即判定为行结束，
// 而是返回 -1 让调用方等待下一个 chunk 携带 \n 或确认 \r 独立。
// 但为避免无限等待，若 \r 后无更多数据，仍按独立 \r 处理（在 Flush 中处理）。
func findLineEnd(buf []byte) (int, int) {
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			// 检查前一个字符是否是 \r（\r\n 序列）
			if i > 0 && buf[i-1] == '\r' {
				// \r\n 序列：行结束位置在 \r，分隔符长度 2
				return i - 1, 2
			}
			// 独立 \n
			return i, 1
		}
		if buf[i] == '\r' {
			// 检查下一个字符是否是 \n
			if i+1 < len(buf) {
				if buf[i+1] == '\n' {
					// \r\n 序列
					return i, 2
				}
				// 独立 \r（老 Mac 风格）
				return i, 1
			}
			// \r 在 buf 末尾，无法判断是 \r\n 还是独立 \r
			// 让调用方等待下次 Write（若 chunk 在此处被切分）
			// 但若这是 buf 的最后字节且 i==len(buf)-1，findLineEnd 自然循环结束返回 -1
			// 这里继续循环不会再次找到，所以最终返回 -1
			// 注意：此行为会导致 \r 在 chunk 末尾时本 chunk 不切行，等下个 chunk
		}
	}
	return -1, 0
}

// processLine 处理一个完整行，返回 (压缩后行, 是否输出)。
//
// 返回值语义：
// - (line, true): 输出 line（可能为空字符串，表示合法空行，调用方应输出 "\n"）
// - ("", false): 丢弃该行（噪声/重复/连续空行）
//
// 步骤链（与 OutputCompressor.Compress 步骤 1-7 对齐）：
// 1. StripANSI — 复用 compressor_steps.go 的 StripANSI
// 2. StripCarriage — 复用 StripCarriage（处理行内残留 \r）
// 3. FilterNoise — 复用 IsNoiseLine，是噪声则丢弃
// 4. StripTimestamps — 复用 StripTimestamp
// 5. CollapseBlanks — 用 lastWasBlank 状态判断，连续空行丢弃
// 6. DedupLines — 用 dedupWindow 滑动窗口，达到 threshold 则 emit "[×N]"
// 7. LogTemplate — 用 templateWindow 滑动窗口，key=normalizeLine(line)
//
// 注意：步骤 8/9 不在 processLine 中执行，仅在 Flush 中触发。
func (s *StreamingCompressor) processLine(line string) (string, bool) {
	// 步骤 1: StripANSI
	if s.config.StripANSI {
		before := len(line)
		line = StripANSI(line)
		if len(line) < before {
			s.markStep("strip_ansi")
		}
	}

	// 步骤 2: StripCarriage（处理行内残留 \r，如老 Mac 风格的行内 \r）
	if s.config.StripCarriage {
		before := len(line)
		line = StripCarriage(line)
		if len(line) < before {
			s.markStep("strip_carriage")
		}
	}

	// 步骤 3: FilterNoise
	if s.config.FilterNoise {
		if IsNoiseLine(line) {
			s.markStep("filter_noise")
			s.linesRemoved["filter_noise"]++
			return "", false // 丢弃噪声行
		}
	}

	// 步骤 4: StripTimestamps
	if s.config.StripTimestamps {
		stripped := StripTimestamp(line)
		if stripped != line {
			line = stripped
			s.markStep("strip_timestamps")
		}
	}

	// 步骤 5: CollapseBlanks
	if s.config.CollapseBlanks {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && s.lastWasBlank {
			s.markStep("collapse_blanks")
			s.linesRemoved["collapse_blanks"]++
			return "", false // 丢弃连续空行
		}
		s.lastWasBlank = isBlank
		// 注意：合法空行（isBlank && !lastWasBlank）返回 ("", true) 输出 "\n"
	}

	// 步骤 6: DedupLines（滑动窗口）
	if s.config.DedupLines && s.dedupWindow != nil {
		emit, shouldEmit := s.dedupWindow.observe(line, line)
		if !shouldEmit {
			s.markStep("dedup_lines")
			s.linesRemoved["dedup_lines"]++
			return "", false // 窗口内已 emit 过该行，丢弃
		}
		// 若 emit 行与原行不同（被改写为 [×N] 格式），记录步骤
		if emit != line {
			s.markStep("dedup_lines")
		}
		line = emit
	}

	// 步骤 7: LogTemplate（滑动窗口，key=normalizeLine）
	if s.config.LogTemplate && s.templateWindow != nil {
		template := normalizeLine(line)
		// 仅对包含 {} 的模板进行窗口计数（与批量 LogTemplate 行为一致）
		if strings.Contains(template, "{}") {
			emit, shouldEmit := s.templateWindow.observe(template, line)
			if !shouldEmit {
				s.markStep("log_template")
				s.linesRemoved["log_template"]++
				return "", false // 窗口内已 emit 过该模板，丢弃
			}
			if emit != line {
				// 改写为 "[×N] template (e.g. sample)" 格式
				s.markStep("log_template")
				line = emit
			}
		}
	}

	return line, true
}

// markStep 标记某步骤被应用（去重）。
func (s *StreamingCompressor) markStep(name string) {
	s.stepsApplied[name] = true
}

// Flush 刷新剩余数据，应用步骤 8/9（若启用），返回最终结果。
//
// 处理流程：
// 1. 若 partialLine 非空，作为最后一行进入 processLine
// 2. 拼接所有已压缩行（累积在 flushBuffer）为完整 output
// 3. 若启用步骤 8（RelevantTruncate）：当行数 > RelevantMaxLines 时截断
// 4. 若启用步骤 9（TOONEncode）：尝试 JSON→TOON 重编码
// 5. MaxOutputLen 硬截断
// 6. 记录 metrics
// 7. 标记 closed=true，后续 Write 返回 ErrCompressorClosed
//
// 返回值：
// - string: flush 产出的最终内容（可能为空）
// - *CompressResult: 完整压缩结果（含 Streaming=true, WindowSize 字段）
func (s *StreamingCompressor) Flush() (string, *CompressResult) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		// 已 Flush，返回空结果（幂等）
		return "", &CompressResult{
			Streaming:  true,
			WindowSize: s.windowSize,
		}
	}
	s.closed = true

	// 处理 partialLine 中剩余的最后一行（无换行符）
	if len(s.partialLine) > 0 {
		line := string(s.partialLine)
		s.partialLine = s.partialLine[:0]
		compressed, emit := s.processLine(line)
		if emit {
			s.flushBuffer = append(s.flushBuffer, compressed)
			s.outBytes += int64(len(compressed))
		}
		s.lineCount++
	}

	// 拼接所有已压缩行
	output := strings.Join(s.flushBuffer, "\n")
	if len(s.flushBuffer) > 0 {
		output += "\n" // 末尾换行符
	}

	stepsApplied := make([]string, 0, len(s.stepsApplied))
	for name := range s.stepsApplied {
		stepsApplied = append(stepsApplied, name)
	}

	// 步骤 8: RelevantTruncate（flush 阶段，需完整行视图）
	if s.config.RelevantTruncate && len(s.flushBuffer) > s.config.RelevantMaxLines {
		lines := s.flushBuffer
		lines = RelevantTruncate(lines, s.config.RelevantKeepLines, s.config.RelevantMaxLines, s.cmd, s.config.SignalWords)
		output = strings.Join(lines, "\n") + "\n"
		stepsApplied = append(stepsApplied, "relevant_truncate")
		s.linesRemoved["relevant_truncate"] = len(s.flushBuffer) - len(lines)
	}

	// 步骤 9: TOONEncode（flush 阶段，需完整 JSON 文档）
	if s.config.TOONEncode {
		toonResult := TryTOON(strings.TrimRight(output, "\n"))
		if toonResult != nil {
			output = *toonResult + "\n"
			stepsApplied = append(stepsApplied, "toon_encode")
		}
	}

	// 最终硬截断（收口：走唯一权威 TruncateTail，真实丢弃量标记）
	if s.config.MaxOutputLen > 0 {
		output = TruncateTail(output, s.config.MaxOutputLen)
	}

	// CompressedLen 计算：
	// - flush 阶段启用时：output 是最终重处理后的内容（步骤 8/9 已应用），
	// CompressedLen = len(output)
	// - flush 阶段禁用时：output 为空（已通过 Write 流式输出），
	// CompressedLen = s.outBytes（累积的流式输出字节数）
	compressedLen := len(output)
	flushEnabled := s.config.RelevantTruncate || s.config.TOONEncode
	if !flushEnabled {
		compressedLen = int(s.outBytes)
	}
	tokensSaved := estimateTokensSaved(int(s.inBytes), compressedLen)

	// 记录 metrics
	s.metrics.Record(s.cmd, stepsApplied, s.linesRemoved, s.inBytes, int64(compressedLen), int64(tokensSaved))

	result := &CompressResult{
		Output:        output,
		OriginalLen:   int(s.inBytes),
		CompressedLen: compressedLen,
		StepsApplied:  stepsApplied,
		LinesRemoved:  s.linesRemoved,
		TokensSaved:   tokensSaved,
		Streaming:     true,
		WindowSize:    s.windowSize,
	}
	return output, result
}

// ProcessChannel 把 StreamingCompressor 适配为 channel → channel 处理器，
// 直接对接 internal/ai/stream.go 的 BackpressurePipe[string]。
//
// 语义：
// - in channel 每条 string 视为一个 chunk，调用 Write 处理
// - Write 返回的非空内容写入 out channel
// - in channel close 时自动调用 Flush，结果写入 out，然后关闭 out
// - ctx 取消时立即关闭 out，未 flush 的 partialLine 丢弃
// - 上游 panic 通过 defer recover 隔离，写入 out 一个 error sentinel 后关闭
//
// 用法：
//
//	in := make(chan string, 16)
//	out := compressor.ProcessChannel(ctx, in)
//	// 上游：in <- chunk; close(in)
//	// 下游：for compressed := range out { ... }
//
// 并发安全：内部用 mu sync.Mutex 保护，可并发读取 out channel。
func (s *StreamingCompressor) ProcessChannel(ctx context.Context, in <-chan string) <-chan string {
	out := make(chan string, 16)

	go func() {
		defer close(out)
		// panic 隔离：上游传入的 channel 读取不应 panic，但防御性处理
		defer func() {
			if r := recover(); r != nil {
				// 写入 error sentinel 后退出（已 defer close，select 守卫）
				select {
				case out <- fmt.Sprintf("[streaming compressor panic: %v]", r):
				default:
				}
			}
		}()

		for {
			select {
			case <-ctx.Done():
				// ctx 取消：立即关闭 out，未 flush 的 partialLine 丢弃
				return
			case chunk, ok := <-in:
				if !ok {
					// in channel 关闭：自动 Flush
					final, _ := s.Flush()
					if final != "" {
						select {
						case out <- final:
						case <-ctx.Done():
						}
					}
					return
				}
				compressed, err := s.Write([]byte(chunk))
				if err != nil {
					// 已关闭，写入 error sentinel 后退出
					select {
					case out <- fmt.Sprintf("[streaming compressor error: %v]", err):
					case <-ctx.Done():
					}
					return
				}
				if compressed != "" {
					select {
					case out <- compressed:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return out
}

// Metrics 返回压缩器metrics引用（与 OutputCompressor.Metrics 一致）。
func (s *StreamingCompressor) Metrics() *CompressorMetrics {
	return s.metrics
}

// ============================================================================
// lineWindow 滑动窗口计数器
// ============================================================================

// lineWindow 滑动窗口计数器，用于步骤 6/7 的流式去重/模板化。
//
// 维护一个 windowSize 行的环形缓冲 + counts map：
// - 新行进入：counts[key]++，若达到 threshold 且未 emit，则 emit "[×N]"
// - 窗口满后驱逐最旧行：counts[old]--，==0 时 delete
//
// key 由调用方提供：
// - 步骤 6（DedupLines）：key = line 本身
// - 步骤 7（LogTemplate）：key = normalizeLine(line)
type lineWindow struct {
	size      int             // 窗口大小（行数）
	threshold int             // 触发去重的阈值
	keys      []string        // 环形缓冲，存 key（按进入顺序）
	originals []string        // 环形缓冲，存 originalLine（首次出现的原始行，用于 [×N] 格式化）
	head      int             // 环形缓冲头指针（下一个被驱逐的位置）
	count     int             // 当前窗口内行数（<= size）
	counts    map[string]int  // key → 窗口内计数
	emitted   map[string]bool // 已 emit 的 key（窗口内）
}

// newLineWindow 创建滑动窗口计数器。
// size 必须大于 0，threshold 必须大于等于 2（否则返回 nil 窗口，调用方需守卫）。
func newLineWindow(size, threshold int) *lineWindow {
	if size < 1 || threshold < 2 {
		return nil
	}
	return &lineWindow{
		size:      size,
		threshold: threshold,
		keys:      make([]string, size),
		originals: make([]string, size),
		head:      0,
		count:     0,
		counts:    make(map[string]int),
		emitted:   make(map[string]bool),
	}
}

// observe 观察一行，返回 (emitLine, shouldEmit)。
//
// 算法：
// 1. counts[key]++
// 2. 若 counts[key] >= threshold 且非 emitted[key]：
// a. emitLine = fmt.Sprintf("%s [×%d]", originalLine, counts[key])
// b. emitted[key] = true
// c. 返回 (emitLine, true)
// 3. 若 emitted[key] 已为 true：返回 ("", false) — 丢弃重复行
// 4. 否则（未达 threshold）：返回 (originalLine, true) — 正常输出
// 5. 窗口驱逐：若 count >= size，驱逐 head 位置的最旧 key
//
// 注意：步骤 7 的调用方需在 emit 时把 emitLine 改写为 "[×N] template (e.g. sample)" 格式，
// 这里返回的 emitLine 仅是 originalLine 或 "[×N]" 形式，调用方按需再加工。
func (w *lineWindow) observe(key, originalLine string) (string, bool) {
	w.counts[key]++

	var emitLine string
	var shouldEmit bool

	if w.counts[key] >= w.threshold && !w.emitted[key] {
		// 首次达到 threshold：emit "[×N]" 格式
		emitLine = fmt.Sprintf("%s  [×%d]", originalLine, w.counts[key])
		w.emitted[key] = true
		shouldEmit = true
	} else if w.emitted[key] {
		// 已 emit 过：丢弃重复行
		emitLine = ""
		shouldEmit = false
	} else {
		// 未达 threshold：正常输出
		emitLine = originalLine
		shouldEmit = true
	}

	// 窗口驱逐
	if w.count >= w.size {
		// 驱逐 head 位置的最旧 key
		oldKey := w.keys[w.head]
		w.counts[oldKey]--
		if w.counts[oldKey] <= 0 {
			delete(w.counts, oldKey)
			delete(w.emitted, oldKey)
		}
		// 覆写 head 位置为新行
		w.keys[w.head] = key
		w.originals[w.head] = originalLine
		w.head = (w.head + 1) % w.size
	} else {
		// 窗口未满，追加
		w.keys[w.head] = key
		w.originals[w.head] = originalLine
		w.head = (w.head + 1) % w.size
		w.count++
	}

	return emitLine, shouldEmit
}

// reset 重置窗口状态（保留 size/threshold 配置）。
// 在 Flush 后或调用方需要清空状态时使用。
func (w *lineWindow) reset() {
	if w == nil {
		return
	}
	w.head = 0
	w.count = 0
	w.counts = make(map[string]int)
	w.emitted = make(map[string]bool)
	// keys/originals 无需显式清空，覆写时自然替换
}
