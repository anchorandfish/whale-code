package tools

import (
	"reflect"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// L1 守卫 —— 权限档位单一真相源（I1）
// ============================================================================
//
// 背景（2026-09-14 发现，缺陷 F5）：仓库里「某工具需要哪一档权限」这个事实曾有**两份
// 互不相通**的定义：
//
//	A. internal/tools/tools.go —— 53 个 Spec 的 RequiredPermission 字段
//	B. internal/permission/permission.go:700 defaultToolRequirements()（另一张手写表）
//
// 两个 Level 是**同名但不同类型的枚举**，全仓**没有任何转换或桥接**（已核：对
// spec.RequiredPermission 的读取仅存在于 *_test.go）。A 只被测试读，生产从不读；
// 生产真正生效的是 B。⇒ 漂移不是意外，是结构必然。
//
// 实测代价：registry 声明只读的 33 个工具里，13 个在只读档被**误拒**
// （git_diff / Task / TaskList / CronList / Sleep / SendMessage / export_session /
// structured_output / config_get / user_choice / Lsp{Implementation,TypeDefinition,DocumentSymbols}）。
//
// 修复（L1，2026-09-14）：删掉 B（`defaultToolRequirements`），把 A 定为唯一真相源，
// permission 经注入的目录来源查询（permission/level.go + tools/toollevel.go），
// 并把 `tools.Level` 改成 `permission.Level` 的**类型别名**（不可能再有第二套枚举）。

// TestGuard_S4_SSOT_ToolLevelIsSingleSource 断言「声明档位 ↔ 生效档位」双向一致。
//
// 这是 L1 的**判据门**（原 TestProbe_S4_SSOT_ToolLevelDrift，修复前刻意红，现已绿）：
// - registry 声明 LevelReadOnly 的工具，只读档下必须 allowed=true；
// - 声明 LevelWorkspaceWrite 的工具，只读档下必须 allowed=false（反向漂移）；
// - 两个入口（直接构造 + 生产装配入口 NewPolicyFromModeName）结论必须一致 ——
// 若不一致，说明还存在第三条判定路径，本身即缺陷。
//
// ⚠️ 本门不比较字段（那是实现细节），只断言**生产语义**。
func TestGuard_S4_SSOT_ToolLevelIsSingleSource(t *testing.T) {
	reg := NewRegistry()

	entries := []struct {
		label  string
		policy *permission.Policy
	}{
		{"NewPolicy(LevelReadOnly)", permission.NewPolicy(permission.LevelReadOnly)},
		{`NewPolicyFromModeName("read-only")`, permission.NewPolicyFromModeName("read-only")},
	}

	results := make(map[string][]string) // label -> 漂移工具名
	roTotal, wwTotal := 0, 0
	wwViolations := 0

	for _, e := range entries {
		for _, spec := range reg.All() {
			if spec.Name == "Agent" {
				continue // Agent 按入参 agent_type 定级（requiredForTool 特例），非本门对象
			}
			allowed, _ := e.policy.Check(spec.Name, `{}`)
			switch spec.RequiredPermission {
			case LevelReadOnly:
				if e.label == entries[0].label {
					roTotal++
				}
				if !allowed {
					results[e.label] = append(results[e.label], spec.Name)
				}
			case LevelWorkspaceWrite:
				if e.label == entries[0].label {
					wwTotal++
				}
				if allowed {
					wwViolations++
					t.Errorf("反向漂移：%s 声明需写，只读档却 allowed=true（%s）", spec.Name, e.label)
				}
			}
		}
	}

	var first []string
	for _, e := range entries {
		got := results[e.label]
		t.Logf("entry=%s  声明只读却被拒 %d 个", e.label, len(got))
		if first == nil {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Errorf("两个入口结论不一致：%d vs %d —— 存在第三条判定路径", len(first), len(got))
		}
	}

	if len(first) == 0 && wwViolations == 0 {
		t.Logf("✅ 声明档位与生效档位一致（L1 单一真相源成立）：只读 %d 个 / 需写 %d 个", roTotal, wwTotal)
		return
	}
	for _, n := range first {
		t.Logf("  ❌ %-20s 声明只读 → 只读档 allowed=false", n)
	}
	t.Errorf("权限级别单一真相源缺口：只读被误拒 %d 个 / 需写被误放行 %d 个", len(first), wwViolations)
}

// TestGuard_ToolLevelTypesAreUnified 钉住漂移的**结构性成因已消除**：
// tools.Level 必须与 permission.Level 是同一个类型（别名），不得退回两套同名枚举。
//
// 判别力：若有人在 tools 包重新定义 `type Level int`，reflect 比较立刻红。
func TestGuard_ToolLevelTypesAreUnified(t *testing.T) {
	if got, want := reflect.TypeOf(LevelReadOnly), reflect.TypeOf(permission.LevelReadOnly); got != want {
		t.Errorf("tools 侧的 Level 与 permission.Level 不再是同一类型：%v != %v —— 又一次双枚举漂移", got, want)
	}
	// 同一组常量必须数值一致（别名下这是恒等式，保留以钉住「序号同序」这一前提）。
	pairs := []struct {
		name string
		tl   Level
		pl   permission.Level
	}{
		{"ReadOnly", LevelReadOnly, permission.LevelReadOnly},
		{"WorkspaceWrite", LevelWorkspaceWrite, permission.LevelWorkspaceWrite},
		{"DangerFullAccess", LevelDangerFullAccess, permission.LevelDangerFullAccess},
	}
	for _, p := range pairs {
		if p.tl != p.pl {
			t.Errorf("%s 常量不一致：tools=%v permission=%v", p.name, p.tl, p.pl)
		}
	}
}

// TestGuard_EveryRegisteredToolHasLevel 是**防将来再漂移的关键门**：
// 注册表里的每一个工具都必须在档位目录中可解析，且解析值 == 它的声明值。
//
// 判别力：未登记的工具会被 RequiredLevelOf fail-closed 到最高档（行为与声明不符），
// 本门直接点名；把 tools/toollevel.go 的 init 注入删掉，本门也会红（见下一条）。
func TestGuard_EveryRegisteredToolHasLevel(t *testing.T) {
	reg := NewRegistry()
	all := reg.All()
	if len(all) == 0 {
		t.Fatal("注册表为空，本门无意义")
	}
	for _, spec := range all {
		lvl, ok := permission.LevelOf(spec.Name)
		if !ok {
			t.Errorf("工具 %q 在档位目录中不可解析 —— 权限层会 fail-closed 到最高档，行为与声明不符", spec.Name)
			continue
		}
		if lvl != spec.RequiredPermission {
			t.Errorf("工具 %q 目录档位 = %v，声明 = %v —— 又出现了第二处定义", spec.Name, lvl, spec.RequiredPermission)
		}
	}

	// 未登记的工具必须返回 ok=false（否则 fail-closed 无从谈起）。
	if _, ok := permission.LevelOf("__definitely_not_a_tool__"); ok {
		t.Error("未登记工具竟能在目录中解析 —— fail-closed 兜底会失效")
	}
	t.Logf("档位目录覆盖 %d 个已注册工具", len(all))
}

// TestGuard_ToolLevelSourceIsWired 断言「目录来源已注入」。
//
// 这条不是形式检查：RequiredLevelOf 在**来源未注入**时会退回历史默认 LevelWorkspaceWrite
// （为了不污染纯权限包单测）。因此在生产二进制里，来源必须已注入 —— 否则整个 L1
// 会静默退化回「第二张表」的行为，而没有任何症状。
func TestGuard_ToolLevelSourceIsWired(t *testing.T) {
	if !permission.ToolLevelSourceRegistered() {
		t.Fatal("permission 未收到工具档位目录来源（tools.init 注入失效）—— L1 已静默退化")
	}
	if lvl, ok := permission.LevelOf("bash"); !ok || lvl != LevelWorkspaceWrite {
		t.Errorf("bash 档位应为 %v（已声明），得到 %v ok=%v", LevelWorkspaceWrite, lvl, ok)
	}
}

// TestGuard_ChildHiddenToolsAreRegistered 断言角色排除表里没有幽灵条目。
//
// childHiddenTools 是「不向子 agent 暴露的顶层编排 / 会话治理面工具」的唯一排除点。
// 工具被删除/改名后若残留在表里，排除规则会静默失效于真实工具（同时误导读者）——
// `PowerShell` 正是这种幽灵的历史实例（它从未注册，却长期留在 permission 的
// 硬编码识别名单里，见 permission/checkwithrules_test.go）。
func TestGuard_ChildHiddenToolsAreRegistered(t *testing.T) {
	reg := NewRegistry()
	for name := range childHiddenTools {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("childHiddenTools 含未注册工具 %q —— 幽灵条目（请随工具变更同步）", name)
		}
	}
}
