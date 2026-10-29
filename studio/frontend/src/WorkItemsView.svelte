<script lang="ts">
  import { onMount } from 'svelte';
  import { Check, ChevronDown, ChevronsDown, CircleCheck, CircleDot, CircleX, ClipboardList, FileText, GitCompareArrows, GitMerge, Link2, MessageSquareReply, MessagesSquare, Pencil, Plus, RefreshCw, RotateCcw, Save, Search, Send, Settings2, SlidersHorizontal, UserCheck, UserMinus, UserPlus, Users, X } from '@lucide/svelte';
  import { requestResponse } from './api';
  import type { StudioProject } from './sessions/types';
  export let kind: 'issue' | 'ticket' = 'issue';
  type Item = { id: string; workspace_id: string; kind: string; number: number; title: string; body_post_id: string; state: string; labels: string[]; acceptance?: string[]; execution_mode?: string; requested_member_id?: string; linked_issue_id?: string; execution_state?: string; execution_reason?: string; active_attempt_id?: string; assigned_member_id?: string; revision: number; deleted: boolean; updated_at: string };
  type Member = { id: string; workspace_id: string; agent_id: string; definition_root: string; execution_root: string; revision: number; disabled: boolean };
  type Attempt = { id: string; ticket_id: string; status: string; worker_session_id: string; member_id: string; acceptance?: string[]; result?: unknown; feedback?: string; operation_error?: string; change_request?: { head_commit?: string; base_commit?: string; status: string } };
  type WorkerMessage = { index: number; role: string; content: string; name?: string; truncated?: boolean; tool_calls?: { id: string; name: string; arguments: string; truncated?: boolean }[] };
  type Post = { id: string; item_id: string; kind: string; content: string; content_truncated?: boolean; author: string; refs?: string[]; revision: number; deleted: boolean; created_at: string };
  type Hit = { item_id: string; post_id: string; workspace_id: string; title: string; snippet: string; revision: number; offset: number };
  const executionGuidance: Record<string, string> = {
    report: 'Read-only research and analysis. Choose Git for creating or editing code and project files, including project initialization.',
    git: 'Create or edit project files in an isolated worktree, then submit changes for review. Requires a clean Git repository with an initial commit.'
  };
  let projects: StudioProject[] = [];
  let workspaceID = '';
  let items: Item[] = [];
  let cursor = '';
  let selected: Item | null = null;
  let posts: Post[] = [];
  let threadCursor = '';
  let expanded: Record<string, { content: string; cursor: string }> = {};
  let query = '';
  let mode = 'hybrid';
  let all = false;
  let hits: Hit[] = [];
  let searchCursor = '';
  let searched = false;
  let searchStatus = '';
  let embeddingPending = false;
  let creating = false;
  let title = '';
  let body = '';
  let labels = '';
  let acceptance = '';
  let executionMode = 'report';
  let members: Member[] = [];
  let agentOptions: { id: string; description: string }[] = [];
  let agentRoot = '';
  let executionRoot = '';
  let agentID = '';
  let memberID = '';
  let attempts: Attempt[] = [];
  let deliveryErrors: { id: string; delivery_error: string; next_delivery_at?: string }[] = [];
  let feedback = '';
  let diff = '';
  let workerPages: Record<string, { messages: WorkerMessage[]; cursor: string; unknown: string }> = {};
  let showMembers = false;
  let filters = '';
  let stateFilter = '';
  let createRequestID = crypto.randomUUID();
  let editDetails = false;
  let detailRevision = 0;
  let detailTitle = '';
  let detailLabels = '';
  let detailAcceptance = '';
  let detailMember = '';
  let detailMode = 'report';
  let detailIssue = '';
  let reply = '';
  let editing = '';
  let editContent = '';
  let editRevision = 0;
  let error = '';
  let busy = false;
  let loading = false;
  let mounted = false;
  let loadedKind = kind;
  let listGeneration = 0;
  let detailGeneration = 0;
  const columns = [{ id: 'todo', name: 'To do' }, { id: 'in_progress', name: 'In progress' }, { id: 'done', name: 'Done' }];
  $: project = projects.find(value => value.id === workspaceID);
  $: if (mounted && loadedKind !== kind) { loadedKind = kind; selected = null; searched = false; creating = false; void loadItems(); }
  async function json<T>(path: string, init?: RequestInit): Promise<T> { return (await requestResponse('/api/v1/' + path, init)).json(); }
  function write(method: string, value: unknown): RequestInit { return { method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(value) }; }
  function message(cause: unknown) { error = cause instanceof Error ? cause.message : String(cause); }
  onMount(() => { void (async () => { try { projects = (await json<{ projects: StudioProject[] }>('projects')).projects; const params = new URL(location.href).searchParams; workspaceID = projects.find(p => p.id === params.get('workspace_id'))?.id || projects[0]?.id || ''; mounted = true; await changeProject(); const id = params.get('item_id'); if (id) await selectItem(id); const postID = params.get('post_id'); if (postID) await revealPost(postID); } catch (cause) { message(cause); } })(); const timer = setInterval(() => { if (document.visibilityState === 'visible' && kind === 'ticket' && !busy && !editing && !editDetails) { void loadItems(); if (selected) void loadAttempts(selected.id); } }, 5000); return () => clearInterval(timer); });
  async function changeProject() { selected = null; searched = false; attempts = []; diff = ''; agentRoot = projects.find(p => p.id === workspaceID)?.workspace_roots[0] || ''; executionRoot = agentRoot; await Promise.all([loadItems(), loadMembers(), loadAgents()]); }
  async function loadMembers() { const id = workspaceID; if (!id) { members = []; return; } try { const value = (await json<{ members: Member[] }>(`projects/${id}/members`)).members; if (id === workspaceID) members = value; } catch (cause) { if (id === workspaceID) message(cause); } }
  async function loadAgents() { const root = agentRoot; if (!root) return; try { const value = await json<{ builtins: { name: string; description: string; available: boolean }[]; profiles: { scope: string; profile: { name: string; description: string }; error?: string; shadowed: boolean }[] }>(`workspaces/agents?workspace_root=${encodeURIComponent(root)}`); if (root !== agentRoot) return; agentOptions = [...value.builtins.filter(a => a.available && a.name !== 'builtin/junior-developer').map(a => ({ id: a.name, description: a.description })), ...value.profiles.filter(p => !p.error && !p.shadowed).map(p => ({ id: `${p.scope}/${p.profile.name}`, description: p.profile.description }))]; agentID = agentOptions[0]?.id || ''; } catch (cause) { if (root === agentRoot) message(cause); } }
  async function joinMember() { busy = true; try { const existing = members.find(m => m.agent_id === agentID && m.definition_root === agentRoot && m.execution_root === executionRoot); await json(`projects/${workspaceID}/members`, write('POST', { id: existing?.id || '', agent_id: agentID, definition_root: agentRoot, execution_root: executionRoot, revision: existing?.revision || 0, disabled: false, request_id: crypto.randomUUID() })); await loadMembers(); } catch (cause) { message(cause); } finally { busy = false; } }
  async function toggleMember(member: Member) { busy = true; try { await json(`projects/${workspaceID}/members`, write('POST', { ...member, workspace_id: undefined, disabled: !member.disabled, request_id: crypto.randomUUID() })); await loadMembers(); } catch (cause) { message(cause); } finally { busy = false; } }
  async function loadAttempts(id: string) { const scope = workspaceID; try { const [value, inbox] = await Promise.all([json<{ attempts: Attempt[] }>(`work/items/${id}/attempts`), json<{ entries: { id: string; ticket_id: string; delivered: boolean; delivery_error: string; next_delivery_at?: string }[] }>(`projects/${scope}/inbox`)]); if (selected?.id !== id) return; attempts = value.attempts; deliveryErrors = inbox.entries.filter(entry => entry.ticket_id === id && !entry.delivered && entry.delivery_error); const current = items.find(i => i.id === id); if (current) selected = current; } catch (cause) { message(cause); } }
  async function controlTicket(action: string) { if (!selected) return; busy = true; error = ''; try { selected = await json<Item>(`work/items/${selected.id}/control`, write('POST', { action, revision: selected.revision, request_id: crypto.randomUUID() })); await loadItems(); await loadAttempts(selected.id); } catch (cause) { message(cause); } finally { busy = false; } }
  async function review(attempt: Attempt, decision: string) { busy = true; error = ''; try { await json(`work/attempts/${attempt.id}/review`, write('POST', { decision, feedback, request_id: crypto.randomUUID() })); feedback = ''; await loadItems(); await loadAttempts(attempt.ticket_id); } catch (cause) { message(cause); } finally { busy = false; } }
  async function readDiff(attempt: Attempt) { try { const value = await json<{ patch: string; truncated?: boolean }>(`work/attempts/${attempt.id}/change`); diff = value.patch + (value.truncated ? '\n[diff truncated]' : ''); } catch (cause) { message(cause); } }
  async function readWorker(attempt: Attempt, more = false) { try { const previous = workerPages[attempt.id]; const value = await json<{ messages: WorkerMessage[]; next_cursor?: string; running_call?: unknown; unknown_tools?: unknown[] }>(`work/attempts/${attempt.id}/worker?cursor=${encodeURIComponent(more ? previous?.cursor || '' : '')}`); workerPages = { ...workerPages, [attempt.id]: { messages: more ? [...previous.messages, ...value.messages] : value.messages, cursor: value.next_cursor || '', unknown: value.running_call || value.unknown_tools?.length ? JSON.stringify({ running: value.running_call, unknown: value.unknown_tools }, null, 2) : '' } }; } catch (cause) { message(cause); } }
  async function openWorkspaceRole(role: 'coordinator' | 'integrator') { try { const value = await json<{ registration_id: string }>(`projects/${workspaceID}/${role}`); location.href = `/sessions/${value.registration_id}`; } catch (cause) { message(cause); } }
  async function revealPost(id: string) { const value = await json<{ post: Post; next_cursor?: string }>(`work/posts/${id}?limit=8192`); if (!posts.some(p => p.id === id)) posts = [...posts, value.post]; expanded = { ...expanded, [id]: { content: value.post.content, cursor: value.next_cursor || '' } }; }
  function permalink(post?: Post) { if (!selected) return ''; const params = new URLSearchParams({ workspace_id: selected.workspace_id, item_id: selected.id }); if (post) params.set('post_id', post.id); return `/${selected.kind === 'ticket' ? 'tickets' : 'issues'}?${params}`; }
  async function loadItems(more = false) {
    const generation = ++listGeneration;
    if (!workspaceID) { items = []; return; }
    loading = true;
    try { const value = await json<{ items: Item[]; next_cursor?: string }>(`work/items?${new URLSearchParams({ workspace_id: workspaceID, kind, cursor: more ? cursor : '', limit: '100' })}`); if (generation !== listGeneration) return; items = more ? [...items, ...value.items] : value.items; cursor = value.next_cursor || ''; } catch (cause) { if (generation === listGeneration) message(cause); } finally { if (generation === listGeneration) loading = false; }
  }
  async function selectItem(id: string, more = false) {
	if (!more) editDetails = false;
    const generation = ++detailGeneration;
    try { const value = await json<{ item: Item; posts: Post[]; next_cursor?: string }>(`work/items/${encodeURIComponent(id)}/posts?${new URLSearchParams({ cursor: more ? threadCursor : '', limit: '20' })}`); if (generation !== detailGeneration) return; selected = value.item; workspaceID = value.item.workspace_id; posts = more ? [...posts, ...value.posts] : value.posts; threadCursor = value.next_cursor || ''; if (!more) { expanded = {}; editing = ''; diff = ''; history.replaceState(null, '', permalink()); } if (value.item.kind === 'ticket') await loadAttempts(id); } catch (cause) { if (generation === detailGeneration) message(cause); }
  }
  async function create() {
    if (!workspaceID || busy) return;
    busy = true; error = '';
    try { const item = await json<Item>('work/items', write('POST', { workspace_id: workspaceID, kind, title, content: body, labels: labels.split(',').map(s => s.trim()).filter(Boolean), acceptance: acceptance.split('\n').map(s => s.trim()).filter(Boolean), execution_mode: executionMode, requested_member_id: memberID, author: 'user', request_id: createRequestID })); createRequestID = crypto.randomUUID(); creating = false; title = ''; body = ''; labels = ''; acceptance = ''; await loadItems(); await selectItem(item.id); } catch (cause) { message(cause); } finally { busy = false; }
  }
  async function addReply() {
    if (!selected || busy) return; busy = true; error = '';
    try { await json(`work/items/${selected.id}/posts`, write('POST', { content: reply, author: 'user', request_id: crypto.randomUUID() })); reply = ''; await selectItem(selected.id); await loadItems(); } catch (cause) { message(cause); } finally { busy = false; }
  }
  async function expand(post: Post) {
    try { const previous = expanded[post.id]; const value = await json<{ post: Post; next_cursor?: string }>(`work/posts/${post.id}?${new URLSearchParams({ cursor: previous?.cursor || '', limit: '8192' })}`); expanded = { ...expanded, [post.id]: { content: (previous?.content || '') + value.post.content, cursor: value.next_cursor || '' } }; } catch (cause) { message(cause); }
  }
  async function editPost(post: Post) {
    try { let next = ''; let content = ''; let revision = 0; do { const value = await json<{ post: Post; next_cursor?: string }>(`work/posts/${post.id}?${new URLSearchParams({ cursor: next, limit: '16384' })}`); content += value.post.content; next = value.next_cursor || ''; revision = value.post.revision; } while (next); editing = post.id; editContent = content; editRevision = revision; } catch (cause) { message(cause); }
  }
  async function savePost() {
    if (!selected || busy || !editing) return; busy = true; error = '';
    try { await json(`work/posts/${editing}`, write('PUT', { content: editContent, author: 'user', revision: editRevision, request_id: crypto.randomUUID() })); await selectItem(selected.id); await loadItems(); } catch (cause) { message(cause); } finally { busy = false; }
  }
  async function toggleIssue() {
    if (!selected || busy) return; busy = true;
    try { selected = await json<Item>(`work/items/${selected.id}`, write('PUT', { revision: selected.revision, title: selected.title, state: selected.state === 'open' ? 'closed' : 'open', labels: selected.labels, request_id: crypto.randomUUID() })); await loadItems(); } catch (cause) { message(cause); } finally { busy = false; }
  }
  function beginDetails() { if (!selected) return; detailRevision = selected.revision; detailTitle = selected.title; detailLabels = selected.labels.join(', '); detailAcceptance = selected.acceptance?.join('\n') || ''; detailMember = selected.requested_member_id || ''; detailMode = selected.execution_mode || 'report'; detailIssue = selected.linked_issue_id || ''; editDetails = true; }
  async function saveDetails() { if (!selected) return; busy = true; error = ''; try { selected = await json<Item>(`work/items/${selected.id}`, write('PUT', { title: detailTitle, revision: detailRevision, state: selected.state, labels: detailLabels.split(',').map(s => s.trim()).filter(Boolean), acceptance: detailAcceptance.split('\n').map(s => s.trim()).filter(Boolean), requested_member_id: detailMember, execution_mode: detailMode, linked_issue_id: detailIssue.trim(), request_id: crypto.randomUUID() })); editDetails = false; await loadItems(); } catch (cause) { message(cause); } finally { busy = false; } }
  function executionLabel(state?: string) { if (!state || state === 'queued') return 'Waiting for assignment'; if (state === 'assignment_waiting') return 'Waiting for a suitable agent'; return state; }
  async function search(more = false) {
    error = ''; busy = true;
    try { const value = await json<{ hits: Hit[]; next_cursor?: string; mode: string; fallback?: string; index_pending: boolean; embedding_pending: boolean; total: number; total_relation: string }>('work/search', write('POST', { workspace_id: workspaceID, all, kind, query, mode, labels: filters.split(',').map(s => s.trim()).filter(Boolean), states: stateFilter ? [stateFilter] : [], cursor: more ? searchCursor : '', limit: 30 })); embeddingPending = value.embedding_pending; hits = more ? [...hits, ...value.hits] : value.hits; searchCursor = value.next_cursor || ''; searched = true; searchStatus = `${value.mode}${value.fallback ? ' · embedding unavailable' : ''}${value.index_pending ? ' · indexing pending' : ''} · ${value.total} ${value.total_relation === 'exact_chunks' ? 'matching passages' : 'candidates (lower bound)'}`; } catch (cause) { message(cause); } finally { busy = false; }
  }
  async function chooseHit(hit: Hit) { workspaceID = hit.workspace_id; await loadItems(); await selectItem(hit.item_id); await revealPost(hit.post_id); }
</script>

<section class="work-items" aria-label={kind === 'issue' ? 'Workspace issues' : 'Workspace tickets'}>
  <div class="work-toolbar">
    <label class="project-field">Project<select aria-label="Work item project" bind:value={workspaceID} onchange={changeProject} disabled={busy}><option value="">Select a project</option>{#each projects as p}<option value={p.id}>{p.name}</option>{/each}</select></label>
    <div class="work-actions">
      <button class="secondary-button work-icon-button" disabled={busy || loading} onclick={() => { searched = false; void loadItems(); }} title="Refresh" aria-label="Refresh"><RefreshCw aria-hidden="true" size={17} /></button>
      {#if workspaceID}<button class="secondary-button work-icon-button" onclick={() => openWorkspaceRole('coordinator')} title="Coordinator · Request intake and planning" aria-label="Coordinator"><ClipboardList aria-hidden="true" size={17} /></button><button class="secondary-button work-icon-button" onclick={() => openWorkspaceRole('integrator')} title="Integrator · Review and merge" aria-label="Integrator"><GitMerge aria-hidden="true" size={17} /></button>{#if kind === 'ticket'}<button class="secondary-button work-icon-button" class:active={showMembers} aria-expanded={showMembers} onclick={() => showMembers = !showMembers} title={`Workspace members (${members.filter(m => !m.disabled).length})`} aria-label={`Workspace members (${members.filter(m => !m.disabled).length})`}><Users aria-hidden="true" size={17} /><span class="member-count" aria-hidden="true">{members.filter(m => !m.disabled).length}</span></button>{/if}{/if}
      <button class="primary-button" disabled={!workspaceID || busy} onclick={() => creating = !creating}><Plus aria-hidden="true" size={16} />New {kind}</button>
    </div>
  </div>
  {#if !projects.length}<p>Create a project in <a href="/sessions">Sessions</a> to organize issues and tickets.</p>{/if}
  {#if error}<div role="alert" class="work-error">{error}</div>{/if}
  {#if kind === 'ticket' && project && !members.some(member => !member.disabled)}
    <div class="work-notice" role="status"><p>No subagents have joined this project. Issued tickets wait for assignment until a suitable subagent joins.</p><button class="secondary-button work-icon-button" onclick={() => showMembers = true} title="Join subagents" aria-label="Join subagents"><UserPlus aria-hidden="true" size={17} /></button></div>
  {/if}
  {#if showMembers && project && kind === 'ticket'}
    <section class="work-create" aria-label="Workspace members"><h2>Workspace members</h2><p>Join existing subagent profiles to this project. System One checks suitability before each execution.</p>
      <label>Profile directory<select bind:value={agentRoot} onchange={loadAgents}>{#each project.workspace_roots as root}<option value={root}>{root}</option>{/each}</select></label>
      <label>Subagent<select aria-label="Join subagent" bind:value={agentID}>{#each agentOptions as option}<option value={option.id}>{option.id} · {option.description}</option>{/each}</select></label>
      <label>Execution directory<select bind:value={executionRoot}>{#each project.workspace_roots as root}<option value={root}>{root}</option>{/each}</select></label><button class="secondary-button work-icon-button" onclick={joinMember} disabled={busy || !agentID} title="Join workspace" aria-label="Join workspace"><UserPlus aria-hidden="true" size={17} /></button>
      {#each members as member}<div class="member-row"><div class="member-description"><strong>{member.agent_id}</strong><small>{member.execution_root}</small></div><span class="member-status">{member.disabled ? 'Disabled' : 'Joined'}</span><button class="secondary-button work-icon-button" onclick={() => toggleMember(member)} disabled={busy} title={member.disabled ? 'Enable member' : 'Disable member'} aria-label={member.disabled ? 'Enable member' : 'Disable member'}>{#if member.disabled}<UserCheck aria-hidden="true" size={17} />{:else}<UserMinus aria-hidden="true" size={17} />{/if}</button></div>{:else}<p>No joined subagents.</p>{/each}
    </section>
  {/if}
  {#if creating}
    <form class="work-create" onsubmit={(event) => { event.preventDefault(); void create(); }}>
      <h2>New {kind}</h2><label>Title<input aria-label="Work item title" bind:value={title} maxlength="512" required /></label>
      <label>Body<textarea aria-label="Work item body" bind:value={body} rows="7" required></textarea></label>
      <label>Labels<input bind:value={labels} placeholder="Comma-separated labels" /></label>
      {#if kind === 'ticket'}<label>Acceptance criteria<textarea bind:value={acceptance} rows="3" placeholder="One criterion per line"></textarea></label><label>Execution mode<select aria-label="Execution mode" bind:value={executionMode}><option value="report">Report / research</option><option value="git">Git worktree with review</option></select><small class="mode-help">{executionGuidance[executionMode]}</small></label>{/if}
      {#if kind === 'ticket'}<label>Requested member<select aria-label="Requested member" bind:value={memberID}><option value="">System One selects from joined members</option>{#each members.filter(m => !m.disabled) as member}<option value={member.id}>{member.agent_id}</option>{/each}</select></label><p class="queue-hint">Tickets are assigned automatically after creation. System One selects a suitable joined subagent.</p>{/if}
      <div class="work-action-group"><button class="primary-button" type="submit" disabled={busy || !title.trim() || !body.trim()}><Plus aria-hidden="true" size={16} />Create {kind}</button> <button class="secondary-button work-icon-button" type="button" onclick={() => creating = false} title="Cancel" aria-label="Cancel"><X aria-hidden="true" size={17} /></button></div>
    </form>
  {/if}
  <div class="work-search-panel">
    <form class="work-search" onsubmit={(event) => { event.preventDefault(); void search(); }}>
      <div class="work-query"><Search size={17} /><input aria-label="Search work items" bind:value={query} placeholder={kind === 'issue' ? 'Search issues or ask a question…' : 'Search tickets or ask a question…'} /></div>
      <select aria-label="Work search mode" bind:value={mode}><option value="hybrid">Hybrid</option><option value="lexical">Text</option><option value="semantic">Semantic</option></select>
      <label class="all-projects"><input type="checkbox" bind:checked={all} />All projects</label>
      <button class="secondary-button work-icon-button" type="submit" disabled={busy || (!workspaceID && !all)} title="Search" aria-label="Search"><Search aria-hidden="true" size={17} /></button>
    </form>
    <details class="work-filters">
      <summary><SlidersHorizontal size={14} />Filters{#if filters.trim() || stateFilter}<span class="filter-count">{Number(!!filters.trim()) + Number(!!stateFilter)}</span>{/if}</summary>
      <div class="filter-fields"><label>Search labels<input aria-label="Search labels" bind:value={filters} placeholder="Comma-separated labels" /></label><label>Search state<select aria-label="Search state" bind:value={stateFilter}><option value="">Any state</option>{#if kind === 'issue'}<option value="open">Open</option><option value="closed">Closed</option>{:else}{#each columns as column}<option value={column.id}>{column.name}</option>{/each}{/if}</select></label></div>
    </details>
  </div>
  {#if searched}<section aria-label="Work search results"><small>{searchStatus}{embeddingPending ? ' · embeddings pending' : ''}</small>{#each hits as hit}<button class="search-hit" onclick={() => chooseHit(hit)}><strong>{hit.title}</strong><p>{hit.snippet}</p><code>{hit.workspace_id} · post {hit.post_id} · revision {hit.revision} · offset {hit.offset}</code></button>{:else}<p>No matching passages.</p>{/each}{#if searchCursor}<button class="secondary-button work-icon-button" onclick={() => search(true)} disabled={busy} title="More search results" aria-label="More search results"><ChevronDown aria-hidden="true" size={17} /></button>{/if}</section>{/if}
  <div class:ticket-layout={kind === 'ticket'} class:has-selection={!!selected} class="work-layout">
    <section class="work-list" aria-label={kind === 'ticket' ? 'Ticket board' : 'Issue list'}>
      {#if kind === 'ticket'}<div class="ticket-board">{#each columns as column}<section class="ticket-column"><h2><span class="column-dot" class:in-progress={column.id === 'in_progress'} class:done={column.id === 'done'}></span>{column.name}<span class="column-count" aria-hidden="true">{items.filter(i => !i.deleted && i.state === column.id).length}</span></h2>{#each items.filter(i => !i.deleted && i.state === column.id) as item}<button class="work-card" class:active={selected?.id === item.id} onclick={() => selectItem(item.id)}><strong>#{item.number} {item.title}</strong><small>{item.execution_mode} · {executionLabel(item.execution_state)}</small>{#each item.labels as label}<span>{label}</span>{/each}</button>{:else}<p class="column-empty">No tickets</p>{/each}</section>{/each}</div>{:else}{#each items.filter(i => !i.deleted) as item}<button class="work-card" class:active={selected?.id === item.id} onclick={() => selectItem(item.id)}><strong>#{item.number} {item.title}</strong><small>{item.state}</small>{#each item.labels as label}<span>{label}</span>{/each}</button>{:else}<div class="work-empty"><div class="empty-symbol"><FileText size={23} /></div><h2>{loading ? 'Loading issues…' : 'No issues yet'}</h2><p>{workspaceID ? 'Keep project decisions, findings, and discussions together.' : 'Select a project to view its issues.'}</p>{#if workspaceID && !loading}<button class="primary-button" disabled={busy} onclick={() => creating = true}><Plus aria-hidden="true" size={15} />Create an issue</button>{/if}</div>{/each}{/if}
      {#if cursor}<button class="secondary-button work-icon-button" onclick={() => loadItems(true)} disabled={loading} title={`More ${kind}s`} aria-label={`More ${kind}s`}><ChevronDown aria-hidden="true" size={17} /></button>{/if}
    </section>
    {#if selected}<section class="work-thread" aria-label="Work item thread"><header><div class="thread-heading"><small>{project?.name} · {selected.state}</small><div class="work-action-group"><button class="secondary-button work-icon-button" onclick={beginDetails} disabled={busy} title="Edit details" aria-label="Edit details"><Settings2 aria-hidden="true" size={17} /></button>{#if kind === 'issue'}<button class="secondary-button work-icon-button" onclick={toggleIssue} disabled={busy} title={selected.state === 'open' ? 'Close issue' : 'Reopen issue'} aria-label={selected.state === 'open' ? 'Close issue' : 'Reopen issue'}>{#if selected.state === 'open'}<CircleCheck aria-hidden="true" size={17} />{:else}<CircleDot aria-hidden="true" size={17} />{/if}</button>{/if}</div></div><h2>#{selected.number} {selected.title}</h2><code>{selected.id}</code></header>
      {#if editDetails}<form onsubmit={(event) => { event.preventDefault(); void saveDetails(); }}><label>Title<input aria-label="Edit work item title" bind:value={detailTitle} required maxlength="512" /></label><label>Labels<input bind:value={detailLabels} /></label>{#if selected.kind === 'ticket'}<label>Acceptance criteria<textarea bind:value={detailAcceptance} rows="3"></textarea></label><label>Execution mode<select aria-label="Execution mode" bind:value={detailMode}><option value="report">Report / research</option><option value="git">Git worktree with review</option></select><small class="mode-help">{executionGuidance[detailMode]}</small></label><label>Requested member<select bind:value={detailMember}><option value="">System One selects</option>{#each members.filter(m => !m.disabled) as member}<option value={member.id}>{member.agent_id}</option>{/each}</select></label><label>Linked issue ID<input bind:value={detailIssue} /></label>{/if}<div class="work-action-group"><button class="secondary-button work-icon-button" type="submit" disabled={busy} title="Save details" aria-label="Save details"><Save aria-hidden="true" size={17} /></button><button class="secondary-button work-icon-button" type="button" onclick={() => editDetails = false} title="Cancel" aria-label="Cancel"><X aria-hidden="true" size={17} /></button></div></form>{/if}
      {#if selected.acceptance?.length}<h3>Acceptance criteria</h3><ul>{#each selected.acceptance as criterion}<li>{criterion}</li>{/each}</ul>{/if}
      {#if selected.kind === 'ticket'}
        <section aria-label="Ticket execution">{#each deliveryErrors as notification}<p role="status">Integrator notification pending: {notification.delivery_error}{notification.next_delivery_at ? ` · retry after ${new Date(notification.next_delivery_at).toLocaleTimeString()}` : ""}</p>{/each}<h3>Execution · {executionLabel(selected.execution_state)}</h3>{#if selected.execution_reason}<p role="status">{selected.execution_reason}</p>{/if}<p>Assigned member: {members.find(m => m.id === selected?.assigned_member_id)?.agent_id || 'Unassigned'}</p>
          {#if selected.state !== 'done' && ['cancelled','blocked','failed','unknown','rejected'].includes(selected.execution_state || '')}<button class="secondary-button work-icon-button" disabled={busy} onclick={() => controlTicket('retry')} title="Retry execution" aria-label="Retry execution"><RotateCcw aria-hidden="true" size={17} /></button>{/if}
          {#each attempts as attempt}<details open={attempt.id === selected.active_attempt_id}><summary>{attempt.status} · {attempt.id}</summary><p>Worker: <code>{attempt.worker_session_id}</code></p><button class="secondary-button work-icon-button" onclick={() => readWorker(attempt)} title="Read worker conversation" aria-label="Read worker conversation"><MessagesSquare aria-hidden="true" size={17} /></button>{#if attempt.result}<pre>{JSON.stringify(attempt.result, null, 2)}</pre>{/if}{#if attempt.feedback}<p>Integrator feedback: {attempt.feedback}</p>{/if}{#if attempt.operation_error}<p role="status">{attempt.operation_error}</p>{/if}
            {#if workerPages[attempt.id]}<section aria-label="Worker conversation">{#if workerPages[attempt.id].unknown}<pre>{workerPages[attempt.id].unknown}</pre>{/if}{#each workerPages[attempt.id].messages as message}<article><strong>{message.role} {message.name || ''} · message {message.index}</strong><pre>{message.content}{message.truncated ? '\n[preview truncated]' : ''}</pre>{#each message.tool_calls || [] as call}<pre>{call.name} ({call.id})\n{call.arguments}{call.truncated ? '\n[arguments truncated]' : ''}</pre>{/each}</article>{/each}{#if workerPages[attempt.id].cursor}<button class="secondary-button work-icon-button" onclick={() => readWorker(attempt, true)} title="More worker messages" aria-label="More worker messages"><ChevronDown aria-hidden="true" size={17} /></button>{/if}</section>{/if}
            {#if attempt.change_request}<p>Pinned base: <code>{attempt.change_request.base_commit}</code><br />Pinned head: <code>{attempt.change_request.head_commit || 'Working'}</code></p>{#if attempt.change_request.head_commit}<button class="secondary-button work-icon-button" onclick={() => readDiff(attempt)} title="Read change request" aria-label="Read change request"><GitCompareArrows aria-hidden="true" size={17} /></button>{/if}{/if}
            {#if attempt.id === selected.active_attempt_id && ['awaiting_review','blocked','failed','unknown','merge_failed','close_failed'].includes(attempt.status)}<label>Review feedback<textarea aria-label="Ticket review feedback" bind:value={feedback} rows="3"></textarea></label><div class="work-action-group">{#if ['awaiting_review','merge_failed'].includes(attempt.status)}<button class="secondary-button work-icon-button" onclick={() => review(attempt, 'accept')} disabled={busy} title={attempt.change_request ? 'Accept and merge' : 'Accept result'} aria-label={attempt.change_request ? 'Accept and merge' : 'Accept result'}>{#if attempt.change_request}<GitMerge aria-hidden="true" size={17} />{:else}<Check aria-hidden="true" size={17} />{/if}</button>{/if}<button class="secondary-button work-icon-button" onclick={() => review(attempt, 'request_changes')} disabled={busy || !feedback.trim()} title="Request changes" aria-label="Request changes"><MessageSquareReply aria-hidden="true" size={17} /></button><button class="secondary-button work-icon-button danger" onclick={() => review(attempt, 'reject')} disabled={busy} title="Reject result" aria-label="Reject result"><CircleX aria-hidden="true" size={17} /></button></div>{/if}
          </details>{/each}{#if diff}<pre aria-label="Ticket change diff">{diff}</pre>{/if}
        </section>
      {/if}
      {#each posts as post}<article class="work-post"><header><div class="post-meta"><strong>{post.kind === 'body' ? 'Body' : post.author}</strong><small>revision {post.revision}</small></div><div class="work-action-group"><a class="work-icon-button post-permalink" href={permalink(post)} title={`Post permalink · ${post.id}`} aria-label={`post ${post.id}`}><Link2 aria-hidden="true" size={17} /></a>{#if !post.deleted}<button class="secondary-button work-icon-button" onclick={() => editPost(post)} disabled={busy} title={`Edit ${post.kind}`} aria-label={`Edit ${post.kind}`}><Pencil aria-hidden="true" size={17} /></button>{/if}</div></header>
        {#if editing === post.id}<textarea aria-label="Edit post content" rows="10" bind:value={editContent}></textarea><div class="work-action-group"><button class="secondary-button work-icon-button" onclick={savePost} disabled={busy || !editContent.trim()} title="Save post" aria-label="Save post"><Save aria-hidden="true" size={17} /></button><button class="secondary-button work-icon-button" onclick={() => editing = ''} title="Cancel" aria-label="Cancel"><X aria-hidden="true" size={17} /></button></div>{:else}<div class="post-content">{post.deleted ? 'Deleted post' : expanded[post.id]?.content || post.content}</div>{#if post.content_truncated && (!expanded[post.id] || expanded[post.id].cursor)}<button class="secondary-button work-icon-button" onclick={() => expand(post)} title="Read more" aria-label="Read more"><ChevronsDown aria-hidden="true" size={17} /></button>{/if}{/if}
        {#if post.refs?.length}<ul>{#each post.refs as ref}<li>{ref}</li>{/each}</ul>{/if}
      </article>{/each}
      {#if threadCursor}<button class="secondary-button work-icon-button" onclick={() => selected && selectItem(selected.id, true)} disabled={busy} title="More replies" aria-label="More replies"><ChevronDown aria-hidden="true" size={17} /></button>{/if}
      <form onsubmit={(event) => { event.preventDefault(); void addReply(); }}><label>Reply<textarea aria-label="Work item reply" bind:value={reply} rows="4" required></textarea></label><button class="primary-button" disabled={busy || !reply.trim()}><Send aria-hidden="true" size={16} />Add reply</button></form>
    </section>{/if}
  </div>
</section>

<style>
  .work-items { display: grid; gap: 20px; min-width: 0; padding-top: 22px; font-size: calc(14px * var(--text-scale)); }
  label { display: grid; min-width: 0; gap: 7px; color: var(--muted); font-size: inherit; }
  input:not([type="checkbox"]), select { height: calc(38px * var(--text-scale)); font: inherit; }
  input:not([type="checkbox"]) { padding: 0 11px; }
  select { padding: 0 30px 0 11px; line-height: 1.4; background-position: calc(100% - 15px) 50%, calc(100% - 10px) 50%; }
  textarea { width: 100%; min-width: 0; padding: 11px; color: var(--text); border: 1px solid var(--border-strong); border-radius: 5px; background: var(--field-bg); font: inherit; line-height: 1.6; resize: vertical; }
  textarea:focus { border-color: var(--violet); }
  button { font: inherit; cursor: pointer; }
  button:disabled { cursor: not-allowed; opacity: .5; }
  .primary-button, .secondary-button { display: inline-flex; align-items: center; justify-content: center; gap: 7px; min-height: calc(38px * var(--text-scale)); padding: 6px 12px; white-space: nowrap; }
  .work-icon-button { position: relative; display: inline-flex; align-items: center; justify-content: center; flex: none; width: calc(38px * var(--text-scale)); height: calc(38px * var(--text-scale)); min-height: calc(38px * var(--text-scale)); padding: 0; color: var(--muted); border: 1px solid var(--border); border-radius: 6px; background: var(--surface-raised); text-decoration: none; }
  .work-icon-button :global(svg) { flex: none; width: calc(17px * var(--text-scale)); height: calc(17px * var(--text-scale)); stroke-width: 1.75px; }
  .work-icon-button:hover { color: var(--text); border-color: var(--border-strong); background: var(--hover-bg); }
  .work-icon-button:focus-visible { outline: 2px solid var(--violet); outline-offset: 3px; }
  .work-icon-button.danger { color: var(--error); }
  .work-action-group { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
  .member-count { position: absolute; top: -5px; right: -5px; display: grid; place-items: center; min-width: 18px; height: 18px; padding: 0 4px; border: 1px solid var(--border); border-radius: 9px; background: var(--panel-bg); color: var(--muted); font-size: calc(10px * var(--text-scale)); }
  .member-row { display: flex; align-items: center; gap: 12px; padding-top: 14px; border-top: 1px solid var(--border); }
  .member-description { flex: 1; min-width: 0; overflow-wrap: anywhere; }
  .member-status { color: var(--muted); font-size: calc(12px * var(--text-scale)); }
  .secondary-button.active { color: var(--accent-text); border-color: var(--violet); background: var(--violet-soft); }
  .work-toolbar { display: flex; align-items: flex-end; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .project-field { width: min(100%, 300px); }
  .work-actions { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; }
  .work-search-panel { display: grid; min-width: 0; gap: 12px; padding: 16px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel-bg); }
  .work-search { display: grid; grid-template-columns: minmax(0, 1fr) 136px auto auto; align-items: center; gap: 12px; min-width: 0; }
  .work-query { position: relative; min-width: 0; }
  .work-query :global(svg) { position: absolute; left: 12px; top: 50%; transform: translateY(-50%); color: var(--subtle); pointer-events: none; }
  .work-query input { padding-left: 38px; }
  .all-projects { display: flex; align-items: center; gap: 8px; white-space: nowrap; cursor: pointer; }
  .all-projects input { flex: none; margin: 0; }
  .work-filters { margin: 0; padding: 0; border: 0; border-radius: 0; }
  .work-filters summary { display: flex; width: fit-content; align-items: center; gap: 7px; color: var(--muted); cursor: pointer; list-style: none; font-size: calc(12px * var(--text-scale)); }
  .work-filters summary::-webkit-details-marker { display: none; }
  .work-filters[open] summary { color: var(--accent-text); }
  .filter-count, .column-count { display: inline-flex; align-items: center; justify-content: center; min-width: 22px; height: 22px; padding: 0 5px; border-radius: 4px; background: var(--hover-bg); color: var(--muted); font-size: calc(12px * var(--text-scale)); font-weight: 400; }
  .filter-fields { display: grid; grid-template-columns: minmax(0, 1fr) minmax(140px, 220px); gap: 12px; max-width: 650px; padding-top: 14px; }
  .work-layout, .work-list { display: grid; min-width: 0; gap: 12px; align-content: start; }
  .work-layout { gap: 20px; align-items: start; }
  .work-layout.has-selection:not(.ticket-layout) { grid-template-columns: minmax(230px, .8fr) minmax(0, 1.6fr); }
  .work-card, .search-hit { display: block; width: 100%; min-width: 0; padding: 16px; border: 1px solid var(--border); border-radius: 7px; background: var(--panel-bg); color: var(--text); text-align: left; overflow-wrap: anywhere; }
  .work-card:hover, .search-hit:hover { border-color: var(--border-strong); background: var(--surface-raised); }
  .work-card.active { border-color: var(--violet); background: var(--violet-soft); }
  .work-card strong { font-weight: 500; line-height: 1.5; }
  .work-card small { display: block; margin-top: 8px; color: var(--muted); font-size: calc(12px * var(--text-scale)); }
  .work-card span { display: inline-block; margin: 10px 5px 0 0; padding: 2px 7px; border: 1px solid var(--border); border-radius: 4px; color: var(--muted); font-size: calc(11px * var(--text-scale)); }
  .work-empty { display: grid; min-height: 280px; justify-items: center; align-content: center; gap: 10px; padding: 32px 20px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel-subtle); text-align: center; }
  .empty-symbol { display: grid; width: 44px; height: 44px; margin-bottom: 3px; place-items: center; color: var(--accent-muted); border: 1px solid var(--border); border-radius: 9px; background: var(--surface-raised); }
  .work-empty h2 { margin: 0; font-size: calc(17px * var(--text-scale)); font-weight: 500; }
  .work-empty p { margin: 0 0 7px; color: var(--muted); line-height: 1.6; }
  .work-create, .work-thread { display: grid; min-width: 0; gap: 16px; padding: 22px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel-bg); }
  .work-create { width: 100%; max-width: 900px; }
  .work-create h2 { margin: 0; font-size: calc(18px * var(--text-scale)); font-weight: 500; }
  .work-create p { margin: 0; color: var(--muted); }
  .mode-help { color: var(--muted); font-size: calc(12px * var(--text-scale)); line-height: 1.6; }
  .work-create small { display: block; color: var(--subtle); overflow-wrap: anywhere; }
  .work-thread > header { display: grid; min-width: 0; gap: 10px; }
  .thread-heading, .work-post header { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-width: 0; }
  .thread-heading > small { min-width: 0; color: var(--muted); overflow-wrap: anywhere; }
  .thread-heading .work-action-group, .work-post header .work-action-group { flex: none; }
  .post-meta { display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 10px; min-width: 0; overflow-wrap: anywhere; }
  .work-thread h2 { margin: 2px 0; font-size: calc(20px * var(--text-scale)); overflow-wrap: anywhere; }
  .work-thread h3 { font-size: calc(15px * var(--text-scale)); }
  .work-thread code, .search-hit code { overflow-wrap: anywhere; font-size: calc(11px * var(--text-scale)); color: var(--subtle); }
  .work-thread form { display: grid; gap: 12px; }
  .work-thread form > button { justify-self: start; }
  .work-post { display: grid; min-width: 0; gap: 12px; padding-top: 20px; border-top: 1px solid var(--border); }
  .work-post header small { color: var(--subtle); }
  .work-post header a { overflow-wrap: anywhere; color: var(--accent-text); font-size: calc(11px * var(--text-scale)); }
  .post-content { white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.7; }
  pre { max-height: 36rem; overflow: auto; padding: 16px; border: 1px solid var(--border); border-radius: 5px; background: var(--code-bg); color: var(--code-text); font-size: calc(12px * var(--text-scale)); white-space: pre-wrap; overflow-wrap: anywhere; }
  details:not(.work-filters) { margin: 12px 0; padding: 12px; border: 1px solid var(--border); border-radius: 6px; }
  .work-error { padding: 14px 16px; color: var(--error); border: 1px solid var(--error); border-radius: 6px; overflow-wrap: anywhere; }
  .work-notice { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px; padding: 14px 16px; border: 1px solid var(--border); border-radius: 6px; background: var(--panel-bg); color: var(--muted); }
  .work-notice p { flex: 1 1 220px; margin: 0; line-height: 1.6; }
  .search-hit { margin-top: 10px; }
  .search-hit p { margin: 8px 0; color: var(--muted); line-height: 1.6; }
  .ticket-board { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; }
  .ticket-column { display: grid; min-width: 0; min-height: 300px; align-content: start; gap: 10px; padding: 14px; border: 1px solid var(--border); border-radius: 8px; background: var(--panel-subtle); }
  .ticket-column h2 { display: flex; align-items: center; gap: 8px; margin: 0 0 5px; font-size: inherit; font-weight: 500; }
  .column-count { margin-left: auto; }
  .column-dot { width: 7px; height: 7px; flex: none; border-radius: 50%; background: var(--subtle); }
  .column-dot.in-progress { background: var(--violet); }
  .column-dot.done { background: var(--green); }
  .column-empty { margin: 16px 0 0; color: var(--subtle); text-align: center; font-size: calc(12px * var(--text-scale)); }
  @media (max-width: 1000px) {
    .work-search { grid-template-columns: minmax(0, 1fr) 130px auto; }
    .work-query { grid-column: 1 / -1; }
    .work-search > button { justify-self: end; }
    .work-layout.has-selection:not(.ticket-layout) { grid-template-columns: 1fr; }
  }
  @media (max-width: 600px) {
    .work-items { padding-top: 18px; gap: 16px; }
    .project-field { width: 100%; }
    .work-actions { width: 100%; }
    .work-actions .primary-button { margin-left: auto; }
    .work-search-panel, .work-create, .work-thread { padding: 14px; }
    .work-search { grid-template-columns: minmax(0, 1fr) auto; gap: 10px; }
    .work-search > select { grid-column: 1; }
    .work-search > .all-projects { grid-column: 1; grid-row: 3; }
    .work-search > button { grid-column: 2; grid-row: 2 / 4; align-self: end; }
    .filter-fields, .ticket-board { grid-template-columns: 1fr; }
    .ticket-column { min-height: 150px; }
  }
</style>
