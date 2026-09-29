<script lang="ts">
  const capabilities = [
    ['/new', 'Sessions', 'Create session'], ['/clear', 'Sessions', 'Clear conversation'], ['/compact', 'Sessions', 'Compact context'], ['/learn', 'Sessions', 'Learning toggle'],
    ['/model', 'Settings · Models', 'Global and workspace assignments'], ['/gateway', 'Settings · Providers / Services', 'Providers, keys, listener'], ['/systemone', 'Settings · System One', 'Decision providers, models, keys'],
    ['/library', 'Settings · Services', 'Library listener'], ['/loom', 'Settings · Runtime', 'Storage and garbage collection'], ['/ignore', 'Settings · Integrations', '.qignore editor'],
    ['/skills', 'Settings · Integrations', 'Skill catalog and Git lifecycle'], ['/lsp', 'Settings · Integrations', 'Language server profiles'], ['/mcp', 'Settings · Integrations', 'MCP servers and grants'],
    ['/subagents', 'Settings · Subagents', 'Profiles, delegation grants, and ACP connections'], ['/subagent', 'Sessions', 'Delegate from chat'], ['/changes', 'Changes', 'Repository diff review'], ['/commit', 'Changes', 'Commit proposals and execution'], ['/sessions', 'Sessions', 'Registered session trees'], ['/help', 'Help', 'This guide']
  ];
  const shortcuts = [
    ['Enter', 'Send a chat message or submit guidance'], ['Shift + Enter', 'Insert a line break'], ['Esc', 'Close the directory browser or active dialog'], ['Ctrl/Cmd + K', 'Focus the Studio navigation'], ['?', 'Open Help when focus is outside an input']
  ];
</script>

<section class="help-view">
  <article class="help-card intro-card"><p class="eyebrow">STUDIO GUIDE</p><h2>Every daily Q workflow now has a Web surface.</h2><p>Register an existing or new root session for any repository, run and guide its default loop, inspect delegated work in the same tree, then review and commit changes without leaving Studio.</p></article>
  <div class="help-grid">
    <article class="help-card"><div class="help-heading"><h2>Keyboard</h2><p>Shortcuts follow the focused surface.</p></div><div class="shortcut-list">{#each shortcuts as shortcut}<div><kbd>{shortcut[0]}</kbd><span>{shortcut[1]}</span></div>{/each}</div></article>
    <article class="help-card"><div class="help-heading"><h2>Recovery</h2><p>What to do when a local operation is interrupted.</p></div><ul class="recovery-list"><li><strong>Disconnected browser</strong><span>Reload Studio. Runs continue in the server and reconnect from their event cursor.</span></li><li><strong>Interrupted run after restart</strong><span>Continue the same session. Q restores the transcript, active task and delegation checkpoint.</span></li><li><strong>Stale repository view</strong><span>Refresh the active Changes or Settings panel before retrying.</span></li><li><strong>Service unavailable</strong><span>Open Operations for the endpoint and bounded diagnostics, then retry after the service is ready.</span></li></ul></article>
  </div>
  <article class="help-card"><div class="help-heading"><h2>TUI command map</h2><p>The Web location that owns each former slash command.</p></div><div class="capability-table"><div class="capability-head"><span>Command</span><span>Studio surface</span><span>Action</span></div>{#each capabilities as item}<div><code>{item[0]}</code><strong>{item[1]}</strong><span>{item[2]}</span></div>{/each}</div></article>
</section>

<style>
  .help-view { display: grid; gap: 13px; max-width: 1120px; padding-bottom: 28px; }
  .help-card { overflow: hidden; border: 1px solid var(--border); border-radius: 7px; background: var(--panel); }
  .intro-card { padding: 24px; background: linear-gradient(125deg, rgba(128,82,255,.15), rgba(12,15,23,.2) 48%), var(--panel); }
  .intro-card h2 { max-width: 680px; margin: 8px 0; color: #f1f2f8; font-size: 22px; }
  .intro-card > p:last-child { max-width: 750px; margin: 0; color: #929caf; font-size: 12px; line-height: 1.65; }
  .help-grid { display: grid; grid-template-columns: .8fr 1.2fr; gap: 13px; }
  .help-heading { padding: 15px 17px; border-bottom: 1px solid var(--border); }
  .help-heading h2 { margin: 0; color: #e9ebf4; font-size: 13px; }
  .help-heading p { margin: 5px 0 0; color: #737e95; font-size: 10px; }
  .shortcut-list { display: grid; }
  .shortcut-list > div { display: grid; grid-template-columns: 115px 1fr; align-items: center; gap: 14px; padding: 11px 16px; border-top: 1px solid var(--border); }
  .shortcut-list > div:first-child { border-top: 0; }
  kbd { width: fit-content; padding: 4px 7px; color: #d4c9ff; border: 1px solid #45366e; border-bottom-width: 2px; border-radius: 4px; background: #1a1430; font: 9px ui-monospace, SFMono-Regular, Consolas, monospace; }
  .shortcut-list span { color: #9aa3b7; font-size: 10px; }
  .recovery-list { display: grid; margin: 0; padding: 0; list-style: none; }
  .recovery-list li { display: grid; gap: 5px; padding: 11px 16px; border-top: 1px solid var(--border); }
  .recovery-list li:first-child { border-top: 0; }
  .recovery-list strong { color: #dfe2ec; font-size: 10px; }
  .recovery-list span { color: #7f899f; font-size: 10px; line-height: 1.5; }
  .capability-table > div { display: grid; grid-template-columns: 125px minmax(180px, .75fr) 1fr; gap: 16px; align-items: center; padding: 9px 16px; border-top: 1px solid var(--border); }
  .capability-table .capability-head { color: #626d84; border-top: 0; font-size: 9px; text-transform: uppercase; }
  .capability-table code { color: #bdaaff; font-size: 10px; }
  .capability-table strong { color: #cdd1dd; font-size: 10px; font-weight: 500; }
  .capability-table div > span:last-child { color: #7e889e; font-size: 10px; }
  @media (max-width: 850px) { .help-grid { grid-template-columns: 1fr; } }
  @media (max-width: 620px) { .capability-table > div { grid-template-columns: 80px 1fr; } .capability-table div > span:last-child { grid-column: 2; } .capability-head span:last-child { display: none; } }
</style>
