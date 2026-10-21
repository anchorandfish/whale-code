package session

import (
	"fmt"
	"sort"
	"time"
)

// SplitResult represents session split result
type SplitResult struct {
	Sessions []*Session
	Names    []string
}

// SplitAt splits a session at message indices.
// Each resulting part gets independent metadata with correct timestamps.
//
// UUID chain (S6): each part inherits the UUIDs of the messages it contains.
// The chain tail (lastUUID) is re-derived for each part so subsequent
// AddMessage calls link correctly. ParentUUID links are preserved as-is so
// provenance back to the original session is not lost.
func SplitAt(s *Session, indices []int) *SplitResult {
	if len(indices) == 0 {
		return &SplitResult{
			Sessions: []*Session{s},
			Names:    []string{s.Metadata.Name},
		}
	}

	result := &SplitResult{}
	start := 0

	for i, idx := range indices {
		if idx <= start || idx >= len(s.messages) {
			continue
		}

		partMsgs := copyMessages(s.messages[start:idx])
		part := &Session{
			Version: s.Version,
			Metadata: SessionMetadata{
				Name:            fmt.Sprintf("%s-part%d", s.Metadata.Name, i+1),
				CreatedAt:       s.Metadata.CreatedAt,
				UpdatedAt:       lastTimestamp(partMsgs),
				TurnCount:       countUserMessages(partMsgs),
				CompactionCount: s.Metadata.CompactionCount,
			},
			messages: partMsgs,
			lastUUID: deriveLastUUID(partMsgs),
		}
		result.Sessions = append(result.Sessions, part)
		result.Names = append(result.Names, part.Metadata.Name)
		start = idx
	}

	if start < len(s.messages) {
		partMsgs := copyMessages(s.messages[start:])
		part := &Session{
			Version: s.Version,
			Metadata: SessionMetadata{
				Name:            fmt.Sprintf("%s-part%d", s.Metadata.Name, len(indices)+1),
				CreatedAt:       s.Metadata.CreatedAt,
				UpdatedAt:       lastTimestamp(partMsgs),
				TurnCount:       countUserMessages(partMsgs),
				CompactionCount: s.Metadata.CompactionCount,
			},
			messages: partMsgs,
			lastUUID: deriveLastUUID(partMsgs),
		}
		result.Sessions = append(result.Sessions, part)
		result.Names = append(result.Names, part.Metadata.Name)
	}

	return result
}

// Merge merges multiple sessions.
// Messages are sorted by timestamp (stable sort preserves insertion order for ties).
// After merge, timestamps are adjusted to ensure monotonic ordering.
//
// UUID chain (S6): merged messages keep their original UUIDs. The chain tail
// (lastUUID) is re-derived from the merged message list so subsequent
// AddMessage calls link to the last surviving message. ParentUUID links from
// the source sessions are preserved (they may point across session
// boundaries, which is acceptable — the chain is a provenance hint, not a
// strict tree, and Compact walks it defensively).
func Merge(sessions []*Session, name string) *Session {
	merged := NewSession()
	merged.Metadata.Name = name

	type indexedMessage struct {
		msg       Message
		order     int
		sessionID int
	}

	var allMsgs []indexedMessage
	order := 0
	for sessionIdx, s := range sessions {
		merged.Metadata.TurnCount += s.Metadata.TurnCount
		merged.Metadata.CompactionCount += s.Metadata.CompactionCount
		for _, m := range s.messages {
			allMsgs = append(allMsgs, indexedMessage{msg: m, order: order, sessionID: sessionIdx})
			order++
		}
	}

	sort.SliceStable(allMsgs, func(i, j int) bool {
		if allMsgs[i].msg.Timestamp != allMsgs[j].msg.Timestamp {
			return allMsgs[i].msg.Timestamp < allMsgs[j].msg.Timestamp
		}
		return allMsgs[i].order < allMsgs[j].order
	})

	// Ensure timestamps are monotonically increasing.
	// If two messages have the same timestamp, increment the later one by 1ms.
	for i := 1; i < len(allMsgs); i++ {
		if allMsgs[i].msg.Timestamp <= allMsgs[i-1].msg.Timestamp {
			allMsgs[i].msg.Timestamp = allMsgs[i-1].msg.Timestamp + 1
		}
	}

	for _, im := range allMsgs {
		merged.messages = append(merged.messages, im.msg)
	}
	// Re-derive the chain tail for the merged session.
	merged.lastUUID = deriveLastUUID(merged.messages)

	if len(merged.messages) > 0 {
		merged.Metadata.CreatedAt = merged.messages[0].Timestamp
		merged.Metadata.UpdatedAt = merged.messages[len(merged.messages)-1].Timestamp
	}

	return merged
}

// countUserMessages counts user messages
func countUserMessages(messages []Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role == RoleUser {
			count++
		}
	}
	return count
}

// copyMessages creates a deep copy of a message slice to prevent
// shared references between split sessions.
func copyMessages(src []Message) []Message {
	dst := make([]Message, len(src))
	copy(dst, src)
	return dst
}

// lastTimestamp returns the timestamp of the last message, or current time if empty.
func lastTimestamp(msgs []Message) int64 {
	if len(msgs) == 0 {
		return time.Now().UnixMilli()
	}
	return msgs[len(msgs)-1].Timestamp
}
