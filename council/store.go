// Package council coordinates Studio councils using persistent Q sessions.
package council

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/internal/sessioncatalog"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

const version = 1
const maximumDocumentSize = 32 << 20
const DefaultRounds = 2
const MaximumRounds = 6

type Scope string

const (
	Independent Scope = "independent"
	Workspace   Scope = "workspace"
	Project     Scope = "project"
)

type Seat struct {
	Model           string `json:"model,omitempty"`
	Agent           string `json:"agent,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// Identity is the display identity; the seat's position identifies its session.
func (s Seat) Identity() string {
	if s.Agent != "" {
		return "acp/" + s.Agent
	}
	return s.Model
}

type Council struct {
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Scope         Scope     `json:"scope"`
	WorkspaceRoot string    `json:"workspace_root,omitempty"`
	ProjectID     string    `json:"project_id,omitempty"`
	Members       []Seat    `json:"members"`
	Chair         Seat      `json:"chair"`
	Rounds        int       `json:"rounds"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Response struct {
	Label string `json:"label"`
	Model string `json:"model,omitempty"`
	Agent string `json:"agent,omitempty"`
	Text  string `json:"text,omitempty"`
	Error string `json:"error,omitempty"`
}

type Review struct {
	Model         string   `json:"model,omitempty"`
	Agent         string   `json:"agent,omitempty"`
	Text          string   `json:"text,omitempty"`
	Ranking       []string `json:"ranking,omitempty"`
	RevisedAnswer string   `json:"revised_answer,omitempty"`
	Error         string   `json:"error,omitempty"`
}

type RankedAnswer struct {
	Label       string  `json:"label"`
	AverageRank float64 `json:"average_rank"`
	Reviews     int     `json:"reviews"`
}

type Round struct {
	Number    int            `json:"number"`
	Responses []Response     `json:"responses,omitempty"`
	Reviews   []Review       `json:"reviews,omitempty"`
	Ranking   []RankedAnswer `json:"ranking,omitempty"`
}

type Turn struct {
	RequestID      string         `json:"request_id,omitempty"`
	ID             string         `json:"id"`
	MemberSessions []string       `json:"member_sessions,omitempty"`
	ChairSession   string         `json:"chair_session,omitempty"`
	CouncilID      string         `json:"council_id"`
	RerunOf        string         `json:"rerun_of,omitempty"`
	Prompt         string         `json:"prompt"`
	Members        []Seat         `json:"members"`
	Chair          Seat           `json:"chair"`
	TotalRounds    int            `json:"total_rounds,omitempty"`
	CurrentRound   int            `json:"current_round,omitempty"`
	Rounds         []Round        `json:"rounds,omitempty"`
	Status         string         `json:"status"`
	Stage          string         `json:"stage"`
	Responses      []Response     `json:"responses"`
	Reviews        []Review       `json:"reviews"`
	Ranking        []RankedAnswer `json:"ranking,omitempty"`
	Final          string         `json:"final,omitempty"`
	Error          string         `json:"error,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

type Store struct {
	Root    string
	mu      sync.Mutex
	catalog *sessioncatalog.Catalog
}

func NewStore(configDir string) *Store {
	return &Store{Root: configDir, catalog: sessioncatalog.New(configDir)}
}

func ValidID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func Validate(value Council) error {
	if strings.TrimSpace(value.Name) == "" || len(value.Name) > 120 {
		return errors.New("council name must contain 1 to 120 characters")
	}
	switch value.Scope {
	case Independent:
		if value.WorkspaceRoot != "" || value.ProjectID != "" {
			return errors.New("independent council cannot have a workspace or project")
		}
	case Workspace:
		if value.WorkspaceRoot == "" || value.ProjectID != "" || !filepath.IsAbs(value.WorkspaceRoot) {
			return errors.New("workspace council requires an absolute workspace root")
		}
	case Project:
		if !ValidID(value.ProjectID) || value.WorkspaceRoot != "" {
			return errors.New("project council requires a project ID")
		}
	default:
		return errors.New("unknown council scope")
	}
	if len(value.Members) < 2 || len(value.Members) > 8 {
		return errors.New("council requires 2 to 8 participants")
	}
	if rounds := EffectiveRounds(value); rounds < 1 || rounds > MaximumRounds {
		return fmt.Errorf("council rounds must be between 1 and %d", MaximumRounds)
	}
	seen := make(map[string]bool, len(value.Members))
	for _, member := range value.Members {
		if err := validateSeat(member); err != nil {
			return err
		}
		if member.Model != "" && seen[member.Model] {
			return fmt.Errorf("duplicate council member model %q", member.Model)
		}
		seen[member.Model] = true
	}
	return validateSeat(value.Chair)
}

func EffectiveRounds(value Council) int {
	if value.Rounds == 0 {
		return DefaultRounds
	}
	return value.Rounds
}

func validateSeat(seat Seat) error {
	if seat.Agent != "" {
		if seat.Model != "" || seat.ReasoningEffort != "" {
			return errors.New("ACP participant cannot specify a model or reasoning effort")
		}
		if !config.ValidAgentConnectionID(seat.Agent) {
			return errors.New("invalid ACP connection ID")
		}
		return nil
	}
	if seat.Model == "" || seat.Model != strings.TrimSpace(seat.Model) || strings.HasPrefix(seat.Model, "group/") {
		return errors.New("select a concrete model")
	}
	if seat.ReasoningEffort != strings.TrimSpace(seat.ReasoningEffort) {
		return errors.New("reasoning effort has surrounding whitespace")
	}
	return nil
}

// SessionStore resolves the single canonical parent; its contents are authoritative.
func (s *Store) SessionStore(id string) (workspace.Store, error) {
	if !ValidID(id) {
		return workspace.Store{}, errors.New("invalid council ID")
	}
	return s.catalog.Session(id, workspace.Council)
}
func (s *Store) SessionRoot(id string) (string, error) {
	store, err := s.SessionStore(id)
	if err != nil {
		return "", err
	}
	return store.SessionDir(), nil
}
func (s *Store) Lock(id string) (*worklock.Lock, error) {
	if !ValidID(id) {
		return nil, errors.New("invalid council ID")
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	return worklock.AcquireFile(s.Root, filepath.Join("session-operation-locks", id+".lock"), "q council workflow")
}
func (s *Store) Create(value Council) (Council, error) { return s.CreateInRoot("", value) }

// CreateInRoot allows a caller to select a workspace independently of read scope.
// Empty root allocates a private workspace using the ordinary chat convention.
func (s *Store) CreateInRoot(root string, value Council) (Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := Validate(value); err != nil {
		return Council{}, err
	}
	now := time.Now().UTC()
	value.Version, value.CreatedAt, value.UpdatedAt = version, now, now
	value.Rounds = EffectiveRounds(value)
	_, err := s.catalog.Create(root, value.Name, workspace.Council, func(store workspace.Store) error {
		value.ID = store.SessionID
		return writeDocument(filepath.Join(store.SessionDir(), "council", "config.json"), value)
	})
	return value, err
}
func (s *Store) List() ([]Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stores, err := s.catalog.Sessions(workspace.Council)
	if err != nil {
		return nil, err
	}
	values := []Council{}
	for _, store := range stores {
		value, err := s.loadLocked(store.SessionID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].UpdatedAt.After(values[j].UpdatedAt) })
	return values, nil
}
func readCouncil(path string) (Council, error) {
	var value Council
	if err := readDocument(path, &value); err != nil {
		return value, err
	}
	if value.Version != version || !ValidID(value.ID) {
		return Council{}, fmt.Errorf("invalid council at %s", path)
	}
	if err := Validate(value); err != nil {
		return Council{}, err
	}
	value.Rounds = EffectiveRounds(value)
	return value, nil
}
func (s *Store) Load(id string) (Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(id)
}
func (s *Store) loadLocked(id string) (Council, error) {
	store, err := s.SessionStore(id)
	if err != nil {
		return Council{}, err
	}
	value, err := readCouncil(filepath.Join(store.SessionDir(), "council", "config.json"))
	if err != nil {
		return Council{}, err
	}
	if value.ID != id {
		return Council{}, errors.New("invalid council identity")
	}
	return value, nil
}
func (s *Store) Update(value Council) (Council, error) {
	guard, err := s.Lock(value.ID)
	if err != nil {
		return Council{}, err
	}
	defer func() { _ = guard.Close() }()
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.loadLocked(value.ID)
	if err != nil {
		return Council{}, err
	}
	if previous.Scope != value.Scope || previous.WorkspaceRoot != value.WorkspaceRoot || previous.ProjectID != value.ProjectID {
		return Council{}, errors.New("council scope cannot be changed")
	}
	if !value.UpdatedAt.IsZero() && !value.UpdatedAt.Equal(previous.UpdatedAt) {
		return Council{}, errors.New("council changed in another Studio; reload and retry")
	}
	if err := Validate(value); err != nil {
		return Council{}, err
	}
	value.Version, value.CreatedAt, value.UpdatedAt = version, previous.CreatedAt, time.Now().UTC()
	value.Rounds = EffectiveRounds(value)
	store, err := s.SessionStore(value.ID)
	if err != nil {
		return Council{}, err
	}
	if err := writeDocument(filepath.Join(store.SessionDir(), "council", "config.json"), value); err != nil {
		return Council{}, err
	}
	if err := s.projectSession(store, value, nil); err != nil {
		return Council{}, err
	}
	return value, nil
}

// Delete requires the workflow lock; active participants must be stopped first.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog.Delete(id, workspace.Council)
}
func (s *Store) SaveTurn(value Turn) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidID(value.ID) || !ValidID(value.CouncilID) {
		return errors.New("invalid council turn ID")
	}
	c, err := s.loadLocked(value.CouncilID)
	if err != nil {
		return err
	}
	store, err := s.SessionStore(c.ID)
	if err != nil {
		return err
	}
	value.UpdatedAt = time.Now().UTC()
	if err := writeDocument(filepath.Join(store.SessionDir(), "council", "turns", value.ID+".json"), value); err != nil {
		return err
	}
	turns, err := s.listTurnsLocked(c.ID)
	if err != nil {
		return err
	}
	return s.projectSession(store, c, turns)
}
func (s *Store) projectSession(store workspace.Store, c Council, turns []Turn) error {
	guard, err := store.AcquireLock("q council projection")
	if err != nil {
		return err
	}
	defer func() { _ = guard.Close() }()
	parent, err := store.Load()
	if err != nil {
		return err
	}
	parent.Title = c.Name
	now := time.Now().UTC()
	parent.UpdatedAt = &now
	if turns != nil {
		parent.Transcript = nil
		for _, turn := range turns {
			parent.Transcript = append(parent.Transcript, client.Message{Role: client.RoleUser, Content: turn.Prompt})
			if turn.Final != "" {
				parent.Transcript = append(parent.Transcript, client.Message{Role: client.RoleAssistant, Content: turn.Final})
			}
		}
	}
	return store.Save(parent)
}
func (s *Store) ListTurns(id string) ([]Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listTurnsLocked(id)
}
func (s *Store) listTurnsLocked(id string) ([]Turn, error) {
	store, err := s.SessionStore(id)
	if err != nil {
		return nil, err
	}
	directory := filepath.Join(store.SessionDir(), "council", "turns")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Turn{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []Turn{}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !entry.Type().IsRegular() || !ValidID(name) || entry.Name() != name+".json" {
			continue
		}
		var turn Turn
		if err := readDocument(filepath.Join(directory, entry.Name()), &turn); err != nil {
			return nil, err
		}
		if turn.ID != name || turn.CouncilID != id {
			return nil, errors.New("invalid council turn identity")
		}
		result = append(result, turn)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}
func (s *Store) Close() error { return nil }

func readDocument(path string, target any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maximumDocumentSize {
		return errors.New("council document is too large")
	}
	decoder := json.NewDecoder(io.LimitReader(file, maximumDocumentSize+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("council document has trailing content")
	}
	return nil
}

func writeDocument(path string, value any) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(body) > maximumDocumentSize {
		return errors.New("council document is too large")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".council-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err := file.Chmod(0o600); err != nil {
		return errors.Join(err, file.Close())
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	return fsreplace.Replace(file.Name(), path)
}
