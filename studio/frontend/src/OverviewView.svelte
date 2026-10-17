<script lang="ts">
  import { ArrowRight, FolderOpen, GitBranch, MessageSquare, MessageSquarePlus, Settings } from '@lucide/svelte';
  import { onMount } from 'svelte';
  import { requestJSON } from './api';
  import { formatSessionTime } from './sessions/format';
  import type { RegisteredSessionTree } from './sessions/types';

  export let onopen: (path: string) => void;

  let sessions: RegisteredSessionTree[] = [];
  let loading = true;
  let error = '';

  async function loadSessions() {
    loading = true;
    error = '';
    try {
      const result = await requestJSON<{ sessions: RegisteredSessionTree[] }>('GET', '/api/v1/registered-sessions');
      sessions = result.sessions.filter(session => !session.issue)
        .sort((a, b) => Date.parse(b.session.updated_at) - Date.parse(a.session.updated_at)).slice(0, 5);
    } catch (cause) {
      error = cause instanceof Error ? cause.message : 'Could not load recent sessions';
    } finally {
      loading = false;
    }
  }

  onMount(() => { void loadSessions(); });
</script>

<div class="overview-content">
  <section class="overview-welcome" aria-labelledby="welcome-heading">
    <div class="welcome-mark" aria-hidden="true">Q</div>
    <h2 id="welcome-heading">What will you work on?</h2>
    <p>Start a conversation, work in a repository, or pick up where you left off.</p>
    <div class="overview-actions">
      <button class="primary-button" onclick={() => onopen('/sessions?new=chat')}><MessageSquarePlus aria-hidden="true" size={17} />New chat</button>
      <button class="secondary-button" onclick={() => onopen('/sessions?new=workspace')}><FolderOpen aria-hidden="true" size={17} />Open a workspace</button>
    </div>
  </section>

  <section class="recent-sessions" aria-labelledby="recent-heading" aria-busy={loading}>
    <div class="overview-section-heading"><h2 id="recent-heading">Recent sessions</h2><button class="text-button" onclick={() => onopen('/sessions')}>View all<ArrowRight aria-hidden="true" size={14} /></button></div>
    {#if loading}
      <p class="overview-notice" role="status">Loading recent sessions…</p>
    {:else if error}
      <div class="overview-notice" role="alert"><p>{error}</p><button class="text-button" onclick={loadSessions}>Try again</button></div>
    {:else if sessions.length}
      <div class="recent-session-list">
        {#each sessions as registration}
          <button class="recent-session" onclick={() => onopen(`/sessions/${encodeURIComponent(registration.registration_id)}`)}>
            <span class="recent-session-icon" aria-hidden="true">{#if registration.kind === 'chat'}<MessageSquare size={18} />{:else}<GitBranch size={18} />{/if}</span>
            <span class="recent-session-description"><strong>{registration.session.title || (registration.kind === 'chat' ? 'New chat' : 'New session')}</strong><small title={registration.workspace_root}>{registration.kind === 'chat' ? 'Personal chat' : registration.project_name || registration.workspace_root}</small></span>
            <time datetime={registration.session.updated_at}>{formatSessionTime(registration.session.updated_at)}</time>
            <ArrowRight class="recent-session-arrow" aria-hidden="true" size={16} />
          </button>
        {/each}
      </div>
    {:else}
      <div class="overview-notice recent-empty"><MessageSquare aria-hidden="true" size={23} /><strong>Your next idea starts here</strong><p>Your chats and workspace sessions will appear here, ready to continue.</p></div>
    {/if}
  </section>

  <button class="overview-setup" onclick={() => onopen('/settings?section=providers')}><Settings aria-hidden="true" size={18} /><span><strong>Set up your models</strong><small>Connect a provider and choose the models Q uses.</small></span><ArrowRight aria-hidden="true" size={16} /></button>
</div>

<style>
  .overview-content { width: min(100%, 900px); margin: 0 auto; padding: 48px 0 30px; }
  .overview-welcome { padding: 12px 0 44px; }
  .welcome-mark { display: grid; width: 48px; height: 48px; place-items: center; margin-bottom: 24px; border: 1px solid var(--border-strong); border-radius: 13px; background: var(--violet-soft); color: var(--accent-text); font-size: calc(29px * var(--text-scale)); font-weight: 650; }
  .overview-welcome h2 { font-size: clamp(calc(26px * var(--text-scale)), calc(3vw * var(--text-scale)), calc(36px * var(--text-scale))); font-weight: 600; letter-spacing: -.035em; line-height: 1.2; }
  .overview-welcome p { max-width: 540px; margin-top: 12px; color: var(--muted); line-height: 1.7; }
  .overview-actions { display: flex; flex-wrap: wrap; gap: 10px; margin-top: 26px; }
  .overview-actions button { display: inline-flex; min-height: 42px; align-items: center; justify-content: center; gap: 9px; padding-inline: 17px; border-radius: 7px; font-size: calc(14px * var(--text-scale)); }
  .overview-actions .primary-button { color: var(--on-accent); border-color: var(--violet); background: var(--violet); }
  .overview-actions .primary-button:hover { filter: brightness(1.1); }
  .overview-section-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding-bottom: 13px; border-bottom: 1px solid var(--border); }
  .overview-section-heading h2 { font-size: calc(15px * var(--text-scale)); font-weight: 600; }
  .overview-section-heading .text-button { display: inline-flex; align-items: center; gap: 8px; font-size: calc(13px * var(--text-scale)); }
  .recent-session { display: flex; width: 100%; min-width: 0; align-items: center; gap: 14px; padding: 17px 10px; color: var(--text); border: 0; border-bottom: 1px solid var(--border); background: transparent; text-align: left; cursor: pointer; }
  .recent-session:hover { background: var(--hover-bg); }
  .recent-session-icon { display: grid; width: 36px; height: 36px; flex: none; place-items: center; border: 1px solid var(--border); border-radius: 8px; color: var(--muted); }
  .recent-session-description { display: grid; min-width: 0; flex: 1; gap: 4px; }
  .recent-session-description strong { overflow: hidden; font-size: calc(14px * var(--text-scale)); font-weight: 550; text-overflow: ellipsis; white-space: nowrap; }
  .recent-session-description small { overflow: hidden; color: var(--subtle); font-size: calc(12px * var(--text-scale)); text-overflow: ellipsis; white-space: nowrap; }
  .recent-session time { flex: none; color: var(--subtle); font-size: calc(12px * var(--text-scale)); }
  .recent-session :global(.recent-session-arrow) { flex: none; color: var(--subtle); }
  .overview-notice { display: grid; min-height: 150px; place-items: center; align-content: center; gap: 10px; padding: 25px 16px; color: var(--muted); font-size: calc(14px * var(--text-scale)); text-align: center; }
  .recent-empty { min-height: 190px; }
  .recent-empty strong { color: var(--text); font-weight: 500; }
  .recent-empty p { max-width: 380px; line-height: 1.6; }
  .overview-setup { display: flex; width: 100%; align-items: center; gap: 14px; margin-top: 30px; padding: 20px 0; color: var(--subtle); border: 0; background: transparent; text-align: left; cursor: pointer; }
  .overview-setup:hover { color: var(--accent-text); }
  .overview-setup span { display: grid; min-width: 0; flex: 1; gap: 4px; }
  .overview-setup strong { color: var(--muted); font-size: calc(14px * var(--text-scale)); font-weight: 500; }
  .overview-setup small { font-size: calc(12px * var(--text-scale)); }
  @media (max-width: 760px) {
    .overview-content { padding-top: 24px; }
    .overview-welcome { padding-bottom: 32px; }
    .recent-session { flex-wrap: wrap; gap: 10px; }
    .recent-session time { width: 100%; margin-left: 46px; }
    .recent-session :global(.recent-session-arrow) { display: none; }
    .overview-setup { margin-top: 16px; }
  }
</style>
