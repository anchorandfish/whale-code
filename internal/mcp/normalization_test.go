package mcp

import (
	"strings"
	"testing"
)

// TestNormalizeNameForMCP 验证名称归一化（M10）。
// 对标同类 CLI。
func TestNormalizeNameForMCP(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "valid name passes through",
			input: "filesystem",
			want:  "filesystem",
		},
		{
			name:  "valid name with dash and underscore",
			input: "my-server_v1",
			want:  "my-server_v1",
		},
		{
			name:  "valid name with numbers",
			input: "server123",
			want:  "server123",
		},
		{
			name:  "dot replaced with underscore",
			input: "my.server",
			want:  "my_server",
		},
		{
			name:  "space replaced with underscore",
			input: "my server",
			want:  "my_server",
		},
		{
			name:  "slash replaced with underscore",
			input: "my/server",
			want:  "my_server",
		},
		{
			name:  "chinese chars replaced with underscore",
			input: "文件系统",
			want:  "____",
		},
		{
			name:  "long name truncated to 64",
			input: strings.Repeat("a", 100),
			want:  strings.Repeat("a", 64),
		},
		{
			name:  "empty string returns empty",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeNameForMCP(tt.input)
			if got != tt.want {
				t.Errorf("NormalizeNameForMCP(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestBuildMcpToolName 验证全限定工具名构建（M10）。
// 格式：mcp__<server>__<tool>
func TestBuildMcpToolName(t *testing.T) {
	tests := []struct {
		name       string
		serverName string
		toolName   string
		want       string
	}{
		{
			name:       "simple names",
			serverName: "filesystem",
			toolName:   "read_file",
			want:       "mcp__filesystem__read_file",
		},
		{
			name:       "server with dot normalized",
			serverName: "my.server",
			toolName:   "read",
			want:       "mcp__my_server__read",
		},
		{
			name:       "tool with space normalized",
			serverName: "db",
			toolName:   "query data",
			want:       "mcp__db__query_data",
		},
		{
			name:       "both normalized",
			serverName: "my.server",
			toolName:   "query.data",
			want:       "mcp__my_server__query_data",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildMcpToolName(tt.serverName, tt.toolName)
			if got != tt.want {
				t.Errorf("BuildMcpToolName(%q, %q) = %q, want %q",
					tt.serverName, tt.toolName, got, tt.want)
			}
		})
	}
}

// TestParseMcpToolName 验证全限定工具名解析（M10）。
func TestParseMcpToolName(t *testing.T) {
	tests := []struct {
		name       string
		fullName   string
		wantServer string
		wantTool   string
		wantOk     bool
	}{
		{
			name:       "valid full name",
			fullName:   "mcp__filesystem__read_file",
			wantServer: "filesystem",
			wantTool:   "read_file",
			wantOk:     true,
		},
		{
			name:       "tool name with double underscore",
			fullName:   "mcp__db__query__data",
			wantServer: "db",
			wantTool:   "query__data",
			wantOk:     true,
		},
		{
			name:       "missing prefix",
			fullName:   "filesystem__read_file",
			wantServer: "",
			wantTool:   "",
			wantOk:     false,
		},
		{
			name:       "missing separator",
			fullName:   "mcp__filesystem",
			wantServer: "",
			wantTool:   "",
			wantOk:     false,
		},
		{
			name:       "empty server",
			fullName:   "mcp____read_file",
			wantServer: "",
			wantTool:   "",
			wantOk:     false,
		},
		{
			name:       "empty string",
			fullName:   "",
			wantServer: "",
			wantTool:   "",
			wantOk:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotServer, gotTool, gotOk := ParseMcpToolName(tt.fullName)
			if gotServer != tt.wantServer || gotTool != tt.wantTool || gotOk != tt.wantOk {
				t.Errorf("ParseMcpToolName(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.fullName, gotServer, gotTool, gotOk,
					tt.wantServer, tt.wantTool, tt.wantOk)
			}
		})
	}
}

// TestQualifiedToolName 验证 ToolInfo 便捷方法（M10）。
func TestQualifiedToolName(t *testing.T) {
	tool := ToolInfo{Name: "read_file"}
	got := QualifiedToolName("filesystem", tool)
	want := "mcp__filesystem__read_file"
	if got != want {
		t.Errorf("QualifiedToolName = %q, want %q", got, want)
	}
}

// TestBuildAndParseRoundTrip 验证构建-解析往返一致性（M10）。
func TestBuildAndParseRoundTrip(t *testing.T) {
	tests := []struct {
		serverName string
		toolName   string
	}{
		{"filesystem", "read_file"},
		{"db", "query"},
		{"my_server", "tool_v1"},
	}

	for _, tt := range tests {
		full := BuildMcpToolName(tt.serverName, tt.toolName)
		server, tool, ok := ParseMcpToolName(full)
		if !ok {
			t.Errorf("ParseMcpToolName(%q) failed to parse", full)
			continue
		}
		// 注意：parse 返回的是归一化后的名称，如果原始名称已合法则一致
		expectedServer := NormalizeNameForMCP(tt.serverName)
		expectedTool := NormalizeNameForMCP(tt.toolName)
		if server != expectedServer || tool != expectedTool {
			t.Errorf("round-trip mismatch: built %q, parsed (server=%q, tool=%q), want (server=%q, tool=%q)",
				full, server, tool, expectedServer, expectedTool)
		}
	}
}
