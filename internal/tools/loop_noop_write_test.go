package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 判别器：
// 没有任何任务到点时，调度器**不得**重写 loop_tasks.json。
//
// 背景（实测，非推断）：原 checkAndFire 每个 tick（1s）无条件 saveToFileLocked()，
// 空任务列表也照写 ⇒ 单实例 86400 写/天。每次写都 CreateTemp 出
// <name>.<rand>.tmp；本机之外层沙箱对 ~/.whale 内的 rename/unlink 返回 EPERM
// （whale.log 有原话：`rename ... operation not permitted`），连带错误分支里的
// os.Remove 也失败 ⇒ 临时文件滞留原地。~/.whale 顶层因此攒到 29.9 万条目，
// 直接把 doctor 的 "Recovery / Data Integrity" 5 秒扫描打超时。
//
// 红线：本测试必须在修复前为 RED（文件被重写），修复后为 GREEN。
func TestCheckAndFire_NoDueTasks_DoesNotRewrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loop_tasks.json")

	seed := []byte("{\n  \"next_id\": 0,\n  \"tasks\": []\n}")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("准备种子文件失败: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before 失败: %v", err)
	}

	ls := NewLoopScheduler(nil, path)
	ls.Start()
	time.Sleep(2500 * time.Millisecond) // 至少跨过 2 个 tick
	ls.Stop()

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after 失败: %v", err)
	}

	if !os.SameFile(before, after) {
		t.Fatalf("没有任务到点，文件却被原子替换了（inode 变了）"+
			"\n  before inode=%v  after inode=%v"+
			"\n  ⇒ checkAndFire 仍在每个 tick 无条件落盘", before, after)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("没有任务到点，文件 mtime 却前进了：%v → %v", before.ModTime(), after.ModTime())
	}

	if leftovers, _ := filepath.Glob(path + ".*.tmp"); len(leftovers) != 0 {
		t.Fatalf("无谓写入留下了 %d 个临时文件：%v", len(leftovers), leftovers)
	}
}

// 反向守卫：真到点的任务必须照旧落盘（防止修复把"该写的"一起抑制掉）。
func TestCheckAndFire_DueTask_IsPersisted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loop_tasks.json")
	if err := os.WriteFile(path, []byte("{\n  \"next_id\": 0,\n  \"tasks\": []\n}"), 0o600); err != nil {
		t.Fatalf("准备种子文件失败: %v", err)
	}

	fired := make(chan string, 4)
	ls := NewLoopScheduler(func(id, prompt string) { fired <- id }, path)
	task, err := ls.CreateWithOpts(LoopCreateOptions{
		IntervalStr: "30s",
		Prompt:      "ping",
		Recurring:   true,
	})
	if err != nil {
		t.Fatalf("建任务失败: %v", err)
	}

	// 把它推成"早该触发"：创建时间退到 1 小时前，且去掉抖动偏移。
	ls.mu.Lock()
	task.CreatedAt = time.Now().Add(-time.Hour)
	task.LastFiredAt = nil
	task.jitterOff = 0
	ls.mu.Unlock()

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before 失败: %v", err)
	}

	ls.Start()
	select {
	case <-fired:
	case <-time.After(5 * time.Second):
		ls.Stop()
		t.Fatal("到点的任务没有触发（checkAndFire 没有识别出 toFire）")
	}
	ls.Stop()

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after 失败: %v", err)
	}
	if os.SameFile(before, after) && after.ModTime().Equal(before.ModTime()) {
		t.Fatal("任务真的触发了，状态却没有落盘 —— 修复把该写的也抑制掉了")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回落盘内容失败: %v", err)
	}
	if !strings.Contains(string(data), `"fire_count": 1`) {
		t.Fatalf("落盘内容里没有 fire_count=1，实际内容：\n%s", string(data))
	}
}
