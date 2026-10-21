// archive.go — G-B（O-B）E3：归档式 GC（不硬删）。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md §2 E3 行（S18-G1）：
// `ApplyForgetting` 只软标记不删 ⇒ 磁盘只增不减
// ⇒ **引入归档式 GC（不硬删）+ 持久化访问热度**。
//
// 设计取向（为什么是「归档」而不是「删除」）：
//   - 记忆条目是人类知识的浓缩，误判（verification/时间锚算错）的代价远高于磁盘；
//   - 归档件保留完整 frontmatter + body，出错可人工/工具搬回活动目录（可逆清理）；
//   - 「清理」= 从**活动目录**移出（不再进 prompt、不再参与检索），
//     与「删除」在语义上分离 —— 本轮只做前者。
//
// 归档判定必须是**可判定的结构化事实**（不是「扫描到就删」）：
//
//	verification == contradicted  ∧  失效时间锚距现在超过 ArchiveGracePeriod
//
// 宽限期存在的意义：给「刚被误判失效」留出人工纠正窗口，避免一次扫描即永久移出。
//
// 落盘布局：<scopeDir>/_archive/<原文件名>[.N]
//   - 位于子目录 ⇒ List()/countFiles/scanOnce 均跳过目录，归档件不会被当成活动记忆；
//   - 目录 0700 / 归档件保留原 0600 权限（权限卫生与全包一致，见 S18-G4）。
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// memoryArchiveDirName 归档目录名（scopeDir 下的子目录）。
const memoryArchiveDirName = "_archive"

// ArchiveGracePeriod 失效记忆被归档前必须经过的宽限期。
//
// 为什么是 7 天：与 EbbinghausTau 同量级（τ=7 天）。失效（contradicted）后先静置一个
// 衰减常数的时间，让「误判」有机会被用户/工具纠正；超过则说明它确实已无价值。
// 这是**策略数值**：=0 表示立即归档（显式关闭宽限），负数按 0 处理。
var ArchiveGracePeriod = 7 * 24 * time.Hour

// archiveFile 把 srcPath 移入 <sourceDir>/_archive/（**移动而非删除**）。
//
// 同名冲突（同一记忆被多次归档）时追加序号，绝不覆盖历史归档件。
// 返回归档后的完整路径。
func archiveFile(sourceDir, srcPath string) (string, error) {
	if sourceDir == "" || srcPath == "" {
		return "", fmt.Errorf("archiveFile: empty path (dir=%q src=%q)", sourceDir, srcPath)
	}
	dir := filepath.Join(sourceDir, memoryArchiveDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("archive: mkdir %s: %w", dir, err)
	}

	base := filepath.Base(srcPath)
	dst := filepath.Join(dir, base)
	for i := 1; ; i++ {
		_, statErr := os.Stat(dst)
		if statErr == nil {
			dst = filepath.Join(dir, fmt.Sprintf("%s.%d", base, i))
			continue
		}
		if !os.IsNotExist(statErr) {
			return "", fmt.Errorf("archive: stat %s: %w", dst, statErr)
		}
		break
	}

	if err := os.Rename(srcPath, dst); err != nil {
		if os.IsNotExist(err) {
			// 源已被并发清理者移走：归档语义已达成，不视为失败。
			return dst, nil
		}
		return "", fmt.Errorf("archive: move %s -> %s: %w", srcPath, dst, err)
	}
	return dst, nil
}

// shouldArchiveEntry 判定一条记忆是否应被归档（结构化判据，无散文关键词）。
//
// 条件（全部满足）：
//  1. verification == contradicted（明确失效）；
//  2. 失效时间锚距 now 超过 ArchiveGracePeriod。
//
// 时间锚优先级：ValidUntil（失效动作记录的时刻）→ UpdatedAt → ValidFrom/CreatedAt。
// 全部缺失时返回 false：无证据不下「该归档」的结论（与 ApplyForgetting 的
// fail-safe 取向一致：宁可多留，不可误移）。
func shouldArchiveEntry(entry *MemoryEntry, now time.Time) bool {
	if entry == nil || entry.DefaultVerification() != VerificationContradicted {
		return false
	}
	anchor, ok := archiveAnchor(entry)
	if !ok {
		return false
	}
	grace := ArchiveGracePeriod
	if grace < 0 {
		grace = 0
	}
	return now.Sub(anchor) > grace
}

// archiveAnchor 返回归档判定使用的时间锚。
func archiveAnchor(entry *MemoryEntry) (time.Time, bool) {
	if entry.ValidUntil != nil && !entry.ValidUntil.IsZero() {
		return *entry.ValidUntil, true
	}
	if !entry.UpdatedAt.IsZero() {
		return entry.UpdatedAt, true
	}
	if t := entry.ValidFromOrCreatedAt(); !t.IsZero() {
		return t, true
	}
	return time.Time{}, false
}
