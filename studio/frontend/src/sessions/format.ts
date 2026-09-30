import type { RunEvent, RunSnapshot } from './types';

export function formatSessionTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return '';
  return new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'short' }).format(date);
}

export function shortID(value: string) {
  return value.length > 10 ? value.slice(0, 10) : value;
}

export function formatTokenCount(value = 0) {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}m`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return new Intl.NumberFormat().format(value);
}

export function contextPercent(run: RunSnapshot) {
  if (!run.context_size) return 0;
  return Math.min(999, Math.floor((run.context_used || 0) * 100 / run.context_size));
}

export function eventTitle(event: RunEvent) {
  if (event.type === 'tool_call') return event.name || 'Tool call';
  if (event.type === 'trace') return [event.agent, event.name || event.kind].filter(Boolean).join(' · ');
  if (event.type === 'activity') return [event.agent, event.action].filter(Boolean).join(' · ');
  if (event.type === 'question') return 'Question';
  return event.name || event.type;
}

export function eventBody(event: RunEvent) {
  return event.detail || event.question || event.content || '';
}

export function toolArguments(value?: string) {
  if (!value) return '';
  try {
    return `\`\`\`json\n${JSON.stringify(JSON.parse(value), null, 2)}\n\`\`\``;
  } catch {
    return `\`\`\`text\n${value}\n\`\`\``;
  }
}
