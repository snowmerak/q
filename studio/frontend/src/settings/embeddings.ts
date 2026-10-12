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
