import { createReadStream } from 'node:fs';
import { createHash } from 'node:crypto';
import { appendFile, readFile, stat, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { pathToFileURL } from 'node:url';

export function buildMatrix(publish, macConfigured) {
  const include = [
    { runner: 'windows-2025', target: 'windows/amd64', key: 'windows-amd64' },
    { runner: 'windows-11-arm', target: 'windows/arm64', key: 'windows-arm64' },
    { runner: 'ubuntu-24.04', target: 'linux/amd64', key: 'linux-amd64' },
    { runner: 'ubuntu-24.04-arm', target: 'linux/arm64', key: 'linux-arm64' },
  ];
  if (!publish || macConfigured) {
    // Apple's cross compiler avoids hosted macOS ARM64 capacity queues.
    for (const arch of ['amd64', 'arm64']) include.push({ runner: 'macos-15-intel', target: `darwin/${arch}`, key: `darwin-${arch}` });
  }
  return { include };
}

export async function releaseAssets(directory, version) {
  if (!/^\d+\.\d+\.\d+$/.test(version)) throw new Error('Expected a numeric release version.');
  const exists = async (name) => {
    try {
      const info = await stat(path.join(directory, name));
      return info.isFile() && info.size > 0;
    } catch (error) { if (error.code === 'ENOENT') return false; throw error; }
  };
  const assets = [];
  const omittedMacTargets = [];
  for (const arch of ['amd64', 'arm64']) {
    for (const suffix of [`windows-${arch}-setup.exe`, `linux-${arch}.deb`, `linux-${arch}.rpm`, `windows-${arch}.zip`, `linux-${arch}.zip`]) {
      const name = `Q-Studio-${version}-${suffix}`;
      if (!await exists(name)) throw Object.assign(new Error(`Required Windows/Linux asset missing: ${name}`), { dependencyFailure: true });
      assets.push(name);
    }
    const pair = [`Q-Studio-${version}-darwin-${arch}.dmg`, `Q-Studio-${version}-darwin-${arch}.zip`];
    const present = await Promise.all(pair.map(exists));
    if (present.every(Boolean)) assets.push(...pair);
    else omittedMacTargets.push(`darwin/${arch}`);
  }
  const lines = [];
  for (const name of assets) {
    const hash = createHash('sha256');
    for await (const chunk of createReadStream(path.join(directory, name))) hash.update(chunk);
    lines.push(`${hash.digest('hex')}  ${name}`);
  }
  const manifest = `Q-Studio-${version}-SHA256SUMS.txt`;
  await writeFile(path.join(directory, manifest), `${lines.join('\n')}\n`);
  return { assets: [...assets, manifest], omittedMacTargets };
}

export function checkExistingAssets(existing, planned) {
  const allowed = new Set(planned);
  if (existing.some((name) => !allowed.has(name))) {
    throw new Error('Existing release has assets absent from this build. Rebuild with complete artifacts before replacing its checksum manifest.');
  }
}

async function main(args, env = process.env) {
  if (args[0] === 'matrix' && args.length === 1) {
    const publish = env.PUBLISH === 'true';
    const configured = env.MACOS_SIGNING_CONFIGURED === 'true';
    await appendFile(env.GITHUB_OUTPUT, `matrix=${JSON.stringify(buildMatrix(publish, configured))}\n`);
    if (publish && !configured && env.GITHUB_STEP_SUMMARY) await appendFile(env.GITHUB_STEP_SUMMARY,
      'macOS packages skipped: Apple signing credentials are not configured. Windows/Linux remain required.\n');
  } else if (args[0] === 'assets' && args.length === 3) {
    const result = await releaseAssets(args[1], args[2]);
    await writeFile(path.join(args[1], 'assets.txt'), `${result.assets.join('\n')}\n`);
    await writeFile(path.join(args[1], 'release-notes.md'),
      'Windows and Linux packages are required. macOS packages are optional and are included only after successful Developer ID signing and Apple notarization. See the assets for available Mac architectures.\n');
    console.log(`Verified ${result.assets.length} release files; omitted macOS targets: ${result.omittedMacTargets.join(', ') || 'none'}.`);
  } else if (args[0] === 'check-existing' && args.length === 3) {
    const lines = async (file) => (await readFile(file, 'utf8')).split(/\r?\n/).filter(Boolean);
    checkExistingAssets(await lines(args[1]), await lines(args[2]));
  } else throw new Error('Usage: studio-release.mjs matrix | assets <directory> <version> | check-existing <existing-list> <planned-list>');
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main(process.argv.slice(2)).catch((error) => {
    if (error.dependencyFailure && process.env.GITHUB_ACTIONS === 'true') {
      console.error(`::error title=Q Studio release dependency::${error.message}`);
    } else console.error(error.message);
    process.exitCode = 1;
  });
}
