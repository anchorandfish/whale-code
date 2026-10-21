package diff

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OpenInIDE 在 IDE 中打开 diff 视图
// 契约：02 §2.2.1 N9 负责渲染输出，IDE 协同是渲染输出的延伸
// 行为：检测 IDE 类型，调用对应命令行打开 diff 视图
//   - VSCode/Cursor：code --diff <left> <right>
//   - JetBrains：idea diff <left> <right>
//   - 其他：fallback 到终端内 Render
//
// 安全通道：code/idea 是受信任 IDE 命令（非用户输入），参数为 os.MkdirTemp 生成的临时文件路径（无注入风险），
//
//	直接用 exec.Command 调用，不走 sandbox.StartCommandWithProcessGroup（该通道用于用户输入的 shell 命令审批）
//
// 临时文件：os.MkdirTemp 创建目录（默认 0700），写文件时显式 os.WriteFile(path, content, 0600) 防止其他用户读取（含密钥场景）
// 清理时机：cmd.Start() + goroutine 监听 cmd.Wait() 进程退出后 os.RemoveAll(tmpDir)，不阻塞主流程
//
//	（不用 defer os.Remove——defer 在函数返回时立即执行，会导致 IDE 显示空白）
//
// 超越点：同类 CLI JetBrains 需手动打开，本方案自动检测命令
func OpenInIDE(change FileChange) error {
	cmdPath, args, ok := DetectIDE()
	if !ok {
		return fmt.Errorf("no IDE detected, fallback to terminal diff")
	}

	tmpDir, err := os.MkdirTemp("", "whale-diff-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}

	leftPath := filepath.Join(tmpDir, "a_"+filepath.Base(change.Path))
	rightPath := filepath.Join(tmpDir, "b_"+filepath.Base(change.Path))

	oldContent := change.OldContent
	if oldContent == "" {
		oldContent = "(empty file)"
	}
	newContent := change.NewContent
	if newContent == "" {
		newContent = "(empty file)"
	}

	if err := os.WriteFile(leftPath, []byte(oldContent), 0600); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to write left file: %w", err)
	}
	if err := os.WriteFile(rightPath, []byte(newContent), 0600); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to write right file: %w", err)
	}

	fullArgs := append(args, leftPath, rightPath)
	cmd := exec.Command(cmdPath, fullArgs...)

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("failed to start IDE: %w", err)
	}

	go func() {
		cmd.Wait()
		os.RemoveAll(tmpDir)
	}()

	return nil
}

// DetectIDE 检测当前可用的 IDE 命令
// 复用 tui/render.go:353 的 editor 检测思路，扩展到 IDE diff 命令
func DetectIDE() (cmd string, args []string, ok bool) {
	candidates := []struct {
		name string
		args []string
	}{
		{"code", []string{"--diff"}},
		{"code.cmd", []string{"--diff"}},
		{"cursor", []string{"--diff"}},
		{"cursor.cmd", []string{"--diff"}},
		{"idea", []string{"diff"}},
		{"pycharm", []string{"diff"}},
		{"webstorm", []string{"diff"}},
		{"goland", []string{"diff"}},
		{"phpstorm", []string{"diff"}},
	}

	for _, c := range candidates {
		if path, err := exec.LookPath(c.name); err == nil {
			return path, c.args, true
		}
	}

	return "", nil, false
}
