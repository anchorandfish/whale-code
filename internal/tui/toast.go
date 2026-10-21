package tui

import (
	"fmt"
	"time"

	"github.com/anchorandfish/whale-code/internal/notify"
	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// toastTimeoutMsg 是带世代号的 toast 超时消息，过期世代会被忽略。
type toastTimeoutMsg struct {
	seq uint64
}

// toastPushMsg toast 推送消息（Notifier onPush 回调 → tea.Msg 桥接）
type toastPushMsg struct {
	notification *notify.Notification
}

// ToastModel 通知 toast 组件（bubbletea Model）
// 订阅 Notifier 的 onPush 回调，渲染当前通知，timeout 自动 Dismiss。
// 布局：顶部叠加，不抢占输入焦点（除非 Immediate）。
type ToastModel struct {
	notifier   *notify.Notifier
	visible    bool
	width      int
	pushCh     chan *notify.Notification
	timeoutSeq uint64
}

// NewToastModel 创建 toast 组件
func NewToastModel(notifier *notify.Notifier) *ToastModel {
	m := &ToastModel{
		notifier: notifier,
		pushCh:   make(chan *notify.Notification, 64),
	}
	// 设置 onPush 回调：将通知写入 pushCh，由 waitForPush Cmd 消费
	notifier.SetOnPush(func(nf *notify.Notification) {
		select {
		case m.pushCh <- nf:
		default:
			// channel 满时丢弃（非阻塞，避免 onPush 回调阻塞）
		}
	})
	return m
}

// waitForPush 返回等待 pushCh 的 tea.Cmd（桥接 onPush → tea.Msg）
func (m *ToastModel) waitForPush() tea.Cmd {
	return func() tea.Msg {
		nf, ok := <-m.pushCh
		if !ok {
			return nil
		}
		return toastPushMsg{notification: nf}
	}
}

// Init bubbletea Model 接口
func (m *ToastModel) Init() tea.Cmd {
	return m.waitForPush()
}

// Update 处理消息（toast 推送 / timeout tick / 窗口大小 / Esc 关闭）
func (m *ToastModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case toastPushMsg:
		// 新通知推送：尝试取下一个通知显示
		return m, tea.Batch(m.showNext(), m.waitForPush())

	case toastTimeoutMsg:
		// 旧通知的 timeout 不得关闭当前新通知。
		if msg.seq != m.timeoutSeq {
			return m, nil
		}
		m.timeoutSeq++
		m.notifier.Dismiss()
		m.visible = false
		// 继续取下一个通知
		return m, m.showNext()

	case tea.KeyMsg:
		// Esc 关闭当前通知（持久通知或 Immediate 通知）
		if msg.String() == "esc" && m.visible {
			m.timeoutSeq++
			m.notifier.Dismiss()
			m.visible = false
			return m, m.showNext()
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
	}

	return m, nil
}

// showNext 尝试显示队列中下一个通知，返回 tea.Cmd
func (m *ToastModel) showNext() tea.Cmd {
	// 如果当前有通知在显示，不再取下一个
	if cur := m.notifier.Current(); cur != nil {
		return nil
	}

	nf := m.notifier.Next()
	if nf == nil {
		m.timeoutSeq++
		m.visible = false
		return nil
	}

	m.visible = true
	m.timeoutSeq++
	seq := m.timeoutSeq

	// 持久通知（TimeoutMs=-1）不启动 timer
	if nf.TimeoutMs < 0 {
		return nil
	}

	// tea.Tick 不持有可被 Stop 后永久阻塞的 timer channel；世代号负责淘汰旧消息。
	duration := time.Duration(nf.TimeoutMs) * time.Millisecond
	return tea.Tick(duration, func(time.Time) tea.Msg {
		return toastTimeoutMsg{seq: seq}
	})
}

// dismissCurrent 关闭当前 toast、失效旧超时并立即切换到队列中的下一条通知。
func (m *ToastModel) dismissCurrent() tea.Cmd {
	if m == nil || m.notifier == nil {
		return nil
	}
	m.timeoutSeq++
	m.notifier.Dismiss()
	m.visible = false
	return m.showNext()
}

// View 渲染当前通知（标题 + 消息 + 计数 + 优先级颜色）
// 布局：顶部叠加，不与底部状态栏冲突
func (m *ToastModel) View() string {
	if !m.visible {
		return ""
	}

	nf := m.notifier.Current()
	if nf == nil {
		return ""
	}

	// 优先级颜色（：Immediate=红色/High=黄色/Medium=白色/Low=灰色）
	var style lipgloss.Style
	width := m.width
	if width <= 0 {
		width = 80
	}
	// 限制最大宽度，留一些边距
	if width > 120 {
		width = 120
	}

	switch nf.Priority {
	case notify.PriorityImmediate:
		style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FF0000")).
			Background(lipgloss.Color("#330000")).
			Bold(true).
			Padding(0, 1).
			Width(width - 2)
	case notify.PriorityHigh:
		style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFAA00")).
			Background(lipgloss.Color("#332200")).
			Padding(0, 1).
			Width(width - 2)
	case notify.PriorityMedium:
		style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(lipgloss.Color("#222222")).
			Padding(0, 1).
			Width(width - 2)
	case notify.PriorityLow:
		style = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#888888")).
			Background(lipgloss.Color("#1A1A1A")).
			Padding(0, 1).
			Width(width - 2)
	}

	// 构建内容
	var content string
	if nf.Title != "" {
		content = nf.Title
	}
	if nf.Message != "" {
		if content != "" {
			content += ": "
		}
		content += nf.Message
	}

	// fold 计数 >1 时显示 ×N
	if nf.Count > 1 {
		content = fmt.Sprintf("%s ×%d", content, nf.Count)
	}

	// TimeoutMs=-1（持久）时显示 [按 Esc 关闭] 提示
	if nf.TimeoutMs < 0 {
		content = fmt.Sprintf("%s  [按 Esc 关闭]", content)
	}

	return style.Render(content)
}

// SetWidth 设置渲染宽度（跟随终端窗口大小）
func (m *ToastModel) SetWidth(w int) {
	m.width = w
}

// Visible 返回 toast 是否可见
func (m *ToastModel) Visible() bool {
	return m.visible
}
