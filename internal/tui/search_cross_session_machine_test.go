package tui

// /search 跨会话机测。
//
// 背景面：/search 的跨会话能力此前只有 internal/search 层的单测（Searcher.Search 对
// 两个会话文件各命中），**命令入口层零判据** —— handleSearch 的接线（searcher 从哪来、
// 无 --session 时是否真扫全会话库、--session 收窄是否生效）没有任何机器断言。
//
// 生产路径（R1 断言）：
// m.handleCommand("/search ...") (app.go:3853 case "search")
// → m.handleSearch(args) (commands_rewind.go:398)
// → m.toolService.Searcher() ← NewModel 在 lifecycle.go:739 用真实 session store 装配
// → Searcher.Search(query) ← 无 --session ⇒ store.List() 全量扫描（search.go:99）
//
// 输入注入（R6b：注入的是「输入值」不是「替换判定」）：
// m.SessionModel.store / m.searcher 按**生产同一构造器**（NewSearcherWithConfig）
// 指向受控临时目录，会话经真实 Store.AppendBatch 落盘 —— 判定链 100% 生产码。
//
// 判别性自证（R4 通道 A）：overlay mutant 把 handleSearch 钉成固定降级文案 ⇒ 三条断言全红。

import (
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/search"
	"github.com/anchorandfish/whale-code/internal/session"
)

func newCrossSessionSearchFixture(t *testing.T) (*Model, *session.Store) {
	t.Helper()
	m := newTUIRegressionModel(t)
	store, err := session.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("session.NewStore: %v", err)
	}
	// 生产同一构造器 + 受控输入（R6b：换的是 store 这个输入，不是 Searcher 的判定逻辑）
	m.SessionModel.store = store
	m.searcher = search.NewSearcherWithConfig(store, 5)

	mustAppend := func(id string, msgs []session.Message) {
		t.Helper()
		if err := store.AppendBatch(id, msgs); err != nil {
			t.Fatalf("AppendBatch %s: %v", id, err)
		}
	}
	mustAppend("probe-alpha-sentinel", []session.Message{
		session.UserText("quantum sandwich protocol review"),
	})
	mustAppend("probe-beta-sentinel", []session.Message{
		session.UserText("unrelated chatter"),
		session.AssistantText("the quantum sandwich protocol returns", "test-model", nil),
	})
	return m, store
}

func TestSearchSlashCommandSearchesAcrossSessions(t *testing.T) {
	m, _ := newCrossSessionSearchFixture(t)

	out := m.handleCommand("/search quantum sandwich")

	if !strings.Contains(out, "searched 2 sessions") {
		t.Fatalf("/search 未扫全会话库（跨会话死角）：输出=%q", out)
	}
	if !strings.Contains(out, "probe-alpha-sentinel") {
		t.Fatalf("跨会话搜索漏掉会话 probe-alpha-sentinel：输出=%q", out)
	}
	if !strings.Contains(out, "probe-beta-sentinel") {
		t.Fatalf("跨会话搜索漏掉会话 probe-beta-sentinel：输出=%q", out)
	}
}

func TestSearchSlashCommandSessionScopeNarrows(t *testing.T) {
	m, _ := newCrossSessionSearchFixture(t)

	out := m.handleCommand("/search --session probe-alpha-sentinel quantum sandwich")

	if !strings.Contains(out, "probe-alpha-sentinel") {
		t.Fatalf("--session 收窄后应只命中指定会话：输出=%q", out)
	}
	if strings.Contains(out, "probe-beta-sentinel") {
		t.Fatalf("--session 收窄失效：范围外会话 probe-beta-sentinel 不应出现：输出=%q", out)
	}
}

func TestSearchSlashCommandMissReturnsHonestEmpty(t *testing.T) {
	m, _ := newCrossSessionSearchFixture(t)

	out := m.handleCommand("/search needle-that-exists-nowhere")
	if !strings.Contains(out, "No matching sessions") {
		t.Fatalf("无命中应给出诚实的空结果文案（不是报错也不是假命中）：输出=%q", out)
	}
	if strings.Contains(out, "probe-alpha-sentinel") || strings.Contains(out, "probe-beta-sentinel") {
		t.Fatalf("无命中却出现了会话条目（假命中）：输出=%q", out)
	}
}
