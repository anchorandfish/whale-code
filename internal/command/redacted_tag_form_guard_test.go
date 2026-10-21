package command

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestRedactedTagForm_FollowsSSOT 是「脱敏标签形态跟随 SSOT」的**包内结构门**。
//
// 背景（方案 A，2026-09-22 落地）：脱敏规则收敛到 internal/secret 单一 SSOT，
// 标签形态由「带类型后缀」统一为**裸标记**。该批翻转了约 12 处断言，
// 但漏改了本包 feedback_test.go 的一处，使本包长期带一条常红测试
// （09-22 已登记「陈旧断言·待清理」，未修）。
//
// 为什么既有全仓门没拦住：internal/feedback/redact_convergence_test.go 的
// TestRedact_NoSecondSecretPatternTable **只扫生产文件且显式跳过 _test.go**，
// 且它拦的是「第二张正则表」，不是「断言里的标签字面量」
// ⇒ 测试侧断言形态无门可拦，这是本类能存活至今的根因层。
//
// 本门补这一段：禁止本包（含 _test.go）出现**带类型后缀**的标签字面量。
// 只禁该形态、不禁裸标记 —— 裸标记是 SSOT 现行契约，禁掉会误伤正确的断言。
//
// ★ 判别力自证：在任意本包 .go 文件里加一行含「冒号形态标签」的字符串 ⇒ 本门变红。
func TestRedactedTagForm_FollowsSSOT(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位测试文件路径")
	}
	pkgDir := filepath.Dir(thisFile) // .../internal/command

	// 运行时拼接 needle，避免本文件自身的注释/常量命中自己。
	needle := "[" + "REDACTED" + ":"

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("读取包目录失败：%v", err)
	}

	violations := []string{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		scanned++
		data, rerr := os.ReadFile(filepath.Join(pkgDir, name))
		if rerr != nil {
			t.Fatalf("读取 %s 失败：%v", name, rerr)
		}
		if strings.Contains(string(data), needle) {
			violations = append(violations, name)
		}
	}

	// 非空转自检：一个 .go 都没扫到 ⇒ 本门是假绿。
	if scanned == 0 {
		t.Fatalf("未扫到任何 .go 文件（pkgDir=%s），本门失去判别力", pkgDir)
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("本包 %s 出现带类型后缀的脱敏标签：标签形态的唯一契约在 internal/secret/redact.go"+
				"（SSOT 现行产出裸标记），断言须对齐（ 方案 A）", v)
		}
	}
}
