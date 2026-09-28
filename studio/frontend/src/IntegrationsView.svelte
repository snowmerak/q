<script lang="ts">
  import { CheckCircle2, FlaskConical, Plus, RefreshCw, Search, Trash2 } from '@lucide/svelte';
  import { onMount } from 'svelte';

  type MCPServer = { transport: 'stdio' | 'streamable-http'; command?: string; args?: string[]; env?: Record<string, string>; url?: string; headers?: Record<string, string> };
  type MCPResponse = { config_path: string; config: { version: number; servers: Record<string, MCPServer>; roles: Record<string, string[]> }; roles: string[] };
  type LSPServer = { languages: string[]; command: string; args?: string[]; disabled?: boolean };
  type LSPRoot = { path: string; language: string; server?: string; source?: string; disabled?: boolean };
  type LSPResponse = { config_path: string; workspace_path: string; workspace_root: string; global: { servers: Record<string, LSPServer>; languages: Record<string, string> }; workspace: { version: number; roots: LSPRoot[] } };
  type DiscoveredServer = { id: string; path: string; config: LSPServer };
  type Skill = { id: string; name: string; description: string; directory: string; source: string; scope: string; tags?: string[]; git_commit?: string; active: boolean; managed: boolean };
  type SkillResponse = { workspace_root: string; skills: Skill[]; issues: { path: string; message: string }[] };
  type AgentConnection = { preset?: string; command?: string; args?: string[]; env?: Record<string, string>; auth_method?: string; disabled?: boolean };
  type AgentDefinition = { name: string; description: string; kind: string; role: string; mutates_workspace: boolean; system_prompt: string; tools: string[]; delegates: string[] };
  type Profile = { version: number; name: string; description?: string; kind: string; role?: string; agent?: string; system_prompt: string; mutates_workspace?: boolean; tools: string[]; delegates: string[] };
  type ProfileEntry = { profile: Profile; scope: string; path: string; revision: string; shadowed: boolean; error?: string; _originalScope?: string };
  type AgentResponse = { workspace_root: string; config_path: string; connections: Record<string, AgentConnection>; bindings: Record<string, string>; external_roles: string[]; native_roles: string[]; builtins: AgentDefinition[]; profiles: ProfileEntry[]; tool_names: string[] };
  type IgnoreResponse = { workspace_root: string; path: string; content: string; revision: string };
  type Panel = 'mcp' | 'lsp' | 'skills' | 'agents' | 'ignore';

  const panels: { id: Panel; label: string; detail: string }[] = [
    { id: 'mcp', label: 'MCP', detail: 'External tool servers and role grants' },
    { id: 'lsp', label: 'Language servers', detail: 'Profiles, defaults, and project roots' },
    { id: 'skills', label: 'Skills', detail: 'Global and repository skill indexes' },
    { id: 'agents', label: 'Agents', detail: 'Profiles, delegation, and ACP connections' },
    { id: 'ignore', label: '.qignore', detail: 'Repository discovery exclusions' }
  ];

  let panel: Panel = 'mcp';
  let workspaceRoot = '';
  let busy = false;
  let message = '';
  let error = '';
  let mcp: MCPResponse | null = null;
  let lsp: LSPResponse | null = null;
  let skills: SkillResponse | null = null;
  let agents: AgentResponse | null = null;
  let ignore: IgnoreResponse | null = null;
  let skillRepository = '';
  let skillScope = 'global';
  let newConnectionID = '';
  let newProfileName = '';
  let newMCPID = '';
  let newLSPID = '';
  let saveChain = Promise.resolve();
  let ignoreTimer: ReturnType<typeof setTimeout> | undefined;
  let probeStates: Record<string, string> = {};

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

  function run(action: () => Promise<void>, success = 'Saved') {
    busy = true; error = ''; message = '';
    saveChain = saveChain.then(action).then(() => { message = success; }).catch((reason) => { error = reason instanceof Error ? reason.message : 'Operation failed'; }).finally(() => { busy = false; });
  }

  function requireWorkspace() {
    const root = workspaceRoot.trim();
    if (!root) throw new Error('Open a repository in Sessions or enter its path here.');
    localStorage.setItem('q-studio-workspace-root', root);
    return root;
  }

  async function loadPanel() {
    error = ''; message = ''; busy = true;
    try {
      if (panel === 'mcp') mcp = await requestJSON<MCPResponse>('GET', '/api/v1/integrations/mcp');
      else {
        const root = requireWorkspace();
        const query = `?workspace_root=${encodeURIComponent(root)}`;
        if (panel === 'lsp') lsp = await requestJSON<LSPResponse>('GET', `/api/v1/workspaces/lsp${query}`);
        if (panel === 'skills') skills = await requestJSON<SkillResponse>('GET', `/api/v1/workspaces/skills${query}`);
        if (panel === 'agents') agents = prepareAgents(await requestJSON<AgentResponse>('GET', `/api/v1/workspaces/agents${query}`));
        if (panel === 'ignore') ignore = await requestJSON<IgnoreResponse>('GET', `/api/v1/workspaces/ignore${query}`);
      }
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Could not load integration settings'; }
    finally { busy = false; }
  }

  function choosePanel(next: Panel) { panel = next; void loadPanel(); }

  function prepareAgents(value: AgentResponse) {
    value.connections ||= {};
    value.bindings ||= {};
    value.profiles = (value.profiles || []).map((entry) => ({ ...entry, _originalScope: entry.scope }));
    return value;
  }

  function saveMCP() {
    if (!mcp) return;
    const payload = structuredClone(mcp.config);
    run(async () => { await requestJSON<MCPResponse>('PUT', '/api/v1/integrations/mcp', payload); });
  }

  function addMCPServer() {
    if (!mcp) return;
    const id = newMCPID.trim();
    if (!id || mcp.config.servers[id]) return;
    mcp.config.servers[id] = { transport: 'stdio', command: 'npx', args: [], env: {} };
    newMCPID = ''; mcp = { ...mcp }; saveMCP();
  }

  function removeMCPServer(id: string) {
    if (!mcp || !confirm(`Delete MCP server “${id}”?`)) return;
    delete mcp.config.servers[id];
    for (const role of Object.keys(mcp.config.roles)) mcp.config.roles[role] = mcp.config.roles[role].filter((server) => server !== id);
    mcp = { ...mcp }; saveMCP();
  }

  function changeMCPTransport(server: MCPServer) {
    if (server.transport === 'stdio') { server.command ||= 'npx'; server.args ||= []; server.env ||= {}; delete server.url; delete server.headers; }
    else { server.url ||= 'https://example.com/mcp'; server.headers ||= {}; delete server.command; delete server.args; delete server.env; }
    saveMCP();
  }

  function setMCPJSON(server: MCPServer, field: 'args' | 'env' | 'headers', value: string) {
    try { (server as Record<string, unknown>)[field] = parseJSON(value, field); saveMCP(); }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Invalid JSON'; }
  }

  function toggleMCPRole(role: string, id: string, checked: boolean) {
    if (!mcp) return;
    const values = new Set(mcp.config.roles[role] || []);
    checked ? values.add(id) : values.delete(id);
    mcp.config.roles[role] = [...values]; saveMCP();
  }

  function saveLSP() {
    if (!lsp) return;
    const global = structuredClone(lsp.global);
    const workspace = structuredClone(lsp.workspace);
    run(async () => { await requestJSON<LSPResponse>('PUT', '/api/v1/workspaces/lsp', { workspace_root: requireWorkspace(), global, workspace }); });
  }

  function addLSPServer() {
    if (!lsp) return;
    const id = newLSPID.trim();
    if (!id || lsp.global.servers[id]) return;
    lsp.global.servers[id] = { languages: ['go'], command: id, args: [] };
    newLSPID = ''; lsp = { ...lsp }; saveLSP();
  }

  function removeLSPServer(id: string) {
    if (!lsp || !confirm(`Delete language server “${id}”?`)) return;
    delete lsp.global.servers[id];
    for (const language of Object.keys(lsp.global.languages)) if (lsp.global.languages[language] === id) delete lsp.global.languages[language];
    for (const root of lsp.workspace.roots) if (root.server === id) root.server = '';
    lsp = { ...lsp }; saveLSP();
  }

  function setLSPArgs(server: LSPServer, value: string) {
    try { server.args = parseJSON<string[]>(value, 'Arguments'); saveLSP(); }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Invalid arguments'; }
  }

  function setLSPLanguages(server: LSPServer, value: string) {
    server.languages = [...new Set(value.split(',').map((item) => item.trim().toLowerCase()).filter(Boolean))]; saveLSP();
  }

  function allLanguages() {
    if (!lsp) return [];
    return [...new Set(Object.values(lsp.global.servers).flatMap((server) => server.languages))].sort();
  }

  function addLSPRoot(root: LSPRoot = { path: '.', language: 'go', source: 'manual' }) {
    if (!lsp) return;
    lsp.workspace.roots = [...lsp.workspace.roots, root]; saveLSP();
  }

  function removeLSPRoot(index: number) {
    if (!lsp) return;
    lsp.workspace.roots = lsp.workspace.roots.filter((_, candidate) => candidate !== index); saveLSP();
  }

  function discoverLSP() {
    if (!lsp) return;
    run(async () => {
      const discovered = await requestJSON<{ roots: LSPRoot[]; servers: DiscoveredServer[] }>('POST', '/api/v1/workspaces/lsp/discover', { workspace_root: requireWorkspace() });
      for (const server of discovered.servers || []) if (!lsp!.global.servers[server.id]) lsp!.global.servers[server.id] = server.config;
      const keys = new Set(lsp!.workspace.roots.map((root) => `${root.path}\0${root.language}`));
      for (const root of discovered.roots || []) if (!keys.has(`${root.path}\0${root.language}`)) lsp!.workspace.roots.push(root);
      lsp = { ...lsp! };
      lsp = await requestJSON<LSPResponse>('PUT', '/api/v1/workspaces/lsp', { workspace_root: requireWorkspace(), global: lsp!.global, workspace: lsp!.workspace });
    }, 'Discovery merged and saved');
  }

  function installSkill() {
    const repository = skillRepository.trim();
    if (!repository) return;
    run(async () => { skills = await requestJSON<SkillResponse>('POST', '/api/v1/workspaces/skills', { workspace_root: requireWorkspace(), scope: skillScope, repository }); skillRepository = ''; }, 'Skill installed and indexed');
  }

  function updateSkill(skill: Skill) {
    run(async () => { skills = await requestJSON<SkillResponse>('POST', `/api/v1/workspaces/skills/${encodeURIComponent(skill.id)}`, { workspace_root: requireWorkspace() }); }, 'Skill updated and indexed');
  }

  function removeSkill(skill: Skill) {
    if (!confirm(`Delete managed skill “${skill.name}”?`)) return;
    run(async () => { skills = await requestJSON<SkillResponse>('DELETE', `/api/v1/workspaces/skills/${encodeURIComponent(skill.id)}`, { workspace_root: requireWorkspace() }); }, 'Skill removed and index updated');
  }

  function reindexSkills() {
    run(async () => { skills = await requestJSON<SkillResponse>('POST', '/api/v1/workspaces/skills/reindex', { workspace_root: requireWorkspace() }); }, 'Skill indexes rebuilt');
  }

  function saveAgents() {
    if (!agents) return;
    const connections = structuredClone(agents.connections);
    const bindings = structuredClone(agents.bindings);
    run(async () => { await requestJSON<AgentResponse>('PUT', '/api/v1/workspaces/agents', { workspace_root: requireWorkspace(), connections, bindings }); });
  }

  function addConnection() {
    if (!agents) return;
    const id = newConnectionID.trim();
    if (!id || agents.connections[id]) return;
    agents.connections[id] = { preset: 'codex', args: [], env: {} };
    newConnectionID = ''; agents = { ...agents }; saveAgents();
  }

  function removeConnection(id: string) {
    if (!agents || !confirm(`Delete ACP connection “${id}”?`)) return;
    delete agents.connections[id];
    for (const role of agents.external_roles) if (agents.bindings[role] === id) agents.bindings[role] = '';
    agents = { ...agents }; saveAgents();
  }

  function setConnectionJSON(connection: AgentConnection, field: 'args' | 'env', value: string) {
    try { (connection as Record<string, unknown>)[field] = parseJSON(value, field); saveAgents(); }
    catch (reason) { error = reason instanceof Error ? reason.message : 'Invalid JSON'; }
  }

  function probeConnection(id: string) {
    probeStates[id] = 'Testing…'; probeStates = { ...probeStates };
    run(async () => {
      await requestJSON('POST', '/api/v1/workspaces/agents/probe', { workspace_root: requireWorkspace(), connection_id: id });
      probeStates[id] = 'Connected'; probeStates = { ...probeStates };
    }, `${id} completed the ACP lifecycle`);
  }

  function addProfile() {
    if (!agents) return;
    const name = newProfileName.trim();
    if (!name) return;
    const entry: ProfileEntry = { scope: 'workspace', _originalScope: 'workspace', path: '', revision: '', shadowed: false, profile: { version: 1, name, description: '', kind: 'inner', role: agents.native_roles[0] || 'research', system_prompt: 'Complete the assigned request and report verified results.', tools: [], delegates: [] } };
    newProfileName = '';
    run(async () => { agents = prepareAgents(await requestJSON<AgentResponse>('POST', '/api/v1/workspaces/agents/profiles', { workspace_root: requireWorkspace(), scope: entry.scope, profile: entry.profile })); }, 'Subagent profile created');
  }

  function saveProfile(entry: ProfileEntry) {
    if (entry.profile.kind === 'external') { entry.profile.role = ''; entry.profile.tools = []; entry.profile.delegates = []; }
    else { entry.profile.agent = ''; entry.profile.mutates_workspace = false; }
    run(async () => { agents = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/workspaces/agents/profiles', { workspace_root: requireWorkspace(), scope: entry.scope, original_scope: entry._originalScope, revision: entry.revision, profile: entry.profile })); });
  }

  function deleteProfile(entry: ProfileEntry) {
    if (!confirm(`Delete subagent profile “${entry.profile.name}”?`)) return;
    run(async () => { agents = prepareAgents(await requestJSON<AgentResponse>('DELETE', `/api/v1/workspaces/agents/profiles/${encodeURIComponent(entry.profile.name)}`, { workspace_root: requireWorkspace(), scope: entry.scope, revision: entry.revision, profile: entry.profile })); }, 'Subagent profile deleted');
  }

  function setProfileList(entry: ProfileEntry, field: 'tools' | 'delegates', value: string) {
    entry.profile[field] = [...new Set(value.split(',').map((item) => item.trim()).filter(Boolean))]; saveProfile(entry);
  }

  function queueIgnoreSave() {
    if (ignoreTimer) clearTimeout(ignoreTimer);
    ignoreTimer = setTimeout(() => {
      if (!ignore) return;
      const content = ignore.content;
      run(async () => {
        if (!ignore) return;
        const result = await requestJSON<IgnoreResponse>('PUT', '/api/v1/workspaces/ignore', { workspace_root: requireWorkspace(), content, revision: ignore.revision });
        ignore = ignore.content === content ? result : { ...ignore, revision: result.revision, path: result.path };
      }, '.qignore saved');
    }, 600);
  }

  onMount(() => {
    const location = new URL(window.location.href);
    workspaceRoot = location.searchParams.get('workspace_root') || localStorage.getItem('q-studio-workspace-root') || '';
    const requestedPanel = location.searchParams.get('panel') as Panel | null;
    if (requestedPanel && panels.some((candidate) => candidate.id === requestedPanel)) panel = requestedPanel;
    void loadPanel();
  });
</script>

<section class="settings-section integration-manager">
  <div class="section-heading">
    <div><p class="eyebrow">INTEGRATIONS AND AGENTS</p><h2>Runtime connections</h2><p>Migrate tool, language, skill, agent, and discovery controls from the TUI.</p></div>
    <button class="icon-button" title="Reload current panel" onclick={loadPanel} disabled={busy}><RefreshCw size={16} class={busy ? 'spin' : undefined} /></button>
  </div>
  <div class="workspace-context settings-card">
    <label><span>Repository path</span><input bind:value={workspaceRoot} placeholder="Open a repository in Sessions" /></label>
    <button class="text-button" onclick={loadPanel}>Load repository</button>
  </div>
  <div class="integration-tabs" role="tablist">
    {#each panels as item}<button class:active={panel === item.id} onclick={() => choosePanel(item.id)}><strong>{item.label}</strong><small>{item.detail}</small></button>{/each}
  </div>
  {#if error}<p class="inline-warning integration-message">{error}</p>{:else if message}<p class="integration-success integration-message"><CheckCircle2 size={14} /> {message}</p>{/if}

  {#if panel === 'mcp'}
    {#if mcp}
      <div class="subsection-heading"><div><h3>MCP servers</h3><p>Credentials stay in environment variables. Role grants apply on the next session turn.</p></div><code>{mcp.config_path}</code></div>
      <div class="inline-create settings-card"><label><span>Server ID</span><input bind:value={newMCPID} placeholder="docs" /></label><button class="primary-button" onclick={addMCPServer} disabled={!newMCPID.trim()}><Plus size={15} /> Add server</button></div>
      {#each Object.entries(mcp.config.servers) as [id, server]}
        <article class="settings-card integration-editor">
          <div class="card-heading"><div><h3>{id}</h3><p>{server.transport}</p></div><button class="danger-icon" title="Delete MCP server" onclick={() => removeMCPServer(id)}><Trash2 size={15} /></button></div>
          <div class="field-grid"><label><span>Transport</span><select bind:value={server.transport} onchange={() => changeMCPTransport(server)}><option value="stdio">stdio</option><option value="streamable-http">Streamable HTTP</option></select></label>
            {#if server.transport === 'stdio'}<label><span>Command</span><input bind:value={server.command} onchange={saveMCP} /></label><label><span>Arguments · JSON</span><input value={jsonText(server.args, [])} onchange={(event) => setMCPJSON(server, 'args', event.currentTarget.value)} /></label><label><span>Child env mapping · JSON</span><input value={jsonText(server.env, {})} onchange={(event) => setMCPJSON(server, 'env', event.currentTarget.value)} /></label>
            {:else}<label class="wide-field"><span>URL</span><input bind:value={server.url} onchange={saveMCP} /></label><label class="wide-field"><span>Header to env mapping · JSON</span><input value={jsonText(server.headers, {})} onchange={(event) => setMCPJSON(server, 'headers', event.currentTarget.value)} /></label>{/if}
          </div>
          <div class="grant-grid">{#each mcp.roles as role}<label class="compact-toggle"><input type="checkbox" checked={(mcp.config.roles[role] || []).includes(id)} onchange={(event) => toggleMCPRole(role, id, event.currentTarget.checked)} /><span>{role}</span></label>{/each}</div>
        </article>
      {:else}<div class="empty-card">No MCP servers configured.</div>{/each}
    {:else}<div class="loading-panel">Loading MCP settings…</div>{/if}
  {:else if panel === 'lsp'}
    {#if lsp}
      <div class="subsection-heading"><div><h3>Language server profiles</h3><p>Commands are trusted global settings; repository roots only select profiles.</p></div><button class="primary-button" onclick={discoverLSP}><Search size={15} /> Discover</button></div>
      <div class="inline-create settings-card"><label><span>Profile ID</span><input bind:value={newLSPID} placeholder="gopls" /></label><button class="primary-button" onclick={addLSPServer} disabled={!newLSPID.trim()}><Plus size={15} /> Add profile</button></div>
      {#each Object.entries(lsp.global.servers) as [id, server]}
        <article class="settings-card integration-editor"><div class="card-heading"><div><h3>{id}</h3><p>{server.languages.join(', ')}</p></div><div class="provider-actions"><label class="compact-toggle"><input type="checkbox" bind:checked={server.disabled} onchange={saveLSP} /><span>{server.disabled ? 'Disabled' : 'Enabled'}</span></label><button class="danger-icon" onclick={() => removeLSPServer(id)}><Trash2 size={15} /></button></div></div>
          <div class="field-grid"><label><span>Languages</span><input value={server.languages.join(', ')} onchange={(event) => setLSPLanguages(server, event.currentTarget.value)} /></label><label><span>Command</span><input bind:value={server.command} onchange={saveLSP} /></label><label class="wide-field"><span>Arguments · JSON</span><input value={jsonText(server.args, [])} onchange={(event) => setLSPArgs(server, event.currentTarget.value)} /></label></div>
          <div class="grant-grid">{#each server.languages as language}<label><span>{language} default</span><select bind:value={lsp.global.languages[language]} onchange={saveLSP}><option value="">No default</option>{#each Object.entries(lsp.global.servers).filter(([, candidate]) => candidate.languages.includes(language)) as [candidateID]}<option value={candidateID}>{candidateID}</option>{/each}</select></label>{/each}</div>
        </article>
      {/each}
      <div class="subsection-heading"><div><h3>Repository roots</h3><p>{lsp.workspace_path}</p></div><button class="text-button" onclick={() => addLSPRoot()}><Plus size={14} /> Add root</button></div>
      <div class="settings-card compact-list">{#each lsp.workspace.roots as root, index}<div class="root-row"><input bind:value={root.path} onchange={saveLSP} aria-label="Root path" /><select bind:value={root.language} onchange={saveLSP}>{#each allLanguages() as language}<option value={language}>{language}</option>{/each}</select><select bind:value={root.server} onchange={saveLSP}><option value="">Language default</option>{#each Object.entries(lsp.global.servers).filter(([, server]) => server.languages.includes(root.language)) as [id]}<option value={id}>{id}</option>{/each}</select><label class="compact-toggle"><input type="checkbox" bind:checked={root.disabled} onchange={saveLSP} /><span>Disabled</span></label><span class="source-badge">{root.source || 'manual'}</span><button class="danger-icon" onclick={() => removeLSPRoot(index)}><Trash2 size={14} /></button></div>{:else}<div class="api-key-empty">No repository roots configured.</div>{/each}</div>
    {:else}<div class="loading-panel">Loading language server settings…</div>{/if}
  {:else if panel === 'skills'}
    {#if skills}
      <div class="subsection-heading"><div><h3>Agent Skills</h3><p>Portable entries are read only. Q managed Git checkouts can be pulled or removed.</p></div><button class="primary-button" onclick={reindexSkills}><RefreshCw size={15} /> Reindex all</button></div>
      <div class="inline-create settings-card skill-installer"><label><span>Scope</span><select bind:value={skillScope}><option value="global">Global</option><option value="workspace">Repository</option></select></label><label class="wide-field"><span>Git repository</span><input bind:value={skillRepository} placeholder="https://github.com/owner/skill.git" /></label><button class="primary-button" onclick={installSkill} disabled={!skillRepository.trim()}>Clone and index</button></div>
      {#each ['global', 'project'] as scope}<div class="subsection-heading"><div><h3>{scope === 'global' ? 'Global' : 'Repository'} skills</h3><p>{skills.skills.filter((skill) => skill.scope === scope).length} discovered</p></div></div><div class="skill-grid">{#each skills.skills.filter((skill) => skill.scope === scope) as skill}<article class:shadowed={!skill.active} class="settings-card skill-card"><div class="card-heading"><div><h3>{skill.name}</h3><p>{skill.description || 'No description'}</p></div><span class="source-badge">{skill.managed ? 'Git managed' : skill.source}</span></div><small>{skill.directory}</small>{#if skill.tags?.length}<div class="tag-list">{#each skill.tags as tag}<span>{tag}</span>{/each}</div>{/if}<div class="card-actions">{#if skill.managed}<button class="text-button" onclick={() => updateSkill(skill)}>Pull</button><button class="text-button danger-text" onclick={() => removeSkill(skill)}>Delete</button>{:else}<span>Read only</span>{/if}{#if !skill.active}<span>Shadowed</span>{/if}</div></article>{/each}</div>{/each}
      {#if skills.issues.length}<div class="settings-card issue-list"><h3>Discovery notes</h3>{#each skills.issues as issue}<p><code>{issue.path}</code> {issue.message}</p>{/each}</div>{/if}
    {:else}<div class="loading-panel">Loading Agent Skills…</div>{/if}
  {:else if panel === 'agents'}
    {#if agents}
      <div class="subsection-heading"><div><h3>ACP connections</h3><p>Preset or custom stdio processes used by external roles and profiles.</p></div><code>{agents.config_path}</code></div>
      <div class="inline-create settings-card"><label><span>Connection ID</span><input bind:value={newConnectionID} placeholder="codex-local" /></label><button class="primary-button" onclick={addConnection} disabled={!newConnectionID.trim()}><Plus size={15} /> Add connection</button></div>
      {#each Object.entries(agents.connections) as [id, connection]}
        <article class="settings-card integration-editor"><div class="card-heading"><div><h3>{id}</h3><p>{connection.preset ? `preset ${connection.preset}` : connection.command}</p></div><div class="provider-actions"><button class="text-button" onclick={() => probeConnection(id)}><FlaskConical size={14} /> {probeStates[id] || 'Test'}</button><button class="danger-icon" onclick={() => removeConnection(id)}><Trash2 size={15} /></button></div></div>
          <div class="field-grid"><label><span>Preset</span><select bind:value={connection.preset} onchange={() => { if (connection.preset) connection.command = ''; saveAgents(); }}><option value="">Custom command</option><option value="codex">Codex</option><option value="grok">Grok</option></select></label><label><span>Command</span><input bind:value={connection.command} onchange={() => { if (connection.command) connection.preset = ''; saveAgents(); }} disabled={!!connection.preset} /></label><label><span>Arguments · JSON</span><input value={jsonText(connection.args, [])} onchange={(event) => setConnectionJSON(connection, 'args', event.currentTarget.value)} /></label><label><span>Child environment · JSON</span><input value={jsonText(connection.env, {})} onchange={(event) => setConnectionJSON(connection, 'env', event.currentTarget.value)} /></label><label><span>ACP auth method</span><input bind:value={connection.auth_method} onchange={saveAgents} /></label><label class="toggle-field"><span>Connection state</span><input type="checkbox" bind:checked={connection.disabled} onchange={saveAgents} /><small>{connection.disabled ? 'Disabled' : 'Enabled'}</small></label></div>
        </article>
      {/each}
      <div class="subsection-heading"><div><h3>External role bindings</h3><p>Choose one enabled ACP connection for each special role.</p></div></div>
      <div class="settings-card binding-grid">{#each agents.external_roles as role}<label><span>{role}</span><select bind:value={agents.bindings[role]} onchange={saveAgents}><option value="">Unassigned</option>{#each Object.entries(agents.connections).filter(([, connection]) => !connection.disabled) as [id]}<option value={id}>{id}</option>{/each}</select></label>{/each}</div>
      <div class="subsection-heading"><div><h3>Built in profiles</h3><p>Read only occupational roles shipped with Q.</p></div></div>
      <div class="agent-definition-grid">{#each agents.builtins as definition}<details class="settings-card agent-definition"><summary><span><strong>{definition.name}</strong><small>{definition.description}</small></span><span class="source-badge">{definition.role || definition.kind}</span></summary><p>{definition.system_prompt}</p><small>Tools: {definition.tools.join(', ') || 'ACP managed'}</small><small>Delegates: {definition.delegates.join(', ') || 'none'}</small></details>{/each}</div>
      <div class="subsection-heading"><div><h3>Custom profiles</h3><p>Define inner model roles or external ACP delegates. Delegation grants use canonical IDs.</p></div></div>
      <div class="inline-create settings-card"><label><span>Profile name</span><input bind:value={newProfileName} placeholder="security-reviewer" /></label><button class="primary-button" onclick={addProfile} disabled={!newProfileName.trim()}><Plus size={15} /> Add profile</button></div>
      {#each agents.profiles as entry}
        <article class:error-card={!!entry.error} class="settings-card profile-editor"><div class="card-heading"><div><h3>{entry.profile.name}</h3><p>{entry.path}</p></div><button class="danger-icon" onclick={() => deleteProfile(entry)}><Trash2 size={15} /></button></div>{#if entry.error}<p class="inline-warning">{entry.error}</p>{/if}
          <div class="field-grid"><label><span>Scope</span><select bind:value={entry.scope} onchange={() => saveProfile(entry)}><option value="global">Global</option><option value="workspace">Repository</option></select></label><label><span>Kind</span><select bind:value={entry.profile.kind} onchange={() => saveProfile(entry)}><option value="inner">Inner</option><option value="external">External ACP</option></select></label><label class="wide-field"><span>Description</span><input bind:value={entry.profile.description} onchange={() => saveProfile(entry)} /></label>
            {#if entry.profile.kind === 'external'}<label><span>ACP connection</span><select bind:value={entry.profile.agent} onchange={() => saveProfile(entry)}>{#each Object.entries(agents.connections).filter(([, connection]) => !connection.disabled) as [id]}<option value={id}>{id}</option>{/each}</select></label><label class="toggle-field"><span>Mutation access</span><input type="checkbox" bind:checked={entry.profile.mutates_workspace} onchange={() => saveProfile(entry)} /><small>{entry.profile.mutates_workspace ? 'Mutates workspace' : 'Read only'}</small></label>
            {:else}<label><span>Model role</span><select bind:value={entry.profile.role} onchange={() => saveProfile(entry)}>{#each agents.native_roles as role}<option value={role}>{role}</option>{/each}</select></label><label class="wide-field"><span>Tools · comma separated</span><input value={entry.profile.tools.join(', ')} list="studio-agent-tools" onchange={(event) => setProfileList(entry, 'tools', event.currentTarget.value)} /></label><label class="wide-field"><span>Delegates · comma separated</span><input value={entry.profile.delegates.join(', ')} onchange={(event) => setProfileList(entry, 'delegates', event.currentTarget.value)} /></label>{/if}
          </div><label class="stacked-field"><span>System prompt</span><textarea rows="5" bind:value={entry.profile.system_prompt} onchange={() => saveProfile(entry)}></textarea></label>
        </article>
      {:else}<div class="empty-card">No custom subagent profiles.</div>{/each}
      <datalist id="studio-agent-tools">{#each agents.tool_names as name}<option value={name}></option>{/each}</datalist>
    {:else}<div class="loading-panel">Loading agent settings…</div>{/if}
  {:else}
    {#if ignore}
      <div class="subsection-heading"><div><h3>Discovery rules</h3><p>Changes save after a short pause and affect new listings and LSP discovery.</p></div><code>{ignore.path}</code></div>
      <div class="settings-card ignore-editor"><p><code>#</code> comment · <code>!</code> include · <code>/</code> root · trailing <code>/</code> directory · <code>* ** ?</code> wildcards</p><textarea bind:value={ignore.content} oninput={queueIgnoreSave} spellcheck="false" aria-label=".qignore content"></textarea></div>
    {:else}<div class="loading-panel">Loading .qignore…</div>{/if}
  {/if}
</section>
