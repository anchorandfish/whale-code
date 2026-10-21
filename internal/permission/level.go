package permission

import (
	"log"
	"sync"
)

// ============================================================================
// L1 · 权限档位单一真相源（R1 · 2026-09-14）
// ============================================================================
//
// 背景（缺陷 F5）：仓库里「某工具需要哪一档权限」这个事实曾有两份**互不相通**的定义：
//
//	A. internal/tools/tools.go —— 53 个 Spec 的 RequiredPermission（类型 tools.Level）
//	B. internal/permission/permission.go defaultToolRequirements()（类型 permission.Level）
//
// 两者同名不同型、全仓零转换，且 A 只被 *_test.go 读 —— 于是漂移成了**结构必然**：
// 实测 13 个「声明只读」的工具在只读档被误拒，`bash` 的声明档与生效档也不一致。
//
// 修法（本文件）：把「唯一真相源」定为 **工具注册表里的声明**（A），
// permission 包不再持有任何工具档位表，改为**向注入的目录来源查询**。
//
// 为什么是注入而不是直接引用：`internal/tools` 已 import `internal/permission`
// （用 Level 常量），反向 import 会成环。因此由 tools 包在 `init()` 里调用
// `RegisterToolLevelSource` 把查询函数注入进来（见 internal/tools/toollevel.go）。
//
// 不变量（I1）：一个工具的档位全仓只有一处定义（Spec.RequiredPermission），
// 其余（权限判定、只读工具面、审批危险判定）全部**派生**。
// 守卫：internal/tools/level_ssot_guard_test.go。

// toolLevelSource 是「工具名 → 声明档位」的目录查询函数，由 internal/tools 注入。
// 返回 ok=false 表示目录里没有这个工具（未登记）。
var (
	toolLevelMu     sync.RWMutex
	toolLevelSource func(tool string) (Level, bool)

	// unknownToolWarned 记录已告警过的未登记工具，避免每次判定都刷日志。
	unknownToolWarnedMu sync.Mutex
	unknownToolWarned   = map[string]bool{}
)

// RegisterToolLevelSource 注入「工具 → 档位」目录来源（唯一真相源）。
//
// 由 internal/tools 包在 init() 中调用一次。幂等：重复注入以最后一次为准
// （测试可用它替换为受控目录，用完应恢复）。
func RegisterToolLevelSource(fn func(tool string) (Level, bool)) {
	toolLevelMu.Lock()
	defer toolLevelMu.Unlock()
	toolLevelSource = fn
}

// ToolLevelSourceRegistered 报告目录来源是否已注入。
//
// 用于区分两种「查不到」：
// - 目录**未装配** —— 该二进制未链接 internal/tools（如纯权限包单测）；
// - 目录已装配但**没有这个工具** —— 真的未登记（新增工具忘了定级 / 拼写错）。
//
// 两者处置不同，详见 RequiredLevelOf。
func ToolLevelSourceRegistered() bool {
	toolLevelMu.RLock()
	defer toolLevelMu.RUnlock()
	return toolLevelSource != nil
}

// LevelOf 查询某工具**声明的**档位（唯一真相源）。ok=false 表示未登记。
//
// 与 RequiredLevelOf 的区别：本函数不做任何兜底，供守卫测试与「按档位派生工具面」使用。
func LevelOf(tool string) (Level, bool) {
	toolLevelMu.RLock()
	fn := toolLevelSource
	toolLevelMu.RUnlock()
	if fn == nil {
		return LevelReadOnly, false
	}
	return fn(tool)
}

// RequiredLevelOf 返回一次工具调用**实际需要**的档位（L1 之后唯一入口）。
//
// 三档语义（2026-09-14 用户拍板）：
// 1. 目录已登记 → 用**声明的**档位（单一真相源，不再有第二张手写表）；
// 2. 目录已装配但该工具**未登记** → **fail-closed 到最高档** + 告警一次。
// 理由：新增一个未分类工具若默认降级为「需确认」，就等于开了一个 deny 逃逸口
// （「声明过严」比「静默放行」危险得多）。宁可拒绝并要求补登记。
// 3. 目录**未装配** → 退回历史默认 LevelWorkspaceWrite。
// 该分支只在未链接 internal/tools 的环境（纯权限包单测）可达；生产必装配
// （internal/tui 两个装配点都会构造 tools.NewRegistry，其 init 已注入来源）。
// 守卫 TestGuard_ToolLevelSourceIsWired 断言生产包确实注入了来源。
func RequiredLevelOf(tool string) Level {
	if lvl, ok := LevelOf(tool); ok {
		return lvl
	}
	if !ToolLevelSourceRegistered() {
		return LevelWorkspaceWrite
	}
	warnUnknownToolOnce(tool)
	return LevelDangerFullAccess
}

// warnUnknownToolOnce 对未登记工具告警一次（每工具名最多一条）。
func warnUnknownToolOnce(tool string) {
	unknownToolWarnedMu.Lock()
	already := unknownToolWarned[tool]
	if !already {
		unknownToolWarned[tool] = true
	}
	unknownToolWarnedMu.Unlock()
	if already {
		return
	}
	log.Printf("[permission] WARN: 工具 %q 未在工具注册表中定级，按最高档 fail-closed 处理（"+
		"请在该工具的 Spec.RequiredPermission 显式定级，见 docs/review0909/子agent_FIX_PLAN.md §4 L1）", tool)
}
