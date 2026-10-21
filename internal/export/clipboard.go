package export

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// clipboardTimeout 剪贴板命令执行超时时间（B6-P1-3）。
// 超过此时间未完成的剪贴板命令将被强制终止，避免阻塞主流程。
const clipboardTimeout = 5 * time.Second

// Clipboard 剪贴板抽象接口
type Clipboard interface {
	// Copy 将文本复制到剪贴板
	Copy(text string) error
	// CopyPath 将文件路径复制到剪贴板
	CopyPath(filepath string) error
}

// NewClipboard 创建跨平台剪贴板实例
//
// 支持的平台：
//   - Windows: 使用 clip.exe
//   - macOS: 使用 pbcopy
//   - Linux: 使用 xclip（首选）或 xsel（备选）
func NewClipboard() Clipboard {
	return &systemClipboard{}
}

// systemClipboard 系统剪贴板实现
type systemClipboard struct{}

// buildClipboardCommand 根据当前操作系统构造剪贴板写入命令。
// Windows 使用 clip.exe，macOS 使用 pbcopy，Linux 使用 xclip 或 xsel。
func buildClipboardCommand() (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("clip"), nil
	case "darwin":
		return exec.Command("pbcopy"), nil
	default:
		cmd := exec.Command("xclip", "-selection", "clipboard")
		if _, err := exec.LookPath("xclip"); err != nil {
			cmd = exec.Command("xsel", "--clipboard", "--input")
			if _, err2 := exec.LookPath("xsel"); err2 != nil {
				return nil, fmt.Errorf("未找到剪贴板工具，请安装 xclip 或 xsel")
			}
		}
		return cmd, nil
	}
}

// Copy 将文本复制到系统剪贴板。
// 使用 exec.CommandContext 配合 5 秒超时，避免剪贴板命令永久阻塞（B6-P1-3）。
func (c *systemClipboard) Copy(text string) error {
	ctx, cancel := context.WithTimeout(context.Background(), clipboardTimeout)
	defer cancel()

	cmd, err := buildClipboardCommand()
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	buf.WriteString(text)
	cmd.Stdin = &buf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动剪贴板命令失败: %w", err)
	}

	// 等待命令完成或超时
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("复制到剪贴板失败: %w", err)
		}
		return nil
	case <-ctx.Done():
		// 超时后强制终止进程
		_ = cmd.Process.Kill()
		<-done // 等待 goroutine 退出，防止泄漏
		return fmt.Errorf("复制到剪贴板超时（%s）", clipboardTimeout)
	}
}

// CopyPath 将文件路径复制到剪贴板
//
// 直接将路径字符串写入剪贴板，方便用户粘贴使用。
func (c *systemClipboard) CopyPath(filepath string) error {
	return c.Copy(filepath)
}
