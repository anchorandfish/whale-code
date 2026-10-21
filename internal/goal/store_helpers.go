package goal

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// 可测试钩子（默认走标准库，测试时可覆盖以加速）。
var (
	// nowUnix 返回当前 Unix 时间戳（秒）。
	nowUnix = func() int64 { return time.Now().Unix() }
	// sleepFn 休眠指定纳秒。
	sleepFn = func(ns int64) { time.Sleep(time.Duration(ns)) }
)

// isStaleLock 检查锁文件是否过期（可被抢占）。
//
// ★ F11 局部收敛：判据唯一来源是 `util.LockVerdict`（见 lockVerdict），
// 本函数不再自带二值分支，只做薄转发 `MaySteal()` —— 与 config.isStaleLock /
// memory.isStaleLock 收敛为同一份声明（之前注释自承「本包只需 5 行」）。
//
// 策略与 `config.isStaleLock` 等价：
// - 读取失败 → Stale（抢占）
// - 内容无法解析（行数 < 2 / PID 非数字 / 时间戳非数字）→ Unknown（保守，不夺）
// - 超时 5 分钟 → Stale
// - 本包不检查 PID 存活（跨平台降级：仅依赖时间戳超时）→ Held
func isStaleLock(lockPath string) bool {
	return lockVerdict(lockPath).MaySteal()
}

// lockVerdict 对锁文件做**三值**判定（F11 L4 的唯一判定点，与 config.lockVerdict 同源）。
func lockVerdict(lockPath string) util.LockVerdict {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return util.LockVerdictStale // 读不到视为 stale
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return util.LockVerdictUnknown // 无法解析 → 未知 → 不夺
	}
	if _, err = strconv.Atoi(strings.TrimSpace(lines[0])); err != nil {
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
	// 跨平台降级：不检查 PID 存活（与 config.isStaleLock 的差异点，见函数注释）
	return util.LockVerdictHeld
}
