<script lang="ts">
  import { Plus, Trash2 } from '@lucide/svelte';
  import { requestJSON } from '../api';
  import { jsonText, parseJSON } from '../settings/json';
  import type { SettingsOperations } from '../settings/operations';
  import type { MCPServer, MCPResponse } from './types';

  export let active = false;
  export let reloadToken = 0;
  export let operations: SettingsOperations;
  let mcp: MCPResponse | null = null;
  let newMCPID = '';
  let loadedToken = -1;
  let loadGeneration = 0;
  let editGeneration = 0;

  $: if (active && loadedToken !== reloadToken) load();

  function load() {
    loadedToken = reloadToken;
    const generation = ++loadGeneration;
    operations.run(async () => {
      const result = prepareMCP(await requestJSON<MCPResponse>('GET', '/api/v1/integrations/mcp'));
      if (generation === loadGeneration) mcp = result;
    }, '');
  }

  function prepareMCP(value: MCPResponse) {
    value.config ||= { version: 1, servers: {}, roles: {} };
    value.config.servers ||= {};
    value.config.roles ||= {};
    value.roles ||= [];
    for (const role of value.roles) value.config.roles[role] ||= [];
    for (const server of Object.values(value.config.servers)) {
      server.args ||= [];
      server.env ||= {};
      server.headers ||= {};
    }
    return value;
  }

  function saveMCP() {
    if (!mcp) return;
    const payload = structuredClone(mcp.config);
    const generation = loadGeneration;
    const edit = ++editGeneration;
    operations.run(async () => {
      const result = prepareMCP(await requestJSON<MCPResponse>('PUT', '/api/v1/integrations/mcp', payload));
      if (generation === loadGeneration && edit === editGeneration) mcp = result;
    });
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
    catch (reason) { operations.fail(reason); }
  }

  function toggleMCPRole(role: string, id: string, checked: boolean) {
    if (!mcp) return;
    const values = new Set(mcp.config.roles[role] || []);
    checked ? values.add(id) : values.delete(id);
    mcp.config.roles[role] = [...values]; saveMCP();
  }
</script>

{#if active}
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
{/if}
