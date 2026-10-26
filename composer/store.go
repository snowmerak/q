// Package composer implements idea-development workflows backed by typed workspace sessions.
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
	"github.com/snowmerak/q/internal/sessioncatalog"
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
	Version       int       `json:"version"`
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Model         string    `json:"model"`
	SessionID     string    `json:"session_id"`
	WorkspaceRoot string    `json:"workspace_root"`
	Turns         []Turn    `json:"turns"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	Question      *Question `json:"question,omitempty"`
}

type Question struct {
	CallID   string                    `json:"call_id"`
	Question string                    `json:"question"`
	Context  string                    `json:"context,omitempty"`
	Choices  []app.AgentQuestionChoice `json:"choices,omitempty"`
}

type Store struct {
	Root    string
	catalog *sessioncatalog.Catalog
}

func NewStore(configDir string) *Store {
	return &Store{Root: configDir, catalog: sessioncatalog.New(configDir)}
}
func ValidID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.String() == id
}
func (s *Store) Lock(id string) (*worklock.Lock, error) {
	if !ValidID(id) {
		return nil, ErrInvalidID
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	return worklock.AcquireFile(s.Root, filepath.Join("session-operation-locks", id+".lock"), "q composer workflow")
}
func (s *Store) SessionStore(c Conversation) workspace.Store {
	return workspace.Store{Root: c.WorkspaceRoot, SessionID: c.ID}
}
func (s *Store) Create(name, model string) (Conversation, error) {
	return s.CreateInRoot("", name, model)
}

// CreateInRoot selects storage independently of the model's authorized tools.
// Empty root gives the session its own ordinary private chat workspace.
func (s *Store) CreateInRoot(root, name, model string) (Conversation, error) {
	name, model = strings.TrimSpace(name), strings.TrimSpace(model)
	if name == "" || utf8.RuneCountInString(name) > 120 || model == "" || len(model) > 512 {
		return Conversation{}, ErrInvalidInput
	}
	now := time.Now().UTC()
	c := Conversation{Version: 1, Name: name, Model: model, Turns: []Turn{}, CreatedAt: now, UpdatedAt: now}
	_, err := s.catalog.Create(root, name, workspace.Composer, func(store workspace.Store) error {
		c.ID, c.SessionID, c.WorkspaceRoot = store.SessionID, store.SessionID, store.Root
		if err := store.SaveSessionModelConfig(workspace.SessionModelConfig{Model: model}); err != nil {
			return err
		}
		return writeConversation(store, c)
	})
	return c, err
}
func (s *Store) Load(id string) (Conversation, error) {
	if !ValidID(id) {
		return Conversation{}, ErrInvalidID
	}
	store, err := s.catalog.Session(id, workspace.Composer)
	if err != nil {
		return Conversation{}, err
	}
	file, err := fsopen.Open(filepath.Join(store.SessionDir(), "composer", "conversation.json"))
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
	if c.Version != 1 || c.ID != id || c.SessionID != id || c.WorkspaceRoot != store.Root {
		return c, errors.New("invalid composer document")
	}
	return c, nil
}
func writeConversation(store workspace.Store, c Conversation) error {
	body, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if len(body) > maximumDocumentSize {
		return errors.New("composer history exceeds size limit")
	}
	directory := filepath.Join(store.SessionDir(), "composer")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	return fsreplace.WriteFile(filepath.Join(directory, "conversation.json"), body, 0o600)
}

// Save writes only the presentation projection. SessionHost exclusively owns
// session.json while a reply or background learning is running.
func (s *Store) Save(c Conversation) error {
	if !ValidID(c.ID) || c.SessionID != c.ID {
		return ErrInvalidID
	}
	store, err := s.catalog.Session(c.ID, workspace.Composer)
	if err != nil {
		return err
	}
	if store.Root != c.WorkspaceRoot {
		return errors.New("composer workspace changed")
	}
	return writeConversation(store, c)
}
func (s *Store) List() ([]Conversation, error) {
	stores, err := s.catalog.Sessions(workspace.Composer)
	if err != nil {
		return nil, err
	}
	result := []Conversation{}
	for _, store := range stores {
		c, err := s.Load(store.SessionID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		c.Turns = nil
		result = append(result, c)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}

// Delete requires the workflow lock and an idle, released native session.
func (s *Store) Delete(id string) error {
	if !ValidID(id) {
		return ErrInvalidID
	}
	return s.catalog.Delete(id, workspace.Composer)
}
