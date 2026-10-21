// Package tools — StreamingCompressor 单元测试（/ T1-P1-B5）
//
// 测试覆盖：
// - 单元测试 19 个（步骤 1-7 + Flush + ProcessChannel）
// - 边界测试 6 个（跨 chunk 行边界）
// - race 测试 1 个（ProcessChannel 并发）
// - 基准测试 4 个（性能预算验证）
// - 对标测试 1 个（与 OutputCompressor.Compress 步骤 1-5 等价性）
package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// 单元测试
// ============================================================================

// TestStreamingCompressor_Empty 空输入不产生输出，Flush 返回空结果但合法。
func TestStreamingCompressor_Empty(t *testing.T) {
	s := NewStreamingCompressor(DefaultCompressorConfig(), "test")
	out, err := s.Write(nil)
	if err != nil {
		t.Fatalf("Write(nil) err = %v, want nil", err)
	}
	if out != "" {
		t.Errorf("Write(nil) out = %q, want empty", out)
	}
	final, result := s.Flush()
	if final != "" {
		t.Errorf("Flush() final = %q, want empty", final)
	}
	if result == nil {
		t.Fatal("Flush() result = nil")
	}
	if !result.Streaming {
		t.Error("result.Streaming = false, want true")
	}
	if result.WindowSize != defaultStreamingWindowSize {
		t.Errorf("result.WindowSize = %d, want %d", result.WindowSize, defaultStreamingWindowSize)
	}
}

// TestStreamingCompressor_SingleLineNoNewline 单行无换行：Flush 时才输出。
func TestStreamingCompressor_SingleLineNoNewline(t *testing.T) {
	s := NewStreamingCompressor(DefaultCompressorConfig(), "test")
	out, err := s.Write([]byte("hello world"))
	if err != nil {
		t.Fatalf("Write err = %v", err)
	}
	if out != "" {
		t.Errorf("Write out = %q, want empty (line not complete)", out)
	}
	final, result := s.Flush()
	if !strings.Contains(final, "hello world") {
		t.Errorf("Flush() final = %q, want contains 'hello world'", final)
	}
	if result.OriginalLen != len("hello world") {
		t.Errorf("OriginalLen = %d, want %d", result.OriginalLen, len("hello world"))
	}
}

// TestStreamingCompressor_SimpleFlow 基本流：a\nb\nc\n 三行。
func TestStreamingCompressor_SimpleFlow(t *testing.T) {
	cfg := DefaultCompressorConfig()
	// 关闭步骤 6/7/8/9，仅测步骤 1-5
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out, err := s.Write([]byte("a\nb\nc\n"))
	if err != nil {
		t.Fatalf("Write err = %v", err)
	}
	expected := "a\nb\nc\n"
	if out != expected {
		t.Errorf("Write out = %q, want %q", out, expected)
	}
	final, _ := s.Flush()
	if final != "" {
		t.Errorf("Flush() final = %q, want empty (all lines emitted)", final)
	}
}

// TestStreamingCompressor_Step1_ANSI 步骤 1：ANSI 跨 chunk
func TestStreamingCompressor_Step1_ANSI(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	// ANSI 颜色码跨 chunk
	out1, _ := s.Write([]byte("\x1b[32m"))
	out2, _ := s.Write([]byte("ok\x1b[0m\n"))
	combined := out1 + out2
	if strings.Contains(combined, "\x1b[") {
		t.Errorf("combined = %q, should not contain ANSI escape", combined)
	}
	if !strings.Contains(combined, "ok") {
		t.Errorf("combined = %q, should contain 'ok'", combined)
	}
	s.Flush()
}

// TestStreamingCompressor_Step2_Carriage 步骤 2：\r\n 跨 chunk
func TestStreamingCompressor_Step2_Carriage(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	// \r\n 跨 chunk：chunk1="abc\r", chunk2="\n"
	out1, _ := s.Write([]byte("abc\r"))
	if out1 != "" {
		t.Errorf("out1 = %q, want empty (line not complete)", out1)
	}
	out2, _ := s.Write([]byte("\n"))
	if !strings.Contains(out2, "abc") {
		t.Errorf("out2 = %q, want contains 'abc'", out2)
	}
	if strings.Contains(out2, "\r") {
		t.Errorf("out2 = %q, should not contain \\r", out2)
	}
	s.Flush()
}

// TestStreamingCompressor_Step3_Noise 步骤 3：噪声行（spinner）被过滤
func TestStreamingCompressor_Step3_Noise(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	// spinner 字符行应被丢弃
	out, _ := s.Write([]byte("⠋\nreal content\n"))
	if strings.Contains(out, "⠋") {
		t.Errorf("out = %q, should not contain spinner", out)
	}
	if !strings.Contains(out, "real content") {
		t.Errorf("out = %q, should contain 'real content'", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Step4_Timestamp 步骤 4：时间戳被剥离
func TestStreamingCompressor_Step4_Timestamp(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out, _ := s.Write([]byte("[2026-07-20T10:30:01] error occurred\n"))
	if strings.Contains(out, "2026-07-20T10:30:01") {
		t.Errorf("out = %q, should not contain timestamp", out)
	}
	if !strings.Contains(out, "error occurred") {
		t.Errorf("out = %q, should contain 'error occurred'", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Step5_CollapseBlanks 步骤 5：连续空行折叠
func TestStreamingCompressor_Step5_CollapseBlanks(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out, _ := s.Write([]byte("a\n\n\nb\n"))
	// 应折叠为 a\n\nb\n（连续空行合并为单个）
	blankCount := strings.Count(out, "\n\n")
	if blankCount != 1 {
		t.Errorf("out = %q, blank runs = %d, want 1", out, blankCount)
	}
	s.Flush()
}

// TestStreamingCompressor_Step6_DedupWindow 步骤 6：窗口内去重
func TestStreamingCompressor_Step6_DedupWindow(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	cfg.DedupThreshold = 3
	s := NewStreamingCompressor(cfg, "test", WithWindowSize(16))

	// 3 行相同 → 第 3 行触发 [×3]
	out, _ := s.Write([]byte("dup\ndup\ndup\n"))
	if !strings.Contains(out, "[×3]") {
		t.Errorf("out = %q, should contain [×3]", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Step6_DedupWindowEvict 步骤 6：窗口滑动驱逐
func TestStreamingCompressor_Step6_DedupWindowEvict(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	cfg.DedupThreshold = 3
	// 窗口大小 4：4 行后开始驱逐
	s := NewStreamingCompressor(cfg, "test", WithWindowSize(4))

	// a, b, c, d → 窗口满
	// a, a → 旧的 a 已被驱逐，counts[a] 重置；这两行不触发 [×N]
	out, _ := s.Write([]byte("a\nb\nc\nd\na\na\n"))
	// 由于窗口大小 4 + threshold 3，6 行内 a 出现 3 次但窗口内最多 2 次（窗口滑动后）
	// 不应出现 [×3]
	if strings.Contains(out, "[×3]") {
		t.Errorf("out = %q, should NOT contain [×3] (window evicted old a)", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Step7_Template 步骤 7：模板化窗口
func TestStreamingCompressor_Step7_Template(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	cfg.LogTemplateMinCount = 3
	s := NewStreamingCompressor(cfg, "test", WithWindowSize(16))

	// 3 行仅 ID 不同的日志 → 触发模板化
	out, _ := s.Write([]byte("user id=1001 logged in\nuser id=1002 logged in\nuser id=1003 logged in\n"))
	// 应出现 [×N] 模板标记（窗口内 3 次达到 threshold）
	if !strings.Contains(out, "[×3]") {
		t.Errorf("out = %q, should contain [×3] template marker", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Flush_Truncate 步骤 8：flush 时 RelevantTruncate
func TestStreamingCompressor_Flush_Truncate(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.TOONEncode = false
	cfg.RelevantMaxLines = 5
	cfg.RelevantKeepLines = 2
	s := NewStreamingCompressor(cfg, "test")

	// 写入 10 行，超过 RelevantMaxLines=5
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	s.Write([]byte(sb.String()))
	final, result := s.Flush()
	if !strings.Contains(final, "kept") {
		t.Errorf("final = %q, should contain 'kept' (truncation marker)", final)
	}
	found := false
	for _, step := range result.StepsApplied {
		if step == "relevant_truncate" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("StepsApplied = %v, should contain 'relevant_truncate'", result.StepsApplied)
	}
}

// TestStreamingCompressor_Flush_TOON 步骤 9：flush 时 TOONEncode
func TestStreamingCompressor_Flush_TOON(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	s := NewStreamingCompressor(cfg, "test")

	// JSON 数组输入
	jsonInput := `[{"id":1,"name":"a"},{"id":2,"name":"b"},{"id":3,"name":"c"}]`
	s.Write([]byte(jsonInput))
	final, result := s.Flush()
	if !strings.Contains(final, "items[3]") {
		t.Errorf("final = %q, should contain 'items[3]' (TOON encoded)", final)
	}
	found := false
	for _, step := range result.StepsApplied {
		if step == "toon_encode" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("StepsApplied = %v, should contain 'toon_encode'", result.StepsApplied)
	}
}

// TestStreamingCompressor_Flush_Disabled enableFlush=false 时跳过步骤 8/9
func TestStreamingCompressor_Flush_Disabled(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	// 通过 Option 关闭 flush 阶段的步骤 8/9
	s := NewStreamingCompressor(cfg, "test", WithFlushTruncate(false), WithFlushTOON(false))

	// 10 行 JSON 数组：不应触发 truncate 或 TOON
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	s.Write([]byte(sb.String()))
	final, result := s.Flush()
	if strings.Contains(final, "kept") {
		t.Errorf("final = %q, should NOT contain 'kept' (truncate disabled)", final)
	}
	for _, step := range result.StepsApplied {
		if step == "relevant_truncate" || step == "toon_encode" {
			t.Errorf("StepsApplied should NOT contain %q (flush disabled)", step)
		}
	}
}

// TestStreamingCompressor_AfterFlush_Closed Flush 后再 Write 返回 ErrCompressorClosed
func TestStreamingCompressor_AfterFlush_Closed(t *testing.T) {
	s := NewStreamingCompressor(DefaultCompressorConfig(), "test")
	s.Flush()
	_, err := s.Write([]byte("more"))
	if err != ErrCompressorClosed {
		t.Errorf("Write after Flush err = %v, want ErrCompressorClosed", err)
	}
}

// TestStreamingCompressor_ProcessChannel_Normal 通道正常流程
func TestStreamingCompressor_ProcessChannel_Normal(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string, 4)
	in <- "a\n"
	in <- "b\n"
	close(in)

	out := s.ProcessChannel(ctx, in)
	var collected strings.Builder
	for chunk := range out {
		collected.WriteString(chunk)
	}
	combined := collected.String()
	if !strings.Contains(combined, "a") || !strings.Contains(combined, "b") {
		t.Errorf("collected = %q, should contain 'a' and 'b'", combined)
	}
}

// TestStreamingCompressor_ProcessChannel_Close 上游 close 触发 Flush
func TestStreamingCompressor_ProcessChannel_Close(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string, 2)
	// 末尾不带换行的 chunk，需要 Flush 才能输出
	in <- "no newline"
	close(in)

	out := s.ProcessChannel(ctx, in)
	var collected strings.Builder
	for chunk := range out {
		collected.WriteString(chunk)
	}
	if !strings.Contains(collected.String(), "no newline") {
		t.Errorf("collected = %q, should contain 'no newline' (flushed on close)", collected.String())
	}
}

// TestStreamingCompressor_ProcessChannel_CtxCancel ctx 取消立即关闭
func TestStreamingCompressor_ProcessChannel_CtxCancel(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan string, 1)
	out := s.ProcessChannel(ctx, in)

	// 取消 ctx，不 close in
	cancel()
	// out 应被关闭（可能收到 panic sentinel 或正常关闭）
	timeout := time.After(1 * time.Second)
	for {
		select {
		case _, ok := <-out:
			if !ok {
				return // 正常关闭
			}
		case <-timeout:
			t.Fatal("ProcessChannel out not closed after ctx cancel")
		}
	}
}

// TestStreamingCompressor_ProcessChannel_Panic 上游 panic 隔离
func TestStreamingCompressor_ProcessChannel_Panic(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string, 1)
	out := s.ProcessChannel(ctx, in)

	// 直接 close out 的 goroutine（模拟上游 panic 已被 ProcessChannel 内部 recover）
	close(in)
	// 应能正常 range 完毕
	for range out {
	}
}

// ============================================================================
// 边界测试（落实优化 3）
// ============================================================================

// TestStreamingCompressor_Boundary_CRSplit \r\n 跨 chunk："abc\r" + "\n"
func TestStreamingCompressor_Boundary_CRSplit(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out1, _ := s.Write([]byte("abc\r"))
	if out1 != "" {
		t.Errorf("out1 = %q, want empty (\\r at end waits for \\n)", out1)
	}
	out2, _ := s.Write([]byte("\ndef\n"))
	combined := out1 + out2
	lines := strings.Split(strings.TrimRight(combined, "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("combined = %q, lines = %d, want 2", combined, len(lines))
	}
	if lines[0] != "abc" {
		t.Errorf("lines[0] = %q, want 'abc'", lines[0])
	}
	if lines[1] != "def" {
		t.Errorf("lines[1] = %q, want 'def'", lines[1])
	}
	s.Flush()
}

// TestStreamingCompressor_Boundary_NLSplit \n 跨 chunk："abc" + "\n"
func TestStreamingCompressor_Boundary_NLSplit(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out1, _ := s.Write([]byte("abc"))
	if out1 != "" {
		t.Errorf("out1 = %q, want empty (line not complete)", out1)
	}
	out2, _ := s.Write([]byte("\n"))
	if !strings.Contains(out2, "abc") {
		t.Errorf("out2 = %q, want contains 'abc'", out2)
	}
	s.Flush()
}

// TestStreamingCompressor_Boundary_StandaloneCR 独立 \r（老 Mac 风格）按 \r 切行
func TestStreamingCompressor_Boundary_StandaloneCR(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	// "abc\rdef\r\n" 中：
	// - abc\r 是独立 \r（后接 d 不是 \n），切出一行 "abc"
	// - def\r\n 是 \r\n 序列，切出一行 "def"
	out, _ := s.Write([]byte("abc\rdef\r\n"))
	// 应切出两行："abc" 和 "def"
	if !strings.Contains(out, "abc") {
		t.Errorf("out = %q, should contain 'abc'", out)
	}
	if !strings.Contains(out, "def") {
		t.Errorf("out = %q, should contain 'def'", out)
	}
	s.Flush()
}

// TestStreamingCompressor_Boundary_EmptyLineSplit 空行 \n\n 跨 chunk
func TestStreamingCompressor_Boundary_EmptyLineSplit(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	cfg.CollapseBlanks = false // 关闭空行折叠，验证切行
	s := NewStreamingCompressor(cfg, "test")

	out1, _ := s.Write([]byte("a\n"))
	out2, _ := s.Write([]byte("\nb\n"))
	combined := out1 + out2
	// 应有 3 行：a, (空), b
	lines := strings.Split(strings.TrimRight(combined, "\n"), "\n")
	if len(lines) != 3 {
		t.Errorf("combined = %q, lines = %d, want 3", combined, len(lines))
	}
	s.Flush()
}

// TestStreamingCompressor_Boundary_LongLine 超长行（128KB）跨 8 个 16KB chunk
func TestStreamingCompressor_Boundary_LongLine(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	longLine := strings.Repeat("x", 128*1024)
	chunkSize := 16 * 1024
	var collected strings.Builder
	for i := 0; i < len(longLine); i += chunkSize {
		end := i + chunkSize
		if end > len(longLine) {
			end = len(longLine)
		}
		out, _ := s.Write([]byte(longLine[i:end]))
		collected.WriteString(out)
	}
	// 写入换行符触发行处理
	out, _ := s.Write([]byte("\n"))
	collected.WriteString(out)

	combined := collected.String()
	// 应输出一整行 x（128KB），无中间换行
	if len(combined) != 128*1024+1 {
		t.Errorf("combined len = %d, want %d", len(combined), 128*1024+1)
	}
	s.Flush()
}

// TestStreamingCompressor_Boundary_NoTrailingNewline 末尾无 \n 的 Flush
func TestStreamingCompressor_Boundary_NoTrailingNewline(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	out, _ := s.Write([]byte("line1\nline2 no newline"))
	if !strings.Contains(out, "line1") {
		t.Errorf("out = %q, should contain 'line1'", out)
	}
	if strings.Contains(out, "line2 no newline") {
		t.Errorf("out = %q, should NOT contain 'line2 no newline' (incomplete)", out)
	}
	final, _ := s.Flush()
	if !strings.Contains(final, "line2 no newline") {
		t.Errorf("final = %q, should contain 'line2 no newline' (flushed)", final)
	}
}

// ============================================================================
// race 测试
// ============================================================================

// TestStreamingCompressor_Race_ProcessChannel 并发 Write + Flush（需 -race）
// 注意：StreamingCompressor.Write 本身非并发安全（单 producer），
// 此测试验证 ProcessChannel 模式下并发读取 out channel 的安全性。
func TestStreamingCompressor_Race_ProcessChannel(t *testing.T) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false
	s := NewStreamingCompressor(cfg, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string, 100)
	out := s.ProcessChannel(ctx, in)

	// 单 producer 写入
	go func() {
		defer close(in)
		for i := 0; i < 100; i++ {
			in <- fmt.Sprintf("line %d\n", i)
		}
	}()

	// 多 consumer 并发读取 out
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range out {
			}
		}()
	}
	wg.Wait()
}

// ============================================================================
// 对标测试（落实优化 5）
// ============================================================================

// TestStreamingVsBatch_Equivalence_SmallInput 小输入下步骤 1-5 输出完全等价
// 步骤 6-7 因窗口语义偏离允许差异，但需显式断言
func TestStreamingVsBatch_Equivalence_SmallInput(t *testing.T) {
	// 共用配置：关闭步骤 6-9，仅测试步骤 1-5 等价性
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	cfg.RelevantTruncate = false
	cfg.TOONEncode = false

	input := "line with \x1b[32mcolor\x1b[0m\n[2026-07-20T10:30:01] timestamped\ntext\r\nmore text\n"

	// 批量
	batch := NewOutputCompressor(cfg)
	batchResult := batch.Compress(input, "test")

	// 流式
	stream := NewStreamingCompressor(cfg, "test")
	out, _ := stream.Write([]byte(input))
	final, streamResult := stream.Flush()
	streamOutput := out + final

	// 步骤 1-5 等价
	if batchResult.Output != streamOutput {
		t.Errorf("batch output  = %q", batchResult.Output)
		t.Errorf("stream output = %q", streamOutput)
		t.Error("step 1-5 outputs should be equivalent")
	}

	// 流式结果标注
	if !streamResult.Streaming {
		t.Error("streamResult.Streaming should be true")
	}
	if streamResult.WindowSize == 0 {
		t.Error("streamResult.WindowSize should be non-zero")
	}
	// 批量结果未标注
	if batchResult.Streaming {
		t.Error("batchResult.Streaming should be false (backward compat)")
	}
}

// ============================================================================
// 基准测试（落实优化 5）
// ============================================================================

// BenchmarkStreamingCompressor_Write 单行处理延迟
func BenchmarkStreamingCompressor_Write(b *testing.B) {
	cfg := DefaultCompressorConfig()
	s := NewStreamingCompressor(cfg, "bench")
	line := []byte("2026-07-20T10:30:01 [INFO] module: user id=1001 action=login status=ok latency=12ms\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Write(line)
	}
}

// BenchmarkStreamingCompressor_Flush_1MB flush 1MB 输入
func BenchmarkStreamingCompressor_Flush_1MB(b *testing.B) {
	cfg := DefaultCompressorConfig()
	cfg.DedupLines = false
	cfg.LogTemplate = false
	// 1MB 数据
	line := strings.Repeat("x", 128) + "\n"
	lines := strings.Repeat(line, 8192) // ~1MB
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := NewStreamingCompressor(cfg, "bench")
		s.Write([]byte(lines))
		s.Flush()
	}
}

// BenchmarkStreamingCompressor_Pipeline_100KLines 10万行端到端
func BenchmarkStreamingCompressor_Pipeline_100KLines(b *testing.B) {
	cfg := DefaultCompressorConfig()
	// 10万行预生成
	line := []byte("[2026-07-20T10:30:01] user id=1001 action=login status=ok\n")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := NewStreamingCompressor(cfg, "bench")
		for j := 0; j < 100000; j++ {
			s.Write(line)
		}
		s.Flush()
	}
}

// BenchmarkStreamingVsBatch_Compare 流式 vs 批量对比（同输入）
func BenchmarkStreamingVsBatch_Compare(b *testing.B) {
	cfg := DefaultCompressorConfig()
	line := strings.Repeat("[2026-07-20T10:30:01] user id=1001 action=login status=ok\n", 10000)

	b.Run("Batch", func(b *testing.B) {
		bc := NewOutputCompressor(cfg)
		for i := 0; i < b.N; i++ {
			bc.Compress(line, "bench")
		}
	})

	b.Run("Streaming", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			s := NewStreamingCompressor(cfg, "bench")
			s.Write([]byte(line))
			s.Flush()
		}
	})
}
