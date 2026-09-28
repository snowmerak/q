<script lang="ts">
  import { highlightCode } from './markdown';

  export let sections: { title: string; patch: string; truncated: boolean }[] = [];
  let selectedLine = '';

  function lines(patch: string) {
    return patch.replace(/\n$/, '').split('\n');
  }

  function selectLine(sectionIndex: number, lineIndex: number) {
    selectedLine = `${sectionIndex + 1}-L${lineIndex + 1}`;
    const url = new URL(window.location.href);
    url.hash = `diff-${selectedLine}`;
    window.history.replaceState({}, '', url.pathname + url.search + url.hash);
  }
</script>

<div class="diff-view">
  {#each sections as section, sectionIndex}
    <section class="diff-section">
      <header><strong>{section.title}</strong>{#if section.truncated}<span>Partial preview</span>{/if}</header>
      <div class="diff-code hljs">
        {#each lines(section.patch) as line, lineIndex}
          <div id={`diff-${sectionIndex + 1}-L${lineIndex + 1}`} class:selected={selectedLine === `${sectionIndex + 1}-L${lineIndex + 1}`} class="diff-line">
            <button aria-label={`Link to diff line ${lineIndex + 1} in ${section.title}`} onclick={() => selectLine(sectionIndex, lineIndex)}>{lineIndex + 1}</button>
            <code>{@html highlightCode(line || ' ', 'diff')}</code>
          </div>
        {/each}
      </div>
    </section>
  {:else}
    <div class="diff-empty">No diff remains for this file.</div>
  {/each}
</div>
