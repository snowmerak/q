package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/workspace"
)

// ParticipantSessionHost is the native session boundary used by councils.
type ParticipantSessionHost interface {
	RunWithOptions(context.Context, workspace.Store, string, string, SessionOptions, SessionEventSink) error
	ReleaseSession(workspace.Store, string) error
}

// ParticipantRunner adds ACP seats to native Q sessions. Each ACP seat owns a
// process for the run. On reopening, completed exchanges rebuild its context;
// uncertain failed prompts are never replayed as completed operations.
type ParticipantRunner struct {
	ctx         context.Context
	native      ParticipantSessionHost
	connections map[string]config.AgentConnectionConfig
	mu          sync.Mutex
	sessions    map[string]*acpParticipantSession
	start       func(context.Context, acpAgentCommand, string, string, string, io.Writer) (*acpRemoteClient, error)
}

type acpParticipantSession struct {
	mu     sync.Mutex
	remote *acpRemoteClient
}

type acpParticipantExchange struct {
	Operation string `json:"operation"`
	Prompt    string `json:"prompt"`
	Answer    string `json:"answer"`
}

type acpParticipantState struct {
	Agent        string                   `json:"agent"`
	SystemPrompt string                   `json:"system_prompt"`
	Roots        []string                 `json:"roots"`
	Exchanges    []acpParticipantExchange `json:"exchanges"`
}

func NewParticipantRunner(ctx context.Context, native ParticipantSessionHost, connections map[string]config.AgentConnectionConfig) *ParticipantRunner {
	return &ParticipantRunner{ctx: ctx, native: native, connections: connections, sessions: make(map[string]*acpParticipantSession), start: startACPRemoteClient}
}

func (r *ParticipantRunner) RunWithOptions(ctx context.Context, store workspace.Store, id, prompt string, options SessionOptions, emit SessionEventSink) error {
	if options.Agent == "" {
		if r.native == nil {
			return errors.New("native session host is unavailable")
		}
		return r.native.RunWithOptions(ctx, store, id, prompt, options, emit)
	}
	if options.Model != "" || options.ReasoningEffort != "" || options.OperationID == "" {
		return errors.New("ACP participant requires an operation ID and cannot specify model controls")
	}
	connection, found := r.connections[options.Agent]
	if !found || connection.Disabled {
		return fmt.Errorf("ACP connection %q is unavailable", options.Agent)
	}
	selected, err := store.ForSession(id)
	if err != nil {
		return err
	}
	key := selected.SessionDir()
	r.mu.Lock()
	session := r.sessions[key]
	if session == nil {
		session = &acpParticipantSession{}
		r.sessions[key] = session
	}
	r.mu.Unlock()
	session.mu.Lock()
	defer session.mu.Unlock()
	// Share the same lock as native sessions, including across Studio processes.
	lock, err := workspace.AcquireSessionLock(store.Root, id, "q council ACP")
	if err != nil {
		return err
	}
	defer lock.Close()
	roots := append([]string(nil), options.AuxiliaryDirectories...)
	if options.WorkingDirectory != "" {
		roots = append([]string{options.WorkingDirectory}, roots...)
	}
	state := acpParticipantState{Agent: options.Agent, SystemPrompt: options.SystemPrompt, Roots: roots}
	path := filepath.Join(key, "council-acp.json")
	if body, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("read ACP participant state: %w", err)
		}
		if state.Agent != options.Agent || state.SystemPrompt != options.SystemPrompt || !slices.Equal(state.Roots, roots) {
			return errors.New("ACP participant configuration changed")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, exchange := range state.Exchanges {
		if exchange.Operation == options.OperationID {
			if exchange.Prompt != prompt {
				return errors.New("ACP participant operation prompt changed")
			}
			return emitParticipantResult(emit, id, exchange.Answer)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	content := prompt
	if session.remote == nil {
		command, err := resolveConfiguredACPAgentCommand(connection, exec.LookPath)
		if err != nil {
			return err
		}
		// Council must never install an agent implicitly through npx.
		if command.installTip != "" {
			return errors.New("council ACP requires an installed codex-acp executable")
		}
		root := key
		if len(roots) > 0 {
			root = roots[0]
		}
		remote, err := r.start(r.ctx, command, root, "", connection.AuthMethod, io.Discard)
		if err != nil {
			return err
		}
		remote.mu.Lock()
		remote.permissions = acpPermissionReadOnly
		remote.requireCompletedText = true
		remote.mu.Unlock()
		session.remote = remote
		var context strings.Builder
		context.WriteString(state.SystemPrompt)
		context.WriteString("\n\nUse only read-only operations. Do not edit files or execute commands.\n")
		if len(roots) > 0 {
			context.WriteString("Authorized repository roots:\n" + strings.Join(roots, "\n") + "\n")
		} else {
			context.WriteString("This council has no repository attached. Answer using the conversation only.\n")
		}
		if len(state.Exchanges) > 0 {
			context.WriteString("\nCompleted conversation restored by Q (context only; do not repeat these tasks):\n")
			for _, exchange := range state.Exchanges {
				fmt.Fprintf(&context, "\nUser:\n%s\nAssistant:\n%s\n", exchange.Prompt, exchange.Answer)
			}
		}
		content = context.String() + "\nCurrent request:\n" + prompt
	}
	response, _, err := session.remote.prompt(ctx, content, nil)
	if err != nil {
		_ = session.remote.Close()
		session.remote = nil
		return err
	}
	answer := response.Choices[0].Message.TextContent()
	state.Exchanges = append(state.Exchanges, acpParticipantExchange{Operation: options.OperationID, Prompt: prompt, Answer: answer})
	if err := saveACPParticipantState(path, state); err != nil {
		_ = session.remote.Close()
		session.remote = nil
		return err
	}
	return emitParticipantResult(emit, id, answer)
}

func emitParticipantResult(emit SessionEventSink, id, answer string) error {
	if emit == nil {
		return nil
	}
	return emit(SessionEvent{Type: "result", SessionID: id, Content: answer})
}

func saveACPParticipantState(path string, state acpParticipantState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".council-acp-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(body); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return fsreplace.Replace(file.Name(), path)
}

func (r *ParticipantRunner) ReleaseSession(store workspace.Store, id string) error {
	selected, err := store.ForSession(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	session := r.sessions[selected.SessionDir()]
	delete(r.sessions, selected.SessionDir())
	r.mu.Unlock()
	if session == nil {
		if r.native != nil {
			return r.native.ReleaseSession(store, id)
		}
		return nil
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.remote != nil {
		return session.remote.Close()
	}
	return nil
}
