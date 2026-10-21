// Package config — lockfile 实现跨进程文件锁（3-C-4）
//
// 用 os.OpenFile(O_CREATE|O_EXCL) 创建排他锁文件，防止多进程并发写入配置。
// 锁文件内容为 "PID\n时间戳" 两行。检测 stale lock（超时 5 分钟或 PID 不存活）后抢占。
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/platform"
	"github.com/anchorandfish/whale-code/internal/util"
)

// acquireLock 获取指定路径的排他锁文件。
// 用 os.OpenFile(O_CREATE|O_EXCL) 创建锁文件（路径 + ".lock"），写入 PID + 创建时间戳。
// 若锁文件已存在且为有效锁（PID 存活且未超时），阻塞重试直到获取或超时。
// 若锁文件已存在但为 stale（PID 不存活或超时 5 分钟），抢占锁。
// 返回 release 函数，调用后删除锁文件释放锁；返回 error 表示获取失败。
//
// 阻塞策略：最多重试 50 次，每次间隔 100ms，总阻塞上限 5 秒。
func acquireLock(path string) (func(), error) {
	lockPath := path + ".lock"

	// 最多重试 50 次 × 100ms = 5 秒
	for attempt := 0; attempt < 50; attempt++ {
		fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			// 创建成功，写入 PID + 时间戳
			content := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Unix())
			if _, wErr := fd.WriteString(content); wErr != nil {
				fd.Close()
				os.Remove(lockPath)
				return nil, fmt.Errorf("write lockfile: %w", wErr)
			}
			fd.Close()

			release := func() {
				os.Remove(lockPath)
			}
			return release, nil
		}

		// 锁文件已存在，检查是否 stale
		if isStaleLock(lockPath) {
			// 抢占 stale lock：删除后重试
			os.Remove(lockPath)
			continue
		}

		// 锁被持有，等待后重试
		time.Sleep(100 * time.Millisecond)
	}

	// 失败收尾：若锁文件处于**未知**态（内容损坏 / 不可解析），把根因写进文案。
	// 修复前这里只会说 "acquire lock timeout (5s)"，用户看不到真正原因是一个损坏的
	// 锁文件（I-3.4 记录的后果：只能人工删锁，且不知道删哪一个）。
	// ★ 前缀保持不变 —— 既有断言依赖 "acquire lock" 子串（`lockfile_test.go`）。
	if v := lockVerdict(lockPath); v == util.LockVerdictUnknown {
		return nil, fmt.Errorf("acquire lock timeout (5s): %s"+
			"（锁文件存在但内容无法解析 ⇒ 按「未知不夺锁」语义保留；"+
			"请人工检查该文件，确认无进程在读它后删除再重试）", lockPath)
	}
	return nil, fmt.Errorf("acquire lock timeout (5s): %s", lockPath)
}

// isStaleLock 检查锁文件是否过期（可被抢占）。
//
// ★ 本函数现为 `lockVerdict` 的**薄转发**（F11 L4，2026-09-20）：判据唯一来源是
// `util.LockVerdict`，本包不再自带二值分支。修复前的写法是四个 `return` 各自
// 直接给 bool，其中「内容无法解析 ⇒ return false」是**提前 return** ⇒ 5 分钟超时
// 分支永远取不到；现在「未知 ⇒ 不可夺」由 `util.LockVerdictUnknown.MaySteal()==false`
// 这份**声明**保证，而不是某一行 return 的位置。
func isStaleLock(lockPath string) bool {
	return lockVerdict(lockPath).MaySteal()
}

// lockVerdict 对锁文件做**三值**判定（F11 L4 的唯一判定点）。
//
// 规则（与 session 侧 `Store.lockVerdict` 同一套语义，两侧都走 util.LockVerdict）：
//
//	读不到文件 ⇒ Stale （锁不存在 ⇒ 可夺）
//	内容无法解析 ⇒ Unknown （**不夺**；修复前 config 与 session 在此相反）
//	超时 5 分钟 ⇒ Stale
//	PID 不存活 ⇒ Stale
//	否则 ⇒ Held
//
// ⚠️ 已知残留（**未修**，本轮刻意不动）：`Unknown` 不可夺意味着损坏的锁文件会让
// 写入一直重试到 5 秒上限后失败（人工删锁才能恢复）。上游批复（I-3.4）选定的语义
// 就是「未知默认不夺」= fail-closed，故保留；但**报错文案**已补上根因提示
// （见 acquireLock 末尾），不让用户只看到 "acquire lock timeout"。
func lockVerdict(lockPath string) util.LockVerdict {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return util.LockVerdictStale // 读不到视为 stale
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return util.LockVerdictUnknown // 无法解析 → 未知 → 不夺
	}

	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return util.LockVerdictUnknown // PID 非数字 → 未知 → 不夺
	}

	timestamp, err := strconv.ParseInt(strings.TrimSpace(lines[1]), 10, 64)
	if err != nil {
		return util.LockVerdictUnknown // 时间戳非数字 → 未知 → 不夺
	}

	// 超时 5 分钟视为 stale
	if time.Since(time.Unix(timestamp, 0)) > 5*time.Minute {
		return util.LockVerdictStale
	}

	// PID 不存活视为 stale
	if !platform.ProcessAlive(pid) {
		return util.LockVerdictStale
	}

	return util.LockVerdictHeld
}

// 注：本文件原先在此处声明 `processAlive`（及其 process_alive_unix.go /
// process_alive_windows.go 两份平台实现）。2026-09-20（收敛）已全部删除 ——
// config / util / daemon / session 四域「pid 还活着吗」统一到
// internal/platform.ProcessAlive，此处经该函数调用，不再自留平台分支。
// 唯一契约（详见该函数注释）：pid<=0 ⇒ false；ESRCH ⇒ false；
// **EPERM ⇒ true**（进程存在但无权发信号，正是 的修复点）。
// 历史：C-1 修复（全模块审查）前，本处实现恒返回 true，
// 导致进程崩溃后 5 分钟内锁不会被抢占。

// cleanupOldBackups 清理旧备份，保留最近 N 个。
// 备份命名格式：settings.json.<timestamp>.bak（timestamp 为 YYYYMMDD-HHMMSS）。
// 按文件名排序（字典序即时间序），超出 keep 数量的旧备份被删除。
func cleanupOldBackups(settingsPath string, keep int) {
	dir := filepath.Dir(settingsPath)
	base := filepath.Base(settingsPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	var backups []os.DirEntry
	for _, entry := range entries {
		name := entry.Name()
		// 匹配 settings.json.<timestamp>.bak 格式
		if strings.HasPrefix(name, base+".") && strings.HasSuffix(name, ".bak") {
			backups = append(backups, entry)
		}
	}

	if len(backups) <= keep {
		return
	}

	// 按文件名排序（含时间戳，字典序即时间序）
	for i := 0; i < len(backups)-1; i++ {
		for j := i + 1; j < len(backups); j++ {
			if backups[j].Name() < backups[i].Name() {
				backups[i], backups[j] = backups[j], backups[i]
			}
		}
	}

	// 删除最旧的，保留最近 keep 个
	for i := 0; i < len(backups)-keep; i++ {
		os.Remove(filepath.Join(dir, backups[i].Name()))
	}
}
