<script lang="ts">
  import { onMount } from 'svelte';
  import { ChevronDown, MessagesSquare } from '@lucide/svelte';
  import { requestJSON } from '../api';
  import Markdown from '../Markdown.svelte';

  export let attemptID: string;
  export let live: boolean;

  type WorkerCall = { id: string; name: string; arguments: string; truncated?: boolean };
  type WorkerMessage = { index: number; role: string; content: string; name?: string; truncated?: boolean; tool_calls?: WorkerCall[] };
  type Guidance = { id: string; content: string };
  type ActivityItem = { id: string; title: string; status: string };
  type WorkerPage = {
    messages: WorkerMessage[];
    next_cursor?: string;
    running_call?: { name: string };
    unknown_tools?: { name: string }[];
    worker_kind?: 'native' | 'acp';
    attempt_status?: string;
    accepts_guidance?: boolean;
    pending_guidance?: Guidance;
    acp_activity?: { response?: string; truncated?: boolean; tools?: ActivityItem[]; plan?: ActivityItem[] };
  };

  let mounted = false;
  let recent: WorkerPage | null = null;
  let history: WorkerPage | null = null;
  let showHistory = false;
  let error = '';
  let historyError = '';
  let recentRequest: AbortController | null = null;
  let historyRequest: AbortController | null = null;
  let loadingHistory = false;
  let guidance = '';
  let guidanceRequestID = '';
  let guidanceError = '';
  let sendingGuidance = false;
  $: attemptPath = `/api/v1/work/attempts/${encodeURIComponent(attemptID)}`;
  $: path = `${attemptPath}/worker`;
  $: messages = showHistory ? history?.messages || [] : [...(recent?.messages || [])].reverse();

  // The transition out of running also reads the final checkpoint.
  $: if (mounted) void refreshRecent(path, live);

  onMount(() => {
    mounted = true;
    const timer = setInterval(() => {
      if (live && !recentRequest && document.visibilityState === 'visible') void refreshRecent(path, true);
    }, 2000);
    return () => {
      clearInterval(timer);
      recentRequest?.abort();
      historyRequest?.abort();
    };
  });

  async function refreshRecent(url: string, running: boolean) {
    recentRequest?.abort();
    const request = new AbortController();
    recentRequest = request;
    try {
      const page = await requestJSON<WorkerPage>('GET', url + '?latest=true', undefined, request.signal);
      if (request.signal.aborted) return;
      recent = running ? page : { ...page, running_call: undefined };
      error = '';
    } catch (cause) {
      if (!request.signal.aborted) error = cause instanceof Error ? cause.message : String(cause);
    } finally {
      if (recentRequest === request) recentRequest = null;
    }
  }

  async function readHistory(more = false) {
    if (loadingHistory) return;
    const request = new AbortController();
    historyRequest = request;
    loadingHistory = true;
    historyError = '';
    try {
      const previous = more ? history : null;
      const cursor = new URLSearchParams({ cursor: previous?.next_cursor || '' });
      const page = await requestJSON<WorkerPage>('GET', path + '?' + cursor, undefined, request.signal);
      if (request.signal.aborted) return;
      history = { ...page, messages: [...(previous?.messages || []), ...page.messages] };
      showHistory = true;
    } catch (cause) {
      if (!request.signal.aborted) historyError = cause instanceof Error ? cause.message : String(cause);
    } finally {
      loadingHistory = false;
    }
  }

  async function sendGuidance(event: SubmitEvent) {
    event.preventDefault();
    if (!live || !recent?.accepts_guidance || recent.pending_guidance || !guidance.trim() || sendingGuidance) return;
    sendingGuidance = true;
    guidanceError = '';
    guidanceRequestID ||= crypto.randomUUID();
    try {
      const pending = await requestJSON<Guidance>('POST', attemptPath + '/guidance', {
        content: guidance, request_id: guidanceRequestID
      });
      recent = { ...recent, pending_guidance: pending };
      guidance = '';
      guidanceRequestID = '';
      await refreshRecent(path, live);
    } catch (cause) {
      guidanceError = cause instanceof Error ? cause.message : String(cause);
      void refreshRecent(path, live);
    } finally {
      sendingGuidance = false;
    }
  }
</script>

<section class="worker-conversation" aria-label="Worker conversation">
  <header>
    <h4>{showHistory ? 'Worker conversation' : 'Recent worker activity'}</h4>
    {#if showHistory}
      <button class="secondary-button" onclick={() => { showHistory = false; }}>Show latest</button>
    {:else if recent?.messages.length}
      <button class="secondary-button" disabled={loadingHistory} onclick={() => readHistory()}>
        <MessagesSquare size={15} aria-hidden="true" />Read worker conversation
      </button>
    {/if}
  </header>
  {#if live && recent?.running_call}<p role="status">Running tool <code>{recent.running_call.name}</code></p>{/if}
  {#if recent?.unknown_tools?.length}
    <p role="status">Tool outcome unknown: {recent.unknown_tools.map(tool => tool.name).join(', ')}</p>
  {/if}
  {#if error}<p role="status">Could not load worker activity. {error}</p>{/if}
  {#if historyError}<p role="status">{historyError}</p>{/if}
  {#if recent?.attempt_status === 'running' && recent.worker_kind === 'acp'}
    <div class="worker-activity" aria-label="Live ACP activity">
      <p role="status">ACP worker running</p>
      {#if recent.acp_activity?.plan?.length}
        <ul aria-label="ACP plan">
          {#each recent.acp_activity.plan as entry (entry.id)}
            <li>{entry.title} <span>· {entry.status.replaceAll('_', ' ')}</span></li>
          {/each}
        </ul>
      {/if}
      {#if recent.acp_activity?.tools?.length}
        <ul aria-label="ACP tools">
          {#each recent.acp_activity.tools as tool (tool.id)}
            <li>{tool.title} <span>· {tool.status}</span></li>
          {/each}
        </ul>
      {/if}
      {#if recent.acp_activity?.response}
        <article aria-label="ACP response in progress">
          <Markdown content={recent.acp_activity.response} />
          {#if recent.acp_activity.truncated}<p class="preview-note">Response preview truncated.</p>{/if}
        </article>
      {/if}
    </div>
  {/if}
  {#if !recent && !error}
    <p role="status">Loading worker activity…</p>
  {:else if !error && !recent?.acp_activity && !messages.length}
    <p role="status">{live ? 'Waiting for the first worker update…' : 'No worker conversation recorded.'}</p>
  {/if}
  {#if messages.length}
    <div class="worker-messages" class:recent={!showHistory}>
      {#each messages as message (message.index)}
        <article class="worker-message">
          <strong>{message.role === 'user' && message.name?.startsWith('q_ticket_guidance_') ? 'Guidance applied' : [message.role, message.name].filter(Boolean).join(' ')} · message {message.index}</strong>
          {#if message.content}
            {#if message.role === 'user' || message.role === 'assistant'}
              <Markdown content={message.content} />
            {:else}<pre>{message.content}</pre>{/if}
          {/if}
          {#if message.truncated}<p class="preview-note">Message preview truncated.</p>{/if}
          {#each message.tool_calls || [] as call}
            <pre>{call.name} ({call.id}){'\n'}{call.arguments}{call.truncated ? '\n[arguments truncated]' : ''}</pre>
          {/each}
        </article>
      {/each}
    </div>
  {/if}
  {#if showHistory && history?.next_cursor}
    <button class="secondary-button" onclick={() => readHistory(true)} disabled={loadingHistory} aria-label="More worker messages">
      <ChevronDown size={15} aria-hidden="true" />More messages
    </button>
  {/if}
  {#if recent?.pending_guidance}
    <article class="worker-message">
      <strong>{live && recent.accepts_guidance ? 'Guidance pending' : 'Guidance was not applied'}</strong>
      <Markdown content={recent.pending_guidance.content} />
    </article>
  {/if}
  {#if live && recent?.accepts_guidance && !recent.pending_guidance}
    <form class="worker-guidance" onsubmit={sendGuidance}>
      <label for={'guidance-' + attemptID}>Guide this worker</label>
      <textarea id={'guidance-' + attemptID} bind:value={guidance} maxlength="8192" rows="2" disabled={sendingGuidance}
        oninput={() => { guidanceRequestID = ''; guidanceError = ''; }} ></textarea>
      <button class="secondary-button" type="submit" disabled={sendingGuidance || !guidance.trim()}>Send guidance</button>
    </form>
  {/if}
  {#if guidanceError}<p role="status">{guidanceError}</p>{/if}
</section>

<style>
  .worker-conversation { display: grid; min-width: 0; gap: 10px; margin-block: 12px; padding: 12px; border: 1px solid var(--border); border-radius: 6px; }
  header { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; }
  h4, p, pre { margin: 0; }
  p { color: var(--muted); }
  .preview-note { color: var(--subtle); font-size: var(--type-small); }
  .secondary-button { display: inline-flex; align-items: center; gap: 6px; justify-self: start; }
  .worker-messages { display: grid; min-width: 0; gap: 10px; }
  .worker-messages.recent { max-height: 26rem; overflow: auto; }
  .worker-activity, .worker-guidance { display: grid; min-width: 0; gap: 8px; }
  .worker-activity ul { margin: 0; padding-inline-start: 1.25rem; overflow-wrap: anywhere; }
  .worker-activity li span { color: var(--muted); }
  .worker-guidance textarea { width: 100%; resize: vertical; }
  .worker-message { display: grid; min-width: 0; gap: 8px; border-top: 1px solid var(--border); padding-top: 8px; overflow-wrap: anywhere; }
  pre { max-width: 100%; max-height: 36rem; overflow: auto; padding: 12px; border: 1px solid var(--border); border-radius: 5px; background: var(--code-bg); color: var(--code-text); white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
