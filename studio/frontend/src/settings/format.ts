export function roleLabel(role: string) {
  return role.split('-').map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(' ');
}

export function formatKeyDate(value?: string) {
  if (!value) return 'Imported key';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(new Date(value));
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB`;
}
