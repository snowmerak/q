function(action, x, y) {
  const parent = e => e.assignedSlot || e.parentElement || e.getRootNode()?.host || null;
  let visible = this.isConnected, supported = true, closed = false;
  for (let e=this; e; e=parent(e)) {
    const style = getComputedStyle(e);
    const root=e.getRootNode();
    if(root!==document&&root.mode!=='open'){supported=false;closed=true;}
    if (style.display==='none' || ['hidden','collapse'].includes(style.visibility) || Number(style.opacity)===0 || e.inert) visible=false;
    if (style.transform!=='none') {
      const m=new DOMMatrixReadOnly(style.transform);
      if (!m.is2D || Math.abs(m.b)>1e-8 || Math.abs(m.c)>1e-8 || m.a<=0 || m.d<=0) supported=false;
    }
    if (style.rotate && style.rotate!=='none' && style.rotate!=='0deg') supported=false;
    if (style.scale && style.scale!=='none' && style.scale.split(/\s+/).some(v=>Number(v)<=0)) supported=false;
  }
  if (action==='scroll' && visible && supported) this.scrollIntoView({block:'center',inline:'center',behavior:'instant'});
  const r=this.getBoundingClientRect(), s=getComputedStyle(this);
  const sx=this.offsetWidth>0?r.width/this.offsetWidth:0, sy=this.offsetHeight>0?r.height/this.offsetHeight:0;
  const pl=parseFloat(s.paddingLeft)||0, pt=parseFloat(s.paddingTop)||0;
  const originX=r.left+(this.clientLeft+pl)*sx, originY=r.top+(this.clientTop+pt)*sy;
  const width=(this.clientWidth-pl-(parseFloat(s.paddingRight)||0))*sx;
  const height=(this.clientHeight-pt-(parseFloat(s.paddingBottom)||0))*sy;
  const px=originX+x*sx, py=originY+y*sy;
  let target=document.elementFromPoint(px,py);
  while(target?.shadowRoot) {const inner=target.shadowRoot.elementFromPoint(px,py);if(!inner||inner===target)break;target=inner;}
  return {OriginX:originX,OriginY:originY,ScaleX:sx,ScaleY:sy,Width:width,Height:height,X:px,Y:py,
    Visible:visible&&r.width>0&&r.height>0,Supported:supported&&sx>0&&sy>0,Closed:closed,
    Hit:target===this&&x>=0&&y>=0&&px<originX+width&&py<originY+height};
}
