<script lang="ts">
  import type { ModelSampling } from './types';

  export let sampling: ModelSampling = {};
  export let model: string;
  export let save: () => void;

  const fields: { key: keyof ModelSampling; min: number; max?: number; step: string; hint: string }[] = [
    { key: 'temperature', min: 0, step: 'any', hint: '0 selects the most likely token' },
    { key: 'top_p', min: 0, max: 1, step: 'any', hint: '1 disables cumulative probability filtering' },
    { key: 'top_k', min: 0, step: '1', hint: '0 disables the top-K limit' },
    { key: 'min_p', min: 0, max: 1, step: 'any', hint: '0 disables minimum probability filtering' },
    { key: 'repetition_penalty', min: 0, step: 'any', hint: '1 disables repetition suppression; must be greater than 0' },
    { key: 'presence_penalty', min: -2, max: 2, step: 'any', hint: '0 disables the presence penalty' },
    { key: 'frequency_penalty', min: -2, max: 2, step: 'any', hint: '0 disables the frequency penalty' }
  ];

  function preset() {
    sampling = { temperature: 0, top_p: 1, top_k: 0, min_p: 0, repetition_penalty: 1, presence_penalty: 0, frequency_penalty: 0 };
    save();
  }

  function reset() {
    sampling = {};
    save();
  }
</script>

<details class="sampling-settings">
  <summary>Sampling overrides</summary>
  <p>Leave a field blank to keep the request or provider default. Values apply to the next request. Available parameters depend on the provider and model.</p>
  <div class="sampling-actions">
    <button class="primary-button compact-button" onclick={preset}>Greedy / penalties off</button>
    <button class="secondary-button compact-button" onclick={reset}>Clear overrides</button>
  </div>
  <div class="sampling-grid">
    {#each fields as field}
      <label><span>{field.key}</span><input type="number" aria-label={field.key} aria-describedby={`sampling-${encodeURIComponent(model)}-${field.key}`} min={field.min} max={field.max} step={field.step} placeholder="Request / provider default" bind:value={sampling[field.key]} onchange={(event) => { if (event.currentTarget.reportValidity()) save(); }} /><small id={`sampling-${encodeURIComponent(model)}-${field.key}`}>{field.hint}</small></label>
    {/each}
  </div>
</details>

<style>
  .sampling-settings { grid-column: 1 / -1; min-width: 0; }
  summary { color: var(--muted); cursor: pointer; font-size: calc(13px * var(--text-scale)); }
  p, small { color: var(--subtle); font-size: calc(12px * var(--text-scale)); }
  .sampling-actions { display: flex; flex-wrap: wrap; gap: 8px; margin: 12px 0; }
  .sampling-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 200px), 1fr)); gap: 16px; }
  label { display: grid; grid-template-rows: auto auto 1fr; min-width: 0; gap: 6px; color: var(--muted); font-size: calc(12px * var(--text-scale)); }
  input { width: 100%; min-width: 0; }
</style>
