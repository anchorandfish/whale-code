// Package secret — 密钥泄露检测共用入口（D7-2 重构，提取自 tools/file.go 与 github/pr.go）。
//
// 设计目标：
// - 消除 detectSecretLeakForWrite（tools 包）与 detectSecretLeakForCommit（github 包）
// 之间的逻辑重复（两处实现完全一致，违背 DRY）
// - 收口到 secret 包，作为"密钥泄露检测 + 标签返回"的统一入口
// - 保留 fail-open 语义（panic 不阻断主流程，REF-OWASP-LLM02 + REF-）
// - 保留性能优化（优先用 IsSecretLeakFast 快速判定，命中后再走 ScanSecrets 取标签）
package secret

import "strings"

// DetectLeakWithLabels 检测文本是否含密钥模式，命中时返回命中的规则标签列表（用 ", " join）。
//
// 检测策略（与原 detectSecretLeakForWrite / detectSecretLeakForCommit 等价）：
// 1. 优先用 IsSecretLeakFast(content) 做快速判定（masterAnyPatternRegex 一次扫描，
// 较遍历 11 条正则有 ~10x 提速，无密钥文本典型 95% 场景走快路径）
// 2. 快速判定不命中 → 直接返回 ("", false)，避免走慢路径
// 3. 快速判定命中 → 用 ScanSecrets(content) 逐规则识别 RuleID/Label，收集 Label 列表
// 4. 用 ", " join 成字符串返回（如 "Anthropic API Key, GitHub Token"）
//
// fail-open 语义（REF-OWASP-LLM02 + REF-）：
// - 内部 panic 时 recover，返回 ("", false)，不阻断调用方主流程
// - 适用场景：写入文件 / git commit / PR 创建等关键路径，检测失败不应阻断业务
// - panic 来源：极端输入（如 1MB+ 超长字符串）触发正则引擎异常的兜底防御
//
// ★ A7（2026-09-15 拍板落地）：**本 recover 分支刻意保持 fail-open，不改运行期 T2**
// （改成 fail-closed 会撞 「写/commit/PR 不因检测失败阻断」）。
// 这条红线改为由"**让失效不可达**"守住 —— 见 `pattern_guard.go`：
// 规则表在包 `init()` 阶段做结构性校验（空表 / nil 正则 / 空正则源 / id 重复 …），
// 违规即 panic ⇒ 进程**启动失败**，而不是"运行到一半开始静默不脱敏"。
// 对齐业内既有实现 “：正则编译失败直接 panic，不静默降级。
//
// 实测边界（诚实声明）：该 recover 在当前实现下**不可达**（Go RE2 保证线性时间，
// 无灾难性回溯）；它只是防御性兜底，故无法用"触发 panic"的用例覆盖，改用
// TestA7_Evidence_EmptyTableIsSilentNotLoud 钉住"规则表失效是**静默**的"这一事实。
//
// 返回值：
// - labels：命中规则的 Label 列表（用 ", " join）；未命中或 panic 时为 ""
// - hit ：是否命中密钥模式；panic 时为 false
//
// 用法：
//
//	labels, hit := secret.DetectLeakWithLabels(content)
//	if hit {
//
// return fmt.Errorf("⚠️ [SECRET_LEAK_BLOCKED] 检测到密钥模式：\n%s", labels)
//
//	}
func DetectLeakWithLabels(content string) (labels string, hit bool) {
	defer func() {
		if r := recover(); r != nil {
			// fail-open：panic 时返回 ("", false)，不阻断主流程。
			// 注：此处不 log（secret 包不依赖 log，保持纯计算语义），
			// 调用方可在需要时自行记录命中/降级事件。
			labels, hit = "", false
		}
	}()
	// 快速判定：masterAnyPatternRegex 一次扫描，无命中直接返回。
	if !IsSecretLeakFast(content) {
		return "", false
	}
	// 命中后走慢路径：逐规则识别 Label（不返回匹配原文，避免二次泄露）。
	matches := ScanSecrets(content)
	var labelList []string
	for _, m := range matches {
		labelList = append(labelList, m.Label)
	}
	return strings.Join(labelList, ", "), true
}
