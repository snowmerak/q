<script lang="ts">
  import { Activity, Database, Server, TerminalSquare } from '@lucide/svelte';
  import { formatNumber, formatBytes, serviceLabel } from './format';
  import type { Snapshot } from './types';
  export let snapshot: Snapshot;
  export let days: number;
  $: maximum = Math.max(1, ...(snapshot.usage.series || []).map((point) => point.total_tokens));
</script>

<div class="operations-dashboard">
    <div class="metric-grid">
      <article><Activity class="metric-icon" aria-hidden="true" size={17} /><span>Active runs</span><strong>{snapshot.workers.active_runs}</strong><small>{snapshot.workers.resident_runs} resident run records</small></article>
      <article><TerminalSquare class="metric-icon" aria-hidden="true" size={17} /><span>Model calls</span><strong>{formatNumber(snapshot.usage.totals.calls)}</strong><small>{formatNumber(snapshot.usage.totals.estimated_calls)} estimated</small></article>
      <article><Database class="metric-icon" aria-hidden="true" size={17} /><span>Total tokens</span><strong>{formatNumber(snapshot.usage.totals.total_tokens)}</strong><small>{formatNumber(snapshot.usage.totals.cached_tokens)} cached</small></article>
      <article><Server class="metric-icon" aria-hidden="true" size={17} /><span>Service health</span><strong>{snapshot.services.filter((service) => service.state === 'ready').length}/{snapshot.services.length}</strong><small>{snapshot.runtime_available ? 'Runtime attached' : 'Runtime unavailable'}</small></article>
    </div>

    {#if snapshot.usage_error}<p class="inline-warning">Usage could not be read: {snapshot.usage_error}</p>{/if}

    <div class="operations-grid">
      <article class="operations-card usage-card">
        <div class="operations-heading"><div><h2>Token usage</h2><p>{snapshot.usage.resolution || 'No samples'} · {days} day window</p></div></div>
        <div class="usage-chart" aria-label="Token usage over time">
          {#each snapshot.usage.series || [] as point}
            <div class="usage-column" title={`${point.bucket}: ${formatNumber(point.total_tokens)} tokens`}><span style={`height:${Math.max(2, point.total_tokens / maximum * 100)}%`}></span><small>{point.bucket.slice(5)}</small></div>
          {:else}<div class="operations-empty">No model usage was recorded in this period.</div>{/each}
        </div>
        <div class="usage-breakdown">
          <div><h3>Models</h3>{#each (snapshot.usage.models || []).slice(0, 6) as item}<p><span>{item.name}</span><strong>{formatNumber(item.total_tokens)}</strong></p>{:else}<small>No model totals</small>{/each}</div>
          <div><h3>Roles</h3>{#each (snapshot.usage.roles || []).slice(0, 6) as item}<p><span>{item.name}</span><strong>{formatNumber(item.total_tokens)}</strong></p>{:else}<small>No role totals</small>{/each}</div>
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
  .usage-card { grid-row: span 2; }
  .usage-chart { display: flex; height: 190px; align-items: flex-end; gap: 4px; padding: 20px 17px 10px; border-bottom: 1px solid var(--border); }
  .usage-column { display: grid; grid-template-rows: minmax(0, 1fr) 24px; align-items: end; flex: 1; height: 100%; min-width: 3px; }
  .usage-column > span { display: block; min-height: 2px; border-radius: 2px 2px 0 0; background: var(--accent); }
  .usage-column small { overflow: hidden; color: var(--subtle); font: var(--type-caption)/1.5 var(--font-code); text-align: center; }
  .usage-breakdown { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; padding: 16px 17px; }
  .usage-breakdown h3 { margin: 0 0 9px; color: var(--muted); font-size: var(--type-caption); text-transform: uppercase; }
  .usage-breakdown p { display: flex; justify-content: space-between; gap: 10px; margin: 6px 0; font-size: var(--type-caption); }
  .usage-breakdown p span { overflow: hidden; color: var(--muted); text-overflow: ellipsis; white-space: nowrap; }
  .usage-breakdown p strong { color: var(--text); font-weight: 500; }
  .usage-breakdown small { color: var(--subtle); }
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
  @media (max-width: 1050px) { .metric-grid { grid-template-columns: 1fr 1fr; } .operations-grid { grid-template-columns: 1fr; } .usage-card { grid-row: auto; } .logs-card { grid-column: auto; } }
  @media (max-width: 620px) { .metric-grid { grid-template-columns: 1fr; } .usage-breakdown { grid-template-columns: 1fr; } .usage-chart { height: 150px; } }

</style>
