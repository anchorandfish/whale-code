package tui

// reassignServices updates the service interface fields to point to the
// current Model's embedded sub-structs. This must be called after any
// operation that copies Model (e.g., Bubble Tea's Update with value receiver)
// to prevent stale pointer issues.
func (m *Model) reassignServices() {
	m.uiService = &m.UIModel
	m.sessionService = &m.SessionModel
	m.aiService = &m.AIModel
	m.toolService = &m.ToolModel
}
