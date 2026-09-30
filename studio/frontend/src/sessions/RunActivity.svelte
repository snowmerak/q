<script lang="ts">
  import { Terminal, Wrench } from '@lucide/svelte';
  import { eventBody, eventTitle, shortID } from './format';
  import type { FlatDelegation, RunEvent, SessionDetail } from './types';

  export let activeTask: SessionDetail['active_task'] = undefined;
  export let delegations: FlatDelegation[] = [];
  export let events: RunEvent[] = [];
</script>

<aside class="run-inspector" aria-label="Current turn activity">
  <div class="inspector-heading"><Terminal aria-hidden="true" size={16} /><span>Turn activity</span></div>
  {#if activeTask}<div class="active-task"><span>ACTIVE TASK</span><strong>{activeTask.objective}</strong></div>{/if}
  {#if delegations.length}
    <div class="delegation-tree">
      <span class="inspector-label">DELEGATION TREE</span>
      {#each delegations as node}
        <details style={`--tree-depth: ${node.depth}`}>
          <summary><span class="tree-branch" aria-hidden="true"></span><strong>{node.bookmark.agent}</strong><em>{node.state?.status || 'recorded'}</em></summary>
          <p>{node.bookmark.prompt}</p>
          {#if node.state?.change_request}<small class="change-request-summary">Change request {node.state.change_request.status} · {node.state.change_request.head_ref} · {shortID(node.state.change_request.base_commit)} → {shortID(node.state.change_request.head_commit || 'working')}</small>{/if}
          {#if node.state?.running_call}<small>Running {node.state.running_call.name} · {shortID(node.state.running_call.call_id)}</small>{/if}
          {#if node.state?.unknown_tools?.length}<small>{node.state.unknown_tools.length} interrupted tool call(s) require review</small>{/if}
          {#if node.issue}<small class="tree-issue">{node.issue}</small>{/if}
        </details>
      {/each}
    </div>
  {/if}
  <div class="event-list">
    {#each events as event}
      <article class:error-event={event.is_error || event.type === 'error'}>
        <div class="event-icon"><Wrench aria-hidden="true" size={13} /></div>
        <div><strong>{eventTitle(event)}</strong>{#if eventBody(event)}<p>{eventBody(event)}</p>{/if}</div>
      </article>
    {:else}
      <div class="events-empty"><p>Tool calls and agent activity from the current turn appear here.</p></div>
    {/each}
  </div>
</aside>
