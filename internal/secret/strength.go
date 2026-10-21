// Package secret — 密钥强度评估（，对标 zxcvbn）。
//
// 设计目标：
// - 对标 zxcvbn 密码强度评估器（https://github.com/dropbox/zxcvbn @2026-07-21）
// - 评估密钥/密码强度，返回 0-4 级评分（0=极弱 / 4=强）
// - 检测常见弱点：长度过短、纯字母/数字、常见模式（123/abc/qwerty）、重复、序列
// - 不引入 zxcvbn 完整字典（避免包体积膨胀），仅实现核心评估逻辑
//
// 使用场景：
// - 配置文件中检测到疑似密钥时，提示用户强度
// - 反馈提交时脱敏后提示用户密钥强度
// - 不阻塞主流程，仅作辅助提示
package secret

import (
	"math"
	"strings"
	"unicode"
)

// Strength 是密钥强度评分（0-4，对标 zxcvbn score）。
type Strength int

const (
	// StrengthVeryWeak 极弱（zxcvbn 0）：长度 < 8 或纯重复/序列
	StrengthVeryWeak Strength = iota
	// StrengthWeak 弱（zxcvbn 1）：长度 8-11 或简单模式
	StrengthWeak
	// StrengthFair 中（zxcvbn 2）：长度 12-15 或混合但有规律
	StrengthFair
	// StrengthGood 强（zxcvbn 3）：长度 16-19 或高熵
	StrengthGood
	// StrengthStrong 极强（zxcvbn 4）：长度 ≥ 20 + 高熵 + 多类字符
	StrengthStrong
)

// String 返回强度的可读字符串。
func (s Strength) String() string {
	switch s {
	case StrengthVeryWeak:
		return "very-weak"
	case StrengthWeak:
		return "weak"
	case StrengthFair:
		return "fair"
	case StrengthGood:
		return "good"
	case StrengthStrong:
		return "strong"
	default:
		return "unknown"
	}
}

// Label 返回强度的中文标签。
func (s Strength) Label() string {
	switch s {
	case StrengthVeryWeak:
		return "极弱"
	case StrengthWeak:
		return "弱"
	case StrengthFair:
		return "中"
	case StrengthGood:
		return "强"
	case StrengthStrong:
		return "极强"
	default:
		return "未知"
	}
}

// StrengthReport 是密钥强度评估报告。
type StrengthReport struct {
	// Score 0-4 评分（zxcvbn 兼容）。
	Score Strength
	// EntropyBits 估算的熵（bits）。
	EntropyBits float64
	// Length 密钥长度（rune 计）。
	Length int
	// Warnings 弱点警告列表（人类可读）。
	Warnings []string
	// Suggestions 改进建议列表。
	Suggestions []string
}

// EvaluateStrength 评估密钥强度（对标 zxcvbn，纯 Go 实现）。
//
// 评估维度：
// 1. 长度：< 8 极弱，8-11 弱，12-15 中，16-19 强，≥20 极强
// 2. 字符多样性：小写/大写/数字/符号 4 类（每类+25%熵）
// 3. 重复检测：连续重复字符（aaa/111）扣分
// 4. 序列检测：升序/降序（abc/123/987）扣分
// 5. 常见模式：qwerty/password/12345678/abc 等
// 6. 字典词：password/admin/secret/root 等（精简版，zxcvbn 完整 30k 词本处不引入）
//
// 熵估算公式：log2(charsetSize) * length
// - charsetSize 由实际出现的字符类决定（小写=26/大写=26/数字=10/符号=32）
// - 重复/序列按比例扣减
func EvaluateStrength(secret string) StrengthReport {
	length := len([]rune(secret))
	report := StrengthReport{
		Length: length,
	}

	// 空密钥
	if length == 0 {
		report.Score = StrengthVeryWeak
		report.Warnings = append(report.Warnings, "密钥为空")
		report.Suggestions = append(report.Suggestions, "使用至少 16 字符的随机密钥")
		return report
	}

	// 1. 字符类检测
	hasLower, hasUpper, hasDigit, hasSymbol := classifyChars(secret)
	charsetSize := 0
	if hasLower {
		charsetSize += 26
	}
	if hasUpper {
		charsetSize += 26
	}
	if hasDigit {
		charsetSize += 10
	}
	if hasSymbol {
		charsetSize += 32
	}
	if charsetSize == 0 {
		charsetSize = 1 // 防止 log2(0)
	}

	// 2. 基础熵估算
	var entropy float64
	{
		// log2(charsetSize) * length
		entropy = log2(float64(charsetSize)) * float64(length)
	}

	// 3. 重复扣减
	repeats := countRepeats(secret)
	if repeats > 0 {
		deduction := float64(repeats) * 1.5
		entropy -= deduction
		report.Warnings = append(report.Warnings, "包含重复字符（如 aaa/111）")
		report.Suggestions = append(report.Suggestions, "避免连续重复字符")
	}

	// 4. 序列扣减
	sequences := countSequences(secret)
	if sequences > 0 {
		deduction := float64(sequences) * 2.0
		entropy -= deduction
		report.Warnings = append(report.Warnings, "包含序列字符（如 abc/123）")
		report.Suggestions = append(report.Suggestions, "避免升序/降序序列")
	}

	// 5. 常见模式检测
	if matched := matchCommonPatterns(secret); matched != "" {
		entropy -= 20
		report.Warnings = append(report.Warnings, "命中常见弱模式: "+matched)
		report.Suggestions = append(report.Suggestions, "避免使用 keyboard/password/abc/123 等常见模式")
	}

	// 6. 字典词检测
	if dict := matchDictionary(secret); dict != "" {
		entropy -= 15
		report.Warnings = append(report.Warnings, "包含常见字典词: "+dict)
		report.Suggestions = append(report.Suggestions, "避免使用 password/admin/secret 等字典词")
	}

	// 7. 字符类多样性建议
	if !hasUpper {
		report.Suggestions = append(report.Suggestions, "添加大写字母提升强度")
	}
	if !hasSymbol {
		report.Suggestions = append(report.Suggestions, "添加符号字符提升强度")
	}

	report.EntropyBits = entropy

	// 8. 综合评分
	switch {
	case length < 8 || entropy < 28:
		report.Score = StrengthVeryWeak
	case length < 12 || entropy < 36:
		report.Score = StrengthWeak
	case length < 16 || entropy < 60:
		report.Score = StrengthFair
	case length < 20 || entropy < 80:
		report.Score = StrengthGood
	default:
		report.Score = StrengthStrong
	}

	return report
}

// classifyChars 检测字符类。
func classifyChars(s string) (lower, upper, digit, symbol bool) {
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			symbol = true
		}
	}
	return
}

// countRepeats 统计连续重复字符数（aaa/111/!!!）。
func countRepeats(s string) int {
	runes := []rune(s)
	if len(runes) < 2 {
		return 0
	}
	count := 0
	prev := runes[0]
	streak := 1
	for i := 1; i < len(runes); i++ {
		if runes[i] == prev {
			streak++
			if streak == 3 {
				count++ // 第 3 个连续字符开始计 1 次
			}
		} else {
			streak = 1
			prev = runes[i]
		}
	}
	return count
}

// countSequences 统计序列字符数（abc/123/987/cba）。
func countSequences(s string) int {
	runes := []rune(s)
	if len(runes) < 3 {
		return 0
	}
	count := 0
	for i := 0; i+2 < len(runes); i++ {
		a, b, c := runes[i], runes[i+1], runes[i+2]
		// 升序序列（步长 1）
		if b-a == 1 && c-b == 1 {
			count++
		}
		// 降序序列（步长 -1）
		if a-b == 1 && b-c == 1 {
			count++
		}
	}
	return count
}

// commonWeakPatterns 是常见弱模式列表（精简版）。
var commonWeakPatterns = []string{
	"qwerty", "asdf", "zxcv",
	"12345", "123456", "1234567", "12345678",
	"abcdef", "abcde",
	"password", "passwd", "pwd",
	"11111", "00000", "aaaaa",
}

// matchCommonPatterns 检测是否命中常见弱模式。
// 返回命中的模式字符串（未命中返回 ""）。
func matchCommonPatterns(s string) string {
	lower := strings.ToLower(s)
	for _, p := range commonWeakPatterns {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// weakDictionary 是常见弱字典词列表（精简版，对标 zxcvbn 的 30k 词本的子集）。
var weakDictionary = []string{
	"password", "admin", "root", "secret", "user",
	"login", "welcome", "master", "qwerty", "letmein",
	"monkey", "dragon", "sunshine", "princess",
	"football", "baseball", "superman", "trustno1",
}

// matchDictionary 检测密钥是否包含常见字典词。
// 返回命中的字典词（未命中返回 ""）。
func matchDictionary(s string) string {
	lower := strings.ToLower(s)
	for _, word := range weakDictionary {
		if strings.Contains(lower, word) {
			return word
		}
	}
	return ""
}

// log2 计算以 2 为底的对数。
//
// 直接使用 math.Log2 保证精度（charsetSize=26 时返回 4.7004）。
func log2(x float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Log2(x)
}
