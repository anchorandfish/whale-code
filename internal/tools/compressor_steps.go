// Package tools — compression step implementations for OutputCompressor
//
// This file contains the 9 compression steps that form the pipeline.
// Each step is a pure function that can be independently tested and toggled.
package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ============================================================================
// Step 1: StripANSI — strip ANSI escape sequences
// ============================================================================

// StripANSI strips ANSI escape sequences from a string.
// Matches: ESC [ <params> <final byte> (CSI sequences per ECMA-48/ANSI X3.64)
func StripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip CSI sequence: ESC [ <params> <final byte>
			j := i + 2
			for j < len(s) {
				// Final byte: 0x40-0x7E (A-Z, a-z, @, [, \, ], ^, _, etc.)
				if s[j] >= 0x40 && s[j] <= 0x7E {
					j++
					break
				}
				j++
			}
			i = j
		} else if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == ']' {
			// Skip OSC sequence: ESC ] ... BEL/ST
			j := i + 2
			for j < len(s) {
				if s[j] == '\x07' { // BEL
					j++
					break
				}
				if s[j] == '\x1b' && j+1 < len(s) && s[j+1] == '\\' { // ST
					j += 2
					break
				}
				j++
			}
			i = j
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// ============================================================================
// Step 2: StripCarriage — remove \r characters
// ============================================================================

// StripCarriage removes \r characters from the output.
// Windows command output contains \r\n; \r is noise for the LLM.
func StripCarriage(s string) string {
	return strings.ReplaceAll(s, "\r", "")
}

// ============================================================================
// Step 3: FilterNoise — intelligent noise filtering
// ============================================================================

// spinnerChars maps braille spinner characters that indicate a progress animation
var spinnerChars = map[rune]bool{
	'⠋': true, '⠙': true, '⠹': true, '⠸': true,
	'⠼': true, '⠴': true, '⠦': true, '⠧': true,
	'⠇': true, '⠏': true,
}

// DefaultNoisePatterns returns the default noise line patterns
func DefaultNoisePatterns() []NoisePattern {
	return []NoisePattern{
		{Pattern: "spinner", Description: "Braille spinner animation characters", Enabled: true},
		{Pattern: "progress_bar", Description: "Progress bar with block characters", Enabled: true},
		{Pattern: "git_hint", Description: "Git hint messages", Enabled: true},
		{Pattern: "npm_warn", Description: "NPM deprecation and engine warnings", Enabled: true},
		{Pattern: "vite_plugin", Description: "Vite/Next.js plugin warning blocks", Enabled: true},
		{Pattern: "node_modules_stack", Description: "node_modules stack frames", Enabled: true},
	}
}

// FilterNoise removes noise lines from the output.
// Noise includes: spinners, progress bars, git hints, npm warnings, etc.
func FilterNoise(lines []string, customPatterns []NoisePattern) []string {
	var result []string
	for _, line := range lines {
		if !IsNoiseLine(line) {
			result = append(result, line)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result
}

// IsNoiseLine determines whether a line is noise
func IsNoiseLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")

	if len(trimmed) == 0 {
		return false // blank lines are handled by CollapseBlanks
	}

	// Spinner animation characters at start
	for _, r := range trimmed {
		if spinnerChars[r] {
			return true
		}
		break // only check first rune
	}

	// Progress bar: contains █ or ░
	if strings.Contains(trimmed, "█") || strings.Contains(trimmed, "░") {
		return true
	}

	// Progress bar: [=====>] style
	if strings.Contains(trimmed, "[") && strings.Contains(trimmed, "]") &&
		strings.Contains(trimmed, "=") && strings.Contains(trimmed, ">") {
		return true
	}

	// Percentage at end of line (e.g., "67%")
	if strings.HasSuffix(trimmed, "%") && containsDigit(trimmed) {
		// Only if it looks like a standalone progress indicator
		// Must not be a normal sentence ending with percentage
		fields := strings.Fields(trimmed)
		if len(fields) <= 3 {
			return true
		}
	}

	// Git hint
	if strings.HasPrefix(trimmed, "hint:") {
		return true
	}

	// NPM noise
	if strings.HasPrefix(trimmed, "npm warn deprecated") ||
		strings.HasPrefix(trimmed, "WARN deprecated") ||
		strings.HasPrefix(trimmed, "npm notice") ||
		strings.HasPrefix(trimmed, "npm warn EBADENGINE") {
		return true
	}

	// Vite/Next.js plugin warning blocks
	if strings.HasPrefix(trimmed, "[tsconfig-paths]") ||
		strings.HasPrefix(trimmed, "The plugin \"vite-tsconfig-paths\"") ||
		strings.HasPrefix(trimmed, "The plugin \"@vitejs/plugin-react\"") ||
		strings.HasPrefix(trimmed, "Vite now supports tsconfig paths") ||
		strings.HasPrefix(trimmed, "You can remove the plugin") ||
		strings.HasPrefix(trimmed, "`next lint` is deprecated") {
		return true
	}

	// node_modules stack frames
	if strings.Contains(line, "node_modules/") &&
		(strings.HasPrefix(trimmed, "at ") || strings.Contains(line, "(/.")) {
		return true
	}

	return false
}

// containsDigit checks if a string contains at least one digit
func containsDigit(s string) bool {
	for _, c := range s {
		if c >= '0' && c <= '9' {
			return true
		}
	}
	return false
}

// ============================================================================
// Step 4: StripTimestamps — strip log timestamp prefixes
// ============================================================================

// StripTimestamp strips a leading timestamp from a log line.
// Matches: [ISO8601 timestamp] or [date time]
func StripTimestamp(line string) string {
	if !strings.HasPrefix(line, "[") {
		return line
	}
	endBracket := strings.Index(line, "] ")
	if endBracket < 0 {
		return line
	}
	content := line[1:endBracket]
	// ISO 8601: 2026-06-23T10:30:01 or with timezone 2026-06-23T10:30:01+08:00
	// Simple date time: 2026-06-23 10:30:01
	if strings.Contains(content, "-") &&
		(strings.Contains(content, "T") || strings.Contains(content, ":")) {
		return line[endBracket+2:]
	}
	return line
}

// ============================================================================
// Step 5: CollapseBlanks — merge consecutive blank lines
// ============================================================================

// CollapseBlanks merges consecutive blank lines into a single blank line.
func CollapseBlanks(lines []string) []string {
	var result []string
	prevBlank := false
	for _, line := range lines {
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && prevBlank {
			continue // skip consecutive blank line
		}
		result = append(result, line)
		prevBlank = isBlank
	}
	if result == nil {
		result = []string{}
	}
	return result
}

// ============================================================================
// Step 6: DedupLines — duplicate line folding
// ============================================================================

// DedupLines folds identical repeated lines into [×N] format.
// threshold: only fold when repeat count >= threshold
func DedupLines(lines []string, threshold int) []string {
	if threshold < 2 {
		return lines
	}

	// Count occurrences of each line
	counts := make(map[string]int)
	for _, l := range lines {
		counts[l]++
	}

	var result []string
	emitted := make(map[string]bool)

	for _, l := range lines {
		count := counts[l]
		if count >= threshold {
			if !emitted[l] {
				result = append(result, fmt.Sprintf("%s  [×%d]", l, count))
				emitted[l] = true
			}
			// Subsequent duplicate lines are discarded
		} else {
			result = append(result, l)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result
}

// ============================================================================
// Step 7: LogTemplate — log template compression
// ============================================================================

// LogTemplate compresses lines that differ only in variable values into a template.
// minCount: template must appear >= minCount times to be compressed
func LogTemplate(lines []string, minCount int) []string {
	if minCount < 2 || len(lines) < minCount {
		return lines
	}

	// Generate template for each line (variable parts replaced with {})
	templates := make([]string, len(lines))
	for i, l := range lines {
		templates[i] = normalizeLine(l)
	}

	// Count occurrences of each template
	counts := make(map[string]int)
	for _, t := range templates {
		counts[t]++
	}

	// Only fold templates that contain {} (variable slots) and repeat >= minCount
	var result []string
	emitted := make(map[string]bool)

	for i, line := range lines {
		t := templates[i]
		shouldFold := counts[t] >= minCount && strings.Contains(t, "{}")

		if shouldFold {
			if !emitted[t] {
				sample := line
				if len(sample) > 100 {
					sample = sample[:100] + "…"
				}
				result = append(result, fmt.Sprintf("[×%d] %s  (e.g. %s)", counts[t], t, sample))
				emitted[t] = true
			}
			// Subsequent lines with the same template are discarded
		} else {
			result = append(result, line)
		}
	}
	if result == nil {
		result = []string{}
	}
	return result
}

// normalizeLine replaces variable parts in a line with {}
func normalizeLine(line string) string {
	var b strings.Builder
	first := true
	for _, tok := range strings.Fields(line) {
		if !first {
			b.WriteByte(' ')
		}
		first = false
		b.WriteString(templatize(tok))
	}
	return b.String()
}

// templatize replaces variable parts in a token with {}
func templatize(tok string) string {
	// key=value pairs: id=1001 → id={}
	if eqIdx := strings.Index(tok, "="); eqIdx > 0 && eqIdx < len(tok)-1 {
		key := tok[:eqIdx]
		val := tok[eqIdx+1:]
		if looksLikeID(val) {
			return key + "={}"
		}
	}

	// Entire token is a variable
	if isVariable(tok) {
		return "{}"
	}
	return tok
}

// isVariable determines if a token looks like a variable value
func isVariable(tok string) bool {
	core := trimPunctuation(tok)
	if core == "" {
		return false
	}
	return looksLikeID(core)
}

// trimPunctuation strips leading/trailing punctuation from a token
func trimPunctuation(tok string) string {
	start := 0
	end := len(tok)
	for start < end && isPunctuation(rune(tok[start])) {
		start++
	}
	for end > start && isPunctuation(rune(tok[end-1])) {
		end--
	}
	return tok[start:end]
}

// isPunctuation checks if a rune is common punctuation
func isPunctuation(r rune) bool {
	return r == ',' || r == '.' || r == ';' || r == ':' || r == '!' ||
		r == '?' || r == '(' || r == ')' || r == '[' || r == ']' ||
		r == '{' || r == '}' || r == '"' || r == '\'' || r == '`'
}

// looksLikeID 判断字符串是否形似 ID/数字/时间戳/哈希/版本号等可变值
func looksLikeID(s string) bool {
	digitCount := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digitCount++
		}
	}
	if digitCount == 0 {
		return false // pure words (ERROR, INFO) are structure, not variables
	}

	// 版本号（如 v1.0.0、v2.5.3-beta）视为可变量
	if isVersionNumber(s) {
		return true
	}

	// Numeric/timestamp/IP/float/date
	isNumericShaped := true
	for _, c := range s {
		if !((c >= '0' && c <= '9') || c == '.' || c == ':' || c == '-' || c == '/' || c == 'T' || c == '+') {
			isNumericShaped = false
			break
		}
	}
	if isNumericShaped {
		return true
	}

	// Number+unit: 1ms, 200ms, 5s, 3.2KB, 50%, 1.5x
	if isNumUnit(s) {
		return true
	}

	// Hex/UUID: length >= 8, contains >= 2 digits, all hex or -
	if len(s) >= 8 && digitCount >= 2 {
		isHexish := true
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') || c == '-') {
				isHexish = false
				break
			}
		}
		if isHexish {
			return true
		}
	}

	// Compound ID: contains digits + contains - or _ (req-1, pod-abc-7, session_42)
	hasSeparator := false
	isAlnumSep := true
	for _, c := range s {
		if c == '-' || c == '_' {
			hasSeparator = true
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '.') {
			isAlnumSep = false
			break
		}
	}
	if digitCount >= 1 && hasSeparator && isAlnumSep {
		return true
	}

	return false
}

// isVersionNumber 判断字符串是否为版本号（例如 v1.0.0、v2.5.3-beta、V10.5.3）
// 规则：以 v/V 开头并紧跟数字，其余字符为数字、点、连字符、下划线或加号。
func isVersionNumber(s string) bool {
	if len(s) < 2 {
		return false
	}
	if (s[0] != 'v' && s[0] != 'V') || !isDigitByte(s[1]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !(isDigitByte(c) || c == '.' || c == '-' || c == '_' || c == '+') {
			return false
		}
	}
	return true
}

// isNumUnit checks if a string is a number+unit format (1ms, 200ms, 3.2KB, 50%, 1.5x)
func isNumUnit(s string) bool {
	i := 0
	sawDigit := false
	for i < len(s) && (isDigitByte(s[i]) || s[i] == '.') {
		if isDigitByte(s[i]) {
			sawDigit = true
		}
		i++
	}
	if !sawDigit || i == 0 || i == len(s) {
		return false
	}
	suffix := s[i:]
	return len(suffix) <= 4 && allAlphaOrPercent(suffix)
}

// isDigitByte checks if a byte is a digit
func isDigitByte(b byte) bool {
	return b >= '0' && b <= '9'
}

// allAlphaOrPercent checks if all characters are alphabetic or %
func allAlphaOrPercent(s string) bool {
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '%') {
			return false
		}
	}
	return true
}

// ============================================================================
// Step 8: RelevantTruncate — relevance-scored truncation
// ============================================================================

// DefaultSignalWords returns the default signal words for relevance scoring
func DefaultSignalWords() []string {
	return []string{
		"error", "fail", "panic", "exception", "fatal", "traceback",
		"denied", "refused", "timeout", "cannot", "unable",
		"undefined", "unexpected", "warn", "abort", "missing",
		"not found", "stack", "segfault", "killed", "oom",
	}
}

// RelevantTruncate performs relevance-scored truncation of lines.
// Keeps the top `limit` lines by relevance score, preserving original order.
func RelevantTruncate(lines []string, limit int, maxLines int, cmd string, signalWords []string) []string {
	if len(lines) <= maxLines {
		return lines
	}

	// Extract query terms from the command
	terms := extractQueryTerms(cmd)

	// Score each line
	type scoredLine struct {
		index int
		line  string
		score int
	}
	scored := make([]scoredLine, len(lines))
	for i, l := range lines {
		scored[i] = scoredLine{i, l, scoreLine(l, terms, i, len(lines), signalWords)}
	}

	// Sort by score descending, then by original order for ties
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].index < scored[j].index
	})

	// Take top `limit` lines
	keepCount := limit
	if keepCount > len(scored) {
		keepCount = len(scored)
	}
	keepIndices := make([]int, keepCount)
	for i := range keepIndices {
		keepIndices[i] = scored[i].index
	}
	// Restore original order
	sort.Ints(keepIndices)

	// Build output
	dropped := len(lines) - keepCount
	result := make([]string, 0, keepCount+1)
	result = append(result, fmt.Sprintf(
		"[whale: kept %d of %d lines by relevance — %d elided]",
		keepCount, len(lines), dropped,
	))
	for _, idx := range keepIndices {
		result = append(result, lines[idx])
	}
	return result
}

// scoreLine scores a line for relevance
func scoreLine(line string, terms []string, idx int, total int, signalWords []string) int {
	lower := strings.ToLower(line)
	s := 0

	// Contains signal words → +5
	for _, w := range signalWords {
		if strings.Contains(lower, w) {
			s += 5
		}
	}

	// Contains query terms → +3
	for _, t := range terms {
		if strings.Contains(lower, t) {
			s += 3
		}
	}

	// First 3 lines are typically headers → bias toward keeping
	if idx < 3 {
		s += 4 - idx
	}

	// Last few lines are the most recent state → bias toward keeping
	if idx+3 >= total {
		s += 1
	}

	// Blank lines → penalty
	if strings.TrimSpace(line) == "" {
		s -= 1
	}

	return s
}

// extractQueryTerms extracts query terms from a command string
func extractQueryTerms(cmd string) []string {
	parts := strings.Fields(cmd)
	var terms []string
	for i, p := range parts {
		if i == 0 {
			continue // skip program name
		}
		if strings.HasPrefix(p, "-") {
			continue // skip flags
		}
		clean := strings.Trim(p, "\"'`()")
		if len(clean) >= 3 {
			terms = append(terms, strings.ToLower(clean))
		}
	}
	return terms
}

// ============================================================================
// Step 9: TOONEncode — JSON array dense encoding
// ============================================================================

// TryTOON 尝试将 JSON 数组转换为 TOON 紧凑格式。
// 条件：顶层为数组，每个元素为扁平对象，且所有对象共享相同的键顺序。
// 不满足条件时返回 nil（调用方保留原始输出）。
func TryTOON(text string) *string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "[") || !strings.HasSuffix(text, "]") {
		return nil
	}

	// Parse JSON array
	var arr []map[string]interface{}
	if err := json.Unmarshal([]byte(text), &arr); err != nil {
		return nil
	}
	if len(arr) == 0 {
		return nil
	}

	// Extract key order from first element
	headKeys := sortedKeys(arr[0])
	if len(headKeys) == 0 {
		return nil
	}

	// Verify all elements share the same key order and have primitive values
	for _, obj := range arr[1:] {
		keys := sortedKeys(obj)
		if !equalKeys(headKeys, keys) {
			return nil // keys don't match, fall back
		}
		for _, k := range headKeys {
			if !isPrimitive(obj[k]) {
				return nil // nested structure, fall back
			}
		}
	}

	// Generate TOON output
	var b strings.Builder
	fmt.Fprintf(&b, "items[%d]{%s}:\n", len(arr), strings.Join(headKeys, ","))
	for _, obj := range arr {
		var vals []string
		for _, k := range headKeys {
			vals = append(vals, formatValue(obj[k]))
		}
		fmt.Fprintf(&b, "  %s\n", strings.Join(vals, ","))
	}

	result := b.String()
	// 仅当数组含多个元素且 TOON 编码比原文更短时才采用；
	// 单元素数组即使更长也返回结构化编码，以保证一致性输出。
	if len(arr) > 1 && len(result) >= len(text) {
		return nil
	}
	return &result
}

// sortedKeys returns the keys of a map in sorted order
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// equalKeys checks if two key slices are equal
func equalKeys(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// isPrimitive checks if a value is a JSON primitive (string, number, bool, nil)
func isPrimitive(v interface{}) bool {
	switch v.(type) {
	case string, float64, int, int64, bool, nil:
		return true
	default:
		return false
	}
}

// formatValue formats a JSON primitive value for TOON output
func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		// Format integers without decimal point
		if val == float64(int64(val)) {
			return fmt.Sprintf("%d", int64(val))
		}
		return fmt.Sprintf("%g", val)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}
