import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import * as fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { buildMatrix, checkExistingAssets, releaseAssets } from './studio-release.mjs';

test('published builds skip unconfigured Macs; development builds retain all six targets', () => {
  const targets = (publish, configured) => buildMatrix(publish, configured).include.map((entry) => entry.target);
  assert.deepEqual(targets(true, false), ['windows/amd64', 'windows/arm64', 'linux/amd64', 'linux/arm64']);
  assert.equal(targets(true, true).length, 6);
  assert.equal(targets(false, false).length, 6);
});

async function fixture(t, macArches = []) {
  const parent = path.resolve(os.tmpdir());
  const directory = await fs.mkdtemp(path.join(parent, 'q-release-assets-'));
  t.after(async () => {
    assert.equal(path.dirname(directory), parent);
    assert.ok(path.basename(directory).startsWith('q-release-assets-'));
    await fs.rm(directory, { recursive: true, force: true });
  });
  const names = [];
  for (const arch of ['amd64', 'arm64']) {
    for (const suffix of [`windows-${arch}-setup.exe`, `linux-${arch}.deb`, `linux-${arch}.rpm`, `windows-${arch}.zip`, `linux-${arch}.zip`]) names.push(`Q-Studio-0.1.0-${suffix}`);
    if (macArches.includes(arch)) for (const suffix of ['dmg', 'zip']) names.push(`Q-Studio-0.1.0-darwin-${arch}.${suffix}`);
  }
  for (const name of names) await fs.writeFile(path.join(directory, name), `binary ${name}`);
  return { directory, names };
}

for (const arches of [[], ['arm64'], ['amd64', 'arm64']]) {
  test(`checksums cover exactly the required and successful optional assets (${arches.length} Mac targets)`, async (t) => {
    const { directory, names } = await fixture(t, arches);
    const result = await releaseAssets(directory, '0.1.0');
    assert.equal(result.assets.length, 11 + arches.length * 2);
    assert.equal(result.omittedMacTargets.length, 2 - arches.length);
    const lines = (await fs.readFile(path.join(directory, 'Q-Studio-0.1.0-SHA256SUMS.txt'), 'utf8')).trim().split('\n');
    assert.equal(lines.length, names.length);
    for (const line of lines) {
      const [digest, name] = line.split('  ');
      assert.ok(names.includes(name));
      assert.equal(digest, createHash('sha256').update(`binary ${name}`).digest('hex'));
    }
  });
}

test('missing mandatory packages block publication; incomplete Mac pairs are omitted', async (t) => {
  const missing = await fixture(t);
  await fs.unlink(path.join(missing.directory, missing.names[0]));
  await assert.rejects(releaseAssets(missing.directory, '0.1.0'), /Required Windows\/Linux asset missing/);
  const empty = await fixture(t);
  await fs.writeFile(path.join(empty.directory, empty.names[0]), '');
  await assert.rejects(releaseAssets(empty.directory, '0.1.0'), /Required Windows\/Linux asset missing/);
  const incomplete = await fixture(t, ['arm64']);
  await fs.unlink(path.join(incomplete.directory, 'Q-Studio-0.1.0-darwin-arm64.zip'));
  const result = await releaseAssets(incomplete.directory, '0.1.0');
  assert.equal(result.assets.length, 11);
  assert.ok(result.omittedMacTargets.includes('darwin/arm64'));
  assert.ok(!result.assets.some((name) => name.includes('darwin-')));
});

test('an existing release cannot be left with orphaned assets outside its new manifest', () => {
  checkExistingAssets(['windows.zip'], ['windows.zip', 'mac.zip']);
  assert.throws(() => checkExistingAssets(['windows.zip', 'mac.zip'], ['windows.zip']), /Existing release has assets absent/);
});
