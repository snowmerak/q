export async function apiError(response: Response) {
  try {
    const body = (await response.json()) as { error?: string };
    return body.error || `Studio returned ${response.status}`;
  } catch {
    return `Studio returned ${response.status}`;
  }
}

export async function requestJSON<T>(method: string, url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, {
    method,
    signal,
    headers: body === undefined ? undefined : { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!response.ok) throw new Error(await apiError(response));
  return (await response.json()) as T;
}
