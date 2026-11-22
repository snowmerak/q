<script lang="ts">
  import { ChevronDown, Timer } from '@lucide/svelte';
  import type { ModelCallTiming } from './types';

  export let calls: ModelCallTiming[];
  const decimal = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
  const count = new Intl.NumberFormat();
  const percent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
  $: latest = calls.at(-1);

  function duration(ms?: number) {
    if (ms === undefined) return '—';
    return ms < 1000 ? `${count.format(ms)} ms` : `${decimal.format(ms / 1000)} s`;
  }

  function rate(tokensPerSecond?: number) {
    return tokensPerSecond === undefined ? '—' : `${decimal.format(tokensPerSecond)} tok/s`;
  }

  function tokens(value?: number) {
    return value === undefined ? '—' : count.format(value);
  }
</script>

{#if latest}
  <details class="model-timing">
    <summary>
      <Timer size={15} aria-hidden="true" />
      <span>Model timing</span>
      <small class="latest" title="Most recent streaming model call">
        {#if latest.ttft_ms !== undefined}<span>TTFT {duration(latest.ttft_ms)}</span>{/if}
        <span>{latest.status === 'completed' ? 'Total' : 'Elapsed'} {duration(latest.duration_ms)}</span>
        {#if latest.output_tokens_per_second !== undefined}<span>{rate(latest.output_tokens_per_second)}</span>{/if}
        {#if latest.total_tokens !== undefined}<span>{tokens(latest.total_tokens)} tokens</span>{/if}
        {#if latest.cache_hit_rate !== undefined}<span>{percent.format(latest.cache_hit_rate)} cache hit</span>{/if}
      </small>
      <span class="expand"><ChevronDown size={14} aria-hidden="true" /></span>
    </summary>
    <ol reversed aria-label="Recent streaming model calls in this turn">
      {#each [...calls].reverse() as call (call.model_call_id)}
        <li>
          <div class="call-label">
            <strong>{call.model}</strong>
            <span>{call.usage_role} · {call.status}</span>
            <code title="Model call ID">{call.model_call_id}</code>
          </div>
          <dl>
            <div title="Request start to first model output received by Q"><dt>TTFT</dt><dd>{duration(call.ttft_ms)}</dd></div>
            <div title="Request start to stream end or stop"><dt>{call.status === 'completed' ? 'Total' : 'Elapsed'}</dt><dd>{duration(call.duration_ms)}</dd></div>
            <div title="First model output to stream completion"><dt>After first</dt><dd>{duration(call.after_first_ms)}</dd></div>
            <div title="Longest interval between model output chunks received by Q"><dt>Max gap</dt><dd>{duration(call.max_output_gap_ms)}</dd></div>
            <div title="Provider-reported output tokens divided by the time from first output to completion"><dt>Output rate</dt><dd>{rate(call.output_tokens_per_second)}</dd></div>
          </dl>
          <dl aria-label="Token usage">
            <div><dt>Input tokens</dt><dd>{tokens(call.input_tokens)}</dd></div>
            <div><dt>Output tokens</dt><dd>{tokens(call.output_tokens)}</dd></div>
            <div><dt>Total tokens</dt><dd>{tokens(call.total_tokens)}</dd></div>
            <div title="Cached tokens are included in input tokens"><dt>Cached tokens</dt><dd>{tokens(call.cached_tokens)}</dd></div>
            <div title="Cached tokens divided by input tokens"><dt>Cache hit</dt><dd>{call.cache_hit_rate === undefined ? '—' : percent.format(call.cache_hit_rate)}</dd></div>
          </dl>
        </li>
      {/each}
    </ol>
  </details>
{/if}

<style>
  .model-timing { flex: none; padding: 0 22px; border-bottom: 1px solid var(--border); color: var(--muted); font-size: var(--type-caption); }
  summary { display: flex; align-items: center; flex-wrap: wrap; gap: .4rem .6rem; padding: .5rem 0; cursor: pointer; color: var(--text); }
  summary::-webkit-details-marker { display: none; }
  .latest { display: flex; flex-wrap: wrap; gap: .25rem .75rem; color: var(--muted); font-size: inherit; font-variant-numeric: tabular-nums; }
  .expand { display: flex; margin-inline-start: auto; }
  details[open] .expand { transform: rotate(180deg); }
  ol { max-height: min(16rem, 30vh); overflow: auto; margin: 0 0 .5rem; padding: 0; list-style: none; }
  li { padding: .5rem 0; border-top: 1px solid var(--border); }
  .call-label, dl { display: flex; flex-wrap: wrap; gap: .25rem .8rem; }
  .call-label { overflow-wrap: anywhere; }
  .call-label strong, dd { color: var(--text); }
  code { font-family: var(--font-code); }
  dl { margin: .25rem 0 0; font-variant-numeric: tabular-nums; }
  dl div { display: flex; gap: .25rem; }
  dd { margin: 0; }
</style>
