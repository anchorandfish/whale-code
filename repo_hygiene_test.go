package main

// repo_hygiene_test.go —— 仓库构建图卫生守卫。
//
// 背景（2026-09-13 实测）：docs/review0909/S9_fix_patch/ 内同时存在 package permission
// 与 package tools 的归档文件，且没有任何排除标记 —— 于是整个模块的
// `go build ./...` / `go vet ./...` / `go test ./...` 全部 exit 1（CI 的 Build/Vet 步骤全红）。
//
// 根因不是"漏加了几个 build tag"，而是缺一条被断言的**不变量**：
//
//	docs/ 是归档树，不是源码 —— 其下任何 .go 都不得进入模块构建图。
//
// 本测试把该不变量变成可执行断言，且只采用 Go 工具**自己**的排除规则，不引入自定义约定：
//
//	目录级（go help packages / go 命令文档明载）
// 1. 路径中任一段以 '.' 或 '_' 开头
// 2. 目录名为 testdata
//	文件级
// 3. 文件自身名字以 '.' 或 '_' 开头
// 4. 文件首部注释块含 `//go:build ignore`（或旧式 `// +build ignore`）
//
// 为什么不能只靠 CI 里的 `go build ./...`：往 docs/ 丢一个**恰好能编译**的 .go 时，
// 构建会成功，归档却**静默**变成构建图的一员（并被 `go test ./...` 捡去跑）。
// 本测试覆盖这个静默面。（顺带：本工作区没有 git 仓库，`git diff` 护栏不可用，
// 所以这条不变量只能靠代码本身来守。）
//
// 姊妹守卫：scripts/fmt-check.sh —— 用构建图驱动 gofmt，保证归档同样不进格式化门的范围
// （gofmt **不认** `_` 前缀与 `//go:build`，所以格式化门必须自己按构建图裁剪范围，
// 否则 docs/ vendor/ third_party/ 下的非源码也会被算进 CI 的 gofmt 结果里）。

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// archiveRoots 是非源码归档根（相对模块根）。其下 .go 必须处于构建图之外。
var archiveRoots = []string{"docs"}

// goIgnoredDirNames 是 Go 工具按**目录名**整体忽略的目录（见 go 命令文档）。
var goIgnoredDirNames = map[string]bool{"testdata": true}

// isGoIgnoredName 判断单个路径元素是否被 Go 工具忽略（'.' 或 '_' 前缀）。
func isGoIgnoredName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return name[0] == '.' || name[0] == '_'
}

// hasGoBuildIgnore 判断文件是否用 `//go:build ignore` 声明"不参与构建"。
// 只扫首个 package 子句之前的内容，避免正文里的同名字符串造成误判。
func hasGoBuildIgnore(src []byte) bool {
	for _, line := range strings.Split(string(src), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "package ") {
			return false // 已进入正文，注释头结束
		}
		var rest string
		switch {
		case strings.HasPrefix(t, "//go:build"):
			rest = strings.TrimPrefix(t, "//go:build")
		case strings.HasPrefix(t, "// +build"):
			rest = strings.TrimPrefix(t, "// +build")
		default:
			continue
		}
		for _, tok := range strings.Fields(rest) {
			if tok == "ignore" {
				return true
			}
		}
	}
	return false
}

// excludedByGoToolPathRules 报告给定（相对模块根的）路径为何被 Go 工具排除；返回空串表示"在构建图内"。
func excludedByGoToolPathRules(rel string) string {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if goIgnoredDirNames[seg] {
			return "目录名 " + seg + "（go 工具整体忽略）"
		}
		if isGoIgnoredName(seg) {
			return "路径段 " + seg + " 以 '.' 或 '_' 开头（go 工具忽略）"
		}
	}
	return ""
}

// TestArchivedGoFilesAreOutsideBuildGraph 断言归档树下的 .go 全部处于构建图之外。
//
// 对抗性自检（证明本测试不是假绿）：在 docs/ 下建一个**未被排除**的目录并放入可编译的 .go，
// 例如 `mkdir -p docs/guardprobe && printf 'package guardprobe\n' > docs/guardprobe/x.go`，
// 本测试必须 FAIL —— 而 `go build ./...` 会 PASS（它只是多编了一个包）。
// 那正是"静默污染构建图"的面，也是本守卫相对构建门的增量价值。
func TestArchivedGoFilesAreOutsideBuildGraph(t *testing.T) {
	for _, root := range archiveRoots {
		if _, err := os.Stat(root); err != nil {
			t.Logf("归档根 %s 不存在，跳过", root)
			continue
		}

		var violations []string
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel := filepath.ToSlash(path)
			if excludedByGoToolPathRules(rel) != "" {
				return nil // 已按路径规则排除
			}
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			if hasGoBuildIgnore(src) {
				return nil // 已按 //go:build ignore 排除
			}
			violations = append(violations, rel)
			return nil
		})
		if err != nil {
			t.Fatalf("遍历 %s 失败: %v", root, err)
		}

		if len(violations) > 0 {
			t.Errorf("归档树 %s 下有 %d 个 .go 在构建图内 —— 会让 go build/vet/test ./... 变红，"+
				"或（当它恰好能编译时）静默污染构建图：", root, len(violations))
			for _, v := range violations {
				t.Errorf("  - %s", v)
			}
			t.Errorf("两种修法（择一，优先第 1 种）：\n" +
				"  1) 目录级：把该文件所在目录改名为以 '_' 开头（如 `_S9_fix_patch`）——" +
				"Go 工具整体忽略该目录，以后往里面新增文件自动生效；\n" +
				"  2) 文件级：在文件首行加 `//go:build ignore`。")
		}
	}
}
