export function jsonText(value: unknown, fallback: unknown) {
  return JSON.stringify(value ?? fallback);
}

export function parseJSON<T>(value: string, label: string): T {
  try { return JSON.parse(value) as T; }
  catch (reason) { throw new Error(`${label}: ${reason instanceof Error ? reason.message : 'invalid JSON'}`); }
}

