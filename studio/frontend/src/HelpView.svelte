<script lang="ts">
  import { cli, slash, studioKeys, terminalKeys, acpCommands } from './help/reference';
  let scroller: HTMLElement;
  const sections = [
    ['workflows', 'Workflows'], ['cli', 'CLI commands'], ['slash', 'Slash commands'],
    ['keyboard', 'Keyboard shortcuts'], ['commit', 'Commit session'], ['files', 'Files'], ['recovery', 'Recovery'],
  ];
  function jump(id: string) {
    const target = scroller.querySelector<HTMLElement>(`#help-${id}`);
    if (target) scroller.scrollTo({ top: scroller.scrollTop + target.getBoundingClientRect().top - scroller.getBoundingClientRect().top });
  }
</script>

<nav class="help-index" aria-label="Help sections">
  {#each sections as section}<button onclick={() => jump(section[0])}>{section[1]}</button>{/each}
</nav>
<!-- svelte-ignore a11y_no_noninteractive_tabindex (Keyboard users need to focus this scroll region to read the entire guide.) -->
<div class="help-view" role="region" aria-label="Studio help guide" tabindex="0" bind:this={scroller}>
  <div class="help-content">
    <article id="help-workflows" class="help-card intro-card">
      <p class="eyebrow">STUDIO GUIDE</p><h2>Sessions, commands, and everyday workflows</h2>
      <p>Register an existing or new root session for any workspace, run and guide its default loop, inspect delegated work in the session tree, then review repository changes. Projects group sessions and can contain multiple editable workspace directories; unassigned sessions appear under Independents.</p>
      <ul class="recovery-list">
        <li><strong>Start and guide work</strong><span>In Sessions, add a root session by directory or project workspace. Choose a saved session or create one. Send a prompt; while a turn is active, the same composer sends guidance. Pause, resume, cancel, and answer questions using the session controls.</span></li>
        <li><strong>Follow delegation</strong><span>Ask the default loop to delegate suitable tasks. Expand the session tree to inspect each child’s transcript and activity. Writing agents work in Git worktrees and return change requests for their caller to review and accept. Deleting a completed delegation removes its descendants; the parent conversation remains.</span></li>
        <li><strong>Configure Q</strong><span>Settings covers global/workspace models, providers, System One, runtime, services, integrations, and subagents. Edits save automatically. Operations shows usage, workers, local services, logs, and retention.</span></li>
      </ul>
    </article>

    <article id="help-cli" class="help-card">
      <div class="help-heading"><h2>CLI commands</h2><p>Run these in a terminal. Configuration commands open Studio; service commands remain in the foreground.</p></div>
      <div class="reference-list">{#each cli as item}<div><code>{item.command}</code><p>{item.action}</p><small>{item.studio}</small></div>{/each}</div>
      <div class="help-note"><strong>Choose a Studio host and port</strong><pre><code>q studio --host 127.0.0.1 --port 7070
q studio --host 0.0.0.0 --port 7070 --no-open</code></pre><p>Use an IP address for --host. Open http://127.0.0.1:7070 locally, or the server’s IP on another device. A non-loopback listener exposes Studio without built-in authentication; restrict access through your network or authenticated proxy.</p></div>
    </article>

    <article id="help-slash" class="help-card">
      <div class="help-heading"><h2>Slash commands</h2><p>These are commands for Q’s terminal chat. The Studio equivalent is listed under each command.</p></div>
      <div class="help-note"><p>Studio chat currently sends text directly to the default loop. Use its buttons and Settings panels for the actions below; typing a terminal slash command into Studio is not a Web command shortcut.</p></div>
      <div class="reference-list">{#each slash as item}<div><code>{item.command}</code><p>{item.action}</p><small>Studio: {item.studio}</small></div>{/each}</div>
      <div class="help-note"><strong>ACP command scope</strong><p>Q’s ACP server advertises: <code>{acpCommands}</code>. Terminal clients connected to another ACP agent use that agent’s advertised command list; /new starts a new client session. Local configuration screens are not forwarded as Q commands.</p></div>
    </article>

    <article id="help-keyboard" class="help-card">
      <div class="help-heading"><h2>Keyboard shortcuts</h2><p>Studio and terminal shortcuts have separate handlers. Choose the section for the interface you are using.</p></div>
      {#each studioKeys as group}<h3>{group.title}</h3><div class="shortcut-list">{#each group.keys as shortcut}<div><kbd>{shortcut[0]}</kbd><span>{shortcut[1]}</span></div>{/each}</div>{/each}
      {#each terminalKeys as group}<details><summary>{group.title}</summary><div class="shortcut-list">{#each group.keys as shortcut}<div><kbd>{shortcut[0]}</kbd><span>{shortcut[1]}</span></div>{/each}</div></details>{/each}
    </article>

    <article id="help-commit" class="help-card">
      <div class="help-heading"><h2>Commit session</h2><p>q commit opens the original interactive workflow in your terminal.</p></div>
      <ol class="steps"><li>Run <code>q commit</code> in the repository, or <code>/commit</code> in terminal chat. Q uses the staged index; if empty, it stages working-tree changes excluding Q’s root .q metadata.</li><li>The configured commit model proposes one Conventional Commit message or a file-disjoint split. Review the captured files and every proposed message.</li><li>Select proposals with ↑/↓, press e to edit, Ctrl+S to save an edit, or r to regenerate. Enter approves and creates the commits; p approves, commits, and pushes. Push requires an existing upstream.</li><li>Escape or q cancels at review without creating a commit. Preparation may already have staged changes. Q verifies the index again before committing; if it changed during review, regenerate.</li></ol>
      <div class="help-note"><p>In Studio, open Changes and use its commit review controls to generate, edit, and execute proposals. Review is a separate step from execution.</p></div>
    </article>

    <article id="help-files" class="help-card">
      <div class="help-heading"><h2>Files: Raw and Diff</h2><p>Inspect the selected workspace while following the conversation.</p></div>
      <ul class="recovery-list"><li><strong>Browse a workspace</strong><span>Open Files, choose a directory or project workspace, expand folders, and select a file. Raw shows syntax highlighting, line numbers, and copy. Diff keeps the whole file visible with previous/current line numbers, +/− gutter markers, and added/removed row colors. Choose All changes (HEAD → working tree), Staged (HEAD → index), or Unstaged (index → working tree).</span></li><li><strong>Inspect session work</strong><span>Use Browse session files in the chat toolbar or click a Markdown file link. A delegated session opens its own worktree. The workspace selector includes the project’s other directories.</span></li><li><strong>Deleted and large files</strong><span>Deleted files remain in the Git changes list. Text previews stop at 256 KiB or 4,000 lines; binary files show a notice. Partial previews may have incomplete change markings. Files is read-only.</span></li></ul>
    </article>

    <article id="help-recovery" class="help-card">
      <div class="help-heading"><h2>Recovery</h2><p>Continue work after a disconnected browser or interrupted local operation.</p></div>
      <ul class="recovery-list"><li><strong>Disconnected browser</strong><span>Reload Studio. Runs continue in the server and reconnect from their durable event cursor.</span></li><li><strong>Interrupted run after restart</strong><span>Continue the same session. Q restores its transcript and available task/delegation checkpoints.</span></li><li><strong>Stale repository view</strong><span>Refresh Files, Changes, or the active Settings panel before retrying.</span></li><li><strong>Service unavailable</strong><span>Open Operations for endpoints and bounded diagnostics, then retry after the service is ready.</span></li></ul>
    </article>
  </div>
</div>

<style>
  .help-index { display: flex; flex: none; flex-wrap: wrap; gap: 7px; padding: 14px 0; }
  .help-index button { width: auto; min-height: 34px; padding: 6px 12px; border: 1px solid var(--border); background: var(--panel, var(--surface)); color: var(--muted); font-size: 13px; cursor: pointer; }
  .help-index button:focus-visible, .help-view:focus-visible { outline: 2px solid var(--violet); outline-offset: -2px; }
  .help-view { min-height: 0; min-width: 0; flex: 1; overflow-y: auto; overscroll-behavior: contain; padding: 0 12px 28px 0; scrollbar-gutter: stable; }
  .help-content { display: grid; gap: 18px; max-width: 1120px; }
  .help-card { min-width: 0; border: 1px solid var(--border); border-radius: 7px; background: var(--panel, var(--surface)); scroll-margin-top: 14px; }
  .intro-card { padding: 24px; background: linear-gradient(125deg, rgba(128,82,255,.15), rgba(12,15,23,.2) 48%), var(--surface); }
  .intro-card h2 { margin: 8px 0 12px; font-size: 24px; }
  .intro-card > p:last-of-type { max-width: 900px; color: var(--muted); font-size: 15px; line-height: 1.7; }
  .intro-card .recovery-list { margin-top: 18px; }
  .help-heading { padding: 20px 22px; border-bottom: 1px solid var(--border); }
  .help-heading h2 { font-size: 20px; }
  .help-heading p { margin-top: 7px; color: var(--muted); font-size: 14px; line-height: 1.6; }
  h3 { margin: 18px 22px 10px; font-size: 16px; }
  .reference-list > div { display: grid; gap: 7px; padding: 16px 22px; border-bottom: 1px solid var(--border); }
  .reference-list > div:last-child { border-bottom: 0; }
  code { color: #c6b1ff; font: 13px/1.6 ui-monospace, SFMono-Regular, Consolas, monospace; overflow-wrap: anywhere; }
  .reference-list p, .help-note p { color: var(--muted); font-size: 14px; line-height: 1.7; }
  .reference-list small { color: var(--subtle); font-size: 13px; }
  .help-note { padding: 18px 22px; border-top: 1px solid var(--border); }
  .help-note strong { display: block; margin-bottom: 8px; font-size: 15px; }
  pre { margin: 12px 0; padding: 12px; border: 1px solid var(--border); border-radius: 5px; background: #090c14; white-space: pre-wrap; overflow-wrap: anywhere; }
  .shortcut-list { display: grid; }
  .shortcut-list > div { display: grid; grid-template-columns: minmax(160px, .6fr) minmax(0, 1fr); align-items: start; gap: 20px; padding: 13px 22px; border-top: 1px solid var(--border); }
  .shortcut-list > div:first-child { border-top: 0; }
  kbd { width: fit-content; max-width: 100%; padding: 4px 8px; color: #d4c9ff; border: 1px solid #45366e; border-bottom-width: 2px; border-radius: 4px; background: #1a1430; font: 13px/1.5 ui-monospace, SFMono-Regular, Consolas, monospace; overflow-wrap: anywhere; }
  .shortcut-list span { color: var(--muted); font-size: 14px; line-height: 1.65; }
  details { border-top: 1px solid var(--border); }
  summary { padding: 16px 22px; color: var(--text); font-size: 16px; cursor: pointer; }
  .recovery-list { display: grid; margin: 0; padding: 0; list-style: none; }
  .recovery-list li { display: grid; gap: 7px; padding: 16px 22px; border-top: 1px solid var(--border); }
  .recovery-list li:first-child { border-top: 0; }
  .recovery-list strong { color: var(--text); font-size: 15px; }
  .recovery-list span { color: var(--muted); font-size: 14px; line-height: 1.7; }
  .steps { margin: 0; padding: 18px 22px 18px 44px; color: var(--muted); font-size: 14px; line-height: 1.7; }
  .steps li + li { margin-top: 12px; }
  @media (max-width: 760px) { .help-view { flex: none; max-height: 70dvh; } .help-index { gap: 5px; } .help-index button { padding: 6px 9px; } }
  @media (max-width: 620px) { .shortcut-list > div { grid-template-columns: minmax(0, 1fr); gap: 9px; } .intro-card, .help-heading { padding: 18px; } .reference-list > div, .help-note, .recovery-list li, .shortcut-list > div { padding-inline: 18px; } }
</style>
