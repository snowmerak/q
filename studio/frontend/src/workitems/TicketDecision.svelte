<script module lang="ts">
  export type UserQuestion = { id: string; question: string; context?: string; choices?: string[]; answer?: string; late?: boolean };
</script>

<script lang="ts">
  import { requestResponse } from '../api';
  import Markdown from '../Markdown.svelte';

  export let ticketID: string;
  export let attemptID = '';
  export let question: UserQuestion | undefined = undefined;
  export let approvalRevision = 0;
  export let onresolved: () => void = () => {};
  let reply = '';
  let busy = false;
  let error = '';

  async function submit(value: string) {
    if (busy) return;
    const pending = question;
    const request_id = crypto.randomUUID();
    const body = pending
      ? { question_id: pending.id, answer: value.trim(), request_id }
      : { revision: approvalRevision, action: 'approve_start', request_id };
    if (pending && (!attemptID || !body.answer)) return;
    busy = true;
    error = '';
    try {
      const path = pending ? `attempts/${encodeURIComponent(attemptID)}/answer` : `items/${encodeURIComponent(ticketID)}/control`;
      await requestResponse('/api/v1/work/' + path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
      reply = '';
      onresolved();
    } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not record the decision.'; }
    finally { busy = false; }
  }
</script>

{#if question}
  <form onsubmit={event => { event.preventDefault(); void submit(reply); }} aria-label="Worker decision">
    <strong>{question.question}</strong>
    {#if question.context}<Markdown content={question.context} />{/if}
    {#if question.answer}<p>Answer recorded · {question.answer}</p>
    {:else}
      {#if question.choices?.length}<div class="choices">{#each question.choices as choice}<button type="button" class="secondary-button" disabled={busy} onclick={() => submit(choice)}>{choice}</button>{/each}</div>{/if}
      <label>Your answer<textarea bind:value={reply} maxlength="8192" rows="2"></textarea></label>
      <button type="submit" class="primary-button" disabled={busy || !reply.trim()}>Send answer</button>
    {/if}
  </form>
{:else if approvalRevision}
  <button type="button" class="primary-button" disabled={busy} onclick={() => submit('')}>Approve and queue</button>
{/if}
{#if error}<p role="alert">{error}</p>{/if}

<style>
  form, label { display: grid; gap: .4rem; }
  form { border-block-start: 1px solid var(--border); padding-top: .65rem; margin-top: .65rem; }
  textarea { padding: .4rem; background: var(--field-bg); color: var(--text); border: 1px solid var(--border-strong); border-radius: 5px; resize: vertical; }
  button { justify-self: start; padding: .45rem .65rem; }
  .choices { display: flex; flex-wrap: wrap; gap: .4rem; }
</style>
