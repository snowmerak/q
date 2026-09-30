import { apiError } from '../api';
import type { SettingsSnapshot } from './types';

export type QueueSave = (request: () => Promise<SettingsSnapshot>) => void;

export async function writeSettings(method: 'POST' | 'PUT' | 'DELETE', url: string, body?: string): Promise<SettingsSnapshot> {
  const response = await fetch(url, { method, headers: body ? { 'Content-Type': 'application/json' } : undefined, body });
  if (!response.ok) throw new Error(await apiError(response));
  return (await response.json()) as SettingsSnapshot;
}

export function putSettings(url: string, body: string) {
  return writeSettings('PUT', url, body);
}
