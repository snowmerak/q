<script lang="ts">
  import { ArrowUp, Bot, BrainCircuit, ChevronDown, Ellipsis, Eraser, Files, Folders, GitCompareArrows, Layers, Minimize2, Pause, Play, Plus, Square, Trash2 } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import SessionTree from './sessions/SessionTree.svelte';
  import Transcript from './sessions/Transcript.svelte';
  import { loadDraft, saveDraft } from './sessions/drafts';
  import ProjectDialog from './sessions/ProjectDialog.svelte';
  import SessionRegistration from './sessions/SessionRegistration.svelte';
  import SessionModelPicker from './sessions/SessionModelPicker.svelte';
  import CouncilView from './CouncilView.svelte';
  import ComposerView from './ComposerView.svelte';
  import FileExplorer from './files/FileExplorer.svelte';
  import { resolveFileLink } from './files/paths';
  import { apiError, requestResponse } from './api';
  import { contextPercent, formatTokenCount, shortID } from './sessions/format';
  import { RunMonitor, runStatusLabel, terminalRun } from './sessions/run-monitor';
  import type { DelegationNode, DelegationSession, FlatDelegation, Message, RegisteredSessionTree, RunEvent, RunSnapshot, SessionDetail, StudioProject, SessionType } from './sessions/types';

  export let openChanges: (workspaceRoot: string) => void = () => {};
  let workflowCreate: 'council' | 'composer' | null = null;
  let workflowDirty = false;
  let typeFilter: SessionType | 'all' = 'all';

  export function canLeave() {
    return !workflowDirty || window.confirm('Discard your unsaved artifact source changes?');
  }
  export function currentPath() {
    return selectedRegistration ? `/sessions/${encodeURIComponent(selectedRegistration.registration_id)}` : workflowCreate ? `/sessions?new=${workflowCreate}` : '/sessions';
  }

  function startWorkflow(type: 'council' | 'composer') {
    if (!canLeave()) return;
    clearSelectedSession();
    workflowCreate = type;
    registrationDialogOpen = false;
    sessionListOpen = false;
    filesOpen = false;
    window.history.pushState({}, '', `/sessions?new=${type}`);
  }

  async function workflowCreated(id: string) {
    const generation = sessionGeneration;
    await refreshSessionListOnly();
    if (!isCurrentSession(generation)) return;
    const registration = registeredSessions.find(item => item.session.session_id === id && item.session.type === workflowCreate);
    if (!registration) { error = 'The new session is not available in the catalog. Refresh to try again.'; return; }
    typeFilter = 'all';
    await selectRegisteredSession(registration);
  }

  async function workflowRemoved(id: string) {
    await refreshSessionListOnly();
    if (selectedRegistration?.registration_id !== id) return;
    workflowDirty = false;
    clearSelectedSession();
    window.history.replaceState({}, '', '/sessions');
  }

  let workspaceRoot = '';
  let modelSaving = false;
  let registeredSessions: RegisteredSessionTree[] = [];
  let projects: StudioProject[] = [];
  let selectedRegistration: RegisteredSessionTree | null = null;
  let selectedDelegation: FlatDelegation | null = null;
  let delegationKind = '';
  let delegationPoll: AbortController | null = null;
  let selected: SessionDetail | null = null;
  let messages: Message[] = [];
  let prompt = '';
  let draftKey = '';
  let actionsMenu: HTMLDetailsElement | undefined;

  let workspaceLoading = false;
  let sessionLoading = false;
  let sending = false;
  let error = '';
  let runStatus = '';
  let events: RunEvent[] = [];
  let activeRun: RunSnapshot | null = null;
  $: runError = activeRun && ['failed', 'cancelled', 'interrupted'].includes(activeRun.status)
    ? activeRun.error || (activeRun.status === 'failed' ? 'The turn failed without an error detail.' : 'The turn stopped before completion.')
    : '';
  let sessionGeneration = 0;
  let questionAnswer = '';
  let delegationRefresh: ReturnType<typeof setTimeout> | null = null;
  let learningEnabled = true;
  let learningLoading = false;
  let registrationDialogOpen = false;
  let registrationMode: 'chat' | 'workspace' = 'chat';
  let sessionListOpen = false;
  let projectDialogOpen = false;
  let editingProject: StudioProject | undefined;
  let filesOpen = false;
  let openedFilePath = '';
  let openedFileLine = 1;
  let openedFileRoot = '';
  let lastFileContext = '';
  let fileExplorer: FileExplorer | null = null;
  $: fileWorkspace = selectedDelegation
    ? selectedDelegation.state?.change_request?.worktree_path || selectedDelegation.bookmark.working_directory || selectedDelegation.state?.change_request?.repository_root || workspaceRoot
    : workspaceRoot;
  $: fileRoots = [...new Set([fileWorkspace, workspaceRoot, ...(projects.find((project) => project.id === selectedRegistration?.project_id)?.workspace_roots || [])].filter(Boolean))];
  $: fileContext = `${selectedRegistration?.registration_id || ''}/${selectedDelegation?.path || ''}`;
  $: if (fileContext !== lastFileContext) {
    lastFileContext = fileContext;
    openedFilePath = ''; openedFileLine = 1; openedFileRoot = '';
  }
  function switchDraft(key: string) {
    saveDraft(draftKey, prompt);
    draftKey = key;
    prompt = key ? loadDraft(key) : '';
    closeActions();
  }

  function clearPrompt() {
    prompt = '';
    saveDraft(draftKey, '');
  }

  function closeActions() {
    if (actionsMenu) actionsMenu.open = false;
  }

  function openFileLink(value: string) {
    const target = resolveFileLink(value, fileRoots);
    if (!target) { error = 'The linked file is outside the available session workspaces.'; return; }
    if (filesOpen && fileExplorer) {
      fileExplorer.openFile(target.root, target.path, target.line);
      return;
    }
    openedFileRoot = target.root; openedFilePath = target.path; openedFileLine = target.line;
    filesOpen = true;
  }

  const runMonitor = new RunMonitor({
    page: async (page) => {
      activeRun = page.run;
      events = [...events, ...page.events.map(record => record.event)];
      for (const record of page.events) {
        handleRunEvent(record.event);
        if (record.event.type === 'redirect' && record.event.run_id) {
          activeRun = null;
          events = [];
          await reloadSelected();
          return;
        }
      }
      sending = !terminalRun(page.run.status);
      runStatus = runStatusLabel(page.run);
    },
    complete: async () => {
      await Promise.all([reloadSelected(), refreshSessionListOnly(), loadDelegations()]);
    },
    retry: (cause) => {
      error = cause instanceof Error ? cause.message : 'Could not reconnect to the run';
      runStatus = 'Connection lost · retrying…';
    }
  });

  function isCurrentSession(generation: number) {
    return generation === sessionGeneration;
  }

  function stopDelegationMonitor() {
    delegationPoll?.abort();
    delegationPoll = null;
  }

  function sessionFromLocation() {
    const match = window.location.pathname.match(/^\/sessions\/([^/]+)$/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  async function refreshSessions() {
    await loadRegisteredSessions(selectedRegistration?.registration_id || '');
  }

  async function registerSession(root: string, sessionID = '', create = false, chat = false) {
    if (!root && !chat) return;
    if (!canLeave()) return;
    sessionLoading = true;
    sessionGeneration += 1;
    runMonitor.stop();
    error = '';
    try {
      const response = await requestResponse(chat ? '/api/v1/chats' : '/api/v1/registered-sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(chat ? {} : { workspace_root: root, session_id: sessionID, create })
      });
      const registration = (await response.json()) as RegisteredSessionTree;
      typeFilter = 'all';
      registrationDialogOpen = false;
      await loadRegisteredSessions(registration.registration_id, true);
      sessionListOpen = false;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not register the session';
    } finally {
      sessionLoading = false;
    }
  }

  async function createChat() {
    await registerSession('', '', true, true);
  }

  function openRegistrationDialog() {
    registrationMode = 'chat';
    registrationDialogOpen = true;
    error = '';
  }

  function openWorkspaceDialog() {
    registrationMode = 'workspace';
    registrationDialogOpen = true;
    error = '';
  }

  async function selectFromTree(registration: RegisteredSessionTree) {
    await selectRegisteredSession(registration);
    sessionListOpen = false;
  }

  async function selectDelegationFromTree(registration: RegisteredSessionTree, node: FlatDelegation) {
    await openDelegatedSession(registration, node);
    sessionListOpen = false;
  }

  function openProjectDialog(project?: StudioProject) {
    editingProject = project || projects.find((candidate) => candidate.id === selectedRegistration?.project_id);
    projectDialogOpen = true;
    error = '';
  }

  async function projectSaved(status: string) {
    projectDialogOpen = false;
    await loadRegisteredSessions(selectedRegistration?.registration_id || '');
    runStatus = status;
  }

  async function clearSession() {
    if (!selected || !workspaceRoot || sending) return;
    if (!window.confirm('Clear this conversation? Its durable archive records will remain available.')) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/clear`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot })
      });
      selected = (await response.json()) as SessionDetail;
      sessionGeneration += 1;
      runMonitor.stop();
      messages = selected.transcript;
      events = [];
      activeRun = null;
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
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/compact`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot })
      });
      runStatus = ((await response.json()) as { status?: string }).status || 'Context compacted';
      events = [];
      await reloadSelected();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not compact the context';
      runStatus = 'Compaction failed';
    } finally {
      sessionLoading = false;
    }
  }

  function clearSelectedSession() {
    workflowCreate = null;
    workflowDirty = false;
    switchDraft('');
    sessionGeneration += 1;
    runMonitor.stop();
    stopDelegationMonitor();
    selectedRegistration = null;
    selectedDelegation = null;
    selected = null;
    workspaceRoot = '';
    sessionLoading = false;
    learningLoading = false;
    messages = [];
    events = [];
    activeRun = null;
    sending = false;
  }

  async function loadRegisteredSessions(preferredRegistration = '', approved = false) {
    workspaceLoading = true;
    error = '';
    try {
      const response = await requestResponse('/api/v1/registered-sessions', { headers: { Accept: 'application/json' } });
      const result = (await response.json()) as { projects?: StudioProject[]; sessions: RegisteredSessionTree[] };
      projects = result.projects || [];
      registeredSessions = result.sessions;
      const legacy = window.location.pathname.match(/^\/(councils|composers)(?:\/([^/]+))?$/);
      if (legacy) typeFilter = legacy[1] === 'councils' ? 'council' : 'composer';
      const targetID = preferredRegistration || selectedRegistration?.registration_id || sessionFromLocation();
      const legacyTarget = legacy?.[2] ? registeredSessions.find(item => item.session.type === typeFilter && item.session.session_id === decodeURIComponent(legacy[2])) : null;
      const target = legacyTarget || registeredSessions.find((registration) => registration.registration_id === targetID && !registration.issue)
        || (!legacy?.[2] ? registeredSessions.find((registration) => !registration.issue && (typeFilter === 'all' || registration.session.type === typeFilter)) : null);
      if (target) {
        await selectRegisteredSession(target, false, approved);
        window.history.replaceState({}, '', currentPath());
      } else {
        clearSelectedSession();
        if (targetID || legacy?.[2]) error = 'The requested session is unavailable.';
        window.history.replaceState({}, '', typeFilter === 'all' ? '/sessions' : `/sessions?type=${typeFilter}`);
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load registered sessions';
    } finally {
      workspaceLoading = false;
    }
  }

  async function unregisterSession(registration: RegisteredSessionTree) {
    const chat = registration.kind === 'chat';
    const workflow = registration.session.type === 'council' || registration.session.type === 'composer' ? registration.session.type : null;
    if (!window.confirm(workflow ? `Delete ${registration.session.title || workflow} and its session data?` : chat ? `Delete ${registration.session.title || 'this chat'} and its locally stored data?` : `Remove ${registration.session.title || 'this session'} from Studio? The workspace session will remain on disk.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await requestResponse(workflow ? `/api/v1/${workflow === 'council' ? 'councils' : 'composers'}/${encodeURIComponent(registration.session.session_id)}` : `/api/v1/${chat ? 'chats' : 'registered-sessions'}/${encodeURIComponent(registration.registration_id)}`, { method: 'DELETE' });
      registeredSessions = registeredSessions.filter((item) => item.registration_id !== registration.registration_id);
      if (selectedRegistration?.registration_id === registration.registration_id) {
        workflowDirty = false;
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
    if (selectedRegistration.kind === 'chat') {
      await unregisterSession(selectedRegistration);
      return;
    }
    if (!window.confirm(`Delete ${selected.session.title || 'this session'} from ${workspaceRoot}? Its durable archive records will remain available.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}?workspace_root=${encodeURIComponent(workspaceRoot)}`, { method: 'DELETE' });
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
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/delegations?${query}`, { method: 'DELETE' });
      if (selectedRegistration) await selectRegisteredSession(selectedRegistration, false);
      runStatus = 'Completed delegation deleted';
      await loadDelegations(selected.session.session_id);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the delegation';
    } finally {
      sessionLoading = false;
    }
  }

  async function loadLearning() {
    if (!workspaceRoot) return;
    learningLoading = true;
    const generation = sessionGeneration;
    try {
      const response = await requestResponse(`/api/v1/workspaces/learning?workspace_root=${encodeURIComponent(workspaceRoot)}`);
      const result = (await response.json()) as { enabled: boolean };
      if (isCurrentSession(generation)) learningEnabled = result.enabled;
    } catch (cause) {
      if (isCurrentSession(generation)) error = cause instanceof Error ? cause.message : 'Could not load learning settings';
    } finally {
      if (isCurrentSession(generation)) learningLoading = false;
    }
  }

  async function toggleLearning() {
    if (!workspaceRoot || learningLoading) return;
    learningLoading = true;
    error = '';
    try {
      const response = await requestResponse('/api/v1/workspaces/learning', {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, disabled: learningEnabled })
      });
      learningEnabled = ((await response.json()) as { enabled: boolean }).enabled;
      runStatus = `Learning ${learningEnabled ? 'enabled' : 'disabled'}`;
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not update learning settings';
    } finally {
      learningLoading = false;
    }
  }

  async function selectRegisteredSession(registration: RegisteredSessionTree, push = true, approved = false) {
    if (registration.registration_id !== selectedRegistration?.registration_id || workflowCreate) {
      if (!approved && !canLeave()) return;
    } else if (registration.session.type && registration.session.type !== 'interaction') {
      selectedRegistration = registration;
      return;
    }
    workflowCreate = null;
    workflowDirty = false;
    modelSaving = false;
    // Typed panels own their drafts. The Interaction input must not overwrite
    // them with its inactive prompt when the selection changes.
    switchDraft(registration.session.type === 'council' || registration.session.type === 'composer' ? '' : registration.registration_id);
    const generation = ++sessionGeneration;
    runMonitor.stop();
    stopDelegationMonitor();
    activeRun = null;
    sending = false;
    sessionLoading = true;
    error = '';
    try {
      selectedRegistration = registration;
      selectedDelegation = null;
      workspaceRoot = registration.workspace_root;
      const sessionID = registration.session.session_id;
      if (registration.session.type === 'council' || registration.session.type === 'composer') {
        selected = { workspace_root: workspaceRoot, session: registration.session, transcript: [] };
        filesOpen = false;
        if (push) window.history.pushState({}, '', `/sessions/${encodeURIComponent(registration.registration_id)}`);
        return;
      }
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(sessionID)}?workspace_root=${encodeURIComponent(workspaceRoot)}`);
      const detail = (await response.json()) as SessionDetail;
      if (!isCurrentSession(generation)) return;
      selected = detail;
      messages = selected.transcript;
      events = [];
      runStatus = '';
      activeRun = null;
      questionAnswer = '';
      if (push) window.history.pushState({}, '', `/sessions/${encodeURIComponent(registration.registration_id)}`);
      await Promise.all([loadLearning(), reconnectLatestRun(sessionID), loadDelegations(sessionID)]);
    } catch (cause) {
      if (isCurrentSession(generation)) error = cause instanceof Error ? cause.message : 'Could not load the session';
    } finally {
      if (isCurrentSession(generation)) sessionLoading = false;
    }
  }

  async function openDelegatedSession(registration: RegisteredSessionTree, node: FlatDelegation) {
    if (selectedRegistration?.registration_id !== registration.registration_id) await selectRegisteredSession(registration);
    selectDelegatedSession(registration, node);
  }

  function selectDelegatedSession(registration: RegisteredSessionTree, node: FlatDelegation) {
    modelSaving = false;
    if (selectedRegistration?.registration_id !== registration.registration_id) return;
    switchDraft(`${registration.registration_id}/${node.path}`);
    const generation = ++sessionGeneration;
    runMonitor.stop();
    stopDelegationMonitor();
    activeRun = null;
    sending = false;
    sessionLoading = true;
    error = '';
    questionAnswer = '';
    delegationKind = '';
    selectedDelegation = node;
    messages = node.transcript || [];
    events = [];
    runStatus = node.state?.status || 'Recorded';
    const controller = new AbortController();
    delegationPoll = controller;
    void monitorDelegation(registration, node.path, generation, controller.signal);
  }

  function applyDelegationSession(value: DelegationSession) {
    if (!selectedDelegation) return;
    selectedDelegation = { ...selectedDelegation, ...value.node };
    delegationKind = value.kind;
    messages = value.node.transcript || [];
    activeRun = value.run ? { ...value.run, session_id: value.node.bookmark.invocation_id, cursor: 0 } : null;
    sending = !!activeRun && !terminalRun(activeRun.status);
    runStatus = activeRun ? runStatusLabel(activeRun) : value.node.state?.status || 'Recorded';
  }

  async function monitorDelegation(registration: RegisteredSessionTree, path: string, generation: number, signal: AbortSignal) {
    const query = new URLSearchParams({ workspace_root: registration.workspace_root, path });
    let first = true;
    while (!signal.aborted) {
      try {
        const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(registration.session.session_id)}/delegations/session?${query}`, { signal });
        const value = await response.json() as DelegationSession;
        if (signal.aborted || !isCurrentSession(generation)) return;
        applyDelegationSession(value);
        if (first) sessionLoading = false;
        first = false;
        await loadDelegations();
      } catch (cause) {
        if (signal.aborted || !isCurrentSession(generation)) return;
        error = cause instanceof Error ? cause.message : 'Could not load the delegated session';
        if (first) sessionLoading = false;
        first = false;
      }
      await new Promise<void>((resolve) => {
        const finish = () => { clearTimeout(timer); signal.removeEventListener('abort', finish); resolve(); };
        const timer = setTimeout(finish, 1000);
        signal.addEventListener('abort', finish, { once: true });
        if (signal.aborted) finish();
      });
    }
  }

  async function sendPrompt() {
    if (modelSaving) return;
    const content = prompt.trim();
    if (!selected || !workspaceRoot || !content || sessionLoading) return;
    const generation = sessionGeneration;
    if (selectedDelegation && delegationKind !== 'inner') return;
    if (selectedDelegation && !sending) {
      error = '';
      sessionLoading = true;
      try {
        const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/delegations/commands`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ workspace_root: workspaceRoot, path: selectedDelegation.path, action: 'message', content })
        });
        const value = await response.json() as DelegationSession;
        if (!isCurrentSession(generation)) return;
        applyDelegationSession(value);
        clearPrompt();
      } catch (cause) {
        if (isCurrentSession(generation)) error = cause instanceof Error ? cause.message : 'Could not continue this subagent';
      } finally {
        if (isCurrentSession(generation)) sessionLoading = false;
      }
      return;
    }
    if (sending && activeRun) {
      sessionLoading = true;
      try { await commandRun('guidance', { content }); }
      finally { if (isCurrentSession(generation)) sessionLoading = false; }
      if (isCurrentSession(generation) && !error) {
        clearPrompt();
        runStatus = selectedDelegation ? 'Guidance sent' : 'Guidance queued…';
      }
      return;
    }
    const target = { workspaceRoot, sessionID: selected.session.session_id };
    clearPrompt();
    sending = true;
    error = '';
    runStatus = 'Starting default loop…';
    activeRun = null;
    events = [];
    messages = [...messages, { role: 'user', content }];
    try {
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, content })
      });
      const run = (await response.json()) as RunSnapshot;
      if (!isCurrentSession(generation)) return;
      activeRun = run;
      runMonitor.start(target, run.id);
    } catch (cause) {
      if (!isCurrentSession(generation)) return;
      error = cause instanceof Error ? cause.message : 'The turn failed';
      runStatus = 'Turn failed';
      if (!prompt) { prompt = content; saveDraft(draftKey, content); }
      await reloadSelected();
      if (!isCurrentSession(generation)) return;
      sending = false;
    }
  }

  async function reconnectLatestRun(sessionID: string) {
    if (!workspaceRoot) return;
    const generation = sessionGeneration;
    const target = { workspaceRoot, sessionID };
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/runs/latest?workspace_root=${encodeURIComponent(target.workspaceRoot)}`);
    if (!isCurrentSession(generation)) return;
    if (response.status === 404) { sending = false; return; }
    if (!response.ok) throw new Error(await apiError(response));
    const run = (await response.json()) as RunSnapshot;
    if (!isCurrentSession(generation)) return;
    activeRun = run;
    sending = !terminalRun(run.status);
    runStatus = runStatusLabel(run);
    runMonitor.start(target, run.id);
  }

  function handleRunEvent(event: RunEvent) {
    if (event.type === 'status') {
      runStatus = event.detail || 'Working…';
    } else if (event.type === 'activity') {
      runStatus = [event.agent, event.action, event.detail].filter(Boolean).join(' · ');
      scheduleDelegationRefresh();
    } else if (event.type === 'tool_call' || event.type === 'trace' || event.type === 'question') {
      if (event.type === 'tool_call') runStatus = `Running ${event.name || 'tool'}…`;
      if (event.type === 'question') runStatus = 'Waiting for your answer';
      if (event.type === 'trace') scheduleDelegationRefresh();
    } else if (event.type === 'result') {
      runStatus = event.outcome === 'succeeded' ? 'Completed' : event.outcome || 'Completed';
    } else if (event.type === 'error' || event.type === 'cancelled' || event.type === 'recovered') {
      runStatus = event.type === 'error' ? 'Turn failed' : 'Turn stopped';
    }
  }

  async function reloadSelected() {
    if (!selected || !workspaceRoot) return;
    const generation = sessionGeneration;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    const detail = (await response.json()) as SessionDetail;
    if (!isCurrentSession(generation)) return;
    selected = detail;
    if (!selectedDelegation) messages = selected.transcript;
  }


  async function loadDelegations(sessionID = selected?.session.session_id || '') {
    if (!workspaceRoot || !sessionID) return;
    const generation = sessionGeneration;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/delegations?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    const result = (await response.json()) as { nodes: DelegationNode[] };
    if (!isCurrentSession(generation)) return;
    if (selectedRegistration) {
      const updated = { ...selectedRegistration, delegations: result.nodes };
      selectedRegistration = updated;
      registeredSessions = registeredSessions.map((registration) => registration.registration_id === updated.registration_id ? updated : registration);
    }
  }

  function scheduleDelegationRefresh() {
    if (delegationRefresh) return;
    delegationRefresh = setTimeout(() => {
      delegationRefresh = null;
      void loadDelegations();
    }, 500);
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
    const generation = sessionGeneration;
    try {
      if (selectedDelegation) {
        if (delegationKind !== 'inner') return;
        const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/delegations/commands`, {
          method: 'POST', headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ workspace_root: workspaceRoot, path: selectedDelegation.path, run_id: activeRun.id, action, ...extra })
        });
        const value = await response.json() as DelegationSession;
        if (isCurrentSession(generation)) applyDelegationSession(value);
        return;
      }
      const response = await requestResponse(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/runs/${encodeURIComponent(activeRun.id)}/commands`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: workspaceRoot, action, ...extra })
      });
      const run = (await response.json()) as RunSnapshot;
      if (!isCurrentSession(generation)) return;
      activeRun = run;
      sending = !terminalRun(activeRun.status);
      runStatus = runStatusLabel(activeRun);
    } catch (cause) {
      if (isCurrentSession(generation)) error = cause instanceof Error ? cause.message : 'Could not control this turn';
    }
  }

  async function answerQuestion(answer = questionAnswer) {
    if (!activeRun?.pending_question) return;
    const generation = sessionGeneration;
    const value = answer.trim();
    if (!value) return;
    await commandRun('answer', { call_id: activeRun.pending_question.call_id, answer: value });
    if (isCurrentSession(generation) && !error) questionAnswer = '';
  }

  function stopTurn() {
    void commandRun('cancel');
  }

  function submitFromKeyboard(event: KeyboardEvent) {
    if (event.isComposing) return;
    if (event.key === 'Enter' && !event.shiftKey) {
      event.preventDefault();
      void sendPrompt();
    }
  }

  onMount(() => {
    let disposed = false;
    const params = new URLSearchParams(window.location.search);
    const requestedType = params.get('type');
    if (requestedType === 'interaction' || requestedType === 'council' || requestedType === 'composer') typeFilter = requestedType;
    const action = params.get('new');
    // Consume creation links once so reloading a session never creates another.
    if (action) {
      const url = new URL(window.location.href);
      url.searchParams.delete('new');
      window.history.replaceState({}, '', url.pathname + url.search);
    }
    void (async () => {
      await loadRegisteredSessions(sessionFromLocation());
      if (disposed) return;
      if (action === 'chat') await createChat();
      else if (action === 'workspace') openWorkspaceDialog();
      else if (action === 'council' || action === 'composer') startWorkflow(action);
    })();
    const onPopState = () => {
      if (!window.location.pathname.startsWith('/sessions')) return;
      const registrationID = sessionFromLocation();
      if (registrationID === selectedRegistration?.registration_id && !workflowCreate) return;
      if (!canLeave()) { window.history.pushState({}, '', currentPath()); return; }
      const registration = registeredSessions.find((item) => item.registration_id === registrationID && !item.issue);
      if (registration && registration.registration_id !== selectedRegistration?.registration_id) void selectRegisteredSession(registration, false, true);
      if (!registrationID) {
        const create = new URLSearchParams(window.location.search).get('new');
        clearSelectedSession();
        if (create === 'council' || create === 'composer') workflowCreate = create;
      }
    };
    const beforeUnload = (event: BeforeUnloadEvent) => { if (workflowDirty) { event.preventDefault(); event.returnValue = ''; } };
    window.addEventListener('popstate', onPopState);
    window.addEventListener('beforeunload', beforeUnload);
    return () => {
      saveDraft(draftKey, prompt);
      disposed = true;
      sessionGeneration += 1;
      runMonitor.stop();
      stopDelegationMonitor();
      if (delegationRefresh) clearTimeout(delegationRefresh);
      window.removeEventListener('popstate', onPopState);
      window.removeEventListener('beforeunload', beforeUnload);
    };
  });
</script>

<svelte:window onclick={(event) => { if (actionsMenu && !actionsMenu.contains(event.target as Node)) closeActions(); }} onkeydown={(event) => { if (event.key === 'Escape' && actionsMenu?.open) { closeActions(); actionsMenu.querySelector('summary')?.focus(); } }} />

<div class="sessions-layout" class:files-open={filesOpen && !!selected} class:session-list-open={sessionListOpen}>
  <button class="mobile-session-toggle" aria-label={sessionListOpen ? 'Hide sessions' : 'Show sessions'} aria-expanded={sessionListOpen} aria-controls="session-tree" onclick={() => sessionListOpen = !sessionListOpen}><Layers aria-hidden="true" size={17} /><span>Sessions<small>{selected?.session.title || 'Browse chats and workspaces'}</small></span><ChevronDown aria-hidden="true" size={17} /></button>
  <SessionTree
    {projects} {registeredSessions} {workspaceLoading} {sessionLoading} {sending}
    bind:typeFilter
    selectedRegistrationID={selectedRegistration?.registration_id || ''}
    selectedDelegationPath={selectedDelegation?.path || ''}
    onrefresh={refreshSessions} onproject={openProjectDialog} onregistration={openRegistrationDialog}
    onchat={createChat}
    onselect={selectFromTree} onunregister={unregisterSession} ondelegate={selectDelegationFromTree}
  />

  {#if workflowCreate || selectedRegistration?.session.type === 'council' || selectedRegistration?.session.type === 'composer'}
    <section class="workflow-panel" aria-label="Session workflow">
      {#if error}<div class="session-error" role="alert">{error}</div>{/if}
      {#key workflowCreate || selectedRegistration?.registration_id}
        {#if workflowCreate === 'council' || selectedRegistration?.session.type === 'council'}
          <CouncilView embedded initialID={workflowCreate ? '' : selectedRegistration?.session.session_id || ''} draftKey={selectedRegistration?.registration_id || ''} oncreated={workflowCreated} onupdated={refreshSessionListOnly} onremoved={workflowRemoved.bind(null, selectedRegistration?.registration_id || '')} />
        {:else}
          <ComposerView embedded initialID={workflowCreate ? '' : selectedRegistration?.session.session_id || ''} draftKey={selectedRegistration?.registration_id || ''} bind:artifactDirty={workflowDirty} oncreated={workflowCreated} onupdated={refreshSessionListOnly} onremoved={workflowRemoved.bind(null, selectedRegistration?.registration_id || '')} />
        {/if}
      {/key}
    </section>
  {:else}
  <section class="chat-panel">
    {#if error}<div class="session-error" role="alert">{error}</div>{/if}
    {#if selected}
      <div class="chat-heading" class:personal-chat={selectedRegistration?.kind === 'chat' && !selectedDelegation}>
        <div><h2>{selectedDelegation ? selectedDelegation.bookmark.agent.replace(/^builtin\//, '') : selected.session.title || (selectedRegistration?.kind === 'chat' ? 'New chat' : 'New session')}</h2><p>{selectedDelegation ? `${selectedDelegation.bookmark.working_directory ? `${selectedDelegation.bookmark.working_directory} · ` : ''}${selectedDelegation.bookmark.prompt}` : selectedRegistration?.kind === 'chat' ? 'Personal chat' : workspaceRoot}</p></div>
        <div class="chat-heading-actions">
          {#if !selectedDelegation && activeRun?.context_size}
            <div class="context-usage" title={`Estimated context usage: ${formatTokenCount(activeRun.context_used)} of ${formatTokenCount(activeRun.context_size)} tokens`}>
              <span>Context {contextPercent(activeRun)}%</span>
              <small>{formatTokenCount(activeRun.context_used)}/{formatTokenCount(activeRun.context_size)}</small>
              <div role="progressbar" aria-label="Estimated context usage" aria-valuemin="0" aria-valuemax={activeRun.context_size} aria-valuenow={activeRun.context_used || 0}><i style={`width: ${Math.min(100, contextPercent(activeRun))}%`}></i></div>
            </div>
          {/if}
          <span class:running={sending}>{runStatus || (sending ? 'Running' : 'Ready')}</span>
          <button title="Browse session files" aria-label="Browse session files" aria-pressed={filesOpen} onclick={() => filesOpen = !filesOpen} disabled={!fileWorkspace}><Files aria-hidden="true" size={15} /></button>
          {#if selectedRegistration?.kind !== 'chat' || selectedDelegation}<button title="Open repository changes" aria-label="Open repository changes" onclick={() => openChanges(fileWorkspace)} disabled={!fileWorkspace}><GitCompareArrows aria-hidden="true" size={15} /></button>{/if}
          {#if !selectedDelegation || (selectedDelegation.state?.status === 'completed' && (!selectedDelegation.state.change_request || ['merged', 'closed'].includes(selectedDelegation.state.change_request.status)))}
            <details class="session-actions-menu" bind:this={actionsMenu}>
              <summary aria-label="More session actions" title="More session actions"><Ellipsis aria-hidden="true" size={18} /></summary>
              <div class="session-actions-items">
                {#if selectedDelegation}
                  <button onclick={() => { closeActions(); void deleteDelegation(); }} disabled={sending || sessionLoading}><Trash2 aria-hidden="true" size={15} />Delete completed delegation</button>
                {:else}
                  {#if selectedRegistration?.kind !== 'chat'}<button aria-label="Configure session project" onclick={() => { closeActions(); openProjectDialog(); }} disabled={sending || sessionLoading}><Folders aria-hidden="true" size={15} />Configure project</button>{/if}
                  <button class:enabled={learningEnabled} aria-label={`Turn learning ${learningEnabled ? 'off' : 'on'}`} onclick={() => { closeActions(); void toggleLearning(); }} disabled={sending || learningLoading}><BrainCircuit aria-hidden="true" size={15} />Learning {learningEnabled ? 'enabled' : 'disabled'}</button>
                  <button onclick={() => { closeActions(); void compactSession(); }} disabled={sending || sessionLoading}><Minimize2 aria-hidden="true" size={15} />Compact context</button>
                  <button onclick={() => { closeActions(); void clearSession(); }} disabled={sending || sessionLoading}><Eraser aria-hidden="true" size={15} />Clear conversation</button>
                  <button class="danger-action" onclick={() => { closeActions(); void deleteSession(); }} disabled={sending || sessionLoading}><Trash2 aria-hidden="true" size={15} />{selectedRegistration?.kind === 'chat' ? 'Delete chat' : 'Delete workspace session'}</button>
                {/if}
              </div>
            </details>
          {/if}
          {#if !selectedDelegation || delegationKind === 'inner'}
            {#if sending && activeRun?.status === 'paused'}<button title="Resume turn" aria-label="Resume turn" onclick={() => commandRun('resume')}><Play aria-hidden="true" size={15} /></button>{:else if sending}<button title="Pause turn" aria-label="Pause turn" onclick={() => commandRun('pause')}><Pause aria-hidden="true" size={15} /></button>{/if}
            {#if sending}<button class="stop-control" title="Stop turn" aria-label="Stop turn" onclick={stopTurn}><Square aria-hidden="true" size={13} fill="currentColor" /></button>{/if}
          {/if}
        </div>
      </div>
      {#if runError}
        <div class="session-error run-error" role="alert">
          <strong>{runStatusLabel(activeRun!)}</strong>
          <p>{runError}</p>
          {#if selected.active_task}<small>The task is still active. Send a message to continue.</small>{/if}
        </div>
      {/if}
      <Transcript {messages} thoughts={selectedDelegation ? [] : selected?.transcript_thoughts || []} events={selectedDelegation ? [] : events} sessionKey={fileContext} loading={sessionLoading} onfile={openFileLink} />
      {#if !selectedDelegation && activeRun?.pending_question}
        <section class="run-question" aria-labelledby="run-question-title">
          <div><span>Q NEEDS INPUT</span><h3 id="run-question-title">{activeRun.pending_question.question}</h3>{#if activeRun.pending_question.context}<p>{activeRun.pending_question.context}</p>{/if}</div>
          {#if activeRun.pending_question.choices?.length}<div class="question-choices">{#each activeRun.pending_question.choices as choice}<button onclick={() => answerQuestion(choice.id)}><strong>{choice.label}</strong>{#if choice.description}<span>{choice.description}</span>{/if}</button>{/each}</div>{/if}
          <div class="question-answer"><input bind:value={questionAnswer} placeholder="Write an answer…" onkeydown={(event) => event.key === 'Enter' && answerQuestion()} /><button class="primary-button" onclick={() => answerQuestion()} disabled={!questionAnswer.trim()}>Answer</button></div>
        </section>
      {/if}
      {#if selectedDelegation?.state?.change_request}
        <div class="delegated-session-note"><code>Change request {selectedDelegation.state.change_request.status} · {selectedDelegation.state.change_request.head_ref} · {shortID(selectedDelegation.state.change_request.base_commit)} → {shortID(selectedDelegation.state.change_request.head_commit || 'working')}</code></div>
      {/if}
      {#if selectedDelegation && !delegationKind}
        <div class="delegated-session-note"><span>Loading delegated session…</span></div>
      {:else if selectedDelegation && delegationKind !== 'inner'}
        <div class="delegated-session-note"><strong>External ACP session</strong><span>Chat and execution controls are unavailable for external ACP sessions.</span></div>
      {:else}
        <div class="composer-wrap">
          <div class="composer">
            <textarea bind:value={prompt} oninput={(event) => saveDraft(draftKey, event.currentTarget.value)} aria-label="Message Q" onkeydown={submitFromKeyboard} placeholder={sending ? 'Guide the current turn…' : selectedRegistration?.kind === 'chat' ? 'Message Q…' : 'Ask Q to work in this repository…'} rows="3" disabled={sessionLoading}></textarea>
            <button class="send-button" title={sending ? 'Guide current turn' : 'Send message'} onclick={sendPrompt} disabled={sessionLoading || modelSaving || !prompt.trim()}><ArrowUp aria-hidden="true" size={17} /></button>
          </div>
          <div class="composer-footer">
            {#if !selectedDelegation && selected.session.session_id === selectedRegistration?.session.session_id && selected.workspace_root === workspaceRoot}
              {#key `${workspaceRoot}/${selected.session.session_id}`}
                <SessionModelPicker {workspaceRoot} sessionID={selected.session.session_id} disabled={sending || sessionLoading} bind:saving={modelSaving} />
              {/key}
            {/if}
            <p>{sending ? 'Enter to redirect the current work with guidance' : 'Enter to send'} · Shift+Enter for a new line · {selectedRegistration?.kind === 'chat' ? 'Personal chat' : workspaceRoot}</p>
          </div>
        </div>
      {/if}
    {:else}
      <div class="chat-empty"><Bot aria-hidden="true" size={34} /><h2>Start a conversation</h2><p>Chat with Q, or add a session from a workspace.</p><button class="primary-button" onclick={createChat} disabled={sessionLoading}><Plus aria-hidden="true" size={15} /> New chat</button><button class="secondary-button" onclick={openWorkspaceDialog}>Add workspace session</button></div>
    {/if}
  </section>
  {/if}

  {#if filesOpen && selected}
    <aside class="session-file-panel" aria-label="Session file browser">{#key fileContext}<FileExplorer bind:this={fileExplorer} initialRoot={openedFileRoot || fileWorkspace} initialPath={openedFilePath} initialLine={openedFileLine} roots={fileRoots} compact onclose={() => filesOpen = false} />{/key}</aside>
  {/if}
</div>

{#if registrationDialogOpen}
  <SessionRegistration {projects} {registeredSessions} initialMode={registrationMode} busy={sessionLoading} onregister={registerSession} onchat={createChat} onworkflow={startWorkflow} onclose={() => registrationDialogOpen = false} onerror={(message) => error = message} />
{/if}

<style>
  .workflow-panel { display:flex; flex-direction:column; min-width:0; min-height:0; overflow:hidden; }
  @media(max-width:800px) { .workflow-panel { min-height:560px; overflow:visible; } }
</style>

{#if projectDialogOpen}
  <ProjectDialog project={editingProject} {workspaceRoot} onsaved={projectSaved} onclose={() => projectDialogOpen = false} />
{/if}
