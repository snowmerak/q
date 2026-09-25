// Package remoteconfig stores q's standalone Remote listener and client
// authentication settings. Remote credentials are intentionally independent
// from Gateway credentials because the two services grant different powers.
package remoteconfig

import (
	"errors"
	"fmt"
	"net"

	"github.com/snowmerak/q/internal/authkey"
)

const (
	CurrentVersion = 1
	DefaultHost    = "127.0.0.1"
	DefaultPort    = 0
	HashPrefix     = authkey.HashPrefix
)

type Config struct {
	Version        int                  `json:"version"`
	Server         ServerConfig         `json:"server"`
	Authentication AuthenticationConfig `json:"authentication"`
	APIKeys        []APIKey             `json:"api_keys,omitempty"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type AuthenticationConfig struct {
	Enabled bool `json:"enabled"`
}

type APIKey = authkey.Record

func Default() Config {
	return Config{Version: CurrentVersion, Server: ServerConfig{Host: DefaultHost, Port: DefaultPort}}
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
		return fmt.Errorf("remoteconfig: unsupported version %d", c.Version)
	}
	if net.ParseIP(c.Server.Host) == nil {
		return fmt.Errorf("remoteconfig: host %q is not an IP address", c.Server.Host)
	}
	if c.Server.Port < 0 || c.Server.Port > 65535 {
		return errors.New("remoteconfig: port must be between 0 and 65535")
	}
	if err := authkey.ValidateRecords(c.APIKeys, "remoteconfig"); err != nil {
		return err
	}
	if c.Authentication.Enabled && c.ActiveKeyCount() == 0 {
		return errors.New("remoteconfig: authentication requires at least one active API key")
	}
	return nil
}

func ValidateAlias(alias string) error {
	return authkey.ValidateAlias(alias, "remoteconfig")
}

func (c Config) ActiveKeyCount() int {
	return authkey.ActiveCount(c.APIKeys)
}

func (c Config) ServerIsLoopback() bool {
	address := net.ParseIP(c.Effective().Server.Host)
	return address != nil && address.IsLoopback()
}
