<script lang="ts">
  import { tick } from 'svelte';
  import { highlightCode } from '../markdown';
  export let content: string;
  export let language: string;
  export let line = 1;
  export let online: (line: number) => void = () => {};
  let scroller: HTMLDivElement;
  $: normalized = content.replace(/\r\n/g, '\n');
  $: count = normalized.split('\n').length;
  $: rendered = highlightCode(normalized, language);
  $: if (line && content !== undefined) void reveal(line);
  async function reveal(value: number) {
    await tick();
    if (scroller) scroller.scrollTop = Math.max(0, (value - 4) * 24);
  }
</script>

<div class="file-code-scroll" bind:this={scroller}>
  <div class="file-code-grid">
    <div class="file-line-numbers">{#each Array(count) as _, index}<button class:selected={line === index + 1} aria-label={`File line ${index + 1}`} onclick={() => { line = index + 1; online(line); }}>{index + 1}</button>{/each}</div>
    <pre><code class={`hljs language-${language}`}>{@html rendered}</code></pre>
  </div>
</div>
