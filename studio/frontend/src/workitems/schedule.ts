export type TicketSchedule = { not_before?: string; every?: string; paused?: boolean };
export type ScheduleDraft = { enabled: boolean; notBefore: string; every: string; paused: boolean };

export function emptyScheduleDraft(): ScheduleDraft {
  return { enabled: false, notBefore: '', every: '', paused: false };
}

export function scheduleDraft(schedule?: TicketSchedule): ScheduleDraft {
  const draft = emptyScheduleDraft();
  if (!schedule) return draft;
  draft.enabled = true;
  draft.every = schedule.every || '';
  draft.paused = schedule.paused || false;
  if (schedule.not_before && !schedule.not_before.startsWith('0001-')) {
    const date = new Date(schedule.not_before);
    const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
    draft.notBefore = local.toISOString().slice(0, 19);
  }
  return draft;
}

export function scheduleInput(draft: ScheduleDraft): TicketSchedule {
  if (!draft.enabled) return {};
  return {
    ...(draft.notBefore ? { not_before: new Date(draft.notBefore).toISOString() } : {}),
    every: draft.every.trim(), paused: draft.paused
  };
}

export function scheduleTime(value?: string): string {
  return value && !value.startsWith('0001-') ? new Date(value).toLocaleString() : 'Now';
}
