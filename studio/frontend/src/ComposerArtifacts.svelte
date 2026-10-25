<script lang="ts">
  import { onMount } from 'svelte';
  import { requestJSON, requestResponse } from './api';

  export let composerID: string;
  export let running = false;
  export let dirty = false;
  type Artifact = { path: string; kind: string; hash: string; content?: string };
  let files: Artifact[] = [];
  let current: Artifact | null = null;
  let source = '';
  let kind = 'd2';
  let filename = '';
  let creating = false;
  let busy = false;
  let error = '';
  let changed = false;
  let tab = 'preview';
  let preview = '';
  let generation = 0;
  let revision = 0;
  let fileSignature = '';
  let expandedDialog: HTMLDialogElement;
  let expandedFrame: HTMLIFrameElement | undefined;
  let expanded = false;
  let timer: ReturnType<typeof setTimeout>;
  const controller = new AbortController();
  $: dirty = current !== null && source !== (current.content || '');
  $: base = `/api/v1/composers/${encodeURIComponent(composerID)}/artifacts`;
  $: if (!current && expanded) expandedDialog?.close();
  function expandPreview() {
    if (!current) return;
    expanded = true;
    expandedDialog.showModal();
  }
  function sourceURL(path: string) { return `${base}/source?path=${encodeURIComponent(path)}`; }
  function failure(cause: unknown) { if (!controller.signal.aborted) error = cause instanceof Error ? cause.message : 'Could not load artifacts'; }
  function display(value: Artifact) {
    current = value; source = value.content || ''; changed = false;
    preview = `${base}/preview?path=${encodeURIComponent(value.path)}&v=${value.hash}-${revision}`;
  }
  async function load(path: string, preserveEdits = false) {
    const expected = ++generation;
    const previousSource = source;
    const value = await requestJSON<Artifact>('GET', sourceURL(path), undefined, controller.signal);
    if (expected === generation && !controller.signal.aborted) {
      if (preserveEdits && source !== previousSource) { changed = true; return; }
      display(value);
    }
  }
  async function refresh() {
    const expected = generation;
    const result = await requestJSON<{ artifacts: Artifact[] }>('GET', base, undefined, controller.signal);
    if (controller.signal.aborted || expected !== generation) return;
    files = result.artifacts;
    const signature = files.map(file => `${file.path}:${file.hash}`).join('|');
    if (signature !== fileSignature) {
      fileSignature = signature; revision++;
      if (current?.kind === 'd2') preview = `${base}/preview?path=${encodeURIComponent(current.path)}&v=${current.hash}-${revision}`;
    }
    if (!current && files.length && !creating) { await load(files[0].path); return; }
    if (!current || busy) return;
    const remote = files.find(file => file.path === current?.path);
    if (!remote || remote.hash !== current.hash) {
      if (dirty) changed = true;
      else if (remote) await load(remote.path, true);
      else { generation++; current = null; source = ''; preview = ''; }
    }
  }
  async function choose(path: string, picker: HTMLSelectElement) {
    if (busy || (dirty && !window.confirm('Discard your unsaved source changes?'))) { picker.value = current?.path || ''; return; }
    error = ''; creating = false;
    try { await load(path); } catch (cause) { picker.value = current?.path || ''; failure(cause); }
  }
  async function reload() {
    if (!current || (dirty && !window.confirm('Reload this file and discard unsaved source changes?'))) return;
    error = '';
    try { await load(current.path); } catch (cause) { failure(cause); }
  }
  async function create() {
    if (running || busy) return;
    if (dirty && !window.confirm('Discard your unsaved source changes?')) return;
    busy = true; error = '';
    const extension = kind === 'd2' ? '.d2' : '.html';
    const name = filename.trim();
    const path = `docs/${kind === 'd2' ? 'diagram' : 'webcomponent'}/${name.endsWith(extension) ? name : name + extension}`;
    const content = kind === 'd2' ? 'direction: right\nidea -> conversation -> prototype\n' : '<!doctype html>\n<html lang="en">\n<head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>Idea</title>\n<style>body{font:16px system-ui;padding:32px}button{padding:12px 20px}</style></head>\n<body><h1>Try your idea</h1><button onclick="this.textContent=\'It works!\'">Try it</button></body>\n</html>\n';
    try {
      display(await requestJSON<Artifact>('PUT', sourceURL(path), { content, expected_hash: '' }, controller.signal));
      creating = false; filename = ''; tab = 'preview'; await refresh();
    } catch (cause) { failure(cause); }
    finally { busy = false; }
  }
  async function save() {
    if (!current || running || busy) return;
    busy = true; error = '';
    try { display(await requestJSON<Artifact>('PUT', sourceURL(current.path), { content: source, expected_hash: current.hash }, controller.signal)); await refresh(); }
    catch (cause) { failure(cause); }
    finally { busy = false; }
  }
  async function remove() {
    if (!current || running || busy || !window.confirm(`Delete ${current.path}?`)) return;
    busy = true; error = '';
    try {
      await requestResponse(sourceURL(current.path), { method: 'DELETE', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_hash: current.hash }), signal: controller.signal });
      generation++; current = null; source = ''; preview = ''; await refresh();
    } catch (cause) { failure(cause); }
    finally { busy = false; }
  }
  onMount(() => {
    const poll = async () => {
      try { await refresh(); } catch (cause) { failure(cause); }
      if (!controller.signal.aborted) timer = setTimeout(poll, 1500);
    };
    void poll();
    const unload = (event: BeforeUnloadEvent) => { if (dirty) { event.preventDefault(); event.returnValue = ''; } };
    window.addEventListener('beforeunload', unload);
    const closeFromPreview = (event: MessageEvent) => {
      if (expanded && event.source === expandedFrame?.contentWindow && event.data?.type === 'q-composer-close-preview') expandedDialog.close();
    };
    window.addEventListener('message', closeFromPreview);
    return () => { generation++; controller.abort(); clearTimeout(timer); window.removeEventListener('beforeunload', unload); window.removeEventListener('message', closeFromPreview); };
  });
</script>

<section class="artifacts" aria-label="Composer artifacts">
  <div class="heading"><div><h2>Artifacts</h2><p>D2 diagrams & HTML components</p></div><button onclick={() => creating = !creating} disabled={running || busy}>New artifact</button></div>
  {#if error}<p class="error" role="alert">{error}</p>{/if}
  {#if creating}
    <form class="create" onsubmit={(event) => { event.preventDefault(); void create(); }}>
      <label>Artifact type<select bind:value={kind} disabled={busy}><option value="d2">D2 diagram</option><option value="html">HTML component</option></select></label>
      <label>Filename<input bind:value={filename} placeholder={kind === 'd2' ? 'idea.d2' : 'idea.html'} required disabled={busy} /></label>
      <button type="submit" disabled={running || busy || !filename.trim()}>Create artifact</button>
    </form>
  {/if}
  {#if files.length}
    <div class="file-picker"><label for="composer-artifact-file">File</label><select id="composer-artifact-file" value={current?.path || ''} disabled={busy} onchange={(event) => choose(event.currentTarget.value, event.currentTarget)}>
      {#if !current}<option value="" disabled>Choose a file</option>{/if}
      {#each files as file (file.path)}<option value={file.path}>{file.path}</option>{/each}
    </select></div>
  {/if}
  {#if current}
    <div class="toolbar"><div role="group" aria-label="Artifact view"><button aria-pressed={tab === 'preview'} onclick={() => tab = 'preview'}>Preview</button><button aria-pressed={tab === 'source'} onclick={() => tab = 'source'}>Source{dirty ? ' •' : ''}</button></div><button onclick={remove} disabled={running || busy}>Delete file</button></div>
    {#if changed}<p class="notice" role="status">The saved file changed. Your edits are kept here. <button onclick={reload}>Reload saved source</button></p>{/if}
    {#if tab === 'source'}
      <label class="source-label" for="composer-source">{current.path}</label>
      <textarea id="composer-source" class="source" bind:value={source} spellcheck="false" disabled={busy} aria-label="Artifact source"></textarea>
      <div class="actions"><small>{running ? 'Save after the current reply finishes.' : dirty ? 'Unsaved changes' : 'All changes saved'}</small><button class="primary" onclick={save} disabled={running || busy || !dirty}>Save source</button></div>
    {:else}
      <div class="preview-actions"><small>{current.kind === 'd2' ? 'Zoom and drag inside the diagram.' : 'Interactive HTML preview'}</small><button onclick={expandPreview}>Expand preview</button></div>
      <iframe title="Artifact preview" src={preview} sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>
      <small class="caption">{dirty ? 'Preview shows the saved source. Save your changes to update it.' : 'Saved preview · HTML runs in an isolated sandbox.'}</small>
    {/if}
  {:else}<div class="empty"><h3>Make the idea visible.</h3><p>Ask Composer to draw a D2 diagram or build an HTML component, or create a file here. Refine it together through conversation.</p><code>docs/diagram/</code><code>docs/webcomponent/</code></div>{/if}
  <dialog class="expanded-preview" bind:this={expandedDialog} aria-labelledby="expanded-preview-title" onclose={() => expanded = false}>
    <header><div><h2 id="expanded-preview-title">Artifact preview</h2><p>{current?.path}</p></div><button onclick={() => expandedDialog.close()} aria-label="Close expanded preview">Close</button></header>
    {#if expanded && current}<iframe bind:this={expandedFrame} title="Expanded artifact preview" src={preview} sandbox="allow-scripts" referrerpolicy="no-referrer"></iframe>{/if}
    <small>{current?.kind === 'd2' ? 'Esc to close · Drag to pan · Scroll to zoom · Fit to see the whole diagram' : 'Close to return · HTML runs in an isolated sandbox'}</small>
  </dialog>
</section>

<style>
  .artifacts{min-width:0;min-height:0;display:flex;flex-direction:column;gap:14px;height:100%;overflow:auto;padding:20px 0 40px 20px;border-left:1px solid var(--border)}
  .preview-actions{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap}
  .expanded-preview{width:calc(100vw - 48px);max-width:none;height:calc(100dvh - 48px);max-height:none;margin:auto;padding:20px;border:1px solid var(--border-strong);border-radius:12px;background:var(--surface);color:var(--text)}
  .expanded-preview[open]{display:flex;flex-direction:column;gap:14px}
  .expanded-preview::backdrop{background:rgb(0 0 0 / .75)}
  .expanded-preview header{display:flex;align-items:center;justify-content:space-between;gap:16px;flex:none}
  .expanded-preview header>div{min-width:0}
  .expanded-preview header p{color:var(--muted);font-size:13px;margin:6px 0 0;overflow-wrap:anywhere}
  .expanded-preview iframe{min-height:0}
  @media(max-width:800px){.expanded-preview{width:calc(100vw - 16px);height:calc(100dvh - 16px);padding:12px;border-radius:8px}}
  .heading,.toolbar,.actions{display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap}h2{margin:0;font-size:calc(20px * var(--text-scale))}.heading p{margin:6px 0 0;color:var(--muted);font-size:12px}.create{display:grid;gap:12px;padding:14px;border:1px solid var(--border);border-radius:8px}label{display:grid;gap:7px;font-size:12px;color:var(--muted)}
  input,select,textarea{width:100%;min-width:0;background:var(--field-bg);color:var(--text);border:1px solid var(--border-strong);border-radius:6px;padding:9px;font:inherit}button{font:inherit;padding:7px 10px;color:var(--text);background:var(--surface-raised);border:1px solid var(--border-strong);border-radius:6px;cursor:pointer}button:disabled{opacity:.5;cursor:default}button[aria-pressed=true],button.primary{border-color:var(--violet);background:var(--violet);color:var(--on-accent)}.toolbar [role=group]{display:flex;gap:6px}iframe{flex:1;width:100%;min-height:340px;border:1px solid var(--border);border-radius:8px;background:white}.source{flex:1;min-height:340px;resize:vertical;font:13px/1.6 ui-monospace,monospace;tab-size:2;white-space:pre;overflow:auto}.caption,small{color:var(--muted);font-size:12px;line-height:1.5}.empty{margin:30px 0;color:var(--muted);line-height:1.7}.empty h3{color:var(--text)}code{display:block;font-size:12px}.error{color:var(--error);overflow-wrap:anywhere}.notice{color:var(--muted);font-size:13px}.source-label{overflow-wrap:anywhere}
  @media(max-width:1200px){.artifacts{border-left:0;border-top:1px solid var(--border);padding:24px 0;min-height:550px}}
</style>
