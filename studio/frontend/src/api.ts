export async function apiError(response: Response) {
  try {
    const body = (await response.json()) as { error?: string | { message?: string }; message?: string };
    return (typeof body.error === 'string' ? body.error : body.error?.message) || body.message || `Studio returned ${response.status}`;
  } catch {
    return `Studio returned ${response.status}`;
  }
}

export async function requestJSON<T>(method: string, url: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await requestResponse(url, {
    method,
    signal,
    headers: { Accept: 'application/json', ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  return (await response.json()) as T;
}

export async function requestResponse(url: string, options?: RequestInit): Promise<Response> {
  const response = await fetch(url, options);
  if (!response.ok) throw new Error(await apiError(response));
  return response;
}
