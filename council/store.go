// Package council coordinates Studio councils using persistent Q sessions.
package council

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
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
	Root string
	mu   sync.Mutex
	db   *sql.DB
}

func NewStore(configDir string) *Store { return &Store{Root: filepath.Join(configDir, "council")} }

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

func workspaceKey(root string) string {
	path := filepath.Clean(root)
	// Windows workspace paths are case insensitive.
	if filepath.Separator == '\\' {
		path = strings.ToLower(path)
	}
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) path(value Council) string {
	switch value.Scope {
	case Independent:
		return filepath.Join(s.Root, "independent", value.ID)
	case Workspace:
		return filepath.Join(s.Root, "workspaces", workspaceKey(value.WorkspaceRoot), value.ID)
	default:
		return filepath.Join(s.Root, "projects", value.ProjectID, value.ID)
	}
}

// SessionRoot locates a council's durable Q execution state. Independent
// councils live entirely below ~/.q/council/independent/<council-id>.
func (s *Store) SessionRoot(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadLocked(id)
	if err != nil {
		return "", err
	}
	return s.path(value), nil
}

func (s *Store) Create(value Council) (Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := Validate(value); err != nil {
		return Council{}, err
	}
	value.Rounds = EffectiveRounds(value)
	id, err := workspace.NewSessionID()
	if err != nil {
		return Council{}, err
	}
	value.ID, value.Version = id, version
	value.CreatedAt = time.Now().UTC()
	value.UpdatedAt = value.CreatedAt
	db, err := s.openIndexLocked()
	if err != nil {
		return Council{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Council{}, err
	}
	defer func() { _ = tx.Rollback() }()
	path := filepath.Join(s.path(value), "council.json")
	body, err := json.Marshal(value)
	if err != nil {
		return Council{}, err
	}
	if _, err := tx.Exec(`INSERT INTO councils(id, path, updated_at, document) VALUES(?, ?, ?, ?)`, value.ID, path, value.UpdatedAt.UnixNano(), body); err != nil {
		return Council{}, err
	}
	if err := writeDocument(path, value); err != nil {
		return Council{}, err
	}
	if err := tx.Commit(); err != nil {
		return Council{}, err
	}
	return value, nil
}

func (s *Store) List() ([]Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked()
}

func (s *Store) listLocked() (result []Council, returnErr error) {
	db, err := s.openIndexLocked()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT document FROM councils ORDER BY updated_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, rows.Close()) }()
	for rows.Next() {
		var body []byte
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var value Council
		if err := json.Unmarshal(body, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) listDiskLocked() ([]Council, error) {
	var result []Council
	for _, scope := range []string{"independent", "workspaces", "projects"} {
		base := filepath.Join(s.Root, scope)
		outer, err := os.ReadDir(base)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range outer {
			if !entry.IsDir() {
				continue
			}
			if scope == "independent" {
				if !ValidID(entry.Name()) {
					continue
				}
				value, err := readCouncil(filepath.Join(base, entry.Name(), "council.json"))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				result = append(result, value)
				continue
			}
			inner, err := os.ReadDir(filepath.Join(base, entry.Name()))
			if err != nil {
				return nil, err
			}
			for _, child := range inner {
				if !child.IsDir() || !ValidID(child.Name()) {
					continue
				}
				value, err := readCouncil(filepath.Join(base, entry.Name(), child.Name(), "council.json"))
				if errors.Is(err, os.ErrNotExist) {
					continue
				}
				if err != nil {
					return nil, err
				}
				result = append(result, value)
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

func readCouncil(path string) (Council, error) {
	var value Council
	if err := readDocument(path, &value); err != nil {
		return Council{}, err
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
	if !ValidID(id) {
		return Council{}, errors.New("invalid council ID")
	}
	path, err := s.indexPathLocked(id)
	if err != nil {
		return Council{}, err
	}
	value, err := readCouncil(path)
	if err != nil {
		return Council{}, err
	}
	if value.ID != id || filepath.Join(s.path(value), "council.json") != path {
		return Council{}, errors.New("invalid council index path")
	}
	return value, nil
}

func (s *Store) Update(value Council) (Council, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := s.loadLocked(value.ID)
	if err != nil {
		return Council{}, err
	}
	if value.Scope != previous.Scope || value.WorkspaceRoot != previous.WorkspaceRoot || value.ProjectID != previous.ProjectID {
		return Council{}, errors.New("council scope cannot be changed")
	}
	if err := Validate(value); err != nil {
		return Council{}, err
	}
	value.Rounds = EffectiveRounds(value)
	value.Version, value.CreatedAt, value.UpdatedAt = version, previous.CreatedAt, time.Now().UTC()
	db, err := s.openIndexLocked()
	if err != nil {
		return Council{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Council{}, err
	}
	defer func() { _ = tx.Rollback() }()
	path := filepath.Join(s.path(value), "council.json")
	body, err := json.Marshal(value)
	if err != nil {
		return Council{}, err
	}
	result, err := tx.Exec(`UPDATE councils SET updated_at = ?, document = ? WHERE id = ? AND updated_at = ?`, value.UpdatedAt.UnixNano(), body, value.ID, previous.UpdatedAt.UnixNano())
	if err != nil {
		return Council{}, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return Council{}, errors.New("council changed in another Studio; reload and retry")
	}
	if err := writeDocument(path, value); err != nil {
		return Council{}, err
	}
	if err := tx.Commit(); err != nil {
		return Council{}, err
	}
	return value, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, err := s.loadLocked(id)
	if err != nil {
		return err
	}
	db, err := s.openIndexLocked()
	if err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM councils WHERE id = ?`, id); err != nil {
		return err
	}
	if err := os.RemoveAll(s.path(value)); err != nil {
		return err
	}
	return tx.Commit()
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
	value.UpdatedAt = time.Now().UTC()
	return writeDocument(filepath.Join(s.path(c), "turns", value.ID+".json"), value)
}

func (s *Store) ListTurns(id string) ([]Turn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.loadLocked(id)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.path(c), "turns"))
	if errors.Is(err, os.ErrNotExist) {
		return []Turn{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]Turn, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !entry.Type().IsRegular() || !ValidID(name) || entry.Name() != name+".json" {
			continue
		}
		var turn Turn
		if err := readDocument(filepath.Join(s.path(c), "turns", entry.Name()), &turn); err != nil {
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
