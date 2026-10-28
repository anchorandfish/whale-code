package main

// 守卫：干净机器首次启动必须能走进 Onboarding 的第 2 步。
//
// 为什么需要它（2026-10-28 实测缺陷）：全新 WHALE_HOME + 无 key 时，`whale` 在
// Onboarding Step 1 就 exit 1。原因是 Step 1 复用了"完整体检"语义的 doctor ——
// 缺 API key / 缺 model 在那里被判为 FATAL，而这两项本来要到 Step 2/3 才收集。
// 用户看到的现象：装好了，敲 whale，刷完一屏诊断就退出，永远进不去。
//
// ★ 判据必须从**真实二进制**起（TestMain 用与生产相同的 `go build .`），
// 不能直调 internal/onboarding 的 step 函数 —— 那条路绕过了 main.go 的接线，
// 而缺陷就长在接线处（main.go:1369 给 doctor 传 nil/nil，即"配置尚不存在的世界"）。
//
// ★ 本文件的两条测试是**成对**的，缺一条就会丧失判别力：
// - TestOnboarding_FreshInstallReachesProviderStep：预检语境必须放宽（能往下走）
// - TestDoctor_UnconfiguredStillFatalInDiagnoseContext：诊断语境必须保持严格
// 只放宽不收紧 = 把门整个拆掉；只收紧不放宽 = 用户还是进不去。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// freshInstallEnv 构造"全新机器"的环境：没有 key、没有 model，
// HOME 与 WHALE_HOME 都指向同一个空目录。
//
// 必须连 WHALE_HOME 一起重置：它在开发者机器上常被指向真实状态目录，
// 一旦漏掉，IsCompleted() 会返回 true ⇒ 整个向导被跳过 ⇒ 判据测的就不是首启路径了。
func freshInstallEnv(homeDir string, extra ...string) []string {
	env := os.Environ()
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL",
		"WHALE_MODEL", "WHALE_FALLBACK_MODEL",
		"HOME", "USERPROFILE", "HOMEDRIVE", "HOMEPATH",
		"WHALE_HOME",
	} {
		env = dropEnvKey(env, k)
	}
	env = append(env,
		"HOME="+homeDir,
		"USERPROFILE="+homeDir,
		"WHALE_HOME="+filepath.Join(homeDir, ".whale"),
	)
	return append(env, extra...)
}

// dropEnvKey 返回移除了指定键的环境变量列表（不修改入参）。
func dropEnvKey(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// tailForReport 截取输出尾部用于失败信息（完整输出常常上百行，结论在最后）。
func tailForReport(s string, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// TestOnboarding_FreshInstallReachesProviderStep 断言全新机器能走到向导第 2 步。
//
// 两个子场景都是实测复现过的真实处境：
// - no_key_no_model —— 照着 README 装完就跑
// - key_without_model —— 已经 export 了 key，但没选过模型（最常见的中间态）
func TestOnboarding_FreshInstallReachesProviderStep(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		// wantSkipped 是"这一步还填不了、必须显示成 [SKIP]"的项。
		// 它随场景变化：已经 export 了 key 的人，API Key 那张卡就该是 [OK]。
		wantSkipped []string
	}{
		{
			name:        "no_key_no_model",
			wantSkipped: []string{"[SKIP] API Key", "[SKIP] Model"},
		},
		{
			name:        "key_without_model",
			extra:       []string{"ANTHROPIC_API_KEY=sk-probe-not-a-real-key"},
			wantSkipped: []string{"[SKIP] Model"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// 不用 t.TempDir()：被强杀的进程可能在目录里留下文件，
			// TempDir 的 RemoveAll 清理会因竞态报错，把"逻辑通过"变成整体 FAIL。
			homeDir, err := os.MkdirTemp("", "whale-fresh-"+tc.name)
			if err != nil {
				t.Fatalf("创建隔离环境失败: %v", err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
			// whalehome.Validate() 要求显式指定的 WHALE_HOME 目录**已存在**
			// （与 $HOME/.whale 自动创建不同），否则会以"配置无效"拒启 ——
			// 那是另一条路径，不是本次要测的首启向导。
			if err := os.MkdirAll(filepath.Join(homeDir, ".whale"), 0o700); err != nil {
				t.Fatalf("预建状态目录失败: %v", err)
			}

			// 向导走完后进程会进入 TUI 等待输入，本判据不依赖退出码，由 ctx 收场。
			// 12s = Step 1 网络探测上限(3s) + 其余检查 + 交互余量，够用且不白等。
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, binaryPath, "--prompt", "hello")
			cmd.Env = freshInstallEnv(homeDir, tc.extra...)
			cmd.Dir = homeDir
			// Step 2 选 Anthropic（1），Step 3 填一个假 key。
			// 走到 Step 2 就已经达到本判据的目的；后续会因为假 key 自然失败。
			cmd.Stdin = strings.NewReader("1\nsk-probe-not-a-real-key\n")

			out, _ := cmd.CombinedOutput() // 退出码不是判据（假 key 必失败），只看输出
			output := string(out)

			// 判据 1（主判据 · 用户可见现象）：能走到第 2 步。
			if !strings.Contains(output, "Step 2/4") {
				t.Fatalf("首启没能走到 Step 2 —— 用户看到的是「刷完一屏诊断就退出」\n"+
					"     生产路径：main.go:1362 → onboarding.Run → step_env.go:43 HasFatal ⇒ main.go:1374 os.Exit(1)\n"+
					"     输出尾部：\n%s", tailForReport(output, 25))
			}
			// 判据 2：不是"报完错继续跑"，而是真的没有以 fatal 收场。
			if strings.Contains(output, "environment check found fatal errors") {
				t.Fatalf("Step 1 仍以 fatal 收场 —— 向导会被 main.go:1374 的 os.Exit(1) 掐断")
			}
			// 判据 3（防"把 Step 1 掏空"）：尚未填写的项必须以 [SKIP] 的形态
			// **出现**在预检结果里，而不是消失、也不是变回 FATAL。
			// 它同时钉住两件事：Skipped 语义真的生效了，且结果真的渲染给了用户
			// （用户由此知道"这一项待会儿要填"）。
			// 若有人把 Step 1 整个删掉、或改回 Fatal，这条会红。
			for _, want := range tc.wantSkipped {
				if !strings.Contains(output, want) {
					t.Fatalf("预检没有把「尚未填写」渲染成 %q —— 判据 1 可能是靠删掉检查换来的\n"+
						"     输出尾部：\n%s", want, tailForReport(output, 25))
				}
			}
		})
	}
}

// TestDoctor_UnconfiguredStillFatalInDiagnoseContext 断言"诊断语境"没有被一起放宽。
//
// 与上面的测试成对：修复只应改变**向导预检**的语义，
// `whale doctor` / `/doctor` / `--check-api` 面对一个没配置的环境，
// 仍然必须明确报 FATAL —— 否则用户就再也找不到"我到底缺什么"的答案了。
func TestDoctor_UnconfiguredStillFatalInDiagnoseContext(t *testing.T) {
	homeDir, err := os.MkdirTemp("", "whale-doctor-unconfigured")
	if err != nil {
		t.Fatalf("创建隔离环境失败: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(homeDir) })
	if err := os.MkdirAll(filepath.Join(homeDir, ".whale"), 0o700); err != nil {
		t.Fatalf("预建状态目录失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, binaryPath, "doctor")
	cmd.Env = freshInstallEnv(homeDir)
	cmd.Dir = homeDir

	out, _ := cmd.CombinedOutput()
	output := string(out)

	if !strings.Contains(output, "[FATAL] API Key") {
		t.Fatalf("诊断语境丢了 API Key 的 FATAL —— 用户将无从知道缺了什么\n输出尾部：\n%s",
			tailForReport(output, 25))
	}
	if !strings.Contains(output, "[FATAL] Model") {
		t.Fatalf("诊断语境丢了 Model 的 FATAL —— 用户将无从知道缺了什么\n输出尾部：\n%s",
			tailForReport(output, 25))
	}
}
