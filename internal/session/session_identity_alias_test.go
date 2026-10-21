package session

import (
	"os"
	"strings"
	"testing"
)

// 本文件锁定「UUID 主键 + 可读别名」这一改造的可判别行为。
//
// 背景：改造前会话文件名 = 「首条输入前10字_时间戳」，即主键与别名是同一个东西。
// 后果有两个真实缺陷：
//  1. 命令行无法点名会话（没有稳定 ID 可引用）；
//  2. 两个实例在同一秒内首句相同时生成同名文件 → 同一份 transcript 被两个对话
//     交错写入（多开探针已复现锁争用/写入被拒）。
//
// 改造后主键是 UUID v7，别名另行持久化。以下用例是**判别器**：
// 把实现退回「文件名即别名」会立刻转红。

// TestAppendBatchNamedPersistsAlias 别名必须随首批消息落盘，且 ID 是 UUID。
func TestAppendBatchNamedPersistsAlias(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	id := GenerateID()
	if !IsSessionID(id) {
		t.Fatalf("会话主键必须是 UUID，实际 %q", id)
	}
	msgs := []Message{UserText("帮我重构这段登录逻辑，顺带补测试")}
	if err := store.AppendBatchNamed(id, DeriveAlias("帮我重构这段登录逻辑，顺带补测试"), msgs); err != nil {
		t.Fatalf("AppendBatchNamed: %v", err)
	}

	// 别名必须能被扫出来（List 是 /session list 与选择器的数据源）。
	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("期望 1 个会话，实际 %d", len(summaries))
	}
	if summaries[0].ID != id {
		t.Errorf("ID = %q, want %q", summaries[0].ID, id)
	}
	const wantAlias = "帮我重构这段登录逻辑..."
	if summaries[0].Name != wantAlias {
		t.Errorf("Name = %q, want %q", summaries[0].Name, wantAlias)
	}

	// Load 也要还原别名：resume 之后 Memory/状态栏依赖它。
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Metadata.Name != wantAlias {
		t.Errorf("Load 后 Metadata.Name = %q, want %q", loaded.Metadata.Name, wantAlias)
	}
	if got := len(loaded.GetMessages()); got != 1 {
		t.Errorf("消息数 = %d, want 1", got)
	}
}

// TestAppendBatchNamedIsIdempotent 后续批次不得再写一条 metadata 记录。
//
// 不幂等的后果是文件里堆积 metadata 行，每次 List 都要多解析一遍，
// 且「哪一条是当前别名」变得依赖遍历顺序。
func TestAppendBatchNamedIsIdempotent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	id := GenerateID()
	if err := store.AppendBatchNamed(id, "alias", []Message{UserText("first")}); err != nil {
		t.Fatalf("AppendBatchNamed #1: %v", err)
	}
	if err := store.AppendBatchNamed(id, "alias", []Message{UserText("second")}); err != nil {
		t.Fatalf("AppendBatchNamed #2: %v", err)
	}

	raw, err := readSessionFile(store, id)
	if err != nil {
		t.Fatalf("读会话文件: %v", err)
	}
	if n := strings.Count(raw, sessionMetadataRecordType); n != 1 {
		t.Errorf("metadata 记录出现 %d 次，期望 1 次\n%s", n, raw)
	}
}

// TestListLegacySessionKeepsFilenameAsName 存量会话（文件名即可读名）展示名不变。
//
// 这是**兼容性判别器**：UUID 化容易顺手把旧会话的展示名也改成 hex，
// 那对存量用户就是一次无意义的界面回归。
func TestListLegacySessionKeepsFilenameAsName(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const legacyID = "修复登录bug_2026-09-19_10-00-00"
	if err := store.AppendBatch(legacyID, []Message{UserText("修复登录bug")}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("期望 1 个会话，实际 %d", len(summaries))
	}
	if summaries[0].Name != legacyID {
		t.Errorf("旧式会话 Name = %q, want %q（文件名即别名）", summaries[0].Name, legacyID)
	}
}

// TestSetAliasChangesNameNotID 改名只动别名，主键（地址）必须不变。
//
// 判别器：若实现按旧语义去 Rename 文件，ID 会变成新名字，本用例转红。
func TestSetAliasChangesNameNotID(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	id := GenerateID()
	if err := store.AppendBatchNamed(id, "旧名字", []Message{UserText("hi")}); err != nil {
		t.Fatalf("AppendBatchNamed: %v", err)
	}
	if err := store.SetAlias(id, "新名字"); err != nil {
		t.Fatalf("SetAlias: %v", err)
	}

	summaries, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("期望 1 个会话，实际 %d", len(summaries))
	}
	if summaries[0].Name != "新名字" {
		t.Errorf("改名后 Name = %q, want 新名字", summaries[0].Name)
	}
	if summaries[0].ID != id {
		t.Errorf("改名后 ID = %q, want %q（主键不得随改名变化）", summaries[0].ID, id)
	}

	loaded, err := store.Load(id)
	if err != nil {
		t.Fatalf("按原 ID 仍应可加载: %v", err)
	}
	if loaded.Metadata.Name != "新名字" {
		t.Errorf("Load 后 Metadata.Name = %q, want 新名字", loaded.Metadata.Name)
	}
}

// TestResolveAcceptsUUIDPrefixAliasAndLegacyName 四种写法都要能命中同一个会话。
func TestResolveAcceptsUUIDPrefixAliasAndLegacyName(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	id := GenerateID()
	if err := store.AppendBatchNamed(id, "登录重构", []Message{UserText("登录重构")}); err != nil {
		t.Fatalf("AppendBatchNamed: %v", err)
	}
	const legacyID = "旧会话_2026-09-01_09-00-00"
	if err := store.AppendBatch(legacyID, []Message{UserText("旧会话")}); err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}

	cases := []struct {
		desc string
		in   string
		want string
	}{
		{"完整 UUID", id, id},
		{"UUID 前 8 位", id[:8], id},
		{"可读别名", "登录重构", id},
		{"旧式文件名", legacyID, legacyID},
	}
	for _, tc := range cases {
		got, err := store.Resolve(tc.in)
		if err != nil {
			t.Errorf("%s(%q): 解析失败 %v", tc.desc, tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s(%q) = %q, want %q", tc.desc, tc.in, got, tc.want)
		}
	}

	// 不存在的标识必须报错，不得静默返回空/猜一个。
	if got, err := store.Resolve("不存在的会话zzz"); err == nil {
		t.Errorf("不存在的标识应报错，实际返回 %q", got)
	}
}

// TestForkInheritsAliasWithMarker fork 复制别名但必须可区分，且主键全新。
func TestForkInheritsAliasWithMarker(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	parent := GenerateID()
	if err := store.AppendBatchNamed(parent, "原会话", []Message{UserText("hello")}); err != nil {
		t.Fatalf("AppendBatchNamed: %v", err)
	}

	forkID, err := store.ForkSession(parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	if forkID == parent {
		t.Fatalf("fork 主键与父会话相同: %q", forkID)
	}

	loaded, err := store.Load(forkID)
	if err != nil {
		t.Fatalf("Load fork: %v", err)
	}
	if loaded.Metadata.Name != "原会话 (fork)" {
		t.Errorf("fork 别名 = %q, want %q", loaded.Metadata.Name, "原会话 (fork)")
	}
	if got := len(loaded.GetMessages()); got != 1 {
		t.Errorf("fork 消息数 = %d, want 1（历史应被复制）", got)
	}
}

// readSessionFile 读取会话文件原文，供计数断言使用。
func readSessionFile(store *Store, sessionID string) (string, error) {
	b, err := os.ReadFile(store.sessionPath(sessionID))
	return string(b), err
}
