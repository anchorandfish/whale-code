// source_test.go — P2 读路径接线判别器。
//
// 判别目标：读路径的数据源从「config 单目录 store」换成「memory 双 scope 合并 + 有效性过滤」。
// 用同一份磁盘 fixture 跑两个实现，证明它们在三个行为点上确实不同：
//
//	行为点 接线前(config store) 接线后(bridge)
//	能看到 _user/ 偏好 否 是
//	注入 contradicted 记忆 是（缺陷） 否
//	注入已过期记忆 是（缺陷） 否
//
// Before 组断言「现状就是坏的」，After 组断言「期望」。两组不能同时通过同一实现
// —— 这正是判别力的证明（若某个实现让两组都过，说明断言写错或被测对象被换掉）。
package memorybridge_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/config"
	"github.com/anchorandfish/whale-code/internal/memory"
	"github.com/anchorandfish/whale-code/internal/memorybridge"
)

// p2Fixture 在临时目录里搭出与生产一致的记忆布局：
//
//	{home}/.whale/memory/_user/ ← user scope（跨项目偏好）
//	{home}/.whale/memory/{workDirHash}/ ← project scope（项目事实）
type p2Fixture struct {
	home    string
	baseDir string
	workDir string
	userDir string
	projDir string
}

func newP2Fixture(t *testing.T) *p2Fixture {
	t.Helper()
	home := t.TempDir()
	baseDir := filepath.Join(home, ".whale")
	// NewSource 从 whalehome.Dir()（$WHALE_HOME）解析 baseDir，fixture 经环境变量注入
	t.Setenv("WHALE_HOME", baseDir)
	workDir := "/p2/integration/workdir"

	userStore, err := memory.NewMemoryStoreForScope(baseDir, "user", false)
	if err != nil {
		t.Fatalf("user scope store: %v", err)
	}
	projStore, err := memory.NewMemoryStoreForProject(baseDir, workDir)
	if err != nil {
		t.Fatalf("project scope store: %v", err)
	}

	f := &p2Fixture{
		home:    home,
		baseDir: baseDir,
		workDir: workDir,
		userDir: userStore.Dir(),
		projDir: projStore.Dir(),
	}
	return f
}

// writeEntry 直写 frontmatter 文件（不走 store.Write，以便精确控制 updated_at / valid_until）。
func (f *p2Fixture) writeEntry(t *testing.T, dir, name, content string, updatedAt time.Time, verification string, validUntil *time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", name)
	fmt.Fprintf(&sb, "description: p2 fixture %s\n", name)
	sb.WriteString("type: user\n")
	fmt.Fprintf(&sb, "created_at: %s\n", updatedAt.Format(time.RFC3339))
	fmt.Fprintf(&sb, "updated_at: %s\n", updatedAt.Format(time.RFC3339))
	if verification != "" {
		fmt.Fprintf(&sb, "verification: %s\n", verification)
	}
	if validUntil != nil {
		fmt.Fprintf(&sb, "valid_until: %s\n", validUntil.Format(time.RFC3339))
	}
	sb.WriteString("---\n")
	sb.WriteString(content)
	sb.WriteString("\n")

	path := filepath.Join(dir, name+".md")
	if err := os.WriteFile(path, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seed 铺 6 条：user 偏好 1 条 + project 事实 1 条 + 作废 1 条 + 过期 1 条 + 同名覆盖 1 对。
func (f *p2Fixture) seed(t *testing.T) {
	t.Helper()
	now := time.Now()
	expiredAt := now.Add(-30 * time.Minute)

	// _user/ scope
	f.writeEntry(t, f.userDir, "user-pref-a", "USER prefers approach A", now.Add(-1*time.Hour), memory.VerificationVerified, nil)
	f.writeEntry(t, f.userDir, "dup-e", "USER version of dup E", now.Add(-5*time.Hour), memory.VerificationVerified, nil)

	// project scope
	f.writeEntry(t, f.projDir, "proj-fact-b", "PROJECT fact B", now.Add(-2*time.Hour), memory.VerificationVerified, nil)
	f.writeEntry(t, f.projDir, "dead-c", "DEAD criticised memory C", now.Add(-3*time.Hour), memory.VerificationContradicted, nil)
	f.writeEntry(t, f.projDir, "expired-d", "EXPIRED memory D", now.Add(-4*time.Hour), memory.VerificationUnverified, &expiredAt)
	f.writeEntry(t, f.projDir, "dup-e", "PROJECT version of dup E", now.Add(-6*time.Hour), memory.VerificationVerified, nil)
}

func entryNames(list []config.MemoryEntryV2) []string {
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name)
	}
	return names
}

func contains(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func count(names []string, want string) int {
	n := 0
	for _, x := range names {
		if x == want {
			n++
		}
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Before 组：现状读路径（config 单目录 store）
// ─────────────────────────────────────────────────────────────────────────────

// TestP2_ReadPath_Before_ConfigStore 记录并证明现状的两个缺陷。
// 它应当恒绿（现状就是这样）；其价值是与 After 组构成对照——
// 若某次改动让 Before 组变红，说明 config.MemoryStore 的语义被动了（不该发生）。
func TestP2_ReadPath_Before_ConfigStore(t *testing.T) {
	f := newP2Fixture(t)
	f.seed(t)

	store, err := config.NewMemoryStore(filepath.Join(f.home, ".whale", "memory"), f.workDir)
	if err != nil {
		t.Fatalf("config.NewMemoryStore: %v", err)
	}
	list, err := store.List()
	if err != nil {
		t.Fatalf("config store List: %v", err)
	}
	names := entryNames(list)

	// 缺陷 1：单目录 ⇒ 看不到 _user/ scope 的偏好
	if contains(names, "user-pref-a") {
		t.Error("before 组：config store 不应看到 _user/ 偏好（若看到，说明单目录语义已变，本判别器的前提失效）")
	}
	// 缺陷 2：无有效性过滤 ⇒ contradicted 照样在候选里
	if !contains(names, "dead-c") {
		t.Error("before 组：config store 现状不过滤 contradicted（若已过滤，说明过滤被提前做掉了）")
	}
	// 缺陷 2'：过期记忆同样在候选里
	if !contains(names, "expired-d") {
		t.Error("before 组：config store 现状不过滤已过期记忆")
	}
	// 对照：project 事实本来就能看到（说明缺陷不是「什么都看不到」）
	if !contains(names, "proj-fact-b") {
		t.Error("before 组：project scope 事实应可见")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// After 组：接线后读路径（bridge → memory 双 scope + 有效性过滤）
// ─────────────────────────────────────────────────────────────────────────────

func TestP2_ReadPath_After_BridgeMergesAndFilters(t *testing.T) {
	f := newP2Fixture(t)
	f.seed(t)

	src, err := memorybridge.NewSource(f.home, f.workDir)
	if err != nil {
		t.Fatalf("memorybridge.NewSource: %v", err)
	}
	list, err := src.List()
	if err != nil {
		t.Fatalf("bridge List: %v", err)
	}
	names := entryNames(list)

	// 期望 1：能看到 _user/ 偏好（接线前看不到）
	if !contains(names, "user-pref-a") {
		t.Errorf("after 组：应能看到 _user/ 偏好，实际候选=%v", names)
	}
	// 期望 2：contradicted 不得注入
	if contains(names, "dead-c") {
		t.Errorf("after 组：contradicted 记忆不应注入，实际候选=%v", names)
	}
	// 期望 3：已过期不得注入
	if contains(names, "expired-d") {
		t.Errorf("after 组：已过期记忆不应注入，实际候选=%v", names)
	}
	// 期望 4：project 事实仍在
	if !contains(names, "proj-fact-b") {
		t.Errorf("after 组：project 事实应仍可见，实际候选=%v", names)
	}
	// 期望 5：同名去重（project 覆盖 user），只留一条
	if got := count(names, "dup-e"); got != 1 {
		t.Errorf("after 组：dup-e 应经合并去重后只剩 1 条，实际 %d 条（候选=%v）", got, names)
	}
	// 期望 6：仍按 UpdatedAt 降序（本地兜底依赖此顺序）
	for i := 1; i < len(list); i++ {
		if list[i-1].UpdatedAt.Before(list[i].UpdatedAt) {
			t.Fatalf("after 组：候选未按 UpdatedAt 降序（idx %d=%s < idx %d=%s）",
				i-1, list[i-1].UpdatedAt, i, list[i].UpdatedAt)
		}
	}
}

func TestP2_ReadPath_After_ProjectOverridesUserOnRead(t *testing.T) {
	f := newP2Fixture(t)
	f.seed(t)

	src, err := memorybridge.NewSource(f.home, f.workDir)
	if err != nil {
		t.Fatalf("memorybridge.NewSource: %v", err)
	}

	// Read 必须遵守覆盖语义：同名取 project 版本
	e, err := src.Read("dup-e")
	if err != nil {
		t.Fatalf("Read(dup-e): %v", err)
	}
	if !strings.Contains(e.Content, "PROJECT") {
		t.Errorf("Read(dup-e) 应返回 project 版本，实际内容=%q", e.Content)
	}

	// 只有 user 有的条目，应能通过 user 兜底读到
	if _, err := src.Read("user-pref-a"); err != nil {
		t.Errorf("Read(user-pref-a) 应经 user scope 兜底成功，实际 err=%v", err)
	}

	// 带 .md 后缀也应可读（caller 传 entry.Filename）
	if _, err := src.Read("proj-fact-b.md"); err != nil {
		t.Errorf("Read 应接受带 .md 后缀的 name，实际 err=%v", err)
	}

	// 不存在的条目要报错（不得静默返回空条目）
	if _, err := src.Read("no-such-entry"); err == nil {
		t.Error("Read 不存在条目应返回 error")
	}
}
