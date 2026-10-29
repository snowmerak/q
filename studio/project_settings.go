package studio

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/workitems"
)

type projectAgentOption struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Kind        string `json:"kind"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
}

func (s *sessionsService) projectAgentOptions(root string) ([]projectAgentOption, int, error) {
	value, err := s.main.Load()
	if errors.Is(err, config.ErrNotFound) {
		value, err = config.Default(), nil
	}
	if err != nil {
		return nil, 0, err
	}
	options := []projectAgentOption{}
	for _, def := range subagent.PublicAgentDefinitions() {
		if def.Info.Name == "builtin/junior-developer" {
			continue
		}
		o := projectAgentOption{ID: def.Info.Name, Description: def.Info.Description, Kind: def.Info.Kind, Available: true}
		if o.Kind == subagent.AgentKindExternal {
			_, _, o.Available = value.ExternalAgentConnection(def.Info.Role)
		}
		if !o.Available {
			o.Reason = "Configure an enabled ACP connection for this role in Providers."
		}
		options = append(options, o)
	}
	for _, entry := range profileStore(s.main, root).List() {
		o := projectAgentOption{ID: entry.Scope + "/" + entry.Profile.Name, Description: entry.Profile.Description, Kind: entry.Profile.EffectiveKind(), Available: entry.Err == nil}
		if entry.Err != nil {
			o.Reason = entry.Err.Error()
		}
		if o.Kind == subagent.AgentKindExternal && o.Available {
			connection, exists := value.Agents.Connections[entry.Profile.Agent]
			o.Available = exists && !connection.Disabled
			if !o.Available {
				o.Reason = "The profile's ACP connection is missing or disabled."
			}
		}
		options = append(options, o)
	}
	return options, value.EffectiveAgents().MaxParallel, nil
}

func (s *sessionsService) validateProjectAgent(id, root string) error {
	options, _, err := s.projectAgentOptions(root)
	if err != nil {
		return err
	}
	for _, o := range options {
		if o.ID == id {
			if !o.Available {
				return fmt.Errorf("subagent %s: %s", id, o.Reason)
			}
			return nil
		}
	}
	return fmt.Errorf("subagent %s is unavailable in this definition directory", id)
}

func (s *sessionsService) serveProjectOptions(w http.ResponseWriter, r *http.Request) {
	root := r.URL.Query().Get("workspace_root")
	if root != "" {
		var err error
		root, err = canonicalWorkspaceDirectory(root)
		if err != nil {
			writeAPIError(w, 400, err)
			return
		}
	}
	options, parallel, err := s.projectAgentOptions(root)
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Agents      []projectAgentOption `json:"agents"`
		MaxParallel int                  `json:"max_parallel"`
	}{options, parallel})
}

func (s *sessionsService) serveProjectDefaults(w http.ResponseWriter, r *http.Request) {
	var value workitems.ProjectDefaults
	if r.Method == http.MethodPut {
		if err := decodeWorkRequest(w, r, &value); err != nil {
			writeAPIError(w, 400, err)
			return
		}
		for _, id := range value.AgentIDs {
			if err := s.validateProjectAgent(id, ""); err != nil {
				writeAPIError(w, 400, err)
				return
			}
		}
	}
	workResponse(s, w, r, 200, func(ctx context.Context, c *library.Client) (workitems.ProjectDefaults, error) {
		if r.Method == http.MethodPut {
			return c.UpdateProjectDefaults(ctx, value)
		}
		return c.GetProjectDefaults(ctx)
	})
}

func (s *sessionsService) prepareInitialMembers(input *studioProjectUpdateRequest) error {
	if input.InitialMembers == nil {
		defaults, err := withProjectCatalog(s.projects, func(ctx context.Context, c *library.Client) (workitems.ProjectDefaults, error) {
			return c.GetProjectDefaults(ctx)
		})
		if err != nil {
			return err
		}
		members := []workitems.MemberInput{}
		for _, id := range defaults.AgentIDs {
			members = append(members, workitems.MemberInput{AgentID: id, DefinitionRoot: input.WorkspaceRoots[0], ExecutionRoot: input.WorkspaceRoots[0]})
		}
		input.InitialMembers = &members
	}
	for i := range *input.InitialMembers {
		member := &(*input.InitialMembers)[i]
		var err error
		member.DefinitionRoot, err = canonicalWorkspaceDirectory(member.DefinitionRoot)
		if err != nil {
			return err
		}
		member.ExecutionRoot, err = canonicalWorkspaceDirectory(member.ExecutionRoot)
		if err != nil {
			return err
		}
		if err := s.validateProjectAgent(member.AgentID, member.DefinitionRoot); err != nil {
			return err
		}
	}
	return nil
}
