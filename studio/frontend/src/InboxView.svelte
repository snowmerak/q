<script module lang="ts">
  import type { UserQuestion } from './workitems/TicketDecision.svelte';
  export type InboxEntry = { workspace_id: string; project: string; ticket_id: string; number: number; title: string; status: string; attempt_id?: string; detail?: string; updated_at: string; question?: UserQuestion; revision: number };
</script>

<script lang="ts">
  import { Bell, ArrowUpRight, RefreshCw } from '@lucide/svelte';
  import TicketDecision from './workitems/TicketDecision.svelte';
  export let entries: InboxEntry[] = [];
  export let more = false;
  export let error = '';
  export let refresh: () => void;
  export let onopen: (path: string) => void;

  let project = '';
  let notifications = typeof Notification !== 'undefined' ? Notification.permission : 'unsupported';
  $: projects = [...new Map(entries.map(entry => [entry.workspace_id, entry.project]))];
  $: visible = entries.filter(entry => !project || entry.workspace_id === project);

  const status: Record<string, string> = {
    awaiting_review: 'Ready for review', blocked: 'Blocked', failed: 'Failed', unknown: 'Execution outcome unknown',
    merge_failed: 'Merge needs attention', close_failed: 'Cleanup needs attention',
    waiting_user: 'Decision requested', waiting_approval: 'Start approval requested'
  };

  async function enableNotifications() {
    try { notifications = await Notification.requestPermission(); }
    catch { notifications = 'unsupported'; }
  }
</script>

<section class="inbox" aria-label="Project inbox">
  <div class="toolbar">
    <label>Project<select bind:value={project}><option value="">All projects</option>{#each projects as [id, name]}<option value={id}>{name}</option>{/each}</select></label>
    <div>
      {#if notifications === 'default'}<button class="secondary-button" onclick={enableNotifications}><Bell size={16} />Enable desktop notifications</button>{/if}
      <button class="secondary-button" aria-label="Refresh inbox" onclick={refresh}><RefreshCw size={16} />Refresh</button>
    </div>
  </div>
  {#if error}<p role="alert">{error}</p>{/if}
  {#if more}<p>Showing the 100 most recent items.</p>{/if}
  {#each visible as entry (entry.ticket_id)}
    <article>
      <div><small>{entry.project} · {status[entry.status] || entry.status}</small><h2>#{entry.number} {entry.title}</h2>
      {#if entry.detail}<p>{entry.detail}</p>{/if}
      {#if entry.status === 'waiting_user' && entry.question}
        {#key entry.question.id}<TicketDecision ticketID={entry.ticket_id} attemptID={entry.attempt_id} question={entry.question} onresolved={refresh} />{/key}
      {/if}</div>
      <button class="secondary-button" onclick={() => onopen('/tickets?' + new URLSearchParams({ workspace_id: entry.workspace_id, item_id: entry.ticket_id, ...(entry.attempt_id ? { attempt_id: entry.attempt_id } : {}) }))}>Open ticket<ArrowUpRight size={16} /></button>
    </article>
  {:else}
    <p role="status">No tickets need your attention.</p>
  {/each}
</section>

<style>
  .inbox { display: grid; min-width: 0; gap: .75rem; margin-block: 1rem; }
  .toolbar, article { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: .75rem; }
  .toolbar label { display: flex; align-items: center; gap: .5rem; }
  .toolbar > div { display: flex; gap: .5rem; }
  select { min-height: 2rem; padding: .3rem 1.7rem .3rem .5rem; }
  button { display: inline-flex; align-items: center; gap: .4rem; padding: .45rem .65rem; }
  article { border: 1px solid var(--border); border-radius: 8px; padding: .75rem 1rem; background: var(--surface-raised); }
  article div { min-width: 0; }
  small, p { color: var(--muted); overflow-wrap: anywhere; }
  h2 { font-size: calc(16px * var(--text-scale)); margin: .25rem 0 0; }
  p { margin: .35rem 0 0; }
</style>
