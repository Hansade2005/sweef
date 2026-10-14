/*
 * edit_prelude.js: ranges, the selection, text control selection, and editing (contenteditable, text controls).
 *
 * The state of the selection lives in Go (so the mouse, painting and the clipboard can use it without a round trip
 * into script): the document selection is a pair of boundary points (__sw.selGet / selSet), the selection of a
 * text control a start, an end and a direction (__sw.ctlSelGet / ctlSelSet). This file gives script the standard
 * objects over that state, the DOM Range with its editing operations, and the default actions of the keyboard in
 * editable places: typing, deleting, caret movement, undo and redo, paste with sanitising.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  // the document does not exist yet when the prelude runs (a pooled VM is built before it is bound to a page)
  function DOC() { return global.document; }
  if (!sw || typeof sw.selGet !== 'function') return;
  var idOf = global.__swIdOf, wrapId = global.__swWrap;

  function DOMEx(msg, name) { return new global.DOMException(msg, name); }
  function isCharData(n) { var t = n.nodeType; return t === 3 || t === 4 || t === 7 || t === 8; }
  function nodeLength(n) { if (n.nodeType === 10) return 0; return isCharData(n) ? n.data.length : n.childNodes.length; }
  function indexOf(n) { var p = n.parentNode; if (!p) return 0; var k = p.childNodes; for (var i = 0; i < k.length; i++) if (k[i] === n) return i; return -1; }
  function rootOf(n) { while (n.parentNode) n = n.parentNode; return n; }
  function isInclusiveAncestor(a, b) { for (; b; b = b.parentNode) if (a === b) return true; return false; }
  // the child indexes down from the root, then the offset; a child c counts as 2c+1 and an offset o as 2o
  function pathKey(n, o) {
    var rev = [];
    for (var x = n; x && x.parentNode; x = x.parentNode) rev.push(2 * indexOf(x) + 1);
    rev.reverse(); rev.push(2 * o); return rev;
  }
  function comparePoints(an, ao, bn, bo) {
    if (an === bn) return ao < bo ? -1 : ao > bo ? 1 : 0;
    var ka = pathKey(an, ao), kb = pathKey(bn, bo);
    for (var i = 0; i < ka.length && i < kb.length; i++) if (ka[i] !== kb[i]) return ka[i] < kb[i] ? -1 : 1;
    return ka.length < kb.length ? -1 : ka.length > kb.length ? 1 : 0;
  }
  function checkBoundary(n, o, what) {
    if (!n || typeof n.nodeType !== 'number') throw new TypeError("Failed to execute '" + what + "' on 'Range': parameter 1 is not of type 'Node'.");
    if (n.nodeType === 10) throw DOMEx("Failed to execute '" + what + "' on 'Range': The node provided is a doctype.", 'InvalidNodeTypeError');
    o = o >>> 0;
    if (o > nodeLength(n)) throw DOMEx("Failed to execute '" + what + "' on 'Range': The offset " + o + ' is larger than the node\'s length (' + nodeLength(n) + ').', 'IndexSizeError');
    return o;
  }

  // ---- live ranges --------------------------------------------------------------------------------------
  var live = [];
  var hasWeak = typeof global.WeakRef === 'function';
  function register(r) { live.push(hasWeak ? new global.WeakRef(r) : { deref: function () { return r; } }); if (live.length > 256 && (live.length & 255) === 0) live = live.filter(function (w) { return w.deref(); }); }
  function eachLive(fn) { for (var i = 0; i < live.length; i++) { var r = live[i].deref(); if (r) fn(r); } }
  var selRange = null, selKey = '', selDir = 0;

  function Range() {
    global.__swRangesLive = true;
    this._sc = this._ec = DOC(); this._so = this._eo = 0; this._linked = false; this._dir = 1;
    register(this);
  }
  function getters(o, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(o, k, { get: map[k], enumerable: true, configurable: true }); }); }
  getters(Range.prototype, {
    startContainer: function () { return this._sc; }, startOffset: function () { return this._so; },
    endContainer: function () { return this._ec; }, endOffset: function () { return this._eo; },
    collapsed: function () { return this._sc === this._ec && this._so === this._eo; },
    commonAncestorContainer: function () { var a = this._sc; while (a && !isInclusiveAncestor(a, this._ec)) a = a.parentNode; return a || DOC(); }
  });
  var P = Range.prototype;
  function set(r, sc, so, ec, eo) { r._sc = sc; r._so = so; r._ec = ec; r._eo = eo; if (r._linked && r === selRange) pushSelection(r); }
  P._set = function (sc, so, ec, eo) { this._sc = sc; this._so = so; this._ec = ec; this._eo = eo; };
  function setBoundary(r, isStart, n, o, what) {
    o = checkBoundary(n, o, what);
    if (isStart) {
      if (rootOf(n) !== rootOf(r._ec) || comparePoints(n, o, r._ec, r._eo) > 0) set(r, n, o, n, o); else set(r, n, o, r._ec, r._eo);
    } else {
      if (rootOf(n) !== rootOf(r._sc) || comparePoints(n, o, r._sc, r._so) < 0) set(r, n, o, n, o); else set(r, r._sc, r._so, n, o);
    }
  }
  P.setStart = function (n, o) { setBoundary(this, true, n, o, 'setStart'); };
  P.setEnd = function (n, o) { setBoundary(this, false, n, o, 'setEnd'); };
  function parentOf(n, what) { var p = n.parentNode; if (!p) throw DOMEx("Failed to execute '" + what + "' on 'Range': the given Node has no parent.", 'InvalidNodeTypeError'); return p; }
  P.setStartBefore = function (n) { var p = parentOf(n, 'setStartBefore'); setBoundary(this, true, p, indexOf(n), 'setStartBefore'); };
  P.setStartAfter = function (n) { var p = parentOf(n, 'setStartAfter'); setBoundary(this, true, p, indexOf(n) + 1, 'setStartAfter'); };
  P.setEndBefore = function (n) { var p = parentOf(n, 'setEndBefore'); setBoundary(this, false, p, indexOf(n), 'setEndBefore'); };
  P.setEndAfter = function (n) { var p = parentOf(n, 'setEndAfter'); setBoundary(this, false, p, indexOf(n) + 1, 'setEndAfter'); };
  P.collapse = function (toStart) { if (toStart) set(this, this._sc, this._so, this._sc, this._so); else set(this, this._ec, this._eo, this._ec, this._eo); };
  P.selectNode = function (n) { var p = parentOf(n, 'selectNode'), i = indexOf(n); set(this, p, i, p, i + 1); };
  P.selectNodeContents = function (n) { if (n.nodeType === 10) throw DOMEx("Failed to execute 'selectNodeContents' on 'Range': The node provided is a doctype.", 'InvalidNodeTypeError'); set(this, n, 0, n, nodeLength(n)); };
  P.cloneRange = function () { var r = new Range(); r._set(this._sc, this._so, this._ec, this._eo); return r; };
  P.detach = function () { };
  P.compareBoundaryPoints = function (how, other) {
    if (how > 3 || how < 0) throw DOMEx("Failed to execute 'compareBoundaryPoints' on 'Range': The comparison method provided must be one of 'START_TO_START', 'START_TO_END', 'END_TO_END', or 'END_TO_START'.", 'NotSupportedError');
    if (rootOf(this._sc) !== rootOf(other._sc)) throw DOMEx("Failed to execute 'compareBoundaryPoints' on 'Range': The two Ranges are not in the same tree.", 'WrongDocumentError');
    var tn, to, on, oo;
    switch (how) {
      case 0: tn = this._sc; to = this._so; on = other._sc; oo = other._so; break;
      case 1: tn = this._ec; to = this._eo; on = other._sc; oo = other._so; break;
      case 2: tn = this._ec; to = this._eo; on = other._ec; oo = other._eo; break;
      default: tn = this._sc; to = this._so; on = other._ec; oo = other._eo;
    }
    return comparePoints(tn, to, on, oo);
  };
  P.comparePoint = function (n, o) {
    o = checkBoundary(n, o, 'comparePoint');
    if (rootOf(n) !== rootOf(this._sc)) throw DOMEx("Failed to execute 'comparePoint' on 'Range': The node provided is not in the same tree.", 'WrongDocumentError');
    if (comparePoints(n, o, this._sc, this._so) < 0) return -1;
    if (comparePoints(n, o, this._ec, this._eo) > 0) return 1;
    return 0;
  };
  P.isPointInRange = function (n, o) {
    if (rootOf(n) !== rootOf(this._sc)) return false;
    o = checkBoundary(n, o, 'isPointInRange');
    return comparePoints(n, o, this._sc, this._so) >= 0 && comparePoints(n, o, this._ec, this._eo) <= 0;
  };
  P.intersectsNode = function (n) {
    if (rootOf(n) !== rootOf(this._sc)) return false;
    var p = n.parentNode; if (!p) return true;
    var i = indexOf(n);
    return comparePoints(p, i, this._ec, this._eo) < 0 && comparePoints(p, i + 1, this._sc, this._so) > 0;
  };
  // the part of the range inside the subtree of parent, copied (cloneContents) or moved
  function between(parent, r, frag) {
    var kids = parent.childNodes;
    for (var i = 0; i < kids.length; i++) {
      var c = kids[i];
      if (comparePoints(parent, i + 1, r._sc, r._so) <= 0) continue; // wholly before
      if (comparePoints(parent, i, r._ec, r._eo) >= 0) break;        // wholly after
      var inside = comparePoints(parent, i, r._sc, r._so) >= 0 && comparePoints(parent, i + 1, r._ec, r._eo) <= 0;
      if (inside) { frag.appendChild(c.cloneNode(true)); continue; }
      if (isCharData(c)) {
        var cc = c.cloneNode(false);
        cc.data = c.data.substring(c === r._sc ? r._so : 0, c === r._ec ? r._eo : c.data.length);
        frag.appendChild(cc);
      } else {
        var sh = c.cloneNode(false);
        between(c, r, sh);
        frag.appendChild(sh);
      }
    }
  }
  P.cloneContents = function () {
    var frag = DOC().createDocumentFragment();
    if (this.collapsed) return frag;
    if (this._sc === this._ec && isCharData(this._sc)) { var c = this._sc.cloneNode(false); c.data = this._sc.data.substring(this._so, this._eo); frag.appendChild(c); return frag; }
    between(this.commonAncestorContainer, this, frag);
    return frag;
  };
  function replaceData(n, off, count, str) { n.replaceData ? n.replaceData(off, count, str) : (n.data = n.data.slice(0, off) + str + n.data.slice(off + count)); }
  P.deleteContents = function () {
    if (this.collapsed) return;
    var sc = this._sc, so = this._so, ec = this._ec, eo = this._eo;
    if (sc === ec && isCharData(sc)) { replaceData(sc, so, eo - so, ''); set(this, sc, so, sc, so); return; }
    var common = this.commonAncestorContainer, toRemove = [];
    (function collect(parent) {
      var kids = parent.childNodes;
      for (var i = 0; i < kids.length; i++) {
        var c = kids[i];
        if (comparePoints(parent, i + 1, sc, so) <= 0) continue;
        if (comparePoints(parent, i, ec, eo) >= 0) break;
        if (comparePoints(parent, i, sc, so) >= 0 && comparePoints(parent, i + 1, ec, eo) <= 0) toRemove.push(c);
        else if (!isCharData(c)) collect(c);
      }
    })(common);
    var nn, no;
    if (isInclusiveAncestor(sc, ec)) { nn = sc; no = so; }
    else { var ref = sc; while (ref.parentNode && !isInclusiveAncestor(ref.parentNode, ec)) ref = ref.parentNode; nn = ref.parentNode; no = indexOf(ref) + 1; }
    if (isCharData(sc) && sc !== ec) replaceData(sc, so, sc.data.length - so, '');
    toRemove.forEach(function (n) { if (n.parentNode) n.parentNode.removeChild(n); });
    if (isCharData(ec) && sc !== ec) replaceData(ec, 0, eo, '');
    set(this, nn, no, nn, no);
  };
  P.extractContents = function () { var f = this.cloneContents(); this.deleteContents(); return f; };
  P.insertNode = function (node) {
    var sc = this._sc;
    if (sc.nodeType === 8 || (sc.nodeType === 3 && !sc.parentNode)) throw DOMEx("Failed to execute 'insertNode' on 'Range': Nodes of type '" + node.nodeName + "' may not be inserted inside nodes of type '" + sc.nodeName + "'.", 'HierarchyRequestError');
    var ref, parent;
    if (sc.nodeType === 3) { ref = sc.splitText(this._so); parent = sc.parentNode; } else { ref = sc.childNodes[this._so] || null; parent = sc; }
    var wasCollapsed = this.collapsed;
    parent.insertBefore(node, ref);
    if (wasCollapsed) set(this, this._sc, this._so, parent, ref ? indexOf(ref) : parent.childNodes.length);
  };
  P.surroundContents = function (np) {
    var r = this, a = this.commonAncestorContainer, bad = false;
    (function walk(parent) {
      var kids = parent.childNodes;
      for (var i = 0; i < kids.length; i++) {
        var c = kids[i];
        if (isCharData(c)) continue;
        var touches = comparePoints(parent, i + 1, r._sc, r._so) > 0 && comparePoints(parent, i, r._ec, r._eo) < 0;
        var inside = comparePoints(parent, i, r._sc, r._so) >= 0 && comparePoints(parent, i + 1, r._ec, r._eo) <= 0;
        if (touches && !inside) bad = true; else if (touches) continue;
      }
    })(a);
    if (bad) throw DOMEx("Failed to execute 'surroundContents' on 'Range': The Range has partially selected a non-Text node.", 'InvalidStateError');
    var frag = this.extractContents();
    while (np.firstChild) np.removeChild(np.firstChild);
    this.insertNode(np);
    np.appendChild(frag);
    this.selectNode(np);
  };
  P.createContextualFragment = function (html) { var t = DOC().createElement('template'); t.innerHTML = String(html); return t.content; };
  P.toString = function () {
    var r = this, out = '';
    if (r._sc === r._ec && isCharData(r._sc)) return r._sc.data.substring(r._so, r._eo);
    if (isCharData(r._sc)) out += r._sc.data.substring(r._so);
    var walker = function (n) {
      if (n === r._ec) return;
      if (n.nodeType === 3 && comparePoints(n.parentNode, indexOf(n), r._sc, r._so) >= 0 && comparePoints(n.parentNode, indexOf(n) + 1, r._ec, r._eo) <= 0) out += n.data;
      for (var c = n.firstChild; c; c = c.nextSibling) walker(c);
    };
    walker(this.commonAncestorContainer);
    if (isCharData(r._ec) && r._sc !== r._ec) out += r._ec.data.substring(0, r._eo);
    return out;
  };
  var REPLACED = /^(img|svg|canvas|video|audio|iframe|hr|br|input|textarea|select|button|object|embed)$/;
  P.getClientRects = function () {
    var r = this, out = [], p = scrollPos();
    var rs = sw.rangeRects(idOf(r._sc), r._so, idOf(r._ec), r._eo) || [];
    rs.forEach(function (q) { out.push(new global.DOMRect(q[0] - p[0], q[1] - p[1], q[2], q[3])); });
    // elements without text of their own (images, rules, line breaks) that the range covers whole
    (function walk(parent) {
      var kids = parent.childNodes;
      for (var i = 0; i < kids.length; i++) {
        var c = kids[i];
        if (c.nodeType !== 1) continue;
        if (comparePoints(parent, i + 1, r._sc, r._so) <= 0 || comparePoints(parent, i, r._ec, r._eo) >= 0) continue;
        if (REPLACED.test(c.localName)) { if (comparePoints(parent, i, r._sc, r._so) >= 0 && comparePoints(parent, i + 1, r._ec, r._eo) <= 0) out.push(c.getBoundingClientRect()); }
        else walk(c);
      }
    })(this.commonAncestorContainer);
    out.item = function (i) { return out[i] || null; };
    return out;
  };
  P.getBoundingClientRect = function () {
    var rs = this.getClientRects();
    if (!rs.length) return new global.DOMRect(0, 0, 0, 0);
    var l = Infinity, t = Infinity, rt = -Infinity, b = -Infinity;
    rs.forEach(function (q) { l = Math.min(l, q.left); t = Math.min(t, q.top); rt = Math.max(rt, q.right); b = Math.max(b, q.bottom); });
    return new global.DOMRect(l, t, rt - l, b - t);
  };
  Object.defineProperty(P, Symbol.toStringTag, { value: 'Range', configurable: true });
  ['START_TO_START', 'START_TO_END', 'END_TO_END', 'END_TO_START'].forEach(function (k, i) { Object.defineProperty(Range, k, { value: i, enumerable: true }); Object.defineProperty(P, k, { value: i, enumerable: true }); });
  function scrollPos() { var p = sw.scrollPos && sw.scrollPos(); return p || [0, 0]; }

  // ---- live range updates when the tree changes ---------------------------------------------------------
  // (called by the DOM layer; see __swRangeNotify in dom_prelude.js)
  global.__swRangeNotify = function (kind, a, b, c, d) {
    switch (kind) {
      case 'remove': { // node a removed from parent b at index c
        eachLive(function (r) {
          var sc = r._sc, so = r._so, ec = r._ec, eo = r._eo, ch = false;
          if (isInclusiveAncestor(a, sc)) { sc = b; so = c; ch = true; } else if (sc === b && so > c) { so--; ch = true; }
          if (isInclusiveAncestor(a, ec)) { ec = b; eo = c; ch = true; } else if (ec === b && eo > c) { eo--; ch = true; }
          if (ch) { r._sc = sc; r._so = so; r._ec = ec; r._eo = eo; }
        });
        break;
      }
      case 'insert': { // n nodes inserted into parent a at index b
        eachLive(function (r) {
          if (r._sc === a && r._so > b) r._so += c;
          if (r._ec === a && r._eo > b) r._eo += c;
        });
        break;
      }
      case 'data': { // text node a: count characters at offset b replaced by c characters
        eachLive(function (r) {
          if (r._sc === a) { if (r._so > b && r._so <= b + c) r._so = b; else if (r._so > b + c) r._so += d - c; }
          if (r._ec === a) { if (r._eo > b && r._eo <= b + c) r._eo = b; else if (r._eo > b + c) r._eo += d - c; }
        });
        break;
      }
      case 'split': { // text node a split at offset b; the rest is node c, inserted after a
        eachLive(function (r) {
          if (r._sc === a && r._so > b) { r._sc = c; r._so -= b; }
          if (r._ec === a && r._eo > b) { r._ec = c; r._eo -= b; }
        });
        break;
      }
    }
  };

  global.Range = Range;
  global.StaticRange = function StaticRange(init) {
    if (!init || !init.startContainer || !init.endContainer) throw new TypeError("Failed to construct 'StaticRange': required member startContainer is undefined.");
    this.startContainer = init.startContainer; this.startOffset = init.startOffset >>> 0; this.endContainer = init.endContainer; this.endOffset = init.endOffset >>> 0;
    Object.defineProperty(this, 'collapsed', { get: function () { return this.startContainer === this.endContainer && this.startOffset === this.endOffset; } });
  };

  // ---- the selection --------------------------------------------------------------------------------------
  function Selection() { throw new TypeError('Illegal constructor'); }
  var SP = Selection.prototype;
  var selChangeTimer = null;
  function scheduleSelChange() {
    if (selChangeTimer !== null) return;
    selChangeTimer = global.setTimeout(function () {
      selChangeTimer = null;
      DOC().dispatchEvent(new global.Event('selectionchange', { bubbles: false, cancelable: false }));
    }, 0);
  }
  global.__swSelChanged = function () { scheduleSelChange(); return true; };
  function goSel() {
    var g = sw.selGet();
    return g ? { an: wrapId(g[0]), ao: g[1], fn: wrapId(g[2]), fo: g[3] } : null;
  }
  function range() {
    var g = sw.selGet();
    if (!g) { selRange = null; selKey = ''; return null; }
    var key = g.join(',');
    if (key !== selKey || !selRange) {
      selKey = key;
      var an = wrapId(g[0]), fn = wrapId(g[2]), r = new Range(), c = comparePoints(an, g[1], fn, g[3]);
      if (c <= 0) r._set(an, g[1], fn, g[3]); else r._set(fn, g[3], an, g[1]);
      r._linked = true; r._dir = c < 0 ? 1 : c > 0 ? -1 : 0;
      selRange = r;
    }
    return selRange;
  }
  function pushSelection(r) {
    var fwd = r._dir >= 0;
    var an = fwd ? r._sc : r._ec, ao = fwd ? r._so : r._eo, fn = fwd ? r._ec : r._sc, fo = fwd ? r._eo : r._so;
    sw.selSet(idOf(an), ao, idOf(fn), fo);
    selKey = (idOf(an) + ',' + ao + ',' + idOf(fn) + ',' + fo);
    scheduleSelChange();
  }
  function setBase(an, ao, fn, fo) {
    sw.selSet(idOf(an), ao, idOf(fn), fo);
    scheduleSelChange();
  }
  function ctlFocused() { var a = DOC().activeElement; return a && isTextCtl(a) ? a : null; }
  getters(SP, {
    anchorNode: function () { var g = goSel(); return g ? g.an : null; },
    anchorOffset: function () { var g = goSel(); return g ? g.ao : 0; },
    focusNode: function () { var g = goSel(); return g ? g.fn : null; },
    focusOffset: function () { var g = goSel(); return g ? g.fo : 0; },
    baseNode: function () { var g = goSel(); return g ? g.an : null; },
    baseOffset: function () { var g = goSel(); return g ? g.ao : 0; },
    extentNode: function () { var g = goSel(); return g ? g.fn : null; },
    extentOffset: function () { var g = goSel(); return g ? g.fo : 0; },
    isCollapsed: function () { var g = goSel(); return !g || (g.an === g.fn && g.ao === g.fo); },
    rangeCount: function () { return goSel() ? 1 : 0; },
    type: function () { var g = goSel(); return !g ? 'None' : (g.an === g.fn && g.ao === g.fo ? 'Caret' : 'Range'); },
    direction: function () { var g = goSel(); if (!g || (g.an === g.fn && g.ao === g.fo)) return 'none'; return comparePoints(g.an, g.ao, g.fn, g.fo) < 0 ? 'forward' : 'backward'; }
  });
  function inDoc(n) { return n && n.isConnected && rootOf(n) === DOC(); }
  var M = {
    getRangeAt: function (i) { var r = range(); if (!r || (i >>> 0) !== 0) throw DOMEx("Failed to execute 'getRangeAt' on 'Selection': " + i + ' is not a valid index.', 'IndexSizeError'); return r; },
    addRange: function (r) {
      if (!(r instanceof Range)) throw new TypeError("Failed to execute 'addRange' on 'Selection': parameter 1 is not of type 'Range'.");
      if (goSel() || !inDoc(r._sc)) return; // one range only
      var c = r.cloneRange(); r._linked = false;
      pushSelection(Object.assign(c, { _dir: 1 }));
      // the range the page added is the one getRangeAt returns, so edits to it keep acting on the selection
      selRange = r; r._linked = true; r._dir = 1; selKey = sw.selGet().join(',');
    },
    removeRange: function (r) { if (range() === r) { r._linked = false; M.removeAllRanges(); } else throw DOMEx("Failed to execute 'removeRange' on 'Selection': The given range isn't in the selection.", 'NotFoundError'); },
    removeAllRanges: function () { if (selRange) selRange._linked = false; selRange = null; selKey = ''; sw.selSet(0, 0, 0, 0); scheduleSelChange(); },
    empty: function () { M.removeAllRanges(); },
    collapse: function (n, o) {
      if (n === null || n === undefined) { M.removeAllRanges(); return; }
      o = checkBoundary(n, o === undefined ? 0 : o, 'collapse');
      if (!inDoc(n)) return;
      setBase(n, o, n, o);
    },
    setPosition: function (n, o) { M.collapse(n, o); },
    collapseToStart: function () { var r = range(); if (!r) throw DOMEx("Failed to execute 'collapseToStart' on 'Selection': there is no selection.", 'InvalidStateError'); setBase(r._sc, r._so, r._sc, r._so); },
    collapseToEnd: function () { var r = range(); if (!r) throw DOMEx("Failed to execute 'collapseToEnd' on 'Selection': there is no selection.", 'InvalidStateError'); setBase(r._ec, r._eo, r._ec, r._eo); },
    extend: function (n, o) {
      var g = goSel(); if (!g) throw DOMEx("Failed to execute 'extend' on 'Selection': This Selection object doesn't have any Ranges.", 'InvalidStateError');
      o = checkBoundary(n, o === undefined ? 0 : o, 'extend');
      if (!inDoc(n)) return;
      setBase(g.an, g.ao, n, o);
    },
    setBaseAndExtent: function (an, ao, fn, fo) {
      ao = checkBoundary(an, ao, 'setBaseAndExtent'); fo = checkBoundary(fn, fo, 'setBaseAndExtent');
      if (!inDoc(an) || !inDoc(fn)) return;
      setBase(an, ao, fn, fo);
    },
    selectAllChildren: function (n) { if (n.nodeType === 10) throw DOMEx("Failed to execute 'selectAllChildren' on 'Selection': doctype", 'InvalidNodeTypeError'); if (!inDoc(n)) return; setBase(n, 0, n, nodeLength(n)); },
    deleteFromDocument: function () { var r = range(); if (!r) return; if (ctlFocused()) return; r.deleteContents(); },
    containsNode: function (n, partial) {
      var r = range(); if (!r || rootOf(n) !== rootOf(r._sc)) return false;
      var p = n.parentNode; if (!p) return true;
      var i = indexOf(n);
      var s = comparePoints(p, i, r._sc, r._so), e = comparePoints(p, i + 1, r._ec, r._eo);
      return partial ? (comparePoints(p, i, r._ec, r._eo) < 0 && comparePoints(p, i + 1, r._sc, r._so) > 0) : (s >= 0 && e <= 0);
    },
    toString: function () { return sw.selText() || ''; },
    getComposedRanges: function () { var r = range(); return r ? [new global.StaticRange({ startContainer: r._sc, startOffset: r._so, endContainer: r._ec, endOffset: r._eo })] : []; },
    // Chromium's Selection.modify, which editors use for caret movement
    modify: function (alter, direction, granularity) {
      var g = goSel(); if (!g) return;
      alter = String(alter).toLowerCase(); direction = String(direction).toLowerCase(); granularity = String(granularity).toLowerCase();
      var back = direction === 'backward' || direction === 'left';
      var dir;
      switch (granularity) {
        case 'character': dir = back ? 'left' : 'right'; break;
        case 'word': dir = back ? 'wordleft' : 'wordright'; break;
        case 'line': dir = back ? 'up' : 'down'; break;
        case 'lineboundary': dir = back ? 'linestart' : 'lineend'; break;
        case 'documentboundary': dir = back ? 'docstart' : 'docend'; break;
        case 'paragraph': case 'sentence': dir = back ? 'wordleft' : 'wordright'; break;
        case 'paragraphboundary': dir = back ? 'linestart' : 'lineend'; break;
        default: return;
      }
      var collapsed = g.an === g.fn && g.ao === g.fo;
      if (alter === 'move' && !collapsed && (granularity === 'character')) {
        var r = range();
        if (back) setBase(r._sc, r._so, r._sc, r._so); else setBase(r._ec, r._eo, r._ec, r._eo);
        return;
      }
      var m = sw.caretMove(idOf(g.fn), g.fo, dir);
      if (!m) return;
      var nn = wrapId(m[0]);
      if (alter === 'extend') setBase(g.an, g.ao, nn, m[1]); else setBase(nn, m[1], nn, m[1]);
    }
  };
  Object.keys(M).forEach(function (k) { Object.defineProperty(SP, k, { value: M[k], writable: true, configurable: true, enumerable: true }); });
  Object.defineProperty(SP, Symbol.toStringTag, { value: 'Selection', configurable: true });
  var theSelection = Object.create(SP);
  global.Selection = Selection;
  global.getSelection = function () { return theSelection; };

  // the caret position under a point (viewport coordinates)
  var DP = global.Document.prototype;
  function docMethod(name, fn) { Object.defineProperty(DP, name, { value: fn, writable: true, configurable: true, enumerable: true }); }
  docMethod('caretPositionFromPoint', function (x, y) {
    var p = scrollPos(), r = sw.posAt((+x || 0) + p[0], (+y || 0) + p[1]);
    return r ? { offsetNode: wrapId(r[0]), offset: r[1], getClientRect: function () { return null; } } : null;
  });
  docMethod('caretRangeFromPoint', function (x, y) {
    var p = scrollPos(), r = sw.posAt((+x || 0) + p[0], (+y || 0) + p[1]);
    if (!r) return null;
    var rg = new Range(); var n = wrapId(r[0]); rg._set(n, r[1], n, r[1]); return rg;
  });

  // ---- selection of text controls ---------------------------------------------------------------------------
  var TEXTY = /^(text|search|url|tel|email|password|number|)$/;
  function isTextCtl(el) {
    if (!el || el.nodeType !== 1) return false;
    if (el.localName === 'textarea') return true;
    return el.localName === 'input' && TEXTY.test((el.getAttribute('type') || '').toLowerCase());
  }
  function supportsSel(el) { // number inputs have a selection internally but expose none to script
    return el.localName === 'textarea' || (el.localName === 'input' && /^(text|search|url|tel|password|)$/.test((el.getAttribute('type') || '').toLowerCase()));
  }
  function getSel(el) {
    var len = el.value.length, g = sw.ctlSelGet(idOf(el));
    if (!g) return [len, len, 0];
    return [Math.min(g[0], len), Math.min(g[1], len), g[2]];
  }
  var selectPending = new Set();
  function queueSelectEvent(el) {
    if (selectPending.has(el)) return;
    selectPending.add(el);
    global.setTimeout(function () { selectPending.delete(el); el.dispatchEvent(new global.Event('select', { bubbles: true, cancelable: false })); }, 0);
  }
  function setSel(el, s, e, dir, quiet) {
    var len = el.value.length;
    s = Math.max(0, Math.min(len, s)); e = Math.max(s, Math.min(len, e));
    sw.ctlSelSet(idOf(el), s, e, dir);
    ensureVisible(el);
    scheduleSelChange();
    if (!quiet) queueSelectEvent(el);
  }
  function dirNum(d) { return d === 'backward' ? -1 : d === 'forward' ? 1 : 0; }
  function ctlProps(proto) {
    var ss = {
      selectionStart: {
        get: function () { return supportsSel(this) ? getSel(this)[0] : null; },
        set: function (v) { if (!supportsSel(this)) throw DOMEx("Failed to set the 'selectionStart' property on 'HTMLInputElement': The input element's type ('" + this.type + "') does not support selection.", 'InvalidStateError'); var s = getSel(this); v = Math.max(0, v | 0); setSel(this, v, Math.max(v, s[1]), s[2]); }
      },
      selectionEnd: {
        get: function () { return supportsSel(this) ? getSel(this)[1] : null; },
        set: function (v) { if (!supportsSel(this)) throw DOMEx("Failed to set the 'selectionEnd' property on 'HTMLInputElement': The input element's type ('" + this.type + "') does not support selection.", 'InvalidStateError'); var s = getSel(this); setSel(this, Math.min(s[0], Math.max(0, v | 0)), Math.max(0, v | 0), s[2]); }
      },
      selectionDirection: {
        get: function () { if (!supportsSel(this)) return null; var d = getSel(this)[2]; return d < 0 ? 'backward' : d > 0 ? 'forward' : 'none'; },
        set: function (v) { var s = getSel(this); setSel(this, s[0], s[1], dirNum(String(v))); }
      }
    };
    Object.keys(ss).forEach(function (k) { ss[k].enumerable = true; ss[k].configurable = true; Object.defineProperty(proto, k, ss[k]); });
    function def(k, fn) { Object.defineProperty(proto, k, { value: fn, writable: true, configurable: true, enumerable: true }); }
    def('setSelectionRange', function (s, e, dir) {
      if (!supportsSel(this)) throw DOMEx("Failed to execute 'setSelectionRange' on 'HTMLInputElement': The input element's type ('" + this.type + "') does not support selection.", 'InvalidStateError');
      s = Math.max(0, s >>> 0); e = Math.max(0, e >>> 0); if (e < s) s = e;
      setSel(this, s, e, dirNum(dir === undefined ? 'none' : String(dir)));
    });
    def('select', function () { if (!isTextCtl(this)) return; var len = this.value.length; sw.ctlSelSet(idOf(this), 0, len, 0); ensureVisible(this); scheduleSelChange(); queueSelectEvent(this); });
    def('setRangeText', function (text, start, end, mode) {
      text = String(text);
      var s = getSel(this), v = this.value;
      if (start === undefined) { start = s[0]; end = s[1]; mode = 'preserve'; } else { if (end === undefined) end = start; start = start >>> 0; end = end >>> 0; if (start > end) throw DOMEx("Failed to execute 'setRangeText' on 'HTMLInputElement': The provided start value (" + start + ") is greater than the provided end value (" + end + ').', 'IndexSizeError'); start = Math.min(start, v.length); end = Math.min(end, v.length); }
      mode = mode || 'preserve';
      var nv = v.slice(0, start) + text + v.slice(end), ins = start + text.length;
      var oldS = s[0], oldE = s[1];
      sw.setValue(idOf(this), nv);
      var ns, ne;
      switch (mode) {
        case 'select': ns = start; ne = ins; break;
        case 'start': ns = ne = start; break;
        case 'end': ns = ne = ins; break;
        default: // preserve
          var d = ins - end;
          ns = oldS > end ? oldS + d : (oldS > start ? start : oldS);
          ne = oldE > end ? oldE + d : (oldE > start ? ins : oldE);
      }
      sw.ctlSelSet(idOf(this), ns, ne, 0);
      ensureVisible(this);
    });
  }
  ctlProps(global.HTMLInputElement.prototype);
  ctlProps(global.HTMLTextAreaElement.prototype);
  // setting the value moves the cursor to the end
  [global.HTMLInputElement.prototype, global.HTMLTextAreaElement.prototype].forEach(function (proto) {
    var d = Object.getOwnPropertyDescriptor(proto, 'value');
    if (!d || !d.set) return;
    Object.defineProperty(proto, 'value', {
      get: d.get, enumerable: true, configurable: true,
      set: function (v) {
        var old = d.get.call(this);
        d.set.call(this, v);
        if (isTextCtl(this)) { var nv = d.get.call(this); if (nv !== old) sw.ctlSelSet(idOf(this), nv.length, nv.length, 0); }
      }
    });
  });

  // keep the caret of a text control in view: the control scrolls its text
  function ensureVisible(el) {
    try {
      if (!isTextCtl(el)) return;
      var s = getSel(el), end = s[2] < 0 ? s[0] : s[1];
      var cr = sw.caretRect(idOf(el), end);
      if (!cr) return;
      var p = scrollPos(), b = el.getBoundingClientRect();
      var bl = b.left + p[0], br = b.right + p[0], bt = b.top + p[1], bb = b.bottom + p[1];
      var cur = sw.box(idOf(el)) || {}, sx = cur.sl || 0, sy = cur.st || 0;
      var nx = sx, ny = sy;
      if (el.localName === 'input') {
        if (cr[0] > br - 8) nx = sx + (cr[0] - (br - 8)); else if (cr[0] < bl + 3) nx = Math.max(0, sx - ((bl + 3) - cr[0]));
      } else {
        if (cr[1] + cr[3] > bb - 3) ny = sy + (cr[1] + cr[3] - (bb - 3)); else if (cr[1] < bt + 3) ny = Math.max(0, sy - ((bt + 3) - cr[1]));
      }
      if (nx !== sx || ny !== sy) sw.scrollSet(idOf(el), nx, ny);
    } catch (e) { /* the caret is where it is */ }
  }

  // ---- undo and redo ----------------------------------------------------------------------------------------
  var UNDO = typeof Symbol === 'function' ? Symbol('undo') : '__swUndo';
  function undoOf(host) { var u = host[UNDO]; if (!u) { u = { undo: [], redo: [], lastKey: '', lastAt: 0 }; Object.defineProperty(host, UNDO, { value: u, configurable: true }); } return u; }
  function pathFrom(host, n) { var p = []; for (; n && n !== host; n = n.parentNode) p.unshift(indexOf(n)); return n === host ? p : null; }
  function nodeAt(host, p) { var n = host; for (var i = 0; i < p.length && n; i++) n = n.childNodes[p[i]]; return n; }
  function snapshot(host) {
    if (isTextCtl(host)) { var s = getSel(host); return { c: true, v: host.value, s: s[0], e: s[1], d: s[2] }; }
    var g = goSel();
    return { c: false, html: host.innerHTML, a: g && pathFrom(host, g.an), ao: g && g.ao, f: g && pathFrom(host, g.fn), fo: g && g.fo };
  }
  function restore(host, sn) {
    if (sn.c) { sw.setValue(idOf(host), sn.v); sw.ctlSelSet(idOf(host), sn.s, sn.e, sn.d); ensureVisible(host); }
    else {
      host.innerHTML = sn.html;
      if (sn.a && sn.f) { var an = nodeAt(host, sn.a), fn = nodeAt(host, sn.f); if (an && fn) setBase(an, Math.min(sn.ao, nodeLength(an)), fn, Math.min(sn.fo, nodeLength(fn))); }
    }
  }
  // call before changing host; key coalesces consecutive edits of one kind (typing) into one undo step
  function record(host, key) {
    var u = undoOf(host), now = Date.now();
    if (key && u.lastKey === key && now - u.lastAt < 1000 && u.undo.length) { u.lastAt = now; u.redo.length = 0; return; }
    u.undo.push(snapshot(host)); if (u.undo.length > 200) u.undo.shift();
    u.redo.length = 0; u.lastKey = key || ''; u.lastAt = now;
  }
  function editingTarget() {
    var a = DOC().activeElement;
    if (a && isTextCtl(a)) return a;
    var h = editHostOf(a && a !== DOC().body ? a : null) || (goSel() ? editHostOf(goSel().fn) : null);
    return h;
  }
  function undoRedo(redo) {
    var host = editingTarget(); if (!host) return false;
    var u = undoOf(host), from = redo ? u.redo : u.undo, to = redo ? u.undo : u.redo;
    if (!from.length) return false;
    var cur = snapshot(host), sn = from.pop();
    if (!fireBeforeInput(host, redo ? 'historyRedo' : 'historyUndo', null)) { from.push(sn); return true; }
    to.push(cur); u.lastKey = '';
    restore(host, sn);
    fireInput(host, redo ? 'historyRedo' : 'historyUndo', null);
    return true;
  }

  // ---- editing ------------------------------------------------------------------------------------------------
  var BLOCKS = /^(p|div|li|h[1-6]|blockquote|pre|td|th|dd|dt|section|article)$/;
  function editHostOf(n) {
    var host = null;
    for (; n; n = n.parentNode) {
      if (n.nodeType !== 1) continue;
      var v = n.getAttribute && n.getAttribute('contenteditable');
      if (v === null || v === undefined) continue;
      v = String(v).toLowerCase();
      if (v === '' || v === 'true' || v === 'plaintext-only') host = n; else if (v === 'false') return host;
    }
    return host;
  }
  function inputEv(type, init) { try { return new global.InputEvent(type, init); } catch (e) { return new global.Event(type, init); } }
  function fireBeforeInput(el, inputType, data, dt) {
    return el.dispatchEvent(inputEv('beforeinput', { bubbles: true, cancelable: true, inputType: inputType, data: data === undefined ? null : data, dataTransfer: dt || null }));
  }
  function fireInput(el, inputType, data) {
    el.dispatchEvent(inputEv('input', { bubbles: true, cancelable: false, inputType: inputType, data: data === undefined ? null : data }));
  }

  // replace the characters [s, e) of a text control's value with str; the caret goes after it
  function ctlReplace(el, s, e, str, inputType, key) {
    var v = el.value;
    if (el.localName === 'input') str = str.replace(/[\r\n]+/g, ' ');
    var nv = v.slice(0, s) + str + v.slice(e);
    if (el.localName === 'input' && el.getAttribute('type') === 'number' && nv !== '' && !/^-?\d*\.?\d*(e-?\d*)?$/i.test(nv)) return false;
    var ml = el.getAttribute('maxlength');
    if (ml !== null && ml !== '' && +ml >= 0 && nv.length > +ml) { str = str.slice(0, Math.max(0, +ml - (v.length - (e - s)))); nv = v.slice(0, s) + str + v.slice(e); if (nv === v) return false; }
    if (!fireBeforeInput(el, inputType, inputType.indexOf('delete') === 0 ? null : str)) return false;
    record(el, key);
    sw.setValue(idOf(el), nv);
    var caret = s + str.length;
    sw.ctlSelSet(idOf(el), caret, caret, 0);
    ensureVisible(el);
    scheduleSelChange();
    fireInput(el, inputType, inputType.indexOf('delete') === 0 ? null : str);
    return true;
  }

  function selectedCtlRange(el) { var s = getSel(el); return [s[0], s[1]]; }

  // text typed, pasted or inserted by a command at the selection of the editing target
  function insertText(str, inputType, key) {
    var el = DOC().activeElement;
    if (el && isTextCtl(el)) { var r = selectedCtlRange(el); return ctlReplace(el, r[0], r[1], str, inputType || 'insertText', key === undefined ? 'type' : key); }
    var host = editHostOf(el && el !== DOC().body ? el : null) || (goSel() ? editHostOf(goSel().fn) : null);
    if (!host) return false;
    return hostInsert(host, str, inputType || 'insertText', key === undefined ? 'type' : key);
  }
  function selRangeIn(host) {
    var r = range();
    if (!r || !isInclusiveAncestor(host, r._sc) || !isInclusiveAncestor(host, r._ec)) {
      // no selection in the host: the caret goes to its end
      var rr = new Range(); var last = host; while (last.lastChild && last.lastChild.nodeType !== 1) last = last.lastChild;
      if (last.nodeType === 3) rr._set(last, last.data.length, last, last.data.length); else rr._set(host, host.childNodes.length, host, host.childNodes.length);
      return rr;
    }
    return r;
  }
  function blockOf(n, host) {
    var b = n.nodeType === 1 ? n : n.parentNode;
    while (b && b !== host && !BLOCKS.test(b.localName)) b = b.parentNode;
    return b && b !== host ? b : null;
  }
  // delete the range's contents and, when it started in one block and ended in another, join what is left of the
  // two into the first
  function deleteAcrossBlocks(host, r) {
    var b1 = blockOf(r._sc, host), b2 = blockOf(r._ec, host), sc = r._sc, so = r._so;
    r.deleteContents();
    // the caret stays where the deletion began (the range itself collapses to the nearest boundary in the tree)
    if (sc.parentNode || sc === host) set(r, sc, Math.min(so, nodeLength(sc)), sc, Math.min(so, nodeLength(sc)));
    if (b1 && b2 && b1 !== b2 && b1.parentNode && b2.parentNode) {
      while (b2.firstChild) {
        var k = b2.firstChild;
        if (k.nodeType === 1 && k.localName === 'br' && !k.nextSibling) { b2.removeChild(k); break; }
        b1.appendChild(k);
      }
      b2.parentNode.removeChild(b2);
    }
  }
  function hostInsert(host, str, inputType, key) {
    if (!fireBeforeInput(host, inputType, str)) return false;
    record(host, key);
    var r = selRangeIn(host);
    if (!r.collapsed) { deleteAcrossBlocks(host, r); setBase(r._sc, r._so, r._sc, r._so); }
    var lines = str.split(/\r\n|\r|\n/);
    for (var i = 0; i < lines.length; i++) {
      if (i > 0) breakLine(host);
      if (lines[i]) insertTextNode(host, lines[i]);
    }
    scrollToCaret();
    fireInput(host, inputType, str);
    return true;
  }
  function insertTextNode(host, text) {
    var g = goSel(), n, o;
    if (g && isInclusiveAncestor(host, g.fn)) { n = g.fn; o = g.fo; } else { var rr = selRangeIn(host); n = rr._sc; o = rr._so; }
    if (n.nodeType === 3) { n.insertData(o, text); setBase(n, o + text.length, n, o + text.length); return; }
    var t = DOC().createTextNode(text);
    var ref = n.childNodes[o] || null;
    // join the text node next to the caret
    var prev = ref ? ref.previousSibling : n.lastChild;
    if (prev && prev.nodeType === 3) { prev.appendData(text); setBase(prev, prev.data.length, prev, prev.data.length); return; }
    n.insertBefore(t, ref);
    setBase(t, text.length, t, text.length);
  }
  function breakLine(host) {
    var g = goSel(); if (!g) return;
    var n = g.fn, o = g.fo;
    var blk = n.nodeType === 1 ? n : n.parentNode;
    while (blk && blk !== host && !BLOCKS.test(blk.localName)) blk = blk.parentNode;
    if (!blk || blk === host || host.getAttribute('contenteditable') === 'plaintext-only' || blk.localName === 'pre') {
      // no paragraph to split: a line break
      var br = DOC().createElement('br');
      var r = new Range(); r._set(n, o, n, o);
      r.insertNode(br);
      var next = br.nextSibling;
      // a trailing <br> needs another one to show the empty line
      if (!next || (next.nodeType === 1 && false)) { var br2 = DOC().createElement('br'); br.parentNode.insertBefore(br2, null); }
      setBase(br.parentNode, indexOf(br) + 1, br.parentNode, indexOf(br) + 1);
      return;
    }
    // split the block at the caret: the content after the caret moves to a new block of the same kind
    var tail = new Range(); tail._set(n, o, blk, blk.childNodes.length);
    var rest = tail.extractContents();
    var nb = blk.cloneNode(false);
    nb.appendChild(rest);
    if (!nb.firstChild || (nb.textContent === '' && !nb.querySelector('img,br'))) nb.appendChild(DOC().createElement('br'));
    if (!blk.firstChild) blk.appendChild(DOC().createElement('br'));
    blk.parentNode.insertBefore(nb, blk.nextSibling);
    var first = nb; while (first.firstChild && first.firstChild.nodeType !== 1 || (first.firstChild && first.firstChild.localName !== 'br' && first.firstChild.nodeType === 1)) first = first.firstChild;
    if (first.nodeType === 3) setBase(first, 0, first, 0); else setBase(first, 0, first, 0);
  }
  function scrollToCaret() {
    try {
      var g = goSel(); if (!g) return;
      var cr = sw.caretRect(idOf(g.fn), g.fo);
      if (!cr) return;
      var p = scrollPos(), vp = global.innerHeight, top = cr[1] - p[1];
      if (top < 0 || top + cr[3] > vp) global.scrollBy({ top: top < 0 ? top - 16 : top + cr[3] - vp + 16, behavior: 'instant' });
    } catch (e) { /* stays where it is */ }
  }

  // delete backward/forward by character or word, or the selection
  function deleteContent(forward, word) {
    var el = DOC().activeElement;
    if (el && isTextCtl(el)) {
      var r = selectedCtlRange(el), s = r[0], e = r[1];
      var type = forward ? (word ? 'deleteWordForward' : 'deleteContentForward') : (word ? 'deleteWordBackward' : 'deleteContentBackward');
      if (s === e) {
        var m = sw.caretMove(idOf(el), s, forward ? (word ? 'wordright' : 'right') : (word ? 'wordleft' : 'left'));
        if (!m) return false;
        var t = m[1];
        if (forward) e = Math.max(e, t); else s = Math.min(s, t);
        if (s === e) return false;
      } else type = 'deleteContentBackward';
      return ctlReplace(el, s, e, '', type, '');
    }
    var host = editHostOf(el && el !== DOC().body ? el : null) || (goSel() ? editHostOf(goSel().fn) : null);
    if (!host) return false;
    var rg = selRangeIn(host), type2 = forward ? 'deleteContentForward' : 'deleteContentBackward';
    if (rg.collapsed) {
      var m2 = sw.caretMove(idOf(rg._sc), rg._so, forward ? (word ? 'wordright' : 'right') : (word ? 'wordleft' : 'left'));
      var nr = new Range();
      if (m2) {
        var mn = wrapId(m2[0]);
        if (!isInclusiveAncestor(host, mn)) return false;
        if (forward) nr._set(rg._sc, rg._so, mn, m2[1]); else nr._set(mn, m2[1], rg._sc, rg._so);
        if (comparePoints(nr._sc, nr._so, nr._ec, nr._eo) > 0) nr._set(nr._ec, nr._eo, nr._sc, nr._so);
      } else {
        // at the edge of the text: a line break or block boundary next to the caret
        var sib = forward ? (rg._sc.nodeType === 1 ? rg._sc.childNodes[rg._so] : null) : (rg._sc.nodeType === 1 ? rg._sc.childNodes[rg._so - 1] : null);
        if (!sib) return false;
        nr._set(rg._sc, forward ? rg._so : rg._so - 1, rg._sc, forward ? rg._so + 1 : rg._so);
      }
      if (nr.collapsed) return false;
      rg = nr;
    }
    if (!fireBeforeInput(host, type2, null)) return false;
    record(host, '');
    deleteAcrossBlocks(host, rg);
    var n0 = rg._sc, o0 = rg._so;
    setBase(n0, o0, n0, o0);
    fireInput(host, type2, null);
    return true;
  }

  // Enter in an editable place
  function insertParagraph() {
    var el = DOC().activeElement;
    if (el && isTextCtl(el)) {
      if (el.localName !== 'textarea') return false;
      var r = selectedCtlRange(el); return ctlReplace(el, r[0], r[1], '\n', 'insertLineBreak', 'type');
    }
    var host = editHostOf(el && el !== DOC().body ? el : null) || (goSel() ? editHostOf(goSel().fn) : null);
    if (!host) return false;
    if (!fireBeforeInput(host, 'insertParagraph', null)) return false;
    record(host, '');
    var rg = selRangeIn(host);
    if (!rg.collapsed) { deleteAcrossBlocks(host, rg); }
    var g = goSel();
    if (!g) setBase(rg._sc, rg._so, rg._sc, rg._so);
    breakLine(host);
    scrollToCaret();
    fireInput(host, 'insertParagraph', null);
    return true;
  }

  // ---- keyboard defaults ---------------------------------------------------------------------------------
  var NAV = { ArrowLeft: 1, ArrowRight: 1, ArrowUp: 1, ArrowDown: 1, Home: 1, End: 1, PageUp: 1, PageDown: 1 };
  function moveDir(key, mods) {
    var word = mods.ctrlKey || mods.altKey && !/Mac/.test(global.navigator.platform || '');
    switch (key) {
      case 'ArrowLeft': return word ? 'wordleft' : 'left';
      case 'ArrowRight': return word ? 'wordright' : 'right';
      case 'ArrowUp': return 'up';
      case 'ArrowDown': return 'down';
      case 'Home': return mods.ctrlKey ? 'docstart' : 'linestart';
      case 'End': return mods.ctrlKey ? 'docend' : 'lineend';
      case 'PageUp': return 'pageup';
      default: return 'pagedown';
    }
  }
  // returns true when the key moved the caret or the selection
  function navigate(el, key, mods) {
    var dir = moveDir(key, mods), shift = !!mods.shiftKey;
    if (isTextCtl(el)) {
      var s = getSel(el), fwdSel = s[2] >= 0;
      var anchor = fwdSel ? s[0] : s[1], focus = fwdSel ? s[1] : s[0];
      if (!shift && s[0] !== s[1] && (dir === 'left' || dir === 'right')) {
        var p = dir === 'left' ? s[0] : s[1]; setSel(el, p, p, 0, true); return true;
      }
      if (el.localName === 'input' && (dir === 'up' || dir === 'pageup')) dir = 'linestart';
      if (el.localName === 'input' && (dir === 'down' || dir === 'pagedown')) dir = 'lineend';
      var m = sw.caretMove(idOf(el), focus, dir);
      var nf = m ? m[1] : (dir === 'up' || dir === 'pageup' ? 0 : dir === 'down' || dir === 'pagedown' ? el.value.length : focus);
      if (shift) { var a = Math.min(anchor, nf), b = Math.max(anchor, nf); setSel(el, a, b, nf < anchor ? -1 : 1, true); }
      else setSel(el, nf, nf, 0, true);
      return true;
    }
    var g = goSel();
    var host = editHostOf(el && el !== DOC().body ? el : null);
    if (!g || (!host && !shift)) return false;
    if (!shift && (g.an !== g.fn || g.ao !== g.fo) && (dir === 'left' || dir === 'right')) {
      var r = range();
      if (dir === 'left') setBase(r._sc, r._so, r._sc, r._so); else setBase(r._ec, r._eo, r._ec, r._eo);
      return true;
    }
    var mv = sw.caretMove(idOf(g.fn), g.fo, dir);
    if (!mv) return !!host;
    var nn = wrapId(mv[0]);
    if (host && !isInclusiveAncestor(host, nn)) return true;
    if (shift) setBase(g.an, g.ao, nn, mv[1]); else setBase(nn, mv[1], nn, mv[1]);
    scrollToCaret();
    return true;
  }

  // the default action of a key press, run after keydown and keypress when the page did not cancel them. Returns
  // true when the key was used.
  global.__swKeyDefault = function (el, key, mods) {
    var editable = isTextCtl(el) || editHostOf(el && el !== DOC().body ? el : null) !== null;
    var host = !isTextCtl(el) && editHostOf(el && el !== DOC().body ? el : null);
    if (!editable && !(NAV[key] && mods.shiftKey && goSel())) return false;
    if (!editable) return navigate(el, key, mods);
    if (NAV[key]) { return navigate(el, key, mods); }
    if (key.length === 1 && !(mods.ctrlKey || mods.metaKey) || (key.length === 1 && mods.ctrlKey && mods.altKey)) return insertText(key, 'insertText', 'type');
    switch (key) {
      case 'Backspace': return deleteContent(false, mods.ctrlKey || mods.altKey);
      case 'Delete': return mods.shiftKey ? false : deleteContent(true, mods.ctrlKey);
      case 'Enter': return insertParagraph();
      case 'Tab': return false;
    }
    return false;
  };

  // ---- clipboard and commands ---------------------------------------------------------------------------------
  // the elements, tags and attributes that survive a paste into an editable place
  var KEEP = /^(a|abbr|b|blockquote|br|caption|cite|code|dd|del|div|dl|dt|em|h[1-6]|hr|i|img|ins|kbd|li|mark|ol|p|pre|q|s|samp|small|span|strong|sub|sup|table|tbody|td|tfoot|th|thead|tr|u|ul)$/;
  var DROP = /^(script|style|iframe|frame|frameset|object|embed|applet|link|meta|title|head|base|form|input|button|select|textarea|svg|math|template|noscript|canvas|video|audio|source|track|param|style)$/;
  function safeURL(u, img) {
    u = String(u).trim().replace(/[\u0000- ]/g, '');
    if (/^(https?:|mailto:|tel:|#|\/|\.)/i.test(u) || (!/^[a-z][a-z0-9+.-]*:/i.test(u))) return true;
    return !!img && /^data:image\/(png|gif|jpe?g|webp|bmp);/i.test(u);
  }
  function sanitize(node) {
    var kids = Array.prototype.slice.call(node.childNodes);
    kids.forEach(function (c) {
      if (c.nodeType === 3) return;
      if (c.nodeType !== 1) { node.removeChild(c); return; }
      var tag = c.localName;
      if (DROP.test(tag)) { node.removeChild(c); return; }
      sanitize(c);
      if (!KEEP.test(tag)) { while (c.firstChild) node.insertBefore(c.firstChild, c); node.removeChild(c); return; }
      c.getAttributeNames().forEach(function (name) {
        var n = name.toLowerCase(), v = c.getAttribute(name), ok = false;
        if (tag === 'a' && n === 'href') ok = safeURL(v, false);
        else if (tag === 'img' && n === 'src') ok = safeURL(v, true);
        else if (tag === 'img' && (n === 'alt' || n === 'width' || n === 'height')) ok = true;
        else if ((tag === 'td' || tag === 'th') && (n === 'colspan' || n === 'rowspan')) ok = true;
        else if (n === 'title') ok = true;
        if (!ok) c.removeAttribute(name);
      });
      if (tag === 'img' && !c.getAttribute('src')) node.removeChild(c);
      if (tag === 'a' && c.getAttribute('href')) { c.setAttribute('rel', 'noopener noreferrer'); }
    });
  }
  global.__swSanitizeHTML = function (html) {
    var t = DOC().createElement('template'); t.innerHTML = String(html);
    var f = t.content; sanitize(f); return f;
  };
  function insertFragment(host, frag, text) {
    if (!fireBeforeInput(host, 'insertFromPaste', text)) return false;
    record(host, '');
    var rg = selRangeIn(host);
    if (!rg.collapsed) { deleteAcrossBlocks(host, rg); setBase(rg._sc, rg._so, rg._sc, rg._so); }
    var last = frag.lastChild;
    rg.insertNode(frag);
    if (last) {
      var after = last; while (after.lastChild) after = after.lastChild;
      if (after.nodeType === 3) setBase(after, after.data.length, after, after.data.length);
      else setBase(last.parentNode, indexOf(last) + 1, last.parentNode, indexOf(last) + 1);
    }
    scrollToCaret();
    fireInput(host, 'insertFromPaste', text);
    return true;
  }
  // paste: html and text for an editing host, text for a control
  global.__swPasteInto = function (html, text) {
    var el = DOC().activeElement;
    if (el && isTextCtl(el)) { var r = selectedCtlRange(el); return ctlReplace(el, r[0], r[1], text || '', 'insertFromPaste', ''); }
    var host = editHostOf(el && el !== DOC().body ? el : null) || (goSel() ? editHostOf(goSel().fn) : null);
    if (!host) return false;
    if (html && host.getAttribute('contenteditable') !== 'plaintext-only') {
      var f = global.__swSanitizeHTML(html);
      if (f.childNodes.length) return insertFragment(host, f, text || '');
    }
    return hostInsert(host, text || '', 'insertFromPaste', '');
  };
  // cut: remove the selection from the editing place after the page let the copy go through
  global.__swCutSelection = function () {
    var el = DOC().activeElement;
    if (el && isTextCtl(el)) { var r = selectedCtlRange(el); if (r[0] === r[1]) return false; return ctlReplace(el, r[0], r[1], '', 'deleteByCut', ''); }
    var host = editHostOf(el && el !== DOC().body ? el : null) || (goSel() ? editHostOf(goSel().fn) : null);
    if (!host) return false;
    var rg = range(); if (!rg || rg.collapsed || !isInclusiveAncestor(host, rg._sc)) return false;
    if (!fireBeforeInput(host, 'deleteByCut', null)) return false;
    record(host, '');
    deleteAcrossBlocks(host, rg); setBase(rg._sc, rg._so, rg._sc, rg._so);
    fireInput(host, 'deleteByCut', null);
    return true;
  };
  global.__swInsertText = function (text, kind) { return insertText(String(text), kind || 'insertText', 'type'); };
  global.__swUndo = function (redo) { return undoRedo(!!redo); };
  // what the context menu offers: can this editing host undo / redo
  global.__swUndoState = function (el) { var h = isTextCtl(el) ? el : editHostOf(el); if (!h) return { u: false, r: false }; var u = undoOf(h); return { u: u.undo.length > 0, r: u.redo.length > 0 }; };
  global.__swDeleteSel = function () { return deleteContent(false, false); };
  global.__swSelectAll = function () { sw.selAll(); scheduleSelChange(); return true; };
  global.__swEditable = function (el) { return isTextCtl(el) || editHostOf(el) !== null; };

  var CMDS = { copy: 1, cut: 1, paste: 1, selectall: 1, insertText: 1, delete: 1, forwarddelete: 1, undo: 1, redo: 1, inserthtml: 1, insertparagraph: 1, insertlinebreak: 1, bold: 1, italic: 1, underline: 1, strikethrough: 1, removeformat: 1, unselect: 1 };
  function norm(c) { return String(c).toLowerCase(); }
  docMethod('queryCommandSupported', function (c) { return !!CMDS[norm(c)] || !!CMDS[c]; });
  docMethod('queryCommandEnabled', function (c) {
    c = norm(c);
    var r = range();
    if (c === 'copy') return !!r && !r.collapsed || !!(ctlFocused() && getSel(ctlFocused())[0] !== getSel(ctlFocused())[1]);
    if (c === 'selectall') return true;
    return !!editingTarget() && !!CMDS[c];
  });
  docMethod('queryCommandState', function (c) {
    c = norm(c);
    var tag = { bold: /^(b|strong)$/, italic: /^(i|em)$/, underline: /^u$/, strikethrough: /^(s|strike|del)$/ }[c];
    var g = goSel(); if (!tag || !g) return false;
    for (var n = g.fn; n; n = n.parentNode) if (n.nodeType === 1 && tag.test(n.localName)) return true;
    return false;
  });
  docMethod('queryCommandValue', function () { return ''; });
  function wrapSelection(tag) {
    var host = editingTarget(); if (!host || isTextCtl(host)) return false;
    var rg = range(); if (!rg || rg.collapsed) return false;
    if (!fireBeforeInput(host, 'format' + tag.toUpperCase(), null)) return false;
    record(host, '');
    var already = DOC().queryCommandState({ b: 'bold', i: 'italic', u: 'underline', s: 'strikethrough' }[tag]);
    var frag = rg.extractContents();
    if (already) {
      // unwrap: drop the formatting tags inside the extracted part
      var re = tag === 'b' ? /^(b|strong)$/ : tag === 'i' ? /^(i|em)$/ : tag === 's' ? /^(s|strike|del)$/ : /^u$/;
      (function un(n) { Array.prototype.slice.call(n.childNodes).forEach(function (c) { if (c.nodeType === 1) { un(c); if (re.test(c.localName)) { while (c.firstChild) n.insertBefore(c.firstChild, c); n.removeChild(c); } } }); })(frag);
      var f2 = frag; var first = f2.firstChild, last = f2.lastChild;
      rg.insertNode(f2);
    } else {
      var w = DOC().createElement(tag); w.appendChild(frag); rg.insertNode(w);
      setBase(w, 0, w, w.childNodes.length);
    }
    fireInput(host, 'format' + tag.toUpperCase(), null);
    return true;
  }
  docMethod('execCommand', function (cmd, ui, value) {
    var c = norm(cmd);
    switch (c) {
      case 'copy': case 'cut': {
        var ok = typeof global.__sweefCopyCommand === 'function' ? global.__sweefCopyCommand(c) : false;
        return ok;
      }
      case 'paste': return false;
      case 'selectall': global.__swSelectAll(); return true;
      case 'unselect': M.removeAllRanges(); return true;
      case 'inserttext': return insertText(String(value), 'insertText', '');
      case 'inserthtml': {
        var host = editingTarget(); if (!host || isTextCtl(host)) return false;
        return insertFragment(host, global.__swSanitizeHTML(String(value)), '');
      }
      case 'delete': return deleteContent(false, false);
      case 'forwarddelete': return deleteContent(true, false);
      case 'insertparagraph': return insertParagraph();
      case 'insertlinebreak': return insertText('\n', 'insertLineBreak', '');
      case 'undo': return undoRedo(false);
      case 'redo': return undoRedo(true);
      case 'bold': return wrapSelection('b');
      case 'italic': return wrapSelection('i');
      case 'underline': return wrapSelection('u');
      case 'strikethrough': return wrapSelection('s');
    }
    return false;
  });
})(globalThis);
