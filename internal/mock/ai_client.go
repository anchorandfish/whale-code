// Package mock — test mocks for isolating external dependencies
// ai_client.go — AI 客户端 Mock
package mock

import (
	"context"
	"sync"

	"github.com/anchorandfish/whale-code/internal/ai"
)

// Verify MockAIClient implements ai.Client at compile time
// 编译期断言：ai.Client 接口新增方法时编译报错，避免运行时 nil panic
var _ ai.Client = (*MockAIClient)(nil)

// MockAIClient is a mock implementation of ai.Client for testing
type MockAIClient struct {
	mu        sync.Mutex
	Responses []ai.Event // 预设响应
	Errors    []error    // 预设错误（每次调用一个）
	callCount int
	model     string
}

// NewMockAIClient creates a mock AI client with preset responses
func NewMockAIClient(responses []ai.Event) *MockAIClient {
	return &MockAIClient{
		Responses: responses,
		model:     "mock-model",
	}
}

// Stream returns pre-set responses or errors
func (m *MockAIClient) Stream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for preset error
	if m.callCount < len(m.Errors) && m.Errors[m.callCount] != nil {
		err := m.Errors[m.callCount]
		m.callCount++
		return nil, err
	}

	ch := make(chan ai.Event, len(m.Responses))
	for _, event := range m.Responses {
		ch <- event
	}
	close(ch)
	m.callCount++
	return ch, nil
}

// Model returns the current model name
func (m *MockAIClient) Model() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}

// SetModel sets the model name
func (m *MockAIClient) SetModel(model string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.model = model
}

// Close is a no-op for the mock client
func (m *MockAIClient) Close() error {
	return nil
}

// CallCount returns the number of times Stream was called
func (m *MockAIClient) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// Reset resets the call count
func (m *MockAIClient) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
}

// MockStreamBuilder helps build mock stream responses
type MockStreamBuilder struct {
	events []ai.Event
}

// NewMockStreamBuilder creates a stream builder
func NewMockStreamBuilder() *MockStreamBuilder {
	return &MockStreamBuilder{}
}

// AddText adds a text delta event
func (b *MockStreamBuilder) AddText(content string) *MockStreamBuilder {
	b.events = append(b.events, ai.TextDeltaEvent{Content: content})
	return b
}

// AddToolUse adds a tool use event
func (b *MockStreamBuilder) AddToolUse(id, name, input string) *MockStreamBuilder {
	b.events = append(b.events, ai.ToolUseEvent{ID: id, Name: name, Input: input})
	return b
}

// AddUsage adds a usage event
func (b *MockStreamBuilder) AddUsage(inputTokens, outputTokens int) *MockStreamBuilder {
	b.events = append(b.events, ai.UsageEvent{InputTokens: inputTokens, OutputTokens: outputTokens})
	return b
}

// AddError adds an error event
func (b *MockStreamBuilder) AddError(err error) *MockStreamBuilder {
	b.events = append(b.events, ai.ErrorEvent{Err: err})
	return b
}

// Build returns the built events
func (b *MockStreamBuilder) Build() []ai.Event {
	return b.events
}

// BuildClient returns a MockAIClient with the built events
func (b *MockStreamBuilder) BuildClient() *MockAIClient {
	return NewMockAIClient(b.events)
}
