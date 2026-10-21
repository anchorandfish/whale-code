package ai

import (
	"reflect"
	"testing"
)

// ---------------------------------------------------------------------------
// B4-A1：能力表**瘦身**的结构面判据
//
// ★ 为什么必须是**结构面**（reflect 查字段存在性），而不是行为面：
// 被删的 5 个字段本来就**零生产读取点**。这意味着即使有人把它们加回来、并且
// 一条断言都没有改，行为也**完全不变**（没人读 ⇒ 值恒零 ⇒ 行为面测不出差别）。
// 换句话说：对"零读取字段"而言，任何行为面判据都是**假判别器**。
// 唯一能抓到"字段回来了"的，是结构面。
//
// 定案见 内部工作回执 §2.2。
// ---------------------------------------------------------------------------

// deadCapabilityFields 是 B4-A1 认定「零生产读取」而已删除的字段。
// 它们一旦回来，本判据必须红。
var deadCapabilityFields = []string{
	"SupportsToolCall",
	"SupportsJSON",
	"SupportsStreaming",
	"ContextWindow",
	"MaxOutputTokens",
}

// liveCapabilityFields 是**仍参与生产决策**（或适配器层在用）的字段。
// 钉住它们是防止"瘦身"被误做成"削掉正在用的能力位"。
var liveCapabilityFields = []string{
	// 参与生产决策
	"SupportsThinking",
	"SupportsNativeSearch",
	"SupportsCacheControl",
	"SupportsCacheEditing",
	"SupportsCacheReference",
	"Supports1hTTL",
	"CachingStyle",
	// 内嵌 CacheCapability 的元信息（B2 三态判据靠它区分"没命中"与"命中前缀"）
	"ProviderName",
	// 仅适配器层使用（适配器层已冻结为非生产路径，见 B4 定案 §0）
	"Provider",
	"ProtocolStyle",
	"PrivateFields",
}

func TestModelCapability_DeadFieldsGone(t *testing.T) {
	typ := reflect.TypeOf(ModelCapability{})
	for _, name := range deadCapabilityFields {
		if _, ok := typ.FieldByName(name); ok {
			t.Errorf("ModelCapability 仍含零生产读取字段 %q —— 它不参与任何决策，"+
				"留着只会让人误以为「改表就能改行为」（定案 B4-A1）", name)
		}
	}
}

func TestModelCapability_LiveFieldsKept(t *testing.T) {
	// ★ 诚实标注判别力：本条**不是**独立判别器 —— 现存 live 字段全部被生产代码
	// 静态引用，删任何一个都会**编译失败**（实测：mutant E 删 SupportsNativeSearch
	// ⇒ `[build failed]`，本断言根本没机会执行）。
	// 所以它的实际作用是：① 把"哪些字段在用"从注释提升为可执行断言；
	// ② 兜住未来若有人把字段访问改成反射/字符串动态访问（那时编译器就不管了）。
	// 真正独立的判别器是上面的 DeadFieldsGone（mutant D 已验证：加回字段 ⇒ 红）。
	typ := reflect.TypeOf(ModelCapability{})
	for _, name := range liveCapabilityFields {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("ModelCapability 缺少字段 %q —— 它参与生产决策（或适配器层在用），"+
				"被误删会让能力判定静默退化", name)
		}
	}
}

// TestModelCapability_DeadFieldDiscriminatorProbe 是**判别性自检**的常驻形态：
// 它不验证产品代码，而是验证"上面那条删除判据本身是个真判别器"——
// 用任意一个候选字段名做对照，证明 FieldByName 在当前字段集上确实有分辨力
// （若字段名拼错 / 结构体被整体换掉，两条判据会同时失效而**双双变绿** ⇒ 假绿）。
//
// 这里的对照是：一个**确实存在**的字段必须被查到。若这条都查不到，
// 说明反射查的是错的类型，上面那条"不存在"的结论不可信。
func TestModelCapability_DeadFieldDiscriminatorProbe(t *testing.T) {
	typ := reflect.TypeOf(ModelCapability{})
	if _, ok := typ.FieldByName("SupportsThinking"); !ok {
		t.Fatal("判别器自检失败：连 SupportsThinking 都查不到 ⇒ 反射目标类型错了，" +
			"TestModelCapability_DeadFieldsGone 的绿是假绿")
	}
}
