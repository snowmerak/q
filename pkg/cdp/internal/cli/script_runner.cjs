// Embedded Node host. Browser operations stay in the existing Go RPC service.
'use strict';
const readline = require('node:readline');
const { Console } = require('node:console');
const { createRequire } = require('node:module');
const input = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
const log = new Console({ stdout: process.stderr, stderr: process.stderr });
globalThis.console = log; // Imported modules should not corrupt the bridge either.
const pending = new Map();
let sequence = 0;
let booted = false;

class DriverError extends Error {
  constructor(detail, result) {
    super(detail.message);
    this.name = 'DriverError';
    Object.assign(this, detail);
    this.result = result;
  }
}

function command(...args) {
  if (!args.every(value => typeof value === 'string')) {
    return Promise.reject(new TypeError('session.command arguments must be strings'));
  }
  const id = ++sequence;
  let settle;
  const promise = new Promise((resolve, reject) => { settle = { resolve, reject }; });
  pending.set(id, { ...settle, promise });
  // Keep unawaited calls from becoming process-level unhandled rejections.
  promise.catch(() => {});
  process.stdout.write(JSON.stringify({ type: 'call', id, args }) + '\n');
  return promise;
}

function flags(options, allowed) {
  const args = [];
  for (const [key, value] of Object.entries(options)) {
    if (!Object.hasOwn(allowed, key)) throw new TypeError(`unknown option: ${key}`);
    if (value !== undefined) args.push('--' + allowed[key], String(value));
  }
  return args;
}

function selector(value) {
  const options = typeof value === 'string' ? { css: value } : value;
  if (!options || typeof options !== 'object' || Array.isArray(options)) {
    throw new TypeError('locator must be a CSS string or {css|ref|text|role, name?, frame?}');
  }
  return flags(options, { css: 'css', ref: 'ref', text: 'text', role: 'role', name: 'name', frame: 'frame' });
}

function expression(value, args) {
  if (typeof value === 'function') return `(${value.toString()})(...${JSON.stringify(args)})`;
  if (typeof value !== 'string' || args.length) throw new TypeError('use an expression string or a function with JSON arguments');
  return value;
}

class Page {
  constructor(name) {
    if (typeof name !== 'string' || !name) throw new TypeError('page name is required');
    this.name = name;
  }
  goto(url) { return command('page', 'goto', this.name, url); }
  title() { return command('page', 'title', this.name); }
  url() { return command('page', 'url', this.name); }
  frames() { return command('page', 'frames', this.name); }
  activate() { return command('page', 'activate', this.name); }
  close() { return command('page', 'close', this.name); }
  release() { return command('page', 'release', this.name); }
  snapshot(options = {}) {
    return command('page', 'snapshot', ...flags(options, { scope: 'scope', root: 'root', maxNodes: 'max-nodes', maxBytes: 'max-bytes' }), this.name);
  }
  click(locator) { return command('page', 'click', ...selector(locator), this.name); }
  fill(locator, text) { return command('page', 'fill', ...selector(locator), this.name, text); }
  press(locator, key) { return command('page', 'press', ...selector(locator), this.name, key); }
  upload(locator, paths) { return command('page', 'upload', ...selector(locator), this.name, ...paths); }
  evaluate(value, ...args) { return command('page', 'eval', this.name, expression(value, args)); }
  waitForFunction(value, ...args) { return command('page', 'wait-function', this.name, expression(value, args)); }
  waitForSelector(locator) { return command('page', 'wait-selector', ...selector(locator), this.name); }
  waitForIdle(idle = '500ms') { return command('page', 'wait-idle', '--idle', idle, this.name); }
  screenshot(path) { return command('page', 'screenshot', this.name, path); }
}

async function execute(config) {
  const session = Object.freeze({
    name: config.session,
    args: Object.freeze(config.args || []),
    page: name => new Page(name),
    async newPage(name, url = '', options = {}) {
      const result = await command('page', 'new', '--name', name, ...flags(options, { task: 'task' }), url);
      return new Page(result.name);
    },
    pages: () => command('page', 'list'),
    status: () => command('status'),
    command,
  });
  const AsyncFunction = Object.getPrototypeOf(async function () {}).constructor;
  const fn = new AsyncFunction('session', 'console', 'require', config.source);
  try {
    const result = await fn(session, log, createRequire(config.filename));
    // Drain calls already submitted if the script forgot to await them. Never
    // let a pending mutation continue merely because the final value is ready.
    await Promise.all([...pending.values()].map(entry => entry.promise));
    return result === undefined ? null : result;
  } catch (error) {
    await Promise.allSettled([...pending.values()].map(entry => entry.promise));
    throw error;
  }
}

function finish(message) {
  let line;
  try { line = JSON.stringify(message); }
  catch (error) { line = JSON.stringify({ type: 'result', error: { code: 'script_error', message: error.message, mayHaveEffects: false } }); }
  process.stdout.write(line + '\n', () => process.exit(0));
}

input.on('line', line => {
  let message;
  try { message = JSON.parse(line); }
  catch { process.exit(1); return; }
  if (!booted) {
    booted = true;
    execute(message).then(
      result => finish({ type: 'result', result }),
      error => finish({ type: 'result', error: {
        code: error instanceof DriverError ? error.code : 'script_error',
        message: String(error?.message ?? error),
        mayHaveEffects: !!error?.mayHaveEffects,
        operation: error?.operation,
        pageId: error?.pageId,
      }, result: error?.result }),
    );
    return;
  }
  const entry = pending.get(message.id);
  if (!entry) { process.exit(1); return; }
  pending.delete(message.id);
  if (message.response.ok) entry.resolve(message.response.result ?? null);
  else entry.reject(new DriverError(message.response.error, message.response.result));
});
