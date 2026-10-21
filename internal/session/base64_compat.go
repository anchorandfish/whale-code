// Package session — Base64Data hash semantics (C7 / S8).
//
// ContentBlock.Base64Data historically stored the raw base64-encoded image
// bytes. After the C7 contract change it stores the SHA-256 hex digest of the
// image bytes (64 chars). This shrinks the JSONL payload dramatically and
// lets the Context module deduplicate identical images by hash.
//
// Migration: when loading a legacy JSONL file, an image block whose
// Base64Data length is > 64 is assumed to be the old format (real base64),
// and is converted in place to its SHA-256 hex digest. A 64-char value is
// ambiguous: it could be a hex digest or exactly 48 bytes of base64 (rare
// but legal). We treat 64-char values as already-hashed (new format) because
// the cost of a false-positive (re-hashing a hash) is a different 64-char
// string that still uniquely identifies the image, while the cost of a
// false-negative (treating a hash as base64 and re-hashing) is the same.
// Either way the field remains a stable unique identifier.
package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// sha256HexOfBase64 decodes s as standard base64 and returns the lowercase
// hex SHA-256 digest of the decoded bytes. If s is not valid base64, the
// digest of the raw string bytes is returned instead — this still produces a
// stable 64-char identifier, so the caller can proceed without erroring.
func sha256HexOfBase64(s string) string {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(decoded) == 0 {
		// Fall back to hashing the raw string so we always return a non-empty
		// 64-char hex digest; this preserves uniqueness even for malformed
		// legacy data.
		sum := sha256.Sum256([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	sum := sha256.Sum256(decoded)
	return hex.EncodeToString(sum[:])
}

// normalizeImageBase64Data applies the legacy→hash conversion to a single
// Base64Data value. Returns the value unchanged if it is empty or already in
// the new (<= 64 char) format.
func normalizeImageBase64Data(v string) string {
	if v == "" {
		return ""
	}
	// A SHA-256 hex digest is exactly 64 chars. Anything longer is legacy
	// base64 and must be converted. (See package doc for the 64-char
	// ambiguity rationale.)
	if len(v) > 64 {
		return sha256HexOfBase64(v)
	}
	return v
}

// normalizeMessagesBase64Data walks msgs and converts any legacy-format
// image Base64Data fields to their SHA-256 hex digest. Called from
// Store.Load / LoadWithReport after JSON parsing so the in-memory session
// always reflects the new hash semantics regardless of file vintage.
//
// The conversion is one-way and idempotent: a second pass is a no-op because
// hashed values are <= 64 chars. Persisting the converted messages back to
// JSONL (via Save / AppendBatch) gradually migrates files on disk.
func normalizeMessagesBase64Data(msgs []Message) {
	for i := range msgs {
		for j := range msgs[i].Blocks {
			b := &msgs[i].Blocks[j]
			if b.Type == "image" && b.Base64Data != "" {
				b.Base64Data = normalizeImageBase64Data(b.Base64Data)
			}
		}
	}
}
