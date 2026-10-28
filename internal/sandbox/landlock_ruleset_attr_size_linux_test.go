//go:build linux

package sandbox

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// TestLandlockRulesetAttrSize 钉住传给 landlock_create_ruleset 的 attr 长度 = 8 字节。
//
// 为什么需要这道门（2026-10-28，x/sys 0.48.0 → 0.49.0 的连带升级）：
// 那次升级把 unix.LandlockRulesetAttr 从 3 个字段扩到 6 个（24B → 48B），同时把
// Access_fs 改名为 Handled_access_fs。**编译器只会报「字段不存在」**，改名就绿了；
// 但如果顺手把 size 也改回 unsafe.Sizeof(attr)（48B），后果是：
//
//	老内核（Landlock ABI v1–v3）收到比自己认识的 struct 更大的 size ⇒ 返回 E2BIG
//	⇒ tryCreateRuleset 探测失败 ⇒ bestHandledAccess 返回 ok=false
//	⇒ 沙箱静默降级为「无沙箱」。
//
// 而这一切在 macOS 上完全看不见（该文件带 linux 构建约束），新内核上也照样通过
// （新内核认识 48B 结构体）。伤害形态 = 安全边界静默消失、测试全绿，所以单独设门：
// 只断言常量本身，不依赖内核，任何 Linux runner 都能跑。
func TestLandlockRulesetAttrSize(t *testing.T) {
	const want = 8 // 只传首个字段 handled_access_fs（uint64）

	if got := landlockRulesetAttrSize; got != want {
		t.Fatalf("landlock ruleset attr size = %d 字节，期望 %d\n"+
			"说明：应只传 handled_access_fs 一个字段的长度；传整个结构体（当前 %d 字节）"+
			"会让老内核（ABI v1–v3）返回 E2BIG，导致 Landlock 探测失败、沙箱静默降级。\n"+
			"修复：把 syscall 的 size 参数改回 landlockRulesetAttrSize，不要用 unsafe.Sizeof(attr)。",
			got, want, unsafe.Sizeof(unix.LandlockRulesetAttr{}))
	}
}

// TestLandlockRulesetAttrFieldLayout 钉住首字段的位置与类型。
//
// size=8 能成立的前提是「内核 struct 的第一个字段就是 handled_access_fs，且为 uint64」。
// 若将来 x/sys 在这个结构体前面插字段或改类型，只判 size 不够（size 可能仍是 8），
// 这里再把「首字段就是它」钉一次。
func TestLandlockRulesetAttrFieldLayout(t *testing.T) {
	var attr unix.LandlockRulesetAttr
	attr.Handled_access_fs = 0xA5

	first := *(*uint64)(unsafe.Pointer(&attr))
	if first != 0xA5 {
		t.Fatalf("Handled_access_fs 不在结构体起始位置（首 8 字节 = %#x，期望 0xa5）\n"+
			"说明：landlock 的 syscall 只读前 8 字节，字段位置一变，传进去的 handled 权限集就是错的",
			first)
	}

	if got := unsafe.Offsetof(attr.Handled_access_fs); got != 0 {
		t.Fatalf("Handled_access_fs offset = %d，期望 0", got)
	}
}
