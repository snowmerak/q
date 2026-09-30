package studio

import (
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workspace"
)

type studioProfileEntry struct {
	Profile  subagent.Profile `json:"profile"`
	Scope    string           `json:"scope"`
	Path     string           `json:"path"`
	Revision string           `json:"revision"`
	Shadowed bool             `json:"shadowed"`
	Error    string           `json:"error,omitempty"`
}

type profileUpdate struct {
	WorkspaceRoot string           `json:"workspace_root"`
	Scope         string           `json:"scope"`
	OriginalScope string           `json:"original_scope,omitempty"`
	Revision      string           `json:"revision,omitempty"`
	Profile       subagent.Profile `json:"profile"`
}

func (service *integrationService) serveProfiles(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost && request.Method != http.MethodPut {
		writer.Header().Set("Allow", "POST, PUT")
		writeAPIError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	var input profileUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := optionalCanonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := requireProfileWorkspace(input.Scope, input.OriginalScope, root); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	store := profileStore(service.main, root)
	var original *subagent.ProfileEntry
	if request.Method == http.MethodPut {
		originalScope := input.OriginalScope
		if originalScope == "" {
			originalScope = input.Scope
		}
		entry, found := findProfileEntry(store.List(), originalScope, input.Profile.Name)
		if !found {
			writeAPIError(writer, http.StatusNotFound, errors.New("subagent profile does not exist"))
			return
		}
		if revision(entry.Raw) != input.Revision {
			writeAPIError(writer, http.StatusConflict, errors.New("subagent profile changed externally; reload before saving"))
			return
		}
		original = &entry
	}
	if input.Profile.EffectiveKind() == subagent.AgentKindExternal {
		if connection, found := value.Agents.Connections[input.Profile.Agent]; !found || connection.Disabled {
			writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("external subagent requires an enabled ACP connection"))
			return
		}
	} else if !value.HasNativeRole(input.Profile.Role) {
		writeAPIError(writer, http.StatusUnprocessableEntity, errors.New("inner subagent references an unavailable model role"))
		return
	}
	if err := validateProfileGraph(store, input.Profile, input.Scope, original); err != nil {
		writeAPIError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	if err := store.Save(input.Profile, input.Scope, original); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func (service *integrationService) serveProfileDelete(writer http.ResponseWriter, request *http.Request) {
	var input profileUpdate
	if err := decodeIntegrationRequest(writer, request, &input); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	root, err := optionalCanonicalWorkspaceDirectory(input.WorkspaceRoot)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	if err := requireProfileWorkspace(input.Scope, input.OriginalScope, root); err != nil {
		writeAPIError(writer, http.StatusBadRequest, err)
		return
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	store := profileStore(service.main, root)
	entry, found := findProfileEntry(store.List(), input.Scope, request.PathValue("profile"))
	if !found {
		writeAPIError(writer, http.StatusNotFound, errors.New("subagent profile does not exist"))
		return
	}
	if revision(entry.Raw) != input.Revision {
		writeAPIError(writer, http.StatusConflict, errors.New("subagent profile changed externally; reload before deleting"))
		return
	}
	id := subagent.CanonicalProfileID(entry.Scope, entry.Profile.Name)
	var references []string
	for _, candidate := range store.List() {
		if candidate.Err == nil && containsString(candidate.Profile.Delegates, id) {
			references = append(references, subagent.CanonicalProfileID(candidate.Scope, candidate.Profile.Name))
		}
	}
	if len(references) > 0 {
		sort.Strings(references)
		writeAPIError(writer, http.StatusConflict, fmt.Errorf("subagent profile is delegated by %s", strings.Join(references, ", ")))
		return
	}
	if err := store.Delete(entry); err != nil {
		writeAPIError(writer, http.StatusConflict, err)
		return
	}
	value, err := service.main.Load()
	if err != nil {
		writeAPIError(writer, http.StatusInternalServerError, err)
		return
	}
	service.writeAgentsUnlocked(writer, root, value)
}

func profileStore(store config.Store, root string) subagent.ProfileStore {
	profiles := subagent.ProfileStore{Global: filepath.Join(store.Dir, "subagents")}
	if root != "" {
		profiles.Workspace = filepath.Join(root, workspace.DirectoryName, "subagents")
	}
	return profiles
}

func requireProfileWorkspace(scope, originalScope, root string) error {
	if root == "" && (scope == "workspace" || originalScope == "workspace") {
		return errors.New("workspace_root is required for repository subagent profiles")
	}
	return nil
}

func findProfileEntry(entries []subagent.ProfileEntry, scope, name string) (subagent.ProfileEntry, bool) {
	for _, entry := range entries {
		if entry.Scope == scope && entry.Profile.Name == name {
			return entry, true
		}
	}
	return subagent.ProfileEntry{}, false
}

func validateProfileGraph(store subagent.ProfileStore, candidate subagent.Profile, scope string, original *subagent.ProfileEntry) error {
	definitions := subagent.PublicAgentDefinitions()
	replaced := false
	for _, entry := range store.List() {
		if entry.Err != nil {
			return fmt.Errorf("subagent profile %s: %w", entry.Path, entry.Err)
		}
		if original != nil && entry.Path == original.Path {
			entry.Profile = candidate
			entry.Scope = scope
			replaced = true
		}
		definition, err := subagent.DefinitionForProfile(entry)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	if !replaced {
		definition, err := subagent.DefinitionForProfile(subagent.ProfileEntry{Profile: candidate, Scope: scope})
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	_, err := subagent.NewRegistry(definitions)
	return err
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
