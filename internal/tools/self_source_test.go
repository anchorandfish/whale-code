package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// —— dogfooding 下「agent 改 whale 自身源码 → 重建二进制」全链零确认
// ============================================================================
//
// 用户拍板：**方案 A** —— 把「whale 自身源码树」纳入**已有的**受保护写入
// SSOT（`internal/permission` 的受保护目标判定），**单一声明、所有入口继承**；
// **废弃方案 B**（只在出口 (a)/(c) 加自指例外）—— 理由是 B 只堵两个出口，
// 新入口不会自动继承，正是 B2 正在收的「多入口各写各的」病。
//
// 为什么两条免问出口在自指场景下失效（详见 `债务清剿_I10_dogfooding_自改源码_F-P_2026-09-19.md` §3.3）：
//
//	出口 (a) 工作区内写免问 —— 前提「没逃逸可写根 ⇒ 无需用户决策」
//	出口 (c) 沙箱真强制免问 —— 前提「副作用全被 OS 边界兜住 ⇒ 无决策信息」
//
// 两条都以「工作区/沙箱边界」为信任边界；dogfooding 时**该边界与被保护对象（whale 自己）重合**
// ⇒ 前提字面成立、结论不成立。
//
// 本文件是本项在**生产入口**上的判别器：`InitPipelineWithHooks` + `ExecuteWithConfirm`。
//
// ★ 判据口径 = **P3（按工具可达面分流；用户 2026-09-20 05:2x 拍板）**：
// 任意命令类（`bash`/`Monitor`）用 P1「工作区**或**二进制在自身树」；
// 目标受限类（写类工具）用 P2「任一目标在自身树」。分流实现见 `pipeline.go::selfTargetedCall`，
// 判据本身在 `permission/selfpath.go`。
//
// ★ 夹具说明：「自指工作区」= 一个 go.mod 声明本仓模块路径的临时目录。
// 它是对**生产输入（workDir）**的构造，不是把守卫置空/替换依赖 —— 判别器走的是
// 与生产完全相同的装配与阶段链。
//
// ★ 另一处必须注入的**生产输入**是「运行中的二进制住在哪」（`permission.RunningExePath`）：
// 测试二进制的 exe 在 go 构建临时目录，而生产二进制（本仓构建的二进制 `whale`）
// 住在自身源码树里 ⇒ 不注入就**测不到生产形态**（2026-09-20 实测踩到，见回执 §9）。
// 注入的是**输入值**，不是替换实现。

// n077Enforcement 是 EnforcementStatus 的测试替身（只报告"OS 沙箱在强制"这一事实）。
type n077Enforcement struct{}

func (n077Enforcement) OSEnforcementActive() bool { return true }

// n077SelfTree 造一棵「whale 自身源码树」形态的工作区。
//
// 判据 = go.mod 里 `module github.com/anchorandfish/whale-code`（本仓真实模块路径）。
// 这正是 dogfooding 的物理形态：工作区就是 whale 自己的源码根。
func n077SelfTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	n077WriteGoMod(t, root, "github.com/anchorandfish/whale-code")
	if err := os.MkdirAll(filepath.Join(root, "internal", "permission"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

// n077OtherTree 造一棵「别人的源码树」：结构相同，但模块路径不是本仓。
func n077OtherTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	n077WriteGoMod(t, root, "example.com/some/other-project")
	if err := os.MkdirAll(filepath.Join(root, "internal", "permission"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root
}

func n077WriteGoMod(t *testing.T, root, module string) {
	t.Helper()
	body := "module " + module + "\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
}

// n077Pipeline 经**生产入口**装配管线（InitPipelineWithHooks），并注入"沙箱在强制"这一事实。
func n077Pipeline(t *testing.T, mode permission.Level, workDir string) *Executor {
	t.Helper()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(mode), workDir)
	e.SetEnforcementStatus(n077Enforcement{})
	return e
}

// ---------------------------------------------------------------------------
// 主判别器
// ---------------------------------------------------------------------------

// TestFix_N077_SelfSourceTreeWritesRequireConfirm 是主判别器。
//
// 断言：工作区 = whale 自身源码树时
// - `write_file` 指向树内（改自身源码）⇒ **必须** NeedConfirm；
// - `bash` 构建/安装/重建（写自身二进制）⇒ **必须** NeedConfirm。
//
// P3 分流下两条分别走：write_file ⇒ P2（目标在自身树）；bash ⇒ P1（工作区即自身树）。
//
// BEFORE（改前）必须红：两条出口同时放行 ——
//
//	① write_file：SafetyCheckStage 判"不受保护" → 出口 (a) `workspaceContainedWrite` 命中 ⇒ 免问；
//	② bash：出口 (c) `IsSandboxBoundedTool(bash) && 沙箱在强制` 命中 ⇒ 免问。
func TestFix_N077_SelfSourceTreeWritesRequireConfirm(t *testing.T) {
	root := n077SelfTree(t)

	cases := []struct {
		name  string
		tool  string
		input string
	}{
		{
			name:  "write_file-自身源码",
			tool:  "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(root, "internal", "permission", "permission.go")) + `,"content":"package permission\n"}`,
		},
		{name: "bash-重建二进制-o", tool: "bash", input: `{"command":"go build -o whale ."}`},
		{name: "bash-构建全部", tool: "bash", input: `{"command":"go build ./..."}`},
		{name: "bash-安装", tool: "bash", input: `{"command":"go install ."}`},
		{name: "bash-make", tool: "bash", input: `{"command":"make build"}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := n077Pipeline(t, permission.LevelWorkspaceWrite, root)
			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID:    "n077-main-" + c.name,
				Name:  c.tool,
				Input: c.input,
			})

			if !pr.NeedConfirm {
				t.Fatalf("自指场景下必须强制确认（%s）：NeedConfirm=false Proceed=%v logs=%+v",
					c.name, pr.Proceed, pr.StageLogs)
			}
			if pr.Proceed {
				t.Fatalf("确认前不得放行（%s）：Proceed=true logs=%+v", c.name, pr.StageLogs)
			}
			// 防空绿：确认请求不得伴随"已经执行过"的产物。
			if pr.Result != nil {
				t.Fatalf("确认前不得到达执行端（%s）：Result=%+v", c.name, pr.Result)
			}
		})
	}
}

// n077JSONString 生成 JSON 字符串字面量（路径里可能有 / 与反斜杠，命令里可能有换行）。
func n077JSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// 守卫（防过度收紧 / 防软化 / 防"任何 go.mod 都算自身"）
// ---------------------------------------------------------------------------

// TestGuard_N077_NonSelfWorkspaceUnchanged：**非自指工作区行为零变化**。
//
// 这是本项最重要的一条守卫 —— 修法只能收紧自指场景，不得波及其他任何工作区。
// 若不红（即"别人的项目里也弹确认"），说明判据退化成了"凡 go.mod 皆自身"。
func TestGuard_N077_NonSelfWorkspaceUnchanged(t *testing.T) {
	root := n077OtherTree(t)

	cases := []struct {
		name  string
		tool  string
		input string
	}{
		{
			name:  "write_file-工作区内",
			tool:  "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(root, "internal", "permission", "x.go")) + `,"content":"package permission\n"}`,
		},
		{name: "bash-重建", tool: "bash", input: `{"command":"go build -o whale ."}`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := n077Pipeline(t, permission.LevelWorkspaceWrite, root)
			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID:    "n077-guard-nonself-" + c.name,
				Name:  c.tool,
				Input: c.input,
			})
			if pr.NeedConfirm {
				t.Fatalf("非自指工作区不得新增确认（%s）：logs=%+v", c.name, pr.StageLogs)
			}
			if !pr.Proceed {
				t.Fatalf("非自指工作区行为不得变化（%s）：Proceed=false logs=%+v", c.name, pr.StageLogs)
			}
		})
	}
}

// TestGuard_N077_ModulePathIsExactNotPrefix：判据是**模块路径精确相等**，
// 不是"含 whale 字样"。前缀相近的第三方模块不得被误保护。
func TestGuard_N077_ModulePathIsExactNotPrefix(t *testing.T) {
	root := t.TempDir()
	n077WriteGoMod(t, root, "github.com/anchorandfish/whale-code-fork-of-someone-else")

	e := n077Pipeline(t, permission.LevelWorkspaceWrite, root)
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n077-guard-prefix",
		Name:  "write_file",
		Input: `{"path":` + n077JSONString(filepath.Join(root, "a.go")) + `,"content":"package main\n"}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("模块路径近似不得被当作自身源码树：logs=%+v", pr.StageLogs)
	}
	// 反假绿：本用例要钉的是"不新增确认 **且** 正常放行"，
	// 若输入被更早的阶段（如 schema_validate 缺字段）拒掉，本用例会以"恰好没确认"假通过。
	if !pr.Proceed {
		t.Fatalf("前置不成立：输入被更早阶段拒绝 ⇒ 本用例无判别力：logs=%+v", pr.StageLogs)
	}
}

// TestGuard_N077_SelfTreeReadOnlyStillDenies：**单向收窄** —— 自指场景下
// read-only 档写自身源码仍必须是**拒绝**，不得被前置确认阶段软化成一次弹窗。
//
// 形态与 -R1 同源：新增的"保护判定"排在被保护路径检查这一层，
// 而该层已有"权限层本来就会拒 ⇒ 让路 deferring to it"的出口。
func TestGuard_N077_SelfTreeReadOnlyStillDenies(t *testing.T) {
	root := n077SelfTree(t)

	e := n077Pipeline(t, permission.LevelReadOnly, root)
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n077-guard-readonly",
		Name:  "write_file",
		Input: `{"path":` + n077JSONString(filepath.Join(root, "internal", "permission", "permission.go")) + `,"content":"package permission\n"}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("read-only 档下的写必须被拒绝而非询问（不得软化硬拒绝）：logs=%+v", pr.StageLogs)
	}
	if pr.Proceed {
		t.Fatalf("read-only 档下写自身源码必须被拒：Proceed=true logs=%+v", pr.StageLogs)
	}
	if pr.FailureKind != FailureKindPolicyDenied {
		t.Fatalf("拒绝类型必须是 policy_denied，实际 %q logs=%+v", pr.FailureKind, pr.StageLogs)
	}
}

// TestKnownResidual_N077_DangerFullAccessSelfModifyStillExempt 钉住**已知残留**（不是期望行为）。
//
// `danger-full-access` 档下 `checkModeLocked` 直接返回 (allowed=true, needConfirm=false)，
// 自指保护闸所在的 `if needConfirm && ...` 块整块不进入 ⇒ 自指写/重建**仍免问**。
//
// 为什么本批有意不修（而不是漏了）：该档的语义是用户**显式选择**的"全放行且免问"；
// 是否给它开第二条例外（现有首例是危险命令护栏，见 -R1）属另一个语义决策，
// 不在用户 2026-09-20 拍板的"方案 A"范围内。已登记为待拍板项。
//
// ★ 本用例是**残留标记**：该边界一旦被收紧，本用例会转红 —— 届时**删掉它**
// （不要为了让测试变绿而回退收紧）。名字里的 KnownResidual 就是这个用途。
func TestKnownResidual_N077_DangerFullAccessSelfModifyStillExempt(t *testing.T) {
	root := n077SelfTree(t)

	e := n077Pipeline(t, permission.LevelDangerFullAccess, root)
	pr := e.ExecuteWithConfirm(context.Background(), Call{
		ID:    "n077-residual-danger",
		Name:  "bash",
		Input: `{"command":"go build -o whale ."}`,
	})
	if pr.NeedConfirm {
		t.Fatalf("已知残留已被收紧（danger-full-access 下自指重建开始确认）。"+
			"若这是有意的语义变更，请删除本用例并更新登记表；logs=%s", n077Logs(pr))
	}
}

// n077ExeAt 注入「正在运行的二进制路径」—— 让 **P3 的生产形态**可判别。
//
// 为什么必须注入：测试二进制的 exe 落在 go 构建的临时目录，而生产二进制
// （本仓构建的二进制 `whale`）**住在自身源码树里** ⇒ 不注入就测不到生产形态
// （2026-09-20 实测踩到，见回执 §9）。
//
// ★ 注入的是**输入值**（"exe 在哪"），不是替换实现、不是置空守卫：
// 判定逻辑（`permission.isSelfSourceTreeFrom`）与阶段链一行未动。
func n077ExeAt(t *testing.T, exePath string) {
	t.Helper()
	old := permission.RunningExePath
	permission.RunningExePath = func() string { return exePath }
	t.Cleanup(func() { permission.RunningExePath = old })
}

// TestFix_N077_P3_SplitByToolReach 是 **P3 口径**（用户 2026-09-20 05:2x 拍板）的判别器。
//
// P3 = 按**工具可达面**分流：
//
//	任意命令类（bash/Monitor：入参界定不了可达面）⇒ P1「工作区**或**二进制在自身树」；
//	目标受限类（写类工具：效果只经由声明目标） ⇒ P2「任一目标在自身树」。
//
// BEFORE（改前 = 单一 P1 谓词、无分流）**必须红**：本用例的第 1 格把 exe 注入到自身树内
// （= 生产形态：二进制住在源码树里），单一 P1 谓词下**所有**工作区都被判自指
// ⇒ 「在别人的项目里写文件」也会弹确认（安全但吵，且与本项守卫自述的
// 「不得波及其他任何工作区」冲突 —— 这正是用户要求裁口径的那处偏差）。
//
// 其余各格是**守恒格**：改前改后都应当成立，用来钉住 P3 没有为了"不吵"而放开安全面。
func TestFix_N077_P3_SplitByToolReach(t *testing.T) {
	selfTree := n077SelfTree(t)
	unrelated := n077OtherTree(t)
	exeOutside := filepath.Join(t.TempDir(), "whale") // 二进制住在树外

	cases := []struct {
		name        string
		exePath     string
		workDir     string
		tool        string
		input       string
		wantConfirm bool
		why         string
	}{
		{
			// ★ 判别格（改前红）：P3 修掉的那处"吵"。
			name:    "P3修复点｜exe在树内·工作区=别人项目·写别人项目",
			exePath: filepath.Join(selfTree, "whale"), workDir: unrelated, tool: "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(unrelated, "internal", "permission", "x.go")) +
				`,"content":"package permission\n"}`,
			wantConfirm: false,
			why:         "目标受限类只看目标：目标不在自身树 ⇒ 不得因「二进制住在树里」而弹确认",
		},
		{
			// 守恒格：P3 保留的从严面 —— 任意命令类仍按 P1。
			name:    "守恒｜exe在树内·工作区=别人项目·bash",
			exePath: filepath.Join(selfTree, "whale"), workDir: unrelated, tool: "bash",
			input:       `{"command":"go build -o whale ."}`,
			wantConfirm: true,
			why:         "bash 的可达面不由入参界定（可从别处 cd 进自身树再重建）⇒ 保留 P1 从严",
		},
		{
			name:    "守恒｜exe在树外·工作区=别人项目·写别人项目",
			exePath: exeOutside, workDir: unrelated, tool: "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(unrelated, "internal", "permission", "y.go")) +
				`,"content":"package permission\n"}`,
			wantConfirm: false,
			why:         "非自指、非自指二进制 ⇒ 零变化（防「凡 go.mod 皆自身」退化）",
		},
		{
			name:    "守恒｜exe在树外·工作区=别人项目·bash",
			exePath: exeOutside, workDir: unrelated, tool: "bash",
			input:       `{"command":"go build -o whale ."}`,
			wantConfirm: false,
			why:         "两条来源都不在自身树 ⇒ 仍免问（出口 (c) 的原有行为不变）",
		},
		{
			name:    "守恒｜exe在树内·工作区=自身树·写自身源码",
			exePath: filepath.Join(selfTree, "whale"), workDir: selfTree, tool: "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(selfTree, "internal", "permission", "permission.go")) +
				`,"content":"package permission\n"}`,
			wantConfirm: true,
			why:         "dogfooding 正形态（P2 命中）",
		},
		{
			name:    "守恒｜exe在树内·工作区=自身树·bash重建",
			exePath: filepath.Join(selfTree, "whale"), workDir: selfTree, tool: "bash",
			input:       `{"command":"go build -o whale ."}`,
			wantConfirm: true,
			why:         "dogfooding 正形态（P1 命中）",
		},
		{
			// 关键守恒格：**即使二进制不住在树里**，写自身源码仍须确认
			// ⇒ 证明 P2 是独立判据，P3 没有把保护挂在"二进制位置"这一个信号上。
			name:    "守恒｜exe在树外·工作区=自身树·写自身源码",
			exePath: exeOutside, workDir: selfTree, tool: "write_file",
			input: `{"path":` + n077JSONString(filepath.Join(selfTree, "internal", "permission", "permission.go")) +
				`,"content":"package permission\n"}`,
			wantConfirm: true,
			why:         "P2 只看目标：二进制位置不影响「写自身树要确认」",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n077ExeAt(t, c.exePath)
			e := n077Pipeline(t, permission.LevelWorkspaceWrite, c.workDir)
			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID:    "n077-p3-" + c.name,
				Name:  c.tool,
				Input: c.input,
			})

			if c.wantConfirm {
				if !pr.NeedConfirm {
					t.Fatalf("%s：应当强制确认 —— NeedConfirm=false Proceed=%v（%s）logs=%s",
						c.name, pr.Proceed, c.why, n077Logs(pr))
				}
				if pr.Proceed {
					t.Fatalf("%s：确认前不得放行 —— Proceed=true logs=%s", c.name, n077Logs(pr))
				}
				// 防空绿：确认请求不得伴随"已经执行过"的产物。
				if pr.Result != nil {
					t.Fatalf("%s：确认前不得到达执行端 —— Result=%+v", c.name, pr.Result)
				}
				return
			}

			// wantConfirm=false：必须**真的放行**（不是「恰好没确认」）。
			if pr.NeedConfirm {
				t.Fatalf("%s：不得新增确认（%s）logs=%s", c.name, c.why, n077Logs(pr))
			}
			if !pr.Proceed {
				t.Fatalf("%s：应当正常放行 —— Proceed=false logs=%s", c.name, n077Logs(pr))
			}
		})
	}
}

// TestGuard_N077_P3_CommandClassNeverUsesTargetReach 钉住 P3 的**结构面**：
// 具备 `CapExec`（能跑任意命令）的工具**永不**落入「按目标判定」的 P2 分支。
//
// 为什么这是要害：P2 的判据是「目标路径是否在自身树」，而任意命令工具的目标
// **提取不出来**（`bash` 的入参是一个串，里面可以 `cd`/`$(...)`/调别的程序）
// ⇒ 一旦有人把这类工具加进写类工具表（`isWriteTool`，`selfTargetedCall` 的分流判据），
// 它就会静默滑进 P2，而 P2 对它是**盲的** ⇒ P3 保留的从严面被破坏。
// 这条守卫不依赖任何输入构造，改一次工具表就会打红。
func TestGuard_N077_P3_CommandClassNeverUsesTargetReach(t *testing.T) {
	execTools := permission.CapabilityToolsOf(permission.CapExec)
	if len(execTools) == 0 {
		t.Fatal("反假绿：CapExec 面为空 ⇒ 本守卫无判别力（能力表被清空？）")
	}
	for _, tool := range execTools {
		if isWriteTool(tool) {
			t.Errorf("工具 %q 具备 CapExec 却被列入写类工具表 ⇒ 会落入 P2（按目标判定），"+
				"而它的目标提取不出来 ⇒ P3 的从严面被破坏（应把它从写类工具表移出）", tool)
		}
		if !permission.CanModifyLocalState(tool) {
			t.Errorf("工具 %q 具备 CapExec 却不算「可改动本机」⇒ 自指闸根本不覆盖它", tool)
		}
	}
}

// TestGuard_N077_P3_TargetlessEditToolsFallBackToArbitraryReach 钉住分流的**另一半**：
// 「能改动本机、但不在写类工具表里」的工具（其效果对象不在入参里 —— 如 `config_set`
// 改的是 whale 自身配置）必须走 P1（从严），不得因为"提取不到目标"而被静默判为"非自指"。
//
// 这是对 `selfTargetedCall` 的**直接**判别（纯谓词，无副作用）；生产入口的行为判别器
// 是上面的 P3 矩阵 —— 两者分别覆盖"分流对不对"与"装配后行为对不对"。
func TestGuard_N077_P3_TargetlessEditToolsFallBackToArbitraryReach(t *testing.T) {
	selfTree := n077SelfTree(t)
	unrelated := n077OtherTree(t)

	// 这些工具能改动本机（能力表）但不在写类工具表里 ⇒ 目标不可静态判定。
	tools := []string{"config_set", "enter_worktree", "exit_worktree"}
	for _, tool := range tools {
		if !permission.CanModifyLocalState(tool) {
			t.Fatalf("前置不成立：%q 应按能力表算「可改动本机」（否则它根本不在自指闸范围内）", tool)
		}
		if isWriteTool(tool) {
			t.Fatalf("前置不成立：%q 已在写类工具表里 ⇒ 本用例的前提（目标不可判定）不成立", tool)
		}
	}

	// 生产形态（二进制在自身树内）⇒ 必须判自指。
	n077ExeAt(t, filepath.Join(selfTree, "whale"))
	for _, tool := range tools {
		if !selfTargetedCall(tool, `{}`, unrelated) {
			t.Errorf("%q 的目标不在入参里（可达面无法界定）⇒ 必须走 P1：二进制在自身树时应判自指", tool)
		}
	}

	// 反向：二进制不在树内 + 非自指工作区 ⇒ 不得判自指（否则是"一律从严"，分流无意义）。
	n077ExeAt(t, filepath.Join(t.TempDir(), "whale"))
	for _, tool := range tools {
		if selfTargetedCall(tool, `{}`, unrelated) {
			t.Errorf("%q 在非自指工作区、非自指二进制下不得被判自指", tool)
		}
	}
}

// n077Logs 把阶段留痕压成一行，便于失败信息定位。
func n077Logs(pr *PipelineResult) string {
	var b strings.Builder
	for _, l := range pr.StageLogs {
		b.WriteString("[" + l.Stage + ":" + l.Status + ":" + l.Message + "]")
	}
	return b.String()
}
