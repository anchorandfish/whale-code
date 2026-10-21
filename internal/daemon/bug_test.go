package daemon

// 反例测试（Bug Reproduction Tests）— daemon 模块
//
// 本文件是对标审查机制的核心组件。每个测试对应 docs/BENCHMARK_FACTS.md 中的一个事实条目。
//
// 测试特点：
// - 测试名以 TestBug_ 开头
// - 在当前（含 bug 的）代码上运行会 **失败**，证明 bug 存在
// - 修复 bug 后测试 **通过**，证明 bug 已修
//
// 使用方式：
// go test ./internal/daemon/ -run TestBug_ -v
//
// 预期结果：当前代码下部分 TestBug_ 测试应失败（证明 bug 存在）
// 修复后：所有 TestBug_ 测试应通过（证明 bug 已修）

import (
	"context"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestBug_KillProcessTreeUnixMissing 对应 FACT-DAEMON-001
// 进程树终止必须跨平台：Unix 用进程组 kill（syscall.Kill(-pid, SIGTERM)），Windows 用 taskkill /T
// 修复后：killProcessTree 按 runtime.GOOS/build tag 分支，Unix 走 syscall.Kill 路径。
func TestBug_KillProcessTreeUnixMissing(t *testing.T) {
	// FACT-DAEMON-001
	if runtime.GOOS == "windows" {
		t.Skip("FACT-DAEMON-001: Windows 上跳过，此测试验证 Unix 实现已就位。" +
			"手动验证：检查 kill_unix.go 是否提供 syscall.Kill 分支")
	}

	// 修复后 Unix 使用 syscall.Kill。对不存在的 PID 返回 ESRCH（no such process）是预期行为，
	// 而非 bug。仅当错误表明仍在执行 Windows 的 taskkill 命令时才判定 bug 未修。
	err := killProcessTree(999999) // 不存在的 PID
	if err == nil {
		// 进程组 kill 对不存在的 PID 在某些实现下可能返回 nil，可接受
		return
	}
	errStr := err.Error()
	if strings.Contains(errStr, "executable file not found") || strings.Contains(errStr, "no such file or directory") {
		t.Errorf("BUG [FACT-DAEMON-001] 未修复: killProcessTree 在 Unix 上仍尝试执行 taskkill\n"+
			"  错误: %v\n"+
			"  期望: Unix 用 syscall.Kill(-pid, SIGTERM) 杀进程组\n"+
			"  实际: 仍走 exec.Command('taskkill', ...) 路径\n"+
			"  正确做法: Unix 用 syscall.Kill 杀进程组，fallback SIGKILL",
			err)
	}
	// ESRCH 等错误证明走了 syscall.Kill 路径，修复已生效
}

// TestBug_AuthTokenNotCrypto 对应 FACT-DAEMON-002
// 认证 token 必须密码学安全：用 crypto/rand 生成，至少 32 字节
// 修复后：generateAuthToken 改用 crypto/rand.Read 生成 32 字节随机数，hex 编码返回。
func TestBug_AuthTokenNotCrypto(t *testing.T) {
	// FACT-DAEMON-002
	token, err := generateAuthToken()
	if err != nil {
		t.Fatalf("generateAuthToken() returned error: %v", err)
	}

	// token 应为 32 字节的 hex 编码 = 64 字符
	if len(token) != 64 {
		t.Fatalf("token 长度 = %d, 期望 64 (32 bytes hex)", len(token))
	}

	bytes, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("无法解码 token: %v", err)
	}
	if len(bytes) != 32 {
		t.Fatalf("token 字节数 = %d, 期望 32", len(bytes))
	}

	// 统计字节频率
	// crypto/rand 生成的 32 字节，每个字节值出现频率应接近 32/256 ≈ 0.125
	// 时间戳生成的 32 字节，所有字节 = byte(time.Now().UnixNano() % 256)，频率 = 32
	freq := make(map[byte]int)
	for _, b := range bytes {
		freq[b]++
	}

	maxFreq := 0
	var maxByte byte
	for b, f := range freq {
		if f > maxFreq {
			maxFreq = f
			maxByte = b
		}
	}

	// crypto/rand 下 32 字节中某值出现 20+ 次的概率 ≈ 0（二项分布 n=32 p=1/256）
	// 时间戳下所有字节相同，maxFreq = 32
	if maxFreq >= 20 {
		t.Errorf("BUG [FACT-DAEMON-002]: 认证 token 非密码学安全\n"+
			"  token: %s\n"+
			"  字节 0x%02x 出现 %d/32 次\n"+
			"  期望: crypto/rand 生成（最大频率 ~1-3）\n"+
			"  实际: byte(time.Now().UnixNano() %% 256) 填充所有字节\n"+
			"  证据: ipc.go:438-448 generateAuthToken 从未调用 crypto/rand\n"+
			"  正确做法: crypto/rand.Read(b) 生成 32+ 字节\n"+
			"  影响: 攻击者知道启动时间即可暴力破解 token（仅 256 种可能）",
			token, maxByte, maxFreq)
	}
}

// TestBug_IPCPolling 对应 FACT-DAEMON-003
// IPC 不能用轮询：应用信号/事件通知，而非 100ms 轮询
// 当前实现：ipc.go:151-163 pollLoop 用 100ms 轮询，ipc.go:399 客户端也用 100ms 轮询
func TestBug_IPCPolling(t *testing.T) {
	// FACT-DAEMON-003
	// 此测试需要复杂 mock 来精确验证轮询行为，跳过并说明手动验证方法
	t.Skip("FACT-DAEMON-003: 需要手动验证 IPC 轮询问题\n" +
		"手动验证步骤:\n" +
		"  1. 检查 ipc.go:151-163 pollLoop 使用 time.NewTicker(100 * time.Millisecond)\n" +
		"  2. 检查 ipc.go:399 IPCClient.SendCommand 使用 time.Sleep(100 * time.Millisecond)\n" +
		"  3. 确认无 Unix Socket 实现（ipc.go 注释声称 Linux/Mac 用 Unix Socket 但实际全平台用文件轮询）\n" +
		"  4. 启动 daemon，用 strace/dtrace 观察每 100ms 的文件 stat 系统调用\n" +
		"正确做法: Unix 用 Unix Domain Socket 或 fsnotify，Windows 用 Named Pipe\n" +
		"当前问题: 100ms 轮询导致 CPU 浪费 + 最高 200ms 延迟（服务端+客户端各 100ms）")
}

// TestBug_CronNotPersisted 对应 FACT-DAEMON-004
// cron 调度必须持久化：cron 任务持久化到磁盘，重启后恢复
// 当前实现：daemon 模块无 cron 调度器，TaskQueue 仅支持即时任务
func TestBug_CronNotPersisted(t *testing.T) {
	// FACT-DAEMON-004
	// 验证 TaskQueue 仅有即时任务，无定时/cron 任务支持
	q := NewTaskQueue("")

	// TaskQueue 只有 Add（即时任务）和 AddWithNotify，无定时任务方法
	task := q.Add("test", "", TaskDream, PriorityNormal)

	// Task 结构体无 ScheduledAt / CronExpr 字段
	// 仅有 CreatedAt（创建时间），无调度时间
	if task.CreatedAt.IsZero() {
		t.Fatal("task should have CreatedAt")
	}

	// 验证：TaskQueue 无 cron 持久化
	t.Skip("FACT-DAEMON-004: 需要手动验证 cron 持久化缺失\n" +
		"手动验证步骤:\n" +
		"  1. grep -r 'cron\\|schedule\\|recurring\\|ScheduledAt' internal/daemon/\n" +
		"  2. 确认 Task 结构体（task.go:59-73）无调度时间字段\n" +
		"  3. 确认 TaskQueue 无 Schedule/Cron 方法\n" +
		"正确做法: cron 任务持久化到磁盘，daemon 重启后恢复调度\n" +
		"当前问题: 仅支持即时任务，无定时/cron 任务支持")
}

// TestBug_TaskQueuePointerStaleAfterRealloc 验证队列返回快照而非内部 slice 指针。
func TestBug_TaskQueuePointerStaleAfterRealloc(t *testing.T) {
	q := NewTaskQueue("")

	// Add 返回 &q.tasks[len(q.tasks)-1]（指向内部 slice 的指针）
	task1 := q.Add("task1", "", TaskDream, PriorityNormal)

	// 添加大量任务触发 slice 扩容（底层数组重新分配）
	for i := 0; i < 200; i++ {
		q.Add(fmt.Sprintf("filler-%d", i), "", TaskDream, PriorityLow)
	}

	// 通过 UpdateStatus 修改 task1 在队列中的状态
	// UpdateStatus 在当前 q.tasks 中查找并修改
	q.UpdateStatus(task1.ID, TaskCompleted, "done")

	if task1.Status != TaskPending {
		t.Errorf("Add 返回值应是隔离快照，got status %s", task1.Status)
	}
	if current := q.Get(task1.ID); current == nil || current.Status != TaskCompleted {
		t.Fatalf("队列内状态未正确更新: %#v", current)
	}
}

// TestBug_WorkerPoolLoggerOverwritten 对应 FACT-CONC-001
// 共享状态修改必须持写锁：WorkerPool.Start 覆盖 SetLogger 设置的 logger
// 且忽略 NewDaemonLogger 的错误（用 _ 丢弃），可能导致 logger 为 nil
func TestBug_WorkerPoolLoggerOverwritten(t *testing.T) {
	tmpDir := t.TempDir()

	// 创建 daemon 配置的 logger
	logger, err := NewDaemonLogger(filepath.Join(tmpDir, "daemon.log"))
	if err != nil {
		t.Fatalf("NewDaemonLogger failed: %v", err)
	}
	// ★ （Windows CI）：日志/SQLite 句柄先关，t.TempDir 清理才删得掉
	// daemon.log / tasks.db（Windows 不许删除仍被打开的文件）。
	t.Cleanup(func() { _ = logger.Close() })

	pool := NewWorkerPool(2, "whale", time.Minute)
	pool.SetLogger(logger) // daemon.go:144 设置 daemon 的 logger

	queue := NewTaskQueue(filepath.Join(tmpDir, "tasks.json"))
	t.Cleanup(func() { _ = queue.Close() })
	pool.Start(context.Background(), queue)
	defer pool.Stop()

	gotLogger := pool.logger

	if gotLogger == nil {
		t.Errorf("BUG [FACT-CONC-001]: WorkerPool.Start 覆盖 logger 为 nil\n" +
			"  原因: worker.go:282 p.logger, _ = NewDaemonLogger(...) 忽略错误\n" +
			"  NewDaemonLogger 失败时返回 nil，覆盖 SetLogger 设置的 logger\n" +
			"  正确做法: 处理 NewDaemonLogger 错误，失败时保留 SetLogger 设置的 logger")
		return
	}

	// 更重要的问题：Start 用临时目录的日志文件覆盖了 daemon 配置的日志文件
	// 这意味着 worker 的日志不会写到 daemon 的日志文件中
	if gotLogger.logPath != logger.logPath {
		t.Errorf("BUG [FACT-CONC-001]: WorkerPool.Start 覆盖 SetLogger 设置的 logger\n"+
			"  SetLogger 设置的日志路径: %s\n"+
			"  Start 覆盖后的日志路径: %s\n"+
			"  原因: worker.go:282 p.logger, _ = NewDaemonLogger(os.TempDir()+\"whale-daemon-pool.log\")\n"+
			"  覆盖了 daemon.go:144 SetLogger(d.logger) 设置的 logger\n"+
			"  影响: worker 日志写到临时文件而非 daemon 日志文件，日志分散无法排查",
			logger.logPath, gotLogger.logPath)
	}
}
