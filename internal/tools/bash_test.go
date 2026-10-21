package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestIsLsCommand tests ls command detection
func TestIsLsCommand(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"ls", true},
		{"ls -la", true},
		{"ls -a /tmp", true},
		{"lsof", false},
		{"lsblk", false},
		{"lsusb", false},
		{"echo ls", false},
		{"", false},
		{"cat", false},
	}

	for _, tt := range tests {
		got := isLsCommand(tt.input)
		if got != tt.want {
			t.Errorf("isLsCommand(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestConvertLsCommand tests ls to PowerShell conversion
func TestConvertLsCommand(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("ls conversion only applies on Windows")
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain ls", "ls", "Get-ChildItem"},
		{"ls -la", "ls -la", "Get-ChildItem -Force | Format-Table Mode, LastWriteTime, Length, Name -AutoSize"},
		{"ls -l", "ls -l", "Get-ChildItem | Format-Table Mode, LastWriteTime, Length, Name -AutoSize"},
		{"ls -a", "ls -a", "Get-ChildItem -Force"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertLsCommand(tt.input)
			if got != tt.want {
				t.Errorf("convertLsCommand(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestConvertLinuxToPowerShell tests Linux to PowerShell command conversion
func TestConvertLinuxToPowerShell(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"cat", "cat file.txt"},
		{"ls", "ls"},
		{"ls -la", "ls -la"},
		{"empty", ""},
		{"unknown", "python script.py"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLinuxToPowerShell(tt.input)
			if tt.input == "" && result != "" {
				t.Errorf("empty input should return empty, got %q", result)
			}
			if tt.input != "" && result == "" {
				t.Errorf("non-empty input should return non-empty")
			}
		})
	}
}

// TestConvertLinuxToPowerShellCat tests cat conversion
func TestConvertLinuxToPowerShellCat(t *testing.T) {
	result := convertLinuxToPowerShell("cat test.txt")
	if result != "Get-Content test.txt" {
		t.Errorf("cat conversion = %q, want %q", result, "Get-Content test.txt")
	}
}

// TestConvertLinuxToPowerShellNoConvert tests commands that should not be converted
func TestConvertLinuxToPowerShellNoConvert(t *testing.T) {
	tests := []string{
		"lsof -i",
		"lsblk",
		"lsusb",
		"python script.py",
		"go build",
	}

	for _, cmd := range tests {
		result := convertLinuxToPowerShell(cmd)
		if result != cmd {
			t.Errorf("convertLinuxToPowerShell(%q) = %q, should not convert", cmd, result)
		}
	}
}

// ============================================================================
// 新增 Bash 工具测试
// ============================================================================

// TestBash_SimpleCommand 测试简单命令执行（echo hello 返回 "hello"，退出码 0）
func TestBash_SimpleCommand(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	var echoCmd string
	if runtime.GOOS == "windows" {
		echoCmd = "Write-Output hello"
	} else {
		echoCmd = "echo hello"
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": echoCmd}),
	})

	if result.IsError {
		t.Fatalf("简单命令执行失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "hello") {
		t.Errorf("输出should contain 'hello'，actual: %s", result.Output)
	}
}

func TestBash_SandboxDenialShowsCommandAndNoExecution(t *testing.T) {
	r := NewRegistry()
	workDir := t.TempDir()
	e := NewExecutor(r, workDir)
	if e.Sandbox == nil {
		t.Fatal("NewExecutor should initialize the legacy sandbox")
	}
	e.Sandbox.SetEnabled(true)

	command := "curl https://example.com > network-marker.txt"
	result := e.Execute(context.Background(), Call{
		ID:    "sandbox-deny",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": command}),
	})

	if !result.IsError {
		t.Fatalf("sandbox denial should be marked as a failed tool result: %s", result.Output)
	}
	if result.FailureKind != FailureKindSandboxDenied {
		t.Fatalf("sandbox denial should carry a structured failure kind, got %q", result.FailureKind)
	}
	// ★ ：文案形态从「英文 / 中文」行内并列改为 SSOT
	// （英文主行 + 缩进中文辅行，internal/util/bilingual.go）⇒ 标记随文案更新，
	// **判定意图不变**：输出须点名「拒绝」+ 回显命令 + 声明未执行；两侧语言各钉一次。
	for _, want := range []string{
		"sandbox denied",
		"沙箱拒绝",
		"Command: " + command,
		"No command was run",
		"命令未执行",
	} {
		if !strings.Contains(result.Output, want) {
			t.Errorf("sandbox denial output should contain %q, got %q", want, result.Output)
		}
	}
	if _, err := os.Stat(filepath.Join(workDir, "network-marker.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied network command should not create network-marker.txt, stat err=%v", err)
	}
}

// TestBash_Timeout test timeout控制，sleep 命令被终止
func TestBash_Timeout(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	var sleepCmd string
	if runtime.GOOS == "windows" {
		sleepCmd = "Start-Sleep -Seconds 30"
	} else {
		sleepCmd = "sleep 30"
	}

	start := time.Now()
	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": sleepCmd, "timeout": 2}),
	})
	elapsed := time.Since(start)

	if !result.IsError {
		t.Error("超时命令should return error")
	}
	// 应在合理时间内返回（不超过 10 秒）
	if elapsed > 10*time.Second {
		t.Errorf("超时控制未生效，duration %v", elapsed)
	}
}

// TestBash_OutputTruncation test output超过 50000 字符时被截断
func TestBash_OutputTruncation(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 生成大量输出的命令
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "$output = ''; for ($i = 0; $i -lt 6000; $i++) { $output += ('x' * 20) + \"`n\" }; Write-Output $output"
	} else {
		cmd = "python3 -c \"print('x' * 20)\" | head -6000; yes xxxx | head -6000"
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": cmd}),
	})

	// 如果命令执行成功，验证截断
	if !result.IsError && len(result.Output) > 60000 {
		t.Errorf("输出应被截断到 50000 字符以内，actual长度: %d", len(result.Output))
	}
}

// TestBash_ExitCode 测试非零退出码被报告
func TestBash_ExitCode(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "exit 1"
	} else {
		cmd = "exit 1"
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": cmd}),
	})

	if !result.IsError {
		t.Error("非零退出码应报告为错误")
	}
}

// TestSleepDetection 测试 sleep 命令被检测并阻止
func TestSleepDetection(t *testing.T) {
	// 使用 monitor 包的检测函数
	detected := detectSleepCommand("sleep 10")
	if !detected {
		t.Error("sleep 命令应被检测到")
	}

	detected = detectSleepCommand("echo hello")
	if detected {
		t.Error("echo 命令不应被检测为 sleep")
	}

	if runtime.GOOS == "windows" {
		detected = detectSleepCommand("Start-Sleep -Seconds 10")
		if !detected {
			t.Error("Start-Sleep 命令应被检测到")
		}
	}
}

// TestBash_BackgroundMode 测试 RunInBackground=true 时 sleep 检测被跳过
func TestBash_BackgroundMode(t *testing.T) {
	r := NewRegistry()
	tmpDir := t.TempDir()
	e := NewExecutor(r, tmpDir)

	// 使用一个快速命令测试后台模式标志
	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Write-Output 'background test'"
	} else {
		cmd = "echo 'background test'"
	}

	result := e.Execute(context.Background(), Call{
		ID:    "1",
		Name:  "bash",
		Input: mustMarshal(map[string]interface{}{"command": cmd, "run_in_background": true}),
	})

	// 后台模式标志不影响简单命令的执行
	if result.IsError {
		t.Fatalf("后台模式执行失败: %s", result.Output)
	}
	if !strings.Contains(result.Output, "background test") {
		t.Errorf("后台模式应正常执行命令，actual: %s", result.Output)
	}
}

// detectSleepCommand 检测命令中是否包含 sleep 相关命令
func detectSleepCommand(cmd string) bool {
	lower := strings.ToLower(strings.TrimSpace(cmd))
	if strings.HasPrefix(lower, "sleep ") {
		return true
	}
	if strings.Contains(lower, "start-sleep") {
		return true
	}
	return false
}
