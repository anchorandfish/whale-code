package hook

// Hooks 信任门（PR-A）。
//
// 拍板依据（AUDIT_ROADMAP S7 §六 Q1）：对齐业内既有实现 `tui/src/startup_hooks_review.rs` ——
// hook 必须先 review 再 trust；**未信任的 hook 静默跳过、根本不运行**；内容变更后需重新信任。
// Q4（改写输入/输出的能力）随此门一并授予：未信任 → 不运行 → 自然改不了单子。
//
// 设计要点（production-path-tdd）：
// - R3⑤ 单一兜底点：门装在**执行最内层** ExecuteHookWithPayload（validator.go）。
// 往 manager 塞 hook 的入口有 4 个（LoadConfig / LoadConfigFromBytes / SetConfig / AddHook），
// 8 个 Run*Hooks 又各拷贝一份列表 —— 在入口或各 Run 处装门都会产生影子实现。
// 所有执行路径最终都汇聚到 ExecuteHookWithPayload，装一处即全覆盖。
// - R3③ 无更早短路点：该函数是执行前最后一道，到达它即意味着要执行。
// - 信任判定**惰性计算**（对比 fingerprint 与信任库），因此 hook 内容一改自动失效，
// 不需要在 4 个配置入口同步维护状态，杜绝状态漂移。
//
// 生产接线（R1）：Executor.InitPipelineWithHooks（executor.go:569 创建 Manager 后）调用 EnableTrustGate()。
// 库默认关闭（向后兼容）；生产必须显式开启，由 TestProduction_TrustGateEnabledAfterInit 断言。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/anchorandfish/whale-code/internal/whalehome"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// HookTrustStatus hook 的信任状态（对齐业内既有实现 HookTrustStatus）。
type HookTrustStatus string

const (
	// TrustUntrusted 从未被信任过。
	TrustUntrusted HookTrustStatus = "untrusted"
	// TrustTrusted 已信任且内容自信任后未变。
	TrustTrusted HookTrustStatus = "trusted"
	// TrustModified 曾信任过，但内容已变更（需重新 review）。
	TrustModified HookTrustStatus = "modified"
)

// trustRecord 一条信任记录。以 fingerprint 为键（内容寻址），
// 同时记住 ID 以便区分「从未信任」与「信任后又被改」。
//
// PR-A2（② 拍板）：信任分两级 —— 记录存在 = 允许**运行**；
// AllowRewrite = 额外授权改写工具输入/输出（Q4）。两级都是内容寻址的，
// hook 内容一改两项同时失效。
type trustRecord struct {
	ID           string    `json:"id"`
	Fingerprint  string    `json:"fingerprint"`
	Command      string    `json:"command,omitempty"`
	TrustedAt    time.Time `json:"trusted_at"`
	AllowRewrite bool      `json:"allow_rewrite,omitempty"`
}

// HookTrustItem 待评审项，供 CLI/TUI 展示 review 列表。
type HookTrustItem struct {
	Event   string          `json:"event"`
	HookID  string          `json:"hook_id"`
	Type    HookType        `json:"type"`
	Command string          `json:"command,omitempty"`
	Status  HookTrustStatus `json:"status"`
	Summary string          `json:"summary"`
}

// defaultTrustStoreName 信任库默认文件名（落在 ~/.whale/ 下）。
const defaultTrustStoreName = "hooks.trust.json"

// SetTrustStorePath 覆盖信任库落盘位置（默认 ~/.whale/hooks.trust.json）。
// 主要给测试用，避免污染真实用户目录；生产不调用。
func (m *Manager) SetTrustStorePath(p string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustPath = p
}

// EnableTrustGate 启用信任门。
// R5：生产路径上第一次被执行在 Executor.InitPipelineWithHooks（executor.go 创建 Manager 之后）。
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

// hookFingerprint 计算 hook 定义的内容指纹。
// 覆盖「会被执行的一切」：类型/匹配器/pattern/动作 + 各类型自己的执行体（命令、prompt、URL…）。
//
// B5（S19-H-E · E3）：指纹**并入来源身份**（`id` + `source`）。
// 修复前指纹只有内容，不含 ID/来源 ⇒ 同内容的两个 hook 跨来源共享同一条信任记录
// （表征 TestCharS19_HookTrust_NotScopedBySource_CrossSourceTransfer：信任 cfg-1 后 cfg-2 自动 Trusted）。
// 这与同类 CLI 的 `PluginHookSource{plugin_id, ...}` 溯源姿态相反：既然"信任"是对**某个来源的某段内容**的授权，
// 键里就必须有来源，否则 A 插件被信任的 hook 内容可以被 B 插件（或工作区配置）原样复用而不触发 review。
//
// ⚠️ 副作用（已知、刻意）：指纹算法变更 ⇒ 既有 ~/.whale/hooks.trust.json 记录全部失配，
// 用户需重新 `/hooks trust all`。这是"信任键必须是内容+来源"的必然代价，已在 G-E_STATUS.md 登记。
//
// 注意：HookCallback 是**进程内编译期代码**（Callback 字段 json:"-"，无法由配置文件注入），
// 不属于「外部代码信任域」，因此 exempt——否则指纹无法计算（func 不可哈希）会导致
// 插件回调类 hook 全部失效。这是有意为之，不是遗漏。
func hookFingerprint(h Hook) string {
	b := h.Base()
	parts := []string{
		"source=" + string(b.Source),
		"id=" + b.ID,
		"type=" + string(b.Type),
		"matcher=" + b.Matcher,
		"pattern=" + b.Pattern,
		"action=" + string(b.Action),
		"reason=" + b.Reason,
	}
	switch v := h.(type) {
	case *HookCommand:
		parts = append(parts, "command="+v.Command)
	case *HookPrompt:
		parts = append(parts, "prompt="+v.Prompt, "model="+v.Model)
	case *HookAgent:
		parts = append(parts, "prompt="+v.Prompt, "model="+v.Model)
	case *HookHTTP:
		parts = append(parts, "url="+v.URL)
		for _, k := range sortedKeys(v.Headers) {
			parts = append(parts, "hdr:"+k+"="+v.Headers[k])
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	return hex.EncodeToString(sum[:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// isCallbackHook 判断是否为进程内回调型 hook（免于信任门，理由见 hookFingerprint 注释）。
func isCallbackHook(h Hook) bool {
	_, ok := h.(*HookCallback)
	return ok
}

// trustStorePath 返回信任库路径（m.trustPath 优先，否则 ~/.whale/hooks.trust.json）。
func (m *Manager) trustStorePath() string {
	if m.trustPath != "" {
		return m.trustPath
	}
	// 收口：Dir() 返回空（home 不可得）⇒ 退回相对文件名，行为同前。
	if dir := whalehome.Dir(); dir != "" {
		return filepath.Join(dir, defaultTrustStoreName)
	}
	return defaultTrustStoreName
}

// loadTrustRecords 惰性加载信任库（进程内只加载一次，之后走缓存）。
// 权限收紧：文件以 0600 读写，防止其他用户篡改信任决策。
func (m *Manager) loadTrustRecords() map[string]trustRecord {
	if m.trustRecords != nil {
		return m.trustRecords
	}
	recs := map[string]trustRecord{}
	path := m.trustStorePath()
	if data, err := os.ReadFile(path); err == nil {
		var list []trustRecord
		if err := json.Unmarshal(data, &list); err == nil {
			for _, r := range list {
				recs[r.Fingerprint] = r
			}
		}
	}
	m.trustRecords = recs
	return recs
}

func (m *Manager) saveTrustRecordsLocked() error {
	recs := m.trustRecords
	if recs == nil {
		return nil
	}
	list := make([]trustRecord, 0, len(recs))
	for _, r := range recs {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Fingerprint < list[j].Fingerprint })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal hook trust store: %w", err)
	}
	path := m.trustStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir hook trust store dir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write hook trust store %s: %w", path, err)
	}
	return nil
}

// TrustStatus 计算单个 hook 的信任状态（对照信任库）。
func (m *Manager) TrustStatus(h Hook) HookTrustStatus {
	if isCallbackHook(h) {
		return TrustTrusted // 进程内编译期代码，不属外部信任域
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.trustStatusLocked(h)
}

func (m *Manager) trustStatusLocked(h Hook) HookTrustStatus {
	// 进程内编译期代码（Callback 字段 json:"-"，配置注入不了）不属外部信任域。
	if isCallbackHook(h) {
		return TrustTrusted
	}
	// PR-A2（② 拍板）：插件 hook **安装即信任"运行"**。
	// 依据：插件由用户显式安装（安装 = 授权运行），且生产注入形态**没有 ID**
	//（tools/executor.go:1031-1039），无法走 review/trust 流程 —— 若按未信任处理，
	// 插件 hook 会永远静默失效。注意这只授予"运行"；**改写特权（Q4）仍默认关**，
	// 由 RewriteGranted 单独判定（见 trustRewrites）。
	if h.Base().Source == SourcePlugin {
		return TrustTrusted
	}
	fp := hookFingerprint(h)
	recs := m.loadTrustRecords()
	if _, ok := recs[fp]; ok {
		return TrustTrusted
	}
	// 内容变了：若信任库中存在同 ID 但不同指纹的记录，说明是「信任后被改」。
	if id := h.Base().ID; id != "" {
		for _, r := range recs {
			if r.ID == id {
				return TrustModified
			}
		}
	}
	return TrustUntrusted
}

// allHooksByEvent 枚举当前配置中所有事件及其 hook 列表。
func (m *Manager) allHooksByEvent() map[string][]Hook {
	events := []string{
		"PreToolUse", "PostToolUse", "PreRead", "PostRead",
		"PreWrite", "PostWrite", "PreEdit", "PostEdit",
		"PreBash", "PostBash", "PreSearch", "PostSearch",
		"PreGit", "PostGit", "PreWeb", "PostWeb",
		"PreAgent", "PostAgent",
		"SessionStart", "SessionStop", "Stop", "PostCompact", "Notification",
	}
	out := make(map[string][]Hook, len(events))
	for _, e := range events {
		out[e] = *m.hooksByEventName(e)
	}
	return out
}

// PendingReview 返回所有需要 review 的 hook（未信任或信任后被改）。
// 对应同类 CLI 的 hook_needs_review（Untrusted | Modified）。
func (m *Manager) PendingReview() []HookTrustItem {
	// 注意用写锁：trustStatusLocked → loadTrustRecords 会惰性写 m.trustRecords 缓存。
	m.mu.Lock()
	defer m.mu.Unlock()
	var items []HookTrustItem
	for event, hooks := range m.allHooksByEvent() {
		for _, h := range hooks {
			st := m.trustStatusLocked(h)
			if st == TrustTrusted {
				continue
			}
			b := h.Base()
			item := HookTrustItem{
				Event:  event,
				HookID: b.ID,
				Type:   b.Type,
				Status: st,
			}
			if c, ok := h.(*HookCommand); ok {
				item.Command = c.Command
			}
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Event != items[j].Event {
			return items[i].Event < items[j].Event
		}
		return items[i].HookID < items[j].HookID
	})
	return items
}

// TrustAllPending 信任当前所有待评审的 hook（对应同类 CLI "Trust all and continue"）。
// 只记录指纹；hook 内容一旦再变，指纹不再命中，自动退回需重新信任。
func (m *Manager) TrustAllPending() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	for _, hooks := range m.allHooksByEvent() {
		for _, h := range hooks {
			if m.trustStatusLocked(h) == TrustTrusted {
				continue
			}
			m.addTrustLocked(recs, h)
		}
	}
	return m.saveTrustRecordsLocked()
}

// TrustHookByID 仅信任指定 ID 的 hook（对应同类 CLI 单条 trust）。
func (m *Manager) TrustHookByID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	found := false
	for _, hooks := range m.allHooksByEvent() {
		for _, h := range hooks {
			if h.Base().ID != id {
				continue
			}
			found = true
			m.addTrustLocked(recs, h)
		}
	}
	if !found {
		return fmt.Errorf("hook with ID %s not found", id)
	}
	return m.saveTrustRecordsLocked()
}

func (m *Manager) addTrustLocked(recs map[string]trustRecord, h Hook) {
	fp := hookFingerprint(h)
	rec := trustRecord{ID: h.Base().ID, Fingerprint: fp, TrustedAt: time.Now()}
	if c, ok := h.(*HookCommand); ok {
		rec.Command = c.Command
	}
	recs[fp] = rec
}

// RunTrustSummary 返回人类可读的待评审摘要，供启动提示使用
// （对应同类 CLI 的 "Hooks need review" 提示）。
//
// PR-A2（① / ③ 拍板）：**按需提示** —— 无待审事项时返回空串，调用方据此不打扰用户；
// 有待审事项时给出数量 + 一键信任命令（命令只提示，**不自动执行**）。
func (m *Manager) RunTrustSummary() string {
	items := m.PendingReview()
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("[hooks] %d hook(s) need review and will NOT run until trusted — run `/hooks trust all` (or `/hooks review` to inspect first)", len(items)))
	for _, it := range items {
		cmd := it.Command
		if cmd == "" {
			cmd = "(no command)"
		}
		b.WriteString(fmt.Sprintf("\n  - [%s] %s (%s, %s) %s", it.Event, it.HookID, it.Type, it.Status, cmd))
	}
	return b.String()
}

// ---------- Q4 改写特权（PR-A2 · ② 拍板：默认关，需单独授权） ----------

// RewriteGranted 报告某 hook 是否获得「改写工具输入/输出」的授权。
//
// 语义（② 拍板）：
// - 运行信任 ≠ 改写授权。hook 可运行只说明它有权参与决策（allow/block/ask/warn）。
// - 改写 `updatedInput` / `Modified` 会改变 agent 的真实动作与模型上下文，
// 属 Q4 强能力，**默认关闭**，需显式 `/hooks allow-rewrite` 授权。
// - 授权与信任同源：按内容指纹记录 → hook 一改即失效。
// - 进程内回调 hook（HookCallback）豁免：它不是外部代码，本来就在 agent 进程里。
func (m *Manager) RewriteGranted(h Hook) bool {
	if isCallbackHook(h) {
		return true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.trustEnabled {
		// 门未启用（测试/兼容路径）→ 保持 PR-A2 之前的行为，不额外拦截。
		return true
	}
	return m.rewriteGrantedLocked(h)
}

func (m *Manager) rewriteGrantedLocked(h Hook) bool {
	rec, ok := m.loadTrustRecords()[hookFingerprint(h)]
	return ok && rec.AllowRewrite
}

// GrantRewriteByID 授予指定 ID 的 hook 改写特权。
//
// ⚠️ 安全约束（T14 钉死）：改写授权**只对已经允许运行的 hook 生效**。
// 本函数**不会**创建"可运行"的信任记录 —— 否则 `allow-rewrite` 就成了
// 绕过 review 门（① 拍板：未信任不运行）的后门。未信任的 hook 需先 `/hooks trust <id>`。
func (m *Manager) GrantRewriteByID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	found, granted, blocked := false, false, false
	for _, hooks := range m.allHooksByEvent() {
		for _, h := range hooks {
			if h.Base().ID != id {
				continue
			}
			found = true
			if m.trustStatusLocked(h) != TrustTrusted {
				blocked = true
				continue
			}
			m.setRewriteLocked(recs, h, true)
			granted = true
		}
	}
	if !found {
		return fmt.Errorf("hook with ID %s not found", id)
	}
	if !granted && blocked {
		return fmt.Errorf("hook %s is not allowed to run yet — run `/hooks trust %s` first (rewrite can only be granted to hooks that may run)", id, id)
	}
	return m.saveTrustRecordsLocked()
}

// GrantRewriteAll 授予当前**允许运行**的 hook 改写特权。
//
// 这是无 ID hook（生产里的插件 hook 就没有 ID）唯一的授权途径。
// 同上：只覆盖已可运行的 hook；未信任的 config hook 必须先 trust，本函数不会替它开门。
func (m *Manager) GrantRewriteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	n := 0
	for _, hooks := range m.allHooksByEvent() {
		for _, h := range hooks {
			if m.trustStatusLocked(h) != TrustTrusted {
				continue
			}
			m.setRewriteLocked(recs, h, true)
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("no run-trusted hook found — run `/hooks trust all` first")
	}
	return m.saveTrustRecordsLocked()
}

// RevokeRewriteByID 撤销改写特权（保留运行信任）。
func (m *Manager) RevokeRewriteByID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	n := 0
	for fp, r := range recs {
		if r.ID != id || !r.AllowRewrite {
			continue
		}
		r.AllowRewrite = false
		recs[fp] = r
		n++
	}
	if n == 0 {
		return fmt.Errorf("no rewrite grant found for hook ID %s", id)
	}
	return m.saveTrustRecordsLocked()
}

// RevokeRewriteAll 撤销所有改写特权（运行信任不变）。
func (m *Manager) RevokeRewriteAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	for fp, r := range recs {
		if r.AllowRewrite {
			r.AllowRewrite = false
			recs[fp] = r
		}
	}
	return m.saveTrustRecordsLocked()
}

// setRewriteLocked 写入/更新一条允许运行的记录，并设定改写位。
func (m *Manager) setRewriteLocked(recs map[string]trustRecord, h Hook, allow bool) {
	fp := hookFingerprint(h)
	rec := recs[fp]
	if rec.Fingerprint == "" {
		rec = trustRecord{ID: h.Base().ID, Fingerprint: fp, TrustedAt: time.Now()}
		if c, ok := h.(*HookCommand); ok {
			rec.Command = c.Command
		}
	}
	rec.AllowRewrite = allow
	recs[fp] = rec
}

// RevokeTrustByID 撤销指定 ID hook 的信任记录，hook 立即停止运行。
func (m *Manager) RevokeTrustByID(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	recs := m.loadTrustRecords()
	n := 0
	for fp, r := range recs {
		if r.ID == id {
			delete(recs, fp)
			n++
		}
	}
	if n == 0 {
		return fmt.Errorf("no trust record found for hook ID %s", id)
	}
	return m.saveTrustRecordsLocked()
}

// RevokeAllTrust 清空信任库，所有 hook 停止运行（对应同类 CLI "Continue without trusting"）。
func (m *Manager) RevokeAllTrust() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trustRecords = map[string]trustRecord{}
	return m.saveTrustRecordsLocked()
}
