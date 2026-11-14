<script lang="ts">
  import { onMount } from 'svelte';
  import { ChevronDown, MessagesSquare } from '@lucide/svelte';
  import { requestJSON } from '../api';
  import Markdown from '../Markdown.svelte';

  export let attemptID: string;
  export let live: boolean;

  type WorkerCall = { id: string; name: string; arguments: string; truncated?: boolean };
  type WorkerMessage = { index: number; role: string; content: string; name?: string; truncated?: boolean; tool_calls?: WorkerCall[] };
  type WorkerPage = {
    messages: WorkerMessage[];
    next_cursor?: string;
    running_call?: { name: string };
    unknown_tools?: { name: string }[];
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
  $: path = `/api/v1/work/attempts/${encodeURIComponent(attemptID)}/worker`;
  $: messages = showHistory ? history?.messages || [] : [...(recent?.messages || [])].reverse();

  // The transition out of running also reads the final checkpoint.
  $: if (mounted) void refreshRecent(path, live);

  onMount(() => {
    mounted = true;
    const timer = setInterval(() => {
      if (live && !recentRequest && document.visibilityState === 'visible') void refreshRecent(path, true);
    }, 5000);
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
  {#if !recent && !error}
    <p role="status">Loading worker activity…</p>
  {:else if !error && !messages.length}
    <p role="status">{live ? 'Waiting for the first worker update…' : 'No worker conversation recorded.'}</p>
  {/if}
  {#if messages.length}
    <div class="worker-messages" class:recent={!showHistory}>
      {#each messages as message (message.index)}
        <article class="worker-message">
          <strong>{message.role} {message.name || ''} · message {message.index}</strong>
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
  .worker-message { display: grid; min-width: 0; gap: 8px; border-top: 1px solid var(--border); padding-top: 8px; overflow-wrap: anywhere; }
  pre { max-width: 100%; max-height: 36rem; overflow: auto; padding: 12px; border: 1px solid var(--border); border-radius: 5px; background: var(--code-bg); color: var(--code-text); white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
