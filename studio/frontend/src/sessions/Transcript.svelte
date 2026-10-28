<script lang="ts">
  import { Bot, User, Wrench } from '@lucide/svelte';
  import Markdown from '../Markdown.svelte';
  import { eventBody, eventTitle, shortID, toolArguments } from './format';
  import { buildTimeline } from './timeline';
  import type { Message, RunEvent } from './types';
  import { afterUpdate, beforeUpdate } from 'svelte';

  export let messages: Message[] = [];
  export let thoughts: { before: number; content: string }[] = [];
  export let events: RunEvent[] = [];
  export let sessionKey = '';
  export let loading = false;
  export let onfile: ((path: string) => void) | undefined = undefined;
  export let assistantLabel = 'Q';
  export let label = '';
  let transcriptElement: HTMLElement | null = null;
  let follow = true;
  let previousSession = '';
  const tokenNumber = new Intl.NumberFormat();
  const cachePercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  $: entries = buildTimeline(messages, thoughts, events);

  function nearBottom() {
    if (!transcriptElement) return true;
    const distance = transcriptElement.scrollHeight - transcriptElement.clientHeight;
    return distance <= 0 || transcriptElement.scrollTop / distance >= 0.95;
  }

  beforeUpdate(() => { follow = sessionKey !== previousSession || nearBottom(); });
  afterUpdate(() => {
    previousSession = sessionKey;
    if (follow) transcriptElement?.scrollTo({ top: transcriptElement.scrollHeight, behavior: 'instant' });
  });
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex (Named conversation regions support keyboard scrolling.) -->
<div class="transcript" bind:this={transcriptElement} aria-live="polite" role={label ? 'region' : undefined} aria-label={label || undefined} tabindex={label ? 0 : undefined}>
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
      <article class="chat-message" class:user-message={message.role === 'user'} class:streaming-message={entry.streaming}>
        <div class="message-avatar">{#if message.role === 'user'}<User aria-hidden="true" size={16} />{:else}<Bot aria-hidden="true" size={17} />{/if}</div>
        <div class="message-content"><header>{message.role === 'user' ? 'You' : assistantLabel}{#if entry.streaming || message.status === 'running'}<span>responding</span>{/if}</header>{#if message.content}<Markdown content={message.content} {onfile} />{/if}
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
  .message-progress { color:var(--muted); font-size:calc(13px * var(--text-scale)); }
  .message-failure { color:var(--error); font-size:calc(13px * var(--text-scale)); overflow-wrap:anywhere; }
</style>
