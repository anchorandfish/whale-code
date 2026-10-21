package command

import (
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/permission"
	"github.com/anchorandfish/whale-code/internal/util"
)

// CurrentConfigVersion 引用 config 包的统一版本号（R8 修复：消除双源真相）。
// config.CurrentConfigVersion 是配置 schema 版本的唯一来源。
const CurrentConfigVersion = config.CurrentConfigVersion

// migrationStep 定义单个迁移步骤
type migrationStep struct {
	// version 迁移后的版本号
	version int
	// migrate 执行迁移的函数，接收原始配置 map，返回修改后的 map
	migrate func(raw map[string]interface{}) []MigrateItem
}

// migrationSteps 按版本号顺序排列的迁移步骤列表
// 新增迁移步骤只需追加到列表末尾
var migrationSteps = []migrationStep{
	{
		version: 2,
		migrate: migrateV1toV2,
	},
	// 未来扩展:
	// {version: 3, migrate: migrateV2toV3},
}

// MigrateItem represents a migration item
type MigrateItem struct {
	Name         string
	FromField    string
	ToField      string
	IsMigratable bool
}

// checkMigrationNeeded checks if migration is needed
func checkMigrationNeeded(homeDir string) ([]MigrateItem, error) {
	configPath := filepath.Join(whalehome.Dir(), "settings.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, nil // file does not exist, no migration needed
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 获取当前版本号
	currentVersion := int(0)
	if v, ok := raw["config_version"].(float64); ok {
		currentVersion = int(v)
	}

	if currentVersion >= CurrentConfigVersion {
		return nil, nil // already latest version
	}

	// 收集所有需要的迁移步骤
	var items []MigrateItem
	for _, step := range migrationSteps {
		if step.version > currentVersion && step.version <= CurrentConfigVersion {
			// 模拟执行以收集迁移项信息
			configCopy := make(map[string]interface{})
			for k, v := range raw {
				configCopy[k] = v
			}
			stepItems := step.migrate(configCopy)
			items = append(items, stepItems...)
		}
	}

	return items, nil
}

// performMigration performs the actual migration through all needed steps
func performMigration(homeDir string) ([]MigrateItem, error) {
	configPath := filepath.Join(whalehome.Dir(), "settings.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// 获取当前版本号
	currentVersion := int(0)
	if v, ok := raw["config_version"].(float64); ok {
		currentVersion = int(v)
	}

	// 创建备份（R6 统一：与 config.Save/migrateWithBackup 的 {path}.{timestamp}.bak 格式对齐）
	// S12-G6 类修复：权限 0644 → 0600 + 原子写，避免含 api_key/jwt_secret 的备份被同机他人读取。
	backupPath := fmt.Sprintf("%s.%s.bak", configPath, time.Now().Format("20060102-150405"))
	if err := util.AtomicWriteFile(backupPath, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to create backup: %w", err)
	}

	// 按顺序执行所有需要的迁移步骤
	var migrated []MigrateItem
	for _, step := range migrationSteps {
		if step.version > currentVersion {
			stepItems := step.migrate(raw)
			migrated = append(migrated, stepItems...)
			currentVersion = step.version
		}
	}

	// 更新版本号（使用 int 避免 float64 格式化问题）
	raw["config_version"] = CurrentConfigVersion
	migrated = append(migrated, MigrateItem{Name: fmt.Sprintf("config_version updated to v%d", CurrentConfigVersion), IsMigratable: true})

	// 写入
	newData, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to serialize config: %w", err)
	}

	// S12-G6 类修复：就地重写也改为原子写 + 0600（原 os.WriteFile(0644) 非原子，
	// 崩溃可能截断 settings.json；且显式 0600 保证权限不随 umask 放宽）。
	if err := util.AtomicWriteFile(configPath, newData, 0600); err != nil {
		if restoreErr := os.WriteFile(configPath, data, 0600); restoreErr != nil {
			return nil, fmt.Errorf("failed to write config: %w (backup restore also failed: %v, manual restore from %s)", err, restoreErr, backupPath)
		}
		return nil, fmt.Errorf("failed to write config: %w (restored from backup)", err)
	}

	return migrated, nil
}

// migrateV1toV2 执行 v1 → v2 的配置迁移
func migrateV1toV2(raw map[string]interface{}) []MigrateItem {
	var migrated []MigrateItem

	// 1. apiKey → apiKeys
	if apiKey, ok := raw["apiKey"]; ok {
		if _, hasApiKeys := raw["apiKeys"]; !hasApiKeys {
			raw["apiKeys"] = map[string]interface{}{
				"default": apiKey,
			}
		}
		delete(raw, "apiKey")
		migrated = append(migrated, MigrateItem{Name: "apiKey → apiKeys.default", IsMigratable: true})
	}

	// 2. permissionMode value update
	// 阶段 2 收模式后：已删除的旧模式（accept-edits / dont-ask / plan / auto / bubble）
	// 必须按单一真相源翻译到正确的 3 档（如 plan → read-only、dont-ask → danger-full-access），
	// 而不是一律归一为 workspace-write —— 后者会把 plan 静默放权、把 dont-ask 静默收紧。
	if pv, ok := raw["permission_mode"]; ok {
		mode := fmt.Sprintf("%v", pv)
		if level, known := permission.LookupModeName(mode); known {
			if canonical := level.String(); canonical != mode {
				raw["permission_mode"] = canonical
				migrated = append(migrated, MigrateItem{Name: "permission_mode value updated", IsMigratable: true})
			}
		} else {
			raw["permission_mode"] = "workspace-write"
			migrated = append(migrated, MigrateItem{Name: "permission_mode value updated", IsMigratable: true})
		}
	}

	// 3. compactThreshold → contextWindow
	if threshold, ok := raw["compactThreshold"]; ok {
		if _, hasCtx := raw["context_window"]; !hasCtx {
			raw["context_window"] = threshold
		}
		delete(raw, "compactThreshold")
		migrated = append(migrated, MigrateItem{Name: "compactThreshold → context_window", IsMigratable: true})
	}

	return migrated
}

// NewMigrateCommandHandler creates the /migrate command handler
func NewMigrateCommandHandler(homeDir string) func(args string) string {
	return func(args string) string {
		subCmd := strings.TrimSpace(strings.ToLower(args))

		if subCmd == "" || subCmd == "check" {
			items, err := checkMigrationNeeded(homeDir)
			if err != nil {
				return fmt.Sprintf("Config migration check failed: %v", err)
			}

			if len(items) == 0 {
				return fmt.Sprintf("Config migration check: current version is up to date (v%d), no migration needed", CurrentConfigVersion)
			}

			var sb strings.Builder
			sb.WriteString("Config migration check: migratable items found\n")
			for i, item := range items {
				sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, item.Name))
				if item.FromField != "" {
					sb.WriteString(fmt.Sprintf("     %s → %s\n", item.FromField, item.ToField))
				}
			}
			sb.WriteString(fmt.Sprintf("\n%d items can be migrated, run /migrate run to start migration", len(items)))
			return sb.String()
		}

		if subCmd == "run" {
			items, err := checkMigrationNeeded(homeDir)
			if err != nil {
				return fmt.Sprintf("Config migration check failed: %v", err)
			}

			if len(items) == 0 {
				return fmt.Sprintf("Config migration complete: already at latest version (v%d), no migration needed", CurrentConfigVersion)
			}

			migrated, err := performMigration(homeDir)
			if err != nil {
				return fmt.Sprintf("Config migration failed: %v", err)
			}

			configPath := filepath.Join(whalehome.Dir(), "settings.json")
			var sb strings.Builder
			sb.WriteString("Config migration complete:\n")
			for _, item := range migrated {
				sb.WriteString(fmt.Sprintf("  ✓ %s\n", item.Name))
			}
			sb.WriteString(util.Bi(fmt.Sprintf("\nBackup file: %s.<timestamp>.bak (unified R6 naming)", configPath), fmt.Sprintf("\n备份文件：%s.<timestamp>.bak（R6 统一命名格式）", configPath)))
			sb.WriteString(fmt.Sprintf("\nMigrated %d fields", len(migrated)))
			return sb.String()
		}

		return "Usage: /migrate [check|run]"
	}
}
