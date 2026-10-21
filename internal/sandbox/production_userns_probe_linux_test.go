// Package sandbox — 真机实证：生产入口在「userns 被管理策略挡住」时的行为。
//
// 背景（2026-10-11 UTM VM Ubuntu 24.04 实测）：现代 Ubuntu 默认
// kernel.apparmor_restrict_unprivileged_userns=1，非特权进程 clone(CLONE_NEWUSER)
// 返回 EPERM。此时 syscallCompatCheck()（只看内核版本 ≥3.8）照样通过 ⇒
// 工厂不降级，Cloneflags 照设，**每条沙箱化命令在 cmd.Start() 时才吃 EPERM**。
//
// 被钉住的不变式（fail-closed 语义的完备面）：
//
//	EnforcementActive(mgr) == true ⟹ 沙箱化命令必须真的能启动。
//
// 反向即「假强制」：状态面宣称 landlock/namespaces 已生效（审批层据此免问），
// 而实际任何命令都起不来——安全上侥幸闭环（fail-closed），但产品不可用且
// 状态口径说谎。
//
// 为什么 opt-in（WHALE_TEST_USERNS_REQUIRED=1）：CI 容器的默认 seccomp 同样会
// 挡 CLONE_NEWUSER（非特权 unshare），把本测试放进常规套件会在 CI 假红。
// 本测试的判据依赖「内核真的放行 userns」这一环境前提，只有真机跑才有效。
package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestE2E_ProductionEntry_EnforcementImpliesStartable(t *testing.T) {
	if os.Getenv("WHALE_TEST_USERNS_REQUIRED") != "1" {
		t.Skip("opt-in real-machine probe: set WHALE_TEST_USERNS_REQUIRED=1 (CI seccomp blocks unshare ⇒ false red)")
	}

	workDir := t.TempDir()
	mgr := NewProductionSandboxManager(workDir)

	st := IsolationStatusOf(mgr)
	t.Logf("platform=%q features=%v degradations=%v enforcementActive=%v",
		st.Platform, st.Features, st.Degradations, EnforcementActive(mgr))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	sbCtx, cleanup, err := mgr.Prepare(ctx, mgr.Level(), workDir)
	if err != nil {
		t.Fatalf("Manager.Prepare failed: %v", err)
	}
	defer cleanup()

	osSB := sbCtx.OSSandbox()
	if osSB == nil {
		t.Fatal("OSSandbox is nil")
	}

	var buf bytes.Buffer
	cmd := exec.Command("sh", "-c", "echo probe-ok")
	cmd.Env = sbCtx.Env
	cmd.Dir = sbCtx.WorkDir
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := osSB.Restrict(cmd); err != nil {
		t.Fatalf("Restrict failed: %v", err)
	}
	startErr := StartCommandWithProcessGroup(cmd)

	enforced := EnforcementActive(mgr)
	if enforced && startErr != nil {
		t.Errorf(" 假强制实锤: EnforcementActive=true 但命令起不来 startErr=%v（状态面宣称 %v，实际无法执行任何命令）",
			startErr, st.Features)
		return
	}
	if !enforced && startErr == nil {
		t.Logf("注意: EnforcementActive=false 但命令启动成功（降级档下命令可跑，属预期形态之一）")
	}
	if startErr != nil {
		// 未宣称强制时的失败：只要求错误可读，不作为缺陷
		if strings.Contains(startErr.Error(), "operation not permitted") {
			t.Logf("fail-closed 生效: 命令被拒（EPERM），平台=%q", st.Platform)
		}
		return
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("command exited: %v (output=%q)", err, buf.String())
	}
	if !strings.Contains(buf.String(), "probe-ok") {
		t.Fatalf("unexpected output: %q", buf.String())
	}
	t.Logf("命令在沙箱内正常执行: platform=%q features=%v", st.Platform, st.Features)
}
