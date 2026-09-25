// Package gatewayconfig stores q's standalone Gateway listener and client
// authentication settings. Provider routing remains in providers.json.
package gatewayconfig

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
	Version int          `json:"version"`
	Server  ServerConfig `json:"server"`
	APIKeys []APIKey     `json:"api_keys,omitempty"`
}

type ServerConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

type APIKey = authkey.Record

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
	if err := authkey.ValidateRecords(c.APIKeys, "gatewayconfig"); err != nil {
		return err
	}
	return nil
}

func ValidateAlias(alias string) error {
	return authkey.ValidateAlias(alias, "gatewayconfig")
}

func (c Config) ActiveKeyCount() int {
	return authkey.ActiveCount(c.APIKeys)
}
