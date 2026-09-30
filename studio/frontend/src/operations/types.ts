export type Totals = {
    calls: number; prompt_tokens: number; completion_tokens: number; total_tokens: number;
    cached_tokens: number; cache_write_tokens: number; estimated_calls: number; cache_estimated_calls: number;
  };
export type Usage = {
    from?: string; to?: string; resolution?: string; totals: Totals;
    series?: Array<{ bucket: string } & Totals>;
    models?: Array<{ name: string } & Totals>;
    roles?: Array<{ name: string } & Totals>;
  };
export type Snapshot = {
    generated_at: string;
    runtime_available: boolean;
    workers: { active_runs: number; resident_runs: number };
    services: Array<{ id: string; state: string; endpoint?: string; detail?: string; leader?: boolean }>;
    usage: Usage;
    usage_error?: string;
    retention: { hot_days: number; database_path: string; database_bytes: number; archive_path: string; archive_files: number; archive_bytes: number };
    logs: string[];
  };
