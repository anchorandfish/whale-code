// Package tools — Ultraplan mode manager
// UltraplanManager manages the Ultraplan mode enable/disable state and system prompt injection
package tools

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// UltraplanManager manages the Ultraplan mode state
// Modeled after a comparable CLI's ultraplan feature, controls whether AI runs in multi-step planning mode
type UltraplanManager struct {
	mu        sync.RWMutex
	enabled   bool
	stepCount int
	startedAt time.Time
	steps     []UltraplanStep
}

// UltraplanStep represents a step in the execution plan
type UltraplanStep struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Dependencies []string `json:"dependencies,omitempty"`
	Status       string   `json:"status"` // "pending", "running", "completed", "failed"
}

// NewUltraplanManager creates an Ultraplan mode manager
func NewUltraplanManager() *UltraplanManager {
	return &UltraplanManager{}
}

// AddStep adds a step to the plan with dependency validation
func (u *UltraplanManager) AddStep(step UltraplanStep) error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if step.ID == "" {
		return fmt.Errorf("step ID cannot be empty")
	}

	for _, dep := range step.Dependencies {
		found := false
		for _, existing := range u.steps {
			if existing.ID == dep {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("dependency %q does not exist for step %q", dep, step.ID)
		}
	}

	if u.hasCyclicDependency(step.ID, step.Dependencies) {
		return fmt.Errorf("cyclic dependency detected for step %q", step.ID)
	}

	step.Status = "pending"
	u.steps = append(u.steps, step)
	return nil
}

// hasCyclicDependency checks if adding a step would create a cycle
func (u *UltraplanManager) hasCyclicDependency(stepID string, deps []string) bool {
	visited := make(map[string]bool)
	var visit func(id string) bool
	visit = func(id string) bool {
		if id == stepID {
			return true
		}
		if visited[id] {
			return false
		}
		visited[id] = true
		for _, s := range u.steps {
			if s.ID == id {
				for _, dep := range s.Dependencies {
					if visit(dep) {
						return true
					}
				}
			}
		}
		return false
	}
	for _, dep := range deps {
		if visit(dep) {
			return true
		}
	}
	return false
}

// Enabled returns whether Ultraplan mode is enabled
func (u *UltraplanManager) Enabled() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.enabled
}

// StepCount returns the number of executed steps
func (u *UltraplanManager) StepCount() int {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.stepCount
}

// Toggle 切换 Ultraplan 模式的启用/禁用状态，并返回状态描述文本
func (u *UltraplanManager) Toggle() string {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.enabled = !u.enabled
	if u.enabled {
		u.startedAt = time.Now()
		u.stepCount = 0
		return formatUltraplanEnabled()
	}
	return util.Bi("Ultraplan mode disabled", "Ultraplan 模式已禁用")
}

// IncrementStep increments the executed step count
func (u *UltraplanManager) IncrementStep() {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.enabled {
		u.stepCount++
	}
}

// Status 返回当前 Ultraplan 模式的状态描述文本
func (u *UltraplanManager) Status() string {
	u.mu.RLock()
	defer u.mu.RUnlock()

	if u.enabled {
		duration := time.Since(u.startedAt).Round(time.Second)
		return util.BiSame(
			"Ultraplan mode: enabled\nStarted: %s\nUptime: %v\nSteps executed: %d",
			"Ultraplan 模式：已启用\n启动时间：%s\n运行时长：%v\n已执行步骤：%d",
			u.startedAt.Format("2006-01-02 15:04:05"),
			duration,
			u.stepCount,
		)
	}
	return util.Bi("Ultraplan mode: not enabled", "Ultraplan 模式：未启用")
}

// GetSystemPrompt returns the system prompt for Ultraplan mode
// Returns the prompt injected into AI conversation when enabled, empty string when disabled
func (u *UltraplanManager) GetSystemPrompt() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if !u.enabled {
		return ""
	}

	b := &strings.Builder{}
	fmt.Fprintln(b, "[SYSTEM_MODE: Ultraplan]")
	fmt.Fprintln(b, "You are now in Ultraplan mode. For every task, follow this structure:")
	fmt.Fprintln(b, "1. ANALYZE: Break down the task into clear sub-steps")
	fmt.Fprintln(b, "2. PLAN: For each sub-step, specify the approach and tools needed")
	fmt.Fprintln(b, "3. EXECUTE: Execute each sub-step sequentially, reporting results")
	fmt.Fprintln(b, "4. VERIFY: After each step, verify the result before proceeding to the next")
	fmt.Fprintln(b, "5. SUMMARIZE: Provide a final summary of all steps and their results")
	fmt.Fprintln(b, "Use /ultraplan to toggle this mode off.")
	return b.String()
}

// formatUltraplanEnabled 返回启用 Ultraplan 模式时的状态描述文本
func formatUltraplanEnabled() string {
	b := &strings.Builder{}
	fmt.Fprintln(b, util.Bi("Ultraplan mode enabled", "Ultraplan 模式已启用"))
	fmt.Fprintln(b)
	fmt.Fprintln(b, util.Bi("In this mode the system will:", "系统将在此模式下："))
	fmt.Fprintln(b, util.Bi("1. Automatically break complex tasks into sub-steps", "1. 自动将复杂任务分解为子步骤"))
	fmt.Fprintln(b, util.Bi("2. Create an execution plan for each step", "2. 为每个步骤创建执行计划"))
	fmt.Fprintln(b, util.Bi("3. Execute each step sequentially and verify results", "3. 顺序执行每个步骤并验证结果"))
	fmt.Fprintln(b, util.Bi("4. Summarize the output of all steps", "4. 汇总所有步骤的输出"))
	fmt.Fprintln(b)
	fmt.Fprintln(b, util.Bi("Use /ultraplan to toggle the mode", "使用 /ultraplan 切换模式"))
	return b.String()
}
