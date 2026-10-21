// Package sandbox — 「本次生效的可写根」的**唯一权威声明**（T1 /）。
//
// 为什么必须有这一层：同一个路径此前有两套边界 ——
// - 审批面（internal/tools 的 PermissionStage）按 `workDir` 单值判「是否逃逸工作区」；
// - 强制面（seatbelt profile 的 ExtraWriteDirs）按「工作区 ＋ 临时目录 ＋ 用户缓存」判。
//
// 于是同一个写入在**两个地方得到两个理由**：审批说"你逃逸了工作区"，
// 而沙箱其实放得行；或者反过来。用户看到的是"到底哪个才算边界"。
//
// 收敛方式（已拍：由 internal/sandbox 导出唯一权威）：
// - 强制面剩下的那份逻辑（`defaultDarwinProfileOptions` 的 ExtraWriteDirs）
// **改成本文件的调用方**，不再自己拼；
// - 审批面改为消费 `WritableRoots`。
//
// ⇒ 改 `TMPDIR` / `os.UserCacheDir()` 时，**两边同变**（这是 T1 的判别器：
// 反例是"审批面自己再拼一份 {workDir, TempDir, caches}"，那样两边会各自漂移）。
//
// ★ 语义边界（刻意不做的）：可写根**不**等于"免问根"。审批面里
//
//	"工作区内的写 ⇒ 免问"仍然只认工作区（见 tools.workspaceContainedWrite）；
//	可写根用于判定"沙箱**必拒**"（证明目标落在可写根之外，因此调用根本跑不成）。
//	把两者混同会把 `/tmp` 变成免问根 —— 那是放大豁免，不是收窄。
package sandbox

import (
	"os"
	"strings"
)

// ExtraWritableRoots 返回 workspace-write 姿态下「除工作区之外」的额外可写根
// —— 系统临时目录 ＋ 用户缓存目录（GOCACHE 等构建缓存位于 ~/Library/Caches，
// 不放开会导致 `go build` 等常规命令不可用）。
//
// ★ SSOT：seatbelt profile（`defaultDarwinProfileOptions`）与审批面的三值判据
// 都必须消费本函数，不得各自拼一份。
//
// 路径已解析符号链接（`/tmp` → `/private/tmp`），与沙箱内核匹配口径一致；
// 解析失败（目录尚不存在）的项直接跳过，不返回未解析字面量。
func ExtraWritableRoots() []string {
	var out []string
	if tmp, err := resolveSandboxPath(os.TempDir()); err == nil && tmp != "" {
		out = append(out, tmp)
	}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		if resolved, rerr := resolveSandboxPath(cache); rerr == nil && resolved != "" {
			out = append(out, resolved)
		}
	}
	return out
}

// WritableRoots 返回本次会话**沙箱实际允许写入**的根集合
// （工作区 ＋ ExtraWritableRoots），已去重、已解析符号链接。
//
// 这是「沙箱会放行哪些写入」的**唯一权威**。调用方（审批面）用它判定
// 「能否**证明**这次调用必被沙箱拒」（目标落在整个集合之外）。
//
// workspace 为空时只返回额外根 —— 这与 seatbelt 侧的 fail-closed 行为一致
// （workspace 为空时 profile 生成会直接报错，不会静默放开）。
func WritableRoots(workspace string) []string {
	roots := make([]string, 0, 3)
	if workspace != "" {
		if ws, err := resolveSandboxPath(workspace); err == nil && ws != "" {
			roots = append(roots, ws)
		}
	}
	roots = append(roots, ExtraWritableRoots()...)
	return dedupeRoots(roots)
}

// dedupeRoots 去重（保持首次出现顺序），并丢掉空串。
func dedupeRoots(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// RootsReason 渲染可写根集合的人类可读理由（审批面与沙箱侧共用同一句）。
//
// 为什么共用渲染而不是各自 fmt.Sprintf： 的失效形态是
// 「同一路径给用户两个理由」⇒ 理由必须由**持有事实的那一层**生成，
// 消费者只引用，不得自造措辞（否则措辞漂移又是一处影子判据）。
func RootsReason(roots []string) string {
	if len(roots) == 0 {
		return "writable roots: unknown"
	}
	return "writable roots: " + strings.Join(roots, ", ")
}
