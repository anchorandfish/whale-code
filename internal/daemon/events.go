package daemon

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

// EventType is the event type
type EventType string

const (
	// EventTaskCompleted is a task completed event
	EventTaskCompleted EventType = "task_completed"
	// EventTaskFailed taskfailedevent
	EventTaskFailed EventType = "task_failed"
	// EventTaskStarted is a task started event
	EventTaskStarted EventType = "task_started"
	// EventTaskProgress is a task progress update event
	EventTaskProgress EventType = "task_progress"
	// EventTaskCancelled is emitted when a task is cancelled.
	EventTaskCancelled EventType = "task_cancelled"
	// EventWorkerRegistered workersregisterevent
	EventWorkerRegistered EventType = "worker_registered"
	// EventWorkerUnregistered workersderegisterevent
	EventWorkerUnregistered EventType = "worker_unregistered"
	// EventWorkerStale workersstaleevent
	EventWorkerStale EventType = "worker_stale"
)

// Event event
type Event struct {
	Type      EventType   `json:"type"`
	Payload   interface{} `json:"payload"`
	Timestamp int64       `json:"timestamp"`
}

// EventBus is the event bus with buffered channels and optional persistence.
type EventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]chan Event
	bufferSize  int
	persistPath string // If set, events are appended to this file
	closed      bool
}

// NewEventBus creates event bus with buffered channels (bufferSize=16)
// to prevent slow consumers from blocking publishers.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[EventType][]chan Event),
		bufferSize:  16,
	}
}

// SetPersistPath enables event persistence to the specified file path.
// Events are appended in JSONL format.
func (bus *EventBus) SetPersistPath(path string) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	bus.persistPath = path
}

// Subscribe subscribes to events with a buffered channel
func (bus *EventBus) Subscribe(eventType EventType) <-chan Event {
	bus.mu.Lock()
	defer bus.mu.Unlock()

	ch := make(chan Event, bus.bufferSize)
	if bus.closed {
		close(ch)
		return ch
	}
	bus.subscribers[eventType] = append(bus.subscribers[eventType], ch)
	return ch
}

// Publish publishes events to all subscribers.
// Uses non-blocking sends with drop-oldest strategy to prevent
// slow consumers from blocking the publisher.
// If persistence is enabled, events are also written to the persist file.
func (bus *EventBus) Publish(event Event) {
	bus.publish(event, true)
}

func (bus *EventBus) publish(event Event, persist bool) {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return
	}

	if event.Timestamp == 0 {
		event.Timestamp = time.Now().UnixMilli()
	}

	// Persist event if path is configured
	if persist && bus.persistPath != "" {
		bus.persistEvent(event)
	}

	subs, ok := bus.subscribers[event.Type]
	if !ok {
		return
	}
	for _, ch := range subs {
		select {
		case ch <- event:
		default:
			// buffer full, drop oldest event
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- event:
			default:
			}
		}
	}
}

// persistEvent appends an event to the persistence file in JSONL format.
// Best-effort: errors are silently ignored to avoid blocking the publisher.
func (bus *EventBus) persistEvent(event Event) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}
	data = append(data, '\n')

	f, err := os.OpenFile(bus.persistPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(data)
}

// Replay reads persisted events from file and publishes them.
// This allows recovery of event history after daemon restart.
func (bus *EventBus) Replay(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := splitLines(string(data))
	for _, line := range lines {
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		// Replay must not append the same event back to its source file.
		bus.publish(event, false)
	}
	return nil
}

// Close close event bus and clean up subscribers
func (bus *EventBus) Close() {
	bus.mu.Lock()
	defer bus.mu.Unlock()
	if bus.closed {
		return
	}
	bus.closed = true

	for _, subs := range bus.subscribers {
		for _, ch := range subs {
			close(ch)
		}
	}
	bus.subscribers = make(map[EventType][]chan Event)
	bus.persistPath = ""
}

// splitLines splits a string into lines
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 {
				lines = append(lines, line)
			}
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
