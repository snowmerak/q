<script lang="ts">
  import { onMount } from 'svelte';
  import { Archive, Check, CircleDot, Folder, FolderOpen, Kanban, Layers, Pencil, Plus, Save, Settings2, SlidersHorizontal, UserCheck, UserMinus, Users, X } from '@lucide/svelte';
  import { requestJSON, requestResponse } from './api';
  import DirectoryBrowser from './sessions/DirectoryBrowser.svelte';
  import type { StudioProject } from './sessions/types';

  type Agent = { id: string; kind: string; description: string; available: boolean; reason?: string };
  type Member = { id: string; agent_id: string; definition_root: string; execution_root: string; disabled: boolean; revision: number };
  type Defaults = { agent_ids: string[]; revision: number };
  let projects: StudioProject[] = [];
  let selectedID = '';
  let editing = false;
  let defaultsMode = false;
  let name = '';
  let roots: string[] = [];
  let rootInput = '';
  let tickets = 0;
  let subagents = 0;
  let section = 'basic';
  let defaults: Defaults = { agent_ids: [], revision: 0 };
  let defaultIDs: string[] = [];
  let initialIDs: string[] = [];
  let options: Agent[] = [];
  let globalOptions: Agent[] = [];
  let globalLimit = 3;
  let members: Member[] = [];
  let memberID = '';
  let memberRevision = 0;
  let agentID = '';
  let definitionRoot = '';
  let executionRoot = '';
  let memberOptions: Agent[] = [];
  let busy = false;
  let loading = true;
  let error = '';
  let status = '';
  let browsing = false;
  let generation = 0;
  let optionGeneration = 0;
  $: project = projects.find(p => p.id === selectedID);
  $: selectedAgents = defaultsMode ? defaultIDs : initialIDs;
  $: selectionOptions = defaultsMode ? globalOptions : options;
  $: missingIDs = selectedAgents.filter(id => !selectionOptions.some(o => o.id === id));
  $: invalidSelection = missingIDs.length > 0 || selectionOptions.some(o => selectedAgents.includes(o.id) && !o.available);
  $: dirty = editing && (defaultsMode ? JSON.stringify(defaultIDs) !== JSON.stringify(defaults.agent_ids) : !project || name !== project.name || JSON.stringify(roots) !== JSON.stringify(project.workspace_roots) || tickets !== (project.limits?.max_parallel_tickets || 0) || subagents !== (project.limits?.max_parallel_subagents || 0) || !!rootInput.trim());
  function message(cause: unknown) { error = cause instanceof Error ? cause.message : String(cause); }
  function effective(value: number) { return value > 0 ? Math.min(value, globalLimit) : globalLimit; }
  function canSwitch() { return !busy && (!dirty || window.confirm('Discard unsaved project settings?')); }
  function updateURL() {
    const params = new URLSearchParams(defaultsMode ? { defaults: '1' } : selectedID ? { project_id: selectedID, section } : { new: '1' });
    history.replaceState({}, '', `/projects?${params}`);
  }
  async function loadOptions(root: string) {
    if (!root) { options = [...globalOptions]; return; }
    options = (await requestJSON<{ agents: Agent[] }>('GET', `/api/v1/project-options?workspace_root=${encodeURIComponent(root)}`)).agents;
  }
  async function resetMember(member?: Member) {
    memberID = member?.id || ''; memberRevision = member?.revision || 0;
    definitionRoot = member?.definition_root || project?.workspace_roots[0] || '';
    executionRoot = member?.execution_root || project?.workspace_roots[0] || '';
    agentID = member?.agent_id || '';
    await loadMemberOptions();
  }
  async function loadMemberOptions() {
    const current = ++optionGeneration;
    if (!definitionRoot) { memberOptions = []; return; }
    try {
      const result = await requestJSON<{ agents: Agent[] }>('GET', `/api/v1/project-options?workspace_root=${encodeURIComponent(definitionRoot)}`);
      if (current !== optionGeneration) return;
      memberOptions = result.agents;
      if (!memberID && !memberOptions.some(a => a.id === agentID && a.available)) agentID = memberOptions.find(a => a.available)?.id || '';
    } catch (cause) { if (current === optionGeneration) message(cause); }
  }
  async function choose(value?: StudioProject, force = false) {
    if (!force && !canSwitch()) return;
    const current = ++generation;
    selectedID = value?.id || ''; defaultsMode = false; editing = true; error = ''; status = ''; loading = true;
    name = value?.name || ''; roots = [...(value?.workspace_roots || [])]; rootInput = ''; members = [];
    tickets = value?.limits?.max_parallel_tickets || 0; subagents = value?.limits?.max_parallel_subagents || 0;
    initialIDs = [...defaults.agent_ids];
    updateURL();
    try {
      await loadOptions(roots[0] || '');
      if (value) {
        const result = await requestJSON<{ members: Member[] }>('GET', `/api/v1/projects/${value.id}/members`);
        if (current !== generation) return;
        members = result.members;
      }
      await resetMember();
    } catch (cause) { if (current === generation) message(cause); }
    finally { if (current === generation) loading = false; }
  }
  function showDefaults() {
    if (!canSwitch()) return;
    generation++; defaultsMode = true; editing = true; loading = false; selectedID = ''; error = ''; status = ''; defaultIDs = [...defaults.agent_ids]; updateURL();
  }
  function toggleAgent(id: string, checked: boolean) {
    const ids = checked ? [...selectedAgents, id] : selectedAgents.filter(value => value !== id);
    if (defaultsMode) defaultIDs = ids; else initialIDs = ids;
  }
  async function addRoot() {
    const root = rootInput.trim(); if (!root) return;
    if (!roots.includes(root)) roots = [...roots, root];
    rootInput = '';
    try { await loadOptions(roots[0]); } catch (cause) { message(cause); }
  }
  async function removeRoot(index: number) {
    roots = roots.filter((_, i) => i !== index);
    try { await loadOptions(roots[0] || ''); } catch (cause) { message(cause); }
  }
  async function save() {
    busy = true; error = ''; status = '';
    try {
      if (defaultsMode) {
        defaults = await requestJSON<Defaults>('PUT', '/api/v1/project-defaults', { agent_ids: defaultIDs, revision: defaults.revision });
        status = 'Default subagents saved. Existing projects keep their members.';
      } else {
        if (rootInput.trim()) await addRoot();
        const input = { name, workspace_roots: roots, limits: { max_parallel_tickets: tickets, max_parallel_subagents: subagents }, ...(!selectedID ? { initial_members: initialIDs.map(id => ({ agent_id: id, definition_root: roots[0], execution_root: roots[0], disabled: false })) } : {}) };
        const saved = await requestJSON<StudioProject>(selectedID ? 'PUT' : 'POST', selectedID ? `/api/v1/projects/${selectedID}` : '/api/v1/projects', input);
        projects = (await requestJSON<{ projects: StudioProject[] }>('GET', '/api/v1/projects')).projects;
        await choose(saved, true);
        status = 'Project saved';
      }
    } catch (cause) { message(cause); }
    finally { busy = false; }
  }
  async function archive() {
    if (!project || !window.confirm(`Archive ${project.name}? Project history and reserved manager sessions are retained. Ordinary sessions remain available as independents.`)) return;
    busy = true; error = '';
    try {
      await requestResponse(`/api/v1/projects/${selectedID}`, { method: 'DELETE' });
      projects = projects.filter(p => p.id !== selectedID); selectedID = ''; editing = false; generation++; status = 'Project archived';
      history.replaceState({}, '', '/projects');
    } catch (cause) { message(cause); }
    finally { busy = false; }
  }
  async function saveMember(member?: Member) {
    busy = true; error = ''; status = '';
    try {
      const input = member ? { ...member, disabled: !member.disabled } : { id: memberID, revision: memberRevision, agent_id: agentID, definition_root: definitionRoot, execution_root: executionRoot, disabled: members.find(m => m.id === memberID)?.disabled || false };
      await requestJSON('POST', `/api/v1/projects/${selectedID}/members`, { id: input.id, agent_id: input.agent_id, definition_root: input.definition_root, execution_root: input.execution_root, disabled: input.disabled, revision: input.revision, request_id: crypto.randomUUID() });
      members = (await requestJSON<{ members: Member[] }>('GET', `/api/v1/projects/${selectedID}/members`)).members;
      await resetMember(); status = 'Subagents updated';
    } catch (cause) { message(cause); }
    finally { busy = false; }
  }
  onMount(() => {
    void (async () => {
      try {
        const [catalog, saved, available] = await Promise.all([
          requestJSON<{ projects: StudioProject[] }>('GET', '/api/v1/projects'),
          requestJSON<Defaults>('GET', '/api/v1/project-defaults'),
          requestJSON<{ agents: Agent[]; max_parallel: number }>('GET', '/api/v1/project-options')
        ]);
        projects = catalog.projects; defaults = saved; globalOptions = available.agents; globalLimit = available.max_parallel;
        const params = new URLSearchParams(location.search);
        section = ['basic', 'members', 'execution'].includes(params.get('section') || '') ? params.get('section')! : 'basic';
        if (params.has('defaults')) showDefaults();
        else if (params.has('new')) { await choose(undefined, true); rootInput = params.get('workspace_root') || ''; }
        else if (projects.length) await choose(projects.find(p => p.id === params.get('project_id')) || projects[0], true);
      } catch (cause) { message(cause); }
      finally { loading = false; }
    })();
  });
</script>

<section class="projects-view" aria-label="Project settings">
  <div class="mobile-project-toolbar">
    <label>Project<select aria-label="Project settings selection" value={selectedID} disabled={busy || loading} onchange={async event => { await choose(projects.find(p => p.id === event.currentTarget.value)); event.target && ((event.target as HTMLSelectElement).value = selectedID); }}><option value="" disabled>{defaultsMode ? 'Default subagents' : 'New project'}</option>{#each projects as p}<option value={p.id}>{p.name}</option>{/each}</select></label>
    <div><button class="primary-button" disabled={busy || loading} onclick={() => { section = 'basic'; void choose(); }}><Plus size={16} aria-hidden="true" />New project</button><button class="secondary-button" disabled={busy || loading} onclick={showDefaults}><Users size={16} aria-hidden="true" />Defaults</button></div>
  </div>
  <aside class="project-list" aria-label="Projects">
    <div class="list-heading"><strong>YOUR PROJECTS</strong><span>{projects.length}</span></div>
    <button class="primary-button new-project" onclick={() => { section = 'basic'; void choose(); }} disabled={busy || loading}><Plus size={16} aria-hidden="true" />New project</button>
    <div class="project-entries">
      {#each projects as p}<button class="project-entry" class:active={p.id === selectedID} onclick={() => choose(p)} disabled={busy || loading}><Folder size={18} aria-hidden="true" /><span><strong>{p.name}</strong><small>{p.workspace_roots.length} {p.workspace_roots.length === 1 ? 'directory' : 'directories'}</small></span></button>{/each}
      {#if !projects.length && !loading}<p class="muted">Create a project to group directories and assign subagents.</p>{/if}
    </div>
    <button class="defaults-button" class:active={defaultsMode} onclick={showDefaults} disabled={busy || loading}><Users size={16} aria-hidden="true" /><span>Default subagents<small>For new projects</small></span></button>
  </aside>
  <div class="project-content" aria-busy={loading}>
    {#if error}<div class="project-error" role="alert">{error}</div>{/if}
    {#if status}<p class="project-status" role="status"><Check size={16} aria-hidden="true" />{status}</p>{/if}
    {#if loading}<p class="muted" role="status">Loading project settings…</p>
    {:else if editing}
      <header class="project-heading"><div><span class="eyebrow">{defaultsMode ? 'GLOBAL DEFAULTS' : selectedID ? 'PROJECT SETTINGS' : 'NEW PROJECT'}</span><h2>{defaultsMode ? 'Default subagents' : selectedID ? project?.name : 'Create a project'}</h2></div>{#if project && !defaultsMode}<button class="icon-button" title="Archive project" aria-label="Archive project" onclick={archive} disabled={busy}><Archive size={18} aria-hidden="true" /></button>{/if}</header>
      {#if defaultsMode}<p class="muted">Preselect these subagents when creating a project. Each project keeps its own members. Manage profile definitions in <a href="/settings">Settings</a>.</p>
      {:else}<div class="project-tabs" aria-label="Project sections">{#each [{ id: 'basic', label: 'General', icon: Settings2 }, { id: 'members', label: 'Subagents', icon: Users }, { id: 'execution', label: 'Execution', icon: SlidersHorizontal }] as tab}<button class:active={section === tab.id} aria-pressed={section === tab.id} onclick={() => { section = tab.id; updateURL(); }}><tab.icon size={16} aria-hidden="true" />{tab.label}</button>{/each}</div>{/if}

      {#if !defaultsMode && !selectedID && section !== 'members' && invalidSelection}<p class="project-error">Some default subagents are unavailable. Review the Subagents selection before creating this project.</p>{/if}
      <form class="project-form" onsubmit={event => { event.preventDefault(); void save(); }}>
        {#if !defaultsMode && section === 'basic'}
          <label>Project name<input aria-label="Project name" bind:value={name} required maxlength="128" placeholder="e.g. Product development" /></label>
          <div class="section-heading"><h3>Workspace directories</h3><small>{roots.length}/32</small></div>
          <p class="muted">Sessions share this project’s issues, tickets, and subagents. The first directory is used for initial member bindings.</p>
          <div class="root-list">{#each roots as root, index}<div class="root-row"><Folder size={16} aria-hidden="true" /><code>{root}</code><button type="button" class="icon-button" title={`Remove directory ${root}`} aria-label={`Remove directory ${root}`} onclick={() => removeRoot(index)} disabled={busy}><X size={16} aria-hidden="true" /></button></div>{/each}</div>
          <div class="add-directory"><input aria-label="Project workspace path" bind:value={rootInput} placeholder="Directory path" onkeydown={e => { if (e.key === 'Enter') { e.preventDefault(); void addRoot(); } }} /><button type="button" class="icon-button" title="Browse project directory" aria-label="Browse project directory" onclick={() => browsing = true}><FolderOpen size={18} aria-hidden="true" /></button><button type="button" class="icon-button" title="Add directory" aria-label="Add directory" onclick={addRoot} disabled={roots.length >= 32 || !rootInput.trim()}><Plus size={18} aria-hidden="true" /></button></div>
          {#if !selectedID}<p class="muted">{initialIDs.length} default {initialIDs.length === 1 ? 'subagent selected' : 'subagents selected'}. Review the selection in <button type="button" class="inline-link" onclick={() => section = 'members'}>Subagents</button>.</p>{/if}
          {#if project}<nav class="project-links" aria-label="Project shortcuts">
            <a class="secondary-button" href={`/issues?workspace_id=${selectedID}`}><CircleDot size={16} aria-hidden="true" />Issues</a>
            <a class="secondary-button" href={`/tickets?workspace_id=${selectedID}`}><Kanban size={16} aria-hidden="true" />Tickets</a>
            <a class="secondary-button" href="/sessions"><Layers size={16} aria-hidden="true" />Sessions</a>
          </nav>{/if}
        {/if}
        {#if !defaultsMode && section === 'execution'}
          <h3>Execution limits</h3><p class="muted">0 inherits the global limit of {globalLimit}. Project limits are capped at the global limit. Changes apply to new work; active work continues.</p>
          <div class="limits-grid"><label>Concurrent tickets<input type="number" aria-label="Concurrent tickets" min="0" max="64" step="1" required bind:value={tickets} /><small>Effective: {effective(tickets)} · Q and ACP ticket workers</small></label><label>Concurrent subagents per parent<input type="number" aria-label="Concurrent subagents per parent" min="0" max="64" step="1" required bind:value={subagents} /><small>Effective: {effective(subagents)} · Q delegate calls</small></label></div>
          <p class="help-note">Each Q parent task has its own delegation budget. ACP agents manage their own internal subagents.</p>
        {/if}
        {#if defaultsMode || (section === 'members' && !selectedID)}
          {#if !defaultsMode}<h3>Initial subagents</h3><p class="muted">Choose existing profiles to join when this project is created. Their definition and execution directory will be the first workspace directory.</p>{/if}
          <div class="agent-options">{#each selectionOptions as option}<label class="agent-option" class:unavailable={!option.available}><input type="checkbox" checked={selectedAgents.includes(option.id)} disabled={!option.available && !selectedAgents.includes(option.id)} onchange={event => toggleAgent(option.id, event.currentTarget.checked)} /><span><strong>{option.id}<em>{option.kind === 'external' ? 'ACP' : 'Q'}</em></strong><small>{option.available ? option.description : option.reason}</small></span></label>{/each}{#each missingIDs as id}<label class="agent-option unavailable"><input type="checkbox" checked onchange={() => toggleAgent(id, false)} /><span><strong>{id}</strong><small>Profile is unavailable. Remove it from the selection to save.</small></span></label>{/each}</div>
          {#if invalidSelection}<p class="project-error">Remove unavailable profiles from the selection before saving.</p>{/if}
        {/if}
        {#if defaultsMode || section !== 'members' || !selectedID}<footer class="save-bar"><button class="primary-button" disabled={busy || (!defaultsMode && (!name.trim() || (!roots.length && !rootInput.trim()))) || ((defaultsMode || !selectedID) && invalidSelection)}><Save size={16} aria-hidden="true" />{busy ? 'Saving…' : defaultsMode ? 'Save defaults' : selectedID ? 'Save project' : 'Create project'}</button><small>{defaultsMode ? 'Applies to future projects' : 'Changes apply to this project'}</small></footer>{/if}
      </form>

      {#if !defaultsMode && selectedID && section === 'members'}
        <div class="section-heading"><h3>Joined subagents</h3><span class="count">{members.filter(m => !m.disabled).length} enabled</span></div><p class="muted">System One selects from enabled members for each ticket. Reuse Q or ACP profiles from <a href="/settings">Settings</a>.</p>
        <div class="members-list">{#each members as member}<article class="member-card" class:disabled={member.disabled}><div><strong>{member.agent_id}</strong><span class="member-state">{member.disabled ? 'Disabled' : 'Enabled'}</span><small>Definitions · {member.definition_root}</small><small>Execution · {member.execution_root}</small></div><div class="member-actions"><button class="icon-button" title="Edit member" aria-label="Edit member" onclick={() => resetMember(member)} disabled={busy}><Pencil size={16} aria-hidden="true" /></button><button class="icon-button" title={member.disabled ? 'Enable member' : 'Disable member'} aria-label={member.disabled ? 'Enable member' : 'Disable member'} onclick={() => saveMember(member)} disabled={busy}>{#if member.disabled}<UserCheck size={16} aria-hidden="true" />{:else}<UserMinus size={16} aria-hidden="true" />{/if}</button></div></article>{:else}<p class="empty-members">No joined subagents. Add one below so tickets can be assigned.</p>{/each}</div>
        <form class="member-form" aria-label="Subagent binding" onsubmit={event => { event.preventDefault(); void saveMember(); }}><h3>{memberID ? 'Edit member binding' : 'Join a subagent'}</h3><div class="binding-grid"><label>Definition directory<select aria-label="Definition directory" bind:value={definitionRoot} onchange={loadMemberOptions}>{#each project?.workspace_roots || [] as root}<option value={root}>{root}</option>{/each}{#if definitionRoot && !project?.workspace_roots.includes(definitionRoot)}<option value={definitionRoot}>{definitionRoot} (removed directory)</option>{/if}</select></label><label>Subagent<select aria-label="Join subagent" bind:value={agentID}>{#if agentID && !memberOptions.some(o => o.id === agentID)}<option value={agentID}>{agentID} (unavailable)</option>{/if}{#each memberOptions as option}<option value={option.id} disabled={!option.available}>{option.id} · {option.kind === 'external' ? 'ACP' : 'Q'}{!option.available ? ' (unavailable)' : ''}</option>{/each}</select></label><label>Execution directory<select aria-label="Execution directory" bind:value={executionRoot}>{#each project?.workspace_roots || [] as root}<option value={root}>{root}</option>{/each}{#if executionRoot && !project?.workspace_roots.includes(executionRoot)}<option value={executionRoot}>{executionRoot} (removed directory)</option>{/if}</select></label></div><div class="member-actions"><button class="primary-button" disabled={busy || !memberOptions.some(o => o.id === agentID && o.available)}><Plus size={16} aria-hidden="true" />{memberID ? 'Save member' : 'Join project'}</button>{#if memberID}<button type="button" class="secondary-button" onclick={() => resetMember()} disabled={busy}>Cancel</button>{/if}</div></form>
      {/if}
    {:else}<div class="project-empty"><Folder size={32} aria-hidden="true" /><h2>Your workspace, organized</h2><p>Group directories into a project, choose subagents, and set execution limits.</p><button class="primary-button" onclick={() => choose()}><Plus size={16} aria-hidden="true" />Create project</button></div>{/if}
  </div>
</section>
{#if browsing}<DirectoryBrowser kind="project" onselect={path => { rootInput = path; browsing = false; }} onclose={() => browsing = false} />{/if}

<style>
  .projects-view { display:grid; grid-template-columns:240px minmax(0,1fr); gap:32px; max-width:1240px; margin:0 auto; align-items:start; }
  .mobile-project-toolbar { display:none; }
  .project-list { border:1px solid var(--border); border-radius:12px; background:var(--surface); padding:16px; display:grid; gap:16px; min-width:0; }
  .list-heading,.section-heading,.project-heading,.root-row,.save-bar,.member-actions,.project-status { display:flex; align-items:center; gap:12px; }
  .list-heading,.section-heading,.project-heading { justify-content:space-between; }
  .list-heading { font-size:calc(11px * var(--text-scale)); letter-spacing:.08em; color:var(--muted); }
  .project-entries { display:grid; gap:5px; }
  .project-entry,.defaults-button { display:flex; align-items:center; gap:12px; padding:12px; text-align:left; border:1px solid transparent; border-radius:8px; color:var(--text); background:transparent; font:inherit; cursor:pointer; min-width:0; }
  .project-entry.active,.defaults-button.active { background:var(--violet-soft); border-color:var(--violet); }
  .project-entry:hover,.defaults-button:hover { background:var(--surface-hover, #ffffff08); }
  .project-entry span,.defaults-button span { min-width:0; }
  .project-entry strong { display:block; overflow:hidden; text-overflow:ellipsis; }
  small,.muted,.help-note { color:var(--muted); font-size:calc(13px * var(--text-scale)); line-height:1.6; }
  .project-entry small,.defaults-button small { display:block; font-size:calc(11px * var(--text-scale)); }
  .defaults-button { border-top:1px solid var(--border); border-radius:0 0 8px 8px; }
  .project-content { min-width:0; padding:4px 0 32px; }
  .project-heading { margin-bottom:22px; }
  .eyebrow { color:var(--violet); font-size:calc(11px * var(--text-scale)); letter-spacing:.12em; }
  h2 { font-size:calc(25px * var(--text-scale)); margin:6px 0 0; overflow-wrap:anywhere; }
  h3 { font-size:calc(15px * var(--text-scale)); margin:0; }
  .project-tabs { display:flex; gap:8px; border-bottom:1px solid var(--border); margin-bottom:26px; }
  .project-tabs button { display:flex; align-items:center; gap:8px; color:var(--muted); background:none; border:0; border-bottom:2px solid transparent; padding:10px 12px; font:inherit; font-size:calc(13px * var(--text-scale)); cursor:pointer; }
  .project-tabs button.active { color:var(--violet); border-bottom-color:var(--violet); }
  .project-form { display:grid; gap:16px; }
  label { display:grid; gap:8px; font-size:calc(13px * var(--text-scale)); font-weight:500; min-width:0; }
  input:not([type=checkbox]),select { width:100%; min-width:0; height:auto; min-height:44px; box-sizing:border-box; line-height:1.45; background:var(--surface); border:1px solid var(--border); color:var(--text); border-radius:7px; padding:11px 12px; font:inherit; line-height:1.45; }
  input:focus,select:focus,button:focus-visible,a:focus-visible { outline:2px solid var(--violet); outline-offset:3px; }
  input[type=number] { max-width:160px; }
  .root-list { display:grid; gap:6px; }
  .root-row { border:1px solid var(--border); border-radius:8px; padding:9px 12px; }
  .root-row code { flex:1; font-size:calc(12px * var(--text-scale)); overflow-wrap:anywhere; min-width:0; }
  .root-row :global(svg) { flex-shrink:0; color:var(--muted); }
  .add-directory { display:grid; grid-template-columns:minmax(0,1fr) auto auto; gap:8px; }
  .icon-button { width:38px; height:38px; display:inline-flex; align-items:center; justify-content:center; padding:0; color:var(--muted); border:1px solid var(--border); background:var(--surface); border-radius:7px; flex-shrink:0; cursor:pointer; }
  .icon-button:hover { color:var(--text); border-color:var(--muted); }
  .root-row .icon-button { border:0; background:transparent; width:28px; height:28px; }
  .save-bar { flex-wrap:wrap; border-top:1px solid var(--border); padding-top:20px; margin-top:8px; }
  .primary-button,.secondary-button { display:inline-flex; justify-content:center; align-items:center; gap:8px; white-space:nowrap; }
  .new-project { width:100%; }
  .project-error { color:#f2a5b3; padding:12px 16px; border:1px solid #e85b7440; border-radius:8px; background:#e85b7410; font-size:calc(13px * var(--text-scale)); overflow-wrap:anywhere; }
  .project-status { color:#8edbb4; font-size:calc(13px * var(--text-scale)); margin-bottom:16px; }
  .muted { margin:0; }
  .section-heading { margin-top:8px; min-height:0; padding:0; }
  .limits-grid,.binding-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:20px; }
  .binding-grid label:last-child { grid-column:1/-1; }
  .help-note { border-left:2px solid var(--violet); padding:8px 14px; margin:4px 0; }
  .agent-options { display:grid; gap:8px; }
  .agent-option { display:flex; align-items:flex-start; gap:12px; padding:14px; border:1px solid var(--border); border-radius:9px; cursor:pointer; }
  .agent-option input { margin-top:4px; accent-color:var(--violet); }
  .agent-option strong,.agent-option small { display:block; overflow-wrap:anywhere; }
  .agent-option em { display:inline-block; font-style:normal; margin-left:10px; font-size:calc(10px * var(--text-scale)); color:var(--muted); padding:1px 6px; border:1px solid var(--border); border-radius:4px; }
  .agent-option.unavailable { opacity:.65; }
  .members-list { display:grid; gap:10px; margin:20px 0; }
  .member-card { display:flex; justify-content:space-between; gap:14px; padding:16px; border:1px solid var(--border); border-radius:9px; background:var(--surface); }
  .member-card>div:first-child { min-width:0; }
  .member-card strong { font-size:calc(13px * var(--text-scale)); overflow-wrap:anywhere; }
  .member-card small { display:block; font-size:calc(11px * var(--text-scale)); overflow-wrap:anywhere; }
  .member-state,.count { color:var(--muted); font-size:calc(11px * var(--text-scale)); white-space:nowrap; }
  .member-state { display:inline-block; margin-left:12px; }
  .member-card.disabled { opacity:.65; }
  .member-actions { flex-wrap:wrap; align-self:center; }
  .member-actions button { font-size:calc(12px * var(--text-scale)); }
  .member-form { display:grid; gap:18px; border-top:1px solid var(--border); padding-top:24px; }
  .empty-members { padding:20px; border:1px dashed var(--border); border-radius:8px; color:var(--muted); font-size:calc(13px * var(--text-scale)); }
  .project-links { display:flex; flex-wrap:wrap; gap:8px; padding-top:10px; font-size:calc(13px * var(--text-scale)); }
  .project-links a { min-height:36px; box-sizing:border-box; color:var(--muted); text-decoration:none; }
  .project-links a:hover { color:var(--text); }
  a,.inline-link { color:var(--violet); }
  .inline-link { border:0; padding:0; background:none; font:inherit; cursor:pointer; text-decoration:underline; }
  .project-empty { display:grid; justify-items:center; text-align:center; gap:16px; padding:70px 20px; color:var(--muted); }
  .project-empty h2,.project-empty p { margin:0; }
  @media(max-width:900px) { .projects-view { grid-template-columns:200px minmax(0,1fr); gap:22px; } .member-card { flex-direction:column; } .member-actions { align-self:start; } }
  @media(max-width:680px) { .project-list { display:none; } .mobile-project-toolbar { display:grid; gap:12px; } .mobile-project-toolbar>div { display:flex; gap:8px; } .projects-view { grid-template-columns:minmax(0,1fr); gap:24px; } .project-list { padding:12px; gap:10px; } .project-entries { max-height:180px; overflow-y:auto; } .project-tabs { gap:0; } .project-tabs button { flex:1; padding:10px 8px; font-size:calc(12px * var(--text-scale)); } .limits-grid,.binding-grid { grid-template-columns:minmax(0,1fr); gap:18px; } .save-bar small { width:100%; } h2 { font-size:calc(22px * var(--text-scale)); } }
</style>
