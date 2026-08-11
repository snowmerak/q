// Package gatewayconfig stores q's standalone Gateway listener and client
// authentication settings. Provider routing remains in providers.json.
package gatewayconfig

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"
)

const (
	CurrentVersion = 1
	DefaultHost    = "127.0.0.1"
	DefaultPort    = 0
	HashPrefix     = "blake3-keyed-v1:"
)

type Config struct {
	Version int          `json:"version"`
	Server  ServerConfig `json:"server"`
	APIKeys []APIKey     `json:"api_keys,omitempty"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type APIKey struct {
	ID        string     `json:"id"`
	Alias     string     `json:"alias"`
	Hash      string     `json:"hash"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

func Default() Config {
	return Config{
		Version: CurrentVersion,
		Server:  ServerConfig{Host: DefaultHost, Port: DefaultPort},
	}
}

func (c Config) Effective() Config {
	result := c
	if result.Version == 0 {
		result.Version = CurrentVersion
	}
	if result.Server.Host == "" {
		result.Server.Host = DefaultHost
	}
	return result
}

func (c Config) Validate() error {
	c = c.Effective()
	if c.Version != CurrentVersion {
		return fmt.Errorf("gatewayconfig: unsupported version %d", c.Version)
	}
	if net.ParseIP(c.Server.Host) == nil {
		return fmt.Errorf("gatewayconfig: host %q is not an IP address", c.Server.Host)
	}
	if c.Server.Port < 0 || c.Server.Port > 65535 {
		return errors.New("gatewayconfig: port must be between 0 and 65535")
	}
	ids := make(map[string]struct{}, len(c.APIKeys))
	aliases := make(map[string]struct{}, len(c.APIKeys))
	for _, key := range c.APIKeys {
		if key.ID == "" {
			return errors.New("gatewayconfig: API key ID is required")
		}
		if _, duplicate := ids[key.ID]; duplicate {
			return fmt.Errorf("gatewayconfig: duplicate API key ID %q", key.ID)
		}
		ids[key.ID] = struct{}{}
		if err := ValidateAlias(key.Alias); err != nil {
			return err
		}
		foldedAlias := strings.ToLower(key.Alias)
		if _, duplicate := aliases[foldedAlias]; duplicate {
			return fmt.Errorf("gatewayconfig: duplicate API key alias %q", key.Alias)
		}
		aliases[foldedAlias] = struct{}{}
		if key.CreatedAt.IsZero() {
			return fmt.Errorf("gatewayconfig: API key %q has no creation time", key.Alias)
		}
		if !strings.HasPrefix(key.Hash, HashPrefix) {
			return fmt.Errorf("gatewayconfig: API key %q has an unsupported hash", key.Alias)
		}
		digest, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(key.Hash, HashPrefix))
		if err != nil || len(digest) != 32 {
			return fmt.Errorf("gatewayconfig: API key %q has an invalid hash", key.Alias)
		}
	}
	return nil
}

func ValidateAlias(alias string) error {
	if alias == "" || alias != strings.TrimSpace(alias) {
		return errors.New("gatewayconfig: API key alias is required without surrounding whitespace")
	}
	if len([]rune(alias)) > 64 {
		return errors.New("gatewayconfig: API key alias must be at most 64 characters")
	}
	if strings.ContainsFunc(alias, unicode.IsControl) {
		return errors.New("gatewayconfig: API key alias must not contain control characters")
	}
	return nil
}

func (c Config) ActiveKeyCount() int {
	count := 0
	for _, key := range c.APIKeys {
		if key.RevokedAt == nil {
			count++
		}
	}
	return count
}
