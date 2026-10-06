<script lang="ts">
  import { ChevronDown, ChevronUp, Plus, RefreshCw, Trash2 } from '@lucide/svelte';
  import WorkspaceModels from '../WorkspaceModels.svelte';
  import { roleLabel } from './format';
  import type { ModelGroup, ModelOption, RoleModelAssignment } from './types';
  import { apiError as responseError } from '../api';
  import { putSettings, writeSettings } from './persistence';
  import { reasoningOptions as modelReasoningOptions } from './reasoning';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let active = false;
  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;

  let started = false;
  let modelOptions: ModelOption[] = [];
  let modelsLoading = false;
  let modelsError = '';
  let catalogGeneration = 0;
  let newModelGroupName = '';
  let newCustomRoleName = '';
  $: if (active && !started) { started = true; void loadModels(); }

  async function loadModels() {
    const generation = ++catalogGeneration;
    modelsLoading = true;
    modelsError = '';
    try {
      const response = await fetch('/api/v1/settings/models', { headers: { Accept: 'application/json' } });
      if (!response.ok) throw new Error(await responseError(response));
      const catalog = (await response.json()) as { models: ModelOption[] };
      if (generation === catalogGeneration) modelOptions = catalog.models;
    } catch (error) {
      if (generation === catalogGeneration) modelsError = error instanceof Error ? error.message : 'Models are unavailable';
    } finally {
      if (generation === catalogGeneration) modelsLoading = false;
    }
  }

  function saveModelAssignment(target: string, assignment: { model: string; reasoning_effort: string; embedding_dimensions?: number; workspace_root?: string }) {
    const payload = JSON.stringify(assignment);
    queueSave(() => putSettings(`/api/v1/settings/models/${encodeURIComponent(target)}`, payload));
  }

  function saveDefaultModel() {
    if (!settings) return;
    saveModelAssignment('default', {
      model: settings.models.default_model,
      reasoning_effort: settings.models.default_reasoning_effort
    });
  }

  function saveEmbeddingModel() {
    if (!settings) return;
    saveModelAssignment('embedding', {
      model: settings.models.embedding_model,
      reasoning_effort: '',
      embedding_dimensions: settings.models.embedding_dimensions,
      workspace_root: localStorage.getItem('q-studio-workspace-root') || ''
    });
  }

  function saveRoleModel(role: RoleModelAssignment) {
    saveModelAssignment(role.role, { model: role.configured_model, reasoning_effort: role.reasoning_effort });
  }

  function saveModelGroup(group: ModelGroup) {
    const payload = JSON.stringify(group);
    queueSave(async () => {
      const snapshot = await putSettings('/api/v1/settings/model-groups', payload);
      await loadModels();
      return snapshot;
    });
  }

  function addModelGroup() {
    const name = newModelGroupName.trim();
    const model = modelOptions.find((option) => !option.group)?.id || '';
    if (!name || !model) return;
    newModelGroupName = '';
    saveModelGroup({ name, candidates: [{ model, reasoning_effort: '', timeout: '' }] });
  }

  function deleteModelGroup(group: ModelGroup) {
    if (!window.confirm(`Delete model group “${group.name}”?`)) return;
    queueSave(async () => {
      const snapshot = await writeSettings('DELETE', `/api/v1/settings/model-groups/${encodeURIComponent(group.name)}`);
      await loadModels();
      return snapshot;
    });
  }

  function addGroupCandidate(group: ModelGroup) {
    const used = new Set(group.candidates.map((candidate) => candidate.model));
    const model = modelOptions.find((option) => !option.group && !used.has(option.id))?.id;
    if (!model) return;
    group.candidates = [...group.candidates, { model, reasoning_effort: '', timeout: '' }];
    saveModelGroup(group);
  }

  function removeGroupCandidate(group: ModelGroup, index: number) {
    if (group.candidates.length <= 1) return;
    group.candidates = group.candidates.filter((_, candidateIndex) => candidateIndex !== index);
    saveModelGroup(group);
  }

  function moveGroupCandidate(group: ModelGroup, index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target < 0 || target >= group.candidates.length) return;
    const candidates = [...group.candidates];
    [candidates[index], candidates[target]] = [candidates[target], candidates[index]];
    group.candidates = candidates;
    saveModelGroup(group);
  }

  function addCustomRole() {
    const role = newCustomRoleName.trim();
    if (!role) return;
    newCustomRoleName = '';
    queueSave(() => writeSettings('POST', '/api/v1/settings/roles', JSON.stringify({ role })));
  }

  function deleteCustomRole(role: RoleModelAssignment) {
    if (!window.confirm(`Delete custom role “${role.role}”?`)) return;
    const root = localStorage.getItem('q-studio-workspace-root') || '';
    const query = root ? `?workspace_root=${encodeURIComponent(root)}` : '';
    queueSave(() => writeSettings('DELETE', `/api/v1/settings/roles/${encodeURIComponent(role.role)}${query}`));
  }

  function saveModelAPIMode(model: ModelOption) {
    const payload = JSON.stringify({ model: model.id, mode: model.api_mode || 'chat_completions' });
    queueSave(() => putSettings('/api/v1/settings/model-api-mode', payload));
  }

  function saveModelContextOverride(model: ModelOption) {
    const payload = JSON.stringify({ model: model.id, context_window: model.context_override || 0 });
    queueSave(() => putSettings('/api/v1/settings/gateway/model-metadata', payload));
  }

  function reasoningOptions(model: string, current: string) {
    return modelReasoningOptions(model, current, modelOptions);
  }
</script>

<section class="settings-section">
  <div class="section-heading">
    <div><p class="eyebrow">GLOBAL MODELS</p><h2>Model assignments</h2><p>Choose Gateway models for chat, embedding, and agent roles.</p></div>
    <button class="icon-button" title="Refresh Gateway models" onclick={loadModels} disabled={modelsLoading}><RefreshCw aria-hidden="true" size={16} class={modelsLoading ? 'spin' : undefined} /></button>
  </div>
  <code class="config-path">{settings.models.config_path}</code>
  {#if modelsError}<p class="inline-warning">{modelsError}</p>{/if}
  <div class="settings-card assignment-list">
    <div class="assignment-row primary-assignment">
      <div><strong>Default</strong><small>Primary chat model and role fallback</small></div>
      <label><span>Model</span><select bind:value={settings.models.default_model} onchange={saveDefaultModel} disabled={modelsLoading}>
        {#if settings.models.default_model && !modelOptions.some((model) => model.id === settings?.models.default_model)}<option value={settings.models.default_model}>{settings.models.default_model}</option>{/if}
        {#each modelOptions as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
      </select></label>
      <label><span>Reasoning</span><select bind:value={settings.models.default_reasoning_effort} onchange={saveDefaultModel}><option value="">Provider default</option>{#each reasoningOptions(settings.models.default_model, settings.models.default_reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
    </div>
    <div class="assignment-row primary-assignment">
      <div><strong>Embedding</strong><small>Semantic indexes and skill search</small></div>
      <label><span>Model</span><select bind:value={settings.models.embedding_model} onchange={saveEmbeddingModel} disabled={modelsLoading}>
        <option value="">Disabled</option>
        {#if settings.models.embedding_model && !modelOptions.some((model) => model.id === settings?.models.embedding_model)}<option value={settings.models.embedding_model}>{settings.models.embedding_model}</option>{/if}
        {#each modelOptions.filter((model) => !model.group) as model}<option value={model.id}>{model.id}</option>{/each}
      </select></label>
      <label><span>Dimensions</span><input type="number" min="1" max="4096" bind:value={settings.models.embedding_dimensions} onchange={saveEmbeddingModel} disabled={!settings.models.embedding_model} /></label>
    </div>
    {#each settings.models.roles as role}
      <div class="assignment-row">
        <div class="assignment-role"><span><strong>{roleLabel(role.role)}</strong><small>{role.inherited ? `Inherits ${role.effective_model}` : role.role}</small></span>{#if role.custom}<button class="danger-icon" title="Delete custom role" onclick={() => deleteCustomRole(role)}><Trash2 aria-hidden="true" size={14} /></button>{/if}</div>
        <label><span>Model</span><select bind:value={role.configured_model} onchange={() => saveRoleModel(role)} disabled={modelsLoading}>
          <option value="">Inherit</option>
          {#if role.configured_model && !modelOptions.some((model) => model.id === role.configured_model)}<option value={role.configured_model}>{role.configured_model}</option>{/if}
          {#each modelOptions as model}<option value={model.id}>{model.id}{model.group ? ' · group' : ''}</option>{/each}
        </select></label>
        <label><span>Reasoning</span><select bind:value={role.reasoning_effort} onchange={() => saveRoleModel(role)}><option value="">Provider default</option>{#each reasoningOptions(role.configured_model || role.effective_model, role.reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
      </div>
    {/each}
  </div>

  {#if started}<WorkspaceModels models={modelOptions} />{/if}

  <div class="subsection-heading"><div><h3>Custom roles</h3><p>Create a reusable model role for subagents.</p></div></div>
  <div class="settings-card inline-create"><label><span>Role name</span><input placeholder="security-reviewer" bind:value={newCustomRoleName} onkeydown={(event) => event.key === 'Enter' && addCustomRole()} /></label><button class="primary-button" onclick={addCustomRole} disabled={!newCustomRoleName.trim()}><Plus aria-hidden="true" size={15} /> Add role</button></div>

  <div class="subsection-heading"><div><h3>Fallback groups</h3><p>Try ordered candidates with optional reasoning and timeout overrides.</p></div></div>
  <div class="settings-card inline-create"><label><span>New group name</span><input placeholder="reliable-coding" bind:value={newModelGroupName} onkeydown={(event) => event.key === 'Enter' && addModelGroup()} /></label><button class="primary-button" onclick={addModelGroup} disabled={!newModelGroupName.trim() || !modelOptions.some((model) => !model.group)}><Plus aria-hidden="true" size={15} /> Add group</button></div>
  <div class="model-group-list">
    {#each settings.models.groups as group}
      <article class="settings-card model-group-card">
        <div class="card-heading"><div><h3>{group.name}</h3><p>{group.candidates.length} ordered candidates</p></div><div class="provider-actions"><button class="primary-button compact-button" onclick={() => addGroupCandidate(group)} disabled={group.candidates.length >= modelOptions.filter((model) => !model.group).length}><Plus aria-hidden="true" size={14} /> Candidate</button><button class="danger-icon" title="Delete model group" onclick={() => deleteModelGroup(group)}><Trash2 aria-hidden="true" size={15} /></button></div></div>
        <div class="group-candidates">
          {#each group.candidates as candidate, candidateIndex}
            <div class="group-candidate">
              <span class="candidate-order">{candidateIndex + 1}</span>
              <label><span>Model</span><select bind:value={candidate.model} onchange={() => saveModelGroup(group)}>{#each modelOptions.filter((model) => !model.group) as model}<option value={model.id}>{model.id}</option>{/each}</select></label>
              <label><span>Reasoning</span><select bind:value={candidate.reasoning_effort} onchange={() => saveModelGroup(group)}><option value="">Provider default</option>{#each reasoningOptions(candidate.model, candidate.reasoning_effort) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
              <label><span>Timeout</span><input placeholder="60s or empty" bind:value={candidate.timeout} onchange={() => saveModelGroup(group)} /></label>
              <div class="candidate-actions"><button title="Move up" onclick={() => moveGroupCandidate(group, candidateIndex, -1)} disabled={candidateIndex === 0}><ChevronUp aria-hidden="true" size={14} /></button><button title="Move down" onclick={() => moveGroupCandidate(group, candidateIndex, 1)} disabled={candidateIndex === group.candidates.length - 1}><ChevronDown aria-hidden="true" size={14} /></button><button title="Remove candidate" onclick={() => removeGroupCandidate(group, candidateIndex)} disabled={group.candidates.length === 1}><Trash2 aria-hidden="true" size={13} /></button></div>
            </div>
          {/each}
        </div>
      </article>
    {/each}
  </div>

  <div class="subsection-heading"><div><h3>Model behavior</h3><p>Select an API mode and override provider context metadata.</p></div></div>
  <div class="settings-card model-behavior-list">
    {#each modelOptions.filter((model) => !model.group) as model}
      <div class="model-behavior-row"><div><strong>{model.id}</strong><small>{model.context_length ? `Discovered context ${model.context_length.toLocaleString()}` : 'No context metadata'}</small></div><label><span>API mode</span><select bind:value={model.api_mode} onchange={() => saveModelAPIMode(model)}><option value="chat_completions">Chat Completions</option><option value="responses">Responses</option></select></label><label><span>Context override</span><input type="number" min="0" placeholder="Provider metadata" bind:value={model.context_override} onchange={() => saveModelContextOverride(model)} /></label></div>
    {:else}<div class="api-key-empty">Refresh Gateway models to edit their behavior.</div>{/each}
  </div>
</section>
