// Package daemon — 鉴权单一强制点（I1）。
//
// PCR 域审计（G-P1-1 / Q3）将此前【4 份】独立鉴权实现收敛为唯一一个常量时间
// 比较原语 authorize：
//
//	① IPCServer.authenticateRequest   (ipc.go,                  文件轮询传输)
//	② UDSServer.authenticate          (uds_transport.go,        !windows)
//	③ UDSServer.authenticate          (uds_transport_windows.go, windows)
//	④ Daemon.withAuth                 (metrics_server.go,       HTTP /metrics,/health)
//
// ②③ 是同一逻辑的 OS 分体副本（build tag !windows / windows）：任一时刻只有一份
// 参与编译，因此二者之间的漂移【无法被单平台测试发现】——这是收敛的最强动因。
//
// 对标同类 CLI：所有传输共用单一 auth.rs 模块的常量时间比较（constant_time_eq_32），
// 而非每套传输各写一份（见 PCR_STATUS.md §1 A2）。
//
// 边界说明：本函数【只】负责"令牌比较"这一件事。各传输的"空 token 策略"属于
// 传输层策略，仍留在各自调用点，并分别由 characterization 测试钉住：
//   - IPC / UDS：空 token → 拒绝（I2 fail-closed）
//   - metrics  ：空 token → 回环受信直通（Q1=A，由 startMetricsServer 强制回环兜底）
package daemon

import "crypto/subtle"

// authorize 是所有 daemon RPC 传输共用的令牌校验原语（I1 单一强制点）。
//
// 契约（I2 fail-closed）：
//   - expectedToken 为空 → 拒绝（初始化异常 / 被绕过）
//   - 长度不匹配         → 拒绝（同时避免 ConstantTimeCompare 提前返回泄露长度）
//   - 否则               → 常量时间比较
//
// 调用方必须传入各自传输的【已配置】期望令牌，不得预先归一化或绕过本函数。
func authorize(expectedToken, providedToken string) bool {
	if expectedToken == "" {
		return false
	}
	if len(providedToken) != len(expectedToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(providedToken), []byte(expectedToken)) == 1
}
