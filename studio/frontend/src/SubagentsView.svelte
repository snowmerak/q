<script lang="ts">
  import { CheckCircle2, FlaskConical, Plus, RefreshCw, Search, Trash2 } from '@lucide/svelte';
  import { onMount } from 'svelte';

  type AgentConnection = { preset?: string; command?: string; args?: string[]; env?: Record<string, string>; auth_method?: string; disabled?: boolean };
  type AgentDefinition = { name: string; description: string; source: string; kind: string; role: string; connection?: string; available: boolean; mutates_workspace: boolean; system_prompt: string; tools: string[]; delegates: string[] };
  type Profile = { version: number; name: string; description?: string; kind: string; role?: string; agent?: string; system_prompt: string; mutates_workspace?: boolean; tools: string[]; delegates: string[] };
  type ProfileEntry = { profile: Profile; scope: string; path: string; revision: string; shadowed: boolean; error?: string; _originalScope?: string };
  type AgentResponse = { workspace_root: string; config_path: string; connections: Record<string, AgentConnection>; bindings: Record<string, string>; external_roles: string[]; native_roles: string[]; builtins: AgentDefinition[]; profiles: ProfileEntry[]; tool_names: string[] };
  type Tab = 'profiles' | 'builtins' | 'connections';

  const tabs: { id: Tab; label: string; detail: string }[] = [
    { id: 'profiles', label: 'Custom profiles', detail: 'Roles, tools, and delegation grants' },
    { id: 'builtins', label: 'Built in', detail: 'Occupational roles shipped with Q' },
    { id: 'connections', label: 'ACP connections', detail: 'External agent processes and bindings' }
  ];

  let tab: Tab = 'profiles';
  let workspaceRoot = '';
  let agents: AgentResponse | null = null;
  let busy = false;
  let message = '';
  let error = '';
  let newProfileName = '';
  let newProfileScope = 'global';
  let newProfileKind = 'inner';
  let newConnectionID = '';
  let saveChain = Promise.resolve();
  let probeStates: Record<string, string> = {};
  let toolFilters: Record<string, string> = {};
  let delegateFilters: Record<string, string> = {};

  function jsonText(value: unknown, fallback: unknown) {
    return JSON.stringify(value ?? fallback);
  }

  function parseJSON<T>(value: string, label: string): T {
    try { return JSON.parse(value) as T; }
    catch (reason) { throw new Error(`${label}: ${reason instanceof Error ? reason.message : 'invalid JSON'}`); }
  }

  async function responseError(response: Response) {
    try { return ((await response.json()) as { error?: string }).error || `Studio returned ${response.status}`; }
    catch { return `Studio returned ${response.status}`; }
  }

  async function requestJSON<T>(method: string, url: string, body?: unknown): Promise<T> {
    const response = await fetch(url, { method, headers: body === undefined ? undefined : { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) throw new Error(await responseError(response));
    return (await response.json()) as T;
  }

  function prepareAgents(value: AgentResponse) {
    value.connections ||= {};
    value.bindings ||= {};
    value.external_roles ||= [];
    value.native_roles ||= [];
    value.builtins = (value.builtins || []).map((definition) => ({
      ...definition,
      tools: definition.tools || [],
      delegates: definition.delegates || []
    }));
    value.profiles = (value.profiles || []).map((entry) => ({ ...entry, profile: { ...entry.profile, tools: entry.profile.tools || [], delegates: entry.profile.delegates || [] }, _originalScope: entry.scope }));
    value.tool_names ||= [];
    for (const connection of Object.values(value.connections)) {
      connection.args ||= [];
      connection.env ||= {};
    }
    return value;
  }

  function rootValue() {
    return workspaceRoot.trim();
  }

  function rememberWorkspace() {
    const root = rootValue();
    if (root) localStorage.setItem('q-studio-workspace-root', root);
    else localStorage.removeItem('q-studio-workspace-root');
    return root;
  }

  async function loadAgents() {
    busy = true;
    error = '';
    message = '';
    try {
      const root = rememberWorkspace();
      const query = root ? `?workspace_root=${encodeURIComponent(root)}` : '';
      agents = prepareAgents(await requestJSON<AgentResponse>('GET', `/api/v1/settings/subagents${query}`));
      if (!root) newProfileScope = 'global';
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Could not load subagent settings';
    } finally {
      busy = false;
    }
  }

  function run(action: () => Promise<void>, success = 'Saved') {
    busy = true;
    error = '';
    message = '';
    saveChain = saveChain.catch(() => undefined).then(action).then(() => { message = success; }).catch((reason) => { error = reason instanceof Error ? reason.message : 'Operation failed'; }).finally(() => { busy = false; });
  }

  function enabledConnections() {
    return Object.entries(agents?.connections || {}).filter(([, connection]) => !connection.disabled);
  }

  function saveConnections() {
    if (!agents) return;
    const connections = structuredClone(agents.connections);
    const bindings = structuredClone(agents.bindings);
    run(async () => {
      agents = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents', { workspace_root: rootValue(), connections, bindings }));
    });
  }

  function addConnection() {
    if (!agents) return;
    const id = newConnectionID.trim();
    if (!id || agents.connections[id]) return;
    agents = {
      ...agents,
      connections: { ...agents.connections, [id]: { preset: 'codex', args: [], env: {} } }
    };
    newConnectionID = '';
    saveConnections();
  }

  function removeConnection(id: string) {
    if (!agents || !confirm(`Delete ACP connection “${id}”?`)) return;
    const connections = { ...agents.connections };
    const bindings = { ...agents.bindings };
    delete connections[id];
    for (const role of agents.external_roles) if (bindings[role] === id) bindings[role] = '';
    agents = { ...agents, connections, bindings };
    saveConnections();
  }

  function setConnectionJSON(connection: AgentConnection, field: 'args' | 'env', value: string) {
    try { (connection as Record<string, unknown>)[field] = parseJSON(value, field); saveConnections(); }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Invalid JSON'; }
  }

  function probeConnection(id: string) {
    const root = rootValue();
    if (!root) {
      error = 'Enter a repository path before testing an ACP connection.';
      return;
    }
    probeStates[id] = 'Testing…';
    probeStates = { ...probeStates };
    run(async () => {
      await requestJSON('POST', '/api/v1/settings/subagents/probe', { workspace_root: root, connection_id: id });
      probeStates[id] = 'Connected';
      probeStates = { ...probeStates };
    }, `${id} completed the ACP lifecycle`);
  }

  function addProfile() {
    if (!agents) return;
    const name = newProfileName.trim();
    if (!name) return;
    const scope = newProfileScope === 'workspace' && rootValue() ? 'workspace' : 'global';
    const kind = newProfileKind === 'external' ? 'external' : 'inner';
    const connection = enabledConnections()[0]?.[0] || '';
    if (kind === 'external' && !connection) {
      error = 'Add and enable an ACP connection before creating an external profile.';
      return;
    }
    const profile: Profile = {
      version: 1,
      name,
      description: '',
      kind,
      role: kind === 'inner' ? agents.native_roles[0] || 'research' : '',
      agent: kind === 'external' ? connection : '',
      system_prompt: 'Complete the assigned request and report verified results.',
      mutates_workspace: false,
      tools: [],
      delegates: []
    };
    newProfileName = '';
    run(async () => {
      agents = prepareAgents(await requestJSON<AgentResponse>('POST', '/api/v1/settings/subagents/profiles', { workspace_root: rootValue(), scope, profile }));
    }, 'Subagent profile created');
  }

  function currentEntry(name: string, preferredScope: string) {
    return agents?.profiles.find((entry) => entry.profile.name === name && entry.scope === preferredScope)
      || agents?.profiles.find((entry) => entry.profile.name === name);
  }

  function saveProfile(entry: ProfileEntry) {
    const desired = structuredClone(entry.profile);
    const desiredScope = entry.scope;
    const knownOriginalScope = entry._originalScope || entry.scope;
    const knownRevision = entry.revision;
    if (desired.kind === 'external') {
      desired.role = '';
      desired.tools = [];
      desired.delegates = [];
    } else {
      desired.agent = '';
      desired.mutates_workspace = false;
    }
    run(async () => {
      const latest = currentEntry(desired.name, desiredScope) || currentEntry(desired.name, knownOriginalScope);
      agents = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents/profiles', {
        workspace_root: rootValue(),
        scope: desiredScope,
        original_scope: latest?._originalScope || knownOriginalScope,
        revision: latest?.revision || knownRevision,
        profile: desired
      }));
    });
  }

  function changeProfileKind(entry: ProfileEntry) {
    if (entry.profile.kind === 'external') {
      const connection = enabledConnections()[0]?.[0];
      if (!connection) {
        entry.profile.kind = 'inner';
        error = 'Add and enable an ACP connection before changing this profile to external.';
        return;
      }
      entry.profile.agent = connection;
      entry.profile.role = '';
      entry.profile.tools = [];
      entry.profile.delegates = [];
    } else {
      entry.profile.role = agents?.native_roles[0] || 'research';
      entry.profile.agent = '';
      entry.profile.mutates_workspace = false;
    }
    saveProfile(entry);
  }

  function deleteProfile(entry: ProfileEntry) {
    if (!confirm(`Delete subagent profile “${entry.profile.name}”?`)) return;
    run(async () => {
      agents = prepareAgents(await requestJSON<AgentResponse>('DELETE', `/api/v1/settings/subagents/profiles/${encodeURIComponent(entry.profile.name)}`, {
        workspace_root: rootValue(), scope: entry.scope, revision: entry.revision, profile: entry.profile
      }));
    }, 'Subagent profile deleted');
  }

  function toggleProfileList(entry: ProfileEntry, field: 'tools' | 'delegates', value: string, checked: boolean) {
    const selected = new Set(entry.profile[field]);
    checked ? selected.add(value) : selected.delete(value);
    entry.profile[field] = [...selected].sort();
    saveProfile(entry);
  }

  function profileKey(entry: ProfileEntry) {
    return `${entry.scope}/${entry.profile.name}`;
  }

  function toolOptions(entry: ProfileEntry) {
    const filter = (toolFilters[profileKey(entry)] || '').trim().toLowerCase();
    const values = [...new Set([...(agents?.tool_names || []), ...entry.profile.tools])].sort();
    return filter ? values.filter((name) => name.toLowerCase().includes(filter)) : values;
  }

  function delegateOptions(entry: ProfileEntry) {
    const ownID = `${entry.scope}/${entry.profile.name}`;
    const filter = (delegateFilters[profileKey(entry)] || '').trim().toLowerCase();
    const builtins = (agents?.builtins || []).map((definition) => ({ id: definition.name, description: definition.description, source: definition.available ? 'built in' : 'built in · unavailable', mutates: definition.mutates_workspace }));
    const custom = (agents?.profiles || []).filter((candidate) => !candidate.error && !candidate.shadowed).map((candidate) => ({
      id: `${candidate.scope}/${candidate.profile.name}`,
      description: candidate.profile.description || 'Custom subagent profile',
      source: candidate.scope,
      mutates: candidate.profile.kind === 'external' ? !!candidate.profile.mutates_workspace : false
    }));
    const selected = entry.profile.delegates.filter((id) => ![...builtins, ...custom].some((candidate) => candidate.id === id)).map((id) => ({ id, description: 'Currently granted profile', source: 'saved', mutates: false }));
    return [...builtins, ...custom, ...selected]
      .filter((candidate) => candidate.id !== ownID && !(entry.scope === 'global' && candidate.source === 'workspace'))
      .filter((candidate) => !filter || `${candidate.id} ${candidate.description}`.toLowerCase().includes(filter))
      .sort((left, right) => left.id.localeCompare(right.id));
  }

  function updateFilter(collection: Record<string, string>, key: string, value: string) {
    collection[key] = value;
    if (collection === toolFilters) toolFilters = { ...collection };
    else delegateFilters = { ...collection };
  }

  onMount(() => {
    const location = new URL(window.location.href);
    workspaceRoot = location.searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
    void loadAgents();
  });
</script>

<section class="settings-section subagent-manager">
  <div class="section-heading">
    <div><p class="eyebrow">SUBAGENTS</p><h2>Delegation profiles</h2><p>Configure occupational roles, delegation grants, and external ACP agents.</p></div>
    <button class="icon-button" title="Reload subagent settings" onclick={loadAgents} disabled={busy}><RefreshCw size={16} class={busy ? 'spin' : undefined} /></button>
  </div>
  <code class="config-path">{agents?.config_path || '~/.q/config.yaml'}</code>

  <div class="workspace-context settings-card">
    <label><span>Repository context · optional</span><input bind:value={workspaceRoot} placeholder="Load repository profiles and test ACP connections" onkeydown={(event) => event.key === 'Enter' && loadAgents()} /></label>
    <button class="text-button" onclick={loadAgents}>{rootValue() ? 'Load repository' : 'Use global only'}</button>
  </div>
  <p class="context-note">Global profiles are always available. A repository path adds its <code>.q/subagents</code> profiles and supplies the working directory for ACP tests.</p>

  <div class="integration-tabs subagent-tabs" role="tablist">
    {#each tabs as item}<button class:active={tab === item.id} onclick={() => tab = item.id}><strong>{item.label}</strong><small>{item.detail}</small></button>{/each}
  </div>
  {#if error}<p class="inline-warning integration-message">{error}</p>{:else if message}<p class="integration-success integration-message"><CheckCircle2 size={14} /> {message}</p>{/if}

  {#if !agents}
    <div class="loading-panel">Loading subagent settings…</div>
  {:else if tab === 'profiles'}
    <div class="subsection-heading"><div><h3>Custom profiles</h3><p>Create reusable inner roles or external ACP delegates. Existing changes save automatically.</p></div><span class="count-badge">{agents.profiles.length} profiles</span></div>
    <div class="settings-card profile-create">
      <label><span>Profile name</span><input bind:value={newProfileName} placeholder="security-reviewer" onkeydown={(event) => event.key === 'Enter' && addProfile()} /></label>
      <label><span>Scope</span><select bind:value={newProfileScope}><option value="global">Global</option><option value="workspace" disabled={!rootValue()}>Repository</option></select></label>
      <label><span>Kind</span><select bind:value={newProfileKind}><option value="inner">Inner</option><option value="external">External ACP</option></select></label>
      <button class="primary-button" onclick={addProfile} disabled={!newProfileName.trim() || (newProfileKind === 'external' && enabledConnections().length === 0)}><Plus size={15} /> Add profile</button>
    </div>

    {#each agents.profiles as entry (entry.path)}
      <article class:error-card={!!entry.error} class:shadowed-card={entry.shadowed} class="settings-card profile-editor subagent-profile">
        <div class="card-heading"><div><h3>{entry.profile.name}</h3><p>{entry.path}</p></div><div class="profile-badges"><span class="source-badge">{entry.scope}</span>{#if entry.shadowed}<span class="source-badge warning-badge">shadowed</span>{/if}<button class="danger-icon" title="Delete profile" onclick={() => deleteProfile(entry)}><Trash2 size={15} /></button></div></div>
        {#if entry.error}<p class="profile-error">{entry.error}</p>{:else}
          <div class="field-grid">
            <label><span>Scope</span><select bind:value={entry.scope} onchange={() => saveProfile(entry)}><option value="global">Global</option><option value="workspace" disabled={!rootValue()}>Repository</option></select></label>
            <label><span>Kind</span><select bind:value={entry.profile.kind} onchange={() => changeProfileKind(entry)}><option value="inner">Inner</option><option value="external">External ACP</option></select></label>
            <label class="wide-field"><span>Description</span><input bind:value={entry.profile.description} onchange={() => saveProfile(entry)} /></label>
            {#if entry.profile.kind === 'external'}
              <label><span>ACP connection</span><select bind:value={entry.profile.agent} onchange={() => saveProfile(entry)}>{#each enabledConnections() as [id]}<option value={id}>{id}</option>{/each}</select></label>
              <label class="toggle-field"><span>Mutation access</span><input type="checkbox" bind:checked={entry.profile.mutates_workspace} onchange={() => saveProfile(entry)} /><small>{entry.profile.mutates_workspace ? 'Mutates workspace' : 'Read only'}</small></label>
            {:else}
              <label><span>Model role</span><select bind:value={entry.profile.role} onchange={() => saveProfile(entry)}>{#each agents.native_roles as role}<option value={role}>{role}</option>{/each}</select></label>
            {/if}
          </div>
          <label class="stacked-field prompt-field"><span>System prompt</span><textarea rows="6" bind:value={entry.profile.system_prompt} onchange={() => saveProfile(entry)}></textarea></label>
          {#if entry.profile.kind === 'inner'}
            <div class="permission-section">
              <div class="permission-heading"><div><strong>Tools</strong><small>Q tools this profile may call</small></div><label class="permission-search"><Search size={13} /><input value={toolFilters[profileKey(entry)] || ''} oninput={(event) => updateFilter(toolFilters, profileKey(entry), event.currentTarget.value)} placeholder="Filter tools" /></label></div>
              <div class="permission-grid tool-permissions">{#each toolOptions(entry) as name}<label class="permission-option"><input type="checkbox" checked={entry.profile.tools.includes(name)} onchange={(event) => toggleProfileList(entry, 'tools', name, event.currentTarget.checked)} /><span><strong>{name}</strong></span></label>{:else}<p>No matching tools.</p>{/each}</div>
            </div>
            <div class="permission-section">
              <div class="permission-heading"><div><strong>Delegates</strong><small>Subagents this profile may call</small></div><label class="permission-search"><Search size={13} /><input value={delegateFilters[profileKey(entry)] || ''} oninput={(event) => updateFilter(delegateFilters, profileKey(entry), event.currentTarget.value)} placeholder="Filter delegates" /></label></div>
              <div class="permission-grid">{#each delegateOptions(entry) as candidate}<label class="permission-option"><input type="checkbox" checked={entry.profile.delegates.includes(candidate.id)} onchange={(event) => toggleProfileList(entry, 'delegates', candidate.id, event.currentTarget.checked)} /><span><strong>{candidate.id}</strong><small>{candidate.description} · {candidate.source}{candidate.mutates ? ' · writes' : ''}</small></span></label>{:else}<p>No matching delegates.</p>{/each}</div>
            </div>
          {/if}
        {/if}
      </article>
    {:else}<div class="empty-card">No custom subagent profiles.</div>{/each}
  {:else if tab === 'builtins'}
    <div class="subsection-heading"><div><h3>Built in profiles</h3><p>These occupational definitions are shipped with Q and cannot be edited here.</p></div><span class="count-badge">{agents.builtins.length} profiles</span></div>
    <div class="agent-definition-grid">{#each agents.builtins as definition}<details class:unavailable-definition={!definition.available} class="settings-card agent-definition"><summary><span><strong>{definition.name}</strong><small>{definition.description}</small></span><span class="source-badge">{definition.role || definition.kind}</span></summary><div class="definition-meta"><span>{definition.kind}</span><span>{definition.mutates_workspace ? 'Mutates workspace' : 'Read only'}</span><span>{definition.available ? definition.connection || 'Available' : 'Unavailable'}</span></div><p>{definition.system_prompt}</p><small><strong>Tools</strong> {definition.tools.join(', ') || 'ACP managed'}</small><small><strong>Delegates</strong> {definition.delegates.join(', ') || 'none'}</small></details>{/each}</div>
  {:else}
    <div class="subsection-heading"><div><h3>External role bindings</h3><p>Assign enabled ACP connections to Q's fixed external roles.</p></div><code>{agents.config_path}</code></div>
    <div class="settings-card binding-grid">{#each agents.external_roles as role}<label><span>{role}</span><select bind:value={agents.bindings[role]} onchange={saveConnections}><option value="">Unassigned</option>{#each enabledConnections() as [id]}<option value={id}>{id}</option>{/each}</select></label>{/each}</div>

    <div class="subsection-heading"><div><h3>ACP connections</h3><p>Preset or custom stdio processes used by external roles and profiles.</p></div></div>
    <div class="inline-create settings-card"><label><span>Connection ID</span><input bind:value={newConnectionID} placeholder="codex-local" onkeydown={(event) => event.key === 'Enter' && addConnection()} /></label><button class="primary-button" onclick={addConnection} disabled={!newConnectionID.trim()}><Plus size={15} /> Add connection</button></div>
    {#each Object.entries(agents.connections) as [id, connection]}
      <article class="settings-card integration-editor"><div class="card-heading"><div><h3>{id}</h3><p>{connection.preset ? `preset ${connection.preset}` : connection.command || 'custom command'}</p></div><div class="provider-actions"><button class="text-button" title={rootValue() ? 'Run ACP initialize and shutdown' : 'Enter a repository path before testing'} onclick={() => probeConnection(id)} disabled={!rootValue()}><FlaskConical size={14} /> {probeStates[id] || 'Test'}</button><button class="danger-icon" title="Delete ACP connection" onclick={() => removeConnection(id)}><Trash2 size={15} /></button></div></div>
        <div class="field-grid"><label><span>Preset</span><select bind:value={connection.preset} onchange={() => { if (connection.preset) connection.command = ''; saveConnections(); }}><option value="">Custom command</option><option value="codex">Codex</option><option value="grok">Grok</option></select></label><label><span>Command</span><input bind:value={connection.command} onchange={() => { if (connection.command) connection.preset = ''; saveConnections(); }} disabled={!!connection.preset} /></label><label><span>Arguments · JSON</span><input value={jsonText(connection.args, [])} onchange={(event) => setConnectionJSON(connection, 'args', event.currentTarget.value)} /></label><label><span>Child environment · JSON</span><input value={jsonText(connection.env, {})} onchange={(event) => setConnectionJSON(connection, 'env', event.currentTarget.value)} /></label><label><span>ACP auth method</span><input bind:value={connection.auth_method} onchange={saveConnections} /></label><label class="toggle-field"><span>Connection state</span><input type="checkbox" checked={!connection.disabled} onchange={(event) => { connection.disabled = !event.currentTarget.checked; saveConnections(); }} /><small>{connection.disabled ? 'Disabled' : 'Enabled'}</small></label></div>
      </article>
    {:else}<div class="empty-card">No ACP connections configured.</div>{/each}
  {/if}
</section>
