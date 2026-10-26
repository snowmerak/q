<script lang="ts">
  import { onMount } from 'svelte';
  import { ArrowUp, Bot, Square, Trash2 } from '@lucide/svelte';
  import { requestJSON, requestResponse } from './api';
  import Transcript from './sessions/Transcript.svelte';
  import type { Message } from './sessions/types';
  import ComposerArtifacts from './ComposerArtifacts.svelte';
  import type { ModelOption } from './settings/types';
  import { reasoningOptions } from './settings/reasoning';
  import { loadDraft, saveDraft } from './sessions/drafts';

  export let embedded = false;
  export let initialID = '';
  export let draftKey = '';
  export let artifactDirty = false;
  export let oncreated: (id: string) => Promise<void> = async () => {};
  export let onupdated: () => Promise<void> = async () => {};
  export let onremoved: () => Promise<void> = async () => {};

  type Turn = { id: string; prompt: string; answer?: string; status: string; error?: string; created_at: string; interactions?: { question: string; answer: string }[] };
  type Question = { call_id: string; question: string; context?: string; choices?: { id: string; label: string; description?: string }[] };
  type Conversation = { id: string; name: string; model: string; reasoning_effort?: string; updated_at: string; turns: Turn[]; question?: Question };
  let answer = '';
  let answering = false;
  let questionID = '';
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
  let reasoningEffort = '';
  let prompt = '';
  let poll: ReturnType<typeof setTimeout> | undefined;
  let generation = 0;
  const controller = new AbortController();
  $: running = selected?.turns.some(turn => turn.status === 'running') || false;
  $: messages = conversationMessages(selected?.turns || []);
  $: concreteModels = models.filter(model => !model.group);
  $: if ((selected?.question?.call_id || '') !== questionID) { questionID = selected?.question?.call_id || ''; answer = ''; }

  function api<T>(method: string, path: string, body?: unknown) {
    return requestJSON<T>(method, path, body, controller.signal);
  }
  function failure(cause: unknown) {
    if (!controller.signal.aborted) error = cause instanceof Error ? cause.message : 'Could not load Composer';
  }
  function stopPoll() { if (poll) clearTimeout(poll); poll = undefined; }
  function pathID() { return window.location.pathname.match(/^\/composers\/([^/]+)$/)?.[1] || ''; }
  function rememberDraft() { if (selected) saveDraft(draftKey || `composer:${selected.id}`, prompt); }
  function remember(value: Conversation) {
    conversations = [value, ...conversations.filter(item => item.id !== value.id)]
      .sort((a, b) => b.updated_at.localeCompare(a.updated_at));
  }
  function conversationMessages(turns: Turn[]): Message[] {
    return turns.flatMap(turn => [
      { role: 'user', content: turn.prompt },
      ...(turn.interactions || []).flatMap(interaction => [
        { role: 'assistant', content: interaction.question },
        { role: 'user', content: interaction.answer }
      ]),
      { role: 'assistant', content: turn.answer || '', status: turn.status, error: turn.error }
    ]);
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
        selected = value;
        remember(value);
        if (value.turns.some(turn => turn.status === 'running')) schedulePoll();
        else { cancelling = false; if (embedded) await onupdated(); }
      } catch (cause) {
        if (generation !== expected) return;
        failure(cause);
        if (!controller.signal.aborted) schedulePoll();
      }
    }, 1000);
  }
  async function open(id: string, navigate = true) {
    if (id !== selected?.id && artifactDirty && !window.confirm('Discard your unsaved artifact source changes?')) return;
    rememberDraft();
    stopPoll();
    const expected = ++generation;
    opening = true; error = ''; cancelling = false;
    try {
      const value = await api<Conversation>('GET', `/api/v1/composers/${encodeURIComponent(id)}`);
      if (generation !== expected || controller.signal.aborted) return;
      selected = value; creating = false; prompt = loadDraft(draftKey || `composer:${id}`);
      if (navigate && !embedded) window.history.pushState({}, '', `/composers/${value.id}`);
      schedulePoll();
    } catch (cause) { if (generation === expected) failure(cause); }
    finally { if (generation === expected) opening = false; }
  }
  function startCreate(navigate = true) {
    if (artifactDirty && !window.confirm('Discard your unsaved artifact source changes?')) return;
    rememberDraft(); stopPoll(); generation++;
    selected = null; creating = true; opening = false; cancelling = false;
    name = ''; reasoningEffort = ''; prompt = ''; error = '';
    if (navigate && !embedded) window.history.pushState({}, '', '/composers');
  }
  async function create() {
    busy = true; error = '';
    try {
      const value = await api<Conversation>('POST', '/api/v1/composers', { name: name.trim(), model, reasoning_effort: reasoningEffort });
      remember(value);
      if (embedded) await oncreated(value.id);
      else await open(value.id);
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
      saveDraft(draftKey || `composer:${id}`, '');
      remember(value);
      if (generation !== expected) return;
      selected = value; prompt = '';
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
  async function submitAnswer(value = answer) {
    if (!selected?.question || !value.trim() || answering) return;
    const expected = generation;
    const id = selected.id;
    answering = true; error = '';
    try {
      await api('POST', `/api/v1/composers/${id}/answer`, { call_id: selected.question.call_id, answer: value });
      if (generation !== expected) return;
      const updated = await api<Conversation>('GET', `/api/v1/composers/${id}`);
      if (generation !== expected) return;
      selected = updated; answer = ''; schedulePoll();
    } catch (cause) { if (generation === expected) failure(cause); }
    finally { answering = false; }
  }
  async function remove() {
    if (!selected || running || !window.confirm(`Delete Composer “${selected.name}” and its conversation?`)) return;
    busy = true; error = '';
    try {
      await requestResponse(`/api/v1/composers/${selected.id}`, { method: 'DELETE', signal: controller.signal });
      saveDraft(draftKey || `composer:${selected.id}`, '');
      artifactDirty = false;
      if (embedded) { await onremoved(); return; }
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
    if (embedded) {
      loading = false;
      if (initialID) void open(initialID, false);
      else startCreate(false);
      return () => { rememberDraft(); generation++; stopPoll(); controller.abort(); };
    }
    void (async () => {
      try { await loadList(); } catch (cause) { failure(cause); }
      finally { loading = false; }
      const id = pathID();
      if (id) await open(id, false);
    })();
    const pop = () => { const id = pathID(); if (id) void open(id, false); else { startCreate(false); creating = false; } };
    window.addEventListener('popstate', pop);
    return () => { rememberDraft(); generation++; stopPoll(); controller.abort(); window.removeEventListener('popstate', pop); };
  });
</script>

<div class="composer-view" class:embedded class:with-artifacts={selected && !creating}>
  {#if !embedded}
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
  {/if}
  <section class="chat-panel conversation-content" class:creating aria-label="Composer conversation">
    {#if error}<div class="session-error" role="alert">{error}</div>{/if}
    {#if opening}<p class="muted" role="status">Opening conversation…</p>
    {:else if creating}
      <form class="create-form" onsubmit={(event) => { event.preventDefault(); void create(); }}>
        <div><p class="eyebrow">COMPOSER</p><h2>Give your idea room to grow.</h2><p class="muted">Choose a model and work through your idea together. Your conversation is saved so you can return to it.</p></div>
        <label>Name<input bind:value={name} maxlength="120" required placeholder="What are you thinking about?" disabled={busy} /></label>
        <label>Model<select bind:value={model} onchange={() => { reasoningEffort = ''; }} required disabled={busy || concreteModels.length === 0}>
          {#if concreteModels.length === 0}<option value="">{modelsLoading ? 'Loading models…' : 'No models available'}</option>{/if}
          {#each concreteModels as choice}<option value={choice.id}>{choice.id}</option>{/each}
        </select></label>
        <label>Reasoning effort<select bind:value={reasoningEffort} disabled={busy || !model}>
          <option value="">Provider default</option>
          {#each reasoningOptions(model, reasoningEffort, models) as effort}<option value={effort}>{effort}</option>{/each}
        </select></label>
        {#if modelError}<p class="error" role="alert">{modelError} <button class="text-button" type="button" onclick={loadModels}>Retry models</button></p>
        {:else if !modelsLoading && concreteModels.length === 0}<p class="muted">Add a provider in <a href="/settings">Settings</a> to choose a model.</p>{/if}
        <div><button class="primary-button" type="submit" disabled={busy || !name.trim() || !model}>{busy ? 'Creating…' : 'Create conversation'}</button></div>
      </form>
    {:else if selected}
      <div class="chat-heading">
        <div><h2>{selected.name}</h2><p>Composer</p></div>
        <div class="chat-heading-actions">
          <span class:running>{running ? 'Running' : 'Ready'}</span>
          {#if running}<button class="stop-control" type="button" title="Stop reply" aria-label="Stop reply" onclick={cancel} disabled={cancelling}><Square aria-hidden="true" size={13} fill="currentColor" /></button>{/if}
          <button type="button" title="Delete conversation" aria-label="Delete" onclick={remove} disabled={busy || running}><Trash2 aria-hidden="true" size={15} /></button>
        </div>
      </div>
      {#if selected.turns.length}
        <Transcript {messages} assistantLabel="Composer" label="Conversation history" sessionKey={selected.id} />
      {:else}
        <div class="chat-empty"><Bot aria-hidden="true" size={34} /><h3>What would you like to explore?</h3><p>Share a rough idea, a problem, or a possibility. Work through the details at your own pace.</p></div>
      {/if}
      {#if selected.question}
        <form class="run-question" onsubmit={(event) => { event.preventDefault(); void submitAnswer(); }}>
          <div><span>COMPOSER NEEDS INPUT</span><h3>{selected.question.question}</h3>{#if selected.question.context}<p>{selected.question.context}</p>{/if}</div>
          {#if selected.question.choices?.length}<div class="question-choices">{#each selected.question.choices as choice}<button type="button" onclick={() => submitAnswer(choice.id)} disabled={answering}><strong>{choice.label}</strong>{#if choice.description}<span>{choice.description}</span>{/if}</button>{/each}</div>{/if}
          <div class="question-answer"><input aria-label="Your answer" bind:value={answer} disabled={answering} placeholder="Write an answer…" maxlength="60000" /><button class="primary-button" type="submit" disabled={answering || !answer.trim()}>Answer</button></div>
        </form>
      {/if}
      <form class="composer-wrap" onsubmit={(event) => { event.preventDefault(); void send(); }}>
        <div class="composer">
          <textarea id="composer-message" aria-label="Develop your idea" bind:value={prompt} oninput={(event) => saveDraft(draftKey || `composer:${selected?.id}`, event.currentTarget.value)} onkeydown={onKey} disabled={busy} placeholder="Share an idea or continue the conversation…" rows="3" maxlength="60000"></textarea>
          <button class="send-button" type="submit" title="Send" aria-label="Send" disabled={busy || running || !prompt.trim()}><ArrowUp aria-hidden="true" size={17} /></button>
        </div>
        <div class="composer-footer"><span class="conversation-model" title={selected.model}>{selected.model}</span><p>{running ? 'Reply in progress' : 'Enter to send'} · Shift + Enter for a new line</p></div>
      </form>
    {:else}<div class="welcome"><p class="eyebrow">COMPOSER</p><h2>From a spark to a clearer idea.</h2><p class="muted">Talk with a model to explore possibilities, ask questions, and work out the details.</p><button class="primary-button" onclick={() => startCreate()}>New conversation</button></div>{/if}
  </section>
  {#if selected && !creating}{#key selected.id}<ComposerArtifacts composerID={selected.id} {running} bind:dirty={artifactDirty} />{/key}{/if}
</div>

<style>
  .composer-view { display:grid; flex:1; grid-template-columns:260px minmax(0,1fr); grid-template-rows:minmax(0,1fr); gap:22px; min-height:0; }
  .composer-view.with-artifacts { grid-template-columns:260px minmax(300px,1fr) minmax(360px,1.1fr); }
  .conversation-content { min-width:0; min-height:0; }
  .conversation-content.creating { overflow:auto; }
  .conversation-model { max-width:50%; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; padding:5px 8px; border:1px solid var(--border); border-radius:6px; color:var(--muted); background:var(--surface-raised); font-size:12px; }
  .conversation-list { overflow:auto; border-right:1px solid var(--border); padding:20px 14px 20px 0; }
  .list-heading { display:flex; align-items:center; justify-content:space-between; gap:12px; margin-bottom:16px; }
  .conversation-item { width:100%; display:flex; flex-direction:column; gap:6px; text-align:left; margin-bottom:8px; padding:12px; overflow-wrap:anywhere; }
  .conversation-item.selected { border-color:var(--violet); background:var(--violet-soft); }
  .conversation-item small,.muted { color:var(--muted); }
  .create-form { display:grid; flex:none; gap:20px; max-width:600px; width:100%; margin:30px auto; padding:0 22px; }
  .create-form label { display:grid; gap:8px; }
  .create-form input,.create-form select { width:100%; min-width:0; height:auto; min-height:38px; padding:10px; border:1px solid var(--border-strong); border-radius:6px; color:var(--text); background:var(--field-bg); font:inherit; line-height:1.5; }
  .create-form input:focus,.create-form select:focus { outline:2px solid var(--violet); outline-offset:2px; }
  .error { color:var(--error); overflow-wrap:anywhere; }
  .welcome { margin:50px auto; padding:0 22px; max-width:550px; }
  .welcome h2,.create-form h2 { font-size:calc(22px * var(--text-scale)); line-height:1.4; }
  .eyebrow { color:var(--accent-text); font-size:calc(11px * var(--text-scale)); letter-spacing:.15em; font-weight:700; }
  .composer-view.embedded { width:100%; height:100%; min-height:0; gap:0; grid-template-columns:minmax(0,1fr); grid-template-rows:minmax(0,1fr); }
  .composer-view.embedded.with-artifacts { grid-template-columns:minmax(300px,1fr) minmax(320px,1fr); }
  @media(max-width:1200px) {
    .composer-view.embedded.with-artifacts { grid-template-columns:minmax(0,1fr); height:auto; overflow:auto; }
    .embedded.with-artifacts :global(.artifacts) { grid-column:1; min-height:500px; }
    .conversation-content { min-height:600px; height:calc(100dvh - 136px); }
  }
  @media(max-width:800px) {
    .composer-view,.composer-view.with-artifacts { grid-template-columns:minmax(0,1fr); height:auto; }
    .conversation-list { max-height:200px; border-right:0; border-bottom:1px solid var(--border); }
    .conversation-content { min-height:560px; height:calc(100dvh - 270px); }
    .composer-view :global(.artifacts) { grid-column:1; }
  }
</style>
