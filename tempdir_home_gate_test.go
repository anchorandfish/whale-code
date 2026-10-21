package main

// tempdir_home_gate_test.go —— 机械门：**禁止**用 `t.TempDir()` 承载 HOME 隔离。
//
// 背景（根因层，2026-09-20）
//
//	`t.TempDir()` 注册的清理会**断言**目录已空（`os.RemoveAll` 失败即把用例判红）；
//	而 `$HOME` 恰好是**后台协程会持续写入**的目录（filehistory persist / usage tracker /
//	memory wiring 的 ForgettingScanner …）。二者叠加 = TOCTOU 竞态：
//	RemoveAll 正在删 `.whale/` 的同时协程又往里面建条目 ⇒
//
// TempDir RemoveAll cleanup: unlinkat .../.whale: directory not empty
//
//	该形态在**本轮已实测发生过**（2026-09-20 `internal/tui` 全量回归 2 条 picker 用例），
//	且它**非确定性**：同一批用例换个时序全绿 ⇒ **「全量绿」不能作为判别器**。
//
// 为什么不能只靠"记得"：本仓无 git，且这类失败只在负载/时序变化时冒头，
// 典型表现是"今天绿、明天红"，最容易被当成"既有 flaky"放过。
//
// 正确形态（**唯一 SSOT：internal/testutil.IsolatedHomeDir**，调用方一行、不手抄）：
//
//	t.Setenv("HOME", testutil.IsolatedHomeDir(t))
//
// 该 helper 内部即「os.MkdirTemp + 忽略错误的 RemoveAll 清理」，并保留 `t.Setenv` 的
// 自动还原 / 并行守卫语义。**不要手抄 MkdirTemp 片段**——这正是本门反复复发的驱动源。
//
// 判别力自检（证明本门不是假绿，照 `repo_hygiene_test.go` 的对抗性自检惯例）：
//
//	在任意 `*_test.go` 里重新写入 `t.Setenv("HOME", t.TempDir())`（或
//	`home := t.TempDir()` 后 `t.Setenv("HOME", home)`），本门必须 FAIL 并点名该文件:行。
//	恢复后 PASS。
//
// 诚实边界：本门只禁**这一种写法**，不证明"用了安全 helper 就再也不会 flaky" ——
// 若某用例另造路径把 HOME 指到别处再让协程写，本门看不见。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// gateSelfName 是本门自身文件名：其源码里含被禁模式的**字面量用于自检说明**，
// 必须在扫描时排除，否则门会自己打红自己。
const gateSelfName = "tempdir_home_gate_test.go"

// skipDirs 是不参与扫描的目录（非源码/归档/依赖）。
var skipDirs = map[string]bool{
	"vendor":       true,
	"docs":         true,
	"third_party":  true,
	"node_modules": true,
	".git":         true,
	".workbuddy":   true,
}

// tempHomeInline 匹配内联形态：t.Setenv("HOME"|"USERPROFILE", t.TempDir())
var tempHomeInline = regexp.MustCompile(`t\.Setenv\(\s*"(?:HOME|USERPROFILE)"\s*,\s*t\.TempDir\(\)\s*\)`)

// tempDirAssign 捕获 `x := t.TempDir()` / `x = t.TempDir()` 的变量名。
var tempDirAssign = regexp.MustCompile(`(?m)\b(\w+)\s*:?=\s*t\.TempDir\(\)`)

// tempHomeVar 判断文件里是否存在 `t.Setenv("HOME"|"USERPROFILE", <var>)`。
func tempHomeVar(fileSrc, varName string) bool {
	re := regexp.MustCompile(`t\.Setenv\(\s*"(?:HOME|USERPROFILE)"\s*,\s*` + regexp.QuoteMeta(varName) + `\s*\)`)
	return re.MatchString(fileSrc)
}

// TestGate_NoTempDirHomeIsolation 全仓扫描被禁写法。
func TestGate_NoTempDirHomeIsolation(t *testing.T) {
	type hit struct {
		path string
		line int
		why  string
	}
	var hits []hit

	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == "." {
				return nil
			}
			if skipDirs[name] || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel := filepath.ToSlash(path)
		if strings.HasSuffix(rel, gateSelfName) {
			return nil // 本门自身：含被禁模式字面量（自检说明），排除
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(src)

		for _, m := range tempHomeInline.FindAllStringIndex(text, -1) {
			if inComment(text, m[0]) {
				continue // 注释里提到被禁写法（解释性文字）不算违规
			}
			hits = append(hits, hit{rel, lineOf(text, m[0]), "内联 t.TempDir() 作 HOME"})
		}
		for _, m := range tempDirAssign.FindAllStringSubmatchIndex(text, -1) {
			varName := text[m[2]:m[3]]
			if inComment(text, m[0]) {
				continue
			}
			if tempHomeVar(text, varName) {
				hits = append(hits, hit{rel, lineOf(text, m[0]), varName + " := t.TempDir() 后被用作 HOME"})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败: %v", err)
	}

	if len(hits) == 0 {
		return
	}
	t.Errorf("发现 %d 处「用 t.TempDir() 承载 HOME 隔离」—— 后台协程会在用例结束后继续写 $HOME，"+
		"与 t.TempDir() 的 RemoveAll 清理断言竞态，报 "+
		"`TempDir RemoveAll cleanup: ...: directory not empty`（非确定性）：", len(hits))
	for _, h := range hits {
		t.Errorf("  - %s:%d（%s）", h.path, h.line, h.why)
	}
	guidance := "修法（**一行，调共享 helper，不要手抄片段**）：\n" +
		"  t.Setenv(\"HOME\", testutil.IsolatedHomeDir(t))\n" +
		"  import \"github.com/anchorandfish/whale-code/internal/testutil\"\n" +
		"testutil.IsolatedHomeDir 是全仓唯一正确承载体（宽松清理：忽略 RemoveAll 错误，见文件头「背景」的 TOCTOU），\n" +
		"并保留 t.Setenv 的自动还原 / 并行守卫语义。手抄 MkdirTemp 片段就是本门的复发源。"
	t.Errorf("%s", guidance)
}

// lineOf 返回字节偏移 off 所在的行号（1 起）。
func lineOf(src string, off int) int {
	if off > len(src) {
		off = len(src)
	}
	return 1 + strings.Count(src[:off], "\n")
}

// inComment 判断字节偏移 off 是否落在**行注释**内（`//` 之前的内容含 `//`）。
//
// 只用于放过「解释性注释里引用被禁写法」这一种误报（如 gu_testinfra_test.go 的
// 设计说明段落），不追求完整词法分析：Go 里字符串量中出现 `//` 的情形
// （如 URL）与本次扫描目标（`t.Setenv(...)` 调用）不会同处一行。
func inComment(src string, off int) bool {
	lineStart := strings.LastIndexByte(src[:off], '\n') + 1
	return strings.Contains(src[lineStart:off], "//")
}
