import type { SessionDetail } from './types';

export const historyPageSize = 80;

// Replace an overlapping tail, or prepend an adjacent earlier page. Thought
// anchors are relative to each page; keep them aligned with the merged messages.
export function mergeHistory(earlier: SessionDetail, later: SessionDetail): SessionDetail {
  if (!earlier.history || !later.history || earlier.history.start > later.history.start || earlier.history.end < later.history.start || earlier.history.end > later.history.total) return later;
  const prefix = later.history.start - earlier.history.start;
  return {
    ...later,
    transcript: [...earlier.transcript.slice(0, prefix), ...later.transcript],
    transcript_thoughts: [
      ...(earlier.transcript_thoughts || []).filter(thought => thought.before < prefix),
      ...(later.transcript_thoughts || []).map(thought => ({ ...thought, before: thought.before + prefix }))
    ],
    history: { ...later.history, start: earlier.history.start }
  };
}
