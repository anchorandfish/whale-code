package command

import (
	"strings"
	"testing"
)

// TestFormatHelp_AllStubsMarked C3判据：`/help --all` 里每条桩命令都须带
// `[未实现]` 标记，且 5 条桩的**具体名字**逐条点名。
//
// ★ 为什么必须有这条：桩命令 = 敲了会被告知"未实现"的命令，它们在 --all 下可见；
// 少了标记，用户会以为功能存在、用起来才失望。此前唯一的相关断言只查了"字面量是否存在"，
// 不查"哪些命令被标"—— 只保留一条标记、漏标其余 4 条时它仍然绿。
func TestFormatHelp_AllStubsMarked(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "--all")
	lines := strings.Split(help, "\n")

	var stubNames []string
	for _, cmd := range r.All() {
		if cmd.Stub {
			stubNames = append(stubNames, cmd.Name)
		}
	}
	if len(stubNames) == 0 {
		t.Fatal("注册表中应存在桩命令（本用例以桩存在为前提）")
	}

	// 逐条：该桩所在的命令行必须含 [未实现]
	for _, name := range stubNames {
		marked := false
		for _, ln := range lines {
			trimmed := strings.TrimSpace(ln)
			if !strings.HasPrefix(trimmed, "/"+name) {
				continue
			}
			// 防前缀误命中（如 /login 前缀撞 /login-extra）：名字后须是空格
			if len(trimmed) > len(name)+1 && trimmed[len(name)+1] != ' ' && trimmed[len(name)+1] != '\t' {
				continue
			}
			// C-6双语化：标记改为 [stub 未实现]（★ 有意翻转，勿当回归）
			marked = strings.Contains(trimmed, "[stub 未实现]")
			break
		}
		if !marked {
			t.Errorf("/help --all 中桩命令 /%s 应带 [未实现] 标记", name)
		}
	}

	// 反向钉住 5 条已知桩仍在（避免"悄悄删一条"绕过上面的逐条断言）
	want := []string{"plan", "voice", "break-cache", "login", "logout"}
	for _, n := range want {
		found := false
		for _, s := range stubNames {
			if s == n {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("桩命令 /%s 应仍在注册表中（若确要移除，请同步更新本清单与文档）", n)
		}
	}
}
