// Package command — env_store.go 提供进程内环境变量隔离存储 EnvStore
// 替代 os.Setenv/os.Unsetenv，避免污染全局 os.Environ 影响 AI client 的 API Key 等敏感配置
package command

import (
	"os"
	"strings"
	"sync"
)

// EnvStore 是进程内环境变量存储，不污染全局 os.Environ
// 通过 sync.RWMutex 保护并发读写，适用于 TUI/Plain/Bridge 三模式共享
// 不持久化：仅进程内有效；持久化仍走 EnvPersister.SaveEnvVar
type EnvStore struct {
	mu   sync.RWMutex
	vars map[string]string
}

// NewEnvStore 创建一个空的 EnvStore
func NewEnvStore() *EnvStore {
	return &EnvStore{vars: make(map[string]string)}
}

// Set 设置一个环境变量到进程内存储，不调用 os.Setenv
// 参数:
//   - key: 变量名
//   - value: 变量值
func (s *EnvStore) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vars[key] = value
}

// Get 读取环境变量，fallback 顺序：EnvStore.vars → os.Getenv（只读，不写入）
// 该顺序不能颠倒：EnvStore 的值优先级高于系统环境变量
// 参数:
//   - key: 变量名
//
// 返回:
//   - value: 变量值
//   - ok: 是否存在 (EnvStore 或 os.Environ 任一命中即 true)
func (s *EnvStore) Get(key string) (value string, ok bool) {
	s.mu.RLock()
	v, hit := s.vars[key]
	s.mu.RUnlock()
	if hit {
		return v, true
	}
	// fallback 到系统环境变量 (只读, 不写入)
	if sysVal := os.Getenv(key); sysVal != "" {
		return sysVal, true
	}
	return "", false
}

// Unset 从 EnvStore 中删除变量，不调用 os.Unsetenv
// 注意：仅删除 EnvStore 中的值，不影响系统环境变量
// 参数:
//   - key: 变量名
func (s *EnvStore) Unset(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.vars, key)
}

// List 返回 EnvStore 中所有变量的副本（不影响内部 map）
// 返回:
//   - map[string]string: 变量名到值的映射副本
func (s *EnvStore) List() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]string, len(s.vars))
	for k, v := range s.vars {
		result[k] = v
	}
	return result
}

// Snapshot 返回 EnvStore + os.Environ 合并后的快照，用于 /env list 显示
// EnvStore 中的值覆盖同名的系统环境变量
// 返回:
//   - map[string]string: 合并后的变量映射
func (s *EnvStore) Snapshot() map[string]string {
	result := make(map[string]string)
	// 先填入系统环境变量
	for _, env := range os.Environ() {
		// 仅取第一个 '=' 之前作为 key，之后作为 value
		if idx := strings.IndexByte(env, '='); idx >= 0 {
			result[env[:idx]] = env[idx+1:]
		}
	}
	// EnvStore 覆盖
	s.mu.RLock()
	for k, v := range s.vars {
		result[k] = v
	}
	s.mu.RUnlock()
	return result
}
