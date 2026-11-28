function(action) {
  const element = this;
  const connected = element.nodeType === 1 && element.isConnected;
  const parent = e => e.assignedSlot || e.parentElement || e.getRootNode()?.host || null;
  let rootNode=element, supported=connected&&element.ownerDocument===document&&!['IFRAME','FRAME'].includes(element.tagName);
  while(supported&&rootNode.getRootNode()!==document){const root=rootNode.getRootNode();if(root.mode!=='open'){supported=false;break;}rootNode=root.host;}
  if (!supported) return {connected, Supported:false};
  function state() {
    const rects = [...element.getClientRects()].filter(r => r.width > 0 && r.height > 0);
    let visible = rects.length > 0;
    let enabled = !element.matches(':disabled');
    for (let e = element; e; e = parent(e)) {
      const style = getComputedStyle(e);
      if (style.display === 'none' || style.visibility === 'hidden' || style.visibility === 'collapse' || Number(style.opacity) === 0) visible = false;
      if (e.getAttribute('aria-disabled') === 'true' || e.inert) enabled = false;
    }
    const rect = rects.find(r => r.right > 0 && r.bottom > 0 && r.left < innerWidth && r.top < innerHeight) || rects[0];
    let x = 0, y = 0, hit = false;
    if (rect) {
      const left = Math.max(0, rect.left), right = Math.min(innerWidth, rect.right);
      const top = Math.max(0, rect.top), bottom = Math.min(innerHeight, rect.bottom);
      x = (left + right) / 2; y = (top + bottom) / 2;
      let target = right > left && bottom > top ? document.elementFromPoint(x, y) : null;
      while(target?.shadowRoot){const inner=target.shadowRoot.elementFromPoint(x,y);if(!inner||inner===target)break;target=inner;}
      for(let e=target;e;e=parent(e)){if(e===element){hit=true;break;}}
    }
    const tag = element.tagName.toLowerCase();
    const textInput = tag === 'textarea' || (tag === 'input' && ['text', 'search', 'email', 'tel', 'url', 'password'].includes(element.type));
    const editingHost = element.isContentEditable && !parent(element)?.isContentEditable;
    let active=document.activeElement;
    while(active?.shadowRoot?.activeElement) active=active.shadowRoot.activeElement;
    return {
      connected: element.isConnected, Supported: true, visible, enabled,
      editable: (textInput || editingHost) && !element.readOnly,
      focused: active === element, x, y, hit,
      width: rect ? Math.max(0, Math.min(innerWidth,rect.right)-Math.max(0,rect.left)) : 0,
      height: rect ? Math.max(0, Math.min(innerHeight,rect.bottom)-Math.max(0,rect.top)) : 0,
      value: textInput ? element.value : element.textContent
    };
  }
  if (action === 'inspect') return state();
  const before = state();
  if (!before.visible || !before.enabled || (action === 'fill' && !before.editable)) return before;
  if (action === 'scroll') element.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  if (action === 'focus' || action === 'fill') {
    element.focus({preventScroll: true});
    if (action === 'fill') {
      if (element.tagName === 'INPUT' || element.tagName === 'TEXTAREA') element.select();
      else {
        const range = document.createRange(); range.selectNodeContents(element);
        const selection = getSelection(); selection.removeAllRanges(); selection.addRange(range);
      }
    }
  }
  return state();
}
