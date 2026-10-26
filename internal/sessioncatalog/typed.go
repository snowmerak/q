package sessioncatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/snowmerak/q/workspace"
)

// Create allocates a canonical typed session. An empty root selects the same
// private workspace convention as ordinary Studio chats. Explicit roots allow
// workspace attachment without coupling storage to a workflow's read scope.
func (c *Catalog) Create(root, title string, kind workspace.SessionType, initialize ...func(workspace.Store) error) (store workspace.Store, returnErr error) {
	if kind != workspace.Council && kind != workspace.Composer {
		return store, errors.New("catalog: expected a typed workflow")
	}
	managed := root == ""
	if managed {
		directory := filepath.Join(filepath.Dir(c.path), "chats")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return store, err
		}
		var err error
		root, err = os.MkdirTemp(directory, "chat-")
		if err != nil {
			return store, err
		}
	}
	defer func() {
		if returnErr != nil && managed {
			_ = os.RemoveAll(root)
		}
	}()
	absolute, err := filepath.Abs(root)
	if err != nil {
		return store, err
	}
	root, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return store, err
	}
	if err := workspace.RejectHomeDirectory(root); err != nil {
		return store, err
	}
	store, lock, err := workspace.CreateTypedSession(root, "q typed session create", kind)
	if err != nil {
		return store, err
	}
	value, err := store.Load()
	if err == nil {
		value.Title = title
		err = store.Save(value)
	}
	for _, init := range initialize {
		if err == nil {
			err = init(store)
		}
	}
	err = errors.Join(err, lock.Close())
	if err != nil {
		_ = store.RemoveTypedSessionDirectory(kind)
		return store, err
	}
	if _, err := c.Register(root, store.SessionID); err != nil {
		_ = store.RemoveTypedSessionDirectory(kind)
		return store, err
	}
	return store, nil
}

func (c *Catalog) Session(id string, kind workspace.SessionType) (workspace.Store, error) {
	entries, err := c.List()
	if err != nil {
		return workspace.Store{}, err
	}
	var found *workspace.Store
	for _, entry := range entries {
		if entry.SessionID != id {
			continue
		}
		store, err := (workspace.Store{Root: entry.WorkspaceRoot}).ForSession(id)
		if err != nil {
			return workspace.Store{}, err
		}
		value, err := store.Load()
		if errors.Is(err, workspace.ErrNotFound) {
			continue
		}
		if err != nil {
			return workspace.Store{}, err
		}
		if value.Type != kind {
			continue
		}
		if found != nil {
			return workspace.Store{}, fmt.Errorf("catalog: ambiguous session ID %q", id)
		}
		found = &store
	}
	if found == nil {
		return workspace.Store{}, os.ErrNotExist
	}
	return *found, nil
}

func (c *Catalog) Sessions(kind workspace.SessionType) ([]workspace.Store, error) {
	entries, err := c.List()
	if err != nil {
		return nil, err
	}
	result := []workspace.Store{}
	for _, entry := range entries {
		store, err := (workspace.Store{Root: entry.WorkspaceRoot}).ForSession(entry.SessionID)
		if err != nil {
			return nil, err
		}
		value, err := store.Load()
		if errors.Is(err, workspace.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if value.Type == kind {
			result = append(result, store)
		}
	}
	return result, nil
}

func (c *Catalog) Delete(id string, kind workspace.SessionType) error {
	store, err := c.Session(id, kind)
	if err != nil {
		return err
	}
	if err := store.RemoveTypedSessionDirectory(kind); err != nil {
		return err
	}
	entries, err := c.List()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.WorkspaceRoot == store.Root && entry.SessionID == id {
			return c.Unregister(entry.ID)
		}
	}
	return nil
}
