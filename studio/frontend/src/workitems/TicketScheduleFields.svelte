<script lang="ts">
  import { CalendarClock } from '@lucide/svelte';
  import type { ScheduleDraft } from './schedule';

  export let draft: ScheduleDraft;
  const timezone = Intl.DateTimeFormat().resolvedOptions().timeZone;
</script>

<fieldset class="ticket-schedule">
  <legend><CalendarClock aria-hidden="true" size={16} />Schedule</legend>
  <label class="toggle"><input type="checkbox" bind:checked={draft.enabled} />Schedule execution</label>
  {#if draft.enabled}
    <div class="schedule-fields">
      <label>Available from<input aria-label="Available from" type="datetime-local" step="1" bind:value={draft.notBefore} required={!draft.every.trim()} /></label>
      <label>Repeat every<input aria-label="Repeat every" bind:value={draft.every} placeholder="Once · or 30m, 2h, 24h" /></label>
    </div>
    <p>Times use {timezone}. Leave the interval empty to run once. Repeats require at least 1m; 24h is one day. Leave the start empty to begin now when repeating.</p>
    <label class="toggle"><input type="checkbox" bind:checked={draft.paused} />Pause schedule</label>
    <p>Each repeat creates a separate ticket for the requested member or System One assignment. Pause stops future occurrences; existing run tickets continue. Unresolved runs prevent overlap; missed intervals are combined into the latest occurrence.</p>
  {/if}
</fieldset>

<style>
  .ticket-schedule { min-width: 0; margin: 0; padding: 16px; display: grid; gap: 12px; border: 1px solid var(--border-strong); border-radius: 6px; }
  legend { padding: 0 7px; color: var(--text); }
  legend :global(svg) { vertical-align: text-bottom; margin-right: 6px; }
  label { display: grid; min-width: 0; gap: 7px; color: var(--muted); }
  .toggle { display: flex; align-items: center; gap: 8px; }
  .schedule-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; }
  input:not([type="checkbox"]) { width: 100%; min-width: 0; height: calc(38px * var(--text-scale)); padding: 0 11px; font: inherit; }
  p { margin: 0; color: var(--muted); font-size: .9em; line-height: 1.6; }
  @media (max-width: 700px) { .schedule-fields { grid-template-columns: 1fr; } }
</style>
