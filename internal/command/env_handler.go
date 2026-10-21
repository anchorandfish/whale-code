package command

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvPersister is the interface for persisting environment variable config
type EnvPersister interface {
	// SaveEnvVar saves an environment variable to persistent config
	SaveEnvVar(key, value string) error
	// RemoveEnvVar removes an environment variable from persistent config
	RemoveEnvVar(key string) error
	// GetEnvVars gets all persisted environment variables
	GetEnvVars() map[string]string
}

// NewEnvCommandHandler 创建 /env 命令处理器
// CMD-P1-004: 新增 envStore 参数用于进程内环境变量隔离，废除 os.Setenv/os.Unsetenv
// 参数:
//   - persister: 持久化接口，nil 时跳过持久化
//   - envStore: 进程内 EnvStore，nil 时内部创建一个临时实例（仅本次 handler 闭包有效）
//
// envStore 用于运行时隔离，persister 用于重启后恢复，两者独立工作
func NewEnvCommandHandler(persister EnvPersister, envStore *EnvStore) func(args string) string {
	// nil fallback：避免 handler 执行时 panic
	if envStore == nil {
		envStore = NewEnvStore()
	}
	return func(args string) string {
		args = strings.TrimSpace(args)
		if args == "" || args == "list" {
			return formatEnvList(persister, envStore)
		}

		// /env unset KEY
		if strings.HasPrefix(args, "unset ") {
			key := strings.TrimSpace(strings.TrimPrefix(args, "unset "))
			return handleEnvUnset(persister, envStore, key)
		}

		// /env KEY=VALUE
		if strings.Contains(args, "=") {
			parts := strings.SplitN(args, "=", 2)
			return handleEnvSet(persister, envStore, parts[0], parts[1])
		}

		// /env KEY
		return handleEnvGet(persister, envStore, args)
	}
}

// formatEnvList 格式化环境变量列表，合并显示 EnvStore 与持久化变量
// CMD-P1-004: 改用 EnvStore.Snapshot 显示运行时变量，并标注来源
func formatEnvList(persister EnvPersister, envStore *EnvStore) string {
	var sb strings.Builder
	sb.WriteString("Whale Code environment variables:\n")

	// 收集 EnvStore 中的变量（进程内运行时设置）
	runtimeVars := envStore.List()
	// 收集持久化变量（whale-code custom vars）
	persistedVars := map[string]string{}
	if persister != nil {
		persistedVars = persister.GetEnvVars()
	}

	// 合并所有 key（EnvStore 优先显示，但持久化变量也要列出）
	allKeys := make(map[string]struct{})
	for k := range runtimeVars {
		allKeys[k] = struct{}{}
	}
	for k := range persistedVars {
		allKeys[k] = struct{}{}
	}

	if len(allKeys) == 0 {
		sb.WriteString("  (no custom environment variables)\n")
	} else {
		// 按 key 排序输出，保证可读性
		keys := make([]string, 0, len(allKeys))
		for k := range allKeys {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, key := range keys {
			// EnvStore 中的值优先显示（运行时覆盖持久化）
			if val, ok := runtimeVars[key]; ok {
				display := maskValue(key, val)
				sb.WriteString(fmt.Sprintf("  %s = %s [runtime]\n", key, display))
				continue
			}
			if val, ok := persistedVars[key]; ok {
				display := maskValue(key, val)
				sb.WriteString(fmt.Sprintf("  %s = %s [persisted]\n", key, display))
			}
		}
	}

	sysCount := len(os.Environ())
	sb.WriteString(fmt.Sprintf("\nRuntime: %d | Persisted: %d | System environment variables: %d",
		len(runtimeVars), len(persistedVars), sysCount))
	return sb.String()
}

// handleEnvSet 设置环境变量到 EnvStore（进程内），并通过 persister 持久化
// CMD-P1-004: 废除 os.Setenv，改用 envStore.Set，避免污染全局 os.Environ
func handleEnvSet(persister EnvPersister, envStore *EnvStore, key, value string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return "❌ Format error: KEY cannot be empty"
	}

	// CMD-P1-004: 写入进程内 EnvStore，不再调用 os.Setenv
	envStore.Set(key, value)

	// 持久化（重启后恢复）
	if persister != nil {
		if err := persister.SaveEnvVar(key, value); err != nil {
			return fmt.Sprintf("⚠️ Environment variable set but persistence failed: %v", err)
		}
	}

	display := maskValue(key, value)
	return fmt.Sprintf("✅ Set: %s=%s\n  Saved to runtime store and config file", key, display)
}

// handleEnvGet 查询环境变量，fallback 顺序：EnvStore → os.Getenv → persister customVars
// CMD-P1-004: 该顺序不能颠倒，EnvStore 优先级最高（运行时覆盖）
// 注意：用 envStore.List() 查询 EnvStore 内部状态，避免 envStore.Get 的 os.Getenv fallback
// 干扰来源判断（否则系统变量会被误标为 [runtime]）
func handleEnvGet(persister EnvPersister, envStore *EnvStore, key string) string {
	key = strings.TrimSpace(key)

	// 1. 优先查 EnvStore 内部（不触发 os.Getenv fallback），命中标 [runtime]
	runtimeVars := envStore.List()
	if val, ok := runtimeVars[key]; ok {
		display := maskValue(key, val)
		return fmt.Sprintf("%s = %s [runtime]", key, display)
	}

	// 2. fallback 到系统环境变量，命中标 [System]
	if sysVal := os.Getenv(key); sysVal != "" {
		display := maskValue(key, sysVal)
		return fmt.Sprintf("%s = %s [System]", key, display)
	}

	// 3. 最后查 whale-code 持久化变量
	if persister != nil {
		customVars := persister.GetEnvVars()
		if val, ok := customVars[key]; ok {
			display := maskValue(key, val)
			return fmt.Sprintf("%s = %s [persisted]", key, display)
		}
	}

	return fmt.Sprintf("%s = (not set)", key)
}

// handleEnvUnset 从 EnvStore 删除变量，并通过 persister 持久化删除
// CMD-P1-004: 废除 os.Unsetenv，改用 envStore.Unset，避免影响全局 os.Environ
func handleEnvUnset(persister EnvPersister, envStore *EnvStore, key string) string {
	key = strings.TrimSpace(key)

	// CMD-P1-004: 从进程内 EnvStore 删除，不再调用 os.Unsetenv
	envStore.Unset(key)

	// 持久化删除（重启后也不恢复）
	if persister != nil {
		customVars := persister.GetEnvVars()
		if _, ok := customVars[key]; ok {
			if err := persister.RemoveEnvVar(key); err != nil {
				return fmt.Sprintf("⚠️ Environment variable deleted but persistence failed: %v", err)
			}
			return fmt.Sprintf("✅ Deleted: %s", key)
		}
	}

	return fmt.Sprintf("ℹ️ %s is not set, no need to delete", key)
}

// maskValue masks sensitive variable values
// for short values (<=10 chars), fully masks to avoid partial exposure
// for longer values, only shows first 4 chars to minimize leak risk
func maskValue(key, value string) string {
	if isSensitiveKey(key) {
		if len(value) <= 10 {
			return "***"
		}
		return value[:4] + "***"
	}
	return value
}

// isSensitiveKey checks if the key name is sensitive
func isSensitiveKey(key string) bool {
	upper := strings.ToUpper(key)
	return strings.Contains(upper, "API_KEY") ||
		strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "PASSWORD")
}

// ConfigEnvPersister is a Config-based environment variable persister
type ConfigEnvPersister struct {
	// getter retrieves the WhaleEnv map
	getter func() map[string]string
	// saver is the callback to save config
	saver func() error
}

// NewConfigEnvPersister creates a Config-based environment variable persister
func NewConfigEnvPersister(getter func() map[string]string, saver func() error) *ConfigEnvPersister {
	return &ConfigEnvPersister{getter: getter, saver: saver}
}

// SaveEnvVar saves an environment variable
// modifies the config's WhaleEnv map in place (consistent with GetEnvVars which returns a copy)
func (p *ConfigEnvPersister) SaveEnvVar(key, value string) error {
	env := p.getter()
	if env == nil {
		return fmt.Errorf("env map is nil")
	}
	env[key] = value
	if p.saver != nil {
		return p.saver()
	}
	return nil
}

// RemoveEnvVar removes an environment variable
// modifies the config's WhaleEnv map in place (consistent with GetEnvVars which returns a copy)
func (p *ConfigEnvPersister) RemoveEnvVar(key string) error {
	env := p.getter()
	if env == nil {
		return fmt.Errorf("env map is nil")
	}
	delete(env, key)
	if p.saver != nil {
		return p.saver()
	}
	return nil
}

// GetEnvVars gets all environment variables
func (p *ConfigEnvPersister) GetEnvVars() map[string]string {
	env := p.getter()
	if env == nil {
		return make(map[string]string)
	}
	result := make(map[string]string)
	for k, v := range env {
		result[k] = v
	}
	return result
}
