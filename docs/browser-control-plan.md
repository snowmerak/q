# Browser control and bundled CDP CLI

Accepted requirements:

- Add Settings → Browser control with cdp-q-driver, ego-browser and playwright-cli presets.
- Store enabled/preferred CLI, executable and an argument array. Do not store a skill binding. A bare executable uses PATH; an explicit path selects that executable.
- Explain the user's browser CLI preference in model context. Agents discover relevant skills themselves; a missing skill must not make a CLI unusable.
- Embed cdp-q-driver as `q cdp`, using its public CLI API and `CommandPrefix: ["cdp"]` for background re-exec.
- The built-in preset uses the current Q executable with `cdp` as its first argument.
- Bundle a Q-specific CDP skill and reference resources with the executable. Library owns the managed source and indexes it through normal skill search/read APIs.
- Compare driver revision plus resource-bundle digest on Q startup/Library connection. Update changed managed resources atomically, preserve user skills, refresh indexes, and support an already running Library service.
- Browser settings take effect on the next user turn, including existing persistent Codex threads. Send an append-only host context update when preferences change, including when disabled, rather than repeatedly changing the prompt prefix.

Implementation/verification boundaries:

1. Pin upstream `ec463b2` (public CLI entrypoint), use its Go 1.27.3 minimum, and verify embedded help, pure JS execution and prefixed child-process launch with isolated data.
2. Add validated browser configuration, snapshots/update API, CLI discovery without launching a browser, and a usable Settings panel with PATH/full-path help.
3. Apply preference context consistently in native sessions, ACP prompts and native workers without granting tools excluded by their runtime.
4. Provide bundled Library skill resources with a managed version manifest. Standalone Library search/get contracts stay unchanged.
5. Cover preference persistence/clearing, existing-thread delivery, settings UI/reload, bundle updates and idempotency, and browser resource ownership. Keep unrelated pending skill-reuse changes intact. Do not commit/push without a new request.

Implemented storage uses immutable content-addressed generations plus an atomic
manifest. Library advertises bundled-skill support in its health response; a
new process uploads its embedded resources when joining a compatible supporting
server. A pre-feature Library requires one restart to gain that capability.
