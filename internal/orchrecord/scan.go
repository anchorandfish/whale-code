package orchrecord

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ScanAllQuality 全量扫描 quality.jsonl 文件返回所有最新记录（去重，每 TaskID 仅返回最后一行）。
//
// 这是「读取契约」的权威实现（原 orchestrator.QualityStore.ScanAll 的纯读取逻辑，
// 从写入端剥离后下沉至此）。memory 侧复用它与 orchestrator 侧保持严格一致。
//
// 注意：返回顺序按 TaskID 首次出现顺序，不保证时间序；调用方需自行排序。
func ScanAllQuality(path string) ([]QualityRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 文件不存在视为空 store
		}
		return nil, fmt.Errorf("orchrecord: scanall open %s: %w", path, err)
	}
	defer f.Close()

	// 第一遍：扫描所有行，记录每个 TaskID 的最新偏移
	latestOffsets := make(map[string]int64)
	reader := bufio.NewReader(f)
	var offset int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			var probe struct {
				TaskID string `json:"task_id"`
			}
			if jerr := json.Unmarshal(line, &probe); jerr == nil && probe.TaskID != "" {
				latestOffsets[probe.TaskID] = offset
			}
			offset += int64(len(line))
		}
		if err != nil {
			break
		}
	}

	// 第二遍：按最新偏移读取每条记录
	var records []QualityRecord
	for _, off := range latestOffsets {
		if _, err := f.Seek(off, 0); err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		if scanner.Scan() {
			var rec QualityRecord
			if err := json.Unmarshal(scanner.Bytes(), &rec); err == nil {
				records = append(records, rec)
			}
		}
	}
	return records, nil
}
