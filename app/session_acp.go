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
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/workspace"
)

type acpSessionCheckpoint struct {
	Agent      string   `json:"agent"`
	SessionID  string   `json:"session_id"`
	Connection [32]byte `json:"connection"`
	Transcript [32]byte `json:"transcript"`
}

func acpSessionPath(store workspace.Store) string {
	return filepath.Join(store.SessionDir(), "acp-session.json")
}

func acpTranscriptDigest(messages []client.Message) [32]byte {
	body, _ := json.Marshal(messages)
	return sha256.Sum256(body)
}

func (host *SessionHost) prepareACPSession(ctx context.Context, prepared *preparedSession, loaded config.Config, agent string) (*preparedSession, error) {
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
	saved, err := prepared.store.Load()
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(connection)
	if err != nil {
		return nil, err
	}
	checkpoint := acpSessionCheckpoint{Agent: agent, Connection: sha256.Sum256(body)}
	remoteID := ""
	if body, err := os.ReadFile(acpSessionPath(prepared.store)); err == nil {
		var previous acpSessionCheckpoint
		if err := json.Unmarshal(body, &previous); err != nil {
			return nil, fmt.Errorf("read ACP session checkpoint: %w", err)
		}
		if previous.Agent == agent && previous.Connection == checkpoint.Connection && previous.Transcript == acpTranscriptDigest(saved.Transcript) {
			remoteID = previous.SessionID
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	remote, err := startACPRemoteClientWithResume(ctx, command, prepared.store.Root, remoteID, connection.AuthMethod, io.Discard, true)
	if err != nil {
		return nil, err
	}
	prepared.client = remote
	remote.sessionCheckpoint = &checkpoint
	if !remote.resumed {
		remote.initialContext = completedACPContext(saved.Transcript)
	}
	prepared.state = newRuntimeModel(ctx, host.store, host.factory, host.manager)
	prepared.state.workspaceStore = &prepared.store
	prepared.state.workspaceLock = prepared.lock
	// The remote agent owns tools, learning and context compaction. Q retains
	// the normal live-session event stream, transcript and workspace lock.
	prepared.state.workspaceLearning = workspace.LearningConfig{Disabled: true}
	prepared.state.workspaceLearningRestored = true
	prepared.state.sessionOptions = &SessionOptions{Agent: agent, DisableLearning: true}
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
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	return fsreplace.WriteFile(acpSessionPath(store), body, 0o600)
}
