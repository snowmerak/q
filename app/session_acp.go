package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/workspace"
)

type acpSessionCheckpoint struct {
	Agent                      string   `json:"agent"`
	SessionID                  string   `json:"session_id"`
	Connection                 [32]byte `json:"connection"`
	Transcript                 [32]byte `json:"transcript"`
	LibraryInstructionsVersion int      `json:"library_instructions_version,omitempty"`
}

func acpSessionPath(store workspace.Store) string {
	return filepath.Join(store.SessionDir(), workspace.ACPSessionFileName)
}

func acpTranscriptDigest(messages []client.Message) [32]byte {
	body, _ := json.Marshal(messages)
	return sha256.Sum256(body)
}

func (host *SessionHost) prepareACPSession(ctx context.Context, prepared *preparedSession, loaded config.Config, selected workspace.SessionModelConfig) (*preparedSession, error) {
	agent := selected.Agent
	if err := prepared.store.RequireInteraction(); err != nil {
		return nil, err
	}
	connection, found := loaded.Agents.Connections[agent]
	if !found || connection.Disabled {
		return nil, fmt.Errorf("ACP connection %q is unavailable", agent)
	}
	command, err := resolveConfiguredACPAgentCommand(connection, exec.LookPath)
	if err != nil {
		return nil, err
	}
	if connection.Library || prepared.executionRoot != prepared.store.Root {
		binding := workitemtools.Binding{SessionID: prepared.store.SessionID}
		binding.EmbeddingConnection, err = host.workEmbeddingConnection()
		if err != nil {
			return nil, err
		}
		if prepared.projectContext != nil {
			binding.WorkspaceID = prepared.projectContext.ProjectID
			binding.Manager = prepared.projectContext.Role == coordinator.RoleIntegrator
		}
		command.mcpServers, err = acpLibraryServers(host.store.Dir, binding)
		if err != nil {
			return nil, fmt.Errorf("configure ACP Library: %w", err)
		}
	}
	saved, err := prepared.store.Load()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(connection)
	if err != nil {
		return nil, err
	}
	// Connection files rotate with the host process; they do not change the
	// workspace/tool binding and must not discard a resumable ACP conversation.
	workspaceID := ""
	integrator := false
	if prepared.projectContext != nil {
		workspaceID = prepared.projectContext.ProjectID
		integrator = prepared.projectContext.Role == coordinator.RoleIntegrator
	}
	mcpBinding, err := json.Marshal(struct {
		Enabled                bool
		WorkspaceID, SessionID string
		Manager                bool
	}{len(command.mcpServers) > 0, workspaceID, prepared.store.SessionID, integrator})
	if err != nil {
		return nil, err
	}
	body = append(body, mcpBinding...)
	if prepared.executionRoot != prepared.store.Root {
		// A resumed remote agent must see the current project roots too.
		// Role is bound by the reserved session ID and manager permission above.
		// Exclude the new display field to retain existing ACP resume fingerprints.
		projectContext := *prepared.projectContext
		projectContext.Role = ""
		project, err := json.Marshal(projectContext)
		if err != nil {
			return nil, err
		}
		body = append(body, []byte("\n"+prepared.executionRoot+"\n")...)
		body = append(body, project...)
	}
	checkpoint := acpSessionCheckpoint{Agent: agent, Connection: sha256.Sum256(body)}
	remoteID := ""
	libraryInstructionsVersion := 0
	if body, err := os.ReadFile(acpSessionPath(prepared.store)); err == nil {
		var previous acpSessionCheckpoint
		if err := json.Unmarshal(body, &previous); err != nil {
			return nil, fmt.Errorf("read ACP session checkpoint: %w", err)
		}
		if previous.Agent == agent && previous.Connection == checkpoint.Connection && previous.Transcript == acpTranscriptDigest(saved.Transcript) {
			remoteID = previous.SessionID
			libraryInstructionsVersion = previous.LibraryInstructionsVersion
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	remote, err := startACPRemoteClientWithResume(ctx, command, prepared.executionRoot, remoteID, connection.AuthMethod, io.Discard, true)
	if err != nil {
		return nil, err
	}
	prepared.client = remote
	if selected.AgentModel != "" && remote.modelSettings().CurrentModel != selected.AgentModel {
		if err := remote.setModel(ctx, selected.AgentModel); err != nil {
			return nil, err
		}
	}
	if selected.AgentReasoningEffort != "" && remote.modelSettings().CurrentReasoningEffort != selected.AgentReasoningEffort {
		if err := remote.setReasoningEffort(ctx, selected.AgentReasoningEffort); err != nil {
			return nil, err
		}
	}
	remote.sessionCheckpoint = &checkpoint
	if prepared.projectContext != nil {
		remote.libraryRoleInstructions = coordinator.RoleInstructions(prepared.projectContext.Role)
	}
	remote.libraryInstructionsSent = remote.resumed && libraryInstructionsVersion == acpLibraryInstructionsVersion
	if !remote.resumed {
		remote.initialContext = completedACPContext(saved.Transcript)
		if prepared.executionRoot != prepared.store.Root && prepared.projectContext != nil {
			remote.initialContext = fmt.Sprintf("Project: %s\nProject directories: %s\n\n", prepared.projectContext.ProjectName, strings.Join(append([]string{prepared.executionRoot}, prepared.projectContext.AuxiliaryRoots...), ", ")) + remote.initialContext
		}
	}
	prepared.state = newRuntimeModel(ctx, host.store, host.factory, host.manager)
	prepared.state.workspaceStore = &prepared.store
	prepared.state.workspaceLock = prepared.lock
	prepared.state.sessionOptions = &SessionOptions{Agent: agent, DisableLearning: true}
	if prepared.executionRoot != prepared.store.Root {
		prepared.state.sessionOptions.WorkingDirectory = prepared.executionRoot
		if prepared.projectContext != nil {
			prepared.state.studioWorkspaceContext = prepared.projectContext
			prepared.state.sessionOptions.AuxiliaryDirectories = append([]string(nil), prepared.projectContext.AuxiliaryRoots...)
		}
	}
	loaded.Provider.Model, loaded.Provider.ReasoningEffort, loaded.Provider.SystemPrompt = "acp/"+agent, "", ""
	prepared.state.enterSession(loaded, remote)
	return prepared, nil
}

func completedACPContext(messages []client.Message) string {
	var context strings.Builder
	var user string
	for _, message := range messages {
		switch message.Role {
		case client.RoleUser:
			user = message.TextContent()
		case client.RoleAssistant:
			if user != "" && message.TextContent() != "" && len(message.ToolCalls) == 0 {
				fmt.Fprintf(&context, "\nUser:\n%s\nAssistant:\n%s\n", user, message.TextContent())
				user = ""
			}
		}
	}
	if context.Len() == 0 {
		return ""
	}
	return "Completed conversation restored by Q as context only. Do not repeat these tasks or their side effects.\n" + context.String()
}

func saveACPSessionCheckpoint(store workspace.Store, remote *acpRemoteClient, messages []client.Message) error {
	if remote.sessionCheckpoint == nil || len(messages) == 0 || messages[len(messages)-1].Role != client.RoleAssistant {
		return nil
	}
	checkpoint := *remote.sessionCheckpoint
	checkpoint.SessionID = string(remote.sessionID)
	checkpoint.Transcript = acpTranscriptDigest(messages)
	if len(remote.mcpServers) > 0 && remote.libraryInstructionsSent {
		checkpoint.LibraryInstructionsVersion = acpLibraryInstructionsVersion
	}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return fsreplace.WriteFile(acpSessionPath(store), body, 0o600)
}
