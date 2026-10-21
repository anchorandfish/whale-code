package command

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// shellInfo contains Shell type and config file path info
type shellInfo struct {
	ShellType    string
	ShellVersion string
	ConfigPath   string
	ProfileCmd   string // Command to reload the profile
}

// detectShell detects the current Shell type
func detectShell() shellInfo {
	info := shellInfo{ShellType: "unknown"}

	if runtime.GOOS == "windows" {
		return detectWindowsShell()
	}

	// Unix-like
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	basename := filepath.Base(shell)
	homeDir, _ := os.UserHomeDir()

	switch basename {
	case "bash":
		info.ShellType = "bash"
		info.ConfigPath = filepath.Join(homeDir, ".bashrc")
		info.ProfileCmd = fmt.Sprintf("source %s", info.ConfigPath)
	case "zsh":
		info.ShellType = "zsh"
		info.ConfigPath = filepath.Join(homeDir, ".zshrc")
		info.ProfileCmd = fmt.Sprintf("source %s", info.ConfigPath)
	case "fish":
		info.ShellType = "fish"
		info.ConfigPath = filepath.Join(homeDir, ".config", "fish", "config.fish")
		info.ProfileCmd = fmt.Sprintf("source %s", info.ConfigPath)
	default:
		info.ShellType = basename
		info.ConfigPath = filepath.Join(homeDir, ".profile")
		info.ProfileCmd = fmt.Sprintf("source %s", info.ConfigPath)
	}

	return info
}

// detectWindowsShell detects the Windows Shell
// Uses multiple detection methods for improved reliability:
// 1. PSModulePath for PowerShell
// 2. ComSpec for cmd.exe
// 3. Process name for pwsh (PowerShell Core)
func detectWindowsShell() shellInfo {
	info := shellInfo{}

	// Check PowerShell by looking for PowerShell-specific environment variables
	// PowerShell sets PSModulePath and has pwsh.exe / powershell.exe in the process path
	psModulePath := os.Getenv("PSModulePath")
	comSpec := os.Getenv("ComSpec")

	// Reliable PowerShell detection: PSModulePath is only set in PowerShell
	if psModulePath != "" {
		// Detect pwsh (PowerShell Core) vs powershell (Windows PowerShell)
		shellName := os.Getenv("PWSH") // pwsh sets this
		procArch := os.Getenv("PROCESSOR_ARCHITECTURE")
		_ = procArch // reserved for future use

		info.ShellType = "powershell"
		if shellName != "" || strings.Contains(strings.ToLower(comSpec), "pwsh") {
			info.ShellType = "pwsh"
		}
		// Get PowerShell profile path; PROFILE is auto-defined in PowerShell sessions
		homeDir, _ := os.UserHomeDir()
		profilePath := os.Getenv("PROFILE")
		if profilePath == "" && homeDir != "" {
			// Default PowerShell 5 profile path
			profilePath = filepath.Join(homeDir, "Documents", "PowerShell", "Microsoft.PowerShell_profile.ps1")
		}
		info.ConfigPath = profilePath
		info.ProfileCmd = fmt.Sprintf(". '%s'", info.ConfigPath)
		return info
	}

	// Check if running in cmd
	if comSpec != "" {
		info.ShellType = "cmd"
		info.ShellVersion = os.Getenv("OS")
		info.ConfigPath = "N/A (cmd does not support auto-configuration)"
		return info
	}

	info.ShellType = "windows-unknown"
	return info
}

// NewTerminalSetupCommandHandler creates the /terminal-setup command handler
func NewTerminalSetupCommandHandler() func(args string) string {
	return func(args string) string {
		args = strings.TrimSpace(strings.ToLower(args))
		if args == "" || args == "status" {
			return formatTerminalStatus()
		}
		switch args {
		case "install":
			return handleTerminalInstall()
		case "uninstall":
			return handleTerminalUninstall()
		default:
			return fmt.Sprintf("Unknown subcommand: %s\nUsage: /terminal-setup [install|uninstall|status]", args)
		}
	}
}

// formatTerminalStatus formats the terminal integration status
func formatTerminalStatus() string {
	info := detectShell()

	installed := false
	if info.ConfigPath != "" && info.ConfigPath != "N/A (cmd does not support auto-configuration)" {
		if data, err := os.ReadFile(info.ConfigPath); err == nil {
			if strings.Contains(string(data), "whale shell-hook") || strings.Contains(string(data), "whale.exe shell-hook") {
				installed = true
			}
		}
	}

	var sb strings.Builder
	sb.WriteString("Terminal integration status:\n")
	sb.WriteString(fmt.Sprintf("  Shell type: %s", info.ShellType))
	if info.ShellVersion != "" {
		sb.WriteString(fmt.Sprintf(" (%s)", info.ShellVersion))
	}
	sb.WriteString("\n")

	if installed {
		sb.WriteString("  Integration status: installed\n")
	} else {
		sb.WriteString("  Integration status: not installed\n")
	}
	sb.WriteString(fmt.Sprintf("  Config file: %s\n", info.ConfigPath))

	if !installed {
		sb.WriteString("\n  Install method: /terminal-setup install")
		if info.ConfigPath == "N/A (cmd does not support auto-configuration)" {
			sb.WriteString("\n  ⚠️  Current Shell does not support auto-configuration, please install manually")
		}
	}

	return sb.String()
}

// handleTerminalInstall installs terminal integration
func handleTerminalInstall() string {
	info := detectShell()

	if info.ConfigPath == "" || strings.Contains(info.ConfigPath, "N/A") {
		return `Unable to detect Shell type, please install manually:
Add the following line to your Shell config file:
  eval $(whale shell-hook)

Common config files:
  PowerShell: $PROFILE
  Bash: ~/.bashrc
  Zsh: ~/.zshrc`
	}

	// Check if already installed
	if data, err := os.ReadFile(info.ConfigPath); err == nil {
		if strings.Contains(string(data), "whale shell-hook") || strings.Contains(string(data), "whale.exe shell-hook") {
			return fmt.Sprintf("Terminal integration already installed\n  Config file: %s", info.ConfigPath)
		}
	}

	// Backup config file
	backupPath := info.ConfigPath + ".whale-backup"
	if _, err := os.Stat(info.ConfigPath); err == nil {
		data, err := os.ReadFile(info.ConfigPath)
		if err != nil {
			return fmt.Sprintf("Unable to read config file: %v", err)
		}
		if err := os.WriteFile(backupPath, data, 0644); err != nil {
			return fmt.Sprintf("Unable to create backup: %v", err)
		}
	}

	// Append Shell hook
	hookLine := getShellHookLine(info.ShellType)

	f, err := os.OpenFile(info.ConfigPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Sprintf("Unable to write config file: %v", err)
	}
	defer f.Close()

	if _, err := f.WriteString("\n# WHALE_CODE_START\n" + hookLine + "\n# WHALE_CODE_END\n"); err != nil {
		return fmt.Sprintf("Failed to write config file: %v", err)
	}

	return fmt.Sprintf(`Terminal integration installed:
  Config file: %s
  Backup file: %s
  Content: %s

Please reopen the terminal or run the following command to apply:
  %s`, info.ConfigPath, backupPath, hookLine, info.ProfileCmd)
}

// handleTerminalUninstall uninstalls terminal integration
// Uses WHALE_CODE_START/END marker blocks for robust removal
func handleTerminalUninstall() string {
	info := detectShell()

	if info.ConfigPath == "" || strings.Contains(info.ConfigPath, "N/A") {
		return "Terminal integration: unable to determine config file path"
	}

	data, err := os.ReadFile(info.ConfigPath)
	if err != nil {
		return "Terminal integration: config file does not exist"
	}

	content := string(data)
	if !strings.Contains(content, "whale shell-hook") && !strings.Contains(content, "whale.exe shell-hook") {
		return "Terminal integration not installed"
	}

	// Remove WHALE_CODE_START/END blocks (new format)
	startMarker := "# WHALE_CODE_START"
	endMarker := "# WHALE_CODE_END"
	content = removeMarkerBlocks(content, startMarker, endMarker)

	// Fallback: remove legacy comment-based integration lines
	content = removeLegacyIntegration(content)

	if err := os.WriteFile(info.ConfigPath, []byte(content), 0644); err != nil {
		return fmt.Sprintf("Uninstall failed: %v", err)
	}

	backupPath := info.ConfigPath + ".whale-backup"
	return fmt.Sprintf("Terminal integration uninstalled\n  Removed from %s\n  Backup available: %s", info.ConfigPath, backupPath)
}

// removeMarkerBlocks removes content between startMarker and endMarker (inclusive)
func removeMarkerBlocks(content, startMarker, endMarker string) string {
	lines := strings.Split(content, "\n")
	var result []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == startMarker {
			inBlock = true
			continue
		}
		if trimmed == endMarker {
			inBlock = false
			continue
		}
		if !inBlock {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

// removeLegacyIntegration removes legacy comment-based integration lines
func removeLegacyIntegration(content string) string {
	lines := strings.Split(content, "\n")
	var newLines []string
	skipNext := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "# Whale Code shell integration" {
			skipNext = true
			continue
		}
		if skipNext && (strings.Contains(trimmed, "whale shell-hook") || strings.Contains(trimmed, "whale.exe shell-hook")) {
			skipNext = false
			continue
		}
		skipNext = false
		newLines = append(newLines, line)
	}
	return strings.Join(newLines, "\n")
}

// getShellHookLine returns the Shell hook line
func getShellHookLine(shellType string) string {
	switch shellType {
	case "powershell":
		return `if (Get-Command whale -ErrorAction SilentlyContinue) { whale shell-hook | Invoke-Expression }`
	case "bash", "zsh", "fish":
		return `eval "$(whale shell-hook)"`
	default:
		return `eval "$(whale shell-hook)"`
	}
}
