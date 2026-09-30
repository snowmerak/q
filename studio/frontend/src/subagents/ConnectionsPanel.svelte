<script lang="ts">
  import { FlaskConical, Plus, Trash2 } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import { prepareAgents } from './catalog';
  import { jsonText, parseJSON } from '../settings/json';
  import type { SettingsOperations } from '../settings/operations';
  import type { AgentConnection, AgentResponse } from './types';

  export let agents: AgentResponse;
  export let operations: SettingsOperations;
  let disposed = false;
  onDestroy(() => { disposed = true; });

  function rootValue() { return agents.workspace_root; }

  let newConnectionID = '';
  let probeStates: Record<string, string> = {};
  let editGeneration = 0;

  $: enabledConnections = Object.entries(agents.connections).filter(([, connection]) => !connection.disabled);

  function setConnectionEnabled(id: string, enabled: boolean) {
    agents = {
      ...agents,
      connections: { ...agents.connections, [id]: { ...agents.connections[id], disabled: !enabled } }
    };
    saveConnections();
  }

  function saveConnections() {
    if (!agents) return;
    const connections = structuredClone(agents.connections);
    const bindings = structuredClone(agents.bindings);
    const root = rootValue();
    const edit = ++editGeneration;
    operations.run(async () => {
      const result = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents', { workspace_root: root, connections, bindings }));
      if (!disposed && edit === editGeneration) {
        agents = { ...agents, connections: result.connections, bindings: result.bindings, builtins: result.builtins };
      }
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
    catch (reason) { operations.fail(reason); }
  }

  function probeConnection(id: string) {
    const root = rootValue();
    if (!root) {
      operations.fail(new Error('Enter a repository path before testing an ACP connection.'));
      return;
    }
    probeStates[id] = 'Testing…';
    probeStates = { ...probeStates };
    operations.run(async () => {
      await requestJSON('POST', '/api/v1/settings/subagents/probe', { workspace_root: root, connection_id: id });
      probeStates[id] = 'Connected';
      probeStates = { ...probeStates };
    }, `${id} completed the ACP lifecycle`);
  }
</script>

<div class="subsection-heading"><div><h3>External role bindings</h3><p>Assign enabled ACP connections to Q's fixed external roles.</p></div><code>{agents.config_path}</code></div>
<div class="settings-card binding-grid">{#each agents.external_roles as role}<label><span>{role}</span><select bind:value={agents.bindings[role]} onchange={saveConnections}><option value="">Unassigned</option>{#each enabledConnections as [id]}<option value={id}>{id}</option>{/each}</select></label>{/each}</div>

<div class="subsection-heading"><div><h3>ACP connections</h3><p>Preset or custom stdio processes used by external roles and profiles.</p></div></div>
<div class="inline-create settings-card"><label><span>Connection ID</span><input bind:value={newConnectionID} placeholder="codex-local" onkeydown={(event) => event.key === 'Enter' && addConnection()} /></label><button class="primary-button" onclick={addConnection} disabled={!newConnectionID.trim()}><Plus size={15} /> Add connection</button></div>
{#each Object.entries(agents.connections) as [id, connection]}
  <article class="settings-card integration-editor"><div class="card-heading"><div><h3>{id}</h3><p>{connection.preset ? `preset ${connection.preset}` : connection.command || 'custom command'}</p></div><div class="provider-actions"><button class="text-button" title={rootValue() ? 'Run ACP initialize and shutdown' : 'Enter a repository path before testing'} onclick={() => probeConnection(id)} disabled={!rootValue()}><FlaskConical size={14} /> {probeStates[id] || 'Test'}</button><button class="danger-icon" title="Delete ACP connection" onclick={() => removeConnection(id)}><Trash2 size={15} /></button></div></div>
    <div class="field-grid"><label><span>Preset</span><select bind:value={connection.preset} onchange={() => { if (connection.preset) connection.command = ''; saveConnections(); }}><option value="">Custom command</option><option value="codex">Codex</option><option value="grok">Grok</option></select></label><label><span>Command</span><input bind:value={connection.command} onchange={() => { if (connection.command) connection.preset = ''; saveConnections(); }} disabled={!!connection.preset} /></label><label><span>Arguments · JSON</span><input value={jsonText(connection.args, [])} onchange={(event) => setConnectionJSON(connection, 'args', event.currentTarget.value)} /></label><label><span>Child environment · JSON</span><input value={jsonText(connection.env, {})} onchange={(event) => setConnectionJSON(connection, 'env', event.currentTarget.value)} /></label><label><span>ACP auth method</span><input bind:value={connection.auth_method} onchange={saveConnections} /></label><label class="toggle-field"><span>Connection state</span><input type="checkbox" checked={!connection.disabled} onchange={(event) => setConnectionEnabled(id, event.currentTarget.checked)} /><small>{connection.disabled ? 'Disabled' : 'Enabled'}</small></label></div>
  </article>
{:else}<div class="empty-card">No ACP connections configured.</div>{/each}
