// g_gg_g1_permission_flag_test.go — G-G（O-G）G1 判别性测试：
// `whale --permission <mode>` 必须被当作**显式 TUI 选项**，不得落进 handleArgs 首步退出。
//
// 决策依据：集中拍板_决策表与并行修复分组_2026-09-14.md
//
//	§2.7 顺带发现 G1：
//
// `whale --permission <mode>` 是**死参数**（`main.go:228-241` 只 setenv、漏设 `startTUI`）
// → **修**（属 main.go，独立小轮）—— 所有用 `--permission` 的手测脚本都会首步退出。
//
//	§6.3 G-G 行：「G1 = `main.go:228-241` `--permission` 死参数（只 setenv 漏设 `startTUI`）」
//
// 改码前实测（真二进制 `/tmp/gg/whale_before`，2026-09-15 01:5x，干净 HOME）：
//
//	$ whale --permission read-only
//	Whale Code v0.1.0
//	Unknown argument: --permission
//	Use whale --help for help ← 首步退出，TUI 从未启动，模式也从未生效
//
// 断言走**生产入口**：构建真实 `main` 包二进制 → 以真实 argv 运行 → 观察终端输出。
// 不用任何内部辅助函数装配（R2：红测必须落在生产入口层）。
// 期望（Fix 后）：输出**不含** `Unknown argument`，且**越过参数解析**走到启动阶段
// —— 无 API Key 时统一输出 `API Key: NOT SET`（复用 integration_test.go 的
// hasMissingAPIKeyOutput 判据，与 TestParseArgs_* 同族口径）。
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var (
	ggG1BinOnce sync.Once
	ggG1BinPath string
	ggG1BinErr  error
)

// ggG1TestBinary 构建一次真实 main 包二进制并在本包测试内复用。
// 用默认 env 构建（可继承 GOFLAGS —— A/B 时靠它注入 -overlay），
// 运行时再换干净 env（与 TestParseArgs_* 的做法一致）。
func ggG1TestBinary(t *testing.T) string {
	t.Helper()
	ggG1BinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "whale-gg-g1-")
		if err != nil {
			ggG1BinErr = err
			return
		}
		bin := filepath.Join(dir, "whale-gg-g1")
		cwd, _ := os.Getwd()
		cmd := exec.Command("go", "build", "-o", bin, ".")
		cmd.Dir = cwd
		if out, berr := cmd.CombinedOutput(); berr != nil {
			ggG1BinErr = fmt.Errorf("构建 main 包失败: %v\n%s", berr, out)
			return
		}
		ggG1BinPath = bin
	})
	if ggG1BinErr != nil {
		t.Fatalf("%v", ggG1BinErr)
	}
	return ggG1BinPath
}

// ggG1Run 以干净环境运行被测二进制，返回 (combined output, exit code)。
func ggG1Run(t *testing.T, bin, tmpDir string, args ...string) (string, int) {
	t.Helper()
	env := buildCleanEnv(tmpDir)
	// 避免宿主环境里残留的 WHALE_PERMISSION_MODE 干扰"本 flag 是否生效"的判定。
	env = removeEnv(env, "WHALE_PERMISSION_MODE")

	cmd := exec.Command(bin, args...)
	cmd.Dir = tmpDir
	cmd.Env = env
	out, _ := cmd.CombinedOutput()

	code := 0
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	return string(out), code
}

// TestGG_G1_PermissionFlagIsRecognizedAndProceedsToStartup —— G1 主语义（before 态必须红）。
//
// 同时覆盖 `--permission <mode>` 与 `--permission=<mode>` 两种形态（main.go 的
// 注释声明支持两种，故两者都必须被识别）。
func TestGG_G1_PermissionFlagIsRecognizedAndProceedsToStartup(t *testing.T) {
	bin := ggG1TestBinary(t)

	for _, args := range [][]string{
		{"--permission", "read-only"},
		{"--permission=read-only"},
	} {
		label := strings.Join(args, " ")
		t.Run(label, func(t *testing.T) {
			tmpDir := t.TempDir()
			out, _ := ggG1Run(t, bin, tmpDir, args...)

			// ① 用户可见现象：修复前正是这三行"首步退出"。
			if strings.Contains(out, "Unknown argument") {
				t.Errorf("G1：`whale %s` 应被识别为显式 TUI 选项，实际落入 handleArgs：\n%s", label, out)
			}
			if strings.Contains(out, "Use whale --help for help") {
				t.Errorf("G1：`whale %s` 走到了 handleArgs 的兜底分支（首步退出）：\n%s", label, out)
			}
			// ② 必须真的"越过了参数解析"：进到配置加载之后的启动阶段。
			// 判据与 TestParseArgs_* 同族（无 Key 时统一报缺 Key），不是自造标准。
			if !hasMissingAPIKeyOutput(out) {
				t.Errorf("G1：`whale %s` 应继续走到启动阶段（无 API Key ⇒ 报 'API Key: NOT SET'），实际输出：\n%s", label, out)
			}
		})
	}
}

// TestGG_G1_InvalidPermissionModeStillRejected —— 反例守卫（判别性，防越界）。
//
// 修复只应让**合法**模式通过；非法模式必须仍然在参数解析阶段就被拒并**非零退出**
// （main.go 的 isValidPermissionMode 校验，是"不把坏值塞进 env"的门）。
// 若修复顺手删掉/绕过该校验，本用例变红。
//
// ⚠️ 本用例**不断言提示文案** —— 实测发现：`fmt.Fprintf(os.Stderr, "invalid permission mode…")`
// 写的是 os.Stderr，而 main 顶部（main.go:102-108）在**非 debug** 模式已把它重定向到
// `~/.whale/logs/whale.log` ⇒ 该提示在终端**不可见**。这是**既有**缺陷（不属 §2 任何决策项，
// 本轮按硬规则 3 不修；已登记在 内部状态清单 §诚实边界）。
// 因此本用例只断言可判定的两个信号：非零退出 + 未进入启动阶段。
func TestGG_G1_InvalidPermissionModeStillRejected(t *testing.T) {
	bin := ggG1TestBinary(t)
	tmpDir := t.TempDir()

	out, code := ggG1Run(t, bin, tmpDir, "--permission", "definitely-bogus")

	if code == 0 {
		t.Errorf("G1 不得越界：非法模式必须以非零码退出，实际 exit=%d；输出：\n%s", code, out)
	}
	// 非法值绝不能被"宽容地"继续用于启动。
	if hasMissingAPIKeyOutput(out) {
		t.Errorf("G1 不得越界：非法模式不得继续走到启动阶段：\n%s", out)
	}
	if strings.Contains(out, "Unknown argument") {
		t.Errorf("G1 不得越界：非法模式应在参数解析阶段就被拒，而不是落到 handleArgs：\n%s", out)
	}
}
