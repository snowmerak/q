import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import * as fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { configuration, developerIdentity, redact, signRelease } from './macos-release.mjs';

const team = 'TEAM123456';
const fingerprint = 'A'.repeat(40);
const secrets = {
  APPLE_DEVELOPER_ID_CERTIFICATE_BASE64: Buffer.from('fixture encrypted p12').toString('base64'),
  APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD: 'fixture-password-do-not-log',
  APPLE_DEVELOPER_TEAM_ID: team,
  APPLE_NOTARY_KEY_BASE64: Buffer.from('-----BEGIN PRIVATE KEY-----\nfixture-notary-key\n-----END PRIVATE KEY-----\n').toString('base64'),
  APPLE_NOTARY_KEY_ID: 'KEY1234567', APPLE_NOTARY_ISSUER_ID: '11111111-2222-4333-8444-555555555555',
  Q_STUDIO_VERSION: '0.0.4', Q_STUDIO_TARGET: 'darwin/arm64',
};

test('publishing fails closed on missing or malformed credentials and target input', () => {
  assert.throws(() => configuration({}), /Missing macOS release secrets/);
  for (const [name, value] of Object.entries({
    APPLE_DEVELOPER_ID_CERTIFICATE_BASE64: 'not base64', APPLE_NOTARY_KEY_BASE64: Buffer.from('not PEM').toString('base64'),
    APPLE_DEVELOPER_TEAM_ID: 'wrong', APPLE_NOTARY_ISSUER_ID: 'wrong',
    Q_STUDIO_VERSION: '../../other', Q_STUDIO_TARGET: 'darwin/../../other',
  })) assert.throws(() => configuration({ ...secrets, [name]: value }));
});

test('the actual --check entry point rejects missing secrets without running signing tools', async () => {
  const env = { ...process.env };
  for (const name of Object.keys(secrets)) delete env[name];
  await assert.rejects(promisify(execFile)(process.execPath, [fileURLToPath(new URL('./macos-release.mjs', import.meta.url)), '--check'], { env }),
    (error) => error.code === 1 && error.stderr.includes('Missing macOS release secrets:') && !error.stderr.includes('fixture-password'));
});

test('only a unique Developer ID Application identity from the configured team is accepted', () => {
  const valid = `  1) ${fingerprint} "Developer ID Application: Q contributors (${team})"\n`;
  assert.equal(developerIdentity(valid, team), fingerprint);
  for (const output of ['', valid.replace('Developer ID Application', 'Apple Development'), valid.replace(team, 'OTHER12345'), valid + valid]) {
    assert.throws(() => developerIdentity(output, team), /exactly one/);
  }
});

test('diagnostics redact multiline private keys and password values', () => {
  const key = Buffer.from(secrets.APPLE_NOTARY_KEY_BASE64, 'base64').toString();
  const text = redact(`failure: ${secrets.APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD}\n${key}`, [key, secrets.APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD]);
  assert.equal(text, 'failure: [redacted]\n[redacted]');
});

// This adapter exercises orchestration and failure boundaries. It does not
// emulate Apple's cryptography, notarization service or Gatekeeper policy.
async function fixture(t, {
  rejectedSubmission = 0, failLabel, failLabels = [],
  searchListOutput = '    "/original/login.keychain-db"\n',
  signingDetails = `TeamIdentifier=${team}\nCodeDirectory flags=0x10000(runtime)\n`,
} = {}) {
  const parent = path.resolve(os.tmpdir());
  const directory = await fs.mkdtemp(path.join(parent, 'q-signing-test-'));
  t.after(async () => {
    assert.equal(path.dirname(directory), parent);
    assert.ok(path.basename(directory).startsWith('q-signing-test-'));
    await fs.rm(directory, { recursive: true, force: true });
  });
  const root = path.join(directory, 'project with spaces');
  const runnerTemp = path.join(directory, 'runner temp');
  await fs.mkdir(path.join(root, 'bin'), { recursive: true });
  await fs.mkdir(runnerTemp);
  const image = path.join(root, 'bin/Q-Studio-0.0.4-darwin-arm64.dmg');
  const archive = path.join(root, 'bin/Q-Studio-0.0.4-darwin-arm64.zip');
  await fs.writeFile(image, 'unsigned image');
  await fs.writeFile(archive, 'unsigned archive');
  const calls = [];
  let submissions = 0;
  let workspace;
  let copiedApp;
  const run = async (label, command, args) => {
    calls.push({ label, command, args });
    if (label === failLabel || failLabels.includes(label)) throw new Error(`Native stage failed: ${label}`);
    let stdout = '';
    let stderr = '';
    if (label === 'Read keychain search list') stdout = searchListOutput;
    if (label === 'Create temporary keychain') {
      workspace = path.dirname(args.at(-1));
      assert.equal((await fs.readFile(path.join(workspace, 'developer-id.p12'))).toString(), 'fixture encrypted p12');
      if (process.platform !== 'win32') assert.equal((await fs.stat(path.join(workspace, 'notary.p8'))).mode & 0o777, 0o600);
    }
    if (label === 'Find Developer ID') stdout = ` 1) ${fingerprint} "Developer ID Application: Q contributors (${team})"\n`;
    if (label === 'Mount disk image') {
      const mount = args[args.indexOf('-mountpoint') + 1];
      const app = path.join(mount, 'Q Studio.app');
      if (args.at(-1) === image) {
        await fs.mkdir(path.join(app, 'Contents/MacOS'), { recursive: true });
        await fs.writeFile(path.join(app, 'Contents/MacOS/q-engine'), 'engine');
        await fs.writeFile(path.join(app, 'Contents/MacOS/Q Studio'), 'app');
      } else await fs.cp(copiedApp, app, { recursive: true });
    }
    if (label === 'Copy app bundle') {
      copiedApp = args[1];
      await fs.cp(args[0], args[1], { recursive: true });
    }
    if (label === 'Inspect signing identity') stderr = signingDetails;
    if (label === 'Archive app for notarization') await fs.writeFile(args.at(-1), 'submission');
    if (label === 'Archive notarized app') {
      await fs.access(path.join(copiedApp, 'ticket'));
      await fs.writeFile(args.at(-1), 'verified app ZIP');
    }
    if (label === 'Submit to Apple notarization') {
      submissions++;
      assert.equal(args[0], 'notarytool');
      assert.ok(!args.includes(secrets.APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD));
      stdout = JSON.stringify({ status: submissions === rejectedSubmission ? 'Invalid' : 'Accepted' });
    }
    if (label === 'Staple app ticket') await fs.writeFile(path.join(args.at(-1), 'ticket'), 'accepted');
    if (label === 'Validate app ticket') await fs.access(path.join(args.at(-1), 'ticket'));
    if (label === 'Create distribution DMG') await fs.writeFile(args.at(-1), 'verified DMG');
    if (label === 'Extract distribution ZIP') await fs.cp(copiedApp, path.join(args.at(-1), 'Q Studio.app'), { recursive: true });
    return { stdout, stderr };
  };
  return { calls, runnerTemp, image, archive, workspace: () => workspace,
    execute: () => signRelease({ root, env: { ...secrets, RUNNER_TEMP: runnerTemp }, run, platform: 'darwin' }) };
}

test('final DMG and app ZIP replace unsigned inputs only after both notarizations and final assessments', async (t) => {
  const f = await fixture(t);
  await f.execute();
  assert.equal(await fs.readFile(f.image, 'utf8'), 'verified DMG');
  assert.equal(await fs.readFile(f.archive, 'utf8'), 'verified app ZIP');
  assert.equal(f.calls.filter((call) => call.label === 'Submit to Apple notarization').length, 2);
  const signatures = f.calls.filter((call) => call.label === 'Sign artifact');
  assert.ok(signatures[0].args.at(-1).endsWith('q-engine'));
  assert.ok(signatures[1].args.at(-1).endsWith('Q Studio.app'));
  assert.ok(signatures[0].args.includes('runtime'));
  assert.ok(signatures[1].args.includes('runtime'));
  assert.ok(!signatures[2].args.includes('runtime'));
  assert.equal(f.calls.filter((call) => call.label === 'Assess app with Gatekeeper').length, 3);
  assert.deepEqual(await fs.readdir(f.runnerTemp), []);
  assert.ok(f.calls.some((call) => call.label === 'Delete temporary keychain'));
  assert.deepEqual(f.calls.findLast((call) => call.label === 'Restore keychain search list').args,
    ['list-keychains', '-d', 'user', '-s', '/original/login.keychain-db']);
});

for (const rejectedSubmission of [1, 2]) {
  test(`notarization rejection ${rejectedSubmission} leaves distribution inputs unchanged and cleans credentials`, async (t) => {
    const f = await fixture(t, { rejectedSubmission });
    await assert.rejects(f.execute(), /did not accept/);
    assert.equal(await fs.readFile(f.image, 'utf8'), 'unsigned image');
    assert.equal(await fs.readFile(f.archive, 'utf8'), 'unsigned archive');
    assert.deepEqual(await fs.readdir(f.runnerTemp), []);
    assert.ok(f.calls.some((call) => call.label === 'Delete temporary keychain'));
  });
}

for (const failLabel of ['Mount disk image', 'Copy app bundle', 'Verify engine signature', 'Assess DMG with Gatekeeper']) {
  test(`${failLabel} failure blocks upload-ready artifacts and cleans owned resources`, async (t) => {
    const f = await fixture(t, { failLabel });
    await assert.rejects(f.execute(), /Native stage failed/);
    assert.equal(await fs.readFile(f.image, 'utf8'), 'unsigned image');
    assert.deepEqual(await fs.readdir(f.runnerTemp), []);
    if (['Mount disk image', 'Copy app bundle'].includes(failLabel)) assert.ok(f.calls.some((call) => call.label === 'Detach signing mount during cleanup'));
  });
}

test('an unreadable search list is never replaced with an empty one', async (t) => {
  const f = await fixture(t, { searchListOutput: '' });
  await assert.rejects(f.execute(), /Could not read/);
  assert.ok(!f.calls.some((call) => call.label === 'Restore keychain search list'));
  assert.deepEqual(await fs.readdir(f.runnerTemp), []);
});

test('a mount that cannot be detached is not traversed during cleanup; private keys are still removed', async (t) => {
  const f = await fixture(t, { failLabels: ['Copy app bundle', 'Detach signing mount during cleanup'] });
  await assert.rejects(f.execute(), /Detach signing mount during cleanup/);
  assert.ok((await fs.stat(f.workspace())).isDirectory());
  for (const name of ['developer-id.p12', 'notary.p8']) {
    await assert.rejects(fs.access(path.join(f.workspace(), name)), { code: 'ENOENT' });
  }
  assert.ok(f.calls.some((call) => call.label === 'Delete temporary keychain'));
  assert.equal(await fs.readFile(f.image, 'utf8'), 'unsigned image');
});

test('a wrong signing team or missing hardened runtime blocks notarization', async (t) => {
  for (const signingDetails of ['TeamIdentifier=OTHER12345\nCodeDirectory flags=0x10000(runtime)\n', `TeamIdentifier=${team}\nCodeDirectory flags=0x0(none)\n`]) {
    const f = await fixture(t, { signingDetails });
    await assert.rejects(f.execute(), /expected team identity or hardened runtime/);
    assert.ok(!f.calls.some((call) => call.label === 'Submit to Apple notarization'));
    assert.deepEqual(await fs.readdir(f.runnerTemp), []);
  }
});
