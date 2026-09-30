<script lang="ts">
  import type { AgentDefinition } from './types';
  export let definitions: AgentDefinition[] = [];
</script>

<div class="subsection-heading"><div><h3>Built in profiles</h3><p>These occupational definitions are shipped with Q and cannot be edited here.</p></div><span class="count-badge">{definitions.length} profiles</span></div>
<div class="agent-definition-grid">{#each definitions as definition}<details class:unavailable-definition={!definition.available} class="settings-card agent-definition"><summary><span><strong>{definition.name}</strong><small>{definition.description}</small></span><span class="source-badge">{definition.role || definition.kind}</span></summary><div class="definition-meta"><span>{definition.kind}</span><span>{definition.mutates_workspace ? 'Mutates workspace' : 'Read only'}</span><span>{definition.available ? definition.connection || 'Available' : 'Unavailable'}</span></div><p>{definition.system_prompt}</p><small><strong>Tools</strong> {definition.tools.join(', ') || 'ACP managed'}</small><small><strong>Delegates</strong> {definition.delegates.join(', ') || 'none'}</small></details>{/each}</div>
