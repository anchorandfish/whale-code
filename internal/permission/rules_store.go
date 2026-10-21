package permission

import (
	"encoding/json"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sync"

	"github.com/anchorandfish/whale-code/internal/log"
	"github.com/anchorandfish/whale-code/internal/util"
)

// PermissionSettings is the structure of permission rules in settings.json
type PermissionSettings struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// RuleStore is the rule persistence store
type RuleStore struct {
	mu       sync.RWMutex
	settings map[RuleSource]*PermissionSettings
	paths    map[RuleSource]string
}

// NewRuleStore creates a rule store
func NewRuleStore() *RuleStore {
	return &RuleStore{
		settings: make(map[RuleSource]*PermissionSettings),
		paths:    make(map[RuleSource]string),
	}
}

// SetPathForSource sets the persistence path for a source
func (rs *RuleStore) SetPathForSource(source RuleSource, path string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.paths[source] = path
}

// settingsFileEnvelope 是 settings.json 文件的反序列化信封，
// 用于检测顶层 priorityMode 字段（R-2 向后兼容修复）。
//
// 红队终审 R-2（112-其他未考虑问题与建议）：
// 桶 X4 将 NewRuleSet() 默认从 PriorityLegacy 改为 PriorityPosix，
// 但旧配置文件无 priorityMode 字段，加载后默认 Posix 导致规则评估顺序反转。
// 修复：通过 *string 指针区分"字段缺失"（旧配置，回退 Legacy）与
// "字段存在但空/非法"（按 Posix 默认处理）。
type settingsFileEnvelope struct {
	Permissions  PermissionSettings `json:"permissions"`
	PriorityMode *string            `json:"priorityMode,omitempty"`
}

// applyPriorityModeFromEnvelope 根据配置文件信封的 priorityMode 字段设置 RuleSet 的优先级模式。
//
// R-2 向后兼容策略：
//   - priorityMode == nil（字段缺失，旧配置）：回退 PriorityLegacy（向后兼容）
//   - priorityMode == "posix"：设置 PriorityPosix
//   - priorityMode == "legacy"：设置 PriorityLegacy
//   - priorityMode 为其他值（非法）：fail-closed 回填 PriorityPosix（与 NewRuleSet 默认一致）
//
// 设计依据：旧配置文件（v0.x）无 priorityMode 字段，升级后保持 Legacy 语义；
// 新配置文件（v1.0+）显式声明 priorityMode，按配置加载。
func applyPriorityModeFromEnvelope(ruleSet *RuleSet, envelope *settingsFileEnvelope) {
	if envelope == nil || envelope.PriorityMode == nil {
		// 旧配置文件（无 priorityMode 字段）：回退 Legacy 保持向后兼容
		ruleSet.SetPriorityMode(PriorityLegacy)
		return
	}
	// 新配置文件：按值设置（SetPriorityMode 内部对非法值 fail-closed 回填 Posix）
	ruleSet.SetPriorityMode(PriorityMode(*envelope.PriorityMode))
}

// Load rules from a source's settings file
func (rs *RuleStore) Load(source RuleSource) (*RuleSet, error) {
	rs.mu.RLock()
	path := rs.paths[source]
	rs.mu.RUnlock()

	ruleSet := NewRuleSet()
	if path == "" {
		return ruleSet, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ruleSet, nil
		}
		return nil, err
	}

	var envelope settingsFileEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ruleSet, nil
	}

	// R-2 修复：根据配置文件 priorityMode 字段设置优先级模式（缺失回退 Legacy）
	applyPriorityModeFromEnvelope(ruleSet, &envelope)

	rs.mu.Lock()
	rs.settings[source] = &envelope.Permissions
	rs.mu.Unlock()

	for _, ruleStr := range envelope.Permissions.Allow {
		rule, err := ParseRuleString(ruleStr, RuleAllow, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}
	for _, ruleStr := range envelope.Permissions.Deny {
		rule, err := ParseRuleString(ruleStr, RuleDeny, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}
	for _, ruleStr := range envelope.Permissions.Ask {
		rule, err := ParseRuleString(ruleStr, RuleAsk, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}

	return ruleSet, nil
}

// LoadAll loads rules from all configured sources and merges them
//
// R-2 修复（红队终审 112-其他未考虑问题与建议）：
// 合并多个 source 时，priorityMode 按"任一 source 是 Legacy 则 merged 用 Legacy"策略。
// 原因：用户可能有 user 级新配置 + project 级旧配置的混合场景，
// 用 Legacy 更安全（向后兼容优先）；Posix 模式需所有 source 显式 opt-in。
// 所有 source 都无 priorityMode 字段（全旧配置）→ merged 用 Legacy（向后兼容）。
// 所有 source 都显式 priorityMode=posix → merged 用 Posix（新配置 opt-in）。
func (rs *RuleStore) LoadAll() (*RuleSet, error) {
	merged := NewRuleSet()

	sources := []RuleSource{
		SourceCLIArg,
		SourceSession,
		SourceLocalSettings,
		SourceProjectSettings,
		SourceUserSettings,
	}

	// R-2：跟踪是否有任一 source 是 Legacy（旧配置或显式 legacy）
	anyLegacy := false

	for _, source := range sources {
		set, err := rs.Load(source)
		if err != nil {
			continue
		}
		for _, rule := range set.List() {
			merged.Add(rule)
		}
		// 检查 source 的 priorityMode（Load 已根据配置文件设置）
		if set.PriorityMode() == PriorityLegacy {
			anyLegacy = true
		}
	}

	// R-2：任一 source 是 Legacy → merged 用 Legacy（向后兼容优先）
	if anyLegacy {
		merged.SetPriorityMode(PriorityLegacy)
	}

	return merged, nil
}

// Save rules of a given source to the settings file
//
// R-2 修复：保存时同时持久化 ruleSet.PriorityMode()，使下次加载保持一致语义。
// 旧配置文件升级后无 priorityMode 字段 → 加载时回退 Legacy（向后兼容）；
// 用户通过 SetPriorityMode 显式切换后保存 → 写入 priorityMode 字段，下次加载按配置。
func (rs *RuleStore) Save(source RuleSource, ruleSet *RuleSet) error {
	if source == SourceCLIArg || source == SourceSession {
		return nil // memory sources are not persisted
	}

	rs.mu.RLock()
	path := rs.paths[source]
	rs.mu.RUnlock()

	if path == "" {
		return nil
	}

	settings := &PermissionSettings{}

	// Only persist rules that belong to this source. Before RuleStore was wired
	// (P0 fix), Save was never reached in production, so passing the merged rule
	// set here was harmless. Once wired, saving the merged set would write every
	// source's rules into a single source's file (cross-source pollution).
	for _, rule := range ruleSet.ListBySource(source) {
		ruleStr := rule.String()
		switch rule.Behavior {
		case RuleAllow:
			settings.Allow = append(settings.Allow, ruleStr)
		case RuleDeny:
			settings.Deny = append(settings.Deny, ruleStr)
		case RuleAsk:
			settings.Ask = append(settings.Ask, ruleStr)
		}
	}

	return rs.writeSettings(path, settings, ruleSet.PriorityMode())
}

// SaveMerged saves all rules to the appropriate sources
func (rs *RuleStore) SaveMerged(ruleSet *RuleSet) error {
	bySource := make(map[RuleSource][]PermissionRule)
	for _, rule := range ruleSet.List() {
		if rule.Source == SourceCLIArg || rule.Source == SourceSession {
			continue
		}
		bySource[rule.Source] = append(bySource[rule.Source], rule)
	}

	for source, rules := range bySource {
		sourceSet := NewRuleSet()
		for _, rule := range rules {
			sourceSet.Add(rule)
		}
		if err := rs.Save(source, sourceSet); err != nil {
			return err
		}
	}

	return nil
}

// AddRuleToSettings adds a single rule to the settings file for a given source
func (rs *RuleStore) AddRuleToSettings(source RuleSource, rule PermissionRule) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	set, err := rs.loadLocked(source)
	if err != nil {
		return err
	}
	set.Add(rule)
	return rs.saveLocked(source, set)
}

// RemoveRuleFromSettings removes a single rule from the settings file
func (rs *RuleStore) RemoveRuleFromSettings(source RuleSource, rule PermissionRule) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	set, err := rs.loadLocked(source)
	if err != nil {
		return err
	}
	set.RemoveByRule(rule)
	return rs.saveLocked(source, set)
}

// loadLocked loads rules while already holding the write lock
func (rs *RuleStore) loadLocked(source RuleSource) (*RuleSet, error) {
	path := rs.paths[source]

	ruleSet := NewRuleSet()
	if path == "" {
		return ruleSet, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ruleSet, nil
		}
		return nil, err
	}

	var envelope settingsFileEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return ruleSet, nil
	}

	// R-2 修复：根据配置文件 priorityMode 字段设置优先级模式（缺失回退 Legacy）
	applyPriorityModeFromEnvelope(ruleSet, &envelope)

	rs.settings[source] = &envelope.Permissions

	for _, ruleStr := range envelope.Permissions.Allow {
		rule, err := ParseRuleString(ruleStr, RuleAllow, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}
	for _, ruleStr := range envelope.Permissions.Deny {
		rule, err := ParseRuleString(ruleStr, RuleDeny, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}
	for _, ruleStr := range envelope.Permissions.Ask {
		rule, err := ParseRuleString(ruleStr, RuleAsk, source)
		if err == nil {
			ruleSet.Add(*rule)
		}
	}

	return ruleSet, nil
}

// saveLocked saves rules while already holding the write lock
// Does NOT re-acquire rs.mu — callers must already hold the lock
//
// R-2 修复：保存时同时持久化 ruleSet.PriorityMode()，使下次加载保持一致语义。
func (rs *RuleStore) saveLocked(source RuleSource, ruleSet *RuleSet) error {
	if source == SourceCLIArg || source == SourceSession {
		return nil
	}

	path := rs.paths[source]
	if path == "" {
		return nil
	}

	settings := &PermissionSettings{}
	for _, rule := range ruleSet.ListBySource(source) {
		ruleStr := rule.String()
		switch rule.Behavior {
		case RuleAllow:
			settings.Allow = append(settings.Allow, ruleStr)
		case RuleDeny:
			settings.Deny = append(settings.Deny, ruleStr)
		case RuleAsk:
			settings.Ask = append(settings.Ask, ruleStr)
		}
	}

	return rs.writeSettings(path, settings, ruleSet.PriorityMode())
}

// writeSettings writes permission settings to a file while preserving other settings
// Uses atomic write to prevent data corruption on crash
//
// R-2 修复：新增 mode 参数，写入 priorityMode 字段到配置文件。
// mode 非 "legacy" 时写入（posix 或其他），legacy 也写入以保持显式语义。
// 这样下次加载时 applyPriorityModeFromEnvelope 能正确读取并设置。
func (rs *RuleStore) writeSettings(filePath string, perms *PermissionSettings, mode PriorityMode) error {
	settings := make(map[string]interface{})

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	if existing, err := os.ReadFile(filePath); err == nil {
		if err := json.Unmarshal(existing, &settings); err != nil {
			log.Warn("Failed to parse existing settings, overwriting", "error", err)
		}
	}

	settings["permissions"] = perms
	// R-2：写入 priorityMode 字段（持久化当前模式，便于下次加载恢复）
	if mode != "" {
		settings["priorityMode"] = string(mode)
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}

	return util.AtomicWriteFile(filePath, append(data, '\n'), 0o600)
}

// GetPathForSource returns the configured path for a source
func (rs *RuleStore) GetPathForSource(source RuleSource) string {
	rs.mu.RLock()
	defer rs.mu.RUnlock()
	return rs.paths[source]
}

// NewRuleStoreFromPaths builds a RuleStore whose persistence paths follow the
// same settings.json layout as the config package (config.go discoverSources):
//
//	SourceUserSettings    -> <homeDir>/.whale/settings.json
//	SourceProjectSettings -> <workDir>/.whale/settings.json
//	SourceLocalSettings   -> <workDir>/.whale/settings.local.json
//
// SourceSession and SourceCLIArg are runtime-only sources and intentionally have
// no persistence path (Save / AddRuleToSettings already treat them as no-ops).
//
// This is the factory production wiring must use; NewRuleStore() alone leaves
// every source with an empty path, so Load/LoadAll can never load anything and
// AddRule silently skips persistence (the P0 "RuleStore not wired" gap).
func NewRuleStoreFromPaths(homeDir, workDir string) *RuleStore {
	store := NewRuleStore()
	if homeDir != "" {
		store.SetPathForSource(SourceUserSettings, filepath.Join(whalehome.Dir(), "settings.json"))
	}
	if workDir != "" {
		store.SetPathForSource(SourceProjectSettings, filepath.Join(workDir, ".whale", "settings.json"))
		store.SetPathForSource(SourceLocalSettings, filepath.Join(workDir, ".whale", "settings.local.json"))
	}
	return store
}
