// Package util — 数据损坏恢复工具（V3-5-1）。
//
// 本文件提供 JSON/JSONL 文件损坏检测与隔离能力：
//   - LoadWithRecovery: 单文件 JSON 加载，损坏时隔离到 backupDir
//   - LoadJSONLWithRecovery: JSONL 逐行加载，损坏行跳过
//   - RepairCorrupted: 扫描 ~/.whale/ 下所有 JSON 文件并隔离损坏文件
//   - quarantineFile: 内部工具，将损坏文件移动到隔离目录
//   - IsJSONFileCorrupted: 只读检测（供 doctor 只读扫描使用）
//
// 设计依据：v3修复方案.md §2.2 V3-5-1（审查修订：删除写入备份，聚焦检测+隔离+doctor）
// 损坏检测策略：json.Unmarshal 失败即判定为损坏（不额外计算校验和，零成本）
// 隔离策略：os.Rename 移动到 ~/.whale/corrupt/<timestamp>/，原路径不再阻塞启动
package util

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrFileCorrupted 表示文件损坏，已被隔离到 backupDir。
// 调用方收到此错误时应重置内存状态为默认值（不阻塞启动）。
var ErrFileCorrupted = errors.New("util: file corrupted and quarantined")

// LoadWithRecovery 带损坏恢复的 JSON 加载。
// path: 文件路径；v: 解码目标；backupDir: 损坏文件隔离目录。
// 返回：
//   - 加载成功返回 nil
//   - 空路径或文件不存在返回 nil（非错误）
//   - 文件为空（0 字节）返回 nil（视为无数据，非损坏）
//   - 文件损坏时隔离到 backupDir 并返回 ErrFileCorrupted
//   - 隔离失败时返回组合错误（不 panic，依据审查修订边界 case）
//   - 其他 IO 错误返回原 error
//
// 并发安全：无需加锁（加载阶段单线程）。
func LoadWithRecovery(path string, v interface{}, backupDir string) error {
	// 空路径：非错误，返回 nil
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在非错误
		}
		return err
	}
	// 空文件（0 字节）：视为无数据，返回 nil（非损坏）
	if info.Size() == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// JSON 解析失败 → 判定为损坏，隔离文件
	if err := json.Unmarshal(data, v); err != nil {
		if qErr := quarantineFile(path, backupDir); qErr != nil {
			// 隔离失败：返回组合错误（不 panic，依据审查修订边界 case）
			return fmt.Errorf("util: file corrupted (%v) and quarantine failed (%v)", err, qErr)
		}
		return ErrFileCorrupted
	}
	return nil
}

// LoadJSONLWithRecovery 带损坏恢复的 JSONL 加载（逐行，损坏行跳过）。
// path: 文件路径；handler: 每行处理函数（handler 返回 error 时该行视为损坏并跳过）；
// backupDir: 损坏文件隔离目录（保留参数以保持 API 对称，JSONL 仅逐行跳过，不整体隔离）。
// 返回：跳过的损坏行数 + 错误。
//
// 行为：
//   - 空路径或文件不存在：返回 (0, nil)
//   - 空文件：返回 (0, nil)
//   - 兼容 \n 与 \r\n 换行符，空行不计入损坏
//   - 逐行调用 handler，handler 返回 error 时计数并跳过
//
// 并发安全：无需加锁（加载阶段单线程）。
func LoadJSONLWithRecovery(path string, handler func([]byte) error, backupDir string) (int, error) {
	// backupDir 参数保留用于未来扩展（如全文件不可读时隔离），当前实现不主动隔离
	_ = backupDir

	// 空路径或文件不存在：非错误
	if path == "" {
		return 0, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if info.Size() == 0 {
		return 0, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	skipped := 0
	// 逐行处理：按 \n 分割
	start := 0
	for i := 0; i <= len(data); i++ {
		if i != len(data) && data[i] != '\n' {
			continue
		}
		line := data[start:i]
		// 去除行尾 \r（Windows 换行符兼容）
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		// 推进下一行起点
		start = i + 1
		// 空行跳过（不计入损坏）
		if len(line) == 0 {
			continue
		}
		// 调用 handler，返回 error 视为损坏行
		if hErr := handler(line); hErr != nil {
			skipped++
		}
	}
	return skipped, nil
}

// RepairCorrupted 扫描指定目录下所有 JSON 文件，隔离损坏文件。
// whaleDir: ~/.whale/ 目录；返回：隔离的文件列表 + 错误。
//
// 行为：
//   - 在 whaleDir/corrupt/<timestamp>/ 下创建隔离目录
//   - 递归扫描 whaleDir，跳过 corrupt 子目录（避免重复扫描隔离区）
//   - 对每个 .json 文件尝试 json.Unmarshal，失败则隔离
//   - 跳过空文件（0 字节，非损坏）与 .bak 文件（备份不隔离）
//   - 返回隔离的源文件路径列表（隔离失败项带后缀标注）
//
// 并发安全：无需加锁（doctor --repair 单次执行）。
func RepairCorrupted(whaleDir string) ([]string, error) {
	if whaleDir == "" {
		return nil, nil
	}
	info, err := os.Stat(whaleDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("util: whaleDir not a directory: %s", whaleDir)
	}

	// 隔离目录：~/.whale/corrupt/<timestamp>/
	backupDir := filepath.Join(whaleDir, "corrupt", time.Now().Format("20060102-150405"))

	var quarantined []string
	err = filepath.Walk(whaleDir, func(path string, fi os.FileInfo, walkErr error) error {
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
		data, rErr := os.ReadFile(path)
		if rErr != nil {
			return nil // 读取失败跳过（不中断扫描）
		}
		// JSON 解析失败 → 隔离
		var probe interface{}
		if jErr := json.Unmarshal(data, &probe); jErr != nil {
			if qErr := quarantineFile(path, backupDir); qErr != nil {
				// 隔离失败不中断扫描，记录到列表（带失败标注）
				quarantined = append(quarantined, path+" (quarantine failed: "+qErr.Error()+")")
			} else {
				quarantined = append(quarantined, path)
			}
		}
		return nil
	})
	if err != nil {
		return quarantined, err
	}
	return quarantined, nil
}

// IsJSONFileCorrupted 只读检测指定文件是否为损坏的 JSON（不隔离）。
// path: 文件路径；返回：true 表示 JSON 解析失败（损坏）。
// 读取失败返回 false（不归类为损坏，doctor 单独报告 IO 错误）。
// 用于 doctor 只读扫描场景，不修改文件。
func IsJSONFileCorrupted(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false // 读取错误不归类为损坏
	}
	var probe interface{}
	return json.Unmarshal(data, &probe) != nil
}

// quarantineFile 将损坏文件移动到隔离目录。
// src: 损坏文件路径；backupDir: 隔离目录。
//
// 行为：
//   - 创建 backupDir（os.MkdirAll 失败返回 error，不 panic）
//   - 目标路径冲突时追加数字后缀（.1, .2, ...）
//   - 优先 os.Rename（同文件系统原子移动）；跨文件系统降级为 copy + remove
//
// 并发安全：无需加锁（加载/修复阶段单线程）。
func quarantineFile(src, backupDir string) error {
	if backupDir == "" {
		return errors.New("util: backupDir is empty")
	}
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		return err
	}
	base := filepath.Base(src)
	dst := filepath.Join(backupDir, base)
	// 文件名冲突时追加后缀
	counter := 1
	for {
		if _, err := os.Stat(dst); os.IsNotExist(err) {
			break
		}
		dst = filepath.Join(backupDir, fmt.Sprintf("%s.%d", base, counter))
		counter++
	}
	// 优先 Rename（同文件系统原子移动）
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	// 跨文件系统降级：copy + remove
	return copyAndRemove(src, dst)
}

// copyAndRemove 跨文件系统隔离降级：复制到目标后删除源文件。
// src: 源文件路径；dst: 目标文件路径。
// 用于 os.Rename 跨文件系统失败时的降级路径。
func copyAndRemove(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return err
	}
	return os.Remove(src)
}
