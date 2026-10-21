package tools

import (
	"strings"
	"testing"
)

func TestUltraplanManager_New_DefaultDisabled(t *testing.T) {
	up := NewUltraplanManager()
	if up.Enabled() {
		t.Error("初始状态应为禁用")
	}
	if up.GetSystemPrompt() != "" {
		t.Error("禁用时不返回提示词")
	}
	if up.StepCount() != 0 {
		t.Error("初始步骤数应为 0")
	}
}

func TestUltraplanManager_Toggle_Enable(t *testing.T) {
	up := NewUltraplanManager()
	result := up.Toggle()
	if !up.Enabled() {
		t.Error("Toggle 后应启用")
	}
	if !strings.Contains(result, "已启用") {
		t.Errorf("should return启用消息，actual: %s", result)
	}
}

func TestUltraplanManager_Toggle_Disable(t *testing.T) {
	up := NewUltraplanManager()
	up.Toggle() // 先启用
	result := up.Toggle()
	if up.Enabled() {
		t.Error("Toggle 后应禁用")
	}
	if !strings.Contains(result, "已禁用") {
		t.Errorf("should return禁用消息，actual: %s", result)
	}
}

func TestUltraplanManager_Toggle_Multiple(t *testing.T) {
	up := NewUltraplanManager()
	for i := 0; i < 5; i++ {
		up.Toggle()
	}
	if !up.Enabled() {
		t.Error("5 次切换后应为启用")
	}
	up.Toggle()
	if up.Enabled() {
		t.Error("6 次切换后应为禁用")
	}
}

func TestUltraplanManager_GetSystemPrompt_Enabled(t *testing.T) {
	up := NewUltraplanManager()
	up.Toggle()
	prompt := up.GetSystemPrompt()
	if prompt == "" {
		t.Error("启用时should return non-empty提示词")
	}
	if !strings.Contains(prompt, "Ultraplan") {
		t.Error("提示词should contain模式名称")
	}
	if !strings.Contains(prompt, "ANALYZE") {
		t.Error("提示词should contain分析步骤")
	}
}

func TestUltraplanManager_GetSystemPrompt_Disabled(t *testing.T) {
	up := NewUltraplanManager()
	if up.GetSystemPrompt() != "" {
		t.Error("禁用时should return empty字符串")
	}
}

func TestUltraplanManager_IncrementStep(t *testing.T) {
	up := NewUltraplanManager()
	up.Toggle()
	up.IncrementStep()
	up.IncrementStep()
	up.IncrementStep()
	if up.StepCount() != 3 {
		t.Errorf("步骤计数应为 3，actual: %d", up.StepCount())
	}
}

func TestUltraplanManager_Status_Format(t *testing.T) {
	up := NewUltraplanManager()
	up.Toggle()
	up.IncrementStep()
	up.IncrementStep()
	status := up.Status()
	if !strings.Contains(status, "启用") {
		t.Error("状态should contain'启用'")
	}
	if !strings.Contains(status, "2") {
		t.Error("状态should contain步骤数")
	}
}

func TestUltraplanManager_Status_Disabled(t *testing.T) {
	up := NewUltraplanManager()
	status := up.Status()
	if !strings.Contains(status, "未启用") {
		t.Errorf("禁用状态should contain'未启用'，actual: %s", status)
	}
}
