import { apiError } from '../api';

export function jsonText(value: unknown, fallback: unknown) {
  return JSON.stringify(value ?? fallback);
}

export function parseJSON<T>(value: string, label: string): T {
  try { return JSON.parse(value) as T; }
  catch (reason) { throw new Error(`${label}: ${reason instanceof Error ? reason.message : 'invalid JSON'}`); }
}

export async function requestJSON<T>(method: string, url: string, body?: unknown): Promise<T> {
  const response = await fetch(url, {
    method,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!response.ok) throw new Error(await apiError(response));
  return (await response.json()) as T;
}

export function requireWorkspace(value: string) {
  const root = value.trim();
  if (!root) throw new Error('Open a repository in Sessions or enter its path here.');
  return root;
}
