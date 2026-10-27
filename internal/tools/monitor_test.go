package tools

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/monitor"
)

// TestMonitor_Start 测试启动后台进程
func TestMonitor_Start(t *testing.T) {
	mgr := monitor.NewManager()
	e := &Executor{
		handlers:   make(map[string]Handler),
		MonitorMgr: mgr,
	}
	handler := e.monitorHandler(t.TempDir())

	// 测试空命令
	t.Run("empty_command", func(t *testing.T) {
		input, _ := json.Marshal(MonitorInput{Command: ""})
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for empty command")
		}
		if !strings.Contains(err.Error(), "命令不能为空") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// 测试正常启动进程
	t.Run("start_process", func(t *testing.T) {
		var cmd string
		if runtime.GOOS == "windows" {
			cmd = "Write-Output hello"
		} else {
			cmd = "echo hello"
		}
		input, _ := json.Marshal(MonitorInput{Command: cmd, Description: "test echo"})
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "监控已启动") {
			t.Fatalf("expected monitor started message, got: %s", output)
		}
		if !strings.Contains(output, "test echo") {
			t.Fatalf("expected description in output, got: %s", output)
		}
	})

	// 测试无描述时自动截断
	t.Run("auto_description_truncate", func(t *testing.T) {
		longCmd := "echo a_very_long_command_that_exceeds_fifty_characters_limit_should_be_truncated"
		input, _ := json.Marshal(MonitorInput{Command: longCmd})
		output, err := handler(context.Background(), input)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(output, "监控已启动") {
			t.Fatalf("expected monitor started message, got: %s", output)
		}
	})

	// 测试 MonitorMgr 为 nil
	t.Run("nil_monitor_mgr", func(t *testing.T) {
		eNil := &Executor{
			handlers:   make(map[string]Handler),
			MonitorMgr: nil,
		}
		handlerNil := eNil.monitorHandler(t.TempDir())
		input, _ := json.Marshal(MonitorInput{Command: "echo test"})
		_, err := handlerNil(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for nil MonitorMgr")
		}
		if !strings.Contains(err.Error(), "监控管理器未初始化") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestMonitor_SleepDetection 测试 sleep 命令检测（时长 > 2s 被阻止）
func TestMonitor_SleepDetection(t *testing.T) {
	// 使用固定目录避免 Windows 上 TempDir 清理时进程占用问题
	workDir := t.TempDir()
	mgr := monitor.NewManager()
	e := &Executor{
		handlers:   make(map[string]Handler),
		MonitorMgr: mgr,
	}
	handler := e.monitorHandler(workDir)

	// 测试 sleep 5 被阻止
	t.Run("sleep_5_blocked", func(t *testing.T) {
		input, _ := json.Marshal(MonitorInput{Command: "sleep 5"})
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for sleep 5")
		}
		if !strings.Contains(err.Error(), "已阻止") {
			t.Fatalf("expected blocked message, got: %v", err)
		}
	})

	// 测试 sleep 1 不被阻止（使用独立 Manager 避免进程残留影响清理）
	t.Run("sleep_1_allowed", func(t *testing.T) {
		mgr1 := monitor.NewManager()
		e1 := &Executor{
			handlers:   make(map[string]Handler),
			MonitorMgr: mgr1,
		}
		handler1 := e1.monitorHandler(workDir)
		input, _ := json.Marshal(MonitorInput{Command: "sleep 1"})
		output, err := handler1(context.Background(), input)
		if err != nil {
			t.Fatalf("sleep 1 should be allowed, got error: %v", err)
		}
		if !strings.Contains(output, "监控已启动") {
			t.Fatalf("expected monitor started, got: %s", output)
		}
		// 等待进程结束后再清理
		time.Sleep(2 * time.Second)
		mgr1.StopAll()
	})

	// 测试链式命令中的 sleep 被阻止
	t.Run("chained_sleep_blocked", func(t *testing.T) {
		input, _ := json.Marshal(MonitorInput{Command: "echo hello && sleep 10"})
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for chained sleep 10")
		}
		if !strings.Contains(err.Error(), "已阻止") {
			t.Fatalf("expected blocked message, got: %v", err)
		}
	})

	// 测试分号分隔的 sleep 被阻止
	t.Run("semicolon_sleep_blocked", func(t *testing.T) {
		input, _ := json.Marshal(MonitorInput{Command: "echo hello; sleep 3"})
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for semicolon sleep 3")
		}
		if !strings.Contains(err.Error(), "已阻止") {
			t.Fatalf("expected blocked message, got: %v", err)
		}
	})

	// 测试空命令中的 sleep 检测
	t.Run("empty_command_sleep", func(t *testing.T) {
		// 空命令应该先触发"命令不能为空"错误
		input, _ := json.Marshal(MonitorInput{Command: ""})
		_, err := handler(context.Background(), input)
		if err == nil {
			t.Fatal("expected error for empty command")
		}
		if !strings.Contains(err.Error(), "命令不能为空") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// TestMonitor_WindowsConversion 测试 Windows 命令转换
func TestMonitor_WindowsConversion(t *testing.T) {
	// 直接测试 convertLinuxToPowerShell 函数
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "ls_to_GetChildItem",
			input:    "ls",
			expected: "Get-ChildItem",
		},
		{
			name:     "ls_la_to_GetChildItem_Force_FormatTable",
			input:    "ls -la",
			expected: "Get-ChildItem -Force | Format-Table Mode, LastWriteTime, Length, Name -AutoSize",
		},
		{
			name:     "ls_l_to_GetChildItem_FormatTable",
			input:    "ls -l",
			expected: "Get-ChildItem | Format-Table Mode, LastWriteTime, Length, Name -AutoSize",
		},
		{
			name:     "ls_a_to_GetChildItem_Force",
			input:    "ls -a",
			expected: "Get-ChildItem -Force",
		},
		{
			name:     "cat_to_GetContent",
			input:    "cat file.txt",
			expected: "Get-Content file.txt",
		},
		{
			name:     "grep_to_SelectString",
			input:    "grep pattern file.txt",
			expected: "Select-String -Path \"file.txt\" -Pattern \"pattern\"",
		},
		{
			name:     "lsof_not_converted",
			input:    "lsof -i :8080",
			expected: "lsof -i :8080",
		},
		{
			name:     "lsblk_not_converted",
			input:    "lsblk",
			expected: "lsblk",
		},
		{
			name:     "empty_string",
			input:    "",
			expected: "",
		},
		{
			name:     "whitespace_only",
			input:    "   ",
			expected: "   ",
		},
		{
			name:     "ls_with_path",
			input:    "ls /tmp",
			expected: "Get-ChildItem \"/tmp\"",
		},
		{
			name:     "ls_la_with_path",
			input:    "ls -la /tmp",
			expected: "Get-ChildItem -Force \"/tmp\" | Format-Table Mode, LastWriteTime, Length, Name -AutoSize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertLinuxToPowerShell(tt.input)
			if result != tt.expected {
				t.Errorf("convertLinuxToPowerShell(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestMonitor_IsLsCommand 测试 ls 命令精确匹配
func TestMonitor_IsLsCommand(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"ls", true},
		{"ls -la", true},
		{"ls /tmp", true},
		{"lsof", false},
		{"lsblk", false},
		{"lsusb", false},
		{"echo ls", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isLsCommand(tt.input)
			if result != tt.expected {
				t.Errorf("isLsCommand(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

// TestMonitor_ProcessOutput 测试监控进程的输出收集
func TestMonitor_ProcessOutput(t *testing.T) {
	mgr := monitor.NewManager()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Write-Output test_output_line"
	} else {
		cmd = "echo test_output_line"
	}

	proc, err := mgr.Start(cmd, "test output", t.TempDir())
	if err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	// 轮询等待进程完成（替代固定 time.Sleep 2s，避免 PowerShell 启动延迟导致 flaky）
	status, done := proc.WaitForDone(15 * time.Second)
	if !done {
		t.Fatalf("process did not finish in time, status: %s", status)
	}
	if status != monitor.StatusFinished {
		t.Fatalf("expected status finished, got %s", status)
	}

	// 进程退出 ≠ 输出已经收完 —— 管道读取/缓冲刷新可能略晚于 WaitForDone 返回，
	// 读完即断言会把"机器此刻多快"变成判据（CI 上偶发空输出）。
	// 给"输出到达"一个有界窗口；断言本身不变 —— 必须包含该行。
	deadline := time.Now().Add(5 * time.Second)
	var output string
	for {
		output = proc.GetOutput()
		if strings.Contains(output, "test_output_line") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected output to contain 'test_output_line', got: %s", output)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMonitor_StopProcess 测试停止监控进程
func TestMonitor_StopProcess(t *testing.T) {
	mgr := monitor.NewManager()

	var cmd string
	if runtime.GOOS == "windows" {
		cmd = "Start-Sleep -Seconds 60"
	} else {
		cmd = "sleep 60"
	}

	proc, err := mgr.Start(cmd, "long sleep", t.TempDir())
	if err != nil {
		t.Fatalf("failed to start process: %v", err)
	}

	// 验证进程正在运行
	if proc.GetStatus() != monitor.StatusRunning {
		t.Fatalf("expected running status, got: %s", proc.GetStatus())
	}

	// 停止进程
	if err := mgr.Stop(proc.ID); err != nil {
		t.Fatalf("failed to stop process: %v", err)
	}

	// 验证状态为 stopped
	if proc.GetStatus() != monitor.StatusStopped {
		t.Fatalf("expected stopped status, got: %s", proc.GetStatus())
	}

	// 再次停止应该报错
	if err := mgr.Stop(proc.ID); err == nil {
		t.Fatal("expected error when stopping already stopped process")
	}
}
