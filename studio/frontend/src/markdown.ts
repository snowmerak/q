import MarkdownIt from 'markdown-it';
import hljs from 'highlight.js/lib/core';
import bash from 'highlight.js/lib/languages/bash';
import c from 'highlight.js/lib/languages/c';
import cpp from 'highlight.js/lib/languages/cpp';
import csharp from 'highlight.js/lib/languages/csharp';
import css from 'highlight.js/lib/languages/css';
import diff from 'highlight.js/lib/languages/diff';
import go from 'highlight.js/lib/languages/go';
import java from 'highlight.js/lib/languages/java';
import javascript from 'highlight.js/lib/languages/javascript';
import json from 'highlight.js/lib/languages/json';
import kotlin from 'highlight.js/lib/languages/kotlin';
import markdownLanguage from 'highlight.js/lib/languages/markdown';
import plaintext from 'highlight.js/lib/languages/plaintext';
import python from 'highlight.js/lib/languages/python';
import rust from 'highlight.js/lib/languages/rust';
import sql from 'highlight.js/lib/languages/sql';
import typescript from 'highlight.js/lib/languages/typescript';
import xml from 'highlight.js/lib/languages/xml';
import yaml from 'highlight.js/lib/languages/yaml';

for (const [name, language] of [
  ['bash', bash], ['c', c], ['cpp', cpp], ['csharp', csharp], ['css', css], ['diff', diff],
  ['go', go], ['java', java], ['javascript', javascript], ['json', json], ['kotlin', kotlin],
  ['markdown', markdownLanguage], ['plaintext', plaintext], ['python', python], ['rust', rust],
  ['sql', sql], ['typescript', typescript], ['xml', xml], ['yaml', yaml]
] as const) {
  hljs.registerLanguage(name, language);
}

const languageAliases: Record<string, string> = {
  cxx: 'cpp', cs: 'csharp', golang: 'go', html: 'xml', js: 'javascript', jsx: 'javascript',
  md: 'markdown', py: 'python', rs: 'rust', sh: 'bash', shell: 'bash', text: 'plaintext',
  ts: 'typescript', tsx: 'typescript', yml: 'yaml'
};

function normalizedLanguage(value: string) {
  const name = value.trim().toLowerCase().split(/\s+/)[0] || '';
  return languageAliases[name] || name;
}

const markdown = new MarkdownIt({
  breaks: true,
  html: false,
  linkify: true,
  typographer: false,
  highlight(source, language) {
    const normalized = normalizedLanguage(language);
    if (!normalized || !hljs.getLanguage(normalized)) return '';
    const highlighted = hljs.highlight(source, { language: normalized, ignoreIllegals: true }).value;
    return `<pre data-language="${normalized}"><code class="hljs language-${normalized}">${highlighted}</code></pre>`;
  }
});

markdown.renderer.rules.link_open = (tokens, index, options, _environment, renderer) => {
  tokens[index].attrSet('target', '_blank');
  tokens[index].attrSet('rel', 'noreferrer noopener');
  return renderer.renderToken(tokens, index, options);
};

export function renderMarkdown(content: string) {
  return markdown.render(content || '');
}
