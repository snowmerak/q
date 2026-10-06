<script lang="ts">
  import { onMount } from 'svelte';
  import { apiError } from './api';
  import Markdown from './Markdown.svelte';
  import { reasoningOptions } from './settings/reasoning';
  import type { ModelOption } from './settings/types';
  import type { StudioProject } from './sessions/types';

  type Seat = { model: string; reasoning_effort?: string };
  type Council = { id: string; name: string; scope: 'independent' | 'workspace' | 'project'; workspace_root?: string; project_id?: string; members: Seat[]; chair: Seat; rounds?: number; updated_at: string };
  type Opinion = { label: string; model: string; text?: string; error?: string };
  type Review = { model: string; text?: string; error?: string };
  type CouncilRound = { number: number; responses?: Opinion[]; reviews?: Review[] };
  type Turn = { id: string; rerun_of?: string; prompt: string; members: Seat[]; chair: Seat; status: string; stage: string; total_rounds?: number; current_round?: number; rounds?: CouncilRound[]; responses: Opinion[]; reviews: Review[]; final?: string; error?: string; created_at: string };

  let councils: Council[] = [];
  let selected: Council | null = null;
  let turns: Turn[] = [];
  let models: ModelOption[] = [];
  let projects: StudioProject[] = [];
  let loading = true;
  let saving = false;
  let retrying = '';
  let error = '';
  let prompt = '';
  let poll: ReturnType<typeof setTimeout> | null = null;
  let draftName = '';
  let draftScope: Council['scope'] = 'independent';
  let draftRoot = '';
  let draftProject = '';
  let draftMembers: Seat[] = [];
  let draftChair: Seat = { model: '' };
  let draftRounds = 2;
  let creating = false;
  let settingsOpen = false;
  let selectedAnswers: Record<string, string> = {};

  $: activeTurn = [...turns].reverse().find((turn) => turn.status === 'queued' || turn.status === 'running');
  $: concreteModels = models.filter((model) => !model.group);

  async function request<T>(path: string, method = 'GET', body?: unknown): Promise<T> {
    const response = await fetch(path, { method, headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) }, body: body === undefined ? undefined : JSON.stringify(body) });
    if (!response.ok) throw new Error(await apiError(response));
    return await response.json() as T;
  }

  function selectedFromPath() {
    const match = window.location.pathname.match(/^\/councils\/([^/]+)$/);
    return match ? decodeURIComponent(match[1]) : '';
  }

  async function loadCouncilList() {
    const listed = await request<{ councils: Council[] }>('/api/v1/councils');
    councils = listed.councils || [];
  }

  async function loadReferenceData() {
    const [catalog, projectList] = await Promise.all([
      request<{ models: ModelOption[] }>('/api/v1/settings/models'),
      request<{ projects: StudioProject[] }>('/api/v1/projects')
    ]);
    models = catalog.models || [];
    projects = projectList.projects || [];
    if (draftMembers.length === 0) {
      const choices = models.filter((model) => !model.group);
      draftMembers = choices.slice(0, 2).map((model) => ({ model: model.id, reasoning_effort: '' }));
      draftChair = { model: choices[0]?.id || '', reasoning_effort: '' };
    }
  }

  async function loadCatalog() {
    await Promise.all([loadCouncilList(), loadReferenceData()]);
  }

  async function openCouncil(id: string, navigate = true) {
    if (poll) { clearTimeout(poll); poll = null; }
    const detail = await request<{ council: Council; turns: Turn[] }>(`/api/v1/councils/${encodeURIComponent(id)}`);
    selected = detail.council;
    turns = detail.turns || [];
    draftName = selected.name;
    draftScope = selected.scope;
    draftRoot = selected.workspace_root || '';
    draftProject = selected.project_id || '';
    draftMembers = selected.members.map((seat) => ({ ...seat, reasoning_effort: seat.reasoning_effort || '' }));
    draftChair = { ...selected.chair, reasoning_effort: selected.chair.reasoning_effort || '' };
    draftRounds = selected.rounds || 2;
    creating = false;
    settingsOpen = false;
    if (navigate && window.location.pathname !== `/councils/${id}`) window.history.pushState({}, '', `/councils/${id}`);
    schedulePoll();
  }

  function schedulePoll() {
    if (poll) clearTimeout(poll);
    const ongoing = [...turns].reverse().find((turn) => turn.status === 'queued' || turn.status === 'running');
    if (!ongoing || !selected) return;
    const councilID = selected.id;
    const turnID = ongoing.id;
    poll = setTimeout(async () => {
      try {
        const updated = await request<Turn>(`/api/v1/councils/${encodeURIComponent(councilID)}/runs/${encodeURIComponent(turnID)}`);
        turns = turns.map((turn) => turn.id === turnID ? updated : turn);
        if (['queued', 'running'].includes(updated.status)) schedulePoll();
        else await loadCatalog();
      } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not read council progress'; }
    }, 1200);
  }

  function startCreate() {
    if (poll) { clearTimeout(poll); poll = null; }
    selected = null;
    turns = [];
    creating = true;
    settingsOpen = true;
    draftName = '';
    draftScope = 'independent';
    draftRoot = '';
    draftProject = '';
    const choices = concreteModels;
    draftMembers = choices.slice(0, 2).map((model) => ({ model: model.id, reasoning_effort: '' }));
    draftChair = { model: choices[0]?.id || '', reasoning_effort: '' };
    draftRounds = 2;
    window.history.pushState({}, '', '/councils');
  }

  function payload() {
    return { name: draftName.trim(), scope: draftScope, workspace_root: draftScope === 'workspace' ? draftRoot.trim() : '', project_id: draftScope === 'project' ? draftProject : '', members: draftMembers, chair: draftChair, rounds: draftRounds };
  }

  async function saveCouncil() {
    saving = true; error = '';
    try {
      const path = selected ? `/api/v1/councils/${encodeURIComponent(selected.id)}` : '/api/v1/councils';
      const saved = await request<Council>(path, selected ? 'PUT' : 'POST', payload());
      await loadCatalog();
      await openCouncil(saved.id);
    } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not save council'; }
    finally { saving = false; }
  }

  async function deleteCouncil() {
    if (!selected || !window.confirm(`Delete council “${selected.name}” and its conversation?`)) return;
    try {
      await request(`/api/v1/councils/${encodeURIComponent(selected.id)}`, 'DELETE');
      selected = null; turns = []; creating = false;
      window.history.pushState({}, '', '/councils');
      await loadCatalog();
    } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not delete council'; }
  }

  async function ask() {
    if (!selected || !prompt.trim()) return;
    error = '';
    try {
      const turn = await request<Turn>(`/api/v1/councils/${encodeURIComponent(selected.id)}/turns`, 'POST', { prompt: prompt.trim() });
      turns = [...turns, turn]; prompt = '';
      schedulePoll();
    } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not start council turn'; }
  }

  async function cancelTurn() {
    if (!selected || !activeTurn) return;
    try { await request(`/api/v1/councils/${encodeURIComponent(selected.id)}/runs/${encodeURIComponent(activeTurn.id)}/cancel`, 'POST'); }
    catch (cause) { error = cause instanceof Error ? cause.message : 'Could not cancel council turn'; }
  }

  async function retryTurn(turn: Turn, mode: 'resume' | 'rerun') {
    if (!selected || activeTurn || retrying) return;
    retrying = turn.id; error = '';
    try {
      const updated = await request<Turn>(`/api/v1/councils/${encodeURIComponent(selected.id)}/runs/${encodeURIComponent(turn.id)}/retry`, 'POST', { mode });
      turns = mode === 'resume' ? turns.map((item) => item.id === updated.id ? updated : item) : [...turns, updated];
      schedulePoll();
    } catch (cause) { error = cause instanceof Error ? cause.message : 'Could not restart council turn'; }
    finally { retrying = ''; }
  }

  function addMember() {
    const used = new Set(draftMembers.map((seat) => seat.model));
    const model = concreteModels.find((option) => !used.has(option.id));
    if (model && draftMembers.length < 8) draftMembers = [...draftMembers, { model: model.id, reasoning_effort: '' }];
  }

  function removeMember(index: number) { draftMembers = draftMembers.filter((_, at) => at !== index); }
  function moveMember(index: number, direction: -1 | 1) {
    const target = index + direction;
    if (target < 0 || target >= draftMembers.length) return;
    const members = [...draftMembers];
    [members[index], members[target]] = [members[target], members[index]];
    draftMembers = members;
  }

  function roundCount(turn: Turn): number { return turn.total_rounds || turn.rounds?.length || 2; }

  function currentRound(turn: Turn): number {
    if (turn.current_round) return turn.current_round;
    if (turn.stage === 'opinions') return 1;
    if (turn.stage === 'reviews') return 2;
    if (turn.stage === 'synthesis' || turn.stage === 'complete') return roundCount(turn);
    return 0;
  }

  function roundAt(turn: Turn, number: number): CouncilRound {
    const stored = turn.rounds?.[number - 1];
    if (stored?.number === number) return stored;
    if (!turn.rounds?.length && number === 1) return { number, responses: turn.responses };
    if (!turn.rounds?.length && number === 2) return { number, reviews: turn.reviews };
    return { number };
  }

  function visibleRounds(turn: Turn): CouncilRound[] {
    const count = Math.min(roundCount(turn), Math.max(1, currentRound(turn)));
    return Array.from({ length: count }, (_, index) => roundAt(turn, index + 1));
  }

  function opinions(turn: Turn, round: CouncilRound): Opinion[] {
    return turn.members.map((seat, index) => round.responses?.[index] || { label: String.fromCharCode(65 + index), model: seat.model });
  }

  function shownOpinion(turn: Turn, round: CouncilRound, selectedLabel?: string): Opinion | undefined {
    const rows = opinions(turn, round);
    return rows.find((row) => row.label === selectedLabel) || rows.find((row) => !!row.text || !!row.error) || rows[0];
  }

  function readyCount(items: { text?: string; error?: string }[] | undefined): number {
    return items?.filter((item) => !!item.text && !item.error).length || 0;
  }

  function hasAnswers(round: CouncilRound): boolean {
    return !!round.responses?.some((item) => !!item.text || !!item.error);
  }

  function reviewText(review: Review): string {
    const raw = review.text || '';
    const trimmed = raw.trim();
    const fenced = trimmed.match(/^```(?:json)?\s*\n?([\s\S]*?)\n?```$/i);
    try {
      const parsed = JSON.parse(fenced ? fenced[1] : trimmed);
      if (typeof parsed?.analysis === 'string' && parsed.analysis.trim()) return parsed.analysis.trim();
    } catch { /* Historical plain-text reviews need no conversion. */ }
    return raw;
  }

  function stageState(turn: Turn, number: number): 'done' | 'active' | 'pending' | 'stopped' {
    const current = turn.stage === 'synthesis' || turn.stage === 'complete' ? roundCount(turn) + 1 : currentRound(turn);
    if (turn.status === 'completed' || current > number) return 'done';
    if (current < number) return 'pending';
    return turn.status === 'running' ? 'active' : 'stopped';
  }

  onMount(() => {
    let mounted = true;
    void (async () => {
      try {
        await loadCouncilList();
        if (mounted) loading = false;
        const id = selectedFromPath();
        if (id) await openCouncil(id, false);
        await loadReferenceData();
      } catch (cause) { if (mounted) error = cause instanceof Error ? cause.message : 'Could not load councils'; }
      finally { if (mounted) loading = false; }
    })();
    const pop = () => {
      const id = selectedFromPath();
      if (id) void openCouncil(id, false);
      else { selected = null; creating = false; turns = []; }
    };
    window.addEventListener('popstate', pop);
    return () => { mounted = false; if (poll) clearTimeout(poll); window.removeEventListener('popstate', pop); };
  });
</script>

<section class="council-view">
  <aside class="council-list">
    <div class="list-heading"><h2>Councils</h2><button onclick={startCreate}>New</button></div>
    {#if loading}<p class="muted">Loading…</p>{/if}
    {#each councils as item}
      <button class:selected={selected?.id === item.id} class="council-item" onclick={() => { error = ''; void openCouncil(item.id).catch((cause) => error = String(cause)); }}>
        <strong>{item.name}</strong><small>{item.scope}{item.scope === 'project' ? ` · ${projects.find((project) => project.id === item.project_id)?.name || 'Project'}` : ''}</small>
      </button>
    {/each}
    {#if !loading && councils.length === 0}<p class="muted">Create a council to bring several models into one conversation.</p>{/if}
  </aside>

  <div class="council-content">
    {#if error}<p class="council-error" role="alert">{error}</p>{/if}
    {#if creating || selected}
      <div class="council-heading"><div><h2>{selected ? selected.name : 'New council'}</h2>{#if selected}<p>{selected.members.length} members · {selected.rounds || 2} rounds · {selected.scope} · Chair: {selected.chair.model}</p>{/if}</div>{#if selected}<div class="council-heading-actions"><details class="export-menu"><summary>Export history ▾</summary><div><a href={`/api/v1/councils/${encodeURIComponent(selected.id)}/export?format=md`}>Markdown</a><a href={`/api/v1/councils/${encodeURIComponent(selected.id)}/export?format=json`}>JSON</a></div></details><button onclick={() => { settingsOpen = !settingsOpen; }}>{settingsOpen ? 'Hide settings' : 'Configure'}</button><button class="danger" onclick={deleteCouncil} disabled={!!activeTurn}>Delete</button></div>{/if}</div>
      {#if creating || settingsOpen}<div class="council-settings">
        <label>Name <input bind:value={draftName} maxlength="120" /></label>
        <label>Scope <select bind:value={draftScope} disabled={!!selected}><option value="independent">Independent</option><option value="workspace">Workspace</option><option value="project">Project</option></select></label>
        {#if draftScope === 'workspace'}<label class="full">Workspace directory <input bind:value={draftRoot} placeholder="Absolute repository path" disabled={!!selected} /></label>{/if}
        {#if draftScope === 'project'}<label class="full">Studio project <select bind:value={draftProject} disabled={!!selected}><option value="">Select a project</option>{#each projects as project}<option value={project.id}>{project.name}</option>{/each}</select></label>{/if}
        <div class="full seat-heading"><strong>Participating models</strong><button onclick={addMember} disabled={draftMembers.length >= 8}>Add model</button></div>
        {#each draftMembers as seat, index}
          <div class="full seat-row"><span>{index + 1}</span><select bind:value={seat.model} onchange={() => { seat.reasoning_effort = ''; draftMembers = [...draftMembers]; }} aria-label={`Model for member ${index + 1}`}>{#each concreteModels as model}<option value={model.id} disabled={draftMembers.some((other, at) => at !== index && other.model === model.id)}>{model.id}</option>{/each}</select><select bind:value={seat.reasoning_effort} aria-label={`Reasoning for member ${index + 1}`}><option value="">Provider default</option>{#each reasoningOptions(seat.model, seat.reasoning_effort || '', models) as effort}<option value={effort}>{effort}</option>{/each}</select><button onclick={() => moveMember(index, -1)} disabled={index === 0} aria-label="Move up">↑</button><button onclick={() => moveMember(index, 1)} disabled={index === draftMembers.length - 1} aria-label="Move down">↓</button><button onclick={() => removeMember(index)} disabled={draftMembers.length <= 2} aria-label="Remove member">×</button></div>
        {/each}
        <label>Chair model <select bind:value={draftChair.model} onchange={() => { draftChair.reasoning_effort = ''; draftChair = { ...draftChair }; }}>{#each concreteModels as model}<option value={model.id}>{model.id}</option>{/each}</select></label>
        <label>Chair reasoning <select bind:value={draftChair.reasoning_effort}><option value="">Provider default</option>{#each reasoningOptions(draftChair.model, draftChair.reasoning_effort || '', models) as effort}<option value={effort}>{effort}</option>{/each}</select></label>
        <label>Rounds <select bind:value={draftRounds}>{#each [1, 2, 3, 4, 5, 6] as count}<option value={count}>{count}</option>{/each}</select></label>
        <p class="rounds-note">Round 1: independent answers · Round 2: peer reviews · Later rounds: integrated reviews of answers and feedback. At least {draftMembers.length * draftRounds + 1} model calls including the chair; later prompts also include previous feedback.</p>
        <div class="full actions"><button class="primary" onclick={saveCouncil} disabled={saving || concreteModels.length < 2}>{saving ? 'Saving…' : selected ? 'Save settings' : 'Create council'}</button></div>
      </div>{/if}
      {#if selected}
        <div class="council-conversation">
          <h3>Conversation</h3>
          {#if activeTurn}
            <div class="ask-box running-controls" role="status"><span>Council is working · {activeTurn.stage === 'synthesis' ? 'Chair synthesis' : `Round ${currentRound(activeTurn)} / ${roundCount(activeTurn)}`}</span><button onclick={cancelTurn}>Cancel turn</button></div>
          {:else}
            <div class="ask-box"><textarea bind:value={prompt} placeholder="Ask this council…" rows="3" aria-label="Ask this council"></textarea><div><button class="primary" onclick={ask} disabled={!prompt.trim()}>Ask council</button></div></div>
          {/if}
          {#each [...turns].reverse() as turn}
            <article class="council-turn">
              <div class="turn-meta"><span>{new Date(turn.created_at).toLocaleString()}{turn.rerun_of ? ' · Rerun' : ''}</span><div class="turn-actions"><span class="turn-status">{turn.status === 'running' ? 'Deliberating' : turn.status}</span>{#if !['queued', 'running'].includes(turn.status)}{#if ['failed', 'cancelled', 'interrupted'].includes(turn.status)}<button onclick={() => retryTurn(turn, 'resume')} disabled={!!activeTurn || !!retrying}>Resume saved progress</button>{/if}<button onclick={() => retryTurn(turn, 'rerun')} disabled={!!activeTurn || !!retrying} title="Create a new turn with the same question and model settings">Rerun turn</button>{/if}</div></div>
              <h4 class="turn-question">{turn.prompt}</h4>
              <div class="stage-track" aria-label="Council stages">
                {#each Array.from({ length: roundCount(turn) }, (_, index) => index + 1) as number}
                  {@const round = roundAt(turn, number)}
                  <div class:done={stageState(turn, number) === 'done'} class:active={stageState(turn, number) === 'active'} class:failed={turn.status === 'failed' && turn.stage !== 'synthesis' && currentRound(turn) === number}><span class="stage-number">{number}</span><span>{number === 1 ? 'Independent opinions' : number === 2 ? 'Peer review' : 'Integrated review'}<small>{readyCount(number === 1 ? round.responses : round.reviews)} / {turn.members.length} finished</small></span></div>
                {/each}
                <div class:done={stageState(turn, roundCount(turn) + 1) === 'done'} class:active={stageState(turn, roundCount(turn) + 1) === 'active'} class:failed={turn.status === 'failed' && turn.stage === 'synthesis'}><span class="stage-number">★</span><span>Chair synthesis<small>{turn.status === 'failed' && turn.stage === 'synthesis' ? `Failed · ${turn.chair.model}` : turn.final ? turn.chair.model : turn.stage === 'synthesis' ? 'Writing answer' : 'Waiting'}</small></span></div>
              </div>
              {#if turn.error}<section class="turn-failure" role="alert" aria-label="Council turn error"><strong>{turn.status === 'failed' && turn.stage === 'synthesis' ? 'Chair synthesis failed' : turn.status === 'cancelled' ? 'Council turn cancelled' : 'Council turn failed'}</strong><p>{turn.error}</p></section>{/if}
              {#if turn.final}<section class="final-answer" aria-label="Chair synthesis"><div class="section-caption"><span>CHAIR'S ANSWER</span><small>{turn.chair.model}</small></div><Markdown content={turn.final} /></section>{/if}
              {#each visibleRounds(turn) as round}
                {@const opinion = shownOpinion(turn, round, selectedAnswers[`${turn.id}-${round.number}`])}
                <section class="deliberation-section">
                  <div class="section-caption"><span>{String(round.number).padStart(2, '0')} · {round.number === 1 ? 'INDEPENDENT OPINIONS' : round.number === 2 ? 'PEER REVIEW' : 'INTEGRATED REVIEW'}</span><small>{round.number === 1 ? 'Each model answered independently' : round.number === 2 ? 'Anonymous evaluation of first answers' : 'Reviewing prior answers and feedback'}</small></div>
                  {#if round.number === 1 || hasAnswers(round)}
                    <div class="opinion-tabs" role="tablist" aria-label={`Round ${round.number} answers for ${turn.prompt}`}>
                      {#each opinions(turn, round) as answer}
                        <button role="tab" aria-selected={opinion?.label === answer.label} class:active={opinion?.label === answer.label} onclick={() => { selectedAnswers = { ...selectedAnswers, [`${turn.id}-${round.number}`]: answer.label }; }}>
                          <strong>{answer.label}</strong><span>{answer.model}</span><small>{answer.error ? 'Failed' : answer.text ? 'Ready' : 'Waiting'}</small>
                        </button>
                      {/each}
                    </div>
                    {#if opinion}
                      <div class="opinion-panel" role="tabpanel">
                        <div class="opinion-title"><strong>Answer {opinion.label}</strong><span>{opinion.model}</span></div>
                        {#if opinion.error}<p class="council-error">{opinion.error}</p>{:else if opinion.text}<Markdown content={opinion.text} />{:else}<p class="muted">Waiting for this model's answer…</p>{/if}
                      </div>
                    {/if}
                  {/if}
                  {#if round.number >= 2}
                    <div class="review-list">
                      {#each round.reviews || [] as review}
                        <details><summary><strong>{review.model}</strong><span>{review.error ? 'Failed' : review.text ? 'Reviewed' : 'Reviewing…'}</span></summary>
                          {#if review.error}<p class="council-error">{review.error}</p>{:else if review.text}<Markdown content={reviewText(review)} />{:else}<p class="muted">Waiting for peer review…</p>{/if}
                        </details>
                      {/each}
                    </div>
                  {/if}
                </section>
              {/each}
              {#if turn.status === 'running' && turn.stage === 'synthesis' && !turn.final}<div class="synthesis-pending">Chair {turn.chair.model} is comparing the answers and reviews…</div>{/if}
            </article>
          {/each}
          {#if turns.length === 0}<p class="muted">Ask the council its first question.</p>{/if}
        </div>
      {/if}
    {:else}
      <div class="council-empty"><h2>Choose a council</h2><p>Independent councils stay in your global Q folder. Workspace and project councils can read their assigned repositories.</p><button class="primary" onclick={startCreate}>Create council</button></div>
    {/if}
  </div>
</section>

<style>
  .council-view { display: grid; grid-template-columns: 260px minmax(0, 1fr); gap: 22px; min-height: calc(100vh - 160px); }
  .council-list { border-right: 1px solid var(--border); padding: 20px 14px 20px 0; }
  .list-heading, .council-heading, .seat-heading, .turn-meta, .ask-box > div { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .council-heading { align-items: flex-start; }
  .council-heading h2 { margin: 0; }
  .council-heading p { margin: 6px 0 0; color: var(--muted); font-size: 12px; }
  .council-heading-actions { display: flex; gap: 8px; }
  .export-menu { position: relative; z-index: 2; }
  .export-menu summary { padding: 8px 11px; border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface-raised); cursor: pointer; list-style: none; white-space: nowrap; }
  .export-menu summary::-webkit-details-marker { display: none; }
  .export-menu > div { position: absolute; right: 0; display: grid; min-width: 150px; margin-top: 5px; padding: 5px; border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface-raised); box-shadow: 0 8px 22px rgba(0, 0, 0, .28); }
  .export-menu a { padding: 8px 10px; border-radius: 4px; color: var(--text); text-decoration: none; }
  .export-menu a:hover { background: var(--violet-soft); }
  .council-item { display: flex; flex-direction: column; width: 100%; margin: 8px 0; padding: 12px; text-align: left; }
  .council-item.selected { border-color: var(--violet); background: var(--violet-soft); }
  .council-item small, .muted, .turn-meta { color: var(--muted); }
  .council-content { min-width: 0; padding: 20px 0 40px; }
  .council-settings { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; margin: 20px 0 32px; padding: 18px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); }
  .council-settings label { display: flex; flex-direction: column; gap: 6px; color: var(--muted); font-size: 13px; }
  .rounds-note { align-self: end; margin: 0; color: var(--muted); font-size: 12px; line-height: 1.5; }
  .full { grid-column: 1 / -1; }
  input, select, textarea { width: 100%; min-width: 0; padding: 9px 10px; color: var(--text); border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface-raised); }
  button { padding: 8px 11px; color: var(--text); border: 1px solid var(--border-strong); border-radius: 6px; background: var(--surface-raised); cursor: pointer; }
  button:disabled { opacity: .48; cursor: default; }
  button.primary { border-color: var(--violet); background: var(--violet); color: white; }
  button.danger, .council-error { color: #ff9caa; }
  .seat-row { display: grid; grid-template-columns: 22px minmax(0, 2fr) minmax(100px, 1fr) repeat(3, 34px); gap: 7px; align-items: center; }
  .seat-row button { padding: 7px 0; }
  .actions { display: flex; justify-content: flex-end; }
  .council-conversation { display: grid; gap: 14px; }
  .council-turn { padding: 18px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); }
  .turn-question { margin: 12px 0 20px; font-size: 18px; line-height: 1.5; }
  .turn-meta { font-size: 12px; }
  .turn-status { color: var(--violet); text-transform: capitalize; }
  .turn-actions { display: flex; flex-wrap: wrap; align-items: center; justify-content: flex-end; gap: 8px; }
  .turn-actions button { padding: 5px 8px; font-size: 12px; }
  .stage-track { display: grid; grid-template-columns: repeat(auto-fit, minmax(155px, 1fr)); gap: 8px; margin-bottom: 20px; }
  .stage-track > div { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 10px; border: 1px solid var(--border); border-radius: 7px; color: var(--muted); font-size: 12px; }
  .stage-track > div.active { border-color: var(--violet); background: var(--violet-soft); color: var(--text); }
  .stage-track > div.failed { border-color: #a94e60; color: #ff9caa; }
  .stage-track > div.done { color: var(--text); }
  .stage-track small { display: block; margin-top: 3px; color: var(--muted); }
  .stage-number { display: grid; flex: none; place-items: center; width: 25px; height: 25px; border: 1px solid var(--border-strong); border-radius: 50%; }
  .stage-track .active .stage-number, .stage-track .done .stage-number { border-color: var(--violet); color: var(--violet); }
  .section-caption { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; margin-bottom: 12px; color: var(--violet); font-size: 11px; font-weight: 700; letter-spacing: .08em; }
  .section-caption small { color: var(--muted); font-size: 12px; font-weight: 400; letter-spacing: 0; text-align: right; }
  .final-answer { margin-bottom: 20px; padding: 18px; border: 1px solid var(--violet); border-left-width: 3px; border-radius: 7px; background: var(--surface-raised); }
  .turn-failure { margin-bottom: 20px; padding: 14px 16px; border: 1px solid #a94e60; border-left-width: 3px; border-radius: 7px; background: var(--surface-raised); color: #ff9caa; }
  .turn-failure p { margin: 8px 0 0; overflow-wrap: anywhere; font-family: ui-monospace, SFMono-Regular, Consolas, monospace; font-size: 12px; white-space: pre-wrap; }
  .deliberation-section { margin-top: 20px; }
  .opinion-tabs { display: flex; gap: 7px; overflow-x: auto; padding-bottom: 8px; }
  .opinion-tabs button { display: flex; flex: 1 0 155px; flex-wrap: wrap; align-items: baseline; gap: 4px 8px; text-align: left; }
  .opinion-tabs button.active { border-color: var(--violet); background: var(--violet-soft); }
  .opinion-tabs button strong { color: var(--violet); }
  .opinion-tabs button span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 12px; }
  .opinion-tabs button small { flex-basis: 100%; color: var(--muted); }
  .opinion-panel { overflow-x: auto; min-height: 92px; padding: 16px; border: 1px solid var(--border); border-radius: 7px; background: var(--surface-raised); }
  .opinion-title { display: flex; align-items: baseline; gap: 9px; margin-bottom: 12px; color: var(--muted); font-size: 12px; }
  .opinion-title strong { color: var(--text); font-size: 13px; }
  .review-list { display: grid; gap: 6px; }
  .review-list details { padding: 10px 12px; border: 1px solid var(--border); border-radius: 6px; }
  .review-list summary { display: flex; justify-content: space-between; gap: 8px; cursor: pointer; font-size: 12px; }
  .review-list summary span { color: var(--muted); }
  .synthesis-pending { margin-top: 20px; padding: 14px; border: 1px dashed var(--violet); border-radius: 7px; color: var(--muted); font-size: 13px; }
  .ask-box { display: grid; gap: 9px; padding: 12px; border: 1px solid var(--border); border-radius: 8px; background: var(--surface); }
  .ask-box > div { justify-content: flex-end; }
  .running-controls { display: flex; align-items: center; justify-content: space-between; color: var(--muted); }
  .council-empty { display: grid; justify-items: start; gap: 12px; margin: 30px 0; }
  @media (max-width: 900px) { .council-view { grid-template-columns: 1fr; } .council-list { border-right: 0; border-bottom: 1px solid var(--border); } .council-settings { grid-template-columns: 1fr; } .seat-row { grid-template-columns: 22px 1fr repeat(3, 32px); } .seat-row select:nth-of-type(2) { grid-column: 2 / -1; grid-row: 2; } }
  @media (max-width: 600px) { .stage-track { grid-template-columns: 1fr; } .section-caption { flex-direction: column; align-items: flex-start; } }
</style>
