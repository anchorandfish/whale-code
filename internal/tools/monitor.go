package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
	"github.com/anchorandfish/whale-code/internal/util"
)

// MonitorInput Monitor toolinput
type MonitorInput struct {
	Command        string `json:"command"`
	Description    string `json:"description,omitempty"`
	SampleInterval int    `json:"sample_interval,omitempty"` // sample interval in seconds, default 2
}

// monitorStartedResult 是 Monitor 启动成功后 tool_result 正文的**唯一文案源**。
//
// ★ 读者是**模型**，不是用户 —— 因此只写"事实 + 可执行的下一步"，且**每一个入口
// 都必须对写它的读者真的可走**（文案可执行性不变式）：
//
//	事实： id / 描述 / 命令
//	模型下一步：MonitorTail（按 monitor_id 读回尾部内容）
//	用户下一步：/monitors（列表）、/monitors stop <id>（停止）
//
// ★ 修正：修复前这里只印 `/monitors` 与 `/monitors stop <id>`
// —— 两者都是**人类斜杠命令**，模型调不到；而 sleep 门文案又主动推荐模型"用 Monitor
// 看日志"。于是模型按指引发起监控后，输出从头到尾不可读（人看得到、模型看不到）。
// 现在把两个读者**分开标注**：模型走工具 MonitorTail，用户走斜杠命令。
// （`/monitors` 与 `/monitors stop` 必须保留 —— b1_monitor_result_copy_test.go 的判据钉着它们。）
//
// 不得写**机制承诺**（"输出将实时显示" "进程结束时自动通知"）：那等于把措辞
// 喂给模型，模型会逐字复述进回答，与工具卡片 100% 重复。
//
//	实证：2026-09-30 真机 I-7.1，工具卡片第 4 行与模型回答第 2 句逐字相同
//	（内部工作回执:41-45）。
//
// 为什么必须带停止路径：后台进程的**唯一**停止出口是 `/monitors stop <id>`，
// 用户若不知道就停不下来（真机 I-7.1 的结论就是"用户无法停止"）。
func monitorStartedResult(id, description, command string) string {
	// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主。
	// 两侧同参（id, description, command, id, id）；英文主行保留模型侧两个入口原文。
	return util.BiSame(
		"📡 Monitor started (id: %s)\nDescription: %s\nCommand: %s\nRead output (model): MonitorTail {\"monitor_id\": \"%s\"}\nView (user): /monitors\nStop (user): /monitors stop %s",
		"📡 监控已启动 (id: %s)\n描述: %s\n命令: %s\n读取输出(模型): MonitorTail {\"monitor_id\": \"%s\"}\n查看(用户): /monitors\n停止(用户): /monitors stop %s",
		id, description, command, id, id,
	)
}

// monitorTailLinesLimit 是 MonitorTail 每次回显的行数上限。
//
// 与人类入口 `/monitors tail <id>`（`internal/tui/format.go:591` 的 monitorTailLines=10）
// **同构且同值**：输出是"证据"不是"陈述"，模型要的是"现在长什么样"。
// tui 不可反向 import tools（方向是 tools ← tui），故此处独立声明常量、
// 两侧注释互指 —— 若要改数，两处一起改并同步这条注释（：同一事实跨入口同源）。
const monitorTailLinesLimit = 10

// monitorTailHandler MonitorTail toolhandler（，2026-10-09）
//
// 补的是「交付」这一环：监控输出早已存在且是**权威副本**（`monitor.Process.Output`，
// 上限 DefaultMaxLines=10000；onDone 也已把全文传出，monitor.go:593），
// 缺的只是模型侧入口 —— 修复前模型没有任何工具能读到它。
//
// 方向 = **拉**（不推）。原「done 回执附尾部内容」方案**已撤回**：
// 撞已拍板原则「对话流有界、内容留权威副本、按需读取」
// （`internal/tui/render_extras.go:546-547` 明写"不再把 500 字输出塞进对话流
// （那也是第二份无界副本）"），且 onDone 是**阻塞**发送路径（monitor.go:585），
// 在其中做读内容/注入的 IO 会拖住 monitor 收敛路径。
//
// ★ 只读权威副本、不新增任何副本：GetOutput() 返回的是既有 Output 的拼接，
// 本 handler 不缓存、不落盘。
// ★ 有界：最多 monitorTailLinesLimit 行 + 总量行数（截断必须可见 —— 与 W3
// 「把输出的界变成用户可见的事实」同口径）。
func (e *Executor) monitorTailHandler() Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input struct {
			MonitorID string `json:"monitor_id"`
			WaitMs    int    `json:"wait_ms"`
		}
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.MonitorID == "" {
			return "", fmt.Errorf("monitor_id cannot be empty")
		}
		if e.MonitorMgr == nil {
			return "", fmt.Errorf("monitor manager not initialized")
		}

		proc, ok := e.MonitorMgr.Get(input.MonitorID)
		if !ok {
			return "", fmt.Errorf("monitor %s does not exist", input.MonitorID)
		}

		// ★ 2026-10-13（手测卡② §19.5 选项 B）：wait_ms —— 让"拉"自带依据。
		// 进程仍 running 时，先等到进程结束或等待预算耗尽，再组返回体 ⇒ 返回体里的
		// status/exit code 就是**终态**，模型不必盲猜时机反复拉。
		// 形态对齐 同类 CLI exec_command 的 yield_time_ms（工具结果本身含
		// "finished during this call" → exit_code / "still running" → session_id）。
		// 只读路径：不新增副本、不改进程状态（WaitForDone 只轮询 GetStatus）。
		if input.WaitMs > 0 && proc.GetStatus() == monitor.StatusRunning {
			proc.WaitForDone(time.Duration(input.WaitMs) * time.Millisecond)
		}

		total := proc.GetLines()
		var lines []string
		if text := strings.TrimRight(proc.GetOutput(), "\n"); text != "" {
			lines = strings.Split(text, "\n")
		}
		tail := lines
		if len(tail) > monitorTailLinesLimit {
			tail = tail[len(tail)-monitorTailLinesLimit:]
		}

		return fmt.Sprintf("monitor %s [%s] exit=%d\nlines: %d total, showing last %d\n----\n%s",
			input.MonitorID, proc.GetStatus(), proc.GetExitCode(), total, len(tail), joinLines(tail)), nil
	}
}

// monitorHandler 返回 Monitor 工具处理器
// 启动后台进程监控，流式输出，进程结束时通知 AI
//
// C-P0-2 修复：启动前调用 prepareSandbox 验证命令（deny 列表拦截），
// 沙箱化时通过 WithSandbox 注入完整沙箱链路（Restrict → Start → PostStartHook）。
func (e *Executor) monitorHandler(workDir string) Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var input MonitorInput
		if err := parseInput(raw, &input); err != nil {
			return "", fmt.Errorf("resolveinputfailed: %w", err)
		}
		if input.Command == "" {
			return "", fmt.Errorf("%s", util.Bi("command cannot be empty", "命令不能为空"))
		}

		if e.MonitorMgr == nil {
			return "", fmt.Errorf("%s", util.Bi("monitor manager is not initialized", "监控管理器未初始化"))
		}

		// C-P0-2：沙箱验证 — 优先使用 Manager.Prepare 模式，回退到 legacy Sandbox.Validate
		sbCtx, cleanup, sandboxed, sandboxErr := e.prepareSandbox(ctx, input.Command, workDir)
		if sandboxErr != nil {
			// 软沙箱拒绝（S11 G1-B 定型，G1-A 收口）：文案不变（模型契约），
			// 以 errSandboxDeniedSoft 让 executeDirect 打上 FailureKind=SandboxDenied，
			// 供编排/TUI 的终局拒绝回路与 R4 结构化判据识别。
			return fmt.Sprintf("sandbox denied: %s", sandboxErr.Error()), errSandboxDeniedSoft
		}

		description := input.Description
		if description == "" {
			description = input.Command
			if len(description) > 50 {
				description = description[:50] + "..."
			}
		}

		// detect sleep pattern, guide to use bash run_in_background
		// ★ ：上界取自 monitor.MaxAllowedSleepSeconds（经 SleepBlock.LimitSeconds），
		// 本处**不得**写死秒数 —— 写死过一次，与判定差 1 秒
		// （"保持在 2 秒以内" 而 `sleep 2` 恰是被拦的边界值 ⇒ 照做再被拒）。
		if sb, blocked := monitor.DetectBlockedSleep(input.Command); blocked {
			if cleanup != nil {
				cleanup()
			}
			return "", fmt.Errorf("%s", util.BiSame(
				"Blocked: %s. To wait for a command to finish, use the bash tool's run_in_background option; "+
					"for streaming monitoring (e.g. tailing logs, polling an API), use the Monitor tool; "+
					"if a delay is truly needed (e.g. rate limiting), keep it at %d seconds or below",
				"已阻止: %s。如需等待命令完成，请使用 bash 工具的 run_in_background 选项；"+
					"如需流式监控（如查看日志、轮询 API），请使用 Monitor 工具监控输出；"+
					"如确实需要延迟（如限流等），请保持在 %d 秒及以下",
				sb.Reason, sb.LimitSeconds,
			))
		}

		// convert command on Windows
		commandToRun := input.Command
		if runtime.GOOS == "windows" {
			commandToRun = convertLinuxToPowerShell(input.Command)
		}

		// C-P0-2：构造沙箱配置（sandboxed 时注入完整沙箱链路）
		var startOpts []monitor.StartOption
		if sandboxed {
			sbCfg := monitor.SandboxConfig{
				Cleanup: cleanup,
			}
			if sbCtx.WorkDir != "" {
				sbCfg.WorkDir = sbCtx.WorkDir
			}
			if len(sbCtx.Env) > 0 {
				sbCfg.Env = sbCtx.Env
			}
			if osSb := sbCtx.OSSandbox(); osSb != nil {
				sbCfg.RestrictFunc = osSb.Restrict
			}
			if sbCtx.PostStartHook != nil {
				sbCfg.PostStartHook = sbCtx.PostStartHook
			}
			startOpts = append(startOpts, monitor.WithSandbox(sbCfg))
		} else if cleanup != nil {
			// 非 sandboxed 但有 cleanup（理论上不应出现，防御性处理）
			startOpts = append(startOpts, monitor.WithSandbox(monitor.SandboxConfig{
				Cleanup: cleanup,
			}))
		}

		// ★ 续：用户已**逐条批准**本条命令的沙箱可批准档。
		//
		// 上面 `prepareSandbox` 已按 sandbox 规则引擎放行（`sandboxed=false` ⇒ 沙箱外执行），
		// 但 `MonitorMgr.Start` 内部**另有一道 `security.CheckCommand`**（bash 的执行链没有
		// 这一道），用的是另一套引擎 ⇒ `$()` 这类可批准档会被它按 L1 硬拒，
		// 表现为用户点了允许仍 `startmonitoringfailed`。这里把"已获用户批准"这一事实
		// 下传给 Manager，让它让路 —— 与 bash 的 `prepareSandbox` 同源。
		//
		// 判据是**已批准事实**本身（`sandboxSanctioned(ctx)`），不是"命令看起来危险不危险"；
		// 硬拒档在 `SandboxPrecheckStage` 已短路，到不了这里。
		if sandboxSanctioned(ctx) {
			startOpts = append(startOpts, monitor.WithUserApproval())
		}

		proc, err := e.MonitorMgr.Start(commandToRun, description, workDir, startOpts...)
		if err != nil {
			// 修复 B3a-P1-2：Start 失败时无论 sandboxed 值均调用 cleanup。
			// 原实现仅非 sandboxed 时调用 cleanup，sandboxed 模式下 cleanup 泄漏。
			// sandboxed 时 cleanup 虽已注入 SandboxConfig，但 Start 失败意味着进程未启动，
			// SandboxConfig.Cleanup 不会被触发，需在此显式调用。
			if cleanup != nil {
				cleanup()
			}
			return "", fmt.Errorf("startmonitoringfailed: %w", err)
		}

		return monitorStartedResult(proc.ID, description, input.Command), nil
	}
}
