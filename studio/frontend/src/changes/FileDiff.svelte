<script lang="ts">
  import DiffView from '../DiffView.svelte';
  import { statusLabel } from './format';
  import type { ChangedFile, ChangeDetail } from './types';
  export let selected: ChangedFile | null;
  export let detail: ChangeDetail | null;
  export let loading: boolean;
  export let error: string;
</script>

  <section class="change-detail">
    <header><div><h2>{selected?.path || 'Repository changes'}</h2><p>{selected ? statusLabel(selected.status) : 'Select a changed file to inspect its patch.'}</p></div>{#if detail?.detail.sections.some((section) => section.truncated)}<span class="partial-badge">Partial preview</span>{/if}</header>
    {#if error}<div class="error-panel"><h2>Changes unavailable</h2><p>{error}</p></div>{:else if loading}<div class="loading-panel">Loading diff…</div>{:else if detail}{#key `${detail.root}/${detail.file.path}`}<DiffView sections={detail.detail.sections} />{/key}{:else}<div class="change-empty">No file selected.</div>{/if}
  </section>
