package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/google/uuid"
)

// NewID generates a random UUID string.
func NewID() string {
	return uuid.NewString()
}

// NowMS returns the current UTC Unix timestamp in milliseconds.
func NowMS() int64 {
	return time.Now().UTC().UnixMilli()
}

// HashInput computes a deterministic SHA-256 hex digest over parts separated by
// NUL bytes so boundary ambiguity cannot produce equal digests.
func HashInput(parts ...string) string {
	h := sha256.New()
	for i, part := range parts {
		if i > 0 {
			_, _ = h.Write([]byte{0})
		}
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// HashBytes computes a lowercase hex SHA-256 digest of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// TrimNonEmpty validates and trims a required string identifier or name.
func TrimNonEmpty(v string) (string, bool) {
	trimmed := strings.TrimSpace(v)
	return trimmed, trimmed != ""
}
