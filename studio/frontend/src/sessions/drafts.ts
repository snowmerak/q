import { uiStorage } from '../uiStorage';

const prefix = 'q-studio-draft:';

export function loadDraft(key: string) {
  return uiStorage.getItem(prefix + key) ?? '';
}

export function saveDraft(key: string, value: string) {
  if (!key) return;
  if (value) uiStorage.setItem(prefix + key, value);
  else uiStorage.removeItem(prefix + key);
}
