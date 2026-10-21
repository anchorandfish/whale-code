// Package secret — A7：规则表 fail-fast（让"检测失效"在进程启动时即不可达）。
//
// 背景（S14-G8 → 决策 A7 / S14-HG-1）：
//
//	`DetectLeakWithLabels` 的 `recover` 兜底是 **fail-open**：panic 等价于判"无泄露"放行。
//	拍板结论是 **保持 fail-open 语义**（：写文件 / commit / PR 不该被检测失败阻断），
//	真红线改为"**让失效不可达**" —— 对齐业内既有实现 `` 的
//	`LazyLock<Regex>` 常量：正则编译失败**直接 panic，绝不静默降级**。
//
// 本文件补的正是"**沉默的规则表**"这一失效面 —— 它比 panic 更危险，因为**连 recover 都不会触发**：
//
// - 规则表为空 ⇒ `RedactSensitive` 原样返回、`IsSecretLeak*` 恒 false
// ⇒ 每个调用点都判"无泄露"，**全仓脱敏静默失效**；
// - 某条规则 `re == nil` ⇒ 调用点 panic ⇒ 走 `recover` ⇒ 又变成静默放行；
// - 某条规则正则源为空（`MustCompile("")`）⇒ 空正则匹配任意位置 ⇒ **恒命中**，
// 脱敏退化为"把所有文本替换成 [REDACTED]"；
// - 规则 id 为空/重复 ⇒ `ScanSecrets` 的 `seen` 去重会静默丢规则、Label 归因错乱。
//
// fail-fast 位置 = 包 `init()`（= 进程启动阶段，**早于任何用户输入**）：违反即 panic，
// 进程**启动失败**，而不是"运行到一半才开始静默不脱敏"。
//
// 为什么不是"改运行期 T2（检测失败就阻断）"：那会撞 「写/commit/PR 不因检测失败阻断」。
// A7 的措辞就是"该 recover 分支当前不可达 ⇒ 真红线是让失效不可达"。
package secret

import (
	"errors"
	"fmt"
	"regexp"
)

// patternTableViolation 返回规则表的**首个结构性违规**；nil 表示表完好。
//
// 供两处复用（单一实现，避免"校验和构造各写一份"的漂移）：
// - `init()`：真实规则表 → 违规即 panic（fail-fast）；
// - 测试：可注入构造出的坏表，证明本守卫**有牙**（能被打红），不是恒真断言。
func patternTableViolation(patterns []secretPattern, master *regexp.Regexp) error {
	if len(patterns) == 0 {
		return errors.New("sensitivePatterns is empty: every scan will silently report no leak (a fail-open more insidious than panic)")
	}
	seen := make(map[string]bool, len(patterns))
	for i, p := range patterns {
		switch {
		case p.id == "":
			return fmt.Errorf("rule #%d has an empty id", i)
		case seen[p.id]:
			return fmt.Errorf("duplicate rule id %q: ScanSecrets dedup silently drops the latter", p.id)
		case p.label == "":
			return fmt.Errorf("rule %q has an empty label: DetectLeakWithLabels would return an empty label", p.id)
		case p.re == nil:
			return fmt.Errorf("rule %q has a nil regex: call sites would panic, recover, and silently report no leak", p.id)
		case p.re.String() == "":
			return fmt.Errorf("rule %q has an empty regex source: an empty regex matches anywhere, so detection would always hit", p.id)
		}
		seen[p.id] = true
	}
	if master == nil {
		return errors.New("masterAnyPatternRegex is nil: IsSecretLeakFast would panic")
	}
	// 构造点一致性：master 必须**由当前规则表**拼出来，而不是某份静态快照
	// （否则快路径与慢路径判定会分裂：一个判"有"、一个判"无"）。
	if want := masterRegexSource(patterns); master.String() != want {
		return errors.New("masterAnyPatternRegex is inconsistent with the current pattern table (master was not built from the current sensitivePatterns)")
	}
	return nil
}

// init 在进程启动阶段校验规则表，违规即 panic（A7 fail-fast）。
//
// 放在 init 而不是"第一次扫描时"：检测入口遍布写文件 / commit / PR / 日志，
// 把失效提前到"进程根本起不来"，才是"让失效不可达"。
func init() {
	if err := patternTableViolation(sensitivePatterns, masterAnyPatternRegex); err != nil {
		panic("secret: invalid secret detection pattern table, refusing to start (A7 fail-fast): " + err.Error())
	}
}
