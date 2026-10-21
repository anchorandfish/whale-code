package tools

import (
	"sync"
	"testing"
)

// TestRegistryIsConcurrencySafe verifies that Registry.IsConcurrencySafe correctly
// reports concurrency safety based on the tool spec's ConcurrencySafe field.
func TestRegistryIsConcurrencySafe(t *testing.T) {
	r := NewRegistry()

	// read_file is a builtin tool marked as concurrency-safe
	if !r.IsConcurrencySafe("read_file") {
		t.Error("read_file should be concurrency-safe")
	}

	// bash is a builtin tool NOT marked as concurrency-safe
	if r.IsConcurrencySafe("bash") {
		t.Error("bash should not be concurrency-safe")
	}

	// Non-existent tool should return false
	if r.IsConcurrencySafe("nonexistent_tool") {
		t.Error("nonexistent tool should not be concurrency-safe")
	}
}

// TestExecutorIsConcurrencySafe verifies that Executor.IsConcurrencySafe delegates
// to the executor's registry correctly.
func TestExecutorIsConcurrencySafe(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, "")

	if !e.IsConcurrencySafe("read_file") {
		t.Error("Executor: read_file should be concurrency-safe")
	}
	if e.IsConcurrencySafe("bash") {
		t.Error("Executor: bash should not be concurrency-safe")
	}
}

// TestExecutorIsConcurrencySafeNilRegistry verifies that Executor.IsConcurrencySafe
// returns false when the registry is nil.
func TestExecutorIsConcurrencySafeNilRegistry(t *testing.T) {
	e := &Executor{
		handlers: make(map[string]Handler),
		registry: nil,
	}

	if e.IsConcurrencySafe("read_file") {
		t.Error("Executor with nil registry should return false for any tool")
	}
}

// TestIndependentRegistries verifies that two independent Registry instances
// do not affect each other's IsConcurrencySafe results.
func TestIndependentRegistries(t *testing.T) {
	r1 := NewRegistry()
	r2 := NewRegistry()

	// Register an MCP tool with ConcurrencySafe=true on r1 only
	err := r1.RegisterMCP(Spec{
		Name:            "mcp_safe_tool",
		Description:     "A safe MCP tool",
		ConcurrencySafe: true,
	})
	if err != nil {
		t.Fatalf("RegisterMCP failed: %v", err)
	}

	// r1 should see the tool as concurrency-safe
	if !r1.IsConcurrencySafe("mcp_safe_tool") {
		t.Error("r1: mcp_safe_tool should be concurrency-safe")
	}

	// r2 should NOT see the tool at all, so IsConcurrencySafe returns false
	if r2.IsConcurrencySafe("mcp_safe_tool") {
		t.Error("r2: mcp_safe_tool should not be found in independent registry")
	}
}

// TestConcurrentRegistryAccess verifies that concurrent calls to Registry.IsConcurrencySafe
// and Registry.RegisterMCP do not cause data races (use with -race flag).
func TestConcurrentRegistryAccess(t *testing.T) {
	r := NewRegistry()

	var wg sync.WaitGroup

	// Concurrent readers
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.IsConcurrencySafe("read_file")
			_ = r.IsConcurrencySafe("bash")
			_ = r.IsConcurrencySafe("nonexistent")
		}()
	}

	// Concurrent writers (register MCP tools)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := "concurrent_mcp_" + string(rune('A'+idx))
			_ = r.RegisterMCP(Spec{
				Name:            name,
				Description:     "Concurrent MCP tool",
				ConcurrencySafe: idx%2 == 0,
			})
		}(i)
	}

	// Concurrent readers of newly registered tools
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := "concurrent_mcp_" + string(rune('A'+idx))
			_ = r.IsConcurrencySafe(name)
		}(i)
	}

	wg.Wait()
}

// TestEmptyRegistryIsConcurrencySafe verifies that an empty Registry (without builtin
// tools) returns false for any tool name.
func TestEmptyRegistryIsConcurrencySafe(t *testing.T) {
	r := &Registry{
		builtin: make(map[string]Spec),
		mcp:     make(map[string]Spec),
		plugin:  make(map[string]Spec),
	}

	if r.IsConcurrencySafe("read_file") {
		t.Error("Empty registry should return false for read_file")
	}
	if r.IsConcurrencySafe("bash") {
		t.Error("Empty registry should return false for bash")
	}
	if r.IsConcurrencySafe("anything") {
		t.Error("Empty registry should return false for any tool")
	}
}

// TestEmptyRegistryGet verifies that calling Get on an empty Registry returns
// (Spec{}, false) for any tool name, equivalent to "not found".
func TestEmptyRegistryGet(t *testing.T) {
	r := &Registry{
		builtin: make(map[string]Spec),
		mcp:     make(map[string]Spec),
		plugin:  make(map[string]Spec),
	}

	_, ok := r.Get("read_file")
	if ok {
		t.Error("Empty registry Get should return false for read_file")
	}

	_, ok = r.Get("bash")
	if ok {
		t.Error("Empty registry Get should return false for bash")
	}

	_, ok = r.Get("nonexistent_tool")
	if ok {
		t.Error("Empty registry Get should return false for nonexistent tool")
	}
}

// TestExecutorDI verifies that the Executor correctly uses the injected Registry
// for tool lookups, confirming dependency injection works as expected.
func TestExecutorDI(t *testing.T) {
	r := NewRegistry()
	e := NewExecutor(r, "")

	// Executor should find builtin tools via the injected registry
	spec, ok := e.registry.Get("read_file")
	if !ok {
		t.Fatal("Executor registry should find read_file")
	}
	if spec.Name != "read_file" {
		t.Errorf("Expected tool name read_file, got %s", spec.Name)
	}

	// Executor should find bash
	spec, ok = e.registry.Get("bash")
	if !ok {
		t.Fatal("Executor registry should find bash")
	}
	if spec.Name != "bash" {
		t.Errorf("Expected tool name bash, got %s", spec.Name)
	}

	// Executor should not find nonexistent tools
	_, ok = e.registry.Get("nonexistent_tool")
	if ok {
		t.Error("Executor registry should not find nonexistent tool")
	}
}
