package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/snowmerak/q/third_party/acp-go-sdk"
	"github.com/snowmerak/q/workspace"
)

var ErrACPModelSelection = errors.New("ACP model selection is unavailable")

type ACPModelOption struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group,omitempty"`
}

type ACPModelSettings struct {
	Supported    bool             `json:"supported"`
	CurrentModel string           `json:"current_model"`
	Models       []ACPModelOption `json:"models"`
}

func (r *acpRemoteClient) updateConfigOptions(options []acp.SessionConfigOption) {
	r.mu.Lock()
	r.configOptions = options
	r.mu.Unlock()
}

func (r *acpRemoteClient) modelOptionLocked() *acp.SessionConfigOptionSelect {
	var fallback *acp.SessionConfigOptionSelect
	for _, option := range r.configOptions {
		selected := option.Select
		if selected == nil {
			continue
		}
		if selected.Category != nil && *selected.Category == acp.SessionConfigOptionCategoryModel {
			return selected
		}
		if selected.Category == nil && selected.Id == "model" && fallback == nil {
			fallback = selected
		}
	}
	return fallback
}

func (r *acpRemoteClient) modelSettings() ACPModelSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	settings := ACPModelSettings{Models: []ACPModelOption{}}
	option := r.modelOptionLocked()
	if option == nil {
		return settings
	}
	settings.CurrentModel = string(option.CurrentValue)
	appendOptions := func(values []acp.SessionConfigSelectOption, group string) {
		for _, value := range values {
			settings.Models = append(settings.Models, ACPModelOption{ID: string(value.Value), Name: value.Name, Group: group})
		}
	}
	if option.Options.Ungrouped != nil {
		appendOptions(*option.Options.Ungrouped, "")
	}
	if option.Options.Grouped != nil {
		for _, group := range *option.Options.Grouped {
			appendOptions(group.Options, group.Name)
		}
	}
	settings.Supported = len(settings.Models) > 0
	return settings
}

func (r *acpRemoteClient) setModel(ctx context.Context, model string) error {
	settings := r.modelSettings()
	found := false
	for _, option := range settings.Models {
		found = found || option.ID == model
	}
	if !settings.Supported || !found {
		return fmt.Errorf("%w: model %q was not offered by the agent", ErrACPModelSelection, model)
	}
	if settings.CurrentModel == model {
		return nil
	}
	r.mu.Lock()
	option := r.modelOptionLocked()
	if option == nil {
		r.mu.Unlock()
		return ErrACPModelSelection
	}
	configID, sessionID, connection := option.Id, r.sessionID, r.connection
	r.mu.Unlock()
	setter, ok := connection.(interface {
		SetSessionConfigOption(context.Context, acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error)
	})
	if !ok {
		return ErrACPModelSelection
	}
	response, err := setter.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
		SessionId: sessionID, ConfigId: configID, Value: acp.SessionConfigValueId(model),
	}})
	if err != nil {
		return err
	}
	r.updateConfigOptions(response.ConfigOptions)
	if r.modelSettings().CurrentModel != model {
		return errors.New("ACP agent did not confirm the requested model")
	}
	return nil
}

// ACPModel connects an ACP session and optionally changes its model in place.
// The ordinary live-session lease excludes an active prompt and retains the
// workspace lock while the agent state and durable selection are updated.
func (host *SessionHost) ACPModel(ctx context.Context, store workspace.Store, id string, selected *string) (ACPModelSettings, error) {
	if err := ctx.Err(); err != nil {
		return ACPModelSettings{}, err
	}
	if err := workspace.RejectHomeDirectory(store.Root); err != nil {
		return ACPModelSettings{}, err
	}
	store, err := store.ForSession(id)
	if err != nil {
		return ACPModelSettings{}, err
	}
	if err := store.RequireInteraction(); err != nil {
		return ACPModelSettings{}, err
	}
	selection, err := store.LoadSessionModelConfig()
	if err != nil {
		return ACPModelSettings{}, err
	}
	if selection.Agent == "" {
		return ACPModelSettings{}, ErrACPModelSelection
	}
	session, err := host.acquireLiveSession(store, id)
	if err != nil {
		return ACPModelSettings{}, err
	}
	defer session.busy.Unlock()
	remote, ok := session.prepared.client.(*acpRemoteClient)
	if !ok {
		return ACPModelSettings{}, ErrACPModelSelection
	}
	if selected == nil {
		return remote.modelSettings(), nil
	}
	previous := remote.modelSettings().CurrentModel
	selection.AgentModel = *selected
	if err := selection.Validate(); err != nil {
		return ACPModelSettings{}, fmt.Errorf("%w: %v", ErrACPModelSelection, err)
	}
	err = remote.setModel(ctx, *selected)
	if err == nil {
		err = session.prepared.store.SaveSessionModelConfig(selection)
	}
	if err != nil {
		if previous != "" && remote.modelSettings().CurrentModel != previous {
			rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			err = errors.Join(err, remote.setModel(rollbackContext, previous))
			cancel()
		}
		return ACPModelSettings{}, err
	}
	// Retain the existing ACP process after changing the saved model selection.
	session.configuration, err = host.sessionConfiguration(session.prepared.store)
	return remote.modelSettings(), err
}
