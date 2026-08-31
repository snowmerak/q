package thinker

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
)

const (
	InvocationLogVersion = 1
	LogRetention         = 72 * time.Hour
)

// InvocationTrace records the model's tool choice without persisting raw
// provider responses or hidden reasoning content.
type InvocationTrace struct {
	Round         int       `json:"round"`
	At            time.Time `json:"at"`
	Tool          string    `json:"tool,omitempty"`
	Outcome       string    `json:"outcome"`
	Action        string    `json:"action,omitempty"`
	PropositionID string    `json:"proposition_id,omitempty"`
	Error         string    `json:"error,omitempty"`
}

// InvocationLog is one completed Thinker execution. InputMessages contains the
// filtered and size-bounded context that was actually encoded for extraction.
type InvocationLog struct {
	Version            int               `json:"version"`
	JobID              string            `json:"job_id"`
	Boundary           string            `json:"boundary,omitempty"`
	StartedAt          time.Time         `json:"started_at"`
	CompletedAt        time.Time         `json:"completed_at"`
	DurationMS         int64             `json:"duration_ms"`
	WorkingDirectory   string            `json:"working_directory,omitempty"`
	Refs               []string          `json:"refs,omitempty"`
	Model              string            `json:"model,omitempty"`
	ModelGroup         string            `json:"model_group,omitempty"`
	ReasoningEffort    string            `json:"reasoning_effort,omitempty"`
	ContextLength      int64             `json:"context_length,omitempty"`
	SourceMessages     int               `json:"source_messages,omitempty"`
	InputMessages      []client.Message  `json:"input_messages,omitempty"`
	InputTokens        int               `json:"input_tokens,omitempty"`
	MaximumInputTokens int               `json:"maximum_input_tokens,omitempty"`
	Truncated          bool              `json:"truncated,omitempty"`
	Trace              []InvocationTrace `json:"trace,omitempty"`
	Result             Result            `json:"result"`
	Error              string            `json:"error,omitempty"`
}

// LogStore writes short-lived Thinker diagnostics below the personal q
// configuration directory. One JSON file is used per invocation so concurrent
// workspaces do not contend on a shared append-only file.
type LogStore struct {
	Dir string
	now func() time.Time
}

func NewLogStore(configDir string) *LogStore {
	return &LogStore{Dir: configDir, now: time.Now}
}

func (s *LogStore) Directory() string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.Dir, "logs", "thinker")
}

func (s *LogStore) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

// Write stores one invocation and removes Thinker logs older than three days.
// A file exactly three days old is retained until it crosses the boundary.
func (s *LogStore) Write(value InvocationLog) (string, error) {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return "", errors.New("thinker: log config directory is unavailable")
	}
	directory := s.Directory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("thinker: create log directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("thinker: secure log directory: %w", err)
	}
	now := s.currentTime()
	pruneErr := s.prune(directory, now)

	value.Version = InvocationLogVersion
	value.StartedAt = value.StartedAt.UTC()
	value.CompletedAt = value.CompletedAt.UTC()
	if value.CompletedAt.IsZero() {
		value.CompletedAt = now
	}
	if value.StartedAt.IsZero() {
		value.StartedAt = value.CompletedAt
	}
	value.DurationMS = max(0, value.CompletedAt.Sub(value.StartedAt).Milliseconds())
	value.Refs = append([]string(nil), value.Refs...)
	value.InputMessages = append([]client.Message(nil), value.InputMessages...)
	value.Trace = append([]InvocationTrace(nil), value.Trace...)
	value.Result.LogPath = ""
	value.Result.LogError = ""
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", errors.Join(fmt.Errorf("thinker: encode invocation log: %w", err), pruneErr)
	}
	body = append(body, '\n')

	temporary, err := os.CreateTemp(directory, ".thinker-*.json")
	if err != nil {
		return "", errors.Join(fmt.Errorf("thinker: create invocation log: %w", err), pruneErr)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", errors.Join(fmt.Errorf("thinker: secure invocation log: %w", err), pruneErr)
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return "", errors.Join(fmt.Errorf("thinker: write invocation log: %w", err), pruneErr)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", errors.Join(fmt.Errorf("thinker: sync invocation log: %w", err), pruneErr)
	}
	if err := temporary.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("thinker: close invocation log: %w", err), pruneErr)
	}
	id, err := logID()
	if err != nil {
		return "", errors.Join(err, pruneErr)
	}
	target := filepath.Join(directory, "thinker-"+value.CompletedAt.Format("20060102T150405.000000000Z")+"-"+id+".json")
	if err := os.Rename(temporaryPath, target); err != nil {
		return "", errors.Join(fmt.Errorf("thinker: publish invocation log: %w", err), pruneErr)
	}
	keep = true
	return target, pruneErr
}

func (s *LogStore) prune(directory string, now time.Time) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("thinker: read log directory: %w", err)
	}
	cutoff := now.Add(-LogRetention)
	var pruneErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 ||
			!strings.HasPrefix(name, "thinker-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("thinker: inspect old log %s: %w", name, err))
			continue
		}
		if !info.Mode().IsRegular() || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(directory, name)); err != nil {
			pruneErrors = append(pruneErrors, fmt.Errorf("thinker: remove old log %s: %w", name, err))
		}
	}
	return errors.Join(pruneErrors...)
}

func logID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("thinker: create log ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
