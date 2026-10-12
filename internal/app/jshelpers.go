package app

// helperJS runs before every document's own scripts in every tab it is added
// to (chrome, internal pages and web pages). Everything privileged is gated on
// location.protocol === 'sweef:'; the helpers for input are harmless to expose.
const helperJS = `(function(G){
  if (G.__sweefH) return; G.__sweefH = 1;
  var D = G.document;
  function closest(el, sel){ try { return el && el.closest ? el.closest(sel) : null; } catch(e){ return null; } }
  function focusable(el){ return closest(el, 'input,textarea,select,button,a[href],[tabindex],[contenteditable=""],[contenteditable="true"]'); }
  function isEditable(el){
    if(!el||el.nodeType!==1) return false;
    var n=el.localName;
    if(n==='textarea') return true;
    if(n==='input'){ var t=(el.getAttribute('type')||'text').toLowerCase(); return /^(text|search|url|tel|email|password|number|)$/.test(t); }
    return !!el.isContentEditable;
  }
  function mods(m){ m=m|0; return {shiftKey:!!(m&1),ctrlKey:!!(m&2),altKey:!!(m&4),metaKey:!!(m&8)}; }
  // pointer: kind = move|down|up|click|context ; m = shift1|ctrl2|alt4|meta8
  G.__sweefPtr = function(kind, x, y, b, m){
    var init = mods(m); init.button = b|0;
    var el = D.elementFromPoint(x, y);
    if (kind === 'move') { G.__swMouse('mousemove', x, y, init); return null; }
    if (kind === 'down') {
      G.__swMouse('mousedown', x, y, init);
      if ((b|0) === 0) {
        var f = focusable(el);
        if (f) { try { f.focus(); } catch(e){} }
        else { var ae = D.activeElement; if (ae && ae !== D.body && ae.blur) { try { ae.blur(); } catch(e){} } }
      }
      return null;
    }
    if (kind === 'up') { G.__swMouse('mouseup', x, y, init); return null; }
    if (kind === 'click') {
      var a = closest(el, 'a[href]');
      if (a && !/^\s*javascript:/i.test(a.getAttribute('href')||'')) {
        var bg = init.ctrlKey || init.metaKey || (b|0) === 1;
        if (bg || a.target === '_blank') return {open: a.href, bg: bg};
      }
      if ((b|0) === 1) return null;
      G.__swMouse('click', x, y, init);
      return null;
    }
    if (kind === 'context') {
      var ok = G.__swMouse('contextmenu', x, y, init);
      var l = closest(el, 'a[href]'), im = closest(el, 'img');
      return {prevented: ok === false, link: l ? l.href : '', image: im ? (im.currentSrc || im.src || '') : '',
              selection: G.__sweefSel(), editable: isEditable(el) || isEditable(D.activeElement)};
    }
    return null;
  };
  G.__sweefCursor = function(x, y){
    var el = D.elementFromPoint(x, y);
    for (var e = el; e && e.nodeType === 1; e = e.parentElement) {
      var c = '';
      try { c = (e.style && e.style.cursor) || ''; } catch(x){}
      if (!c) { try { c = G.getComputedStyle(e).cursor || ''; } catch(x){} }
      if (c && c !== 'auto' && c !== 'default') return c;
      if (c === 'default') return 'default';
      var n = e.localName;
      if (n === 'a' && e.hasAttribute('href')) return 'pointer';
      if (n === 'button' || n === 'select' || n === 'summary' || n === 'label' && e.hasAttribute('for') || e.getAttribute('role') === 'button' || e.hasAttribute('onclick')) return 'pointer';
      if (n === 'input') { var t=(e.getAttribute('type')||'text').toLowerCase(); return /^(button|submit|reset|checkbox|radio|image|file)$/.test(t) ? 'pointer' : 'text'; }
      if (n === 'textarea' || e.isContentEditable) return 'text';
    }
    return 'default';
  };
  G.__sweefSel = function(){
    var ae = D.activeElement;
    if (ae && (ae.localName === 'input' || ae.localName === 'textarea')) {
      try { var s = ae.selectionStart, e = ae.selectionEnd; if (s > e) { var t = s; s = e; e = t; } return (ae.value||'').slice(s, e); } catch(x){}
    }
    try { return String(G.getSelection ? G.getSelection() : ''); } catch(x){ return ''; }
  };
  function setVal(el, v, caret){
    el.value = v;
    try { el.setSelectionRange(caret, caret); } catch(x){}
    try { el.dispatchEvent(new G.Event('input', {bubbles:true})); } catch(x){}
  }
  G.__sweefInsert = function(text){
    var el = D.activeElement;
    if (!el || !isEditable(el)) return false;
    if (el.localName === 'input' || el.localName === 'textarea') {
      var v = el.value || '', s = el.selectionStart, e = el.selectionEnd;
      if (s == null) { s = e = v.length; }
      if (s > e) { var t = s; s = e; e = t; }
      if (el.localName === 'input') text = String(text).replace(/[\r\n]+/g, ' ');
      setVal(el, v.slice(0, s) + text + v.slice(e), s + String(text).length);
    } else { el.textContent = (el.textContent||'') + text; }
    return true;
  };
  // edit: selectall | cut | delete
  G.__sweefEdit = function(op){
    var el = D.activeElement;
    if (op === 'selectall') {
      if (el && (el.localName === 'input' || el.localName === 'textarea')) { try { el.setSelectionRange(0, (el.value||'').length); } catch(x){} return true; }
      try { var r = D.createRange(); r.selectNodeContents(D.body); var s = G.getSelection(); s.removeAllRanges(); s.addRange(r); } catch(x){}
      return true;
    }
    if (op === 'cut' || op === 'delete') {
      if (el && isEditable(el) && (el.localName === 'input' || el.localName === 'textarea')) {
        var v = el.value||'', s = el.selectionStart, e = el.selectionEnd;
        if (s > e) { var t = s; s = e; e = t; }
        if (s === e) return false;
        setVal(el, v.slice(0, s) + v.slice(e), s);
        return true;
      }
    }
    return false;
  };
  G.__sweefScrollTo = function(y){
    try {
      G.__sweefY = y|0;
      Object.defineProperty(G, 'scrollY', {get:function(){return G.__sweefY;}, configurable:true});
      Object.defineProperty(G, 'pageYOffset', {get:function(){return G.__sweefY;}, configurable:true});
      D.dispatchEvent(new G.Event('scroll'));
      G.dispatchEvent(new G.Event('scroll'));
    } catch(x){}
  };
  G.__sweefDocH = function(){
    var de = D.documentElement, b = D.body;
    return Math.max(de ? de.scrollHeight|0 : 0, b ? b.scrollHeight|0 : 0);
  };
  G.__sweefMedia = function(){
    try { var m = D.querySelectorAll('audio,video'); for (var i=0;i<m.length;i++) if (!m[i].paused && !m[i].muted) return true; } catch(x){}
    return false;
  };
  G.__sweefMeta = function(){
    var r = {icon:'', title: D.title||''};
    try {
      var l = D.querySelectorAll('link[rel]'), best = '';
      for (var i=0;i<l.length;i++) {
        var rel = (l[i].getAttribute('rel')||'').toLowerCase().split(/\s+/);
        if (rel.indexOf('icon') >= 0) { best = l[i].href || l[i].getAttribute('href') || ''; if (rel.indexOf('shortcut') < 0 || !r.icon) r.icon = best; }
      }
    } catch(x){}
    return r;
  };
  // the GUI draws its own overlay scrollbar: no classic 15px gutter in the layout
  try {
    var sty = D.createElement('style');
    sty.textContent = 'html{scrollbar-width:none !important}';
    (D.head || D.documentElement).appendChild(sty);
  } catch(x){}
  // popups: window.open becomes "open a tab" (gated in Go by a recent user gesture)
  if (typeof G.__sweefOpen === 'function') {
    G.open = function(u){ try { G.__sweefOpen(String(u == null ? 'about:blank' : u)); } catch(x){} return null; };
  }
  // privileged bridge only for sweef:// documents
  if (G.location && G.location.protocol === 'sweef:' && typeof G.__sweefPost === 'function') {
    var q = [], h = null, s = {};
    s.post = function(t, p){ G.__sweefPost(String(t), p === undefined ? null : p); };
    Object.defineProperty(s, 'onMessage', {
      get: function(){ return h; },
      set: function(f){ h = f; if (typeof f === 'function') { var a = q; q = []; for (var i=0;i<a.length;i++) f(a[i][0], a[i][1]); } },
      enumerable: true
    });
    s.__recv = function(t, p){ if (typeof h === 'function') h(t, p); else q.push([t, p]); };
    Object.defineProperty(G, 'sweef', {value: s, writable: false, configurable: false});
  }
})(globalThis);`

// helperJS2 is appended to the helper script: editing state and find-in-page.
const helperJS2 = `(function(G){
  if (G.__sweefH2) return; G.__sweefH2 = 1;
  var D = G.document;
  G.__sweefEditing = function(){
    var e = D.activeElement;
    if(!e||e.nodeType!==1) return false;
    var n=e.localName;
    if(n==='textarea'||n==='select') return true;
    if(n==='input'){ var t=(e.getAttribute('type')||'text').toLowerCase(); return !/^(button|submit|reset|checkbox|radio|image|file|range|color)$/.test(t); }
    return !!e.isContentEditable;
  };
  G.__sweefKey = function(key, m){
    m = m|0;
    var ae = D.activeElement, id = (ae && ae !== D.body && ae !== D.documentElement) ? G.__swIdOf(ae) : 0;
    return G.__swDispatchKey(id, key, {shiftKey:!!(m&1),ctrlKey:!!(m&2),altKey:!!(m&4),metaKey:!!(m&8)});
  };
  // find: returns {count, top} where top is the page Y of match number idx (0-based) or -1
  G.__sweefFind = function(q, idx){
    var res = {count:0, top:-1};
    if(!q || !D.body) return res;
    q = q.toLowerCase();
    var w = D.createTreeWalker(D.body, 4), n, k = 0;
    while ((n = w.nextNode())) {
      var t = (n.nodeValue||'').toLowerCase(), p = 0, i;
      var par = n.parentElement;
      if (par && /^(script|style|noscript|template)$/.test(par.localName)) continue;
      while ((i = t.indexOf(q, p)) >= 0) {
        if (k === idx && par) {
          var r = par.getBoundingClientRect();
          res.top = Math.round(r.top + (G.__sweefY|0));
        }
        k++; p = i + q.length;
      }
    }
    res.count = k;
    return res;
  };
})(globalThis);`
