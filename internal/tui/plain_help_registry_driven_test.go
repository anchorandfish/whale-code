package tui

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
)

// TestPlainFormatHelp_IsRegistryDriven C2判据：plain /help 由注册表驱动。
//
// ★ 为什么必须有这条：plain 帮助此前是**第 4 份手抄命令表**，实测缺 13 条
// （add-dir / checkpoint×6 / history / monitors stop,tail / session / session pick / tool）——
// 而旧断言只抽查了几个样本，漏掉的命令它从不报红，于是"缺 13 条"能长期潜伏。
// 本用例把「注册表可见集 ⊆ plain 帮助」钉死：任何一条可见命令没出现在帮助里即红。
func TestPlainFormatHelp_IsRegistryDriven(t *testing.T) {
	reg := command.NewRegistry()
	help := formatHelp(reg)

	var missing []string
	visible := 0
	for _, cmd := range reg.All() {
		if cmd.Hidden || cmd.Stub {
			continue
		}
		visible++
		if cmd.Name == "" {
			continue
		}
		if !strings.Contains(help, "/"+cmd.Name) {
			missing = append(missing, cmd.Name)
		}
	}
	if visible == 0 {
		t.Fatal("注册表中应存在可见命令（本用例以可见命令存在为前提）")
	}
	if len(missing) > 0 {
		t.Errorf("plain /help 缺少 %d 条注册表可见命令（手抄漂移复发？）：%v", len(missing), missing)
	}
}
