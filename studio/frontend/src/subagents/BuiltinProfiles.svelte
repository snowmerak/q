<script lang="ts">
  import { requestJSON } from '../api';
  import ExecutionPicker from './ExecutionPicker.svelte';
  import { prepareAgents } from './catalog';
  import type { SettingsOperations } from '../settings/operations';
  import type { ModelOption } from '../settings/types';
  import type { AgentResponse, ExecutionTarget } from './types';

  export let agents: AgentResponse;
  export let models: ModelOption[];
  export let operations: SettingsOperations;
  $: enabledConnections = Object.entries(agents.connections).filter(([, connection]) => !connection.disabled).map(([id]) => id).sort();

  function saveTarget(id: string, target: ExecutionTarget | null, externalOnly: boolean) {
    const root = agents.workspace_root;
    operations.run(async () => {
      const result = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents/target', {
        workspace_root: root, scope: root && !externalOnly ? 'workspace' : 'global', id, target
      }));
      agents = { ...agents, builtins: result.builtins, bindings: result.bindings };
    });
  }
</script>

<div class="subsection-heading"><div><h3>Built in profiles</h3><p>{agents.workspace_root ? 'Model and ACP changes for native profiles apply in this repository.' : 'Execution selections apply across projects.'}</p></div><span class="count-badge">{agents.builtins.length} profiles</span></div>
<div class="agent-definition-grid">
  {#each agents.builtins as definition (definition.name)}
    <details class:unavailable-definition={!definition.available} class="settings-card agent-definition">
      <summary>
        <span><strong>{definition.name}</strong><small>{definition.description}</small></span>
        <span class="source-badge">{definition.role || definition.kind}</span>
      </summary>
      <ExecutionPicker target={definition.target} {models} connections={enabledConnections} externalOnly={!!definition.external_only}
        workspaceRoot={agents.workspace_root} defaultModel={agents.default_model} disabled={$operations.busy}
        selectTarget={target => saveTarget(definition.name, target, !!definition.external_only)} />
      <div class="definition-meta">
        <span>{definition.kind}</span><span>{definition.mutates_workspace ? 'Mutates workspace' : 'Read only'}</span>
        <span>{definition.available ? definition.connection || 'Available' : 'Unavailable'}</span>
      </div>
      <p>{definition.system_prompt}</p>
      <small><strong>Tools</strong> {definition.tools.join(', ') || 'ACP managed'}</small>
      <small><strong>Delegates</strong> {definition.delegates.join(', ') || 'none'}</small>
    </details>
  {/each}
</div>
