// Package main 是 whale-lint 独立二进制入口（方案 A，地基层 §2.3 v0.3）。
// 不依赖 go plugin（PIT-004 Windows 不可用），通过命令行调用。
// 用法：whale-lint <dir>
package main

import (
	"fmt"
	"os"

	"github.com/anchorandfish/whale-code/internal/orchestrator/whale_lint"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: whale-lint <dir>")
		os.Exit(2)
	}
	dir := os.Args[1]
	diags, err := whale_lint.Run(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "whale-lint error: %v\n", err)
		os.Exit(2)
	}
	for _, d := range diags {
		fmt.Printf("%s: %s\n", d.Pos, d.Message)
	}
	if len(diags) > 0 {
		os.Exit(1) // 有诊断结果 → exit 1
	}
	os.Exit(0) // 无诊断 → exit 0
}
