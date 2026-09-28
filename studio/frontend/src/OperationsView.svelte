<script lang="ts">
  import { Activity, Database, RefreshCw, Server, TerminalSquare } from '@lucide/svelte';
  import { onMount } from 'svelte';

  type Totals = {
    calls: number; prompt_tokens: number; completion_tokens: number; total_tokens: number;
    cached_tokens: number; cache_write_tokens: number; estimated_calls: number; cache_estimated_calls: number;
  };
  type Usage = {
    from?: string; to?: string; resolution?: string; totals: Totals;
    series?: Array<{ bucket: string } & Totals>;
    models?: Array<{ name: string } & Totals>;
    roles?: Array<{ name: string } & Totals>;
  };
  type Snapshot = {
    generated_at: string;
    runtime_available: boolean;
    workers: { active_runs: number; resident_runs: number };
    services: Array<{ id: string; state: string; endpoint?: string; detail?: string; leader?: boolean }>;
    usage: Usage;
    usage_error?: string;
    retention: { hot_days: number; database_path: string; database_bytes: number; archive_path: string; archive_files: number; archive_bytes: number };
    logs: string[];
  };

  let days = 30;
  let snapshot: Snapshot | null = null;
  let loading = true;
  let error = '';
  let timer: ReturnType<typeof setInterval> | undefined;

  async function load() {
    loading = true;
    error = '';
    try {
      const response = await fetch(`/api/v1/operations?days=${days}`, { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await responseError(response));
      snapshot = await response.json() as Snapshot;
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Operations are unavailable';
    } finally {
      loading = false;
    }
  }

  async function responseError(response: Response) {
    try {
      const value = await response.json() as { error?: string | { message?: string } };
      return typeof value.error === 'string' ? value.error : value.error?.message || `Request failed (${response.status})`;
    } catch {
      return `Request failed (${response.status})`;
    }
  }

  function formatNumber(value = 0) { return new Intl.NumberFormat().format(value); }
  function formatBytes(value = 0) {
    if (value < 1024) return `${value} B`;
    if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
    return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
  }
  function serviceLabel(id: string) { return id.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' '); }
  function maximumSeries() { return Math.max(1, ...(snapshot?.usage.series || []).map((point) => point.total_tokens)); }

  onMount(() => {
    void load();
    timer = setInterval(() => void load(), 15000);
    return () => timer && clearInterval(timer);
  });
</script>

<section class="operations-view">
  <div class="operations-toolbar">
    <label><span>Usage period</span><select bind:value={days} onchange={load}><option value={1}>24 hours</option><option value={7}>7 days</option><option value={30}>30 days</option><option value={90}>90 days</option></select></label>
    <button class="icon-button" title="Refresh operations" onclick={load} disabled={loading}><RefreshCw aria-hidden="true" size={16} class={loading ? 'spin' : undefined} /></button>
  </div>

  {#if error}
    <div class="error-panel"><h2>Operations unavailable</h2><p>{error}</p><button class="retry" onclick={load}>Retry</button></div>
  {:else if !snapshot}
    <div class="loading-panel">Loading runtime operations…</div>
  {:else}
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
            <div class="usage-column" title={`${point.bucket}: ${formatNumber(point.total_tokens)} tokens`}><span style={`height:${Math.max(2, point.total_tokens / maximumSeries() * 100)}%`}></span><small>{point.bucket.slice(5)}</small></div>
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
  {/if}
</section>

<style>
  .operations-view { display: grid; gap: 18px; min-height: 0; padding: 0 0 26px; }
  .operations-toolbar { display: flex; justify-content: flex-end; gap: 9px; }
  .operations-toolbar label { display: flex; align-items: center; gap: 9px; color: #8791a8; font-size: 10px; }
  .operations-toolbar select { min-width: 130px; }
  .metric-grid { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 10px; }
  .metric-grid article { display: grid; grid-template-columns: auto 1fr; gap: 4px 9px; min-width: 0; padding: 15px; border: 1px solid var(--border); border-radius: 7px; background: var(--panel); }
  .metric-grid :global(.metric-icon) { grid-row: 1 / 4; color: #9575ff; }
  .metric-grid span, .metric-grid small { color: #778198; font-size: 10px; }
  .metric-grid strong { color: #f1f2f8; font-size: 21px; }
  .operations-grid { display: grid; grid-template-columns: minmax(0, 1.45fr) minmax(300px, .8fr); gap: 12px; align-items: start; }
  .operations-card { min-width: 0; overflow: hidden; border: 1px solid var(--border); border-radius: 7px; background: var(--panel); }
  .operations-heading { display: flex; padding: 15px 17px; border-bottom: 1px solid var(--border); }
  .operations-heading h2 { margin: 0; color: #edf0f8; font-size: 13px; }
  .operations-heading p { margin: 5px 0 0; color: #778198; font-size: 10px; }
  .usage-card { grid-row: span 2; }
  .usage-chart { display: flex; height: 190px; align-items: flex-end; gap: 4px; padding: 20px 17px 10px; border-bottom: 1px solid var(--border); }
  .usage-column { display: grid; grid-template-rows: minmax(0, 1fr) 18px; align-items: end; flex: 1; height: 100%; min-width: 3px; }
  .usage-column > span { display: block; min-height: 2px; border-radius: 2px 2px 0 0; background: linear-gradient(#9a74ff, #5f46ba); }
  .usage-column small { overflow: hidden; color: #626d84; font: 8px ui-monospace, SFMono-Regular, Consolas, monospace; text-align: center; }
  .usage-breakdown { display: grid; grid-template-columns: 1fr 1fr; gap: 20px; padding: 16px 17px; }
  .usage-breakdown h3 { margin: 0 0 9px; color: #aeb5c7; font-size: 10px; text-transform: uppercase; }
  .usage-breakdown p { display: flex; justify-content: space-between; gap: 10px; margin: 6px 0; font-size: 10px; }
  .usage-breakdown p span { overflow: hidden; color: #9da5b8; text-overflow: ellipsis; white-space: nowrap; }
  .usage-breakdown p strong { color: #e1e4ed; font-weight: 500; }
  .usage-breakdown small { color: #667188; }
  .service-status-list { display: grid; }
  .service-status { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 10px; padding: 12px 16px; border-top: 1px solid var(--border); }
  .service-status:first-child { border-top: 0; }
  .service-light { width: 7px; height: 7px; border-radius: 50%; background: #e17581; box-shadow: 0 0 0 3px rgba(225,117,129,.08); }
  .service-light.ready { background: #49d6a1; box-shadow: 0 0 0 3px rgba(73,214,161,.08); }
  .service-status div { display: grid; gap: 4px; min-width: 0; }
  .service-status strong { color: #e5e8f1; font-size: 11px; }
  .service-status small { overflow: hidden; color: #68738a; font: 8.5px ui-monospace, SFMono-Regular, Consolas, monospace; text-overflow: ellipsis; white-space: nowrap; }
  .service-status b { color: #d9828c; font-size: 9px; font-weight: 500; text-transform: uppercase; }
  .service-status b.ready { color: #54d3a4; }
  .retention-list { margin: 0; }
  .retention-list > div { display: grid; grid-template-columns: 1fr auto; gap: 5px 12px; padding: 13px 16px; border-top: 1px solid var(--border); }
  .retention-list > div:first-child { border-top: 0; }
  .retention-list dt { color: #aeb5c7; font-size: 10px; }
  .retention-list dd { margin: 0; color: #e3e6ef; font-size: 10px; }
  .retention-list code { grid-column: 1 / -1; overflow: hidden; color: #646f86; font-size: 8.5px; text-overflow: ellipsis; white-space: nowrap; }
  .logs-card { grid-column: 1 / -1; }
  .logs-card pre { max-height: 230px; margin: 0; overflow: auto; padding: 14px 17px; color: #9fa8ba; background: #080b12; font: 10px/1.65 ui-monospace, SFMono-Regular, Consolas, monospace; white-space: pre-wrap; }
  .operations-empty { width: 100%; align-self: center; color: #68738a; font-size: 10px; text-align: center; }
  @media (max-width: 1050px) { .metric-grid { grid-template-columns: 1fr 1fr; } .operations-grid { grid-template-columns: 1fr; } .usage-card { grid-row: auto; } .logs-card { grid-column: auto; } }
  @media (max-width: 620px) { .metric-grid { grid-template-columns: 1fr; } .usage-breakdown { grid-template-columns: 1fr; } .usage-chart { height: 150px; } }
</style>
