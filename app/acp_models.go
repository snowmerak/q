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
	Supported              bool             `json:"supported"`
	CurrentModel           string           `json:"current_model"`
	Models                 []ACPModelOption `json:"models"`
	ReasoningSupported     bool             `json:"reasoning_supported"`
	CurrentReasoningEffort string           `json:"current_reasoning_effort"`
	ReasoningEfforts       []ACPModelOption `json:"reasoning_efforts"`
}

type ACPSettingsUpdate struct {
	Model           *string
	ReasoningEffort *string
}

func (r *acpRemoteClient) updateConfigOptions(options []acp.SessionConfigOption) {
	r.mu.Lock()
	r.configOptions = options
	r.mu.Unlock()
}

func (r *acpRemoteClient) configOptionLocked(category acp.SessionConfigOptionCategory, fallbackID acp.SessionConfigId) *acp.SessionConfigOptionSelect {
	var fallback *acp.SessionConfigOptionSelect
	for _, option := range r.configOptions {
		selected := option.Select
		if selected == nil {
			continue
		}
		if selected.Category != nil && *selected.Category == category {
			return selected
		}
		if selected.Category == nil && selected.Id == fallbackID && fallback == nil {
			fallback = selected
		}
	}
	return fallback
}

func (r *acpRemoteClient) modelSettings() ACPModelSettings {
	r.mu.Lock()
	defer r.mu.Unlock()
	settings := ACPModelSettings{Models: []ACPModelOption{}, ReasoningEfforts: []ACPModelOption{}}
	if option := r.configOptionLocked(acp.SessionConfigOptionCategoryModel, "model"); option != nil {
		settings.CurrentModel = string(option.CurrentValue)
		settings.Models = acpSelectOptions(option)
		settings.Supported = len(settings.Models) > 0
	}
	if option := r.configOptionLocked(acp.SessionConfigOptionCategoryThoughtLevel, "reasoning_effort"); option != nil {
		settings.CurrentReasoningEffort = string(option.CurrentValue)
		settings.ReasoningEfforts = acpSelectOptions(option)
		settings.ReasoningSupported = len(settings.ReasoningEfforts) > 0
	}
	return settings
}

func acpSelectOptions(option *acp.SessionConfigOptionSelect) []ACPModelOption {
	options := []ACPModelOption{}
	appendOptions := func(values []acp.SessionConfigSelectOption, group string) {
		for _, value := range values {
			options = append(options, ACPModelOption{ID: string(value.Value), Name: value.Name, Group: group})
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
	return options
}

func (r *acpRemoteClient) setModel(ctx context.Context, model string) error {
	return r.setConfigValue(ctx, acp.SessionConfigOptionCategoryModel, "model", model)
}

func (r *acpRemoteClient) setReasoningEffort(ctx context.Context, effort string) error {
	return r.setConfigValue(ctx, acp.SessionConfigOptionCategoryThoughtLevel, "reasoning_effort", effort)
}

func (r *acpRemoteClient) setConfigValue(ctx context.Context, category acp.SessionConfigOptionCategory, fallbackID acp.SessionConfigId, value string) error {
	r.mu.Lock()
	option := r.configOptionLocked(category, fallbackID)
	if option == nil {
		r.mu.Unlock()
		return ErrACPModelSelection
	}
	found := false
	for _, option := range acpSelectOptions(option) {
		found = found || option.ID == value
	}
	if !found {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s value %q was not offered by the agent", ErrACPModelSelection, category, value)
	}
	if string(option.CurrentValue) == value {
		r.mu.Unlock()
		return nil
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
		SessionId: sessionID, ConfigId: configID, Value: acp.SessionConfigValueId(value),
	}})
	if err != nil {
		return err
	}
	r.updateConfigOptions(response.ConfigOptions)
	r.mu.Lock()
	confirmed := r.configOptionLocked(category, fallbackID)
	ok = confirmed != nil && string(confirmed.CurrentValue) == value
	r.mu.Unlock()
	if !ok {
		return errors.New("ACP agent did not confirm the requested setting")
	}
	return nil
}

// ACPModel connects an ACP session and optionally changes its model in place.
// The ordinary live-session lease excludes an active prompt and retains the
// workspace lock while the agent state and durable selection are updated.
func (host *SessionHost) ACPModel(ctx context.Context, store workspace.Store, id string, selected *string) (ACPModelSettings, error) {
	return host.ConfigureACP(ctx, store, id, ACPSettingsUpdate{Model: selected})
}

// ConfigureACP updates choices offered by the agent without replacing its session.
func (host *SessionHost) ConfigureACP(ctx context.Context, store workspace.Store, id string, update ACPSettingsUpdate) (ACPModelSettings, error) {
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
	if update.Model == nil && update.ReasoningEffort == nil {
		return remote.modelSettings(), nil
	}
	previous := remote.modelSettings()
	if update.Model != nil {
		selection.AgentModel = *update.Model
	}
	if update.ReasoningEffort != nil {
		selection.AgentReasoningEffort = *update.ReasoningEffort
	}
	if err := selection.Validate(); err != nil {
		return ACPModelSettings{}, fmt.Errorf("%w: %v", ErrACPModelSelection, err)
	}
	if update.Model != nil {
		err = remote.setModel(ctx, *update.Model)
	}
	if err == nil && update.ReasoningEffort != nil {
		err = remote.setReasoningEffort(ctx, *update.ReasoningEffort)
	}
	if err == nil && update.Model != nil && update.ReasoningEffort == nil && selection.AgentReasoningEffort != "" {
		// A new model may offer different effort levels or reset the selection.
		current := remote.modelSettings()
		selection.AgentReasoningEffort = ""
		if current.ReasoningSupported {
			selection.AgentReasoningEffort = current.CurrentReasoningEffort
		}
	}
	if err == nil {
		err = session.prepared.store.SaveSessionModelConfig(selection)
	}
	if err != nil {
		rollbackContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		if previous.CurrentModel != "" && remote.modelSettings().CurrentModel != previous.CurrentModel {
			err = errors.Join(err, remote.setModel(rollbackContext, previous.CurrentModel))
		}
		if previous.ReasoningSupported && remote.modelSettings().CurrentReasoningEffort != previous.CurrentReasoningEffort {
			err = errors.Join(err, remote.setReasoningEffort(rollbackContext, previous.CurrentReasoningEffort))
		}
		cancel()
		return ACPModelSettings{}, err
	}
	// Retain the existing ACP process after changing the saved model selection.
	session.configuration, err = host.sessionConfiguration(session.prepared.store)
	return remote.modelSettings(), err
}
