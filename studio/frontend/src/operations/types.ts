export type Totals = {
    calls: number; prompt_tokens: number; completion_tokens: number; total_tokens: number;
    cached_tokens: number; cache_write_tokens: number; estimated_calls: number; cache_estimated_calls: number;
    cache_hit_calls?: number; cache_hit_samples?: number;
  };
export type DimensionTotal = { id?: string; name: string } & Totals;
export type ModelPerformance = {
  name: string; completed: number; failed: number; stopped: number; retried: number;
  duration_p50_ms?: number; duration_p95_ms?: number; ttft_p50_ms?: number; ttft_p95_ms?: number; output_rate_p50?: number;
};
export type Usage = {
    from?: string; to?: string; resolution?: string; totals: Totals;
    series?: Array<{ bucket: string } & Totals>;
    models?: DimensionTotal[];
    roles?: DimensionTotal[];
    projects?: DimensionTotal[];
    subagents?: DimensionTotal[];
    performance?: ModelPerformance[];
  };
export type Snapshot = {
    generated_at: string;
    runtime_available: boolean;
    workers: { active_runs: number; resident_runs: number };
    services: Array<{ id: string; state: string; endpoint?: string; detail?: string; leader?: boolean }>;
    usage: Usage;
    usage_error?: string;
    previous_usage?: Totals;
    comparison_error?: string;
    retention: { hot_days: number; database_path: string; database_bytes: number; archive_path: string; archive_files: number; archive_bytes: number };
    logs: string[];
  };
