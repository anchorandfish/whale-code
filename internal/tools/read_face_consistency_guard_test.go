package tools

// read_face_consistency_guard_test.go — R2b：**跨维度一致性守卫**（L1 档位 × R2 能力）。
//
// 背景：仓库里有两条各自独立的真相源，描述同一个工具：
//   - **L1**（`Spec.RequiredPermission`，经 `permission.LevelOf` 查询）——「这个工具**需要哪一档**权限」；
//   - **R2**（`internal/permission/capability.go`）——「这个工具**会做什么**」。
//
// 两者必须相容，否则就是**既有不一致**：一个工具声明「只读档就够」（LevelReadOnly），
// 能力上却被判为「会写盘」（Edit）或「会跑命令」（Exec）—— 那么只读档用户**跑不动它**，
// 或者（更糟）它被放进只读工具面却真的产生了副作用。
//
// 本守卫就是把这组矛盾**变成编译期之后的第一道红灯**：
//
//	声明 LevelReadOnly 的工具，能力**不得**含 Edit / Exec。
//
// 为什么这能抓住真问题：R2a 分桶时实测出 `export_session` 声明 `LevelReadOnly` 却
// 实际写 JSON 文件到磁盘（`capability.go:95` 判为 `Edit`）—— 正是本守卫要拦的形态。
// **R1（A 会话）已把它的声明改为 `LevelWorkspaceWrite`**（`tools.go:943`），本守卫因此转绿；
// 若谁把它改回 `LevelReadOnly`，本守卫立刻红 —— 这就是它存在的意义。
//
// ⚠️ **本守卫不覆盖**（有意，别误以为已被覆盖）：
//   - `Task` / `Agent`：声明 `LevelReadOnly`、能力 `Meta`，但会**派生子代理**。
//     能力上 Meta 不假（它不直接写盘），风险在「委派是否扩权」—— 那是 **L2** 的
//     `mayDelegate()` / 父面下传管的，不是档位-能力一致性。见协调文档 §8.4-②。
//   - 只读**角色**工具面：`agent_type=verification` 是 `readOnly ∪ {bash}`（**显式含 Exec**），
//     这是有意设计（要能跑测试）。判据若套到「角色面」上会误红，故本守卫只认
//     `LevelOf(tool)==LevelReadOnly` 的**声明**，不认角色派生面。见 §8.5。

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// TestGuard_NoReadCapabilityViaExec 见文件头。
func TestGuard_NoReadCapabilityViaExec(t *testing.T) {
	// ---- 前置 0：档位目录必须已注入 ----
	//
	// 这是本守卫**最容易假绿的地方**：若注入没发生，`permission.LevelOf` 对每个工具
	// 都返回 `ok=false`，下面的循环体一次都不执行，测试安然变绿 —— 而它什么都没检查。
	// 所以先把这个退化形态钉死。
	if !permission.ToolLevelSourceRegistered() {
		t.Fatal("工具档位目录未注入（无人调用 permission.RegisterToolLevelSource）——" +
			"LevelOf 会全返回 ok=false，本守卫将退化为空循环（假绿）。" +
			"先查 internal/tools/toollevel.go 的 init 是否仍在调用注入")
	}

	reg := NewRegistry()
	all := reg.All()
	if len(all) == 0 {
		t.Fatal("注册表为空 —— 守卫失去意义，先查 NewRegistry/registerBuiltinTools")
	}

	// ---- 主体：声明 RO 的工具，能力不得含 Edit / Exec ----
	var (
		readOnly   int
		violations []string
		pairs      []string
	)
	for _, spec := range all {
		lvl, ok := permission.LevelOf(spec.Name)
		if !ok {
			// 注册表里有、档位目录里没有 = L1 目录与注册表**漂移**（另一类缺陷，同样要报）。
			t.Errorf("工具 %q 在注册表中，但档位目录查不到（L1 目录快照与注册表漂移；"+
				"查 internal/tools/toollevel.go 的内置快照是否漏了它）", spec.Name)
			continue
		}
		if lvl != permission.LevelReadOnly {
			continue
		}
		readOnly++

		caps := permission.CapabilitiesOf(spec.Name)
		names := make([]string, 0, len(caps))
		for _, c := range caps {
			names = append(names, string(c))
		}
		sort.Strings(names)
		pairs = append(pairs, fmt.Sprintf("%-20s %v", spec.Name, names))

		for _, c := range caps {
			if c == permission.CapEdit || c == permission.CapExec {
				violations = append(violations,
					fmt.Sprintf("%s：声明 LevelReadOnly（只需只读档），能力却含 %s", spec.Name, c))
			}
		}
	}

	// ---- 前置 1：判据必须真的被走到过 ----
	//
	// 只读工具不可能为 0（`read_file`/`grep_search` 等必然在）。若为 0，说明判据本身失效
	// （Level 常量改名、LevelOf 语义被换、LevelReadOnly 值变了），此时"零违例"毫无意义。
	if readOnly == 0 {
		t.Fatalf("未扫到任何 LevelReadOnly 工具（共 %d 个已注册）—— 判据失效，守卫已无判别力。"+
			"常见原因：Level 常量被改名/重定义，或 LevelOf 不再返回声明档", len(all))
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("发现 %d 个「声明只读、能力却可写/可执行」的工具（档位-能力不一致）：\n  %s\n"+
			"两条修法（择一，别在守卫里豁免）：\n"+
			"  (a) 若它**确实**会写盘/跑命令 → 把 internal/tools/tools.go 里它的 RequiredPermission\n"+
			"      提到 LevelWorkspaceWrite（正解，如 R1 对 export_session 的做法）；\n"+
			"  (b) 若它**确实**只读 → 修 internal/permission/capability.go 的 toolCapabilities 误判。",
			len(violations), strings.Join(violations, "\n  "))
	}

	t.Logf("声明 LevelReadOnly 的工具 %d 个，能力均不含 Edit/Exec：", readOnly)
	for _, p := range pairs {
		t.Logf("  %s", p)
	}
}

// TestGuard_WriteCapableToolsAreNotDeclaredReadOnly 是上一条的**逆否**表述。
//
// 与上一条逻辑等价（同一命题），但报错方向相反、更好定位：从「能力」出发列出所有
// Edit/Exec 工具，逐个校验它们的档位 ≥ LevelWorkspaceWrite。
//
// 保留两条的理由：当有人**只**改动能力表（把某工具从 Read 挪到 Edit）时，
// 上一条的报错会指向"档位声明"，而实际该修的是**档位**；这条从能力侧枚举，
// 输出的清单直接就是"必须至少 WW 的工具全集"，便于与 tools.go 对照。
func TestGuard_WriteCapableToolsAreNotDeclaredReadOnly(t *testing.T) {
	if !permission.ToolLevelSourceRegistered() {
		t.Fatal("工具档位目录未注入 —— 见 TestGuard_NoReadCapabilityViaExec 的说明")
	}

	reg := NewRegistry()
	var (
		writeCapable int
		bad          []string
	)
	for _, spec := range reg.All() {
		caps := permission.CapabilitiesOf(spec.Name)
		isWriteCapable := false
		for _, c := range caps {
			if c == permission.CapEdit || c == permission.CapExec {
				isWriteCapable = true
				break
			}
		}
		if !isWriteCapable {
			continue
		}
		writeCapable++

		lvl, ok := permission.LevelOf(spec.Name)
		if !ok {
			t.Errorf("工具 %q 在注册表中但档位目录查不到（L1 目录漂移）", spec.Name)
			continue
		}
		if lvl == permission.LevelReadOnly {
			bad = append(bad, fmt.Sprintf("%s（能力含 Edit/Exec，档位却只给 LevelReadOnly）", spec.Name))
		}
	}

	if writeCapable == 0 {
		t.Fatal("未扫到任何具备 Edit/Exec 能力的已注册工具 —— 判据失效（能力表被清空？），守卫已无判别力")
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Fatalf("以下工具具备写/执行能力，但不能只要求只读档：\n  %s", strings.Join(bad, "\n  "))
	}
	t.Logf("具备 Edit/Exec 能力的工具 %d 个，档位均 ≥ LevelWorkspaceWrite", writeCapable)
}
