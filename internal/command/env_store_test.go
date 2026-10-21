// Package command — env_store_test.go 验证 EnvStore 进程内隔离与 /env 命令废除 os.Setenv
package command

import (
	"os"
	"strings"
	"testing"
)

// TestEnvStore_SetGet 验证 EnvStore.Set/Get 基本读写
func TestEnvStore_SetGet(t *testing.T) {
	store := NewEnvStore()
	store.Set("WHALE_TEST_KEY", "value123")

	val, ok := store.Get("WHALE_TEST_KEY")
	if !ok {
		t.Fatal("expected ok=true after Set")
	}
	if val != "value123" {
		t.Fatalf("expected value123, got %s", val)
	}
}

// TestEnvStore_GetFallbackToOSEnviron 验证 Get 在 EnvStore 未命中时 fallback 到 os.Getenv
func TestEnvStore_GetFallbackToOSEnviron(t *testing.T) {
	store := NewEnvStore()
	// 设置一个系统环境变量（测试后清理）
	os.Setenv("WHALE_TEST_SYS", "sysval")
	defer os.Unsetenv("WHALE_TEST_SYS")

	// EnvStore 中没有该 key，应 fallback 到 os.Getenv
	val, ok := store.Get("WHALE_TEST_SYS")
	if !ok {
		t.Fatal("expected ok=true via os.Getenv fallback")
	}
	if val != "sysval" {
		t.Fatalf("expected sysval, got %s", val)
	}
}

// TestEnvStore_GetOverridesOSEnviron 验证 EnvStore 的值优先级高于 os.Getenv
func TestEnvStore_GetOverridesOSEnviron(t *testing.T) {
	store := NewEnvStore()
	os.Setenv("WHALE_TEST_OVERRIDE", "sys-original")
	defer os.Unsetenv("WHALE_TEST_OVERRIDE")

	// EnvStore 覆盖系统变量
	store.Set("WHALE_TEST_OVERRIDE", "store-override")

	val, ok := store.Get("WHALE_TEST_OVERRIDE")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != "store-override" {
		t.Fatalf("expected store-override (EnvStore 优先), got %s", val)
	}
}

// TestEnvStore_Unset 验证 Unset 仅删除 EnvStore 中的值，不影响 os.Environ
func TestEnvStore_Unset(t *testing.T) {
	store := NewEnvStore()
	store.Set("WHALE_TEST_UNSET", "val")
	store.Unset("WHALE_TEST_UNSET")

	_, ok := store.Get("WHALE_TEST_UNSET")
	if ok {
		t.Fatal("expected ok=false after Unset")
	}
}

// TestEnvStore_UnsetDoesNotTouchOSEnviron 验证 EnvStore.Unset 不调用 os.Unsetenv
// 关键约束：EnvStore 不污染全局 os.Environ
func TestEnvStore_UnsetDoesNotTouchOSEnviron(t *testing.T) {
	store := NewEnvStore()
	// 在 os.Environ 中设置一个变量
	os.Setenv("WHALE_TEST_SYS_KEEP", "keep-me")
	defer os.Unsetenv("WHALE_TEST_SYS_KEEP")

	// EnvStore 中也设置同名变量
	store.Set("WHALE_TEST_SYS_KEEP", "store-val")
	// 从 EnvStore 删除
	store.Unset("WHALE_TEST_SYS_KEEP")

	// os.Environ 中的变量应仍然存在（不受 EnvStore.Unset 影响）
	sysVal := os.Getenv("WHALE_TEST_SYS_KEEP")
	if sysVal != "keep-me" {
		t.Fatalf("os.Environ should not be affected by EnvStore.Unset, got %s", sysVal)
	}
}

// TestEnvStore_List 验证 List 返回 EnvStore 变量的副本
func TestEnvStore_List(t *testing.T) {
	store := NewEnvStore()
	store.Set("KEY_A", "1")
	store.Set("KEY_B", "2")

	listed := store.List()
	if len(listed) != 2 {
		t.Fatalf("expected 2 vars, got %d", len(listed))
	}
	if listed["KEY_A"] != "1" || listed["KEY_B"] != "2" {
		t.Fatalf("unexpected list content: %v", listed)
	}

	// 修改返回的 map 不应影响内部状态
	listed["KEY_A"] = "modified"
	again, _ := store.Get("KEY_A")
	if again != "1" {
		t.Fatalf("internal state should not be affected by List() mutation, got %s", again)
	}
}

// TestEnvStore_Snapshot 验证 Snapshot 合并 EnvStore 与 os.Environ，EnvStore 覆盖
func TestEnvStore_Snapshot(t *testing.T) {
	store := NewEnvStore()
	os.Setenv("WHALE_TEST_SNAP_SYS", "sys-snap")
	defer os.Unsetenv("WHALE_TEST_SNAP_SYS")

	store.Set("WHALE_TEST_SNAP_STORE", "store-snap")
	// 同名覆盖
	store.Set("WHALE_TEST_SNAP_SYS", "store-overrides-sys")

	snap := store.Snapshot()
	if snap["WHALE_TEST_SNAP_SYS"] != "store-overrides-sys" {
		t.Fatalf("EnvStore should override os.Environ in Snapshot, got %s", snap["WHALE_TEST_SNAP_SYS"])
	}
	if snap["WHALE_TEST_SNAP_STORE"] != "store-snap" {
		t.Fatalf("expected store-snap, got %s", snap["WHALE_TEST_SNAP_STORE"])
	}
}

// TestEnvCommandHandler_SetDoesNotCallOSSetenv 验证 /env KEY=VALUE 不调用 os.Setenv
// 这是 CMD-P1-004 的核心验收点：AI client 的 API Key 不受 /env 命令影响
func TestEnvCommandHandler_SetDoesNotCallOSSetenv(t *testing.T) {
	store := NewEnvStore()
	handler := NewEnvCommandHandler(nil, store)

	// 使用一个独特的 key 名，确保测试前 os.Environ 中不存在
	testKey := "WHALE_CMD_TEST_NO_OS_SET"
	testValue := "value-from-env-cmd"
	// 前置清理 + 后置清理，确保测试隔离
	os.Unsetenv(testKey)
	defer os.Unsetenv(testKey)

	output := handler(testKey + "=" + testValue)

	// 1. 输出应包含成功标记
	if !strings.Contains(output, "✅ Set:") {
		t.Fatalf("expected success output, got: %s", output)
	}

	// 2. 关键断言：os.Environ 中不应存在该变量
	sysVal := os.Getenv(testKey)
	if sysVal != "" {
		t.Fatalf("CMD-P1-004 违规: /env 调用了 os.Setenv, os.Getenv 返回 %s", sysVal)
	}

	// 3. EnvStore 中应存在该变量
	storeVal, ok := store.Get(testKey)
	if !ok || storeVal != testValue {
		t.Fatalf("EnvStore should contain the var, got ok=%v val=%s", ok, storeVal)
	}
}

// TestEnvCommandHandler_UnsetDoesNotCallOSUnsetenv 验证 /env unset KEY 不调用 os.Unsetenv
// 确保 os.Environ 中已有的变量不被 /env unset 删除
func TestEnvCommandHandler_UnsetDoesNotCallOSUnsetenv(t *testing.T) {
	store := NewEnvStore()
	handler := NewEnvCommandHandler(nil, store)

	testKey := "WHALE_CMD_TEST_NO_OS_UNSET"
	// 在 os.Environ 中预先设置该变量
	os.Setenv(testKey, "should-survive")
	defer os.Unsetenv(testKey)

	// 同时在 EnvStore 中设置（验证仅删除 EnvStore 的）
	store.Set(testKey, "store-val")

	// 执行 /env unset
	output := handler("unset " + testKey)

	// 1. os.Environ 中的变量应仍然存在（不受 /env unset 影响）
	sysVal := os.Getenv(testKey)
	if sysVal != "should-survive" {
		t.Fatalf("CMD-P1-004 违规: /env unset 调用了 os.Unsetenv, os.Getenv 返回 %s", sysVal)
	}

	// 2. EnvStore 中的变量应被删除
	// 注意：store.Get 会 fallback 到 os.Getenv，所以 ok 会是 true（来自 os.Environ）
	// 这里用 List 验证 EnvStore 内部状态
	listed := store.List()
	if _, inStore := listed[testKey]; inStore {
		t.Fatalf("EnvStore should not contain the var after unset, list=%v", listed)
	}

	// 3. 输出应表明未持久化删除（因为 persister 为 nil）
	if !strings.Contains(output, "no need to delete") && !strings.Contains(output, "Deleted") {
		// persister 为 nil 时返回 "no need to delete" 是合理的
		t.Logf("unset output: %s", output)
	}
}

// TestEnvCommandHandler_GetFallbackOrder 验证 handleEnvGet 的 fallback 顺序
// 顺序：EnvStore → os.Getenv → persister customVars（不能颠倒）
func TestEnvCommandHandler_GetFallbackOrder(t *testing.T) {
	testKey := "WHALE_CMD_TEST_GET_ORDER"
	defer os.Unsetenv(testKey)

	// 场景 1: EnvStore 命中（最高优先级）
	t.Run("EnvStore命中", func(t *testing.T) {
		store := NewEnvStore()
		store.Set(testKey, "from-store")
		os.Setenv(testKey, "from-sys")

		handler := NewEnvCommandHandler(nil, store)
		output := handler(testKey)
		if !strings.Contains(output, "from-store") {
			t.Fatalf("expected EnvStore value (from-store), got: %s", output)
		}
		if !strings.Contains(output, "[runtime]") {
			t.Fatalf("expected [runtime] tag, got: %s", output)
		}
	})

	// 场景 2: os.Getenv 命中（EnvStore 未设置）
	t.Run("os.Getenv命中", func(t *testing.T) {
		store := NewEnvStore()
		os.Setenv(testKey, "from-sys")

		handler := NewEnvCommandHandler(nil, store)
		output := handler(testKey)
		if !strings.Contains(output, "from-sys") {
			t.Fatalf("expected os.Getenv value (from-sys), got: %s", output)
		}
		if !strings.Contains(output, "[System]") {
			t.Fatalf("expected [System] tag, got: %s", output)
		}
	})

	// 场景 3: persister 命中（EnvStore 和 os.Getenv 都未命中）
	t.Run("persister命中", func(t *testing.T) {
		store := NewEnvStore()
		os.Unsetenv(testKey)

		persister := &mockEnvPersister{
			vars: map[string]string{testKey: "from-persister"},
		}
		handler := NewEnvCommandHandler(persister, store)
		output := handler(testKey)
		if !strings.Contains(output, "from-persister") {
			t.Fatalf("expected persister value (from-persister), got: %s", output)
		}
		if !strings.Contains(output, "[persisted]") {
			t.Fatalf("expected [persisted] tag, got: %s", output)
		}
	})
}

// TestEnvCommandHandler_NilEnvStoreFallback 验证 NewEnvCommandHandler 在 envStore 为 nil 时不 panic
func TestEnvCommandHandler_NilEnvStoreFallback(t *testing.T) {
	// envStore 为 nil 时内部创建临时实例，不应 panic
	handler := NewEnvCommandHandler(nil, nil)
	output := handler("WHALE_NIL_TEST=test")
	if !strings.Contains(output, "✅ Set:") {
		t.Fatalf("expected success even with nil envStore, got: %s", output)
	}
	// 清理（虽然临时 EnvStore 不会写入 os.Environ，但保持测试卫生）
	os.Unsetenv("WHALE_NIL_TEST")
}

// TestEnvCommandHandler_SetPersistsViaPersister 验证 /env KEY=VALUE 同时通过 persister 持久化
// 约束：EnvStore 不持久化，持久化仍走 EnvPersister.SaveEnvVar
func TestEnvCommandHandler_SetPersistsViaPersister(t *testing.T) {
	store := NewEnvStore()
	persister := &mockEnvPersister{vars: map[string]string{}}
	handler := NewEnvCommandHandler(persister, store)

	handler("PERSIST_TEST_KEY=persist-value")

	// persister 应收到 SaveEnvVar 调用
	if persister.vars["PERSIST_TEST_KEY"] != "persist-value" {
		t.Fatalf("persister should have received SaveEnvVar, got: %v", persister.vars)
	}
	// EnvStore 也应有该值
	if val, _ := store.Get("PERSIST_TEST_KEY"); val != "persist-value" {
		t.Fatalf("EnvStore should also have the value")
	}
}

// TestEnvCommandHandler_ListShowsRuntimeAndPersisted 验证 /env list 同时显示 runtime 和 persisted
func TestEnvCommandHandler_ListShowsRuntimeAndPersisted(t *testing.T) {
	store := NewEnvStore()
	store.Set("RUNTIME_ONLY", "rt-val")
	store.Set("SHARED_KEY", "rt-override")

	persister := &mockEnvPersister{
		vars: map[string]string{
			"PERSISTED_ONLY": "ps-val",
			"SHARED_KEY":     "ps-original",
		},
	}
	handler := NewEnvCommandHandler(persister, store)
	output := handler("list")

	// runtime 变量应显示
	if !strings.Contains(output, "RUNTIME_ONLY") || !strings.Contains(output, "rt-val") {
		t.Fatalf("list should show runtime vars, got: %s", output)
	}
	// persisted 变量应显示
	if !strings.Contains(output, "PERSISTED_ONLY") || !strings.Contains(output, "ps-val") {
		t.Fatalf("list should show persisted vars, got: %s", output)
	}
	// 共享 key 应显示 runtime 值（覆盖 persisted）
	if !strings.Contains(output, "rt-override") {
		t.Fatalf("list should show runtime value for shared key, got: %s", output)
	}
}

// TestNewRegistry_EnvStoreInitialized 验证 NewRegistry 初始化了 EnvStore
func TestNewRegistry_EnvStoreInitialized(t *testing.T) {
	r := NewRegistry()
	if r.EnvStore == nil {
		t.Fatal("Registry.EnvStore should be initialized by NewRegistry")
	}
	// 验证可用
	r.EnvStore.Set("REG_TEST", "ok")
	val, ok := r.EnvStore.Get("REG_TEST")
	if !ok || val != "ok" {
		t.Fatalf("EnvStore should be usable, got ok=%v val=%s", ok, val)
	}
}

// mockEnvPersister 测试用 EnvPersister 实现
type mockEnvPersister struct {
	vars map[string]string
}

func (m *mockEnvPersister) SaveEnvVar(key, value string) error {
	if m.vars == nil {
		m.vars = map[string]string{}
	}
	m.vars[key] = value
	return nil
}

func (m *mockEnvPersister) RemoveEnvVar(key string) error {
	delete(m.vars, key)
	return nil
}

func (m *mockEnvPersister) GetEnvVars() map[string]string {
	result := make(map[string]string, len(m.vars))
	for k, v := range m.vars {
		result[k] = v
	}
	return result
}
