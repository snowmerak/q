export function requireWorkspace(value: string) {
  const root = value.trim();
  if (!root) throw new Error('Open a repository in Sessions or enter its path here.');
  return root;
}
