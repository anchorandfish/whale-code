package monitor

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestDetectBlockedSleep 测试 sleep 模式检测（后为结构化 API）。
func TestDetectBlockedSleep(t *testing.T) {
	tests := []struct {
		name    string
		command string
		blocked bool
	}{
		{"empty command", "", false},
		{"sleep 0", "sleep 0", false},
		{"sleep 1", "sleep 1", false},
		{"sleep 2", "sleep 2", true},
		{"sleep 5", "sleep 5", true},
		{"sleep 10", "sleep 10", true},
		{"sleep 60", "sleep 60", true},
		{"sleep with chain &&", "build && sleep 5", true},
		{"sleep with chain ;", "build ; sleep 3", true},
		{"no sleep", "echo hello", false},
		{"ls command", "ls -la", false},
		{"grep command", "grep error log.txt", false},
		{"sleep in string", `echo "sleep 5"`, false},
		// ★ ：必须取**全部命中的最大秒数**。旧实现用 FindStringSubmatch
		// （只取首个命中）⇒ 首个是 `sleep 1` ⇒ 判为不拦，尽管后面跟着 `sleep 200`。
		{"max across matches", "echo x && sleep 1 && sleep 200", true},
		{"every occurrence within limit", "echo x && sleep 1 && sleep 1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sb, blocked := DetectBlockedSleep(tt.command)
			if blocked != tt.blocked {
				t.Errorf("DetectBlockedSleep(%q) blocked = %v, want %v",
					tt.command, blocked, tt.blocked)
			}
			if blocked && sb.LimitSeconds != MaxAllowedSleepSeconds {
				t.Errorf("SleepBlock.LimitSeconds = %d, want %d（提示文案必须由唯一声明派生）",
					sb.LimitSeconds, MaxAllowedSleepSeconds)
			}
			if blocked && sb.Reason == "" {
				t.Errorf("SleepBlock.Reason 不得为空（调用点要用它渲染提示）")
			}
		})
	}
}

// TestMaxAllowedSleepSecondsIsEnforcedBoundary 钉住 的核心不变量：
// **声明的上界 == 实际放行的边界**（`sleep N` 放行、`sleep N+1` 被拒，N = 上界）。
//
// 本条不依赖任何调用点文案，纯判定面 —— 它是"文案与判定同源"的地基：
// 若哪天有人把判定改成 `>= MaxAllowedSleepSeconds`，上界声明就变成谎话，本条先红。
func TestMaxAllowedSleepSecondsIsEnforcedBoundary(t *testing.T) {
	n := MaxAllowedSleepSeconds
	if _, blocked := DetectBlockedSleep(fmt.Sprintf("sleep %d", n)); blocked {
		t.Errorf("声明上界=%d，但 `sleep %d` 被拦（提示会指向一个做不到的秒数）", n, n)
	}
	if _, blocked := DetectBlockedSleep(fmt.Sprintf("sleep %d", n+1)); !blocked {
		t.Errorf("`sleep %d` 未被拦（判定比声明的上界更松）", n+1)
	}
}

// TestExtractSleepSeconds 测试秒数提取
func TestExtractSleepSeconds(t *testing.T) {
	tests := []struct {
		command string
		idx     int
		want    int
	}{
		{"sleep 5", 0, 5},
		{"sleep 10", 0, 10},
		{"sleep 0", 0, 0},
		{"sleep 1", 0, 1},
		{"build && sleep 3", 9, 3},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := ExtractSleepSeconds(tt.command, tt.idx)
			if got != tt.want {
				t.Errorf("extractSleepSeconds(%q, %d) = %d, want %d",
					tt.command, tt.idx, got, tt.want)
			}
		})
	}
}

// TestManagerStartStop 测试进程管理器启动和停止
func TestManagerStartStop(t *testing.T) {
	m := NewManager()

	// 启动一个短命进程
	proc, err := m.Start("echo hello", "test echo", "")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	if proc.ID == "" {
		t.Error("Expected non-empty monitor ID")
	}
	if proc.Status != StatusRunning {
		t.Errorf("Expected status running, got %s", proc.Status)
	}
	if proc.Command != "echo hello" {
		t.Errorf("Expected command 'echo hello', got %s", proc.Command)
	}

	// 轮询等待进程结束（替代固定 time.Sleep，避免 PowerShell 启动延迟导致 flaky）
	// Windows 上 PowerShell 启动需 ~2s，固定 2s sleep 恰好在边界上，CI/慢机会失败
	if !proc.WaitForStatus(StatusFinished, 15*time.Second) {
		t.Errorf("Expected status finished, got %s", proc.GetStatus())
	}
}

// TestManagerStopRunning 测试停止运行中的进程
func TestManagerStopRunning(t *testing.T) {
	m := NewManager()

	// 启动一个长运行进程
	proc, err := m.Start("sleep 60", "long sleep", "")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	// 确认进程在运行
	if m.RunningCount() != 1 {
		t.Errorf("Expected 1 running process, got %d", m.RunningCount())
	}

	// 停止进程
	err = m.Stop(proc.ID)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	if m.RunningCount() != 0 {
		t.Errorf("Expected 0 running processes, got %d", m.RunningCount())
	}
}

// TestManagerList 测试列出进程
func TestManagerList(t *testing.T) {
	m := NewManager()

	_, err1 := m.Start("echo one", "first", "")
	_, err2 := m.Start("echo two", "second", "")
	if err1 != nil || err2 != nil {
		t.Fatalf("Start failed: %v, %v", err1, err2)
	}

	list := m.List()
	if len(list) != 2 {
		t.Errorf("Expected 2 processes, got %d", len(list))
	}

	m.StopAll()
}

// TestManagerCallbacks 测试回调函数
func TestManagerCallbacks(t *testing.T) {
	m := NewManager()

	var doneStatus MonitorStatus
	var doneOutput string
	doneCh := make(chan struct{})

	m.SetDoneCallback(func(id string, status MonitorStatus, exitCode int, output string) {
		doneStatus = status
		doneOutput = output
		close(doneCh)
	})

	proc, err := m.Start("echo test_output", "callback test", "")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 轮询等待进程结束（替代固定 time.Sleep 3s，避免 PowerShell 启动延迟导致 flaky）
	status, done := proc.WaitForDone(15 * time.Second)
	if !done {
		t.Fatalf("Process did not finish in time, status: %s", status)
	}
	select {
	case <-doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("done callback did not fire in time")
	}

	if doneStatus != StatusFinished {
		t.Errorf("Expected done callback with status finished, got %s", doneStatus)
	}

	if !strings.Contains(doneOutput, "test_output") {
		t.Errorf("Expected output to contain 'test_output', got %q", doneOutput)
	}
}

// TestManagerStopNonexistent 测试停止不存在的进程
func TestManagerStopNonexistent(t *testing.T) {
	m := NewManager()

	err := m.Stop("nonexistent")
	if err == nil {
		t.Error("Expected error when stopping nonexistent process")
	}
}

// TestProcessGetOutput 测试获取进程输出
func TestProcessGetOutput(t *testing.T) {
	p := &Process{
		Output: []string{"line1", "line2", "line3"},
	}

	output := p.GetOutput()
	expected := "line1\nline2\nline3"
	if output != expected {
		t.Errorf("Expected %q, got %q", expected, output)
	}
}

// TestProcessAddLine 测试添加输出行
func TestProcessAddLine(t *testing.T) {
	p := &Process{
		Output: make([]string, 0),
	}

	p.AddLine("hello")
	p.AddLine("world")

	if p.Lines != 2 {
		t.Errorf("Expected 2 lines, got %d", p.Lines)
	}
}

// TestManagerStopStatusNotOverridden 测试 Stop() 后 waitProcess 不会覆盖 StatusStopped
// 验证 waitProcess 的状态守卫：Stop() 设置 StatusStopped 并 cancel context 后，
// 进程被杀导致 cmd.Wait() 返回 error，waitProcess 不应将状态覆盖为 StatusFailed
func TestManagerStopStatusNotOverridden(t *testing.T) {
	m := NewManager()

	proc, err := m.Start("sleep 60", "long sleep for stop test", "")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// 确认进程在运行
	if proc.GetStatus() != StatusRunning {
		t.Fatalf("Expected running status, got %s", proc.GetStatus())
	}

	// 停止进程
	if err := m.Stop(proc.ID); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// 等待进程彻底退出（cmd.Wait 返回），给 waitProcess goroutine 充分时间执行
	status, done := proc.WaitForDone(10 * time.Second)
	if !done {
		t.Fatalf("Process did not exit after stop, status: %s", status)
	}

	// 核心断言：状态应保持 StatusStopped，不应被 waitProcess 覆盖为 StatusFailed
	if status != StatusStopped {
		t.Errorf("Expected status stopped (not overridden by waitProcess), got %s", status)
	}
}
