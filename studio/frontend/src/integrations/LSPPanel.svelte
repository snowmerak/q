<script lang="ts">
  import { Plus, Search, Trash2 } from '@lucide/svelte';
  import { jsonText, parseJSON, requestJSON, requireWorkspace } from './api';
  import type { IntegrationOperations } from './operations';
  import type { LSPServer, LSPRoot, LSPResponse, DiscoveredServer } from './types';

  export let active = false;
  export let reloadToken = 0;
  export let operations: IntegrationOperations;
  export let workspaceRoot = '';
  let lsp: LSPResponse | null = null;
  let newLSPID = '';
  let loadedKey = '';
  let loadGeneration = 0;
  let editGeneration = 0;

  $: if (active && loadedKey !== JSON.stringify([workspaceRoot, reloadToken])) load();

  function load() {
    loadedKey = JSON.stringify([workspaceRoot, reloadToken]);
    const generation = ++loadGeneration;
    const target = workspaceRoot;
    lsp = null;
    operations.run(async () => {
      const root = requireWorkspace(target);
      const result = prepareLSP(await requestJSON<LSPResponse>('GET', `/api/v1/workspaces/lsp?workspace_root=${encodeURIComponent(root)}`));
      if (generation === loadGeneration) lsp = result;
    }, '');
  }

  function prepareLSP(value: LSPResponse) {
    value.global ||= { servers: {}, languages: {} };
    value.global.servers ||= {};
    value.global.languages ||= {};
    value.workspace ||= { version: 1, roots: [] };
    value.workspace.version ||= 1;
    value.workspace.roots ||= [];
    for (const server of Object.values(value.global.servers)) {
      server.languages ||= [];
      server.args ||= [];
    }
    return value;
  }

  function saveLSP() {
    if (!lsp) return;
    const global = structuredClone(lsp.global);
    const workspace = structuredClone(lsp.workspace);
    const root = lsp.workspace_root;
    const generation = loadGeneration;
    const edit = ++editGeneration;
    operations.run(async () => {
      const result = prepareLSP(await requestJSON<LSPResponse>('PUT', '/api/v1/workspaces/lsp', { workspace_root: root, global, workspace }));
      if (generation === loadGeneration && edit === editGeneration) lsp = result;
    });
  }

  function addLSPServer() {
    if (!lsp) return;
    const id = newLSPID.trim();
    if (!id || lsp.global.servers[id]) return;
    lsp = {
      ...lsp,
      global: {
        ...lsp.global,
        servers: { ...lsp.global.servers, [id]: { languages: ['go'], command: id, args: [] } }
      }
    };
    newLSPID = '';
    saveLSP();
  }

  function removeLSPServer(id: string) {
    if (!lsp || !confirm(`Delete language server “${id}”?`)) return;
    const servers = { ...lsp.global.servers };
    const languages = { ...lsp.global.languages };
    delete servers[id];
    for (const language of Object.keys(languages)) if (languages[language] === id) delete languages[language];
    const roots = lsp.workspace.roots.map((root) => root.server === id ? { ...root, server: '' } : root);
    lsp = { ...lsp, global: { ...lsp.global, servers, languages }, workspace: { ...lsp.workspace, roots } };
    saveLSP();
  }

  function setLSPArgs(server: LSPServer, value: string) {
    try { server.args = parseJSON<string[]>(value, 'Arguments'); saveLSP(); }
    catch (reason) { operations.fail(reason); }
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
    lsp = { ...lsp, workspace: { ...lsp.workspace, roots: [...lsp.workspace.roots, root] } };
    saveLSP();
  }

  function removeLSPRoot(index: number) {
    if (!lsp) return;
    lsp = { ...lsp, workspace: { ...lsp.workspace, roots: lsp.workspace.roots.filter((_, candidate) => candidate !== index) } };
    saveLSP();
  }

  function discoverLSP() {
    if (!lsp) return;
    const global = structuredClone(lsp.global);
    const workspace = structuredClone(lsp.workspace);
    const root = lsp.workspace_root;
    const generation = loadGeneration;
    const edit = ++editGeneration;
    operations.run(async () => {
      const discovered = await requestJSON<{ roots: LSPRoot[]; servers: DiscoveredServer[] }>('POST', '/api/v1/workspaces/lsp/discover', { workspace_root: root });
      for (const server of discovered.servers || []) if (!global.servers[server.id]) global.servers[server.id] = server.config;
      const keys = new Set(workspace.roots.map((root) => `${root.path}\0${root.language}`));
      for (const root of discovered.roots || []) if (!keys.has(`${root.path}\0${root.language}`)) workspace.roots.push(root);
      const result = prepareLSP(await requestJSON<LSPResponse>('PUT', '/api/v1/workspaces/lsp', { workspace_root: root, global, workspace }));
      if (generation === loadGeneration && edit === editGeneration) lsp = result;
    }, 'Discovery merged and saved');
  }
</script>

{#if active}
    {#if lsp}
      <div class="subsection-heading"><div><h3>Language server profiles</h3><p>Commands are trusted global settings; repository roots only select profiles.</p></div><button class="primary-button" onclick={discoverLSP}><Search size={15} /> Discover</button></div>
      <div class="inline-create settings-card"><label><span>Profile ID</span><input bind:value={newLSPID} placeholder="gopls" /></label><button class="primary-button" onclick={addLSPServer} disabled={!newLSPID.trim()}><Plus size={15} /> Add profile</button></div>
      {#each Object.entries(lsp.global.servers) as [id, server]}
        <article class="settings-card integration-editor"><div class="card-heading"><div><h3>{id}</h3><p>{server.languages.join(', ')}</p></div><div class="provider-actions"><label class="compact-toggle"><input type="checkbox" checked={!server.disabled} onchange={(event) => { server.disabled = !event.currentTarget.checked; saveLSP(); }} /><span>{server.disabled ? 'Disabled' : 'Enabled'}</span></label><button class="danger-icon" onclick={() => removeLSPServer(id)}><Trash2 size={15} /></button></div></div>
          <div class="field-grid"><label><span>Languages</span><input value={server.languages.join(', ')} onchange={(event) => setLSPLanguages(server, event.currentTarget.value)} /></label><label><span>Command</span><input bind:value={server.command} onchange={saveLSP} /></label><label class="wide-field"><span>Arguments · JSON</span><input value={jsonText(server.args, [])} onchange={(event) => setLSPArgs(server, event.currentTarget.value)} /></label></div>
          <div class="grant-grid">{#each server.languages as language}<label><span>{language} default</span><select bind:value={lsp.global.languages[language]} onchange={saveLSP}><option value="">No default</option>{#each Object.entries(lsp.global.servers).filter(([, candidate]) => candidate.languages.includes(language)) as [candidateID]}<option value={candidateID}>{candidateID}</option>{/each}</select></label>{/each}</div>
        </article>
      {/each}
      <div class="subsection-heading"><div><h3>Repository roots</h3><p>{lsp.workspace_path}</p></div><button class="text-button" onclick={() => addLSPRoot()}><Plus size={14} /> Add root</button></div>
      <div class="settings-card compact-list">{#each lsp.workspace.roots as root, index}<div class="root-row"><input bind:value={root.path} onchange={saveLSP} aria-label="Root path" /><select bind:value={root.language} onchange={saveLSP}>{#each allLanguages() as language}<option value={language}>{language}</option>{/each}</select><select bind:value={root.server} onchange={saveLSP}><option value="">Language default</option>{#each Object.entries(lsp.global.servers).filter(([, server]) => server.languages.includes(root.language)) as [id]}<option value={id}>{id}</option>{/each}</select><label class="compact-toggle"><input type="checkbox" checked={!root.disabled} onchange={(event) => { root.disabled = !event.currentTarget.checked; saveLSP(); }} /><span>{root.disabled ? 'Disabled' : 'Enabled'}</span></label><span class="source-badge">{root.source || 'manual'}</span><button class="danger-icon" onclick={() => removeLSPRoot(index)}><Trash2 size={14} /></button></div>{:else}<div class="api-key-empty">No repository roots configured.</div>{/each}</div>
    {:else}<div class="loading-panel">Loading language server settings…</div>{/if}
{/if}
