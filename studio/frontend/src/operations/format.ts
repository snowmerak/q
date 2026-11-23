import type { Totals } from './types';

const integer = new Intl.NumberFormat();
const decimal = new Intl.NumberFormat(undefined, { maximumFractionDigits: 1 });
const percent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1 });
const signedPercent = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 1, signDisplay: 'exceptZero' });

export function formatNumber(value = 0) { return integer.format(value); }
export function formatAverage(total: number, calls: number) { return calls ? decimal.format(total / calls) : '—'; }
export function formatRate(count: number, total: number) { return total ? percent.format(count / total) : '—'; }
export function formatDuration(value?: number) {
  if (value === undefined) return '—';
  return value < 1000 ? `${decimal.format(value)} ms` : `${decimal.format(value / 1000)} s`;
}
export function formatComparison(current: number, previous: number) {
  if (previous) return signedPercent.format((current - previous) / previous);
  return current ? `+${formatNumber(current)}` : '0';
}
export function formatCacheTokens(usage: Totals, field: 'cached_tokens' | 'cache_write_tokens') {
  return usage.calls > usage.cache_estimated_calls ? formatNumber(usage[field]) : '—';
}
export function formatCacheHitRate(usage: Totals) {
  if (usage.calls <= usage.cache_estimated_calls) return '—';
  return formatRate(usage.cached_tokens, usage.prompt_tokens);
}
export function formatBytes(value = 0) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}
export function serviceLabel(id: string) { return id.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' '); }
