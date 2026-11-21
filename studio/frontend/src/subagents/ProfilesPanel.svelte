<script lang="ts">
  import { Plus, Search, Trash2 } from '@lucide/svelte';
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';
  import { prepareAgents } from './catalog';
  import ExecutionPicker from './ExecutionPicker.svelte';
  import type { SettingsOperations } from '../settings/operations';
  import type { ModelOption } from '../settings/types';
  import type { AgentResponse, ExecutionTarget, Profile, ProfileEntry } from './types';

  export let agents: AgentResponse;
  export let operations: SettingsOperations;
  export let models: ModelOption[];
  let disposed = false;
  onDestroy(() => { disposed = true; });

  function rootValue() { return agents.workspace_root; }

  let newProfileName = '';
  let newProfileScope = 'global';
  let newProfileTarget: ExecutionTarget | null = null;
  let toolFilters: Record<string, string> = {};
  let delegateFilters: Record<string, string> = {};
  let editGeneration = 0;
  const revisions = new Map<string, { scope: string; revision: string }>();

  function adoptProfiles(result: AgentResponse, edit: number) {
    if (!disposed && edit === editGeneration) agents = { ...agents, profiles: result.profiles || [] };
  }

  $: enabledConnections = Object.entries(agents.connections).filter(([, connection]) => !connection.disabled).map(([id]) => id).sort();

  function addProfile() {
    if (!agents) return;
    const name = newProfileName.trim();
    if (!name) return;
    const scope = newProfileScope === 'workspace' && rootValue() ? 'workspace' : 'global';
    const connection = newProfileTarget?.agent || '';
    const kind = connection ? 'external' : 'inner';
    const profile: Profile = {
      version: 1,
      name,
      description: '',
      kind,
      agent: connection,
      target: kind === 'inner' ? newProfileTarget || { default: true } : undefined,
      system_prompt: 'Complete the assigned request and report verified results.',
      mutates_workspace: false,
      tools: [],
      delegates: []
    };
    newProfileName = '';
    newProfileTarget = null;
    const root = rootValue();
    const edit = ++editGeneration;
    operations.run(async () => {
      const result = prepareAgents(await requestJSON<AgentResponse>('POST', '/api/v1/settings/subagents/profiles', { workspace_root: root, scope, profile }));
      adoptProfiles(result, edit);
    }, 'Subagent profile created');
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
    const root = rootValue();
    const path = entry.path;
    const edit = ++editGeneration;
    operations.run(async () => {
      // Later queued edits use the acknowledged scope/revision, while the UI
      // keeps the latest draft until its own save finishes.
      const latest = revisions.get(path);
      const result = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents/profiles', {
        workspace_root: root,
        scope: desiredScope,
        original_scope: latest?.scope || knownOriginalScope,
        revision: latest?.revision || knownRevision,
        profile: desired
      }));
      const saved = result.profiles.find((candidate) => candidate.profile.name === desired.name && candidate.scope === desiredScope);
      if (saved) revisions.set(path, { scope: saved.scope, revision: saved.revision });
      adoptProfiles(result, edit);
    });
  }

  function changeProfileTarget(entry: ProfileEntry, target: ExecutionTarget | null) {
    const root = rootValue();
    if (root && entry.scope === 'global' && entry.profile.kind === 'inner') {
      const edit = ++editGeneration;
      operations.run(async () => {
        const result = prepareAgents(await requestJSON<AgentResponse>('PUT', '/api/v1/settings/subagents/target', {
          workspace_root: root, scope: 'workspace', id: `global/${entry.profile.name}`, target
        }));
        adoptProfiles(result, edit);
      });
      return;
    }
    if (entry.profile.kind === 'external') {
      if (target?.agent) {
        entry.profile.agent = target.agent;
      } else if (target) {
        entry.profile.kind = 'inner';
        entry.profile.agent = '';
        entry.profile.mutates_workspace = false;
        entry.profile.target = target;
      }
    } else {
      entry.profile.target = target || { default: true };
    }
    saveProfile(entry);
  }

  function deleteProfile(entry: ProfileEntry) {
    if (!confirm(`Delete subagent profile “${entry.profile.name}”?`)) return;
    const root = rootValue();
    const path = entry.path;
    const profile = structuredClone(entry.profile);
    const scope = entry.scope;
    const revision = entry.revision;
    const edit = ++editGeneration;
    operations.run(async () => {
      const latest = revisions.get(path);
      const result = prepareAgents(await requestJSON<AgentResponse>('DELETE', `/api/v1/settings/subagents/profiles/${encodeURIComponent(profile.name)}`, {
        workspace_root: root, scope: latest?.scope || scope, revision: latest?.revision || revision, profile
      }));
      revisions.delete(path);
      adoptProfiles(result, edit);
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
</script>

<div class="subsection-heading"><div><h3>Custom profiles</h3><p>Create reusable profiles. Existing changes save automatically.</p></div><span class="count-badge">{agents.profiles.length} profiles</span></div>
<div class="settings-card profile-create">
  <label><span>Profile name</span><input bind:value={newProfileName} placeholder="security-reviewer" onkeydown={(event) => event.key === 'Enter' && addProfile()} /></label>
  <label><span>Scope</span><select bind:value={newProfileScope}><option value="global">Global</option><option value="workspace" disabled={!rootValue()}>Repository</option></select></label>
  <ExecutionPicker target={newProfileTarget} {models} connections={enabledConnections} defaultModel={agents.default_model}
    selectTarget={target => newProfileTarget = target} />
  <button class="primary-button" onclick={addProfile} disabled={!newProfileName.trim()}><Plus size={15} /> Add profile</button>
</div>

{#each agents.profiles as entry (entry.path)}
  <article class:error-card={!!entry.error} class:shadowed-card={entry.shadowed} class="settings-card profile-editor subagent-profile">
    <div class="card-heading"><div><h3>{entry.profile.name}</h3><p>{entry.path}</p></div><div class="profile-badges"><span class="source-badge">{entry.scope}</span>{#if entry.shadowed}<span class="source-badge warning-badge">shadowed</span>{/if}<button class="danger-icon" title="Delete profile" onclick={() => deleteProfile(entry)}><Trash2 size={15} /></button></div></div>
    {#if entry.error}<p class="profile-error">{entry.error}</p>{:else}
      <div class="field-grid">
        <label><span>Scope</span><select bind:value={entry.scope} onchange={() => saveProfile(entry)}><option value="global">Global</option><option value="workspace" disabled={!rootValue()}>Repository</option></select></label>
        <ExecutionPicker target={entry.effective_target} {models} connections={enabledConnections} defaultModel={agents.default_model}
          selectTarget={target => changeProfileTarget(entry, target)} />
        <label class="wide-field"><span>Description</span><input bind:value={entry.profile.description} onchange={() => saveProfile(entry)} /></label>
        {#if entry.profile.kind === 'external'}
          <label class="toggle-field"><span>Mutation access</span><input type="checkbox" bind:checked={entry.profile.mutates_workspace} onchange={() => saveProfile(entry)} /><small>{entry.profile.mutates_workspace ? 'Mutates workspace' : 'Read only'}</small></label>
        {/if}
      </div>
      <label class="stacked-field prompt-field"><span>System prompt</span><textarea rows="6" bind:value={entry.profile.system_prompt} onchange={() => saveProfile(entry)}></textarea></label>
      {#if entry.profile.kind === 'inner' && !entry.effective_target?.agent}
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
