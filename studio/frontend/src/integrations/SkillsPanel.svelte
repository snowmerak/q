<script lang="ts">
  import { RefreshCw } from '@lucide/svelte';
  import { requestJSON, requireWorkspace } from './api';
  import type { IntegrationOperations } from './operations';
  import type { Skill, SkillResponse } from './types';

  export let active = false;
  export let reloadToken = 0;
  export let operations: IntegrationOperations;
  export let workspaceRoot = '';
  let skills: SkillResponse | null = null;
  let skillRepository = '';
  let skillScope = 'global';
  let loadedKey = '';
  let loadGeneration = 0;

  $: if (active && loadedKey !== JSON.stringify([workspaceRoot, reloadToken])) load();

  function load() {
    loadedKey = JSON.stringify([workspaceRoot, reloadToken]);
    const generation = ++loadGeneration;
    const target = workspaceRoot;
    skills = null;
    operations.run(async () => {
      const root = requireWorkspace(target);
      const result = prepareSkills(await requestJSON<SkillResponse>('GET', `/api/v1/workspaces/skills?workspace_root=${encodeURIComponent(root)}`));
      if (generation === loadGeneration) skills = result;
    }, '');
  }

  function skillOperation(method: string, url: string, extra: Record<string, string>, success: string, onSaved?: () => void) {
    if (!skills) return;
    const root = skills.workspace_root;
    const generation = loadGeneration;
    return operations.run(async () => {
      const result = prepareSkills(await requestJSON<SkillResponse>(method, url, { workspace_root: root, ...extra }));
      if (generation === loadGeneration) {
        skills = result;
        onSaved?.();
      }
    }, success);
  }

  function prepareSkills(value: SkillResponse) {
    value.skills ||= [];
    value.issues ||= [];
    return value;
  }

  function installSkill() {
    const repository = skillRepository.trim();
    if (!repository) return;
    const scope = skillScope;
    void skillOperation('POST', '/api/v1/workspaces/skills', { scope, repository }, 'Skill installed and indexed', () => {
      if (skillRepository.trim() === repository) skillRepository = '';
    });
  }

  function updateSkill(skill: Skill) {
    void skillOperation('POST', `/api/v1/workspaces/skills/${encodeURIComponent(skill.id)}`, {}, 'Skill updated and indexed');
  }

  function removeSkill(skill: Skill) {
    if (!confirm(`Delete managed skill “${skill.name}”?`)) return;
    void skillOperation('DELETE', `/api/v1/workspaces/skills/${encodeURIComponent(skill.id)}`, {}, 'Skill removed and index updated');
  }

  function reindexSkills() {
    void skillOperation('POST', '/api/v1/workspaces/skills/reindex', {}, 'Skill indexes rebuilt');
  }
</script>

{#if active}
    {#if skills}
      <div class="subsection-heading"><div><h3>Agent Skills</h3><p>Portable entries are read only. Q managed Git checkouts can be pulled or removed.</p></div><button class="primary-button" onclick={reindexSkills}><RefreshCw size={15} /> Reindex all</button></div>
      <div class="inline-create settings-card skill-installer"><label><span>Scope</span><select bind:value={skillScope}><option value="global">Global</option><option value="workspace">Repository</option></select></label><label class="wide-field"><span>Git repository</span><input bind:value={skillRepository} placeholder="https://github.com/owner/skill.git" /></label><button class="primary-button" onclick={installSkill} disabled={!skillRepository.trim()}>Clone and index</button></div>
      {#each ['global', 'project'] as scope}<div class="subsection-heading"><div><h3>{scope === 'global' ? 'Global' : 'Repository'} skills</h3><p>{skills.skills.filter((skill) => skill.scope === scope).length} discovered</p></div></div><div class="skill-grid">{#each skills.skills.filter((skill) => skill.scope === scope) as skill}<article class:shadowed={!skill.active} class="settings-card skill-card"><div class="card-heading"><div><h3>{skill.name}</h3><p>{skill.description || 'No description'}</p></div><span class="source-badge">{skill.managed ? 'Git managed' : skill.source}</span></div><small>{skill.directory}</small>{#if skill.tags?.length}<div class="tag-list">{#each skill.tags as tag}<span>{tag}</span>{/each}</div>{/if}<div class="card-actions">{#if skill.managed}<button class="text-button" onclick={() => updateSkill(skill)}>Pull</button><button class="text-button danger-text" onclick={() => removeSkill(skill)}>Delete</button>{:else}<span>Read only</span>{/if}{#if !skill.active}<span>Shadowed</span>{/if}</div></article>{/each}</div>{/each}
      {#if skills.issues.length}<div class="settings-card issue-list"><h3>Discovery notes</h3>{#each skills.issues as issue}<p><code>{issue.path}</code> {issue.message}</p>{/each}</div>{/if}
    {:else}<div class="loading-panel">Loading Agent Skills…</div>{/if}
{/if}
