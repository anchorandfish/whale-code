// capability.go — CapabilityCache: a fallback capability probe backed by
// doctor.Run() plus a local JSON cache.
//
// The early phase probes capabilities by running doctor.Run() and mapping
// severity to a boolean; later phases switch to the dedicated
// ProbeCapability path. Results are cached at
// .whale/orchestrator/capability_cache.json.

package orchestrator

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/anchorandfish/whale-code/internal/doctor"
)

// CapabilityCacheFallbackDecl is a machine-checkable declaration that this
// component is a fallback, including its trade-offs and retirement condition.
// Tests assert its required content.
const CapabilityCacheFallbackDecl = `
CapabilityCache is a FALLBACK implementation:
  Trade-off: no trust_level metadata, only a boolean verdict; keyed by
  (category.name) only, without scope isolation.
  Why it exists: the early phase needs capability probing for router
  downgrade, and a first-class GetCapability API is not built yet.
  Retirement condition: retire once the first-class GetCapability API (with
  trust_level metadata) is available; if it is not available by 2026-12-31,
  re-evaluate promoting this fallback to the permanent solution.
`

// CapabilityCache is the fallback probe: doctor.Run() plus a local JSON cache.
// Retirement condition: retire once a first-class GetCapability API (with
// trust_level metadata) is available; if it is not available by 2026-12-31,
// re-evaluate promoting this fallback to the permanent solution.
type CapabilityCache struct {
	path      string // .whale/orchestrator/capability_cache.json 绝对路径
	mu        sync.RWMutex
	checkedAt int64           // 上次探测时间戳（Unix 秒）
	items     map[string]bool // capability key → bool
}

// NewCapabilityCache 创建 CapabilityCache，path 为 capability_cache.json 绝对路径。
func NewCapabilityCache(path string) (*CapabilityCache, error) {
	// 路径穿越防护：在 Clean 前检查原始 path 是否含 ".."（filepath.Clean 会折叠 .. 导致检测失效）
	if strings.Contains(path, "..") {
		return nil, fmt.Errorf("capability_cache: path contains '..': %s", path)
	}
	cleaned := filepath.Clean(path)
	return &CapabilityCache{
		path:  cleaned,
		items: make(map[string]bool),
	}, nil
}

// ProbeViaDoctor 跑 doctor.Run() 拿 []CheckResult，按 severity 映射为 capability bool（02 §2.7.4）。
// 映射规则：error→false / warning→true / ok→true / 无对应项→unknown（不写入 cache）。
// 探测在编排启动时批量跑一次，运行期 GetCapability 只读缓存不跑探测。
func (c *CapabilityCache) ProbeViaDoctor(ctx context.Context, d *doctor.Doctor) error {
	// N17 现有接口，返回 []CheckResult（不改 N17 签名）
	results := d.Run(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.checkedAt = time.Now().Unix()
	// 清空旧 items（重新探测，防止已修复的能力仍标记 false）
	c.items = make(map[string]bool)

	// 委派 applyDoctorResults 做 severity → bool 映射（可独立测试）
	for k, v := range applyDoctorResults(results) {
		c.items[k] = v
	}

	return c.saveLocked()
}

// applyDoctorResults 将 doctor.CheckResult 列表按 severity 映射为 capability bool map（02 §2.7.4）。
// 映射规则：OK/Warn → true / Error/Fatal → false / 其他 → 不写入（unknown）。
// 此函数纯函数无副作用，便于单元测试 severity 映射逻辑。
func applyDoctorResults(results []doctor.CheckResult) map[string]bool {
	items := make(map[string]bool)
	for _, r := range results {
		// key 格式：env.<Category>.<Name>（如 env.Dependencies.git）
		key := "env." + r.Category + "." + r.Name
		// capability key 写入前校验不含 / \ ..（防注入，地基层 §2.4.2 安全要求）
		if !isValidCapabilityKey(key) {
			continue // 非法 key 跳过，不写入
		}
		switch r.Severity {
		case doctor.SeverityOK, doctor.SeverityWarn:
			items[key] = true // 能力可用（warning 有风险但可用）
		case doctor.SeverityError, doctor.SeverityFatal:
			items[key] = false // 能力不可用
		}
		// 无对应项（doctor 未覆盖）→ 不写入 cache，留空待独立方向填充
	}
	return items
}

// GetCapability 降级版：从缓存读，不跑探测（探测在编排启动时批量跑一次）。
// 缓存未命中（key 不存在）返回 error，调用方按需降级（如默认禁用该能力）。
func (c *CapabilityCache) GetCapability(key string) (bool, error) {
	// key 校验（防注入，地基层 §2.4.2）
	if !isValidCapabilityKey(key) {
		return false, fmt.Errorf("capability_cache: invalid key %q (must not contain / \\ ..)", key)
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	v, ok := c.items[key]
	if !ok {
		return false, fmt.Errorf("capability_cache: key %q not found (run ProbeViaDoctor first)", key)
	}
	return v, nil
}

// Snapshot 返回 capability_snapshot 注入 TaskContext（02 §2.7.1，子任务只读）。
// 返回 map 的副本，外部修改不影响内部状态（防子任务污染编排器状态）。
func (c *CapabilityCache) Snapshot() map[string]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// 深拷贝（map 是引用类型，必须显式复制）
	snap := make(map[string]bool, len(c.items))
	for k, v := range c.items {
		snap[k] = v
	}
	return snap
}

// Save 持久化到 capability_cache.json（加锁写，地基层 §2.4.1 存储位置）。
func (c *CapabilityCache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.saveLocked()
}

// saveLocked 持锁状态下的保存实现（内部调用，已持有 c.mu）。
func (c *CapabilityCache) saveLocked() error {
	// 确保目录存在
	if dir := filepath.Dir(c.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("capability_cache: mkdir %s: %w", dir, err)
		}
	}

	data := struct {
		CheckedAt int64           `json:"checked_at"`
		Items     map[string]bool `json:"items"`
	}{
		CheckedAt: c.checkedAt,
		Items:     c.items,
	}

	buf, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("capability_cache: marshal: %w", err)
	}

	// 原子写入：先写临时文件再 rename（防写入中断导致文件损坏）
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("capability_cache: write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		return fmt.Errorf("capability_cache: rename %s → %s: %w", tmp, c.path, err)
	}
	return nil
}

// Load 从 capability_cache.json 加载（启动时调用，若文件不存在返回空 cache 不报错）。
func (c *CapabilityCache) Load() error {
	// 路径穿越防护（双层防护，NewCapabilityCache 已校验，Load 再校验防外部修改 path）
	if strings.Contains(c.path, "..") {
		return fmt.Errorf("capability_cache: path contains '..': %s", c.path)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	buf, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件不存在时 Load 返回空 cache 不报错（地基层 §2.7.4）
			c.items = make(map[string]bool)
			c.checkedAt = 0
			return nil
		}
		return fmt.Errorf("capability_cache: read %s: %w", c.path, err)
	}

	var data struct {
		CheckedAt int64           `json:"checked_at"`
		Items     map[string]bool `json:"items"`
	}
	if err := json.Unmarshal(buf, &data); err != nil {
		return fmt.Errorf("capability_cache: unmarshal: %w", err)
	}

	// 校验每个 key 合法性（防缓存文件被篡改注入非法 key）
	c.items = make(map[string]bool)
	for k, v := range data.Items {
		if isValidCapabilityKey(k) {
			c.items[k] = v
		}
	}
	c.checkedAt = data.CheckedAt
	return nil
}

// isValidCapabilityKey 校验 capability key 合法性（防注入，地基层 §2.4.2 安全要求）。
// 规则：非空 + 不含 / \ .. + 不含空格 + 不含控制字符。
func isValidCapabilityKey(key string) bool {
	if key == "" {
		return false
	}
	if strings.ContainsAny(key, "/\\\x00 ") {
		return false
	}
	if strings.Contains(key, "..") {
		return false
	}
	return true
}

// CheckedAt 返回上次探测时间戳（仅用于诊断）。
func (c *CapabilityCache) CheckedAt() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.checkedAt
}
