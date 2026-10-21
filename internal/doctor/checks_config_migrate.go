package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/util"
)

// checkConfigMigrate 检测配置文件版本并支持手动迁移。
// V3-1-3：为 `whale doctor --config-migrate` 提供独立的迁移检测入口。
// 该方法不修改 doctor.Run() 的调用顺序（避免并行冲突），仅作为 CLI 子命令的专用 check。
// 行为：
//   - 检测 settings.json 是否存在 + 可解析
//   - 读取 config_version 字段
//   - 与 CurrentConfigVersion 对比，落后时执行迁移前备份 + 迁移
//   - 返回迁移状态结果（已最新/已迁移/失败）
func (d *Doctor) checkConfigMigrate() []CheckResult {
	var results []CheckResult

	// 1. settings.json 存在性 + 可解析性检测
	if d.settingsPath == "" {
		results = append(results, CheckResult{
			Category:   "Config",
			Name:       "Config Migrate",
			Severity:   SeverityWarn,
			Message:    "settings path not provided",
			FixSuggest: "run /onboarding to initialize config first",
		})
		return results
	}

	data, err := os.ReadFile(d.settingsPath)
	if err != nil {
		results = append(results, CheckResult{
			Category:   "Config",
			Name:       "Config Migrate",
			Severity:   SeverityWarn,
			Message:    "settings file not readable: " + err.Error(),
			FixSuggest: "run /onboarding to initialize config first",
		})
		return results
	}

	// 2. 解析 raw map 仅读取 config_version 字段（不依赖完整 Config 结构）
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		results = append(results, CheckResult{
			Category:   "Config",
			Name:       "Config Migrate",
			Severity:   SeverityError,
			Message:    "settings file corrupted: " + err.Error(),
			FixSuggest: "restore from .bak backup or rerun /onboarding",
		})
		return results
	}

	currentVersion := readConfigVersion(raw)
	results = append(results, CheckResult{
		Category: "Config",
		Name:     "Config Version",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("current=%d (latest=%d)", currentVersion, config.CurrentConfigVersion),
	})

	// 3. 版本已是最新 → 直接返回 OK
	if currentVersion >= config.CurrentConfigVersion {
		results = append(results, CheckResult{
			Category: "Config",
			Name:     "Config Migrate",
			Severity: SeverityOK,
			Message:  "config is up-to-date, no migration needed",
		})
		return results
	}

	// 4. 版本落后 → 执行迁移前备份
	backupPath, backupErr := backupSettingsFile(d.settingsPath, data)
	if backupErr != nil {
		results = append(results, CheckResult{
			Category:   "Config",
			Name:       "Config Migrate",
			Severity:   SeverityError,
			Message:    "backup before migration failed: " + backupErr.Error(),
			FixSuggest: "manually copy settings.json to a safe location and retry",
		})
		return results
	}

	// 5. 执行迁移：复用 config 包的 Loader 重新加载并 Save（Save 会写回 ConfigVersion=CurrentConfigVersion）
	cfg, loadErr := reloadAndMigrate(d.settingsPath)
	if loadErr != nil {
		results = append(results, CheckResult{
			Category:   "Config",
			Name:       "Config Migrate",
			Severity:   SeverityError,
			Message:    "migration failed: " + loadErr.Error(),
			Detail:     "backup at " + backupPath,
			FixSuggest: "restore settings.json from " + backupPath,
		})
		return results
	}

	results = append(results, CheckResult{
		Category: "Config",
		Name:     "Config Migrate",
		Severity: SeverityOK,
		Message:  fmt.Sprintf("migrated v%d → v%d (backup: %s)", currentVersion, cfg.ConfigVersion, backupPath),
	})
	return results
}

// readConfigVersion 从 raw map 读取 config_version 字段，缺失或类型不匹配时返回 0。
// 不依赖完整 Config 结构，仅做最小化解析以判断是否需要迁移。
func readConfigVersion(raw map[string]interface{}) int {
	if v, ok := raw["config_version"]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return 0
}

// backupSettingsFile 将原始配置数据写入时间戳命名的备份文件。
// path: 原配置文件路径；data: 原配置文件内容。
// 返回：备份文件路径 + 错误。
// S12-G6 类修复：权限 0644 → 0600 + 原子写（与 config.Save/migrateWithBackup 一致），
// 避免含 api_key/jwt_secret 的备份被同机其他用户读取。
func backupSettingsFile(path string, data []byte) (string, error) {
	backupPath := fmt.Sprintf("%s.bak.%s", path, time.Now().Format("20060102-150405"))
	if err := util.AtomicWriteFile(backupPath, data, 0600); err != nil {
		return "", fmt.Errorf("write backup failed: %w", err)
	}
	return backupPath, nil
}

// reloadAndMigrate 重新加载配置文件并执行迁移，最后写回磁盘。
// path: settings.json 路径；返回：迁移后的 Config + 错误。
// 复用 config.Loader 的 Load + Save 逻辑，确保迁移流程与启动时一致。
func reloadAndMigrate(path string) (*config.Config, error) {
	// 推断 userDir：settings.json 路径形如 <userDir>/.whale/settings.json
	// 这里直接用 filepath.Dir(filepath.Dir(path)) 推算 userDir
	// 若路径结构异常则 fallback 到空字符串（Loader 仍能从绝对路径加载）
	userDir := deriveUserDir(path)
	loader := config.NewLoader(userDir, "")
	cfg, err := loader.Load()
	if err != nil {
		return nil, fmt.Errorf("reload config: %w", err)
	}
	if err := loader.Save(cfg); err != nil {
		return nil, fmt.Errorf("save migrated config: %w", err)
	}
	return cfg, nil
}

// deriveUserDir 从 settings.json 路径推算 userDir。
// 约定路径：<userDir>/.whale/settings.json → userDir = filepath.Dir(filepath.Dir(path))。
// 路径不符合约定时返回空字符串（Loader 容错处理）。
func deriveUserDir(settingsPath string) string {
	if settingsPath == "" {
		return ""
	}
	parent := dirOf(settingsPath)
	if parent == "" {
		return ""
	}
	return dirOf(parent)
}

// dirOf 安全获取路径的父目录，错误时返回空字符串。
func dirOf(path string) string {
	if path == "" {
		return ""
	}
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			if i == 0 {
				return string(path[0])
			}
			return path[:i]
		}
	}
	return ""
}
