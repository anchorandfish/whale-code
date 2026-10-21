// b6_pattern_guard_english_guard_test.go — 批 6 守卫（secret 包 pattern_guard 英文单侧）。
//
// 背景（「两种读者」判别提问）：pattern_guard 的错误串流向 **panic 输出 / 启动日志**，
// 读者是开发者，不回给模型、也不进 TUI 渲染 ⇒ **英文单侧**（加中文辅行会断行且无读者）。
// 本守卫钉两件事：
// 1. 结构面：pattern_guard.go 代码里的字符串字面量**零汉字**（含 init 的 panic 前缀）；
// 2. 行为面：每类坏表判出的错误消息必须非空且无汉字。
//
// 判别自证用内存合成源码（零磁盘写回，见双语技能 §7）。
package secret

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/util"
)

// TestW083B6_PatternGuardSourceEnglishOnly 结构面：pattern_guard.go 代码零 Han 字面量。
func TestW083B6_PatternGuardSourceEnglishOnly(t *testing.T) {
	src, err := os.ReadFile("pattern_guard.go")
	if err != nil {
		t.Fatalf("读取 pattern_guard.go 失败（本守卫以包目录为 CWD 运行）：%v", err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "pattern_guard.go", src, 0)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING || !util.HasHan(lit.Value) {
			return true
		}
		t.Errorf("pattern_guard.go:%d 出现 Han 字面量 %s —— panic/启动日志面必须英文单侧，勿加中文辅行",
			fset.Position(lit.Pos()).Line, lit.Value)
		return true
	})
}

// TestW083B6_GateDiscriminating 判别自证：合成违规源码必被抓（不红 = 门是假的）。
func TestW083B6_GateDiscriminating(t *testing.T) {
	src := "package p\nfunc f() error { return errors.New(\"规则表不合法：中文消息\") }\n"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "snippet.go", src, 0)
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if ok && lit.Kind == token.STRING && util.HasHan(lit.Value) {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("判据没牙：合成违规源码中的 Han 字面量未被识别")
	}
}

// TestW083B6_ViolationMessagesNoHan 行为面：每类坏表判出的错误必须非空且无汉字。
func TestW083B6_ViolationMessagesNoHan(t *testing.T) {
	good := secretPattern{id: "k", label: "K", re: regexp.MustCompile(`k-[a-z]{4}`)}
	goodMaster := buildMasterRegexFrom([]secretPattern{good})

	cases := []struct {
		name     string
		patterns []secretPattern
		master   *regexp.Regexp
	}{
		{name: "empty table", patterns: nil, master: goodMaster},
		{name: "empty id", patterns: []secretPattern{{id: "", label: "L", re: good.re}}, master: goodMaster},
		{name: "duplicate id", patterns: []secretPattern{good, good}, master: goodMaster},
		{name: "empty label", patterns: []secretPattern{{id: "a", label: "", re: good.re}}, master: goodMaster},
		{name: "nil regex", patterns: []secretPattern{{id: "a", label: "A", re: nil}}, master: goodMaster},
		{name: "empty regex source", patterns: []secretPattern{{id: "a", label: "A", re: regexp.MustCompile("")}}, master: goodMaster},
		{name: "nil master", patterns: []secretPattern{good}, master: nil},
		{
			name:     "master mismatch",
			patterns: []secretPattern{good},
			master:   buildMasterRegexFrom([]secretPattern{{id: "other", label: "O", re: regexp.MustCompile(`zzz`)}}),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := patternTableViolation(c.patterns, c.master)
			if err == nil {
				t.Fatal("A7 守卫失灵：坏表应当被判出，实际 nil")
			}
			msg := err.Error()
			if strings.TrimSpace(msg) == "" {
				t.Fatal("错误消息为空")
			}
			if util.HasHan(msg) {
				t.Errorf("错误消息含汉字（panic/启动日志面必须英文单侧，）：%q", msg)
			}
		})
	}
}

// 编译期引用钉住：errors/fmt 不得因重构而被误删（本文件自身的 import 面自检）。
var (
	_ = errors.New
	_ = fmt.Sprintf
)
