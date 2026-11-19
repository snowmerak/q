import { writable } from 'svelte/store';

type Values = Record<string, string | null>;
const endpoint = '/api/v1/ui-state';
const migrationKey = 'q-studio-ui-state-migrated-v1';
const values = new Map<string, string>();
const pending = new Map<string, string | null>();
let saving: Promise<void> | undefined;
let timer: ReturnType<typeof setTimeout> | undefined;
let retryDelay = 1000;

export const uiStateError = writable('');

function isUIKey(key: string) {
  return ['q-studio-theme', 'q-studio-text-scale', 'q-studio-compact-sessions', 'q-studio-sidebar-collapsed', 'q-studio-workspace-root'].includes(key)
    || key.startsWith('q-studio-draft:');
}

function cache(key: string, value: string | null) {
  try {
    if (value === null) localStorage.removeItem(key);
    else localStorage.setItem(key, value);
  } catch {
    // The profile remains authoritative when browser storage is unavailable.
  }
}

async function readState(): Promise<Values> {
  const response = await fetch(endpoint, { cache: 'no-store', signal: AbortSignal.timeout(5000) });
  if (!response.ok) throw new Error(`Studio returned ${response.status}`);
  const state = await response.json() as { version: number; values: Values };
  if (state.version !== 1 || !state.values || Object.values(state.values).some(value => value !== null && typeof value !== 'string')) {
    throw new Error('Unsupported UI state');
  }
  return state.values;
}

async function writeState(update: Values, onlyMissing = false) {
  const body = JSON.stringify({ values: update, only_missing: onlyMissing });
  const response = await fetch(endpoint, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body,
    // Keep small preference/draft writes alive while the page navigates away.
    keepalive: new TextEncoder().encode(body).byteLength < 60 * 1024,
    signal: AbortSignal.timeout(5000)
  });
  if (!response.ok) throw new Error(`Studio returned ${response.status}`);
}

export async function initializeUIStorage() {
  const legacy: Values = {};
  let migrated = false;
  try {
    migrated = localStorage.getItem(migrationKey) === 'true';
    for (let index = 0; index < localStorage.length; index++) {
      const key = localStorage.key(index);
      if (key && isUIKey(key)) {
        const value = localStorage.getItem(key);
        if (value !== null) { values.set(key, value); legacy[key] = value; }
      }
    }
  } catch {
    // A new origin or blocked localStorage still loads the shared profile.
  }
  try {
    let saved = await readState();
    if (!migrated && Object.keys(legacy).length) {
      await writeState(legacy, true);
      saved = await readState();
    }
    for (const key of values.keys()) cache(key, null);
    values.clear();
    for (const [key, value] of Object.entries(saved)) {
      if (!isUIKey(key)) continue;
      if (value !== null) values.set(key, value);
      cache(key, value);
    }
    try { localStorage.setItem(migrationKey, 'true'); } catch { /* Profile writes do not require localStorage. */ }
    uiStateError.set('');
  } catch {
    uiStateError.set('Saved preferences could not be loaded. Reload to retry.');
  }
  window.addEventListener('pagehide', () => { void flushUIStorage(); });
}

function scheduleSave(delay = 0) {
  clearTimeout(timer);
  timer = setTimeout(() => { void flushUIStorage(); }, delay);
}

export async function flushUIStorage(): Promise<void> {
  if (saving) return saving;
  if (!pending.size) return;
  const batch = Object.fromEntries(pending);
  pending.clear();
  saving = (async () => {
    let failed = false;
    try {
      await writeState(batch);
      retryDelay = 1000;
      uiStateError.set('');
    } catch {
      // New edits take precedence over an older request that needs retrying.
      for (const [key, value] of Object.entries(batch)) {
        if (!pending.has(key)) pending.set(key, value);
      }
      failed = true;
      uiStateError.set('Preferences could not be saved. Retrying…');
    } finally {
      saving = undefined;
      if (pending.size) scheduleSave(failed ? retryDelay : 0);
      if (failed) retryDelay = Math.min(retryDelay * 2, 30000);
    }
  })();
  return saving;
}

export const uiStorage = {
  getItem(key: string): string | null { return values.get(key) ?? null; },
  setItem(key: string, value: string) {
    values.set(key, value);
    cache(key, value);
    pending.set(key, value);
    scheduleSave();
  },
  removeItem(key: string) {
    if (!values.has(key) && !pending.has(key)) return;
    values.delete(key);
    cache(key, null);
    pending.set(key, null);
    scheduleSave();
  }
};
