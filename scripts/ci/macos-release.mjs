import { execFile } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import * as fs from 'node:fs/promises';
import path from 'node:path';
import { promisify } from 'node:util';
import { pathToFileURL } from 'node:url';

const exec = promisify(execFile);
const nativeTools = {
  security: '/usr/bin/security', codesign: '/usr/bin/codesign', ditto: '/usr/bin/ditto',
  hdiutil: '/usr/bin/hdiutil', xcrun: '/usr/bin/xcrun', spctl: '/usr/sbin/spctl', ln: '/bin/ln',
};
const secretNames = [
  'APPLE_DEVELOPER_ID_CERTIFICATE_BASE64', 'APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD',
  'APPLE_DEVELOPER_TEAM_ID', 'APPLE_NOTARY_KEY_BASE64', 'APPLE_NOTARY_KEY_ID', 'APPLE_NOTARY_ISSUER_ID',
];

export function configuration(env) {
  const missing = secretNames.filter((name) => !env[name]);
  if (missing.length) throw new Error(`Missing macOS release secrets: ${missing.join(', ')}`);
  if (!/^[A-Z0-9]{10}$/.test(env.APPLE_DEVELOPER_TEAM_ID) || !/^[A-Z0-9]{10}$/.test(env.APPLE_NOTARY_KEY_ID) ||
      !/^[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}$/i.test(env.APPLE_NOTARY_ISSUER_ID)) {
    throw new Error('Invalid Apple team ID, notary key ID or issuer ID.');
  }
  if (!/^\d+\.\d+\.\d+$/.test(env.Q_STUDIO_VERSION ?? '') || !/^darwin\/(amd64|arm64)$/.test(env.Q_STUDIO_TARGET ?? '')) {
    throw new Error('Expected a numeric Q_STUDIO_VERSION and darwin Q_STUDIO_TARGET.');
  }
  const decode = (name) => {
    const encoded = env[name].replace(/\s/g, '');
    const decoded = Buffer.from(encoded, 'base64');
    if (!decoded.length || decoded.length > 64 * 1024 || decoded.toString('base64') !== encoded) {
      throw new Error(`Invalid Base64 data in ${name}.`);
    }
    return decoded;
  };
  const certificate = decode('APPLE_DEVELOPER_ID_CERTIFICATE_BASE64');
  const key = decode('APPLE_NOTARY_KEY_BASE64');
  if (!key.toString('utf8').includes('-----BEGIN PRIVATE KEY-----')) throw new Error('Expected a PEM notary private key.');
  return { certificate, key, arch: env.Q_STUDIO_TARGET.split('/')[1] };
}

export function developerIdentity(output, team) {
  const matches = [...output.matchAll(/\b([A-Fa-f0-9]{40})\s+"Developer ID Application: [^"\n]+ \(([A-Z0-9]{10})\)"/g)]
    .filter((match) => match[2] === team);
  if (matches.length !== 1) throw new Error('Expected exactly one valid Developer ID Application identity for the configured team.');
  return matches[0][1];
}

export function redact(message, values) {
  for (const value of values.filter(Boolean).sort((a, b) => b.length - a.length)) message = message.replaceAll(value, '[redacted]');
  return message;
}

// Native Apple tools remain responsible for signature verification and policy.
// run is injectable so failure/cleanup paths can be exercised without a key or Mac.
export async function signRelease({ env = process.env, root = process.cwd(), run, platform = process.platform } = {}) {
  const config = configuration(env);
  if (platform !== 'darwin') throw new Error('macOS distribution signing requires a macOS host.');
  if (!env.RUNNER_TEMP) throw new Error('RUNNER_TEMP is required for isolated signing credentials.');
  const prefix = path.join(root, 'bin', `Q-Studio-${env.Q_STUDIO_VERSION}-darwin-${config.arch}`);
  const image = `${prefix}.dmg`;
  const archive = `${prefix}.zip`;
  await fs.access(image);
  await fs.access(archive);
  const tempRoot = path.resolve(env.RUNNER_TEMP);
  const workspace = await fs.mkdtemp(path.join(tempRoot, 'q-studio-macos-'));
  await fs.chmod(workspace, 0o700);
  const keychain = path.join(workspace, 'signing.keychain-db');
  const certificate = path.join(workspace, 'developer-id.p12');
  const key = path.join(workspace, 'notary.p8');
  const mount = path.join(workspace, 'mount');
  const payload = path.join(workspace, 'payload');
  const app = path.join(payload, 'Q Studio.app');
  const password = randomBytes(32).toString('hex');
  const sensitive = [...secretNames.map((name) => env[name]), password, config.key.toString('utf8')];
  const toolEnvironment = { ...env };
  for (const name of [...secretNames, 'GH_TOKEN', 'GITHUB_TOKEN']) delete toolEnvironment[name];
  run ??= async (label, command, args, timeout = 120_000) => {
    try {
      return await exec(nativeTools[command], args, { timeout, maxBuffer: 4 * 1024 * 1024, env: toolEnvironment });
    } catch (error) {
      // execFile's message includes its argument list (including security -P).
      // Report only a stage name, status and redacted tool diagnostics.
      throw new Error(`${label} failed (${error.code ?? 'timeout'}): ${redact(String(error.stderr ?? '').slice(-4000), sensitive)}`);
    }
  };
  let mounted = false;
  let keychainCreated = false;
  let searchList;
  let failure;
  const sign = async (file, identity, runtime) => {
    const args = ['--force', '--sign', identity, '--keychain', keychain, '--timestamp'];
    if (runtime) args.push('--options', 'runtime');
    await run('Sign artifact', 'codesign', [...args, file]);
  };
  const verifyApp = async (bundle) => {
    await run('Verify app signature', 'codesign', ['--verify', '--deep', '--strict', '--verbose=2', bundle]);
    await run('Validate app ticket', 'xcrun', ['stapler', 'validate', bundle]);
    await run('Assess app with Gatekeeper', 'spctl', ['--assess', '--type', 'execute', '--verbose=4', bundle]);
  };
  const attach = async (file) => {
    // A failed/timed-out attach can have mounted the volume already. Retain
    // ownership until detach succeeds, even when attach's outcome is unknown.
    mounted = true;
    await run('Mount disk image', 'hdiutil', ['attach', '-readonly', '-nobrowse', '-mountpoint', mount, file]);
  };
  const detach = async () => {
    await run('Detach disk image', 'hdiutil', ['detach', mount]);
    mounted = false;
  };
  const notarize = async (file) => {
    const result = await run('Submit to Apple notarization', 'xcrun', ['notarytool', 'submit', file,
      '--key', key, '--key-id', env.APPLE_NOTARY_KEY_ID, '--issuer', env.APPLE_NOTARY_ISSUER_ID,
      '--wait', '--timeout', '15m', '--output-format', 'json'], 17 * 60_000);
    let status;
    try { status = JSON.parse(result.stdout).status; } catch { throw new Error('Invalid notarization response.'); }
    if (status !== 'Accepted') throw new Error('Apple notarization did not accept the artifact; no distribution files were replaced.');
  };
  try {
    await fs.writeFile(certificate, config.certificate, { mode: 0o600 });
    await fs.writeFile(key, config.key, { mode: 0o600 });
    const existing = await run('Read keychain search list', 'security', ['list-keychains', '-d', 'user']);
    searchList = [...existing.stdout.matchAll(/^\s*"([^"\n]+)"\s*$/gm)].map((match) => match[1]);
    if (!searchList.length) throw new Error('Could not read the existing keychain search list.');
    await run('Create temporary keychain', 'security', ['create-keychain', '-p', password, keychain]);
    keychainCreated = true;
    await run('Configure temporary keychain', 'security', ['set-keychain-settings', '-lut', '21600', keychain]);
    await run('Unlock temporary keychain', 'security', ['unlock-keychain', '-p', password, keychain]);
    await run('Import Developer ID', 'security', ['import', certificate, '-P', env.APPLE_DEVELOPER_ID_CERTIFICATE_PASSWORD,
      '-t', 'cert', '-f', 'pkcs12', '-k', keychain, '-T', '/usr/bin/codesign', '-T', '/usr/bin/security']);
    await run('Authorize signing tools', 'security', ['set-key-partition-list', '-S', 'apple-tool:,apple:,codesign:', '-s', '-k', password, keychain]);
    await run('Select temporary keychain', 'security', ['list-keychains', '-d', 'user', '-s', keychain, ...searchList]);
    const identities = await run('Find Developer ID', 'security', ['find-identity', '-v', '-p', 'codesigning', keychain]);
    const identity = developerIdentity(identities.stdout, env.APPLE_DEVELOPER_TEAM_ID);
    await fs.mkdir(mount);
    await fs.mkdir(payload);
    await attach(image);
    await run('Copy app bundle', 'ditto', [path.join(mount, 'Q Studio.app'), app]);
    await detach();
    const engine = path.join(app, 'Contents', 'MacOS', 'q-engine');
    await fs.access(engine);
    await sign(engine, identity, true);
    await sign(app, identity, true);
    await run('Verify engine signature', 'codesign', ['--verify', '--strict', '--verbose=2', engine]);
    await run('Verify sealed app signature', 'codesign', ['--verify', '--deep', '--strict', '--verbose=2', app]);
    for (const executable of [engine, app]) {
      const details = await run('Inspect signing identity', 'codesign', ['--display', '--verbose=4', executable]);
      const description = details.stdout + details.stderr;
      if (!description.includes(`TeamIdentifier=${env.APPLE_DEVELOPER_TEAM_ID}\n`) || !/flags=.*\(runtime\)/.test(description)) {
        throw new Error('Signed code lacks the expected team identity or hardened runtime.');
      }
    }
    const submission = path.join(workspace, 'submission.zip');
    await run('Archive app for notarization', 'ditto', ['-c', '-k', '--keepParent', app, submission]);
    await notarize(submission);
    await run('Staple app ticket', 'xcrun', ['stapler', 'staple', app]);
    await verifyApp(app);
    const signedZIP = path.join(workspace, 'distribution.zip');
    await run('Archive notarized app', 'ditto', ['-c', '-k', '--keepParent', app, signedZIP]);
    await run('Add Applications shortcut', 'ln', ['-s', '/Applications', path.join(payload, 'Applications')]);
    const signedDMG = path.join(workspace, 'distribution.dmg');
    await run('Create distribution DMG', 'hdiutil', ['create', '-volname', 'Q Studio', '-fs', 'HFS+', '-srcfolder', payload, '-format', 'UDZO', signedDMG]);
    await sign(signedDMG, identity, false);
    await notarize(signedDMG);
    await run('Staple DMG ticket', 'xcrun', ['stapler', 'staple', signedDMG]);
    await run('Validate DMG ticket', 'xcrun', ['stapler', 'validate', signedDMG]);
    await run('Verify disk image', 'hdiutil', ['verify', signedDMG]);
    await run('Verify DMG signature', 'codesign', ['--verify', '--strict', '--verbose=2', signedDMG]);
    await run('Assess DMG with Gatekeeper', 'spctl', ['--assess', '--type', 'open', '--context', 'context:primary-signature', '--verbose=4', signedDMG]);
    await attach(signedDMG);
    await verifyApp(path.join(mount, 'Q Studio.app'));
    await detach();
    const unpacked = path.join(workspace, 'unpacked');
    await fs.mkdir(unpacked);
    await run('Extract distribution ZIP', 'ditto', ['-x', '-k', signedZIP, unpacked]);
    await verifyApp(path.join(unpacked, 'Q Studio.app'));
    await fs.copyFile(signedDMG, image);
    await fs.copyFile(signedZIP, archive);
  } catch (error) {
    failure = error;
  } finally {
    const cleanupErrors = [];
    const cleanup = async (operation) => { try { await operation(); } catch (error) { cleanupErrors.push(error.message); } };
    if (mounted) await cleanup(async () => {
      await run('Detach signing mount during cleanup', 'hdiutil', ['detach', '-force', mount], 30_000);
      mounted = false;
    });
    if (searchList?.length) await cleanup(() => run('Restore keychain search list', 'security', ['list-keychains', '-d', 'user', '-s', ...searchList], 30_000));
    if (keychainCreated) await cleanup(() => run('Delete temporary keychain', 'security', ['delete-keychain', keychain], 30_000));
    for (const file of [certificate, key]) await cleanup(() => fs.rm(file, { force: true }));
    // Never traverse an attached volume during recursive cleanup. Restrict
    // deletion to the exact private directory returned by mkdtemp.
    if (!mounted && path.dirname(workspace) === tempRoot && path.basename(workspace).startsWith('q-studio-macos-')) {
      await cleanup(() => fs.rm(workspace, { recursive: true, force: true }));
    }
    config.certificate.fill(0);
    config.key.fill(0);
    if (failure || cleanupErrors.length) throw new Error(redact([failure?.message, ...cleanupErrors].filter(Boolean).join('\n'), sensitive));
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  const action = process.argv.slice(2);
  Promise.resolve().then(async () => {
    if (action.length === 1 && action[0] === '--check') {
      const config = configuration(process.env);
      config.certificate.fill(0);
      config.key.fill(0);
      console.log('macOS release credentials are configured.');
    } else if (!action.length) {
      await signRelease();
      console.log('macOS DMG and app ZIP signed, notarized, stapled and verified.');
    } else throw new Error('Usage: node scripts/ci/macos-release.mjs [--check]');
  }).catch((error) => { console.error(error.message); process.exitCode = 1; });
}
