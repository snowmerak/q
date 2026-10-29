<script lang="ts">
  import { RefreshCw, Search, RotateCw } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestJSON } from '../api';
  import { embeddingStateLabels, readEmbeddingStatus, type EmbeddingSnapshot, type EmbeddingArchive, type EmbeddingSyncJob } from './embeddings';

  let snapshot: EmbeddingSnapshot | null = null;
  let loading = false;
  let error = '';
  let partial = false;
  let received = 0;
  let workspaceRoot = localStorage.getItem('q-studio-workspace-root') || '';
  let inspectedRoot = workspaceRoot;
  let generation = 0;
  let controller: AbortController | undefined;
  let syncController: AbortController | undefined;
  let syncJob: EmbeddingSyncJob | null = null;
  let syncStarting = false;
  let syncError = '';
  let disposed = false;
  $: syncActive = syncJob?.state === 'queued' || syncJob?.state === 'running';
  $: totals = snapshot?.archives.reduce((sum, archive) => ({
    eligible: sum.eligible + archive.eligible, current: sum.current + archive.current,
    missing: sum.missing + archive.missing, stale: sum.stale + archive.stale
  }), { eligible: 0, current: 0, missing: 0, stale: 0 });

  async function refresh() {
    if (loading || syncStarting) return;
    const current = ++generation;
    controller = new AbortController();
    loading = true;
    received = 0;
    error = '';
    try {
      const query = inspectedRoot ? `?workspace_root=${encodeURIComponent(inspectedRoot)}` : '';
      let scan: EmbeddingSnapshot | null = null;
      await readEmbeddingStatus(`/api/v1/embeddings/status${query}`, controller.signal, event => {
        if (generation !== current) return;
        if (event.type === 'started') {
          scan = event.snapshot;
          // Keep the previous rows until the first fresh archive is available.
          if (!snapshot) { snapshot = scan; partial = true; }
          syncJob = scan.sync_job || null;
        } else if (event.type === 'archive' && scan) {
          scan = { ...scan, archives: [...scan.archives, event.archive] };
          received++;
          snapshot = scan;
          partial = true;
        } else if (event.type === 'completed') {
          snapshot = { ...event.snapshot, archives: scan?.archives || event.snapshot.archives };
          syncJob = snapshot.sync_job || null;
          partial = false;
        }
      });
    } catch (reason) {
      if (generation === current && !controller.signal.aborted) {
        error = reason instanceof Error ? reason.message : 'Embedding status is unavailable';
        // Archive inspection can fail independently of an accepted background job.
        // Keep its terminal result available even if a selected path disappears.
        if (syncJob && syncActive) {
          try {
            const job = await requestJSON<EmbeddingSyncJob>('GET', `/api/v1/embeddings/sync/${encodeURIComponent(syncJob.id)}`, undefined, controller.signal);
            if (generation === current) syncJob = job;
          } catch {
            if (generation === current && !controller.signal.aborted) error += ' Sync progress is also unavailable; retry Refresh.';
          }
        }
      }
    } finally {
      if (generation === current) loading = false;
    }
  }

  function inspectWorkspace() {
    inspectedRoot = workspaceRoot.trim();
    void refresh();
  }

  async function startSync(archive?: EmbeddingArchive) {
    if (syncStarting || syncActive || loading) return;
    syncStarting = true;
    syncError = '';
    syncController = new AbortController();
    try {
      const job = await requestJSON<EmbeddingSyncJob>('POST', '/api/v1/embeddings/sync', {
        scope: archive?.scope || 'all',
        workspace_root: archive?.workspace_root || (archive ? undefined : inspectedRoot || undefined)
      }, syncController.signal);
      if (!disposed) syncJob = job;
    } catch (reason) {
      if (!disposed && !syncController.signal.aborted) syncError = reason instanceof Error ? reason.message : 'Could not start sync';
    } finally {
      if (!disposed) { syncStarting = false; void refresh(); }
    }
  }

  onMount(() => {
    void refresh();
    let lastRefresh = Date.now();
    const timer = setInterval(() => {
      if (document.visibilityState === 'visible' && Date.now() - lastRefresh >= (syncActive ? 3000 : 15000)) {
        lastRefresh = Date.now(); void refresh();
      }
    }, 1000);
    return () => { clearInterval(timer); disposed = true; generation++; controller?.abort(); syncController?.abort(); };
  });
</script>

<section class="settings-section embedding-status" aria-label="Embedding status">
  <div class="section-heading">
    <div><p class="eyebrow">EMBEDDINGS</p><h2>Embedding status</h2><p>Global and workspace coverage. Updates every 15 seconds while this page is open.</p></div>
    <div class="status-actions"><button class="secondary-button" onclick={refresh} disabled={loading || syncStarting}><RefreshCw aria-hidden="true" size={16} /> {loading ? 'Checking…' : 'Refresh'}</button><button class="secondary-button" onclick={() => startSync()} disabled={!snapshot?.enabled || loading || syncStarting || syncActive}><RotateCw aria-hidden="true" size={16} /> Sync all</button></div>
  </div>
  <form class="workspace-inspect" onsubmit={(event) => { event.preventDefault(); inspectWorkspace(); }}>
    <label><span>Additional workspace path</span><input bind:value={workspaceRoot} placeholder="Absolute workspace path" /></label>
    <button class="secondary-button" type="submit" disabled={loading}><Search aria-hidden="true" size={16} /> Inspect workspace</button>
  </form>
  {#if error}<div class="status-error" role="alert">{error}{#if snapshot} — {partial ? 'Showing the archives received before the scan stopped.' : 'Showing the last successful scan.'}{/if}</div>{/if}
  {#if syncError}<div class="status-error" role="alert">{syncError}</div>{/if}
  {#if syncJob}
    <div class="settings-card sync-progress" role="region" aria-label="Embedding sync progress" aria-live="polite">
      <strong>{syncJob.state === 'queued' ? 'Sync queued' : syncJob.state === 'running' ? 'Sync in progress' : syncJob.state === 'succeeded' ? 'Sync complete' : syncJob.state === 'cancelled' ? 'Sync cancelled' : 'Sync failed'}</strong>
      <p>{syncJob.model} · {syncJob.dimensions} dimensions</p>
      <p>{syncJob.completed} / {syncJob.total} archives processed · {syncJob.embedded} records updated · {syncJob.failed} failed</p>
      {#if syncActive}<progress value={syncJob.completed} max={Math.max(1, syncJob.total)} aria-label="Sync archive progress"></progress><p>{syncJob.total ? 'Counts below refresh during sync.' : 'Preparing archive list…'} Leaving this page keeps the sync running.</p>{/if}
      {#if syncJob.error}<p class="status-error">{syncJob.error}</p>{/if}
      {#each syncJob.targets.filter(target => target.state === 'running' || target.error) as target}<p class:status-error={!!target.error}>{target.name}: {target.error || 'Syncing…'}</p>{/each}
    </div>
  {/if}
  {#if snapshot && totals}
    <div class="settings-card model-summary">
      <div><span class="scope-label">CONFIGURED MODEL</span><strong>{snapshot.enabled ? snapshot.model : 'Embeddings disabled'}</strong><small>{snapshot.dimensions} dimensions · {loading ? 'Checking archives…' : partial ? 'Scan interrupted' : `Checked ${new Date(snapshot.completed_at).toLocaleString()}`}</small></div>
      <div class="coverage"><strong>{totals.current.toLocaleString()} / {totals.eligible.toLocaleString()}</strong><span>records current</span></div>
      <div class="coverage"><strong>{totals.missing.toLocaleString()}</strong><span>missing</span></div>
      <div class="coverage"><strong>{totals.stale.toLocaleString()}</strong><span>outdated / incomplete</span></div>
    </div>
    {#if loading}<p class="scan-note" role="status">Checking embedding status… {received} {received === 1 ? 'archive' : 'archives'} received. Totals update as each archive is checked.</p>{:else if !snapshot.complete && !partial}<p class="status-error" role="status">Scan incomplete. Counts cover only records that could be inspected.</p>{/if}
    {#each snapshot.warnings as warning}<p class="status-error">{warning}</p>{/each}
    <div class="table-scroll">
      <table aria-label="Embedding coverage by archive">
        <thead><tr><th>Archive</th><th>Status</th><th>Current / eligible</th><th>Missing</th><th>Outdated</th><th>Vectors</th><th>Sync</th></tr></thead>
        <tbody>{#each snapshot.archives as archive (archive.workspace_root || archive.scope)}
          <tr>
            <td><strong>{archive.name}</strong><small>{archive.scope === 'global' ? 'Global skills' : archive.registered ? 'Registered workspace' : 'Unregistered workspace'}</small>{#if archive.workspace_root}<code>{archive.workspace_root}</code>{/if}</td>
            <td><span class="status-badge" class:pending={archive.state === 'pending'} class:failed={archive.state === 'error'}>{embeddingStateLabels[archive.state]}</span></td>
            <td>{archive.current.toLocaleString()} / {archive.eligible.toLocaleString()}</td><td>{archive.missing.toLocaleString()}</td><td>{archive.stale.toLocaleString()}</td><td>{archive.vectors.toLocaleString()}</td>
            <td><button class="secondary-button" aria-label={`Sync ${archive.name}`} onclick={() => startSync(archive)} disabled={!snapshot.enabled || !archive.exists || loading || syncStarting || syncActive}><RotateCw aria-hidden="true" size={14} /> Sync</button></td>
          </tr>
          {#if archive.exists || archive.issues.length}
            <tr class="details-row"><td colspan="7"><details><summary>{archive.name} details</summary>
              <p>{archive.records.toLocaleString()} source records · {archive.excluded.toLocaleString()} excluded · {archive.invalid_vectors.toLocaleString()} invalid vectors · {archive.unreadable.toLocaleString()} unreadable files</p>
              {#if archive.index}<p>Index: {archive.index.model || 'Vector index disabled'} · {archive.index.dimensions} dimensions · {archive.index.vectors.toLocaleString()} vectors · {archive.index.configuration_matches ? 'Model matches' : 'Model differs'}</p><p>Indexed: {new Date(archive.index.indexed_at).toLocaleString()}</p>{:else}<p>No index metadata.</p>{/if}
              {#each archive.models as model}<p><code>{model.model}</code> · {model.dimensions} dimensions · {model.vectors.toLocaleString()} vectors</p>{/each}
              {#each archive.kinds as kind}<p>{kind.kind}: {kind.current} / {kind.eligible} current · {kind.missing} missing · {kind.stale} outdated</p>{/each}
              {#each archive.issues as issue}<p class="status-error">{issue}</p>{/each}
            </details></td></tr>
          {/if}
        {/each}</tbody>
      </table>
    </div>
    <p class="scan-note">Unregistered archives are discovered beside known workspaces. Other locations can be inspected by path. Counts come from saved records; viewing this page does not start reindexing.</p>
    {#if snapshot.discovery_roots.length}<details class="scan-note"><summary>Discovery folders</summary>{#each snapshot.discovery_roots as root}<p><code>{root}</code></p>{/each}</details>{/if}
  {:else if loading}<p role="status">Checking embedding status…</p>{/if}
</section>

<style>
  .embedding-status { min-width: 0; }
  .status-actions { display: flex; flex-wrap: wrap; gap: 8px; }
  .sync-progress { padding: 18px; margin-bottom: 18px; }
  .sync-progress p { font-size: 13px; margin: 8px 0; color: var(--muted); }
  .sync-progress .status-error { color: var(--danger, #d96666); }
  .sync-progress progress { width: 100%; accent-color: var(--violet); }
  .section-heading { padding-right: 0; }
  .secondary-button { display: inline-flex; align-items: center; justify-content: center; gap: 7px; flex-shrink: 0; white-space: nowrap; }
  .workspace-inspect { display: flex; align-items: end; gap: 12px; margin-bottom: 20px; }
  .workspace-inspect label { flex: 1; min-width: 0; }
  .model-summary { display: flex; flex-wrap: wrap; gap: 24px; align-items: center; margin-bottom: 20px; padding: 18px; }
  .model-summary > div:first-child { flex: 1; min-width: 180px; overflow-wrap: anywhere; }
  .model-summary strong, .model-summary small, .coverage span { display: block; }
  .model-summary small, .coverage span, .scan-note, td small, details p { color: var(--muted); font-size: 13px; }
  .model-summary strong { margin: 6px 0; }
  .table-scroll { max-width: 100%; overflow-x: auto; border: 1px solid var(--border); border-radius: 8px; }
  table { width: 100%; min-width: 690px; border-collapse: collapse; text-align: left; font-size: 14px; }
  th, td { padding: 12px; border-bottom: 1px solid var(--border); vertical-align: top; }
  th { color: var(--muted); font-weight: 500; }
  td small, td code { display: block; margin-top: 5px; }
  td code { max-width: 360px; overflow-wrap: anywhere; font-size: 12px; color: var(--muted); }
  .status-badge { white-space: nowrap; color: var(--text); }
  .status-badge.pending { color: var(--warning, #d8a447); }
  .status-badge.failed, .status-error { color: var(--danger, #d96666); }
  .status-error { margin: 12px 0; overflow-wrap: anywhere; }
  details summary { cursor: pointer; color: var(--muted); }
  .details-row td { padding-top: 0; }
  .details-row p { margin: 8px 0; overflow-wrap: anywhere; }
  .scan-note { line-height: 1.6; }
  @media (max-width: 700px) { .workspace-inspect { align-items: stretch; flex-direction: column; } .section-heading { flex-wrap: wrap; gap: 12px; } }
</style>
