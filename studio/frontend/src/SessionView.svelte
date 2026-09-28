<script lang="ts">
  import { ArrowUp, Bot, FolderOpen, GitBranch, Plus, RefreshCw, Square, Terminal, User, Wrench } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';

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
    session_id?: string;
    kind?: string;
    start?: boolean;
    content?: string;
    detail?: string;
    name?: string;
    role?: string;
    agent?: string;
    action?: string;
    is_error?: boolean;
    question?: string;
    context?: string;
    outcome?: string;
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
  let abortController: AbortController | null = null;
  let transcriptElement: HTMLElement | null = null;

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
      const target = preferredSession || selected?.session.session_id || result.sessions[0]?.session_id || '';
      if (target && result.sessions.some((session) => session.session_id === target)) {
        await selectSession(target, false);
        window.history.replaceState({}, '', `/sessions/${encodeURIComponent(target)}`);
      } else {
        selected = null;
        messages = [];
        window.history.replaceState({}, '', '/sessions');
      }
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not open the repository';
    } finally {
      workspaceLoading = false;
    }
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
      sessions = [detail.session, ...sessions.filter((session) => session.session_id !== detail.session.session_id)];
      window.history.pushState({}, '', `/sessions/${encodeURIComponent(detail.session.session_id)}`);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not create a session';
    } finally {
      sessionLoading = false;
    }
  }

  async function selectSession(sessionID: string, push = true) {
    if (!workspaceRoot || sending) return;
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
      if (push) window.history.pushState({}, '', `/sessions/${encodeURIComponent(sessionID)}`);
      await scrollToBottom();
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load the session';
    } finally {
      sessionLoading = false;
    }
  }

  async function sendPrompt() {
    const content = prompt.trim();
    if (!selected || !workspaceRoot || !content || sending) return;
    prompt = '';
    sending = true;
    error = '';
    runStatus = 'Starting default loop…';
    events = [];
    responseDraft = '';
    thinkingDraft = '';
    messages = [...messages, { role: 'user', content }];
    abortController = new AbortController();
    await scrollToBottom();
    try {
      const response = await fetch(`/api/v1/sessions/${encodeURIComponent(selected.session.session_id)}/messages`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', Accept: 'application/x-ndjson' },
        body: JSON.stringify({ workspace_root: workspaceRoot, content }), signal: abortController.signal
      });
      if (!response.ok) throw new Error(await apiError(response));
      if (!response.body) throw new Error('Studio returned an empty stream');
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = '';
      while (true) {
        const { done, value } = await reader.read();
        buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
        const lines = buffer.split('\n');
        buffer = done ? '' : lines.pop() || '';
        for (const line of lines) {
          if (line.trim()) handleRunEvent(JSON.parse(line) as RunEvent);
        }
        if (done) {
          if (buffer.trim()) handleRunEvent(JSON.parse(buffer) as RunEvent);
          break;
        }
      }
      await reloadSelected();
      await refreshSessionListOnly();
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') {
        runStatus = 'Turn stopped';
      } else {
        error = cause instanceof Error ? cause.message : 'The turn failed';
        runStatus = 'Turn failed';
      }
      await reloadSelected();
    } finally {
      sending = false;
      abortController = null;
      responseDraft = '';
      thinkingDraft = '';
      await scrollToBottom();
    }
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
    } else if (event.type === 'tool_call' || event.type === 'trace' || event.type === 'question') {
      events = [...events, event];
      if (event.type === 'tool_call') runStatus = `Running ${event.name || 'tool'}…`;
      if (event.type === 'question') runStatus = 'The model requested input that Studio cannot answer yet';
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

  async function refreshSessionListOnly() {
    if (!workspaceRoot) return;
    const response = await fetch(`/api/v1/sessions?workspace_root=${encodeURIComponent(workspaceRoot)}`);
    if (!response.ok) return;
    sessions = ((await response.json()) as { sessions: SessionSummary[] }).sessions;
  }

  function stopTurn() {
    abortController?.abort();
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
        selected = null;
        messages = [];
      }
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  });
</script>

<div class="sessions-layout">
  <aside class="session-rail" aria-label="Repository sessions">
    <div class="workspace-picker">
      <label for="workspace-root">Repository path</label>
      <div class="workspace-input-row">
        <input id="workspace-root" bind:value={workspaceInput} placeholder="C:\\path\\to\\repository" onkeydown={(event) => event.key === 'Enter' && openWorkspace()} />
        <button class="icon-button" title="Open repository" onclick={() => openWorkspace()} disabled={!workspaceInput.trim() || workspaceLoading}><FolderOpen aria-hidden="true" size={16} /></button>
      </div>
    </div>

    {#if workspaceRoot}
      <div class="workspace-meta"><GitBranch aria-hidden="true" size={14} /><span title={workspaceRoot}>{workspaceRoot}</span></div>
      <div class="session-list-heading"><span>SESSIONS</span><div><button title="Refresh sessions" onclick={refreshSessions} disabled={workspaceLoading || sending}><RefreshCw aria-hidden="true" size={14} /></button><button title="New session" onclick={createSession} disabled={sessionLoading || sending}><Plus aria-hidden="true" size={15} /></button></div></div>
      <div class="session-list">
        {#each sessions as session}
          <button class:active={selected?.session.session_id === session.session_id} onclick={() => selectSession(session.session_id)} disabled={sending}>
            <strong>{session.title || 'New session'}</strong>
            <small>{formatSessionTime(session.updated_at)}</small>
            <code>{shortID(session.session_id)}</code>
          </button>
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
        <span class:running={sending}>{sending ? 'Running' : runStatus || 'Ready'}</span>
      </div>
      <div class="transcript" bind:this={transcriptElement} aria-live="polite">
        {#each messages as message}
          {#if message.role === 'user' || message.role === 'assistant'}
            <article class="chat-message" class:user-message={message.role === 'user'}>
              <div class="message-avatar">{#if message.role === 'user'}<User aria-hidden="true" size={16} />{:else}<Bot aria-hidden="true" size={17} />{/if}</div>
              <div><header>{message.role === 'user' ? 'You' : 'Q'}</header><p>{message.content || (message.tool_calls?.length ? 'Preparing tool calls…' : '')}</p></div>
            </article>
          {/if}
        {/each}
        {#if thinkingDraft}<details class="thinking-block"><summary>Thinking</summary><p>{thinkingDraft}</p></details>{/if}
        {#if responseDraft}<article class="chat-message"><div class="message-avatar"><Bot aria-hidden="true" size={17} /></div><div><header>Q</header><p>{responseDraft}</p></div></article>{/if}
        {#if sessionLoading}<div class="transcript-loading">Loading session…</div>{/if}
      </div>
      <div class="composer-wrap">
        <div class="composer">
          <textarea bind:value={prompt} onkeydown={submitFromKeyboard} placeholder="Ask Q to work in this repository…" rows="3" disabled={sending}></textarea>
          {#if sending}<button class="send-button stop" title="Stop turn" onclick={stopTurn}><Square aria-hidden="true" size={15} fill="currentColor" /></button>{:else}<button class="send-button" title="Send message" onclick={sendPrompt} disabled={!prompt.trim()}><ArrowUp aria-hidden="true" size={17} /></button>{/if}
        </div>
        <p>Enter to send · Shift+Enter for a new line · default loop runs with this repository as root</p>
      </div>
    {:else}
      <div class="chat-empty"><Bot aria-hidden="true" size={34} /><h2>{workspaceRoot ? 'Choose or create a session' : 'Open a repository'}</h2><p>{workspaceRoot ? 'The default loop will use the selected repository for files, tools, and session state.' : 'Sessions are scoped to the repository path you choose.'}</p>{#if workspaceRoot}<button class="primary-button" onclick={createSession}><Plus aria-hidden="true" size={15} /> New session</button>{/if}</div>
    {/if}
  </section>

  <aside class="run-inspector" aria-label="Current turn activity">
    <div class="inspector-heading"><Terminal aria-hidden="true" size={16} /><span>Turn activity</span></div>
    {#if selected?.active_task}<div class="active-task"><span>ACTIVE TASK</span><strong>{selected.active_task.objective}</strong></div>{/if}
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
