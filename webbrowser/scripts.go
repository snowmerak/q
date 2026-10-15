package webbrowser

// These scripts read the rendered main document, not raw HTML. Limits are
// applied in Chrome so a large page cannot flood the CDP response or Loom.
const readScript = `(limit = 24000) => {
 const body = document.querySelector('main, article, [role="main"]') || document.body;
 const rendered = body ? body.innerText : '';
 const alternatives = body ? Array.from(body.querySelectorAll('img[alt]'))
   .map(img => (img.getAttribute('alt') || '').trim())
   .filter(alt => alt && !rendered.includes(alt)) : [];
 const text = Array.from([rendered, ...alternatives].join('\n'));
 const anchors = Array.from(document.querySelectorAll('a[href]'));
 const links = anchors.filter(a => {
   try { return /^https?:$/.test(new URL(a.href).protocol); }
   catch { return false; }
 })
   .slice(0, 100).map(a => ({text: (a.innerText || a.getAttribute('aria-label') || '').trim().slice(0, 300), url: a.href}));
 return {url: location.href, title: document.title, text: text.slice(0, limit).join(''), links,
   status_code: performance.getEntriesByType('navigation')[0]?.responseStatus || 0,
   truncated: text.length > limit || anchors.length > 100};
}`

const documentScript = `() => JSON.stringify([location.href, performance.timeOrigin])`

const elementsScript = `(limit) => Array.from(document.querySelectorAll(
 'a[href], button, input:not([type="hidden"]), textarea, select, summary, [role="button"], [role="link"], [role="textbox"], [contenteditable="true"]'
)).filter(el => {
 const style = getComputedStyle(el);
 return el.getClientRects().length > 0 && style.visibility !== 'hidden' && style.display !== 'none' && el.getAttribute('aria-hidden') !== 'true';
}).slice(0, limit)`

const elementScript = `() => {
 const el = this;
 const tag = el.tagName.toLowerCase();
 const labelled = (el.getAttribute('aria-labelledby') || '').split(/\s+/)
   .map(id => document.getElementById(id)?.innerText || '').join(' ').trim();
 const label = el.getAttribute('aria-label') || labelled ||
   Array.from(el.labels || []).map(l => l.innerText).join(' ') || el.innerText ||
   el.getAttribute('placeholder') || el.getAttribute('title') || el.getAttribute('name') || '';
 const roles = {a: 'link', button: 'button', textarea: 'textbox', select: 'combobox', summary: 'button'};
 const inputRole = ['button', 'submit', 'reset'].includes(el.type) ? 'button' :
   el.type === 'checkbox' ? 'checkbox' : el.type === 'radio' ? 'radio' : 'textbox';
 const form = el.form;
 const getForm = !form || ((form.method || 'get').toLowerCase() === 'get' &&
   Array.from(form.elements).filter(e => e.type === 'submit').every(e =>
     !e.hasAttribute('formmethod') || e.formMethod.toLowerCase() === 'get'));
 const searchField = e => e && e.tagName === 'INPUT' &&
   ['text', 'search'].includes(e.type) && (e.type === 'search' || /^(q|query|search|search_query|term|keyword|keywords)$/i.test(e.name) ||
   e.getAttribute('role') === 'searchbox' || e.form?.getAttribute('role') === 'search');
 const search = getForm && (searchField(el) ||
   (form && (tag === 'button' && el.type === 'submit' || tag === 'input' && el.type === 'submit') &&
    Array.from(form.elements).some(searchField)));
 return {role: el.getAttribute('role') || roles[tag] || (tag === 'input' ? inputRole : 'textbox'),
   name: label.trim().slice(0, 300), url: tag === 'a' ? el.href : undefined,
   disabled: !!el.disabled || el.getAttribute('aria-disabled') === 'true',
   input_type: tag === 'input' ? el.type : undefined,
   value: search && tag === 'input' ? (el.value || '').slice(0, 1000) : undefined, search: !!search};
}`
