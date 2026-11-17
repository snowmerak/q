import { appendFile, readFile } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import { setTimeout as sleep } from 'node:timers/promises';

export const backoff = [60_000, 180_000, 600_000];
const workflowPath = '.github/workflows/studio-desktop.yml';
const failed = (item) => ['failure', 'timed_out', 'cancelled'].includes(item.conclusion);
const setupStep = /^(?:Set up job|Run actions\/(?:checkout|setup-go|setup-node|download-artifact)@\S+|Install Studio frontend dependencies|Install Linux native GUI build dependencies|Prepare NSIS|Upload installation artifacts)$/;
const manualCancellation = /(?:cancell?ed by @|cancell?ed by (?:a |the )?user|user requested cancellation)/i;
const runnerFailure = /(?:job was not acquired by Runner|runner.*lost communication|lost communication with (?:the )?(?:server|runner)|runner has received a shutdown signal|internal server error.*runner)/i;
const transientNetwork = /(?:\b(?:EAI_AGAIN|ETIMEDOUT|ECONNRESET|ENETUNREACH)\b|temporary failure in name resolution|connection (?:timed out|reset)|(?:HTTP|status(?: code)?|response code)[^\n]{0,30}\b(?:502|503|504)\b)/i;

// Unknown failures are deliberately not retried. Compilation and test output
// cannot opt into network retries just by containing a matching error string.
export function retryPlan(run, jobs) {
  if (run.status !== 'completed' || !['failure', 'timed_out'].includes(run.conclusion)) {
    return { reason: `Run is ${run.status}/${run.conclusion}; no retry.` };
  }
  if (!Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1 || run.run_attempt > backoff.length) {
    return { reason: 'Retry budget exhausted (at most three additional attempts).' };
  }
  const candidates = jobs.filter(failed);
  if (!candidates.length) return { reason: 'No failed jobs to retry.' };
  for (const job of candidates) {
    const messages = (job.annotations ?? []).map((a) => a.message ?? '').join('\n');
    if (manualCancellation.test(messages)) return { reason: 'A job was manually cancelled.' };
    const steps = job.steps ?? [];
    const failedSteps = steps.filter(failed);
    if (failedSteps.some((step) => step.conclusion === 'failure' && !setupStep.test(step.name))) {
      return { reason: 'A build, test or release step failed; inspect it manually.' };
    }
    const capacity = !job.runner_name && !steps.length && /capacity constraints/i.test(messages);
    const network = failedSteps.length > 0 && failedSteps.every((s) => setupStep.test(s.name)) && transientNetwork.test(messages);
    if (!runnerFailure.test(messages) && !capacity && !network) {
      return { reason: 'A failed job has no recognized infrastructure error; inspect it manually.' };
    }
  }
  return { jobs: candidates.map((job) => job.id), delay: backoff[run.run_attempt - 1] };
}

export function validateRun(run, repository) {
  if (run.repository?.full_name !== repository || run.head_repository?.full_name !== repository ||
      run.path !== workflowPath || !['push', 'workflow_dispatch'].includes(run.event) ||
      (run.event === 'push' && !/^v\d+\.\d+\.\d+$/.test(run.head_branch))) {
    throw new Error('Refusing to retry an untrusted or unrelated workflow run.');
  }
}

// One controller owns the whole retry sequence: a GITHUB_TOKEN-triggered rerun
// need not trigger another workflow_run event. Successful jobs/artifacts survive
// job reruns, and GitHub reruns dependent jobs (including release) automatically.
export async function retryRun({ api, repository, runID, wait = sleep, now = Date.now, report = console.log }) {
  const prefix = `/repos/${repository}`;
  const runPath = `${prefix}/actions/runs/${runID}`;
  let run = await api('GET', runPath);
  validateRun(run, repository);
  const source = run.head_sha;
  for (;;) {
    validateRun(run, repository);
    if (run.head_sha !== source) throw new Error('Workflow source changed.');
    if (run.status !== 'completed' || !['failure', 'timed_out'].includes(run.conclusion) ||
        !Number.isSafeInteger(run.run_attempt) || run.run_attempt < 1 || run.run_attempt > backoff.length) {
      report(retryPlan(run, []).reason);
      return;
    }
    const jobs = await list(api, `${runPath}/attempts/${run.run_attempt}/jobs`, 'jobs');
    for (const job of jobs.filter(failed)) {
      // Derive the endpoint ourselves; never follow an untrusted check_run_url.
      const checkID = Number(job.check_run_url?.split('/').at(-1));
      job.annotations = Number.isSafeInteger(checkID) && checkID > 0
        ? await list(api, `${prefix}/check-runs/${checkID}/annotations`) : [];
    }
    const plan = retryPlan(run, jobs);
    if (!plan.jobs) {
      report(plan.reason);
      return;
    }
    report(`Attempt ${run.run_attempt}: recognized infrastructure failure; waiting ${plan.delay / 1000}s before retry.`);
    await wait(plan.delay);
    const current = await api('GET', runPath);
    validateRun(current, repository);
    if (current.head_sha !== source || current.run_attempt !== run.run_attempt || current.status !== 'completed' || current.conclusion !== run.conclusion) {
      report('Run changed while waiting; leaving it to the current owner.');
      return;
    }
    const endpoint = plan.jobs.length === 1
      ? `${prefix}/actions/jobs/${plan.jobs[0]}/rerun`
      : `${runPath}/rerun-failed-jobs`;
    // Never blindly replay this POST after a timeout: its outcome is unknown.
    await api('POST', endpoint);
    report(`Retry ${run.run_attempt}/3 requested for ${plan.jobs.length} unsuccessful job(s) and their dependents.`);
    const previousAttempt = run.run_attempt;
    const deadline = now() + 65 * 60_000;
    do {
      if (now() >= deadline) throw new Error('Timed out waiting for the rerun; no further retry requested.');
      await wait(30_000);
      run = await api('GET', runPath);
      validateRun(run, repository);
      if (run.head_sha !== source) throw new Error('Workflow source changed.');
      // A cancelled run stays cancelled even if the user reopens the controller.
      if (run.conclusion === 'cancelled') {
        report('Run cancelled; stopping retries.');
        return;
      }
    } while (run.run_attempt <= previousAttempt || run.status !== 'completed');
  }
}

async function list(api, path, key) {
  const all = [];
  for (let page = 1; page <= 10; page++) {
    const data = await api('GET', `${path}?per_page=100&page=${page}`);
    const items = key ? data[key] : data;
    all.push(...items);
    if (items.length < 100) return all;
  }
  throw new Error('Too many jobs or annotations to classify safely.');
}

export async function main(env = process.env) {
  const event = JSON.parse(await readFile(env.GITHUB_EVENT_PATH, 'utf8'));
  if (env.GITHUB_EVENT_NAME !== 'workflow_run' || !Number.isSafeInteger(event.workflow_run?.id) || event.workflow_run.id <= 0 ||
      event.repository?.full_name !== env.GITHUB_REPOSITORY || !env.GH_TOKEN) {
    throw new Error('Expected an authenticated workflow_run event for this repository.');
  }
  const messages = [];
  const report = (message) => { console.log(message); messages.push(message); };
  const api = async (method, path) => {
    const response = await fetch(`https://api.github.com${path}`, {
      method,
      headers: { Authorization: `Bearer ${env.GH_TOKEN}`, Accept: 'application/vnd.github+json', 'X-GitHub-Api-Version': '2022-11-28' },
      signal: AbortSignal.timeout(30_000),
    });
    if (!response.ok) throw new Error(`GitHub ${method} failed with HTTP ${response.status}; no request replayed.`);
    return response.status === 204 || method === 'POST' ? undefined : response.json();
  };
  try {
    await retryRun({ api, repository: env.GITHUB_REPOSITORY, runID: event.workflow_run.id, report });
  } catch (error) {
    messages.push(`Stopped: ${error.message}`);
    throw error;
  } finally {
    if (env.GITHUB_STEP_SUMMARY) await appendFile(env.GITHUB_STEP_SUMMARY, `${messages.join('\n\n')}\n`);
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch((error) => { console.error(error.message); process.exitCode = 1; });
}
