// Package util — 多实例检测锁（V3-5-3 v1.0 轻量版）。
//
// 采用 PID 文件 + 进程存活检测实现警告式多实例检测：
//   - 启动时写入 PID 到 ~/.whale/.lock
//   - 已有存活 PID 时仅警告（不阻止多开，尊重用户多开意图）
//   - 进程不存活时视为 stale lock，覆盖写入
//   - Release 时校验 PID 匹配再删除（避免误删其他实例的锁）
//
// 复用模式：参考 internal/config/lockfile.go 的 PID + 时间戳 + stale 检测模式。
// 跨平台进程存活检测实现见 instance_lock_unix.go / instance_lock_windows.go
// （因 config 包已引用 util 包，util 不可反向引用 config，故在此独立实现）。
package util

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
)

// InstanceLock 实例锁，用于检测多实例并发运行。
// 采用 PID 文件 + 进程存活检测，不强制阻止多开（仅警告）。
type InstanceLock struct {
	lockPath string // PID 文件路径（~/.whale/.lock）
	pid      int    // 当前进程 PID
}

// NewInstanceLock 创建实例锁。
// lockPath: PID 文件路径（通常为 ~/.whale/.lock）。
func NewInstanceLock(lockPath string) *InstanceLock {
	return &InstanceLock{
		lockPath: lockPath,
		pid:      os.Getpid(),
	}
}

// Acquire 尝试获取实例锁。
// 返回：已存在的存活 PID（0 表示无其他实例）；错误（锁文件创建失败）。
// 不强制阻止多开：即使检测到其他实例也返回 nil 错误，仅返回已存在 PID。
// 行为：
//   - 锁文件不存在 → 创建并写入当前 PID + 时间戳，返回 (0, nil)
//   - 锁文件存在但 PID 不存活（stale）→ 覆盖写入，返回 (0, nil)
//   - 锁文件存在且 PID 存活 → 不覆盖，返回 (existingPID, nil)（警告式）
//   - 锁文件目录不可写等错误 → 返回 (0, err)（调用方决定是否阻塞）
func (l *InstanceLock) Acquire() (int, error) {
	// 确保锁文件所在目录存在（权限取唯一声明 LockDirPerm，见 lock_verdict.go F11 L2）
	if err := os.MkdirAll(filepath.Dir(l.lockPath), LockDirPerm); err != nil {
		return 0, fmt.Errorf("create lock dir: %w", err)
	}

	// 读取已有锁文件，检测前序实例
	existingPID, err := l.readExistingPID()
	if err != nil {
		// 读失败（文件不存在或格式异常）视为无前序实例，继续创建
		existingPID = 0
	}

	// 若已有存活实例，不覆盖写入（警告式，由调用方决定是否继续）
	if existingPID > 0 && existingPID != l.pid {
		return existingPID, nil
	}

	// 无前序实例或前序实例已退出（stale）→ 写入当前 PID
	if err := l.writeLock(); err != nil {
		return 0, fmt.Errorf("write instance lock: %w", err)
	}

	return 0, nil
}

// Release 释放实例锁（删除 PID 文件）。
// 仅当 PID 文件中的 PID 与当前进程匹配时才删除（避免误删其他实例的锁）。
// 进程崩溃时由后续实例的 stale 检测清理。
func (l *InstanceLock) Release() error {
	data, err := os.ReadFile(l.lockPath)
	if err != nil {
		// 文件不存在视为已释放
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read lock file: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		return nil
	}

	filePID, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		// 格式异常，保守不删除（避免误删）
		return nil
	}

	// 仅当 PID 匹配时才删除（避免误删其他实例的锁）
	if filePID != l.pid {
		return nil
	}

	return os.Remove(l.lockPath)
}

// CheckExistingInstance 检查是否已有实例运行（只读检测，不获取锁）。
// 返回：已存在的存活 PID（0 表示无其他实例）；错误。
// 用于 doctor --lock-test 检测，不修改锁文件。
func CheckExistingInstance(lockPath string) (int, error) {
	l := &InstanceLock{lockPath: lockPath, pid: os.Getpid()}
	return l.readExistingPID()
}

// readExistingPID 读取锁文件并检测 PID 是否存活。
// 返回：存活的 PID（0 表示无前序实例或前序实例已退出）；错误。
// 行为：
//   - 文件不存在 → (0, nil)
//   - 格式异常 → (0, nil)（保守，视为无前序实例）
//   - PID 不存活 → (0, nil)（stale，调用方可覆盖）
//   - PID 存活 → (pid, nil)
func (l *InstanceLock) readExistingPID() (int, error) {
	data, err := os.ReadFile(l.lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 1 {
		return 0, nil
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, nil
	}

	if pid <= 0 {
		return 0, nil
	}

	// 自身 PID 视为无前序实例（避免同一进程重复检测误报）
	if pid == l.pid {
		return 0, nil
	}

	// 检测进程存活
	if !platform.ProcessAlive(pid) {
		return 0, nil
	}

	return pid, nil
}

// writeLock 写入当前 PID + 时间戳到锁文件。
// 使用 O_TRUNC 覆盖已有 stale lock。
func (l *InstanceLock) writeLock() error {
	content := fmt.Sprintf("%d\n%d\n", l.pid, time.Now().Unix())
	return os.WriteFile(l.lockPath, []byte(content), 0600)
}
