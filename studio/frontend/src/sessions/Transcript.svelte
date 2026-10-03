<script lang="ts">
  import { Bot, User, Wrench } from '@lucide/svelte';
  import Markdown from '../Markdown.svelte';
  import { shortID, toolArguments } from './format';
  import type { Message } from './types';

  export let messages: Message[] = [];
  export let thinkingDraft = '';
  export let responseDraft = '';
  export let loading = false;
  export let onfile: ((path: string) => void) | undefined = undefined;
  let transcriptElement: HTMLElement | null = null;
  const tokenNumber = new Intl.NumberFormat();

  export function scrollToBottom() {
    transcriptElement?.scrollTo({ top: transcriptElement.scrollHeight, behavior: 'smooth' });
  }
</script>

<div class="transcript" bind:this={transcriptElement} aria-live="polite">
  {#each messages as message, messageIndex (`${messageIndex}-${message.role}-${message.tool_call_id || ''}`)}
    {#if message.role === 'user' || message.role === 'assistant'}
      <article class="chat-message" class:user-message={message.role === 'user'}>
        <div class="message-avatar">{#if message.role === 'user'}<User aria-hidden="true" size={16} />{:else}<Bot aria-hidden="true" size={17} />{/if}</div>
        <div class="message-content"><header>{message.role === 'user' ? 'You' : 'Q'}</header>{#if message.content}<Markdown content={message.content} {onfile} />{/if}
          {#if message.tool_calls?.length}
            <div class="message-tools">
              {#each message.tool_calls as call, callIndex (`${callIndex}-${call.id || ''}`)}
                <details class="tool-card">
                  <summary><Wrench aria-hidden="true" size={14} /><span>{call.function?.name || 'Tool call'}</span><code>{call.id ? shortID(call.id) : 'pending'}</code></summary>
                  {#if call.function?.arguments}<Markdown compact content={toolArguments(call.function.arguments)} {onfile} />{/if}
                </details>
              {/each}
            </div>
          {/if}
          {#if message.role === 'assistant' && !message.tool_calls?.length && message.usage}
            <footer class="response-usage" aria-label="Response token usage">
              <span>Input {tokenNumber.format(message.usage.input_tokens)}</span>
              <span title={message.usage.cached_tokens === undefined ? 'The provider did not report cached input tokens' : 'Cached input is included in input tokens'}>Cached {message.usage.cached_tokens === undefined ? '—' : tokenNumber.format(message.usage.cached_tokens)}</span>
              <span>Output {tokenNumber.format(message.usage.output_tokens)}</span>
            </footer>
          {/if}
        </div>
      </article>
    {:else if message.role === 'tool'}
      <details class="transcript-tool-result">
        <summary><Wrench aria-hidden="true" size={14} /><span>{message.name || 'Tool result'}</span>{#if message.tool_call_id}<code>{shortID(message.tool_call_id)}</code>{/if}</summary>
        <Markdown compact content={message.content || '_No output_'} {onfile} />
      </details>
    {/if}
  {/each}
  {#if thinkingDraft}<details class="thinking-block"><summary>Thinking</summary><Markdown compact content={thinkingDraft} {onfile} /></details>{/if}
  {#if responseDraft}<article class="chat-message streaming-message"><div class="message-avatar"><Bot aria-hidden="true" size={17} /></div><div class="message-content"><header>Q <span>responding</span></header><Markdown content={responseDraft} {onfile} /></div></article>{/if}
  {#if loading}<div class="transcript-loading">Loading session…</div>{/if}
</div>
