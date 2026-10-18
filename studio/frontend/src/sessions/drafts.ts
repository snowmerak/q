const drafts = new Map<string, string>();
const prefix = 'q-studio-draft:';

export function loadDraft(key: string) {
  try {
    return drafts.get(key) ?? localStorage.getItem(prefix + key) ?? '';
  } catch {
    return drafts.get(key) ?? '';
  }
}

export function saveDraft(key: string, value: string) {
  if (!key) return;
  if (value) drafts.set(key, value);
  else drafts.delete(key);
  try {
    if (value) localStorage.setItem(prefix + key, value);
    else localStorage.removeItem(prefix + key);
  } catch {
    // Keep drafts available during navigation when browser storage is unavailable.
  }
}
