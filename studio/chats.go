package studio

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/workspace"
)

func (service *sessionsService) chatDirectory() string {
	return filepath.Join(filepath.Dir(service.registry.path), "chats")
}

func (service *sessionsService) isChat(root string) bool {
	directory, err := filepath.EvalSymlinks(service.chatDirectory())
	if err != nil {
		return false
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(root)
	return err == nil && comparableStudioPath(resolved) == comparableStudioPath(root) && strings.HasPrefix(filepath.Base(root), "chat-") &&
		comparableStudioPath(filepath.Dir(root)) == comparableStudioPath(directory)
}

// Each chat owns a private working directory. Existing session persistence,
// locking and execution can be reused without attaching a user's project.
func (service *sessionsService) createChat() (_ registeredSession, returnErr error) {
	if err := os.MkdirAll(service.chatDirectory(), 0o700); err != nil {
		return registeredSession{}, err
	}
	root, err := os.MkdirTemp(service.chatDirectory(), "chat-")
	if err != nil {
		return registeredSession{}, err
	}
	createdRoot := root
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(returnErr, os.RemoveAll(createdRoot))
		}
	}()
	root, err = canonicalWorkspaceDirectory(root)
	if err != nil {
		return registeredSession{}, err
	}
	store, lock, err := workspace.CreateSession(root, "q studio chat")
	if lock != nil {
		err = errors.Join(err, lock.Close())
	}
	if err != nil {
		return registeredSession{}, err
	}
	return service.registry.register(root, store.SessionID)
}

func (service *sessionsService) serveCreateChat(writer http.ResponseWriter, request *http.Request) {
	var input struct{}
	if err := decodeSessionRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	item, err := service.createChat()
	if err != nil {
		writeSessionError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, service.registeredTree(item, nil))
}

func (service *sessionsService) serveDeleteChat(writer http.ResponseWriter, request *http.Request) {
	items, err := service.registry.list()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	for _, item := range items {
		if item.ID != request.PathValue("registration") {
			continue
		}
		// Never recursively remove a user-supplied workspace or the config root.
		if !service.isChat(item.WorkspaceRoot) {
			writeAPIError(writer, http.StatusBadRequest, errors.New("session is not a managed chat"))
			return
		}
		if err := service.releaseSession(item.WorkspaceRoot, item.SessionID); err != nil {
			writeSessionError(writer, err)
			return
		}
		store := workspace.Store{Root: item.WorkspaceRoot, SessionID: item.SessionID}
		if err := service.deleteSession(store); err != nil {
			writeSessionError(writer, err)
			return
		}
		if err := os.RemoveAll(item.WorkspaceRoot); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		if err := service.registry.unregister(item.ID); err != nil {
			writeAPIError(writer, http.StatusInternalServerError, err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	writeAPIError(writer, http.StatusNotFound, errors.New("chat does not exist"))
}
