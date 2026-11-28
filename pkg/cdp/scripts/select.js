function(options) {
  const roots = [document], elements = [], seen = new Set();
  for (let i=0; i<roots.length; i++) {
    for (const element of roots[i].querySelectorAll('*')) {
      if (!seen.has(element)) { elements.push(element); seen.add(element); }
      if (element.shadowRoot) roots.push(element.shadowRoot);
    }
  }
  let matches;
  if (options.kind === 'css') {
    const selected = new Set();
    for (const root of roots) for (const element of root.querySelectorAll(options.selector)) selected.add(element);
    matches = [...selected];
  } else {
    const text = e => (e.innerText ?? e.textContent ?? '').replace(/\s+/gu,' ').trim();
    matches = elements.filter(e => !['SCRIPT','STYLE','HEAD'].includes(e.tagName) && text(e) === options.text);
    matches = matches.filter(e => !matches.some(child => child!==e && e.contains(child)));
  }
  return matches.length === 1 ? matches[0] : matches.length;
}
