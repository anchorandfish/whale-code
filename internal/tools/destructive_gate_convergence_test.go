package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// B2 收尾—— 破坏性闸与 cmdrules 的判定合一
// ============================================================================
//
// 背景：`checkBashDestructive` 与 `cmdrules.MatchDestructive` 是"同一判定、两处分写"。
// 第九批已把**名单**收口到 cmdrules，但**匹配语义**仍是两套：旧实现只按 `\n` 切行、
// 只取首个 token；cmdrules 按 shell 分隔符逐段取命令位（并处理包装命令、路径前缀）。
//
// 差异的方向是**漏判**（不安全侧），不是一个等价实现：
//
//	`echo hi ; rm -rf build` —— 旧实现首 token = `echo` ⇒ 破坏性闸放行；
//	随后权限层判 destructive（needConfirm=true），但在"沙箱真在强制的 WW 档"下
//	被出口 (c) 免问 ⇒ **整条命令零确认执行**。
//
// 形态与 同源：豁免出口的前提在"命令被组合起来"时失效。

// TestFix_B2_DestructiveGateCatchesChainedCommand 是主判别器。
//
// 断言：`bash` 执行 `echo hi ; rm -rf build` 必须在**破坏性闸**上被拦下并请求确认。
//
// BEFORE（改前）必须红：破坏性闸漏判 ⇒ 落到出口 (c) ⇒ NeedConfirm=false 且真的执行。
// ★ 刻意用**非自指工作区**（t.TempDir）—— 本项与 不同轴，
//
//	若这里也变红只能说明 的闸误伤，不能说明本项修好。
func TestFix_B2_DestructiveGateCatchesChainedCommand(t *testing.T) {
	workDir := t.TempDir()

	cases := []struct{ name, command string }{
		{"分号连接", "echo hi ; rm -rf build"},
		{"与运算连接", "true && rm -rf build"},
		{"或运算连接", "false || rm -rf build"},
		{"换行连接", "echo hi\nrm -rf build"},
		{"路径前缀", "/bin/rm -rf build"},
		{"sudo 包装", "sudo rm -rf build"},
		{"sudo + 路径前缀", "sudo /sbin/rm -rf build"},
		{"环境赋值前缀", "FOO=bar rm -rf build"},
	}
	// ★ 不在上表内的形态（`echo x | xargs rm -rf`）是 cmdrules **明文标注的已知边界**：
	// "不处理引号内包含分隔符、命令替换 $(...)、xargs 后的次级命令"（cmdrules.go:445-447，
	// 有意不做以避免过度工程，方向为漏判）。本批不重开该决定 ⇒ 不在此断言、登记为未闭合。

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := n077Pipeline(t, permission.LevelWorkspaceWrite, workDir)
			pr := e.ExecuteWithConfirm(context.Background(), Call{
				ID:    "b2-chain-" + c.name,
				Name:  "bash",
				Input: `{"command":` + n077JSONString(c.command) + `}`,
			})

			if !pr.NeedConfirm {
				t.Fatalf("组合/包装形态的破坏性命令必须请求确认（%s：%s）：NeedConfirm=false Proceed=%v logs=%s",
					c.name, c.command, pr.Proceed, n077Logs(pr))
			}
			if pr.Proceed {
				t.Fatalf("确认前不得放行（%s）：Proceed=true logs=%s", c.name, n077Logs(pr))
			}
			// 防空绿：必须由**破坏性闸**拦下（而不是被危险闸或其它阶段顺带拦下）
			if msg := n040StageMessage(pr, "destructive_check"); !strings.Contains(msg, "destructive operation detected") {
				t.Fatalf("必须由破坏性闸拦下（%s），实际 destructive_check 留痕=%q logs=%s",
					c.name, msg, n077Logs(pr))
			}
		})
	}
}

// TestGuard_B2_ArgumentPositionStillNotFlagged 反例守卫：命令位判定不得退化成"全文出现即命中"。
//
// `echo "rm -rf build"` 里 `rm` 出现在**参数位**（引号内），不是命令位 ⇒ 不得判为破坏性。
// 这条守卫防的是"把闸改宽成子串匹配"这类反向修法。
func TestGuard_B2_ArgumentPositionStillNotFlagged(t *testing.T) {
	cases := []string{
		`echo "rm -rf build"`,
		`grep -rn "rm -rf" docs/`,
		`printf '%s' 'mv a b'`,
	}
	for _, cmd := range cases {
		if en, _ := checkBashDestructive(cmd); en != "" {
			t.Errorf("参数位出现命令名不得判为破坏性：%q ⇒ %q", cmd, en)
		}
	}
}

// TestGuard_B2_GitDestructiveParamsStillCaught git 破坏性参数（名单只有一处）仍必须命中。
func TestGuard_B2_GitDestructiveParamsStillCaught(t *testing.T) {
	cases := []string{
		"git reset --hard HEAD~1",
		"git clean -fd",
		"git push --force origin main",
		"sudo git reset --hard",
	}
	for _, cmd := range cases {
		if en, _ := checkBashDestructive(cmd); en == "" {
			t.Errorf("git 破坏性参数必须命中：%q", cmd)
		}
	}
}

// TestGuard_B2_TopLevelDelegateLimitIsOnlyAnUpperBound 收 A1 的残留项：
// `web.go` 的委派上限（`ParentToolSet` 的来源）**只能由 Policy 派生**，
// 不得出现"上限里带了权限层会拒的工具"这种把上限当授权用的形态。
//
// 背景（B2/A1 裁定）：A1 的"输入无关定级"经实测为**非缺陷**（`requiredForTool` 的
// input 依赖路径只有 `Agent` 一条，空 agent_type ⇒ 默认只读角色 explore ⇒ 保守取值）。
// 但那条裁定只证明了"取值保守"，没钉住"**这个面只作上限**"。本用例钉后者。
//
// 判别力：若有人把委派上限改成手工构造的清单（例如为图省事直接给全集），
// 只读档下它会立刻包含 write_file/bash ⇒ 本用例转红。
func TestGuard_B2_TopLevelDelegateLimitIsOnlyAnUpperBound(t *testing.T) {
	workDir := t.TempDir()
	e := NewExecutor(NewRegistry(), workDir)
	e.InitPipelineWithHooks(permission.NewPolicy(permission.LevelReadOnly), workDir)

	face := topLevelDelegateLimit(e)
	if face == nil {
		t.Fatal("前置不成立：read-only 档 + 已接线 Policy 下应能算出委派上限")
	}

	// ① 上限里的每个工具都必须是当前 Policy 允许的（上限 ⊆ 允许面）
	for name := range face {
		if allowed, _ := e.Pipeline.Policy().Check(name, "{}"); !allowed {
			t.Fatalf("%q 在 read-only 档被权限层拒绝，却出现在委派上限里 ⇒ 上限被当成了授权", name)
		}
	}

	// ② 反向：写/命令类必须不在面内（若上一条的实现被改成"全部放进去"，本行会先红）
	for _, w := range []string{"write_file", "edit_file", "bash", "apply_unified_diff"} {
		if face[w] {
			t.Errorf("read-only 档下 %q 不得进入委派上限", w)
		}
	}

	// ③ 非空性反假绿：只读档下必须**仍有**可用工具，否则 ① 会因空集而无判别力
	if len(face) == 0 {
		t.Fatal("前置不成立：read-only 档下委派面为空 ⇒ 本用例无判别力（①退化为恒真）")
	}
}

// TestGuard_B2_NonDestructiveStillClean 普通命令仍不得命中（防闸被改宽）。
func TestGuard_B2_NonDestructiveStillClean(t *testing.T) {
	cases := []string{
		"ls -la",
		"cat go.mod",
		"go build ./...",
		"git status",
		"git diff HEAD",
		"mkdir -p build",
	}
	for _, cmd := range cases {
		if en, _ := checkBashDestructive(cmd); en != "" {
			t.Errorf("非破坏性命令不得命中：%q ⇒ %q", cmd, en)
		}
	}
}
