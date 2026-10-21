// Package util — 跨平台路径比较。
//
// ★ 本文件只提供 PathsEqual 一个入口，其**机制全部委托 platform**：
//
//	规范化 → platform.NormalizePath（null 字节防护 + ~ 展开 + EvalSymlinks 优先 + Abs + Clean + NFC）
//	等价判定 → platform.PathEqual（折叠大小写**当且仅当**文件系统大小写不敏感）
//
// 收敛背景：此前本包自带一份 `normalizePath`（Abs + EvalSymlinks + Clean
// + 按 `runtime.GOOS` 折叠大小写），与 platform.NormalizePath / platform.PathEqual
// 构成三份**各自实现**的路径等价逻辑。三者对"什么时候折叠大小写"的判据并不一致
// （GOOS 硬编码 vs CaseInsensitiveFS()——后者尊重 WHALE_CASE_SENSITIVE 覆盖）。
//
// 不依赖 os.SameFile（Windows 上对 rename 检测不可靠，见 session/bug_test.go:34）。
package util

import (
	"github.com/anchorandfish/whale-code/internal/platform"
)

// PathsEqual 跨平台比较两个路径是否指向**同一文件系统对象**（会解析 symlink）。
//
// 语义（收敛后与收敛前一致，只是机制改为共享）：
// 1. 两条路径都走 platform.NormalizePath（EvalSymlinks 优先，失败降级 Abs+Clean）；
// 2. 规范化失败时**退回原串**（不把"无法规范化"当成"不相等"）；
// 3. 用 platform.PathEqual 比较（大小写折叠跟随文件系统语义）。
//
// 与 platform.PathEqual 的区别是**策略**而非机制：本函数先解析 symlink
// （"指向同一对象"），PathEqual 只看规范化后的字符串（"字面等价"，性能优先）。
func PathsEqual(p1, p2 string) bool {
	n1, err1 := platform.NormalizePath(p1)
	if err1 != nil {
		n1 = p1
	}
	n2, err2 := platform.NormalizePath(p2)
	if err2 != nil {
		n2 = p2
	}
	return platform.PathEqual(n1, n2)
}
