// Package context — contextcollapsecompact
package context

import (
	"fmt"
	"strings"

	"github.com/anchorandfish/whale-code/internal/session"
)

// CollapseResult collapsecompactresult
type CollapseResult struct {
	// PreservedMessages is the message list after collapse (including summary message + preserved recent messages)
	PreservedMessages []session.Message
	// CollapsedSummary is the collapse summary text
	CollapsedSummary string
	// PreservedFiles holds file references extracted from collapsed messages
	PreservedFiles []string
	// PreservedDecisions holds key decisions extracted from collapsed messages
	PreservedDecisions []string
	// PreservedErrors holds errors and fix info extracted from collapsed messages
	PreservedErrors []string
	// PreservedPatterns holds code patterns extracted from collapsed messages
	PreservedPatterns []string
	// RemovedCount is the count of messages removed by collapse
	RemovedCount int
}

// ContextCollapser is the context collapse compactor
// modeled after a comparable CLI Context Collapse: creates projected collapse view, preserves fine-grained context markers
// compacts large history of messages into structured summary, preserving key file references and decision info
type ContextCollapser struct{}

// NewContextCollapser creates a context collapse compactor
func NewContextCollapser() *ContextCollapser {
	return &ContextCollapser{}
}

// Collapse executecollapsecompact
// preserveCount: preserve the most recent N messages from collapse
func (c *ContextCollapser) Collapse(messages []session.Message, preserveCount int) *CollapseResult {
	if len(messages) <= preserveCount {
		return &CollapseResult{
			PreservedMessages: messages,
			RemovedCount:      0,
		}
	}

	collapsed := messages[:len(messages)-preserveCount]
	preserved := messages[len(messages)-preserveCount:]

	// extract marker info from collapsed messages
	var files []string
	var decisions []string
	var userIntents []string
	var toolCalls []string
	var errors []string
	var patterns []string

	for _, msg := range collapsed {
		for _, block := range msg.Blocks {
			// Extract file references (loose matching, supports plain file names)
			extractFileRefs(block.Text, &files)
			if block.Type == "tool_result" {
				extractFileRefs(block.Output, &files)
			}

			// extract tool call info
			if block.Type == "tool_use" {
				toolCalls = append(toolCalls, block.Name)
			}

			// extract user intent
			if msg.Role == session.RoleUser && block.Type == "text" && block.Text != "" {
				intent := truncateRunes(strings.TrimSpace(block.Text), 80)
				if intent != "" && len(userIntents) < 10 {
					userIntents = append(userIntents, intent)
				}
			}

			// Extract key decisions
			if msg.Role == session.RoleAssistant && block.Type == "text" {
				text := block.Text
				if isDecisionText(text) {
					decision := truncateRunes(strings.TrimSpace(text), 100)
					if len(decisions) < 5 {
						decisions = append(decisions, decision)
					}
				}
				// Extract code patterns
				extractPatterns(text, &patterns)
			}

			// extract error and fix info
			if block.Type == "tool_result" && block.IsError {
				errInfo := truncateRunes(block.Output, 100)
				if errInfo != "" && len(errors) < 5 {
					errors = append(errors, errInfo)
				}
			}
			if msg.Role == session.RoleAssistant && block.Type == "text" {
				extractErrorFixes(block.Text, &errors)
			}
		}
	}

	// dedup and truncate file list
	files = uniqueStrings(files)
	if len(files) > 15 {
		files = files[:15]
	}

	// dedup and truncate patterns
	patterns = uniqueStrings(patterns)
	if len(patterns) > 5 {
		patterns = patterns[:5]
	}

	// build collapse summary
	summary := buildCollapseSummary(len(collapsed), userIntents, decisions, files, toolCalls, errors, patterns)

	// createsummarymessage
	summaryMsg := session.Message{
		Role: session.RoleSystem,
		Blocks: []session.ContentBlock{
			{Type: "text", Text: summary},
		},
	}

	result := make([]session.Message, 0, 1+len(preserved))
	result = append(result, summaryMsg)
	result = append(result, preserved...)

	return &CollapseResult{
		PreservedMessages:  result,
		CollapsedSummary:   summary,
		PreservedFiles:     files,
		PreservedDecisions: decisions,
		PreservedErrors:    errors,
		PreservedPatterns:  patterns,
		RemovedCount:       len(collapsed),
	}
}

// CollapseWithCacheBoundary is a cache-boundary-aware collapse compaction
// identifies system role messages (cache boundaries), collapse does not affect their position
// modeled after a comparable CLI cache-aware Context Collapse
func (c *ContextCollapser) CollapseWithCacheBoundary(messages []session.Message, preserveCount int) *CollapseResult {
	if len(messages) <= preserveCount {
		return &CollapseResult{
			PreservedMessages: messages,
			RemovedCount:      0,
		}
	}

	// separate system messages from collapsible messages
	var systemMsgs []session.Message
	var foldableMsgs []session.Message

	for _, msg := range messages {
		if msg.Role == session.RoleSystem {
			systemMsgs = append(systemMsgs, msg)
		} else {
			foldableMsgs = append(foldableMsgs, msg)
		}
	}

	// execute collapse on collapsible messages
	if len(foldableMsgs) <= preserveCount {
		// not enough collapsible messages, no collapse needed
		return &CollapseResult{
			PreservedMessages: messages,
			RemovedCount:      0,
		}
	}

	result := c.Collapse(foldableMsgs, preserveCount)

	// put system messages back at the front (keeping cache prefix unchanged)
	finalMessages := make([]session.Message, 0, len(systemMsgs)+len(result.PreservedMessages))
	finalMessages = append(finalMessages, systemMsgs...)
	finalMessages = append(finalMessages, result.PreservedMessages...)

	return &CollapseResult{
		PreservedMessages:  finalMessages,
		CollapsedSummary:   result.CollapsedSummary,
		PreservedFiles:     result.PreservedFiles,
		PreservedDecisions: result.PreservedDecisions,
		PreservedErrors:    result.PreservedErrors,
		PreservedPatterns:  result.PreservedPatterns,
		RemovedCount:       result.RemovedCount,
	}
}

// buildCollapseSummary builds the collapse summary
func buildCollapseSummary(
	collapsedCount int,
	userIntents, decisions, files, toolCalls, errors, patterns []string,
) string {
	var parts []string
	parts = append(parts, "[collapsesummary]")
	parts = append(parts, fmt.Sprintf("collapsed %d messages", collapsedCount))

	if len(userIntents) > 0 {
		parts = append(parts, "\nUser intent:")
		for i, intent := range userIntents {
			parts = append(parts, fmt.Sprintf("  %d. %s", i+1, intent))
		}
	}

	if len(decisions) > 0 {
		parts = append(parts, "\nKey decisions:")
		for _, d := range decisions {
			parts = append(parts, fmt.Sprintf("  - %s", d))
		}
	}

	if len(files) > 0 {
		parts = append(parts, fmt.Sprintf("\nFiles involved: %s", strings.Join(files, ", ")))
	}

	if len(toolCalls) > 0 {
		uniqueTools := uniqueStrings(toolCalls)
		parts = append(parts, fmt.Sprintf("\nTool calls: %s", strings.Join(uniqueTools, ", ")))
	}

	if len(errors) > 0 {
		parts = append(parts, "\nErrors and fixes:")
		for i, e := range errors {
			if i < 3 {
				parts = append(parts, fmt.Sprintf("  - %s", e))
			}
		}
	}

	if len(patterns) > 0 {
		parts = append(parts, "\nCode patterns:")
		for _, p := range patterns {
			parts = append(parts, fmt.Sprintf("  - %s", p))
		}
	}

	return strings.Join(parts, "\n")
}

// isDecisionText determines if text contains decision keywords
func isDecisionText(text string) bool {
	decisionKeywords := []string{
		"decided", "select", "approach", "adopted", "use",
		"decision", "chose", "selected",
		"strategy", "pattern", "implement",
	}
	lower := strings.ToLower(text)
	for _, kw := range decisionKeywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// extractPatterns extracts code patterns from text
func extractPatterns(text string, patterns *[]string) {
	patternKeywords := map[string]string{
		"singleton":           "singletonmode",
		"factory":             "factorymode",
		"observer":            "observermode",
		"strategy":            "strategymode",
		"adapter":             "adaptermode",
		"decorator":           "decoratormode",
		"middleware":          "middlewaremode",
		"pipeline":            "pipelinemode",
		"circuit breaker":     "circuit breaker pattern",
		"retry":               "retrymode",
		"cache":               "cachemode",
		"mutex":               "mutex",
		"goroutine":           "goroutineconcurrent",
		"channel":             "channel communication",
		"interface":           "interfaceabstract",
		"generic":             "generic",
		"recursion":           "recursive",
		"DFS":                 "depth-first search",
		"BFS":                 "breadth-first search",
		"binary":              "binary search",
		"dynamic programming": "dynamic programming",
		"greedy":              "greedyalgorithm",
	}

	lower := strings.ToLower(text)
	for kw, label := range patternKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) && len(*patterns) < 5 {
			*patterns = append(*patterns, label)
		}
	}
}

// extractErrorFixes extracts error fix info from text
func extractErrorFixes(text string, errors *[]string) {
	fixKeywords := []string{
		"fixed", "resolve", "fix", "resolved", "workaround",
		"error", "bug", "issue", "problem",
		"failed", "failure", "panic",
	}

	lower := strings.ToLower(text)
	for _, kw := range fixKeywords {
		if strings.Contains(lower, strings.ToLower(kw)) && len(*errors) < 5 {
			lines := strings.Split(text, "\n")
			for _, line := range lines {
				if strings.Contains(strings.ToLower(line), strings.ToLower(kw)) {
					fix := truncateRunes(strings.TrimSpace(line), 100)
					if fix != "" {
						*errors = append(*errors, fix)
					}
					if len(*errors) >= 5 {
						return
					}
				}
			}
		}
	}
}

// extractFileRefs extracts file references from text (loose matching)
// compared to extractPaths, also supports extracting file names without path separators (e.g. main.go)
func extractFileRefs(text string, paths *[]string) {
	// first use strict path extraction
	extractPaths(text, paths)

	// then use loose file name extraction, matching words with extensions
	extensions := []string{".go", ".rs", ".py", ".ts", ".js", ".md", ".json", ".yaml", ".toml"}
	words := strings.Fields(text)
	for _, w := range words {
		// remove common punctuation
		cleaned := strings.TrimRight(w, ".,;:!?()[]{}\"'")
		for _, ext := range extensions {
			if strings.HasSuffix(cleaned, ext) && len(cleaned) > len(ext) {
				*paths = append(*paths, cleaned)
				break
			}
		}
	}
}
