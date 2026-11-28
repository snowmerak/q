package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

type sessionMetadata struct {
	Version        int           `json:"version"`
	ID             string        `json:"id"`
	Endpoint       string        `json:"endpoint"`
	Chrome         string        `json:"chrome,omitempty"`
	Headless       bool          `json:"headless,omitempty"`
	InputMode      string        `json:"inputMode,omitempty"`
	BrowserPID     int           `json:"browserPid,omitempty"`
	BrowserProfile string        `json:"browserProfile,omitempty"`
	CommandTimeout time.Duration `json:"commandTimeout"`
	CleanupTimeout time.Duration `json:"cleanupTimeout"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
	Instance       string        `json:"instance"`
	PID            int           `json:"pid"`
	State          string        `json:"state"`
	Tasks          []string      `json:"tasks"`
	Pages          []pageView    `json:"pages"`
	Cleanup        any           `json:"cleanup,omitempty"`
}

type sessionStore struct {
	mu       sync.Mutex
	path     string
	metadata sessionMetadata
}
type startResult struct {
	Session  string `json:"session,omitempty"`
	Instance string `json:"instance"`
	PID      int    `json:"pid"`
	State    string `json:"state"`
	Log      string `json:"log,omitempty"`
	Created  bool   `json:"created"`
}

func validSessionID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for i, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (i == 0 || (c != '-' && c != '_')) {
			return false
		}
	}
	// Keep IDs portable, including when metadata is copied to Windows.
	switch id {
	case "con", "prn", "aux", "nul":
		return false
	}
	if len(id) == 4 && (strings.HasPrefix(id, "com") || strings.HasPrefix(id, "lpt")) && id[3] >= '1' && id[3] <= '9' {
		return false
	}
	return true
}

func platformDataRoot(goos, home, local, xdg string) (string, error) {
	switch goos {
	case "windows":
		if filepath.IsAbs(local) {
			return filepath.Join(local, "cdp-q-driver"), nil
		}
		if home != "" {
			return filepath.Join(home, "AppData", "Local", "cdp-q-driver"), nil
		}
	case "darwin":
		if home != "" {
			return filepath.Join(home, "Library", "Application Support", "cdp-q-driver"), nil
		}
	default:
		if filepath.IsAbs(xdg) {
			return filepath.Join(xdg, "cdp-q-driver"), nil
		}
		if home != "" {
			return filepath.Join(home, ".local", "share", "cdp-q-driver"), nil
		}
	}
	return "", errors.New("cannot determine application data directory; supply --data-dir")
}

func configureSession(o *appOptions, seen map[string]bool) error {
	if o.State != "" {
		if o.Chrome != "" || o.headlessSet {
			return invalid("--chrome/--headless require a named session, not --state")
		}
		if seen["session"] || seen["data-dir"] {
			return invalid("--state cannot be combined with --session or --data-dir")
		}
		path, err := filepath.Abs(o.State)
		if err != nil {
			return err
		}
		o.State = path
		return nil
	}
	if !validSessionID(o.Session) {
		return invalid("session ID must be 1..64 lowercase ASCII letters/digits, '-' or '_', starting with a letter/digit; reserved device names are rejected")
	}
	if o.DataDir == "" {
		home, _ := os.UserHomeDir()
		root, err := platformDataRoot(runtime.GOOS, home, os.Getenv("LOCALAPPDATA"), os.Getenv("XDG_DATA_HOME"))
		if err != nil {
			return err
		}
		o.DataDir = root
	}
	root, err := filepath.Abs(o.DataDir)
	if err != nil {
		return err
	}
	o.DataDir = root
	o.Named = true
	directory := filepath.Join(root, "sessions", o.Session)
	o.State = filepath.Join(directory, "runtime.json")
	o.Metadata = filepath.Join(directory, "session.json")
	return nil
}

func readMetadata(path, id string) (sessionMetadata, error) {
	var result sessionMetadata
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(io.LimitReader(f, 2<<20))
	if err := dec.Decode(&result); err != nil {
		return result, fmt.Errorf("invalid session metadata: %w", err)
	}
	if result.Version != protocolVersion || result.ID != id || result.Endpoint == "" || result.CommandTimeout <= 0 || result.CommandTimeout > 24*time.Hour || result.CleanupTimeout <= 0 || result.CleanupTimeout > time.Minute {
		return result, errors.New("unsupported or invalid session metadata")
	}
	return result, nil
}

func resolveServe(o appOptions, requested serveOptions) (serveOptions, error) {
	if requested.InputMode != "" && o.InputMode != "" && requested.InputMode != o.InputMode {
		return requested, invalid("global and start/serve --input-mode values disagree")
	}
	if requested.InputMode == "" {
		requested.InputMode = o.InputMode
	}
	if requested.Chrome != "" && o.Chrome != "" && requested.Chrome != o.Chrome {
		return requested, invalid("global and start/serve --chrome values disagree")
	}
	if requested.headlessSet && o.headlessSet && requested.Headless != o.Headless {
		return requested, invalid("global and start/serve --headless values disagree")
	}
	if requested.Chrome == "" {
		requested.Chrome = o.Chrome
	}
	if o.headlessSet {
		requested.Headless, requested.headlessSet = o.Headless, true
	}
	if requested.Endpoint != "" && o.Endpoint != "" && requested.Endpoint != o.Endpoint {
		return requested, invalid("global and start/serve --endpoint values disagree")
	}
	if requested.Endpoint == "" {
		requested.Endpoint = o.Endpoint
	}
	if requested.Endpoint != "" && requested.Chrome != "" {
		return requested, invalid("choose --chrome PATH or --endpoint URL, not both")
	}
	if o.Named {
		saved, err := readMetadata(o.Metadata, o.Session)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return requested, err
		}
		if err == nil {
			if requested.InputMode == "" {
				requested.InputMode = saved.InputMode
			}
			if requested.Endpoint == "" && requested.Chrome == "" {
				if saved.Chrome != "" {
					requested.Chrome = saved.Chrome
				} else {
					requested.Endpoint = saved.Endpoint
				}
			}
			if requested.Chrome != "" && !requested.headlessSet {
				requested.Headless = saved.Headless
			}
			if !requested.commandSet {
				requested.CommandTimeout = saved.CommandTimeout
			}
			if !requested.cleanupSet {
				requested.CleanupTimeout = saved.CleanupTimeout
			}
		}
	}
	if requested.InputMode == "" {
		requested.InputMode = "paced"
	}
	if requested.InputMode != "paced" && requested.InputMode != "fast" {
		return requested, invalid("--input-mode must be paced or fast")
	}
	if requested.Chrome != "" {
		if !o.Named {
			return requested, invalid("--chrome requires a named session, not --state")
		}
		path, err := exec.LookPath(requested.Chrome)
		if err != nil {
			return requested, invalid("Chrome executable: %v", err)
		}
		requested.Chrome, err = filepath.Abs(path)
		if err != nil {
			return requested, err
		}
	} else if requested.headlessSet && requested.Endpoint != "" {
		return requested, invalid("--headless configures managed Chrome launch and cannot be used with an external endpoint")
	}
	if requested.CommandTimeout == 0 {
		requested.CommandTimeout = 15 * time.Second
	}
	if requested.CleanupTimeout == 0 {
		requested.CleanupTimeout = 5 * time.Second
	}
	return requested, nil
}

func endpointHash(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

func checkSession(o appOptions, d discovery, serve serveOptions) error {
	if serve.InputMode != "" && serve.InputMode != d.InputMode {
		return &commandError{Code: "session_conflict", Message: "session uses different input pacing; stop it before changing --input-mode"}
	}
	if o.Named && d.Session != o.Session {
		return &commandError{Code: "session_conflict", Message: "runtime belongs to another session"}
	}
	if serve.Endpoint != "" && d.Chrome != "" {
		return &commandError{Code: "session_conflict", Message: "session owns a managed Chrome instance; stop it before switching to an external endpoint"}
	}
	if serve.Endpoint != "" && d.EndpointHash != "" && d.EndpointHash != endpointHash(serve.Endpoint) {
		return &commandError{Code: "session_conflict", Message: "session is already attached to a different endpoint; stop it before changing configuration"}
	}
	if serve.Chrome != "" && (d.Chrome != serve.Chrome || d.Headless != serve.Headless) {
		return &commandError{Code: "session_conflict", Message: "session uses different Chrome launch settings; stop it before changing configuration"}
	}
	if serve.headlessSet && (d.Chrome == "" || d.Headless != serve.Headless) {
		return &commandError{Code: "session_conflict", Message: "session uses different Chrome launch settings; stop it before changing configuration"}
	}
	if (serve.commandSet && d.CommandTimeout != serve.CommandTimeout) || (serve.cleanupSet && d.CleanupTimeout != serve.CleanupTimeout) {
		return &commandError{Code: "session_conflict", Message: "running session uses different SDK timeouts; stop it before changing configuration"}
	}
	return nil
}

func awaitSession(ctx context.Context, o appOptions, serve serveOptions) (discovery, error) {
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		d, err := loadDiscovery(o.State)
		if err == nil {
			if err := checkSession(o, d, serve); err != nil {
				return discovery{}, err
			}
			probe, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			reply, callErr := call(probe, d, request{Command: "status", TimeoutMS: 500})
			cancel()
			if callErr == nil && reply.OK {
				var status struct{ Instance, State string }
				data, _ := json.Marshal(reply.Result)
				if json.Unmarshal(data, &status) == nil && status.Instance == d.Instance && status.State == "running" {
					return d, nil
				}
			}
		} else if errors.Is(err, os.ErrNotExist) {
			return discovery{}, err
		}
		select {
		case <-ctx.Done():
			return discovery{}, &commandError{Code: "session_unavailable", Message: "session did not become ready before the deadline; inspect status and its runtime file before recovery"}
		case <-ticker.C:
		}
	}
}

// Only startup/readiness may be joined or retried. The caller dispatches its
// actual browser mutation exactly once after this returns a ready generation.
func ensureSession(ctx context.Context, o appOptions, requested serveOptions) (discovery, startResult, error) {
	serve, err := resolveServe(o, requested)
	if err != nil {
		return discovery{}, startResult{}, err
	}
	if _, err := os.Lstat(o.State); err == nil {
		d, err := awaitSession(ctx, o, serve)
		return d, startResult{Session: o.Session, Instance: d.Instance, PID: d.PID, State: o.State, Created: false}, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return discovery{}, startResult{}, err
	}
	serve, err = autoChrome(serve)
	if err != nil {
		return discovery{}, startResult{}, err
	}
	started, startErr := startProcess(ctx, o, serve)
	if startErr == nil {
		d, err := loadDiscovery(o.State)
		return d, started, err
	}
	// Another client may have won the exclusive runtime-file reservation.
	if _, err := os.Lstat(o.State); err == nil {
		d, joinErr := awaitSession(ctx, o, serve)
		if joinErr == nil {
			return d, startResult{Session: o.Session, Instance: d.Instance, PID: d.PID, State: o.State, Created: false}, nil
		}
		return discovery{}, startResult{}, errors.Join(startErr, joinErr)
	}
	return discovery{}, startResult{}, startErr
}

func newSessionStore(o appOptions, serve serveOptions, instance string) (*sessionStore, error) {
	now := time.Now().UTC()
	created := now
	if saved, err := readMetadata(o.Metadata, o.Session); err == nil {
		created = saved.CreatedAt
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &sessionStore{path: o.Metadata, metadata: sessionMetadata{Version: protocolVersion, ID: o.Session, Endpoint: serve.Endpoint, Chrome: serve.Chrome, Headless: serve.Headless, InputMode: serve.InputMode, CommandTimeout: serve.CommandTimeout, CleanupTimeout: serve.CleanupTimeout, CreatedAt: created, UpdatedAt: now, Instance: instance, PID: os.Getpid(), State: "running", Tasks: []string{"default"}, Pages: []pageView{}}}, nil
}

func (s *sessionStore) save(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.metadata, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > 2<<20 {
		return errors.New("session metadata exceeded 2 MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".session-*.tmp")
	if err != nil {
		return err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err = os.Rename(path, s.path)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("publish session metadata: %w", errors.Join(err, ctx.Err()))
		case <-time.After(20 * time.Millisecond):
		}
	}
}
func (s *sessionStore) update(tasks []string, pages []pageView) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.metadata.State != "running" {
		return nil
	}
	s.metadata.Tasks = tasks
	s.metadata.Pages = pages
	s.metadata.UpdatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.save(ctx)
}
func (s *sessionStore) finish(cleanup any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metadata.State = "stopped"
	s.metadata.Cleanup = cleanup
	s.metadata.UpdatedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return s.save(ctx)
}

func publicEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "[invalid endpoint]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}
func sessionInfo(ctx context.Context, o appOptions) (map[string]any, error) {
	metadata, err := readMetadata(o.Metadata, o.Session)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": metadata.ID, "endpoint": publicEndpoint(metadata.Endpoint), "state": metadata.State, "running": false, "createdAt": metadata.CreatedAt, "updatedAt": metadata.UpdatedAt, "lastInstance": metadata.Instance, "lastPid": metadata.PID, "lastObservedTasks": metadata.Tasks, "lastObservedPages": metadata.Pages, "metadata": o.Metadata, "runtime": o.State, "lastCleanup": metadata.Cleanup}
	result["inputMode"] = metadata.InputMode
	if metadata.Chrome != "" {
		result["browser"] = map[string]any{"chrome": metadata.Chrome, "headless": metadata.Headless, "profile": metadata.BrowserProfile, "lastPid": metadata.BrowserPID, "managed": true}
	}
	if d, err := loadDiscovery(o.State); err == nil && d.Session == o.Session {
		probe, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		reply, err := call(probe, d, request{Command: "status", TimeoutMS: 300})
		cancel()
		if err == nil && reply.OK {
			result["running"] = true
		}
	}
	return result, nil
}
func listSessions(ctx context.Context, o appOptions) ([]map[string]any, error) {
	root := filepath.Join(o.DataDir, "sessions")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || !validSessionID(entry.Name()) {
			continue
		}
		candidate := o
		candidate.Session = entry.Name()
		candidate.State = filepath.Join(root, entry.Name(), "runtime.json")
		candidate.Metadata = filepath.Join(root, entry.Name(), "session.json")
		info, err := sessionInfo(ctx, candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read session %s: %w", entry.Name(), err)
		}
		result = append(result, info)
	}
	slices.SortFunc(result, func(a, b map[string]any) int { return strings.Compare(a["id"].(string), b["id"].(string)) })
	return result, nil
}
