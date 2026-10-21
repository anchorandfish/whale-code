package diff

import (
	"math"
	"strings"

	"github.com/sergi/go-diff/diffmatchpatch"
)

// splitLines 按换行符分割文本为行切片，正确处理空文本
// 空文本返回空切片（而非 [""]），确保空文件 diff 正确
func splitLines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

// ComputeLineDiffs 计算行级 diff（基于 Myers O(ND) 算法，M07-05/M23-02 改造）
// 算法层次：公共前缀/后缀修剪 → Myers O(ND) 主体 → 朴素 diff fallback
// 性能：大文件（10K 行）< 100ms；内存 O(N+M)；D 过大时降级为朴素 diff
// 向后兼容：输出格式（LineDiff 类型与文本）与原 Levenshtein 实现一致
type LineDiff struct {
	Type diffmatchpatch.Operation
	Text string
}

// traceMemoryBudget 限制 Myers 回溯 trace 的总 int 槽位数（约 16MB / 8B per int）
// trace 存储 D+1 个 V 快照，每个 V 大小为 2*maxD+1，总量 = (D+1)*(2*maxD+1)
// 当 N+M 较大时，按 sqrt(budget/2) 限制 maxD，避免 O((N+M)^2) 内存爆炸
const traceMemoryBudget = 2_000_000

func ComputeLineDiffs(oldText, newText string) []LineDiff {
	oldLines := splitLines(oldText)
	newLines := splitLines(newText)

	n, m := len(oldLines), len(newLines)

	// 快速路径：双空文本
	if n == 0 && m == 0 {
		return nil
	}
	// 快速路径：旧文件空，全部为新增
	if n == 0 {
		result := make([]LineDiff, 0, m)
		for _, line := range newLines {
			result = append(result, LineDiff{Type: diffmatchpatch.DiffInsert, Text: line})
		}
		return result
	}
	// 快速路径：新文件空，全部为删除
	if m == 0 {
		result := make([]LineDiff, 0, n)
		for _, line := range oldLines {
			result = append(result, LineDiff{Type: diffmatchpatch.DiffDelete, Text: line})
		}
		return result
	}

	// 公共前缀修剪（O(N+M)，显著缩小 Myers 问题规模）
	prefixLen := 0
	minLen := min(n, m)
	for prefixLen < minLen && oldLines[prefixLen] == newLines[prefixLen] {
		prefixLen++
	}

	// 公共后缀修剪（注意不能与前缀重叠）
	suffixLen := 0
	for suffixLen < minLen-prefixLen &&
		oldLines[n-1-suffixLen] == newLines[m-1-suffixLen] {
		suffixLen++
	}

	// 切出中间差异部分
	midOld := oldLines[prefixLen : n-suffixLen]
	midNew := newLines[prefixLen : m-suffixLen]

	// 预分配结果切片：前缀 + 中间 + 后缀
	result := make([]LineDiff, 0, prefixLen+suffixLen+len(midOld)+len(midNew))

	// 前缀（Equal）
	for i := 0; i < prefixLen; i++ {
		result = append(result, LineDiff{Type: diffmatchpatch.DiffEqual, Text: oldLines[i]})
	}

	// 中间部分：Myers O(ND) 主体，失败则降级朴素 diff
	if len(midOld) > 0 && len(midNew) > 0 {
		if midDiffs, ok := myersDiff(midOld, midNew); ok {
			result = append(result, midDiffs...)
		} else {
			result = append(result, naiveDiff(midOld, midNew)...)
		}
	} else if len(midOld) > 0 {
		// 中间仅旧文件有内容：全部删除
		for _, line := range midOld {
			result = append(result, LineDiff{Type: diffmatchpatch.DiffDelete, Text: line})
		}
	} else if len(midNew) > 0 {
		// 中间仅新文件有内容：全部新增
		for _, line := range midNew {
			result = append(result, LineDiff{Type: diffmatchpatch.DiffInsert, Text: line})
		}
	}

	// 后缀（Equal）
	for i := 0; i < suffixLen; i++ {
		result = append(result, LineDiff{Type: diffmatchpatch.DiffEqual, Text: oldLines[n-suffixLen+i]})
	}

	return result
}

// myersDiff 基于 Myers O(ND) 算法计算行级最短编辑脚本（SES）
// 论文：Eugene W. Myers, "An O(ND) Difference Algorithm and Its Variations", 1986
// 时间复杂度 O((N+M)*D)，空间复杂度 O(D*(N+M))（trace 存储）
// 内存保护：当 D 超过 sqrt(traceMemoryBudget/2) 时返回 (nil, false) 触发上层 fallback
// 返回：diff 序列（前向顺序，old→new）；ok=false 表示 D 过大需降级
func myersDiff(oldLines, newLines []string) ([]LineDiff, bool) {
	n := len(oldLines)
	m := len(newLines)
	maxD := n + m

	// 自适应 D 上限：trace 总量 ≈ maxD * (2*maxD) <= budget → maxD <= sqrt(budget/2)
	capD := int(math.Sqrt(float64(traceMemoryBudget) / 2))
	if capD < 1 {
		capD = 1
	}
	if capD > maxD {
		capD = maxD
	}

	// V 数组：v[offset+k] = 对角线 k 上最远到达的 x 坐标
	// k 范围 [-capD, capD]，vSize = 2*capD+1，offset = capD
	vSize := 2*capD + 1
	offset := capD
	v := make([]int, vSize)
	// 初始化 v[offset+1]=0，使得 d=0, k=0 时 x=v[offset+1]=0
	v[offset+1] = 0

	// trace[d] = 第 d 轮迭代前的 V 快照，用于回溯
	trace := make([][]int, 0, capD+1)

	foundD := -1

	for d := 0; d <= capD; d++ {
		// 保存当前 V 快照（深拷贝，回溯时读取）
		vSnap := make([]int, vSize)
		copy(vSnap, v)
		trace = append(trace, vSnap)

		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d {
				// 下移（Insert）：取 k+1 对角线的 x
				x = v[offset+k+1]
			} else if k == d {
				// 右移（Delete）：取 k-1 对角线的 x+1
				x = v[offset+k-1] + 1
			} else if v[offset+k-1] < v[offset+k+1] {
				// 下移（Insert）：k+1 更远
				x = v[offset+k+1]
			} else {
				// 右移（Delete）：k-1 更远
				x = v[offset+k-1] + 1
			}

			y := x - k

			// 沿对角线延伸（相等行）
			for x < n && y < m && oldLines[x] == newLines[y] {
				x++
				y++
			}

			v[offset+k] = x

			// 到达终点：(n, m)
			if x >= n && y >= m {
				foundD = d
				break
			}
		}

		if foundD >= 0 {
			break
		}
	}

	// D 超出预算，触发上层 fallback
	if foundD < 0 {
		return nil, false
	}

	return myersBacktrack(trace, oldLines, newLines, foundD, offset), true
}

// myersBacktrack 从 Myers 正向 trace 回溯构建 diff 序列
// 从终点 (n, m) 逆向追踪每轮 d 的编辑操作（Insert/Delete）和对角线移动（Equal）
// 返回前向顺序的 diff（old→new），可直接被 Render/groupHunks 消费
func myersBacktrack(trace [][]int, oldLines, newLines []string, D int, offset int) []LineDiff {
	n := len(oldLines)
	m := len(newLines)

	result := make([]LineDiff, 0, n+m)
	x, y := n, m

	for d := D; d > 0; d-- {
		v := trace[d] // 第 d 轮迭代前的 V 快照
		k := x - y

		var prevK int
		if k == -d {
			prevK = k + 1
		} else if k == d {
			prevK = k - 1
		} else if v[offset+k-1] < v[offset+k+1] {
			prevK = k + 1
		} else {
			prevK = k - 1
		}

		prevX := v[offset+prevK]
		prevY := prevX - prevK

		// 对角线移动（Equal）：从 (x,y) 回退到 (prevX, prevY) 的后继
		for x > prevX && y > prevY {
			result = append(result, LineDiff{Type: diffmatchpatch.DiffEqual, Text: oldLines[x-1]})
			x--
			y--
		}

		// 编辑操作：Insert（下移）或 Delete（右移）
		if x > prevX {
			// Delete：prevX → prevX+1
			result = append(result, LineDiff{Type: diffmatchpatch.DiffDelete, Text: oldLines[x-1]})
			x--
		} else if y > prevY {
			// Insert：prevY → prevY+1
			result = append(result, LineDiff{Type: diffmatchpatch.DiffInsert, Text: newLines[y-1]})
			y--
		}
	}

	// d=0：剩余对角线移动（从 (0,0) 到 (x,y) 全部为 Equal）
	for x > 0 && y > 0 {
		result = append(result, LineDiff{Type: diffmatchpatch.DiffEqual, Text: oldLines[x-1]})
		x--
		y--
	}

	// 反转：回溯产生的是逆序，需翻转为前向顺序
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// naiveDiff 朴素 diff（fallback）：删除全部旧行 + 插入全部新行
// 当 Myers 因 D 过大降级时使用，保证功能正确性（非最小 diff 但合法）
// 时间 O(N+M)，空间 O(N+M)，满足大文件极端 case 的性能与内存约束
func naiveDiff(oldLines, newLines []string) []LineDiff {
	result := make([]LineDiff, 0, len(oldLines)+len(newLines))
	for _, line := range oldLines {
		result = append(result, LineDiff{Type: diffmatchpatch.DiffDelete, Text: line})
	}
	for _, line := range newLines {
		result = append(result, LineDiff{Type: diffmatchpatch.DiffInsert, Text: line})
	}
	return result
}

// ComputeDiffs 用 sergi/go-diff 计算字符级 diff（Myers 算法）
func ComputeDiffs(oldText, newText string) []diffmatchpatch.Diff {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMainRunes([]rune(oldText), []rune(newText), true)
	diffs = dmp.DiffCleanupSemantic(diffs)
	return diffs
}

// ComputePatch 用 sergi/go-diff 计算行级 patch
func ComputePatch(change FileChange) []diffmatchpatch.Patch {
	dmp := diffmatchpatch.New()
	oldText := change.OldContent
	newText := change.NewContent

	diffs := dmp.DiffMainRunes([]rune(oldText), []rune(newText), true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	return dmp.PatchMake(oldText, diffs)
}

// ComputeWordDiff 计算词级 diff
func ComputeWordDiff(oldText, newText string) []diffmatchpatch.Diff {
	if oldText == "" || newText == "" {
		return nil
	}

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMainRunes([]rune(oldText), []rune(newText), true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	return diffs
}
