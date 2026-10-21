package tui

import (
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/tools"
)

// handlePluginList list installed plugins
func (m *Model) handlePluginList() string {
	pluginDir := filepath.Join(whalehome.Dir(), "plugins")
	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No plugins installed\nUse /plugin install <url> to install"
		}
		return fmt.Sprintf("Error: %v", err)
	}
	if len(entries) == 0 {
		return "No plugins installed"
	}
	var sb strings.Builder
	sb.WriteString("Plugins:\n")
	for _, entry := range entries {
		if entry.IsDir() {
			sb.WriteString(fmt.Sprintf(" %s/\n", entry.Name()))
		}
	}
	return sb.String()
}

// handlePluginInstall install plugin from URL or local path.
//
// M20-04：安装后若插件默认启用且工具注册失败，会输出失败列表供用户感知。
func (m *Model) handlePluginInstall(source string) string {
	if source == "" {
		return "Usage: /plugin install <url|path>"
	}
	plugin, err := m.toolService.Executor().PluginMgr.Install(source)
	if err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	if plugin.Enabled {
		// M20-04：清空累计器，确保 Drain 仅返回本次 install 的失败项。
		tools.ResetToolRegistrationFailures()
		if err := m.toolService.Executor().RegisterSinglePluginTools(plugin.ID); err != nil {
			return fmt.Sprintf("⚠️ Plugin installed but tool registration failed: %v\nInstalled: %s (%s) v%s", err, plugin.Name, plugin.ID, plugin.Version)
		}
		m.toolService.Executor().ReinjectPluginHooks()

		// M20-04：Drain 失败项并附加输出（若有）。
		failures := tools.DrainToolRegistrationFailures()
		if len(failures) > 0 {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("⚠️ Installed: %s (%s) v%s but %d tool(s) failed to register:\n", plugin.Name, plugin.ID, plugin.Version, len(failures)))
			sb.WriteString(tools.FormatToolRegistrationFailures(failures))
			return sb.String()
		}
	}
	return fmt.Sprintf("✅ Installed: %s (%s) v%s", plugin.Name, plugin.ID, plugin.Version)
}

// handlePluginEnable Enable plugin and register its tools to Registry.
//
// M20-04：启用后若有工具注册失败，会输出失败列表供用户感知。
// 失败项来自 process-global 累计器（DrainToolRegistrationFailures），
// 单次 enable 仅返回该次产生的失败（启用前已 reset）。
func (m *Model) handlePluginEnable(id string) string {
	if id == "" {
		return "Usage: /plugin enable <id>"
	}
	if err := m.toolService.Executor().PluginMgr.Enable(id); err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	// M20-04：在 RegisterSinglePluginTools 前清空累计器，确保后续 Drain 仅返回本次失败。
	tools.ResetToolRegistrationFailures()
	if err := m.toolService.Executor().RegisterSinglePluginTools(id); err != nil {
		return fmt.Sprintf("⚠️ Plugin enabled but tool registration failed: %v", err)
	}
	m.toolService.Executor().ReinjectPluginHooks()

	// M20-04：Drain 本次注册失败项，若有则附加输出。
	failures := tools.DrainToolRegistrationFailures()
	if len(failures) == 0 {
		return fmt.Sprintf("✅ Plugin %s enabled", id)
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("⚠️ Plugin %s enabled but %d tool(s) failed to register:\n", id, len(failures)))
	sb.WriteString(tools.FormatToolRegistrationFailures(failures))
	sb.WriteString(fmt.Sprintf("Other tools of %s are still available.\n", id))
	return sb.String()
}

// handlePluginDisable Disable plugin and remove its tools from Registry
func (m *Model) handlePluginDisable(id string) string {
	if id == "" {
		return "Usage: /plugin disable <id>"
	}
	if err := m.toolService.Executor().PluginMgr.Disable(id); err != nil {
		return fmt.Sprintf("❌ %v", err)
	}
	m.toolService.Executor().UnregisterPluginTools(id)
	m.toolService.Executor().ReinjectPluginHooks()
	return fmt.Sprintf("✅ Plugin %s disabled", id)
}

// handleReloadPlugins Reload all plugins.
//
// M20-04：重载后若有工具注册失败，会输出失败列表供用户感知。
// 失败项来自 process-global 累计器（DrainToolRegistrationFailures）。
func (m *Model) handleReloadPlugins() string {
	plugins, err := m.toolService.Executor().PluginMgr.ReloadAll()
	if err != nil {
		return fmt.Sprintf("❌ Plugin reload failed: %v", err)
	}

	// Unload all plugin tools, then re-register
	m.toolService.Executor().UnregisterAllPluginTools()
	// M20-04：清空累计器，确保 Drain 仅返回本次 reload 的失败项。
	tools.ResetToolRegistrationFailures()
	m.toolService.Executor().RegisterPluginTools()
	m.toolService.Executor().ReinjectPluginHooks()

	var sb strings.Builder
	sb.WriteString("Plugin reload complete:\n")
	for _, p := range plugins {
		status := "✅"
		if !p.Enabled {
			status = "⏸️"
		}
		state := "Enabled"
		if !p.Enabled {
			state = "Disabled"
		}
		sb.WriteString(fmt.Sprintf(" %s %s (v%s) - %s\n", status, p.Name, p.Version, state))
	}

	// M20-04：Drain 失败项并附加输出（若有）。
	failures := tools.DrainToolRegistrationFailures()
	if len(failures) > 0 {
		sb.WriteString(fmt.Sprintf("⚠️ %d tool(s) failed to register during reload:\n", len(failures)))
		sb.WriteString(tools.FormatToolRegistrationFailures(failures))
	}
	return sb.String()
}

// handleSandbox 切换**能力轴**（OS 沙箱）；**不碰审批档**。
//
// A1：开关必须对**两套门**同时生效（见 sandbox_toggle.go 头注释），
// 否则会出现"报告 OFF、Manager 门仍拦命令"的假关闭。
//
// G12（2026-09-16，拍板 Opt-A）：本命令**只写能力轴**。此前它顺带把审批档改成
// read-only（on）/ 回落 cfg.PermissionMode（off）—— 那是跨轴偷写，含三个毛病：
// 取值来源错（cfg 是启动初始档而非"上一档"）、不可逆、以及 fail-open
// （read-only 用户敲 off 会把写入放开）。详见 sandbox_toggle.go 的 sandboxAxisNotice。
// 要一次改两轴，用 `/security-preset`（名字声明写两轴）。
func (m *Model) handleSandbox(args string) string {
	exec := m.toolService.Executor()
	// R4：档位是**本命令不会修改**的结构化事实，读一次即可（与旧实现不同，
	// 旧实现必须在 SetModeByName 之后读才能报"真实"档位）。
	mode := m.toolService.Policy().Mode()
	switch args {
	case "on":
		sandboxEnable(exec)
		return fmt.Sprintf("Sandbox ON - command validation enabled [%s]\n%s",
			sandboxGateStatus(exec), sandboxAxisNotice(mode))
	case "off":
		sandboxDisable(exec)
		return fmt.Sprintf("Sandbox OFF [%s]\n⚠️ %s\n%s",
			sandboxGateStatus(exec), sandboxDisableNotice, sandboxAxisNotice(mode))
	default:
		out := fmt.Sprintf("Sandbox: %s [%s]\n%s",
			sandboxAxisBadge(exec), sandboxGateStatus(exec), sandboxAxisNotice(mode))
		// 出口③：敲错参数回用法（唯一实现 = sandboxUnknownArgNotice，）。
		if hint := sandboxUnknownArgNotice(args); hint != "" {
			out = hint + "\n" + out
		}
		return out
	}
}

// handleUndoWithStack 使用 FileHistory 撤销最近一次工具调用的文件变更（工具级，仅文件）
func (m *Model) handleUndoWithStack() string {
	fh := m.toolService.FileHistory()
	if fh == nil {
		return "文件历史未初始化"
	}
	exec := m.toolService.Executor()
	if exec == nil {
		return "执行器未初始化"
	}
	msgId := exec.CurrentMessageId()
	if !fh.CanUndo(msgId) {
		return "没有可撤销的操作"
	}
	entry, err := fh.Undo(msgId)
	if err != nil {
		return fmt.Sprintf("撤销失败: %v", err)
	}
	var files []string
	for _, fc := range entry.Files {
		files = append(files, filepath.Base(fc.Path))
	}
	return fmt.Sprintf("✅ 已撤销: %s\n文件: %s\n可重做: /redo", entry.ToolName, strings.Join(files, ", "))
}

// handleRedoWithStack 使用 FileHistory 重做最近一次撤销的工具调用（工具级，仅文件）
func (m *Model) handleRedoWithStack() string {
	fh := m.toolService.FileHistory()
	if fh == nil {
		return "文件历史未初始化"
	}
	exec := m.toolService.Executor()
	if exec == nil {
		return "执行器未初始化"
	}
	msgId := exec.CurrentMessageId()
	if !fh.CanRedo(msgId) {
		return "没有可重做的操作"
	}
	entry, err := fh.Redo(msgId)
	if err != nil {
		return fmt.Sprintf("重做失败: %v", err)
	}
	var files []string
	for _, fc := range entry.Files {
		files = append(files, filepath.Base(fc.Path))
	}
	return fmt.Sprintf("✅ 已重做: %s\n文件: %s", entry.ToolName, strings.Join(files, ", "))
}

// handleTheme change theme
// （M10-05）：即时切换主题，无需重启 TUI。
// 调用 themeMgr.SetTheme 重建 renderer + 清空 RenderCache + VirtualScroll 缓存，
// 下一帧 View() 用新 renderer 重渲染。
// 对标 VSCode workbench.colorTheme 即时切换 @官方文档 2026-07-21。
func (m *Model) handleTheme(args string) string {
	switch args {
	case "light", "dark", "mono":
		if m.themeMgr == nil {
			return fmt.Sprintf("Theme: %s (theme manager not initialized)", args)
		}
		if err := m.themeMgr.SetTheme(args); err != nil {
			return fmt.Sprintf("Theme switch failed: %v", err)
		}
		// 更新 mdRenderer 快照（向后兼容既有调用方）
		m.mdRenderer = m.themeMgr.Renderer()
		// 清空渲染缓存，下一帧用新 renderer 重渲染
		if m.renderCache != nil {
			m.renderCache.InvalidateAll()
		}
		// 清空虚拟滚动行切片（chunkedVirtualScroll）
		if m.virtualScroll != nil {
			m.virtualScroll.InvalidateAll()
		}
		return fmt.Sprintf("Theme: %s (applied)", args)
	default:
		return "Usage: /theme light|dark|mono"
	}
}

// configOutputStyleSetter implements the command.OutputStyleSetter interface
type configOutputStyleSetter struct {
	config *config.Config
	saver  func() error
}

func (s *configOutputStyleSetter) SetOutputStyle(style string) error {
	s.config.OutputStyle = config.OutputStyle(style)
	if s.saver != nil {
		return s.saver()
	}
	return nil
}

func (s *configOutputStyleSetter) GetOutputStyle() string {
	if s.config.OutputStyle == "" {
		return string(config.OutputDefault)
	}
	return string(s.config.OutputStyle)
}
