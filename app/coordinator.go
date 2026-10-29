package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/snowmerak/q/archiveembed"
	"github.com/snowmerak/q/change"
	"github.com/snowmerak/q/client"
	"github.com/snowmerak/q/config"
	"github.com/snowmerak/q/coordinator"
	"github.com/snowmerak/q/gitwork"
	"github.com/snowmerak/q/internal/fsreplace"
	"github.com/snowmerak/q/internal/workitemtools"
	"github.com/snowmerak/q/library"
	"github.com/snowmerak/q/mcpconfig"
	"github.com/snowmerak/q/sessionstore"
	"github.com/snowmerak/q/subagent"
	"github.com/snowmerak/q/systemoneconfig"
	qtools "github.com/snowmerak/q/tools"
	"github.com/snowmerak/q/workitems"
	"github.com/snowmerak/q/workspace"
	"github.com/snowmerak/q/workspacememory"
)

func (host *SessionHost) startCoordinator() {
	host.coordinatorMu.Lock()
	defer host.coordinatorMu.Unlock()
	if host.coordinator != nil || host.coordinatorClosed || host.ctx == nil || host.store.Dir == "" {
		return
	}
	host.coordinator = coordinator.Start(host.ctx, coordinator.EngineOptions{Directory: host.store.Dir, Candidates: host.coordinatorCandidates, Run: host.runTicket, Deliver: host.deliverTicketReview, Embed: host.embedWorkItems, Parallel: func() int {
		value, err := host.store.Load()
		if err != nil {
			return 1
		}
		return value.EffectiveAgents().MaxParallel
	}, Log: func(err error) {
		if host.logs != nil {
			_, _ = host.logs.Write([]byte("coordinator: " + err.Error() + "\n"))
		}
	}})
}

func (host *SessionHost) stopCoordinator() {
	host.coordinatorMu.Lock()
	host.coordinatorClosed = true
	engine := host.coordinator
	host.coordinatorMu.Unlock()
	if engine != nil {
		engine.Close()
	}
}

// SetCoordinatorDelivery connects manager turns to the application's normal
// run monitor and question/control channel.
func (host *SessionHost) SetCoordinatorDelivery(deliver func(context.Context, workspace.Store, string, string) error) {
	host.coordinatorMu.Lock()
	host.coordinatorDelivery = deliver
	host.coordinatorMu.Unlock()
}

func (host *SessionHost) coordinatorCandidates(ctx context.Context, project workitems.Workspace, item workitems.Item, members []workitems.Member) ([]coordinator.Candidate, error) {
	value, err := host.store.Load()
	if err != nil {
		return nil, err
	}
	out := []coordinator.Candidate{}
	for _, member := range members {
		if member.Disabled || item.RequestedMemberID != "" && item.RequestedMemberID != member.ID {
			continue
		}
		belongs := false
		for _, root := range project.WorkspaceRoots {
			if root == member.ExecutionRoot {
				belongs = true
			}
		}
		if !belongs {
			continue
		}
		if info, err := os.Stat(member.ExecutionRoot); err != nil || !info.IsDir() {
			continue
		}
		registry, err := buildSubagentRegistry((model{hostState: hostState{store: host.store}}).customStoreAt(member.DefinitionRoot))
		if err != nil {
			return nil, err
		}
		definition, found := registry.Get(member.AgentID)
		if !found || !registry.CanDelegate("", member.AgentID) {
			continue
		}
		if item.ExecutionMode == "report" && definition.Info.MutatesWorkspace {
			continue
		}
		var assignment any
		workspaceModels, err := (workspace.Store{Root: member.DefinitionRoot}).LoadModelConfig()
		if err != nil {
			return nil, err
		}
		effective := model{conversationState: conversationState{config: value}}
		effective.workspaceModel = workspaceModels
		memberConfig := effective.activeConfig()
		if definition.Info.Kind == subagent.AgentKindExternal {
			if definition.Connection == "" {
				id, _, available := value.ExternalAgentConnection(definition.Info.Role)
				if !available {
					continue
				}
				definition.Connection = id
			}
			connection, found := value.Agents.Connections[definition.Connection]
			if !found || connection.Disabled {
				continue
			}
			assignment = connection
		} else {
			role, err := memberConfig.EffectiveAgent(definition.Info.Role)
			if err != nil {
				continue
			}
			assignment = struct {
				Role    config.AgentConfig
				Groups  map[string]config.ModelGroupConfig
				Default config.ProviderConfig
			}{role, memberConfig.ModelGroups, memberConfig.Provider}
		}
		raw, err := json.Marshal([]any{definition, assignment, []string{member.ID, member.DefinitionRoot, member.ExecutionRoot}})
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		out = append(out, coordinator.Candidate{Member: member, Definition: definition, Digest: hex.EncodeToString(digest[:])})
	}
	return out, ctx.Err()
}

func ticketWorkerStore(directory string, a workitems.Attempt) workspace.Store {
	return workspace.Store{Root: filepath.Join(directory, "workspaces", a.WorkspaceID, "workers"), SessionID: a.WorkerSessionID}
}

// runTicket uses GeneralRunner and the existing delegation runtime. The worker
// owns a separate durable session; the manager receives only its final handoff.
func (host *SessionHost) runTicket(ctx context.Context, c *library.Client, project workitems.Workspace, item workitems.Item, candidate coordinator.Candidate, a workitems.Attempt, prompt string) (result subagent.TaskResult, returnErr error) {
	// Settings/profile changes between selection and execution invalidate the
	// reservation rather than switching capabilities beneath the chosen agent.
	current, err := host.coordinatorCandidates(ctx, project, item, []workitems.Member{candidate.Member})
	if err != nil {
		return result, err
	}
	if len(current) != 1 || current[0].Digest != a.DefinitionDigest {
		return result, errors.New("assigned profile or model configuration changed")
	}
	worker := ticketWorkerStore(host.store.Dir, a)
	if err := os.MkdirAll(worker.Root, 0o700); err != nil {
		return result, err
	}
	lock, err := worker.AcquireLock("q ticket worker")
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()
	root := candidate.Member.ExecutionRoot
	if err := (workspace.Store{Root: root}).EnsureQGitLocallyIgnored(ctx); err != nil {
		return result, err
	}
	worktrees := gitwork.Manager{Root: filepath.Join(host.store.Dir, "worktrees")}
	request := a.ChangeRequest
	// A worker checkpoint may have persisted a prepared worktree before the
	// Library acknowledged it. Recover that exact identity, never create another.
	if state, err := worker.LoadDelegationState(); err == nil && request == nil {
		request = state.ChangeRequest
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if item.ExecutionMode == "git" {
		if request == nil {
			prepared, err := worktrees.Prepare(ctx, root, a.WorktreeID)
			if err != nil {
				return result, err
			}
			request = &prepared
		} else if err := worktrees.Resume(ctx, *request); err != nil {
			return result, err
		}
		if a.PreviousAttemptID != "" {
			continuing := *request
			continuing.Status, continuing.HeadCommit = change.StatusWorking, ""
			request = &continuing
		}
		root = request.WorktreePath
	} else if candidate.Definition.Info.MutatesWorkspace {
		return result, errors.New("a mutating subagent requires Git execution mode")
	}
	state := workspace.DelegationState{Agent: candidate.Member.AgentID, Kind: candidate.Definition.Info.Kind, Prompt: prompt, WorkingDirectory: root, RunID: a.ID, TaskID: a.WorktreeID, Status: "running", ChangeRequest: request}
	if previous, err := worker.LoadDelegationState(); err == nil {
		state = previous
		state.ChangeRequest = request
		state.Status = "running"
		state.Result = nil
		state.RunID = a.ID
		state.ParentID = itemParent(a)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if err := worker.SaveDelegationState(state); err != nil {
		return result, err
	}
	if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running", ChangeRequest: request}); err != nil {
		return result, err
	}
	if candidate.Definition.Info.Kind == subagent.AgentKindExternal {
		result, returnErr = host.runACPTicket(ctx, c, worker, candidate, a, prompt, root)
	} else {
		result, returnErr = host.runNativeTicket(ctx, c, worker, candidate, a, prompt, root, &state)
	}
	if request != nil {
		if returnErr == nil && result.Outcome == "succeeded" {
			submitted, err := worktrees.Submit(ctx, *request, result.Summary)
			request = &submitted
			returnErr = errors.Join(returnErr, err)
		} else {
			blocked := worktrees.MarkBlocked(*request)
			request = &blocked
		}
		result.ChangeRequest = request
		state.ChangeRequest = request
	}
	raw, _ := json.Marshal(result)
	state.Result = &client.ToolResult{Content: string(raw), IsError: returnErr != nil}
	state.Status = "completed"
	if returnErr != nil {
		state.Status = "blocked"
	}
	returnErr = errors.Join(returnErr, worker.SaveDelegationState(state))
	return result, returnErr
}

func (host *SessionHost) runNativeTicket(ctx context.Context, c *library.Client, worker workspace.Store, candidate coordinator.Candidate, a workitems.Attempt, prompt, root string, state *workspace.DelegationState) (result subagent.TaskResult, returnErr error) {
	ctx = context.WithValue(ctx, delegationHostKey{}, host)
	value, err := host.store.Load()
	if err != nil {
		return result, err
	}
	value, err = host.ensureProviderContext(ctx, value)
	if err != nil {
		return result, err
	}
	configuredClient, err := host.factory(value)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, configuredClient.Close()) }()
	models, err := configuredClient.ListModels(ctx)
	if err != nil {
		return result, err
	}
	workspaceModels, err := (workspace.Store{Root: candidate.Member.DefinitionRoot}).LoadModelConfig()
	if err != nil {
		return result, err
	}
	effective := model{conversationState: conversationState{config: value}}
	effective.workspaceModel = workspaceModels
	value = effective.activeConfig()
	spec, err := subagent.Resolve(value, candidate.Definition.Info.Role, models)
	if err != nil {
		return result, err
	}
	memoryRuntime, err := workspacememory.Ensure(context.WithoutCancel(ctx), host.store.Dir)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, memoryRuntime.Close()) }()
	archiveStore, err := workspacememory.NewClient(memoryRuntime.Endpoint(), "", time.Minute).OpenWorkspace(ctx, candidate.Member.ExecutionRoot, sessionstore.VectorConfig{Model: value.Embedding.Model, Dimensions: value.Embedding.Dimensions})
	if err != nil {
		return result, err
	}
	archive := archiveembed.New(archiveStore)
	if value.Embedding.Model != "" {
		embedder, ok := configuredClient.(library.Embedder)
		if !ok {
			_ = archiveStore.Close()
			return result, errors.New("configured client does not support embeddings")
		}
		guard := &currentEmbeddingProvider{store: host.store, model: value.Embedding.Model, dimensions: value.Embedding.Dimensions, provider: embedder}
		if err := archive.ConfigureContext(ctx, guard, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
			_ = archiveStore.Close()
			return result, err
		}
		if err := c.ConfigureEmbedding(guard, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
			_ = archiveStore.Close()
			return result, err
		}
	}
	writer := sessionstore.NewWriterWithOptions(archiveStore, sessionstore.WriterOptions{Context: context.WithoutCancel(ctx), Prepare: archive.Prepare})
	defer func() { returnErr = errors.Join(returnErr, writer.Close()) }()
	lsp, err := (workspace.Store{Root: candidate.Member.ExecutionRoot}).LoadLSP()
	if err != nil {
		return result, err
	}
	base, err := qtools.NewRuntimeWithRoots(ctx, qtools.RuntimeRoots{WorkspaceStateRoot: candidate.Member.ExecutionRoot, CheckoutRoot: root}, archive, value.LoomStoreOptions(nil), value.LSP, lsp, c, qtools.WithWorkspaceWork(c, a.WorkspaceID, a.WorkerSessionID, false), qtools.WithSystemOneSkillRanking(systemoneconfig.Store{Dir: host.store.Dir}), qtools.WithSystemOneArchiveRanking(systemoneconfig.Store{Dir: host.store.Dir}))
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, base.Close()) }()
	external, err := (mcpconfig.Store{Dir: host.store.Dir}).LoadOrDefault()
	if err != nil {
		return result, err
	}
	base.ConfigureExternal(ctx, root, external)
	m := newRuntimeModel(ctx, host.store, host.factory, host.manager)
	m.config = value
	m.client = configuredClient
	m.workspaceStore = &worker
	m.libraryClient = c
	m.studioWorkspaceContext = &SessionWorkspaceContext{ProjectID: a.WorkspaceID}
	m.runID = a.ID
	m.archive, m.archiveSearch = writer, archive
	runtime, err := m.configuredDelegationRuntimeIn(base, executionWorkspace{sessionStore: &worker, workspaceStateRoot: candidate.Member.DefinitionRoot, checkoutRoot: root}, candidate.Member.AgentID, []string{candidate.Member.AgentID})
	if err != nil {
		return result, err
	}
	var resume *subagent.GeneralRunState
	if saved, err := worker.Load(); err == nil {
		if state.Model != "" {
			if state.APIMode != value.ModelAPIMode(state.Model) {
				return result, errors.New("saved worker model API mode changed")
			}
			checked, _, err := reconcileChildExecutionState(saved, *state, spec)
			if err != nil {
				return result, err
			}
			*state = checked
			resume = &subagent.GeneralRunState{Transcript: saved.Transcript, Context: restoreResponseReplay(saved.Context, saved.ResponseReplay), Round: state.Round, TurnStartRound: state.TurnStartRound, Started: state.Started, Reminders: state.Reminders, Spec: subagent.SpecCheckpoint{Model: state.Model, Candidate: state.Candidate, ConversationID: state.ConversationID}}
		}
	} else if !errors.Is(err, workspace.ErrNotFound) {
		return result, err
	}
	checkpoint := func(saved subagent.GeneralRunState) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running", ChangeRequest: state.ChangeRequest}); err != nil {
			return err
		}
		if err := worker.Save(workspace.Session{RunID: a.ID, Transcript: saved.Transcript, Context: saved.Context, ResponseReplay: collectResponseReplay(saved.Context), ResponseAffinity: &workspace.ResponseAffinity{Model: saved.Spec.Model, Key: saved.Spec.ConversationID, APIMode: value.ModelAPIMode(saved.Spec.Model), Candidate: saved.Spec.Candidate}}); err != nil {
			return err
		}
		state.Round, state.TurnStartRound, state.Started, state.Reminders = saved.Round, saved.TurnStartRound, saved.Started, saved.Reminders
		state.Model, state.Candidate, state.ConversationID = saved.Spec.Model, saved.Spec.Candidate, saved.Spec.ConversationID
		state.RunningCall, state.UnknownTools = delegatedToolOutcomes(saved.Transcript)
		state.APIMode = value.ModelAPIMode(saved.Spec.Model)
		return worker.SaveDelegationState(*state)
	}
	followup := ""
	if resume != nil {
		followup = prompt
	}
	return (subagent.GeneralRunner{Client: configuredClient, Tools: runtime, Spec: spec, Definition: candidate.Definition, WorkingDirectory: root, Environment: fmt.Sprintf("%+v", base.Environment()), RunID: a.ID, TaskID: a.WorktreeID, ParentID: itemParent(a), Resume: resume, Checkpoint: checkpoint, Followup: followup, Sink: writer}).Run(ctx, state.Prompt)
}
func itemParent(a workitems.Attempt) string { return "ticket/" + a.TicketID }

type ticketACPCheckpoint struct {
	Agent, SessionID, Root, Digest, AttemptID string
	Started, Completed                        bool
	Answer                                    string
}

func (host *SessionHost) runACPTicket(ctx context.Context, c *library.Client, worker workspace.Store, candidate coordinator.Candidate, a workitems.Attempt, prompt, root string) (result subagent.TaskResult, returnErr error) {
	value, err := host.store.Load()
	if err != nil {
		return result, err
	}
	connection, found := value.Agents.Connections[candidate.Definition.Connection]
	if !found || connection.Disabled {
		return result, errors.New("assigned ACP connection is unavailable")
	}
	command, err := resolveConfiguredACPAgentCommand(connection, exec.LookPath)
	if err != nil {
		return result, err
	}
	bridgePath, err := host.workEmbeddingConnection()
	if err != nil {
		return result, err
	}
	command.mcpServers, err = acpLibraryServers(host.store.Dir, workitemtools.Binding{WorkspaceID: a.WorkspaceID, SessionID: a.WorkerSessionID, EmbeddingConnection: bridgePath})
	if err != nil {
		return result, err
	}
	path := filepath.Join(worker.SessionDir(), "ticket-acp.json")
	saved := ticketACPCheckpoint{Agent: candidate.Definition.Connection, Root: root, Digest: a.DefinitionDigest}
	recovering := false
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &saved); err != nil {
			return result, err
		}
		if saved.Agent != candidate.Definition.Connection || saved.Root != root || saved.Digest != a.DefinitionDigest {
			return result, errors.New("ACP worker checkpoint configuration changed")
		}
		if saved.Started && !saved.Completed {
			if saved.AttemptID == a.ID || a.PreviousAttemptID != saved.AttemptID || strings.TrimSpace(a.Feedback) == "" {
				return result, errors.New("ACP worker prompt has an unknown outcome; manager feedback and real session resume are required")
			}
			recovering = true
		}
		if saved.AttemptID == a.ID && saved.Completed {
			return subagent.TaskResult{Outcome: "succeeded", Summary: "ACP worker completed", Report: saved.Answer}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	remote, err := startACPRemoteClientWithResume(ctx, command, root, saved.SessionID, connection.AuthMethod, io.Discard, false)
	if err != nil {
		return result, err
	}
	defer func() { returnErr = errors.Join(returnErr, remote.Close()) }()
	remote.permissions = acpPermissionReadOnly
	if candidate.Definition.Info.MutatesWorkspace {
		remote.permissions = acpPermissionAutomatic
	}
	remote.requireCompletedText = true
	saved.SessionID = string(remote.sessionID)
	saved.AttemptID = a.ID
	saved.Started = true
	saved.Completed = false
	raw, _ := json.Marshal(saved)
	if err := fsreplace.WriteFile(path, raw, 0o600); err != nil {
		return result, err
	}
	if _, err := c.UpdateAttempt(ctx, a.ID, workitems.AttemptUpdate{Owner: a.Owner, Token: a.Token, Generation: a.Generation, Status: "running"}); err != nil {
		return result, err
	}
	if !remote.resumed {
		prompt = externalSubagentPrompt(candidate.Definition.SystemPrompt, prompt)
	}
	if recovering {
		prompt = "The previous prompt has an unknown outcome. Do not replay it or assume its effects failed. Inspect the persisted agent state and current files first. Continue only according to this manager feedback, and report any remaining uncertainty.\nTicket ID: " + a.TicketID + "\n\nManager feedback:\n" + a.Feedback
	}
	answer, err := remote.promptText(ctx, prompt)
	if err != nil {
		return result, err
	}
	saved.Completed = true
	saved.Answer = answer
	raw, _ = json.Marshal(saved)
	if err := fsreplace.WriteFile(path, raw, 0o600); err != nil {
		return result, err
	}
	user := client.Message{Role: client.RoleUser, Content: prompt}
	assistant := client.Message{Role: client.RoleAssistant, Content: answer}
	session, err := worker.Load()
	if errors.Is(err, workspace.ErrNotFound) {
		session = workspace.Session{}
	} else if err != nil {
		return result, err
	}
	session.Transcript = append(session.Transcript, user, assistant)
	session.Context = append(session.Context, user, assistant)
	if err := worker.Save(session); err != nil {
		return result, err
	}
	return subagent.TaskResult{Outcome: "succeeded", Summary: "ACP worker completed", Report: strings.TrimSpace(answer)}, nil
}

func (host *SessionHost) deliverTicketReview(ctx context.Context, project workitems.Workspace, entry workitems.InboxEntry, a workitems.Attempt) error {
	store, err := coordinator.EnsureManager(ctx, host.store.Dir, project)
	if err != nil {
		return err
	}
	prompt := fmt.Sprintf("Ticket result ready for manager review.\nTicket ID: %s\nAttempt ID: %s\nWorker session: %s\nStatus: %s\n\nUse ticket_attempt_get and ticket_change_read to inspect the durable result and pinned diff. Review against the ticket acceptance criteria. Use ticket_review with accept, request_changes (include feedback), or reject. A Git accept queues the actual pinned merge; the ticket is done only after it succeeds.\n\nResult excerpt:\n%s", entry.TicketID, a.ID, a.WorkerSessionID, a.Status, truncateTicketResult(string(a.Result)))
	host.coordinatorMu.Lock()
	deliver := host.coordinatorDelivery
	host.coordinatorMu.Unlock()
	if deliver != nil {
		return deliver(ctx, store, entry.OperationID, prompt)
	}
	return host.RunWithOptions(ctx, store, store.SessionID, prompt, SessionOptions{OperationID: entry.OperationID}, nil)
}
func truncateTicketResult(raw string) string {
	r := []rune(raw)
	if len(r) > 8000 {
		return string(r[:8000]) + "\n[read attempt for full result]"
	}
	return raw
}

func (host *SessionHost) SearchWork(ctx context.Context, in library.WorkSearchRequest) (out library.WorkSearchResponse, returnErr error) {
	runtime, err := library.Ensure(ctx, host.store.Dir)
	if err != nil {
		return out, err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	c := runtime.Client()
	embeddingUnavailable := func(cause error) (library.WorkSearchResponse, error) {
		if in.Mode == "semantic" {
			return out, errors.Join(library.ErrSemanticUnavailable, cause)
		}
		return c.SearchWork(ctx, in)
	}
	if in.Mode != "lexical" {
		value, err := host.store.Load()
		if err != nil {
			return out, err
		}
		if value.Embedding.Model != "" {
			value, err = host.ensureProviderContext(ctx, value)
			if err != nil {
				return embeddingUnavailable(err)
			}
			provider, err := host.factory(value)
			if err != nil {
				return embeddingUnavailable(err)
			}
			defer func() { returnErr = errors.Join(returnErr, provider.Close()) }()
			embedder, ok := provider.(library.Embedder)
			if !ok {
				return embeddingUnavailable(errors.New("configured client does not support embeddings"))
			}
			if err := c.ConfigureEmbedding(&currentEmbeddingProvider{store: host.store, model: value.Embedding.Model, dimensions: value.Embedding.Dimensions, provider: embedder}, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
				return out, err
			}
		}
	}
	return c.SearchWork(ctx, in)
}

func (host *SessionHost) embedWorkItems(ctx context.Context) (returnErr error) {
	value, err := host.store.Load()
	if err != nil {
		return err
	}
	if value.Embedding.Model == "" {
		return nil
	}
	runtime, err := library.Ensure(ctx, host.store.Dir)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, runtime.Close()) }()
	value, err = host.ensureProviderContext(ctx, value)
	if err != nil {
		return err
	}
	if count, err := runtime.Client().PendingWorkEmbeddings(ctx); err != nil || count == 0 {
		return err
	}
	provider, err := host.factory(value)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, provider.Close()) }()
	embedder, ok := provider.(library.Embedder)
	if !ok {
		return errors.New("configured client does not support embeddings")
	}
	c := runtime.Client()
	if err := c.ConfigureEmbedding(&currentEmbeddingProvider{store: host.store, model: value.Embedding.Model, dimensions: value.Embedding.Dimensions, provider: embedder}, value.Embedding.Model, value.Embedding.Dimensions); err != nil {
		return err
	}
	_, err = c.SyncWorkEmbeddings(ctx)
	return err
}
