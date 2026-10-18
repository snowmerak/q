import type { Message, RunEvent } from './types';

export type TimelineEntry =
  | { kind: 'message'; message: Message; streaming?: boolean }
  | { kind: 'thinking'; content: string }
  | { kind: 'activity'; event: RunEvent };

// The latest run replays from cursor zero. Replace its persisted tail instead of
// appending events to it, including after reconnecting halfway through a tool call.
export function buildTimeline(messages: Message[], thoughts: { before: number; content: string }[], events: RunEvent[]): TimelineEntry[] {
  let lastUser = -1;
  for (let index = messages.length - 1; index >= 0; index--) {
    if (messages[index].role === 'user') { lastUser = index; break; }
  }
  const replay = events.length > 0 && lastUser >= 0;
  const saved = replay ? messages.slice(0, lastUser + 1) : messages;
  const entries: TimelineEntry[] = [];
  for (let index = 0; index <= saved.length; index++) {
    for (const thought of thoughts.filter(thought => thought.before === index && (!replay || index < saved.length))) {
      if (thought.content) entries.push({ kind: 'thinking', content: thought.content });
    }
    if (index < saved.length) entries.push({ kind: 'message', message: saved[index] });
  }
  if (!replay) return entries;

  let response: Extract<TimelineEntry, { kind: 'message' }> | undefined;
  let thinking: Extract<TimelineEntry, { kind: 'thinking' }> | undefined;
  function finishResponse(content: string, usage?: Message['usage']) {
    if (response) {
      response.message = { role: 'assistant', content: content || response.message.content, usage };
      response.streaming = false;
    } else if (content) {
      const last = entries[entries.length - 1];
      if (last?.kind === 'message' && last.message.role === 'assistant' && last.message.content === content) {
        if (usage) last.message.usage = usage;
      } else entries.push({ kind: 'message', message: { role: 'assistant', content, usage } });
    }
    response = undefined;
    thinking = undefined;
  }

  for (const event of events) {
    if (event.type === 'stream') {
      if (event.kind === 'thinking') {
        if (event.start || !thinking) {
          thinking = { kind: 'thinking', content: '' };
          entries.push(thinking);
        }
        thinking.content += event.content || '';
      } else if (event.kind === 'response') {
        if (event.start || !response) {
          finishResponse('');
          response = { kind: 'message', message: { role: 'assistant', content: '' }, streaming: true };
          entries.push(response);
        }
        response.message.content += event.content || '';
      }
    } else if (event.type === 'message' && event.role === 'assistant') {
      finishResponse(event.content || '', event.usage);
    } else if (event.type === 'tool_call') {
      finishResponse('');
      entries.push({ kind: 'message', message: { role: 'assistant', tool_calls: [{ id: event.call_id || '', function: { name: event.name, arguments: event.content } }] } });
    } else if (event.type === 'message' && event.role === 'tool') {
      entries.push({ kind: 'message', message: { role: 'tool', name: event.name, tool_call_id: event.call_id, content: event.content } });
    } else if (event.type === 'result') {
      finishResponse(event.content || '', event.usage);
    } else if (['activity', 'trace', 'question', 'error', 'cancelled', 'recovered', 'control'].includes(event.type)) {
      if (['error', 'cancelled', 'recovered'].includes(event.type)) finishResponse('');
      entries.push({ kind: 'activity', event });
    }
  }
  return entries;
}
