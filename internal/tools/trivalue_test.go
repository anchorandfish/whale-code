package tools

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// 本文件是 **T2 / T3 / T4**（2026-09-20 基础设施判定面）的永久判别器。
//
//	失败模式本体：自指保护闸（出口 (0)，）命中时**不分青红皂白地弹框** ——
//	连"沙箱必然拒掉、根本跑不成"的调用也问一次，用户得不到任何决策信息。
//
// 修法（用户拍板的穿透口径）：只放行「必拒」。
// - 必拒（已证明全部写目标落在可写根之外）⇒ **硬拒绝**（不弹）；
// - 必允 / 未知 ⇒ **仍然弹**（保留从严面，不因"目标在可写根内"放开自指保护）。
//
// 判别性：三条**配对**跑才成立 ——
// · ① 必拒 必须 Proceed=false 且 NeedConfirm=false（T3 + T4：不得软化成弹窗）；
// · ② 必允 必须 Proceed=true 且 NeedConfirm=true（若"必允"也穿透 ⇒ ② 红）；
// · ③ 未知 必须 Proceed=true 且 NeedConfirm=true（若把未知当必允 ⇒ ③ 红）。
// · ④ 对照：关闭自指保护（WHALE_ALLOW_SELF_MODIFY=1）后，同一条必拒命令**变回询问**
// ⇒ 证明"不弹"确实来自本闸，而不是别处顺带不弹。
//
// ★ 反例注入（打红）：把 MustDeny 分支改回无条件 `NeedConfirm: true` ⇒ ① 红；
// 把必允也判成 MustDeny ⇒ ② 红。

const (
	i22DenyCommand    = `echo x > /etc/whale_i22_probe`
	i25InsideCommand  = `echo x > ./i25_inside.txt`
	i25UnknownCommand = `echo x > "$OUTDIR/i25.txt"`
)

func bashInput(command string) string {
	return fmt.Sprintf(`{"command":%q}`, command)
}

// TestT2_ClassifyWriteVerdict_ThreeValues 是 T2 的**表驱动判别器**（三值本体）。
func TestT2_ClassifyWriteVerdict_ThreeValues(t *testing.T) {
	workDir := t.TempDir()

	cases := []struct {
		name    string
		tool    string
		input   string
		want    WriteTargetVerdict
		whyText string
	}{
		{
			name:    "必拒：绝对目标在可写根之外",
			tool:    "bash",
			input:   bashInput(i22DenyCommand),
			want:    WriteVerdictMustDeny,
			whyText: "/etc 不在 工作区/临时目录/缓存 任何一个根内",
		},
		{
			name:    "必允：相对目标落在工作区内",
			tool:    "bash",
			input:   bashInput(i25InsideCommand),
			want:    WriteVerdictMustAllow,
			whyText: "./i25_inside.txt 解析到 workDir 内",
		},
		{
			name:    "未知：变量目标不可归约",
			tool:    "bash",
			input:   bashInput(i25UnknownCommand),
			want:    WriteVerdictUnknown,
			whyText: "$OUTDIR 的值静态不可知",
		},
		{
			name:    "未知：命令替换目标不可归约",
			tool:    "bash",
			input:   bashInput(`echo x > "$(mktemp)/i24.txt"`),
			want:    WriteVerdictUnknown,
			whyText: "$(mktemp) 的值静态不可知",
		},
		{
			name:    "未知：没有写目标（不是必拒）",
			tool:    "bash",
			input:   bashInput("ls -la /tmp"),
			want:    WriteVerdictUnknown,
			whyText: "空目标集不得判成必拒，否则所有只读命令会被硬拒",
		},
		{
			name:    "未知：非进程类工具不在本判据适用范围",
			tool:    "write_file",
			input:   `{"path":"/etc/x","content":"y"}`,
			want:    WriteVerdictUnknown,
			whyText: "写目标不在命令串里",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyWriteVerdict(tc.tool, tc.input, workDir)
			if got != tc.want {
				t.Fatalf("classifyWriteVerdict(%s) = %v, want %v（%s）", tc.tool, got, tc.want, tc.whyText)
			}
		})
	}
}

// TestT3_T4_SelfSourceGate_PenetratesOnlyMustDeny 是 T3 ＋ T4 的端到端判别器。
//
// 走**生产入口**：`PermissionStage.Execute`（不是手工构造的旁路）。
func TestT3_T4_SelfSourceGate_PenetratesOnlyMustDeny(t *testing.T) {
	// 测试进程的 cwd = internal/tools ⇒ 落在 whale 自身源码树内
	// ⇒ selfTargetedCall("bash", …) 必为 true（P3 来源 1）。
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if !permission.IsSelfSourceTree(workDir) {
		t.Fatalf("前置不成立：cwd=%s 未被判为自身源码树（本判别器需要自指场景）", workDir)
	}

	policy := permission.NewPolicy(permission.LevelWorkspaceWrite)
	stage := &PermissionStage{policy: policy, workDir: workDir}

	run := func(t *testing.T, command string) StageResult {
		t.Helper()
		return stage.Execute(context.Background(), Call{
			ID:    "t3-" + command,
			Name:  "bash",
			Input: bashInput(command),
		}, &PipelineState{})
	}

	t.Run("①必拒：硬拒绝且不弹", func(t *testing.T) {
		sr := run(t, i22DenyCommand)
		if sr.Proceed {
			t.Fatalf("必拒必须 Proceed=false；得到 %+v", sr)
		}
		// T4：硬拒绝**不得**软化成弹窗。
		if sr.NeedConfirm {
			t.Fatalf("必拒不得产出 NeedConfirm（T4 红线：硬拒绝不许软化）；得到 %+v", sr)
		}
		if sr.Rejection == "" {
			t.Fatal("必拒必须带拒绝原因")
		}
		if sr.FailureKind != FailureKindSandboxDenied {
			t.Fatalf("必拒的 FailureKind = %q, want %q", sr.FailureKind, FailureKindSandboxDenied)
		}
	})

	t.Run("②必允：仍然询问", func(t *testing.T) {
		sr := run(t, i25InsideCommand)
		if !sr.Proceed || !sr.NeedConfirm {
			t.Fatalf("必允必须仍然弹（Proceed=true, NeedConfirm=true）；得到 %+v", sr)
		}
	})

	t.Run("③未知：仍然询问", func(t *testing.T) {
		sr := run(t, i25UnknownCommand)
		if !sr.Proceed || !sr.NeedConfirm {
			t.Fatalf("未知必须仍然弹（Proceed=true, NeedConfirm=true）；得到 %+v", sr)
		}
	})

	t.Run("④对照：关闭自指保护后必拒变回询问", func(t *testing.T) {
		t.Setenv("WHALE_ALLOW_SELF_MODIFY", "1")
		sr := run(t, i22DenyCommand)
		if !sr.Proceed || !sr.NeedConfirm {
			t.Fatalf("关闭自指保护后不得再硬拒（应回到询问）；得到 %+v", sr)
		}
	})
}

// TestT3_Invariant3_DangerFullAccessUnaffected 钉住不变式 3：
// `danger-full-access` 档本就不问（该档 `checkModeLocked` 直接返回 (true,false)）
// ⇒ 必拒**不得**在该档引入新的拒绝（那是放大，不是收窄）。
func TestT3_Invariant3_DangerFullAccessUnaffected(t *testing.T) {
	workDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	policy := permission.NewPolicy(permission.LevelDangerFullAccess)
	stage := &PermissionStage{policy: policy, workDir: workDir}

	sr := stage.Execute(context.Background(), Call{
		ID:    "t3-dfa",
		Name:  "bash",
		Input: bashInput(i22DenyCommand),
	}, &PipelineState{})
	if !sr.Proceed || sr.NeedConfirm {
		t.Fatalf("danger-full-access 档不受本判据影响（应 Proceed=true, NeedConfirm=false）；得到 %+v", sr)
	}
}
