package tools

import (
	"fmt"
	"strings"
	"testing"
)

// ============================================================================
// Step 1: StripANSI tests
// ============================================================================

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no ANSI",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "green color",
			input: "\x1b[32m✓\x1b[0m 42 tests passed",
			want:  "✓ 42 tests passed",
		},
		{
			name:  "bold text",
			input: "\x1b[1mbold\x1b[0m normal",
			want:  "bold normal",
		},
		{
			name:  "multiple colors",
			input: "\x1b[31mred\x1b[0m \x1b[32mgreen\x1b[0m \x1b[34mblue\x1b[0m",
			want:  "red green blue",
		},
		{
			name:  "cursor movement",
			input: "\x1b[2J\x1b[Hclear screen",
			want:  "clear screen",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "OSC title sequence",
			input: "\x1b]0;window title\x07prompt> ",
			want:  "prompt> ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.input)
			if got != tt.want {
				t.Errorf("StripANSI() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ============================================================================
// Step 2: StripCarriage tests
// ============================================================================

func TestStripCarriage(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "Windows line endings",
			input: "line1\r\nline2\r\n",
			want:  "line1\nline2\n",
		},
		{
			name:  "bare CR",
			input: "hello\rworld",
			want:  "helloworld",
		},
		{
			name:  "no CR",
			input: "hello\nworld",
			want:  "hello\nworld",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripCarriage(tt.input)
			if got != tt.want {
				t.Errorf("StripCarriage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ============================================================================
// Step 3: FilterNoise tests
// ============================================================================

func TestFilterNoise(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "spinner characters",
			input: []string{"⠋ Loading...", "real output", "⠙ Loading..."},
			want:  []string{"real output"},
		},
		{
			name:  "progress bar with blocks",
			input: []string{"████████░░ 80%", "real output"},
			want:  []string{"real output"},
		},
		{
			name:  "git hint",
			input: []string{"hint: Using 'master' as the default branch", "real output"},
			want:  []string{"real output"},
		},
		{
			name:  "npm warn deprecated",
			input: []string{"npm warn deprecated package@1.0.0", "real output"},
			want:  []string{"real output"},
		},
		{
			name:  "node_modules stack frame",
			input: []string{"at something (node_modules/express/index.js:42:5)", "real output"},
			want:  []string{"real output"},
		},
		{
			name:  "keep normal lines",
			input: []string{"error: something failed", "warning: check this", "all good"},
			want:  []string{"error: something failed", "warning: check this", "all good"},
		},
		{
			name:  "keep blank lines (handled by CollapseBlanks)",
			input: []string{"", "real output", ""},
			want:  []string{"", "real output", ""},
		},
		{
			name:  "percentage in short line",
			input: []string{"67%", "real output"},
			want:  []string{"real output"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FilterNoise(tt.input, nil)
			if len(got) != len(tt.want) {
				t.Errorf("FilterNoise() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("FilterNoise()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsNoiseLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"⠋ Loading...", true},
		{"████░░░ 67%", true},
		{"hint: Using 'master'", true},
		{"npm warn deprecated foo@1.0.0", true},
		{"npm notice some notice", true},
		{"at foo (node_modules/bar/index.js:1:1)", true},
		{"[tsconfig-paths] something", true},
		{"real output line", false},
		{"ERROR: something failed", false},
		{"", false},
		{"  indented real line", false},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := IsNoiseLine(tt.line)
			if got != tt.want {
				t.Errorf("IsNoiseLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// ============================================================================
// Step 4: StripTimestamps tests
// ============================================================================

func TestStripTimestamp(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "ISO 8601 timestamp",
			input: "[2026-06-23T10:30:01.123Z] INFO: Server started",
			want:  "INFO: Server started",
		},
		{
			name:  "simple date time",
			input: "[2026-06-23 10:30:01] WARN: Cache miss",
			want:  "WARN: Cache miss",
		},
		{
			name:  "no timestamp",
			input: "ERROR: Database timeout",
			want:  "ERROR: Database timeout",
		},
		{
			name:  "brackets but not timestamp",
			input: "[ERROR] Something went wrong",
			want:  "[ERROR] Something went wrong",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "timestamp with timezone",
			input: "[2026-06-23T10:30:01+08:00] INFO: Message",
			want:  "INFO: Message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripTimestamp(tt.input)
			if got != tt.want {
				t.Errorf("StripTimestamp() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ============================================================================
// Step 5: CollapseBlanks tests
// ============================================================================

func TestCollapseBlanks(t *testing.T) {
	tests := []struct {
		name  string
		input []string
		want  []string
	}{
		{
			name:  "consecutive blanks",
			input: []string{"line1", "", "", "", "line2"},
			want:  []string{"line1", "", "line2"},
		},
		{
			name:  "no blanks",
			input: []string{"line1", "line2", "line3"},
			want:  []string{"line1", "line2", "line3"},
		},
		{
			name:  "single blank",
			input: []string{"line1", "", "line2"},
			want:  []string{"line1", "", "line2"},
		},
		{
			name:  "trailing blanks",
			input: []string{"line1", "", ""},
			want:  []string{"line1", ""},
		},
		{
			name:  "leading blanks",
			input: []string{"", "", "line1"},
			want:  []string{"", "line1"},
		},
		{
			name:  "whitespace-only lines",
			input: []string{"line1", "   ", "\t", "line2"},
			want:  []string{"line1", "   ", "line2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CollapseBlanks(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("CollapseBlanks() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("CollapseBlanks()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ============================================================================
// Step 6: DedupLines tests
// ============================================================================

func TestDedupLines(t *testing.T) {
	tests := []struct {
		name      string
		input     []string
		threshold int
		want      []string
	}{
		{
			name:      "fold 3+ duplicates",
			input:     []string{"retrying...", "retrying...", "retrying..."},
			threshold: 3,
			want:      []string{"retrying...  [×3]"},
		},
		{
			name:      "2 duplicates below threshold",
			input:     []string{"line1", "line1"},
			threshold: 3,
			want:      []string{"line1", "line1"},
		},
		{
			name:      "mixed unique and duplicate",
			input:     []string{"header", "retrying...", "retrying...", "retrying...", "footer"},
			threshold: 3,
			want:      []string{"header", "retrying...  [×3]", "footer"},
		},
		{
			name:      "no duplicates",
			input:     []string{"line1", "line2", "line3"},
			threshold: 3,
			want:      []string{"line1", "line2", "line3"},
		},
		{
			name:      "threshold 2",
			input:     []string{"same", "same"},
			threshold: 2,
			want:      []string{"same  [×2]"},
		},
		{
			name:      "threshold 1 (disabled)",
			input:     []string{"same", "same", "same"},
			threshold: 1,
			want:      []string{"same", "same", "same"},
		},
		{
			name:      "empty input",
			input:     []string{},
			threshold: 3,
			want:      []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DedupLines(tt.input, tt.threshold)
			if len(got) != len(tt.want) {
				t.Errorf("DedupLines() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("DedupLines()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

// ============================================================================
// Step 7: LogTemplate tests
// ============================================================================

func TestLogTemplate(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		minCount int
		want     int // expected number of output lines
	}{
		{
			name: "template with varying IDs",
			input: []string{
				"2026-06-21T10:30:01 worker 4821 processing job a3f2c1d4e50",
				"2026-06-21T10:30:02 worker 4822 processing job b1c9d2e3f4a",
				"2026-06-21T10:30:03 worker 4823 processing job d7e0f1a2b3c",
			},
			minCount: 3,
			want:     1, // all 3 lines should collapse to 1 template
		},
		{
			name: "key=value pairs",
			input: []string{
				"request id=1001 status=ok",
				"request id=1002 status=ok",
				"request id=1003 status=ok",
			},
			minCount: 3,
			want:     1,
		},
		{
			name: "no template match (all unique)",
			input: []string{
				"first line",
				"second line",
				"third line",
			},
			minCount: 3,
			want:     3,
		},
		{
			name:     "too few lines",
			input:    []string{"only one line"},
			minCount: 3,
			want:     1,
		},
		{
			name: "version numbers preserved",
			input: []string{
				"using version v1.0.0",
				"using version v2.0.0",
				"using version v3.0.0",
			},
			minCount: 3,
			want:     1, // version numbers are treated as variables
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LogTemplate(tt.input, tt.minCount)
			if len(got) != tt.want {
				t.Errorf("LogTemplate() returned %d lines, want %d lines: %v", len(got), tt.want, got)
			}
		})
	}
}

func TestLooksLikeID(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"12345", true},
		{"a3f2c1d4e50", true}, // hex ID
		{"2026-06-23", true},  // date
		{"10:30:01", true},    // time
		{"1ms", true},         // number+unit
		{"200ms", true},
		{"3.2KB", true},
		{"50%", true},
		{"req-1", true},      // compound ID
		{"session_42", true}, // compound ID
		{"ERROR", false},     // pure word
		{"INFO", false},
		{"hello", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := looksLikeID(tt.input)
			if got != tt.want {
				t.Errorf("looksLikeID(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// ============================================================================
// Step 8: RelevantTruncate tests
// ============================================================================

func TestRelevantTruncate(t *testing.T) {
	t.Run("short output not truncated", func(t *testing.T) {
		lines := make([]string, 50)
		for i := range lines {
			lines[i] = fmt.Sprintf("line %d", i)
		}
		result := RelevantTruncate(lines, 50, 100, "cat file.txt", DefaultSignalWords())
		if len(result) != 50 {
			t.Errorf("expected 50 lines, got %d", len(result))
		}
	})

	t.Run("long output truncated with errors preserved", func(t *testing.T) {
		lines := make([]string, 200)
		for i := range lines {
			lines[i] = fmt.Sprintf("normal line %d", i)
		}
		// Insert error lines
		lines[50] = "ERROR: database connection timeout"
		lines[100] = "FATAL: out of memory"

		result := RelevantTruncate(lines, 50, 100, "cat file.txt", DefaultSignalWords())
		if len(result) != 51 { // 50 kept + 1 header line
			t.Errorf("expected 51 lines, got %d", len(result))
		}

		// Check header line
		if !strings.Contains(result[0], "[whale:") {
			t.Errorf("expected header line, got %q", result[0])
		}

		// Check error lines are preserved
		foundError := false
		foundFatal := false
		for _, l := range result {
			if strings.Contains(l, "ERROR: database connection timeout") {
				foundError = true
			}
			if strings.Contains(l, "FATAL: out of memory") {
				foundFatal = true
			}
		}
		if !foundError {
			t.Error("ERROR line should be preserved")
		}
		if !foundFatal {
			t.Error("FATAL line should be preserved")
		}
	})

	t.Run("query terms boost relevance", func(t *testing.T) {
		lines := make([]string, 200)
		for i := range lines {
			lines[i] = fmt.Sprintf("normal line %d", i)
		}
		lines[50] = "timeout after 30s"
		lines[100] = "connection refused"

		result := RelevantTruncate(lines, 50, 100, "grep timeout logs.txt", DefaultSignalWords())
		// "timeout" line should be preserved due to query term match
		foundTimeout := false
		for _, l := range result {
			if strings.Contains(l, "timeout after 30s") {
				foundTimeout = true
			}
		}
		if !foundTimeout {
			t.Error("line matching query term 'timeout' should be preserved")
		}
	})
}

// ============================================================================
// Step 9: TOONEncode tests
// ============================================================================

func TestTryTOON(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantTOON   bool   // whether TOON encoding should be applied
		wantPrefix string // expected prefix of the result (if TOON applied)
	}{
		{
			name:       "flat JSON array",
			input:      `[{"name":"Alice","age":30},{"name":"Bob","age":25},{"name":"Carol","age":35}]`,
			wantTOON:   true,
			wantPrefix: "items[3]",
		},
		{
			name:     "not JSON array",
			input:    "just plain text",
			wantTOON: false,
		},
		{
			name:     "empty array",
			input:    "[]",
			wantTOON: false,
		},
		{
			name:     "nested objects",
			input:    `[{"name":"Alice","addr":{"city":"NYC"}},{"name":"Bob","addr":{"city":"LA"}}]`,
			wantTOON: false,
		},
		{
			name:     "inconsistent keys",
			input:    `[{"name":"Alice","age":30},{"name":"Bob","email":"bob@test.com"}]`,
			wantTOON: false,
		},
		{
			name:       "single element",
			input:      `[{"name":"Alice","age":30}]`,
			wantTOON:   true,
			wantPrefix: "items[1]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := TryTOON(tt.input)
			if tt.wantTOON {
				if result == nil {
					t.Error("TryTOON() returned nil, expected TOON encoding")
					return
				}
				if !strings.HasPrefix(*result, tt.wantPrefix) {
					t.Errorf("TryTOON() = %q, want prefix %q", *result, tt.wantPrefix)
				}
			} else {
				if result != nil {
					t.Errorf("TryTOON() = %q, expected nil", *result)
				}
			}
		})
	}
}

// ============================================================================
// Pipeline tests
// ============================================================================

func TestCompressPipeline(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	t.Run("empty input", func(t *testing.T) {
		result := compressor.Compress("", "echo hello")
		if result.Output != "" {
			t.Errorf("expected empty output, got %q", result.Output)
		}
	})

	t.Run("disabled compressor", func(t *testing.T) {
		disabledConfig := DefaultCompressorConfig()
		disabledConfig.Enabled = false
		c := NewOutputCompressor(disabledConfig)
		input := "\x1b[32mcolored\x1b[0m output"
		result := c.Compress(input, "echo test")
		if result.Output != input {
			t.Errorf("disabled compressor should return original input")
		}
	})

	t.Run("full pipeline with ANSI and noise", func(t *testing.T) {
		input := "\x1b[32m✓\x1b[0m Build successful\n" +
			"⠋ Compiling...\n" +
			"npm warn deprecated old-package@1.0.0\n" +
			"hint: Using 'master' as default\n" +
			"\n\n\n" +
			"retrying...\nretrying...\nretrying...\n" +
			"real output here"

		result := compressor.Compress(input, "npm run build")
		if result.OriginalLen == 0 {
			t.Error("expected non-zero original length")
		}
		if result.CompressedLen >= result.OriginalLen {
			t.Errorf("expected compression, got original=%d compressed=%d", result.OriginalLen, result.CompressedLen)
		}
		// Should not contain ANSI codes
		if strings.Contains(result.Output, "\x1b[") {
			t.Error("output should not contain ANSI codes")
		}
		// Should not contain spinner
		if strings.Contains(result.Output, "⠋") {
			t.Error("output should not contain spinner characters")
		}
		// Should not contain npm warn
		if strings.Contains(result.Output, "npm warn") {
			t.Error("output should not contain npm warnings")
		}
		// Should contain dedup marker
		if !strings.Contains(result.Output, "[×3]") {
			t.Error("output should contain dedup marker [×3]")
		}
	})

	t.Run("all steps disabled", func(t *testing.T) {
		allOff := CompressorConfig{Enabled: true}
		c := NewOutputCompressor(allOff)
		input := "\x1b[32mcolored\x1b[0m output"
		result := c.Compress(input, "echo test")
		if result.Output != input {
			t.Errorf("all steps disabled should return original input")
		}
	})
}

func TestCompressWithProfile(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	t.Run("build command profile", func(t *testing.T) {
		// Generate long build output
		var lines []string
		for i := 0; i < 50; i++ {
			lines = append(lines, fmt.Sprintf("compiling module %d...", i))
		}
		lines = append(lines, "ERROR: build failed at module 42")
		input := strings.Join(lines, "\n")

		result := compressor.CompressWithProfile(input, "npm run build")
		// Build profile should apply aggressive truncation
		if result.CompressedLen >= result.OriginalLen {
			t.Errorf("expected compression for build command, got original=%d compressed=%d",
				result.OriginalLen, result.CompressedLen)
		}
	})

	t.Run("short command no compression", func(t *testing.T) {
		input := "hello world"
		result := compressor.CompressWithProfile(input, "echo hello")
		if result.Output != input {
			t.Errorf("echo command should not be compressed, got %q", result.Output)
		}
	})

	t.Run("git command preserves timestamps", func(t *testing.T) {
		input := "[2026-06-23T10:30:01] commit abc123\nreal git output"
		result := compressor.CompressWithProfile(input, "git log")
		// Git profile has StripTimestamps=false
		if !strings.Contains(result.Output, "2026-06-23") {
			t.Error("git profile should preserve timestamps")
		}
	})

	t.Run("unknown command uses default profile", func(t *testing.T) {
		input := "some output with \x1b[32mcolor\x1b[0m"
		result := compressor.CompressWithProfile(input, "unknown_command")
		if strings.Contains(result.Output, "\x1b[") {
			t.Error("default profile should still strip ANSI")
		}
	})
}

// ============================================================================
// Metrics tests
// ============================================================================

func TestCompressorMetrics(t *testing.T) {
	metrics := NewCompressorMetrics()

	// Record some operations
	metrics.Record("npm run build", []string{"strip_ansi", "filter_noise"}, map[string]int{"filter_noise": 5}, 1000, 500, 125)
	metrics.Record("git status", []string{"strip_ansi"}, map[string]int{}, 2000, 1500, 125)

	snap := metrics.Snapshot()
	if snap.TotalCompressions != 2 {
		t.Errorf("expected 2 compressions, got %d", snap.TotalCompressions)
	}
	if snap.TotalOriginalBytes != 3000 {
		t.Errorf("expected 3000 original bytes, got %d", snap.TotalOriginalBytes)
	}
	if snap.TotalTokensSaved != 250 {
		t.Errorf("expected 250 tokens saved, got %d", snap.TotalTokensSaved)
	}

	// Check step stats
	if stat, ok := snap.StepStats["strip_ansi"]; !ok || stat.Applications != 2 {
		t.Error("strip_ansi should have 2 applications")
	}
	if stat, ok := snap.StepStats["filter_noise"]; !ok || stat.Applications != 1 {
		t.Error("filter_noise should have 1 application")
	}

	// Check command stats
	if cstat, ok := snap.CommandStats["npm"]; !ok || cstat.Invocations != 1 {
		t.Error("npm command should have 1 invocation")
	}

	// Format report should not panic
	report := metrics.FormatReport()
	if report == "" {
		t.Error("FormatReport() should not return empty string")
	}
}

// ============================================================================
// Real-world scenario tests
// ============================================================================

func TestCompressWithRealNPMOutput(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	// Simulated npm build output
	input := "\x1b[32m✓\x1b[0m \x1b[1m42 modules compiled\x1b[0m\n" +
		"npm warn deprecated package@1.0.0: use package@2.0.0 instead\n" +
		"npm warn deprecated another@3.0.0: no longer maintained\n" +
		"⠋ Compiling...\n" +
		"████████░░░ 80%\n" +
		"hint: Using 'master' as the default branch name\n" +
		"\n\n\n" +
		"Compiling src/index.ts...\n" +
		"Compiling src/utils.ts...\n" +
		"Compiling src/handler.ts...\n" +
		"Compiling src/handler.ts...\n" +
		"Compiling src/handler.ts...\n" +
		"Compiling src/handler.ts...\n" +
		"Compiling src/handler.ts...\n" +
		"ERROR: TypeScript compilation failed in handler.ts:42\n" +
		"Build completed with 1 error"

	result := compressor.CompressWithProfile(input, "npm run build")

	// Verify key properties
	if strings.Contains(result.Output, "\x1b[") {
		t.Error("should strip ANSI codes")
	}
	if strings.Contains(result.Output, "npm warn") {
		t.Error("should filter npm warnings")
	}
	if strings.Contains(result.Output, "⠋") {
		t.Error("should filter spinner")
	}
	if strings.Contains(result.Output, "████") {
		t.Error("should filter progress bar")
	}
	if !strings.Contains(result.Output, "ERROR:") {
		t.Error("should preserve ERROR line")
	}
	if result.CompressedLen >= result.OriginalLen {
		t.Errorf("expected compression: original=%d compressed=%d", result.OriginalLen, result.CompressedLen)
	}
}

func TestCompressWithRealGitStatus(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	// Simulated git status output with many files
	input := "On branch main\n" +
		"hint: Using 'master' as the default branch name\n" +
		"Changes not staged for commit:\n" +
		"  modified:   config.json\n" +
		"  modified:   main.go\n" +
		"  modified:   utils.go\n" +
		"  modified:   handler.go\n" +
		"  modified:   server.go\n" +
		"  modified:   client.go\n" +
		"Untracked files:\n" +
		"  new_feature.go\n" +
		"  test_new_feature.go"

	result := compressor.CompressWithProfile(input, "git status")

	// Git hint should be filtered
	if strings.Contains(result.Output, "hint:") {
		t.Error("should filter git hints")
	}
	// Main content should be preserved
	if !strings.Contains(result.Output, "On branch main") {
		t.Error("should preserve branch info")
	}
}

func TestCompressWithRealCargoTest(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	// Simulated cargo test output with many passing tests
	var lines []string
	lines = append(lines, "running 100 tests")
	for i := 0; i < 90; i++ {
		lines = append(lines, fmt.Sprintf("test test_%04d ... ok", i))
	}
	lines = append(lines, "test test_0090 ... FAILED")
	lines = append(lines, "test test_0091 ... ok")
	lines = append(lines, "test test_0092 ... ok")
	lines = append(lines, "test test_0093 ... ok")
	lines = append(lines, "test test_0094 ... ok")
	lines = append(lines, "test test_0095 ... ok")
	lines = append(lines, "test test_0096 ... ok")
	lines = append(lines, "test test_0097 ... ok")
	lines = append(lines, "test test_0098 ... ok")
	lines = append(lines, "test test_0099 ... ok")
	lines = append(lines, "", "failures:", "    test_0090")
	lines = append(lines, "", "test result: FAILED. 99 passed; 1 failed; 0 ignored")

	input := strings.Join(lines, "\n")
	result := compressor.CompressWithProfile(input, "cargo test")

	// FAILED line should be preserved
	if !strings.Contains(result.Output, "FAILED") {
		t.Error("should preserve FAILED line")
	}
	// Should be significantly compressed
	if result.CompressedLen >= result.OriginalLen/2 {
		t.Errorf("expected significant compression: original=%d compressed=%d", result.OriginalLen, result.CompressedLen)
	}
}

// ============================================================================
// Unicode / special character tests
// ============================================================================

func TestCompressUnicode(t *testing.T) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	t.Run("Chinese output", func(t *testing.T) {
		input := "构建成功\n编译完成\n测试通过"
		result := compressor.Compress(input, "echo test")
		if result.Output != input {
			t.Errorf("Chinese text should pass through unchanged")
		}
	})

	t.Run("emoji output", func(t *testing.T) {
		input := "✅ Success\n❌ Failure\n⚠️ Warning"
		result := compressor.Compress(input, "echo test")
		// Emoji should be preserved (not treated as spinner)
		if !strings.Contains(result.Output, "✅") {
			t.Error("emoji should be preserved")
		}
	})
}

// ============================================================================
// Benchmark tests
// ============================================================================

func BenchmarkStripANSI_1KB(b *testing.B) {
	input := strings.Repeat("\x1b[32m✓\x1b[0m test output line\n", 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripANSI(input)
	}
}

func BenchmarkFilterNoise_100Lines(b *testing.B) {
	lines := make([]string, 100)
	for i := range lines {
		if i%5 == 0 {
			lines[i] = "⠋ Loading..."
		} else {
			lines[i] = "normal output line"
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		FilterNoise(lines, nil)
	}
}

func BenchmarkDedupLines_1000Lines(b *testing.B) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i%10) // 10 unique lines, each repeated 100 times
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		DedupLines(lines, 3)
	}
}

func BenchmarkLogTemplate_500Lines(b *testing.B) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("2026-06-21T10:30:%02d worker %d processing job %08x", i%60, 4000+i, i*0x12345678)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		LogTemplate(lines, 3)
	}
}

func BenchmarkRelevantTruncate_500Lines(b *testing.B) {
	lines := make([]string, 500)
	for i := range lines {
		lines[i] = fmt.Sprintf("normal line %d", i)
	}
	lines[50] = "ERROR: something failed"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		RelevantTruncate(lines, 50, 100, "cat file.txt", DefaultSignalWords())
	}
}

func BenchmarkTOONEncode_50Records(b *testing.B) {
	var records []string
	for i := 0; i < 50; i++ {
		records = append(records, fmt.Sprintf(`{"name":"user%d","age":%d,"role":"member"}`, i, 20+i%40))
	}
	input := "[" + strings.Join(records, ",") + "]"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		TryTOON(input)
	}
}

func BenchmarkFullPipeline_1000Lines(b *testing.B) {
	config := DefaultCompressorConfig()
	compressor := NewOutputCompressor(config)

	var lines []string
	for i := 0; i < 1000; i++ {
		switch i % 10 {
		case 0:
			lines = append(lines, "\x1b[32m✓\x1b[0m Build step "+fmt.Sprint(i))
		case 1:
			lines = append(lines, "⠋ Compiling...")
		case 2:
			lines = append(lines, "npm warn deprecated package@1.0.0")
		case 3:
			lines = append(lines, "")
		default:
			lines = append(lines, fmt.Sprintf("normal output line %d", i))
		}
	}
	input := strings.Join(lines, "\n")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		compressor.Compress(input, "npm run build")
	}
}
