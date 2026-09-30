import { writable } from 'svelte/store';

export function createSettingsOperations() {
  const state = writable({ busy: false, message: '', error: '' });
  let queue = Promise.resolve();
  let pending = 0;

  function fail(reason: unknown) {
    state.update((value) => ({ ...value, message: '', error: reason instanceof Error ? reason.message : 'Operation failed' }));
  }

  function run(action: () => Promise<void>, success = 'Saved') {
    pending += 1;
    state.set({ busy: true, message: '', error: '' });
    queue = queue.then(action).then(() => {
      state.update((value) => ({ ...value, message: success, error: '' }));
    }).catch(fail).finally(() => {
      pending -= 1;
      state.update((value) => ({ ...value, busy: pending > 0 }));
    });
    return queue;
  }

  // Panel loads share the write queue so an older read cannot replace a save.
  // Payloads and workspace paths are captured by the panel before enqueueing.
  return { subscribe: state.subscribe, run, fail };
}

export type SettingsOperations = ReturnType<typeof createSettingsOperations>;
