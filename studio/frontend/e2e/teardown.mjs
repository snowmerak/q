export default async function teardown(config) {
  // Let Go close Gateway, Loom and storage and remove its temporary fixtures
  // before Playwright stops the webServer process.
  const baseURL = config.projects[0].use.baseURL;
  await fetch(`${baseURL}/_test/shutdown`, { method: 'POST', signal: AbortSignal.timeout(5000) });
}
