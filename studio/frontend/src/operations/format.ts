export function formatNumber(value = 0) { return new Intl.NumberFormat().format(value); }
export function formatBytes(value = 0) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}
export function serviceLabel(id: string) { return id.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' '); }
