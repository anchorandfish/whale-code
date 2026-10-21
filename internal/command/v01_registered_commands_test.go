package command

import (
	"strings"
	"testing"
)

// v01_registered_commands_test.go —— G1 登记批的可发现性守卫。
//
// ★★ 已翻转（2026-10-12，，用户口径「1 版有的所有命令都要有，其他无承诺的都不要有」）：
// 原本守卫「这 13 条必须可见」（10-03 审计件登记批）；现按 v1 承诺面收口，这批命令
// **不在 10-10 判据册的 v1 承诺清单** ⇒ 全部收回隐藏（Hidden: true，handler 保留、手输仍可执行）。
// 登记与恢复方式见 内部工作回执；
// 以后改版若要重新暴露，翻转本文件 + command_hidden_test.go 的样本表即可（本仓已有两次先例）。
//
// 判别面（三面各一，互不重复）：
// 1. 注册表：已注册 + Hidden + 非 Stub + handler 非 nil（收回 ≠ 删除）
// 2. 结果面：默认 `/help` 文本里找不到该命令名（行首匹配，防前缀蒙对）
// 3. 声明面：`Registry.Completion` 不再按前缀给出来
//
// ★ 判别性：把任一命令的 Hidden 翻回 false ⇒ 1/2/3 会**同时**点名它（不是静默通过）。

// w082ReclaimedG1 是收回的 10-03 登记批 13 条（含家族全名）。
var w082ReclaimedG1 = []string{
	"fast",
	"daemon start", "daemon stop", "daemon restart", "daemon status",
	"daemon add", "daemon list", "daemon cancel", "daemon log",
	"task list", "task show", "task cancel",
}

func TestW082G1_Reclaimed_HiddenInRegistry(t *testing.T) {
	r := NewRegistry()
	for _, name := range w082ReclaimedG1 {
		cmd, ok := r.Get(name)
		if !ok {
			t.Errorf("/%s 应在注册表中存在（收回 ≠ 删除）", name)
			continue
		}
		if !cmd.Hidden {
			t.Errorf("/%s 应标记 Hidden（：v1 无承诺，收回可见面）", name)
		}
		if cmd.Stub {
			t.Errorf("/%s 不应标记 Stub（它是已实现命令，收回只改可见性）", name)
		}
		if cmd.Handler == nil {
			t.Errorf("/%s 的 handler 不应为 nil（手输仍需可执行）", name)
		}
	}
}

func TestW082G1_Reclaimed_AbsentFromDefaultHelp(t *testing.T) {
	r := NewRegistry()
	help := FormatHelp(r, "")
	// 前缀 + DescriptionEN 结尾整行匹配（防前缀假阳性 / 多词命令名腰斩，见 w082 判据注释）
	for _, name := range w082ReclaimedG1 {
		cmd, ok := r.Get(name)
		if !ok {
			continue
		}
		for _, line := range strings.Split(help, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "/"+name+" ") && strings.HasSuffix(trimmed, cmd.DescriptionEN) {
				t.Errorf("默认 /help 不应列出 /%s（ 收回）", name)
				break
			}
		}
	}
}

func TestW082G1_Reclaimed_NotHintedInCompletion(t *testing.T) {
	r := NewRegistry()
	// 单命令：一级前缀
	// ★ /memory 已于 10-12 14:50 拍板恢复可见 ⇒ 移出本"补全不提示"对照表
	for name, prefix := range map[string]string{"fast": "fas"} {
		if got := r.Completion(prefix); containsSlice(got, name) {
			t.Errorf("补全 %q 不应给出收回命令 %q，实际 %v", prefix, name, got)
		}
	}
	// 家族：根名 + 尾随空格与子命令前缀都不应给出全名
	for root, subs := range map[string][]string{
		"daemon": {"start", "stop", "restart", "status", "add", "list", "cancel", "log"},
		"task":   {"list", "show", "cancel"},
	} {
		got := r.Completion(root + " ")
		for _, sub := range subs {
			full := root + " " + sub
			if containsSlice(got, full) {
				t.Errorf("补全 %q 不应给出收回命令 %q，实际 %v", root+" ", full, got)
			}
		}
	}
}
