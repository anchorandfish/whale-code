import re

filepath = r'd:\zlj\claw-code-main\whale-code\internal\tui\plain_test.go'
with open(filepath, 'r', encoding='utf-8') as f:
    content = f.read()

# Fix the newTestPlainRunner function - wrap fields in runnerCore
# Replace the struct literal initialization
old = """func newTestPlainRunner() *PlainRunner {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := newTestClient("test-model")
	reg := newTestRegistry()
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	return &PlainRunner{
		Session:       session.NewSession(),
		Config:        &cfg,
		AIClient:      client,
		Registry:      toolRegistry,
		Executor:      tools.NewExecutor(toolRegistry, os.TempDir()),
		Policy:        permission.NewPolicy(permission.LevelWorkspaceWrite),
		Budget:        ctxpkg.DefaultBudget(cfg.ContextWindow),
		Compactor:     ctxpkg.NewCompactor(estimator),
		Estimator:     estimator,
		MagicDocs:     Config.NewMagicDocs(""),
		AutoMemory:    Config.NewAutoMemory(os.TempDir()),
		MemSelector:   Config.NewMemorySelector(client),
		Fallback:      ai.NewFallbackTracker("test-model", ""),
		ModelName:     "test-model",
		ModelRegistry: reg,
		AvailableModels: reg.Names(),
		WorkDir:       os.TempDir(),
	}
}"""

new = """func newTestPlainRunner() *PlainRunner {
	cfg := config.DefaultConfig()
	cfg.Model = "test-model"
	cfg.PermissionMode = config.PermWorkspaceWrite

	client := newTestClient("test-model")
	reg := newTestRegistry()
	estimator := ai.NewTokenEstimator()
	toolRegistry := tools.NewRegistry()

	return &PlainRunner{
		runnerCore: &runnerCore{
			Session:         session.NewSession(),
			Config:          &cfg,
			AIClient:        client,
			Registry:        toolRegistry,
			Executor:        tools.NewExecutor(toolRegistry, os.TempDir()),
			Policy:          permission.NewPolicy(permission.LevelWorkspaceWrite),
			Budget:          ctxpkg.DefaultBudget(cfg.ContextWindow),
			Compactor:       ctxpkg.NewCompactor(estimator),
			Estimator:       estimator,
			MagicDocs:       config.NewMagicDocs(""),
			AutoMemory:      config.NewAutoMemory(os.TempDir()),
			MemSelector:     config.NewMemorySelector(client),
			Fallback:        ai.NewFallbackTracker("test-model", ""),
			ModelName:       "test-model",
			ModelRegistry:   reg,
			AvailableModels: reg.Names(),
			WorkDir:         os.TempDir(),
		},
	}
}"""

if old in content:
    content = content.replace(old, new)
    with open(filepath, 'w', encoding='utf-8') as f:
        f.write(content)
    print('Done')
else:
    print('Pattern not found')
    # Try to find it with slight variations
    idx = content.find('func newTestPlainRunner')
    if idx >= 0:
        print('Found function at index', idx)
        print(content[idx:idx+200])