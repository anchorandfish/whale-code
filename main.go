package main

import (
	"bufio"

	"context"
	"errors"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"io"
	stdlog "log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/doctor"
	"github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/metrics"
	"github.com/anchorandfish/whale-code/internal/migrate"
	"github.com/anchorandfish/whale-code/internal/onboarding"
	"github.com/anchorandfish/whale-code/internal/orchestrator"
	"github.com/anchorandfish/whale-code/internal/sandbox"
	"github.com/anchorandfish/whale-code/internal/session"
	sig "github.com/anchorandfish/whale-code/internal/signal"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/tui"
	"github.com/anchorandfish/whale-code/internal/util"
	versionpkg "github.com/anchorandfish/whale-code/internal/version"
)

// version 的唯一真源是 internal/version.Version（见该包注释）。
// 本包不再声明自己的 version，避免 --version / help / 横幅各读一份而出现分叉。
var (
	terminalStderr = os.Stderr
)

// reportSessionInUse 在 err 属于「会话已被另一个窗口占用」时打印面向用户的
// 双语提示并返回 true；其余错误返回 false，由调用方按原有方式报错退出。
//
// 为什么被占用必须**拒绝启动**而不是继续：继续意味着第二个窗口会往同一份 transcript
// 追加，两段对话按写入时刻交错成一段前后不接的对话（探针实测「被拒进程数=0、40 条
// 全落盘但互相交错」），而且两个窗口的内存上下文互相看不到对方的新消息。
// 成功的判据不是"没报错"，而是**只有一个窗口在写**。
//
// 文案取自 session.SessionInUseError.UserMessage（唯一实现，与 TUI 内 /resume ·
// /session switch 共用同一份）；本函数只负责「打到 stderr」。
func reportSessionInUse(err error) bool {
	var inUse *session.SessionInUseError
	if !errors.As(err, &inUse) {
		return false
	}
	fmt.Fprintln(os.Stderr, inUse.UserMessage())
	return true
}

func main() {
	// 沙箱 re-exec helper 分发（唯一实现点 = sandbox.HandleSandboxHelperArg）。
	//
	// restrict 把命令最外层改写为
	//
	//	<exe> __whale-sandbox-exec -- <cmd> <args...> （linux pre-exec：Landlock + seccomp）
	//	<exe> __whale-rlimit -- sandbox-exec ... （darwin：setrlimit 后 exec）
	//
	// 该子进程在此进入 helper 分支，不走正常 CLI 初始化（日志/配置/TUI）。helper 不返回。
	//
	// ★ ：分发同时由 internal/sandbox 的 init 自动完成（helper_dispatch.go）——
	// 测试二进制没有 main.go，只靠这里会递归重跑整个测试套件并挂死。此处保留一次显式调用，
	// 作为"生产二进制在哪里进入 helper"的可读入口；两处都委派给同一函数，不会分歧。
	sandbox.HandleSandboxHelperArg(os.Args)

	// D4 修复：panic 恢复，确保栈信息写到恢复后的 stderr（fd 2）。
	// os.Stderr 可能已被重定向到日志文件，panic 时需重建 fd 2 让栈信息直接写终端（/exit 后可见）。
	defer func() {
		if r := recover(); r != nil {
			os.Stderr = os.NewFile(2, "stderr")
			fmt.Fprintf(os.Stderr, "panic: %v\n%s", r, debug.Stack())
			os.Exit(1)
		}
	}()

	// 性能计时点（路径1-启动）：记录程序启动时刻，供后续各阶段耗时计算。
	// 仅 DEBUG 级别输出，默认 Info 级别下被过滤，不影响生产吞吐。
	programStart := time.Now()
	setupConsoleEncoding()

	// 方案C：WHALE_HOME fail-fast（对标 CODEX_HOME：设了就必须可用）。
	// ★ 必须在 loadEnvFiles/initFileLog 之前——否则状态目录搬家后，日志/配置仍写旧路径。
	// 语义：宁可拒绝启动，也不静默写一个不可用的状态目录
	// （那正是 「无声攒 59 天」的根源）。
	if err := whalehome.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%s\n", util.BiSame(
			"⚠️ Invalid WHALE_HOME, refusing to start: %v",
			"⚠️ WHALE_HOME 无效，拒绝启动：%v",
			err))
		os.Exit(1)
	}

	loadEnvFiles()

	// D4 修复：在 initFileLog 之前解析 --debug 标志，
	// 使 SetDebugMode 在 InitFileLogger 构建首个 slog handler 时生效。
	// debug 模式：slog 双写 stderr + 文件（便于开发期实时查看日志）。
	// 非 debug 模式：slog 只写文件 + stderr 重定向到日志文件（避免日志泄漏到 TUI 下方）。
	debugMode := false
	for _, arg := range os.Args[1:] {
		if arg == "--debug" {
			debugMode = true
			log.SetDebugMode(true)
			break
		}
	}

	logFilePath := initFileLog() // 5-A: stderr 之外双写 ~/.whale/logs/whale.log（lumberjack 轮转）
	defer log.CloseFileLogger()

	// D4 修复：非 debug 模式重定向 stderr 到日志文件。
	// 影响：(1) 标准库 log.Printf（magic_docs 等用 stdlog，需 stdlog.SetOutput 显式重定向，
	// 因 stdlog 在包初始化时绑定 os.Stderr 指针，os.Stderr = file 不影响已创建 logger）。
	// (2) fmt.Fprintf(os.Stderr, ...)（每次读 os.Stderr 变量，受重定向影响）。
	// panic 栈由 Go runtime 直接写 fd 2（终端），不受 os.Stderr 重定向影响 → /exit 后可见。
	if !debugMode && logFilePath != "" {
		if stderrFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err == nil {
			os.Stderr = stderrFile
			stdlog.SetOutput(stderrFile)
			defer stderrFile.Close()
		}
	}

	continueSession := false
	forkSession := false
	forkSessionID := ""
	// sessionResumeID 是 --session/-s 点名的会话标识（UUID / UUID 前缀 /
	// 可读别名 / 旧式文件名，解析规则见 session.Store.Resolve）。
	sessionResumeID := ""
	promptMode := false
	ciMode := false
	ciPromptStdin := false
	var promptArgs []string
	bridgeContinue := false
	enablePlugins := false      // P0-7/B7: 默认禁用插件，需 --enable-plugins 显式启用
	disableMetricsAuth := false // M22-05 P0: 默认启用鉴权，--disable-metrics-auth 仅本地调试
	// macOS 终端优先保留原生拖选 + Command+C；AI 活动期间临时启用
	// 鼠标报告以支持 Stop，结束后自动恢复原生选择。Linux/Windows 保持
	// 鼠标捕获默认开启，可用 --no-mouse 显式切换到终端原生选择。
	// 终端能力开关（--mouse / --no-mouse / --alt-screen / --no-alt-screen）统一由
	// parseTerminalFlags 解析：本地 TUI 与 SSH 两条路径共用**一份**实现，避免双实现漂移。
	termFlags := parseTerminalFlags(os.Args[1:])
	mouseCapture, autoActiveMouse := termFlags.mouseCapture, termFlags.autoActiveMouse
	altScreen := termFlags.altScreen
	// 显式 TUI 选项（例如 --mouse / --no-alt-screen）不应落入 handleArgs。
	startTUI := termFlags.explicit

	if len(os.Args) > 1 {
		// whale doctor [--network|--update-check|--config-migrate|--repair|--lock-test|--resource] — CLI 诊断模式
		// V3-5-1/V3-4-1/V3-1-2/V3-1-3/V3-5-3/V3-4-2：补接缺失的 doctor CLI 入口，使 checkRecovery 的
		// FixSuggest 提示 "whale doctor --repair" 真正可用，并支持单项诊断子参数。
		if os.Args[1] == "doctor" || os.Args[1] == "--doctor" {
			runDoctorMode(os.Args[2:])
			return
		}

		// whale migrate --from 同类 CLI|同类 CLI — 从竞品迁移配置
		// V3-7-1：补接缺失的 migrate CLI 入口，使 internal/migrate 包对外可用。
		if os.Args[1] == "migrate" {
			runMigrateMode(os.Args[2:])
			return
		}

		for i, arg := range os.Args[1:] {
			// 终端能力开关：取值已由上面的 parseTerminalFlags 统一决定，
			// 这里只把参数**消费掉**（continue），避免它落入下面的 handleArgs
			// 被当成未知参数而首步退出（那会让 --no-alt-screen 形同虚设）。
			if isTerminalFlag(arg) {
				continue
			}
			if arg == "--plain" {
				runPlainMode()
				return
			}
			if arg == "--bridge" {
				// check if --continue follows
				if i+1 < len(os.Args[1:]) && (os.Args[1:][i+1] == "--continue" || os.Args[1:][i+1] == "-c") {
					bridgeContinue = true
				}
				runBridgeMode(bridgeContinue)
				return
			}
			if arg == "--daemon" {
				runDaemonMode()
				return
			}
			if arg == "--check-api" {
				checkAPI()
				return
			}
			if arg == "-c" || arg == "--continue" {
				continueSession = true
				continue
			}
			// --session <id> / -s <id> / --session=<id>：启动时直接加载指定会话。
			//
			// 存在意义：-c 只能恢复「最近一个」，多开场景下用户需要点名某个
			// 具体会话（例如两个终端各接续一个对话）。没有它就只能先起 TUI
			// 再敲 /resume，命令行/脚本无法定位会话。
			// 命名与同类 CLI 的「点名恢复会话」习惯对齐（`resume <id>`），
			// 此处用 --session 以区别于已有的 --fork-session。
			{
				args := os.Args[1:]
				next := ""
				if i+1 < len(args) {
					next = args[i+1]
				}
				val, matched, perr := parseSessionFlag(arg, next)
				if matched {
					if perr != nil {
						fmt.Fprintf(os.Stderr, "%v\n", perr)
						os.Exit(1)
					}
					sessionResumeID = val
					continue
				}
			}
			if arg == "--fork-session" {
				forkSession = true
				// support specifying session ID: --fork-session <session-id>
				if i+1 < len(os.Args[1:]) && !strings.HasPrefix(os.Args[1:][i+1], "-") {
					forkSessionID = os.Args[1:][i+1]
				}
				continue
			}
			if arg == "-p" || arg == "--prompt" {
				promptMode = true
				if i+1 < len(os.Args[1:]) {
					promptArgs = os.Args[1:][i+1:]
				}
				break
			}
			if arg == "--ci" {
				ciMode = true
				if i+1 < len(os.Args[1:]) {
					promptArgs = os.Args[1:][i+1:]
				}
				break
			}
			if arg == "--ci-prompt-stdin" {
				ciMode = true
				ciPromptStdin = true
				break
			}
			if arg == "--enable-plugins" {
				enablePlugins = true
				continue
			}
			// M22-05 P0：--disable-metrics-auth flag 仅用于本地调试，禁用 Bearer Token 鉴权与 127.0.0.1 强制
			if arg == "--disable-metrics-auth" {
				disableMetricsAuth = true
				continue
			}
			// D4 修复：--debug 已在 initFileLog 之前提前解析，此处跳过避免重复处理。
			if arg == "--debug" {
				continue
			}
			// R14 P0-17：--permission flag 设置权限模式（支持 --permission=xxx 和 --permission xxx 两种形式）
			if arg == "--permission" || strings.HasPrefix(arg, "--permission=") {
				mode := ""
				if strings.HasPrefix(arg, "--permission=") {
					mode = strings.TrimPrefix(arg, "--permission=")
				} else if i+1 < len(os.Args[1:]) {
					mode = os.Args[1:][i+1]
				}
				if !isValidPermissionMode(mode) {
					fmt.Fprintf(os.Stderr, "invalid permission mode: %s (valid: read-only/workspace-write/danger-full-access)\n", mode)
					os.Exit(1)
				}
				os.Setenv("WHALE_PERMISSION_MODE", mode)
				// G1（§2.7）：--permission 是**显式 TUI 选项**，必须与 --mouse/--no-mouse 同类，
				// 否则下面的 `!startTUI` 判据会把 `whale --permission <mode>`（单独使用时
				// 四个标志全为 false）误导进 handleArgs ⇒ 打印 "Unknown argument: --permission"
				// 后首步退出，参数形同虚设（所有用 --permission 的手测/脚本都被静默打断）。
				startTUI = true
				continue
			}
		}
		if !continueSession && !forkSession && sessionResumeID == "" && !promptMode && !ciMode && !startTUI {
			handleArgs(os.Args[1:])
			return
		}
	}

	// CI 模式必须保持非交互：首次运行时不能进入 Onboarding，否则无人值守
	// 任务会卡在交互流程或输出与 CI 契约无关的诊断报告。
	cfg := loadConfigWithOnboarding(!ciMode)

	// 性能计时点（路径1-启动）：配置加载完成（含 env + Onboarding + settings.json）。
	// 输出"启动 → 配置加载完成"阶段耗时。
	configDone := time.Now()
	log.Debug("[perf] startup: config loaded",
		"phase", "config_load",
		"elapsed_ms", configDone.Sub(programStart).Milliseconds(),
	)

	// V3-5-3：多实例检测（警告式，不阻止启动）。
	// 在 loadConfig 之后、TUI 启动之前获取实例锁，检测是否已有 whale 实例运行。
	// 检测到前序实例时仅输出 stderr 警告（不阻塞），由用户决定是否继续多开。
	// 收口：Dir() 返回空（home 不可得）⇒ 跳过多实例检测，行为同前。
	if whalehome.Dir() != "" {
		instanceLock := util.NewInstanceLock(filepath.Join(whalehome.Dir(), ".lock"))
		if existingPID, acqErr := instanceLock.Acquire(); acqErr == nil && existingPID > 0 {
			log.Warn("检测到另一个 whale 实例正在运行", "pid", existingPID)
			// ★ ：双语 SSOT 形态（util.BiSame）—— 修前中文为主。
			// 中文辅行保留「检测到另一个 whale 实例」，e2e 断言可按需钉该子串。
			fmt.Fprint(os.Stderr, util.BiSame(
				"⚠️ Another whale instance is already running (PID: %d)\n  Concurrent writes to ~/.whale/ may cause data conflicts\n  If you must run multiple instances, use different projects",
				"⚠️ 检测到另一个 whale 实例正在运行 (PID: %d)\n多实例并发写 ~/.whale/ 可能导致数据冲突\n如确需多开，请确保操作不同项目",
				existingPID)+"\n")
		}
		defer instanceLock.Release()
	} else {
		log.Warn("多实例检测跳过：无法确定 whale 状态目录")
	}

	// P0-7/B7: --enable-plugins flag 覆盖配置文件，显式启用插件加载
	if enablePlugins {
		cfg.PluginsEnabled = true
	}

	// 5-E-1：在 createClient 之前注入用户自定义定价表到 ai 全局查询路径。

	// 5-E-3：StartMetricsServer 返回 *http.Server 供退出时 Shutdown。
	// M22-05 P0：传入 ServerOptions（Addr + AuthToken + DisableAuth），支持 Bearer Token 鉴权与 127.0.0.1 默认绑定。
	var metricsSrv *http.Server
	if cfg.MetricsEnabled {
		metricsSrv = metrics.StartMetricsServer(metrics.ServerOptions{
			Addr:        cfg.MetricsAddr,
			AuthToken:   cfg.MetricsAuthToken,
			DisableAuth: disableMetricsAuth,
		})
	}

	// 在模型名解析前检查 API Key：loadOrCreateModelRegistry 内部会调用 resolveModel，
	// 若未配置模型名会先于 createClient 退出并输出 "No model name set"，掩盖真正根因
	// （缺少 API Key）。前置此检查使无 Key 场景统一输出 "No API Key set"，
	// 与 createClient / checkAPI 行为一致，便于用户定位配置缺失。
	ensureAPIKeyOrExit(cfg)

	registry := loadOrCreateModelRegistry(cfg)
	client := createClient(cfg)

	if ciMode {
		prompt := strings.Join(promptArgs, " ")
		if ciPromptStdin {
			var readErr error
			prompt, readErr = readCIPrompt(os.Stdin)
			if readErr != nil {
				fmt.Fprintf(os.Stderr, "failed to read CI prompt from stdin: %v\n", readErr)
				shutdownMetrics(metricsSrv)
				os.Exit(2)
			}
		}
		runCIMode(cfg, client, registry, prompt)
		shutdownMetrics(metricsSrv)
		return
	}

	model := tui.NewModel(cfg, client, registry)
	model.SetVersion(versionpkg.Version)
	model.SetMouseMode(mouseCapture, autoActiveMouse)

	// H10 Phase 6: 创建并注入编排器到 TUI Model（S4 接线）。
	// 失败时不阻塞 TUI 启动，仅记录警告（/orchestrate 命令将返回未初始化提示）。
	setupOrchestrator(&model, client)

	// --session 的互斥/组合规则（在真正改动 model 之前一次性判定，避免
	// 「先恢复 A 再被 B 覆盖」这种半途生效）。
	if sessionResumeID != "" && continueSession {
		fmt.Fprintln(os.Stderr, "--session cannot be combined with -c/--continue (both name a session to open)")
		shutdownMetrics(metricsSrv)
		os.Exit(1)
	}
	if sessionResumeID != "" && forkSession {
		// --session <id> --fork-session = 分叉**指定**会话（而非最近会话）。
		forkSessionID = sessionResumeID
		sessionResumeID = ""
	}

	if continueSession {
		// 装载失败只可能是「被另一个窗口占用」——存储故障在
		// ResumeLastSession 内部已按既有行为静默跳过（只记日志）。
		// 被占用必须**拒绝启动**：放第二个窗口进来 = 两段对话写进同一份记录。
		if err := model.ResumeLastSession(); err != nil {
			if !reportSessionInUse(err) {
				fmt.Fprintf(os.Stderr, "failed to resume last session: %v\n", err)
			}
			shutdownMetrics(metricsSrv)
			os.Exit(1)
		}
	}

	if sessionResumeID != "" {
		// 点名恢复：失败必须报错退出，不得静默新开一个会话 —— 用户以为接上了
		// 旧对话、实际另起一份，是比报错更糟的失败模式。
		if err := model.ResumeSessionByID(sessionResumeID); err != nil {
			if !reportSessionInUse(err) {
				fmt.Fprintf(os.Stderr, "failed to resume session %q: %v\n", sessionResumeID, err)
			}
			shutdownMetrics(metricsSrv)
			os.Exit(1)
		}
	}

	if forkSession {
		if forkSessionID != "" {
			model.ForkSessionByID(forkSessionID)
		} else {
			model.ForkLastSession()
		}
	}

	if promptMode && len(promptArgs) > 0 {
		model.SendPrompt(strings.Join(promptArgs, " "))
	}

	// 桶 X1：WithFPS(120) 提升渲染帧率上限（对标同类 CLI frame_rate_limiter 120FPS）。
	// 鼠标模式按平台选择：macOS 空闲态保留终端原生拖选，AI 活动时临时捕获
	// 鼠标以支持 Stop；Linux/Windows 默认捕获滚轮和点击，--no-mouse 可切回
	// 终端原生选择。该开关只改变鼠标报告，不影响 Ctrl+C 中断和应用内复制命令。
	//
	// Windows 下不使用 tea.WithInputTTY()（2026-07-31 第4轮乱码根治）：
	// WithInputTTY 会打开 CONIN$ 新句柄（Fd ≠ os.Stdin.Fd()），导致 bubbletea 跳过
	// coninput 读取器（ReadConsoleInputW 宽字符版），回退到 ANSI 读取器路径。
	// 该路径经过 go-localereader，它在 Windows 上硬编码用 CP_ACP(936/GBK) 解码输入，
	// 但 SetConsoleCP(65001) 让 IME 发送 UTF-8 字节 → UTF-8 被当 GBK 解码 → "你好"→"浣犲ソ"。
	// 不用 WithInputTTY 时，bubbletea 用 os.Stdin → coninput(ReadConsoleInputW) → 正确读取 Unicode。
	// Linux/macOS 仍需 WithInputTTY 确保管道重定向 stdin 时 TTY 输入独立。
	programOpts := newTUIProgramOptions(mouseCapture, altScreen)
	p := tea.NewProgram(model, programOpts...)
	// N12 D1: 注入 Program 引用，供 /memory edit 启动外部编辑器时暂停终端控制。
	model.SetProgram(p)

	// TUI 生命周期信号处理：SIGTERM 通过 cleanup 发送 tea.Quit，让 Bubble Tea
	// 主循环正常退出；Ctrl+C 保留给模型作为 KeyMsg 处理，避免中断请求时退出 TUI。
	tuiShutdown := sig.NewGracefulShutdown()
	tuiShutdown.RegisterCleanup(func() error { p.Send(tea.Quit); return nil })
	// H11N27+H12 接线 cleanup：取消 wiring goroutine（ForgettingScanner 等），避免泄漏（吸纳优化 3）。
	tuiShutdown.RegisterCleanup(func() error {
		model.CancelWiring()
		return nil
	})
	// M22-04 P0：注册 SIGHUP 配置热重载 hook（不退出进程，重新加载 settings.json）。
	// 失败时仅记录日志，不影响后续信号处理。
	// ★ D 批：SIGHUP 原先"只热重载 custom_pricing"，其余配置一律需重启
	// —— 而这个反直觉行为正是 v3 全模块审查登记的 P0-3（日志说 "config reloaded"，
	// 实际只有定价表生效）。现在 custom_pricing 随成本线删除 ⇒
	// **已经没有任何可热重载字段**。留一个"读了配置但什么也不应用"的钩子就是纯谎言，
	// 所以这里如实告知：配置改动需重启。
	tuiShutdown.RegisterReloadHook(func() {
		log.Info("[signal] SIGHUP received: no hot-reloadable config field remains, restart to apply config changes")
	})
	// M22-04 P0：注册 SIGUSR1 日志级别切换 hook（在 Debug 与 Info 之间切换）。
	// 切换状态保存在包级 atomic 中，反复收到 SIGUSR1 在两级间 ping-pong。
	tuiShutdown.RegisterLogLevelToggleHook(func() {
		// 通过 log.ToggleDebug 间接调用 log.SetLevel，避免直接依赖 log/slog。
		// log.ToggleDebug 内部维护 atomic.Int32 状态，重复调用在 Debug/Info 之间切换。
		log.ToggleDebug()
	})
	sig.InstallSignalHandlerWithoutInterrupt(tuiShutdown)

	// 性能计时点（路径1-启动）：TUI 启动开始（所有初始化已完成）。
	// 输出"配置加载完成 → TUI 启动开始"阶段耗时（含 client/registry/orchestrator 创建）。
	tuiStart := time.Now()
	log.Debug("[perf] startup: pre-TUI init done",
		"phase", "pre_tui_init",
		"elapsed_ms", tuiStart.Sub(configDone).Milliseconds(),
	)

	finalModel, runErr := p.Run()
	if runErr != nil {
		log.Error("Failed to start", "error", runErr)
		shutdownMetrics(metricsSrv)
		os.Exit(1)
	}
	// 止血：alt-screen 模式下终端退出时会恢复成**启动前**画面 ⇒ 用户看到"整屏消失、
	// 历史没了"（内容其实还在 ~/.whale/sessions/*.jsonl）。退出后在主屏回印会话尾部，
	// 让内容落进终端原生 scrollback、可继续上翻。
	// ★ 止血≠修根：运行期的历史归属未变（滚轮仍翻不到历史）；修根见
	// 大修方案_TUI历史归属与回执可见性_2026-09-23.md（STEP 3 归属权重构）。
	// inline 模式（--no-alt-screen）下最后一帧本来就留在主屏，再印一次会重复 ⇒ 只在备用屏模式下回印。
	if altScreen {
		printExitTranscriptTail(finalModel)
	}

	// 性能计时点（路径1-启动）：TUI 退出（含 TUI 启动初始化 + 完整交互会话）。
	// 输出"TUI 启动开始 → TUI 退出"阶段耗时 + 启动总耗时（programStart → TUI 退出）。
	tuiEnd := time.Now()
	log.Debug("[perf] startup: TUI session ended",
		"phase", "tui_session",
		"elapsed_ms", tuiEnd.Sub(tuiStart).Milliseconds(),
	)
	log.Debug("[perf] startup: total elapsed",
		"phase", "total",
		"elapsed_ms", tuiEnd.Sub(programStart).Milliseconds(),
	)

	// 5-E-3：TUI 正常退出后优雅关闭 metrics server。
	shutdownMetrics(metricsSrv)
}

// exitTranscriptTailLines 退出回印的行数上限（约一屏）。
// 不设上限会把整个会话灌进用户终端（长会话上千行），所以刻意只给"退出前那屏"。
const exitTranscriptTailLines = 40

// printExitTranscriptTail 把会话尾部回印到主屏（止血，见 exitTranscriptTailLines）。
//
// 取值/取尾语义的唯一实现在 `tui.Model.ExitTranscriptTail`（内含 nil/空/边界处理）；
// 本函数只负责**打印**，不自己再切一份行（同语义第二份实现是本仓复发坑）。
func printExitTranscriptTail(m tea.Model) {
	tm, ok := m.(tui.Model)
	if !ok {
		return
	}
	tail := tm.ExitTranscriptTail(exitTranscriptTailLines)
	if strings.TrimSpace(tail) == "" {
		return
	}
	fmt.Println()
	fmt.Println("── conversation tail (kept in terminal scrollback) ─────────────")
	fmt.Println(tail)
	fmt.Println("── session saved · use /resume to continue ─────────────────────")
}

// altScreenMode 是终端备用屏（alternate screen）的三种取值。
//
// 对齐同类 CLI 的 `--no-alt-screen` + `determine_alt_screen_mode`（always/never/auto）：
// 备用屏与**终端原生 scrollback 互斥** —— 一旦 `\x1b[?1049h`，终端里就没有 whale 的任何历史行，
// 滚轮上翻看到的是启动前主屏的残留。故"历史归谁"是一个必须显式给出的产品选择，不是隐式默认。
type altScreenMode string

const (
	// altScreenAuto = 现状：启用备用屏（默认值，保证零体感变化）。
	altScreenAuto altScreenMode = "auto"
	// altScreenAlways 强制启用备用屏。
	altScreenAlways altScreenMode = "always"
	// altScreenNever 禁用备用屏 ⇒ inline 模式（历史可留在主屏/scrollback）。
	altScreenNever altScreenMode = "never"
)

// resolveAltScreen 判定最终是否启用备用屏 —— **唯一**判定点。
//
// 调用方只问这一句，不得各自再判一次（本仓"同一语义多处各写各的"是复发坑）。
// auto 当前等价于 always：保持现状，零体感变化。**是否把默认翻成 never 需单独拍板**，
// 因为翻默认会同时改变三组既有手测的前提（点击定位 / 子态输入 / IME 顺序）。
func resolveAltScreen(mode altScreenMode) bool {
	return mode != altScreenNever
}

// terminalFlags 是「终端能力开关」的解析结果（本地 TUI 与 SSH 两条路径共用同一份）。
type terminalFlags struct {
	mouseCapture    bool
	autoActiveMouse bool
	altScreen       bool
	// explicit 表示命令行**显式**给了终端能力开关。main 的参数循环用它决定是否
	// 跳过 handleArgs（显式 TUI 选项不应被当成未知参数）。
	explicit bool
}

// terminalFlagNames 是终端能力开关族的**唯一**名单。
var terminalFlagNames = []string{"--mouse", "--no-mouse", "--alt-screen", "--no-alt-screen"}

// isTerminalFlag 判定参数是否属于终端能力开关族（供参数循环消费，避免落入 handleArgs）。
func isTerminalFlag(arg string) bool {
	for _, f := range terminalFlagNames {
		if arg == f {
			return true
		}
	}
	return false
}

// parseTerminalFlags 是 `--mouse` / `--no-mouse` / `--alt-screen` / `--no-alt-screen`
// 的**唯一**解析实现。
//
// 为什么必须收口（本仓"命令双实现"复发坑）：这四个开关原先在 `main()` 与 `runSSHMode()`
// 里**各手写一遍** `arg == "--xxx"`。加第三、第四个开关时若继续各写一遍，两条路径迟早分叉
// （本仓已多次因双实现漂移吃过亏：改了一侧、另一侧既无文案又不认子命令）。
// 现在两侧都只薄转发到本函数；`hasFlag`（原先只有测试在用）保持不动，不再新增手写比较。
func parseTerminalFlags(args []string) terminalFlags {
	f := terminalFlags{}
	f.mouseCapture, f.autoActiveMouse = defaultMouseMode()
	f.altScreen = resolveAltScreen(altScreenAuto)
	for _, arg := range args {
		switch arg {
		case "--mouse":
			f.mouseCapture, f.autoActiveMouse, f.explicit = true, false, true
		case "--no-mouse":
			f.mouseCapture, f.autoActiveMouse, f.explicit = false, false, true
		case "--alt-screen":
			f.altScreen, f.explicit = true, true
		case "--no-alt-screen":
			f.altScreen, f.explicit = false, true
		}
	}
	return f
}

// newTUIProgramOptions centralizes terminal capabilities for both local and SSH TUI.
// Mouse capture is enabled by default so clicks and wheel scrolling work in the
// composer on every supported platform. Use --no-mouse or /mouse off when native
// terminal selection is preferred.
//
// altScreen 由 resolveAltScreen 判定后传入（本地与 SSH 同源），本函数**不再**自行判定：
// 备用屏开关只能有"解析"与"判定"各一处，否则又是同语义双实现。
func newTUIProgramOptions(mouseCapture bool, altScreen bool) []tea.ProgramOption {
	// TUI owns Ctrl+C as a key to stop the active turn. The
	// application signal handler owns SIGTERM and lifecycle cleanup.
	opts := []tea.ProgramOption{tea.WithFPS(120), tea.WithoutSignalHandler()}
	// ②：上报终端窗口焦点事件（CSI 1004）。用户切走再切回（截图、复制图片后）
	// 触发 FocusMsg ⇒ 探测剪贴板有图则提示「按 Ctrl+V」。不支持的终端不会上报，
	// 探针只在收到事件时运行，零常驻开销。
	opts = append(opts, tea.WithReportFocus())
	if altScreen {
		opts = append(opts, tea.WithAltScreen())
	}
	if mouseCapture {
		opts = append(opts, tea.WithMouseCellMotion())
	}
	// Windows uses Bubble Tea's CONIN$ reader to preserve Unicode input. Other
	// platforms need an independent TTY when stdin is redirected by a launcher.
	if runtime.GOOS != "windows" {
		opts = append(opts, tea.WithInputTTY())
	}
	return opts
}

func defaultMouseMode() (capture bool, autoDuringActive bool) {
	return true, false
}

func hasFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag {
			return true
		}
	}
	return false
}

// 原 runSSHMode（`whale ssh <host>`，自动发 /remote connect）已随
// /remote 整面砍除。远程开发走 ssh+tmux：ssh 登录主机后直接运行 whale（tmux 保活）。

// runPlainMode start Whale Code in plain text mode (no TUI, full copy support)
func runPlainMode() {
	cfg := loadConfig()
	if hasEnablePluginsFlag() { // P0-7/B7: plain 模式也支持 --enable-plugins
		cfg.PluginsEnabled = true
	}
	ensureAPIKeyOrExit(cfg) // 在模型名解析前检查 API Key（与 main 同因，见 main 中注释）
	registry := loadOrCreateModelRegistry(cfg)
	client := createClient(cfg)

	runner := tui.NewPlainRunner(cfg, client, registry)
	defer runner.Close()
	if err := runner.Run(); err != nil {
		log.Error("Error", "error", err)
		os.Exit(1)
	}
}

// runBridgeMode start Whale Code in bridge mode (JSON-RPC over stdio for IDE integration)
//
// 安全说明：bridge 模块的 JWT 解码不验签（internal/bridge/jwt.go DecodeJWTPayload
// 仅 base64 解码 payload 段，完全不验证签名），存在安全漏洞。个人单机用户不需要
// 远程中继鉴权，因此默认禁用。需显式配置 bridge_enabled=true 或环境变量
// WHALE_BRIDGE_ENABLED=1 才启用，使用方自行承担安全风险。
func runBridgeMode(continueSession bool) {
	// Bridge 使用 JSON-RPC stdio，不具备 Onboarding 交互终端。
	cfg := loadConfigWithOnboarding(false)
	if hasEnablePluginsFlag() { // P0-7/B7: bridge 模式也支持 --enable-plugins
		cfg.PluginsEnabled = true
	}

	// 默认禁用 bridge 模式：JWT 不验签是安全漏洞，需显式 opt-in
	if !cfg.BridgeEnabled {
		fmt.Fprintln(os.Stderr, "Bridge mode is disabled by default for security reasons:")
		fmt.Fprintln(os.Stderr, "  - bridge module JWT decoding does not verify signatures (security vulnerability)")
		fmt.Fprintln(os.Stderr, "  - personal local users do not need remote relay authentication")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "To enable bridge mode, set one of:")
		fmt.Fprintln(os.Stderr, "  - config file: \"bridge_enabled\": true in ~/.whale/settings.json")
		fmt.Fprintln(os.Stderr, "  - environment: WHALE_BRIDGE_ENABLED=1")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "WARNING: enabling bridge mode is at your own risk.")
		os.Exit(1)
	}

	ensureAPIKeyOrExit(cfg) // 在模型名解析前检查 API Key（与 main 同因，见 main 中注释）
	registry := loadOrCreateModelRegistry(cfg)
	client := createClient(cfg)

	runner, err := tui.NewBridgeRunner(cfg, client, registry, continueSession)
	if err != nil {
		// P0-13: fail-closed — bridge 初始化失败（如空 jwt_secret）直接退出，不降级到不验签
		log.Error("Bridge mode init failed", "error", err)
		os.Exit(1)
	}
	defer runner.Close()
	if err := runner.Run(); err != nil {
		log.Error("Bridge mode error", "error", err)
		os.Exit(1)
	}
}

// hasEnablePluginsFlag 检查命令行参数中是否包含 --enable-plugins（P0-7/B7）。
// 供 plain/bridge 等提前 return 的模式复用 flag 检测逻辑。
func hasEnablePluginsFlag() bool {
	for _, arg := range os.Args[1:] {
		if arg == "--enable-plugins" {
			return true
		}
	}
	return false
}

// runDaemonMode starts Whale Code as a daemon process for background task execution
func runDaemonMode() {
	executable, err := os.Executable()
	if err != nil {
		log.Error("Failed to resolve daemon executable", "error", err)
		return
	}
	daemonCfg := daemon.ConfigFromEnv(getWorkDir(), executable)

	d := daemon.NewDaemon(daemonCfg)
	// The worker launches the trusted Whale CI orchestrator. Sandboxing this
	// outer process would strip model credentials and network access. Commands
	// and tools requested by the model remain constrained by the CI runner's
	// normal permission and sandbox pipeline.

	// Signal handling: graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Info("Received signal, stopping daemon", "signal", sig)
		d.NotifyStop()
	}()

	if err := d.Run(); err != nil {
		log.Error("Daemon error", "error", err)
		os.Exit(1)
	}
}

// getWorkDir gets current working directory with fallback
func getWorkDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		log.Warn("Failed to get working directory", "error", err)
		return "."
	}
	return cwd
}

// runCIMode run in non-interactive CI mode (single prompt, auto-exit)
func runCIMode(cfg *config.Config, client *ai.SimpleClient, registry *ai.ModelRegistry, prompt string) {
	if prompt == "" {
		log.Error("CI mode requires a prompt argument")
		os.Exit(2)
	}

	runner := tui.NewPlainRunner(cfg, client, registry)
	defer runner.Close()

	// CI 模式信号处理：非交互单次执行，收到信号时执行 runner.Close 清理后强制退出。
	// 单独注册（CI 走 RunCI 而非 Run，不复用 PlainRunner.Run 的信号循环）。
	ciShutdown := sig.NewGracefulShutdown()
	ciShutdown.RegisterCleanup(func() error { runner.Close(); return nil })
	sig.InstallSignalHandler(ciShutdown)

	exitCode, err := runner.RunCI(prompt)
	if err != nil {
		log.Error("CI mode error", "error", err)
		os.Exit(1)
	}
	os.Exit(exitCode)
}

const maxCIPromptSize = 1 << 20

func readCIPrompt(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxCIPromptSize+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxCIPromptSize {
		return "", fmt.Errorf("prompt exceeds %d bytes", maxCIPromptSize)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("prompt is empty")
	}
	return string(data), nil
}

// setupOrchestrator 创建并注入 H10 Phase 6 编排器到 TUI Model（S4 接线）。
// 在 tui.NewModel 之后、tea.NewProgram 之前调用。
// 流程：
// 1. 创建 Phase 0 地基层组件（QualityStore/ProfileStore/CapabilityCache/TraceStore）
// 2. 创建 OrchestratorLLMAdapter（复用主对话 SimpleClient）+ n1AgentAdapter 包装
// 3. 创建 channel 对（TUIAskUserGateway 与 OrchBridge 共用）
// 4. 创建 TUIAskUserGateway + Orchestrator（注入 functional options）
// 5. 创建 OrchBridge 并注入到 TUI Model（SetOrchBridge）
// 6. 调用 SetOrchestrator 注册 /orchestrate 命令回调
//
// 失败时记录警告并跳过编排器注入（不阻塞 TUI 启动，/orchestrate 命令将返回未初始化提示）。
// client 为 nil 时直接返回（CI/headless 模式不需要编排器）。
func setupOrchestrator(model *tui.Model, client *ai.SimpleClient) {
	if client == nil {
		log.Warn("[orchestrator] AI client is nil, skip orchestrator injection")
		return
	}

	// 收口：Dir() 返回空（home 不可得）⇒ 跳过编排器注入，行为同前。
	baseDir := whalehome.Dir()
	if baseDir == "" {
		log.Warn("[orchestrator] Failed to get home directory")
		return
	}

	orchDir := filepath.Join(baseDir, "orchestrator")

	// 1. 创建 Phase 0 地基层组件
	qualityPath := filepath.Join(orchDir, "quality.jsonl")
	quality, err := orchestrator.NewQualityStore(qualityPath)
	if err != nil {
		log.Warn("[orchestrator] QualityStore init failed", "error", err)
		return
	}

	profile := orchestrator.NewProfileStore()

	capPath := filepath.Join(orchDir, "capability_cache.json")
	capability, err := orchestrator.NewCapabilityCache(capPath)
	if err != nil {
		log.Warn("[orchestrator] CapabilityCache init failed", "error", err)
		return
	}

	// TraceStore 降级：失败时 tracer=nil，不阻塞编排器启动（仅丢失 trace 日志）
	tracePath := filepath.Join(orchDir, "review_trace.jsonl")
	tracer, err := orchestrator.NewTraceStore(tracePath)
	if err != nil {
		log.Warn("[orchestrator] TraceStore init failed, trace disabled", "error", err)
		tracer = nil
	}

	// 2. 创建 LLM 适配器（复用主对话 SimpleClient， S3）
	llmAdapter := orchestrator.NewOrchestratorLLMAdapter(client)
	// N1Agent 接口单独包装（Go 不允许同名方法不同签名同时存在， S3）
	n1Agent := orchestrator.NewN1AgentAdapter(llmAdapter)

	// 3. 创建 channel 对（TUIAskUserGateway 与 OrchBridge 共用同一双向 channel）
	askChRecv, contractChRecv, askChSend, contractChSend := tui.NewOrchChannels()

	// 4. 创建 AskUserGateway（TUI 实现，超时使用默认 30 分钟）
	askUserGateway := orchestrator.NewTUIAskUserGateway(askChSend, contractChSend, 0)

	// 5. 创建 Orchestrator（注入 functional options， S4）
	orch := orchestrator.NewOrchestrator(
		baseDir, n1Agent, quality, profile, capability, orchestrator.BudgetModeBalanced,
		orchestrator.WithClarifier(llmAdapter, tracer),
		orchestrator.WithPlanGenerator(llmAdapter, tracer),
		orchestrator.WithContractor(askUserGateway, tracer),
		orchestrator.WithAskUser(askUserGateway),
	)

	// 6. 注入到 TUI Model
	bridge := tui.NewOrchBridge(askChRecv, contractChRecv)
	model.SetOrchBridge(bridge)
	model.SetOrchestrator(orch)

	// 7. 创建 ExperienceOrchestrator（B-P0-5 修复：实例化后 lifecycle.go:889 guard 自动放行 wireH12Evolution）。
	// 失败时 warn 不阻断主流程（guard 仍跳过 wireH12Evolution，回退 Phase 1 行为）。
	setupExperienceOrchestrator(model, baseDir, orchDir, profile, quality, tracer)

	log.Info("[orchestrator] orchestrator injected")
}

// setupExperienceOrchestrator 创建并注入 ExperienceOrchestrator 到 TUI Model（B-P0-5 修复）。
// 实例化后 lifecycle.go:889 的 guard `if m.experienceOrch != nil` 自动放行 wireH12Evolution，
// EvolutionLoop 自动构造 + 注入 + 后台 TTL 扫描启动（接线点在 lifecycle.go，不在 main.go）。
// 失败时 warn 不阻断主流程（guard 仍跳过 wireH12Evolution，回退 Phase 1 行为）。
//
// 参数：
// - model：TUI Model（注入目标）
// - baseDir：.whale/ 根目录
// - orchDir：.whale/orchestrator/ 目录
// - profile：已创建的 ProfileStore（复用）
// - quality：已创建的 QualityStore（复用）
// - tracer：已创建的 TraceStore（复用，可为 nil）
func setupExperienceOrchestrator(model *tui.Model, baseDir, orchDir string, profile *orchestrator.ProfileStore, quality *orchestrator.QualityStore, tracer *orchestrator.TraceStore) {
	// 1. 创建 ExperienceStore（全局 + 项目级双层存储）
	globalExpPath := filepath.Join(orchDir, "experience", "global")
	projectExpPath := filepath.Join(orchDir, "experience", "project")
	expStore, err := orchestrator.NewExperienceStore(globalExpPath, projectExpPath)
	if err != nil {
		log.Warn("[orchestrator] ExperienceStore init failed, H12 evolution degraded", "error", err)
		return
	}

	// 2. 创建 L1ScorerExt（权重学习扩展器）
	l1Scorer := orchestrator.NewL1ScorerExt(filepath.Join(orchDir, "l1_weights.json"))

	// 3. 创建 ParallelScheduler 所需组件
	graph := orchestrator.NewDepGraph()
	pool := orchestrator.NewWorktreePool(filepath.Join(orchDir, "worktrees"), 3)
	budget := orchestrator.NewBudgetTracker(orchestrator.DefaultTotalTokensCap) // 预算轴 = provider 真报的 token
	state := orchestrator.NewStateMachine("")
	scheduler := orchestrator.NewParallelScheduler(graph, pool, budget, tracer, state, 3)

	// 4. 创建 Router（复用 ProfileStore + BudgetModeBalanced）
	router := orchestrator.NewRouterFromPhase0(profile, orchestrator.BudgetModeBalanced)

	// 5. 创建 ExperienceOrchestrator（注入全部依赖）
	expOrch := orchestrator.NewExperienceOrchestrator(scheduler, state, router, expStore, quality)

	// 6. 注入到 TUI Model（解除 lifecycle.go:889 guard，wireH12Evolution 自动执行）
	model.SetExperienceOrchestrator(expOrch)
	model.SetExpStore(expStore)
	model.SetQualityStore(quality)
	model.SetL1Scorer(l1Scorer)

	log.Info("[orchestrator] ExperienceOrchestrator instantiated")
}

// runDoctorMode 执行 CLI 诊断模式（whale doctor 子命令）。
// 支持子参数：--network（网络检测）/ --update-check（更新检查）/
// --config-migrate（配置迁移）/ --repair（从备份恢复 + 隔离损坏文件）。
// 无子参数时执行全量诊断（等同 TUI 内 /doctor）。
//
// 设计依据：v3修复方案.md §2.2 V3-5-1/V3-4-1/V3-1-2/V3-1-3，
// 补接 checkRecovery 的 FixSuggest 提示 "whale doctor --repair" 的 CLI 入口。
//
// 退出码：含 Fatal 级别结果返回 1，否则返回 0（--repair 失败也返回 1）。
func runDoctorMode(args []string) {
	loadEnvFiles()

	// 解析子参数（任一出现即只跑该项；多参数时按优先级 --repair > --config-migrate > --lock-test > --resource > --network > --update-check）
	var (
		doNetwork       bool
		doUpdateCheck   bool
		doConfigMigrate bool
		doRepair        bool
		doLockTest      bool
		doResource      bool
	)
	for _, a := range args {
		switch a {
		case "--network":
			doNetwork = true
		case "--update-check":
			doUpdateCheck = true
		case "--config-migrate":
			doConfigMigrate = true
		case "--repair":
			doRepair = true
		case "--lock-test":
			doLockTest = true
		case "--resource":
			doResource = true
		case "-h", "--help":
			fmt.Println("Usage: whale doctor [--network|--update-check|--config-migrate|--repair|--lock-test|--resource]")
			fmt.Println("  --network          Check network connectivity (online/offline)")
			fmt.Println("  --update-check     Check GitHub Release for latest version")
			fmt.Println("  --config-migrate   Migrate settings.json to current config version")
			fmt.Println("  --repair           Restore config from backup, then quarantine still-corrupted files")
			fmt.Println("  --lock-test        Check multi-instance lock status (V3-5-3)")
			fmt.Println("  --resource         Check system resources: memory/CPU/disk (V3-4-2)")
			fmt.Println("  (no args)          Run full diagnostics (same as /doctor in TUI)")
			return
		default:
			fmt.Printf("Unknown doctor argument: %s\n", a)
			fmt.Println("Usage: whale doctor [--network|--update-check|--config-migrate|--repair|--lock-test|--resource]")
			os.Exit(1)
		}
	}

	// --repair 特殊路径：直接调用 util.RepairCorrupted，不走 doctor.Run
	if doRepair {
		runRepairMode()
		return
	}

	// 准备 doctor 实例所需上下文（与 checkAPI 一致的最小化构建）
	workDir, _ := os.Getwd()
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}
	settingsPath := filepath.Join(whalehome.Dir(), "settings.json")

	// 配置 + AI client 仅在需要全量诊断或单项诊断时构建；
	// --network / --update-check / --config-migrate / --lock-test / --resource 不依赖 AI client，传 nil 即可（doctor 内部标记 fatal）。
	// P5 修复：先加载 settings.json，让 doctor 能读到 model/api_key（原实现传 nil 导致 doctor 误报 FATAL）
	loader := config.NewLoader(homeDir, workDir)
	cfg, cfgErr := loader.Load()
	if cfgErr != nil {
		// settings.json 加载失败时降级为默认配置 + 环境变量（保持原行为）
		cfg = config.DefaultConfigPtr()
	}
	apiKey, provider, baseURL := resolveProviderConfig(cfg)
	model := cfg.Model
	if model == "" {
		model = os.Getenv("WHALE_MODEL")
	}
	var client ai.Client
	if apiKey != "" && model != "" {
		client = ai.NewSimpleClient(model, apiKey, baseURL, provider)
	}

	doc := doctor.NewDoctor(cfg, client, workDir, settingsPath)

	// 根据子参数选择性执行单项检测；无子参数时跑全量
	var results []doctor.CheckResult
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch {
	case doConfigMigrate:
		results = doc.CheckConfigMigrate()
	case doLockTest:
		results = doc.CheckInstanceLock()
	case doResource:
		results = doc.CheckResource()
	case doNetwork:
		results = doc.CheckNetwork()
	case doUpdateCheck:
		results = doc.CheckUpdate()
	default:
		results = doc.Run(ctx)
	}

	// 渲染并输出结果
	fmt.Print(doc.Render(results))

	// 含致命错误时退出码 1
	if doc.HasFatal(results) {
		os.Exit(1)
	}
}

// runRepairMode 执行 `whale doctor --repair` 的配置修复流程。
//
// 两步（Q4/A，PR#2）：
// 1. 显式恢复：对每个「存在但损坏」的配置源，尝试从最新 .bak 备份恢复（Loader.RestoreFromBackup）。
// 2. 隔离残留：仍损坏的文件交由 util.RepairCorrupted 移动到 ~/.whale/corrupt/<timestamp>/。
//
// 行为对标 util.RepairCorrupted（V3-5-1）；第 1 步是 PR#2 新增（补偿 Load 不再自动恢复）。
func runRepairMode() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to get home directory: %s\n", err)
		os.Exit(1)
	}
	whaleDir := whalehome.Dir()

	// Q4/A（PR#2）：显式恢复流程。
	// Q4 把「从 .bak 自动恢复」从 Load 中移除后，恢复能力落到这里：
	// 先尝试从备份恢复损坏的配置源，再对残留的损坏文件做隔离（util.RepairCorrupted）。
	cwd, _ := os.Getwd()
	loader := config.NewLoader(homeDir, cwd)
	restored := 0
	for _, src := range loader.Sources() {
		// 仅对「存在但损坏」的源尝试恢复（IsJSONFileCorrupted 为只读检测）
		if !util.IsJSONFileCorrupted(src) {
			continue
		}
		ok, rerr := loader.RestoreFromBackup(src)
		if rerr != nil {
			fmt.Printf("⚠️  Restore from backup failed for %s: %s\n", src, rerr)
			continue
		}
		if ok {
			restored++
			fmt.Printf("♻️  Restored %s from backup\n", src)
		}
	}
	if restored > 0 {
		fmt.Printf("Restored %d config source(s) from backup.\n\n", restored)
	}

	quarantined, err := util.RepairCorrupted(whaleDir)
	if err != nil {
		fmt.Printf("Repair scan failed: %s\n", err)
		// 即便扫描中途出错，util 仍会返回已隔离的文件列表
		if len(quarantined) == 0 {
			os.Exit(1)
		}
	}

	if len(quarantined) == 0 {
		fmt.Println("✅ No corrupted JSON files found under ~/.whale/")
		return
	}

	fmt.Printf("🧹 Quarantined %d corrupted file(s):\n", len(quarantined))
	for _, f := range quarantined {
		fmt.Println("  - " + f)
	}
	fmt.Println()
	fmt.Println("Corrupted files have been moved to ~/.whale/corrupt/<timestamp>/")
	fmt.Println("You can now restart whale safely.")
}

// runMigrateMode 从同类 CLI（两类）迁移配置到 whale。
// 用法：whale migrate --from 同类 CLI 或 whale migrate --from 同类 CLI
//
// 设计依据：v3修复方案.md §2.2 V3-7-1，补接 internal/migrate 包的 CLI 入口。
// 仅导入必要的 API Key + Provider 信息，权限规则因语义差异不自动迁移（仅提示）。
func runMigrateMode(args []string) {
	// 解析 --from 参数
	var source string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--from":
			if i+1 >= len(args) {
				fmt.Println("Error: --from requires a value (codex or claude-code)")
				fmt.Println("Usage: whale migrate --from codex|claude-code")
				os.Exit(1)
			}
			source = args[i+1]
			i++ // 跳过已消费的值
		case "-h", "--help":
			fmt.Println("Usage: whale migrate --from codex|claude-code")
			fmt.Println("  --from codex         Migrate API Key from ~/.codex/ (codex CLI)")
			fmt.Println("  --from claude-code   Migrate API Key from ~/.claude/ (claude-code CLI)")
			return
		default:
			fmt.Printf("Unknown migrate argument: %s\n", a)
			fmt.Println("Usage: whale migrate --from codex|claude-code")
			os.Exit(1)
		}
	}

	if source == "" {
		fmt.Println("Error: --from is required")
		fmt.Println("Usage: whale migrate --from codex|claude-code")
		os.Exit(1)
	}

	// 获取 homeDir
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to get home directory: %s\n", err)
		os.Exit(1)
	}

	// 根据来源调用对应的迁移函数
	var imported []string
	switch source {
	case "codex":
		imported, err = migrate.MigrateFromCodex(homeDir)
	case "claude-code":
		imported, err = migrate.MigrateFromClaudeCode(homeDir)
	default:
		fmt.Printf("Error: unsupported --from value %q (must be 'codex' or 'claude-code')\n", source)
		fmt.Println("Usage: whale migrate --from codex|claude-code")
		os.Exit(1)
	}

	if err != nil {
		fmt.Printf("Migration from %s failed: %s\n", source, err)
		os.Exit(1)
	}

	// 打印导入的配置项
	fmt.Printf("✅ Migration from %s completed. Imported items:\n", source)
	if len(imported) == 0 {
		fmt.Println("  (no items imported)")
	}
	for _, item := range imported {
		fmt.Println("  - " + item)
	}
	fmt.Println()
	fmt.Println("You can now run `whale` to start using the migrated configuration.")
}

// handleArgs handle command line arguments
func handleArgs(args []string) {
	switch args[0] {
	case "--version", "-v":
		fmt.Print(formatVersionString())
	case "--help", "-h":
		printHelp()
	case "--check-api":
		checkAPI()
	default:
		fmt.Print(formatVersionString())
		fmt.Printf("Unknown argument: %s\n", args[0])
		fmt.Println("Use whale --help for help")
	}
}

// formatVersionString 格式化版本信息字符串，供 --version/-v 及启动横幅复用。
// 桶 X5 死代码清理：接线 internal/version 包，使 version.Version/BuildDate/Commit 不再是零调用死代码。
// 当 BuildDate/Commit 经 -ldflags 注入时一并展示，否则仅展示版本号。
func formatVersionString() string {
	// 版本号只在 internal/version 包声明一次（main 包不再有同名变量）。
	v := versionpkg.Version
	if versionpkg.BuildDate == "" && versionpkg.Commit == "" {
		return fmt.Sprintf("Whale Code v%s\n", v)
	}
	return fmt.Sprintf("Whale Code v%s (commit: %s, built: %s)\n", v, versionpkg.Commit, versionpkg.BuildDate)
}

// parseSessionFlag 解析会话点名的四种写法：
//
//	--session <id> --session=<id> -s <id> -s=<id>
//
// 返回 matched=true 表示该参数属于会话选择（调用方应 continue，继续扫后续参数）。
//
// 为何单独抽成函数：参数解析原本内联在 main 的 for 循环里，无法被测试覆盖，
// 而「缺值时静默忽略」这类退化只会在用户手上暴露（`whale --session` 变成
// 打开新会话 —— 用户以为接上了旧对话，比报错更糟）。抽出来后缺值路径可判别。
//
// next 是紧随其后的参数（无后续参数时为空串）。值不允许以 '-' 开头，
// 否则 `whale --session --debug` 会把 --debug 当作会话 ID。
func parseSessionFlag(arg, next string) (value string, matched bool, err error) {
	switch {
	case strings.HasPrefix(arg, "--session="):
		value, matched = strings.TrimPrefix(arg, "--session="), true
	case strings.HasPrefix(arg, "-s="):
		value, matched = strings.TrimPrefix(arg, "-s="), true
	case arg == "--session" || arg == "-s":
		matched = true
		if next == "" || strings.HasPrefix(next, "-") {
			return "", true, fmt.Errorf("missing value for %s (usage: whale --session <session-id>)", arg)
		}
		value = next
	}
	if matched && strings.TrimSpace(value) == "" {
		return "", true, fmt.Errorf("missing value for %s (usage: whale --session <session-id>)", arg)
	}
	return value, matched, nil
}

// isValidPermissionMode 校验权限模式是否合法（R14 P0-17）。
// 合法值：read-only / workspace-write / danger-full-access，与 config.PermissionMode 常量对齐。
func isValidPermissionMode(mode string) bool {
	switch config.PermissionMode(mode) {
	case config.PermReadOnly, config.PermWorkspaceWrite, config.PermDangerFullAccess:
		return true
	}
	return false
}

// printHelp print help info
func printHelp() {
	fmt.Printf("Whale Code v%s - Personal AI Coding Assistant\n\n", versionpkg.Version)
	fmt.Println("Usage:")
	fmt.Println("  whale              Start interactive mode (TUI)")
	fmt.Println("  whale --mouse      Enable TUI mouse clicks/scrolling (native terminal selection is the default)")
	fmt.Println("  whale --no-mouse   Disable TUI mouse reporting and use native terminal selection")
	fmt.Println("  whale --plain      Start plain mode (full copy support)")
	fmt.Println("  whale -p \"task\"    Start TUI with prompt pre-filled")
	fmt.Println("  whale --ci \"task\"  Non-interactive CI mode (auto-exit)")
	fmt.Println("  whale --ci-prompt-stdin  Read the non-interactive CI prompt from stdin")
	fmt.Println("  whale -c           Resume last session")
	fmt.Println("  whale --session <id>       Resume a specific session (UUID, UUID prefix, or name)")
	fmt.Println("  whale -s <id>              Alias for --session")
	fmt.Println("  whale --fork-session       Fork last session (copy history to new ID)")
	fmt.Println("  whale --fork-session <id>  Fork specific session by ID")
	fmt.Println("  whale --check-api  Test API connection")
	fmt.Println("  whale --enable-plugins  Enable the plugin system (disabled by default)")
	fmt.Println("  whale --alt-screen / --no-alt-screen  Force the TUI on/off the terminal alternate screen")
	fmt.Println("  whale --permission <mode>  Set permission mode (read-only/workspace-write/danger-full-access)")
	fmt.Println("  whale doctor [--network|--update-check|--config-migrate|--repair|--lock-test|--resource]  Run diagnostics")
	fmt.Println("  whale --doctor       Alias for `whale doctor` (run diagnostics)")
	fmt.Println("  whale migrate --from codex|claude-code  Migrate config from codex/claude-code")
	fmt.Println("  whale --version    Show version")
	fmt.Println("  whale --help       Show this help")
	fmt.Println("  whale --debug      Also write debug logs to stderr")
	fmt.Println("  whale --disable-metrics-auth  Local debugging only: disable metrics Bearer-token auth")
	fmt.Println()
	fmt.Println("Required env vars (pick one):")
	fmt.Println("  ANTHROPIC_API_KEY      Anthropic API Key")
	fmt.Println("  OPENAI_API_KEY         OpenAI-compatible API Key")
	fmt.Println()
	fmt.Println("Optional env vars:")
	fmt.Println("  OPENAI_BASE_URL        OpenAI-compatible API URL")
	fmt.Println("  WHALE_MODEL            Model name (required)")
	fmt.Println("  WHALE_FALLBACK_MODEL   Fallback model (auto-switch on 529)")
	fmt.Println("  WHALE_PERMISSION_MODE  Permission mode (read-only/workspace-write/danger-full-access)")
	fmt.Println("  WHALE_CONTEXT_WINDOW   Context window size")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  # Anthropic Claude")
	fmt.Println("  set ANTHROPIC_API_KEY=sk-ant-xxx")
	fmt.Println("  set WHALE_MODEL=claude-sonnet-4-20250514")
	fmt.Println("  whale")
	fmt.Println()
	fmt.Println("  # Qwen (OpenAI-compatible)")
	fmt.Println("  set OPENAI_API_KEY=sk-xxx")
	fmt.Println("  set OPENAI_BASE_URL=https://dashscope.aliyuncs.com/compatible-mode/v1")
	fmt.Println("  set WHALE_MODEL=qwen3.5-27b")
	fmt.Println("  whale")
	fmt.Println()
	fmt.Println("  # DeepSeek (OpenAI-compatible)")
	fmt.Println("  set OPENAI_API_KEY=sk-xxx")
	fmt.Println("  set OPENAI_BASE_URL=https://api.deepseek.com")
	fmt.Println("  set WHALE_MODEL=deepseek-chat")
	fmt.Println("  whale")
}

// loadConfig loads configuration with unified error handling.
// On partial failure (missing config file, invalid fields), logs a warning
// and falls back to defaults. Only exits on complete inability to proceed.
//
// N18：首次运行检测 — 若 Onboarding 未完成，先尝试为已有用户自动标记（settings.json 存在
// 且环境变量 API key 已配置），否则触发交互式 Onboarding 4 步骤。Onboarding 完成后重新加载配置。
func loadConfig() *config.Config {
	return loadConfigWithOnboarding(true)
}

// loadConfigWithOnboarding 加载配置，并由调用方决定是否允许首次运行交互引导。
// TUI/Plain 等人工交互模式传 true；CI/Bridge 等机器接口必须传 false。
func loadConfigWithOnboarding(allowOnboarding bool) *config.Config {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Warn("Failed to get home directory, using current directory", "error", err)
		homeDir = "."
	}
	cwd, err := os.Getwd()
	if err != nil {
		log.Warn("Failed to get working directory, using current directory", "error", err)
		cwd = "."
	}

	loader := config.NewLoader(homeDir, cwd)

	// Q1/A（PR#2）：在任何可能写盘的流程（onboarding 向导会 Save settings.json）**之前**
	// 先做只读完整性预检。否则损坏配置会让 onboarding 用新配置覆盖损坏文件，
	// 从而绕过「损坏即拒绝启动」的语义。损坏 → 拒绝启动。
	// A9（S12-N3）：同一条预检也覆盖「存在但不可读」（ErrUnreadableConfig）——
	// 否则 onboarding 的写盘会绕过「不可读也要拒启」的语义。
	if err := loader.CheckIntegrity(); err != nil {
		refuseToStartOnUnusableConfig(err)
	}

	// N18 首次运行检测：未完成 Onboarding 时触发引导（替代旧的 os.Exit(1) 痛点）
	if allowOnboarding && !onboarding.IsCompleted(homeDir) {
		// 先尝试为已有用户自动标记（settings.json + 环境变量 API key 已配置）
		if onboarding.MaybeMarkExistingUser(homeDir) {
			log.Info("Existing user detected, auto-marked onboarding complete")
		} else {
			// 真正的新用户，执行 4 步骤交互式 Onboarding
			settingsPath := filepath.Join(whalehome.Dir(), "settings.json")
			doc := doctor.NewDoctor(nil, nil, cwd, settingsPath)
			ob := onboarding.NewOnboarding(nil, doc, cwd, homeDir)
			completed, obErr := ob.Run(context.Background())
			if obErr != nil || !completed {
				log.Error("Onboarding failed", "error", obErr)
				os.Exit(1)
			}
		}
	}

	cfg, err := loader.Load()
	if err != nil {
		// Q1/A（PR#2）：配置损坏拒绝启动，绝不静默退回宽松默认
		// （对齐业内既有实现 拒启 / 同类 CLI exit(1)）。
		// A9：配置存在但不可读同样拒绝启动（同档 T3，见 readSources 注释）。
		refuseToStartOnUnusableConfig(err)
	}

	// S6 / L1：把 settings.json 里**显式声明**支持原生检索的模型推给 ai 包。
	// ★ 与本函数同处一个咽喉点，是为了结构性地排除"新加了一条入口却忘了接线"
	// （同批 1 的 applyEnvDerivedToolAvailability 用法）。
	applyNativeSearchOverrides(cfg)

	return cfg
}

// refuseToStartOnUnusableConfig 打印可执行的修复指引并以非零码退出
//
//	fail-closed T3：配置**损坏**（Q1/A，S12-PR#2）或**存在但不可读**（A9，S12-N3）
//	都属「拿不到用户配置」⇒ 一律拒绝启动。
//
// 不进入 TUI/onboarding，避免任何写盘动作覆盖损坏文件（保留取证与备份恢复价值）；
// 「不可读」同理 —— 写盘既救不回也只会掩盖取证。
//
// 注意：非 debug 模式下 main 顶部会把 os.Stderr 重定向到 ~/.whale/logs/whale.log，
// 因此「拒绝启动」提示若写 os.Stderr 用户将看不到；这里直接写 fd 2（与 panic 恢复同一手法），
// 保证终端可见，同时用 log.Error 记入日志文件。
//
// 命名说明：函数原名为 refuseToStartOnCorruptConfig。A9 让它多承担「不可读」这一类，
// 沿用旧名会名不副实（G-E §1.17 记过的「名字描述旧语义」债），故更名为 Unusable。
func refuseToStartOnUnusableConfig(err error) {
	log.Error("refuse to start: config unusable", "error", err)
	stderr := os.NewFile(2, "stderr")
	// ★ ：双语 SSOT 形态（util.Bi/BiSame）—— 修前中文为主；
	// 中文辅行保留「配置加载失败」（scripts/e2e_journey_test.go:865 钉该子串）。
	fmt.Fprintf(stderr, "%s\n\n", util.BiSame("❌ Failed to load config: %v", "❌ 配置加载失败：%v", err))

	// 两类失效的**修复动作不同**，指引必须分开 —— 否则用户会被指向错误的动作
	// （「不可读」去跑 doctor --repair 是徒劳的：备份恢复仍要能读源文件）。
	if errors.Is(err, config.ErrUnreadableConfig) {
		fmt.Fprintln(stderr, util.Bi(
			"The config file exists but **cannot be read** (permissions/IO). Fix it, then retry:",
			"配置文件存在但**无法读取**（权限/IO）。请修复后重试："))
		fmt.Fprintln(stderr, "    ls -l ~/.whale/settings.json          # check owner/permission bits")
		fmt.Fprintln(stderr, "    chmod 600 ~/.whale/settings.json      # ensure the current user can read/write")
		fmt.Fprintln(stderr, util.Bi(
			"Also confirm this path is not a directory and its disk is not read-only. Until this is fixed, whale will not run with default permissions.",
			"并确认该路径不是目录、所在磁盘未只读。修复前 whale 不会以默认权限运行。"))
		os.Exit(1)
	}

	fmt.Fprintln(stderr, util.Bi(
		"The config file appears corrupt. Repair it before starting:",
		"配置文件疑似损坏。请先修复再启动："))
	fmt.Fprintln(stderr, "    whale doctor --repair     # try restoring from the .bak backup; quarantine if still corrupt")
	fmt.Fprintln(stderr, util.Bi(
		"Or manually inspect ~/.whale/settings.json (and the project-level .whale/settings.json).",
		"或手动检查 ~/.whale/settings.json（及项目内 .whale/settings.json）。"))
	os.Exit(1)
}

// D 批：applyCustomPricing 随成本线删除 ——
// 它把 settings.json 的 custom_pricing 注入 ai 包的全局定价表，而那张表算出的
// 金额是我们自己估的（provider 不上报、也不等于账单，且混币种），已整体删除。

// shutdownMetrics 优雅关闭 metrics HTTP server（5-E-3）。
// srv 为 nil 时直接返回；5 秒超时防止阻塞退出流程。
func shutdownMetrics(srv *http.Server) {
	if srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Warn("Metrics server shutdown error", "error", err)
	}
}

// ensureAPIKeyOrExit 在模型名解析之前检查 API Key 是否已配置。
// 若环境变量与 settings.json 中 API Key 均为空，则输出错误信息并以退出码 1 退出。
//
// 背景：loadOrCreateModelRegistry 内部调用 resolveModel，当未配置模型名时会先于
// createClient 退出并输出 "No model name set"，掩盖真正根因（缺少 API Key）。
// 此函数将 API Key 检查前移，使无 API Key 场景统一输出 "No API Key set"，
// 与 createClient / checkAPI 的行为保持一致，便于用户定位配置缺失。
// 保留 createClient 内部的同名检查作为防御性兜底（未来若有新调用方不经过此函数）。
//
// N18：新增 cfg 参数，使 resolveProviderConfig 能回退到 settings.json 中的 APIKey 字段
// （Onboarding 交互输入的 API key 存储在 settings.json）。
func ensureAPIKeyOrExit(cfg *config.Config) {
	apiKey, _, _ := resolveProviderConfig(cfg)
	if apiKey == "" {
		const message = "No API Key set. Set ANTHROPIC_API_KEY or OPENAI_API_KEY, or run /onboarding"
		// 非 debug 模式会替换 os.Stderr 指针；保留的原始句柄仍连接调用方，
		// 让配置错误稳定显示给 CLI 用户和自动化调用方。
		fmt.Fprintln(terminalStderr, message)
		log.Error(message)
		os.Exit(1)
	}
}

// createClient create AI client
func createClient(cfg *config.Config) *ai.SimpleClient {
	apiKey, provider, baseURL := resolveProviderConfig(cfg)

	if apiKey == "" {
		log.Error("No API Key set. Set ANTHROPIC_API_KEY or OPENAI_API_KEY, or run /onboarding")
		os.Exit(1)
	}

	model := resolveModel(cfg)
	return ai.NewSimpleClient(model, apiKey, baseURL, provider)
}

// resolveProviderConfig resolve API key, provider, and base URL.
// Priority: ANTHROPIC_API_KEY env > OPENAI_API_KEY env > settings.json APIKey/Provider/CustomBaseURL
//
// N18：新增 cfg 参数，当环境变量未设置时回退到 settings.json 中的字段
// （Onboarding 交互输入的 API key 通过 settings.json 持久化）。
func resolveProviderConfig(cfg *config.Config) (apiKey, provider, baseURL string) {
	// 1. 优先环境变量（现有行为，向后兼容）
	apiKey = os.Getenv("ANTHROPIC_API_KEY")
	provider = ai.ProviderAnthropic
	baseURL = ""

	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
		provider = ai.ProviderOpenAI
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}

	// 2. 环境变量均空时回退到 settings.json（N18 Onboarding 存储的值）
	if apiKey == "" && cfg != nil {
		if cfg.APIKey != "" {
			apiKey = cfg.APIKey
			if cfg.Provider != "" {
				provider = cfg.Provider
			}
			if cfg.CustomBaseURL != "" {
				baseURL = cfg.CustomBaseURL
			}
		}
	}

	return
}

// resolveModel resolve model name with validation.
//
// 优先级（P7 候选 A / P6 候选 B，2026-07-28 已拍板，见
// 内部交付台账:454-469）：
//
//	settings.json 的 model > WHALE_MODEL 环境变量 > 报错退出
//
// ★ 这里【不得】再直接读 os.Getenv("WHALE_MODEL")：
// - env 的注入已由 internal/config/config.go 的 applyEnvironmentOverrides（P6 候选 B）
// 在 Load() 内完成 —— 仅当 settings.json 的 model 为空时才填进 cfg.Model；
// - 旧实现无条件读 env ⇒ 覆盖 settings.json，而 /model 的写路径只落 settings.json
// （internal/tui/commands_model.go handleModelSwitch → loader.Save）
// ⇒ 用户切换模型后冷启动被 env 盖回，即"写了个读不到的地方"。
//
// 同一事实只允许一处定义（同源）；判据见 model_resolution_test.go。
// Validates model name format before returning to fail early.
func resolveModel(cfg *config.Config) string {
	if cfg != nil && cfg.Model != "" {
		if err := validateModelName(cfg.Model); err != nil {
			log.Error("Invalid model name from config", "model", cfg.Model, "error", err)
			os.Exit(1)
		}
		return cfg.Model
	}
	log.Error("No model name set. Set the model field in ~/.whale/settings.json or WHALE_MODEL env var")
	os.Exit(1)
	return ""
}

// validateModelName checks that a model name is non-empty and contains
// only valid characters (alphanumeric, hyphens, underscores, dots, colons, slashes).
func validateModelName(name string) error {
	if name == "" {
		return fmt.Errorf("model name is empty")
	}
	for _, ch := range name {
		if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
			ch == '-' || ch == '_' || ch == '.' || ch == ':' || ch == '/') {
			return fmt.Errorf("model name contains invalid character: %q", ch)
		}
	}
	return nil
}

// validateModelExists checks that the model name exists in the model registry.
// Returns nil if the model is registered, error otherwise.
func validateModelExists(name string, registry *ai.ModelRegistry) error {
	if registry == nil {
		return fmt.Errorf("model registry is not initialized")
	}
	if registry.Get(name) == nil {
		available := registry.Names()
		return fmt.Errorf("model %q is not registered. Available models: %s", name, strings.Join(available, ", "))
	}
	return nil
}

// loadOrCreateModelRegistry load model registry, auto-create default entry from .env if not exists
func loadOrCreateModelRegistry(cfg *config.Config) *ai.ModelRegistry {
	// 收口：状态目录唯一走 whalehome.Dir()。
	modelsPath := filepath.Join(whalehome.Dir(), "models.json")
	cfg.ModelsJSONPath = modelsPath

	registry, err := ai.LoadModels(modelsPath)
	if err != nil {
		log.Warn("Model registry load failed", "error", err)
		registry = ai.NewModelRegistry()
	}

	modelName := resolveModel(cfg)
	apiKey, provider, baseURL := resolveProviderConfig(cfg)

	needSave := false

	if registry.Get(modelName) == nil {
		log.Warn("Model not found in registry, auto-configuring from environment",
			"model", modelName, "provider", provider)
		apiKeyRef := resolveAPIKeyRef()
		mc := &ai.ModelConfig{
			Name:      modelName,
			Provider:  provider,
			BaseURL:   baseURL,
			APIKey:    apiKey,
			APIKeyRef: apiKeyRef,
		}
		mc.FillDefaults()
		registry.Add(mc)
		needSave = true
	}

	if cfg.FallbackModel != "" && registry.Get(cfg.FallbackModel) == nil {
		apiKeyRef := resolveAPIKeyRef()
		mc := &ai.ModelConfig{
			Name:      cfg.FallbackModel,
			Provider:  provider,
			BaseURL:   baseURL,
			APIKey:    apiKey,
			APIKeyRef: apiKeyRef,
		}
		mc.FillDefaults()
		registry.Add(mc)
		needSave = true
	}

	if needSave {
		if err := ai.SaveModels(modelsPath, registry); err != nil {
			log.Warn("Failed to save models.json", "error", err)
		}
	}

	// Validate that the resolved model exists in the registry after auto-configuration
	if err := validateModelExists(modelName, registry); err != nil {
		log.Error("Model validation failed", "error", err)
		os.Exit(1)
	}

	// B1：把 models.json 里用户声明的 thinking 能力灌进 internal/ai 的声明层。
	// ★ 必须在这里做，且只做一次（启动单点）—— 否则 `/model add --supports-thinking`
	// 写下的声明**重启后就失效**：下发判据 ai.ThinkingSupportFor 在 internal/ai 深处，
	// 够不到 ModelConfig。声明优先于能力表/前缀推断，见 capability_declaration.go。
	registerThinkingDeclarations(registry)

	return registry
}

// registerThinkingDeclarations 把模型注册表里的用户声明同步给 ai 的声明层（B1）。
//
// 只登记**能解析**的声明（ParseThinkingCapability 的 ok）：空串与垃圾值一律跳过，
// 跳过之后该模型回到能力表/前缀推断 —— 这正是"未声明"应有的语义。
func registerThinkingDeclarations(registry *ai.ModelRegistry) {
	if registry == nil {
		return
	}
	for _, mc := range registry.List() {
		if mc == nil {
			continue
		}
		if t, ok := ai.ParseThinkingCapability(mc.ThinkingSupport); ok {
			ai.RegisterThinkingDeclaration(mc.Name, t)
		}
	}
}

// resolveAPIKeyRef determines the environment variable name for the current API key
func resolveAPIKeyRef() string {
	if os.Getenv("ANTHROPIC_API_KEY") != "" {
		return "ANTHROPIC_API_KEY"
	}
	if os.Getenv("OPENAI_API_KEY") != "" {
		return "OPENAI_API_KEY"
	}
	return ""
}

// loadEnvFiles 加载 .env 文件到环境变量（P6 候选 A：调整加载顺序 + 先到先得语义）。
//
// 加载顺序（先加载优先级最高，配合 loadEnvFile 的"仅当 key 未设置时才注入"实现）：
// 1. 用户全局 ~/.whale/.env（先加载，优先级最高）
// 2. 项目级 ./.env.dev（仅当 WHALE_DEV=1 时加载，优先级低于用户全局）
//
// 最终优先级：启动进程前已设置的系统环境变量 > ~/.whale/.env > ./.env.dev
// （loadEnvFile 仅当 key 当前为空时才注入；已有环境变量不会被文件覆盖，先加载的文件先占位）
//
// 安全说明：项目根的 .env 不再自动加载，避免误提交的密钥覆盖用户全局配置。
// 开发期如需项目级覆盖，请将 .env 改名为 .env.dev 并设置 WHALE_DEV=1。
func loadEnvFiles() {
	// 1. 用户全局 .env（先加载，优先级最高； 收口后 .env 跟随状态目录，
	// WHALE_HOME 设定时即加载 $WHALE_HOME/.env）
	loadEnvFile(filepath.Join(whalehome.Dir(), ".env"))

	// 2. 项目级 .env.dev（仅开发期 WHALE_DEV=1 时加载，优先级低于用户全局）
	if os.Getenv("WHALE_DEV") == "1" {
		cwd, err := os.Getwd()
		if err != nil {
			log.Warn("Failed to get working directory for .env.dev loading", "error", err)
			return
		}
		envDevPath := filepath.Join(cwd, ".env.dev")
		if _, statErr := os.Stat(envDevPath); statErr != nil {
			// .env.dev 不存在但 .env 存在，提示用户改名
			envPath := filepath.Join(cwd, ".env")
			if _, e := os.Stat(envPath); e == nil {
				log.Warn("项目根存在 .env，建议改名为 .env.dev 并设置 WHALE_DEV=1 后才会加载")
			}
		} else {
			loadEnvFile(envDevPath)
		}
	}

	applyEnvDerivedToolAvailability()

	// （2026-09-20 批 D）：把 env 派生的「分级输出限制」装载进 tools 的包级 SSOT。
	//
	// 与 applyEnvDerivedToolAvailability 同一理由选在此处：本函数是**唯一 env 咽喉点**
	// ——主路径 / `whale doctor` / `whale --check-api` 三条入口都经此，
	// 于是"新加了一条入口却忘了接线"被结构性排除（靠"记得多写一行"则代价是静默的）。
	//
	// 时机即语义：H13-G4 规定一次性读取、运行时不动态变更（TTL）⇒ 必须在会话开始前装载。
	// 修复前 LoadFromEnv 全仓零调用 ⇒ WHALE_BASH_MAX_OUTPUT / WHALE_TASK_MAX_OUTPUT /
	// WHALE_MAX_OUTPUT_TOKENS 三个 env「设了不生效」。
	tools.LoadOutputLimitsFromEnv()
}

// applyEnvDerivedToolAvailability 把"环境派生的能力位"落到工具面（S3 · 裁定 C=②）。
//
// 为什么接线点选在 loadEnvFiles 里、而不是 main() 里单写一行：
// 本函数是**唯一**加载 env 的咽喉点 —— 主路径（main.go 启动）、`whale doctor`、
// `whale --check-api` 三条入口都经此。放在这里可以**结构性**排除
// "新加了一条入口却忘了接线 ⇒ 工具面与实际能力不一致"这类静默假修复；
// 靠"记得多写一行"则忘记的代价是静默的（正是本仓反复吃亏的那一类）。
//
// 语义：tools.toolAvailable 默认"全部可用"，所以本调用**只在无通道时**产生行为差异
// （无通道 ⇒ WebSearch 不注册、不下发、不可执行）；有通道时写入的正是默认值，等价于没写。
func applyEnvDerivedToolAvailability() {
	tools.SetToolAvailability("WebSearch", tools.WebSearchChannel() != tools.ChannelNone)

	// GitHub 面（S3 · 裁定 C=②；修）：同一谓词、同一咽喉点。
	//
	// 与 WebSearch 唯一的差别只在于"能力"的判据：检索那边要能解析出可用契约，
	// GitHub 这边就是"有没有 GITHUB_TOKEN"。**装配与判定都各只有一处**：
	// 判据在 github.ApplyToolAvailability（唯一"写"点），消费在 tools.ToolAvailable（唯一"读"口）。
	// 于是"没有凭据 ⇒ 12 个 github_* 不进工具面（注册点/档位目录/执行门三面一致）"
	// 是**一条派生事实**，而不是三处各自记得要判断。
	github.ApplyToolAvailability()

	// MCP / LSP 面（/ · W5）：同一谓词、同一咽喉点。
	//
	// MCP 判据 = 启动时刻是否配置了任何 MCP server（loadMCPConfig 读 <workDir>/.whale/mcp.json
	// + ~/.whale/mcp.json）；LSP 判据 = 启动时刻 PATH 上是否有任一语言服务器
	// （lsp.AnyLanguageServerAvailable 探针）。二者都是**保守能力位**，零 server 才隐藏，
	// 与"启动时刻配置承诺"语义一致（/mcp add 明说 restart）。
	//
	// LSP 的语义边界（拍板）：**不得照抄启动期全局谓词**——能力位是运行期 per-language，
	// 所以探针只在"一个 server 都没有"时才整体隐藏（此时 100% 不可用、无误伤）；有任一 server
	// 就保留，per-language 可用性仍交运行期 getLSPClient 判定。
	workDir, _ := os.Getwd()
	tools.ApplyMCPToolAvailability(workDir)
	tools.ApplyLSPToolAvailability()
}

// applyNativeSearchOverrides 把 settings.json 里显式声明的"原生检索能力"推给 ai 包。
//
// 为什么必须"推"而不能"拉"：`internal/config` 已经 import `internal/ai`（memory_*.go 等处），
// ai 反向 import config 会成环 ⇒ ai 读不到 config。与批 1 的 tools.SetToolAvailability 同一手法。
//
// 语义（单向、只用于启用）：`model_capabilities.<片段>.supports_native_search: true`
// ⇒ 即便该模型不在 ai 的内置能力表里，也允许对它注入 enable_search。
// 关闭仍走全局开关，不在这里表示（原因见 ai.SetNativeSearchModelOverride 的注释）。
func applyNativeSearchOverrides(cfg *config.Config) {
	if cfg == nil || len(cfg.ModelCapabilities) == 0 {
		ai.SetNativeSearchModelOverride(nil)
		return
	}
	models := make([]string, 0, len(cfg.ModelCapabilities))
	for prefix, caps := range cfg.ModelCapabilities {
		if caps.SupportsNativeSearch {
			models = append(models, prefix)
		}
	}
	ai.SetNativeSearchModelOverride(models)
}

// loadEnvFile 加载单个 .env 文件到环境变量。
// Supports: comments, quoted values with escape sequences, empty values
//
// 注入语义（P6 候选 A）：仅当 key 未设置时才注入（先到先得），
// 配合 loadEnvFiles 的加载顺序实现"先加载优先级最高"。
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.Index(line, "=")
		if idx <= 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])

		value = unquoteEnvValue(value)

		if key == "" {
			continue
		}

		// 仅当 key 未设置时才注入（先到先得语义，配合加载顺序实现优先级）
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				log.Warn("Failed to set env var", "key", key, "error", err)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		log.Warn("Error reading .env file", "path", path, "error", err)
	}
}

// unquoteEnvValue removes surrounding quotes and handles escape sequences
func unquoteEnvValue(value string) string {
	if len(value) >= 2 {
		quoteChar := value[0]
		if (quoteChar == '"' || quoteChar == '\'') && value[len(value)-1] == quoteChar {
			inner := value[1 : len(value)-1]
			return strings.ReplaceAll(inner, `\`+string(quoteChar), string(quoteChar))
		}
	}
	return value
}

// initFileLog 初始化文件日志 sink（stderr 之外双写 ~/.whale/logs/whale.log）。
// 路径可经 WHALE_LOG_FILE 环境变量覆盖；失败不阻塞启动，降级到仅 stderr。
//
// D4 修复：返回日志文件路径，供 main 函数非 debug 模式下重定向 stderr 使用。
//
// O-1 修复（桶 X6）：默认参数调整为 100MB 单文件 / 3 备份 / 30 天保留（对标 98 文档建议）。
// 同时在初始化后执行：
// 1. 磁盘剩余空间检查（<1GB 警告）
// 2. quality.jsonl / trace.jsonl 按日期分片 + 30 天清理
//
// 默认参数：100MB 单文件 / 3 备份 / 30 天保留 / gzip 压缩（lumberjack size-based 轮转）。
//
// 返回值：日志文件绝对路径；未配置日志文件或初始化失败时返回空字符串。
func initFileLog() string {
	logFile := os.Getenv("WHALE_LOG_FILE")
	if logFile == "" {
		// 收口：日志默认跟随状态目录（$WHALE_HOME/logs/whale.log）。
		if whalehome.Dir() != "" {
			logFile = filepath.Join(whalehome.Dir(), "logs", "whale.log")
		}
	}
	if logFile == "" {
		return ""
	}
	// O-1：调整默认参数为 100MB / 3 备份 / 30 天（原 10MB / 5 / 7）
	if err := log.InitFileLogger(logFile, 100, 3, 30); err != nil {
		fmt.Fprintf(os.Stderr, "[log] 文件 sink 初始化失败，降级 stderr: %v\n", err)
		return ""
	}

	// O-1：启动时检查磁盘剩余空间（<1GB 警告，不阻断启动）
	if whalehome.Dir() != "" {
		whaleDir := whalehome.Dir()
		log.CheckDiskSpace(whaleDir)

		// O-1：对 quality.jsonl / trace.jsonl 按日期分片 + 30 天清理
		// 这些文件不走 lumberjack（由 orchestrator 包管理），需单独轮转
		orchDir := filepath.Join(whaleDir, "orchestrator")
		qualityPath := filepath.Join(orchDir, "quality.jsonl")
		tracePath := filepath.Join(orchDir, "review_trace.jsonl")
		if rotated, cleaned, err := log.RotateJSONLByDate(qualityPath, 30); err != nil {
			// 轮转失败不阻断启动，仅记录
			fmt.Fprintf(os.Stderr, "[log] quality.jsonl 轮转失败: %v\n", err)
		} else if rotated || cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[log] quality.jsonl 轮转：分片=%v，清理=%d\n", rotated, cleaned)
		}
		if rotated, cleaned, err := log.RotateJSONLByDate(tracePath, 7); err != nil {
			// trace 7 天清理（98 文档建议 trace 7 天，quality 30 天）
			fmt.Fprintf(os.Stderr, "[log] trace.jsonl 轮转失败: %v\n", err)
		} else if rotated || cleaned > 0 {
			fmt.Fprintf(os.Stderr, "[log] trace.jsonl 轮转：分片=%v，清理=%d\n", rotated, cleaned)
		}
	}
	return logFile
}

// checkAPI check if API connection works（N17：内部调用 doctor.Run() 完成全量诊断）
// 保留 --check-api CLI 入口，行为兼容：仍加载 env、测试连通性、失败时 exit 1。
// 新增：输出完整诊断报告（环境/配置/依赖/平台/连通性），而非仅 API 三步检测。
func checkAPI() {
	loadEnvFiles()

	// 解析工作目录
	workDir, _ := os.Getwd()

	// 收口：状态目录唯一走 whalehome.Dir()。
	settingsPath := filepath.Join(whalehome.Dir(), "settings.json")

	// 从环境变量解析 API key / provider / baseURL（复用 resolveProviderConfig）
	// checkAPI 路径不加载 settings.json，传 nil 保持 env-only 行为
	apiKey, provider, baseURL := resolveProviderConfig(nil)

	// 从环境变量解析 model
	model := os.Getenv("WHALE_MODEL")

	// 构建最小 Config（仅设置 Model，其余用默认值）
	cfg := config.DefaultConfigPtr()
	if model != "" {
		cfg.Model = model
	}

	// 创建 AI client（API key 或 model 缺失时传 nil，doctor 内部标记 fatal）
	var client ai.Client
	if apiKey != "" && model != "" {
		client = ai.NewSimpleClient(model, apiKey, baseURL, provider)
	}

	// 执行 doctor 全量诊断
	doc := doctor.NewDoctor(cfg, client, workDir, settingsPath)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	results := doc.Run(ctx)

	// 保留稳定的命令标题，便于人工识别和脚本兼容；详细结果由 doctor 渲染。
	fmt.Println("API Connection Check")
	// 渲染并输出结果
	fmt.Println(doc.Render(results))

	// 含致命错误时退出码 1（对标原 checkAPI 行为）
	if doc.HasFatal(results) {
		os.Exit(1)
	}
}

// printAPIErrorHelp print helpful error diagnosis based on API error
func printAPIErrorHelp(err error, baseURL, model, fallbackModel string) {
	errMsg := err.Error()

	if strings.Contains(errMsg, "403") || strings.Contains(errMsg, "AllocationQuota") {
		fmt.Println("API returned 403 (Forbidden). Possible causes:")
		fmt.Println("  1. Free tier quota exhausted - enable paid access in the provider console")
		fmt.Println("  2. API Key does not have permission to access this model")
		fmt.Println("  3. Account billing issue - check your provider dashboard")
		fmt.Println()
		if strings.Contains(errMsg, "FreeTierOnly") || strings.Contains(errMsg, "free tier") {
			fmt.Printf("  Model '%s' free tier is exhausted.\n", model)
			fmt.Println("  Fix: Disable 'use free tier only' in your provider management console,")
			fmt.Println("       or switch to a model with available quota.")
			if fallbackModel != "" && fallbackModel != model {
				fmt.Printf("  Tip: Try fallback model: set WHALE_MODEL=%s\n", fallbackModel)
			}
		}
	} else if strings.Contains(errMsg, "401") || strings.Contains(errMsg, "Unauthorized") || strings.Contains(errMsg, "invalid api key") {
		fmt.Println("API Key is invalid or expired. Possible fixes:")
		fmt.Println("  1. Check your API Key is correct")
		fmt.Println("  2. Regenerate the key in your provider dashboard")
		fmt.Println("  3. Make sure the key matches the provider (Anthropic vs OpenAI)")
	} else if strings.Contains(errMsg, "429") || strings.Contains(errMsg, "rate limit") || strings.Contains(errMsg, "Rate limit") {
		fmt.Println("Rate limited by the API. Possible fixes:")
		fmt.Println("  1. Wait a moment and try again")
		fmt.Println("  2. Reduce request frequency")
		fmt.Println("  3. Upgrade your API plan for higher rate limits")
		if fallbackModel != "" && fallbackModel != model {
			fmt.Printf("  4. Try fallback model: set WHALE_MODEL=%s\n", fallbackModel)
		}
	} else if strings.Contains(errMsg, "context canceled") {
		fmt.Println("Request was canceled. Possible causes:")
		fmt.Println("  1. Request took too long (timeout)")
		fmt.Println("  2. Network connection was interrupted")
		fmt.Println("  3. You pressed Ctrl+C during the request")
		fmt.Println()
		fmt.Println("Possible fixes:")
		fmt.Println("  1. Check your internet connection")
		fmt.Println("  2. Set HTTP_PROXY / HTTPS_PROXY if behind a proxy")
		fmt.Println("  3. Try again - the server might be temporarily slow")
		host := extractHost(baseURL)
		if host != "" {
			fmt.Printf("  4. Test connectivity: ping %s\n", host)
		}
	} else if strings.Contains(errMsg, "context deadline exceeded") {
		fmt.Println("Connection timed out (30s). Possible fixes:")
		fmt.Println("  1. Check your internet connection")
		fmt.Println("  2. The API server might be slow or overloaded - try again later")
		fmt.Println("  3. Set HTTP_PROXY / HTTPS_PROXY if behind a proxy")
		host := extractHost(baseURL)
		if host != "" {
			fmt.Printf("  4. Test connectivity: ping %s\n", host)
		}
	} else if strings.Contains(errMsg, "lookup ") || strings.Contains(errMsg, "no such host") {
		fmt.Println("DNS resolution failed. Possible fixes:")
		fmt.Println("  1. Check internet connection")
		fmt.Println("  2. Set HTTP_PROXY / HTTPS_PROXY if behind a proxy")
		host := extractHost(baseURL)
		if host != "" {
			fmt.Printf("  3. Try: nslookup %s\n", host)
		}
	} else if strings.Contains(errMsg, "connection refused") {
		fmt.Println("The server refused the connection. Possible fixes:")
		fmt.Println("  1. Check OPENAI_BASE_URL is correct")
		fmt.Println("  2. The server might be down - try again later")
	} else if strings.Contains(errMsg, "timeout") {
		fmt.Println("Connection timed out. Possible fixes:")
		fmt.Println("  1. Check internet connection")
		fmt.Println("  2. The API server might be slow or blocked")
		fmt.Println("  3. Set HTTP_PROXY / HTTPS_PROXY if behind a proxy")
	} else if strings.Contains(errMsg, "model overloaded") || strings.Contains(errMsg, "529") {
		fmt.Println("The model is overloaded. Possible fixes:")
		fmt.Println("  1. Wait a moment and try again")
		if fallbackModel != "" && fallbackModel != model {
			fmt.Printf("  2. Try fallback model: set WHALE_MODEL=%s\n", fallbackModel)
		}
	} else {
		fmt.Println("Unknown error. General troubleshooting:")
		fmt.Println("  1. Check internet connection")
		fmt.Println("  2. Verify API Key and Base URL are correct")
		fmt.Println("  3. Try a different model")
		if fallbackModel != "" && fallbackModel != model {
			fmt.Printf("  4. Try fallback model: set WHALE_MODEL=%s\n", fallbackModel)
		}
	}
}

// extractHost extract hostname from URL
func extractHost(rawURL string) string {
	s := strings.TrimPrefix(rawURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	if idx := strings.Index(s, "/"); idx > 0 {
		s = s[:idx]
	}
	if idx := strings.Index(s, ":"); idx > 0 {
		s = s[:idx]
	}
	return s
}
