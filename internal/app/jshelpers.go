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
  G.__sweefPtr = function(kind, x, y, b, m, detail){
    var init = mods(m); init.button = b|0; init.detail = detail|0 || 1;
    var el = G.__swElAtPage(x, y);
    if (kind === 'move') { G.__swMouse('mousemove', x, y, init); return null; }
    if (kind === 'down') {
      // preventDefault on mousedown keeps the focus where it is and starts no selection
      var ok = G.__swMouse('mousedown', x, y, init) !== false;
      var res = {prevented: !ok};
      if (ok && (b|0) === 0) {
        var f = focusable(el);
        if (f) { try { f.focus(); } catch(e){} }
        else { var ae = D.activeElement; if (ae && ae !== D.body && ae.blur) { try { ae.blur(); } catch(e){} } }
        // the page can refuse the selection in selectstart
        try { var tgt = el || D.body; if (!tgt.dispatchEvent(new G.Event('selectstart', {bubbles: true, cancelable: true}))) res.prevented = true; } catch(e){}
      }
      return res;
    }
    if (kind === 'up') { G.__swMouse('mouseup', x, y, init); return null; }
    if (kind === 'click') {
      var sk = closest(el, '[data-sweef-lite="skipped"]');
      if (sk) { // Data Saver placeholder: tap to load (the app resolves the index to an engine node)
        var all = D.querySelectorAll('[data-sweef-lite="skipped"]');
        for (var i = 0; i < all.length; i++) if (all[i] === sk) return {lite: i};
      }
      var a = closest(el, 'a[href]');
      if (a && !/^\s*javascript:/i.test(a.getAttribute('href')||'')) {
        var bg = init.ctrlKey || init.metaKey || (b|0) === 1;
        // target=_blank is a window with no opener (the default of current browsers): open it as a tab here.
        // Links that keep the opener (rel=opener) and named targets go through the page, which asks the engine.
        if (bg || (a.target === '_blank' && !/(^|\s)opener(\s|$)/i.test(a.getAttribute('rel') || ''))) return {open: a.href, bg: bg};
      }
      if ((b|0) === 1) return null;
      G.__swMouse('click', x, y, init);
      if ((detail|0) === 2 && (b|0) === 0) G.__swMouse('dblclick', x, y, init);
      return null;
    }
    if (kind === 'context') {
      var ok = G.__swMouse('contextmenu', x, y, init);
      var l = closest(el, 'a[href]'), im = closest(el, 'img'), md = closest(el, 'video,audio');
      var ed = isEditable(el) ? el : null;
      if (ed && ok !== false && D.activeElement !== ed) { try { ed.focus(); } catch(e){} }
      var r = {prevented: ok === false, link: l ? l.href : '', linkText: l ? String(l.textContent || '').trim().slice(0, 200) : '',
              image: im ? (im.currentSrc || im.src || '') : '', selection: G.__sweefSel(), editable: !!ed,
              edit: ed ? G.__sweefEditState(ed) : null, media: null, frame: String(G.location && G.location.href || '')};
      if (md) {
        r.media = {kind: md.localName, src: md.currentSrc || md.src || '', paused: !!md.paused, muted: !!md.muted, loop: !!md.loop,
                   rate: +md.playbackRate || 1, controls: !!md.controls, pip: typeof md.requestPictureInPicture === 'function' && D.pictureInPictureEnabled !== false && md.localName === 'video'};
      }
      return r;
    }
    return null;
  };
  G.__sweefCursor = function(x, y){
    var el = G.__swElAtPage(x, y);
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
      if ((ae.getAttribute('type') || '').toLowerCase() === 'password') return '';
      try { var s = ae.selectionStart, e = ae.selectionEnd; if (s > e) { var t = s; s = e; e = t; } return (ae.value||'').slice(s, e); } catch(x){}
    }
    try { return String(G.getSelection ? G.getSelection() : ''); } catch(x){ return ''; }
  };
  function inputEvent(type, init){
    try { return new G.InputEvent(type, init); } catch(x){ return new G.Event(type, init); }
  }
  // undo/redo for text fields: snapshots of the value before each edit (consecutive typing within a second is one step)
  var hist = new G.WeakMap();
  function histOf(el){ var h = hist.get(el); if (!h) { h = {u: [], r: [], k: '', t: 0}; hist.set(el, h); } return h; }
  function snap(el){ var s = 0; try { s = el.selectionStart; } catch(x){} return {v: el.value || '', s: s == null ? 0 : s}; }
  function record(el, kind){
    var h = histOf(el), now = Date.now();
    if (!(kind === 'insertText' && h.k === 'insertText' && now - h.t < 1000 && h.u.length)) { h.u.push(snap(el)); if (h.u.length > 200) h.u.shift(); }
    h.r.length = 0; h.k = kind; h.t = now;
  }
  function restore(el, sn, inputType){
    el.value = sn.v;
    try { el.setSelectionRange(sn.s, sn.s); } catch(x){}
    try { el.dispatchEvent(inputEvent('input', {bubbles:true, inputType: inputType, data: null})); } catch(x){}
  }
  G.__sweefEditState = function(el){
    var h = hist.get(el) || {u: [], r: []}, hasSel = false, tag = el.localName;
    if (tag === 'input' || tag === 'textarea') { try { hasSel = el.selectionStart !== el.selectionEnd; } catch(x){} }
    else { try { var s = G.getSelection(); hasSel = !!s && !s.isCollapsed; } catch(x){} }
    var empty = (tag === 'input' || tag === 'textarea') ? !(el.value || '').length : !(el.textContent || '').length;
    return {readOnly: !!(el.readOnly || el.disabled), password: tag === 'input' && String(el.type).toLowerCase() === 'password',
            hasSel: hasSel, empty: empty, canUndo: h.u.length > 0, canRedo: h.r.length > 0};
  };
  function setVal(el, v, caret, inputType, data){
    record(el, inputType || 'insertText');
    el.value = v;
    try { el.setSelectionRange(caret, caret); } catch(x){}
    try { el.dispatchEvent(inputEvent('input', {bubbles:true, inputType: inputType || 'insertText', data: data === undefined ? null : data})); } catch(x){}
  }
  // insert text at the caret of the focused editable element; kind is the InputEvent inputType
  G.__sweefInsert = function(text, kind){ return G.__swInsertText ? G.__swInsertText(String(text), kind || 'insertText') : false; };
  function bytesOfB64(b64){ var bin = G.atob(b64), u = new Uint8Array(bin.length); for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i); return u; }
  // Ctrl+V: entries are the clipboard's flavours [{type, text} | {type, b64}]. The page sees a cancelable paste event
  // with a read-only DataTransfer; when it does not cancel it, the text goes in (beforeinput, then input).
  G.__sweefPaste = function(entries){
    var target = D.activeElement && D.activeElement !== D.documentElement ? D.activeElement : D.body;
    if (!target) return false;
    var list = [];
    (entries || []).forEach(function(e){
      if (e.b64 !== undefined) { var ext = (e.type.split('/')[1] || 'bin').replace(/[^a-z0-9]/g, ''); try { list.push({type: e.type, file: new G.File([bytesOfB64(e.b64)], 'image.' + ext, {type: e.type})}); } catch(x){} }
      else list.push({type: e.type, text: e.text});
    });
    var dt = G.__swMakeDataTransfer(list, true), ev;
    try { ev = new G.ClipboardEvent('paste', {bubbles:true, cancelable:true, clipboardData: dt}); } catch(x){ return false; }
    if (!target.dispatchEvent(ev)) return true; // the page handled it
    var text = dt.getData('text/plain'), html = dt.getData('text/html');
    if (!text && !html) return false;
    return G.__swPasteInto(html, text || '');
  };
  function selectionHTML(){
    try {
      var s = G.getSelection(); if (!s || !s.rangeCount || s.isCollapsed) return '';
      var d = D.createElement('div'); d.appendChild(s.getRangeAt(0).cloneContents()); return d.innerHTML;
    } catch(x){ return ''; }
  }
  // Ctrl+C / Ctrl+X: the page sees a cancelable copy or cut event with a writable DataTransfer; when it supplies data
  // or cancels, its data is what is copied, otherwise the selection (text and html)
  G.__sweefCopy = function(op){
    var target = D.activeElement && D.activeElement !== D.documentElement && D.activeElement !== D.body ? D.activeElement : null;
    if (!target) { var sn = null; try { sn = G.getSelection().anchorNode; } catch(x){} target = sn ? (sn.nodeType === 1 ? sn : sn.parentElement) : null; }
    target = target || D.body;
    var dt = G.__swMakeDataTransfer([], false), prevented = false;
    if (target) { try { prevented = !target.dispatchEvent(new G.ClipboardEvent(op, {bubbles:true, cancelable:true, clipboardData: dt})); } catch(x){} }
    var set = G.__swDataTransferEntries(dt).filter(function(e){ return e.text !== undefined; });
    if (prevented || set.length) return {handled: true, entries: set};
    var text = G.__sweefSel();
    return {handled: false, entries: text ? [{type:'text/plain', text: text}].concat((function(){ var h = selectionHTML(); return h ? [{type:'text/html', text:h}] : []; })()) : []};
  };
  // edit: selectall | cut | undo | redo
  G.__sweefEdit = function(op){
    if (op === 'selectall') return G.__swSelectAll ? G.__swSelectAll() : false;
    if (op === 'cut') return G.__swCutSelection ? G.__swCutSelection() : false;
    if (op === 'undo') return G.__swUndo ? G.__swUndo(false) : false;
    if (op === 'redo') return G.__swUndo ? G.__swUndo(true) : false;
    return false;
  };
  // document.execCommand('copy' | 'cut'): the copy or cut event runs, then what it left (or the selection) goes to the clipboard
  function b64OfText(t){ try { return G.btoa(unescape(encodeURIComponent(t))); } catch(x){ return ''; } }
  G.__sweefCopyCommand = function(op){
    var r = G.__sweefCopy(op);
    if (!r.entries.length) return !!r.handled;
    try { if (!G.__sw.execCopy(JSON.stringify(r.entries.map(function(e){ return {type: e.type, b64: b64OfText(e.text)}; })))) return false; } catch(x){ return false; }
    if (op === 'cut' && !r.handled && G.__swCutSelection) G.__swCutSelection();
    return true;
  };
  // a wheel turn: the page's wheel listeners run, and the answer says whether one cancelled it and whether the
  // page has any listener that can
  G.__sweefWheel = function(dx, dy, x, y, m){
    var ok = G.__swWheel(dx, dy, x, y, m);
    return {prevented: ok === false, np: !!G.__swWheelNP()};
  };
  G.__sweefScrollTo = function(y){
    try { G.__swScrollEvent('document'); } catch(x){}
  };
  G.__sweefDocH = function(){
    var de = D.documentElement, b = D.body;
    return Math.max(de ? de.scrollHeight|0 : 0, b ? b.scrollHeight|0 : 0);
  };
  // coarse page kind for the sidebar's suggestion chips
  G.__sweefPageType = function(){
    try {
      var og = D.querySelector('meta[property="og:type"]'), t = og ? (og.getAttribute('content')||'').toLowerCase() : '';
      if (D.querySelector('video') || t.indexOf('video') >= 0) return 'video';
      if (D.querySelector('input[type=password]') || D.querySelectorAll('form input[type=text],form input[type=email]').length >= 3) return 'form';
      if (t.indexOf('product') >= 0 || D.querySelector('[itemtype*="Product"]')) return 'shop';
      if (D.querySelector('article') || t === 'article') return 'article';
    } catch(x){}
    return 'any';
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
    sty.textContent = 'html{overflow-y:hidden !important}';
    sty.setAttribute('data-sweef-internal', ''); // Developer Tools leaves it out of the tree, the sources and the matched rules
    (D.head || D.documentElement).appendChild(sty);
  } catch(x){}
  // privileged bridge only for sweef:// documents
  if (G.location && G.location.protocol === 'sweef:' && typeof G.__sweefPost === 'function') {
    var q = [], h = null, s = {sync: true};  // sync: the host settles the page and paints once after every message, and answers omni.input inside the same event turn
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
    var mods = {shiftKey:!!(m&1),ctrlKey:!!(m&2),altKey:!!(m&4),metaKey:!!(m&8)};
    if ((m & 14) && key.length === 1) {
      // a chord (Ctrl+T, ...): the page sees keydown/keyup, but nothing is typed
      var code = /[a-z]/i.test(key) ? 'Key' + key.toUpperCase() : /[0-9]/.test(key) ? 'Digit' + key : '';
      var init = {key: key, code: code, bubbles: true, cancelable: true, shiftKey: mods.shiftKey, ctrlKey: mods.ctrlKey, altKey: mods.altKey, metaKey: mods.metaKey};
      var ok = G.__swDispatch(id, 'keydown', init);
      G.__swDispatch(id, 'keyup', Object.assign({}, init, {cancelable: false}));
      return ok;
    }
    return G.__swDispatchKey(id, key, mods);
  };
  // context-menu actions on the media element under (x,y): toggle | mute | loop | rate | pip
  G.__sweefCtxAct = function(op, x, y, arg){
    try {
      var el = D.elementFromPoint(x, y), m = el && el.closest ? el.closest('video,audio') : null;
      if (!m) return false;
      if (op === 'toggle') { if (m.paused) { var p = m.play(); if (p && p.catch) p.catch(function(){}); } else m.pause(); }
      else if (op === 'mute') m.muted = !m.muted;
      else if (op === 'loop') m.loop = !m.loop;
      else if (op === 'rate') m.playbackRate = +arg || 1;
      else if (op === 'pip') { var q = m.requestPictureInPicture(); if (q && q.catch) q.catch(function(){}); }
      else return false;
      return true;
    } catch(x2){ return false; }
  // a key chord the window handles itself (copy, paste, undo...): the page sees keydown and keyup, and the answer is
  // false when it cancelled the keydown
  G.__sweefChord = function(key, m){
    m = m|0;
    var ae = D.activeElement, id = (ae && ae !== D.body && ae !== D.documentElement) ? G.__swIdOf(ae) : 0;
    var init = {key: key, code: /^[a-z]$/i.test(key) ? 'Key' + key.toUpperCase() : key, bubbles: true, cancelable: true, shiftKey: !!(m&1), ctrlKey: !!(m&2), altKey: !!(m&4), metaKey: !!(m&8)};
    var ok = G.__swDispatch(id, 'keydown', init);
    G.__swDispatch(id, 'keyup', Object.assign({}, init, {cancelable: false}));
    return ok !== false;
  };
  // true when something other than the viewport placeholder (or an ancestor of it) is at (x,y):
  // a scrim, modal or popover that must receive pointer events instead of the page
  G.__sweefHoleBusy = function(x, y){
    var v = D.getElementById('viewport');
    if (!v) return false;
    var e = D.elementFromPoint(x, y);
    return !!e && !(e === v || e.contains(v));
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
