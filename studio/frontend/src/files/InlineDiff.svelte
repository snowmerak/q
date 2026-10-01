<script lang="ts">
  import { tick } from 'svelte';
  import type { FileDiff } from './types';
  import { fullFileDiff, type InlineLine } from './inline-diff';
  export let diff: FileDiff;
  export let language: string;
  export let line = 1;
  export let online: (line: number) => void = () => {};
  let rows: InlineLine[] = [];
  let error = '';
  let scroller: HTMLDivElement;
  $: patch = diff.sections.map((section) => section.patch).join('\n');
  $: partial = diff.content.truncated || diff.sections.some((section) => section.truncated);
  $: binary = diff.content.binary || /^Binary files .* differ$|^GIT binary patch$/m.test(patch);
  $: build(diff.content.content, patch, diff.all_added, partial, language, binary);
  $: if (line && rows.length) void reveal(line, rows);
  function build(content: string, patch: string, added: boolean, truncated: boolean, language: string, binary: boolean) {
    error = '';
    try { rows = binary ? [] : fullFileDiff(content, patch, added, truncated, language); }
    catch (reason) { rows = []; error = reason instanceof Error ? reason.message : 'Could not compare file lines'; }
  }
  async function reveal(value: number, current: InlineLine[]) {
    await tick();
    const index = current.findIndex((row) => row.newLine === value);
    if (scroller && index >= 0) scroller.scrollTop = Math.max(0, (index - 3) * 24);
  }
</script>

<div class="file-diff-meta"><span>{diff.comparison === 'staged' ? 'HEAD → index' : diff.comparison === 'unstaged' ? 'Index → working tree' : 'HEAD → working tree'}</span>{#if partial}<span>Partial preview · change markings may be incomplete</span>{:else if !patch && !diff.all_added}<span>No Git changes for this file.</span>{/if}</div>
{#if error}<div class="file-view-state" role="alert">{error}</div>
{:else if binary}<div class="file-view-state">Binary or non-UTF-8 file · content is not displayed.</div>
{:else if !rows.length}<div class="file-view-state">{diff.content.missing && diff.comparison === 'staged' ? 'No file content in the index.' : 'Empty file.'}</div>
{:else}
  <div class="inline-file-diff-scroll" bind:this={scroller}>
    <div class="inline-file-diff-grid">
      <div class="inline-diff-columns"><span>Before</span><span>After</span><span></span></div>
      {#each rows as row, index}
        <div class="inline-file-diff-row" class:added={row.kind === 'added'} class:removed={row.kind === 'removed'} class:selected={row.newLine === line} data-old-line={row.oldLine} data-new-line={row.newLine}>
          <div class="inline-diff-gutter"><span title="Previous line number">{row.oldLine ?? ''}</span>{#if row.newLine}<button aria-label={`Diff line ${row.newLine}`} onclick={() => online(row.newLine!)}>{row.newLine}</button>{:else}<span></span>{/if}<span class="inline-diff-marker" aria-label={row.kind === 'added' ? 'Added line' : row.kind === 'removed' ? 'Removed line' : undefined}>{row.kind === 'added' ? '+' : row.kind === 'removed' ? '−' : ''}</span></div>
          <code class="hljs">{@html row.html || ' '}</code>
        </div>
      {/each}
    </div>
  </div>
{/if}
