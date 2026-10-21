package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/anchorandfish/whale-code/internal/agentguard"
	"github.com/anchorandfish/whale-code/internal/ai"
	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/config"
	ctxpkg "github.com/anchorandfish/whale-code/internal/context"
	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/doctor"
	gh "github.com/anchorandfish/whale-code/internal/github"
	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/mention"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
)

// PlainRunner runs Whale Code in plain text mode (no TUI)
// Supports full native copy/paste in Windows terminal
type PlainRunner struct {
	*runnerCore
	// exitCh 收到信号后关闭，用于通知主循环退出（替代直接 os.Exit，避免跳过 defer）
	exitCh           chan struct{}
	exitOnce         sync.Once
	dispatchHandlers map[string]func(name, args, output string) string
	daemonControl    daemonController
	// guard P0 agent 防护（agent 防护体系 2026-08-31）：轮次上限/无进展熔断/
	// 审查子循环上限/进度检测。processInput 时 BeginRequest 归零；
	// streamAIRequest 顶部统一闸门判定。nil 时防线降级放行。
	guard *agentguard.Guard
}

// NewPlainRunner create a plain mode runner with full feature set
func NewPlainRunner(cfg *config.Config, client *ai.SimpleClient, reg *ai.ModelRegistry) *PlainRunner {
	workDir, _ := os.Getwd()
	homeDir, _ := os.UserHomeDir()
	sessionDir := filepath.Join(whalehome.Dir(), "sessions")

	core := NewRunnerCore(cfg, client, reg, workDir, homeDir, sessionDir, "Chinese")

	runner := &PlainRunner{
		runnerCore:    core,
		exitCh:        make(chan struct{}),
		daemonControl: daemon.NewController(daemon.DaemonConfig{WorkDir: workDir}),
		guard:         newAgentGuard(cfg),
	}

	// Register common command callbacks
	registerCommonCommands(core,
		func() string { return runner.handleUndo() },
		func() string { return runner.handleRedo() },
		func(args string) string { return runner.handlePluginInstall(args) },
		func(args string) string { return runner.handlePluginEnable(args) },
		func(args string) string { return runner.handlePluginDisable(args) },
		func() string { return runner.handleReloadPlugins() },
		func(args string) string { return runner.handleGitHub(args) },
		func(args string) string { return runner.handleDaemon(args) },
	)

	// initialize tool execution pipeline (already done in NewRunnerCore)

	// S6/G8 ③ 阶段一：失败不再静默 —— error 落 ~/.whale/logs/whale.log（同 lifecycle.go）。
	// plain（非 TUI）模式是第三个生产调用点；只改 lifecycle.go 会让本模式下的 MCP 失败继续完全静默。
	// 本文件约定用 internal/log 的结构化 logger（见同文件 log.Info("Received signal", ...)）。
	go func() {
		if err := runner.Executor.DiscoverAndRegisterMCPTools(workDir); err != nil {
			log.Error("[mcp] tool discovery failed", "error", err)
		}
	}()

	runner.dispatchHandlers = runner.initDispatchHandlers()

	// register /fcache command callback
	runner.CmdRegistry.OnFileCache = func(args string) string {
		return runner.handleFileCache(args)
	}

	// register /doctor command callback（N17：接线 /doctor 命令到 internal/doctor 包）
	// 创建 Doctor 实例并执行全量检测，返回树形文本渲染结果。
	runner.CmdRegistry.OnDoctor = func(args string) (string, error) {
		settingsPath := filepath.Join(whalehome.Dir(), "settings.json")
		doc := doctor.NewDoctor(runner.Config, runner.AIClient, runner.WorkDir, settingsPath)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		results := doc.Run(ctx)
		return doc.Render(results), nil
	}

	// P0-4 修复：接线 Session 类命令回调，避免 PlainRunner 模式泄露魔法字符串
	// TUI 模式由 app.go 拦截（case "session list" 等），Bridge 模式由 bridge.go 拦截，
	// PlainRunner 模式通过回调直接返回真实结果（复用已有的 handleSession* 方法）。
	runner.CmdRegistry.OnSessionList = func() string { return runner.handleSessions() }
	runner.CmdRegistry.OnSessionSave = func() string { return runner.handleSessionSave() }
	runner.CmdRegistry.OnSessionSwitch = func(args string) string { return runner.handleSessionSwitch(args) }
	runner.CmdRegistry.OnResume = func(args string) string { return runner.handleResume(args) }
	runner.CmdRegistry.OnSessionMerge = func(args string) string { return runner.handleSessionMerge(args) }
	runner.CmdRegistry.OnSessionSplit = func(args string) string {
		return "Session split is not supported in plain mode. Use TUI mode for full session management."
	}
	runner.CmdRegistry.OnRename = func(args string) string {
		if args == "" {
			return "Usage: /rename <name>"
		}
		// 只改别名，不改主键（UUID 文件名）。
		// 旧实现调 Store.Rename(oldName, args) 并同步 SessionName —— UUID 化后
		// 那等于给会话换地址，已发出的 --session <id> 引用会全部失效。
		runner.Session.Metadata.Name = args
		if runner.SessionName != "" && runner.Store != nil {
			if err := runner.Store.SetAlias(runner.SessionName, args); err != nil {
				return fmt.Sprintf("Rename failed: %v", err)
			}
		}
		return fmt.Sprintf("Session renamed to: %s", args)
	}

	// P0-16 修复（v4 模块审查）：接线 /import 命令回调到真实的导入逻辑。
	// 原实现 PlainRunner 未接线 OnImport，走 handleImport 桩（command.go:751-760），
	// 返回 "Session imported" 但不真正导入，导致假成功。
	// 修复后调用 runnerCore.handleImportSessionShared，与 TUI 模式行为一致。
	runner.CmdRegistry.OnImport = func(args string) (string, error) {
		return runner.handleImportSessionShared(args), nil
	}

	// B7-P1-1 修复：接线 /summary 命令回调到 handleSummaryShared。
	// 原实现 PlainRunner 未接线 OnSummary，走 Registry 桩返回 "Generating summary..."（假成功）。
	// 修复后调用 runnerCore.handleSummaryShared 生成基于统计的真实摘要，与 TUI 模式行为一致。
	runner.CmdRegistry.OnSummary = func() string {
		return runner.handleSummaryShared()
	}

	// B7-P1-6 修复：接线 /compact-strategy 命令回调到 handleCompactStrategyShared。
	// 原实现 PlainRunner 未接线 OnCompactStrategy，走 Registry 桩返回
	// "switched to: <args> (Note: strategy only takes effect in memory)"（假成功，不持久化）。
	// 修复后调用 runnerCore.handleCompactStrategyShared 实际更新 Config 并持久化到 settings.json。
	runner.CmdRegistry.OnCompactStrategy = func(strategy string) string {
		return runner.handleCompactStrategyShared(strategy)
	}

	// §4.1 + §4.5 修复：非 TUI 模式补齐 security.Init + sandbox.SetEnabled + wireH11N27Memory。
	// 等价于 TUI 模式 lifecycle.go 中的 4 步生产初始化，必须在 registerCommonCommands 之后调用。
	// wiringCancel 保存到 runnerCore，Close 时自动取消 ForgettingScanner goroutine。
	runner.wiringCancel = initProductionWiring(cfg, client, runner.runnerCore, homeDir, workDir)

	return runner
}

// Run start plain mode interactive loop
// Simple: Enter to send, /command for commands
//
// 信号处理（修复点5）：收到 SIGINT/SIGTERM 时通过 exitCh 通知主循环退出，
// 而非直接 os.Exit(0)。这样 Run() 正常 return 后，runPlainMode 中的
// defer runner.Close() 会执行，避免跳过资源清理。
// reader.ReadString 是阻塞调用，需用 goroutine 包装 + select 监听 exitCh。
func (r *PlainRunner) Run() error {
	printWelcome(r.ModelName, r.WorkDir, r.Policy.Mode().String())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Info("Received signal, stopping", "signal", sig)
		// 恢复信号默认行为：用户再次按 Ctrl+C 时由系统默认处理（立即终止），
		// 实现自然的“二次强制退出”语义，无需自行 os.Exit。
		signal.Stop(sigCh)
		r.cancelSignal()
	}()

	// defer cleanupOnExit：无论 Run() 经由信号退出还是 stdin EOF 退出，
	// 都会执行会话保存与缓存持久化（/exit 命令走 os.Exit 不触发 defer，已自行调用 cleanupOnExit）。
	defer r.cleanupOnExit()

	reader := bufio.NewReader(os.Stdin)

	for {
		if r.getState() != stateIdle {
			fmt.Print("\n... ")
		} else {
			fmt.Print("\n> ")
		}

		// 用 goroutine 包装阻塞的 ReadString，配合 select 监听 exitCh 实现信号可中断
		lineCh := make(chan string, 1)
		errCh := make(chan error, 1)
		go func() {
			line, err := reader.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			lineCh <- line
		}()

		select {
		case <-r.exitCh:
			// 信号通知退出：return 让 defer（cleanupOnExit + runPlainMode 的 runner.Close）正常执行
			fmt.Println("\nSession saved. Goodbye!")
			return nil
		case line := <-lineCh:
			// 编码兜底：Windows 中文环境 stdin 可能发送 GBK 字节，
			// 先解码为 UTF-8 再 trim（GBK 的 \r\n 是 ASCII，trim 顺序不影响）
			line = decodeStdinBytes([]byte(line))
			line = strings.TrimRight(line, "\r\n")
			// Strip UTF-8 BOM if present (PowerShell Get-Content may add it)
			line = strings.TrimPrefix(line, "\ufeff")

			// 走唯一谓词 command.IsCommandInput（不再裸 HasPrefix）。
			// line 只做了 TrimRight("\r\n") + 去 BOM，**未** TrimSpace ⇒ 前导空白逃逸语义天然保留。
			if command.IsCommandInput(line) {
				r.handleCommand(line)
				continue
			}

			r.processInput(line)
		case err := <-errCh:
			// stdin EOF 或读取错误：正常退出，触发 defer 清理
			_ = err
			return nil
		}
	}
}

// cancelSignal 关闭 exitCh 通知主循环退出。使用 sync.Once 保证只关闭一次。
func (r *PlainRunner) cancelSignal() {
	r.exitOnce.Do(func() {
		close(r.exitCh)
	})
}

// processInput handle user message input
func (r *PlainRunner) processInput(input string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}

	fmt.Printf("\n[You] %s\n", input)

	// parse mention, detect images
	mentions := mention.DetectMentions(input)
	resolved := mention.ResolveMentions(mentions, r.WorkDir)

	// build message content (use multimodal format for images)
	hasImage := false
	for _, rm := range resolved {
		if rm.IsImage {
			hasImage = true
			break
		}
	}
	if hasImage {
		// extract text part (remove original image mention references)
		textPart := input
		for _, rm := range resolved {
			if rm.IsImage {
				textPart = strings.Replace(textPart, rm.Raw, "", 1)
			}
		}
		textPart = strings.TrimSpace(textPart)

		// build image list
		var images []session.ImageBlock
		for _, rm := range resolved {
			if rm.IsImage {
				images = append(images, session.ImageBlock{
					MediaType:  rm.MediaType,
					Base64Data: rm.Base64,
				})
			}
		}
		r.Session.SetMessages(append(r.Session.GetMessages(), session.UserWithImages(textPart, images)))
	} else {
		expandedInput := mention.ExpandMentions(input, r.WorkDir)
		r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(expandedInput)))
	}
	// 双写期：设置当前消息 ID，供 FileHistory.Track/Snapshot 关联使用
	if r.Executor != nil {
		msgs := r.Session.GetMessages()
		if len(msgs) > 0 {
			r.Executor.SetCurrentMessageId(msgs[len(msgs)-1].UUID)
		}
	}

	// 取反侧同走唯一谓词（与 runLoop 判定侧同源）。
	if r.FirstUserInput == "" && !command.IsCommandInput(input) {
		r.FirstUserInput = input
	}

	if r.SessionName == "" {
		r.autoNameSession()
	}

	// P0 agent 防护：新用户请求开始，防护状态归零（轮次/熔断/审查/进度）。
	r.guardBeginRequest()
	r.setState(stateStreaming)
	r.streamAIRequest()
}

// streamAIRequest send request and stream AI response with retry recovery
func (r *PlainRunner) streamAIRequest() {
	// P0 agent 防护统一闸门：每次模型调用前判定（轮次上限/审查超限/空转/已停机）。
	// Stop 时已渲染进度快照并交回控制权，直接返回，不发起新的模型调用。
	if r.guardBeforeModelCall() {
		return
	}
	req := r.buildRequest()
	// H8：流超时从 Config.StreamTimeoutSeconds 读取（原硬编码 300s）
	streamTimeout := 300
	if r.Config != nil && r.Config.StreamTimeoutSeconds >= 30 {
		streamTimeout = r.Config.StreamTimeoutSeconds
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(streamTimeout)*time.Second)
	defer cancel()

	events, err := ai.StreamWithRetry(ctx, r.AIClient, req, r.RetryConfig, r.Fallback, r.RecoveryCoordinator)
	if err != nil {
		r.handleStreamError(err)
		return
	}

	r.handleStreamEvents(ctx, events)
}

// handleStreamEvents process AI stream events using shared processStreamEvents
func (r *PlainRunner) handleStreamEvents(ctx context.Context, events <-chan ai.Event) {
	fmt.Print("[Whale] ")

	handler := &plainTextHandler{}
	responseText, thinkingText, pendingTools, streamErr := r.runnerCore.processStreamEvents(ctx, events, handler)

	// Handle stream error: set state to idle and return
	if streamErr != nil {
		fmt.Printf("\n[Error] %v\n", streamErr)
		r.setState(stateIdle)
		return
	}

	fmt.Println()

	// Print thinking summary if available
	if thinkingText != "" && len(thinkingText) > 0 {
		summary := thinkingText
		if len(summary) > 200 {
			summary = summary[:200] + "..."
		}
		fmt.Printf("\n[Thinking] %s\n", summary)
	}

	blocks := make([]session.ContentBlock, 0)
	if responseText != "" {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: responseText})
	}
	blocks = append(blocks, pendingTools...)

	usage := &session.TokenUsage{
		InputTokens:  r.LastInputTokens,
		OutputTokens: r.LastOutputTokens,
	}
	assistantMsg := session.AssistantWithTools(blocks, r.ModelName, usage)
	r.Session.SetMessages(append(r.Session.GetMessages(), assistantMsg))

	for _, t := range pendingTools {
		fmt.Printf("\n[Tool: %s(%s)]\n", t.Name, strings.TrimSpace(t.Input))
	}

	if len(pendingTools) > 0 {
		r.setState(stateToolExec)
		r.executeAndContinue(pendingTools)
	} else {
		r.setState(stateIdle)
		r.Session.IncrementTurn()
		_ = r.saveSession()
		// 修复：每轮对话结束自动创建 FileHistory 快照
		r.autoSnapshotOnTurnEnd()
		// N26：触发 turn 结束钩子（goal.Manager.OnTurnEnd，）
		r.runTurnEndHooks(context.Background())
		r.checkAutoCompact()
		r.printStatus()

		// V2 auto preference extraction: detect and extract user preferences in background
		if r.MemExtractor != nil && r.Config.AutoMemoryEnabled {
			go func() {
				_ = r.MemExtractor.ExtractAndSave(context.Background(), r.AIClient, r.Session.GetMessages())
			}()
		}
	}
}

// plainTextHandler is a simple OutputHandler that prints text to stdout
type plainTextHandler struct{}

// OnText prints text content to stdout
func (h *plainTextHandler) OnText(text string) {
	fmt.Print(text)
}

// OnThinking handles thinking content (not displayed in plain mode by default)
func (h *plainTextHandler) OnThinking(text string) {
	// Thinking is collected separately, not printed inline
}

// OnToolUse handles tool use notification
func (h *plainTextHandler) OnToolUse(name string, input json.RawMessage) {
	// Tool use notification handled after stream completes
}

// OnToolResult handles tool result notification
func (h *plainTextHandler) OnToolResult(result string) {
	// Tool result handled separately
}

// OnError handles error events
func (h *plainTextHandler) OnError(err error) {
	// Error state is managed by the caller (handleStreamEvents)
}

// executeAndContinue execute tool and send results back to AI
func (r *PlainRunner) executeAndContinue(pendingTools []session.ContentBlock) {
	// onConfirm callback for Pipeline safety confirmation in terminal mode
	onConfirm := func(toolName, input string) bool {
		fmt.Printf("\nSafety confirmation / 安全确认\n%s\nAllow once or deny / 允许本次或拒绝\n", approvalTargetSummary(toolName, input))
		if r.promptConfirm() {
			return true
		}
		return false
	}

	// resultCallback prints tool results to console
	// 同时收集结果供 P0 防护观测（无进展熔断/进度检测输入）。
	guardResults := make([]tools.Result, 0, len(pendingTools))
	resultCallback := func(res tools.Result) {
		guardResults = append(guardResults, res)
		// ★ --plain 没有 /tool 命令（本文件无 case "tool"）⇒ 不得提示一个该入口不存在的命令
		// （同族：同一事实跨入口必须同源）。传空 Command ⇒ 省略行只报行数、不指路。
		display := tools.FormatToolResultStructWithHint(res, tools.MaxToolResultLength, tools.ToolFoldHint{})
		fmt.Printf("\n%s\n", display)
	}

	r.runnerCore.executeToolsAndContinue(pendingTools, resultCallback, onConfirm)
	// P0 agent 防护：每轮工具落定后记录观测 + 无进展熔断判定。
	// 熔断时已渲染停机快照（含增量 turn 收尾），不再发起下一轮模型调用。
	if r.guardAfterToolRound(pendingTools, guardResults) {
		return
	}
	r.streamAIRequest()
}

// handleSkillPrompt handle Prompt Skill commands (skillify/simplify/verify/batch)
// inject rendered prompt as user message and trigger AI stream
func (r *PlainRunner) handleSkillPrompt(skillName, args, output string) string {
	// output is the result returned by the command handler (prompt rendered via OnSkillExecute)
	if output == "" || strings.Contains(output, "not initialized") {
		return output
	}
	// inject rendered prompt as user message
	r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(output)))
	if r.FirstUserInput == "" {
		r.FirstUserInput = output
	}
	if r.SessionName == "" {
		r.autoNameSession()
	}
	r.guardBeginRequest() // P0 防护：Skill 注入视为新用户请求，状态归零
	r.setState(stateStreaming)
	r.streamAIRequest()
	return "" // no longer display output since it was sent to AI as user message
}

// handleSecurityReview 处理 /security-review 命令（B7-P1-2 修复）。
// 对齐 TUI Model.handleSecurityReview 的逻辑：通过 runnerCore.buildSecurityReviewPrompt
// 构建 AI 审查提示词（优先加载 security-review skill 模板，回退内置模板），
// 注入会话作为用户消息并触发 AI 流式响应，执行真实安全审查。
// 原实现走 Registry 桩返回静态 "Security review started" 文本（假成功，不触发 AI）。
func (r *PlainRunner) handleSecurityReview(args string) string {
	reviewPrompt := r.buildSecurityReviewPrompt(args)
	if reviewPrompt == "" {
		return "Security review prompt generation failed"
	}

	// 显示命令确认（对齐 TUI 的 displayText 行为）
	fmt.Printf("\n🛡️ Security review started\n")

	// 注入审查提示词作为用户消息
	r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(reviewPrompt)))
	if r.FirstUserInput == "" {
		r.FirstUserInput = reviewPrompt
	}
	if r.SessionName == "" {
		r.autoNameSession()
	}

	// 触发 AI 流式响应
	r.guardBeginRequest() // P0 防护：审查请求视为新用户请求，状态归零
	r.setState(stateStreaming)
	r.streamAIRequest()
	return "" // AI 流将输出审查结果，无需返回文本
}

// handleStreamError handle streaming errors with friendly messages.
// 2026-07-31：context.Canceled 在 streaming/toolExec 状态下为用户主动取消，静默处理。
func (r *PlainRunner) handleStreamError(err error) {
	prevState := r.getState()
	r.setState(stateIdle)
	// 用户主动取消（Ctrl+C / stop），静默处理
	if errors.Is(err, context.Canceled) && (prevState == stateStreaming || prevState == stateToolExec) {
		fmt.Println("[已中断]")
		return
	}
	errMsg := err.Error()

	if ai.IsContextOverflowError(errMsg) {
		fmt.Println("[!] Context overflow, auto-compacting...")
		beforeRevision := sessionRevision(r.Session)
		compactResult := r.handleCompactWithTrigger("", ctxpkg.CompactionTriggerReactive, ctxpkg.CompactionPolicyForce)
		if compactCommittedSince(r.Session, beforeRevision) {
			r.setState(stateStreaming)
			r.streamAIRequest()
		} else {
			fmt.Printf("[!] Context overflow recovery stopped: %s. Use /rewind, /clear, or start a new session.\n", compactResult)
		}
		return
	}

	if ai.IsOverloadedError(errMsg) {
		if r.Fallback.Record529() {
			fmt.Printf("[!] Model overloaded, switched to fallback: %s\n", r.Fallback.ActiveModel())
			r.ModelName = r.Fallback.ActiveModel()
			r.requestSnapshot = contextRequestSnapshot{}
			r.AIClient.SetModel(r.ModelName)
			if r.Caching != nil {
				r.Caching.UpdateModel(r.ModelName)
			}
		}
		// 切换模型后重试当前请求
		r.setState(stateStreaming)
		r.streamAIRequest()
		return
	}

	fmt.Printf("[Error] %s\n", r.friendlyError(err))
}

// handleCommand process slash commands in plain mode
func (r *PlainRunner) handleCommand(input string) {
	name, args := command.Parse(input)
	cmd, ok := r.CmdRegistry.Get(name)
	if !ok {
		suggestions := command.Suggest(name, r.CmdRegistry)
		if len(suggestions) > 0 {
			fmt.Printf("Unknown: /%s\nDid you mean: /%s?\n", name, suggestions[0])
		} else {
			fmt.Printf("Unknown: /%s\nType /help for all commands\n", name)
		}
		return
	}

	output, err := cmd.Handler(args)
	if err != nil {
		fmt.Printf("Command error: %v\n", err)
		return
	}

	output = r.dispatchCommand(name, args, output)
	fmt.Println(output)
}

// initDispatchHandlers initializes the command dispatch handler map
func (r *PlainRunner) initDispatchHandlers() map[string]func(name, args, output string) string {
	exitHandler := func(name, args, output string) string {
		r.cleanupOnExit()
		fmt.Println("Session saved. Goodbye!")
		os.Exit(0)
		return ""
	}

	passthroughHandler := func(name, args, output string) string {
		return output
	}

	skillPromptHandler := func(name, args, output string) string {
		return r.handleSkillPrompt(name, args, output)
	}

	return map[string]func(name, args, output string) string{
		"exit": exitHandler,
		"quit": exitHandler,
		"help": func(name, args, output string) string {
			// P1-3：--all 透传注册表 FormatHelp（与 TUI 同源，含隐藏命令与 [隐藏] 后缀）
			for _, t := range strings.Fields(args) {
				if t == "--all" || t == "-all" {
					return command.FormatHelp(r.CmdRegistry, args)
				}
			}
			return formatHelp(r.CmdRegistry)
		},
		"model": func(name, args, output string) string { return r.handleModelCommand(args) },
		"models": func(name, args, output string) string {
			return ai.FormatModelList(r.ModelName, r.AvailableModels, r.ModelRegistry)
		},
		// G12b：本行原先是一段 inline 实现，与 TUI 侧各写各的，
		// 结果是 (a) 没有轴归属文案、(b) 不认 `status`、(c) 两 UI 必然漂移。
		// 现改为转发 runnerCore.handlePermissions ⇒ 与 TUI 侧同一份实现 + 同一份文案。
		"permissions": func(name, args, output string) string { return r.handlePermissions(args) },
		"status":      func(name, args, output string) string { return r.formatFullStatus() },
		"compact":     func(name, args, output string) string { return r.handleCompact(args) },
		"archive":     func(name, args, output string) string { return r.handleArchive(args) },
		"clear": func(name, args, output string) string {
			r.replaceSession(session.NewSession())
			return "Chat cleared"
		},
		"session list": passthroughHandler, // P0-4 修复：OnSessionList 回调已处理，避免双重执行
		"session save": passthroughHandler, // P0-4 修复：OnSessionSave 回调已处理
		"resume":       passthroughHandler, // P0-4 修复：OnResume 回调已处理
		"sessions":     passthroughHandler, // P0-4 修复：OnSessionList 回调已处理
		"export":       func(name, args, output string) string { return r.handleExport(args) },
		"export-user":  func(name, args, output string) string { return r.handleExportUser(args) },
		"think":        func(name, args, output string) string { return r.handleThink(args) },
		"config": func(name, args, output string) string {
			if args == "env" {
				return r.formatEnvConfig()
			}
			return r.formatCurrentConfig()
		},
		"init":            func(name, args, output string) string { return r.handleInit() },
		"name":            passthroughHandler, // P0-4 修复：OnRename 回调已处理，避免双重执行
		"rewind":          func(name, args, output string) string { return r.handleRewind(args) },
		"copy":            func(name, args, output string) string { return r.handleCopy(args) },
		"session switch":  passthroughHandler, // P0-4 修复：OnSessionSwitch 回调已处理
		"session merge":   passthroughHandler, // P0-4 修复：OnSessionMerge 回调已处理
		"branch list":     func(name, args, output string) string { return r.handleBranchList() },
		"commit":          func(name, args, output string) string { return r.handleCommit() },
		"worktree list":   func(name, args, output string) string { return r.handleWorktreeList() },
		"worktree add":    func(name, args, output string) string { return r.handleWorktreeAdd(args) },
		"worktree remove": func(name, args, output string) string { return r.handleWorktreeRemove(args) },
		"worktree prune":  func(name, args, output string) string { return r.handleWorktreePrune() },
		"diff":            func(name, args, output string) string { return r.handleDiff(args) },
		"plugin list":     func(name, args, output string) string { return r.handlePluginList() },
		"plugin install":  func(name, args, output string) string { return r.handlePluginInstall(args) },
		"plugin enable":   func(name, args, output string) string { return r.handlePluginEnable(args) },
		"plugin disable":  func(name, args, output string) string { return r.handlePluginDisable(args) },
		"reload-plugins":  func(name, args, output string) string { return r.handleReloadPlugins() },
		"sandbox":         func(name, args, output string) string { return r.handleSandbox(args) },
		// 2026-10-14：注册全名 "sandbox status" 后，Parse 把 `/sandbox status` 切成该名字
		// ⇒ 调度表必须有对应条目，否则用户会看到注册侧的 TUI-only 提示（而修复前
		// plain 侧 `/sandbox status` 走 default 分支本来就能查状态）。与 TUI 侧
		// （app.go case "sandbox status"）同源同一实现， 两侧都验。
		"sandbox status":  func(name, args, output string) string { return r.handleSandbox("status") },
		"security-preset": func(name, args, output string) string { return r.handleSecurityPreset(args) },
		"undo":            func(name, args, output string) string { return r.handleUndo() },
		"theme":           func(name, args, output string) string { return r.handleTheme(args) },
		"agents":          func(name, args, output string) string { return r.handleAgents() },
		"skills":          func(name, args, output string) string { return r.handleSkills() },
		"cron add":        func(name, args, output string) string { return r.handleCronAdd(args) },
		"cron list":       func(name, args, output string) string { return r.handleCronList() },
		"cron remove":     func(name, args, output string) string { return r.handleCronRemove(args) },
		"commit-push-pr": func(name, args, output string) string {
			if output == "COMMIT_PUSH_PR_REQUEST" {
				return r.handleCommitPushPR()
			}
			return output
		},
		"actions list":    passthroughHandler,
		"actions view":    passthroughHandler,
		"actions trigger": passthroughHandler,
		"actions rerun":   passthroughHandler,
		"actions logs":    passthroughHandler,
		"issue create":    passthroughHandler,
		"issue list":      passthroughHandler,
		"issue view":      passthroughHandler,
		"skillify":        skillPromptHandler,
		"simplify":        skillPromptHandler,
		"verify":          skillPromptHandler,
		"batch":           skillPromptHandler,
		"fcache":          func(name, args, output string) string { return r.handleFileCache(args) },
		// B7-P1-2 修复：拦截 /security-review，构建 AI 审查提示词并注入会话触发 AI 流。
		// 原实现走 Registry 桩返回 "Security review started — AI will scan..."（假成功，不触发 AI）。
		"security-review": func(name, args, output string) string { return r.handleSecurityReview(args) },
	}
}

// dispatchCommand handle command side effects for specific commands
func (r *PlainRunner) dispatchCommand(name, args, output string) string {
	if handler, ok := r.dispatchHandlers[name]; ok {
		return handler(name, args, output)
	}
	return output
}

// printStatus print current status line
func (r *PlainRunner) printStatus() {
	estimated := r.estimateCurrentInputContextTokens()
	ctxPercent := r.Budget.UsagePercent(estimated)
	parts := []string{
		fmt.Sprintf("🐋 Whale %s", r.ModelName),
		fmt.Sprintf("ctx %d/%d(%.0f%%)", estimated, r.Config.ContextWindow, ctxPercent),
	}
	if r.InputTokens > 0 || r.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("in:%d out:%d", r.InputTokens, r.OutputTokens))
	}
	parts = append(parts, fmt.Sprintf("turn %d", r.Session.Metadata.TurnCount))
	parts = append(parts, r.Policy.Mode().String())

	fmt.Printf("--- %s ---\n", strings.Join(parts, " | "))
}

// formatFullStatus print detailed status
func (r *PlainRunner) formatFullStatus() string {
	estimated := r.estimateCurrentInputContextTokens()
	var sb strings.Builder
	// 5维度审查修复：统一引用 r.CmdRegistry.Version，消除硬编码 v0.1.0
	if r.CmdRegistry != nil && r.CmdRegistry.Version != "" {
		sb.WriteString(fmt.Sprintf("Whale Code v%s (plain mode)\n", r.CmdRegistry.Version))
	} else {
		sb.WriteString("Whale Code (plain mode)\n")
	}
	sb.WriteString(fmt.Sprintf("Model: %s\n", r.ModelName))
	sb.WriteString(fmt.Sprintf("Permission: %s\n", r.Policy.Mode()))
	sb.WriteString(fmt.Sprintf("Context: %.0f%% (%d/%d tokens)\n",
		r.Budget.UsagePercent(estimated), estimated, r.Config.ContextWindow))
	sb.WriteString(fmt.Sprintf("Tokens: in %d / out %d\n", r.InputTokens, r.OutputTokens))
	sb.WriteString(fmt.Sprintf("Turns: %d\n", r.Session.Metadata.TurnCount))
	sb.WriteString(fmt.Sprintf("Messages: %d\n", len(r.Session.GetMessages())))
	sb.WriteString(fmt.Sprintf("Workdir: %s\n", r.WorkDir))
	if r.Fallback.IsDegraded() {
		sb.WriteString(fmt.Sprintf("! Degraded: %s -> %s\n",
			r.Config.Model, r.Fallback.ActiveModel()))
	}
	return sb.String()
}

// handleCost 已移除（plain 模式）：provider 不返回费用详情，本地费用估算不可靠。
// token 用量统计保留在 /usage（UsageTracker.FormatUsageReport）。

// handleCopy copy content to clipboard
func (r *PlainRunner) handleCopy(args string) string {
	var content string
	switch args {
	case "all":
		content = r.formatAllForCopy()
	case "last", "":
		content = r.formatLastForCopy()
	default:
		var n int
		if _, err := fmt.Sscanf(args, "%d", &n); err != nil || n <= 0 {
			return "Usage: /copy [last|all|N]  (N = message number)"
		}
		content = r.formatMessageNForCopy(n)
	}
	if content == "" {
		return "Nothing to copy"
	}
	if err := copyToClipboard(content); err != nil {
		return fmt.Sprintf("Copy failed: %v\nFallback: use /export to save to file", err)
	}
	preview := content
	if len(preview) > 60 {
		preview = preview[:60] + "..."
	}
	return fmt.Sprintf("Copied: %s", preview)
}

// formatAllForCopy format entire conversation for clipboard
func (r *PlainRunner) formatAllForCopy() string {
	var sb strings.Builder
	for i, msg := range r.Session.GetMessages() {
		switch msg.Role {
		case session.RoleUser:
			sb.WriteString(fmt.Sprintf("[User] %s\n", msg.TextContent()))
		case session.RoleAssistant:
			sb.WriteString(fmt.Sprintf("[Whale] %s\n", msg.TextContent()))
			for _, b := range msg.Blocks {
				if b.Type == "tool_use" {
					sb.WriteString(fmt.Sprintf("[Tool: %s] %s\n", b.Name, b.Input))
				}
			}
		case session.RoleTool:
			for _, b := range msg.Blocks {
				sb.WriteString(fmt.Sprintf("[Tool Result: %s]\n%s\n", b.ToolName, b.Output))
			}
		}
		if i < len(r.Session.GetMessages())-1 {
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// formatLastForCopy format last AI response for clipboard
func (r *PlainRunner) formatLastForCopy() string {
	for i := len(r.Session.GetMessages()) - 1; i >= 0; i-- {
		msg := r.Session.GetMessages()[i]
		if msg.Role == session.RoleAssistant {
			return msg.TextContent()
		}
	}
	return ""
}

// formatMessageNForCopy format message #N for clipboard (1-based)
func (r *PlainRunner) formatMessageNForCopy(n int) string {
	idx := n - 1
	if idx < 0 || idx >= len(r.Session.GetMessages()) {
		return ""
	}
	msg := r.Session.GetMessages()[idx]
	return fmt.Sprintf("[%s] %s", msg.Role, msg.TextContent())
}

// handleCompact handle compact command

// handleSessionList handle session list (deprecated, use handleSessions)
func (r *PlainRunner) handleSessionList() string {
	return r.handleSessions()
}

// handleResume resume previous session
func (r *PlainRunner) handleResume(args string) string {
	if r.Store == nil {
		return "session store not initialized"
	}

	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		// list recent sessions for selection
		summaries, err := r.Store.List()
		if err != nil {
			return fmt.Sprintf("failed to get session list: %v", err)
		}
		if len(summaries) == 0 {
			return "no sessions available to resume"
		}

		var sb strings.Builder
		sb.WriteString("Resumable sessions (use /resume <ID> to resume):\n")
		maxShow := 10
		if len(summaries) < maxShow {
			maxShow = len(summaries)
		}
		for i, s := range summaries[:maxShow] {
			timeStr := time.UnixMilli(s.UpdatedAt).Format("2006-01-02 15:04")
			sb.WriteString(fmt.Sprintf("  %d. %s (turns:%d %s)\n", i+1, s.ID, s.TurnCount, timeStr))
		}
		return sb.String()
	}

	// load specified session
	loaded, err := r.Store.Load(sessionID)
	if err != nil {
		return fmt.Sprintf("failed to load session: %v", err)
	}

	// save current session first
	_ = r.saveSession()

	r.replaceSession(loaded)
	r.SessionName = sessionID
	r.SavedMsgCount = len(loaded.GetMessages())
	r.FirstUserInput = ""

	return fmt.Sprintf("Resumed session: %s (%d turns, %d messages)", sessionID, loaded.Metadata.TurnCount, len(loaded.GetMessages()))
}

// handleSessions list all sessions
func (r *PlainRunner) handleSessions() string {
	if r.Store == nil {
		return "session store not initialized"
	}

	summaries, err := r.Store.List()
	if err != nil {
		return fmt.Sprintf("failed to get session list: %v", err)
	}

	if len(summaries) == 0 {
		return "no saved sessions"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Total %d sessions:\n", len(summaries)))
	for i, s := range summaries {
		timeStr := time.UnixMilli(s.UpdatedAt).Format("2006-01-02 15:04")
		sb.WriteString(fmt.Sprintf("  %d. %s (turns:%d %s)\n", i+1, s.ID, s.TurnCount, timeStr))
	}
	sb.WriteString("\nuse /resume <ID> to resume a specific session")
	return sb.String()
}

// handleSessionSave handle session save
func (r *PlainRunner) handleSessionSave() string {
	if len(r.Session.GetMessages()) == 0 {
		return "Empty session"
	}
	_ = r.saveSession()
	return fmt.Sprintf("Saved: %s", r.SessionName)
}

// handleExport handle export
func (r *PlainRunner) handleExport(filename string) string {
	return r.runnerCore.handleExport(filename)
}

// handleExportUser handle user message export
func (r *PlainRunner) handleExportUser(args string) string {
	timeline := false
	filename := "whale-user-messages.md"

	if args != "" {
		parts := strings.Fields(args)
		for _, p := range parts {
			if p == "--timeline" {
				timeline = true
			} else if !strings.HasPrefix(p, "--") {
				filename = p
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("# User Messages Export\n\n")
	for _, msg := range r.Session.GetMessages() {
		if msg.Role == session.RoleUser {
			if timeline {
				t := time.UnixMilli(msg.Timestamp).Format("2006-01-02 15:04:05")
				sb.WriteString(fmt.Sprintf("- [%s] %s\n\n", t, msg.TextContent()))
			} else {
				sb.WriteString(fmt.Sprintf("- %s\n\n", msg.TextContent()))
			}
		}
	}
	if !filepath.IsAbs(filename) {
		filename = filepath.Join(r.WorkDir, filename)
	}
	if err := util.AtomicWriteFile(filename, []byte(sb.String()), 0644); err != nil {
		return fmt.Sprintf("Export failed: %v", err)
	}
	return fmt.Sprintf("Exported: %s", filename)
}

// handleThink 处理 thinking 三态开关。
//
// ★ 与 TUI 侧 `Model.handleThink` 是**同一命令的两处实现**（本仓 同族坑：
// 改一侧不改另一侧 = 假修复），两侧语义必须一致：
//
//	on → 显式开启 · off → 显式关闭 · 其余 → 未指定（交回服务端默认）
func (r *PlainRunner) handleThink(args string) string {
	switch args {
	case "on":
		r.ThinkingEnabled = ai.ThinkingPtr(true)
		return "Thinking mode ON (token usage may increase 4-26x)"
	case "off":
		r.ThinkingEnabled = ai.ThinkingPtr(false)
		return "Thinking mode OFF"
	default:
		r.ThinkingEnabled = nil
		return fmt.Sprintf("Thinking mode reset → %s", thinkingModeLabel(nil))
	}
}

// formatCurrentConfig format current config
func (r *PlainRunner) formatCurrentConfig() string {
	var sb strings.Builder
	sb.WriteString("Config:\n")
	sb.WriteString(fmt.Sprintf("  model: %s\n", r.Config.Model))
	sb.WriteString(fmt.Sprintf("  fallback: %s\n", r.Config.FallbackModel))
	sb.WriteString(fmt.Sprintf("  permission: %s\n", r.Config.PermissionMode))
	sb.WriteString(fmt.Sprintf("  contextWindow: %d\n", r.Config.ContextWindow))
	sb.WriteString(fmt.Sprintf("  compaction: %s\n", r.Config.CompactionStrategy))
	sb.WriteString(fmt.Sprintf("  autoMemory: %v\n", r.Config.AutoMemoryEnabled))
	return sb.String()
}

// formatEnvConfig format env vars
func (r *PlainRunner) formatEnvConfig() string {
	envVars := []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL",
		"WHALE_MODEL", "WHALE_FALLBACK_MODEL", "WHALE_PERMISSION_MODE",
		"WHALE_CONTEXT_WINDOW", "WHALE_SESSION_BUDGET",
	}
	var sb strings.Builder
	sb.WriteString("Environment:\n")
	for _, v := range envVars {
		val := os.Getenv(v)
		if val != "" {
			if strings.Contains(strings.ToUpper(v), "KEY") && len(val) > 8 {
				val = val[:4] + "..." + val[len(val)-4:]
			}
			sb.WriteString(fmt.Sprintf("  %s=%s\n", v, val))
		} else {
			sb.WriteString(fmt.Sprintf("  %s=(not set)\n", v))
		}
	}
	return sb.String()
}

// handleDoctor handle diagnostics
func (r *PlainRunner) handleDoctor() string {
	var parts []string
	parts = append(parts, "Diagnostics:")

	hasAnthropic := os.Getenv("ANTHROPIC_API_KEY") != ""
	hasOpenAI := os.Getenv("OPENAI_API_KEY") != ""
	if hasAnthropic {
		parts = append(parts, "  [OK] ANTHROPIC_API_KEY set")
	} else {
		parts = append(parts, "  [!!] ANTHROPIC_API_KEY not set")
	}
	if hasOpenAI {
		parts = append(parts, "  [OK] OPENAI_API_KEY set")
	} else {
		parts = append(parts, "  [!!] OPENAI_API_KEY not set")
	}
	if !hasAnthropic && !hasOpenAI {
		parts = append(parts, "  [XX] No API Key set")
	}

	model := r.ModelName
	envModel := os.Getenv("WHALE_MODEL")
	if model != "" {
		modelInfo := fmt.Sprintf("  [OK] Model: %s", model)
		if envModel != "" && envModel != model {
			modelInfo += fmt.Sprintf(" (WHALE_MODEL=%s)", envModel)
		}
		parts = append(parts, modelInfo)
	} else if envModel != "" {
		parts = append(parts, fmt.Sprintf("  [OK] Model: %s (from WHALE_MODEL)", envModel))
	} else {
		parts = append(parts, "  [XX] No model set (set WHALE_MODEL or use /model)")
	}

	baseURL := os.Getenv("OPENAI_BASE_URL")
	if baseURL != "" {
		parts = append(parts, fmt.Sprintf("  [OK] OPENAI_BASE_URL: %s", baseURL))
	}

	docs := r.MagicDocs.List() //nolint:staticcheck // H4 保留 V1 作为 fallback
	if len(docs) > 0 {
		parts = append(parts, fmt.Sprintf("  [OK] Magic Docs: %d files", len(docs)))
	} else {
		parts = append(parts, "  [..] No WHALE.md detected")
	}

	// N12: V2 记忆文件计数（store 为 nil 时为 0）。
	memCount := 0
	if r.MemoryStore != nil {
		if entries, err := r.MemoryStore.List(); err == nil {
			memCount = len(entries)
		}
	}
	parts = append(parts, fmt.Sprintf("  Memory entries: %d", memCount))

	if r.Fallback.IsDegraded() {
		parts = append(parts, fmt.Sprintf("  [!] Degraded -> %s", r.Fallback.ActiveModel()))
	} else {
		parts = append(parts, "  [OK] Model OK")
	}

	return strings.Join(parts, "\n")
}

// handleInit init WHALE.md
func (r *PlainRunner) handleInit() string {
	whaleMd := `# Whale Code Project Info

This file is auto-read by Whale Code for project context.

## Project Structure
Describe your project structure here.

## Coding Standards
Describe your coding conventions here.

## Notes
Add any important notes here.
`
	path := filepath.Join(r.WorkDir, "WHALE.md")
	if _, err := os.Stat(path); err == nil {
		return "WHALE.md already exists"
	}
	if err := util.AtomicWriteFile(path, []byte(whaleMd), 0644); err != nil {
		return fmt.Sprintf("Create failed: %v", err)
	}
	r.MagicDocs.Refresh() //nolint:staticcheck // H4 保留 V1 作为 fallback
	return "Created WHALE.md (edit it to describe your project)"
}

// handleRewind delegates to shared runnerCore implementation（阶段 4：补接 plain 路径）
func (r *PlainRunner) handleRewind(args string) string {
	return r.runnerCore.handleRewind(args)
}

// handleModelCommand handle /model command and its subcommands (add/remove/edit/info)
func (r *PlainRunner) handleModelCommand(args string) string {
	if args == "" {
		cfg := r.ModelRegistry.Get(r.ModelName)
		if cfg != nil {
			return ai.FormatModelInfo(cfg)
		}
		return fmt.Sprintf("Model: %s", r.ModelName)
	}

	if strings.HasPrefix(args, "add ") {
		return r.handleModelAdd(strings.TrimPrefix(args, "add "))
	}
	if args == "add" {
		return "Usage: /model add <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]\n" +
			"           [--context-window <tokens>] [--max-output <tokens>]\n" +
			"           [--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]\n\n" +
			"--supports-thinking overrides the capability table and prefix inference for this\n" +
			"model (declaration wins). omit to keep the current value.\n" +
			"--thinking-evidence records where that declaration comes from (vendor doc URL or\n" +
			"capture artifact path); shown by /model info, never verified.\n\n" +
			"unspecified fields will inherit defaults from current model config"
	}

	if strings.HasPrefix(args, "edit ") {
		return r.handleModelEdit(strings.TrimPrefix(args, "edit "))
	}
	if args == "edit" {
		return "Usage: /model edit <name> [--base-url <url>] [--api-key <key>] [--provider <openai|anthropic>]\n" +
			"           [--context-window <tokens>] [--max-output <tokens>]\n" +
			"           [--supports-thinking <supported|unsupported>] [--thinking-evidence <url>]\n\n" +
			"--supports-thinking overrides the capability table and prefix inference for this\n" +
			"model (declaration wins). omit to keep the current value.\n" +
			"--thinking-evidence records where that declaration comes from; shown by\n" +
			"/model info, never verified. omit to keep the current value.\n\n" +
			"only modify specified fields, unspecified fields keep original values"
	}

	if strings.HasPrefix(args, "remove ") {
		return r.handleModelRemove(strings.TrimPrefix(args, "remove "))
	}
	if args == "remove" {
		return "Usage: /model remove <name>"
	}

	if strings.HasPrefix(args, "info ") {
		name := strings.TrimSpace(strings.TrimPrefix(args, "info "))
		cfg := r.ModelRegistry.Get(name)
		if cfg == nil {
			return ai.ModelNotConfiguredHint(name, r.ModelRegistry)
		}
		return ai.FormatModelInfo(cfg)
	}
	if args == "info" {
		return "Usage: /model info <name>"
	}

	return r.handleModelSwitch(args)
}

// handleModelSwitch switch model (full linkage)
func (r *PlainRunner) handleModelSwitch(name string) string {
	cfg := r.ModelRegistry.Get(name)
	if cfg == nil {
		return ai.ModelNotConfiguredHint(name, r.ModelRegistry)
	}
	r.ModelName = cfg.Name
	r.requestSnapshot = contextRequestSnapshot{}
	r.AIClient.SetModelConfig(cfg)
	if r.Caching != nil {
		r.Caching.UpdateModel(cfg.Name)
	}
	r.Fallback.SetOriginalModel(cfg.Name)
	r.Config.ContextWindow = cfg.ContextWindow
	// H8：Budget 从 Config 构造（原 DefaultBudget 硬编码阈值）
	r.Budget = ctxpkg.NewBudgetFromConfig(config.ResolveContextWindow(cfg.ContextWindow, cfg.Name), r.Config)
	return fmt.Sprintf("Model: %s\nProvider: %s\nBase URL: %s\nContext: %d tokens",
		cfg.Name, cfg.Provider, cfg.BaseURL, cfg.ContextWindow)
}

// handleModelAdd handle /model add subcommand
// unspecified fields inherit from current model config, then filled by FillDefaults
func (r *PlainRunner) handleModelAdd(args string) string {
	cfg, err := ai.ParseModelAddArgs(args)
	if err != nil {
		return fmt.Sprintf("Error: %v\nUsage: /model add <name> [--base-url <url>] [--api-key <key>]", err)
	}

	// inherit unspecified fields from current model config
	var addWarning string
	currentCfg := r.ModelRegistry.Get(r.ModelName)
	if currentCfg != nil {
		if cfg.APIKey == "" {
			cfg.APIKey = currentCfg.APIKey
		}
		if cfg.BaseURL == "" {
			cfg.BaseURL = currentCfg.BaseURL
		}
		if cfg.Provider == "" {
			cfg.Provider = currentCfg.Provider
		}
		if cfg.ContextWindow == 0 {
			cfg.ContextWindow = currentCfg.ContextWindow
		}
		if cfg.MaxOutputTokens == 0 {
			cfg.MaxOutputTokens = currentCfg.MaxOutputTokens
		}
	}

	if cfg.APIKey == "" {
		cfg.APIKey = os.Getenv("OPENAI_API_KEY")
	}
	if cfg.BaseURL == "" && cfg.Provider != ai.ProviderAnthropic {
		cfg.BaseURL = os.Getenv("OPENAI_BASE_URL")
	}
	cfg.FillDefaults()

	if cfg.BaseURL != "" && currentCfg != nil && cfg.BaseURL != currentCfg.BaseURL && cfg.APIKey == currentCfg.APIKey {
		addWarning = "\n⚠️ Warning: API key inherited from current model but base URL differs. Ensure the key is valid for this provider."
	}

	isUpdate := r.ModelRegistry.Get(cfg.Name) != nil

	// B1：声明"未指定则保留"—— 理由同 TUI 侧（重加同名模型不得抹掉已有声明）。
	if cfg.ThinkingSupport == "" {
		if existing := r.ModelRegistry.Get(cfg.Name); existing != nil {
			cfg.ThinkingSupport = existing.ThinkingSupport
		}
	}
	// B3：来源与声明同理 —— 重加同名模型时未被显式指定就保留已记的来源。
	if cfg.ThinkingEvidence == "" {
		if existing := r.ModelRegistry.Get(cfg.Name); existing != nil {
			cfg.ThinkingEvidence = existing.ThinkingEvidence
		}
	}

	r.ModelRegistry.Add(cfg)
	// B1：与 TUI 侧共用 syncThinkingDeclaration —— 本仓栽过"命令双实现只接一侧"。
	syncThinkingDeclaration(cfg)
	r.AvailableModels = r.ModelRegistry.Names()

	if r.Config.ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(r.Config.ModelsJSONPath, r.ModelRegistry); saveErr != nil {
			return fmt.Sprintf("model added: %s (but save failed: %v)", cfg.Name, saveErr)
		}
	}

	if isUpdate {
		return fmt.Sprintf("model updated: %s", cfg.Name)
	}
	return fmt.Sprintf("model added: %s\nProvider: %s\nBase URL: %s\nContext: %d tokens\nUse /model %s to switch to this model%s",
		cfg.Name, cfg.Provider, cfg.BaseURL, cfg.ContextWindow, cfg.Name, addWarning)
}

// handleModelRemove handle /model remove subcommand
func (r *PlainRunner) handleModelRemove(name string) string {
	name = strings.TrimSpace(name)
	if name == r.ModelName {
		return "cannot remove the currently active model, please switch to another model first"
	}
	if !r.ModelRegistry.Remove(name) {
		return fmt.Sprintf("model '%s' not found", name)
	}
	// B1：删模型必须同时撤销声明（声明层是进程级的）。
	clearThinkingDeclaration(name)
	r.AvailableModels = r.ModelRegistry.Names()

	if r.Config.ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(r.Config.ModelsJSONPath, r.ModelRegistry); saveErr != nil {
			return fmt.Sprintf("model removed: %s (but save failed: %v)", name, saveErr)
		}
	}
	return fmt.Sprintf("model removed: %s", name)
}

// handleModelEdit handle /model edit subcommand, modify specified fields of an existing model
func (r *PlainRunner) handleModelEdit(args string) string {
	name, updates, err := ai.ParseModelEditArgs(args)
	if err != nil {
		return fmt.Sprintf("Error: %v\nUsage: /model edit <name> [--base-url <url>] [--provider <openai|anthropic>]", err)
	}

	cfg := r.ModelRegistry.Get(name)
	if cfg == nil {
		return ai.ModelNotConfiguredHint(name, r.ModelRegistry)
	}

	if err := ai.ApplyModelUpdates(cfg, updates); err != nil {
		return fmt.Sprintf("modification failed: %v", err)
	}
	// B1：与 TUI 侧共用（同上）。
	syncThinkingDeclaration(cfg)

	// if modifying the current model, sync runtime state
	if name == r.ModelName {
		r.AIClient.SetModelConfig(cfg)
		r.Config.ContextWindow = cfg.ContextWindow
		// H8：Budget 从 Config 构造（原 DefaultBudget 硬编码阈值）
		r.Budget = ctxpkg.NewBudgetFromConfig(config.ResolveContextWindow(cfg.ContextWindow, cfg.Name), r.Config)
	}

	// persist
	if r.Config.ModelsJSONPath != "" {
		if saveErr := ai.SaveModels(r.Config.ModelsJSONPath, r.ModelRegistry); saveErr != nil {
			return fmt.Sprintf("model modified: %s (but save failed: %v)", name, saveErr)
		}
	}

	return fmt.Sprintf("model modified: %s\n%s", name, ai.FormatModelInfo(cfg))
}

// checkAutoCompact check if auto-compact needed
func (r *PlainRunner) checkAutoCompact() {
	estimated := r.estimateCurrentInputContextTokens()
	// H13-G1: BodyAfterPrefix scope — 获取 cachedInputTokens
	cached := ctxpkg.GetCachedInputTokens(r.TokenSource, r.ModelName, estimated, func() {
		if r.Caching != nil {
			r.Caching.RecordCacheAnomaly()
		}
	})
	state := r.Budget.CheckState(estimated, cached)
	// 97% 兜底：只做硬停 + 最小交接模板，不再尝试压缩（窗口已满时压缩请求本身可能失败）
	if state == ctxpkg.StateBlocked {
		fmt.Printf("%s\n", blockedHandoffMessage(r.Budget.UsagePercent(estimated), r.FirstUserInput, r.Session.GetMessages()))
		return
	}
	if autoCompactDue(r.Config, r.Budget, estimated, cached) {
		fmt.Printf("[!] Context at %.0f%%, auto-compacting with AI summary...\n", r.Budget.UsagePercent(estimated))
		// 对抗式审查 3.11 P1 修复：auto-compact 结果不得丢弃——归档失败会
		// 中止 Rewrite 并经此返回错误文案，静默丢弃 = 压缩未发生零感知。
		if out := r.handleCompactWithTrigger("--ai", ctxpkg.CompactionTriggerAuto, ctxpkg.CompactionPolicyBudgeted); out != "" {
			fmt.Printf("[!] auto-compact: %s\n", out)
		}
		// 问题②：固定开销（静态 system prompt + 工具定义）≥ 窗口时压缩无法缓解，提示增大窗口
		if snap := r.requestSnapshot; snap.Valid && r.Budget.ContextWindow > 0 {
			if fixed := fixedRequestOverhead(snap); fixed >= r.Budget.ContextWindow {
				fmt.Printf("[!] %s\n", formatFixedOverheadWarning(fixed, r.Budget.ContextWindow))
			}
		}
	} else if state == ctxpkg.StateShouldCompact {
		// 0.80 提示线（用户决策窗口）：真实决策机会，主力自动压缩线之前
		if r.Config != nil && r.Config.AutoCompact {
			fmt.Printf("[!] Context at %.0f%%（决策窗口）：可 /compact 提前压缩，或继续（约 %.0f%% 将自动压缩）\n",
				r.Budget.UsagePercent(estimated), r.Budget.MainLinePercent())
		} else {
			fmt.Printf("[!] Context at %.0f%%; auto-compact is off, use /compact\n", r.Budget.UsagePercent(estimated))
		}
	}
}

// cleanupOnExit cleanup on exit - remove empty sessions
// 补全（修复点5）：追加 AIClient.Close（若有），保持 FileCache.Persist。
// streamCancel/toolCancel 在 PlainRunner 中未持久化存储（streamAIRequest 使用局部
// cancel + defer cancel），因此此处无可调用的 cancel；进程退出时流 goroutine 随之终止。
func (r *PlainRunner) cleanupOnExit() {
	if r.SessionName != "" && r.FirstUserInput == "" {
		r.Store.Delete(r.SessionName)
		r.SessionName = ""
	} else if r.SessionName != "" {
		_ = r.saveSession()
	}
	// 关闭 AI 客户端，释放底层 HTTP 连接（与 runnerCore.Close 中的 Close 幂等，重复调用安全）
	if r.AIClient != nil {
		_ = r.AIClient.Close()
	}
	// persist file cache metadata on exit
	if r.Executor != nil && r.Executor.FileCache != nil {
		_ = r.Executor.FileCache.Persist()
	}
}

// autoNameSession auto name session based on first user input

// saveSession incremental save

// estimateCurrentTokens estimate current token count

// friendlyError convert raw error to user-friendly message

// handleSessionSwitch switch to a saved session
//
// C20: 会话切换成功后通过 Store.NotifySessionSwitch 发布
// bus.SessionSwitchEvent，供 Context 模块清空缓存并从新会话重载。
func (r *PlainRunner) handleSessionSwitch(args string) string {
	if args == "" {
		return "Usage: /session switch <session-id>"
	}
	summaries, err := r.Store.List()
	if err != nil {
		return fmt.Sprintf("List failed: %v", err)
	}
	found := false
	for _, s := range summaries {
		if s.Name == args {
			found = true
			break
		}
	}
	if !found {
		return fmt.Sprintf("Session not found: %s\nUse /session list to see available sessions", args)
	}
	loaded, err := r.Store.Load(args)
	if err != nil {
		return fmt.Sprintf("Load failed: %v", err)
	}
	oldID := r.SessionName
	r.SessionName = args
	r.replaceSession(loaded)
	// 发布会话切换事件（C20，bus 未注入时静默跳过）
	r.Store.NotifySessionSwitch(oldID, args)
	return fmt.Sprintf("Loaded session: %s (%d messages)", args, len(loaded.GetMessages()))
}

// handleSessionMerge merge two sessions
func (r *PlainRunner) handleSessionMerge(args string) string {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		return "Usage: /session merge <id1> <id2>"
	}
	id1, id2 := parts[0], parts[1]
	sess1, err1 := r.Store.Load(id1)
	sess2, err2 := r.Store.Load(id2)
	if err1 != nil || err2 != nil {
		return fmt.Sprintf("Load failed: id1=%v, id2=%v", err1, err2)
	}
	mergedID := session.GenerateID()
	merged := session.Merge([]*session.Session{sess1, sess2}, mergedID)
	if err := r.Store.Save(mergedID, merged); err != nil {
		return fmt.Sprintf("Save failed: %v", err)
	}
	return fmt.Sprintf("Merged:\n  %s (%d msgs)\n  %s (%d msgs)\n-> %s (%d msgs)",
		id1, len(sess1.GetMessages()), id2, len(sess2.GetMessages()), mergedID, len(merged.GetMessages()))
}

// handleBranchList show git branches
func (r *PlainRunner) handleBranchList() string {
	return r.runnerCore.handleBranchList()
}

// handleCommit auto commit changes
func (r *PlainRunner) handleCommit() string {
	return r.runnerCore.handleCommit()
}

// handleWorktreeList show worktrees
func (r *PlainRunner) handleWorktreeList() string {
	return r.runnerCore.handleWorktreeList()
}

// handleWorktreeAdd create worktree
func (r *PlainRunner) handleWorktreeAdd(args string) string {
	return r.runnerCore.handleWorktreeAdd(args)
}

// handleWorktreeRemove remove worktree
func (r *PlainRunner) handleWorktreeRemove(args string) string {
	return r.runnerCore.handleWorktreeRemove(args)
}

// handleWorktreePrune prune stale worktrees
func (r *PlainRunner) handleWorktreePrune() string {
	return r.runnerCore.handleWorktreePrune()
}

// handleDiff show file changes with colored diff
func (r *PlainRunner) handleDiff(args string) string {
	return r.runnerCore.handleDiff(args)
}

// handlePluginList list installed plugins
func (r *PlainRunner) handlePluginList() string {
	return r.runnerCore.handlePluginList()
}

// handlePluginInstall install plugin from URL or local path
func (r *PlainRunner) handlePluginInstall(source string) string {
	return r.runnerCore.handlePluginInstall(source)
}

// handlePluginEnable enable plugin and register its tools to Registry
func (r *PlainRunner) handlePluginEnable(id string) string {
	return r.runnerCore.handlePluginEnable(id)
}

// handlePluginDisable disable plugin and remove its tools from Registry
func (r *PlainRunner) handlePluginDisable(id string) string {
	return r.runnerCore.handlePluginDisable(id)
}

// handleReloadPlugins reload all plugins
func (r *PlainRunner) handleReloadPlugins() string {
	return r.runnerCore.handleReloadPlugins()
}

// handleFileCache handles /fcache command for file cache management
func (r *PlainRunner) handleFileCache(args string) string {
	if r.Executor == nil || r.Executor.FileCache == nil {
		return "File cache not initialized"
	}

	cache := r.Executor.FileCache
	metrics := cache.Metrics()

	switch args {
	case "", "status":
		return metrics.FormatReport()

	case "detail":
		var sb strings.Builder
		sb.WriteString(metrics.FormatReport())
		sb.WriteString("\n\nCached files:\n")
		top := cache.TopSavedFiles(50)
		if len(top) == 0 {
			sb.WriteString("  (no cached files)\n")
		}
		for _, entry := range top {
			sb.WriteString(fmt.Sprintf("  %s\n    reads=%d saved=%d hit_rate=%.1f%%\n",
				entry.Path, entry.ReadCount, entry.TokensSaved, entry.HitRate*100))
		}
		return sb.String()

	case "clear":
		cache.Clear()
		return "File cache cleared"

	case "top":
		var sb strings.Builder
		sb.WriteString("Top 10 files by tokens saved:\n")
		top := cache.TopSavedFiles(10)
		if len(top) == 0 {
			sb.WriteString("  (no cached files)\n")
		}
		for i, entry := range top {
			sb.WriteString(fmt.Sprintf("  %d. %s\n     reads=%d saved=%d hit_rate=%.1f%%\n",
				i+1, entry.Path, entry.ReadCount, entry.TokensSaved, entry.HitRate*100))
		}
		return sb.String()

	default:
		return "Usage: /fcache [detail|clear|top]"
	}
}

// promptConfirm prompts user in terminal to confirm an operation (Y/N)
func (r *PlainRunner) promptConfirm() bool {
	return r.runnerCore.promptConfirm()
}

// handleSandbox toggles sandbox mode, controlling command validation and permission level
func (r *PlainRunner) handleSandbox(args string) string {
	return r.runnerCore.handleSandbox(args)
}

// handleUndo delegates to shared runnerCore implementation
func (r *PlainRunner) handleUndo() string {
	return r.runnerCore.handleUndo()
}

// handleRedo delegates to shared runnerCore implementation
func (r *PlainRunner) handleRedo() string {
	return r.runnerCore.handleRedo()
}

// handleTheme change theme
// （M10-05）：PlainRunner 路径同步支持即时主题切换。
// PlainRunner 无 TUI Model 实例，仅返回提示字符串（实际切换由 TUI 路径处理）。
func (r *PlainRunner) handleTheme(args string) string {
	switch args {
	case "light", "dark", "mono":
		return fmt.Sprintf("Theme: %s (applied for next TUI session)", args)
	default:
		return "Usage: /theme light|dark|mono"
	}
}

// handleAgents show available AI agents
func (r *PlainRunner) handleAgents() string {
	return "Available Agents:\n" +
		"  coder     - General coding assistant (current)\n" +
		"  reviewer  - Code review specialist\n" +
		"  debugger  - Debugging expert\n" +
		"  refactor  - Refactoring assistant\n" +
		"\nSwitch with: /agents <name>"
}

// handleSkills show available skills
func (r *PlainRunner) handleSkills() string {
	return "Available Skills:\n" +
		"  go         - Go development\n" +
		"  python     - Python development\n" +
		"  web        - Web development\n" +
		"  devops     - DevOps & deployment\n" +
		"  database   - Database operations\n" +
		"  testing    - Testing & TDD\n" +
		"\nSkills are automatically activated based on context"
}

// handleCronAdd add scheduled task
func (r *PlainRunner) handleCronAdd(args string) string {
	return r.runnerCore.handleCronAdd(args)
}

// handleCronList list scheduled tasks
func (r *PlainRunner) handleCronList() string {
	return r.runnerCore.handleCronList()
}

// handleCronRemove remove scheduled task
func (r *PlainRunner) handleCronRemove(args string) string {
	return r.runnerCore.handleCronRemove(args)
}

// copyToClipboard is defined in app.go, used here for consistency

// printWelcome show welcome message for plain mode
func printWelcome(model, workDir, permMode string) {
	fmt.Println("========================================")
	fmt.Println("  Whale Code v0.1.0  (plain mode)")
	fmt.Println("  Full copy/paste support enabled")
	fmt.Println("========================================")
	fmt.Println()
	fmt.Println("Your personal AI coding assistant.")
	fmt.Println()
	fmt.Println("Input: Type a message, press Enter to send")
	fmt.Println("      /help View all commands")
	fmt.Println()
	fmt.Println("Common commands:")
	fmt.Println("  /help       - Show all commands")
	fmt.Println("  /status     - Show full status")
	fmt.Println("  /model <name>- Switch AI model")
	fmt.Println("  /models     - List all available models")
	fmt.Println("  /compact    - Compact context")
	fmt.Println("  /copy       - Copy last AI reply")
	fmt.Println("  /copy all   - Copy full conversation")
	fmt.Println("  /clear      - Clear conversation")
	fmt.Println("  /undo       - Undo file modifications")
	fmt.Println("  /redo       - Redo file modifications")
	fmt.Println("  /diff       - Show Git changes")
	fmt.Println("  /exit       - Exit")
	fmt.Println()
	fmt.Println("Tips:")
	fmt.Println("  - Select text with your terminal; use its native copy shortcut (macOS Command+C, Linux/Windows commonly Ctrl+Shift+C)")
	fmt.Println("  - All output can be selected and copied")
	fmt.Println("  - Use /init to create WHALE.md template")
	fmt.Println("  - Use @filename to reference file content")
	fmt.Println()
	fmt.Printf("Model: %s | Work Dir: %s | Permission: %s\n\n", model, workDir, permMode)
}

// formatHelp plain 模式的 /help 正文。
//
// ★ C2：命令集合与描述**全部由注册表驱动**，不再手抄。
// 此前这里是"第 4 份手抄命令表"，实测缺 13 条（add-dir / checkpoint×6 / history /
// monitors stop,tail / session / session pick / tool）—— 主面（注册表）登记了、
// plain 副面看不到，且与 /help --all 分叉成两份实现（长期铁律「命令双实现是复发坑」）。
//
// ★ 描述文字（含 `/permissions` `/sandbox` `/security-preset` 的轴归属声明）取自
// `command.Command.DescriptionEN`，其 SSOT = `command.englishCommandDescriptions`（同包守卫
// `TestSuggestionMetadataDeclaresAxisOwnership`）。此处**不再复制**轴归属文案 —— 复制即第二份，
// 与 「同一事实跨入口必须同源」冲突（首版曾加过一张 plainAxisAnnotation 表，已删）。
//
// 保留 plain 自己的**单行** "usage description" 版式：G13 判据四要求轴归属声明与 usage
// 出现在同一行（command.FormatHelp 把 usage 排在第二行，结构上满足不了该判据）。
func formatHelp(reg *command.Registry) string {
	var sb strings.Builder
	sb.WriteString("Whale Code Command List (plain text mode)\n\n")
	if reg == nil {
		// 保留原先对 nil 注册表的容错（旧实现在 plainHelpRowHidden 里做了同样的 None-safe）
		return sb.String()
	}

	categories := reg.ByCategory()
	order := []string{"Core", "Model", "Session", "Memory", "Permission", "Export", "Git", "GitHub", "Plugin", "Daemon", "Task", "Workspace", "Config", "Auth", "Review", "Notify", "Security", "Automation", "Other"}
	seen := make(map[string]bool, len(categories))
	for _, cat := range order {
		cmds, ok := categories[cat]
		if !ok {
			continue
		}
		seen[cat] = true
		writePlainHelpCategory(&sb, cat, cmds)
	}
	// 注册表里出现但不在 order 的类别（防新增类别被静默吞掉）
	var rest []string
	for cat := range categories {
		if !seen[cat] {
			rest = append(rest, cat)
		}
	}
	sort.Strings(rest)
	for _, cat := range rest {
		writePlainHelpCategory(&sb, cat, categories[cat])
	}

	sb.WriteString("Tips:\n")
	sb.WriteString("  - Select text with your terminal and use its native copy shortcut\n")
	sb.WriteString("  - Multi-line input: type multiple lines, then '.' alone to send\n")
	sb.WriteString("  - Use @filename to reference file content\n")
	return sb.String()
}

// writePlainHelpCategory 输出一个分类的命令行；隐藏/桩命令不显示，整类清空则不输出空标题。
func writePlainHelpCategory(sb *strings.Builder, cat string, cmds []command.Command) {
	type row struct{ usage, desc string }
	rows := make([]row, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd.Hidden || cmd.Stub {
			continue
		}
		usage := cmd.Usage
		if usage == "" {
			usage = "/" + cmd.Name
		}
		// 描述一律取注册表（英文优先，SSOT = englishCommandDescriptions；为空则回退中文）。
		// 不再有任何 plain 侧文案副本 —— 轴归属声明也走这里（见 formatHelp 注释）。
		desc := cmd.DescriptionEN
		if desc == "" {
			desc = cmd.Description
		}
		rows = append(rows, row{usage, desc})
	}
	if len(rows) == 0 {
		return
	}
	sb.WriteString(fmt.Sprintf("[%s]\n", cat))
	for _, r := range rows {
		sb.WriteString(fmt.Sprintf("  %-40s %s\n", r.usage, r.desc))
	}
	sb.WriteString("\n")
}

// plainConfirmPrompt 渲染 plain（--ci）模式的确认提示。
//
// 与 TUI 确认弹窗（renderLegacyConfirmUI）同一家族：修前是
// "Safety confirmation / 安全确认\n…\nAllow once or deny / 允许本次或拒绝"
// 行内并列；现按双语 SSOT（bi）改英文主行 + 缩进中文辅行。
// 提取为包级函数是为了让形态守卫能直接调它（闭包内的 Printf 不可判别）。
func plainConfirmPrompt(toolName, input string) string {
	return "\n" + bi("Safety confirmation", "安全确认") + "\n" +
		approvalTargetSummary(toolName, input) + "\n" +
		bi("Allow once or deny", "允许本次或拒绝") + "\n"
}

// RunCI run in non-interactive CI mode: execute a single prompt and exit
// Returns exit code: 0=success, 1=error, 2=tool failure
func (r *PlainRunner) RunCI(prompt string) (int, error) {
	r.Policy.SetAllowedTools(r.Config.AllowedTools)
	// H8：CI 权限从 Config.CIPermission 读取（env WHALE_CI_PERMISSION 已在 applyEnvironmentOverrides 收口）
	ciPerm := ""
	if r.Config != nil {
		ciPerm = r.Config.CIPermission
	}
	if ciPerm == "" {
		ciPerm = string(config.PermDangerFullAccess) // fallback
	}
	// 阶段 2：CI 是无人值守环境。「危险命令二次确认」护栏假定有人在环应答，
	// 在此无人应答 → OnConfirm 读 stdin 遇 EOF → 判「用户取消」→ 命令静默失败，
	// 这比放行更糟（CI 里 `rm -rf dist` 这类清理步骤会莫名失败）。
	// 故由本装配点显式关闭。窄豁免：只关这条护栏，不动三档能力边界。
	r.Policy.SetDangerousCommandGuardrail(false)
	// 走 SetModeByName（模式名解析的单一真相源）：ci_permission 若为
	// accept-edits 等已删档也能被正确翻译并注入对应默认规则。
	if !r.Policy.SetModeByName(ciPerm) {
		// 未知档位：回退 workspace-write（与历史 ParseLevel 行为一致）
		r.Policy.SetMode(permission.LevelWorkspaceWrite)
	}

	// H8：CI 超时从 Config.CITimeoutMinutes 读取（原硬编码 10min）
	ciTimeoutMinutes := 10
	if r.Config != nil && r.Config.CITimeoutMinutes >= 1 {
		ciTimeoutMinutes = r.Config.CITimeoutMinutes
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(ciTimeoutMinutes)*time.Minute)
	defer cancel()

	r.Session.SetMessages(append(r.Session.GetMessages(), session.UserText(prompt)))

	// set firstUserInput to support V2 memory selection
	// 取反侧同走唯一谓词（CI/--prompt 入口）。
	if r.FirstUserInput == "" && !command.IsCommandInput(prompt) {
		r.FirstUserInput = prompt
	}

	fmt.Printf("> %s\n\n", prompt)

	// Loop: send request -> handle response -> execute tools -> send results back
	// until AI no longer calls tools (task complete)
	// H8：CI 阈值从 Config 读取（原硬编码 maxIterations=50 / maxConsecutiveFailures=3）
	maxIterations := 50
	if r.Config != nil && r.Config.CIMaxIterations >= 1 {
		maxIterations = r.Config.CIMaxIterations
	}
	consecutiveToolFailures := 0
	maxConsecutiveFailures := 3
	if r.Config != nil && r.Config.CIMaxConsecutiveFailures >= 1 {
		maxConsecutiveFailures = r.Config.CIMaxConsecutiveFailures
	}
	for i := 0; i < maxIterations; i++ {
		r.LastInputTokens = 0
		r.LastOutputTokens = 0
		req := r.buildRequest()
		events, err := ai.StreamWithRetry(ctx, r.AIClient, req, r.RetryConfig, r.Fallback, r.RecoveryCoordinator)
		if err != nil {
			fmt.Fprintf(os.Stderr, "API error: %v\n", err)
			return 1, err
		}

		var responseText string
		var pendingTools []session.ContentBlock
		toolInputBuf := make(map[string]string)
		var currentToolID string
		var requestUsage ai.RequestUsageAccumulator
		var streamErr error
		requestEpoch := r.sessionEpoch.Load()
		// 空响应排障信号（2026-09-08 A2 事故观测缺口）
		stopReceived := false
		finishReason := ""

		fmt.Print("[Whale] ")

		for event := range events {
			switch e := event.(type) {
			case ai.TextDeltaEvent:
				fmt.Print(e.Content)
				responseText += e.Content
			case ai.ToolUseEvent:
				if e.ID != "" && e.Name != "" {
					currentToolID = e.ID
					toolInputBuf[e.ID] = ""
					pendingTools = append(pendingTools, session.ContentBlock{
						Type: "tool_use",
						ID:   e.ID,
						Name: e.Name,
					})
				}
				if e.Input != "" {
					toolID := e.ID
					if toolID == "" {
						toolID = currentToolID
					}
					if toolID != "" {
						if buf, ok := toolInputBuf[toolID]; ok {
							toolInputBuf[toolID] = buf + e.Input
						}
					}
				}
			case ai.ToolInputDeltaEvent:
				if currentToolID != "" {
					if buf, ok := toolInputBuf[currentToolID]; ok {
						toolInputBuf[currentToolID] = buf + e.PartialJSON
					}
				}
			case ai.UsageEvent:
				requestUsage.Add(e)
			case ai.MessageStopEvent:
				stopReceived = true
				finishReason = e.FinishReason
			case ai.ErrorEvent:
				streamErr = e.Err
			}
		}
		r.finalizeRequestUsage(ctx, &requestUsage, requestEpoch)
		if streamErr != nil {
			fmt.Fprintf(os.Stderr, "\n[Error] %v\n", streamErr)
			return 1, streamErr
		}

		fmt.Println()

		// No tools called - AI has finished its response
		if len(pendingTools) == 0 {
			// 空响应 canary（2026-09-08 A2 事故观测缺口）：此前完全静默。
			// 仅用户主动取消（Canceled）静默；超时（DeadlineExceeded）属于需观测的异常形态。
			if responseText == "" && (ctx.Err() == nil || ctx.Err() == context.DeadlineExceeded) {
				snap := requestUsage.Snapshot()
				fmt.Fprintf(os.Stderr, "[ci] empty response (canary): model=%s stopReceived=%v finishReason=%q inputTokens=%d outputTokens=%d\n",
					r.AIClient.Model(), stopReceived, finishReason, snap.InputTokens, snap.OutputTokens)
			}
			return 0, nil
		}

		// Fill tool inputs from buffer
		for i := range pendingTools {
			pendingTools[i].Input = toolInputBuf[pendingTools[i].ID]
		}

		// Save assistant message with tool calls to session
		blocks := make([]session.ContentBlock, 0)
		if responseText != "" {
			blocks = append(blocks, session.ContentBlock{Type: "text", Text: responseText})
		}
		blocks = append(blocks, pendingTools...)
		usage := &session.TokenUsage{
			InputTokens:  r.LastInputTokens,
			OutputTokens: r.LastOutputTokens,
		}
		assistantMsg := session.AssistantWithTools(blocks, r.ModelName, usage)
		r.Session.SetMessages(append(r.Session.GetMessages(), assistantMsg))

		// Execute tools and collect results
		// when Pipeline is initialized, use ExecuteWithConfirm to execute tools one by one
		var results []tools.Result
		if r.Executor.Pipeline != nil {
			// set OnConfirm callback, prompt Y/N in terminal
			r.Executor.OnConfirm = func(toolName, input string) bool {
				fmt.Print(plainConfirmPrompt(toolName, input))
				if r.promptConfirm() {
					return true
				}
				return false
			}
			// on-failure 沙箱升级 —— 命令在沙箱内被 EPERM 卡住时
			// 问一次，Yes 则这一条出沙箱重跑（逐条、不记忆）。与 OnConfirm 同一应答通道。
			r.Executor.OnSandboxEscalate = func(toolName, command, reason string) bool {
				fmt.Print("\n" + bi("Sandbox blocked this command", "沙箱卡住了这条命令") + "\n" +
					bi("Command: "+command, "命令："+command) + "\n" +
					bi("The OS sandbox denied it (EPERM). Run this one command outside the sandbox?",
						"OS 沙箱拒绝了它（EPERM）。让这一条命令在沙箱外执行吗？") + "\n" +
					bi("y = run outside sandbox once / n = keep the denial", "y = 仅本次沙箱外执行 / n = 维持拒绝") + "\n")
				return r.promptConfirm()
			}
			results = make([]tools.Result, 0, len(pendingTools))
			for _, pt := range pendingTools {
				fmt.Printf("[Tool: %s(%s)]\n", pt.Name, truncateStr(pt.Input, 300))
				pr := r.Executor.ExecuteWithConfirm(ctx, tools.Call{
					ID:    pt.ID,
					Name:  pt.Name,
					Input: pt.Input,
				})
				if !pr.Proceed && !pr.NeedConfirm {
					// 收口到 tools.DenialResult（**唯一实现点**），
					// 与 `tui/runner_core.go` 是同一处判据 —— 本仓"命令面双实现"
					// 是复发坑，两处必须同批改、此后只改一处即可。
					//
					// ★ `--plain` 是本条判别器的**必验侧**：删掉这里的消费，
					// `--plain` 下的拒绝原因即丢失（I-2.11 的反例注入）。
					results = append(results, tools.DenialResult(pt.ID, pt.Name, pr))
					continue
				}
				if pr.Proceed && pr.Result != nil {
					results = append(results, *pr.Result)
				} else if !pr.Proceed {
					results = append(results, tools.Result{
						ID:      pt.ID,
						Name:    pt.Name,
						Output:  fmt.Sprintf("⚠️ Pipeline Rejected: %s", pr.Rejection),
						IsError: true,
					})
				} else {
					results = append(results, tools.Result{
						ID:      pt.ID,
						Name:    pt.Name,
						Output:  "⚠️ Pipeline execution returned no result",
						IsError: true,
					})
				}
			}
		} else {
			calls := make([]tools.Call, 0, len(pendingTools))
			for _, pt := range pendingTools {
				fmt.Printf("[Tool: %s(%s)]\n", pt.Name, truncateStr(pt.Input, 300))
				calls = append(calls, tools.Call{
					ID:    pt.ID,
					Name:  pt.Name,
					Input: pt.Input,
				})
			}
			results = r.Executor.ExecuteBatch(ctx, calls)
		}

		// 工具结果出口（打印 + 组装回传消息）收口到 emitToolResults，
		// 判据见 plain_tool_result_test.go。
		toolMessages, toolFailed := emitToolResults(os.Stdout, os.Stderr, results)

		// Save tool results to session for next AI request
		r.Session.SetMessages(append(r.Session.GetMessages(), toolMessages...))

		if toolFailed {
			consecutiveToolFailures++
			if consecutiveToolFailures >= maxConsecutiveFailures {
				fmt.Fprintf(os.Stderr, "[Error] Consecutive tool failures (%d) reached, stopping to avoid infinite loop\n", consecutiveToolFailures)
				return 2, nil
			}
			// Continue loop to let AI try again with error feedback
			continue
		}
		// Reset counter on successful tool execution
		consecutiveToolFailures = 0
	}

	fmt.Fprintf(os.Stderr, "[Error] Max iterations (%d) reached\n", maxIterations)
	return 1, nil
}

// emitToolResults 是 `--plain` 的**工具结果唯一出口**：把本轮结果写到终端
// （成功 → stdout 一行 `[Result: …]`；失败 → stderr），并返回要写进会话的 tool 消息
// 与"本轮是否有失败"。
//
// 屏幕输出与**回传模型**共用 session.NormalizeToolOutput ⇒ 执行成功但输出本就为空的
// 命令（`echo hi > f` 这类重定向，stdout 天然为空）既不会印成空的 `[Result: ]`，
// 也不会让模型收到一个没有内容的 tool_result 而重发同一条命令
// （2026-09-30 真机 I-8.2）。这是「空输出」的第三个出口，前两个见
// session.NormalizeToolOutput 的注释。
//
// 为什么抽成独立函数而不是留在 RunCI 里内联：RunCI 是 ~250 行的巨型循环，
// 判据无法在不伪造 AIClient/Executor 的前提下进入其中段；抽出后本函数就是
// RunCI 的**唯一**调用点（无旁路），判据覆盖的即生产路径本身
// （见 plain_tool_result_test.go）。
func emitToolResults(stdout, stderr io.Writer, results []tools.Result) ([]session.Message, bool) {
	msgs := make([]session.Message, 0, len(results))
	failed := false
	for _, res := range results {
		shown := session.NormalizeToolOutput(res.Output, res.IsError)
		if res.IsError {
			fmt.Fprintf(stderr, "Tool error: %s\n", shown)
			failed = true
		} else {
			fmt.Fprintf(stdout, "[Result: %s]\n", truncateStr(shown, 200))
		}
		msgs = append(msgs, session.ToolResult(res.ID, res.Name, res.Output, res.IsError))
	}
	return msgs, failed
}

// truncateStr truncate string to maxLen runes
func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

// handleGitHub handles GitHub-related commands
func (r *PlainRunner) handleGitHub(args string) string {
	parts := strings.SplitN(args, " ", 2)
	subCmd := parts[0]
	subArgs := ""
	if len(parts) > 1 {
		subArgs = parts[1]
	}

	token := os.Getenv("GITHUB_TOKEN")
	if token == "" && subCmd != "status" {
		return "❌ GITHUB_TOKEN environment variable not set\nRun: set GITHUB_TOKEN=ghp_your_token_here"
	}

	ghClient := gh.NewClient(token)
	owner, repo, err := ghClient.DetectRepo(r.WorkDir)
	if err != nil && subCmd != "status" {
		return fmt.Sprintf("❌ failed to detect repo info: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch subCmd {
	case "pr":
		// pr create / pr review / pr list / pr merge
		prParts := strings.SplitN(subArgs, " ", 2)
		prSubCmd := prParts[0]
		prArgs := ""
		if len(prParts) > 1 {
			prArgs = prParts[1]
		}
		return r.handlePRCommand(ctx, ghClient, owner, repo, prSubCmd, prArgs)
	case "github":
		// github watch / github stop / github status
		ghParts := strings.SplitN(subArgs, " ", 2)
		ghSubCmd := ghParts[0]
		return r.handleGitHubCommand(ctx, ghClient, owner, repo, ghSubCmd)
	case "issue":
		// issue create / issue list / issue view
		issueParts := strings.SplitN(subArgs, " ", 2)
		issueSubCmd := issueParts[0]
		issueArgs := ""
		if len(issueParts) > 1 {
			issueArgs = issueParts[1]
		}
		return r.handleIssueCommand(ctx, ghClient, owner, repo, issueSubCmd, issueArgs)
	case "actions":
		// actions list / actions view / actions trigger / actions rerun / actions logs
		actionsParts := strings.SplitN(subArgs, " ", 2)
		actionsSubCmd := actionsParts[0]
		actionsArgs := ""
		if len(actionsParts) > 1 {
			actionsArgs = actionsParts[1]
		}
		return r.handleActionsCommand(ctx, ghClient, owner, repo, actionsSubCmd, actionsArgs)
	case "status":
		return r.handleGitHubStatus(ctx, ghClient)
	default:
		return fmt.Sprintf("Unknown GitHub command: %s\nAvailable: pr create/review/list/merge, github watch/stop/status, issue create/list/view, actions list/view/trigger/rerun/logs", subCmd)
	}
}

// handlePRCommand handles PR subcommands
func (r *PlainRunner) handlePRCommand(ctx context.Context, ghClient *gh.Client, owner, repo, subCmd, args string) string {
	adapter := gh.NewAIAdapter(r.AIClient)
	adapter.SetRecoveryConfig(r.RecoveryCoordinator, r.RetryConfig, r.Fallback)

	switch subCmd {
	case "create":
		creator := gh.NewPRCreator(ghClient, adapter, r.WorkDir)
		prInfo, err := creator.CreatePR(ctx, "")
		if err != nil {
			return fmt.Sprintf("❌ PR creation failed: %v", err)
		}
		return fmt.Sprintf("✅ PR #%d created: %s\n%s", prInfo.Number, prInfo.Title, prInfo.HTMLURL)
	case "review":
		var prNum int
		if args != "" {
			fmt.Sscanf(args, "%d", &prNum)
		}
		if prNum <= 0 {
			return "Usage: /pr review <PR number>"
		}
		reviewer := gh.NewReviewer(ghClient, adapter)
		comment, err := reviewer.ReviewPR(ctx, owner, repo, prNum, false)
		if err != nil {
			return fmt.Sprintf("❌ PR review failed: %v", err)
		}
		// publish review comments
		if err := ghClient.CreateComment(ctx, owner, repo, prNum, comment); err != nil {
			return fmt.Sprintf("⚠️ review completed but comment publish failed: %v\n\n%s", err, comment)
		}
		return fmt.Sprintf("✅ PR #%d review completed and comment posted\n\n%s", prNum, comment)
	case "list":
		state := "open"
		if args != "" {
			state = args
		}
		prs, err := ghClient.ListPRs(ctx, owner, repo, state)
		if err != nil {
			return fmt.Sprintf("❌ failed to get PR list: %v", err)
		}
		if len(prs) == 0 {
			return "no matching Pull Requests found"
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d PRs:\n", len(prs)))
		for _, pr := range prs {
			sb.WriteString(fmt.Sprintf("  #%d %s [%s] %s → %s\n    %s\n", pr.Number, pr.Title, pr.State, pr.HeadRef, pr.BaseRef, pr.HTMLURL))
		}
		return sb.String()
	case "merge":
		var prNum int
		if args != "" {
			fmt.Sscanf(args, "%d", &prNum)
		}
		if prNum <= 0 {
			return "Usage: /pr merge <PR number>"
		}
		if err := ghClient.MergePR(ctx, owner, repo, prNum); err != nil {
			return fmt.Sprintf("❌ PR merge failed: %v", err)
		}
		return fmt.Sprintf("✅ PR #%d successfully merged", prNum)
	default:
		return fmt.Sprintf("Unknown PR command: %s\nAvailable: create, review, list, merge", subCmd)
	}
}

// handleGitHubCommand handles GitHub subcommands (status only)
// Note: watch/stop subcommands were removed — personal dev machines have no public ingress,
// and the GitHub webhook/watcher polling was over-engineering for solo developers.
func (r *PlainRunner) handleGitHubCommand(ctx context.Context, ghClient *gh.Client, owner, repo, subCmd string) string {
	switch subCmd {
	case "status":
		return r.handleGitHubStatus(ctx, ghClient)
	default:
		return fmt.Sprintf("Unknown command: %s\nAvailable: status", subCmd)
	}
}

// handleGitHubStatus shows GitHub connection status
func (r *PlainRunner) handleGitHubStatus(ctx context.Context, ghClient *gh.Client) string {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return "❌ GITHUB_TOKEN not set\nRun: set GITHUB_TOKEN=ghp_your_token_here"
	}

	rateLimit, err := ghClient.GetRateLimit(ctx)
	if err != nil {
		return fmt.Sprintf("⚠️ GitHub API connection failed: %v", err)
	}

	var sb strings.Builder
	sb.WriteString("GitHub connection status:\n")
	tokenPreview := "****"
	if len(token) >= 8 {
		tokenPreview = token[:4] + "..." + token[len(token)-4:]
	} else if len(token) > 0 {
		tokenPreview = token[:1] + "***"
	}
	sb.WriteString(fmt.Sprintf("  Token: %s\n", tokenPreview))
	sb.WriteString(fmt.Sprintf("  API rate limit: %d/%d (remaining)\n", rateLimit.Remaining, rateLimit.Limit))

	owner, repo, err := ghClient.DetectRepo(r.WorkDir)
	if err != nil {
		sb.WriteString(fmt.Sprintf("  Repo: not detected (%v)\n", err))
	} else {
		sb.WriteString(fmt.Sprintf("  Repo: %s/%s\n", owner, repo))
	}
	return sb.String()
}

// handleIssueCommand handles Issue subcommands (create/list/view)
func (r *PlainRunner) handleIssueCommand(ctx context.Context, ghClient *gh.Client, owner, repo, subCmd, args string) string {
	switch subCmd {
	case "create":
		title := strings.TrimSpace(args)
		if title == "" {
			return "Usage: /issue create <title>"
		}
		issueInfo, err := ghClient.CreateIssue(ctx, owner, repo, title, "")
		if err != nil {
			return fmt.Sprintf("❌ Issue creation failed: %v", err)
		}
		return fmt.Sprintf("✅ Issue #%d created: %s\n%s", issueInfo.Number, issueInfo.Title, issueInfo.HTMLURL)
	case "list":
		state := "open"
		if args != "" {
			state = args
		}
		issues, err := ghClient.ListIssues(ctx, owner, repo, state)
		if err != nil {
			return fmt.Sprintf("❌ failed to get Issue list: %v", err)
		}
		if len(issues) == 0 {
			return "no matching Issues found"
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d Issues:\n", len(issues)))
		for _, issue := range issues {
			labels := strings.Join(issue.Labels, ", ")
			if labels != "" {
				labels = " [" + labels + "]"
			}
			sb.WriteString(fmt.Sprintf("  #%d %s [%s]%s by %s\n    %s\n", issue.Number, issue.Title, issue.State, labels, issue.User, issue.HTMLURL))
		}
		return sb.String()
	case "view":
		var issueNum int
		if args != "" {
			fmt.Sscanf(args, "%d", &issueNum)
		}
		if issueNum <= 0 {
			return "Usage: /issue view <number>"
		}
		issue, err := ghClient.GetIssue(ctx, owner, repo, issueNum)
		if err != nil {
			return fmt.Sprintf("❌ failed to get Issue details: %v", err)
		}
		labels := strings.Join(issue.Labels, ", ")
		if labels != "" {
			labels = "\nLabels: " + labels
		}
		body := issue.Body
		if body == "" {
			body = "((no body))"
		}
		return fmt.Sprintf("Issue #%d: %s\nStatus: %s\nAuthor: %s%s\n\n%s\n\n%s",
			issue.Number, issue.Title, issue.State, issue.User, labels, body, issue.HTMLURL)
	default:
		return fmt.Sprintf("Unknown Issue command: %s\nAvailable: create, list, view", subCmd)
	}
}

// handleActionsCommand handles Actions subcommands (list/view/trigger/rerun/logs)
func (r *PlainRunner) handleActionsCommand(ctx context.Context, ghClient *gh.Client, owner, repo, subCmd, args string) string {
	switch subCmd {
	case "list":
		branch := strings.TrimSpace(args)
		runs, err := ghClient.ListWorkflowRuns(ctx, owner, repo, branch)
		if err != nil {
			return fmt.Sprintf("❌ failed to get Workflow runs: %v", err)
		}
		if len(runs) == 0 {
			return "no Workflow runs found"
		}
		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Found %d Workflow runs:\n", len(runs)))
		for _, run := range runs {
			conclusion := run.Conclusion
			if conclusion == "" {
				conclusion = run.Status
			}
			sb.WriteString(fmt.Sprintf("  #%d %s [%s] branch:%s run:#%d %s\n    %s\n",
				run.ID, run.Name, conclusion, run.HeadBranch, run.RunNumber, run.CreatedAt.Format("2006-01-02 15:04"), run.HTMLURL))
		}
		return sb.String()
	case "view":
		var runID int64
		if args != "" {
			fmt.Sscanf(args, "%d", &runID)
		}
		if runID <= 0 {
			return "Usage: /actions view <run_id>"
		}
		runInfo, err := ghClient.GetWorkflowRun(ctx, owner, repo, runID)
		if err != nil {
			return fmt.Sprintf("❌ failed to get Workflow run details: %v", err)
		}
		jobs, _ := ghClient.GetWorkflowJobs(ctx, owner, repo, runID)

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Workflow run #%d\n", runInfo.ID))
		sb.WriteString(fmt.Sprintf("  Name: %s\n", runInfo.Name))
		sb.WriteString(fmt.Sprintf("  Status: %s\n", runInfo.Status))
		sb.WriteString(fmt.Sprintf("  Conclusion: %s\n", runInfo.Conclusion))
		sb.WriteString(fmt.Sprintf("  Branch: %s\n", runInfo.HeadBranch))
		sb.WriteString(fmt.Sprintf("  Run number: #%d\n", runInfo.RunNumber))
		sb.WriteString(fmt.Sprintf("  Created: %s\n", runInfo.CreatedAt.Format("2006-01-02 15:04:05")))
		sb.WriteString(fmt.Sprintf("  URL: %s\n", runInfo.HTMLURL))
		if len(jobs) > 0 {
			sb.WriteString("\nJobs:\n")
			for _, job := range jobs {
				conclusion := job.Conclusion
				if conclusion == "" {
					conclusion = job.Status
				}
				sb.WriteString(fmt.Sprintf("  - %s [%s] runner:%s\n", job.Name, conclusion, job.RunnerName))
			}
		}
		return sb.String()
	case "trigger":
		parts := strings.Fields(args)
		if len(parts) < 2 {
			return "Usage: /actions trigger <workflow_id> <ref>"
		}
		var workflowID int64
		fmt.Sscanf(parts[0], "%d", &workflowID)
		ref := parts[1]
		if workflowID <= 0 || ref == "" {
			return "Usage: /actions trigger <workflow_id> <ref>"
		}
		if err := ghClient.TriggerWorkflow(ctx, owner, repo, workflowID, ref, nil); err != nil {
			return fmt.Sprintf("❌ Trigger Workflow failed: %v", err)
		}
		return fmt.Sprintf("✅ Workflow #%d triggered (ref: %s)", workflowID, ref)
	case "rerun":
		var runID int64
		if args != "" {
			fmt.Sscanf(args, "%d", &runID)
		}
		if runID <= 0 {
			return "Usage: /actions rerun <run_id>"
		}
		if err := ghClient.RerunWorkflow(ctx, owner, repo, runID); err != nil {
			return fmt.Sprintf("❌ Re-run Workflow failed: %v", err)
		}
		return fmt.Sprintf("✅ Workflow run #%d re-triggered", runID)
	case "logs":
		var runID int64
		if args != "" {
			fmt.Sscanf(args, "%d", &runID)
		}
		if runID <= 0 {
			return "Usage: /actions logs <run_id>"
		}
		logs, err := ghClient.GetWorkflowLogs(ctx, owner, repo, runID)
		if err != nil {
			return fmt.Sprintf("❌ failed to get Workflow logs: %v", err)
		}
		return fmt.Sprintf("Workflow #%d logs:\n%s", runID, logs)
	default:
		return fmt.Sprintf("Unknown Actions command: %s\nAvailable: list, view, trigger, rerun, logs", subCmd)
	}
}

// handleCommitPushPR handles one-click commit+push+create PR flow
// PRCreator.CreatePR internally handles git add/commit/push and PR creation
func (r *PlainRunner) handleCommitPushPR() string {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		return "❌ GITHUB_TOKEN environment variable not set\nRun: set GITHUB_TOKEN=ghp_your_token_here"
	}

	rgenv := exec.Command("git", "rev-parse", "--git-dir")
	rgenv.Dir = r.WorkDir
	if err := rgenv.Run(); err != nil {
		return "❌ current directory is not a Git repository"
	}

	if r.AIClient == nil {
		return "❌ AI client not initialized, cannot generate commit message"
	}

	ghClient := gh.NewClient(token)
	adapter := gh.NewAIAdapter(r.AIClient)
	adapter.SetRecoveryConfig(r.RecoveryCoordinator, r.RetryConfig, r.Fallback)
	creator := gh.NewPRCreator(ghClient, adapter, r.WorkDir)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prInfo, err := creator.CreatePR(ctx, "")
	if err != nil {
		return fmt.Sprintf("❌ create PR failed: %v", err)
	}

	return fmt.Sprintf("✅ One-click operation complete!\n  Committed and pushed\n  PR #%d created: %s\n%s", prInfo.Number, prInfo.Title, prInfo.HTMLURL)
}

// handleDaemon handles daemon commands
func (r *PlainRunner) handleDaemon(args string) string {
	args = strings.TrimSpace(args)
	parts := strings.SplitN(args, " ", 2)
	subCmd := parts[0]
	subArgs := ""
	if len(parts) > 1 {
		subArgs = parts[1]
	}

	control := r.daemonControl
	if control == nil {
		control = daemon.NewController(daemon.DaemonConfig{WorkDir: r.WorkDir})
		r.daemonControl = control
	}

	switch subCmd {
	case "start":
		if control.IsRunning() {
			return "⚠️ Daemon is already running"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		if err := control.Start(ctx); err != nil {
			return fmt.Sprintf("❌ Failed to start daemon: %v", err)
		}
		return "✅ Daemon started"

	case "stop":
		if !control.IsRunning() {
			return "⚠️ Daemon not running"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		if err := control.Stop(ctx); err != nil {
			return fmt.Sprintf("❌ Failed to stop daemon: %v", err)
		}
		return "✅ Daemon stopped"

	case "restart":
		ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
		defer cancel()
		if err := control.Restart(ctx); err != nil {
			return fmt.Sprintf("❌ Failed to restart daemon: %v", err)
		}
		return "✅ Daemon restarted"

	case "status":
		status, err := control.Status()
		if err != nil {
			return fmt.Sprintf("❌ Failed to query daemon status: %v", err)
		}
		if !status.Running {
			return "Daemon not running"
		}
		metricsLine := ""
		if status.MetricsAddr != "" {
			metricsLine = "\n  metrics: http://" + status.MetricsAddr + "/metrics"
		}
		return fmt.Sprintf("Daemon running:\n  PID: %d\n  uptime: %v\n  workers: %d\n  pending: %d\n  running: %d%s",
			status.PID, status.Uptime.Round(time.Second), status.Workers, status.PendingTasks, status.RunningTasks, metricsLine)

	case "add":
		subArgs = strings.TrimSpace(subArgs)
		if subArgs == "" {
			return "Usage: /daemon add <prompt>"
		}
		if !control.IsRunning() {
			return "⚠️ Daemon not running; use /daemon start first"
		}
		task, err := control.AddTask(subArgs, r.WorkDir, daemon.TaskDream, daemon.PriorityNormal)
		if err != nil {
			return fmt.Sprintf("❌ Failed to add task: %v", err)
		}
		return fmt.Sprintf("✅ Task added\n  ID: %s\n  Prompt: %s", task.ID, task.Prompt)

	case "list":
		if !control.IsRunning() {
			return "⚠️ Daemon not running; use /daemon start first"
		}
		tasks, err := control.ListTasks()
		if err != nil {
			return fmt.Sprintf("❌ Failed to get task list: %v", err)
		}
		if len(tasks) == 0 {
			return "No background tasks"
		}
		var output strings.Builder
		output.WriteString(fmt.Sprintf("Background tasks (%d):\n", len(tasks)))
		for _, task := range tasks {
			output.WriteString(fmt.Sprintf("  %s  %-10s  %s\n", task.ID, task.Status, task.Prompt))
		}
		return strings.TrimRight(output.String(), "\n")

	case "cancel":
		subArgs = strings.TrimSpace(subArgs)
		if subArgs == "" {
			return "Usage: /daemon cancel <task-id>"
		}
		if !control.IsRunning() {
			return "⚠️ Daemon not running; use /daemon start first"
		}
		if err := control.CancelTask(subArgs); err != nil {
			return fmt.Sprintf("❌ Failed to cancel task: %v", err)
		}
		return fmt.Sprintf("✅ Task %s cancelled", subArgs)

	case "log":
		logPath := control.Config().LogFilePath
		data, err := os.ReadFile(logPath)
		if err != nil {
			return "no logs yet"
		}
		lines := strings.Split(string(data), "\n")
		maxLines := 50
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		return strings.Join(lines, "\n")

	case "":
		return "Usage: /daemon <start|stop|restart|status|add|list|cancel|log>"

	default:
		return fmt.Sprintf("Unknown daemon command: %s\nAvailable: start, stop, restart, status, add, list, cancel, log", subCmd)
	}
}
