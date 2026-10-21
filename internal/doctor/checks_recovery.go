package doctor

import (
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/util"
)

// checkRecovery 检测 ~/.whale/ 下的损坏 JSON 文件并提供修复建议。
// 只读扫描 whaleDir 下的 .json 文件，损坏文件标记为 Warn 并建议 `whale doctor --repair`。
// 对标 v3修复方案.md §2.2 V3-5-1 doctor 扩展。
//
// 注意：本方法不修改 doctor.Run()，由 Run() 在主对话统一接线（避免并行冲突）。
func (d *Doctor) checkRecovery() []CheckResult {
	whaleDir := defaultWhaleDir()
	if whaleDir == "" {
		return []CheckResult{{
			Category:   "Recovery",
			Name:       "WhaleDir",
			Severity:   SeverityWarn,
			Message:    "home directory unavailable",
			FixSuggest: "set HOME or USERPROFILE environment variable",
		}}
	}

	// 检查 ~/.whale/ 是否存在
	if _, err := os.Stat(whaleDir); err != nil {
		if os.IsNotExist(err) {
			return []CheckResult{{
				Category: "Recovery",
				Name:     "WhaleDir",
				Severity: SeverityOK,
				Message:  "not initialized yet (no ~/.whale/ directory)",
			}}
		}
		return []CheckResult{{
			Category:   "Recovery",
			Name:       "WhaleDir",
			Severity:   SeverityError,
			Message:    "stat failed: " + err.Error(),
			FixSuggest: "check ~/.whale/ permissions",
		}}
	}

	// 只读扫描损坏文件（不实际隔离，隔离由 --repair 触发）
	corrupted, err := scanCorruptedJSON(whaleDir)
	if err != nil {
		return []CheckResult{{
			Category:   "Recovery",
			Name:       "Data Integrity",
			Severity:   SeverityError,
			Message:    "scan failed: " + err.Error(),
			FixSuggest: "check ~/.whale/ readability",
		}}
	}

	if len(corrupted) == 0 {
		return []CheckResult{{
			Category: "Recovery",
			Name:     "Data Integrity",
			Severity: SeverityOK,
			Message:  "no corrupted JSON files detected",
		}}
	}

	// 发现损坏文件：列出前 5 个 + 建议修复
	detail := strings.Join(corrupted[:min(5, len(corrupted))], "\n")
	return []CheckResult{{
		Category:   "Recovery",
		Name:       "Data Integrity",
		Severity:   SeverityWarn,
		Message:    "corrupted JSON files detected",
		Detail:     detail,
		FixSuggest: "run `whale doctor --repair` to quarantine corrupted files",
	}}
}

// defaultWhaleDir 返回默认的 ~/.whale/ 目录路径。
// 收口：唯一走 whalehome.Dir()（WHALE_HOME 覆盖 > $HOME/.whale），失败返回空串。
func defaultWhaleDir() string {
	return whalehome.Dir()
}

// scanCorruptedJSON 只读扫描目录下的损坏 JSON 文件（不隔离）。
// whaleDir: 扫描根目录；返回：损坏文件路径列表。
// 跳过 corrupt 子目录（避免扫描已隔离文件）与 .bak 文件（备份不隔离）。
// 性能修复：加 5s 超时，避免 ~/.whale/ 文件过多时拖慢 doctor。
// 超时后返回 timeout 错误，调用方按 scan failed 容错处理。
func scanCorruptedJSON(whaleDir string) ([]string, error) {
	type result struct {
		corrupted []string
		err       error
	}
	ch := make(chan result, 1)
	go func() {
		var corrupted []string
		err := filepath.Walk(whaleDir, func(path string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if fi.IsDir() {
				// 跳过隔离目录本身（避免扫描已隔离文件）
				rel, _ := filepath.Rel(whaleDir, path)
				if rel == "corrupt" {
					return filepath.SkipDir
				}
				return nil
			}
			// 仅检查 .json 文件（.bak/.tmp 跳过）
			if filepath.Ext(path) != ".json" {
				return nil
			}
			// 空文件跳过（非损坏）
			if fi.Size() == 0 {
				return nil
			}
			if util.IsJSONFileCorrupted(path) {
				corrupted = append(corrupted, path)
			}
			return nil
		})
		ch <- result{corrupted, err}
	}()
	select {
	case r := <-ch:
		return r.corrupted, r.err
	case <-time.After(5 * time.Second):
		return nil, fmt.Errorf("scan timeout after 5s (too many files in ~/.whale/?)")
	}
}
