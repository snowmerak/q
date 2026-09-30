export async function apiError(response: Response) {
  try {
    const body = (await response.json()) as { error?: string };
    return body.error || `Studio returned ${response.status}`;
  } catch {
    return `Studio returned ${response.status}`;
  }
}
