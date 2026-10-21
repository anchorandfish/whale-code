// liveness.go — 记忆有效性判定唯一权威（P2 接线，2026-10-08）。
//
// ## 为什么单独成一个声明点
//
// 「这条记忆现在还算不算数」是**一个语义**。读路径（注入 system prompt）必须问它；
// 未来任何其它消费者（检索工具、面板展示）也必须问它。
// 本仓已有两次教训（命令双实现、G5–G7 工具面双源）：同一语义两处实现必然漂移，
// 漂移后「改一处还漏一处」。因此这里只声明一次，调用方不得复制条件。
//
// ## 判据来自哪里（不是拍脑袋）
//
// memory 包内**所有**让一条记忆失效的路径，最终都落在两个结构化字段上：
//
// 1. verification = contradicted（终态）
// - /memory invalidate（invalidate.go:TransitionVerification 写 contradicted + ValidUntil=now）
// - ProbeFn 连续 3 次失败（同上）
// - Ebbinghaus 衰减致死（forgetting.go ApplyForgetting 的 default 分支写 contradicted + ValidUntil）
// 2. valid_until 已过
// - 上面两条路径都会顺带设置；也覆盖未来「只设时间不设状态」的路径（防御性）
//
// 因此判定这两个字段即可覆盖当前全部失效来源，不需要（也不应该）去问「谁把它弄失效的」。
package memory

import "time"

// LiveAt 判定一条记忆在给定时刻是否「存活」（应参与读路径注入）。
//
// 失效条件（任一命中即失效）：
// - verification == contradicted（空串按 unverified 处理，不算失效）
// - valid_until 非 nil 且 now >= valid_until
//
// 参数 now 由调用方显式传入（而非函数内 time.Now()），使判定可测试、可复现
// （判据不得依赖「运行时刻」这个不可控输入）。
func (e *MemoryEntry) LiveAt(now time.Time) bool {
	if e == nil {
		return false
	}
	if e.DefaultVerification() == VerificationContradicted {
		return false
	}
	if e.ValidUntil != nil && !now.Before(*e.ValidUntil) {
		return false
	}
	return true
}
