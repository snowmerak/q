<script lang="ts">
  import { GitCommitHorizontal, RefreshCw, RotateCcw, Send, Trash2 } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { apiError, requestJSON } from '../api';
  import { createSettingsOperations } from '../settings/operations';
  import { proposalMessage } from './format';
  import type { CommitReview, CommitResult } from './types';

  export let root: string;
  export let preferredPath = '';
  export let onrefresh: (preferred?: string) => Promise<void>;
  export let busy = false;
  export let reviewing = false;
  let review: CommitReview | null = null;
  let result: CommitResult | null = null;
  let disposed = false;
  let editGeneration = 0;
  const operations = createSettingsOperations();
  $: busy = $operations.busy;
  $: reviewing = !!review;

  export function prepareCommit() {
    if (!root || busy || review) return;
    const requestedRoot = root;
    const preferred = preferredPath;
    result = null;
    operations.run(async () => {
      review = await requestJSON<CommitReview>('POST', '/api/v1/workspaces/commits', { workspace_root: requestedRoot });
      if (!disposed) await onrefresh(preferred);
    });
  }

  function saveProposal(index: number, message: string) {
    if (!review) return;
    const id = review.id;
    const edit = ++editGeneration;
    operations.run(async () => {
      const saved = await requestJSON<CommitReview>('PUT', `/api/v1/workspaces/commits/${id}/proposals/${index}`, { message });
      if (edit === editGeneration) review = saved;
    });
  }

  function regenerate() {
    if (!review || busy) return;
    const id = review.id;
    operations.run(async () => {
      review = await requestJSON<CommitReview>('POST', `/api/v1/workspaces/commits/${id}/regenerate`);
    });
  }

  function executeCommit(push: boolean) {
    if (!review || busy || !confirm(push ? 'Create the reviewed commit(s) and push the current branch?' : 'Create the reviewed commit(s)?')) return;
    const id = review.id;
    operations.run(async () => {
      result = await requestJSON<CommitResult>('POST', `/api/v1/workspaces/commits/${id}/execute`, { push });
      review = null;
      if (!disposed) await onrefresh();
    });
  }

  async function removeReview() {
    if (!review) return;
    const response = await fetch(`/api/v1/workspaces/commits/${review.id}`, { method: 'DELETE' });
    if (!response.ok) throw new Error(await apiError(response));
    review = null;
  }

  function cancelReview() {
    if (busy) return;
    const preferred = preferredPath;
    operations.run(async () => {
      await removeReview();
      if (!disposed) await onrefresh(preferred);
    });
  }

  onDestroy(() => {
    disposed = true;
    // Finish queued writes before releasing a review, including one still
    // being prepared. The server owns the commit operation once submitted.
    operations.run(removeReview, '');
  });
</script>

  <aside class="commit-review">
    <div class="commit-review-heading"><div><p class="eyebrow">COMMIT REVIEW</p><h2>{review ? (review.proposals.length > 1 ? `${review.proposals.length} commit proposal` : 'Commit proposal') : result ? 'Commit result' : 'Ready when you are'}</h2></div>{#if review}<button class="danger-icon" title="Cancel review" onclick={cancelReview} disabled={$operations.busy}><Trash2 size={15} /></button>{/if}</div>
    {#if $operations.error}<p class="inline-warning">{$operations.error}</p>{/if}
    {#if $operations.busy && !review}<div class="commit-wait"><RefreshCw class="spin" size={18} /><p>Inspecting the index and preparing a proposal…</p></div>{/if}
    {#if review}
      {#if review.auto_staged}<p class="auto-stage-note">The workflow staged all visible working tree changes because the index was empty.</p>{/if}
      <div class="commit-progress">{#each review.progress.slice(-8) as event}<p><strong>{event.stage}</strong><span>{event.message}</span></p>{/each}</div>
      <div class="proposal-list">{#each review.proposals as proposal, index}<article><div><strong>{index + 1}. {proposal.type}{proposal.scope ? `(${proposal.scope})` : ''}</strong><small>{proposal.files?.length ? proposal.files.join(', ') : 'all staged files'}</small></div><textarea rows={proposal.body?.length ? 6 : 3} value={proposalMessage(proposal)} onchange={(event) => saveProposal(index, event.currentTarget.value)}></textarea></article>{/each}</div>
      <div class="commit-actions"><button class="text-button" onclick={regenerate} disabled={$operations.busy}><RotateCcw size={14} /> Regenerate</button><button class="primary-button" onclick={() => executeCommit(false)} disabled={$operations.busy}><GitCommitHorizontal size={15} /> Commit</button><button class="primary-button push-button" onclick={() => executeCommit(true)} disabled={$operations.busy}><Send size={14} /> Commit and push</button></div>
    {:else if result}
      <div class="commit-result"><strong>{result.status === 'pushed' ? 'Committed and pushed' : 'Committed'}</strong>{#each result.result.messages as message}<pre>{message}</pre>{/each}{#if result.push_error}<p class="inline-warning">Commit succeeded. Push failed: {result.push_error}</p>{/if}</div>
    {:else if !$operations.busy}
      <div class="commit-intro"><GitCommitHorizontal size={26} /><p>Prepare a proposal from the current index. If the index is empty, Q stages visible working tree changes as the TUI workflow does.</p></div>
    {/if}
  </aside>
