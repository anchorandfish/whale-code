package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anchorandfish/whale-code/internal/command"
	"github.com/anchorandfish/whale-code/internal/session"
)

// 本文件判「思考内容」的**持久化闭环**：模型思考 → 会话存档（唯一真相源）→ /thinkback 回读。
//
// 两条生产入口（都是真机路径，非手工构造）：
// - 写：(*Model).handleStreamDone —— 回合收尾的**唯一**会话落盘点
// （真机链路：MessageStopEvent → finalizeStream（stream.go:898）→ streamDoneMsg → Update → handleStreamDone）
// - 读：(*Model).handleCommand("/thinkback") —— 真机唯一转发点（`stream.go` 的 `m.handleCommand(input)`）
//
// 为什么必须有这组判据（改动前的实测缺陷，非推断）：
// - `ThinkingDeltaEvent`（stream.go:672）在生产只写 `m.activeCell.AppendThinking()`，那是**渲染态**；
// `ActiveCell.Flush()`（active_cell.go:82）只返回 text、把 thinking `Reset()` 掉 ⇒ 内容被丢弃；
// - 会话消息里 `ContentBlock{Type:"thinking"}` 这个块类型**从来没有任何生产写入点**
// ⇒ `export/{html,markdown,json}`、`session.TokenCount`、`context.SessionMessagesToAIEntries`
// 这些早就认得它的消费者全部读不到东西；
// - 于是 `/thinkback` 的两个「有内容」出口（`CurrentThinking() != ""` 与 `len(ThinkingHistory()) > 0`）
// 在真机上**恒为假** ⇒ 无论模型有没有思考，都只显示「（暂无思考记录）」；
// - 连带使 `format.go:92` 那句「完整思考内容永远在会话存档 `~/.whale/sessions/*.jsonl` 可查」成为**假话**。
//
// 判别面说明：本条组观测的是「会话存档里的块」与「命令回执文本」，两者都是结构化事实，
// 不是散文关键词（R4）。viewport 的换行/截断仍留给人工。
//
// 夹具说明：用 `isolatedHomeModel`（隔离 HOME 后经生产入口 `NewModel` 构造），不用裸的
// `newTUIRegressionModel`。原因是 `clearThinkingRecords()` 会调 `saveSession()`（落盘是语义的一部分，
// 「clear 了但没清掉」比不提供 clear 更糟）⇒ store 会真的写文件。TestMain 虽已把 HOME 隔离到
// **一个共享**临时目录，但本组用例写的是会话文件，落进共享目录会污染同包其它会话用例
// （既有纪律：探针不得写真实用户文件，也不得跨测互串 —— 见 session_picker_test.go `isolatedHomeModel`）。

// transcriptWithThinking 组装一份「一轮问答」的会话存档：user 提问 + assistant（思考块 + 正文）。
func transcriptWithThinking(prompt, thinking, answer string) *session.Session {
	s := session.NewSession()
	s.SetMessages([]session.Message{
		session.UserText(prompt),
		session.AssistantWithTools(
			session.WithThinkingBlock([]session.ContentBlock{{Type: "text", Text: answer}}, thinking),
			"test-model", nil),
	})
	return s
}

// TestStreamDonePersistsThinkingIntoTranscript 判**生产者**：
// 回合收尾后，思考内容必须真的落进会话存档（而不是只留在渲染态的 activeCell 里）。
//
// 判别性：改动前 `handleStreamDone` 只用 `msg.blocks`（只含 text/tool_use）构造消息，
// thinking 仅作为渲染入参 ⇒ 存档里 `HasThinking()` 恒为 false ⇒ 本判据必红。
func TestStreamDonePersistsThinkingIntoTranscript(t *testing.T) {
	m := isolatedHomeModel(t)
	m.sessionService.SetCurrentSession(session.NewSession())
	m.sessionService.SetState(StateStreaming)

	_ = m.handleStreamDone(streamDoneMsg{
		blocks:   []session.ContentBlock{{Type: "text", Text: "答案正文"}},
		thinking: "先看 A，再看 B。",
		model:    "test-model",
	})

	msgs := m.sessionService.CurrentSession().GetMessages()
	if len(msgs) == 0 {
		t.Fatal("回合收尾后会话存档为空 ⇒ 生产者根本没落盘")
	}
	last := msgs[len(msgs)-1]

	if !last.HasThinking() {
		t.Fatalf("思考内容没有落进会话存档 —— 它是思考在生产里唯一的持久化落点。actual blocks=%+v", last.Blocks)
	}
	if got := last.ThinkingContent(); got != "先看 A，再看 B。" {
		t.Fatalf("存档里的思考内容 = %q, want %q", got, "先看 A，再看 B。")
	}
	if got := last.TextContent(); got != "答案正文" {
		t.Fatalf("正文不应因落盘思考而改变，got %q", got)
	}
	if len(last.Blocks) == 0 || last.Blocks[0].Type != session.ThinkingBlockType {
		t.Fatalf("thinking 块应排在 text/tool_use 之前（与模型返回的块序同构），actual=%+v", last.Blocks)
	}
}

// TestThinkbackReadsThinkingFromTranscript 判**消费者**：会话存档里有思考，/thinkback 就必须读出来。
//
// 判别性：改动前 `/thinkback` 只读 TUI 侧两份从未被写入的存储（CurrentThinking / ThinkingHistory），
// 与存档完全脱钩 ⇒ 回执恒为「（暂无思考记录）」⇒ 本判据必红。
func TestThinkbackReadsThinkingFromTranscript(t *testing.T) {
	m := isolatedHomeModel(t)
	m.cmdRegistry = command.NewRegistry() // 补装配：零值 Registry 的 commands map 为 nil，走不到被测分支
	m.reassignServices()
	m.sessionService.SetCurrentSession(
		transcriptWithThinking("解释一下这个函数", "先拆约束，再验边界。", "这是解释。"))

	out := m.handleCommand("/thinkback")

	if !strings.Contains(out, "先拆约束，再验边界。") {
		t.Fatalf("存档里有思考内容，/thinkback 却没读出来（真实思考过程对用户不可见）：\n%s", out)
	}
	if strings.Contains(out, "暂无思考记录") {
		t.Fatalf("存档里有思考却仍报「暂无思考记录」—— 这是一句假话：\n%s", out)
	}
	if !strings.Contains(out, "最近一次思考过程") {
		t.Fatalf("应保留「最近一次思考过程」出口：\n%s", out)
	}
}

// TestThinkbackAllListsEveryTurnFromTranscript 判 `all` 出口能列全整个会话，而不是内存里那 50 条。
func TestThinkbackAllListsEveryTurnFromTranscript(t *testing.T) {
	m := isolatedHomeModel(t)
	m.cmdRegistry = command.NewRegistry()
	m.reassignServices()

	s := session.NewSession()
	s.SetMessages([]session.Message{
		session.UserText("第一问"),
		session.AssistantWithTools(session.WithThinkingBlock(nil, "第一段推理"), "test-model", nil),
		session.UserText("第二问"),
		session.AssistantWithTools(session.WithThinkingBlock(nil, "第二段推理"), "test-model", nil),
	})
	m.sessionService.SetCurrentSession(s)

	out := m.handleCommand("/thinkback all")

	for _, want := range []string{"2 条记录", "记录 #1", "记录 #2", "第一段推理", "第二段推理", "第一问", "第二问"} {
		if !strings.Contains(out, want) {
			t.Fatalf("/thinkback all 应列全会话思考，缺 %q：\n%s", want, out)
		}
	}
}

// TestThinkbackClearStripsThinkingButKeepsConversation 判 `clear` 的**作用域**：
// 只清思考记录，不得顺手删掉对话本体。
func TestThinkbackClearStripsThinkingButKeepsConversation(t *testing.T) {
	m := isolatedHomeModel(t)
	m.cmdRegistry = command.NewRegistry()
	m.reassignServices()
	m.sessionService.SetCurrentSession(
		transcriptWithThinking("解释一下这个函数", "先拆约束，再验边界。", "这是解释。"))

	out := m.handleCommand("/thinkback clear")
	if !strings.Contains(out, "思考记录已清空") {
		t.Fatalf("clear 回执应保留确认文案：\n%s", out)
	}

	s := m.sessionService.CurrentSession()
	for i, msg := range s.GetMessages() {
		if msg.HasThinking() {
			t.Fatalf("clear 后第 %d 条消息仍带思考块：%+v", i, msg.Blocks)
		}
	}
	joined := ""
	for _, msg := range s.GetMessages() {
		joined += msg.TextContent() + "\n"
	}
	if !strings.Contains(joined, "解释一下这个函数") || !strings.Contains(joined, "这是解释。") {
		t.Fatalf("clear 只应清思考记录，不得删对话本体：%q", joined)
	}

	if again := m.handleCommand("/thinkback"); !strings.Contains(again, "暂无思考记录") {
		t.Fatalf("clear 之后 /thinkback 应回到空记录形态：\n%s", again)
	}
}

// TestSessionMessagesToAPINeverCarriesThinking 是**护栏（两态皆绿，不具判别性）**。
//
// 为什么留它：把思考写进会话存档**绝不能**顺手改掉发给模型的请求体 ——
// 一旦思考被下发，行为立刻偏离基线（模型看到的上下文变了），而这一点在真机上
// 表现为"回答风格变了"，几乎无法归因到本次改动。
//
// ⚠️ 本条在改动前后**都绿**（`sessionMessagesToAPI` 从来只映射 text/image/tool_use/tool_result），
// 因此它不证明本次修复生效，只证明本次修复**没有越界**。不要把它当判别器读。
// TestSessionMessagesToAPICarriesThinkingFaithfully 判**上游传递面**：
// thinking 块必须如实进入 provider-neutral 的消息形态 —— 否则 DeepSeek 面
// （required：不回传即 HTTP 400）根本拿不到内容可回传。
//
// ★ 口径变更（2026-10-04，thinking 回传步骤 2）：本测试原名
// `TestSessionMessagesToAPINeverCarriesThinking`，断言"请求体里永不出现 thinking"。
// 那条口径把**两件事**压成了一个：
//
//	① 上游能不能把思考递下去 —— 传递能力（必须为「能」）；
//	② 最终要不要发给服务端 —— **模型面**裁决（唯一判据 ai.ResolveThinkingReplay）。
//
// 现在二者分开：上游**如实传递**（本测试），裁决在 ai 层，且由两条判据钉住 ——
//
//	· ai.TestBuildOpenAIBody_ReplayRespectsFace（qwen/claude/gpt/未知面**不得**写出）；
//	· ai.TestConvertMessagesForAnthropic_StripsUnsignedThinking（Anthropic 面必须剥离）。
//
// "上游不带"不再等价于"线上不发"，所以护栏必须下移到**真正决定 wire 的那一层**，
// 留在这一层会变成一条**测错了对象**的假护栏。
func TestSessionMessagesToAPICarriesThinkingFaithfully(t *testing.T) {
	msgs := transcriptWithThinking("问", "内部推理：我记得密钥长这样 sk-ant-api03-xxxxxxxx", "答").GetMessages()

	entries := sessionMessagesToAPI(msgs)
	raw, err := json.Marshal(entries)
	if err != nil {
		t.Fatalf("序列化请求体失败：%v", err)
	}
	wire := string(raw)

	// ① 传递能力：thinking 必须如实带下去（形态与 context.SessionMessagesToAIEntries 一致）
	if !strings.Contains(wire, `"type":"thinking"`) {
		t.Fatalf("thinking 块未进入消息形态 ⇒ DeepSeek 面拿不到内容可回传：%s", wire)
	}
	if !strings.Contains(wire, "内部推理") {
		t.Fatalf("思考正文未如实传递：%s", wire)
	}
	// ② 暂存形态里正文照常（回传不得顶掉正文）
	if !strings.Contains(wire, "答") {
		t.Fatalf("正文必须照常下发，actual=%s", wire)
	}
}

// TestRedactSecretLeakCoversThinkingBlocks 判**泄密面**：
// 思考一旦落盘，就同样必须过密钥脱敏 —— 否则思考里出现的密钥会以明文写进
// `~/.whale/sessions/*.jsonl`，并被 `/share` 导出带出去。
//
// 判别性：改动前 `redactSecretLeakInBlocks` 只扫 `Type == "text"` 的块 ⇒ 本判据必红。
func TestRedactSecretLeakCoversThinkingBlocks(t *testing.T) {
	rawKey := "sk-ant-api03-" + strings.Repeat("a", 30)
	blocks := []session.ContentBlock{{
		Type:     session.ThinkingBlockType,
		Thinking: "我先把密钥 " + rawKey + " 记下来再继续",
	}}

	redactSecretLeakInBlocks(blocks)

	if strings.Contains(blocks[0].Thinking, rawKey) {
		t.Fatalf("思考块里的密钥未被脱敏 ⇒ 会明文落盘并随 /share 外泄：%q", blocks[0].Thinking)
	}
	if !strings.Contains(blocks[0].Thinking, "SECRET_LEAK_DETECTED") {
		t.Fatalf("思考块命中密钥后应留下脱敏标记（否则用户不知道被改过）：%q", blocks[0].Thinking)
	}
}
