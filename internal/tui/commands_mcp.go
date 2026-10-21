package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/daemon"
	"github.com/anchorandfish/whale-code/internal/session"
	"github.com/anchorandfish/whale-code/internal/tools"
	"github.com/anchorandfish/whale-code/internal/util"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	tea "github.com/charmbracelet/bubbletea"
)

// handleMcp handles /mcp command
func (m *Model) handleMcp(args string) string {
	if cb := m.cmdRegistry.OnMcp; cb != nil {
		return cb(args)
	}
	return "MCP management: use /mcp help for more"
}

// agentRole agent role definition
type agentRole struct {
	Name        string
	Description string
	SystemHint  string
}

// availableAgents available agent roles
var availableAgents = []agentRole{
	{Name: "coder", Description: "General coding assistant", SystemHint: "You are a coding assistant. Focus on writing clean, efficient code."},
	{Name: "reviewer", Description: "Code review specialist", SystemHint: "You are a code reviewer. Focus on finding bugs, security issues, and suggesting improvements."},
	{Name: "debugger", Description: "Debugging specialist", SystemHint: "You are a debugging specialist. Focus on identifying root causes and providing targeted fixes."},
	{Name: "refactor", Description: "Refactoring specialist", SystemHint: "You are a refactoring specialist. Focus on improving code structure, readability, and maintainability."},
	{Name: "architect", Description: "Architecture advisor", SystemHint: "You are a software architect. Focus on system design, patterns, and scalability."},
}

// handleAgents show or switch AI agents（N1 扩展：附加子任务列表摘要）
func (m *Model) handleAgents() string {
	var sb strings.Builder
	sb.WriteString("Available agents:\n")
	for _, a := range availableAgents {
		marker := " "
		if a.Name == m.sessionService.CurrentAgent() {
			marker = "→ "
		}
		sb.WriteString(fmt.Sprintf("%s%s — %s\n", marker, a.Name, a.Description))
	}
	sb.WriteString("\nUse /agents <name> to switch agent role")

	// N1 subagent / Task tool：附加子任务列表摘要（若有活跃子任务）
	if exec := m.toolService.Executor(); exec != nil && exec.AgentTaskMgr != nil {
		tasks := exec.AgentTaskMgr.List()
		if len(tasks) > 0 {
			running := 0
			pending := 0
			completed := 0
			for _, t := range tasks {
				switch t.Status {
				case tools.TaskRunning:
					running++
				case tools.TaskPending:
					pending++
				case tools.TaskCompleted:
					completed++
				}
			}
			sb.WriteString(fmt.Sprintf("\n\n🤖 Sub-Agent Tasks: %d total (running=%d, pending=%d, completed=%d)\n", len(tasks), running, pending, completed))
			sb.WriteString("Use /task list for details, /task cancel <id> to cancel")
		}
	}
	return sb.String()
}

// handleAgentSwitch switch to a specific agent role
func (m *Model) handleAgentSwitch(name string) string {
	for _, a := range availableAgents {
		if a.Name == name {
			m.sessionService.SetCurrentAgent(name)
			return fmt.Sprintf("Switched to agent: %s (%s)", name, a.Description)
		}
	}
	return fmt.Sprintf("Unknown agent: %s\nUse /agents to see available agents", name)
}

// skillDef skill definition
type skillDef struct {
	Name        string
	Description string
	Category    string
}

// availableSkills available skills
var availableSkills = []skillDef{
	{Name: "go", Description: "Go development (build, test, lint)", Category: "language"},
	{Name: "python", Description: "Python development (venv, pip, pytest)", Category: "language"},
	{Name: "typescript", Description: "TypeScript/Node.js development", Category: "language"},
	{Name: "rust", Description: "Rust development (cargo, clippy)", Category: "language"},
	{Name: "web", Description: "Web development (HTML/CSS/JS)", Category: "domain"},
	{Name: "devops", Description: "DevOps (Docker, CI/CD, k8s)", Category: "domain"},
	{Name: "database", Description: "Database (SQL, migrations)", Category: "domain"},
	{Name: "testing", Description: "Testing strategies and frameworks", Category: "domain"},
}

// handleSkills show available skills
func (m *Model) handleSkills() string {
	var sb strings.Builder
	sb.WriteString("Available skills:\n")
	categories := make(map[string][]skillDef)
	for _, s := range availableSkills {
		categories[s.Category] = append(categories[s.Category], s)
	}
	for cat, skills := range categories {
		sb.WriteString(fmt.Sprintf("\n [%s]\n", cat))
		for _, s := range skills {
			active := ""
			for _, a := range m.sessionService.ActiveSkills() {
				if a == s.Name {
					active = " ✓"
					break
				}
			}
			sb.WriteString(fmt.Sprintf(" %s — %s%s\n", s.Name, s.Description, active))
		}
	}
	sb.WriteString("\nUse /skills <name> to toggle a skill")
	return sb.String()
}

// handleSkillToggle toggle a skill on/off
func (m *Model) handleSkillToggle(name string) string {
	for _, s := range availableSkills {
		if s.Name == name {
			for i, a := range m.sessionService.ActiveSkills() {
				if a == name {
					skills := m.sessionService.ActiveSkills()
					m.sessionService.SetActiveSkills(append(skills[:i], skills[i+1:]...))
					return fmt.Sprintf("Skill deactivated: %s", name)
				}
			}
			m.sessionService.SetActiveSkills(append(m.sessionService.ActiveSkills(), name))
			return fmt.Sprintf("Skill activated: %s (%s)", name, s.Description)
		}
	}
	return fmt.Sprintf("Unknown skill: %s\nUse /skills to see available skills", name)
}

// handleSkillPrompt handles Prompt Skill commands (skillify/simplify/verify/batch)
// Renders skill template via OnSkillExecute callback; injects result as user message and triggers AI stream
func (m *Model) handleSkillPrompt(skillName, args string) string {
	if m.cmdRegistry.OnSkillExecute == nil {
		return "Skill execution not initialized; please verify .whale/skills/ directory exists"
	}
	prompt := m.cmdRegistry.OnSkillExecute(skillName, args)
	if prompt == "" {
		return fmt.Sprintf("Skill %q not found or render failed; please verify .whale/skills/%s.md exists", skillName, skillName)
	}
	// Inject rendered prompt as user message
	m.addMessage(formatUserMessage(prompt, 0))
	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(prompt)))
	if m.firstUserInput == "" {
		m.firstUserInput = prompt
	}
	if m.sessionService.SessionName() == "" {
		m.autoNameSession()
	}
	// Return special marker for caller to trigger AI stream
	return "__SKILL_PROMPT__"
}

// handleCronAdd add scheduled task
func (m *Model) handleCronAdd(args string) string {
	if args == "" {
		return `Usage: /cron add "<schedule>" "<message>"`
	}
	parts := strings.SplitN(args, "\" \"", 2)
	if len(parts) < 2 {
		return `Usage: /cron add "<schedule>" "<message>"`
	}
	schedule := strings.Trim(parts[0], "\" ")
	message := strings.TrimSuffix(parts[1], "\"")
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	var entries []map[string]string
	if data, err := os.ReadFile(cronFile); err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			entries = []map[string]string{}
		}
	}
	entry := map[string]string{
		"id":       fmt.Sprintf("%d", time.Now().Unix()),
		"schedule": schedule,
		"message":  message,
		"enabled":  "true",
	}
	entries = append(entries, entry)
	data, err := json.MarshalIndent(entries, "", " ")
	if err != nil {
		return fmt.Sprintf("Error serializing cron data: %v", err)
	}
	if err := util.AtomicWriteFile(cronFile, data, 0644); err != nil {
		return fmt.Sprintf("Error saving cron: %v", err)
	}
	return fmt.Sprintf("Cron added: %s -> %s", schedule, message)
}

// handleCronList list scheduled tasks
func (m *Model) handleCronList() string {
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	data, err := os.ReadFile(cronFile)
	if err != nil {
		return "No scheduled tasks"
	}
	var entries []map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return "No scheduled tasks (invalid data)"
	}
	if len(entries) == 0 {
		return "No scheduled tasks"
	}
	var sb strings.Builder
	sb.WriteString("Scheduled Tasks:\n")
	for i, e := range entries {
		status := "[ON]"
		if e["enabled"] == "false" {
			status = "[OFF]"
		}
		sb.WriteString(fmt.Sprintf(" %d. %s %s: %s\n", i+1, status, e["schedule"], e["message"]))
	}
	return sb.String()
}

// handleCronRemove remove scheduled task
func (m *Model) handleCronRemove(args string) string {
	if args == "" {
		return "Usage: /cron remove <id>"
	}
	cronFile := filepath.Join(whalehome.Dir(), "cron.json")
	data, err := os.ReadFile(cronFile)
	if err != nil {
		return "No scheduled tasks"
	}
	var entries []map[string]string
	if err := json.Unmarshal(data, &entries); err != nil {
		return "No scheduled tasks (invalid data)"
	}
	found := false
	var removed string
	for i, e := range entries {
		if e["id"] == args {
			removed = e["message"]
			entries = append(entries[:i], entries[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return fmt.Sprintf("Task not found: %s", args)
	}
	newData, err := json.MarshalIndent(entries, "", " ")
	if err != nil {
		return fmt.Sprintf("Error serializing cron data: %v", err)
	}
	if err := util.AtomicWriteFile(cronFile, newData, 0644); err != nil {
		return fmt.Sprintf("Error saving cron: %v", err)
	}
	return fmt.Sprintf("Removed: %s", removed)
}

// handleLoop handle /loop command, creates recurring AI prompt tasks
func (m *Model) handleLoop(args string) string {
	if m.toolService.LoopScheduler() == nil {
		return "Loop scheduler not initialized"
	}

	if args == "" {
		return "Usage: /loop [options] <interval> <prompt>\n" +
			" Interval format: 30s, 5m, 2h, 1d\n" +
			" Options:\n" +
			" --delay Delay first fire (default: immediate)\n" +
			" --once One-shot task (completes after one fire)\n" +
			" --max-fires N Limit max fires\n" +
			" Example: /loop 5m check deployment status\n" +
			" /loop --once 30s execute once\n" +
			" Subcommands: /loop list | /loop pause <id> | /loop resume <id> | /loop cancel <id>"
	}

	// Check if it is a subcommand
	if args == "list" {
		return m.handleLoopList()
	}
	if strings.HasPrefix(args, "cancel ") {
		return m.handleLoopCancel(strings.TrimPrefix(args, "cancel "))
	}
	if strings.HasPrefix(args, "pause ") {
		return m.handleLoopPause(strings.TrimPrefix(args, "pause "))
	}
	if strings.HasPrefix(args, "resume ") {
		return m.handleLoopResume(strings.TrimPrefix(args, "resume "))
	}

	// Parse options and arguments
	opts := tools.LoopCreateOptions{
		Recurring:       true,
		FireImmediately: true,
	}

	remaining := args
	for {
		if strings.HasPrefix(remaining, "--delay ") {
			opts.FireImmediately = false
			remaining = strings.TrimPrefix(remaining, "--delay ")
			remaining = strings.TrimSpace(remaining)
		} else if strings.HasPrefix(remaining, "--once ") {
			opts.Recurring = false
			remaining = strings.TrimPrefix(remaining, "--once ")
			remaining = strings.TrimSpace(remaining)
		} else if strings.HasPrefix(remaining, "--max-fires ") {
			parts := strings.SplitN(strings.TrimPrefix(remaining, "--max-fires "), " ", 2)
			n, err := strconv.Atoi(parts[0])
			if err != nil || n <= 0 {
				return "Invalid --max-fires value, positive integer required"
			}
			opts.MaxFires = n
			if len(parts) > 1 {
				remaining = parts[1]
			} else {
				remaining = ""
			}
		} else {
			break
		}
	}

	// Parse interval and prompt
	parts := strings.SplitN(remaining, " ", 2)
	if len(parts) < 2 {
		return "Usage: /loop <interval> <prompt>\n Example: /loop 5m check deployment status"
	}

	opts.IntervalStr = parts[0]
	opts.Prompt = strings.TrimSpace(parts[1])

	task, err := m.toolService.LoopScheduler().CreateWithOpts(opts)
	if err != nil {
		return fmt.Sprintf("Failed to create loop task: %v", err)
	}

	mode := "Recurring"
	if !task.Recurring {
		mode = "One-shot"
	}
	extra := ""
	if task.MaxFires > 0 {
		extra = fmt.Sprintf("\n Limit: %d times", task.MaxFires)
	}

	return fmt.Sprintf("🔄 %s task created\n ID: %s\n Interval: %s\n Prompt: %s%s\n Use /loop list | /loop cancel %s cancel",
		mode, task.ID, task.IntervalStr, task.Prompt, extra, task.ID)
}

// handleLoopList lists all recurring AI prompt tasks
func (m *Model) handleLoopList() string {
	if m.toolService.LoopScheduler() == nil {
		return "Loop scheduler not initialized"
	}
	return m.toolService.LoopScheduler().FormatList()
}

// handleLoopCancel cancels a recurring AI prompt task
func (m *Model) handleLoopCancel(args string) string {
	if m.toolService.LoopScheduler() == nil {
		return "Loop scheduler not initialized"
	}

	id := strings.TrimSpace(args)
	if id == "" {
		return "Usage: /loop cancel <id>"
	}

	if err := m.toolService.LoopScheduler().Cancel(id); err != nil {
		return fmt.Sprintf("Cancel failed: %v", err)
	}

	return fmt.Sprintf("⏹ RecurringTask %s cancelled", id)
}

// handleLoopPause pauses a recurring AI prompt task
func (m *Model) handleLoopPause(args string) string {
	if m.toolService.LoopScheduler() == nil {
		return "Loop scheduler not initialized"
	}

	id := strings.TrimSpace(args)
	if id == "" {
		return "Usage: /loop pause <id>"
	}

	if err := m.toolService.LoopScheduler().Pause(id); err != nil {
		return fmt.Sprintf("Pause failed: %v", err)
	}

	return fmt.Sprintf("⏸ loop task %s paused, use /loop resume %s to resume", id, id)
}

// handleLoopResume resumes a paused recurring AI prompt task
func (m *Model) handleLoopResume(args string) string {
	if m.toolService.LoopScheduler() == nil {
		return "Loop scheduler not initialized"
	}

	id := strings.TrimSpace(args)
	if id == "" {
		return "Usage: /loop resume <id>"
	}

	if err := m.toolService.LoopScheduler().Resume(id); err != nil {
		return fmt.Sprintf("Resume failed: %v", err)
	}

	return fmt.Sprintf("▶ loop task %s resumed", id)
}

// handleLoopCmd handles /loop command callback (used by OnLoop callback)
func (m *Model) handleLoopCmd(args string) string {
	return m.handleLoop(args)
}

// daemonAlreadyRunning / daemonNotRunningStop / daemonNotRunning 是 `/daemon` 的复用提示。
//
// ⚠️ 「未运行」存在**两种措辞**且都被既有测试逐字锁定
// （daemon_test.go：stop/add/cancel 断言「未在运行」，status/list 断言「未运行」）
// ⇒ 本批**保持逐字不变**、只做双语化；措辞统一属另一件事（已登记，勿在本批顺手改）。
var (
	daemonAlreadyRunning = bi("⚠️ Daemon is already running", "⚠️ 守护进程已在运行")
	daemonNotRunningStop = bi("⚠️ Daemon is not running; use /daemon start to start it",
		"⚠️ 守护进程未在运行；请使用 /daemon start 启动守护进程")
	daemonNotRunning = bi("⚠️ Daemon is not running; use /daemon start to start it",
		"⚠️ 守护进程未运行；请使用 /daemon start 启动守护进程")
)

// handleDaemon handles daemon-related commands
// Uses deferred start mode: daemon instance created at TUI init but not started,
// User manually starts daemon with /daemon start
func (m *Model) handleDaemon(args string) string {
	control := m.daemonControl
	if control == nil && m.toolService != nil && m.toolService.Daemon() != nil {
		control = daemon.NewController(m.toolService.Daemon().GetConfig())
		m.daemonControl = control
	}
	if control == nil {
		return bi("Daemon not initialized; please restart Whale Code", "守护进程未初始化；请重启 Whale Code")
	}

	args = strings.TrimSpace(args)

	switch {
	case args == "start":
		if control.IsRunning() {
			return daemonAlreadyRunning
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		if err := control.Start(ctx); err != nil {
			if errors.Is(err, daemon.ErrAlreadyRunning) {
				return daemonAlreadyRunning
			}
			return biSame("❌ Failed to start daemon: %v", "❌ 启动守护进程失败: %v", err)
		}
		return bi("✅ Daemon started", "✅ 守护进程已启动")

	case args == "stop":
		if !control.IsRunning() {
			return daemonNotRunningStop
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		if err := control.Stop(ctx); err != nil {
			return biSame("❌ Failed to stop daemon: %v", "❌ 停止守护进程失败: %v", err)
		}
		return bi("✅ Daemon stopped", "✅ 守护进程已停止")

	case args == "restart":
		ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
		defer cancel()
		if err := control.Restart(ctx); err != nil {
			return biSame("❌ Failed to restart daemon: %v", "❌ 重启守护进程失败: %v", err)
		}
		return bi("✅ Daemon restarted", "✅ 守护进程已重启")

	case args == "status":
		status, err := control.Status()
		if err != nil {
			return biSame("❌ Failed to get daemon status: %v", "❌ 获取守护进程状态失败: %v", err)
		}
		if !status.Running {
			return daemonNotRunning
		}
		return m.formatDaemonStatus(status)

	case args == "add" || strings.HasPrefix(args, "add "):
		prompt := strings.TrimSpace(strings.TrimPrefix(args, "add"))
		if prompt == "" {
			return bi("Please provide a task description: /daemon add <prompt>", "请输入任务描述: /daemon add <prompt>")
		}
		if !control.IsRunning() {
			return daemonNotRunningStop
		}
		// 获取工作目录：优先从 sessionService，回退到 Model.workDir
		var workDir string
		if m.sessionService != nil {
			workDir = m.sessionService.WorkDir()
		} else {
			workDir = m.workDir
		}
		task, err := control.AddTask(prompt, workDir, daemon.TaskDream, daemon.PriorityNormal)
		if err != nil {
			return biSame("❌ Failed to add background task: %v", "❌ 添加后台任务失败: %v", err)
		}
		return biSame("✅ Task added\n ID: %s\n Prompt: %s", "✅ 任务已添加\n ID: %s\n Prompt: %s", task.ID, task.Prompt)

	case args == "list":
		if !control.IsRunning() {
			return daemonNotRunning
		}
		tasks, err := control.ListTasks()
		if err != nil {
			return biSame("❌ Failed to get background tasks: %v", "❌ 获取后台任务失败: %v", err)
		}
		return m.formatDaemonTaskList(tasks)

	case args == "cancel" || strings.HasPrefix(args, "cancel "):
		id := strings.TrimSpace(strings.TrimPrefix(args, "cancel"))
		if id == "" {
			return bi("Usage: /daemon cancel <id>", "用法: /daemon cancel <id>")
		}
		if !control.IsRunning() {
			return daemonNotRunningStop
		}
		if err := control.CancelTask(id); err != nil {
			return biSame("Cancel failed: %v", "取消失败: %v", err)
		}
		return biSame("Task %s cancelled", "任务 %s 已取消", id)

	case args == "log":
		return m.handleDaemonLog()

	case args == "":
		return bi("Usage:\n"+
			" /daemon start — start the daemon\n"+
			" /daemon stop — stop the daemon\n"+
			" /daemon restart — restart the daemon\n"+
			" /daemon status — show daemon status\n"+
			" /daemon add — add a background task\n"+
			" /daemon list — list background tasks\n"+
			" /daemon cancel — cancel a background task\n"+
			" /daemon log — view the daemon log",
			"用法:\n"+
				" /daemon start — 启动守护进程\n"+
				" /daemon stop — 停止守护进程\n"+
				" /daemon restart — 重启守护进程\n"+
				" /daemon status — 查看守护进程状态\n"+
				" /daemon add — 添加后台任务\n"+
				" /daemon list — 列出后台任务\n"+
				" /daemon cancel — 取消后台任务\n"+
				" /daemon log — 查看守护进程日志")

	default:
		return biSame("Unknown daemon subcommand: %s\nAvailable: start, stop, restart, status, add, list, cancel, log",
			"未知守护进程子命令: %s\n可用: start, stop, restart, status, add, list, cancel, log", args)
	}
}

// formatDaemonStatus format daemon status info
func (m *Model) formatDaemonStatus(status daemon.DaemonStatus) string {
	var sb strings.Builder
	sb.WriteString(bi("🐋 Daemon status", "🐋 守护进程状态") + "\n\n")

	if status.Running {
		sb.WriteString(bi("Status: ✅ running", "状态: ✅ 运行中") + "\n")
		sb.WriteString(fmt.Sprintf("PID: %d\n", status.PID))
		sb.WriteString(biSame("Workers: %d", "工作器: %d", status.Workers) + "\n")
		sb.WriteString(biSame("Running tasks: %d", "运行中任务: %d", status.RunningTasks) + "\n")
		sb.WriteString(biSame("Pending tasks: %d", "待处理任务: %d", status.PendingTasks) + "\n")
		sb.WriteString(biSame("Uptime: %s", "运行时间: %s", status.Uptime.Truncate(time.Second).String()) + "\n")
		if status.MetricsAddr != "" {
			sb.WriteString(fmt.Sprintf("Metrics: http://%s/metrics\n", status.MetricsAddr))
		}
	} else {
		sb.WriteString(bi("Status: ⛔ not running", "状态: ⛔ 未运行") + "\n")
	}

	return sb.String()
}

// formatDaemonTaskList formats daemon task list
func (m *Model) formatDaemonTaskList(tasks []daemon.Task) string {
	if len(tasks) == 0 {
		return bi("No background tasks", "暂无后台任务")
	}

	var sb strings.Builder
	sb.WriteString(biSame("🐋 Background task list (%d total)", "🐋 后台任务列表 (共 %d 个)", len(tasks)) + "\n\n")

	// 表头是**单行表格**，容不下缩进辅行 ⇒ 一律英文单侧（与 priorityLabel 同处理）。
	sb.WriteString(fmt.Sprintf("%-36s %-10s %-10s %s\n", "ID", "Status", "Type", "Created"))
	sb.WriteString(strings.Repeat("-", 80) + "\n")

	for _, t := range tasks {
		statusIcon := "⏳"
		switch t.Status {
		case daemon.TaskCompleted:
			statusIcon = "✅"
		case daemon.TaskRunning:
			statusIcon = "🔄"
		case daemon.TaskFailed:
			statusIcon = "❌"
		case daemon.TaskCancelled:
			statusIcon = "⛔"
		}

		sb.WriteString(fmt.Sprintf("%-36s %s %-9s %-10s %s\n",
			t.ID, statusIcon, t.Status, t.Type, t.CreatedAt.Format("2006-01-02 15:04:05")))

		if t.Prompt != "" {
			prompt := t.Prompt
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			sb.WriteString(fmt.Sprintf(" 📝 %s\n", prompt))
		}
	}

	return sb.String()
}

// handleDaemonLog 读取并返回守护进程日志
func (m *Model) handleDaemonLog() string {
	if m.daemonControl == nil {
		return bi("Daemon not initialized; please restart Whale Code", "守护进程未初始化；请重启 Whale Code")
	}
	cfg := m.daemonControl.Config()
	if cfg.LogFilePath == "" {
		return bi("Log file path is not configured", "日志文件路径未配置")
	}

	data, err := os.ReadFile(cfg.LogFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			return bi("Log file does not exist", "日志文件不存在")
		}
		return biSame("Failed to read log: %v", "读取日志失败: %v", err)
	}

	if len(data) == 0 {
		return bi("🐋 Daemon log (empty)", "🐋 守护进程日志 (空)")
	}

	// Return last 50 lines of log
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	start := 0
	if len(lines) > 50 {
		start = len(lines) - 50
	}

	// 仅表头双语；日志正文原样拼一次（不要把整段正文复制进中文辅行）。
	return bi(fmt.Sprintf("🐋 Daemon log (last %d lines)", len(lines)-start),
		fmt.Sprintf("🐋 守护进程日志 (最后 %d 行)", len(lines)-start)) + "\n\n" + strings.Join(lines[start:], "\n")
}

// handleLoopFire 处理循环任务触发事件，把提示注入会话并启动新的流式请求。
func (m *Model) handleLoopFire(msg loopFireMsg) tea.Cmd {
	// If AI is streaming, skip this fire (avoid interrupting current conversation)
	//
	// A-1：判定改走 turnWriteOpen()（同一事实的唯一实现）。
	// 语义不变（原先就是 `State() != StateIdle`），变的只是"同一事实不再有两处实现"。
	// ★ 同时把措辞摆正：这里守的是"别在流式中开新一轮"，**不等于**"主对话流的写入
	// 权只在 turn 内" —— 后者是 A-1 新增的结构不变量；两者只是恰好用同一个判据。
	if m.turnWriteOpen() {
		// Non-idle: loop fire is skipped to avoid interrupting current conversation
		// For --once tasks, this means the task will be silently dropped if AI is streaming
		return waitForLoopFire(m.toolService.LoopCh())
	}

	// Inject loop task prompt as virtual user message into conversation
	prompt := fmt.Sprintf("[Loop %s] %s", msg.taskID, msg.prompt)
	m.addMessage(cmdStyle.Render("🔄 " + prompt))
	m.sessionService.CurrentSession().SetMessages(append(m.sessionService.CurrentSession().GetMessages(), session.UserText(prompt)))

	if m.firstUserInput == "" {
		m.firstUserInput = prompt
	}
	if m.sessionService.SessionName() == "" {
		m.autoNameSession()
	}

	m.sessionService.SetState(StateStreaming)
	m.resetStreamState()
	// P0 agent 防护：loop 触发视为新用户请求，防护状态归零。不归零的后果：
	// ① 上一请求以停机结束时闩锁未释放，loop fire 反复撞闸门重复渲染快照；
	// ② 上一请求的轮次/审查计数跨请求污染，loop 任务预算被吃掉。
	m.guardBeginRequest()
	m.aiService.SetRequestID(fmt.Sprintf("%d", time.Now().UnixNano()))

	return tea.Batch(m.startAIStream(), m.restartTickCmd(), waitForLoopFire(m.toolService.LoopCh()))
}

// handleMcpCommand MCP server management
func (m *Model) handleMcpCommand(args string) string {
	if cb := m.cmdRegistry.OnMcp; cb != nil {
		return cb(args)
	}
	parts := strings.Fields(args)
	if len(parts) == 0 || parts[0] == "list" {
		// Read MCP config from ~/.whale/mcp.json
		mcpConfigPath := filepath.Join(whalehome.Dir(), "mcp.json")
		data, err := os.ReadFile(mcpConfigPath)
		if err != nil {
			return "MCP servers: (none)\nConfig file: " + mcpConfigPath + "\nUse /mcp add <name> <command> to add server"
		}

		var mcpConfig map[string]interface{}
		if err := json.Unmarshal(data, &mcpConfig); err != nil {
			return "MCP config parse failed: " + err.Error()
		}

		servers, ok := mcpConfig["mcpServers"].(map[string]interface{})
		if !ok || len(servers) == 0 {
			return "MCP servers: (none)\nUse /mcp add <name> <command> to add server"
		}

		var sb strings.Builder
		sb.WriteString("MCP servers:\n")
		for name, cfg := range servers {
			cfgMap, _ := cfg.(map[string]interface{})
			cmd := ""
			if c, ok := cfgMap["command"]; ok {
				cmd = fmt.Sprintf("%v", c)
			}
			sb.WriteString(fmt.Sprintf(" ✅ %s — %s\n", name, cmd))
		}
		return sb.String()
	}
	switch parts[0] {
	case "add":
		if len(parts) < 3 {
			return "Usage: /mcp add <name> <command>"
		}
		return fmt.Sprintf("Please manually add MCP server config in ~/.whale/mcp.json:\n \"mcpServers\": {\n \"%s\": {\n \"command\": \"%s\"\n }\n }", parts[1], strings.Join(parts[2:], " "))
	case "remove":
		if len(parts) < 2 {
			return "Usage: /mcp remove <name>"
		}
		return fmt.Sprintf("Please remove the config for \"%s\" in ~/.whale/mcp.json", parts[1])
	case "restart":
		if len(parts) < 2 {
			return "Usage: /mcp restart <name>"
		}
		return fmt.Sprintf("Please modify the config for \"%s\" in ~/.whale/mcp.json and restart", parts[1])
	default:
		return "Unknown subcommand. Usage: /mcp [list|add|remove|restart]"
	}
}
