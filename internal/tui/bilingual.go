package tui

// bilingual.go — 「英文为主 + 中文为辅」文案形态的 **TUI 侧薄转发**。
//
// ★ 形态 SSOT 已于 2026-09-19（`N-039b`）**下沉到 `internal/util/bilingual.go`**。
// 原因：`internal/command` 的 `/help` 也要输出这个形态，而**本包已经 import `internal/command`**
// （`app.go` / `bridge.go` / `lifecycle.go` / `plain.go` …）⇒ command 反向 import tui **必成环**；
// `util` 是不 import 任何 internal 包的叶子包 ⇒ 唯一可共用的落点。
//
// 本文件保留 `bi` / `biSame` / `indentCopyLines` / `copyIndent` 这几个**包私有别名**，
// 只为让本包既有的全部调用点**零改动**；实现一律转发，**不许**在这里另写一套
// （那正是 `` 的「双实现必漂移」形态）。
//
// 形态契约（任何含汉字的行都必须以 `copyIndent` 开头）见 `bilingual_guard_test.go`，
// 以及 SSOT 侧的 `internal/util/bilingual_test.go`。

import "github.com/anchorandfish/whale-code/internal/util"

// copyIndent 是中文辅助行的统一缩进（转发 SSOT，保证两侧永远同一个值）。
const copyIndent = util.CopyIndent

// bi 渲染「英文主行 + 缩进中文辅助行」。转发 `util.Bi`。
func bi(en, zh string) string { return util.Bi(en, zh) }

// biSame 渲染双语提示，两侧共用同一组格式化参数。转发 `util.BiSame`。
func biSame(formatEN, formatZH string, args ...any) string {
	return util.BiSame(formatEN, formatZH, args...)
}

// indentCopyLines 给中文辅助行的每一非空行加缩进。转发 `util.IndentCopyLines`。
func indentCopyLines(s string) string { return util.IndentCopyLines(s) }

// hasHan 报告 s 是否含汉字。转发 `util.HasHan`（判定字符集与形态 SSOT 同源）。
func hasHan(s string) bool { return util.HasHan(s) }
