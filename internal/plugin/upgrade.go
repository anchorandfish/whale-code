// Package plugin — M20-05 插件版本升级路径（SNAPSHOT）
//
// 本文件实现插件版本升级机制：同 ID 不同版本升级、降级、版本比较。
// 对标 VSCode extension auto-update（https://code.visualstudio.com/docs/editor/extension-gallery#_extension-autoupdate）。
//
// 设计要点（候选 A 选定）：
// - 接口签名：UpgradeManager 接口抽象，支持多升级策略
// - 结构体字段：UpgradeResult 含 OldVersion/NewVersion/BackupPath/Migrated
// - 分层权责：本文件只做"版本比较+备份+替换"，数据迁移由 Migrator 回调
// - 降级策略：升级失败自动回滚（从 backup 恢复）
// - TTL规则：无 TTL（升级是即时操作）
package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UpgradeResult 是插件升级结果（SNAPSHOT-1）。
type UpgradeResult struct {
	PluginID   string    // 插件 ID
	OldVersion string    // 旧版本
	NewVersion string    // 新版本
	BackupPath string    // 备份路径（升级失败可回滚）
	Migrated   bool      // 是否执行了数据迁移
	UpgradedAt time.Time // 升级时间
}

// Migrator 是数据迁移回调（SNAPSHOT-2）。
//
// 用于在版本升级时迁移插件数据（如配置文件格式变更）。
// oldDir 是旧插件目录（备份前），newDir 是新插件目录。
// 返回 error 表示迁移失败，调用方应回滚。
type Migrator func(pluginID, oldVersion, newVersion, oldDir, newDir string) error

// UpgradeManager 是插件升级管理器接口（SNAPSHOT-3）。
//
// 接口契约：
// - CheckUpgrade：检查插件是否有新版本可用
// - Upgrade：执行升级（备份旧版本→安装新版本→迁移数据）
// - Rollback：回滚到备份版本
//
// 实现方：
// - defaultUpgradeManager：本文件实现（基于 Manager + Marketplace）
type UpgradeManager interface {
	// CheckUpgrade 检查插件是否有新版本。
	// 返回 newVersion（无新版本返回空字符串）+ error。
	CheckUpgrade(pluginID string) (newVersion string, err error)

	// Upgrade 执行插件升级。
	// pluginID: 要升级的插件 ID。
	// migrator: 数据迁移回调（可为 nil，表示无数据迁移）。
	// 返回 UpgradeResult（含备份路径，用于回滚）。
	Upgrade(pluginID string, migrator Migrator) (*UpgradeResult, error)

	// Rollback 回滚到备份版本。
	// result: 之前 Upgrade 返回的 UpgradeResult。
	Rollback(result *UpgradeResult) error
}

// defaultUpgradeManager 是默认升级管理器实现（候选 A）。
type defaultUpgradeManager struct {
	mgr    *Manager
	market Marketplace
	verif  SignatureVerifier
}

// NewUpgradeManager 创建升级管理器（SNAPSHOT-4）。
//
// 参数：
// - mgr: 插件 Manager（用于读取已安装插件、安装新版本）
// - market: 插件市场（用于查询新版本）
// - verif: 签名校验器（升级前校验新版本签名）
//
// 任意参数为 nil 返回 nil + error（升级需要三者协同）。
//
// TODO(v1.1): 插件升级子系统尚未在生产接线。依赖 NewGitMarketplace 接线完成后，
// 需新增 /plugin upgrade <id> 命令并在 NewExecutor 中注入 NewUpgradeManager。
// 桶 X5 死代码清理：当前该工厂零生产调用，标记为 v1.1 路线图项。
func NewUpgradeManager(mgr *Manager, market Marketplace, verif SignatureVerifier) (UpgradeManager, error) {
	if mgr == nil {
		return nil, errors.New("upgrade: Manager is nil")
	}
	if market == nil {
		return nil, errors.New("upgrade: Marketplace is nil")
	}
	if verif == nil {
		return nil, errors.New("upgrade: SignatureVerifier is nil")
	}
	return &defaultUpgradeManager{mgr: mgr, market: market, verif: verif}, nil
}

// CheckUpgrade 检查插件是否有新版本可用（SNAPSHOT-5）。
//
// 行为：
// - 从 Manager 读取已安装插件版本
// - 从 Marketplace 查询最新版本
// - 比较 semver，新版本更高时返回 newVersion
func (u *defaultUpgradeManager) CheckUpgrade(pluginID string) (string, error) {
	installed, ok := u.mgr.Get(pluginID)
	if !ok {
		return "", fmt.Errorf("upgrade: plugin %q not installed", pluginID)
	}

	ctx, cancel := upgradeCtx()
	defer cancel()
	entry, err := u.market.FetchEntry(ctx, pluginID)
	if err != nil {
		return "", fmt.Errorf("upgrade: fetch entry: %w", err)
	}
	if entry == nil {
		return "", nil // 市场无此插件，无升级
	}

	// 比较 semver：市场版本 > 已安装版本 → 返回新版本
	if compareSemver(entry.Version, installed.Version) > 0 {
		return entry.Version, nil
	}
	return "", nil
}

// Upgrade 执行插件升级（SNAPSHOT-6）。
//
// 流程：
// 1. CheckUpgrade 确认有新版本
// 2. 备份旧插件目录到 .bak
// 3. 卸载旧版本（Manager.Uninstall）
// 4. 从市场下载新版本源（git URL）
// 5. 安装新版本（Manager.Install）
// 6. 签名校验（SignatureVerifier.Verify）
// 7. 执行数据迁移（migrator 回调，非 nil 时）
// 8. 失败时自动回滚（从备份恢复）
func (u *defaultUpgradeManager) Upgrade(pluginID string, migrator Migrator) (*UpgradeResult, error) {
	installed, ok := u.mgr.Get(pluginID)
	if !ok {
		return nil, fmt.Errorf("upgrade: plugin %q not installed", pluginID)
	}

	newVersion, err := u.CheckUpgrade(pluginID)
	if err != nil {
		return nil, err
	}
	if newVersion == "" {
		return nil, fmt.Errorf("upgrade: plugin %q already at latest version", pluginID)
	}

	// 备份旧插件目录
	backupPath := installed.SourcePath + ".bak." + formatTimestamp(time.Now())
	if err := copyDir(installed.SourcePath, backupPath); err != nil {
		return nil, fmt.Errorf("upgrade: backup old version: %w", err)
	}

	result := &UpgradeResult{
		PluginID:   pluginID,
		OldVersion: installed.Version,
		NewVersion: newVersion,
		BackupPath: backupPath,
		UpgradedAt: time.Now(),
	}

	// 卸载旧版本（不触发 onUninstall 回调的副作用，仅清理内存）
	if err := u.mgr.Uninstall(pluginID); err != nil {
		// 卸载失败，尝试回滚
		_ = rollbackFromBackup(backupPath, installed.SourcePath)
		return nil, fmt.Errorf("upgrade: uninstall old: %w", err)
	}

	// 从市场下载新版本源
	ctx, cancel := upgradeCtx()
	defer cancel()
	source, err := u.market.Download(ctx, pluginID)
	if err != nil {
		_ = rollbackFromBackup(backupPath, installed.SourcePath)
		return nil, fmt.Errorf("upgrade: download new: %w", err)
	}

	// 安装新版本
	newPlugin, err := u.mgr.Install(source)
	if err != nil {
		_ = rollbackFromBackup(backupPath, installed.SourcePath)
		return nil, fmt.Errorf("upgrade: install new: %w", err)
	}

	// 签名校验
	if err := u.verif.Verify(newPlugin.SourcePath); err != nil {
		// 验签失败，回滚
		_ = u.mgr.Uninstall(pluginID)
		_ = rollbackFromBackup(backupPath, installed.SourcePath)
		return nil, fmt.Errorf("upgrade: verify signature: %w", err)
	}

	// 数据迁移
	if migrator != nil {
		if err := migrator(pluginID, installed.Version, newVersion, backupPath, newPlugin.SourcePath); err != nil {
			// 迁移失败，回滚
			_ = u.mgr.Uninstall(pluginID)
			_ = rollbackFromBackup(backupPath, installed.SourcePath)
			return nil, fmt.Errorf("upgrade: migrate data: %w", err)
		}
		result.Migrated = true
	}

	// 清理备份（升级成功）
	_ = os.RemoveAll(backupPath)
	return result, nil
}

// Rollback 回滚到备份版本（SNAPSHOT-7）。
//
// 行为：
// - 验证 result.BackupPath 存在
// - 卸载当前版本（若存在）
// - 从备份路径恢复
func (u *defaultUpgradeManager) Rollback(result *UpgradeResult) error {
	if result == nil || result.BackupPath == "" {
		return errors.New("rollback: empty result or backup path")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		return fmt.Errorf("rollback: backup not found: %w", err)
	}

	// 卸载当前版本（忽略 not exist 错误）
	if _, ok := u.mgr.Get(result.PluginID); ok {
		if err := u.mgr.Uninstall(result.PluginID); err != nil {
			return fmt.Errorf("rollback: uninstall current: %w", err)
		}
	}

	// 从备份恢复
	targetPath := filepath.Dir(result.BackupPath) + string(os.PathSeparator) + result.PluginID
	return rollbackFromBackup(result.BackupPath, targetPath)
}

// compareSemver 比较 semver 版本字符串（SNAPSHOT-8）。
//
// 返回值：
// - 1: a > b
// - 0: a == b
// - -1: a < b
//
// 仅比较 MAJOR.MINOR.PATCH（忽略 prerelease/build，简化实现）。
func compareSemver(a, b string) int {
	pa := parseSemverParts(a)
	pb := parseSemverParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return 1
		}
		if pa[i] < pb[i] {
			return -1
		}
	}
	return 0
}

// parseSemverParts 解析 semver 为 [3]int（MAJOR.MINOR.PATCH）。
//
// prerelease/build 后缀被忽略（-alpha / +build 被截断）。
func parseSemverParts(v string) [3]int {
	var parts [3]int
	// 截断 prerelease/build
	if idx := strings.IndexAny(v, "-+"); idx >= 0 {
		v = v[:idx]
	}
	seg := strings.Split(v, ".")
	for i := 0; i < 3 && i < len(seg); i++ {
		n := 0
		for _, ch := range seg[i] {
			if ch >= '0' && ch <= '9' {
				n = n*10 + int(ch-'0')
			} else {
				break
			}
		}
		parts[i] = n
	}
	return parts
}

// copyDir 递归复制目录（内部工具函数）。
func copyDir(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("copyDir: %s is not a directory", src)
	}
	if err := os.MkdirAll(dst, info.Mode()); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		s := filepath.Join(src, entry.Name())
		d := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyDir(s, d); err != nil {
				return err
			}
		} else {
			data, err := os.ReadFile(s)
			if err != nil {
				return err
			}
			if err := os.WriteFile(d, data, entry.Type()); err != nil {
				return err
			}
		}
	}
	return nil
}

// rollbackFromBackup 从备份路径恢复插件目录（内部工具函数）。
func rollbackFromBackup(backupPath, targetPath string) error {
	// 删除可能残留的目标目录
	_ = os.RemoveAll(targetPath)
	// 从备份复制回目标路径
	return copyDir(backupPath, targetPath)
}

// formatTimestamp 格式化时间戳为文件名安全字符串（内部工具函数）。
func formatTimestamp(t time.Time) string {
	return t.Format("20060102-150405")
}

// upgradeCtx 返回升级操作的 context（30s 超时）及 cancel 函数。
// 调用方必须 defer cancel() 释放资源，避免 context 泄漏（vet lostcancel 修复）。
func upgradeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}
