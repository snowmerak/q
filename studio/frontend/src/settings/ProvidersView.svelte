<script lang="ts">
  import { onMount } from 'svelte';
  import ProvidersPanel from './ProvidersPanel.svelte';
  import ACPProvidersPanel from './ACPProvidersPanel.svelte';
  import type { QueueSave } from './persistence';
  import type { SettingsSnapshot } from './types';

  export let settings: SettingsSnapshot;
  export let queueSave: QueueSave;
  export let active = false;
  let tab = new URL(window.location.href).searchParams.get('provider_tab') === 'acp' ? 'acp' : 'api';
  let acpOpened = tab === 'acp';
  $: if (active && tab === 'acp') acpOpened = true;
  function choose(value: string) {
    tab = value;
    const url = new URL(window.location.href);
    url.searchParams.set('provider_tab', value);
    window.history.pushState({}, '', url.pathname + url.search);
  }
  function tabKeydown(event: KeyboardEvent) {
    if (!['ArrowLeft', 'ArrowRight', 'Home', 'End'].includes(event.key)) return;
    event.preventDefault();
    const value = event.key === 'Home' ? 'api' : event.key === 'End' ? 'acp' : tab === 'api' ? 'acp' : 'api';
    choose(value);
    document.getElementById(`provider-tab-${value}`)?.focus();
  }
  onMount(() => {
    const sync = () => tab = new URL(window.location.href).searchParams.get('provider_tab') === 'acp' ? 'acp' : 'api';
    window.addEventListener('popstate', sync);
    return () => window.removeEventListener('popstate', sync);
  });
</script>

<div class="integration-tabs" role="tablist" aria-label="Provider types">
  <button id="provider-tab-api" role="tab" aria-controls="provider-panel-api" aria-selected={tab === 'api'} tabindex={tab === 'api' ? 0 : -1} class:active={tab === 'api'} onclick={() => choose('api')} onkeydown={tabKeydown}><strong>API Provider</strong><small>Model APIs and gateways</small></button>
  <button id="provider-tab-acp" role="tab" aria-controls="provider-panel-acp" aria-selected={tab === 'acp'} tabindex={tab === 'acp' ? 0 : -1} class:active={tab === 'acp'} onclick={() => choose('acp')} onkeydown={tabKeydown}><strong>ACP</strong><small>External agent connections</small></button>
</div>
<div id="provider-panel-api" role="tabpanel" aria-labelledby="provider-tab-api" hidden={tab !== 'api'}><ProvidersPanel bind:settings {queueSave} /></div>
<div id="provider-panel-acp" role="tabpanel" aria-labelledby="provider-tab-acp" hidden={tab !== 'acp'}>{#if acpOpened}<ACPProvidersPanel />{/if}</div>
