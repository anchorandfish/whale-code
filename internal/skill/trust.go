package skill

// trust.go — skill 来源信任门（B1 = S19-H-A · A2「按来源分流 + 首次使用确认一次」）。
//
// 缺陷（修复前）：`<ws>/.whale/skills/*.md` 与 `~/.whale/skills/*.md` 一律**启动即加载**，
// 正文经 `Skill` 工具原样进入模型上下文 ⇒ `git clone` 一个仓库并在该目录启动 whale，
// 仓库就能向模型灌注任意指令（间接注入面，I8/I10）。
//
// 拍板（§2.2 B1）：
// - `user`（及进程内 `builtin`）来源 **免确认** —— 用户自己的目录；
// - `project` / `plugin` 来源 **首次使用确认一次**，记入信任库（内容 + 来源 + 名字寻址）；
// - **复用 S7 的范式**（B-3）：内容指纹 + 显式 trust + 变更即失效（`~/.whale/hooks.trust.json` 同构，
// 落 `~/.whale/skills.trust.json`；同一范式，不另造第二套信任机制）。
//
// 门的位置（单一兜底点）：`IsInjectionAllowed` 是**唯一判据**；生产接线点在
// `internal/tools` 的 `Skill` 工具处理器（正文进上下文前）。⚠️ 该接线属 G-T 包边界 ⇒ 见移交单
// `内部工作回执`；本文件只负责机制与判定。
//
// 库默认**关闭**（与 S7 信任门同姿态，向后兼容、不打断既有调用方与测试）；
// 生产在装配处显式 `EnableTrustGate()`（该调用点同样在移交单里）。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SkillTrustStatus skill 的信任状态（对齐 hook 域的三态命名）。
type SkillTrustStatus string

const (
	// SkillTrustUntrusted 从未被确认过（project/plugin 来源的默认态）。
	SkillTrustUntrusted SkillTrustStatus = "untrusted"
	// SkillTrustTrusted 已确认且内容自确认后未变（或来源本身免确认：user/builtin）。
	SkillTrustTrusted SkillTrustStatus = "trusted"
	// SkillTrustModified 曾确认过，但内容已变更（需重新确认）。
	SkillTrustModified SkillTrustStatus = "modified"
)

// defaultSkillTrustStoreName 信任库默认文件名（落在 ~/.whale/ 下，与 hooks.trust.json 同目录同范式）。
const defaultSkillTrustStoreName = "skills.trust.json"

// skillTrustRecord 一条信任记录：按「名字 + 来源 + 内容指纹」寻址。
//
// 为什么键里要有**来源**（与 hook 域的 B5-E3 同一理由）：同名 skill 在 project 与 user 目录里
// 可以内容相同但来历完全不同 —— 确认过"用户自己的那份"不等于确认过"仓库里那份"。
type skillTrustRecord struct {
	Name        string    `json:"name"`
	Source      string    `json:"source"`
	Fingerprint string    `json:"fingerprint"`
	TrustedAt   time.Time `json:"trusted_at"`
}

// skillTrustFile 信任库落盘结构。
type skillTrustFile struct {
	Version int                `json:"version"`
	Records []skillTrustRecord `json:"records"`
}

// SkillTrustItem 待确认项（供 CLI/TUI 展示 review 列表）。
type SkillTrustItem struct {
	Name    string           `json:"name"`
	Source  string           `json:"source"`
	Status  SkillTrustStatus `json:"status"`
	Summary string           `json:"summary"`
}

// SetTrustStorePath 覆盖信任库落盘位置（默认 ~/.whale/skills.trust.json）。
// 主要给测试用，避免污染真实用户目录；生产不调用。
func (m *Manager) SetTrustStorePath(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustPath = p
}

// EnableTrustGate 启用 skill 信任门。
//
// 库默认关闭（向后兼容）；生产装配处调用（见移交单）。与 hook 域同一取舍：
// 门关闭时 `IsInjectionAllowed` 恒 true，既有调用方行为不变。
func (m *Manager) EnableTrustGate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustEnabled = true
}

// TrustGateEnabled 报告信任门是否启用（生产接线断言用）。
func (m *Manager) TrustGateEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.trustEnabled
}

// skillSourceNeedsConfirmation 报告某来源是否需要"首次使用确认"。
//
// B1 的分流依据是 `Skill.Source`（user / project / plugin / builtin）：
// - user / builtin / 空（历史调用方未标注）⇒ 免确认；
// - project / plugin ⇒ 需确认（外部制品随仓库/远程包进入本机）。
func skillSourceNeedsConfirmation(source string) bool {
	switch strings.ToLower(source) {
	case "project", "plugin":
		return true
	default:
		return false
	}
}

// skillFingerprint 计算 skill 的**内容指纹**（覆盖"会进入模型上下文的一切"）。
//
// 含 source 与 name：同名不同来源、或同名同来源但内容变了，都必须是不同的键。
func skillFingerprint(s Skill) string {
	parts := []string{
		"source=" + strings.ToLower(s.Source),
		"name=" + s.Name,
		"description=" + s.Description,
		"prompt=" + s.Prompt,
		"model=" + s.Model,
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// trustStorePath 返回信任库路径（m.trustPath 优先，否则 ~/.whale/skills.trust.json）。
func (m *Manager) trustStorePath() string {
	if m.trustPath != "" {
		return m.trustPath
	}
	// 收口：状态目录唯一走 whalehome.Dir()；home 不可得时 Dir() 返回空，
	// 保留原有"退回相对文件名"的降级路径。
	if dir := whalehome.Dir(); dir != "" {
		return filepath.Join(dir, defaultSkillTrustStoreName)
	}
	return defaultSkillTrustStoreName
}

// loadTrustRecords 惰性加载信任库（进程内只加载一次，之后走缓存）。
// 文件 0600 读写，防止其他用户篡改信任决策。
func (m *Manager) loadTrustRecords() map[string]skillTrustRecord {
	if m.trustRecords != nil {
		return m.trustRecords
	}
	recs := map[string]skillTrustRecord{}
	if data, err := os.ReadFile(m.trustStorePath()); err == nil {
		var f skillTrustFile
		if err := json.Unmarshal(data, &f); err == nil {
			for _, r := range f.Records {
				recs[r.Fingerprint] = r
			}
		}
	}
	m.trustRecords = recs
	return recs
}

// saveTrustRecordsLocked 落盘信任库（调用方须持锁）。
func (m *Manager) saveTrustRecordsLocked() error {
	if m.trustRecords == nil {
		return nil
	}
	list := make([]skillTrustRecord, 0, len(m.trustRecords))
	for _, r := range m.trustRecords {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Fingerprint < list[j].Fingerprint })
	data, err := json.MarshalIndent(skillTrustFile{Version: 1, Records: list}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal skill trust store: %w", err)
	}
	path := m.trustStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir skill trust store dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write skill trust store %s: %w", path, err)
	}
	return nil
}

// TrustStatus 计算单个 skill 的信任状态。
func (m *Manager) TrustStatus(s Skill) SkillTrustStatus {
	if !skillSourceNeedsConfirmation(s.Source) {
		return SkillTrustTrusted
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.trustStatusLocked(s)
}

func (m *Manager) trustStatusLocked(s Skill) SkillTrustStatus {
	if !skillSourceNeedsConfirmation(s.Source) {
		return SkillTrustTrusted
	}
	fp := skillFingerprint(s)
	if _, ok := m.loadTrustRecords()[fp]; ok {
		return SkillTrustTrusted
	}
	// 内容变了：存在同名同来源但不同指纹的记录 ⇒ "确认后被改"，需重新确认。
	for _, r := range m.loadTrustRecords() {
		if r.Name == s.Name && strings.EqualFold(r.Source, s.Source) {
			return SkillTrustModified
		}
	}
	return SkillTrustUntrusted
}

// IsInjectionAllowed 是**唯一判据**：该 skill 的正文是否可以进入模型上下文。
//
// 返回：
// - allowed: 是否放行；
// - status: 信任状态（结构化，供 UI 展示"为什么被拦"）；
// - reason: 人类可读原因（**只用于展示**，判定不依赖它）。
//
// 语义：门未启用 ⇒ 恒放行（库默认/兼容路径）；门启用 ⇒ 免确认来源放行，
// project/plugin 必须有匹配的信任记录。
func (m *Manager) IsInjectionAllowed(name string) (bool, SkillTrustStatus, string) {
	m.mu.Lock()
	enabled := m.trustEnabled
	s, ok := m.skills[name]
	m.mu.Unlock()

	if !ok {
		return false, SkillTrustUntrusted, fmt.Sprintf("skill %q not loaded", name)
	}
	if !enabled {
		return true, SkillTrustTrusted, "skill trust gate disabled (legacy path)"
	}
	st := m.TrustStatus(s)
	if st == SkillTrustTrusted {
		return true, st, ""
	}
	return false, st, fmt.Sprintf(
		"skill %q from source %q is %s — its body will NOT be injected into the model context; run `/skill trust %s` to confirm once",
		s.Name, s.Source, st, s.Name)
}

// ErrSkillUntrusted 表示 skill 正文被信任门拦下（未确认或确认后被改）。
//
// 调用方（tools/tui）可用 errors.Is 判别这是"信任门拒绝"而非"skill 不存在"：
// 两者的处理完全不同 —— 前者应提示用户去确认，后者应提示名字错了。
var ErrSkillUntrusted = errors.New("skill content blocked by trust gate")

// ensureInjectionAllowed 是 Execute / ExecuteWithMeta 共用的**唯一兜底判定**。
//
// 装点理由（对齐 hook 域 R3⑤ 单一兜底点）：skill 正文进入模型上下文有两条路径 ——
// `Execute`（返回 string，tui 的 OnSkillExecute 回调用）与 `ExecuteWithMeta`（返回 SkillResult，
// `tools/web.go:837` 的 Skill 工具与 tui 的 OnSkillExecuteWithMeta 回调用）。
// 两条路径都在这两个函数出门，因此门装在这里即全覆盖；往调用方散装（tools 一处、tui 三处）
// 会产生影子实现且必然漏一个。
//
// 门未启用（库默认）⇒ 恒 nil，既有调用方与测试零感知。
func (m *Manager) ensureInjectionAllowed(name string) error {
	allowed, _, reason := m.IsInjectionAllowed(name)
	if allowed {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrSkillUntrusted, reason)
}

// TrustSkill 确认单个 skill（记入信任库；内容一改自动失效）。
//
// ⚠️ 只对**需要确认**的来源有意义：user/builtin 本就免确认（调用本函数是 no-op，返回 error
// 以免用户误以为"确认"改变了什么）。
func (m *Manager) TrustSkill(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.skills[name]
	if !ok {
		return fmt.Errorf("skill %q not found", name)
	}
	if !skillSourceNeedsConfirmation(s.Source) {
		return fmt.Errorf("skill %q (source %q) does not require confirmation", name, s.Source)
	}
	recs := m.loadTrustRecords()
	recs[skillFingerprint(s)] = skillTrustRecord{
		Name:        s.Name,
		Source:      s.Source,
		Fingerprint: skillFingerprint(s),
		TrustedAt:   time.Now(),
	}
	return m.saveTrustRecordsLocked()
}

// TrustAllPending 确认当前所有待确认的 skill（对应 S7 的 `/hooks trust all`）。
func (m *Manager) TrustAllPending() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	n := 0
	for _, s := range m.skills {
		if !skillSourceNeedsConfirmation(s.Source) {
			continue
		}
		if m.trustStatusLocked(s) == SkillTrustTrusted {
			continue
		}
		fp := skillFingerprint(s)
		recs[fp] = skillTrustRecord{Name: s.Name, Source: s.Source, Fingerprint: fp, TrustedAt: time.Now()}
		n++
	}
	if n == 0 {
		return nil
	}
	return m.saveTrustRecordsLocked()
}

// RevokeTrust 撤销某 skill 的信任记录（其正文立即不再注入）。
func (m *Manager) RevokeTrust(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	n := 0
	for fp, r := range recs {
		if r.Name == name {
			delete(recs, fp)
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("no trust record found for skill %q", name)
	}
	return m.saveTrustRecordsLocked()
}

// PendingReview 返回所有需要确认的 skill（未确认或确认后被改）。
func (m *Manager) PendingReview() []SkillTrustItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []SkillTrustItem
	for _, s := range m.skills {
		if !skillSourceNeedsConfirmation(s.Source) {
			continue
		}
		st := m.trustStatusLocked(s)
		if st == SkillTrustTrusted {
			continue
		}
		items = append(items, SkillTrustItem{
			Name:    s.Name,
			Source:  s.Source,
			Status:  st,
			Summary: fmt.Sprintf("%s (%s) — first use needs confirmation", s.Name, s.Source),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Source != items[j].Source {
			return items[i].Source < items[j].Source
		}
		return items[i].Name < items[j].Name
	})
	return items
}

// RunTrustSummary 返回人类可读的待确认摘要（按需提示：无待确认时返回空串，调用方据此不打扰用户）。
func (m *Manager) RunTrustSummary() string {
	items := m.PendingReview()
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[skills] %d skill(s) from project/plugin sources need one-time confirmation before their content is injected — run `/skill trust all` (or `/skill trust <name>`)",
		len(items)))
	for _, it := range items {
		b.WriteString(fmt.Sprintf("\n  - %s [%s] %s", it.Name, it.Source, it.Status))
	}
	return b.String()
}

// HandleTrustCommand 处理 `/skill` 信任管理子命令（O-2 收口，2026-10-11）。
//
// 实现与文案的唯一声明（SSOT）——注册侧（command.go "skill" 命令）
// 与各 UI 接线只做薄转发到 OnSkillManage 回调，禁止各自再写一份解析/文案。
// 子命令："" 列待确认 · trust <name> · trust all · untrust <name>；未知子命令回 usage。
// 契约：单返回值，错误一律进文案（用户在 TUI 里看到的是完整提示，不是 err）。
//
// R5：生产第一次执行点 = 用户敲 `/skill trust ...`（注册表正门，/help 与补全可见）；
// 写侧经 TrustSkill/TrustAllPending/RevokeTrust 落盘信任库（trustStorePath）。
func (m *Manager) HandleTrustCommand(args string) string {
	if m == nil {
		return "skill trust manager not initialized"
	}
	f := strings.Fields(args)
	if len(f) == 0 || (len(f) == 1 && f[0] == "trust") {
		// 空参数 / `/skill trust`（不带名字）= 列待确认清单（帮助用户发现该确认什么；
		// 拦截文案与 usage 均承诺 `/skill trust` 可列清单）。
		items := m.PendingReview()
		if len(items) == 0 {
			return "No skills pending confirmation."
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[skills] %d skill(s) pending one-time confirmation — run `/skill trust all` (or `/skill trust <name>`):", len(items))
		for _, it := range items {
			fmt.Fprintf(&b, "\n  - %s [%s] %s", it.Name, it.Source, it.Status)
		}
		return b.String()
	}
	switch f[0] {
	case "trust":
		if f[1] == "all" {
			pending := m.PendingReview()
			if len(pending) == 0 {
				return "No skills pending confirmation."
			}
			if err := m.TrustAllPending(); err != nil {
				return fmt.Sprintf("trust all failed: %v", err)
			}
			return fmt.Sprintf("trusted %d skill(s).", len(pending))
		}
		name := f[1]
		if err := m.TrustSkill(name); err != nil {
			return err.Error()
		}
		return fmt.Sprintf("skill %q trusted — its content will now be injected into the model context.", name)
	case "untrust":
		if len(f) == 1 {
			return "usage: /skill untrust <name>"
		}
		name := f[1]
		if err := m.RevokeTrust(name); err != nil {
			return err.Error()
		}
		return fmt.Sprintf("skill %q untrusted — its content will require confirmation again before injection.", name)
	default:
		return "usage: /skill trust <name> | /skill trust all | /skill untrust <name>  (or run `/skill trust` to list pending)"
	}
}
