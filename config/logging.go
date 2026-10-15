package config

import (
	"errors"
	"log/slog"
	"math"
	"strings"
	"time"
)

const (
	DefaultLogFileMiB          = 25
	DefaultLogTotalBytes int64 = 1_000_000_000
	DefaultLogAgeDays          = 7
)

// LoggingConfig controls Studio diagnostics only. Pointer booleans distinguish
// defaults from explicit disabling in existing configuration files.
type LoggingConfig struct {
	Level         string `yaml:"level,omitempty" json:"level,omitempty"`
	Stdout        *bool  `yaml:"stdout,omitempty" json:"stdout,omitempty"`
	FileEnabled   *bool  `yaml:"file_enabled,omitempty" json:"file_enabled,omitempty"`
	MaxFileMiB    int64  `yaml:"max_file_mib,omitempty" json:"max_file_mib,omitempty"`
	MaxTotalBytes int64  `yaml:"max_total_bytes,omitempty" json:"max_total_bytes,omitempty"`
	MaxAgeDays    int64  `yaml:"max_age_days,omitempty" json:"max_age_days,omitempty"`
}

// Effective fills omitted settings without treating zero as unlimited retention.
func (c LoggingConfig) Effective() LoggingConfig {
	if c.Level == "" {
		c.Level = "info"
	}
	if c.Stdout == nil {
		value := false
		c.Stdout = &value
	}
	if c.FileEnabled == nil {
		value := true
		c.FileEnabled = &value
	}
	if c.MaxFileMiB == 0 {
		c.MaxFileMiB = DefaultLogFileMiB
	}
	if c.MaxTotalBytes == 0 {
		c.MaxTotalBytes = DefaultLogTotalBytes
	}
	if c.MaxAgeDays == 0 {
		c.MaxAgeDays = DefaultLogAgeDays
	}
	return c
}

func (c LoggingConfig) Validate() error {
	c = c.Effective()
	switch strings.ToLower(c.Level) {
	case "debug", "info", "warn", "error":
	default:
		return errors.New("logging: invalid level")
	}
	if c.MaxFileMiB < 1 || c.MaxFileMiB > math.MaxInt64/(1<<20) || c.MaxTotalBytes < c.MaxFileMiB*(1<<20) {
		return errors.New("logging: total bytes must cover a positive file size")
	}
	if c.MaxAgeDays < 1 || c.MaxAgeDays > math.MaxInt64/int64(24*time.Hour) {
		return errors.New("logging: invalid retention days")
	}
	return nil
}

func (c LoggingConfig) LogLevel() slog.Level {
	switch strings.ToLower(c.Level) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
