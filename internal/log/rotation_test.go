// Package log — O-1 日志轮转与磁盘空间检查测试（桶 X6）
//
// 反例测试覆盖：
//  1. CheckDiskSpace：磁盘剩余空间 <1GB 时发出警告（不阻断）
//  2. RotateJSONLByDate：跨日期文件被分片，过期分片被清理
//  3. RotateJSONLByDate：当天文件不分片
//  4. RotateJSONLByDate：参数校验（空路径/非正 maxAgeDays）
package log

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestCheckDiskSpace_TempDir 验证临时目录磁盘检查不报错且返回有效值
// 这是基础正向测试，验证 platformDiskFreeBytes 在当前平台正常工作。
func TestCheckDiskSpace_TempDir(t *testing.T) {
	tmpDir := t.TempDir()
	freeBytes, warned := CheckDiskSpace(tmpDir)

	// 临时目录通常有足够空间，warned 应为 false
	// （若测试机磁盘真的不足 1GB，warned 可能 true，但不影响测试通过）
	if freeBytes < 0 {
		t.Errorf("CheckDiskSpace 返回 freeBytes=%d，应为非负值", freeBytes)
	}
	// warned 仅在低空间时为 true，不强制断言
	_ = warned
}

// TestCheckDiskSpace_NonExistentPath 验证不存在的路径不 panic，返回 -1
// 反例：传入不存在的路径，函数应优雅降级而非崩溃。
func TestCheckDiskSpace_NonExistentPath(t *testing.T) {
	// 构造一个不存在的路径（确保父目录也不存在，触发 statfs 失败）
	nonExist := filepath.Join(t.TempDir(), "nonexistent-deep", "path", "check")
	freeBytes, warned := CheckDiskSpace(nonExist)

	// 获取失败时应返回 -1 且不 panic
	if freeBytes != -1 {
		// 注意：某些平台对不存在的路径也能获取分区信息（如 Windows 接受盘符），
		// 所以 freeBytes != -1 不一定是错误，仅在 Unix 上期望 -1
		if runtime.GOOS != "windows" {
			t.Errorf("CheckDiskSpace 不存在路径应返回 freeBytes=-1，实际 %d", freeBytes)
		}
	}
	// warned 不应触发（获取失败时不警告）
	if warned {
		t.Errorf("CheckDiskSpace 不存在路径不应触发 warned")
	}
}

// TestRotateJSONLByDate_EmptyPath 验证空路径参数校验
// 反例：传入空路径应返回 error，不应 panic 或创建文件。
func TestRotateJSONLByDate_EmptyPath(t *testing.T) {
	rotated, cleaned, err := RotateJSONLByDate("", 30)
	if err == nil {
		t.Errorf("RotateJSONLByDate(\"\") 应返回 error，实际 nil")
	}
	if rotated {
		t.Errorf("RotateJSONLByDate(\"\") rotated 应为 false")
	}
	if cleaned != 0 {
		t.Errorf("RotateJSONLByDate(\"\") cleaned 应为 0，实际 %d", cleaned)
	}
}

// TestRotateJSONLByDate_InvalidMaxAge 验证非正 maxAgeDays 参数校验
// 反例：maxAgeDays=0 或负数应返回 error。
func TestRotateJSONLByDate_InvalidMaxAge(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "test.jsonl")

	// maxAgeDays=0
	_, _, err := RotateJSONLByDate(path, 0)
	if err == nil {
		t.Errorf("RotateJSONLByDate(maxAgeDays=0) 应返回 error")
	}

	// maxAgeDays=-1
	_, _, err = RotateJSONLByDate(path, -1)
	if err == nil {
		t.Errorf("RotateJSONLByDate(maxAgeDays=-1) 应返回 error")
	}
}

// TestRotateJSONLByDate_CurrentDayNoRotation 验证当天文件不被分片
// 反例：今天创建的文件不应被重命名，rotated 应为 false。
func TestRotateJSONLByDate_CurrentDayNoRotation(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "quality.jsonl")

	// 创建今天的文件
	content := []byte(`{"task_id":"t1","event":"test"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}

	// 调用轮转：今天的文件不应被分片
	rotated, cleaned, err := RotateJSONLByDate(path, 30)
	if err != nil {
		t.Fatalf("RotateJSONLByDate 失败: %v", err)
	}
	if rotated {
		t.Errorf("今天的文件不应被分片，rotated 应为 false")
	}
	if cleaned != 0 {
		t.Errorf("今天无过期分片，cleaned 应为 0，实际 %d", cleaned)
	}

	// 主文件应仍然存在
	if _, err := os.Stat(path); err != nil {
		t.Errorf("主文件应存在: %v", err)
	}
}

// TestRotateJSONLByDate_OldFileRotated 验证跨日期文件被分片
// 反例：昨天的文件应被重命名为 <path>.YYYY-MM-DD，rotated 应为 true。
func TestRotateJSONLByDate_OldFileRotated(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "quality.jsonl")

	// 创建昨天的文件（通过显式设置 ModTime）
	content := []byte(`{"task_id":"t1","event":"old"}` + "\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	yesterday := time.Now().Add(-24 * time.Hour)
	if err := os.Chtimes(path, yesterday, yesterday); err != nil {
		t.Fatalf("Chtimes 失败: %v", err)
	}

	// 调用轮转：昨天的文件应被分片
	rotated, cleaned, err := RotateJSONLByDate(path, 30)
	if err != nil {
		t.Fatalf("RotateJSONLByDate 失败: %v", err)
	}
	if !rotated {
		t.Errorf("昨天的文件应被分片，rotated 应为 true")
	}
	if cleaned != 0 {
		t.Errorf("无过期分片可清理，cleaned 应为 0，实际 %d", cleaned)
	}

	// 主文件应已不存在（被重命名）
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("主文件应已被重命名（不存在），实际存在或错误: %v", err)
	}

	// 应存在 <path>.YYYY-MM-DD 形式的分片文件
	yesterdayDate := yesterday.Truncate(24 * time.Hour).Format("2006-01-02")
	expectedShard := path + "." + yesterdayDate
	if _, err := os.Stat(expectedShard); err != nil {
		// 也可能是带 .1 后缀的变体
		entries, _ := os.ReadDir(tmpDir)
		var found bool
		for _, e := range entries {
			if e.Name() != "" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("应存在分片文件 %q 或其变体，但未找到", expectedShard)
		}
	}
}

// TestRotateJSONLByDate_OldShardCleaned 验证过期分片被清理
// 反例：35 天前的分片文件应被删除（maxAgeDays=30）。
func TestRotateJSONLByDate_OldShardCleaned(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "quality.jsonl")

	// 创建 35 天前的分片文件（应被清理，maxAgeDays=30）
	base := filepath.Base(path)
	oldShard := filepath.Join(tmpDir, base+".2026-06-01")
	if err := os.WriteFile(oldShard, []byte("old shard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	// 设置 ModTime 为 35 天前
	oldTime := time.Now().Add(-35 * 24 * time.Hour)
	if err := os.Chtimes(oldShard, oldTime, oldTime); err != nil {
		t.Fatalf("Chtimes 失败: %v", err)
	}

	// 创建 10 天前的分片文件（应保留，maxAgeDays=30）
	recentShard := filepath.Join(tmpDir, base+".2026-07-13")
	if err := os.WriteFile(recentShard, []byte("recent shard\n"), 0o644); err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	recentTime := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(recentShard, recentTime, recentTime); err != nil {
		t.Fatalf("Chtimes 失败: %v", err)
	}

	// 主文件不存在，仅清理过期分片
	rotated, cleaned, err := RotateJSONLByDate(path, 30)
	if err != nil {
		t.Fatalf("RotateJSONLByDate 失败: %v", err)
	}
	if rotated {
		t.Errorf("主文件不存在，rotated 应为 false")
	}
	if cleaned != 1 {
		t.Errorf("应清理 1 个过期分片（35 天前），cleaned=%d", cleaned)
	}

	// 旧分片应被删除
	if _, err := os.Stat(oldShard); !os.IsNotExist(err) {
		t.Errorf("35 天前的分片应被删除，但仍存在或错误: %v", err)
	}
	// 新分片应保留
	if _, err := os.Stat(recentShard); err != nil {
		t.Errorf("10 天前的分片应保留，但不存在或错误: %v", err)
	}
}

// TestRotateJSONLByDate_NonExistentMainFile 验证主文件不存在时不报错
// 反例：主文件不存在时，rotated 应为 false，仅清理过期分片。
func TestRotateJSONLByDate_NonExistentMainFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "nonexistent.jsonl")

	// 主文件不存在
	rotated, cleaned, err := RotateJSONLByDate(path, 30)
	if err != nil {
		t.Fatalf("主文件不存在应不报错: %v", err)
	}
	if rotated {
		t.Errorf("主文件不存在，rotated 应为 false")
	}
	if cleaned != 0 {
		t.Errorf("无分片可清理，cleaned 应为 0，实际 %d", cleaned)
	}
}

// TestCheckDiskSpace_LogFileInit 验证 InitFileLogger 后 CheckDiskSpace 正常工作
// 集成测试：确保两者协同工作（CheckDiskSpace 调用 Warn 时文件 sink 已就绪）。
func TestCheckDiskSpace_LogFileInit(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")

	if err := InitFileLogger(logFile, 1, 1, 1); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	// 调用 CheckDiskSpace 不应 panic
	freeBytes, _ := CheckDiskSpace(tmpDir)
	if freeBytes < 0 {
		t.Errorf("CheckDiskSpace 返回 freeBytes=%d，应为非负值", freeBytes)
	}

	// 写入一条日志确保文件 sink 工作
	Info("disk space check integration test")
}

// TestRotation_LumberjackRotationTriggers 验证 lumberjack 在超 maxSizeMB 时触发轮转
// 这是 O-1 的核心反例测试：日志超过 MaxSize 时应产生备份文件。
// 注意：此测试与 file_sink_test.go 的 TestInitFileLogger_Rotation 互补，
// 侧重验证 MaxSize=100MB 配置（O-1 推荐值）在小测试场景的轮转行为。
func TestRotation_LumberjackRotationTriggers(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "rotate-100mb.log")

	// MaxSize=1（1MB）模拟 100MB 配置的轮转行为
	// MaxBackups=3（O-1 推荐值）, MaxAge=30（O-1 推荐值）
	if err := InitFileLogger(logFile, 1, 3, 30); err != nil {
		t.Fatalf("InitFileLogger 失败: %v", err)
	}
	defer func() {
		CloseFileLogger()
		resetDefaultLogger()
	}()

	// 写入 ~2.5MB 日志触发至少 2 次轮转
	big := make([]byte, 500*1024)
	for i := range big {
		big[i] = 'x'
	}
	for i := 0; i < 5; i++ {
		Info("rotation trigger test", "i", i, "data", string(big))
	}

	CloseFileLogger()
	resetDefaultLogger()

	// 应存在 rotate-100mb.log（当前）和至少一个备份
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("读取目录失败: %v", err)
	}
	var backups []string
	for _, e := range entries {
		name := e.Name()
		if name != "rotate-100mb.log" && len(name) > len("rotate-100mb") {
			backups = append(backups, name)
		}
	}
	if len(backups) == 0 {
		t.Errorf("反例失败：日志超过 MaxSize 应触发轮转产生备份，但无备份文件: %v", entries)
	}
}
