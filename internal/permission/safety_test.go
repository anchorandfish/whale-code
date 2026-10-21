package permission

import (
	"os"
	"path/filepath"
	"testing"
)

// homePath builds an absolute path under the user's home directory
func homePath(elem ...string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(elem...)
	}
	return filepath.Join(append([]string{home}, elem...)...)
}

func TestCheckPathSafety(t *testing.T) {
	tests := []struct {
		name               string
		filePath           string
		workDir            string
		wantProtected      bool
		wantContainsReason string
	}{
		// .git 路径
		{".git/config", ".git/config", "", true, ".git"},
		{".git/HEAD", ".git/HEAD", "", true, ".git"},
		{"src/.git/HEAD", "src/.git/HEAD", "", true, ".git"},
		{".gitignore", ".gitignore", "", false, ""},

		// .whale 路径
		{".whale/settings.json", ".whale/settings.json", "", true, ".whale"},
		{".whale/hooks.json", ".whale/hooks.json", "", true, ".whale"},

		// .vscode 路径
		{".vscode/settings.json", ".vscode/settings.json", "", true, ".vscode"},

		// .env 路径
		{".env", ".env", "", true, ".env"},
		{".env.local", ".env.local", "", true, ".env.local"},
		{".env.production", ".env.production", "", true, ".env.production"},
		{".env.development", ".env.development", "", true, ".env.development"},
		{".env.staging", ".env.staging", "", true, ".env.staging"},

		// 密钥文件
		{"id_rsa", "id_rsa", "", true, "id_rsa"},
		{"id_ed25519", "id_ed25519", "", true, "id_ed25519"},
		{"id_ecdsa", "id_ecdsa", "", true, "id_ecdsa"},
		{"credentials.json", "credentials.json", "", true, "credentials"},
		{"credentials", "credentials", "", true, "credentials"},
		{"secrets.yaml", "secrets.yaml", "", true, "secrets"},
		{"config/secrets.yml", "config/secrets.yml", "", true, "secrets"},

		// npm/pip 配置
		{".npmrc", ".npmrc", "", true, ".npmrc"},
		{".pypirc", ".pypirc", "", true, ".pypirc"},

		// 正常文件
		{"src/main.go", "src/main.go", "", false, ""},
		{"test.txt", "test.txt", "", false, ""},
		{"node_modules/package.json", "node_modules/package.json", "", false, ""},

		// path traversal
		{"../etc/passwd", "../etc/passwd", "", true, "path traversal"},

		// 系统路径
		{homePath(".ssh", "config"), homePath(".ssh", "config"), "", true, "system path"},
		{homePath(".gitconfig"), homePath(".gitconfig"), "", true, "system path"},

		// 空路径
		{"", "", "", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CheckPathSafety(tt.filePath, tt.workDir)
			if tt.wantProtected {
				if result == nil {
					t.Errorf("expected protected path, got nil")
					return
				}
				if !result.IsProtected {
					t.Errorf("IsProtected = false, want true")
				}
				if tt.wantContainsReason != "" && !stringContains(result.Reason, tt.wantContainsReason) {
					t.Errorf("Reason %q does not contain %q", result.Reason, tt.wantContainsReason)
				}
				if result.ClassifierApprovable {
					t.Errorf("ClassifierApprovable should be false for safety checks")
				}
			} else {
				if result != nil && result.IsProtected {
					t.Errorf("expected not protected, got: %s", result.Reason)
				}
			}
		})
	}
}

func TestCheckPathSafety_WithWorkDir(t *testing.T) {
	result := CheckPathSafety(".git/config", "/home/user/project")
	if result == nil {
		t.Fatal("expected protected path")
	}
	if !result.IsProtected {
		t.Error("IsProtected should be true for .git/config within workDir")
	}
}

// TestIsWriteTool ★ 2026-09-20 批 C：期望值随「唯一声明」收敛而**变更**（原值留档于下）。
//
// 原表（收敛前 permission 侧名单）期望 `bash`=true / `PowerShell`=true，
// 而 `tools/pipeline` 侧同一语义期望 `bash`=false ⇒ 两份对 bash 的答案**相反**。
// 收敛到 `permission.WriteToolSet`（目标受限写类工具，**不含**任意命令类）后：
//   - `bash` / `PowerShell`：true → **false**（它们无法由入参界定可达面；删除已证中性，
//     见 TestWriteToolSet_RemovedMembersAreInert）
//   - `LspRename` / `export_session` / `git_*`：此前 permission 侧漏登记 → 现补上
func TestIsWriteTool(t *testing.T) {
	tests := []struct {
		toolName string
		expected bool
	}{
		{"write_file", true},
		{"edit_file", true},
		{"NotebookEdit", true},
		{"LspRename", true},      // ★ 收敛后新纳入（原 permission 侧漏登记）
		{"export_session", true}, // ★ 同上
		{"bash", false},          // ★ 原为 true（任意命令类，已按收敛移出）
		{"PowerShell", false},    // ★ 原为 true
		{"read_file", false},
		{"grep_search", false},
		{"TodoWrite", false},
	}

	for _, tt := range tests {
		t.Run(tt.toolName, func(t *testing.T) {
			got := IsWriteTool(tt.toolName)
			if got != tt.expected {
				t.Errorf("IsWriteTool(%q) = %v, want %v", tt.toolName, got, tt.expected)
			}
		})
	}
}

func TestExtractFilePath(t *testing.T) {
	// ExtractFilePath 仅提取 path-like 字段（file_path / path），
	// 不提取 "command" 字段——命令不应被 SafetyCheckStage 当作文件路径校验。
	// 修复：原实现通过 extractToolCommand 误将 "command" 当作路径返回，
	// 导致 Bash/PowerShell 命令（如 "echo hello"）触发 "path escapes work directory" 误报。
	tests := []struct {
		input    string
		expected string
	}{
		{`{"file_path":"src/main.go"}`, "src/main.go"},
		{`{"path":"test.txt"}`, "test.txt"},
		{`{"command":"rm -rf /"}`, ""}, // command 不是文件路径，应返回空
		{"not json", ""},               // 无效 JSON 返回空
	}

	for _, tt := range tests {
		name := tt.expected
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			got := ExtractFilePath(tt.input)
			if got != tt.expected {
				t.Errorf("ExtractFilePath(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// stringContains checks if s contains substr (case-insensitive)
func stringContains(s, substr string) bool {
	return len(s) >= len(substr) &&
		simpleContains(s, substr)
}

func simpleContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestCheckPathSafety_NewFileInSymlinkedWorkDir(t *testing.T) {
	realDir := t.TempDir()
	linkRoot := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(realDir, linkRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if result := CheckPathSafety("network-marker.txt", linkRoot); result != nil && result.IsProtected {
		t.Fatalf("new file under a symlinked workdir should not be falsely blocked: %s", result.Reason)
	}
}
