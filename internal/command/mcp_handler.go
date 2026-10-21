package command

import (
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/anchorandfish/whale-code/internal/mcp"
	"github.com/anchorandfish/whale-code/internal/util"
)

// MCPManager is the interface for decoupling MCP command handling from the Executor implementation
type MCPManager interface {
	// RemoveMCPClient disconnects and removes the cached MCP client
	RemoveMCPClient(name string) error
	// RefreshMCPTools refreshes the MCP server tool registration
	RefreshMCPTools(workDir string, serverName string) (int, error)
}

// NewMcpCommandHandler creates the /mcp command handler
// M09-05 改造：用 ShellSplitOrFields 替代 strings.Fields，支持引号包裹的命令参数
func NewMcpCommandHandler(mgr MCPManager, workDir string) func(args string) string {
	return func(args string) string {
		parts := ShellSplitOrFields(args)
		if len(parts) == 0 || parts[0] == "help" {
			return formatMcpHelp()
		}

		switch parts[0] {
		case "list":
			return formatMcpList(workDir)
		case "add":
			return handleMcpAdd(workDir, parts[1:])
		case "remove":
			return handleMcpRemove(mgr, workDir, parts[1:])
		case "restart":
			return handleMcpRestart(mgr, workDir, parts[1:])
		default:
			return fmt.Sprintf("Unknown subcommand: %s\n%s", parts[0], formatMcpHelp())
		}
	}
}

// formatMcpHelp returns the /mcp command help info
func formatMcpHelp() string {
	return `MCP server management commands:
  /mcp list                       List all MCP servers and status
  /mcp add <name> <command> [args] Add MCP server
  /mcp remove <name>              Remove MCP server
  /mcp restart <name>             Restart MCP server

Config file path:
  <workDir>/.whale/mcp.json
  ~/.whale/mcp.json`
}

// formatMcpList formats the MCP server list output
func formatMcpList(workDir string) string {
	cfg := loadMcpConfig2(workDir)
	if len(cfg.Servers) == 0 {
		return "MCP servers: (no MCP servers configured)"
	}

	var sb strings.Builder
	sb.WriteString("MCP servers:\n")

	for name, srv := range cfg.Servers {
		transport := srv.Transport
		if transport == "" {
			if srv.Command != "" {
				transport = "stdio"
			} else if srv.URL != "" {
				transport = "sse"
			}
		}

		cmd := srv.Command
		if len(srv.Args) > 0 {
			cmd += " " + strings.Join(srv.Args, " ")
		}
		if cmd == "" && srv.URL != "" {
			cmd = srv.URL
		}

		sb.WriteString(fmt.Sprintf("\n  [%s]\n", name))
		sb.WriteString(fmt.Sprintf("    Transport: %s\n", transport))
		sb.WriteString(fmt.Sprintf("    Command: %s\n", cmd))
	}

	sb.WriteString(fmt.Sprintf("\nTotal %d servers", len(cfg.Servers)))
	return sb.String()
}

// handleMcpAdd handles the MCP server add command
func handleMcpAdd(workDir string, args []string) string {
	if len(args) < 2 {
		return "Usage: /mcp add <name> <command> [args...]"
	}

	name := args[0]
	command := args[1]
	cmdArgs := args[2:]

	// Validate name
	if strings.Contains(name, " ") {
		return fmt.Sprintf("Error: server name cannot contain spaces: %q", name)
	}

	// Validate command is executable (look up in PATH)
	if _, err := exec.LookPath(command); err != nil {
		return fmt.Sprintf("Warning: command %q not found in PATH. The MCP server may fail to start.", command)
	}

	cfg := loadMcpConfig2(workDir)
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]mcp.ServerConfig)
	}

	if _, exists := cfg.Servers[name]; exists {
		return fmt.Sprintf("Error: MCP server %q already exists", name)
	}

	cfg.Servers[name] = mcp.ServerConfig{
		Command: command,
		Args:    cmdArgs,
	}

	if err := saveMcpConfig(workDir, cfg); err != nil {
		return fmt.Sprintf("Failed to add server: %v", err)
	}

	configPath := getMcpConfigPath(workDir)
	// G2 决策：维持「仅 exec.LookPath」的轻量校验（来源可信度风险由
	// G1 的沙箱隔离兜底），但补一条外部信任域提示，避免用户误以为命令来源已审计。
	return fmt.Sprintf("Added MCP server: %s\n  Command: %s %s\n  File: %s\n  Note: MCP server is an external trust domain — the command is resolved via PATH\n  (not audited) and will be launched inside the sandbox. Review the source of untrusted servers.",
		name, command, strings.Join(cmdArgs, " "), configPath)
}

// handleMcpRemove handles the MCP server remove command
func handleMcpRemove(mgr MCPManager, workDir string, args []string) string {
	if len(args) < 1 {
		return "Usage: /mcp remove <name>"
	}

	name := args[0]

	cfg := loadMcpConfig2(workDir)
	if _, exists := cfg.Servers[name]; !exists {
		return fmt.Sprintf("Error: MCP server %q does not exist", name)
	}

	delete(cfg.Servers, name)

	if err := saveMcpConfig(workDir, cfg); err != nil {
		return fmt.Sprintf("Failed to remove server: %v", err)
	}

	// Disconnect client
	if mgr != nil {
		_ = mgr.RemoveMCPClient(name)
	}

	return fmt.Sprintf("Removed MCP server: %s\n  Disconnected and cleaned up tool registration\n  Config updated", name)
}

// handleMcpRestart handles the MCP server restart command
func handleMcpRestart(mgr MCPManager, workDir string, args []string) string {
	if len(args) < 1 {
		return "Usage: /mcp restart <name>"
	}

	name := args[0]

	cfg := loadMcpConfig2(workDir)
	if _, exists := cfg.Servers[name]; !exists {
		return fmt.Sprintf("Error: MCP server %q does not exist, please use /mcp add first", name)
	}

	var sb strings.Builder

	// Disconnect old connection
	removeErr := ""
	if mgr != nil {
		if err := mgr.RemoveMCPClient(name); err != nil {
			removeErr = err.Error()
			sb.WriteString(fmt.Sprintf("  ✗ Failed to disconnect old connection: %s\n", err))
		} else {
			sb.WriteString("  ✓ Old connection disconnected\n")
		}
	} else {
		sb.WriteString("  - Manager not initialized, skipping disconnect\n")
	}

	// Reconnect and register tools
	if mgr != nil && removeErr == "" {
		toolCount, err := mgr.RefreshMCPTools(workDir, name)
		if err != nil {
			sb.WriteString(fmt.Sprintf("  ✗ Failed to establish new connection: %v\nPlease check if the server command is correct", err))
			return fmt.Sprintf("MCP server %s restart failed:\n%s", name, sb.String())
		}
		sb.WriteString(fmt.Sprintf("  ✓ New connection established\n  ✓ Registered %d tools", toolCount))
	} else {
		sb.WriteString("  - Please restart Whale Code to reconnect")
	}

	return fmt.Sprintf("Restarted MCP server: %s\n%s", name, sb.String())
}

// loadMcpConfig2 loads MCP config from multiple sources
func loadMcpConfig2(workDir string) *mcp.Config {
	var dirs []string

	if workDir != "" {
		dirs = append(dirs, filepath.Join(workDir, ".whale"))
	}
	homeDir, err := os.UserHomeDir()
	if err == nil && homeDir != "" {
		dirs = append(dirs, whalehome.Dir())
	}

	cfg := mcp.LoadConfigFromDirs(dirs...)
	return cfg
}

// getMcpConfigPath returns the MCP config file path (prefers workDir)
func getMcpConfigPath(workDir string) string {
	if workDir != "" {
		path := filepath.Join(workDir, ".whale", "mcp.json")
		return path
	}
	// 收口：状态目录唯一走 whalehome.Dir()。
	return filepath.Join(whalehome.Dir(), "mcp.json")
}

// saveMcpConfig saves MCP config to file atomically
func saveMcpConfig(workDir string, cfg *mcp.Config) error {
	configPath := getMcpConfigPath(workDir)
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize config: %w", err)
	}

	if err := util.AtomicWriteFile(configPath, data, 0o600); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}
