<script lang="ts">
  import { onMount } from 'svelte';
  import { requestResponse } from '../api';

  type Ticket = { id: string; workspace_id: string; number: number; title: string; kind: string; state: string; deleted: boolean; schedule?: { every?: string } };
  export let workspaceID: string;
  export let ticketID = '';
  export let ids: string[] = [];
  export let waitingOn: string[] = [];
  export let editable = false;
  let tickets: Ticket[] = [];
  let references: Ticket[] = [];
  let cursor = '';
  let query = '';
  let loading = false;
  let error = '';
  let mounted = false;
  let scope = '';
  let referenceKey = '';
  let generation = 0;
  let referenceGeneration = 0;
  $: candidates = tickets.filter(ticket => !ticket.deleted && ticket.id !== ticketID && !ticket.schedule?.every && `${ticket.number} ${ticket.title}`.toLowerCase().includes(query.toLowerCase()));
  $: if (mounted && scope !== workspaceID) { scope = workspaceID; tickets = []; references = []; cursor = ''; query = ''; if (editable) void load(); }
  $: if (mounted && referenceKey !== `${workspaceID}:${ids.join(',')}`) { referenceKey = `${workspaceID}:${ids.join(',')}`; void loadReferences([...ids]); }
  onMount(() => { mounted = true; return () => { generation++; referenceGeneration++; }; });

  async function load(more = false) {
    const request = ++generation;
    const workspace = workspaceID;
    loading = true;
    error = '';
    try {
      const response = await requestResponse(`/api/v1/work/items?${new URLSearchParams({ workspace_id: workspace, kind: 'ticket', limit: '100', cursor: more ? cursor : '' })}`);
      const page: { items: Ticket[]; next_cursor?: string } = await response.json();
      if (request !== generation || workspace !== workspaceID) return;
      tickets = more ? [...tickets, ...page.items] : page.items;
      cursor = page.next_cursor || '';
    } catch (cause) { if (request === generation) error = cause instanceof Error ? cause.message : String(cause); }
    finally { if (request === generation) loading = false; }
  }

  async function loadReferences(selected: string[]) {
    const request = ++referenceGeneration;
    try {
      const values = await Promise.all(selected.map(async id => (await requestResponse(`/api/v1/work/items/${encodeURIComponent(id)}`)).json() as Promise<Ticket>));
      if (request === referenceGeneration) references = values;
    } catch (cause) { if (request === referenceGeneration) error = cause instanceof Error ? cause.message : String(cause); }
  }

  function toggle(id: string, checked: boolean) {
    ids = checked ? [...ids, id] : ids.filter(value => value !== id);
  }
</script>

<fieldset class="ticket-dependencies">
  <legend>Prerequisite tickets</legend>
  {#if editable}
    <p>All selected tickets must be done before this ticket can run. Cancellation or rejection keeps it waiting.</p>
    <label>Find prerequisite tickets<input aria-label="Find prerequisite tickets" bind:value={query} type="search" placeholder="Ticket number or title" /></label>
    {#if ids.length}<ul aria-label="Selected prerequisites">{#each ids as id}<li><span>{references.find(ticket => ticket.id === id)?.title || id}</span><button type="button" class="secondary-button" aria-label={`Remove prerequisite ${references.find(ticket => ticket.id === id)?.title || id}`} onclick={() => toggle(id, false)}>Remove</button></li>{/each}</ul>{/if}
    <div class="dependency-options">
      {#each candidates as ticket (ticket.id)}
        <label class="dependency-option"><input type="checkbox" checked={ids.includes(ticket.id)} disabled={ids.length >= 64 && !ids.includes(ticket.id)} onchange={event => toggle(ticket.id, event.currentTarget.checked)} /><span>#{ticket.number} {ticket.title}<small>{ticket.state}</small></span></label>
      {:else}<p>{loading ? 'Loading tickets…' : 'No matching tickets in the loaded pages.'}</p>{/each}
    </div>
    {#if cursor}<button type="button" class="secondary-button" disabled={loading} onclick={() => load(true)}>More prerequisite tickets</button>{/if}
  {:else}
    <ul>{#each ids as id}<li><a href={`/tickets?${new URLSearchParams({ workspace_id: workspaceID, item_id: id })}`}>{#if references.find(ticket => ticket.id === id)}#{references.find(ticket => ticket.id === id)?.number} {references.find(ticket => ticket.id === id)?.title}{:else}{id}{/if}</a><small>{waitingOn.includes(id) ? 'Waiting for completion' : 'Done'}</small></li>{/each}</ul>
  {/if}
  {#if error}<p role="alert">{error}</p>{/if}
</fieldset>

<style>
  .ticket-dependencies { display: grid; min-width: 0; gap: 12px; margin: 0; padding: 14px; border: 1px solid var(--border); border-radius: 6px; }
  legend { padding: 0 5px; color: var(--text); }
  p { margin: 0; color: var(--muted); line-height: 1.6; }
  label { display: grid; min-width: 0; gap: 7px; color: var(--muted); }
  input[type="search"] { width: 100%; min-width: 0; height: calc(38px * var(--text-scale)); padding: 0 11px; font: inherit; }
  .dependency-options { display: grid; gap: 6px; max-height: 220px; overflow-y: auto; }
  .dependency-option { display: flex; align-items: start; gap: 9px; padding: 6px 0; color: var(--text); cursor: pointer; }
  input[type="checkbox"] { flex: none; margin-top: 4px; }
  span, a { min-width: 0; overflow-wrap: anywhere; }
  a { color: var(--accent-text); }
  small { display: block; color: var(--muted); }
  ul { display: grid; gap: 8px; margin: 0; padding-left: 20px; }
  li { min-width: 0; overflow-wrap: anywhere; }
  li button { margin-left: 8px; }
  button { width: fit-content; max-width: 100%; font: inherit; }
  [role="alert"] { color: var(--error); }
</style>
