package doctor

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// checkPlatform 执行平台特定检测项（3 项）。
// Windows：Esafenet DLP 检测 + Winsock 健康检测。
// Linux：inotify 限制检测。
// 对标改进方案 §2.2-2.3 + project_memory 中记录的本机环境痛点。
// 非目标平台的检测项返回 "not applicable"（SeverityOK）。
func (d *Doctor) checkPlatform() []CheckResult {
	var results []CheckResult

	// 1. Windows Esafenet DLP 检测
	results = append(results, checkEsafenetDLP())

	// 2. Winsock 健康检测
	results = append(results, checkWinsock())

	// 3. Linux inotify 限制检测
	results = append(results, checkInotify())

	return results
}

// checkEsafenetDLP 检测 Esafenet DLP 是否拦截 go test 二进制。
// 仅 Windows，对标 project_memory "Esafenet DLP 加密 go test 二进制导致 fork/exec 报 Access denied"。
// 检测方法：编译一个小 probe binary 到临时目录，尝试 exec 它。
// probe build 失败不算 fatal（容错处理），仅当 exec 报 "Access is denied" 时标记 Warn。
func checkEsafenetDLP() CheckResult {
	if runtime.GOOS != "windows" {
		return CheckResult{
			Category: "Platform",
			Name:     "Esafenet DLP",
			Severity: SeverityOK,
			Message:  "not applicable (non-windows)",
		}
	}

	// 编译 probe binary 到临时目录
	tmpExe := filepath.Join(os.TempDir(), "whale-doctor-probe.exe")
	if err := buildProbeBinary(tmpExe); err != nil {
		// probe build 失败容错（可能是 go 命令不可用），不算 DLP 拦截
		return CheckResult{
			Category: "Platform",
			Name:     "Esafenet DLP",
			Severity: SeverityWarn,
			Message:  "probe build failed: " + err.Error(),
		}
	}
	defer os.Remove(tmpExe)

	// 尝试执行 probe binary
	cmd := exec.Command(tmpExe)
	if err := cmd.Run(); err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "Access is denied") {
			return CheckResult{
				Category:   "Platform",
				Name:       "Esafenet DLP",
				Severity:   SeverityWarn,
				Message:    "detected (go test binary may be blocked)",
				Detail:     errMsg,
				FixSuggest: "add whale-code binary to Esafenet whitelist or disable DLP for go build",
			}
		}
		// 其他执行错误不算 DLP
		return CheckResult{
			Category: "Platform",
			Name:     "Esafenet DLP",
			Severity: SeverityOK,
			Message:  "no DLP interference detected (probe exited with non-zero)",
		}
	}
	return CheckResult{
		Category: "Platform",
		Name:     "Esafenet DLP",
		Severity: SeverityOK,
		Message:  "no DLP interference detected",
	}
}

// buildProbeBinary 编译一个最小的 Go 程序到指定路径。
// 用于 Esafenet DLP 检测：编译后尝试 exec，若被拦截说明 DLP 干扰。
// 性能修复：加 10s 超时，避免冷编译/AV 扫描/慢磁盘拖慢 doctor。
// 超时后返回 timeout 错误，调用方按 probe build failed 容错处理（不算 DLP 拦截）。
func buildProbeBinary(outputPath string) error {
	// 创建最小 Go 源文件
	srcDir := filepath.Dir(outputPath)
	srcPath := filepath.Join(srcDir, "whale-doctor-probe.go")
	if err := os.WriteFile(srcPath, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		return err
	}
	defer os.Remove(srcPath)

	// 10s 超时，避免 go build 卡住整个 doctor
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 使用 go build 编译（CommandContext 响应 ctx 取消）
	cmd := exec.CommandContext(ctx, "go", "build", "-o", outputPath, srcPath)
	cmd.Env = append(os.Environ(), "GO111MODULE=off")
	if err := cmd.Run(); err != nil {
		// 优化1：超时或编译失败时清理可能产生的部分输出文件，避免临时文件泄漏
		os.Remove(outputPath)
		// 区分超时与其他错误，便于排查
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("go build timeout after 10s (cold cache or AV scan?): %w", err)
		}
		return err
	}
	return nil
}

// checkWinsock 检测 Winsock 是否损坏。
// 仅 Windows，对标 project_memory "Windows Winsock 栈损坏，httptest.NewServer 绑定 [::1]/127.0.0.1 可能失败"。
// 检测方法：net.Listen("tcp", "[::1]:0")（不依赖 httptest，避免 Winsock 损坏时测试自身失败）。
func checkWinsock() CheckResult {
	if runtime.GOOS != "windows" {
		return CheckResult{
			Category: "Platform",
			Name:     "Winsock",
			Severity: SeverityOK,
			Message:  "not applicable (non-windows)",
		}
	}

	// 尝试绑定 [::1]:0（httptest 模式，但不依赖 httptest 包）
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		return CheckResult{
			Category:   "Platform",
			Name:       "Winsock",
			Severity:   SeverityWarn,
			Message:    "bind [::1] failed: " + err.Error(),
			FixSuggest: "run `netsh winsock reset` and reboot",
		}
	}
	ln.Close()
	return CheckResult{
		Category: "Platform",
		Name:     "Winsock",
		Severity: SeverityOK,
		Message:  "bind [::1]:0 ok",
	}
}

// checkInotify 检测 Linux inotify watch 限制。
// 仅 Linux，检测 /proc/sys/fs/inotify/max_user_watches 值是否过低。
// 非 Linux 平台返回 "not applicable"。
func checkInotify() CheckResult {
	if runtime.GOOS != "linux" {
		return CheckResult{
			Category: "Platform",
			Name:     "inotify",
			Severity: SeverityOK,
			Message:  "not applicable (non-linux)",
		}
	}

	// 读取 inotify watch 限制
	data, err := os.ReadFile("/proc/sys/fs/inotify/max_user_watches")
	if err != nil {
		return CheckResult{
			Category: "Platform",
			Name:     "inotify",
			Severity: SeverityWarn,
			Message:  "cannot read max_user_watches: " + err.Error(),
		}
	}
	value := strings.TrimSpace(string(data))
	// 524288 是常见推荐值，低于此值标记 Warn
	if n, err := strconv.Atoi(value); err == nil && n < 524288 {
		return CheckResult{
			Category:   "Platform",
			Name:       "inotify",
			Severity:   SeverityWarn,
			Message:    "max_user_watches=" + value + " (recommended >= 524288)",
			FixSuggest: "run: echo fs.inotify.max_user_watches=524288 | sudo tee -a /etc/sysctl.conf && sudo sysctl -p",
		}
	}
	return CheckResult{
		Category: "Platform",
		Name:     "inotify",
		Severity: SeverityOK,
		Message:  "max_user_watches=" + value,
	}
}
