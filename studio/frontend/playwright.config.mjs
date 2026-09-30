import { defineConfig } from '@playwright/test';
import { createServer } from 'node:net';
import { fileURLToPath } from 'node:url';

let port = Number(process.env.Q_STUDIO_BROWSER_PORT || 0);
if (!port) {
  const listener = createServer();
  await new Promise((resolve, reject) => listener.once('error', reject).listen(0, '127.0.0.1', resolve));
  port = listener.address().port;
  await new Promise((resolve) => listener.close(resolve));
  process.env.Q_STUDIO_BROWSER_PORT = String(port);
}
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: './e2e',
  timeout: 45_000,
  expect: { timeout: 20_000 },
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: { baseURL, browserName: 'chromium', viewport: { width: 1440, height: 900 }, trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  globalTeardown: './e2e/teardown.mjs',
  webServer: {
    command: 'go test -v ./studio -run ^TestStudioBrowserFixture$ -count=1 -timeout=5m',
    cwd: fileURLToPath(new URL('../../', import.meta.url)),
    env: { Q_STUDIO_BROWSER_ADDRESS: `127.0.0.1:${port}` },
    url: `${baseURL}/api/v1/status`,
    timeout: 120_000,
    reuseExistingServer: false,
    stdout: 'pipe',
    stderr: 'pipe'
  }
});
