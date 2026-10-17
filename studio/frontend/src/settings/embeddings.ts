import { requestResponse } from '../api';

export type EmbeddingCounts = {
  records: number; eligible: number; current: number; missing: number; stale: number; excluded: number;
};

export type EmbeddingArchive = EmbeddingCounts & {
  name: string;
  scope: 'global' | 'workspace';
  workspace_root?: string;
  registered: boolean;
  state: 'current' | 'pending' | 'absent' | 'disabled' | 'error';
  exists: boolean;
  vectors: number;
  current_vectors: number;
  invalid_vectors: number;
  unreadable: number;
  complete: boolean;
  kinds: (EmbeddingCounts & { kind: string })[];
  models: { model: string; dimensions: number; vectors: number }[];
  index?: { model: string; dimensions: number; vectors: number; indexed_at: string; configuration_matches: boolean };
  issues: string[];
};

export type EmbeddingSnapshot = {
  model: string;
  dimensions: number;
  enabled: boolean;
  observed_at: string;
  completed_at: string;
  complete: boolean;
  discovery_roots: string[];
  warnings: string[];
  archives: EmbeddingArchive[];
  sync_job?: EmbeddingSyncJob;
};

export type EmbeddingStatusEvent =
  | { type: 'started' | 'completed'; snapshot: EmbeddingSnapshot }
  | { type: 'archive'; archive: EmbeddingArchive }
  | { type: 'error'; error: string };

export async function readEmbeddingStatus(url: string, signal: AbortSignal, onEvent: (event: EmbeddingStatusEvent) => void) {
  const response = await requestResponse(url, { signal, headers: { Accept: 'application/x-ndjson, application/json' } });
  // Older Studio servers and JSON API consumers retain the snapshot format.
  if (!response.headers.get('Content-Type')?.includes('application/x-ndjson')) {
    onEvent({ type: 'completed', snapshot: await response.json() as EmbeddingSnapshot });
    return;
  }
  if (!response.body) throw new Error('Embedding status stream is unavailable');
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let pending = '';
  function deliver(line: string) {
    if (!line.trim()) return false;
    const event = JSON.parse(line) as EmbeddingStatusEvent;
    if (event.type === 'error') throw new Error(event.error);
    if (!['started', 'archive', 'completed'].includes(event.type)) throw new Error('Unknown embedding status event');
    onEvent(event);
    return event.type === 'completed';
  }
  try {
    for (;;) {
      const { value, done } = await reader.read();
      pending += decoder.decode(value, { stream: !done });
      let newline: number;
      while ((newline = pending.indexOf('\n')) !== -1) {
        const line = pending.slice(0, newline);
        pending = pending.slice(newline + 1);
        if (deliver(line)) return;
      }
      if (done) {
        if (pending && deliver(pending)) return;
        throw new Error('Embedding status stream ended before the scan completed. Refresh to try again.');
      }
    }
  } finally {
    await reader.cancel().catch(() => {});
    reader.releaseLock();
  }
}

export type EmbeddingSyncJob = {
  id: string; scope: 'all' | 'global' | 'workspace'; workspace_root?: string;
  state: 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled';
  model: string; dimensions: number; started_at: string; completed_at?: string;
  total: number; completed: number; failed: number; embedded: number; error?: string;
  targets: { name: string; scope: string; workspace_root?: string; state: string; embedded: number; error?: string }[];
};

export const embeddingStateLabels: Record<EmbeddingArchive['state'], string> = {
  current: 'Up to date', pending: 'Needs sync', absent: 'No archive', disabled: 'Disabled', error: 'Incomplete scan'
};
