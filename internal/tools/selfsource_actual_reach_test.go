package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/permission"
)

// ============================================================================
// （2026-10-08 用户拍板「乙」）—— 自指保护 (0) 从「命令类可达」收窄为「实际可达」
// ============================================================================
//
// 现场动机（I-13.11 真机取证，2026-10-08 09:32）：开发件（二进制住在自身源码树里）下
// 自指保护 (0) 对**每一条** bash 无条件弹窗，把 [P] 前缀免问 (d)、只读免问 (e) 全部短路
// —— 用户按多少次 [P] 都会再弹。而生产件（装好的 whale 不在源码树里）根本不触发 (0)。
//
// 平衡原则 = 三问（①越界 ②不可逆 ③没人真在兜底 —— 三者都不成立就不该问）：
// - 全段只读的命令**改不到树**（①不成立：没有写副作用）；
// - OS 沙箱真在强制、且自身树不在任何可写根内（③不成立：内核已兜住）。
// 这两类让路；其余（能写且没兜底）照旧必须问。
//
// 判别器全部走生产入口 `InitPipelineWithHooks` → `ExecuteWithConfirm`；
// 「exe 在哪」按 R6b 注入**生产输入**（`permission.RunningExePath`，不是替换实现）。
//
// ★ 夹具要点（2026-10-08 实测踩到）：测试里用 `t.TempDir()` 造的「自身树」物理上落在
// 临时可写根内 ⇒ 「树不在任何可写根内」这一形态**构造不出来**。
// ⇒ 涉及判据 (ii) 的用例必须把树造在**可写根集合之外**的目录（按平台对口，见
// w048OutsideRootsBase），才能忠实复现生产形态（生产树在 ~/Desktop，同样不在可写根内）。
// （2026-10-14 Linux 真机暴露：原实现硬编码 `/private/tmp` 是 Darwin 专属路径，
// Linux 上不存在 ⇒ precondition 直接炸。根集合 = 工作区 + os.TempDir() + 用户缓存
// （`sandbox.WritableRoots`），各平台选一个天然落在其外的 FHS/约定目录。）

// w048OutsideRootsBase 返回「不在任何可写根内」的造树基目录（按平台对口）。
// 可写根的唯一权威是 `sandbox.WritableRoots` = {工作区, os.TempDir(), os.UserCacheDir()}：
// - darwin：TempDir 在 /var/folders/…（解析后 /private/var/…），/private/tmp 不在其中；
// - 其余（linux 等）：TempDir = /tmp 本身就是可写根 ⇒ 用 FHS 保留的 /var/tmp。
//
// 若某平台连对口目录都拿不到（如沙箱化的容器），Skip 而非 Fatal —— 本格测的是
// 「树在根外」的形态，环境造不出这个形态时红在这里不具判别力。
func w048OutsideRootsBase(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		return "/private/tmp"
	}
	if fi, err := os.Stat("/var/tmp"); err != nil || !fi.IsDir() {
		t.Skipf("precondition: 本平台（GOOS=%s）无 /var/tmp，无法构造「可写根之外」的树", runtime.GOOS)
	}
	return "/var/tmp"
}

// w048SelfTreeOutsideRoots 造一棵**不在任何可写根内**的自身源码树（生产形态）。
func w048SelfTreeOutsideRoots(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp(w048OutsideRootsBase(t), "w048_selftree_")
	if err != nil {
		t.Fatalf("precondition: 造可写根之外的自身树失败: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	body := "module " + permission.SelfModulePath + "\n\ngo 1.25\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatalf("precondition: 写 go.mod 失败: %v", err)
	}
	return root
}

// ① 判别格（改前红）：全段只读命令在自身源码树内**不再弹** —— 它改不到树。
// 夹具刻意**不注入** enforcement（nil = 不可强制）：否则出口 (c)（排在 (e) 前）会
// 以 "sandbox-bounded" 先放行，正向留痕就验不到只读出口 (e) 这条路径（实测踩过）。
// 正向留痕：放行必须来自 (e)（message 含 "read-only command"），
// 排除「被更早阶段恰好放行」的假绿。
func TestW048_ReadOnlyInSelfTreeNotConfirmed(t *testing.T) {
	t.Setenv("WHALE_ALLOW_SELF_MODIFY", "")
	ctx := context.Background()

	root := n077SelfTree(t) // 工作区 = 自身源码树（来源 1 命中）
	e, _ := w023Executor(t, root)

	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w048-ro", Name: "bash", Input: bashInput("ls -la")})
	if pr.NeedConfirm {
		t.Fatalf("W-048①：全段只读命令改不到自身树，自指闸应让路（N-040①：无越界）; logs=%s", n077Logs(pr))
	}
	if !pr.Proceed || pr.Result == nil {
		t.Fatalf("W-048①：免问 ≠ 免执行，命令必须真的跑; proceed=%v logs=%s", pr.Proceed, n077Logs(pr))
	}
	if !w048LogsContain(pr, "read-only command") {
		t.Fatalf("W-048①：正向留痕缺失 —— 放行必须来自只读免问出口 (e)，不是更早的偶然放行; logs=%s", n077Logs(pr))
	}
}

// ② 反例守卫（不具判别性：改前改后都绿）—— 钉住收窄**不越界**：
// 能写的命令在自身树内（沙箱未注入 = 不可强制）仍必须问。
func TestW048_WriteCommandsInSelfTreeStillConfirm(t *testing.T) {
	t.Setenv("WHALE_ALLOW_SELF_MODIFY", "")
	ctx := context.Background()

	root := n077SelfTree(t)
	e := n077Pipeline(t, permission.LevelWorkspaceWrite, root) // ⚠️ n077Pipeline 注入 enforcement=true；本格要测"树在可写根内"的形态，workDir=树 ⇒ 判据(ii)不适用

	cases := []struct{ name, cmd, why string }{
		{"mkdir", "mkdir -p " + filepath.Join(root, "w048d"), "能写"},
		{"redirect", "ls > " + filepath.Join(root, "w048o.txt"), "重定向 = 写入面 ⇒ 不算全段只读"},
		{"compound", "ls && mkdir -p " + filepath.Join(root, "w048d2"), "复合含写段 ⇒ 不算全段只读"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pr := e.ExecuteWithConfirm(ctx, Call{ID: "w048-guard-" + tc.name, Name: "bash", Input: bashInput(tc.cmd)})
			if !pr.NeedConfirm {
				t.Fatalf("W-048②：自身树内能写的命令仍必须确认（%s）; logs=%s", tc.why, n077Logs(pr))
			}
		})
	}
}

// ③ 判别格（改前红）：OS 沙箱真在强制 ＋ 自身树不在任何可写根内 ⇒ 进程被内核关在树外，
// 自指闸让路给出口 (c)（N-040③：强制机制真在兜底）。生产形态 = exe 住 ~/Desktop 树内、
// 工作区在别处 —— 单测里用「可写根之外」的树忠实复现（t.TempDir 树会被临时可写根吞掉；
// 基目录按平台对口选，见 w048OutsideRootsBase）。
func TestW048_EnforcementConfinedTreeOutsideRootsExempts(t *testing.T) {
	t.Setenv("WHALE_ALLOW_SELF_MODIFY", "")
	ctx := context.Background()

	tree := w048SelfTreeOutsideRoots(t)        // 树在可写根之外（不在 T/缓存可写根内）
	workDir := t.TempDir()                     // 工作区与树互不相干
	n077ExeAt(t, filepath.Join(tree, "whale")) // 生产形态：二进制住在自身树里（来源 2 命中）

	e := n077Pipeline(t, permission.LevelWorkspaceWrite, workDir)

	target := filepath.Join(workDir, "w048d")
	pr := e.ExecuteWithConfirm(ctx, Call{ID: "w048-sbx", Name: "bash", Input: bashInput("mkdir -p " + target)})
	if pr.NeedConfirm {
		t.Fatalf("W-048③：沙箱真兜住（树不在可写根内）时自指闸应让路（N-040③）; logs=%s", n077Logs(pr))
	}
	if !pr.Proceed {
		t.Fatalf("W-048③：应正常放行（沙箱兜底的写工作区操作）; proceed=%v logs=%s", pr.Proceed, n077Logs(pr))
	}
	if !w048LogsContain(pr, "sandbox-bounded") {
		t.Fatalf("W-048③：正向留痕缺失 —— 放行必须来自沙箱免问出口 (c)，不是更早的偶然放行; logs=%s", n077Logs(pr))
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("W-048③：免问后应真正落盘（目录应存在）: %v", err)
	}
}

// ④ 反例守卫（不具判别性：改前改后都绿）—— 判据 (ii) 的**作用域**钉子：
// 沙箱在强制、但树**落在可写根内**（dogfooding：工作区=自身树）⇒ 树可写 ⇒ 仍必须问。
// 防"把 (ii) 写成只要 enforcement 就放行"的一刀切。
func TestW048_EnforcementActiveTreeInsideWorkDirStillConfirms(t *testing.T) {
	t.Setenv("WHALE_ALLOW_SELF_MODIFY", "")
	ctx := context.Background()

	root := n077SelfTree(t) // 工作区 = 自身树 ⇒ 可写根含树
	n077ExeAt(t, filepath.Join(root, "whale"))
	e := n077Pipeline(t, permission.LevelWorkspaceWrite, root)

	pr := e.ExecuteWithConfirm(ctx, Call{
		ID: "w048-sbx-in-tree", Name: "bash",
		Input: bashInput("mkdir -p " + filepath.Join(root, "w048d")),
	})
	if !pr.NeedConfirm {
		t.Fatalf("W-048④：树在可写根内时，沙箱强制也不得让路（dogfooding 正形态）; logs=%s", n077Logs(pr))
	}
}

// w048LogsContain 在阶段留痕的 Message 里找子串。
func w048LogsContain(pr *PipelineResult, substr string) bool {
	for _, l := range pr.StageLogs {
		if strings.Contains(l.Message, substr) {
			return true
		}
	}
	return false
}
