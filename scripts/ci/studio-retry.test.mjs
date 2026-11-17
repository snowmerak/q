import assert from 'node:assert/strict';
import { test } from 'node:test';
import { backoff, retryPlan, retryRun, validateRun } from './studio-retry.mjs';

const repository = 'snowmerak/q';
const run = (overrides = {}) => ({
  id: 42, status: 'completed', conclusion: 'failure', run_attempt: 1,
  repository: { full_name: repository }, head_repository: { full_name: repository },
  head_sha: 'immutable-source', head_branch: 'v0.0.2', event: 'push',
  path: '.github/workflows/studio-desktop.yml', ...overrides,
});
// Captured from the first v0.0.2 attempt's ARM64 job annotations.
const capacityMessage = 'The job was not acquired by Runner of type hosted even after multiple attempts';
const job = (overrides = {}) => ({
  id: 7, name: 'darwin/arm64', conclusion: 'cancelled', runner_name: '', steps: [],
  check_run_url: `https://api.github.com/repos/${repository}/check-runs/7`,
  annotations: [{ message: capacityMessage }], ...overrides,
});

test('the actual cancelled ARM64 allocation failure is retryable, with bounded backoff', () => {
  for (let attempt = 1; attempt <= 3; attempt++) {
    assert.deepEqual(retryPlan(run({ run_attempt: attempt }), [job(), job({ id: 8, conclusion: 'success' }), job({ conclusion: 'skipped' })]),
      { jobs: [7], delay: [60_000, 180_000, 600_000][attempt - 1] });
  }
  assert.equal(retryPlan(run({ run_attempt: 4 }), [job()]).jobs, undefined);
});

test('success, running state, manual cancellation, and unknown failures never retry', () => {
  for (const overrides of [{ conclusion: 'success' }, { conclusion: 'cancelled' }, { status: 'in_progress' }, { run_attempt: 0 }]) {
    assert.equal(retryPlan(run(overrides), [job()]).jobs, undefined);
  }
  assert.equal(retryPlan(run(), [job({ annotations: [{ message: capacityMessage }, { message: 'The run was canceled by @snowmerak.' }] })]).jobs, undefined);
  assert.equal(retryPlan(run(), [job({ annotations: [] })]).jobs, undefined);
  assert.equal(retryPlan(run(), [job(), job({ id: 9, conclusion: 'failure', annotations: [] })]).jobs, undefined);
});

test('network failures retry only recognizable setup/download/upload steps', () => {
  for (const name of ['Run actions/setup-go@v6', 'Install Studio frontend dependencies', 'Upload installation artifacts']) {
    assert.deepEqual(retryPlan(run(), [job({ conclusion: 'failure', runner_name: 'runner', steps: [{ name, conclusion: 'failure' }], annotations: [{ message: 'Error: HTTP status code 503' }] })]).jobs, [7]);
  }
  for (const name of ['Build native packages (Unix)', 'Check Studio frontend', 'Test assembled engine and desktop module (Unix)']) {
    assert.equal(retryPlan(run(), [job({ conclusion: 'failure', steps: [{ name, conclusion: 'failure' }], annotations: [{ message: 'ECONNRESET' }] })]).jobs, undefined);
    assert.equal(retryPlan(run(), [job({ conclusion: 'failure', steps: [{ name, conclusion: 'failure' }], annotations: [{ message: 'The runner has received a shutdown signal' }] })]).jobs, undefined);
  }
  const invalidSetup = job({ conclusion: 'failure', steps: [{ name: 'Run actions/setup-go@v6', conclusion: 'failure' }], annotations: [{ message: 'Unsupported Go version' }] });
  assert.equal(retryPlan(run(), [invalidSetup]).jobs, undefined);
});

test('only the intended workflow in this repository can be retried', () => {
  validateRun(run(), repository);
  validateRun(run({ event: 'workflow_dispatch', head_branch: 'main' }), repository);
  for (const overrides of [{ event: 'pull_request' }, { head_repository: { full_name: 'fork/q' } }, { path: '.github/workflows/other.yml' }, { head_branch: 'main' }]) {
    assert.throws(() => validateRun(run(overrides), repository), /untrusted/);
  }
});

// Exercise the controller across REST boundaries without dispatching real CI.
function harness({ outcomes = ['success'], beforeRetry, postError, neverStart = false, jobs = [job()] } = {}) {
  let current = run();
  let clock = 0;
  let pending;
  let oldSnapshot = false;
  const calls = [];
  const waits = [];
  const reports = [];
  return {
    calls, waits, reports,
    execute: () => retryRun({
      repository, runID: 42, now: () => clock, report: (message) => reports.push(message),
      wait: async (ms) => {
        waits.push(ms);
        clock += ms;
        if (backoff.includes(ms) && beforeRetry) current = run(beforeRetry);
      },
      api: async (method, path) => {
        calls.push([method, path]);
        if (method === 'POST') {
          if (postError) throw new Error('Network timeout; POST outcome unknown');
          pending = outcomes.shift() ?? 'failure';
          oldSnapshot = true;
          return;
        }
        if (path.includes('/annotations')) return jobs.find((j) => path.includes(`/check-runs/${j.id}/`)).annotations;
        if (path.includes('/jobs?')) return { jobs };
        if (pending) {
          if (neverStart) return structuredClone(current);
          // GitHub may expose the old completed attempt briefly after POST.
          if (oldSnapshot) oldSnapshot = false;
          else {
            current = run({ run_attempt: current.run_attempt + 1, conclusion: pending });
            pending = undefined;
          }
        }
        return structuredClone(current);
      },
    }),
  };
}

test('controller waits for a new attempt and preserves successful jobs', async () => {
  const fixture = harness();
  await fixture.execute();
  assert.deepEqual(fixture.waits, [60_000, 30_000, 30_000]);
  assert.deepEqual(fixture.calls.filter(([method]) => method === 'POST'), [['POST', '/repos/snowmerak/q/actions/jobs/7/rerun']]);
  assert.match(fixture.reports.at(-1), /success/);
});

test('three additional failures exhaust the retry budget', async () => {
  const fixture = harness({ outcomes: ['failure', 'failure', 'failure'] });
  await fixture.execute();
  assert.deepEqual(fixture.waits.filter((ms) => backoff.includes(ms)), [60_000, 180_000, 600_000]);
  assert.equal(fixture.calls.filter(([method]) => method === 'POST').length, 3);
  assert.match(fixture.reports.at(-1), /budget exhausted/);
});

test('multiple infrastructure failures use the failed-jobs endpoint', async () => {
  const fixture = harness({ jobs: [job(), job({ id: 8, check_run_url: 'https://api.github.com/repos/snowmerak/q/check-runs/8' })] });
  await fixture.execute();
  assert.deepEqual(fixture.calls.filter(([method]) => method === 'POST'), [['POST', '/repos/snowmerak/q/actions/runs/42/rerun-failed-jobs']]);
});

test('manual rerun, cancellation or success during backoff prevents the POST', async () => {
  for (const beforeRetry of [{ run_attempt: 2 }, { conclusion: 'cancelled' }, { conclusion: 'success' }, { status: 'in_progress' }, { head_sha: 'changed' }]) {
    const fixture = harness({ beforeRetry });
    await fixture.execute();
    assert.equal(fixture.calls.filter(([method]) => method === 'POST').length, 0);
  }
});

test('cancellation of a rerun stops all further attempts', async () => {
  const fixture = harness({ outcomes: ['cancelled'] });
  await fixture.execute();
  assert.equal(fixture.calls.filter(([method]) => method === 'POST').length, 1);
  assert.match(fixture.reports.at(-1), /cancelled/);
});

test('an ambiguous mutation error is not replayed', async () => {
  const fixture = harness({ postError: true });
  await assert.rejects(fixture.execute(), /outcome unknown/);
  assert.equal(fixture.calls.filter(([method]) => method === 'POST').length, 1);
});

test('a rerun that never starts times out without another POST', async () => {
  const fixture = harness({ neverStart: true });
  await assert.rejects(fixture.execute(), /Timed out waiting/);
  assert.equal(fixture.calls.filter(([method]) => method === 'POST').length, 1);
});
