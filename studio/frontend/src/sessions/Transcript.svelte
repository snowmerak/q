<script lang="ts">
  import { Bot, ChevronUp, Pencil, RotateCcw, Trash2, User, Wrench } from '@lucide/svelte';
  import Markdown from '../Markdown.svelte';
  import { eventBody, eventTitle, shortID, toolArguments } from './format';
  import { buildTimeline } from './timeline';
  import type { Message, ResponseAction, RunEvent } from './types';
  import { afterUpdate, beforeUpdate, tick } from 'svelte';

  export let messages: Message[] = [];
  export let thoughts: { before: number; content: string }[] = [];
  export let events: RunEvent[] = [];
  export let sessionKey = '';
  export let historyRevision = '';
  export let useSavedReply = false;
  export let loading = false;
  export let onfile: ((path: string) => void) | undefined = undefined;
  export let assistantLabel = 'Q';
  export let label = '';
  export let hasEarlier = false;
  export let loadingEarlier = false;
  export let onearlier: (() => Promise<void>) | undefined = undefined;
  export let onresponse: ((action: ResponseAction, index: number, content?: string) => void) | undefined = undefined;
  export let responseActionsDisabled = false;
  export let canRegenerate = false;
  let prepending = false;
  let transcriptElement: HTMLElement | null = null;
  let follow = true;
  let previousSession = '';
  let editingIndex: number | undefined;
  let responseDraft = '';
  let editorContext = '';
  $: if (editorContext !== `${sessionKey}/${historyRevision}`) {
    editorContext = `${sessionKey}/${historyRevision}`;
    editingIndex = undefined;
  }
  const tokenNumber = new Intl.NumberFormat();
  const cachePercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  $: entries = buildTimeline(messages, thoughts, events, useSavedReply);

  function nearBottom() {
    if (!transcriptElement) return true;
    const distance = transcriptElement.scrollHeight - transcriptElement.clientHeight;
    return distance <= 0 || transcriptElement.scrollTop / distance >= 0.95;
  }

  beforeUpdate(() => { follow = sessionKey !== previousSession || (!prepending && nearBottom()); });
  afterUpdate(() => {
    previousSession = sessionKey;
    if (follow) transcriptElement?.scrollTo({ top: transcriptElement.scrollHeight, behavior: 'instant' });
  });

  async function loadEarlier() {
    if (!transcriptElement || !hasEarlier || loadingEarlier || prepending || !onearlier) return;
    const element = transcriptElement;
    const key = sessionKey;
    const height = element.scrollHeight;
    const top = element.scrollTop;
    prepending = true;
    try {
      await onearlier();
      await tick();
      if (sessionKey === key) element.scrollTop = top + element.scrollHeight - height;
    } finally {
      prepending = false;
    }
  }

  function scrolled() {
    if (!loading && transcriptElement && transcriptElement.scrollTop <= 80) void loadEarlier();
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex (Named conversation regions support keyboard scrolling.) -->
<div class="transcript" bind:this={transcriptElement} onscroll={scrolled} aria-live="polite" role={label ? 'region' : undefined} aria-label={label || undefined} tabindex={label ? 0 : undefined}>
  {#if hasEarlier}<button class="secondary-button history-earlier" disabled={loadingEarlier} onclick={loadEarlier}><ChevronUp aria-hidden="true" size={15} />{loadingEarlier ? 'Loading earlier messages…' : 'Load earlier messages'}</button>{/if}
  {#each entries as entry, entryIndex (entryIndex)}
    {#if entry.kind === 'thinking'}
      <details class="thinking-block"><summary>Thinking</summary><Markdown compact content={entry.content} {onfile} /></details>
    {:else if entry.kind === 'activity'}
      <details class="transcript-activity" class:error-event={entry.event.is_error || ['error', 'cancelled', 'recovered'].includes(entry.event.type)}>
        <summary><Wrench aria-hidden="true" size={14} /><span>{eventTitle(entry.event)}</span></summary>
        {#if eventBody(entry.event)}<Markdown compact content={entry.event.type === 'trace' ? toolArguments(eventBody(entry.event)) : eventBody(entry.event)} {onfile} />{/if}
      </details>
    {:else}
    {@const message = entry.message}
    {#if message.role === 'user' || message.role === 'assistant'}
      {@const responseIndex = onresponse && message.role === 'assistant' && message.content?.trim() && !entry.streaming && !message.tool_calls?.length ? message.index : undefined}
      <article class="chat-message" class:user-message={message.role === 'user'} class:streaming-message={entry.streaming}>
        <div class="message-avatar">{#if message.role === 'user'}<User aria-hidden="true" size={16} />{:else}<Bot aria-hidden="true" size={17} />{/if}</div>
        <div class="message-content"><header>{message.role === 'user' ? 'You' : assistantLabel}{#if entry.streaming || message.status === 'running'}<span>responding</span>{/if}</header>
          {#if responseIndex !== undefined && responseIndex === editingIndex}
            <form class="response-editor" onsubmit={(event) => { event.preventDefault(); onresponse?.('edit', responseIndex, responseDraft); }}>
              <textarea rows="5" aria-label="Response text" bind:value={responseDraft} disabled={responseActionsDisabled}></textarea>
              <div class="response-actions">
                <button type="submit" class="primary-button" disabled={responseActionsDisabled || !responseDraft.trim() || responseDraft === message.content}>Save response</button>
                <button type="button" class="secondary-button" onclick={() => editingIndex = undefined}>Cancel</button>
              </div>
            </form>
          {:else if message.content}
            <Markdown content={message.content} {onfile} />
          {/if}
          {#each message.images || [] as image, index}<img class="message-image" src={image} alt={`Attached image ${index + 1}`} loading="lazy" />{/each}
          {#if message.status === 'running' && !message.content}<p class="message-progress" role="status">Thinking through your idea…</p>
          {:else if message.status && !['completed', 'running'].includes(message.status)}<p class="message-failure">{message.status === 'cancelled' ? 'Reply stopped. You can continue the conversation.' : message.error || 'This reply did not finish.'}</p>{/if}
          {#if message.tool_calls?.length}
            <div class="message-tools">
              {#each message.tool_calls as call, callIndex (`${callIndex}-${call.id || ''}`)}
                <details class="tool-card">
                  <summary><Wrench aria-hidden="true" size={14} /><span>{call.function?.name || 'Tool call'}</span><small class="tool-kind">Call</small><code>{call.id ? shortID(call.id) : 'pending'}</code></summary>
                  {#if call.function?.arguments}<Markdown compact content={toolArguments(call.function.arguments)} {onfile} />{/if}
                </details>
              {/each}
            </div>
          {/if}
          {#if message.role === 'assistant' && !message.tool_calls?.length && message.usage}
            <footer class="response-usage" aria-label="Response token usage">
              <span>Input {tokenNumber.format(message.usage.input_tokens)}</span>
              <span title={message.usage.cached_tokens === undefined ? 'The provider did not report cached input tokens' : 'Cached input is included in input tokens'}>Cached {message.usage.cached_tokens === undefined ? '—' : tokenNumber.format(message.usage.cached_tokens)}</span>
              <span title="Cached input tokens divided by all input tokens">Cache hit {message.usage.cached_tokens === undefined || message.usage.input_tokens <= 0 ? '—' : cachePercent.format(Math.min(1, Math.max(0, message.usage.cached_tokens / message.usage.input_tokens)))}</span>
              <span>Output {tokenNumber.format(message.usage.output_tokens)}</span>
            </footer>
          {/if}
          {#if responseIndex !== undefined && responseIndex !== editingIndex}
            <div class="response-actions" role="group" aria-label="Response actions">
              <button type="button" class="icon-button" title={canRegenerate ? 'Regenerate response' : 'Choose an API model to regenerate'} aria-label="Regenerate response" disabled={responseActionsDisabled || !canRegenerate} onclick={() => onresponse?.('regenerate', responseIndex)}><RotateCcw aria-hidden="true" size={14} /></button>
              <button type="button" class="icon-button" title="Edit response" aria-label="Edit response" disabled={responseActionsDisabled} onclick={() => { editingIndex = responseIndex; responseDraft = message.content || ''; }}><Pencil aria-hidden="true" size={14} /></button>
              <button type="button" class="icon-button" title="Delete response" aria-label="Delete response" disabled={responseActionsDisabled} onclick={() => { if (window.confirm('Delete this response from the conversation?')) onresponse?.('delete', responseIndex); }}><Trash2 aria-hidden="true" size={14} /></button>
            </div>
          {/if}
        </div>
      </article>
    {:else if message.role === 'tool'}
      <details class="transcript-tool-result">
        <summary><Wrench aria-hidden="true" size={14} /><span>{message.name || 'Tool result'}</span><small class="tool-kind">Result</small>{#if message.tool_call_id}<code>{shortID(message.tool_call_id)}</code>{/if}</summary>
        <Markdown compact content={message.content || '_No output_'} {onfile} />
      </details>
    {/if}
    {/if}
  {/each}
  {#if loading}<div class="transcript-loading">Loading session…</div>{/if}
</div>

<style>
  .message-image { display: block; max-width: min(100%, 540px); max-height: 420px; object-fit: contain; margin-block: .5rem; border-radius: 6px; }
  .history-earlier { display:flex; align-items:center; gap:.5rem; margin:0 auto 1rem; color:var(--muted); }
  .message-progress { color:var(--muted); font-size:calc(13px * var(--text-scale)); }
  .message-failure { color:var(--error); font-size:calc(13px * var(--text-scale)); overflow-wrap:anywhere; }
  .response-actions { display: flex; align-items: center; gap: 6px; margin-top: .45rem; }
  .response-actions .icon-button { width: 28px; min-height: 28px; color: var(--muted); border: 0; background: transparent; }
  .response-actions .icon-button:hover:not(:disabled) { color: var(--text); }
  .response-actions .icon-button:disabled { cursor: not-allowed; opacity: .55; }
  .response-editor textarea { width: 100%; resize: vertical; }
</style>
