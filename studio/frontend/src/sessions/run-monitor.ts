import { apiError } from '../api';
import type { RunPage, RunSnapshot } from './types';

export type RunTarget = { workspaceRoot: string; sessionID: string };

type RunObserver = {
  page: (page: RunPage) => void;
  complete: () => Promise<void>;
  retry: (cause: unknown) => void;
};

export function terminalRun(status: string) {
  return ['completed', 'cancelled', 'failed', 'interrupted', 'redirected'].includes(status);
}

export function runStatusLabel(run: RunSnapshot) {
  if (run.status === 'waiting') return 'Waiting for your answer';
  if (run.status === 'paused') return 'Paused';
  if (run.status === 'running' || run.status === 'queued') return 'Running';
  if (run.status === 'redirecting') return 'Applying guidance…';
  if (run.status === 'completed') return run.outcome === 'succeeded' ? 'Completed' : run.outcome || 'Completed';
  if (run.status === 'cancelled') return 'Turn stopped';
  if (run.status === 'cancelling') return 'Stopping…';
  if (run.status === 'interrupted') return 'Interrupted · send a message to recover';
  if (run.status === 'failed') return 'Turn failed';
  return run.status;
}

function retryDelay(signal: AbortSignal) {
  if (signal.aborted) return Promise.resolve();
  return new Promise<void>((resolve) => {
    const finish = () => {
      clearTimeout(timer);
      signal.removeEventListener('abort', finish);
      resolve();
    };
    const timer = setTimeout(finish, 1000);
    signal.addEventListener('abort', finish, { once: true });
  });
}

// One monitor belongs to one mounted session view. Switching sessions or leaving
// the view aborts its request and retry timer; late responses cannot notify it.
export class RunMonitor {
  private controller: AbortController | null = null;

  constructor(private observer: RunObserver) {}

  stop() {
    this.controller?.abort();
    this.controller = null;
  }

  start(target: RunTarget, runID: string) {
    this.stop();
    const controller = new AbortController();
    this.controller = controller;
    void this.poll(target, runID, controller.signal);
  }

  private async poll(target: RunTarget, runID: string, signal: AbortSignal) {
    let cursor = 0;
    while (!signal.aborted) {
      try {
        const query = new URLSearchParams({ workspace_root: target.workspaceRoot, after: String(cursor), wait_ms: '25000' });
        const response = await fetch(`/api/v1/sessions/${encodeURIComponent(target.sessionID)}/runs/${encodeURIComponent(runID)}/events?${query}`, { signal });
        if (!response.ok) throw new Error(await apiError(response));
        const page = (await response.json()) as RunPage;
        if (signal.aborted) return;
        this.observer.page(page);
        if (signal.aborted) return;
        const redirect = page.events.find((record) => record.event.type === 'redirect' && record.event.run_id);
        if (redirect) {
          runID = redirect.event.run_id!;
          cursor = 0;
          continue;
        }
        if (page.events.length) cursor = page.events[page.events.length - 1].cursor;
        if (terminalRun(page.run.status)) {
          await this.observer.complete();
          return;
        }
      } catch (cause) {
        if (signal.aborted) return;
        this.observer.retry(cause);
        await retryDelay(signal);
      }
    }
  }
}
