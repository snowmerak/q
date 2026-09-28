<script lang="ts">
  import { ArrowUp, Bot, BrainCircuit, ChevronUp, Eraser, Folder, FolderOpen, GitBranch, HardDrive, Home, Minimize2, Pause, Play, Plus, RefreshCw, Square, Terminal, Trash2, User, Wrench, X } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';
  import Markdown from './Markdown.svelte';

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
  };
  type RunQuestion = { call_id: string; question: string; context?: string; choices?: { id: string; label: string; description?: string }[] };
  type RunSnapshot = {
    id: string;
    session_id: string;
    status: string;
    outcome?: string;
    error?: string;
    pending_question?: RunQuestion;
    cursor: number;
  };
  type RunEventEnvelope = { cursor: number; at: string; event: RunEvent };
  type RunPage = { run: RunSnapshot; events: RunEventEnvelope[]; next_cursor: number };
  type DelegationNode = {
    bookmark: { invocation_id: string; agent: string; prompt: string; task_id?: string; parent_id?: string };
    state?: { status: string; task_id?: string; parent_id?: string; model?: string; running_call?: { name: string; call_id: string }; unknown_tools?: { name: string; call_id: string }[] };
    transcript?: Message[];
    children?: DelegationNode[];
    issue?: string;
  };
  type FlatDelegation = DelegationNode & { depth: number };
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

  async function openWorkspace(preferredSession = '') {
    const requested = workspaceInput.trim();
    if (!requested) return;
    workspaceLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(requested)}`);
      if (!response.ok) throw new Error(await apiError(response));
      const result = (await response.json()) as { workspace_root: string; sessions: SessionSummary[] };
      workspaceRoot = result.workspace_root;
      workspaceInput = result.workspace_root;
      sessions = result.sessions;
      localStorage.setItem('q-studio-workspace-root', result.workspace_root);
      await loadLearning();
      const target = preferredSession || selected?.session.session_id || result.sessions[0]?.session_id || '';
      if (target && result.sessions.some((session) => session.session_id === target)) {
        await selectSession(target, false);
        window.history.replaceState({}, '', `/sessions/${encodeURIComponent(target)}`);
      } else {
        pollGeneration += 1;
        selected = null;
        messages = [];
        activeRun = null;
        sending = false;
        delegations = [];
        window.history.replaceState({}, '', '/sessions');
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not open the repository';
    } finally {
      workspaceLoading = false;
    }
  }

  async function chooseWorkspace() {
    if (directoryLoading) return;
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
    workspaceInput = directoryListing.current;
    closeDirectoryPicker();
    await openWorkspace();
  }

  async function refreshSessions() {
    if (!workspaceRoot) return;
    workspaceInput = workspaceRoot;
    await openWorkspace(selected?.session.session_id || '');
  }

  async function createSession() {
    const root = workspaceRoot || workspaceInput.trim();
    if (!root) return;
    sessionLoading = true;
    pollGeneration += 1;
    error = '';
    try {
      const response = await fetch('/api/v1/sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ workspace_root: root })
      });
      if (!response.ok) throw new Error(await apiError(response));
      const detail = (await response.json()) as SessionDetail;
      workspaceRoot = detail.workspace_root;
      workspaceInput = detail.workspace_root;
      selected = detail;
      messages = detail.transcript;
      activeRun = null;
      sending = false;
      events = [];
      delegations = [];
      sessions = [detail.session, ...sessions.filter((session) => session.session_id !== detail.session.session_id)];
      window.history.pushState({}, '', `/sessions/${encodeURIComponent(detail.session.session_id)}`);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not create a session';
    } finally {
      sessionLoading = false;
    }
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
      sessions = [selected.session, ...sessions.filter((session) => session.session_id !== selected?.session.session_id)];
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

  async function deleteSession(sessionID: string) {
    if (!workspaceRoot || sending) return;
    const target = sessions.find((session) => session.session_id === sessionID);
    if (!window.confirm(`Delete ${target?.title || 'this session'}? Its durable archive records will remain available.`)) return;
    sessionLoading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}?workspace_root=${encodeURIComponent(workspaceRoot)}`, { method: 'DELETE' });
      if (!response.ok) throw new Error(await apiError(response));
      sessions = sessions.filter((session) => session.session_id !== sessionID);
      if (selected?.session.session_id === sessionID) {
        pollGeneration += 1;
        selected = null;
        messages = [];
        events = [];
        activeRun = null;
        sending = false;
        delegations = [];
        const next = sessions[0]?.session_id;
        if (next) {
          await selectSession(next, false);
          window.history.replaceState({}, '', `/sessions/${encodeURIComponent(next)}`);
        } else {
          window.history.replaceState({}, '', '/sessions');
        }
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not delete the session';
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

  async function selectSession(sessionID: string, push = true) {
    if (!workspaceRoot) return;
    pollGeneration += 1;
    sessionLoading = true;
    error = '';
    try {
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
      if (push) window.history.pushState({}, '', `/sessions/${encodeURIComponent(sessionID)}`);
      await Promise.all([reconnectLatestRun(sessionID), loadDelegations(sessionID)]);
      await scrollToBottom();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load the session';
    } finally {
      sessionLoading = false;
    }
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
    messages = selected.transcript;
  }

  function flattenDelegations(nodes: DelegationNode[], depth = 0): FlatDelegation[] {
    return nodes.flatMap((node) => [{ ...node, depth }, ...flattenDelegations(node.children || [], depth + 1)]);
  }

  async function loadDelegations(sessionID = selected?.session.session_id || '') {
    if (!workspaceRoot || !sessionID) return;
    const response = await fetch(`/api/v1/sessions/${encodeURIComponent(sessionID)}/delegations?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    const result = (await response.json()) as { nodes: DelegationNode[] };
    delegations = flattenDelegations(result.nodes);
  }

  function scheduleDelegationRefresh() {
    if (delegationRefresh) clearTimeout(delegationRefresh);
    delegationRefresh = setTimeout(() => { void loadDelegations(); }, 500);
  }

  async function refreshSessionListOnly() {
    if (!workspaceRoot) return;
    const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    sessions = ((await response.json()) as { sessions: SessionSummary[] }).sessions;
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
    const requestedRoot = new URL(window.location.href).searchParams.get('workspace_root') || '';
    workspaceInput = requestedRoot || localStorage.getItem('q-studio-workspace-root') || '';
    if (workspaceInput) void openWorkspace(sessionFromLocation());
    const onPopState = () => {
      const sessionID = sessionFromLocation();
      if (sessionID && sessionID !== selected?.session.session_id) void selectSession(sessionID, false);
      if (!sessionID) {
        pollGeneration += 1;
        selected = null;
        messages = [];
        activeRun = null;
        sending = false;
      }
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && directoryPickerOpen) closeDirectoryPicker();
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
  <aside class="session-rail" aria-label="Repository sessions">
    <div class="workspace-picker">
      <label for="workspace-root">Repository path</label>
      <div class="workspace-input-row">
        <input id="workspace-root" bind:value={workspaceInput} placeholder="/path/to/repository" onkeydown={(event) => event.key === 'Enter' && openWorkspace()} />
        <button class="icon-button" title="Choose repository folder" aria-label="Choose repository folder" onclick={chooseWorkspace} disabled={workspaceLoading}><FolderOpen aria-hidden="true" size={16} /></button>
      </div>
    </div>

    {#if workspaceRoot}
      <div class="workspace-meta"><GitBranch aria-hidden="true" size={14} /><span title={workspaceRoot}>{workspaceRoot}</span></div>
      <div class="session-list-heading"><span>SESSIONS</span><div><button title="Refresh sessions" onclick={refreshSessions} disabled={workspaceLoading}><RefreshCw aria-hidden="true" size={14} /></button><button title="New session" onclick={createSession} disabled={sessionLoading}><Plus aria-hidden="true" size={15} /></button></div></div>
      <div class="session-list">
        {#each sessions as session}
          <div class="session-list-item" class:active={selected?.session.session_id === session.session_id}>
            <button class="session-select" onclick={() => selectSession(session.session_id)}>
              <strong>{session.title || 'New session'}</strong>
              <small>{formatSessionTime(session.updated_at)}</small>
              <code>{shortID(session.session_id)}</code>
            </button>
            <button class="session-delete" title="Delete session" aria-label={`Delete ${session.title || 'session'}`} onclick={() => deleteSession(session.session_id)} disabled={sending || sessionLoading}><Trash2 aria-hidden="true" size={13} /></button>
          </div>
        {:else}
          <div class="session-list-empty"><p>No sessions in this repository.</p><button class="primary-button" onclick={createSession}><Plus aria-hidden="true" size={15} /> New session</button></div>
        {/each}
      </div>
    {:else}
      <div class="repository-empty"><FolderOpen aria-hidden="true" size={26} /><p>Enter a repository path to load its Q sessions.</p></div>
    {/if}
  </aside>

  <section class="chat-panel">
    {#if error}<div class="session-error" role="alert">{error}</div>{/if}
    {#if selected}
      <div class="chat-heading">
        <div><h2>{selected.session.title || 'New session'}</h2><p>{workspaceRoot}</p></div>
        <div class="chat-heading-actions">
          <span class:running={sending}>{runStatus || (sending ? 'Running' : 'Ready')}</span>
          <button class:enabled={learningEnabled} title={`Learning ${learningEnabled ? 'enabled' : 'disabled'}`} aria-label={`Turn learning ${learningEnabled ? 'off' : 'on'}`} onclick={toggleLearning} disabled={sending || learningLoading}><BrainCircuit aria-hidden="true" size={15} /></button>
          <button title="Compact context" aria-label="Compact context" onclick={compactSession} disabled={sending || sessionLoading}><Minimize2 aria-hidden="true" size={15} /></button>
          <button title="Clear conversation" aria-label="Clear conversation" onclick={clearSession} disabled={sending || sessionLoading}><Eraser aria-hidden="true" size={15} /></button>
          {#if sending && activeRun?.status === 'paused'}<button title="Resume turn" aria-label="Resume turn" onclick={() => commandRun('resume')}><Play aria-hidden="true" size={15} /></button>{:else if sending}<button title="Pause turn" aria-label="Pause turn" onclick={() => commandRun('pause')}><Pause aria-hidden="true" size={15} /></button>{/if}
          {#if sending}<button class="stop-control" title="Stop turn" aria-label="Stop turn" onclick={stopTurn}><Square aria-hidden="true" size={13} fill="currentColor" /></button>{/if}
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
      {#if activeRun?.pending_question}
        <section class="run-question" aria-labelledby="run-question-title">
          <div><span>Q NEEDS INPUT</span><h3 id="run-question-title">{activeRun.pending_question.question}</h3>{#if activeRun.pending_question.context}<p>{activeRun.pending_question.context}</p>{/if}</div>
          {#if activeRun.pending_question.choices?.length}<div class="question-choices">{#each activeRun.pending_question.choices as choice}<button onclick={() => answerQuestion(choice.id)}><strong>{choice.label}</strong>{#if choice.description}<span>{choice.description}</span>{/if}</button>{/each}</div>{/if}
          <div class="question-answer"><input bind:value={questionAnswer} placeholder="Write an answer…" onkeydown={(event) => event.key === 'Enter' && answerQuestion()} /><button class="primary-button" onclick={() => answerQuestion()} disabled={!questionAnswer.trim()}>Answer</button></div>
        </section>
      {/if}
      <div class="composer-wrap">
        <div class="composer">
          <textarea bind:value={prompt} onkeydown={submitFromKeyboard} placeholder={sending ? 'Guide the current turn…' : 'Ask Q to work in this repository…'} rows="3"></textarea>
          <button class="send-button" title={sending ? 'Guide current turn' : 'Send message'} onclick={sendPrompt} disabled={!prompt.trim()}><ArrowUp aria-hidden="true" size={17} /></button>
        </div>
        <p>{sending ? 'Enter to redirect the current work with guidance' : 'Enter to send'} · Shift+Enter for a new line · default loop runs with this repository as root</p>
      </div>
    {:else}
      <div class="chat-empty"><Bot aria-hidden="true" size={34} /><h2>{workspaceRoot ? 'Choose or create a session' : 'Open a repository'}</h2><p>{workspaceRoot ? 'The default loop will use the selected repository for files, tools, and session state.' : 'Sessions are scoped to the repository path you choose.'}</p>{#if workspaceRoot}<button class="primary-button" onclick={createSession}><Plus aria-hidden="true" size={15} /> New session</button>{/if}</div>
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

{#if directoryPickerOpen}
  <div class="directory-dialog-backdrop">
    <div class="directory-dialog" role="dialog" aria-modal="true" aria-labelledby="directory-dialog-title">
      <header>
        <div><span>REPOSITORY</span><h2 id="directory-dialog-title">Choose a folder</h2></div>
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
