import { highlightCode } from '../markdown';

export type InlineLine = { text: string; kind: 'context' | 'added' | 'removed'; oldLine: number | null; newLine: number | null; html?: string };

function sourceLines(content: string) {
  if (!content) return [];
  return content.replace(/\r\n/g, '\n').replace(/\n$/, '').split('\n');
}

// Highlight each complete version once, carrying multiline grammar spans across
// rows. Splitting balanced HTML retains syntax state for comments and strings.
function highlightedLines(content: string, language: string) {
  const html = highlightCode(content, language);
  const open: string[] = [];
  const lines: string[] = [];
  let row = '', offset = 0;
  for (const match of html.matchAll(/\n|<span\b[^>]*>|<\/span>/g)) {
    row += html.slice(offset, match.index);
    const token = match[0];
    if (token === '\n') { lines.push(row + '</span>'.repeat(open.length)); row = open.join(''); }
    else { row += token; if (token === '</span>') open.pop(); else open.push(token); }
    offset = match.index! + token.length;
  }
  lines.push(row + html.slice(offset));
  return lines;
}

export function fullFileDiff(content: string, patch: string, allAdded: boolean, partial: boolean, language: string): InlineLine[] {
  if (/^@@@/m.test(patch)) throw new Error('Unresolved merge entries use a combined diff. Select All changes to compare the working tree with HEAD.');
  const current = sourceLines(content);
  const rows: InlineLine[] = [];
  let nextNew = 1, nextOld = 1;
  const contextUntil = (end: number) => {
    while (nextNew < end && nextNew <= current.length) {
      rows.push({ text: current[nextNew - 1], kind: 'context', oldLine: nextOld++, newLine: nextNew++ });
    }
  };
  if (allAdded) {
    for (const [index, text] of current.entries()) rows.push({ text, kind: 'added', oldLine: null, newLine: index + 1 });
  } else {
    let remainingOld = 0, remainingNew = 0, inHunk = false;
    for (const raw of patch.replace(/\r\n/g, '\n').split('\n')) {
      const header = raw.match(/^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/);
      if (header) {
        if (inHunk && (remainingOld || remainingNew) && !partial) throw new Error('Incomplete Git comparison. Refresh the file.');
        const oldStart = Number(header[1]), newStart = Number(header[3]);
        remainingOld = header[2] === undefined ? 1 : Number(header[2]);
        remainingNew = header[4] === undefined ? 1 : Number(header[4]);
        const newPosition = remainingNew ? newStart : newStart + 1;
        if (newPosition > current.length + 1) break;
        contextUntil(newPosition);
        nextOld = remainingOld ? oldStart : oldStart + 1;
        nextNew = newPosition;
        inHunk = true;
        continue;
      }
      if (!inHunk || (!remainingOld && !remainingNew) || raw.startsWith('\\')) continue;
      const prefix = raw[0], text = raw.slice(1);
      if (prefix !== ' ' && prefix !== '+' && prefix !== '-') continue;
      if (prefix !== '-') {
        if (nextNew > current.length) { if (partial) break; throw new Error('File changed while loading. Refresh the file.'); }
        if (current[nextNew - 1] !== text) {
          if (partial) break;
          throw new Error('File changed while loading. Refresh the file.');
        }
      }
      if (prefix === '-') { rows.push({ text, kind: 'removed', oldLine: nextOld++, newLine: null }); remainingOld--; }
      else if (prefix === '+') { rows.push({ text, kind: 'added', oldLine: null, newLine: nextNew++ }); remainingNew--; }
      else { rows.push({ text, kind: 'context', oldLine: nextOld++, newLine: nextNew++ }); remainingOld--; remainingNew--; }
    }
    if (inHunk && (remainingOld || remainingNew) && !partial) throw new Error('Incomplete Git comparison. Refresh the file.');
    contextUntil(current.length + 1);
  }
  const before = highlightedLines(rows.filter((row) => row.kind !== 'added').map((row) => row.text).join('\n'), language);
  const after = highlightedLines(current.join('\n'), language);
  let oldIndex = 0;
  for (const row of rows) {
    row.html = row.kind === 'removed' ? before[oldIndex] : after[row.newLine! - 1];
    if (row.kind !== 'added') oldIndex++;
  }
  return rows;
}
