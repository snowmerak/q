// Package providerenv keeps refreshed provider credentials in memory.
//
// A long-running Studio inherits its environment once, at startup. Reloading
// replaces the values of the variables that provider settings reference,
// without changing the process environment and without writing values to
// disk. Lookups prefer a refreshed value and fall back to the process
// environment.
package providerenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	commandTimeout   = 30 * time.Second
	maximumOutput    = 1 << 20
	maximumErrorText = 512
)

// Settings describes how to obtain a fresh environment.
type Settings struct {
	// Shell runs Command with "-c". It defaults to $SHELL, then /bin/sh.
	Shell string
	// Command prints a JSON object that maps variable names to string values.
	// When it is empty, Windows reads a fresh user and system environment
	// block, and other systems report ErrNotConfigured.
	Command string
}

// ErrNotConfigured reports that no environment source is available.
var ErrNotConfigured = errors.New("providerenv: no environment reload command is configured")

// Source names where reloaded values came from.
type Source string

const (
	SourceCommand          Source = "command"
	SourceEnvironmentBlock Source = "environment_block"
)

// Result describes a reload without exposing any value.
type Result struct {
	Source Source `json:"source"`
	// Loaded lists the requested names that the source provided.
	Loaded []string `json:"loaded"`
	// Missing lists the requested names that the source did not provide.
	// They keep resolving from the process environment.
	Missing []string `json:"missing"`
}

var (
	mu      sync.RWMutex
	values  map[string]string
	changed = make(chan struct{})
)

// Lookup returns the refreshed value of name, or the process environment's
// value when name has not been refreshed.
func Lookup(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	mu.RLock()
	value, ok := lookupLocked(name)
	mu.RUnlock()
	if ok {
		return value, true
	}
	return os.LookupEnv(name)
}

// Getenv is Lookup without the presence flag, matching os.Getenv.
func Getenv(name string) string {
	value, _ := Lookup(name)
	return value
}

func lookupLocked(name string) (string, bool) {
	if value, ok := values[name]; ok {
		return value, true
	}
	if runtime.GOOS == "windows" {
		for key, value := range values {
			if strings.EqualFold(key, name) {
				return value, true
			}
		}
	}
	return "", false
}

// Values returns a copy of the refreshed values.
func Values() map[string]string {
	mu.RLock()
	defer mu.RUnlock()
	return maps.Clone(values)
}

// Changed returns a channel that is closed by the next Notify.
func Changed() <-chan struct{} {
	mu.RLock()
	defer mu.RUnlock()
	return changed
}

// Notify tells consumers waiting on Changed to rebuild their provider
// clients.
func Notify() {
	mu.Lock()
	close(changed)
	changed = make(chan struct{})
	mu.Unlock()
}

// Replace installs refreshed values and returns the previous ones, so that a
// caller can restore them when applying the new values fails. It does not
// notify consumers.
func Replace(next map[string]string) map[string]string {
	mu.Lock()
	defer mu.Unlock()
	previous := values
	values = maps.Clone(next)
	return previous
}

// Load reads a fresh environment and keeps only the requested names.
func Load(ctx context.Context, settings Settings, names []string) (map[string]string, Result, error) {
	var environment map[string]string
	var source Source
	var err error
	switch {
	case strings.TrimSpace(settings.Command) != "":
		source = SourceCommand
		environment, err = runCommand(ctx, settings)
	case runtime.GOOS == "windows":
		source = SourceEnvironmentBlock
		environment, err = userEnvironment()
	default:
		return nil, Result{}, ErrNotConfigured
	}
	if err != nil {
		return nil, Result{}, err
	}
	selected := make(map[string]string)
	result := Result{Source: source, Loaded: []string{}, Missing: []string{}}
	for _, name := range uniqueNames(names) {
		value, ok := environment[name]
		if !ok && runtime.GOOS == "windows" {
			for key, candidate := range environment {
				if strings.EqualFold(key, name) {
					value, ok = candidate, true
					break
				}
			}
		}
		if !ok {
			result.Missing = append(result.Missing, name)
			continue
		}
		selected[name] = value
		result.Loaded = append(result.Loaded, name)
	}
	return selected, result, nil
}

func uniqueNames(names []string) []string {
	result := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && !slices.Contains(result, name) {
			result = append(result, name)
		}
	}
	slices.Sort(result)
	return result
}

func runCommand(ctx context.Context, settings Settings) (map[string]string, error) {
	shell := strings.TrimSpace(settings.Shell)
	if shell == "" {
		shell = os.Getenv("SHELL")
	}
	if shell == "" {
		if runtime.GOOS == "windows" {
			return nil, errors.New("providerenv: set a shell to run the environment reload command on Windows")
		}
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, shell, "-c", settings.Command)
	command.Stdin = nil
	stdout := &limitedBuffer{limit: maximumOutput}
	stderr := &limitedBuffer{limit: maximumErrorText}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		// stderr may echo a value, so report only the exit status.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("providerenv: environment reload command timed out after %s", commandTimeout)
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("providerenv: environment reload command exited with status %d", exit.ExitCode())
		}
		return nil, fmt.Errorf("providerenv: run environment reload command: %w", err)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("providerenv: environment reload command printed more than %d bytes", maximumOutput)
	}
	return parseJSON(stdout.Bytes())
}

// parseJSON decodes a flat object of string values. Errors never include
// the output, which holds credentials.
func parseJSON(body []byte) (map[string]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(body), &raw); err != nil || raw == nil {
		return nil, errors.New("providerenv: environment reload command must print a JSON object of string values")
	}
	result := make(map[string]string, len(raw))
	for name, encoded := range raw {
		var value string
		if err := json.Unmarshal(encoded, &value); err != nil {
			return nil, fmt.Errorf("providerenv: environment variable %q is not a string", name)
		}
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, 0) {
			return nil, fmt.Errorf("providerenv: environment variable %q is invalid", name)
		}
		result[name] = value
	}
	return result, nil
}

type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if len(p) > remaining {
		b.overflow = true
		if remaining > 0 {
			_, _ = b.Buffer.Write(p[:remaining])
		}
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

// Environment returns base with the refreshed values applied, for starting a
// child process.
func Environment(base []string) []string {
	refreshed := Values()
	if len(refreshed) == 0 {
		return base
	}
	result := make([]string, 0, len(base)+len(refreshed))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		if _, ok := refreshed[name]; ok {
			continue
		}
		if runtime.GOOS == "windows" && containsFold(refreshed, name) {
			continue
		}
		result = append(result, entry)
	}
	for _, name := range slices.Sorted(maps.Keys(refreshed)) {
		result = append(result, name+"="+refreshed[name])
	}
	return result
}

func containsFold(values map[string]string, name string) bool {
	for key := range values {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
