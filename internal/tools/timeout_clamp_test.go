// 判别器（2026-09-20 批 D 落盘）。
//
// 缺陷：`bash` 超时**无上界 clamp**（`input.Timeout = 36000` 原样生效）；
// `run_in_background` 不填 timeout ⇒ `WithCancel(Background())` = **永不过期**。
//
// ★ 判据三分工（为什么这么写）：
// - 纯函数判据（结构面）：`ClampTimeoutSeconds` 的取值面 —— 可判别、反例必红；
// - 端到端判据（行为面）：clamp 事实**可观测**（bash 返回文案 / 后台 task 的生效超时字段）；
// - **配对反证**：显式小超时 ⇒ **不得**出现 clamp 标记（证明该标记不是恒真装饰）。
package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

// TestN079_ClampTimeoutSeconds 是「上界真的存在」的结构面判据。
//
// ★ 为什么不能只测「设了小超时能生效」：那只证明**下界**，对「无上界」缺陷恒绿。
func TestN079_ClampTimeoutSeconds(t *testing.T) {
	cases := []struct {
		name                string
		requested, def, max int
		want                int
	}{
		{"未指定走默认", 0, 120, 7200, 120},
		{"负值走默认", -5, 120, 7200, 120},
		{"正常值原样", 30, 120, 7200, 30},
		{"恰为上界", 7200, 120, 7200, 7200},
		{"上界加一被 clamp", 7201, 120, 7200, 7200},
		{"10 小时被 clamp", 36000, 120, 7200, 7200},
		{"后台默认 600", 0, 600, 7200, 600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampTimeoutSeconds(tc.requested, tc.def, tc.max); got != tc.want {
				t.Errorf("ClampTimeoutSeconds(%d,%d,%d) = %d, want %d",
					tc.requested, tc.def, tc.max, got, tc.want)
			}
		})
	}
}

// TestN079_BashClampIsObservable 端到端：超上界请求必须**可观测地被 clamp**。
//
// 反例注入：删掉 bash.go 的 `ClampTimeoutSeconds`（改回 `if timeout <= 0 { timeout = 120s }`）
// ⇒ ① 崩溃（timeoutClamped 变量不存在）或 ② 标记不出现 ⇒ **必红**。
func TestN079_BashClampIsObservable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("走 sh；Windows 侧走 powershell，本批未手测")
	}
	e := NewExecutor(NewRegistry(), t.TempDir())

	// ① 请求 10h ⇒ 必须报「被 clamp 到 7200」
	res := e.Execute(context.Background(), Call{
		ID:   "n079-clamp",
		Name: "bash",
		Input: mustMarshal(map[string]interface{}{
			"command": "echo n079",
			"timeout": 36000,
		}),
	})
	wantMark := "[timeout clamped: 36000s -> 7200s]"
	if !strings.Contains(res.Output, wantMark) {
		t.Errorf("请求 timeout=36000 必须被 clamp 且可观测：期望输出含 %q，实际输出 = %q",
			wantMark, res.Output)
	}

	// ② **配对反证**：显式小超时 ⇒ 不得出现 clamp 标记（证明标记不是恒真）。
	res2 := e.Execute(context.Background(), Call{
		ID:   "n079-noclamp",
		Name: "bash",
		Input: mustMarshal(map[string]interface{}{
			"command": "echo n079b",
			"timeout": 2,
		}),
	})
	if strings.Contains(res2.Output, "timeout clamped") {
		t.Errorf("timeout=2 不该触发 clamp，实际输出 = %q", res2.Output)
	}
}

// TestN079_BackgroundTaskHasBoundedDefaultTimeout 端到端：后台任务**默认不再永久**。
//
// 判据落在**结构化可观测量** `BackgroundTask.TimeoutSeconds`（不是读代码、也不是散文）。
// 反例注入：把 `Create` 改回 `if timeout > 0 { WithTimeout }`（0 ⇒ 永久）⇒ 本测试必红。
func TestN079_BackgroundTaskHasBoundedDefaultTimeout(t *testing.T) {
	e := NewExecutor(NewRegistry(), t.TempDir())

	// ① 不填 timeout ⇒ 必须有默认（非永久）
	def := e.TaskMgr.Create("sleep 1", "n079 default", 0)
	if def.TimeoutSeconds != DefaultBackgroundTimeoutSeconds {
		t.Errorf("后台任务不填 timeout 时生效超时 = %d, want %d（修复前为 0 = 永不过期）",
			def.TimeoutSeconds, DefaultBackgroundTimeoutSeconds)
	}

	// ② 超上界 ⇒ clamp
	big := e.TaskMgr.Create("sleep 1", "n079 clamp", 36000)
	if big.TimeoutSeconds != MaxBackgroundTimeoutSeconds {
		t.Errorf("后台任务 timeout=36000 生效超时 = %d, want %d（未 clamp）",
			big.TimeoutSeconds, MaxBackgroundTimeoutSeconds)
	}

	// ③ **配对反证**：显式且在界内的超时 ⇒ 原样保留（证明「恒为默认值」不是修复方式）
	explicit := e.TaskMgr.Create("sleep 1", "n079 explicit", 5)
	if explicit.TimeoutSeconds != 5 {
		t.Errorf("后台任务 timeout=5 生效超时 = %d, want 5", explicit.TimeoutSeconds)
	}
}
