package doctor

import (
	"os/exec"
	"runtime"
)

// checkDependencies 执行依赖检测项（2 项）。
// 检测：git 可用性（exec.LookPath）、gcc/cgo 可用性（仅 Linux，Windows 不检测）。
// 对标同类 CLI ripgrep status 区块。
// 依赖缺失时标记 Warn（非阻塞，仅影响部分功能如 go test -race）。
func (d *Doctor) checkDependencies() []CheckResult {
	var results []CheckResult

	// 1. git 可用性（所有平台）
	results = append(results, checkGit())

	// 2. gcc/cgo 可用性（仅 Linux，对标 project_memory "本机无 gcc" 痛点）
	results = append(results, checkGcc())

	return results
}

// checkGit 检测 git 是否在 PATH 中可用。
// 使用 exec.LookPath（不实际执行 git），git 缺失时标记 Warn（非阻塞）。
func checkGit() CheckResult {
	path, err := exec.LookPath("git")
	if err != nil {
		return CheckResult{
			Category:   "Dependencies",
			Name:       "git",
			Severity:   SeverityWarn,
			Message:    "not found in PATH",
			FixSuggest: "install git (e.g., apt install git / brew install git / download from git-scm.com)",
		}
	}
	return CheckResult{
		Category: "Dependencies",
		Name:     "git",
		Severity: SeverityOK,
		Message:  path,
	}
}

// checkGcc 检测 gcc/cgo 是否可用。
// 仅 Linux 检测（Windows 本机无 gcc 是已知限制，go test -race 不可用）。
// gcc 缺失时标记 Warn（非阻塞，仅影响 cgo 相关功能）。
func checkGcc() CheckResult {
	// Windows 平台跳过（本机无 gcc 是已知限制，对标改进方案 §4.1）
	if runtime.GOOS == "windows" {
		return CheckResult{
			Category: "Dependencies",
			Name:     "gcc",
			Severity: SeverityOK,
			Message:  "not applicable (windows, cgo not required)",
		}
	}
	path, err := exec.LookPath("gcc")
	if err != nil {
		return CheckResult{
			Category:   "Dependencies",
			Name:       "gcc",
			Severity:   SeverityWarn,
			Message:    "not found in PATH (go test -race unavailable)",
			FixSuggest: "install build-essential (apt) or gcc (brew/dnf)",
		}
	}
	return CheckResult{
		Category: "Dependencies",
		Name:     "gcc",
		Severity: SeverityOK,
		Message:  path,
	}
}
