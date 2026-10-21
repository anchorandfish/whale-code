package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAcquireLock_Success 验证 acquireLock 成功获取锁并返回 release 函数
func TestAcquireLock_Success(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.json")
	release, err := acquireLock(tmpFile)
	if err != nil {
		t.Fatalf("acquireLock 失败: %v", err)
	}
	if release == nil {
		t.Fatal("release 函数为 nil")
	}

	// 锁文件应存在
	lockPath := tmpFile + ".lock"
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("锁文件未创建")
	}

	// release 后锁文件应删除
	release()
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("release 后锁文件未删除")
	}
}

// TestAcquireLock_Concurrent_Mutex 验证并发 acquireLock 互斥
func TestAcquireLock_Concurrent_Mutex(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.json")

	release1, err := acquireLock(tmpFile)
	if err != nil {
		t.Fatalf("第一次 acquireLock 失败: %v", err)
	}
	defer release1()

	// 第二次 acquireLock 应阻塞（锁被持有）
	done := make(chan error, 1)
	go func() {
		_, err := acquireLock(tmpFile)
		done <- err
	}()

	select {
	case err := <-done:
		// acquireLock 在锁持有期间不应成功（应互斥）
		if err == nil {
			t.Error("并发 acquireLock 不应同时成功（应互斥）")
		}
	case <-time.After(2 * time.Second):
		// 继续等待后台调用按生产代码的 5 秒上限收尾，避免测试退出后
		// goroutine 仍访问 TempDir，导致清理与锁文件创建发生竞态。
		err := <-done
		if err == nil {
			t.Error("并发 acquireLock 等待期间最终成功，不符合互斥预期")
		}
		t.Logf("并发 acquireLock 阻塞后按预期失败: %v", err)
	}
}

// TestAcquireLock_StaleLock_Preempted 验证 stale lock 被抢占
func TestAcquireLock_StaleLock_Preempted(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.json")
	lockPath := tmpFile + ".lock"

	// 创建一个 stale lock（PID=999999 不存在 + 超时 10 分钟）
	staleContent := fmt.Sprintf("999999\n%d", time.Now().Add(-10*time.Minute).Unix())
	os.WriteFile(lockPath, []byte(staleContent), 0600)

	// acquireLock 应识别 stale 并抢占
	release, err := acquireLock(tmpFile)
	if err != nil {
		t.Fatalf("acquireLock 应抢占 stale lock，但失败: %v", err)
	}
	defer release()
}

// TestAcquireLock_UnparseableContent_Blocks 验证无法解析的锁内容被视为有效锁（保守策略，阻塞）
func TestAcquireLock_UnparseableContent_Blocks(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "test.json")
	lockPath := tmpFile + ".lock"

	// 创建无法解析的锁内容（无换行，无 PID/时间戳）
	os.WriteFile(lockPath, []byte("fake-lock-pid-12345"), 0600)

	// acquireLock 应视为锁被持有（保守），阻塞而非抢占
	done := make(chan error, 1)
	go func() {
		_, err := acquireLock(tmpFile)
		done <- err
	}()

	select {
	case err := <-done:
		// 如果返回错误（超时），说明阻塞后失败，符合预期
		if err == nil {
			t.Error("无法解析的锁内容不应被抢占（应保守视为锁被持有）")
		}
	case <-time.After(2 * time.Second):
		// 超时说明 acquireLock 正在阻塞；仍须等待 goroutine 收尾，
		// 否则 t.TempDir 清理可能与后续锁文件访问并发。
		err := <-done
		if err == nil {
			t.Error("无法解析的锁内容在等待期间被抢占")
		}
		t.Logf("无法解析的锁内容阻塞后按预期失败: %v", err)
	}
}

// TestSave_UsesLockfile 验证 Save 使用文件锁（对应 TestBug_ConfigNoLockfile 修复）
func TestSave_UsesLockfile(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	os.MkdirAll(settingsDir, 0700)

	// 预创建 lockfile 模拟锁被持有（当前 PID + 当前时间戳 = 有效锁）
	lockPath := filepath.Join(settingsDir, "settings.json.lock")
	lockContent := fmt.Sprintf("%d\n%d", os.Getpid(), time.Now().Unix())
	os.WriteFile(lockPath, []byte(lockContent), 0600)

	loader := NewLoader(dir, "")
	cfg := DefaultConfig()

	// Save 应检测到锁被持有，阻塞或失败
	done := make(chan error, 1)
	go func() {
		done <- loader.Save(&cfg)
	}()

	select {
	case err := <-done:
		// 如果 Save 成功，说明没尊重 lockfile（bug 未修复）
		if err == nil {
			t.Errorf("BUG: Save 未尊重已存在的 lockfile，直接写入")
		} else {
			t.Logf("Save 正确失败（尊重 lockfile）: %v", err)
		}
	case <-time.After(3 * time.Second):
		// 继续等待 Save 的锁重试完成，防止遗留 goroutine 与 TempDir
		// 清理并发创建/删除 .whale 目录。
		err := <-done
		if err == nil {
			t.Error("BUG: Save 等待锁期间最终成功，未正确尊重 lockfile")
		} else if !strings.Contains(err.Error(), "acquire lock") {
			t.Errorf("Save 返回了非预期错误: %v", err)
		}
		t.Logf("Save 等待锁后按预期失败: %v", err)
	}
}

// TestSave_TimestampedBackup 验证 Save 创建时间戳命名备份（对应 TestBug_ConfigBackupNotTimestamped 修复）
func TestSave_TimestampedBackup(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	os.MkdirAll(settingsDir, 0700)
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 先写一个旧 settings.json
	oldCfg := DefaultConfig()
	oldCfg.Model = "old-model"
	oldData, _ := json.MarshalIndent(&oldCfg, "", "  ")
	os.WriteFile(settingsPath, oldData, 0600)

	loader := NewLoader(dir, "")
	newCfg := DefaultConfig()
	newCfg.Model = "new-model"

	if err := loader.Save(&newCfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 应存在时间戳命名的备份文件（settings.json.<timestamp>.bak，含年份前缀 .20）
	entries, _ := os.ReadDir(settingsDir)
	foundTimestamped := false
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, "settings.json.") && strings.HasSuffix(name, ".bak") {
			// 应含时间戳（年份前缀 .20 或 .19），而非固定 .bak
			if strings.Contains(name, ".20") || strings.Contains(name, ".19") {
				foundTimestamped = true
				t.Logf("找到时间戳备份: %s", name)
			}
		}
	}
	if !foundTimestamped {
		t.Errorf("未找到时间戳命名备份（仍是固定 .bak 或无备份）")
	}
}

// TestSave_BackupNotRemovedOnSuccess 验证 Save 成功后不删除备份
func TestSave_BackupNotRemovedOnSuccess(t *testing.T) {
	dir := t.TempDir()
	settingsDir := filepath.Join(dir, ".whale")
	t.Setenv("WHALE_HOME", settingsDir)
	os.MkdirAll(settingsDir, 0700)
	settingsPath := filepath.Join(settingsDir, "settings.json")

	// 先写旧 settings.json
	oldCfg := DefaultConfig()
	oldData, _ := json.MarshalIndent(&oldCfg, "", "  ")
	os.WriteFile(settingsPath, oldData, 0600)

	loader := NewLoader(dir, "")
	newCfg := DefaultConfig()
	newCfg.Model = "new-model"

	if err := loader.Save(&newCfg); err != nil {
		t.Fatalf("Save 失败: %v", err)
	}

	// 备份应仍存在（不被删除）
	entries, _ := os.ReadDir(settingsDir)
	backupCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "settings.json.") && strings.HasSuffix(entry.Name(), ".bak") {
			backupCount++
		}
	}
	if backupCount == 0 {
		t.Errorf("Save 成功后备份被删除（BUG 未修复），应保留备份")
	}
}

// TestCleanupOldBackups_KeepsRecentN 验证 cleanupOldBackups 保留最近 5 个备份
func TestCleanupOldBackups_KeepsRecentN(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")

	// 创建 10 个备份（不同时间戳，日期格式 YYYYMMDD-HHMMSS）
	base := time.Now()
	for i := 0; i < 10; i++ {
		ts := base.Add(-time.Duration(i) * time.Hour).Format("20060102-150405")
		backupPath := fmt.Sprintf("%s.%s.bak", settingsPath, ts)
		os.WriteFile(backupPath, []byte("backup"), 0600)
	}

	cleanupOldBackups(settingsPath, 5)

	// 应保留最近 5 个
	entries, _ := os.ReadDir(dir)
	backupCount := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "settings.json.") && strings.HasSuffix(entry.Name(), ".bak") {
			backupCount++
		}
	}
	if backupCount != 5 {
		t.Errorf("备份保留数 = %d, want 5", backupCount)
	}
}
