<script lang="ts">
  import { onMount, tick } from 'svelte';
  import { requestJSON, requestResponse } from './api';
  import Markdown from './Markdown.svelte';
  import type { ModelOption } from './settings/types';

  type Turn = { id: string; prompt: string; answer?: string; status: string; error?: string; created_at: string };
  type Conversation = { id: string; name: string; model: string; updated_at: string; turns: Turn[] };
  let conversations: Conversation[] = [];
  let selected: Conversation | null = null;
  let models: ModelOption[] = [];
  let loading = true;
  let opening = false;
  let creating = false;
  let busy = false;
  let cancelling = false;
  let error = '';
  let modelError = '';
  let modelsLoading = true;
  let name = '';
  let model = '';
  let prompt = '';
  let history: HTMLDivElement;
  let poll: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;
  const controller = new AbortController();
  const drafts = new Map<string, string>();
  $: running = selected?.turns.some(turn => turn.status === 'running') || false;
  $: concreteModels = models.filter(model => !model.group);

  function api<T>(method: string, path: string, body?: unknown) {
    return requestJSON<T>(method, path, body, controller.signal);
  }
  function failure(cause: unknown) {
    if (!controller.signal.aborted) error = cause instanceof Error ? cause.message : 'Could not load Composer';
  }
  function stopPoll() { if (poll) clearTimeout(poll); poll = undefined; }
  function pathID() { return window.location.pathname.match(/^\/composers\/([^/]+)$/)?.[1] || ''; }
  function rememberDraft() { if (selected) drafts.set(selected.id, prompt); }
  function remember(value: Conversation) {
    conversations = [value, ...conversations.filter(item => item.id !== value.id)]
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  }
  async function scrollToEnd() {
    await tick();
    if (history) history.scrollTop = history.scrollHeight;
  }

  async function loadList() {
    const result = await api<{ composers: Conversation[] }>('GET', '/api/v1/composers');
    conversations = result.composers || [];
  }
  async function loadModels() {
    modelError = ''; modelsLoading = true;
    try {
      const result = await api<{ models: ModelOption[] }>('GET', '/api/v1/settings/models');
      models = result.models || [];
      if (!model) model = models.find(item => !item.group)?.id || '';
    } catch (cause) {
      if (!controller.signal.aborted) modelError = cause instanceof Error ? cause.message : 'Could not load models';
    }
    finally { modelsLoading = false; }
  }
  function schedulePoll() {
    stopPoll();
    if (!selected || !selected.turns.some(turn => turn.status === 'running')) return;
    const id = selected.id;
    const expected = generation;
    poll = setTimeout(async () => {
      try {
        const value = await api<Conversation>('GET', `/api/v1/composers/${id}`);
        if (generation !== expected || controller.signal.aborted) return;
        const nearEnd = !history || history.scrollHeight - history.scrollTop - history.clientHeight < 100;
        selected = value;
        remember(value);
        if (value.turns.some(turn => turn.status === 'running')) schedulePoll();
        else cancelling = false;
        if (nearEnd) await scrollToEnd();
      } catch (cause) {
        if (generation !== expected) return;
        failure(cause);
        if (!controller.signal.aborted) schedulePoll();
      }
    }, 1000);
  }
  async function open(id: string, navigate = true) {
    rememberDraft();
    stopPoll();
    const expected = ++generation;
    opening = true; error = ''; cancelling = false;
    try {
      const value = await api<Conversation>('GET', `/api/v1/composers/${encodeURIComponent(id)}`);
      if (generation !== expected || controller.signal.aborted) return;
      selected = value; creating = false; prompt = drafts.get(id) || '';
      if (navigate) window.history.pushState({}, '', `/composers/${value.id}`);
      await scrollToEnd();
      schedulePoll();
    } catch (cause) { if (generation === expected) failure(cause); }
    finally { if (generation === expected) opening = false; }
  }
  function startCreate(navigate = true) {
    rememberDraft(); stopPoll(); generation++;
    selected = null; creating = true; opening = false; cancelling = false;
    name = ''; prompt = ''; error = '';
    if (navigate) window.history.pushState({}, '', '/composers');
  }
  async function create() {
    busy = true; error = '';
    try {
      const value = await api<Conversation>('POST', '/api/v1/composers', { name: name.trim(), model });
      remember(value);
      await open(value.id);
    } catch (cause) { failure(cause); }
    finally { busy = false; }
  }
  async function send() {
    if (!selected || running || busy || opening || !prompt.trim()) return;
    const id = selected.id;
    const expected = generation;
    const content = prompt.trim();
    busy = true; error = '';
    try {
      const value = await api<Conversation>('POST', `/api/v1/composers/${id}/messages`, { content });
      drafts.delete(id);
      remember(value);
      if (generation !== expected) return;
      selected = value; prompt = '';
      await scrollToEnd();
      schedulePoll();
    } catch (cause) { if (generation === expected) failure(cause); }
    finally { busy = false; }
  }
  async function cancel() {
    if (!selected || cancelling) return;
    cancelling = true;
    try { await api('POST', `/api/v1/composers/${selected.id}/cancel`, {}); }
    catch (cause) { failure(cause); cancelling = false; }
  }
  async function remove() {
    if (!selected || running || !window.confirm(`Delete Composer “${selected.name}” and its conversation?`)) return;
    busy = true; error = '';
    try {
      await requestResponse(`/api/v1/composers/${selected.id}`, { method: 'DELETE', signal: controller.signal });
      drafts.delete(selected.id);
      startCreate();
      creating = false;
      await loadList();
    } catch (cause) { failure(cause); }
    finally { busy = false; }
  }
  function onKey(event: KeyboardEvent) {
    if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && event.keyCode !== 229) {
      event.preventDefault(); void send();
    }
  }
  onMount(() => {
    void loadModels();
    void (async () => {
      try { await loadList(); } catch (cause) { failure(cause); }
      finally { loading = false; }
      const id = pathID();
      if (id) await open(id, false);
    })();
    const pop = () => { const id = pathID(); if (id) void open(id, false); else { startCreate(false); creating = false; } };
    window.addEventListener('popstate', pop);
    return () => { generation++; stopPoll(); controller.abort(); window.removeEventListener('popstate', pop); };
  });
</script>

<div class="composer-view">
  <aside class="conversation-list" aria-label="Composer conversations">
    <div class="list-heading"><h2>Conversations</h2><button onclick={() => startCreate()} disabled={busy}>New</button></div>
    {#if loading}<p class="muted" role="status">Loading conversations…</p>
    {:else if conversations.length === 0}<p class="muted">Your ideas start here. Create a conversation to get started.</p>{/if}
    {#each conversations as conversation (conversation.id)}
      <button class="conversation-item" class:selected={selected?.id === conversation.id} aria-current={selected?.id === conversation.id ? 'true' : undefined} disabled={busy} onclick={() => open(conversation.id)}>
        <strong>{conversation.name}</strong><small>{conversation.model}</small>
        <small>{new Date(conversation.updated_at).toLocaleString()}</small>
      </button>
    {/each}
  </aside>
  <section class="conversation-content" aria-label="Composer conversation">
    {#if error}<div class="error" role="alert">{error}</div>{/if}
    {#if opening}<p class="muted" role="status">Opening conversation…</p>
    {:else if creating}
      <form class="create-form" onsubmit={(event) => { event.preventDefault(); void create(); }}>
        <div><p class="eyebrow">COMPOSER</p><h2>Give your idea room to grow.</h2><p class="muted">Choose a model and work through your idea together. Your conversation is saved so you can return to it.</p></div>
        <label>Name<input bind:value={name} maxlength="120" required placeholder="What are you thinking about?" disabled={busy} /></label>
        <label>Model<select bind:value={model} required disabled={busy || concreteModels.length === 0}>
          {#if concreteModels.length === 0}<option value="">{modelsLoading ? 'Loading models…' : 'No models available'}</option>{/if}
          {#each concreteModels as choice}<option value={choice.id}>{choice.id}</option>{/each}
        </select></label>
        {#if modelError}<p class="error" role="alert">{modelError} <button type="button" onclick={loadModels}>Retry models</button></p>
        {:else if !modelsLoading && concreteModels.length === 0}<p class="muted">Add a provider in <a href="/settings">Settings</a> to choose a model.</p>{/if}
        <div><button class="primary" type="submit" disabled={busy || !name.trim() || !model}>{busy ? 'Creating…' : 'Create conversation'}</button></div>
      </form>
    {:else if selected}
      <div class="conversation-heading"><div><h2>{selected.name}</h2><p class="muted">{selected.model}</p></div><button onclick={remove} disabled={busy || running}>Delete</button></div>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex (Scrollable conversation history is keyboard accessible.) -->
      <div class="history" bind:this={history} role="region" aria-label="Conversation history" tabindex="0">
        {#if selected.turns.length === 0}<div class="empty-history"><h3>What would you like to explore?</h3><p class="muted">Share a rough idea, a problem, or a possibility. Work through the details at your own pace.</p></div>{/if}
        {#each selected.turns as turn (turn.id)}
          <article class="turn">
            <div class="user-message"><strong>You</strong><p>{turn.prompt}</p></div>
            <div class="model-message"><strong>Composer</strong>
              {#if turn.answer}<Markdown content={turn.answer} />{/if}
              {#if turn.status === 'running'}<p class="muted" role="status">Thinking through your idea…</p>
              {:else if turn.status !== 'completed'}<p class="error">{turn.status === 'cancelled' ? 'Reply stopped. You can continue the conversation.' : turn.error || 'This reply did not finish.'}</p>{/if}
            </div>
          </article>
        {/each}
      </div>
      <form class="message-form" onsubmit={(event) => { event.preventDefault(); void send(); }}>
        <label class="message-label" for="composer-message">Develop your idea</label>
        <textarea id="composer-message" bind:value={prompt} onkeydown={onKey} disabled={busy} placeholder="Share an idea or continue the conversation…" rows="3" maxlength="60000"></textarea>
        <div class="message-actions"><small class="muted">Enter to send · Shift + Enter for a new line</small>
          {#if running}<button type="button" onclick={cancel} disabled={cancelling}>{cancelling ? 'Stopping…' : 'Stop reply'}</button>
          {:else}<button class="primary" type="submit" disabled={busy || !prompt.trim()}>Send</button>{/if}
        </div>
      </form>
    {:else}<div class="welcome"><p class="eyebrow">COMPOSER</p><h2>From a spark to a clearer idea.</h2><p class="muted">Talk with a model to explore possibilities, ask questions, and work out the details.</p><button class="primary" onclick={() => startCreate()}>New conversation</button></div>{/if}
  </section>
</div>

<style>
  .composer-view { display:grid; grid-template-columns:260px minmax(0,1fr); gap:24px; height:calc(100vh - 180px); min-height:440px; }
  .conversation-list { overflow:auto; border-right:1px solid var(--border); padding-right:16px; }
  .list-heading,.conversation-heading,.message-actions { display:flex; align-items:center; justify-content:space-between; gap:12px; }
  h2 { margin:0; font-size:calc(18px * var(--text-scale)); }
  .list-heading { margin-bottom:16px; }
  .conversation-item { width:100%; display:flex; flex-direction:column; gap:6px; text-align:left; margin-bottom:8px; padding:12px; overflow-wrap:anywhere; }
  .conversation-item.selected { border-color:var(--violet); background:var(--violet-soft); }
  .conversation-item small { color:var(--muted); }
  .conversation-content { min-width:0; min-height:0; display:flex; flex-direction:column; gap:16px; }
  .conversation-heading { flex:none; border-bottom:1px solid var(--border); padding-bottom:12px; }
  .conversation-heading p { margin:6px 0 0; overflow-wrap:anywhere; }
  .history { flex:1; min-height:0; overflow:auto; padding:0 16px 8px 0; scrollbar-gutter:stable; }
  .turn { max-width:860px; margin:0 auto 28px; }
  .user-message { border:1px solid var(--border); border-radius:8px; padding:16px; background:var(--surface-raised); margin-bottom:18px; }
  .user-message p { white-space:pre-wrap; overflow-wrap:anywhere; margin:8px 0 0; line-height:1.6; }
  .model-message { padding:0 16px; }
  .model-message > strong { display:block; color:var(--accent-text); margin-bottom:10px; }
  .message-form { flex:none; border-top:1px solid var(--border); padding-top:16px; }
  .message-label { display:block; color:var(--muted); font-size:calc(12px * var(--text-scale)); margin-bottom:8px; }
  textarea,input,select { width:100%; min-width:0; color:var(--text); background:var(--field-bg); border:1px solid var(--border-strong); border-radius:7px; padding:12px; font:inherit; }
  textarea { resize:vertical; min-height:84px; max-height:220px; }
  textarea:focus,input:focus,select:focus { outline:2px solid var(--violet); outline-offset:2px; }
  .message-actions { margin-top:10px; }
  button { color:var(--text); background:var(--surface-raised); border:1px solid var(--border-strong); border-radius:6px; padding:8px 12px; cursor:pointer; font:inherit; }
  button:hover { border-color:var(--violet); }
  button:disabled { opacity:.5; cursor:default; }
  button.primary { background:var(--violet); border-color:var(--violet); color:var(--on-accent); }
  .create-form { display:grid; gap:20px; max-width:600px; margin:30px auto; width:100%; }
  .create-form label { display:grid; gap:8px; }
  .welcome,.empty-history { margin:50px auto; max-width:550px; }
  .welcome h2,.create-form h2 { font-size:calc(25px * var(--text-scale)); line-height:1.3; }
  .muted { color:var(--muted); line-height:1.6; }
  .error { color:var(--error); overflow-wrap:anywhere; }
  .eyebrow { color:var(--accent-text); font-size:calc(11px * var(--text-scale)); letter-spacing:.15em; font-weight:700; }
  @media(max-width:800px) { .composer-view { grid-template-columns:1fr; height:auto; min-height:calc(100vh - 180px); } .conversation-list { max-height:200px; border-right:0; border-bottom:1px solid var(--border); padding:0 0 12px; } .conversation-content { min-height:500px; } .history { max-height:65vh; } .message-actions small { font-size:10px; } }
</style>
