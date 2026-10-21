package sandbox

// —— rm 从硬拒档降为可批准档（TierConfirm）。
//
// 拍板：2026-10-14 用户（推翻 10-05 的"rm/dd/mkfs/format 保持硬拒"中 rm 的部分）：
//
//	"rm 这些也可以走审批，主要在做程序的过程，比如会有生成测试文件，那它就可以修，
// 你最多提醒这是高危操作，但是直接不让操作是不是不对，其他同理。"
//
// 业内对标（本地快照实测，file:line）：
// - 同类实现
// "We prefer to prompt the user rather than outright forbid the command"；
// `rm -f`/`rm -rf` 命中危险判据 ⇒ Decision::Prompt（弹审批），
// 仅用户显式设 AskForApproval::Never 才 Forbidden。
//
// 改后的分层语义（本文件逐格钉住）：
//
//	rm（命令位，普通目标） ⇒ TierConfirm —— 弹审批，批准后出沙箱跑这一条
//	rm -rf / / ~ / $变量（灾难形态）⇒ TierHardDeny —— 连批准框都不弹
// （与执行链 RM-RF-AST Critical 同源：
// validator.AssessRmRfDangerousTarget，
// 否则"批准了还拒"= 病窄集合复发）
//	dd / mkfs / format ⇒ TierHardDeny（不变）
//
// R10① 病历：改前 audit_char_g0_test 钉过 `rm file.txt` ⇒ DENYLIST（Validate 层，
// 仍绿不翻 —— rm 仍命中 deny 规则，只是"拦下来之后能不能被批准"变了）；
// 本文件钉的是 tier 面。ruleengine_delegate_test 的 deny-wins 样本已随
// 本拍板由 rm 换为 dd（判别意图不变：deny 扫描先于替换判定）。

import "testing"

// TestW099_RmPlainTarget_IsConfirmTier 主判据：普通 rm 落可批准档。
// before（rm 在硬拒名单）必红；after 绿。
func TestW099_RmPlainTarget_IsConfirmTier(t *testing.T) {
	s := newW021Sandbox(t)
	for _, cmd := range []string{
		`rm -f /tmp/whale-w099-probe`,
		`rm build/artifacts.txt`,
		`/bin/rm -f /tmp/whale-w099-probe`, // basename 口径与 matchDenyPattern 一致
	} {
		tier, rule, hit := s.Classify(cmd)
		if tier != TierConfirm {
			t.Fatalf("普通 rm 应为可批准档（ 拍板），实际 tier=%v rule=%s hit=%q cmd=%q",
				tier, rule, hit, cmd)
		}
	}
}

// TestW099_RmRecursiveWorkspaceDir_IsConfirmTier 开发日常形态：
// `rm -rf 子目录`（工作区内清理测试产物）也应是可批准档 ——
// RM-RF-AST Critical 只钉灾难目标（/、~、$HOME、$变量），不钉相对/普通绝对路径。
func TestW099_RmRecursiveWorkspaceDir_IsConfirmTier(t *testing.T) {
	s := newW021Sandbox(t)
	tier, _, _ := s.Classify(`rm -rf ./build/test-output`)
	if tier != TierConfirm {
		t.Fatalf("rm -rf 工作区子目录应为可批准档，实际 tier=%v", tier)
	}
}

// TestW099_RmCatastrophicTargets_StayHard 灾难形态保持硬拒（回归守卫）。
// ⚠️ 本条 before/after 两态皆绿（改前经 irreversibleDenyCommands 硬、
// 改后经 AssessRmRfDangerousTarget 硬）—— 它不具判别性，作用是钉住
// "rm 降档没有把灾难形态一并放下去"。判别性由 TestW099_RmPlainTarget 承担。
func TestW099_RmCatastrophicTargets_StayHard(t *testing.T) {
	s := newW021Sandbox(t)
	for _, cmd := range []string{
		`rm -rf /`,
		`rm -rf ~`,
		`rm -rf $HOME/tmp`,
		`rm -rf $WHATEVER`,
	} {
		tier, _, _ := s.Classify(cmd)
		if tier != TierHardDeny {
			t.Fatalf("灾难形态 rm 必须保持硬拒（连批准框都不弹），实际 tier=%v cmd=%q", tier, cmd)
		}
	}
}

// TestW099_DdMkfsFormat_StayHard 配对反例：本次只降 rm，dd/mkfs/format 不动。
func TestW099_DdMkfsFormat_StayHard(t *testing.T) {
	s := newW021Sandbox(t)
	for _, cmd := range []string{
		`dd if=/dev/zero of=/tmp/whale-w099-junk bs=1 count=1`,
		`mkfs /dev/sda1`,
		`format C:`,
	} {
		tier, _, _ := s.Classify(cmd)
		if tier != TierHardDeny {
			t.Fatalf("dd/mkfs/format 应保持硬拒（ 只动 rm），实际 tier=%v cmd=%q", tier, cmd)
		}
	}
}
