// Package config — memory_editor.go $EDITOR 集成（N12 P1-5）
//
// editInEditor 使用 $VISUAL/$EDITOR 打开记忆文件编辑，对标同类 CLI。
// TUI 暂停由调用方（app.go handleMemoryEdit）处理，本函数仅负责编辑器进程的启动与阻塞等待。
package config

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// editInEditor 使用 $VISUAL 或 $EDITOR 打开文件编辑。
// $VISUAL 优先于 $EDITOR（POSIX 惯例）。均未设置时返回错误。
// editor 可能含参数（如 "code -w"），用 strings.Fields 拆分，不走 shell（避免命令注入）。
// 阻塞式：cmd.Run() 等待编辑器退出，期间独占 stdin/stdout/stderr。
// 调用方需在 TUI 释放终端后调用（tea.Program.ReleaseTerminal），编辑完成后恢复。
func editInEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		return ai.WithID(fmt.Errorf("未设置 $VISUAL 或 $EDITOR 环境变量，请先设置后重试（如 export EDITOR=code）"), ai.ErrMemoryEditor)
	}

	// 拆分 editor 名和参数（如 "code -w" → ["code", "-w"]）
	fields := strings.Fields(editor)
	if len(fields) == 0 {
		return ai.WithID(fmt.Errorf("$VISUAL/$EDITOR 环境变量为空"), ai.ErrMemoryEditor)
	}

	// 构造参数：editor 自带参数 + 文件路径
	args := make([]string, 0, len(fields))
	args = append(args, fields[1:]...)
	args = append(args, path)

	cmd := exec.Command(fields[0], args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
