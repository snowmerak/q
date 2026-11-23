<script lang="ts">
  import { Activity, Database, Server, TerminalSquare } from '@lucide/svelte';
  import { formatAverage, formatBytes, formatCacheHitRate, formatCacheTokens, formatComparison, formatDuration, formatNumber, formatRate, serviceLabel } from './format';
  import type { DimensionTotal, Snapshot, Totals } from './types';
  export let snapshot: Snapshot;
  export let days: number;
  $: totals = snapshot.usage.totals;
  $: previous = snapshot.previous_usage;
  $: periodLabel = days === 1 ? '24 hours' : `${days} days`;
  $: resolutionLabel = snapshot.usage.resolution === 'hour' ? 'Hourly' : snapshot.usage.resolution === 'day' ? 'Daily' : 'No samples';
  $: maximum = Math.max(1, ...(snapshot.usage.series || []).map((point) => point.total_tokens));

  function bucketTitle(point: { bucket: string } & Totals) {
    const lines = [
      point.bucket,
      `Total ${formatNumber(point.total_tokens)} tokens`,
      `Input ${formatNumber(point.prompt_tokens)} · Output ${formatNumber(point.completion_tokens)}`,
      `Cache read ${formatCacheTokens(point, 'cached_tokens')} · Cache write ${formatCacheTokens(point, 'cache_write_tokens')}`,
      `Cache hit rate ${formatCacheHitRate(point)}`,
    ];
    if (point.cache_estimated_calls) lines.push(`Cache data missing for ${formatNumber(point.cache_estimated_calls)} calls`);
    return lines.join('\n');
  }
</script>

{#snippet breakdown(dimension: string, items: DimensionTotal[] | undefined)}
  {#if items?.length}
    <!-- svelte-ignore a11y_no_noninteractive_tabindex (The scrollable table needs focus for keyboard scrolling.) -->
    <div class="usage-table-scroll" role="region" aria-label={`${dimension} usage table`} tabindex="0">
      <table aria-label={`${dimension} usage`}>
        <thead>
          <tr>
            <th scope="col">{dimension}</th>
            <th scope="col">Calls</th>
            <th scope="col">Input</th>
            <th scope="col">Output</th>
            <th scope="col">Cache read</th>
            <th scope="col">Cache write</th>
            <th scope="col">Hit rate</th>
            <th scope="col">Total</th>
          </tr>
        </thead>
        <tbody>
          {#each items as item (item.id || item.name)}
            <tr>
              <th scope="row" title={item.id || item.name}>{item.name}</th>
              <td>{formatNumber(item.calls)}</td>
              <td>{formatNumber(item.prompt_tokens)}</td>
              <td>{formatNumber(item.completion_tokens)}</td>
              <td>{formatCacheTokens(item, 'cached_tokens')}</td>
              <td>{formatCacheTokens(item, 'cache_write_tokens')}</td>
              <td>
                {formatCacheHitRate(item)}
                {#if item.cache_estimated_calls}<small class="cache-missing">{formatNumber(item.cache_estimated_calls)} missing</small>{/if}
              </td>
              <td>{formatNumber(item.total_tokens)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <small class="usage-note">No {dimension.toLowerCase()} totals</small>
  {/if}
{/snippet}

<div class="operations-dashboard">
    <div class="metric-grid">
      <article><Activity class="metric-icon" aria-hidden="true" size={17} /><span>Active runs</span><strong>{snapshot.workers.active_runs}</strong><small>{snapshot.workers.resident_runs} resident run records</small></article>
      <article><TerminalSquare class="metric-icon" aria-hidden="true" size={17} /><span>Model calls</span><strong>{formatNumber(totals.calls)}</strong><small>{formatNumber(totals.estimated_calls)} estimated</small></article>
      <article><Database class="metric-icon" aria-hidden="true" size={17} /><span>Total tokens</span><strong>{formatNumber(totals.total_tokens)}</strong></article>
      <article><Server class="metric-icon" aria-hidden="true" size={17} /><span>Service health</span><strong>{snapshot.services.filter((service) => service.state === 'ready').length}/{snapshot.services.length}</strong><small>{snapshot.runtime_available ? 'Runtime attached' : 'Runtime unavailable'}</small></article>
    </div>

    {#if snapshot.usage_error}<p class="inline-warning">Usage could not be read: {snapshot.usage_error}</p>{/if}

    <div class="operations-grid">
      <article class="operations-card usage-card">
        <div class="operations-heading"><div><h2>Token usage</h2><p>Last {periodLabel} · {resolutionLabel}</p></div></div>
        <div class="usage-summary usage-panels">
          <section class="usage-panel">
            <h3>Token volume</h3>
            <dl class="usage-totals" aria-label="Token volume">
              <div><dt>Input tokens</dt><dd>{formatNumber(totals.prompt_tokens)}</dd></div>
              <div><dt>Output tokens</dt><dd>{formatNumber(totals.completion_tokens)}</dd></div>
              <div class="secondary-value"><dt>Avg. input per call</dt><dd>{formatAverage(totals.prompt_tokens, totals.calls)}</dd></div>
              <div class="secondary-value"><dt>Avg. output per call</dt><dd>{formatAverage(totals.completion_tokens, totals.calls)}</dd></div>
            </dl>
          </section>
          <section class="usage-panel">
            <h3>Prompt cache</h3>
            <dl class="usage-totals" aria-label="Prompt cache">
              <div><dt>Read tokens</dt><dd>{formatCacheTokens(totals, 'cached_tokens')}</dd></div>
              <div><dt>Written tokens</dt><dd>{formatCacheTokens(totals, 'cache_write_tokens')}</dd></div>
              <div class="secondary-value">
                <dt>Input token hit rate</dt>
                <dd>
                  {formatCacheHitRate(totals)}
                  <small class="usage-note">Cache reads / total input</small>
                </dd>
              </div>
              <div class="secondary-value">
                <dt>Calls with a cache hit</dt>
                <dd>
                  {formatRate(totals.cache_hit_calls ?? 0, totals.cache_hit_samples ?? 0)}
                  {#if totals.cache_hit_samples}<small class="usage-note">{formatNumber(totals.cache_hit_calls ?? 0)} of {formatNumber(totals.cache_hit_samples)} measured</small>{/if}
                </dd>
              </div>
            </dl>
            {#if totals.cache_estimated_calls}
              <p class="usage-note cache-coverage">Cache data missing for {formatNumber(totals.cache_estimated_calls)} of {formatNumber(totals.calls)} calls.</p>
            {/if}
          </section>
        </div>
        {#if previous}
          <section class="usage-summary">
            <h3>Previous {periodLabel}</h3>
            <dl class="usage-totals" aria-label="Previous period totals and changes">
              <div><dt>Calls</dt><dd>{formatNumber(previous.calls)} <small class="usage-note">({formatComparison(totals.calls, previous.calls)})</small></dd></div>
              <div><dt>Total tokens</dt><dd>{formatNumber(previous.total_tokens)} <small class="usage-note">({formatComparison(totals.total_tokens, previous.total_tokens)})</small></dd></div>
              <div><dt>Input tokens</dt><dd>{formatNumber(previous.prompt_tokens)} <small class="usage-note">({formatComparison(totals.prompt_tokens, previous.prompt_tokens)})</small></dd></div>
              <div><dt>Output tokens</dt><dd>{formatNumber(previous.completion_tokens)} <small class="usage-note">({formatComparison(totals.completion_tokens, previous.completion_tokens)})</small></dd></div>
            </dl>
            <p class="usage-note">Parentheses show how the current period changed compared to the previous period.</p>
          </section>
        {:else if snapshot.comparison_error}
          <div class="usage-summary"><small class="usage-note">Previous period unavailable.</small></div>
        {/if}
        <div class="usage-chart" aria-label="Token usage over time">
          {#each snapshot.usage.series || [] as point}
            <div class="usage-column" title={bucketTitle(point)}><span style={`height:${Math.max(2, point.total_tokens / maximum * 100)}%`}></span><small>{point.bucket.slice(5)}</small></div>
          {:else}<div class="operations-empty">No model usage was recorded in this period.</div>{/each}
        </div>
        <div class="usage-breakdown">
          <section>
            <h3>Models</h3>
            {@render breakdown('Model', snapshot.usage.models)}
          </section>
          <section>
            <div class="usage-section-heading">
              <h3>Roles</h3>
              <details class="usage-note">
                <summary>What are roles?</summary>
                <p>Roles label where Q made a model call. <code>main</code> is the main chat loop, <code>embedding</code> generates vectors, and <code>gateway</code> is a direct Gateway call without a role label. Subagent calls use the configured role, such as <code>coder</code> or <code>reviewer</code>. <code>unknown</code> means the role is missing or invalid.</p>
              </details>
            </div>
            {@render breakdown('Role', snapshot.usage.roles)}
          </section>
          <section>
            <h3>Projects</h3>
            {@render breakdown('Project', snapshot.usage.projects)}
          </section>
          <section>
            <h3>Subagents</h3>
            {@render breakdown('Subagent', snapshot.usage.subagents)}
            <p class="usage-note attribution-note">Project and subagent totals cover calls attributed by Q after the update, within hot retention.</p>
          </section>
        </div>
      </article>

      <article class="operations-card performance-card">
        <div class="operations-heading">
          <div><h2>Model performance</h2><p>Measured API calls from Q. Timing percentiles use completed calls. Failure and retry rates exclude stopped calls.</p></div>
        </div>
        <div class="performance-content">
          {#if snapshot.usage.performance?.length}
            <!-- svelte-ignore a11y_no_noninteractive_tabindex (The scrollable table needs focus for keyboard scrolling.) -->
            <div class="usage-table-scroll" role="region" aria-label="Model performance table" tabindex="0">
              <table aria-label="Model performance">
                <thead><tr>
                  <th scope="col">Model</th>
                  <th scope="col">Measured</th>
                  <th scope="col">Failures</th>
                  <th scope="col" title="Calls that retried the provider transport">Retried</th>
                  <th scope="col">First output p50 / p95</th>
                  <th scope="col">Duration p50 / p95</th>
                  <th scope="col">Output tokens/s p50</th>
                </tr></thead>
                <tbody>
                  {#each snapshot.usage.performance as row (row.name)}
                    {@const measured = row.completed + row.failed}
                    <tr>
                      <th scope="row">{row.name}</th>
                      <td>
                        {formatNumber(measured)}
                        {#if row.stopped}<small class="cache-missing">{formatNumber(row.stopped)} stopped</small>{/if}
                      </td>
                      <td>
                        {formatRate(row.failed, measured)}
                        {#if measured}<small class="cache-missing">{formatNumber(row.failed)} failed</small>{/if}
                      </td>
                      <td>{formatRate(row.retried, measured)}</td>
                      <td>{formatDuration(row.ttft_p50_ms)} / {formatDuration(row.ttft_p95_ms)}</td>
                      <td>{formatDuration(row.duration_p50_ms)} / {formatDuration(row.duration_p95_ms)}</td>
                      <td>{row.output_rate_p50 === undefined ? '—' : formatAverage(row.output_rate_p50, 1)}</td>
                    </tr>
                  {/each}
                </tbody>
              </table>
            </div>
          {:else}
            <p class="operations-empty">No model performance was measured in this period.</p>
          {/if}
        </div>
      </article>

      <article class="operations-card">
        <div class="operations-heading"><div><h2>Services</h2><p>Process owned and user level dependencies</p></div></div>
        <div class="service-status-list">
          {#each snapshot.services as service}
            <div class="service-status"><span class:ready={service.state === 'ready'} class="service-light"></span><div><strong>{serviceLabel(service.id)}</strong><small>{service.endpoint || service.detail || service.state}{service.leader ? ' · local leader' : ''}</small></div><b class:ready={service.state === 'ready'}>{service.state}</b></div>
          {:else}<div class="operations-empty">No runtime is attached to this Studio server.</div>{/each}
        </div>
      </article>

      <article class="operations-card">
        <div class="operations-heading"><div><h2>Retention</h2><p>Hot usage stays queryable for {snapshot.retention.hot_days} days.</p></div></div>
        <dl class="retention-list"><div><dt>Hot database</dt><dd>{formatBytes(snapshot.retention.database_bytes)}</dd><code>{snapshot.retention.database_path}</code></div><div><dt>Daily archives</dt><dd>{snapshot.retention.archive_files} · {formatBytes(snapshot.retention.archive_bytes)}</dd><code>{snapshot.retention.archive_path}</code></div></dl>
      </article>

      <article class="operations-card logs-card">
        <div class="operations-heading"><div><h2>Runtime logs</h2><p>Bounded service diagnostics for this Studio process.</p></div></div>
        <pre>{snapshot.logs.length ? snapshot.logs.join('\n') : 'No service diagnostics have been emitted.'}</pre>
      </article>
    </div>
</div>

<style>
  .operations-dashboard { display: grid; gap: 18px; }

  .metric-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 14px; }
  .metric-grid article { display: grid; grid-template-columns: auto 1fr; gap: 4px 9px; min-width: 0; padding: 20px; border: 1px solid var(--border); border-radius: 10px; background: var(--panel); }
  .metric-grid :global(.metric-icon) { grid-row: 1 / 4; color: var(--accent-text); }
  .metric-grid span, .metric-grid small { color: var(--subtle); font-size: var(--type-caption); }
  .metric-grid strong { color: var(--text); font-size: calc(28px * var(--text-scale)); font-weight: 600; font-variant-numeric: tabular-nums; }
  .operations-grid { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(300px, .8fr); gap: 18px; align-items: start; }
  .operations-card { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 10px; background: var(--panel); }
  .operations-heading { display: flex; padding: 15px 17px; border-bottom: 1px solid var(--border); }
  .operations-heading h2 { margin: 0; color: var(--text); font-size: var(--type-body); font-weight: 600; }
  .operations-heading p { margin: 6px 0 0; color: var(--muted); font-size: var(--type-small); line-height: 1.5; }
  .usage-card, .performance-card { grid-column: 1 / -1; }
  .usage-summary { padding: 15px 17px; border-bottom: 1px solid var(--border); }
  .usage-panels { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 23em), 1fr)); gap: 12px; font-size: var(--type-body); }
  .usage-panel { min-width: 0; padding: 14px 16px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel-subtle); container-type: inline-size; }
  .usage-totals { display: grid; grid-template-columns: repeat(auto-fit, minmax(min(100%, 10em), 1fr)); gap: 14px 16px; margin: 0; font-size: var(--type-body); }
  .usage-panel .usage-totals { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .usage-totals > div { display: flex; flex-direction: column; align-items: stretch; gap: 3px; min-width: 0; }
  .usage-totals dt { color: var(--muted); font-size: var(--type-caption); }
  .usage-totals dd { margin: 0; color: var(--text); font: 600 calc(18px * var(--text-scale))/1.25 var(--font-ui); font-variant-numeric: tabular-nums; white-space: nowrap; overflow-wrap: normal; }
  .usage-panel dd { font-size: calc(22px * var(--text-scale)); }
  .usage-panel .secondary-value dd { font-size: calc(18px * var(--text-scale)); }
  .usage-totals dd small { display: block; margin-top: 2px; font-weight: 400; white-space: normal; }
  .usage-note { color: var(--muted); font-size: var(--type-caption); line-height: 1.5; }
  .usage-summary p { margin: 12px 0 0; }
  .usage-panel .cache-coverage { padding-top: 9px; border-top: 1px solid var(--border); }
  @container (max-width: 18em) { .usage-panel .usage-totals { grid-template-columns: minmax(0, 1fr); } }
  .usage-chart { display: flex; height: 190px; align-items: flex-end; gap: 4px; padding: 20px 17px 10px; border-bottom: 1px solid var(--border); }
  .usage-column { display: grid; grid-template-rows: minmax(0, 1fr) 24px; align-items: end; flex: 1; height: 100%; min-width: 0; }
  .usage-column > span { display: block; min-height: 2px; border-radius: 2px 2px 0 0; background: var(--accent); }
  .usage-column small { overflow: hidden; color: var(--subtle); font: var(--type-caption)/1.5 var(--font-code); text-align: center; }
  .usage-breakdown { display: grid; gap: 20px; padding: 16px 17px; }
  .usage-breakdown section { min-width: 0; }
  .usage-breakdown h3, .usage-summary h3 { margin: 0 0 9px; color: var(--text); font-size: var(--type-small); font-weight: 600; }
  .attribution-note { margin: 9px 0 0; }
  .usage-section-heading details { margin: 0 0 9px; }
  .usage-section-heading summary { width: fit-content; cursor: pointer; }
  .usage-section-heading p { margin: 6px 0 0; }
  .usage-table-scroll { overflow-x: auto; }
  .usage-table-scroll:focus-visible { outline: 1px solid var(--accent-text); outline-offset: -1px; }
  .usage-table-scroll table { width: 100%; border-collapse: collapse; color: var(--text); font-size: var(--type-caption); font-variant-numeric: tabular-nums; }
  .usage-table-scroll th, .usage-table-scroll td { padding: 8px 10px; border-bottom: 1px solid var(--border); text-align: right; vertical-align: top; white-space: nowrap; }
  .usage-table-scroll th:first-child { min-width: 10em; max-width: 20em; text-align: left; overflow-wrap: anywhere; white-space: normal; }
  .usage-table-scroll thead th { background: var(--code-bg); color: var(--muted); font-weight: 500; }
  .usage-table-scroll tbody th { font-weight: 500; }
  .cache-missing { display: block; color: var(--muted); font-size: inherit; }
  .performance-content { padding: 15px 17px; }
  .service-status-list { display: grid; }
  .service-status { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 10px; padding: 12px 16px; border-top: 1px solid var(--border); }
  .service-status:first-child { border-top: 0; }
  .service-light { width: 7px; height: 7px; border-radius: 50%; background: var(--error); box-shadow: 0 0 0 3px var(--error-soft); }
  .service-light.ready { background: var(--success-text); box-shadow: 0 0 0 3px var(--success-soft); }
  .service-status div { display: grid; gap: 4px; min-width: 0; }
  .service-status strong { color: var(--text); font-size: var(--type-small); }
  .service-status small { overflow: hidden; color: var(--subtle); font: var(--type-caption)/1.6 var(--font-code); overflow-wrap: anywhere; }
  .service-status b { color: var(--error); font-size: var(--type-caption); font-weight: 500; text-transform: uppercase; }
  .service-status b.ready { color: var(--success-text); }
  .retention-list { margin: 0; }
  .retention-list > div { display: grid; grid-template-columns: 1fr auto; gap: 5px 12px; padding: 13px 16px; border-top: 1px solid var(--border); }
  .retention-list > div:first-child { border-top: 0; }
  .retention-list dt { color: var(--muted); font-size: var(--type-caption); }
  .retention-list dd { margin: 0; color: var(--text); font-size: var(--type-caption); }
  .retention-list code { grid-column: 1 / -1; color: var(--muted); font: var(--type-caption)/1.65 var(--font-code); overflow-wrap: anywhere; }
  .logs-card { grid-column: 1 / -1; }
  .logs-card pre { max-height: 230px; margin: 0; overflow: auto; padding: 14px 17px; color: var(--muted); background: var(--code-bg); font: var(--type-caption)/1.65 var(--font-code); white-space: pre-wrap; }
  .operations-empty { width: 100%; align-self: center; color: var(--subtle); font-size: var(--type-caption); text-align: center; }
  @media (max-width: 1050px) { .metric-grid { grid-template-columns: 1fr 1fr; } .operations-grid { grid-template-columns: 1fr; } }
  @media (max-width: 620px) { .metric-grid { grid-template-columns: 1fr; } .usage-chart { height: 150px; gap: 1px; } }

</style>
