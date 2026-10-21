// probe_persist.go — P0-3 ProbeFailCount 旁路持久化到 probe_fail.jsonl。
//
// 对标 PIT-002 quality.jsonl 模式：yaml:"-" 字段不持久化到配置文件，
// 但旁路记账到独立 jsonl 文件，使 PIT-001 防护（连续 3 次失败转 contradicted）跨会话生效。
//
// 设计要点：
// - 内存缓存 + 懒加载：首次访问时从 probe_fail.jsonl 加载，后续读缓存
// - 追加写入：每次 fail count 变化追加一行 JSON 到 probe_fail.jsonl
// - 降级策略：文件读写失败仅更新内存缓存，不阻断探测逻辑
// - 测试隔离：ResetProbeFailCacheForTest 清空缓存 + 阻止文件读写
package memory

import (
	"encoding/json"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// probeFailRecord 是 probe_fail.jsonl 的一条记录（P0-3 旁路持久化）。
type probeFailRecord struct {
	Name      string `json:"name"`       // 记忆条目 Name（即 capability key）
	FailCount int    `json:"fail_count"` // 连续失败次数
	Timestamp int64  `json:"timestamp"`  // 记录时间（Unix 秒）
}

// probeFailCache 旁路持久化的内存缓存（避免每次都读文件）。
type probeFailCache struct {
	mu       sync.Mutex
	loaded   bool           // 是否已从文件加载
	counts   map[string]int // name → fail count
	filePath string         // probe_fail.jsonl 路径
}

// globalProbeFailCache 全局 ProbeFailCount 旁路缓存（进程级单例）。
var globalProbeFailCache = &probeFailCache{
	counts: make(map[string]int),
}

// defaultProbeFailPath 返回 probe_fail.jsonl 默认路径（~/.whale/memory/probe_fail.jsonl）。
// HOME 不可用时返回空字符串（降级为仅内存缓存）。
func defaultProbeFailPath() string {
	// 收口：Dir() 返回空（home 不可得）⇒ 降级为仅内存缓存，行为同前。
	if whalehome.Dir() == "" {
		return ""
	}
	return filepath.Join(whalehome.Dir(), "memory", "probe_fail.jsonl")
}

// ensureLoaded 从 probe_fail.jsonl 懒加载到缓存（仅首次调用执行）。
// 文件不存在或读取失败时降级为空缓存（首次启动或文件损坏）。
func (c *probeFailCache) ensureLoaded() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded {
		return
	}
	c.loaded = true
	if c.filePath == "" {
		c.filePath = defaultProbeFailPath()
	}
	if c.filePath == "" {
		return // HOME 不可用，仅用内存缓存
	}
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return // 文件不存在或读取失败，首次启动
	}
	// 逐行解析，保留每个 name 的最后一条记录（jsonl 追加写入，后写入的为最新）
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec probeFailRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // 跳过损坏行
		}
		c.counts[rec.Name] = rec.FailCount
	}
}

// persistProbeFailCount 将 fail count 旁路写入 probe_fail.jsonl（P0-3）。
// 同时更新内存缓存，确保同进程内后续读取一致。
// 文件写入失败时仅更新内存缓存（降级，不阻断探测逻辑）。
func persistProbeFailCount(name string, count int) error {
	globalProbeFailCache.ensureLoaded()
	globalProbeFailCache.mu.Lock()
	defer globalProbeFailCache.mu.Unlock()

	// 更新内存缓存
	globalProbeFailCache.counts[name] = count

	// filePath 为空时（如测试环境或 HOME 不可用），仅更新内存缓存
	if globalProbeFailCache.filePath == "" {
		return nil
	}

	rec := probeFailRecord{
		Name:      name,
		FailCount: count,
		Timestamp: time.Now().Unix(),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	// 确保目录存在
	dir := filepath.Dir(globalProbeFailCache.filePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	// 追加写入（jsonl 格式，每行一条 JSON 记录）
	f, err := os.OpenFile(globalProbeFailCache.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// restoreProbeFailCount 从缓存恢复指定 name 的 ProbeFailCount（P0-3 跨会话恢复）。
// 首次调用时懒加载 probe_fail.jsonl 到缓存，后续直接读缓存。
func restoreProbeFailCount(name string) int {
	globalProbeFailCache.ensureLoaded()
	globalProbeFailCache.mu.Lock()
	defer globalProbeFailCache.mu.Unlock()
	return globalProbeFailCache.counts[name]
}

// LoadProbeFailCounts 从 probe_fail.jsonl 加载所有 name 的 fail count（公开 API，P0-3）。
// 供外部在批量加载记忆时恢复 ProbeFailCount（跨会话连续失败计数）。
func LoadProbeFailCounts() (map[string]int, error) {
	globalProbeFailCache.ensureLoaded()
	globalProbeFailCache.mu.Lock()
	defer globalProbeFailCache.mu.Unlock()
	result := make(map[string]int, len(globalProbeFailCache.counts))
	for k, v := range globalProbeFailCache.counts {
		result[k] = v
	}
	return result, nil
}

// ResetProbeFailCacheForTest 测试间重置 ProbeFailCount 旁路缓存（避免跨测试状态污染）。
// 标记 loaded=true 阻止从文件读取，清空 filePath 阻止写入文件。
func ResetProbeFailCacheForTest() {
	globalProbeFailCache.mu.Lock()
	defer globalProbeFailCache.mu.Unlock()
	globalProbeFailCache.loaded = true
	globalProbeFailCache.counts = make(map[string]int)
	globalProbeFailCache.filePath = ""
}
