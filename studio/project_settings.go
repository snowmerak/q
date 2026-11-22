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
	"github.com/snowmerak/q/workspace"
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
	var targets map[string]config.SubagentTarget
	if root != "" {
		targets, err = (workspace.Store{Root: root}).LoadSubagentTargets()
		if err != nil {
			return nil, 0, err
		}
	}
	options := []projectAgentOption{}
	for _, definition := range subagent.PublicAgentDefinitions() {
		options = append(options, projectAgentFromDefinition(value, definition, targets))
	}
	for _, entry := range profileStore(s.main, root).List() {
		definition, err := subagent.DefinitionForProfile(entry)
		if err != nil {
			options = append(options, projectAgentOption{ID: entry.Scope + "/" + entry.Profile.Name, Description: entry.Profile.Description, Kind: entry.Profile.EffectiveKind(), Reason: err.Error()})
			continue
		}
		options = append(options, projectAgentFromDefinition(value, definition, targets))
	}
	return options, value.EffectiveAgents().MaxParallel, nil
}

func projectAgentFromDefinition(value config.Config, definition subagent.AgentDefinition, targets map[string]config.SubagentTarget) projectAgentOption {
	effective := subagent.ExecutionDefinition(value, definition, targets)
	option := projectAgentOption{ID: effective.Info.Name, Description: effective.Info.Description, Kind: effective.Info.Kind, Available: true}
	if effective.Info.Kind == subagent.AgentKindExternal {
		connection, found := value.Agents.Connections[effective.Connection]
		if !found || connection.Disabled {
			option.Available = false
			option.Reason = "Select an enabled ACP connection for this subagent."
		}
	}
	return option
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
