export const transferSections = [
  { id: 'models', label: 'Models' },
  { id: 'providers', label: 'Providers' },
  { id: 'system-one', label: 'System One' },
  { id: 'runtime', label: 'Runtime' },
  { id: 'services', label: 'Services' },
  { id: 'subagents', label: 'Subagents' },
  { id: 'integrations', label: 'Integrations' }
] as const;

export type TransferSection = typeof transferSections[number]['id'];
export type SettingsBundle = {
  format: 'q-settings';
  version: 1;
  scope: 'global';
  sections: Partial<Record<TransferSection, Record<string, unknown>>>;
};
export type TransferChange = { section: TransferSection; id: string; action: 'add' | 'replace' | 'unchanged'; current?: unknown };
export type TransferPreview = { changes: TransferChange[] };
export const maximumTransferSize = 4 * 1024 * 1024;

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

export function parseSettingsBundle(text: string): SettingsBundle {
  const value: unknown = JSON.parse(text);
  if (!isRecord(value) || value.format !== 'q-settings' || value.version !== 1 || value.scope !== 'global' || !isRecord(value.sections)) {
    throw new Error('Choose a Q settings file with format q-settings, version 1, and global scope.');
  }
  if (Object.keys(value).some((key) => !['format', 'version', 'scope', 'sections'].includes(key))) {
    throw new Error('The settings file contains unsupported fields.');
  }
  for (const [section, items] of Object.entries(value.sections)) {
    if (!transferSections.some((candidate) => candidate.id === section) || !isRecord(items)) {
      throw new Error(`Unsupported settings section: ${section}`);
    }
    if (Object.values(items).some((item) => item === null)) throw new Error(`Invalid empty setting in ${section}.`);
  }
  return value as SettingsBundle;
}

export function transferKey(section: TransferSection, id: string): string {
  return JSON.stringify([section, id]);
}

export function selectedBundle(bundle: SettingsBundle, selected: Set<string>): SettingsBundle {
  const sections: SettingsBundle['sections'] = {};
  for (const section of transferSections) {
    const items = Object.entries(bundle.sections[section.id] ?? {}).filter(([id]) => selected.has(transferKey(section.id, id)));
    if (items.length) sections[section.id] = Object.fromEntries(items);
  }
  return { format: 'q-settings', version: 1, scope: 'global', sections };
}

export function transferLabel(section: TransferSection, id: string): string {
  const names: Record<string, string> = {
    'models:default': 'Default chat model', 'models:embedding': 'Embedding model',
    'system-one:default': 'Default decision model', 'runtime:parallel-agents': 'Maximum parallel agents',
    'runtime:context': 'Context compaction', 'runtime:loom': 'Loom storage', 'runtime:system-prompt': 'System prompt',
    'services:gateway': 'Gateway listener', 'services:system-one': 'System One listener',
    'services:library': 'Library listener', 'services:workspace-memory': 'Workspace Memory listener', 'services:usage': 'Usage listener'
  };
  if (names[`${section}:${id}`]) return names[`${section}:${id}`];
  const slash = id.indexOf('/');
  const kind = slash < 0 ? id : id.slice(0, slash);
  const name = slash < 0 ? '' : id.slice(slash + 1);
  const kinds: Record<string, string> = {
    role: 'Role', group: 'Model group', 'api-mode': 'Model API mode', provider: 'Provider',
    connection: 'ACP connection', binding: 'External role', profile: 'Profile',
    'mcp-server': 'MCP server', 'mcp-role': 'MCP role', 'lsp-server': 'LSP server', 'lsp-language': 'LSP language'
  };
  return `${kinds[kind] ?? kind}${name ? ` · ${name}` : ''}`;
}

function transferSummaryText(value: unknown): string {
  if (typeof value === 'string') return value || 'Inherit / default';
  if (typeof value === 'number') return String(value);
  if (Array.isArray(value)) {
    if (!value.length) return 'No assignments';
    return value.map((item) => isRecord(item) && typeof item.model === 'string' ? item.model : String(item)).join(', ');
  }
  if (isRecord(value)) {
    if (!Object.keys(value).length) return 'Inherit the chat model';
    if (typeof value.model === 'string') return value.model || 'Inherit / disabled';
    if (typeof value.group === 'string') return `group/${value.group}`;
    if (typeof value.trigger_ratio === 'number' && typeof value.target_ratio === 'number') return `${Math.round(value.trigger_ratio * 100)}% trigger · ${Math.round(value.target_ratio * 100)}% target · ${Math.round(Number(value.recent_ratio) * 100)}% recent`;
    if (typeof value.maximum_artifact_mib === 'number') return `${value.maximum_artifact_mib} MiB per artifact · ${value.maximum_store_mib} MiB total`;
    if (typeof value.base_url === 'string' && value.base_url) return value.base_url;
    if (typeof value.uri === 'string') return value.uri;
    if (typeof value.host === 'string') return `${value.host}:${value.port}`;
    if (typeof value.preset === 'string' && value.preset) return value.preset;
    if (typeof value.command === 'string') return value.command;
    if (typeof value.description === 'string') return value.description;
    if (typeof value.reasoning_effort === 'string') return `Inherited model · ${value.reasoning_effort} reasoning`;
  }
  return JSON.stringify(value);
}

export function transferSummary(value: unknown): string {
  const summary = transferSummaryText(value);
  return summary.length > 140 ? `${summary.slice(0, 140)}…` : summary;
}
