package hook

// 回归测试（对抗式审查阻断项，v1 门 1）：applySandbox 的 Env/Dir 覆盖必须
// 先于 Restrict。修复前顺序为 Restrict → Env 覆盖，darwin rlimit helper 在
// Restrict 时追加的 WHALE_SANDBOX_RLIMIT_SPEC 被 sctx.Env 整体覆盖剥离，
// helper 启动后 exit 126，所有走沙箱的 HookCommand 失败（已实证复现；
// linux landlock helper 同模式受益于同一修复）。
//
// 断言层级：接线断言（helper 已包装 + spec env 存活）。
// 生产组合路径的运行时结果断言由 sandbox 包 TestRlimitE2E_*（普通终端）
// 与 VM Landlock E2E 覆盖。
import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

func TestApplySandbox_HelperSpecEnvPreserved(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("rlimit helper 仅 darwin（linux landlock 同模式由 VM E2E 覆盖）")
	}
	ws := t.TempDir()
	m := NewManager(ws)
	sbMgr := sandbox.NewManager(&sandbox.SandboxConfig{
		WorkDir: ws,
		OSLevel: sandbox.OSLevelConfig{Darwin: sandbox.DarwinSandboxConfig{
			UseSeatbelt:    true,
			ResourceLimits: sandbox.ResourceLimits{CPUMaxSeconds: 120},
		}},
	}, sandbox.SandboxLevelRelaxed)
	m.SetSandboxManager(sbMgr, sandbox.SandboxLevelRelaxed)

	cmd := exec.Command("/bin/sh", "-c", "true")
	h := &HookCommand{HookBase: HookBase{SandboxLevel: sandbox.SandboxLevelRelaxed}}
	cleanup, _, err := m.applySandbox(context.Background(), cmd, h)
	if err != nil {
		t.Fatalf("applySandbox: %v", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// 断言 1（接线）：命令已被 helper 包装
	if len(cmd.Args) < 2 || cmd.Args[1] != sandbox.RlimitHelperArg {
		// 嵌套沙箱（CI 的 agent 沙箱 / 已处在 sandbox-exec 内的进程）里
		// sandbox-exec 无法再 apply 一个 profile ⇒ OSSandbox 降级为 noop、
		// 不会挂 rlimit helper。此时"helper 是否被包装"这条判据的**前提不存在**，
		// 必须显式跳过而不是判红（否则等于用环境限制冒充回归）。
		if !seatbeltApplicable() {
			t.Skipf("环境无法应用 sandbox-exec profile（嵌套沙箱）⇒ rlimit helper 不会被包装: args=%v", cmd.Args)
		}
		t.Fatalf("cmd not wrapped by rlimit helper: args=%v", cmd.Args)
	}
	// 断言 2（回归核心）：spec env 必须在 Env 覆盖后仍存活
	//（修复前：Restrict 追加 → 随后被 sctx.Env 覆盖剥离 → helper exit 126）
	found := false
	for _, kv := range cmd.Env {
		if strings.HasPrefix(kv, "WHALE_SANDBOX_RLIMIT_SPEC=") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SECURITY/REGRESSION: helper spec env stripped after applySandbox (ordering bug): env=%v", cmd.Env)
	}
}

// seatbeltApplicable 探测当前环境能否真的应用 macOS sandbox profile。
// 嵌套沙箱里 `sandbox-exec` 会以 "sandbox_apply: Operation not permitted" 失败。
func seatbeltApplicable() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	probe := exec.Command("/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true")
	return probe.Run() == nil
}
