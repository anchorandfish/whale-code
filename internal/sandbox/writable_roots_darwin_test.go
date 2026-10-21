//go:build darwin

package sandbox

import (
	"os"
	"strings"
	"testing"
)

// TestDarwinProfileExtraWriteDirs_ComesFromSharedSource 是 T1 / 的
// **同源判别器**（行为面）：seatbelt profile 的额外可写根必须就是
// `ExtraWritableRoots()` 的返回值 —— 即"沙箱会放行什么"与"审批面拿到的可写根"
// 是同一份事实。
func TestDarwinProfileExtraWriteDirs_ComesFromSharedSource(t *testing.T) {
	ws := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())

	want := ExtraWritableRoots()
	opts, err := defaultDarwinProfileOptions(ws)
	if err != nil {
		t.Fatalf("defaultDarwinProfileOptions: %v", err)
	}
	if len(opts.ExtraWriteDirs) != len(want) {
		t.Fatalf("profile ExtraWriteDirs = %v, ExtraWritableRoots() = %v —— 两边不同源", opts.ExtraWriteDirs, want)
	}
	for i := range want {
		if opts.ExtraWriteDirs[i] != want[i] {
			t.Fatalf("profile ExtraWriteDirs[%d] = %q, ExtraWritableRoots()[%d] = %q —— 两边不同源",
				i, opts.ExtraWriteDirs[i], i, want[i])
		}
	}
}

// TestDarwinProfileOptions_DoesNotRespellRoots 是 T1 的**结构面判别器**。
//
// 为什么需要结构面：行为面无法区分"两边都调 os.TempDir()"（各自拼一份，但值恰好相同）
// 与"两边共用同一份声明"。而 的失效形态恰恰是**各自拼一份** —— 今天值相同、
// 明天某一侧多加了 caches 就漂移。⇒ 断言 profile 构造处**不得**出现
// `os.TempDir` / `os.UserCacheDir`（它们必须只出现在 writable_roots.go）。
//
// ★ 反例注入（打红）：把 `extraWriteDirs := ExtraWritableRoots()` 换回内联的
// `os.TempDir()` / `os.UserCacheDir()` 拼接 ⇒ 本测试红。
func TestDarwinProfileOptions_DoesNotRespellRoots(t *testing.T) {
	src, err := os.ReadFile("os_sandbox_darwin.go")
	if err != nil {
		t.Fatalf("read os_sandbox_darwin.go: %v", err)
	}
	// 只检查 defaultDarwinProfileOptions 函数体那一段。
	body := functionBody(string(src), "func defaultDarwinProfileOptions(")
	if body == "" {
		t.Fatal("未能定位 defaultDarwinProfileOptions 函数体（测试需随源码结构调整）")
	}
	for _, forbidden := range []string{"os.TempDir()", "os.UserCacheDir()"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("defaultDarwinProfileOptions 里又出现了 %s —— 可写根必须唯一声明在 writable_roots.go\n%s",
				forbidden, body)
		}
	}
	if !strings.Contains(body, "ExtraWritableRoots()") {
		t.Fatalf("defaultDarwinProfileOptions 未消费 ExtraWritableRoots() —— 同源关系断了\n%s", body)
	}
}

// functionBody 截取以 prefix 开头、到下一个顶层 `}` 为止的源码片段。
// 只用于结构面断言（不需要精确解析 Go），找不到时返回空串。
func functionBody(src, prefix string) string {
	i := strings.Index(src, prefix)
	if i < 0 {
		return ""
	}
	rest := src[i:]
	end := strings.Index(rest, "\n}\n")
	if end < 0 {
		return rest
	}
	return rest[:end]
}
