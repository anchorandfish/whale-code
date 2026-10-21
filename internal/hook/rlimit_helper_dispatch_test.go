//go:build darwin

// 平台范围说明：rlimit re-exec helper 仅 macOS 存在（linux 的等价物是
// `__whale-sandbox-exec` pre-exec helper，同一分发点 HandleSandboxHelperArg）。
// ⇒ 本判别器只能标 **PLATFORM-PARTIAL**，不得标 CLOSED（教训）。
package hook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/sandbox"
)

const rlimitSpecEnv = "WHALE_SANDBOX_RLIMIT_SPEC"

// TestN042_HookTestBinaryDispatchesRlimitHelper 是 的**永久判别器**。
//
// 缺陷本体：沙箱 re-exec helper 的分发若只装在 main.go 或**逐个包**的 TestMain 上，
// 任何**没有**守卫的测试包在"经生产沙箱真执行命令"时会被 re-exec 成
// `<该包的测试二进制> __whale-rlimit -- ...` ⇒ 测试二进制不认这个参数 ⇒
// **递归重跑整个测试套件并挂死**（症状是"挂住"不是变红，极难定位）。
//
// 本包（internal/hook）**刻意不写** per-package 守卫 —— 它就是要证明"结构性分发"成立：
// internal/sandbox 的 init（helper_dispatch.go）必须覆盖到**每一个**链接了 sandbox 的二进制。
//
// 判别性：把**自己的测试二进制**当子进程拉起，argv[1] 设为 helper 参数。
// - 修好：子进程立即进 helper 分支 ⇒ 可观测的 exit code ＋ stderr canary；
// - 未修：子进程进 testing 框架递归跑整套 hook 测试 ⇒ 30s 拿不到期望输出 ⇒ 红。
//
// ★ 反例注入（打红）：拆掉 internal/sandbox/helper_dispatch.go 的 init ⇒ 本测试超时红。
// ★ 不触碰任何用户文件：只拉起 /usr/bin/true，不写盘（纪律：探针不得写真实用户文件）。
func TestN042_HookTestBinaryDispatchesRlimitHelper(t *testing.T) {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		t.Fatalf("resolve test executable: %v", err)
	}

	t.Run("missing_spec_env_exits_126_with_canary", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, exe, sandbox.RlimitHelperArg, "--", "/usr/bin/true")
		cmd.Env = withoutEnv(os.Environ(), rlimitSpecEnv)
		out, runErr := cmd.CombinedOutput()
		got := string(out)

		if ctx.Err() != nil {
			t.Fatalf("子进程 30s 未结束 —— helper 分发未生效（递归重入 testing 框架）。output=%q", truncate(got))
		}
		var ee *exec.ExitError
		if !errors.As(runErr, &ee) {
			t.Fatalf("期望 helper 以非 0 退出，实际 err=%v output=%q", runErr, truncate(got))
		}
		if ee.ExitCode() != 126 {
			t.Fatalf("helper 退出码 = %d, want 126（EX_NOTUSABLE）; output=%q", ee.ExitCode(), truncate(got))
		}
		if !strings.Contains(got, "whale-rlimit-helper") {
			t.Fatalf("stderr 缺 helper canary（说明没走到 helper 分支）；output=%q", truncate(got))
		}
		// ★ 反向断言：绝不能出现 testing 框架的痕迹（出现即递归复发）。
		if strings.Contains(got, "PASS") || strings.Contains(got, "FAIL") || strings.Contains(got, "--- ") {
			t.Fatalf("子进程跑起了 testing 框架（递归复发）；output=%q", truncate(got))
		}
	})

	t.Run("valid_spec_execs_target", func(t *testing.T) {
		spec, err := json.Marshal(sandbox.ResourceLimits{
			CPUMaxSeconds:     10,
			FileDescriptorMax: 256,
			FileSizeMaxBytes:  1 << 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, exe, sandbox.RlimitHelperArg, "--", "/usr/bin/true")
		cmd.Env = append(withoutEnv(os.Environ(), rlimitSpecEnv), rlimitSpecEnv+"="+string(spec))
		out, runErr := cmd.CombinedOutput()

		if ctx.Err() != nil {
			t.Fatalf("子进程 30s 未结束（递归重入）; output=%q", truncate(string(out)))
		}
		if runErr != nil {
			t.Fatalf("helper 应 exec /usr/bin/true 并成功: err=%v output=%q", runErr, truncate(string(out)))
		}
	})
}

func withoutEnv(env []string, key string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, key+"=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func truncate(s string) string {
	if len(s) > 600 {
		return s[:600] + "...(truncated)"
	}
	return s
}
