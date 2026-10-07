<script lang="ts">
  import { onDestroy } from 'svelte';
  import { requestJSON } from '../api';

  export let providerID: string;
  export let enabled: boolean;
  type Profile = { id: string; label: string; verified: boolean; connected: boolean; plan_enabled: boolean };
  type Status = { active_profile: string; profiles: Profile[]; pending: boolean; error?: string };
  let status: Status | null = null;
  let error = '';
  let busy = false;
  let observedID = '';
  let timer: ReturnType<typeof setTimeout> | undefined;
  let destroyed = false;
  let controller: AbortController | undefined;
  $: active = status?.profiles.find((profile) => profile.id === status?.active_profile);
  $: if (providerID && enabled && providerID !== observedID) { observedID = providerID; void refresh(); }
  $: if (!enabled) { observedID = ''; error = ''; clearTimeout(timer); controller?.abort(); }

  onDestroy(() => { destroyed = true; clearTimeout(timer); controller?.abort(); });
  function endpoint() { return `/api/v1/settings/gateway/providers/${encodeURIComponent(providerID)}/chatgpt`; }
  async function refresh() {
    clearTimeout(timer);
    controller?.abort();
    const pending = new AbortController();
    const requestedID = providerID;
    controller = pending;
    try {
      const result = await requestJSON<Status>('GET', endpoint(), undefined, pending.signal);
      if (destroyed || pending.signal.aborted || requestedID !== providerID) return;
      status = result;
      error = result.error || '';
      if (result.pending) timer = setTimeout(refresh, 2000);
    } catch (failure) {
      if (!destroyed && !pending.signal.aborted && requestedID === providerID) error = failure instanceof Error ? failure.message : 'Could not read ChatGPT connection';
    }
  }
  async function connect(newAccount = false) {
    // Open synchronously so browsers allow the sign-in tab after the request.
    const tab = window.open('about:blank', '_blank');
    if (tab) tab.opener = null;
    busy = true; error = '';
    try {
      const result = await requestJSON<{ authorization_url: string }>('POST', `${endpoint()}/login`, { new_account: newAccount });
      const destination = new URL(result.authorization_url);
      if (destination.origin !== 'https://auth.openai.com') throw new Error('Unexpected sign-in destination');
      if (tab) tab.location.href = result.authorization_url;
      else window.location.assign(result.authorization_url);
      await refresh();
    } catch (failure) {
      tab?.close(); error = failure instanceof Error ? failure.message : 'Could not start sign-in';
    } finally { busy = false; }
  }
  async function action(name: string, body: object = {}) {
    busy = true; error = '';
    try { await requestJSON('POST', `${endpoint()}/${name}`, body); await refresh(); }
    catch (failure) { error = failure instanceof Error ? failure.message : 'ChatGPT account action failed'; }
    finally { busy = false; }
  }
</script>

<div class="chatgpt-connection">
  <p>{status?.pending ? 'Complete sign-in in the browser.' : active?.connected ? (active.plan_enabled ? 'ChatGPT plan connected to Q' : 'Account connected · plan permission required') : 'Connect your ChatGPT account to Q.'}</p>
  {#if status && status.profiles.length > 0}
    <label><span>ChatGPT account used by Q</span><select value={status.active_profile} disabled={busy || status.pending || !enabled} onchange={(event) => action('select', { profile: event.currentTarget.value })}>
      {#if !status.active_profile}<option value="">Choose a verified account</option>{/if}
      {#each status.profiles as profile}<option value={profile.id} disabled={!profile.verified}>{profile.label}</option>{/each}
    </select></label>
  {/if}
  <div class="provider-actions">
    <button class="primary-button" onclick={() => connect()} disabled={!enabled || busy || status?.pending}>{active?.connected ? 'Reconnect with ChatGPT' : 'Continue with ChatGPT'}</button>
    {#if status && status.profiles.length > 0}<button class="text-button" onclick={() => connect(true)} disabled={!enabled || busy || status.pending}>Add another account</button>{/if}
    {#if active?.connected}<button class="text-button" onclick={() => action('disconnect')} disabled={!enabled || busy || status?.pending}>Disconnect</button>{/if}
    <button class="text-button" onclick={refresh} disabled={!enabled || busy}>Refresh connection</button>
    <a href="https://chatgpt.com/settings/usage" target="_blank" rel="noopener noreferrer">Manage ChatGPT usage</a>
  </div>
  {#if error}<p role="alert">{error}</p>{/if}
</div>

<style>
  .chatgpt-connection { display: grid; gap: 12px; padding: 0 20px 20px; }
  p { margin: 0; color: var(--muted); font-size: calc(13px * var(--text-scale)); line-height: 1.5; }
  label { display: grid; gap: 8px; min-width: 0; color: var(--muted); font-size: calc(13px * var(--text-scale)); }
  .provider-actions { flex-wrap: wrap; gap: 8px; }
  a { color: var(--muted); font-size: calc(13px * var(--text-scale)); text-underline-offset: 3px; }
  [role='alert'] { color: #e5a1a1; overflow-wrap: anywhere; }
</style>
