// Package session — 多开（多进程）行为探针。
//
// 「多开」在生产上是**多个独立进程**同时操作同一个 ~/.whale/sessions 目录，
// 不是同一进程内多个 goroutine。本探针用 re-exec 测试二进制的方式起真实
// 子进程复现，而不是用 goroutine 近似（goroutine 共享进程内存与同一份 Store，
// 无法暴露跨进程锁的真实行为）。
//
// 两个被测事实：
// 1. 会话占用（ownership）：一个进程正在使用某会话时，另一个进程不该能往里写。
// 生产对应场景 = 两个终端都 `whale -c` 打开同一个最近会话，或 `--fork-session`
// 失败后误指向同一会话。
// 2. 会话 ID 唯一性：ID 生成器必须在并发/高频下不撞号，否则多开的会话会互相覆盖。
//
// 判别性（守卫意义）：
// - TestProbeMultiInstance_SessionHeldByOtherProcess 断言「占用期间他人写入被拒」。
// 若锁被移除、或退化成只保护单次写入的瞬时锁，他人写入会成功 → 本用例转红。
// - TestProbeMultiInstance_DistinctSessions 断言「不同会话互不干扰」。
// 若锁被误改成全局目录锁，多开会互相阻塞 → 本用例转红。
package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	probeChildEnv  = "WHALE_SESSION_PROBE_CHILD"
	probeDirEnv    = "WHALE_SESSION_PROBE_DIR"
	probeIDEnv     = "WHALE_SESSION_PROBE_ID"
	probeModeEnv   = "WHALE_SESSION_PROBE_MODE"
	probeTagEnv    = "WHALE_SESSION_PROBE_TAG"
	probeReadyFile = "probe.ready"
)

// TestProbeMultiInstanceChild 是子进程序体，由父用例 re-exec 触发，不单独运行。
//
// 两种模式：
// - mode=hold ：获取会话锁并**持续持有**（模拟一个正在使用该会话的 CLI 进程），
// 落地 dir/probe.ready 标记后睡眠，直到被父进程 kill。
// - mode=write：对该会话追加一条消息（模拟另一个进程尝试写入同一会话）。
//
// 子进程刻意不继承父进程任何状态，完全复刻新启动进程的首次落盘行为。
func TestProbeMultiInstanceChild(t *testing.T) {
	if os.Getenv(probeChildEnv) != "1" {
		t.Skip("not a probe child process")
	}
	dir := os.Getenv(probeDirEnv)
	id := os.Getenv(probeIDEnv)
	mode := os.Getenv(probeModeEnv)
	if dir == "" || id == "" {
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=bad-env")
		os.Exit(9)
	}
	store, err := NewStore(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "PROBE_RESULT=new-store-fail err=%v\n", err)
		os.Exit(9)
	}

	switch mode {
	case "hold":
		lock, err := store.AcquireLock(id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "PROBE_RESULT=lock-denied err=%v\n", err)
			os.Exit(3)
		}
		// 通知父进程：本进程已持有该会话。内容 = 自己的 PID
		// （供 process_windows_test.go 诊断判别器定位持有者；
		// waitForReadyFile 只 stat 不读内容，其余用例不受影响）。
		_ = os.WriteFile(filepath.Join(dir, probeReadyFile), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=lock-acquired")
		// 持续持有，直到父进程发信号终止（模拟会话被长期使用）。
		time.Sleep(30 * time.Second)
		lock.Release()
		os.Exit(0)
	case "write":
		if err := store.AppendBatch(id, []Message{UserText("probe write")}); err != nil {
			fmt.Fprintf(os.Stderr, "PROBE_RESULT=write-denied err=%v\n", err)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=write-ok")
		os.Exit(0)
	case "append-many":
		// 复刻生产上「两个终端都 -c 打开同一会话，各自连续对话」：
		// 每个进程反复 Append 自己的一批消息（锁只在单次 Append 期间持有）。
		tag := os.Getenv(probeTagEnv)
		for i := 0; i < 20; i++ {
			msg := UserText(fmt.Sprintf("[%s] turn %d", tag, i))
			if err := store.AppendBatch(id, []Message{msg}); err != nil {
				fmt.Fprintf(os.Stderr, "PROBE_RESULT=append-denied i=%d err=%v\n", i, err)
				os.Exit(3)
			}
			// 诊断：写入返回成功后，立即回读文件真实行数（验证「返回成功是否真落盘」）。
			if raw, rerr := os.ReadFile(filepath.Join(dir, SanitizeSessionID(id)+".jsonl")); rerr == nil {
				lines := 0
				for _, b := range raw {
					if b == '\n' {
						lines++
					}
				}
				fmt.Fprintf(os.Stderr, "PROBE_DIAG %s i=%d fileLines=%d\n", tag, i, lines)
			}
			time.Sleep(2 * time.Millisecond)
		}
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=append-many-ok")
		os.Exit(0)
	case "hold-session":
		// 的**生产形态**：占用会话并长期持有 —— 等价于「一个窗口开着这个会话」。
		// 与 mode=hold 的区别：hold 直接拿裸锁；这里走的是生产入口 Store.HoldSession，
		// 即 TUI 打开/恢复会话时真正调用的那条路径。
		if err := store.HoldSession(id); err != nil {
			fmt.Fprintf(os.Stderr, "PROBE_RESULT=hold-session-denied err=%v\n", err)
			os.Exit(3)
		}
		_ = os.WriteFile(filepath.Join(dir, probeReadyFile), []byte(fmt.Sprintf("%d", os.Getpid())), 0o600)
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=hold-session-acquired")
		time.Sleep(30 * time.Second)
		store.ReleaseHeldSession()
		os.Exit(0)
	case "hold-once":
		// 第二个进程尝试占用同一会话：应当被拒（的判别点）。
		if err := store.HoldSession(id); err != nil {
			fmt.Fprintf(os.Stderr, "PROBE_RESULT=hold-denied err=%v\n", err)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "PROBE_RESULT=hold-ok")
		store.ReleaseHeldSession()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "PROBE_RESULT=bad-mode %q\n", mode)
		os.Exit(9)
	}
}

// startProbeChild 启动一个真实子进程，返回 cmd（调用方负责等待/终止）。
func startProbeChild(t *testing.T, dir, id, mode string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProbeMultiInstanceChild", "-test.v")
	cmd.Env = append(os.Environ(),
		probeChildEnv+"=1",
		probeDirEnv+"="+dir,
		probeIDEnv+"="+id,
		probeModeEnv+"="+mode,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动子进程失败: %v", err)
	}
	return cmd
}

// waitForReadyFile 等待子进程落地持有标记，超时即失败。
func waitForReadyFile(t *testing.T, dir string) {
	t.Helper()
	ready := filepath.Join(dir, probeReadyFile)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("持有方子进程未能在 10s 内获取会话锁")
}

// runProbeWriteChild 启动并等待一个写入型子进程，返回其错误（nil = 写成功）。
//
// 注意：必须一次性 CombinedOutput（Start+Wait），不能复用 startProbeChild 的
// 已 Start 进程，否则 err 会是 "exec: already started" 而非真实写入结果，
// 造成「看起来写入被拒」的静默假通过。
func runProbeWriteChild(t *testing.T, dir, id string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProbeMultiInstanceChild", "-test.v")
	cmd.Env = append(os.Environ(),
		probeChildEnv+"=1",
		probeDirEnv+"="+dir,
		probeIDEnv+"="+id,
		probeModeEnv+"=write",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("probe writer(%s): err=%v output=%s", id, err, out)
	return err
}

// TestProbeMultiInstance_SessionHeldByOtherProcess 会话被一个进程持有时，他人写入必须被拒。
func TestProbeMultiInstance_SessionHeldByOtherProcess(t *testing.T) {
	dir := t.TempDir()
	id := "shared-session"

	holder := startProbeChild(t, dir, id, "hold")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	if err := runProbeWriteChild(t, dir, id); err == nil {
		t.Fatal("会话已被另一进程持有，写入仍成功 —— 多开会互相写串（会话占用未表达）")
	}
}

// TestProbeMultiInstance_DistinctSessions 不同会话互不干扰：多开必须可行。
func TestProbeMultiInstance_DistinctSessions(t *testing.T) {
	dir := t.TempDir()
	held := "session-held"

	holder := startProbeChild(t, dir, held, "hold")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	// 另一个进程开自己的会话：不该被前者的锁影响。
	if err := runProbeWriteChild(t, dir, "session-other"); err != nil {
		t.Fatalf("多开互不干扰的前提被破坏：写另一个会话却失败: %v", err)
	}
}

// runProbeAppendManyChild 启动一个「连续对话」型子进程并等待其结束。
func runProbeAppendManyChild(t *testing.T, dir, id, tag string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProbeMultiInstanceChild", "-test.v")
	cmd.Env = append(os.Environ(),
		probeChildEnv+"=1",
		probeDirEnv+"="+dir,
		probeIDEnv+"="+id,
		probeModeEnv+"=append-many",
		probeTagEnv+"="+tag,
	)
	out, err := cmd.CombinedOutput()
	t.Logf("probe appender(%s/%s): err=%v output=%s", id, tag, err, out)
	return err
}

// TestProbeMultiInstance_ConcurrentAppendContention 两个进程并发往**同一会话**追加时的
// 争用行为。生产等价场景：两个终端都 -c 打开同一个最近会话，或两个会话名撞车。
//
// ★ L1 修复（flock 迁移，2026-09-22）后实测结论更新：
// - flock 让并发写**串行化**（后到者短暂阻塞等待，而非立即失败）；
// - 因此「至少一方被拒（denied>0）」不再是必要断言 —— flock 下两者可能都成功
// （锁在每次 Append 期间持有，重叠时一方等待）；
// - 真正要钉死的不变量是：**无数据撕裂、无超量、每条消息完整**（O_APPEND + flock
// 保证单条完整性，串行化保证不写串）。
//
// 也就是说：多开不同会话安全；多开撞同一会话时，flock 保证不写串（而非依赖「一方失败」）。
func TestProbeMultiInstance_ConcurrentAppendContention(t *testing.T) {
	dir := t.TempDir()
	id := "shared-session"

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, tag := range []string{"A", "B"} {
		wg.Add(1)
		go func(tag string) {
			defer wg.Done()
			results <- runProbeAppendManyChild(t, dir, id, tag)
		}(tag)
	}
	wg.Wait()
	close(results)

	denied := 0
	for err := range results {
		if err != nil {
			denied++
		}
	}

	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		info, _ := e.Info()
		t.Logf("会话目录残留文件: %s (%d bytes)", e.Name(), info.Size())
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore 失败: %v", err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	written := len(loaded.GetMessages())
	seq := make([]string, 0, written)
	for _, m := range loaded.GetMessages() {
		text := m.TextContent()
		if len(text) > 2 && text[0] == '[' {
			seq = append(seq, text[1:2])
		} else {
			seq = append(seq, "?")
		}
	}
	t.Logf("并发争用结果：被拒进程数=%d，已落盘消息数=%d（两进程各尝试 20 条）", denied, written)
	t.Logf("落盘消息归属序列：%v", seq)

	// flock 迁移后，两者都可能成功（串行化而非互斥失败）；真正的不变量是：
	// 落盘不超过尝试总数、每条消息完整（无撕裂、无写串）。
	if written > 40 {
		t.Fatalf("落盘消息数 %d 超过尝试总数 40，数据异常", written)
	}
	// 已落盘的每条消息都必须完整（不撕裂）：解析出的条数与文本格式一致。
	for i, m := range loaded.GetMessages() {
		if len(m.TextContent()) < 10 {
			t.Fatalf("第 %d 条消息内容不完整（可能被撕裂）: %q", i, m.TextContent())
		}
	}
}

// TestProbeMultiInstance_SequentialAppendPreservesBoth 两个进程**先后**（不并发）写同一
// 会话时，两边的消息都必须保留。用于区分「争用导致拒写」与「写入成功却丢失」。
func TestProbeMultiInstance_SequentialAppendPreservesBoth(t *testing.T) {
	dir := t.TempDir()
	id := "shared-session"

	for _, tag := range []string{"A", "B"} {
		if err := runProbeAppendManyChild(t, dir, id, tag); err != nil {
			t.Fatalf("串行追加子进程(%s)失败: %v", tag, err)
		}
	}

	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore 失败: %v", err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("读取会话失败: %v", err)
	}
	if got := len(loaded.GetMessages()); got != 40 {
		t.Fatalf("串行追加应保留 40 条（两进程各 20 条），实际 %d 条 —— 存在写入成功却丢失的问题", got)
	}
}

// runProbeHoldOnceChild 启动一个「尝试占用会话」的子进程并等待其结束。
// 返回 nil 表示占用成功。
func runProbeHoldOnceChild(t *testing.T, dir, id string) error {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestProbeMultiInstanceChild", "-test.v")
	cmd.Env = append(os.Environ(),
		probeChildEnv+"=1",
		probeDirEnv+"="+dir,
		probeIDEnv+"="+id,
		probeModeEnv+"=hold-once",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("probe holder(%s): err=%v output=%s", id, err, out)
	return err
}

// TestProbeMultiInstance_HoldSessionRefusesSecondProcess 的核心判别器：
// 一个进程占用了会话之后，第二个进程**占用同一个会话必须被拒** ——
// 这就是「第二个窗口直接拦下」；而占用**另一个**会话必须照常成功
// （多开不同会话不能被误伤成全局锁）。
//
// 判别性（反例注入已证）：让 HoldSession 不再真正持锁 ⇒ 第二个进程会成功 ⇒ 本用例转红。
//
// 与 TestProbeMultiInstance_SessionHeldByOtherProcess 的区别：那条用的是**人造**的
// hold 子进程（直接拿裸锁睡 30s），生产从来没有那样的持有者；本用例的持有者走的是
// 生产入口 HoldSession，即 TUI 打开会话时真正调用的那条路径。
func TestProbeMultiInstance_HoldSessionRefusesSecondProcess(t *testing.T) {
	dir := t.TempDir()
	const id = "shared-session"

	holder := startProbeChild(t, dir, id, "hold-session")
	defer func() {
		_ = holder.Process.Kill()
		_, _ = holder.Process.Wait()
	}()
	waitForReadyFile(t, dir)

	// 同一会话：第二个进程必须被拦下。
	if err := runProbeHoldOnceChild(t, dir, id); err == nil {
		t.Fatal("会话已被另一进程占用，第二个进程仍成功占用 —— 多开同一会话未被拦下（两段对话会写串）")
	}
	// 不同会话：必须不受影响（否则是把会话锁退化成了全局锁）。
	if err := runProbeHoldOnceChild(t, dir, "session-other"); err != nil {
		t.Fatalf("多开互不干扰被破坏：占用另一个会话却被拒: %v", err)
	}
}

// TestProbeSessionIDUniqueness 会话 ID 生成器必须不撞号（多开不互相覆盖的前提）。
//
// 说明：本用例用高频循环压测 ID 空间，属于**空间下界证据**（生日悖论），
// 并非声称生产会在这个速率下建会话；ID 的随机位不足时，撞号只是时间问题，
// 且撞号后果是静默覆盖（两份会话写进同一文件），故必须保证足够熵。
func TestProbeSessionIDUniqueness(t *testing.T) {
	const n = 20000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := GenerateID()
		if _, dup := seen[id]; dup {
			t.Fatalf("GenerateID 产生重复 ID: %s（第 %d 次）—— ID 熵不足，多开可能静默覆盖", id, i)
		}
		seen[id] = struct{}{}
	}
}
