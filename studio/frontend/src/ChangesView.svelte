<script lang="ts">
  import { GitCommitHorizontal, RefreshCw, RotateCcw, Send, Trash2 } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import DiffView from './DiffView.svelte';

  type ChangedFile = { path: string; old_path?: string; status: string };
  type ChangeSnapshot = { root: string; files: ChangedFile[] };
  type ChangeDetail = { root: string; file: ChangedFile; detail: { sections: { title: string; patch: string; truncated: boolean }[] } };
  type Proposal = { type: string; scope?: string; summary: string; body?: string[]; files?: string[] };
  type Progress = { stage: string; message: string };
  type CommitReview = { id: string; root: string; proposals: Proposal[]; auto_staged: boolean; progress: Progress[]; created_at: string; updated_at: string };
  type CommitResult = { status: string; result: { messages: string[]; auto_staged: boolean; used_fallback: boolean; split: boolean }; push_error?: string };

  let workspaceInput = '';
  let workspaceRoot = '';
  let snapshot: ChangeSnapshot | null = null;
  let selected: ChangedFile | null = null;
  let detail: ChangeDetail | null = null;
  let loading = false;
  let detailLoading = false;
  let commitLoading = false;
  let error = '';
  let commitError = '';
  let review: CommitReview | null = null;
  let result: CommitResult | null = null;

  async function apiError(response: Response) {
    try { return ((await response.json()) as { error?: string }).error || `Studio returned ${response.status}`; }
    catch { return `Studio returned ${response.status}`; }
  }

  async function requestJSON<T>(method: string, url: string, body?: unknown): Promise<T> {
    const response = await fetch(url, { method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) throw new Error(await apiError(response));
    return (await response.json()) as T;
  }

  async function loadChanges(preferred = '') {
    const requested = workspaceInput.trim();
    if (!requested) return;
    loading = true; error = '';
    try {
      const response = await requestJSON<{ workspace_root: string; snapshot: ChangeSnapshot }>('GET', `/api/v1/workspaces/changes?workspace_root=${encodeURIComponent(requested)}`);
      workspaceRoot = response.workspace_root;
      workspaceInput = response.workspace_root;
      localStorage.setItem('q-studio-workspace-root', response.workspace_root);
      snapshot = response.snapshot;
      selected = response.snapshot.files.find((file) => file.path === preferred) || response.snapshot.files[0] || null;
      if (selected) await loadDetail(selected); else detail = null;
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Could not read repository changes'; snapshot = null; detail = null; }
    finally { loading = false; }
  }

  async function loadDetail(file: ChangedFile) {
    if (!workspaceRoot && !workspaceInput.trim()) return;
    selected = file; detailLoading = true; error = '';
    try {
      detail = await requestJSON<ChangeDetail>('GET', `/api/v1/workspaces/changes/file?workspace_root=${encodeURIComponent(workspaceRoot || workspaceInput.trim())}&path=${encodeURIComponent(file.path)}`);
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Could not load the selected diff'; detail = null; }
    finally { detailLoading = false; }
  }

  function statusLabel(status: string) {
    if (status === '??') return 'untracked';
    const labels: string[] = [];
    if (status[0] && status[0] !== ' ') labels.push(`index ${status[0]}`);
    if (status[1] && status[1] !== ' ') labels.push(`worktree ${status[1]}`);
    return labels.join(' · ') || status;
  }

  function proposalMessage(proposal: Proposal) {
    const subject = `${proposal.type}${proposal.scope ? `(${proposal.scope})` : ''}: ${proposal.summary}`;
    return proposal.body?.length ? `${subject}\n\n${proposal.body.map((line) => `- ${line}`).join('\n')}` : subject;
  }

  async function prepareCommit() {
    if (!workspaceRoot && !workspaceInput.trim()) return;
    commitLoading = true; commitError = ''; result = null;
    try { review = await requestJSON<CommitReview>('POST', '/api/v1/workspaces/commits', { workspace_root: workspaceRoot || workspaceInput.trim() }); await loadChanges(selected?.path || ''); }
    catch (reason) { commitError = reason instanceof Error ? reason.message : 'Could not prepare a commit review'; }
    finally { commitLoading = false; }
  }

  async function saveProposal(index: number, message: string) {
    if (!review) return;
    commitLoading = true; commitError = '';
    try { review = await requestJSON<CommitReview>('PUT', `/api/v1/workspaces/commits/${review.id}/proposals/${index}`, { message }); }
    catch (reason) { commitError = reason instanceof Error ? reason.message : 'Could not update the commit message'; }
    finally { commitLoading = false; }
  }

  async function regenerate() {
    if (!review) return;
    commitLoading = true; commitError = '';
    try { review = await requestJSON<CommitReview>('POST', `/api/v1/workspaces/commits/${review.id}/regenerate`); }
    catch (reason) { commitError = reason instanceof Error ? reason.message : 'Could not regenerate the proposal'; }
    finally { commitLoading = false; }
  }

  async function executeCommit(push: boolean) {
    if (!review || !confirm(push ? 'Create the reviewed commit(s) and push the current branch?' : 'Create the reviewed commit(s)?')) return;
    commitLoading = true; commitError = '';
    try { result = await requestJSON<CommitResult>('POST', `/api/v1/workspaces/commits/${review.id}/execute`, { push }); review = null; await loadChanges(); }
    catch (reason) { commitError = reason instanceof Error ? reason.message : 'Commit failed'; }
    finally { commitLoading = false; }
  }

  async function cancelReview() {
    if (!review) return;
    const response = await fetch(`/api/v1/workspaces/commits/${review.id}`, { method: 'DELETE' });
    if (!response.ok) commitError = await apiError(response);
    else { review = null; commitError = ''; await loadChanges(selected?.path || ''); }
  }

  onMount(() => { workspaceInput = new URL(window.location.href).searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || ''; if (workspaceInput) void loadChanges(); });
</script>

<div class="changes-layout">
  <aside class="changes-rail">
    <div class="changes-workspace"><label><span>Repository</span><input bind:value={workspaceInput} placeholder="Repository path" /></label><button class="icon-button" title="Load changes" onclick={() => loadChanges()} disabled={loading}><RefreshCw size={15} class={loading ? 'spin' : undefined} /></button></div>
    <div class="changes-heading"><span>{snapshot?.files.length || 0} changed files</span><button class="text-button" onclick={() => loadChanges(selected?.path || '')} disabled={loading}>Refresh</button></div>
    <div class="changed-files">{#each snapshot?.files || [] as file}<button class:active={selected?.path === file.path} onclick={() => loadDetail(file)}><span class="git-status">{file.status}</span><span><strong>{file.path}</strong>{#if file.old_path}<small>{file.old_path} →</small>{:else}<small>{statusLabel(file.status)}</small>{/if}</span></button>{:else}<p>{loading ? 'Reading changes…' : 'Working tree is clean.'}</p>{/each}</div>
    <button class="primary-button prepare-commit" onclick={prepareCommit} disabled={commitLoading || !snapshot?.files.length}><GitCommitHorizontal size={16} /> Prepare commit</button>
  </aside>

  <section class="change-detail">
    <header><div><h2>{selected?.path || 'Repository changes'}</h2><p>{selected ? statusLabel(selected.status) : 'Select a changed file to inspect its patch.'}</p></div>{#if detail?.detail.sections.some((section) => section.truncated)}<span class="partial-badge">Partial preview</span>{/if}</header>
    {#if error}<div class="error-panel"><h2>Changes unavailable</h2><p>{error}</p></div>{:else if detailLoading}<div class="loading-panel">Loading diff…</div>{:else if detail}<DiffView sections={detail.detail.sections} />{:else}<div class="change-empty">No file selected.</div>{/if}
  </section>

  <aside class="commit-review">
    <div class="commit-review-heading"><div><p class="eyebrow">COMMIT REVIEW</p><h2>{review ? (review.proposals.length > 1 ? `${review.proposals.length} commit proposal` : 'Commit proposal') : result ? 'Commit result' : 'Ready when you are'}</h2></div>{#if review}<button class="danger-icon" title="Cancel review" onclick={cancelReview}><Trash2 size={15} /></button>{/if}</div>
    {#if commitError}<p class="inline-warning">{commitError}</p>{/if}
    {#if commitLoading && !review}<div class="commit-wait"><RefreshCw class="spin" size={18} /><p>Inspecting the index and preparing a proposal…</p></div>{/if}
    {#if review}
      {#if review.auto_staged}<p class="auto-stage-note">The workflow staged all visible working tree changes because the index was empty.</p>{/if}
      <div class="commit-progress">{#each review.progress.slice(-8) as event}<p><strong>{event.stage}</strong><span>{event.message}</span></p>{/each}</div>
      <div class="proposal-list">{#each review.proposals as proposal, index}<article><div><strong>{index + 1}. {proposal.type}{proposal.scope ? `(${proposal.scope})` : ''}</strong><small>{proposal.files?.length ? proposal.files.join(', ') : 'all staged files'}</small></div><textarea rows={proposal.body?.length ? 6 : 3} value={proposalMessage(proposal)} onchange={(event) => saveProposal(index, event.currentTarget.value)}></textarea></article>{/each}</div>
      <div class="commit-actions"><button class="text-button" onclick={regenerate} disabled={commitLoading}><RotateCcw size={14} /> Regenerate</button><button class="primary-button" onclick={() => executeCommit(false)} disabled={commitLoading}><GitCommitHorizontal size={15} /> Commit</button><button class="primary-button push-button" onclick={() => executeCommit(true)} disabled={commitLoading}><Send size={14} /> Commit and push</button></div>
    {:else if result}
      <div class="commit-result"><strong>{result.status === 'pushed' ? 'Committed and pushed' : 'Committed'}</strong>{#each result.result.messages as message}<pre>{message}</pre>{/each}{#if result.push_error}<p class="inline-warning">Commit succeeded. Push failed: {result.push_error}</p>{/if}</div>
    {:else if !commitLoading}
      <div class="commit-intro"><GitCommitHorizontal size={26} /><p>Prepare a proposal from the current index. If the index is empty, Q stages visible working tree changes as the TUI workflow does.</p></div>
    {/if}
  </aside>
</div>
