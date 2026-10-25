// Package composer owns idea-development conversations independently of workspace sessions.
package composer

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/snowmerak/q/app"
	"github.com/snowmerak/q/internal/fsopen"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/worklock"
	"github.com/snowmerak/q/workspace"
)

const maximumDocumentSize = 32 << 20

var ErrInvalidID = errors.New("invalid composer ID")
var ErrInvalidInput = errors.New("a name of up to 120 characters and a model are required")

type Turn struct {
	ID           string        `json:"id"`
	Prompt       string        `json:"prompt"`
	Answer       string        `json:"answer,omitempty"`
	Status       string        `json:"status"`
	Error        string        `json:"error,omitempty"`
	CreatedAt    time.Time     `json:"created_at"`
	Interactions []Interaction `json:"interactions,omitempty"`
}

type Interaction struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type Conversation struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Model     string    `json:"model"`
	SessionID string    `json:"session_id"`
	Turns     []Turn    `json:"turns"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Question  *Question `json:"question,omitempty"`
}

type Question struct {
	CallID   string                    `json:"call_id"`
	Question string                    `json:"question"`
	Context  string                    `json:"context,omitempty"`
	Choices  []app.AgentQuestionChoice `json:"choices,omitempty"`
}

type Store struct{ Root string }

func NewStore(configDir string) *Store { return &Store{Root: filepath.Join(configDir, "composer")} }

func ValidID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}

// Lock serializes mutations across Studio instances. The caller holds it for
// the entire model turn so another instance cannot overwrite accepted work.
func (s *Store) Lock(id string) (*worklock.Lock, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	return worklock.AcquireFile(s.Root, filepath.Join("locks", id+".lock"), "q studio composer")
}

func (s *Store) SessionStore(c Conversation) workspace.Store {
	return workspace.Store{Root: filepath.Join(s.Root, c.ID), SessionID: c.SessionID}
}

func (s *Store) Create(name, model string) (Conversation, error) {
	name, model = strings.TrimSpace(name), strings.TrimSpace(model)
	if name == "" || utf8.RuneCountInString(name) > 120 || model == "" || len(model) > 512 {
		return Conversation{}, ErrInvalidInput
	}
	now := time.Now().UTC()
	c := Conversation{Version: 1, ID: uuid.NewString(), Name: name, Model: model, Turns: []Turn{}, CreatedAt: now, UpdatedAt: now}
	root := s.SessionStore(c).Root
	if err := os.MkdirAll(root, 0o700); err != nil {
		return c, err
	}
	session, lock, err := workspace.CreateSession(root, "q studio composer")
	if lock != nil {
		err = errors.Join(err, lock.Close())
	}
	if err != nil {
		return c, err
	}
	c.SessionID = session.SessionID
	return c, s.Save(c)
}

func (s *Store) Load(id string) (Conversation, error) {
	if !ValidID(id) {
		return Conversation{}, ErrInvalidID
	}
	file, err := fsopen.Open(filepath.Join(s.Root, id, "composer.json"))
	if err != nil {
		return Conversation{}, err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, maximumDocumentSize+1))
	if err != nil {
		return Conversation{}, err
	}
	if len(body) > maximumDocumentSize {
		return Conversation{}, errors.New("composer history exceeds size limit")
	}
	var c Conversation
	if err := json.Unmarshal(body, &c); err != nil {
		return c, err
	}
	if c.Version != 1 || c.ID != id || !ValidID(c.SessionID) {
		return c, errors.New("invalid composer document")
	}
	return c, nil
}

// Save requires the conversation lock, except for a newly allocated UUID.
func (s *Store) Save(c Conversation) error {
	if !ValidID(c.ID) || !ValidID(c.SessionID) {
		return ErrInvalidID
	}
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(body) > maximumDocumentSize {
		return errors.New("composer history exceeds size limit")
	}
	return fsreplace.WriteFile(filepath.Join(s.Root, c.ID, "composer.json"), body, 0o600)
}

func (s *Store) List() ([]Conversation, error) {
	entries, err := os.ReadDir(s.Root)
	result := []Conversation{}
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !ValidID(entry.Name()) {
			continue
		}
		c, err := s.Load(entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		// The list needs metadata only; full history belongs to the detail API.
		c.Turns = nil
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

// Delete removes only a validated, Composer-owned directory. Hold Lock first.
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	return os.RemoveAll(filepath.Join(s.Root, id))
}
