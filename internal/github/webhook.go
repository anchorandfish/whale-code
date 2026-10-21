package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Event is a repository event
type Event struct {
	Type     string `json:"type"`      // pull_request / push / issues
	Action   string `json:"action"`    // opened / synchronized / closed etc
	PRNumber int    `json:"pr_number"` // PR number for PR events
	Sender   string `json:"sender"`    // Event trigger sender
}

// EventHandler is the event handler callback function type
type EventHandler func(event Event)

// ghEventResponse is the GitHub API event response structure
type ghEventResponse struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Actor struct {
		Login string `json:"login"`
	} `json:"actor"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt string          `json:"created_at"`
}

// ghPREventPayload is the PR event payload
type ghPREventPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`
}

// ghIssuesEventPayload is the Issues event payload
type ghIssuesEventPayload struct {
	Action string `json:"action"`
	Number int    `json:"number"`
}

// ghPushEventPayload is the Push event payload
type ghPushEventPayload struct {
	Ref string `json:"ref"`
}

// Watcher polls GitHub repository events
// - Checks for new events every 60 seconds
// - Supports PR/Push/Issues events
// - Notifies via callback when events are detected
type Watcher struct {
	client   *Client
	owner    string
	repo     string
	interval time.Duration
	cancel   context.CancelFunc
	mu       sync.Mutex
	running  bool
}

// NewWatcher creates an event poller
func NewWatcher(client *Client, owner, repo string, interval time.Duration) *Watcher {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return &Watcher{
		client:   client,
		owner:    owner,
		repo:     repo,
		interval: interval,
	}
}

// Start starts event polling
func (w *Watcher) Start(ctx context.Context, handler EventHandler) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("poller is already running")
	}
	w.running = true
	w.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	w.cancel = cancel

	// Record last check time, initially current time
	since := time.Now()

	go func() {
		defer func() {
			w.mu.Lock()
			w.running = false
			w.mu.Unlock()
		}()

		ticker := time.NewTicker(w.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				events, err := w.pollEvents(ctx, since)
				if err != nil {
					// B5-P1-10: 记录轮询错误到日志，避免完全静默导致问题无法诊断
					log.Printf("[github-watcher] poll events failed: %v", err)
					continue
				}

				// Update check timestamp
				if len(events) > 0 {
					since = time.Now()
				}

				// Notify each event
				for _, event := range events {
					handler(event)
				}
			}
		}
	}()

	return nil
}

// Stop stops event polling
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.cancel != nil {
		w.cancel()
		w.cancel = nil
	}
	w.running = false
}

// pollEvents polls new events after the specified time
func (w *Watcher) pollEvents(ctx context.Context, since time.Time) ([]Event, error) {
	path := fmt.Sprintf("/repos/%s/%s/events?per_page=30", w.owner, w.repo)
	resp, err := w.client.doRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to poll events: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to poll events (HTTP %d): %s", resp.StatusCode, string(body))
	}

	var ghEvents []ghEventResponse
	if err := json.NewDecoder(resp.Body).Decode(&ghEvents); err != nil {
		return nil, fmt.Errorf("failed to parse event response: %w", err)
	}

	var events []Event
	for _, ghEvent := range ghEvents {
		// Parse event creation time
		createdAt, err := time.Parse(time.RFC3339, ghEvent.CreatedAt)
		if err != nil {
			continue
		}

		// Only process events after since
		if !createdAt.After(since) {
			continue
		}

		event, ok := w.parseEvent(ghEvent)
		if ok {
			events = append(events, event)
		}
	}

	return events, nil
}

// parseEvent parses GitHub event into internal Event structure
func (w *Watcher) parseEvent(ghEvent ghEventResponse) (Event, bool) {
	event := Event{
		Sender: ghEvent.Actor.Login,
	}

	switch ghEvent.Type {
	case "PullRequestEvent":
		var payload ghPREventPayload
		if err := json.Unmarshal(ghEvent.Payload, &payload); err != nil {
			return Event{}, false
		}
		event.Type = "pull_request"
		event.Action = payload.Action
		event.PRNumber = payload.Number
		return event, true

	case "IssuesEvent":
		var payload ghIssuesEventPayload
		if err := json.Unmarshal(ghEvent.Payload, &payload); err != nil {
			return Event{}, false
		}
		event.Type = "issues"
		event.Action = payload.Action
		event.PRNumber = payload.Number
		return event, true

	case "PushEvent":
		var payload ghPushEventPayload
		if err := json.Unmarshal(ghEvent.Payload, &payload); err != nil {
			return Event{}, false
		}
		event.Type = "push"
		event.Action = "push"
		return event, true

	default:
		return Event{}, false
	}
}

// VerifyWebhookSignature verifies GitHub Webhook signature using HMAC-SHA256
func VerifyWebhookSignature(payload []byte, signature string, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	return hmac.Equal(sigBytes, expected)
}

// maxWebhookBodySize is the maximum allowed webhook request body size (1MB)
const maxWebhookBodySize = 1 << 20

// HandleWebhook creates an HTTP handler for GitHub Webhook events with signature verification
func HandleWebhook(secret string, handler EventHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		contentType := r.Header.Get("Content-Type")
		if contentType != "application/json" && !strings.HasPrefix(contentType, "application/json;") {
			http.Error(w, "invalid Content-Type, expected application/json", http.StatusUnsupportedMediaType)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodySize)
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		sig := r.Header.Get("X-Hub-Signature-256")
		if !VerifyWebhookSignature(payload, sig, secret) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}

		// Parse the webhook payload into GitHub's actual event structure,
		// then convert to the internal Event type
		event := parseWebhookPayload(r.Header.Get("X-GitHub-Event"), payload)
		handler(event)
		w.WriteHeader(http.StatusOK)
	}
}

// parseWebhookPayload parses the raw webhook payload into the internal Event type.
// GitHub webhooks use X-GitHub-Event header for the event type and a nested payload
// structure that differs from our simplified Event struct.
func parseWebhookPayload(eventType string, payload []byte) Event {
	event := Event{Type: eventType}

	switch eventType {
	case "pull_request":
		var body struct {
			Action      string `json:"action"`
			Number      int    `json:"number"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(payload, &body); err == nil {
			event.Action = body.Action
			event.PRNumber = body.PullRequest.Number
			if event.PRNumber == 0 {
				event.PRNumber = body.Number
			}
			event.Sender = body.Sender.Login
		}

	case "issues":
		var body struct {
			Action string `json:"action"`
			Issue  struct {
				Number int `json:"number"`
			} `json:"issue"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(payload, &body); err == nil {
			event.Action = body.Action
			event.PRNumber = body.Issue.Number
			event.Sender = body.Sender.Login
		}

	case "push":
		var body struct {
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(payload, &body); err == nil {
			event.Action = "push"
			event.Sender = body.Sender.Login
		}

	// Handle pull_request_review, pull_request_review_comment, etc.
	case "pull_request_review", "pull_request_review_comment":
		var body struct {
			Action      string `json:"action"`
			PullRequest struct {
				Number int `json:"number"`
			} `json:"pull_request"`
			Sender struct {
				Login string `json:"login"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(payload, &body); err == nil {
			event.Action = body.Action
			event.PRNumber = body.PullRequest.Number
			event.Sender = body.Sender.Login
		}
	}

	return event
}
