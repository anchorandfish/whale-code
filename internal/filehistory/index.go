package filehistory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/anchorandfish/whale-code/internal/util"
)

// appendEntry 追加一条索引记录到 JSONL 文件（O_APPEND，文件锁保护）
// 使用跨进程文件锁确保并发安全，O_APPEND 保证追加写不重写整个文件
func appendEntry(indexFile string, entry IndexEntry) error {
	lockPath := filepath.Join(filepath.Dir(indexFile), "flock")
	return util.WithFileLock(lockPath, func(_ *os.File) error {
		// 确保目录存在
		if err := os.MkdirAll(filepath.Dir(indexFile), 0755); err != nil {
			return fmt.Errorf("create index dir: %w", err)
		}

		// 以 O_APPEND 模式打开（追加写，不重写整个文件）
		f, err := os.OpenFile(indexFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open index file: %w", err)
		}
		defer f.Close()

		data, err := marshalEntry(entry)
		if err != nil {
			return fmt.Errorf("marshal entry: %w", err)
		}

		if _, err := f.Write(data); err != nil {
			return fmt.Errorf("write entry: %w", err)
		}
		return nil
	})
}

// scanEntries 从头扫描索引文件，返回所有记录。
//
// S10 / HG1（决策 HG1=a，fail-closed）：遇到损坏行时**返回错误**，不再静默跳过。
// 静默跳过会造成"部分加载的索引"——rewind/undo 会基于不完整的历史对工作树做
// 删除/覆盖，属于静默数据损坏风险。调用方据此拒绝启动文件历史或拒绝 rewind。
// 注意：文件不存在（首次使用）仍返回空列表、不报错。
func scanEntries(indexFile string) ([]IndexEntry, error) {
	f, err := os.Open(indexFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 文件不存在时返回空列表，不报错
		}
		return nil, fmt.Errorf("open index file: %w", err)
	}
	defer f.Close()

	var entries []IndexEntry
	scanner := bufio.NewScanner(f)
	// 增大缓冲区，支持较长的 JSON 行
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, err := unmarshalEntry(line)
		if err != nil {
			// HG1：fail-closed —— 损坏行不再静默跳过，直接上抛（含行号便于人工定位/恢复）
			return nil, fmt.Errorf("corrupt index line %d in %s: %w", lineNo, indexFile, err)
		}
		entries = append(entries, *entry)
	}

	// scanner.Err() 返回非 EOF 的读取错误
	if err := scanner.Err(); err != nil {
		return entries, fmt.Errorf("scan index: %w", err)
	}

	return entries, nil
}

// scanEntriesAfter 扫描指定 messageId 之后的记录
// 包含 messageId 对应的记录本身
func scanEntriesAfter(indexFile string, msgId string) ([]IndexEntry, error) {
	all, err := scanEntries(indexFile)
	if err != nil {
		return nil, err
	}

	var result []IndexEntry
	found := false
	for _, entry := range all {
		if entry.MsgId == msgId {
			found = true
		}
		if found {
			result = append(result, entry)
		}
	}
	return result, nil
}

// findEntry 查找指定 messageId + type 的记录
// 返回第一条匹配的记录，未找到时返回 nil
func findEntry(indexFile string, msgId string, entryType string) (*IndexEntry, error) {
	entries, err := scanEntries(indexFile)
	if err != nil {
		return nil, err
	}

	for i := range entries {
		if entries[i].MsgId == msgId && entries[i].Type == entryType {
			return &entries[i], nil
		}
	}
	return nil, nil
}

// truncateAfter 删除指定 messageId 之后的所有记录
// 实现策略：读取 msgId 前的记录（含 msgId 本身）→ 原子写新文件 → rename 替换
func truncateAfter(indexFile string, msgId string) error {
	lockPath := filepath.Join(filepath.Dir(indexFile), "flock")
	return util.WithFileLock(lockPath, func(_ *os.File) error {
		// 读取所有记录
		entries, err := scanEntries(indexFile)
		if err != nil {
			return fmt.Errorf("scan entries: %w", err)
		}

		// 找到 msgId 对应的最后一条记录的位置（保留 msgId 及之前的记录）
		cutIdx := -1
		for i, entry := range entries {
			if entry.MsgId == msgId {
				cutIdx = i
			}
		}

		if cutIdx < 0 {
			// msgId 不存在，不截断
			return nil
		}

		// 保留 cutIdx 及之前的记录
		keep := entries[:cutIdx+1]

		// 序列化保留的记录
		var data []byte
		for _, entry := range keep {
			line, err := marshalEntry(entry)
			if err != nil {
				return fmt.Errorf("marshal entry: %w", err)
			}
			data = append(data, line...)
		}

		// 原子写入（tmp + rename）
		tmpFile := indexFile + ".tmp"
		if err := os.WriteFile(tmpFile, data, 0o600); err != nil {
			return fmt.Errorf("write tmp index: %w", err)
		}
		if err := os.Rename(tmpFile, indexFile); err != nil {
			os.Remove(tmpFile)
			return fmt.Errorf("rename tmp index: %w", err)
		}

		return nil
	})
}

// marshalEntry 将索引记录序列化为 JSON 行（含换行符）
func marshalEntry(entry IndexEntry) ([]byte, error) {
	data, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("json marshal: %w", err)
	}
	// 追加换行符，确保每条记录占一行
	return append(data, '\n'), nil
}

// unmarshalEntry 从 JSON 行反序列化索引记录
func unmarshalEntry(line []byte) (*IndexEntry, error) {
	var entry IndexEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	return &entry, nil
}
