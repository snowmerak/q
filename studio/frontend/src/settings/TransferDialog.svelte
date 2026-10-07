<script lang="ts">
  import { Download, Upload, X } from '@lucide/svelte';
  import { onMount, tick } from 'svelte';
  import { requestJSON } from '../api';
  import { maximumTransferSize, parseSettingsBundle, selectedBundle, transferKey, transferLabel, transferSections, transferSummary } from './transfer';
  import type { SettingsBundle, TransferPreview, TransferSection } from './transfer';

  export let mode: 'import' | 'export';
  export let onimport: (bundle: SettingsBundle) => Promise<string | undefined>;
  export let onclose: () => void;
  export let oncomplete: (message: string) => void;

  let dialog: HTMLDialogElement;
  let source: SettingsBundle | null = null;
  let current: SettingsBundle | null = null;
  let selected = new Set<string>();
  let section: TransferSection = 'models';
  let busy = true;
  let error = '';
  let fileName = '';
  let preview: TransferPreview | null = null;
  $: items = Object.entries(source?.sections[section] ?? {}).sort(([left], [right]) => left.localeCompare(right));
  $: selectedCount = selected.size;
  $: allInSection = items.length > 0 && items.every(([id]) => selected.has(transferKey(section, id)));
  $: listenerChanges = preview?.changes.some((item) => item.section === 'services') ?? false;

  onMount(() => {
    dialog.showModal();
    void loadCurrent();
    return () => dialog.close();
  });

  async function loadCurrent() {
    busy = true;
    error = '';
    try {
      current = await requestJSON<SettingsBundle>('GET', '/api/v1/settings/transfer');
      if (mode === 'export') source = current;
    } catch (reason) {
      error = reason instanceof Error ? reason.message : 'Could not load settings';
    } finally { busy = false; }
  }

  function invalidateReview() { preview = null; error = ''; }

  function toggle(id: string, checked: boolean) {
    const next = new Set(selected);
    if (checked) next.add(transferKey(section, id)); else next.delete(transferKey(section, id));
    selected = next;
    invalidateReview();
  }

  function toggleSection() {
    const next = new Set(selected);
    for (const [id] of items) {
      if (allInSection) next.delete(transferKey(section, id)); else next.add(transferKey(section, id));
    }
    selected = next;
    invalidateReview();
  }

  function selectAll() {
    selected = new Set(transferSections.flatMap((tab) => Object.keys(source?.sections[tab.id] ?? {}).map((id) => transferKey(tab.id, id))));
    invalidateReview();
  }

  async function readFile(event: Event) {
    const input = event.currentTarget as HTMLInputElement;
    const file = input.files?.[0];
    if (!file) return;
    source = null;
    selected = new Set();
    fileName = '';
    invalidateReview();
    busy = true;
    try {
      if (file.size > maximumTransferSize) throw new Error('Choose a settings file smaller than 4 MiB.');
      source = parseSettingsBundle(await file.text());
      fileName = file.name;
      section = transferSections.find((tab) => Object.keys(source?.sections[tab.id] ?? {}).length > 0)?.id ?? 'models';
      if (!transferSections.some((tab) => Object.keys(source?.sections[tab.id] ?? {}).length)) throw new Error('This file contains no settings.');
    } catch (reason) {
      source = null;
      error = reason instanceof Error ? reason.message : 'Could not read settings file';
      input.value = '';
    } finally { busy = false; }
  }

  function download() {
    if (!source || !selectedCount) return;
    const payload = JSON.stringify(selectedBundle(source, selected), null, 2) + '\n';
    if (new Blob([payload]).size > maximumTransferSize) { error = 'The selection exceeds 4 MiB. Export fewer settings in each file.'; return; }
    const url = URL.createObjectURL(new Blob([payload], { type: 'application/json' }));
    const link = document.createElement('a');
    link.href = url;
    link.download = 'q-settings.json';
    link.click();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    oncomplete(`Exported ${selectedCount} settings to q-settings.json.`);
  }

  async function review() {
    if (!source || !selectedCount) return;
    busy = true;
    error = '';
    try {
      preview = await requestJSON<TransferPreview>('POST', '/api/v1/settings/transfer/preview', selectedBundle(source, selected));
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Could not review settings'; }
    finally { busy = false; }
  }

  async function apply() {
    if (!source || !preview) return;
    busy = true;
    error = '';
    try {
      const warning = await onimport(selectedBundle(source, selected));
      oncomplete(warning || `Imported ${selectedCount} settings. Unselected settings were preserved.`);
    } catch (reason) { error = reason instanceof Error ? reason.message : 'Could not import settings'; }
    finally { busy = false; }
  }

  function actionFor(id: string, review: TransferPreview | null): string {
    const change = review?.changes.find((item) => item.section === section && item.id === id);
    if (change) return change.action === 'replace' ? 'Replace' : change.action === 'add' ? 'Add' : 'Unchanged';
    return current?.sections[section] && Object.hasOwn(current.sections[section]!, id) ? 'Existing item' : 'New item';
  }

  function currentValue(id: string, review: TransferPreview | null): string {
    const change = review?.changes.find((item) => item.section === section && item.id === id);
    if (change) return change.action === 'add' ? 'No existing item' : JSON.stringify(change.current, null, 2);
    return current?.sections[section] && Object.hasOwn(current.sections[section]!, id) ? JSON.stringify(current.sections[section]![id], null, 2) : 'No existing item';
  }

  async function navigateTabs(event: KeyboardEvent) {
    const index = transferSections.findIndex((tab) => tab.id === section);
    let next = index;
    if (event.key === 'ArrowRight') next = (index + 1) % transferSections.length;
    else if (event.key === 'ArrowLeft') next = (index + transferSections.length - 1) % transferSections.length;
    else if (event.key === 'Home') next = 0;
    else if (event.key === 'End') next = transferSections.length - 1;
    else return;
    event.preventDefault();
    const tabs = (event.currentTarget as HTMLElement).parentElement;
    section = transferSections[next].id;
    await tick();
    tabs?.querySelectorAll<HTMLButtonElement>('[role=tab]')[next]?.focus();
  }
</script>

<dialog bind:this={dialog} class="transfer-dialog" aria-labelledby="transfer-title" oncancel={(event) => { event.preventDefault(); if (!busy) onclose(); }}>
  <header><div><p class="eyebrow">GLOBAL SETTINGS</p><h2 id="transfer-title">{mode === 'export' ? 'Export settings' : 'Import settings'}</h2><p>{mode === 'export' ? 'Select items across tabs for one settings file.' : 'Select items to merge into your current settings.'}</p></div><button class="dialog-close" aria-label="Close settings transfer" onclick={onclose} disabled={busy}><X aria-hidden="true" size={18} /></button></header>
  {#if mode === 'import'}
    <div class="file-picker"><label><span>Q settings file</span><input type="file" accept=".json,application/json" onchange={readFile} disabled={busy} /></label>{#if fileName}<small>{fileName}</small>{/if}</div>
  {/if}
  <div class="transfer-tabs" role="tablist" aria-label="Settings file sections">
    {#each transferSections as tab}
      <button role="tab" id={`transfer-tab-${tab.id}`} aria-controls="transfer-items" aria-selected={section === tab.id} tabindex={section === tab.id ? 0 : -1} class:active={section === tab.id} onclick={() => section = tab.id} onkeydown={navigateTabs} disabled={busy}>{tab.label}<span>{Object.keys(source?.sections[tab.id] ?? {}).filter((id) => selected.has(transferKey(tab.id, id))).length}/{Object.keys(source?.sections[tab.id] ?? {}).length}</span></button>
    {/each}
  </div>
  <div class="transfer-toolbar"><span>{selectedCount} selected across all tabs</span><div><button class="text-button" onclick={selectAll} disabled={!source || busy}>Select all</button><button class="text-button" onclick={() => { selected = new Set(); invalidateReview(); }} disabled={!selectedCount || busy}>Clear selection</button></div></div>
  <div class="transfer-body" id="transfer-items" role="tabpanel" aria-labelledby={`transfer-tab-${section}`} aria-busy={busy}>
    {#if error}<div class="workspace-dialog-error" role="alert">{error}{#if !current}<button class="text-button" onclick={loadCurrent} disabled={busy}>Retry loading settings</button>{/if}</div>{/if}
    {#if source && items.length}
      <label class="select-section"><input type="checkbox" checked={allInSection} onchange={toggleSection} disabled={busy} /><span>Select all in {transferSections.find((tab) => tab.id === section)?.label}</span></label>
      {#each items as [id, value] (transferKey(section, id))}
        <article class:selected={selected.has(transferKey(section, id))} class="transfer-item">
          <label><input type="checkbox" checked={selected.has(transferKey(section, id))} onchange={(event) => toggle(id, event.currentTarget.checked)} disabled={busy} /><span><strong>{transferLabel(section, id)}</strong><small>{transferSummary(value)}</small></span>{#if mode === 'import'}<em>{actionFor(id, preview)}</em>{/if}</label>
          <details><summary>{mode === 'import' ? 'Compare values' : 'View values'}</summary><div class="value-comparison">{#if mode === 'import'}<div><h4>Current</h4><pre>{currentValue(id, preview)}</pre></div>{/if}<div><h4>{mode === 'import' ? 'Incoming' : 'Export'}</h4><pre>{JSON.stringify(value, null, 2)}</pre></div></div></details>
        </article>
      {/each}
    {:else}<p class="transfer-empty">{busy ? 'Loading settings…' : !source && mode === 'import' ? 'Choose a settings file to select its items.' : 'No settings in this tab.'}</p>{/if}
  </div>
  <footer><div><strong>{preview ? 'Selection reviewed' : `${selectedCount} settings selected`}</strong><small>{preview ? `${preview.changes.filter((item) => item.action === 'add').length} added · ${preview.changes.filter((item) => item.action === 'replace').length} replaced · ${preview.changes.filter((item) => item.action === 'unchanged').length} unchanged` : mode === 'export' ? 'API keys and credential values are excluded.' : 'Unselected settings and existing secrets are preserved.'}</small>{#if listenerChanges}<small>Listener changes apply when their services restart.</small>{/if}</div><button class="secondary-button" onclick={onclose} disabled={busy}>Cancel</button>{#if mode === 'export'}<button class="primary-button" onclick={download} disabled={busy || !selectedCount}><Download aria-hidden="true" size={15} />Export file</button>{:else if preview}<button class="primary-button" onclick={apply} disabled={busy || !selectedCount}><Upload aria-hidden="true" size={15} />{busy ? 'Importing…' : 'Import selected'}</button>{:else}<button class="primary-button" onclick={review} disabled={busy || !selectedCount}>{busy ? 'Reviewing…' : 'Review selection'}</button>{/if}</footer>
</dialog>

<style>
  .transfer-dialog { width: min(960px, calc(100vw - 40px)); max-height: calc(100dvh - 40px); padding: 0; margin: auto; color: var(--text); background: var(--surface); border: 1px solid var(--border-strong); border-radius: 8px; box-shadow: 0 28px 90px rgba(0, 0, 0, .55); }
  .transfer-dialog[open] { display: flex; flex-direction: column; }
  .transfer-dialog::backdrop { background: var(--overlay); backdrop-filter: blur(4px); }
  header { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; padding: 22px; }
  header h2 { margin: 5px 0 8px; font-size: calc(20px * var(--text-scale)); }
  header p:last-child, .file-picker small { color: var(--muted); font-size: calc(12px * var(--text-scale)); }
  .file-picker { display: grid; gap: 8px; padding: 0 22px 18px; }
  .file-picker label { display: grid; gap: 8px; font-size: calc(12px * var(--text-scale)); }
  .file-picker input { min-width: 0; max-width: 100%; font-size: calc(12px * var(--text-scale)); }
  .transfer-tabs { display: flex; flex-shrink: 0; overflow-x: auto; border-top: 1px solid var(--border); border-bottom: 1px solid var(--border); }
  .transfer-tabs button { display: grid; flex: 1; gap: 5px; padding: 13px 15px; border: 0; border-bottom: 2px solid transparent; border-radius: 0; white-space: nowrap; background: transparent; color: var(--muted); font-size: calc(12px * var(--text-scale)); }
  .transfer-tabs button.active { color: var(--text); border-bottom-color: var(--violet); background: rgba(160, 132, 232, .06); }
  .transfer-tabs span { font-size: calc(10px * var(--text-scale)); color: var(--subtle); }
  .transfer-toolbar { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 8px; padding: 12px 22px; font-size: calc(12px * var(--text-scale)); color: var(--muted); }
  .transfer-toolbar > div { display: flex; gap: 14px; }
  .transfer-body { min-height: 180px; overflow-y: auto; padding: 0 22px 20px; }
  .select-section { display: flex; align-items: center; gap: 10px; padding: 8px 0 16px; font-size: calc(12px * var(--text-scale)); }
  input[type=checkbox] { flex: none; width: 15px; height: 15px; accent-color: var(--violet); }
  .transfer-item { margin-bottom: 8px; border: 1px solid var(--border); border-radius: 5px; }
  .transfer-item.selected { border-color: rgba(160, 132, 232, .5); background: rgba(160, 132, 232, .04); }
  .transfer-item > label { display: flex; align-items: center; gap: 12px; padding: 14px; cursor: pointer; }
  .transfer-item label > span { display: grid; flex: 1; min-width: 0; gap: 5px; }
  .transfer-item strong { font-size: calc(13px * var(--text-scale)); font-weight: 500; overflow-wrap: anywhere; }
  .transfer-item small { color: var(--muted); font-size: calc(11px * var(--text-scale)); overflow-wrap: anywhere; }
  .transfer-item em { font-size: calc(10px * var(--text-scale)); font-style: normal; color: var(--violet); white-space: nowrap; }
  details { padding: 0 14px 12px 41px; }
  summary { color: var(--subtle); font-size: calc(11px * var(--text-scale)); cursor: pointer; }
  .value-comparison { display: flex; flex-wrap: wrap; gap: 12px; padding-top: 12px; }
  .value-comparison > div { flex: 1 1 240px; min-width: 0; }
  h4 { margin: 0 0 6px; font-size: calc(11px * var(--text-scale)); color: var(--muted); }
  pre { max-height: 220px; overflow: auto; padding: 10px; white-space: pre-wrap; overflow-wrap: anywhere; background: var(--field-bg); font-size: calc(11px * var(--text-scale)); line-height: 1.6; }
  .transfer-empty { padding: 45px 0; text-align: center; color: var(--subtle); font-size: calc(13px * var(--text-scale)); }
  .workspace-dialog-error { margin-bottom: 14px; }
  footer { display: flex; flex-shrink: 0; flex-wrap: wrap; align-items: center; gap: 12px; padding: 18px 22px; border-top: 1px solid var(--border); background: var(--field-bg); }
  footer > div { display: grid; flex: 1; gap: 6px; min-width: 200px; }
  footer strong { font-size: calc(12px * var(--text-scale)); }
  footer small { font-size: calc(11px * var(--text-scale)); color: var(--muted); }
  footer button { display: inline-flex; align-items: center; gap: 8px; }
  @media (max-width: 600px) {
    .transfer-dialog { width: calc(100vw - 20px); max-height: calc(100dvh - 20px); }
    header, footer { padding: 16px; }
    .file-picker, .transfer-toolbar { padding-left: 16px; padding-right: 16px; }
    .transfer-body { padding-left: 16px; padding-right: 16px; }
    footer > div { flex-basis: 100%; }
    details { padding-left: 14px; }
  }
</style>
