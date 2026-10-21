// Package command — output style 渲染配置单元测试。
package command

import (
	"strings"
	"testing"
)

// TestGetOutputStyleRenderConfig 验证各 style 对应的渲染配置。
// 契约：N14 自定义边界（非 02 §2.2.2，M4 修复），N9 消费此配置。
func TestGetOutputStyleRenderConfig(t *testing.T) {
	tests := []struct {
		name            string
		style           string
		wantThinking    bool
		wantExplanation bool
		wantDiffContext int
		wantCollapse    bool
		wantMaxLines    int
	}{
		{
			name:            "concise 简洁模式",
			style:           "concise",
			wantThinking:    false,
			wantExplanation: false,
			wantDiffContext: 3,
			wantCollapse:    true,
			wantMaxLines:    50,
		},
		{
			name:            "default 默认模式",
			style:           "default",
			wantThinking:    true,
			wantExplanation: true,
			wantDiffContext: 5,
			wantCollapse:    false,
			wantMaxLines:    200,
		},
		{
			name:            "verbose 详细模式",
			style:           "verbose",
			wantThinking:    true,
			wantExplanation: true,
			wantDiffContext: 10,
			wantCollapse:    false,
			wantMaxLines:    500,
		},
		{
			name:            "technical 技术模式",
			style:           "technical",
			wantThinking:    true,
			wantExplanation: true,
			wantDiffContext: 8,
			wantCollapse:    false,
			wantMaxLines:    300,
		},
		{
			name:            "未知值 → default 配置",
			style:           "unknown",
			wantThinking:    true,
			wantExplanation: true,
			wantDiffContext: 5,
			wantCollapse:    false,
			wantMaxLines:    200,
		},
		{
			name:            "空字符串 → default 配置",
			style:           "",
			wantThinking:    true,
			wantExplanation: true,
			wantDiffContext: 5,
			wantCollapse:    false,
			wantMaxLines:    200,
		},
		{
			name:            "大小写不敏感 CONCISE",
			style:           "CONCISE",
			wantThinking:    false,
			wantExplanation: false,
			wantDiffContext: 3,
			wantCollapse:    true,
			wantMaxLines:    50,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := GetOutputStyleRenderConfig(tt.style)
			if cfg.ShowThinking != tt.wantThinking {
				t.Errorf("ShowThinking = %v, want %v", cfg.ShowThinking, tt.wantThinking)
			}
			if cfg.ShowCodeExplanation != tt.wantExplanation {
				t.Errorf("ShowCodeExplanation = %v, want %v", cfg.ShowCodeExplanation, tt.wantExplanation)
			}
			if cfg.ShowDiffContext != tt.wantDiffContext {
				t.Errorf("ShowDiffContext = %d, want %d", cfg.ShowDiffContext, tt.wantDiffContext)
			}
			if cfg.CollapseLongOutput != tt.wantCollapse {
				t.Errorf("CollapseLongOutput = %v, want %v", cfg.CollapseLongOutput, tt.wantCollapse)
			}
			if cfg.MaxDisplayLines != tt.wantMaxLines {
				t.Errorf("MaxDisplayLines = %d, want %d", cfg.MaxDisplayLines, tt.wantMaxLines)
			}
		})
	}
}

// TestOutputStyleDescriptionChinese 验证 OutputStyleDescription 返回中文描述（G14 修复）。
func TestOutputStyleDescriptionChinese(t *testing.T) {
	tests := []struct {
		style    string
		contains string
	}{
		{"concise", "简洁模式"},
		{"verbose", "详细模式"},
		{"technical", "技术模式"},
	}

	for _, tt := range tests {
		t.Run(tt.style, func(t *testing.T) {
			desc := OutputStyleDescription(tt.style)
			if desc == "" {
				t.Errorf("OutputStyleDescription(%q) returned empty", tt.style)
			}
			if !strings.Contains(desc, tt.contains) {
				t.Errorf("OutputStyleDescription(%q) = %q, want contains %q", tt.style, desc, tt.contains)
			}
		})
	}
}

// TestOutputStyleDescriptionDefault 验证 default style 返回空字符串。
func TestOutputStyleDescriptionDefault(t *testing.T) {
	if desc := OutputStyleDescription("default"); desc != "" {
		t.Errorf("OutputStyleDescription(default) = %q, want empty", desc)
	}
	if desc := OutputStyleDescription(""); desc != "" {
		t.Errorf("OutputStyleDescription(empty) = %q, want empty", desc)
	}
}
