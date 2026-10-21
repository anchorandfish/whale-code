package doctor

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/anchorandfish/whale-code/internal/whalehome"
)

// checkStateDirWritable 检测 whale 状态目录是否真实可写（方案A，2026-10-15）。
//
// 背景： 的根因之一是外层沙箱挡住 ~/.whale 内的 rename/unlink ⇒
// 原子写残留 .tmp 无声攒了 59 天（29.9 万个）。whale 修不了外层沙箱的 profile，
// 但必须把"第三道门悄悄杀人"变成"启动即诊断"：doctor 在状态目录里做一次
// 真实的 写→改名→删 探针，失败即告警并给出 WHALE_HOME 搬家指引。
//
// 安全：探针文件用私有新名（含 PID），绝不触碰真实用户文件（production-path-tdd R2 探针自伤规则）。
func (d *Doctor) checkStateDirWritable() []CheckResult {
	dir := whalehome.Dir()
	if dir == "" {
		return []CheckResult{{
			Category:   "State Dir",
			Name:       "StateDirWritable",
			Severity:   SeverityWarn,
			Message:    "state dir unavailable (no home directory)",
			FixSuggest: "set HOME environment variable",
		}}
	}

	if err := probeStateDirWritable(dir); err != nil {
		return []CheckResult{{
			Category: "State Dir",
			Name:     "StateDirWritable",
			Severity: SeverityWarn,
			Message:  fmt.Sprintf("state dir NOT writable: %s (%v)", dir, err),
			FixSuggest: "an outer sandbox is likely blocking writes to the state dir; " +
				"set WHALE_HOME to a writable directory (e.g. inside the workspace or /tmp) and restart",
		}}
	}

	label := dir
	if override := os.Getenv(whalehome.EnvVar); override != "" {
		label += " (via WHALE_HOME)"
	}
	return []CheckResult{{
		Category: "State Dir",
		Name:     "StateDirWritable",
		Severity: SeverityOK,
		Message:  "state dir is writable: " + label,
	}}
}

// probeStateDirWritable 在 dir 内做一次真实 写→改名→删 探针。
// 任一步失败即返回错误（错误信息带失败步骤）。
// 典型失败形态（实证）：write 成功但 rename/unlink 被 EPERM ⇒ tmp 滞留。
func probeStateDirWritable(dir string) error {
	// 私有新名：doctor 探针专属，与任何真实文件不可能撞名。
	f1 := filepath.Join(dir, fmt.Sprintf(".whale-doctor-probe-%d.tmp", os.Getpid()))
	f2 := f1 + ".renamed"

	writeErr := func(step string, err error) error {
		// 失败路径尽力清理，避免探针自己制造残留
		os.Remove(f1)
		os.Remove(f2)
		return fmt.Errorf("probe %s failed: %w", step, err)
	}

	if err := os.WriteFile(f1, []byte("doctor-writability-probe"), 0o600); err != nil {
		return writeErr("write", err)
	}
	if err := os.Rename(f1, f2); err != nil {
		return writeErr("rename", err)
	}
	if err := os.Remove(f2); err != nil {
		return writeErr("unlink", err)
	}
	return nil
}
