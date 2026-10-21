package tools

import (
	"sync"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// L1 · 工具档位目录（权限级别的唯一真相源出口）
// ============================================================================
//
// 「某个工具需要哪一档权限」的**唯一真相源**是每个 Spec 的 RequiredPermission 声明
// （tools.go / lsp_mcp_notebook.go / executor.go 的插件注册点）。
//
// permission 包需要这个事实，但它不能 import tools（tools 已 import permission，
// 反向会成环）。因此本文件把查询函数**注入**给 permission（见 permission/level.go），
// 并在 init() 里建立内置工具快照 —— 使档位查询不受「谁先构造 Registry」这种
// 实现细节影响（否则语义会依赖调用顺序，那是另一种脆弱）。

var (
	levelCatalogMu sync.RWMutex

	// levelCatalog 内置工具的档位快照，init() 建立，此后只读。
	levelCatalog = map[string]permission.Level{}

	// liveRegistries 已构造的注册表（后加入者优先）。
	// 用于解析**动态注册**的工具：插件、MCP，以及包内测试注入的自定义 spec。
	liveRegistries []*Registry
)

// LookupToolLevel 是注入给 permission 包的目录查询函数。
//
// 返回 ok=false 表示目录里没有这个工具（未登记）—— 由 permission.RequiredLevelOf
// 决定兜底策略（fail-closed 到最高档 + 告警）。
func LookupToolLevel(tool string) (permission.Level, bool) {
	levelCatalogMu.RLock()
	defer levelCatalogMu.RUnlock()

	if lvl, ok := levelCatalog[tool]; ok {
		return lvl, true
	}
	for i := len(liveRegistries) - 1; i >= 0; i-- {
		if spec, ok := liveRegistries[i].lookupSpec(tool); ok {
			return spec.RequiredPermission, true
		}
	}
	return permission.LevelReadOnly, false
}

// toolLevelsSnapshot 汇总「目录已知的全部工具 → 档位」。
//
// 顺序：内置快照打底，再叠加每个活注册表（后构造者覆盖先构造者）。
// 供「按档位派生工具面」使用（agent.go 的角色工具集）。
//
// ★ S3（裁定 C=②）：出口处按 toolAvailable() —— **与注册点共用同一谓词** —— 过滤。
//
// 为什么必须在这一层补一刀（而不是靠"不注册就自动消失"）：
//
//	levelCatalog 是 **init()** 期由 newBareRegistry().registerBuiltinTools() 建立的静态快照
//	（toollevel.go init()），而那早于 main.go 的 loadEnvFiles()；且下面这个合并循环
//	**只增不减** ⇒ 快照里的名字永远抹不掉（§11.1 实测结论，也是一条已登记的横切问题）。
//
// 所以这里过滤的是**存在性**（能力面），不是档位：档位仍是静态事实，本函数只多一层
// "这个工具此刻到底存不存在"。过滤后 getAllowedToolSet() 与 roleToolSet() 同源，
// g6 的公式 intersectToolSet(roleToolSet(role), parent) 依旧成立（判据 T9 因此不破）。
func toolLevelsSnapshot() map[string]permission.Level {
	levelCatalogMu.RLock()
	defer levelCatalogMu.RUnlock()

	out := make(map[string]permission.Level, len(levelCatalog)+len(liveRegistries)*4)
	for name, lvl := range levelCatalog {
		out[name] = lvl
	}
	for _, r := range liveRegistries {
		r.mu.RLock()
		for name, spec := range r.builtin {
			out[name] = spec.RequiredPermission
		}
		for name, spec := range r.plugin {
			out[name] = spec.RequiredPermission
		}
		for name, spec := range r.mcp {
			out[name] = spec.RequiredPermission
		}
		r.mu.RUnlock()
	}

	// 能力面过滤（唯一谓词）。逐名判定而不是在内层循环里判：两个来源（静态快照 / 活注册表）
	// 用的是同一份判定，避免"某一层忘了判"。
	for name := range out {
		if !toolAvailable(name) {
			delete(out, name)
		}
	}
	return out
}

// toolNamesWithLevel 返回目录中声明为指定档位的工具名集合。
func toolNamesWithLevel(lvl permission.Level) map[string]bool {
	all := toolLevelsSnapshot()
	out := make(map[string]bool, len(all))
	for name, l := range all {
		if l == lvl {
			out[name] = true
		}
	}
	return out
}

// allToolNames 返回目录已知的全部工具名（= 全量工具面）。
func allToolNames() map[string]bool {
	all := toolLevelsSnapshot()
	out := make(map[string]bool, len(all))
	for name := range all {
		out[name] = true
	}
	return out
}

// registerLiveRegistry 把新构造的注册表纳入目录可见范围。
// 由 NewRegistry 调用（含包内测试直接构造的注册表 —— 它们注入的自定义 spec 因此可解析）。
func registerLiveRegistry(r *Registry) {
	levelCatalogMu.Lock()
	defer levelCatalogMu.Unlock()
	liveRegistries = append(liveRegistries, r)
}

// lookupSpec 在单个注册表内查找 spec（不登记目录），供 LookupToolLevel 复用。
func (r *Registry) lookupSpec(name string) (Spec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if spec, ok := r.builtin[name]; ok {
		return spec, true
	}
	if spec, ok := r.plugin[name]; ok {
		return spec, true
	}
	if spec, ok := r.mcp[name]; ok {
		return spec, true
	}
	return Spec{}, false
}

// newBareRegistry 构造一个空注册表骨架（**不**登记进目录）。
// 仅用于 init() 建立内置档位快照。
func newBareRegistry() *Registry {
	return &Registry{
		builtin: make(map[string]Spec),
		mcp:     make(map[string]Spec),
		plugin:  make(map[string]Spec),
	}
}

func init() {
	// 内置工具档位快照。不构造完整 Registry（那会把骨架登记进目录）。
	snapshot := newBareRegistry()
	snapshot.registerBuiltinTools()

	snapshot.mu.RLock()
	for name, spec := range snapshot.builtin {
		levelCatalog[name] = spec.RequiredPermission
	}
	snapshot.mu.RUnlock()

	permission.RegisterToolLevelSource(LookupToolLevel)
}
