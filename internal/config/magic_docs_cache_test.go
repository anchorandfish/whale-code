package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ── · 指令文件发现的缓存判据 ──────────────────────────
//
// 背景：`DiscoverInstructionFiles` 从 CWD **逐层向上扫到根目录**，每层对 6 个候选
// 文件做读盘 + 读 `.whale/rules/`。它在**每次 AI 调用**（含每个工具轮次续轮）都被调用
// ⇒ 同样的目录链被反复扫。
//
// 本文件两条判据 + 一条基线取证：
// - 取证：如实 log「首次 / 再次」的耗时与读盘次数（先看数据，再决定要不要优化）；
// - 主判据：文件未改动 ⇒ 第二次调用**不得**重复读文件内容（修复前必红）；
// - 反例：文件改动 ⇒ 缓存**必须**失效（防"缓存太激进、改了不生效"）。

// countReads 替换 readFile 以计数，同时转发到真实的 os.ReadFile（行为不变）。
func countReads(t *testing.T) *int64 {
	t.Helper()
	var n int64
	prev := readFile
	readFile = func(path string) ([]byte, error) {
		atomic.AddInt64(&n, 1)
		return os.ReadFile(path)
	}
	t.Cleanup(func() { readFile = prev })
	return &n
}

// buildInstructionTree 造一条真实形态的目录链：每层都放一个 WHALE.md，
// 返回最深一层（模拟"用户在深目录里工作，上层有多级指令文件"）。
func buildInstructionTree(t *testing.T, layers int) string {
	t.Helper()
	cur := t.TempDir()
	for i := 0; i < layers; i++ {
		cur = filepath.Join(cur, fmt.Sprintf("L%d", i))
		if err := os.MkdirAll(cur, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cur, "WHALE.md"), []byte("# rules of "+cur), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cur
}

// TestDiscoverInstructionFilesBaselineCost 是**取证**（不判红绿）：
// 如实记录首次/再次的耗时与读盘次数，为"要不要做缓存"提供事实依据。
func TestDiscoverInstructionFilesBaselineCost(t *testing.T) {
	dir := buildInstructionTree(t, 6)
	reads := countReads(t)

	t0 := time.Now()
	files := DiscoverInstructionFiles(dir)
	first := time.Since(t0)
	readsAfterFirst := atomic.LoadInt64(reads)

	t0 = time.Now()
	_ = DiscoverInstructionFiles(dir)
	second := time.Since(t0)
	readsAfterSecond := atomic.LoadInt64(reads)

	t.Logf("首次=%v（读盘 %d 次）· 再次=%v（读盘 +%d 次）· 指令文件 %d 个",
		first, readsAfterFirst, second, readsAfterSecond-readsAfterFirst, len(files))
}

// TestDiscoverInstructionFilesDoesNotReReadUnchangedFiles 是**主判据**：
// 目录链与文件都没变 ⇒ 第二次调用不得再读文件内容。
//
// ★ 判别性自检：修复前（无缓存）本判据必红 —— 见回执里的 before 取证。
func TestDiscoverInstructionFilesDoesNotReReadUnchangedFiles(t *testing.T) {
	dir := buildInstructionTree(t, 6)
	reads := countReads(t)

	first := DiscoverInstructionFiles(dir)
	if atomic.LoadInt64(reads) == 0 {
		t.Fatal("首次调用应读盘 —— 若为 0，说明判据失去判别力（注入面没生效）")
	}

	before := atomic.LoadInt64(reads)
	second := DiscoverInstructionFiles(dir)
	repeated := atomic.LoadInt64(reads) - before

	if repeated != 0 {
		t.Fatalf("缓存未生效：目录链与文件均未改动，第二次调用仍读了 %d 个文件内容", repeated)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("缓存命中时结果必须与首次逐字段一致（first=%d second=%d）", len(first), len(second))
	}
}

// TestDiscoverInstructionFilesCacheInvalidatesOnContentChange 是**反例**：
// 文件内容改了 ⇒ 缓存必须失效，必须返回新内容。
// 没有这条，"缓存"就可能是"永远返回陈旧值"。
func TestDiscoverInstructionFilesCacheInvalidatesOnContentChange(t *testing.T) {
	dir := buildInstructionTree(t, 3)
	target := filepath.Join(dir, "WHALE.md")

	_ = DiscoverInstructionFiles(dir)

	if err := os.WriteFile(target, []byte("# CHANGED CONTENT FOR CACHE TEST"), 0o644); err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, f := range DiscoverInstructionFiles(dir) {
		if strings.Contains(f.Content, "CHANGED CONTENT FOR CACHE TEST") {
			found = true
		}
	}
	if !found {
		t.Fatal("文件已修改，但返回的是缓存里的陈旧内容 ⇒ 缓存失效判据不对")
	}
}
