<script lang="ts">
  import { ArrowUp, Bot, BrainCircuit, ChevronUp, Eraser, Folder, FolderOpen, Folders, GitBranch, GitCompareArrows, HardDrive, Home, Minimize2, Pause, Play, Plus, RefreshCw, Square, Terminal, Trash2, User, Wrench, X } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';
  import Markdown from './Markdown.svelte';

  export let openChanges: (workspaceRoot: string) => void = () => {};

  type SessionSummary = {
    session_id: string;
    run_id?: string;
    title?: string;
    updated_at: string;
  };
  type ToolCall = { id: string; function?: { name?: string; arguments?: string } };
  type Message = {
    role: string;
    content?: string;
    name?: string;
    tool_call_id?: string;
    tool_calls?: ToolCall[];
  };
  type SessionDetail = {
    workspace_root: string;
    session: SessionSummary;
    transcript: Message[];
    active_task?: { objective: string; completion_criteria?: string[]; started_at: string };
  };
  type RunEvent = {
    type: string;
    run_id?: string;
    session_id?: string;
    kind?: string;
    start?: boolean;
    content?: string;
    detail?: string;
    name?: string;
    role?: string;
    agent?: string;
    task_id?: string;
    parent_id?: string;
    action?: string;
    is_error?: boolean;
    question?: string;
    context?: string;
    outcome?: string;
    call_id?: string;
    context_used?: number;
    context_size?: number;
  };
  type RunQuestion = { call_id: string; question: string; context?: string; choices?: { id: string; label: string; description?: string }[] };
  type RunSnapshot = {
    id: string;
    session_id: string;
    status: string;
    outcome?: string;
    error?: string;
    pending_question?: RunQuestion;
    context_used?: number;
    context_size?: number;
    cursor: number;
  };
  type RunEventEnvelope = { cursor: number; at: string; event: RunEvent };
  type RunPage = { run: RunSnapshot; events: RunEventEnvelope[]; next_cursor: number };
  type ChangeRequest = {
    id: string;
    repository_root: string;
    worktree_path?: string;
    base_ref: string;
    base_commit: string;
    head_ref: string;
    head_commit?: string;
    merged_commit?: string;
    status: string;
  };
  type DelegationNode = {
    bookmark: { invocation_id: string; agent: string; prompt: string; working_directory?: string; task_id?: string; parent_id?: string };
    state?: { status: string; task_id?: string; parent_id?: string; model?: string; running_call?: { name: string; call_id: string }; unknown_tools?: { name: string; call_id: string }[]; change_request?: ChangeRequest };
    transcript?: Message[];
    children?: DelegationNode[];
    issue?: string;
  };
  type FlatDelegation = DelegationNode & { depth: number; path: string };
  type RegisteredSessionTree = {
    registration_id: string;
    workspace_root: string;
    project_id?: string;
    project_name?: string;
    session: SessionSummary;
    registered_at: string;
    delegations?: DelegationNode[];
    issue?: string;
  };
  type StudioProject = {
    id: string;
    name: string;
    workspace_roots: string[];
    created_at: string;
    updated_at: string;
  };
  type DirectoryEntry = { name: string; path: string };
  type DirectoryListing = {
    home: string;
    current: string;
    parent?: string;
    roots: string[];
    directories: DirectoryEntry[];
    can_select: boolean;
  };

  let workspaceInput = '';
  let workspaceRoot = '';
  let sessions: SessionSummary[] = [];
  let registeredSessions: RegisteredSessionTree[] = [];
  let projects: StudioProject[] = [];
  let selectedRegistration: RegisteredSessionTree | null = null;
  let selectedDelegation: FlatDelegation | null = null;
  let selected: SessionDetail | null = null;
  let messages: Message[] = [];
  let prompt = '';
  let workspaceLoading = false;
  let sessionLoading = false;
  let sending = false;
  let error = '';
  let runStatus = '';
  let events: RunEvent[] = [];
  let responseDraft = '';
  let thinkingDraft = '';
  let activeRun: RunSnapshot | null = null;
  let runCursor = 0;
  let pollGeneration = 0;
  let questionAnswer = '';
  let delegations: FlatDelegation[] = [];
  let delegationRefresh: ReturnType<typeof setTimeout> | null = null;
  let transcriptElement: HTMLElement | null = null;
  let directoryPickerOpen = false;
  let directoryLoading = false;
  let directoryError = '';
  let directoryPathInput = '';
  let directoryListing: DirectoryListing | null = null;
  let learningEnabled = true;
  let learningLoading = false;
  let registrationDialogOpen = false;
  let projectDialogOpen = false;
  let projectSaving = false;
  let projectDialogError = '';
  let editingProjectID = '';
  let projectName = '';
  let projectRootInput = '';
  let projectRoots: string[] = [];
  let directoryPickerTarget: 'registration' | 'project' = 'registration';

  function sessionFromLocation() {
    const match = window.location.pathname.match(/^\/sessions\/([^/]+)$/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  async function apiError(response: Response) {
    try {
      const body = (await response.json()) as { error?: string };
      return body.error || `Studio returned ${response.status}`;
    } catch {
      return `Studio returned ${response.status}`;
    }
  }

  async function loadWorkspaceCandidates() {
    const requested = workspaceInput.trim();
    if (!requested) return;
    workspaceLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(requested)}`);
      if (!response.ok) throw new Error(await apiError(response));
      const result = (await response.json()) as { workspace_root: string; sessions: SessionSummary[] };
      workspaceInput = result.workspace_root;
      sessions = result.sessions;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not inspect the repository';
    } finally {
      workspaceLoading = false;
    }
  }

  async function chooseWorkspace() {
    if (directoryLoading) return;
    directoryPickerTarget = 'registration';
    directoryPickerOpen = true;
    directoryListing = null;
    directoryPathInput = '';
    directoryError = '';
    await browseDirectory();
  }

  async function chooseProjectWorkspace() {
    if (directoryLoading) return;
    directoryPickerTarget = 'project';
    directoryPickerOpen = true;
    directoryListing = null;
    directoryPathInput = '';
    directoryError = '';
    await browseDirectory();
  }

  async function browseDirectory(path = '') {
    directoryLoading = true;
    directoryError = '';
    try {
      const query = path.trim() ? `?path=${encodeURIComponent(path.trim())}` : '';
      const response = await fetch(`/api/v1/directories${query}`, { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await apiError(response));
      directoryListing = (await response.json()) as DirectoryListing;
      directoryPathInput = directoryListing.current;
    } catch (cause) {
      directoryError = cause instanceof Error ? cause.message : 'Could not browse this directory';
    } finally {
      directoryLoading = false;
    }
  }

  function closeDirectoryPicker() {
    directoryPickerOpen = false;
    directoryError = '';
  }

  async function selectBrowsedDirectory() {
    if (!directoryListing?.can_select) return;
    if (directoryPickerTarget === 'project') {
      projectRootInput = directoryListing.current;
      closeDirectoryPicker();
      return;
    }
    workspaceInput = directoryListing.current;
    closeDirectoryPicker();
    await loadWorkspaceCandidates();
  }

  async function refreshSessions() {
    await loadRegisteredSessions(selectedRegistration?.registration_id || '');
  }

  async function registerSession(sessionID = '', create = false) {
    const root = workspaceInput.trim();
    if (!root) return;
    sessionLoading = true;
    pollGeneration += 1;
    error = '';
    try {
      const response = await fetch('/api/v1/registered-sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: root, session_id: sessionID, create })
      });
      if (!response.ok) throw new Error(await apiError(response));
      const registration = (await response.json()) as RegisteredSessionTree;
      registrationDialogOpen = false;
      sessions = [];
      await loadRegisteredSessions(registration.registration_id);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not register the session';
    } finally {
      sessionLoading = false;
    }
  }

  function openRegistrationDialog() {
    registrationDialogOpen = true;
    workspaceInput = '';
    sessions = [];
    error = '';
  }

  function openProjectDialog(project?: StudioProject) {
    const selectedProject = project || projects.find((candidate) => candidate.id === selectedRegistration?.project_id);
    editingProjectID = selectedProject?.id || '';
    projectName = selectedProject?.name || '';
    projectRoots = selectedProject ? [...selectedProject.workspace_roots] : (workspaceRoot ? [workspaceRoot] : []);
    projectRootInput = '';
    projectDialogError = '';
    projectDialogOpen = true;
    error = '';
  }

  function addProjectWorkspace() {
    const value = projectRootInput.trim();
    if (!value) return;
    if (!projectRoots.includes(value)) {
      projectRoots = [...projectRoots, value];
    }
    projectRootInput = '';
  }

  function removeProjectWorkspace(index: number) {
    projectRoots = projectRoots.filter((_, candidate) => candidate !== index);
  }

  async function saveProject() {
    if (projectSaving) return;
    addProjectWorkspace();
    if (!projectName.trim() || !projectRoots.length) return;
    projectSaving = true;
    projectDialogError = '';
    try {
      const target = editingProjectID ? `/api/v1/projects/${encodeURIComponent(editingProjectID)}` : '/api/v1/projects';
      const response = await fetch(target, {
        method: editingProjectID ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ name: projectName.trim(), workspace_roots: projectRoots })
      });
      if (!response.ok) throw new Error(await apiError(response));
      projectDialogOpen = false;
      await loadRegisteredSessions(selectedRegistration?.registration_id || '');
      runStatus = editingProjectID ? 'Project updated' : 'Project created';
    } catch (cause) {
      projectDialogError = cause instanceof Error ? cause.message : 'Could not save the project';
    } finally {
      projectSaving = false;
    }
  }

  async function deleteProject() {
    if (!editingProjectID || projectSaving) return;
    if (!window.confirm(`Delete project ${projectName}? Its sessions and workspace data will remain available as independents.`)) return;
    projectSaving = true;
    projectDialogError = '';
    try {
      const response = await fetch(`/api/v1/projects/${encodeURIComponent(editingProjectID)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      projectDialogOpen = false;
      await loadRegisteredSessions(selectedRegistration?.registration_id || '');
      runStatus = 'Project deleted';
    } catch (cause) {
      projectDialogError = cause instanceof Error ? cause.message : 'Could not delete the project';
    } finally {
      projectSaving = false;
    }
  }

  function projectSessions(projectID: string) {
    return registeredSessions.filter((registration) => registration.project_id === projectID);
  }

  function independentSessions() {
    return registeredSessions.filter((registration) => !registration.project_id);
  }

  async function clearSession() {
    if (!selected || !workspaceRoot || sending) return;
    if (!window.confirm('Clear this conversation? Its durable archive records will remain available.')) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/clear`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot })
      });
      if (!response.ok) throw new Error(await apiError(response));
      selected = (await response.json()) as SessionDetail;
      messages = selected.transcript;
      events = [];
      responseDraft = '';
      thinkingDraft = '';
      activeRun = null;
      runCursor = 0;
      delegations = [];
      runStatus = 'Conversation cleared';
      await refreshSessionListOnly();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not clear the conversation';
    } finally {
      sessionLoading = false;
    }
  }

  async function compactSession() {
    if (!selected || !workspaceRoot || sending || sessionLoading) return;
    sessionLoading = true;
    error = '';
    runStatus = 'Compacting context…';
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/compact`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot })
      });
      if (!response.ok) throw new Error(await apiError(response));
      runStatus = ((await response.json()) as { status?: string }).status || 'Context compacted';
      await reloadSelected();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not compact the context';
      runStatus = 'Compaction failed';
    } finally {
      sessionLoading = false;
    }
  }

  function clearSelectedSession() {
    pollGeneration += 1;
    selectedRegistration = null;
    selectedDelegation = null;
    selected = null;
    workspaceRoot = '';
    messages = [];
    events = [];
    activeRun = null;
    sending = false;
    delegations = [];
  }

  async function loadRegisteredSessions(preferredRegistration = '') {
    workspaceLoading = true;
    error = '';
    try {
      const response = await fetch('/api/v1/registered-sessions', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await apiError(response));
      const result = (await response.json()) as { projects?: StudioProject[]; sessions: RegisteredSessionTree[] };
      projects = result.projects || [];
      registeredSessions = result.sessions;
      const targetID = preferredRegistration || selectedRegistration?.registration_id || sessionFromLocation();
      const target = registeredSessions.find((registration) => registration.registration_id === targetID && !registration.issue)
        || registeredSessions.find((registration) => !registration.issue);
      if (target) {
        await selectRegisteredSession(target, false);
        window.history.replaceState({}, '', `/sessions/${encodeURIComponent(target.registration_id)}`);
      } else {
        clearSelectedSession();
        window.history.replaceState({}, '', '/sessions');
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load registered sessions';
    } finally {
      workspaceLoading = false;
    }
  }

  async function unregisterSession(registration: RegisteredSessionTree) {
    if (!window.confirm(`Remove ${registration.session.title || 'this session'} from Studio? The workspace session will remain on disk.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/registered-sessions/${encodeURIComponent(registration.registration_id)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      registeredSessions = registeredSessions.filter((item) => item.registration_id !== registration.registration_id);
      if (selectedRegistration?.registration_id === registration.registration_id) {
        const next = registeredSessions.find((item) => !item.issue);
        if (next) await selectRegisteredSession(next, false);
        else clearSelectedSession();
        window.history.replaceState({}, '', next ? `/sessions/${encodeURIComponent(next.registration_id)}` : '/sessions');
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not unregister the session';
    } finally {
      sessionLoading = false;
    }
  }

  async function deleteSession() {
    if (!selected || !selectedRegistration || !workspaceRoot || sending || selectedDelegation) return;
    if (!window.confirm(`Delete ${selected.session.title || 'this session'} from ${workspaceRoot}? Its durable archive records will remain available.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}?workspace_root=${encodeURIComponent(workspaceRoot)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      await fetch(`/api/v1/registered-sessions/${encodeURIComponent(selectedRegistration.registration_id)}`, { method: 'DELETE' });
      registeredSessions = registeredSessions.filter((item) => item.registration_id !== selectedRegistration?.registration_id);
      const next = registeredSessions.find((item) => !item.issue);
      if (next) await selectRegisteredSession(next, false);
      else clearSelectedSession();
      window.history.replaceState({}, '', next ? `/sessions/${encodeURIComponent(next.registration_id)}` : '/sessions');
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the session';
    } finally {
      sessionLoading = false;
    }
  }

  async function deleteDelegation() {
    if (!selected || !selectedDelegation || !workspaceRoot || selectedDelegation.state?.status !== 'completed' || sessionLoading || sending) return;
    const agent = selectedDelegation.bookmark.agent.replace(/^builtin\//, '');
    if (!window.confirm(`Delete the completed ${agent} delegation? Its child transcript and descendants will be removed; the parent conversation will remain.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const query = new URLSearchParams({ workspace_root: workspaceRoot, path: selectedDelegation.path });
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/delegations?${query}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      selectedDelegation = null;
      messages = selected.transcript;
      events = [];
      responseDraft = '';
      thinkingDraft = '';
      runStatus = 'Completed delegation deleted';
      await loadDelegations(selected.session.session_id);
      await scrollToBottom();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the delegation';
    } finally {
      sessionLoading = false;
    }
  }

  async function loadLearning() {
    if (!workspaceRoot) return;
    learningLoading = true;
    try {
      const response = await fetch(`/api/v1/workspaces/learning?workspace_root=${encodeURIComponent(workspaceRoot)}`);
      if (!response.ok) throw new Error(await apiError(response));
      learningEnabled = ((await response.json()) as { enabled: boolean }).enabled;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load learning settings';
    } finally {
      learningLoading = false;
    }
  }

  async function toggleLearning() {
    if (!workspaceRoot || learningLoading) return;
    learningLoading = true;
    error = '';
    try {
      const response = await fetch('/api/v1/workspaces/learning', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, disabled: learningEnabled })
      });
      if (!response.ok) throw new Error(await apiError(response));
      learningEnabled = ((await response.json()) as { enabled: boolean }).enabled;
      runStatus = `Learning ${learningEnabled ? 'enabled' : 'disabled'}`;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not update learning settings';
    } finally {
      learningLoading = false;
    }
  }

  async function selectRegisteredSession(registration: RegisteredSessionTree, push = true) {
    pollGeneration += 1;
    sessionLoading = true;
    error = '';
    try {
      selectedRegistration = registration;
      selectedDelegation = null;
      workspaceRoot = registration.workspace_root;
      const sessionID = registration.session.session_id;
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}?workspace_root=${encodeURIComponent(workspaceRoot)}`);
      if (!response.ok) throw new Error(await apiError(response));
      selected = (await response.json()) as SessionDetail;
      messages = selected.transcript;
      events = [];
      runStatus = '';
      responseDraft = '';
      thinkingDraft = '';
      activeRun = null;
      runCursor = 0;
      questionAnswer = '';
      if (push) window.history.pushState({}, '', `/sessions/${encodeURIComponent(registration.registration_id)}`);
      await Promise.all([loadLearning(), reconnectLatestRun(sessionID), loadDelegations(sessionID)]);
      await scrollToBottom();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load the session';
    } finally {
      sessionLoading = false;
    }
  }

  function selectDelegatedSession(registration: RegisteredSessionTree, node: FlatDelegation) {
    if (selectedRegistration?.registration_id !== registration.registration_id) return;
    selectedDelegation = node;
    messages = node.transcript || [];
    events = [];
    responseDraft = '';
    thinkingDraft = '';
    runStatus = node.state?.status || 'Recorded';
    void scrollToBottom();
  }

  async function sendPrompt() {
    const content = prompt.trim();
    if (!selected || !workspaceRoot || !content) return;
    if (sending && activeRun) {
      await commandRun('guidance', { content });
      if (!error) {
        prompt = '';
        runStatus = 'Guidance queued…';
      }
      return;
    }
    prompt = '';
    sending = true;
    error = '';
    runStatus = 'Starting default loop…';
    events = [];
    responseDraft = '';
    thinkingDraft = '';
    messages = [...messages, { role: 'user', content }];
    await scrollToBottom();
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, content })
      });
      if (!response.ok) throw new Error(await apiError(response));
      activeRun = (await response.json()) as RunSnapshot;
      runCursor = 0;
      const generation = ++pollGeneration;
      void pollRun(activeRun.id, generation);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'The turn failed';
      runStatus = 'Turn failed';
      await reloadSelected();
      sending = false;
      await scrollToBottom();
    }
  }

  function terminalRun(status: string) {
    return ['completed', 'cancelled', 'failed', 'interrupted', 'redirected'].includes(status);
  }

  async function reconnectLatestRun(sessionID: string) {
    if (!workspaceRoot) return;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/runs/latest?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (response.status === 404) { sending = false; return; }
    if (!response.ok) throw new Error(await apiError(response));
    activeRun = (await response.json()) as RunSnapshot;
    sending = !terminalRun(activeRun.status);
    runStatus = runStatusLabel(activeRun);
    runCursor = 0;
    const generation = ++pollGeneration;
    void pollRun(activeRun.id, generation);
  }

  async function pollRun(runID: string, generation: number) {
    if (!selected || !workspaceRoot) return;
    const sessionID = selected.session.session_id;
    let currentRunID = runID;
    while (generation === pollGeneration && selected?.session.session_id === sessionID) {
      try {
        const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/runs/${encodeURIComponent(currentRunID)}/events?workspace_root=${encodeURIComponent(workspaceRoot)}&after=${runCursor}&wait_ms=25000`);
        if (!response.ok) throw new Error(await apiError(response));
        const page = (await response.json()) as RunPage;
        activeRun = page.run;
        for (const record of page.events) {
          handleRunEvent(record.event);
          runCursor = record.cursor;
          if (record.event.type === 'redirect' && record.event.run_id) {
            currentRunID = record.event.run_id;
            runCursor = 0;
            activeRun = null;
            events = [];
            responseDraft = '';
            thinkingDraft = '';
            break;
          }
        }
        if (currentRunID !== page.run.id) continue;
        sending = !terminalRun(page.run.status);
        runStatus = runStatusLabel(page.run);
        if (terminalRun(page.run.status)) {
          responseDraft = '';
          thinkingDraft = '';
          await Promise.all([reloadSelected(), refreshSessionListOnly(), loadDelegations(sessionID)]);
          break;
        }
      } catch (cause) {
        if (generation !== pollGeneration) return;
        error = cause instanceof Error ? cause.message : 'Could not reconnect to the run';
        runStatus = 'Connection lost · retrying…';
        await new Promise((resolve) => setTimeout(resolve, 1000));
      }
    }
  }

  function runStatusLabel(run: RunSnapshot) {
    if (run.status === 'waiting') return 'Waiting for your answer';
    if (run.status === 'paused') return 'Paused';
    if (run.status === 'running' || run.status === 'queued') return 'Running';
    if (run.status === 'redirecting') return 'Applying guidance…';
    if (run.status === 'completed') return run.outcome === 'succeeded' ? 'Completed' : run.outcome || 'Completed';
    if (run.status === 'cancelled') return 'Turn stopped';
    if (run.status === 'interrupted') return 'Interrupted · send a message to recover';
    if (run.status === 'failed') return run.error || 'Turn failed';
    return run.status;
  }

  function handleRunEvent(event: RunEvent) {
    if (event.type === 'stream') {
      if (event.kind === 'thinking') thinkingDraft = (event.start ? '' : thinkingDraft) + (event.content || '');
      if (event.kind === 'response') responseDraft = (event.start ? '' : responseDraft) + (event.content || '');
    } else if (event.type === 'status') {
      runStatus = event.detail || 'Working…';
    } else if (event.type === 'activity') {
      events = [...events, event];
      runStatus = [event.agent, event.action, event.detail].filter(Boolean).join(' · ');
      scheduleDelegationRefresh();
    } else if (event.type === 'tool_call' || event.type === 'trace' || event.type === 'question') {
      events = [...events, event];
      if (event.type === 'tool_call') runStatus = `Running ${event.name || 'tool'}…`;
      if (event.type === 'question') runStatus = 'Waiting for your answer';
      if (event.type === 'trace') scheduleDelegationRefresh();
    } else if (event.type === 'message' && event.role === 'tool') {
      events = [...events, event];
    } else if (event.type === 'result') {
      runStatus = event.outcome === 'succeeded' ? 'Completed' : event.outcome || 'Completed';
    } else if (event.type === 'error') {
      error = event.detail || 'The turn failed';
      runStatus = 'Turn failed';
    }
    void scrollToBottom();
  }

  async function reloadSelected() {
    if (!selected || !workspaceRoot) return;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    selected = (await response.json()) as SessionDetail;
    if (!selectedDelegation) messages = selected.transcript;
  }

  function flattenDelegations(nodes: DelegationNode[], depth = 0, parentPath = ''): FlatDelegation[] {
    return nodes.flatMap((node) => {
      const path = parentPath ? `${parentPath}/${node.bookmark.invocation_id}` : node.bookmark.invocation_id;
      return [{ ...node, depth, path }, ...flattenDelegations(node.children || [], depth + 1, path)];
    });
  }

  async function loadDelegations(sessionID = selected?.session.session_id || '') {
    if (!workspaceRoot || !sessionID) return;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/delegations?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    const result = (await response.json()) as { nodes: DelegationNode[] };
    delegations = flattenDelegations(result.nodes);
    if (selectedRegistration) {
      const updated = { ...selectedRegistration, delegations: result.nodes };
      selectedRegistration = updated;
      registeredSessions = registeredSessions.map((registration) => registration.registration_id === updated.registration_id ? updated : registration);
    }
  }

  function scheduleDelegationRefresh() {
    if (delegationRefresh) clearTimeout(delegationRefresh);
    delegationRefresh = setTimeout(() => { void loadDelegations(); }, 500);
  }

  async function refreshSessionListOnly() {
    const response = await fetch('/api/v1/registered-sessions');
    if (!response.ok) return;
    const result = (await response.json()) as { projects?: StudioProject[]; sessions: RegisteredSessionTree[] };
    projects = result.projects || [];
    registeredSessions = result.sessions;
    if (selectedRegistration) {
      selectedRegistration = registeredSessions.find((registration) => registration.registration_id === selectedRegistration?.registration_id) || selectedRegistration;
    }
  }

  async function commandRun(action: string, extra: Record<string, string> = {}) {
    if (!selected || !workspaceRoot || !activeRun) return;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/runs/${encodeURIComponent(activeRun.id)}/commands`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, action, ...extra })
      });
      if (!response.ok) throw new Error(await apiError(response));
      activeRun = (await response.json()) as RunSnapshot;
      sending = !terminalRun(activeRun.status);
      runStatus = runStatusLabel(activeRun);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not control this turn';
    }
  }

  async function answerQuestion(answer = questionAnswer) {
    if (!activeRun?.pending_question) return;
    const value = answer.trim();
    if (!value) return;
    await commandRun('answer', { call_id: activeRun.pending_question.call_id, answer: value });
    if (!error) questionAnswer = '';
  }

  function stopTurn() {
    void commandRun('cancel');
  }

  function submitFromKeyboard(event: KeyboardEvent) {
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void sendPrompt();
    }
  }

  function formatSessionTime(value: string) {
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) return '';
    return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
  }

  function shortID(value: string) {
    return value.length > 10 ? value.slice(0, 10) : value;
  }

  function formatTokenCount(value = 0) {
    if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}m`;
    if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
    return new Intl.NumberFormat().format(value);
  }

  function contextPercent(run: RunSnapshot) {
    if (!run.context_size) return 0;
    return Math.min(999, Math.floor((run.context_used || 0) * 100 / run.context_size));
  }

  function eventTitle(event: RunEvent) {
    if (event.type === 'tool_call') return event.name || 'Tool call';
    if (event.type === 'trace') return [event.agent, event.name || event.kind].filter(Boolean).join(' · ');
    if (event.type === 'activity') return [event.agent, event.action].filter(Boolean).join(' · ');
    if (event.type === 'question') return 'Question';
    return event.name || event.type;
  }

  function eventBody(event: RunEvent) {
    return event.detail || event.question || event.content || '';
  }

  function toolArguments(value?: string) {
    if (!value) return '';
    try {
      return `\`\`\`json\n${JSON.stringify(JSON.parse(value), null, 2)}\n\`\`\``;
    } catch {
      return `\`\`\`text\n${value}\n\`\`\``;
    }
  }

  async function scrollToBottom() {
    await tick();
    transcriptElement?.scrollTo({ top: transcriptElement.scrollHeight, behavior: 'smooth' });
  }

  onMount(() => {
    void loadRegisteredSessions(sessionFromLocation());
    const onPopState = () => {
      const registrationID = sessionFromLocation();
      const registration = registeredSessions.find((item) => item.registration_id === registrationID && !item.issue);
      if (registration && registration.registration_id !== selectedRegistration?.registration_id) void selectRegisteredSession(registration, false);
      if (!registrationID) clearSelectedSession();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && directoryPickerOpen) closeDirectoryPicker();
      else if (event.key === 'Escape' && projectDialogOpen) projectDialogOpen = false;
      else if (event.key === 'Escape' && registrationDialogOpen) registrationDialogOpen = false;
    };
    window.addEventListener('popstate', onPopState);
    window.addEventListener('keydown', onKeyDown);
    return () => {
      pollGeneration += 1;
      if (delegationRefresh) clearTimeout(delegationRefresh);
      window.removeEventListener('popstate', onPopState);
      window.removeEventListener('keydown', onKeyDown);
    };
  });
</script>

<div class="sessions-layout">
  <aside class="session-rail" aria-label="Registered session tree">
    <div class="session-list-heading"><span>SESSION TREE</span><div><button title="Refresh session tree" onclick={refreshSessions} disabled={workspaceLoading}><RefreshCw aria-hidden="true" size={14} /></button><button title="Create project" onclick={() => openProjectDialog()} disabled={sessionLoading}><Folders aria-hidden="true" size={15} /></button><button title="Add or create session" onclick={openRegistrationDialog} disabled={sessionLoading}><Plus aria-hidden="true" size={15} /></button></div></div>
    <div class="session-list session-tree">
      {#snippet sessionBranch(registration: RegisteredSessionTree)}
        <div class="session-list-item session-root" class:active={selectedRegistration?.registration_id === registration.registration_id && !selectedDelegation} class:broken={!!registration.issue}>
          <button class="session-select" onclick={() => !registration.issue && selectRegisteredSession(registration)} disabled={!!registration.issue}>
            <strong>{registration.session.title || 'New session'}</strong>
            <small title={registration.workspace_root}>{registration.workspace_root}</small>
            <code>{registration.issue || `${formatSessionTime(registration.session.updated_at)} · ${shortID(registration.session.session_id)}`}</code>
          </button>
          <button class="session-delete" title="Remove from Studio" aria-label={`Remove ${registration.session.title || 'session'} from Studio`} onclick={() => unregisterSession(registration)} disabled={sending || sessionLoading}><X aria-hidden="true" size={13} /></button>
        </div>
        {#each flattenDelegations(registration.delegations || [], 1) as node}
          <button class="session-child" class:active={selectedRegistration?.registration_id === registration.registration_id && selectedDelegation?.path === node.path} style={`--tree-depth: ${node.depth}`} onclick={async () => { if (selectedRegistration?.registration_id !== registration.registration_id) await selectRegisteredSession(registration); selectDelegatedSession(registration, node); }} disabled={!!registration.issue}>
            <span class="tree-branch" aria-hidden="true"></span>
            <span><strong>{node.bookmark.agent.replace(/^builtin\//, '')}</strong><small>{node.state?.change_request ? `${node.state.status} · CR ${node.state.change_request.status}` : node.state?.status || 'recorded'}</small></span>
          </button>
        {/each}
      {/snippet}
      {#if projects.length || registeredSessions.length}
        {#each projects as project}
          <div class="session-project-heading"><span><Folders aria-hidden="true" size={13} /><strong>{project.name}</strong><small>{project.workspace_roots.length}</small></span><button title={`Edit ${project.name}`} aria-label={`Edit project ${project.name}`} onclick={() => openProjectDialog(project)}>Edit</button></div>
          {#each projectSessions(project.id) as registration}
            {@render sessionBranch(registration)}
          {:else}
            <div class="session-group-empty">No registered sessions</div>
          {/each}
        {/each}
        <div class="session-project-heading independent-heading"><span><GitBranch aria-hidden="true" size={13} /><strong>Independents</strong><small>{independentSessions().length}</small></span></div>
        {#each independentSessions() as registration}
          {@render sessionBranch(registration)}
        {:else}
          <div class="session-group-empty">No independent sessions</div>
        {/each}
      {:else}
        <div class="session-list-empty"><p>Add a workspace session to build your Studio session tree.</p><button class="primary-button" onclick={openRegistrationDialog}><Plus aria-hidden="true" size={15} /> Add session</button></div>
      {/if}
    </div>
  </aside>

  <section class="chat-panel">
    {#if error}<div class="session-error" role="alert">{error}</div>{/if}
    {#if selected}
      <div class="chat-heading">
        <div><h2>{selectedDelegation ? selectedDelegation.bookmark.agent.replace(/^builtin\//, '') : selected.session.title || 'New session'}</h2><p>{selectedDelegation ? `${selectedDelegation.bookmark.working_directory ? `${selectedDelegation.bookmark.working_directory} · ` : ''}${selectedDelegation.bookmark.prompt}` : workspaceRoot}</p></div>
        <div class="chat-heading-actions">
          {#if !selectedDelegation && activeRun?.context_size}
            <div class="context-usage" title={`Estimated context usage: ${formatTokenCount(activeRun.context_used)} of ${formatTokenCount(activeRun.context_size)} tokens`}>
              <span>Context {contextPercent(activeRun)}%</span>
              <small>{formatTokenCount(activeRun.context_used)}/{formatTokenCount(activeRun.context_size)}</small>
              <div role="progressbar" aria-label="Estimated context usage" aria-valuemin="0" aria-valuemax={activeRun.context_size} aria-valuenow={activeRun.context_used || 0}><i style={`width: ${Math.min(100, contextPercent(activeRun))}%`}></i></div>
            </div>
          {/if}
          <span class:running={sending}>{runStatus || (sending ? 'Running' : 'Ready')}</span>
          <button title="Open repository changes" aria-label="Open repository changes" onclick={() => openChanges(workspaceRoot)} disabled={!workspaceRoot}><GitCompareArrows aria-hidden="true" size={15} /></button>
          {#if selectedDelegation?.state?.status === 'completed' && (!selectedDelegation.state.change_request || ['merged', 'closed'].includes(selectedDelegation.state.change_request.status))}
            <button title="Delete completed delegation" aria-label="Delete completed delegation" onclick={deleteDelegation} disabled={sending || sessionLoading}><Trash2 aria-hidden="true" size={14} /></button>
          {/if}
          {#if !selectedDelegation}
            <button title={selectedRegistration?.project_id ? 'Edit session project' : 'Create project from this workspace'} aria-label="Configure session project" onclick={() => openProjectDialog()} disabled={sending || sessionLoading}><Folders aria-hidden="true" size={15} /></button>
            <button class:enabled={learningEnabled} title={`Learning ${learningEnabled ? 'enabled' : 'disabled'}`} aria-label={`Turn learning ${learningEnabled ? 'off' : 'on'}`} onclick={toggleLearning} disabled={sending || learningLoading}><BrainCircuit aria-hidden="true" size={15} /></button>
            <button title="Compact context" aria-label="Compact context" onclick={compactSession} disabled={sending || sessionLoading}><Minimize2 aria-hidden="true" size={15} /></button>
            <button title="Clear conversation" aria-label="Clear conversation" onclick={clearSession} disabled={sending || sessionLoading}><Eraser aria-hidden="true" size={15} /></button>
            <button title="Delete workspace session" aria-label="Delete workspace session" onclick={deleteSession} disabled={sending || sessionLoading}><Trash2 aria-hidden="true" size={14} /></button>
            {#if sending && activeRun?.status === 'paused'}<button title="Resume turn" aria-label="Resume turn" onclick={() => commandRun('resume')}><Play aria-hidden="true" size={15} /></button>{:else if sending}<button title="Pause turn" aria-label="Pause turn" onclick={() => commandRun('pause')}><Pause aria-hidden="true" size={15} /></button>{/if}
            {#if sending}<button class="stop-control" title="Stop turn" aria-label="Stop turn" onclick={stopTurn}><Square aria-hidden="true" size={13} fill="currentColor" /></button>{/if}
          {/if}
        </div>
      </div>
      <div class="transcript" bind:this={transcriptElement} aria-live="polite">
        {#each messages as message, messageIndex (`${message.role}-${message.tool_call_id || messageIndex}`)}
          {#if message.role === 'user' || message.role === 'assistant'}
            <article class="chat-message" class:user-message={message.role === 'user'}>
              <div class="message-avatar">{#if message.role === 'user'}<User aria-hidden="true" size={16} />{:else}<Bot aria-hidden="true" size={17} />{/if}</div>
              <div class="message-content"><header>{message.role === 'user' ? 'You' : 'Q'}</header>{#if message.content}<Markdown content={message.content} />{/if}
                {#if message.tool_calls?.length}
                  <div class="message-tools">
                    {#each message.tool_calls as call, callIndex (call.id || callIndex)}
                      <details class="tool-card">
                        <summary><Wrench aria-hidden="true" size={14} /><span>{call.function?.name || 'Tool call'}</span><code>{call.id ? shortID(call.id) : 'pending'}</code></summary>
                        {#if call.function?.arguments}<Markdown compact content={toolArguments(call.function.arguments)} />{/if}
                      </details>
                    {/each}
                  </div>
                {/if}
              </div>
            </article>
          {:else if message.role === 'tool'}
            <details class="transcript-tool-result">
              <summary><Wrench aria-hidden="true" size={14} /><span>{message.name || 'Tool result'}</span>{#if message.tool_call_id}<code>{shortID(message.tool_call_id)}</code>{/if}</summary>
              <Markdown compact content={message.content || '_No output_'} />
            </details>
          {/if}
        {/each}
        {#if thinkingDraft}<details class="thinking-block"><summary>Thinking</summary><Markdown compact content={thinkingDraft} /></details>{/if}
        {#if responseDraft}<article class="chat-message streaming-message"><div class="message-avatar"><Bot aria-hidden="true" size={17} /></div><div class="message-content"><header>Q <span>responding</span></header><Markdown content={responseDraft} /></div></article>{/if}
        {#if sessionLoading}<div class="transcript-loading">Loading session…</div>{/if}
      </div>
      {#if !selectedDelegation && activeRun?.pending_question}
        <section class="run-question" aria-labelledby="run-question-title">
          <div><span>Q NEEDS INPUT</span><h3 id="run-question-title">{activeRun.pending_question.question}</h3>{#if activeRun.pending_question.context}<p>{activeRun.pending_question.context}</p>{/if}</div>
          {#if activeRun.pending_question.choices?.length}<div class="question-choices">{#each activeRun.pending_question.choices as choice}<button onclick={() => answerQuestion(choice.id)}><strong>{choice.label}</strong>{#if choice.description}<span>{choice.description}</span>{/if}</button>{/each}</div>{/if}
          <div class="question-answer"><input bind:value={questionAnswer} placeholder="Write an answer…" onkeydown={(event) => event.key === 'Enter' && answerQuestion()} /><button class="primary-button" onclick={() => answerQuestion()} disabled={!questionAnswer.trim()}>Answer</button></div>
        </section>
      {/if}
      {#if selectedDelegation}
        <div class="delegated-session-note"><strong>Delegated session</strong><span>This transcript belongs to the selected child invocation. Interaction remains owned by its parent session.</span>{#if selectedDelegation.state?.change_request}<code>Change request {selectedDelegation.state.change_request.status} · {selectedDelegation.state.change_request.head_ref} · {shortID(selectedDelegation.state.change_request.base_commit)} → {shortID(selectedDelegation.state.change_request.head_commit || 'working')}</code>{/if}</div>
      {:else}
        <div class="composer-wrap">
          <div class="composer">
            <textarea bind:value={prompt} onkeydown={submitFromKeyboard} placeholder={sending ? 'Guide the current turn…' : 'Ask Q to work in this repository…'} rows="3"></textarea>
            <button class="send-button" title={sending ? 'Guide current turn' : 'Send message'} onclick={sendPrompt} disabled={!prompt.trim()}><ArrowUp aria-hidden="true" size={17} /></button>
          </div>
          <p>{sending ? 'Enter to redirect the current work with guidance' : 'Enter to send'} · Shift+Enter for a new line · {workspaceRoot}</p>
        </div>
      {/if}
    {:else}
      <div class="chat-empty"><Bot aria-hidden="true" size={34} /><h2>Build a session tree</h2><p>Register an existing workspace session or create a new root session.</p><button class="primary-button" onclick={openRegistrationDialog}><Plus aria-hidden="true" size={15} /> Add session</button></div>
    {/if}
  </section>

  <aside class="run-inspector" aria-label="Current turn activity">
    <div class="inspector-heading"><Terminal aria-hidden="true" size={16} /><span>Turn activity</span></div>
    {#if selected?.active_task}<div class="active-task"><span>ACTIVE TASK</span><strong>{selected.active_task.objective}</strong></div>{/if}
    {#if delegations.length}
      <div class="delegation-tree">
        <span class="inspector-label">DELEGATION TREE</span>
        {#each delegations as node}
          <details style={`--tree-depth: ${node.depth}`}>
            <summary><span class="tree-branch" aria-hidden="true"></span><strong>{node.bookmark.agent}</strong><em>{node.state?.status || 'recorded'}</em></summary>
            <p>{node.bookmark.prompt}</p>
            {#if node.state?.change_request}<small class="change-request-summary">Change request {node.state.change_request.status} · {node.state.change_request.head_ref} · {shortID(node.state.change_request.base_commit)} → {shortID(node.state.change_request.head_commit || 'working')}</small>{/if}
            {#if node.state?.running_call}<small>Running {node.state.running_call.name} · {shortID(node.state.running_call.call_id)}</small>{/if}
            {#if node.state?.unknown_tools?.length}<small>{node.state.unknown_tools.length} interrupted tool call(s) require review</small>{/if}
            {#if node.issue}<small class="tree-issue">{node.issue}</small>{/if}
          </details>
        {/each}
      </div>
    {/if}
    <div class="event-list">
      {#each events as event}
        <article class:error-event={event.is_error || event.type === 'error'}>
          <div class="event-icon"><Wrench aria-hidden="true" size={13} /></div>
          <div><strong>{eventTitle(event)}</strong>{#if eventBody(event)}<p>{eventBody(event)}</p>{/if}</div>
        </article>
      {:else}
        <div class="events-empty"><p>Tool calls and agent activity from the current turn appear here.</p></div>
      {/each}
    </div>
  </aside>
</div>

{#if registrationDialogOpen}
  <div class="directory-dialog-backdrop">
    <div class="directory-dialog registration-dialog" role="dialog" aria-modal="true" aria-labelledby="registration-dialog-title">
      <header>
        <div><span>SESSION REGISTRY</span><h2 id="registration-dialog-title">Add a root session</h2></div>
        <button class="dialog-close" title="Close" aria-label="Close session registry" onclick={() => registrationDialogOpen = false}><X aria-hidden="true" size={18} /></button>
      </header>
      <div class="registration-workspace">
        <label for="registration-workspace"><span>Workspace directory</span><input id="registration-workspace" bind:value={workspaceInput} placeholder="/path/to/repository" onkeydown={(event) => event.key === 'Enter' && loadWorkspaceCandidates()} /></label>
        <button class="icon-button" title="Choose repository folder" aria-label="Choose repository folder" onclick={chooseWorkspace} disabled={workspaceLoading}><FolderOpen aria-hidden="true" size={16} /></button>
        <button class="secondary-button" onclick={loadWorkspaceCandidates} disabled={workspaceLoading || !workspaceInput.trim()}>Load sessions</button>
      </div>
      {#if projects.length}
        <div class="project-workspace-shortcuts">
          <span>PROJECT WORKSPACES</span>
          <div>{#each projects as project}{#each project.workspace_roots as root}<button title={root} onclick={() => { workspaceInput = root; void loadWorkspaceCandidates(); }}><strong>{project.name}</strong><small>{root}</small></button>{/each}{/each}</div>
        </div>
      {/if}
      <div class="registration-candidates">
        {#if workspaceLoading}
          <div class="directory-state">Loading sessions…</div>
        {:else if workspaceInput && sessions.length}
          <button class="new-session-candidate" onclick={() => registerSession('', true)} disabled={sessionLoading}><Plus aria-hidden="true" size={16} /><span><strong>Create new session</strong><small>{workspaceInput}</small></span></button>
          {#each sessions as session}
            <button onclick={() => registerSession(session.session_id, false)} disabled={sessionLoading || registeredSessions.some((registration) => registration.workspace_root === workspaceInput && registration.session.session_id === session.session_id)}>
              <GitBranch aria-hidden="true" size={15} /><span><strong>{session.title || 'New session'}</strong><small>{formatSessionTime(session.updated_at)} · {shortID(session.session_id)}</small></span>
              <em>{registeredSessions.some((registration) => registration.workspace_root === workspaceInput && registration.session.session_id === session.session_id) ? 'Registered' : 'Add'}</em>
            </button>
          {/each}
        {:else if workspaceInput}
          <div class="registration-empty"><p>No saved sessions found in this workspace.</p><button class="primary-button" onclick={() => registerSession('', true)} disabled={sessionLoading}><Plus aria-hidden="true" size={15} /> Create new session</button></div>
        {:else}
          <div class="directory-state">Choose a workspace directory, then select an existing session or create a new one.</div>
        {/if}
      </div>
      <footer><div><span>REGISTRATION</span><code>Workspace data stays in the repository. Studio stores only the root session reference.</code></div><button class="secondary-button" onclick={() => registrationDialogOpen = false}>Cancel</button></footer>
    </div>
  </div>
{/if}

{#if projectDialogOpen}
  <div class="directory-dialog-backdrop">
    <div class="directory-dialog workspace-dialog" role="dialog" aria-modal="true" aria-labelledby="project-dialog-title">
      <header>
        <div><span>STUDIO PROJECT</span><h2 id="project-dialog-title">{editingProjectID ? 'Edit project' : 'Create project'}</h2></div>
        <button class="dialog-close" title="Close" aria-label="Close project settings" onclick={() => projectDialogOpen = false}><X aria-hidden="true" size={18} /></button>
      </header>
      <div class="workspace-dialog-body">
        {#if projectDialogError}<div class="workspace-dialog-error" role="alert">{projectDialogError}</div>{/if}
        <label><span>PROJECT NAME</span><input bind:value={projectName} placeholder="Project name" /></label>
        <p>Registered sessions whose primary workspace matches one of these directories appear under this project.</p>
        <div class="auxiliary-heading"><div><span>WORKSPACE DIRECTORIES</span><p>Each session keeps its own primary directory and receives the other project directories as auxiliary workspaces.</p></div><em>{projectRoots.length}/{32}</em></div>
        <div class="auxiliary-create">
          <input bind:value={projectRootInput} placeholder="/path/to/workspace" onkeydown={(event) => event.key === 'Enter' && addProjectWorkspace()} />
          <button class="icon-button" title="Choose workspace folder" aria-label="Choose workspace folder" onclick={chooseProjectWorkspace}><FolderOpen aria-hidden="true" size={16} /></button>
          <button class="secondary-button" onclick={addProjectWorkspace} disabled={!projectRootInput.trim()}>Add</button>
        </div>
        <div class="auxiliary-list">
          {#each projectRoots as root, index}
            <div><Folder aria-hidden="true" size={15} /><code title={root}>{root}</code><button title="Remove workspace" aria-label={`Remove ${root}`} onclick={() => removeProjectWorkspace(index)}><X aria-hidden="true" size={14} /></button></div>
          {:else}
            <div class="auxiliary-empty">Add at least one workspace directory.</div>
          {/each}
        </div>
      </div>
      <footer class="project-dialog-footer"><div><span>PROJECT CONTEXT</span><code>Workspace changes apply to project sessions on their next turn.</code></div>{#if editingProjectID}<button class="danger-button" onclick={deleteProject} disabled={projectSaving}>Delete</button>{/if}<button class="secondary-button" onclick={() => projectDialogOpen = false}>Cancel</button><button class="primary-button" onclick={saveProject} disabled={projectSaving || !projectName.trim() || (!projectRoots.length && !projectRootInput.trim()) || projectRoots.length > 32}>{projectSaving ? 'Saving…' : 'Save'}</button></footer>
    </div>
  </div>
{/if}

{#if directoryPickerOpen}
  <div class="directory-dialog-backdrop">
    <div class="directory-dialog" role="dialog" aria-modal="true" aria-labelledby="directory-dialog-title">
      <header>
        <div><span>{directoryPickerTarget === 'project' ? 'PROJECT WORKSPACE' : 'REPOSITORY'}</span><h2 id="directory-dialog-title">Choose a folder</h2></div>
        <button class="dialog-close" title="Close" aria-label="Close folder browser" onclick={closeDirectoryPicker}><X aria-hidden="true" size={18} /></button>
      </header>

      <div class="directory-toolbar">
        <button title="Home" aria-label="Go to home directory" onclick={() => browseDirectory(directoryListing?.home || '')} disabled={directoryLoading}><Home aria-hidden="true" size={16} /></button>
        <button title="Parent directory" aria-label="Go to parent directory" onclick={() => directoryListing?.parent && browseDirectory(directoryListing.parent)} disabled={directoryLoading || !directoryListing?.parent}><ChevronUp aria-hidden="true" size={17} /></button>
        <input aria-label="Directory path" bind:value={directoryPathInput} onkeydown={(event) => event.key === 'Enter' && browseDirectory(directoryPathInput)} />
        <button class="browse-button" onclick={() => browseDirectory(directoryPathInput)} disabled={directoryLoading}>Go</button>
      </div>

      {#if directoryListing && directoryListing.roots.length > 1}
        <div class="directory-roots">
          {#each directoryListing.roots as root}
            <button class:active={directoryListing.current.startsWith(root)} onclick={() => browseDirectory(root)} disabled={directoryLoading}><HardDrive aria-hidden="true" size={13} /> {root}</button>
          {/each}
        </div>
      {/if}

      <div class="directory-list" aria-busy={directoryLoading}>
        {#if directoryLoading && !directoryListing}
          <div class="directory-state">Loading folders…</div>
        {:else if directoryError}
          <div class="directory-state error">{directoryError}</div>
        {:else if directoryListing}
          {#each directoryListing.directories as directory}
            <button onclick={() => browseDirectory(directory.path)} disabled={directoryLoading}>
              <Folder aria-hidden="true" size={17} /><span>{directory.name}</span>
            </button>
          {:else}
            <div class="directory-state">This folder has no subdirectories.</div>
          {/each}
        {/if}
      </div>

      <footer>
        <div>
          <span>SELECTED FOLDER</span>
          <code>{directoryListing?.current || 'Loading…'}</code>
          {#if directoryListing && !directoryListing.can_select}<small>Choose a folder inside the home directory.</small>{/if}
        </div>
        <button class="secondary-button" onclick={closeDirectoryPicker}>Cancel</button>
        <button class="primary-button" onclick={selectBrowsedDirectory} disabled={directoryLoading || !directoryListing?.can_select}>Choose folder</button>
      </footer>
    </div>
  </div>
{/if}
