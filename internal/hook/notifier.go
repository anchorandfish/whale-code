package hook

// RunNotificationHooks 运行通知 hooks
func (m *Manager) RunNotificationHooks(message string) HookResult {
	m.mu.RLock()
	hooks := make([]Hook, len(m.config.Notification))
	copy(hooks, m.config.Notification)
	m.mu.RUnlock()
	return m.runLifecycleHooks(hooks, EventNotification)
}
