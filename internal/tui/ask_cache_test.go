// ask_cache_test.go — T3 S5 提问缓存单元测试。
// 覆盖 Set/Get/TTL 过期/Cleanup/Key 碰撞防护/Size 等核心路径。
package tui

import (
	"testing"
	"time"

	"github.com/anchorandfish/whale-code/internal/tools"
)

// TestAskCache_SetGet 验证 Set 后 Get 命中返回原答案（REF-T3-S5-新增）。
func TestAskCache_SetGet(t *testing.T) {
	c := NewAskCache()
	ans := tools.ClarifyAnswer{QuestionID: "q1", SelectedIdx: 2}
	c.Set("clarify", "用 JWT 还是 Session？", ans)

	got, ok := c.Get("clarify", "用 JWT 还是 Session？")
	if !ok {
		t.Fatalf("Get should hit after Set")
	}
	if got.QuestionID != "q1" || got.SelectedIdx != 2 {
		t.Errorf("Get returned wrong answer: %+v", got)
	}
}

// TestAskCache_MissBeforeSet 验证未 Set 时 Get 返回未命中（REF-T3-S5-新增）。
func TestAskCache_MissBeforeSet(t *testing.T) {
	c := NewAskCache()
	if _, ok := c.Get("clarify", "未写入的问题"); ok {
		t.Fatalf("Get should miss before Set")
	}
}

// TestAskCache_TTLExpiry 验证 TTL 过期后 Get 返回未命中（REF-T3-S5-新增）。
// 通过 SetTTL(1ms) + sleep 5ms 模拟过期。
func TestAskCache_TTLExpiry(t *testing.T) {
	c := NewAskCache()
	c.SetTTL(1 * time.Millisecond)
	c.Set("clarify", "短期问题", tools.ClarifyAnswer{QuestionID: "q1"})

	time.Sleep(5 * time.Millisecond)
	if _, ok := c.Get("clarify", "短期问题"); ok {
		t.Fatalf("Get should miss after TTL expiry")
	}
}

// TestAskCache_Cleanup 验证 Cleanup 清除过期条目但保留未过期条目（REF-T3-S5-新增）。
func TestAskCache_Cleanup(t *testing.T) {
	c := NewAskCache()
	c.SetTTL(1 * time.Millisecond)
	c.Set("clarify", "过期问题", tools.ClarifyAnswer{QuestionID: "q1"})
	time.Sleep(5 * time.Millisecond)

	// 再写入一个未过期条目（SetTTL 后续 Set 用新 TTL，但 Set 内部读 c.ttl=1ms 仍过期）
	// 为避免测试 flaky，重置 TTL 后再写
	c.SetTTL(5 * time.Minute)
	c.Set("clarify", "未过期问题", tools.ClarifyAnswer{QuestionID: "q2"})

	c.Cleanup()
	if c.Size() != 1 {
		t.Errorf("Cleanup should leave 1 entry, got %d", c.Size())
	}
	if _, ok := c.Get("clarify", "未过期问题"); !ok {
		t.Errorf("Cleanup should not delete fresh entry")
	}
	if _, ok := c.Get("clarify", "过期问题"); ok {
		t.Errorf("Cleanup should delete expired entry")
	}
}

// TestAskCache_KeyCollision 验证不同 category 或不同 question 不会共享缓存（REF-T3-S5-新增，REF-降级策略-优化3）。
// 同 question 不同 category → key 不同 → 不命中。
// 不同 question 同 category → key 不同 → 不命中。
func TestAskCache_KeyCollision(t *testing.T) {
	c := NewAskCache()
	c.Set("clarify", "问题 A", tools.ClarifyAnswer{QuestionID: "q1"})

	// 同 question 不同 category
	if _, ok := c.Get("dialogue-clarify", "问题 A"); ok {
		t.Errorf("different category should not hit (key isolation failed)")
	}
	// 不同 question 同 category
	if _, ok := c.Get("clarify", "问题 B"); ok {
		t.Errorf("different question should not hit (key isolation failed)")
	}
}

// TestAskCache_Size 验证 Size 反映当前条目数（REF-T3-S5-新增）。
func TestAskCache_Size(t *testing.T) {
	c := NewAskCache()
	if c.Size() != 0 {
		t.Errorf("initial Size should be 0, got %d", c.Size())
	}
	c.Set("clarify", "q1", tools.ClarifyAnswer{})
	c.Set("clarify", "q2", tools.ClarifyAnswer{})
	c.Set("dialogue-clarify", "q1", tools.ClarifyAnswer{})
	if c.Size() != 3 {
		t.Errorf("Size after 3 Sets should be 3, got %d", c.Size())
	}
}

// TestAskCache_OverwriteSameKey 验证同 key 重复 Set 覆盖旧值而非新增（REF-T3-S5-新增）。
func TestAskCache_OverwriteSameKey(t *testing.T) {
	c := NewAskCache()
	c.Set("clarify", "同问题", tools.ClarifyAnswer{QuestionID: "old"})
	c.Set("clarify", "同问题", tools.ClarifyAnswer{QuestionID: "new"})

	if c.Size() != 1 {
		t.Errorf("Size should be 1 after overwrite, got %d", c.Size())
	}
	got, _ := c.Get("clarify", "同问题")
	if got.QuestionID != "new" {
		t.Errorf("Overwrite should keep latest value, got %+v", got)
	}
}
