// Package tools — command-aware compression profiles for OutputCompressor
//
// Different commands produce different types of output and benefit from
// different compression strategies. CommandProfile allows per-command
// tuning of the compression pipeline.
package tools

import (
	"regexp"
)

// CommandProfile defines a compression profile for a specific command pattern
type CommandProfile struct {
	// CommandPattern is a regex pattern to match against the command string
	CommandPattern string
	// Profile is the compressor config override for matching commands
	Profile CompressorConfig
	// Description is a human-readable description of the profile
	Description string

	// compiled is the pre-compiled regex (lazily initialized)
	compiled *regexp.Regexp
}

// Match checks if a command matches this profile's pattern
func (p *CommandProfile) Match(cmd string) bool {
	if p.compiled == nil {
		var err error
		p.compiled, err = regexp.Compile(p.CommandPattern)
		if err != nil {
			return false
		}
	}
	return p.compiled.MatchString(cmd)
}

// DefaultCommandProfiles returns the default command compression profiles
func DefaultCommandProfiles() []CommandProfile {
	return []CommandProfile{
		{
			// Build commands: very long output, mostly noise
			CommandPattern: `^(npm run|cargo build|make|docker build|go build|pip install|yarn install|pnpm install)`,
			Profile: CompressorConfig{
				FilterNoise:       true,
				DedupLines:        true,
				LogTemplate:       true,
				RelevantTruncate:  true,
				RelevantKeepLines: 20, // build output: only keep 20 lines
				RelevantMaxLines:  30, // truncate above 30 lines
			},
			Description: "Build commands: aggressive compression, keep only errors and key info",
		},
		{
			// Test commands: keep failure info, fold passing info
			CommandPattern: `^(cargo test|pytest|go test|npm test|jest|vitest|mocha|ruby.*test)`,
			Profile: CompressorConfig{
				FilterNoise:       true,
				DedupLines:        true,
				RelevantTruncate:  true,
				RelevantKeepLines: 30,
				RelevantMaxLines:  50,
			},
			Description: "Test commands: keep failure info, fold passing info",
		},
		{
			// Git commands: moderate compression
			CommandPattern: `^git `,
			Profile: CompressorConfig{
				FilterNoise:       true,
				StripTimestamps:   false, // git log timestamps are meaningful
				DedupLines:        true,
				LogTemplate:       true,
				RelevantTruncate:  true,
				RelevantKeepLines: 50,
				RelevantMaxLines:  100,
			},
			Description: "Git commands: moderate compression, keep timestamps",
		},
		{
			// Log viewing: most aggressive compression
			CommandPattern: `^(kubectl logs|docker logs|tail|journalctl|cat .*/log)`,
			Profile: CompressorConfig{
				FilterNoise:       true,
				StripTimestamps:   true,
				DedupLines:        true,
				LogTemplate:       true,
				RelevantTruncate:  true,
				RelevantKeepLines: 30,
				RelevantMaxLines:  50,
			},
			Description: "Log viewing: most aggressive compression",
		},
		{
			// Short commands: no compression needed
			CommandPattern: `^(echo|pwd|whoami|date|hostname|uname|which|type|true|false)`,
			Profile: CompressorConfig{
				Enabled: false,
			},
			Description: "Short commands: no compression",
		},
		{
			// Package listing: moderate compression
			CommandPattern: `^(ls|dir|tree|find|du)`,
			Profile: CompressorConfig{
				FilterNoise:       true,
				DedupLines:        true,
				RelevantTruncate:  true,
				RelevantKeepLines: 50,
				RelevantMaxLines:  100,
			},
			Description: "Directory listing: moderate compression",
		},
	}
}
