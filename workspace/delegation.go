package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/internal/fsreplace"
)

const delegationVersion = 1

// DelegationBookmark identifies one call by its position in the parent
// transcript. Call IDs alone are not unique across model turns.
type DelegationBookmark struct {
	InvocationID string    `json:"invocation_id"`
	CallIndex    int       `json:"call_index"`
	ToolIndex    int       `json:"tool_index"`
	CallID       string    `json:"call_id"`
	Agent        string    `json:"agent"`
	Prompt       string    `json:"prompt"`
	RunID        string    `json:"run_id"`
	CreatedAt    time.Time `json:"created_at"`
}

type delegationBookmarks struct {
	Version int                  `json:"version"`
	Items   []DelegationBookmark `json:"items"`
}

// DelegationState is the execution marker stored beside one child session.
// Its Result is saved before the result is appended to the parent transcript.
type DelegationState struct {
	Version        int                  `json:"version"`
	Agent          string               `json:"agent"`
	Prompt         string               `json:"prompt"`
	RunID          string               `json:"run_id"`
	TaskID         string               `json:"task_id,omitempty"`
	ParentID       string               `json:"parent_id,omitempty"`
	Model          string               `json:"model,omitempty"`
	APIMode        string               `json:"api_mode,omitempty"`
	Candidate      int                  `json:"candidate,omitempty"`
	ConversationID string               `json:"conversation_id,omitempty"`
	Round          int                  `json:"round,omitempty"`
	Reminders      int                  `json:"reminders,omitempty"`
	Started        bool                 `json:"started,omitempty"`
	Status         string               `json:"status"`
	RunningCall    *DelegationToolCall  `json:"running_call,omitempty"`
	UnknownTools   []DelegationToolCall `json:"unknown_tools,omitempty"`
	Result         *client.ToolResult   `json:"result,omitempty"`
}

type DelegationToolCall struct {
	MessageIndex int    `json:"message_index"`
	ToolIndex    int    `json:"tool_index"`
	CallID       string `json:"call_id"`
	Name         string `json:"name"`
}

func (s Store) DelegationsPath() string { return filepath.Join(s.SessionDir(), "delegations.json") }
func (s Store) DelegationStatePath() string {
	return filepath.Join(s.SessionDir(), "delegation-state.json")
}

// ChildStore constructs a child beneath this exact session, never in the
// top-level session list. Existing symlinks along the path are rejected.
func (s Store) ChildStore(invocationID string) (Store, error) {
	if err := validateSessionID(invocationID); err != nil {
		return Store{}, fmt.Errorf("workspace: invalid invocation ID: %w", err)
	}
	path := filepath.Join(s.SessionDir(), "delegates", invocationID)
	if err := rejectSymlinks(s.Root, path); err != nil {
		return Store{}, err
	}
	return Store{Root: s.Root, SessionID: invocationID, nestedDir: path}, nil
}

func (s Store) LoadDelegations() ([]DelegationBookmark, error) {
	var saved delegationBookmarks
	if err := rejectSymlinks(s.Root, s.DelegationsPath()); err != nil {
		return nil, err
	}
	if err := readDelegationJSON(s.DelegationsPath(), &saved); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if saved.Version != delegationVersion {
		return nil, fmt.Errorf("workspace: unsupported delegation bookmark version %d", saved.Version)
	}
	seen := make(map[[2]int]bool, len(saved.Items))
	seenIDs := make(map[string]bool, len(saved.Items))
	for _, item := range saved.Items {
		if err := validateSessionID(item.InvocationID); err != nil {
			return nil, err
		}
		if item.CallIndex < 0 || item.ToolIndex < 0 || item.Agent == "" || item.Prompt == "" {
			return nil, errors.New("workspace: invalid delegation bookmark")
		}
		key := [2]int{item.CallIndex, item.ToolIndex}
		if seen[key] {
			return nil, errors.New("workspace: duplicate delegation bookmark")
		}
		if seenIDs[item.InvocationID] {
			return nil, errors.New("workspace: reused delegation invocation ID")
		}
		seen[key] = true
		seenIDs[item.InvocationID] = true
	}
	return saved.Items, nil
}

// AddDelegation is idempotent for the same parent call position. A conflicting
// write is rejected rather than silently allocating a second child.
func (s Store) AddDelegation(item DelegationBookmark) (DelegationBookmark, error) {
	if err := validateSessionID(item.InvocationID); err != nil {
		return DelegationBookmark{}, err
	}
	if item.CallIndex < 0 || item.ToolIndex < 0 || item.Agent == "" || item.Prompt == "" {
		return DelegationBookmark{}, errors.New("workspace: invalid delegation bookmark")
	}
	items, err := s.LoadDelegations()
	if err != nil {
		return DelegationBookmark{}, err
	}
	for _, existing := range items {
		if existing.CallIndex == item.CallIndex && existing.ToolIndex == item.ToolIndex {
			if existing.CallID != item.CallID || existing.Agent != item.Agent || existing.Prompt != item.Prompt || existing.RunID != item.RunID {
				return DelegationBookmark{}, errors.New("workspace: conflicting delegation bookmark")
			}
			return existing, nil
		}
		if existing.InvocationID == item.InvocationID {
			return DelegationBookmark{}, errors.New("workspace: reused delegation invocation ID")
		}
	}
	items = append(items, item)
	if err := writeDelegationJSON(s.Root, s.DelegationsPath(), delegationBookmarks{Version: delegationVersion, Items: items}); err != nil {
		return DelegationBookmark{}, err
	}
	return item, nil
}

func (s Store) LoadDelegationState() (DelegationState, error) {
	var state DelegationState
	if err := rejectSymlinks(s.Root, s.DelegationStatePath()); err != nil {
		return DelegationState{}, err
	}
	if err := readDelegationJSON(s.DelegationStatePath(), &state); err != nil {
		return DelegationState{}, err
	}
	if state.Version != delegationVersion {
		return DelegationState{}, errors.New("workspace: invalid delegation state version")
	}
	if err := validateDelegationState(state); err != nil {
		return DelegationState{}, err
	}
	return state, nil
}

func validateDelegationState(state DelegationState) error {
	if state.Agent == "" || state.Prompt == "" || state.Round < 0 || state.Reminders < 0 {
		return errors.New("workspace: invalid delegation state")
	}
	switch state.Status {
	case "running":
		if state.Result != nil {
			return errors.New("workspace: running delegation has a final result")
		}
	case "completed", "unknown", "blocked":
		if state.Result == nil {
			return errors.New("workspace: final delegation result is missing")
		}
	default:
		return errors.New("workspace: invalid delegation status")
	}
	return nil
}

func (s Store) SaveDelegationState(state DelegationState) error {
	state.Version = delegationVersion
	if err := validateDelegationState(state); err != nil {
		return err
	}
	return writeDelegationJSON(s.Root, s.DelegationStatePath(), state)
}

func readDelegationJSON(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maximumSize {
		return errors.New("workspace: delegation file exceeds maximum size")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumSize))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("workspace: trailing delegation data")
	}
	return nil
}

func writeDelegationJSON(root, path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(body) > maximumSize {
		return errors.New("workspace: delegation file exceeds maximum size")
	}
	body = append(body, '\n')
	return withLoomRootMutation(root, func() error {
		if err := rejectSymlinks(root, path); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		file, err := os.CreateTemp(filepath.Dir(path), ".delegation-*.json")
		if err != nil {
			return err
		}
		temporary := file.Name()
		defer os.Remove(temporary)
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return err
		}
		if _, err := file.Write(body); err != nil {
			file.Close()
			return err
		}
		if err := file.Sync(); err != nil {
			file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		return fsreplace.Replace(temporary, path)
	})
}

func rejectSymlinks(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("workspace: delegation path escapes workspace")
	}
	path := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "." || part == "" {
			continue
		}
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace: delegation path contains symlink %s", path)
		}
	}
	return nil
}

// ClearDelegations removes this session's child tree after validating every
// resolved path. It is called only as part of explicit session deletion.
func (s Store) ClearDelegations() error {
	children := filepath.Join(s.SessionDir(), "delegates")
	if err := rejectSymlinks(s.Root, children); err != nil {
		return err
	}
	if _, err := os.Lstat(children); err == nil {
		parent, err := filepath.Abs(s.SessionDir())
		if err != nil {
			return err
		}
		err = filepath.WalkDir(children, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(parent, absolute)
			if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return errors.New("workspace: delegation deletion path escapes session")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return errors.New("workspace: delegation tree contains symlink")
			}
			return nil
		})
		if err != nil {
			return err
		}
		if err := os.RemoveAll(children); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(s.DelegationsPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
