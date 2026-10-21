package permission

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPolicyAllowedTools tests allowedTools auto-approval
func TestPolicyAllowedTools(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash", "write_file", "edit_file"})

	t.Run("allowed tool auto-approved", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"ls"}`)
		if !allowed {
			t.Error("bash in allowedTools should be allowed")
		}
		if needConfirm {
			t.Error("bash in allowedTools should not need confirmation")
		}
	})

	t.Run("allowed write_file auto-approved", func(t *testing.T) {
		allowed, needConfirm := policy.Check("write_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("write_file in allowedTools should be allowed")
		}
		if needConfirm {
			t.Error("write_file in allowedTools should not need confirmation")
		}
	})

	t.Run("non-allowed tool still needs confirmation", func(t *testing.T) {
		allowed, needConfirm := policy.Check("NotebookEdit", `{"path":"test.ipynb"}`)
		if !allowed {
			t.Error("NotebookEdit should be allowed in workspace-write mode")
		}
		if !needConfirm {
			t.Error("NotebookEdit not in allowedTools should need confirmation")
		}
	})

	t.Run("read-only tool auto-approved even without allowedTools", func(t *testing.T) {
		allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("read_file should always be allowed")
		}
		if needConfirm {
			t.Error("read_file should not need confirmation")
		}
	})
}

// TestPolicyAllowedToolsInReadOnly tests allowedTools in read-only mode
func TestPolicyAllowedToolsInReadOnly(t *testing.T) {
	policy := NewPolicy(LevelReadOnly)
	policy.SetAllowedTools([]string{"bash", "write_file"})

	t.Run("write tool in allowedTools but read-only mode blocks it", func(t *testing.T) {
		allowed, _ := policy.Check("write_file", `{"path":"test.go"}`)
		if allowed {
			t.Error("write_file should be blocked in read-only mode even if in allowedTools")
		}
	})
}

// TestPolicyAllowedToolsInDangerMode tests allowedTools in danger-full-access mode
func TestPolicyAllowedToolsInDangerMode(t *testing.T) {
	policy := NewPolicy(LevelDangerFullAccess)
	policy.SetAllowedTools([]string{"bash"})

	t.Run("danger mode overrides allowedTools", func(t *testing.T) {
		allowed, needConfirm := policy.Check("write_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("write_file should be allowed in danger-full-access mode")
		}
		if needConfirm {
			t.Error("danger-full-access mode should not need confirmation")
		}
	})
}

// TestPolicyAddAllowedTool tests adding individual allowed tools
func TestPolicyAddAllowedTool(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)

	policy.AddAllowedTool("bash")
	policy.AddAllowedTool("write_file")

	allowed, needConfirm := policy.Check("bash", `{"command":"ls"}`)
	if !allowed || needConfirm {
		t.Error("bash should be auto-approved after AddAllowedTool")
	}

	allowed, needConfirm = policy.Check("edit_file", `{"path":"test.go"}`)
	if !allowed || !needConfirm {
		t.Error("edit_file should still need confirmation")
	}
}

// TestPolicyRemoveAllowedTool tests removing allowed tools
func TestPolicyRemoveAllowedTool(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash", "write_file"})

	policy.RemoveAllowedTool("bash")

	allowed, needConfirm := policy.Check("bash", `{"command":"ls"}`)
	if !allowed || !needConfirm {
		t.Error("bash should need confirmation after RemoveAllowedTool")
	}

	allowed, needConfirm = policy.Check("write_file", `{"path":"test.go"}`)
	if !allowed || needConfirm {
		t.Error("write_file should still be auto-approved")
	}
}

// TestPolicyGetAllowedTools tests getting allowed tools list
func TestPolicyGetAllowedTools(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash", "write_file"})

	tools := policy.GetAllowedTools()
	if len(tools) != 2 {
		t.Errorf("GetAllowedTools returned %d tools, want 2", len(tools))
	}
}

// TestPolicyIsAutoApproved tests IsAutoApproved check
func TestPolicyIsAutoApproved(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash"})

	if !policy.IsAutoApproved("bash") {
		t.Error("bash should be auto-approved")
	}
	if policy.IsAutoApproved("write_file") {
		t.Error("write_file should not be auto-approved")
	}
}

// TestMemoryPersistence tests persistent permission memory
func TestMemoryPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "permissions.json")

	t.Run("save and load memory", func(t *testing.T) {
		mem := NewMemory(256)
		mem.Set("bash", "ls -la", true)
		mem.Set("write_file", "test.go", true)
		mem.Set("bash", "rm -rf /", false)

		if err := mem.SaveToFile(storePath); err != nil {
			t.Fatalf("SaveToFile failed: %v", err)
		}

		loaded, err := LoadMemoryFromFile(storePath, 256)
		if err != nil {
			t.Fatalf("LoadMemoryFromFile failed: %v", err)
		}

		if !loaded.Has("bash", "ls -la") {
			t.Error("loaded memory should have bash ls -la")
		}
		if !loaded.Has("write_file", "test.go") {
			t.Error("loaded memory should have write_file test.go")
		}
		if loaded.Has("bash", "rm -rf /") {
			t.Error("rejected memory should not be loaded as allowed")
		}
	})

	t.Run("load non-existent file", func(t *testing.T) {
		_, err := LoadMemoryFromFile("/nonexistent/permissions.json", 256)
		if err == nil {
			t.Error("should return error for non-existent file")
		}
	})

	t.Run("save creates directory", func(t *testing.T) {
		nestedPath := filepath.Join(tmpDir, "nested", "dir", "perms.json")
		mem := NewMemory(256)
		mem.Set("bash", "ls", true)

		if err := mem.SaveToFile(nestedPath); err != nil {
			t.Fatalf("SaveToFile with nested dir failed: %v", err)
		}

		if _, err := os.Stat(nestedPath); os.IsNotExist(err) {
			t.Error("file should exist after save")
		}
	})
}

// TestPolicyWithPersistentMemory tests policy with persistent memory
func TestPolicyWithPersistentMemory(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "permissions.json")

	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetMemoryStorePath(storePath)

	policy.Remember("bash", "ls -la", true)
	policy.Remember("write_file", "test.go", true)

	if err := policy.SaveMemory(); err != nil {
		t.Fatalf("SaveMemory failed: %v", err)
	}

	policy2 := NewPolicy(LevelWorkspaceWrite)
	policy2.SetMemoryStorePath(storePath)
	if err := policy2.LoadMemory(); err != nil {
		t.Fatalf("LoadMemory failed: %v", err)
	}

	allowed, needConfirm := policy2.Check("bash", "ls -la")
	if !allowed || needConfirm {
		t.Error("loaded memory should auto-approve previously allowed tool")
	}
}

// TestAnalyzeBashCommandExtended tests more bash command patterns
func TestAnalyzeBashCommandExtended(t *testing.T) {
	tests := []struct {
		cmd  string
		want BashRisk
	}{
		{"Get-ChildItem", BashRiskReadOnly},
		{"Remove-Item file.txt", BashRiskDestructive},
		{"npm install", BashRiskDestructive},
		{"pip install package", BashRiskDestructive},
		{"go build ./...", BashRiskDestructive},
		{"go test ./...", BashRiskDestructive},
		{"dotnet build", BashRiskDestructive},
		{"cargo build", BashRiskDestructive},
		{"git push --force origin main", BashRiskDangerous},
		{"chmod 777 /etc/passwd", BashRiskDestructive},
	}

	for _, tt := range tests {
		if got := AnalyzeBashCommand(tt.cmd); got != tt.want {
			t.Errorf("AnalyzeBashCommand(%q) = %s, want %s", tt.cmd, got, tt.want)
		}
	}
}

// TestPolicyCheckWithBashRisk tests policy check integrating bash risk analysis
func TestPolicyCheckWithBashRisk(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)

	t.Run("dangerous bash command needs confirm even in allowedTools", func(t *testing.T) {
		policy.SetAllowedTools([]string{"bash"})

		allowed, needConfirm := policy.Check("bash", `{"command":"rm -rf /"}`)
		if !allowed {
			t.Error("bash in allowedTools should be allowed")
		}
		if !needConfirm {
			t.Error("dangerous bash command should still need confirmation even in allowedTools")
		}
	})

	t.Run("safe bash command auto-approved in allowedTools", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"ls -la"}`)
		if !allowed {
			t.Error("bash in allowedTools should be allowed")
		}
		if needConfirm {
			t.Error("safe bash command in allowedTools should not need confirmation")
		}
	})
}

// TestParseLevel 测试从字符串解析权限级别
// 验证 "read-only"→LevelReadOnly, "workspace-write"→LevelWorkspaceWrite,
// "danger-full-access"→LevelDangerFullAccess, unknown→LevelWorkspaceWrite
func TestParseLevel(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"read-only", LevelReadOnly},
		{"readonly", LevelReadOnly},
		{"workspace-write", LevelWorkspaceWrite},
		{"workspace_write", LevelWorkspaceWrite},
		{"danger-full-access", LevelDangerFullAccess},
		{"danger_full_access", LevelDangerFullAccess},
		{"unknown-mode", LevelWorkspaceWrite},
		{"", LevelWorkspaceWrite},
	}

	for _, tt := range tests {
		got := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %d, expected %d", tt.input, got, tt.want)
		}
	}
}

// TestPolicy_Check_DangerFullAccess 测试 danger-full-access 模式下的权限判定。
//
// 阶段 2 收模式后：danger 全放行且免问，但**危险命令保留二次确认护栏**
// （承接原 dont-ask 档语义，使「dont-ask 并入 danger」不构成扩权）。
func TestPolicy_Check_DangerFullAccess(t *testing.T) {
	policy := NewPolicy(LevelDangerFullAccess)

	// 写工具应允许且无需确认
	allowed, needConfirm := policy.Check("write_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("danger-full-access 模式下 write_file 应允许")
	}
	if needConfirm {
		t.Error("danger-full-access 模式下 write_file 无需确认")
	}

	// 读工具也应允许且无需确认
	allowed, needConfirm = policy.Check("read_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("danger-full-access 模式下 read_file 应允许")
	}
	if needConfirm {
		t.Error("danger-full-access 模式下 read_file 无需确认")
	}

	// 安全 bash 允许且无需确认
	allowed, needConfirm = policy.Check("bash", `{"command":"ls -la"}`)
	if !allowed || needConfirm {
		t.Errorf("danger-full-access 模式下安全 bash: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}

	// 危险 bash：允许但需二次确认（护栏不变量）
	allowed, needConfirm = policy.Check("bash", `{"command":"rm -rf /"}`)
	if !allowed {
		t.Error("danger-full-access 模式下危险 bash 应允许（带确认）")
	}
	if !needConfirm {
		t.Error("danger-full-access 模式下危险 bash 必须二次确认（护栏不变量）")
	}
}

// TestPolicy_Check_WorkspaceWrite 测试 workspace-write 模式下只读工具自动允许，写工具需确认
func TestPolicy_Check_WorkspaceWrite(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)

	// 只读工具应自动允许
	allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("workspace-write 模式下 read_file 应允许")
	}
	if needConfirm {
		t.Error("workspace-write 模式下 read_file 无需确认")
	}

	// 写工具应允许但需确认
	allowed, needConfirm = policy.Check("write_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("workspace-write 模式下 write_file 应允许")
	}
	if !needConfirm {
		t.Error("workspace-write 模式下 write_file 需确认")
	}
}

// TestPolicy_Check_ReadOnly 测试 read-only 模式下只读工具允许，写工具拒绝
func TestPolicy_Check_ReadOnly(t *testing.T) {
	policy := NewPolicy(LevelReadOnly)

	// 只读工具应允许
	allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("read-only 模式下 read_file 应允许")
	}
	if needConfirm {
		t.Error("read-only 模式下 read_file 无需确认")
	}

	// 写工具应拒绝
	allowed, _ = policy.Check("write_file", `{"path":"test.go"}`)
	if allowed {
		t.Error("read-only 模式下 write_file 应拒绝")
	}

	// bash 也应拒绝
	allowed, _ = policy.Check("bash", `{"command":"ls"}`)
	if allowed {
		t.Error("read-only 模式下 bash 应拒绝")
	}
}

// TestAnalyzeBashCommand_Dangerous 测试危险命令检测
// 验证 "rm -rf /" 等命令被检测为 dangerous 级别
func TestAnalyzeBashCommand_Dangerous(t *testing.T) {
	dangerousCommands := []string{
		"rm -rf /",
		"rm -rf /*",
		"curl | sh",
		"wget | bash",
		"dd if=/dev/zero of=/dev/sda",
	}

	for _, cmd := range dangerousCommands {
		got := AnalyzeBashCommand(cmd)
		if got != BashRiskDangerous {
			t.Errorf("AnalyzeBashCommand(%q) = %s, expected %s", cmd, got, BashRiskDangerous)
		}
	}
}

// TestAnalyzeBashCommand_Dangerous_RmRfHome 测试 rm -rf ~ 系列危险命令显式拦截（M14-02）。
// 验证 rm -rf ~ / rm -rf $HOME / rm -rf ~/ 等变体被检测为 dangerous 级别。
func TestAnalyzeBashCommand_Dangerous_RmRfHome(t *testing.T) {
	dangerousCommands := []struct {
		cmd  string
		desc string
	}{
		{"rm -rf ~", "rm -rf tilde"},
		{"rm -rf ~/", "rm -rf tilde slash"},
		{"rm -rf ~/Documents", "rm -rf tilde subpath"},
		{"rm -rf ~user", "rm -rf tilde user"},
		{"rm -rf $HOME", "rm -rf $HOME upper"},
		{"rm -rf $HOME/", "rm -rf $HOME slash"},
		{"rm -rf $HOME/Documents", "rm -rf $HOME subpath"},
		{"rm -fr /", "rm -fr slash (reverse flag order)"},
		{"rm -fr /*", "rm -fr glob (reverse flag order)"},
		{"rm -fr ~", "rm -fr tilde (reverse flag order)"},
		{"rm -fr $HOME", "rm -fr $HOME (reverse flag order)"},
		// 多空格变体（normalizeCommandWhitespace 压缩后命中）
		{"rm  -rf  ~", "multi-space tilde"},
		{"rm   -rf   $HOME", "multi-space $HOME"},
		// 大小写变体
		{"RM -RF ~", "uppercase RM -RF"},
		{"Rm -rF $HoMe", "mixed case"},
	}

	for _, tc := range dangerousCommands {
		t.Run(tc.desc, func(t *testing.T) {
			got := AnalyzeBashCommand(tc.cmd)
			if got != BashRiskDangerous {
				t.Errorf("AnalyzeBashCommand(%q) = %s, expected %s (%s)",
					tc.cmd, got, BashRiskDangerous, tc.desc)
			}
		})
	}
}

// TestAnalyzeBashCommand_Dangerous_RmRfHome_FalsePositives 验证合法 rm 命令不被误报为 dangerous（M14-02 误报测试）。
// 合法的 rm -rf <相对路径> 应归为 destructive（Ask），而非 dangerous（Deny）。
// 注：rm -rf /<绝对路径> 已被现有 "rm -rf /" 规则覆盖（不在本次改造范围），故误报测试仅覆盖相对路径。
func TestAnalyzeBashCommand_Dangerous_RmRfHome_FalsePositives(t *testing.T) {
	// 这些命令应归为 Destructive（破坏性，需确认），而非 Dangerous（直接拒绝）
	safeFromDangerous := []struct {
		cmd  string
		desc string
	}{
		{"rm -rf ./build", "rm -rf relative build dir"},
		{"rm -rf build/node_modules", "rm -rf relative subpath"},
		{"rm -rf ../sibling/dir", "rm -rf parent relative path"},
		{"rm file.txt", "rm single file"},
	}

	for _, tc := range safeFromDangerous {
		t.Run(tc.desc, func(t *testing.T) {
			got := AnalyzeBashCommand(tc.cmd)
			if got == BashRiskDangerous {
				t.Errorf("AnalyzeBashCommand(%q) = %s, 不应为 Dangerous（误报，%s）",
					tc.cmd, got, tc.desc)
			}
		})
	}
}

// TestAnalyzeBashCommand_Destructive 测试破坏性命令检测
// 验证 "git push --force" 等命令被检测为 destructive 级别
func TestAnalyzeBashCommand_Destructive(t *testing.T) {
	destructiveCommands := []string{
		"git push --force",
		"git push -f origin main",
		"rm file.txt",
		"chmod 755 script.sh",
		"docker rm container",
	}

	for _, cmd := range destructiveCommands {
		got := AnalyzeBashCommand(cmd)
		if got != BashRiskDestructive && got != BashRiskDangerous {
			t.Errorf("AnalyzeBashCommand(%q) = %s, expected destructive 或 dangerous", cmd, got)
		}
	}
}

// TestAnalyzeBashCommand_ReadOnly 测试只读命令检测
// 验证 "ls -la", "cat file.txt" 等命令被检测为 read-only 级别
func TestAnalyzeBashCommand_ReadOnly(t *testing.T) {
	readOnlyCommands := []string{
		"ls -la",
		"cat file.txt",
		"grep pattern file",
		"git status",
		"git log",
		"pwd",
		"which go",
	}

	for _, cmd := range readOnlyCommands {
		got := AnalyzeBashCommand(cmd)
		if got != BashRiskReadOnly {
			t.Errorf("AnalyzeBashCommand(%q) = %s, expected %s", cmd, got, BashRiskReadOnly)
		}
	}
}

// TestAnalyzeBashCommand_PowerShell 测试 PowerShell 命令风险分析
// 验证 "Remove-Item" 被检测为 dangerous/destructive，"Get-Content" 被检测为 read-only
func TestAnalyzeBashCommand_PowerShell(t *testing.T) {
	// Remove-Item 应为 destructive
	got := AnalyzeBashCommand("Remove-Item file.txt")
	if got == BashRiskReadOnly {
		t.Errorf("Remove-Item 应为 destructive 或 dangerous，actual为 %s", got)
	}

	// Get-Content 应为 read-only
	got = AnalyzeBashCommand("Get-Content file.txt")
	if got != BashRiskReadOnly {
		t.Errorf("Get-Content 应为 read-only，actual为 %s", got)
	}
}

// TestPermissionMemory 测试 LRU + SHA256 fingerprint记忆
// 验证权限记忆的设置、查询和 LRU 淘汰机制
func TestPermissionMemory(t *testing.T) {
	mem := NewMemory(3) // 最大 3 条

	// 设置记忆
	mem.Set("bash", "ls -la", true)
	mem.Set("bash", "cat file.txt", true)
	mem.Set("write_file", "test.go", true)

	// 查询已设置的记忆
	if !mem.Has("bash", "ls -la") {
		t.Error("应找到 bash ls -la 的记忆")
	}
	if !mem.Has("write_file", "test.go") {
		t.Error("应找到 write_file test.go 的记忆")
	}

	// 查询未设置的记忆
	if mem.Has("bash", "rm -rf /") {
		t.Error("不应找到未设置的记忆")
	}

	// 设置第 4 条，应淘汰最久未访问的条目（LRU）
	// 由于之前 Has("bash", "ls -la") 访问了 key1，LRU 顺序变为:
	// key2("bash cat file.txt") 最旧 -> key3 -> key1 最新
	// 所以 key2 应被淘汰
	mem.Set("bash", "git status", true)
	if mem.Has("bash", "cat file.txt") {
		t.Error("LRU 淘汰后不应找到最久未访问的记忆")
	}
	if !mem.Has("bash", "ls -la") {
		t.Error("LRU 淘汰后被访问过的记忆应保留")
	}
	if !mem.Has("bash", "git status") {
		t.Error("应找到新设置的记忆")
	}
}

// TestPermissionMemory_LRU 测试 LRU 淘汰行为
// 验证 Has() 访问会更新最近使用顺序，使被访问的条目不被淘汰
func TestPermissionMemory_LRU(t *testing.T) {
	mem := NewMemory(3)

	// 设置 3 条记忆
	mem.Set("bash", "ls -la", true)        // key1
	mem.Set("bash", "cat file.txt", true)  // key2
	mem.Set("write_file", "test.go", true) // key3

	// 访问 key1，使其变为最近使用
	mem.Has("bash", "ls -la")

	// 此时 LRU 顺序: key2(最旧) -> key3 -> key1(最新)
	// 添加新条目应淘汰 key2
	mem.Set("bash", "git status", true)

	// key1 应因被访问而保留
	if !mem.Has("bash", "ls -la") {
		t.Error("LRU: 被访问的条目应保留，不应被淘汰")
	}
	// key2 应被淘汰
	if mem.Has("bash", "cat file.txt") {
		t.Error("LRU: 最久未访问的条目应被淘汰")
	}
	// key3 应保留
	if !mem.Has("write_file", "test.go") {
		t.Error("LRU: 未被淘汰的条目应保留")
	}
}

// TestPermissionMemory_SaveOriginalValues test persistence保存原始值
// 验证 SaveToFile 保存的是原始 toolName 和 input，而非 SHA256 fingerprint
func TestPermissionMemory_SaveOriginalValues(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "original_values.json")

	mem := NewMemory(256)
	mem.Set("bash", "ls -la", true)
	mem.Set("write_file", "main.go", true)

	if err := mem.SaveToFile(storePath); err != nil {
		t.Fatalf("SaveToFile 失败: %v", err)
	}

	// 读取文件内容验证保存的是原始值
	data, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("读取文件失败: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, `"tool_name": "bash"`) {
		t.Error("保存的文件should contain原始 toolName 'bash'，而非 SHA256 fingerprint")
	}
	if !strings.Contains(content, `"input": "ls -la"`) {
		t.Error("保存的文件should contain原始 input 'ls -la'，而非 SHA256 fingerprint")
	}
	if !strings.Contains(content, `"tool_name": "write_file"`) {
		t.Error("保存的文件should contain原始 toolName 'write_file'，而非 SHA256 fingerprint")
	}
	if !strings.Contains(content, `"input": "main.go"`) {
		t.Error("保存的文件should contain原始 input 'main.go'，而非 SHA256 fingerprint")
	}
}

// TestPermissionMemory_Persistence 测试记忆持久化
// 验证 Save() 后 Load() 数据一致
func TestPermissionMemory_Persistence(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "perm_mem.json")

	// 创建并保存记忆
	mem := NewMemory(256)
	mem.Set("bash", "ls -la", true)
	mem.Set("write_file", "main.go", true)
	mem.Set("bash", "rm -rf /", false) // rejected的不应被保存为 allowed

	if err := mem.SaveToFile(storePath); err != nil {
		t.Fatalf("SaveToFile 失败: %v", err)
	}

	// 加载记忆
	loaded, err := LoadMemoryFromFile(storePath, 256)
	if err != nil {
		t.Fatalf("LoadMemoryFromFile 失败: %v", err)
	}

	// 验证允许的记忆被加载
	if !loaded.Has("bash", "ls -la") {
		t.Error("加载后应找到 bash ls -la")
	}
	if !loaded.Has("write_file", "main.go") {
		t.Error("加载后应找到 write_file main.go")
	}

	// 验证rejected的记忆不被加载为 allowed
	if loaded.Has("bash", "rm -rf /") {
		t.Error("rejected的记忆不应被加载为 allowed")
	}
}

// TestAllowedTools 测试 AddAllowed 使工具自动批准
// 验证 AddAllowedTool("read_file") 后 read_file 自动批准
func TestAllowedTools(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)

	// 添加 read_file 到自动批准列表
	policy.AddAllowedTool("read_file")

	// read_file 应自动批准
	allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("read_file 应允许")
	}
	if needConfirm {
		t.Error("AddAllowedTool 后 read_file 应无需确认")
	}

	// 未添加的工具仍需确认
	allowed, needConfirm = policy.Check("write_file", `{"path":"test.go"}`)
	if !allowed {
		t.Error("write_file 在 workspace-write 模式下应允许")
	}
	if !needConfirm {
		t.Error("未添加到 allowedTools 的 write_file 应需确认")
	}

	// 验证 IsAutoApproved
	if !policy.IsAutoApproved("read_file") {
		t.Error("read_file 应在自动批准列表中")
	}
	if policy.IsAutoApproved("write_file") {
		t.Error("write_file 不应在自动批准列表中")
	}
}

// ============================================================================
// Section 18: 性能benchmark
// ============================================================================

// BenchmarkPermissionCheck benchmark Policy.Check 在 1000 次迭代下的性能
func BenchmarkPermissionCheck(b *testing.B) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetAllowedTools([]string{"bash", "read_file", "write_file"})

	// 预设一些记忆
	policy.Remember("bash", "ls -la", true)
	policy.Remember("write_file", "main.go", true)

	tools := []struct {
		name  string
		input string
	}{
		{"read_file", `{"path":"test.go"}`},
		{"write_file", `{"path":"test.go"}`},
		{"bash", `{"command":"ls -la"}`},
		{"edit_file", `{"path":"test.go"}`},
		{"grep_search", `{"pattern":"test"}`},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tool := tools[i%len(tools)]
		policy.Check(tool.name, tool.input)
	}
}

// ============================================================================
// 新增权限模式测试
// ============================================================================

// TestParseLevel_AcceptEdits 测试解析 accept-edits 权限级别（阶段 2 收模式后）
// accept-edits 已收敛到 workspace-write，其「编辑免问」语义改由默认规则承担。
func TestParseLevel_AcceptEdits(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"accept-edits", LevelWorkspaceWrite},
		{"accept_edits", LevelWorkspaceWrite},
		{"acceptEdits", LevelWorkspaceWrite},
	}
	for _, tt := range tests {
		got := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %d, expected %d", tt.input, got, tt.want)
		}
	}
}

// TestParseLevel_DontAsk 测试解析 dont-ask 权限级别（阶段 2 收模式后）
// dont-ask 已并入 danger-full-access。
func TestParseLevel_DontAsk(t *testing.T) {
	tests := []struct {
		input string
		want  Level
	}{
		{"dont-ask", LevelDangerFullAccess},
		{"dont_ask", LevelDangerFullAccess},
		{"dontAsk", LevelDangerFullAccess},
	}
	for _, tt := range tests {
		got := ParseLevel(tt.input)
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %d, expected %d", tt.input, got, tt.want)
		}
	}
}

// TestParseLevel_Plan 测试解析 plan 权限级别（阶段 2 收模式后）
// plan 已收敛到 read-only。
func TestParseLevel_Plan(t *testing.T) {
	got := ParseLevel("plan")
	if got != LevelReadOnly {
		t.Errorf("ParseLevel(\"plan\") = %d, expected %d (read-only)", got, LevelReadOnly)
	}
}

// TestPolicyCheck_PlanMode 测试计划模式下的权限检查
// 验证 plan 迁移后等价于 read-only：写操作被拒
func TestPolicyCheck_PlanMode(t *testing.T) {
	policy := NewPolicy(ParseLevel("plan"))

	t.Run("只读工具允许", func(t *testing.T) {
		allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("plan 模式下 read_file 应允许")
		}
		if needConfirm {
			t.Error("plan 模式下 read_file 无需确认")
		}
	})

	t.Run("写工具拒绝", func(t *testing.T) {
		allowed, _ := policy.Check("write_file", `{"path":"test.go"}`)
		if allowed {
			t.Error("plan 模式下 write_file 应拒绝")
		}
	})

	t.Run("bash 拒绝", func(t *testing.T) {
		allowed, _ := policy.Check("bash", `{"command":"ls"}`)
		if allowed {
			t.Error("plan 模式下 bash 应拒绝")
		}
	})
}

// TestPolicyCheck_AcceptEdits_FileEdits 测试 accept-edits 模式下文件编辑自动接受
// 阶段 2 收模式后：accept-edits 经生产入口 SetModeByName 迁移为
// workspace-write + 默认 allow 规则，编辑工具应免问。
func TestPolicyCheck_AcceptEdits_FileEdits(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetModeByName("accept-edits")

	t.Run("write_file 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("write_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("accept-edits 模式下 write_file 应允许")
		}
		if needConfirm {
			t.Error("accept-edits 模式下 write_file 应自动接受，无需确认")
		}
	})

	t.Run("edit_file 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("edit_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("accept-edits 模式下 edit_file 应允许")
		}
		if needConfirm {
			t.Error("accept-edits 模式下 edit_file 应自动接受，无需确认")
		}
	})

	t.Run("NotebookEdit 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("NotebookEdit", `{"path":"test.ipynb"}`)
		if !allowed {
			t.Error("accept-edits 模式下 NotebookEdit 应允许")
		}
		if needConfirm {
			t.Error("accept-edits 模式下 NotebookEdit 应自动接受，无需确认")
		}
	})

	t.Run("只读工具自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("accept-edits 模式下 read_file 应允许")
		}
		if needConfirm {
			t.Error("accept-edits 模式下 read_file 无需确认")
		}
	})

	// L2声明即真相：未声明 agent_type 的 Agent 调用默认按**只读角色 explore**
	// 处理（§7⑤），只能拿到只读工具面，不具备写能力 ⇒ 无需确认。
	// 旧断言（"其他写操作需确认"）成立的前提是「未声明 = 可能具备任意能力」；L2 已用
	// 「默认只读、不扩权」替换该前提。需要写能力的子代理必须由委派方显式声明 agent_type。
	t.Run("未声明类型的 Agent 默认只读角色，无需确认", func(t *testing.T) {
		allowed, needConfirm := policy.Check("Agent", `{"task":"do something"}`)
		if !allowed {
			t.Error("accept-edits 模式下只读角色的 Agent 应允许")
		}
		if needConfirm {
			t.Error("未声明类型的 Agent 默认 explore（只读），不具写能力 ⇒ 不应请求确认")
		}
	})
}

// TestPolicyCheck_AcceptEdits_Bash 测试 accept-edits 模式下 bash 行为
// 阶段 2 收模式后：安全 bash 经默认 allow 规则免问，危险 bash 需确认
func TestPolicyCheck_AcceptEdits_Bash(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetModeByName("accept-edits")

	t.Run("安全 bash 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"ls -la"}`)
		if !allowed {
			t.Error("accept-edits 模式下安全 bash 应允许")
		}
		if needConfirm {
			t.Error("accept-edits 模式下安全 bash 应自动接受，无需确认")
		}
	})

	// L1：`PowerShell` 不是已登记工具（详见 checkwithrules_test.go 的
	// TestCheckWithRules_AllowRule_CannotResurrectUnregisteredTool 说明），未登记工具
	// fail-closed 到最高档 ⇒ 在 accept-edits（= workspace-write）档被直接拒绝。
	// 生产不可达：Windows 下 shell 工具名仍是 `bash`（internal/tui/stream.go:633）。
	t.Run("未登记的 PowerShell 名字被 fail-closed 拒绝", func(t *testing.T) {
		allowed, needConfirm := policy.Check("PowerShell", `{"command":"Get-ChildItem"}`)
		if allowed {
			t.Error("accept-edits 模式下未登记工具 PowerShell 必须被拒绝（fail-closed）")
		}
		if needConfirm {
			t.Error("被拒绝的调用不应请求确认")
		}
	})
}

// TestPolicyCheck_AcceptEdits_DangerousBash 测试 accept-edits 模式下危险 bash 需确认
// 阶段 2 收模式后：危险命令护栏经 allow 分支保留，仍需确认
func TestPolicyCheck_AcceptEdits_DangerousBash(t *testing.T) {
	policy := NewPolicy(LevelWorkspaceWrite)
	policy.SetModeByName("accept-edits")

	t.Run("危险 bash 需确认", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"rm -rf /"}`)
		if !allowed {
			t.Error("accept-edits 模式下危险 bash 应允许")
		}
		if !needConfirm {
			t.Error("accept-edits 模式下危险 bash 仍需确认")
		}
	})

	// 同上：PowerShell 未登记 ⇒ 模式判定先于危险命令护栏，命令是否危险已无意义。
	t.Run("未登记的 PowerShell 名字被 fail-closed 拒绝", func(t *testing.T) {
		allowed, needConfirm := policy.Check("PowerShell", `{"command":"rm -rf /"}`)
		if allowed {
			t.Error("accept-edits 模式下未登记工具 PowerShell 必须被拒绝（fail-closed）")
		}
		if needConfirm {
			t.Error("被拒绝的调用不应请求确认")
		}
	})
}

// TestPolicyCheck_DontAsk_ReadTools 测试 dont-ask 模式下只读工具自动接受
// 验证只读工具无需确认即可执行
func TestPolicyCheck_DontAsk_ReadTools(t *testing.T) {
	policy := NewPolicy(ParseLevel("dont-ask"))

	t.Run("read_file 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("read_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("dont-ask 模式下 read_file 应允许")
		}
		if needConfirm {
			t.Error("dont-ask 模式下 read_file 无需确认")
		}
	})

	t.Run("grep_search 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("grep_search", `{"pattern":"test"}`)
		if !allowed {
			t.Error("dont-ask 模式下 grep_search 应允许")
		}
		if needConfirm {
			t.Error("dont-ask 模式下 grep_search 无需确认")
		}
	})
}

// TestPolicyCheck_DontAsk_WriteTools 测试 dont-ask 模式下写工具自动接受
// 验证写工具无需确认即可执行
func TestPolicyCheck_DontAsk_WriteTools(t *testing.T) {
	policy := NewPolicy(ParseLevel("dont-ask"))

	t.Run("write_file 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("write_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("dont-ask 模式下 write_file 应允许")
		}
		if needConfirm {
			t.Error("dont-ask 模式下 write_file 无需确认")
		}
	})

	t.Run("edit_file 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("edit_file", `{"path":"test.go"}`)
		if !allowed {
			t.Error("dont-ask 模式下 edit_file 应允许")
		}
		if needConfirm {
			t.Error("dont-ask 模式下 edit_file 无需确认")
		}
	})

	t.Run("安全 bash 自动接受", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"ls -la"}`)
		if !allowed {
			t.Error("dont-ask 模式下安全 bash 应允许")
		}
		if needConfirm {
			t.Error("dont-ask 模式下安全 bash 无需确认")
		}
	})
}

// TestPolicyCheck_DontAsk_DangerousBash 测试 dont-ask 模式下危险 bash 仍需确认
// 验证即使是 dont-ask 模式，危险 bash 命令仍需确认
func TestPolicyCheck_DontAsk_DangerousBash(t *testing.T) {
	policy := NewPolicy(ParseLevel("dont-ask"))

	t.Run("危险 bash 需确认", func(t *testing.T) {
		allowed, needConfirm := policy.Check("bash", `{"command":"rm -rf /"}`)
		if !allowed {
			t.Error("dont-ask 模式下危险 bash 应允许")
		}
		if !needConfirm {
			t.Error("dont-ask 模式下危险 bash 仍需确认")
		}
	})

	// ★ P-002：原为幽灵名 `PowerShell`（从未注册）⇒ 改指真身 `Monitor`。
	t.Run("危险 Monitor 需确认", func(t *testing.T) {
		allowed, needConfirm := policy.Check("Monitor", `{"command":"rm -rf /"}`)
		if !allowed {
			t.Error("dont-ask 模式下危险 Monitor 应允许")
		}
		if !needConfirm {
			t.Error("dont-ask 模式下危险 Monitor 仍需确认")
		}
	})
}

// TestLevelString_OnlyThreeTiers 测试 3 档的字符串表示
// 阶段 2 收模式后：只有 read-only / workspace-write / danger-full-access 三档。
func TestLevelString_OnlyThreeTiers(t *testing.T) {
	tests := []struct {
		level Level
		want  string
	}{
		{LevelReadOnly, "read-only"},
		{LevelWorkspaceWrite, "workspace-write"},
		{LevelDangerFullAccess, "danger-full-access"},
	}
	for _, tt := range tests {
		got := tt.level.String()
		if got != tt.want {
			t.Errorf("Level(%d).String() = %q, expected %q", tt.level, got, tt.want)
		}
	}
}

// ============================================================================
// D8: auto / bubble 收模式后测试
//
// auto / bubble 不再是会话档：auto 移除（同类 CLI 权限决策确定性、可审计），
// bubble 降级为子代理委派参数（由 BubblePolicy 承担）。作为字符串它们保守
// 降级为 workspace-write。
// ============================================================================

// TestAutoStringDegradesToWorkspaceWrite 测试 auto 字符串降级后的行为
// 验证降级到 workspace-write：读操作免问、写操作需确认
func TestAutoStringDegradesToWorkspaceWrite(t *testing.T) {
	policy := NewPolicy(ParseLevel("auto"))

	// read_file 应自动允许（只读操作）
	allowed, needConfirm := policy.Check("read_file", `{"path": "test.go"}`)
	if !allowed {
		t.Error("auto 降级后 read_file 应允许")
	}
	if needConfirm {
		t.Error("auto 降级后 read_file 无需确认")
	}

	// write_file 应需确认（写操作）
	allowed, needConfirm = policy.Check("write_file", `{"path": "test.go", "content": "hello"}`)
	if !allowed {
		t.Error("auto 降级后 write_file 应允许")
	}
	if !needConfirm {
		t.Error("auto 降级后 write_file 需确认")
	}
}

// TestAutoStringDegrades_UnknownToolFailClosed 测试降级后「未登记工具」的处置。
//
// L1（2026-09-14，用户拍板）：目录已装配但工具未登记 ⇒ **fail-closed 到最高档** + 告警一次
// （见 permission/level.go RequiredLevelOf 第 2 条）。动机是关掉这个口子：
// 「新增工具忘登记 ⇒ 静默降级为需确认 ⇒ deny 规则逃逸」。旧实现回退 LevelWorkspaceWrite
// （允许 + 需确认），本用例原名 TestAutoStringDegrades_UnknownToolNeedsConfirm 即断言旧行为。
//
// 判别性：若把 RequiredLevelOf 的未登记分支改回 LevelWorkspaceWrite，本用例立即转红。
func TestAutoStringDegrades_UnknownToolFailClosed(t *testing.T) {
	policy := NewPolicy(ParseLevel("auto"))

	allowed, needConfirm := policy.Check("unknown_tool", `{}`)
	if allowed {
		t.Error("未登记工具必须 fail-closed 拒绝（不得静默放行，哪怕是带确认）")
	}
	if needConfirm {
		t.Error("被拒绝的调用不应请求确认")
	}
}

// TestBubbleModeForward 测试 BubblePolicy 将子代理权限请求转发给父策略
// 验证冒泡策略正确转发父策略的权限决策（bubble 现为委派参数，非会话档）
func TestBubbleModeForward(t *testing.T) {
	// BubblePolicy 应将子代理权限请求转发给父策略
	parentPolicy := NewPolicy(LevelWorkspaceWrite)
	bubble := NewBubblePolicy(parentPolicy)

	// write_file 在父策略的 workspace-write 模式下需确认
	allowed, needConfirm := bubble.Check("write_file", `{"path": "test.go", "content": "hello"}`)
	if !allowed {
		t.Error("BubblePolicy 应转发到父策略并允许")
	}
	// 应需确认，因为父策略是 workspace-write
	if !needConfirm {
		t.Error("BubblePolicy 应转发父策略的确认要求")
	}
}

// TestBubbleModeReadOnlyParent 测试父策略为只读时 BubblePolicy 拒绝写操作
func TestBubbleModeReadOnlyParent(t *testing.T) {
	parentPolicy := NewPolicy(LevelReadOnly)
	bubble := NewBubblePolicy(parentPolicy)

	allowed, _ := bubble.Check("write_file", `{"path": "test.go", "content": "hello"}`)
	if allowed {
		t.Error("BubblePolicy 下父策略为只读时应拒绝写操作")
	}
}

// TestBubbleModeDangerFullAccessParent 测试父策略为 danger-full-access 时 BubblePolicy
// 阶段 2 收模式后：danger 对危险命令保留二次确认护栏。
func TestBubbleModeDangerFullAccessParent(t *testing.T) {
	parentPolicy := NewPolicy(LevelDangerFullAccess)
	bubble := NewBubblePolicy(parentPolicy)

	// 普通写操作：全放、免问
	allowed, needConfirm := bubble.Check("write_file", `{"path": "test.go", "content": "hello"}`)
	if !allowed || needConfirm {
		t.Errorf("danger 父策略下 write_file: allowed=%v needConfirm=%v, want true/false", allowed, needConfirm)
	}
	// 危险命令：允许但二次确认（护栏不变量）
	allowed, needConfirm = bubble.Check("bash", `{"command": "rm -rf /"}`)
	if !allowed {
		t.Error("danger 父策略下危险 bash 应允许（带确认）")
	}
	if !needConfirm {
		t.Error("danger 父策略下危险 bash 必须二次确认（护栏不变量）")
	}
}

// TestParseAutoLevel 测试解析 auto（已移除，降级 workspace-write）
func TestParseAutoLevel(t *testing.T) {
	if ParseLevel("auto") != LevelWorkspaceWrite {
		t.Error("ParseLevel('auto') should return workspace-write")
	}
}

// TestParseBubbleLevel 测试解析 bubble（已移除，降级 workspace-write）
func TestParseBubbleLevel(t *testing.T) {
	if ParseLevel("bubble") != LevelWorkspaceWrite {
		t.Error("ParseLevel('bubble') should return workspace-write")
	}
}
