// Package tui — N1 subagent / Task tool 的 Task UI 组件。
//
// 本文件实现 taskListModel（Bubble Tea Model），用于展示 AgentTaskManager
// 管理的子任务列表。设计要点：
// - 复用现有 Bubble Tea 架构（tea.Model 接口），不引入新依赖
// - tea.Tick 1s 定时拉取 AgentTaskManager.List() 刷新视图
// - 显示字段：ID（前 8 位）/ 状态图标（⏳▶️✅❌🚫）/ 深度 / 父 ID / 创建时间
// - 既可作为独立 tea.Program 运行（占屏交互），也可作为渲染组件被 /task 命令复用
package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// taskListTickMsg 是 taskListModel 的定时刷新消息（tea.Tick 触发）。
type taskListTickMsg time.Time

// taskListStyles 是 Task UI 的 lipgloss 样式集合（复用全局色调，独立定义避免污染 app.go）。
var taskListStyles = struct {
	title   lipgloss.Style
	header  lipgloss.Style
	row     lipgloss.Style
	pending lipgloss.Style
	running lipgloss.Style
	ok      lipgloss.Style
	failed  lipgloss.Style
	cancel  lipgloss.Style
	empty   lipgloss.Style
}{
	title: lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#00ADD8")).
		MarginBottom(1),
	header: lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#8AADF4")),
	row: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#CAD3F5")),
	pending: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#EED49F")), // 黄
	running: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#8BE9FD")), // 蓝
	ok: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#A6DA95")), // 绿
	failed: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ED8796")), // 红
	cancel: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6C7086")), // 灰
	empty: lipgloss.NewStyle().
		Foreground(lipgloss.Color("#6C7086")).
		Italic(true),
}

// taskListModel 是 Task UI 的 Bubble Tea Model。
//
// 字段：
// - manager：AgentTaskManager 引用，通过 List() 拉取任务快照
// - tasks：最近一次拉取的任务列表（受 tick 刷新）
// - width/height：终端尺寸（供 View 自适应换行）
// - quit：是否退出（q/Ctrl+C 触发）
type taskListModel struct {
	manager *tools.AgentTaskManager
	tasks   []tools.AgentTaskInfo
	width   int
	height  int
	quit    bool
}

// newTaskListModel 创建 taskListModel 实例。
// manager 为 nil 时模型仍可运行，View 显示"管理器未初始化"。
func newTaskListModel(manager *tools.AgentTaskManager) *taskListModel {
	return &taskListModel{
		manager: manager,
	}
}

// Init 实现 tea.Model 接口，启动 1s 定时刷新（tea.Tick）。
func (m *taskListModel) Init() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return taskListTickMsg(t)
	})
}

// Update 实现 tea.Model 接口，处理定时刷新与按键。
//
// 消息：
// - taskListTickMsg：拉取 manager.List() 刷新 tasks，继续下一次 tick
// - tea.WindowSizeMsg：记录终端尺寸
// - tea.KeyMsg：q/Ctrl+C 退出
func (m *taskListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case taskListTickMsg:
		m.refresh()
		return m, tea.Tick(time.Second, func(t time.Time) tea.Msg {
			return taskListTickMsg(t)
		})
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.quit = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// View 实现 tea.Model 接口，渲染任务列表。
func (m *taskListModel) View() string {
	if m.quit {
		return ""
	}
	return m.Render()
}

// refresh 从 manager 拉取最新任务列表（nil 安全）。
func (m *taskListModel) refresh() {
	if m.manager == nil {
		m.tasks = nil
		return
	}
	m.tasks = m.manager.List()
}

// Render 渲染任务列表为字符串（供 /task 命令复用，无需启动独立 tea.Program）。
//
// 格式：
//
//	🤖 Sub-Agent Tasks (N)
//
//	ID Status Depth Parent Created
//	abcd1234 ⏳ 1 - 15:04:05
//	...
//
// 无任务时显示空状态提示。
func (m *taskListModel) Render() string {
	if m.manager == nil {
		return taskListStyles.empty.Render("🤖 Sub-Agent Tasks: 管理器未初始化")
	}
	if m.tasks == nil {
		m.refresh()
	}

	var sb strings.Builder
	sb.WriteString(taskListStyles.title.Render(fmt.Sprintf("🤖 Sub-Agent Tasks (%d)", len(m.tasks))))
	sb.WriteString("\n")

	if len(m.tasks) == 0 {
		sb.WriteString(taskListStyles.empty.Render("（无子任务，使用 Task tool 让 AI 派生子任务）"))
		return sb.String()
	}

	// 表头
	sb.WriteString(taskListStyles.header.Render(
		fmt.Sprintf("%-8s %-8s %-5s %-8s %s",
			"ID", "Status", "Depth", "Parent", "Created")))
	sb.WriteString("\n")

	// 表体
	for _, t := range m.tasks {
		icon, style := statusIconAndStyle(t.Status)
		parent := shortID(t.ParentID)
		if parent == "" {
			parent = "-"
		}
		created := t.CreatedAt.Format("15:04:05")
		row := fmt.Sprintf("%-8s %s %-5d %-8s %s",
			shortID(t.ID), icon, t.Depth, parent, created)
		sb.WriteString(style.Render(row))
		sb.WriteString("\n")
	}

	sb.WriteString(taskListStyles.empty.Render("\n按 q 退出（独立窗口模式）"))
	return sb.String()
}

// statusIconAndStyle 根据 TaskStatus 返回状态图标与对应样式。
// 图标：⏳ pending / ▶️ running / ✅ completed / ❌ failed / 🚫 cancelled。
func statusIconAndStyle(s tools.TaskStatus) (string, lipgloss.Style) {
	switch s {
	case tools.TaskPending:
		return "⏳", taskListStyles.pending
	case tools.TaskRunning:
		return "▶️", taskListStyles.running
	case tools.TaskCompleted:
		return "✅", taskListStyles.ok
	case tools.TaskFailed:
		return "❌", taskListStyles.failed
	case tools.TaskCancelled:
		return "🚫", taskListStyles.cancel
	default:
		return "?", taskListStyles.row
	}
}

// shortID 截取 UUID 前 8 位用于紧凑显示。
func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

// RenderTaskList 是给 /task 命令调用的便捷函数，返回任务列表字符串。
// manager 为 nil 时返回空状态提示。
func RenderTaskList(manager *tools.AgentTaskManager) string {
	m := newTaskListModel(manager)
	return m.Render()
}
