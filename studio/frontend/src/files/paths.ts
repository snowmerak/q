export function fileLanguage(path: string) {
  const name = path.split(/[\\/]/).pop()?.toLowerCase() || '';
  if (name === 'dockerfile') return 'bash';
  const extension = name.split('.').pop() || '';
  return ({ go: 'go', js: 'javascript', mjs: 'javascript', cjs: 'javascript', jsx: 'javascript', ts: 'typescript', tsx: 'typescript', py: 'python', rs: 'rust', c: 'c', h: 'c', cc: 'cpp', cpp: 'cpp', hpp: 'cpp', cs: 'csharp', java: 'java', kt: 'kotlin', sql: 'sql', json: 'json', yaml: 'yaml', yml: 'yaml', sh: 'bash', bash: 'bash', css: 'css', html: 'xml', xml: 'xml', svg: 'xml', svelte: 'xml', vue: 'xml', md: 'markdown', diff: 'diff', patch: 'diff' } as Record<string, string>)[extension] || 'plaintext';
}

export function resolveFileLink(value: string, roots: string[]) {
  let path = value;
  try { path = decodeURIComponent(path); } catch { /* Keep a literal filename containing %. */ }
  const match = path.match(/(?:#L|:)(\d+)(?::\d+)?$/);
  const line = match ? Number(match[1]) : 1;
  if (match) path = path.slice(0, match.index);
  path = path.replace(/\\/g, '/');
  if (path.startsWith('/') || /^[a-z]:\//i.test(path)) {
    const candidates = [...roots].sort((a, b) => b.length - a.length);
    for (const root of candidates) {
      const normalized = root.replace(/\\/g, '/').replace(/\/$/, '');
      const windows = /^[a-z]:\//i.test(normalized) || normalized.startsWith('//');
      const compare = windows ? path.toLowerCase() : path;
      const prefix = windows ? normalized.toLowerCase() : normalized;
      if (compare.startsWith(prefix + '/')) return { root, path: path.slice(normalized.length + 1), line };
    }
    return null;
  }
  if (!roots[0] || /(^|\/)\.\.(\/|$)/.test(path) || /^[a-z][a-z\d+.-]*:/i.test(path)) return null;
  return { root: roots[0], path: path.replace(/^\.\//, ''), line };
}
