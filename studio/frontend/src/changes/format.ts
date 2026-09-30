import type { Proposal } from './types';

export function statusLabel(status: string) {
  if (status === '??') return 'untracked';
  const labels: string[] = [];
  if (status[0] && status[0] !== ' ') labels.push(`index ${status[0]}`);
  if (status[1] && status[1] !== ' ') labels.push(`worktree ${status[1]}`);
  return labels.join(' · ') || status;
}

export function proposalMessage(proposal: Proposal) {
  const subject = `${proposal.type}${proposal.scope ? `(${proposal.scope})` : ''}: ${proposal.summary}`;
  return proposal.body?.length ? `${subject}\n\n${proposal.body.map((line) => `- ${line}`).join('\n')}` : subject;
}
