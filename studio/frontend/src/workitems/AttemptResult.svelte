<script lang="ts">
  import Markdown from '../Markdown.svelte';

  export let result: unknown;

  const fields = [
    { key: 'summary', label: 'Summary' },
    { key: 'report', label: 'Report' },
    { key: 'findings', label: 'Findings' },
    { key: 'verification', label: 'Verification' },
    { key: 'blocker', label: 'Blocker' }
  ];

  $: record = result && typeof result === 'object' && !Array.isArray(result)
    ? result as Record<string, unknown> : {};
  $: sections = fields.flatMap(field => {
    const value = record[field.key];
    if (typeof value === 'string' && value) return [{ ...field, content: value, items: [] as string[] }];
    if (Array.isArray(value)) {
      const items = value.filter((item): item is string => typeof item === 'string' && !!item);
      if (items.length) return [{ ...field, content: '', items }];
    }
    return [];
  });
</script>

<div class="attempt-result">
  {#if typeof result === 'string'}
    <Markdown content={result} />
  {:else if sections.length}
    {#each sections as section}
      <section class="result-section" aria-label={section.label}>
        <h4>{section.label}</h4>
        {#if section.items.length}
          <ul>{#each section.items as item}<li><Markdown content={item} /></li>{/each}</ul>
        {:else}
          <Markdown content={section.content} />
        {/if}
      </section>
    {/each}
    <details><summary>Raw result</summary><pre>{JSON.stringify(result, null, 2)}</pre></details>
  {:else}
    <pre>{JSON.stringify(result, null, 2)}</pre>
  {/if}
</div>

<style>
  .attempt-result { display: grid; min-width: 0; gap: 16px; }
  .result-section { min-width: 0; overflow-x: auto; }
  h4 { margin: 0 0 8px; color: var(--muted); font-size: var(--type-small); font-weight: 600; }
  ul { display: grid; gap: 8px; margin: 0; padding-left: 1.5em; }
  li { min-width: 0; }
  details { min-width: 0; }
  summary { color: var(--muted); font-size: var(--type-small); cursor: pointer; }
  pre { overflow: auto; margin: 10px 0 0; padding: 16px; border: 1px solid var(--border); border-radius: 5px; background: var(--code-bg); color: var(--code-text); font: var(--type-small)/1.6 var(--font-code); white-space: pre-wrap; overflow-wrap: anywhere; }
</style>
