// Package authkey implements the shared persisted API key primitive used by
// q's independently scoped services.
package authkey

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/zeebo/blake3"
)

const HashPrefix = "blake3-keyed-v1:"

// Policy separates credentials belonging to different services.
type Policy struct {
	Name         string
	SecretPrefix string
	Domain       string
}

// Record is the JSON-compatible persisted form of an API key. It never
// contains the plaintext secret.
type Record struct {
	ID        string     `json:"id"`
	Alias     string     `json:"alias"`
	Hash      string     `json:"hash"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// Generated contains a new persisted record and the plaintext secret that is
// returned to the caller once.
type Generated struct {
	Record Record
	Secret string
}

// Generate creates a service-scoped API key.
func Generate(masterKey [32]byte, alias string, now time.Time, policy Policy) (Generated, error) {
	alias = strings.TrimSpace(alias)
	if err := ValidateAlias(alias, policy.Name); err != nil {
		return Generated{}, err
	}
	idBytes := make([]byte, 12)
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		return Generated{}, fmt.Errorf("%s: generate API key ID: %w", policy.Name, err)
	}
	if _, err := rand.Read(secretBytes); err != nil {
		return Generated{}, fmt.Errorf("%s: generate API key secret: %w", policy.Name, err)
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	secretPart := base64.RawURLEncoding.EncodeToString(secretBytes)
	return Generated{
		Record: Record{
			ID:        id,
			Alias:     alias,
			Hash:      Hash(masterKey, id, secretPart, policy),
			CreatedAt: now.UTC(),
		},
		Secret: policy.SecretPrefix + id + "." + secretPart,
	}, nil
}

// Hash derives the versioned digest stored with an API key record.
func Hash(masterKey [32]byte, id, secret string, policy Policy) string {
	hasher, _ := blake3.NewKeyed(masterKey[:])
	_, _ = hasher.Write([]byte(policy.Domain))
	_, _ = hasher.Write([]byte(id))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(secret))
	return HashPrefix + base64.RawURLEncoding.EncodeToString(hasher.Sum(nil))
}

// Verify checks a plaintext credential against one active record.
func Verify(masterKey [32]byte, record Record, presented string, policy Policy) bool {
	id, secret, ok := Parse(presented, policy)
	if !ok || id != record.ID || record.RevokedAt != nil {
		return false
	}
	want, err := decodeHash(record.Hash)
	if err != nil {
		return false
	}
	got, err := decodeHash(Hash(masterKey, id, secret, policy))
	return err == nil && subtle.ConstantTimeCompare(got, want) == 1
}

// Parse separates the public ID from the secret portion of a credential.
func Parse(value string, policy Policy) (string, string, bool) {
	if !strings.HasPrefix(value, policy.SecretPrefix) {
		return "", "", false
	}
	id, secret, found := strings.Cut(strings.TrimPrefix(value, policy.SecretPrefix), ".")
	return id, secret, found && id != "" && secret != ""
}

// ValidateAlias applies the shared persisted alias contract.
func ValidateAlias(alias, name string) error {
	if alias == "" || alias != strings.TrimSpace(alias) {
		return fmt.Errorf("%s: API key alias is required without surrounding whitespace", name)
	}
	if len([]rune(alias)) > 64 {
		return fmt.Errorf("%s: API key alias must be at most 64 characters", name)
	}
	if strings.ContainsFunc(alias, unicode.IsControl) {
		return fmt.Errorf("%s: API key alias must not contain control characters", name)
	}
	return nil
}

// ValidateRecords checks IDs, aliases, timestamps, and versioned hashes.
func ValidateRecords(records []Record, name string) error {
	ids := make(map[string]struct{}, len(records))
	aliases := make(map[string]struct{}, len(records))
	for _, key := range records {
		if key.ID == "" {
			return fmt.Errorf("%s: API key ID is required", name)
		}
		if _, duplicate := ids[key.ID]; duplicate {
			return fmt.Errorf("%s: duplicate API key ID %q", name, key.ID)
		}
		ids[key.ID] = struct{}{}
		if err := ValidateAlias(key.Alias, name); err != nil {
			return err
		}
		foldedAlias := strings.ToLower(key.Alias)
		if _, duplicate := aliases[foldedAlias]; duplicate {
			return fmt.Errorf("%s: duplicate API key alias %q", name, key.Alias)
		}
		aliases[foldedAlias] = struct{}{}
		if key.CreatedAt.IsZero() {
			return fmt.Errorf("%s: API key %q has no creation time", name, key.Alias)
		}
		if !strings.HasPrefix(key.Hash, HashPrefix) {
			return fmt.Errorf("%s: API key %q has an unsupported hash", name, key.Alias)
		}
		if _, err := decodeHash(key.Hash); err != nil {
			return fmt.Errorf("%s: API key %q has an invalid hash", name, key.Alias)
		}
	}
	return nil
}

// Add appends a record after enforcing case-insensitive alias uniqueness.
func Add(records []Record, generated Generated, name string) ([]Record, error) {
	for _, existing := range records {
		if strings.EqualFold(existing.Alias, generated.Record.Alias) {
			return nil, fmt.Errorf("%s: API key alias %q already exists", name, generated.Record.Alias)
		}
	}
	return append(records, generated.Record), nil
}

// Revoke marks one record as revoked.
func Revoke(records []Record, id string, now time.Time, name string) ([]Record, error) {
	for index := range records {
		if records[index].ID != id {
			continue
		}
		if records[index].RevokedAt != nil {
			return nil, fmt.Errorf("%s: API key is already revoked", name)
		}
		revokedAt := now.UTC()
		records[index].RevokedAt = &revokedAt
		return records, nil
	}
	return nil, fmt.Errorf("%s: API key %q was not found", name, id)
}

// ActiveCount returns the number of records that have not been revoked.
func ActiveCount(records []Record) int {
	count := 0
	for _, key := range records {
		if key.RevokedAt == nil {
			count++
		}
	}
	return count
}

// Active returns active records indexed by their public IDs.
func Active(records []Record) map[string]Record {
	active := make(map[string]Record, ActiveCount(records))
	for _, key := range records {
		if key.RevokedAt == nil {
			active[key.ID] = key
		}
	}
	return active
}

func decodeHash(value string) ([]byte, error) {
	if !strings.HasPrefix(value, HashPrefix) {
		return nil, errors.New("unsupported API key hash")
	}
	digest, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, HashPrefix))
	if err != nil || len(digest) != 32 {
		return nil, errors.New("invalid API key hash")
	}
	return digest, nil
}
