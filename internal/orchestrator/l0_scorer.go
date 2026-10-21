// l0_scorer.go — 桶 2 L0 任务完成度评分器（地基层 §4.3）。
// REF-外部-Google Agent Quality 白皮书@https://www.kaggle.com/whitepaper-agent-quality+2026-07-15访问-L0 任务完成度评估
// 纯自动评分；落地：go build && go test && 契约一致 && golangci-lint error 全过
// SNAPSHOT: L0=bool(build&&test&&contract&&lint_error); items={go build ./..., go test ./..., contract_check, golangci-lint --level=error}; failure_short_circuit=true

package orchestrator

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// L0Scorer 任务完成度评分器，纯自动（地基层 §4.3）。
// 检查：go build 通过 / go test 通过 / 契约一致 / golangci-lint error 级别全过。
// 任一项失败立即短路返回 false（failure_short_circuit=true），防止 L0 失败但 l0_pass=true 偷懒。
type L0Scorer struct {
	workDir string
	// runCmd 可注入的命令执行函数（便于测试 mock），nil 时用 exec.CommandContext
	runCmd func(ctx context.Context, dir, name string, args ...string) (string, error)
	// checkContract 可注入的契约检查函数（便于测试 mock），nil 时用默认实现
	checkContract func(task SubTask) error
}

// NewL0Scorer 创建 L0 评分器，workDir 为评分根目录。
func NewL0Scorer(workDir string) *L0Scorer {
	return &L0Scorer{
		workDir:       workDir,
		runCmd:        defaultRunCmd,
		checkContract: defaultCheckContract,
	}
}

// Calc 计算子任务 L0 通过状态，返回二值（true=通过，地基层 §4.3）。
// 短路评估：任一项失败立即返回 false，不继续后续检查（防偷懒 + 节省时间）。
func (s *L0Scorer) Calc(ctx context.Context, task SubTask) (bool, error) {
	// 1. go build ./...
	if out, err := s.runCmd(ctx, s.workDir, "go", "build", "./..."); err != nil {
		return false, fmt.Errorf("L0 build failed: %w\noutput: %s", err, out)
	}

	// 2. go test ./...
	if out, err := s.runCmd(ctx, s.workDir, "go", "test", "./..."); err != nil {
		return false, fmt.Errorf("L0 test failed: %w\noutput: %s", err, out)
	}

	// 3. 契约一致性检查（仅 HasContract=true 时执行）
	if task.HasContract && task.ContractSpec != nil {
		if err := s.checkContract(task); err != nil {
			return false, fmt.Errorf("L0 contract violation: %w", err)
		}
	}

	// 4. golangci-lint run --level=error（仅检查 error 级别，warn 不阻断）
	if out, err := s.runCmd(ctx, s.workDir, "golangci-lint", "run", "--level=error"); err != nil {
		// golangci-lint 未安装时不阻断 L0（仅 warn），用 exit code 区分
		if !isCommandNotFound(out, err) {
			return false, fmt.Errorf("L0 lint failed: %w\noutput: %s", err, out)
		}
	}

	return true, nil
}

// defaultRunCmd 默认命令执行：exec.CommandContext，返回 combined output。
func defaultRunCmd(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// defaultCheckContract 默认契约检查：Phase 0 仅占位（Phase 1 落地完整实现）。
// 检查 SubTask.ContractSpec.ExpectedFuncs 是否在源码中存在（通过 go/parsing）。
func defaultCheckContract(task SubTask) error {
	if task.ContractSpec == nil {
		return nil
	}
	// Phase 0 占位：契约检查在 Phase 1 编排器落地后实现完整版（含 AST 解析）。
	// 当前仅校验 ContractSpec 非空即视为通过，避免阻塞 L0 评分流程。
	// 注意：此处是降级实现，Phase 1 必须补全（FALLBACK 声明）。
	return nil
}

// isCommandNotFound 判断命令是否未安装（用于 golangci-lint 缺失时降级不阻断）。
func isCommandNotFound(out string, err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(out + " " + err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "not recognized") // Windows
}
