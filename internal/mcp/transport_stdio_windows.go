package mcp

import (
	"os/exec"
)

// signalProcessGroupGraceful 向进程组发送优雅终止信号（Windows no-op）。
//
// 用于 stdio 三阶段清理的 Stage 2（M5/C11）：
//
//	Stage 1: stdin Close（EOF）
//	Stage 2: 本函数 — Windows 无优雅组终止信号，返回 nil（no-op）
//	Stage 3: sandbox.KillProcessGroup（taskkill /T /F 兜底）
//
// Windows 缺乏类似 Unix SIGTERM 的优雅组终止原语（无 Job Object 时），
// 故 Stage 2 为 no-op，直接由 Stage 3 的 sandbox.KillProcessGroup 强制终止。
// 这是 P0 降级方案；Phase C 将引入 Job Object 实现真正的优雅终止。
func signalProcessGroupGraceful(cmd *exec.Cmd) error {
	_ = cmd // Windows no-op：无优雅组终止信号
	return nil
}
