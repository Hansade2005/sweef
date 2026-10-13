/*
 * XML documents for DOMParser ('text/xml', 'application/xml' and other +xml types except XHTML and SVG, which
 * stay on the HTML tree) and XMLHttpRequest.responseXML. Streaming players read their manifests with them
 * (DASH MPD, smooth streaming, TTML): tag names keep their case, names are namespace aware, malformed input
 * gives a document that contains a <parsererror> element, as Chromium does.
 *
 * The documents are standalone JavaScript trees that implement the read side of the DOM those players use
 * (they are not nodes of the page and cannot be inserted into it).
 */
(function (global) {
  'use strict';
  if (!global.DOMParser || global.__xmlDocs) return;
  Object.defineProperty(global, '__xmlDocs', { value: true });

  var XMLNS = 'http://www.w3.org/2000/xmlns/', XML_NS = 'http://www.w3.org/XML/1998/namespace', XHTML = 'http://www.w3.org/1999/xhtml';
  function def(o, name, get, set) { Object.defineProperty(o, name, { get: get, set: set, configurable: true, enumerable: true }); }
  function method(o, name, fn) { Object.defineProperty(o, name, { value: fn, writable: true, configurable: true, enumerable: true }); }

  // ---- nodes
  function XNode() { throw new TypeError('Illegal constructor'); }
  function init(n, type, doc) { Object.defineProperty(n, '_p', { value: { type: type, parent: null, kids: [], doc: doc }, writable: true }); }
  def(XNode.prototype, 'nodeType', function () { return this._p.type; });
  def(XNode.prototype, 'parentNode', function () { return this._p.parent; });
  def(XNode.prototype, 'parentElement', function () { var p = this._p.parent; return p && p._p.type === 1 ? p : null; });
  def(XNode.prototype, 'ownerDocument', function () { return this._p.type === 9 ? null : this._p.doc; });
  def(XNode.prototype, 'childNodes', function () { return this._p.kids.slice(); });
  def(XNode.prototype, 'firstChild', function () { return this._p.kids[0] || null; });
  def(XNode.prototype, 'lastChild', function () { var k = this._p.kids; return k[k.length - 1] || null; });
  function sibling(n, d) { var p = n._p.parent; if (!p) return null; var k = p._p.kids, i = k.indexOf(n); return k[i + d] || null; }
  def(XNode.prototype, 'nextSibling', function () { return sibling(this, 1); });
  def(XNode.prototype, 'previousSibling', function () { return sibling(this, -1); });
  def(XNode.prototype, 'isConnected', function () { var n = this; while (n._p.parent) n = n._p.parent; return n._p.type === 9; });
  def(XNode.prototype, 'baseURI', function () { return ''; });
  method(XNode.prototype, 'hasChildNodes', function () { return this._p.kids.length > 0; });
  method(XNode.prototype, 'contains', function (o) { for (var n = o; n; n = n._p && n._p.parent) if (n === this) return true; return false; });
  method(XNode.prototype, 'getRootNode', function () { var n = this; while (n._p.parent) n = n._p.parent; return n; });
  function text(n) {
    var t = n._p.type;
    if (t === 3 || t === 4) return n._p.data;
    if (t === 1 || t === 9 || t === 11) { if (t === 9) return null; return n._p.kids.map(function (k) { return k._p.type === 3 || k._p.type === 4 || k._p.type === 1 ? text(k) : ''; }).join(''); }
    return n._p.data;
  }
  def(XNode.prototype, 'textContent', function () { return text(this); }, function (v) {
    var t = this._p.type;
    if (t === 1) { this._p.kids.forEach(function (k) { k._p.parent = null; }); this._p.kids = []; if (v !== '' && v != null) { var tn = mk(Text, 3, this._p.doc); tn._p.data = String(v); tn._p.parent = this; this._p.kids.push(tn); } }
    else if (t !== 9) this._p.data = String(v);
  });
  method(XNode.prototype, 'cloneNode', function (deep) { return clone(this, !!deep, this._p.doc); });
  function clone(n, deep, doc) {
    var t = n._p.type, c;
    if (t === 1) { c = mk(Element, 1, doc); c._p.name = n._p.name; c._p.local = n._p.local; c._p.prefix = n._p.prefix; c._p.ns = n._p.ns; c._p.attrs = n._p.attrs.map(function (a) { return mkAttr(a._p.name, a._p.local, a._p.prefix, a._p.ns, a._p.value, c); }); }
    else { c = mk(t === 3 ? Text : t === 4 ? CDATA : t === 8 ? Comment : PI, t, doc); c._p.data = n._p.data; c._p.target = n._p.target; }
    if (deep) n._p.kids.forEach(function (k) { var kc = clone(k, true, doc); kc._p.parent = c; c._p.kids.push(kc); });
    return c;
  }
  var NODE_CONSTS = { ELEMENT_NODE: 1, ATTRIBUTE_NODE: 2, TEXT_NODE: 3, CDATA_SECTION_NODE: 4, PROCESSING_INSTRUCTION_NODE: 7, COMMENT_NODE: 8, DOCUMENT_NODE: 9 };
  Object.keys(NODE_CONSTS).forEach(function (k) { Object.defineProperty(XNode.prototype, k, { value: NODE_CONSTS[k], enumerable: true }); });

  function mk(C, type, doc) { var n = Object.create(C.prototype); init(n, type, doc); return n; }
  function sub(name, parent) { var C = function () { throw new TypeError('Illegal constructor'); }; C.prototype = Object.create(parent.prototype, { constructor: { value: C, writable: true, configurable: true } }); Object.defineProperty(C.prototype, Symbol.toStringTag, { value: name, configurable: true }); return C; }
  var Text = sub('Text', XNode), CDATA = sub('CDATASection', XNode), Comment = sub('Comment', XNode), PI = sub('ProcessingInstruction', XNode);
  [Text, CDATA, Comment, PI].forEach(function (C) {
    def(C.prototype, 'data', function () { return this._p.data; }, function (v) { this._p.data = String(v); });
    def(C.prototype, 'nodeValue', function () { return this._p.data; }, function (v) { this._p.data = String(v); });
    def(C.prototype, 'length', function () { return this._p.data.length; });
  });
  def(Text.prototype, 'nodeName', function () { return '#text'; });
  def(CDATA.prototype, 'nodeName', function () { return '#cdata-section'; });
  def(Comment.prototype, 'nodeName', function () { return '#comment'; });
  def(PI.prototype, 'nodeName', function () { return this._p.target; });
  def(PI.prototype, 'target', function () { return this._p.target; });
  def(Text.prototype, 'wholeText', function () { return this._p.data; });

  var Attr = sub('Attr', XNode);
  function mkAttr(name, local, prefix, ns, value, owner) { var a = mk(Attr, 2, null); a._p.name = name; a._p.local = local; a._p.prefix = prefix; a._p.ns = ns; a._p.value = value; a._p.owner = owner; return a; }
  def(Attr.prototype, 'name', function () { return this._p.name; });
  def(Attr.prototype, 'nodeName', function () { return this._p.name; });
  def(Attr.prototype, 'localName', function () { return this._p.local; });
  def(Attr.prototype, 'prefix', function () { return this._p.prefix; });
  def(Attr.prototype, 'namespaceURI', function () { return this._p.ns; });
  def(Attr.prototype, 'value', function () { return this._p.value; }, function (v) { this._p.value = String(v); });
  def(Attr.prototype, 'nodeValue', function () { return this._p.value; }, function (v) { this._p.value = String(v); });
  def(Attr.prototype, 'textContent', function () { return this._p.value; });
  def(Attr.prototype, 'ownerElement', function () { return this._p.owner; });
  def(Attr.prototype, 'specified', function () { return true; });

  var Element = sub('Element', XNode);
  def(Element.prototype, 'nodeName', function () { return this._p.name; });
  def(Element.prototype, 'tagName', function () { return this._p.name; });
  def(Element.prototype, 'localName', function () { return this._p.local; });
  def(Element.prototype, 'prefix', function () { return this._p.prefix; });
  def(Element.prototype, 'namespaceURI', function () { return this._p.ns; });
  def(Element.prototype, 'nodeValue', function () { return null; });
  def(Element.prototype, 'id', function () { return this.getAttribute('id') || ''; });
  def(Element.prototype, 'className', function () { return this.getAttribute('class') || ''; });
  function NamedNodeMap(el) {
    var attrs = el._p.attrs, m = Object.create(NamedNodeMap.prototype);
    attrs.forEach(function (a, i) { m[i] = a; });
    Object.defineProperty(m, 'length', { value: attrs.length });
    return m;
  }
  NamedNodeMap.prototype = { constructor: NamedNodeMap, item: function (i) { return this[i] || null; },
    getNamedItem: function (n) { for (var i = 0; i < this.length; i++) if (this[i]._p.name === n) return this[i]; return null; },
    getNamedItemNS: function (ns, l) { for (var i = 0; i < this.length; i++) if (this[i]._p.local === l && (this[i]._p.ns || null) === (ns || null)) return this[i]; return null; } };
  NamedNodeMap.prototype[Symbol.iterator] = Array.prototype[Symbol.iterator];
  Object.defineProperty(NamedNodeMap.prototype, Symbol.toStringTag, { value: 'NamedNodeMap', configurable: true });
  def(Element.prototype, 'attributes', function () { return NamedNodeMap(this); });
  function findAttr(el, name) { var a = el._p.attrs; for (var i = 0; i < a.length; i++) if (a[i]._p.name === name) return a[i]; return null; }
  function findAttrNS(el, ns, local) { var a = el._p.attrs; for (var i = 0; i < a.length; i++) if (a[i]._p.local === local && (a[i]._p.ns || null) === (ns || null)) return a[i]; return null; }
  method(Element.prototype, 'getAttribute', function (n) { var a = findAttr(this, String(n)); return a ? a._p.value : null; });
  method(Element.prototype, 'getAttributeNS', function (ns, l) { var a = findAttrNS(this, ns, String(l)); return a ? a._p.value : null; });
  method(Element.prototype, 'hasAttribute', function (n) { return !!findAttr(this, String(n)); });
  method(Element.prototype, 'hasAttributeNS', function (ns, l) { return !!findAttrNS(this, ns, String(l)); });
  method(Element.prototype, 'hasAttributes', function () { return this._p.attrs.length > 0; });
  method(Element.prototype, 'getAttributeNode', function (n) { return findAttr(this, String(n)); });
  method(Element.prototype, 'getAttributeNames', function () { return this._p.attrs.map(function (a) { return a._p.name; }); });
  method(Element.prototype, 'setAttribute', function (n, v) {
    n = String(n); var a = findAttr(this, n);
    if (a) a._p.value = String(v);
    else { var i = n.indexOf(':'); this._p.attrs.push(mkAttr(n, i < 0 ? n : n.slice(i + 1), i < 0 ? null : n.slice(0, i), null, String(v), this)); }
  });
  method(Element.prototype, 'removeAttribute', function (n) { var a = findAttr(this, String(n)); if (a) this._p.attrs.splice(this._p.attrs.indexOf(a), 1); });
  function elems(n) { return n._p.kids.filter(function (k) { return k._p.type === 1; }); }
  def(Element.prototype, 'children', function () { return elems(this); });
  def(Element.prototype, 'firstElementChild', function () { return elems(this)[0] || null; });
  def(Element.prototype, 'lastElementChild', function () { var e = elems(this); return e[e.length - 1] || null; });
  def(Element.prototype, 'childElementCount', function () { return elems(this).length; });
  function elSibling(n, d) { var s = sibling(n, d); while (s && s._p.type !== 1) s = sibling(s, d); return s; }
  def(Element.prototype, 'nextElementSibling', function () { return elSibling(this, 1); });
  def(Element.prototype, 'previousElementSibling', function () { return elSibling(this, -1); });
  function descendants(n, out, pred) { n._p.kids.forEach(function (k) { if (k._p.type === 1) { if (pred(k)) out.push(k); descendants(k, out, pred); } }); return out; }
  function byName(root, name) { name = String(name); return descendants(root, [], function (e) { return name === '*' || e._p.name === name; }); }
  function byNameNS(root, ns, local) { return descendants(root, [], function (e) { return (local === '*' || e._p.local === local) && (ns === '*' || (e._p.ns || null) === (ns || null)); }); }
  method(Element.prototype, 'getElementsByTagName', function (n) { return byName(this, n); });
  method(Element.prototype, 'getElementsByTagNameNS', function (ns, l) { return byNameNS(this, ns, String(l)); });
  method(Element.prototype, 'getElementsByClassName', function (c) { var w = String(c).split(/\s+/).filter(Boolean); return descendants(this, [], function (e) { var have = (e.getAttribute('class') || '').split(/\s+/); return w.every(function (x) { return have.indexOf(x) >= 0; }); }); });
  // querySelector: tag names, '*', descendant (' ') and child ('>') combinators and comma lists; attribute and id/class parts are rejected
  function matcher(sel) {
    return String(sel).split(',').map(function (part) {
      part = part.trim().replace(/\s*>\s*/g, ' > ');
      var steps = part.split(/\s+/).filter(Boolean);
      if (!steps.length) throw new SyntaxError("'" + sel + "' is not a valid selector.");
      steps.forEach(function (s) { if (s !== '>' && !/^(\*|[A-Za-z_][\w.-]*|[A-Za-z_][\w.-]*\|[A-Za-z_][\w.-]*)$/.test(s)) throw new SyntaxError("'" + sel + "' is not a valid selector."); });
      return steps;
    });
  }
  function nameOk(e, s) { return s === '*' || e._p.local === s || e._p.name === s; }
  function matchesSteps(e, steps, scope) {
    var i = steps.length - 1;
    if (!nameOk(e, steps[i])) return false;
    var cur = e;
    for (i--; i >= 0; i--) {
      var child = false;
      if (steps[i] === '>') { child = true; i--; }
      var p = cur._p.parent;
      if (child) { if (!p || p._p.type !== 1 || !nameOk(p, steps[i])) return false; cur = p; continue; }
      while (p && p._p.type === 1 && !nameOk(p, steps[i])) p = p._p.parent;
      if (!p || p._p.type !== 1) return false;
      cur = p;
    }
    return true;
  }
  function qsa(root, sel) { var ms = matcher(sel); return descendants(root, [], function (e) { return ms.some(function (st) { return matchesSteps(e, st, root); }); }); }
  method(Element.prototype, 'querySelectorAll', function (s) { return qsa(this, s); });
  method(Element.prototype, 'querySelector', function (s) { return qsa(this, s)[0] || null; });
  method(Element.prototype, 'matches', function (s) { return matcher(s).some(function (st) { return matchesSteps(this, st); }, this); });
  method(Element.prototype, 'closest', function (s) { for (var e = this; e && e._p.type === 1; e = e._p.parent) if (e.matches(s)) return e; return null; });
  def(Element.prototype, 'innerHTML', function () { return this._p.kids.map(serialize).join(''); });
  def(Element.prototype, 'outerHTML', function () { return serialize(this); });

  var Doc = sub('XMLDocument', XNode);
  def(Doc.prototype, 'nodeName', function () { return '#document'; });
  def(Doc.prototype, 'nodeValue', function () { return null; });
  def(Doc.prototype, 'documentElement', function () { return elems(this)[0] || null; });
  def(Doc.prototype, 'doctype', function () { return null; });
  def(Doc.prototype, 'children', function () { return elems(this); });
  def(Doc.prototype, 'firstElementChild', function () { return elems(this)[0] || null; });
  def(Doc.prototype, 'contentType', function () { return this._p.contentType; });
  def(Doc.prototype, 'characterSet', function () { return 'UTF-8'; });
  def(Doc.prototype, 'inputEncoding', function () { return 'UTF-8'; });
  def(Doc.prototype, 'URL', function () { return 'about:blank'; });
  def(Doc.prototype, 'documentURI', function () { return 'about:blank'; });
  def(Doc.prototype, 'defaultView', function () { return null; });
  method(Doc.prototype, 'getElementsByTagName', function (n) { return byName(this, n); });
  method(Doc.prototype, 'getElementsByTagNameNS', function (ns, l) { return byNameNS(this, ns, String(l)); });
  method(Doc.prototype, 'querySelectorAll', function (s) { return qsa(this, s); });
  method(Doc.prototype, 'querySelector', function (s) { return qsa(this, s)[0] || null; });
  method(Doc.prototype, 'getElementById', function (id) { return descendants(this, [], function (e) { return e.getAttribute('id') === String(id); })[0] || null; });
  method(Doc.prototype, 'createElement', function (n) { var e = mk(Element, 1, this); e._p.name = e._p.local = String(n); e._p.prefix = null; e._p.ns = null; e._p.attrs = []; return e; });
  method(Doc.prototype, 'createElementNS', function (ns, q) { q = String(q); var i = q.indexOf(':'), e = mk(Element, 1, this); e._p.name = q; e._p.local = i < 0 ? q : q.slice(i + 1); e._p.prefix = i < 0 ? null : q.slice(0, i); e._p.ns = ns || null; e._p.attrs = []; return e; });
  method(Doc.prototype, 'createTextNode', function (d) { var t = mk(Text, 3, this); t._p.data = String(d); return t; });
  method(XNode.prototype, 'appendChild', function (c) {
    if (c._p.parent) c._p.parent.removeChild(c);
    c._p.parent = this; this._p.kids.push(c); return c;
  });
  method(XNode.prototype, 'removeChild', function (c) {
    var i = this._p.kids.indexOf(c);
    if (i < 0) throw new DOMException("Failed to execute 'removeChild' on 'Node': The node to be removed is not a child of this node.", 'NotFoundError');
    this._p.kids.splice(i, 1); c._p.parent = null; return c;
  });

  // ---- serialization
  function esc(s, attr) { s = String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;'); return attr ? s.replace(/"/g, '&quot;') : s; }
  function serialize(n) {
    switch (n._p.type) {
      case 1: {
        var s = '<' + n._p.name;
        n._p.attrs.forEach(function (a) { s += ' ' + a._p.name + '="' + esc(a._p.value, true) + '"'; });
        return n._p.kids.length ? s + '>' + n._p.kids.map(serialize).join('') + '</' + n._p.name + '>' : s + '/>';
      }
      case 3: return esc(n._p.data);
      case 4: return '<![CDATA[' + n._p.data + ']]>';
      case 8: return '<!--' + n._p.data + '-->';
      case 7: return '<?' + n._p.target + (n._p.data ? ' ' + n._p.data : '') + '?>';
      case 9: return n._p.kids.map(serialize).join('');
    }
    return '';
  }

  // ---- parser
  function XMLError(msg, pos, src) {
    var line = 1, col = 1;
    for (var i = 0; i < pos && i < src.length; i++) { if (src.charCodeAt(i) === 10) { line++; col = 1; } else col++; }
    this.message = 'error on line ' + line + ' at column ' + col + ': ' + msg;
  }
  var ENT = { lt: '<', gt: '>', amp: '&', quot: '"', apos: "'" };
  function decode(s, pos, src) {
    if (s.indexOf('&') < 0) return s;
    return s.replace(/&(#x[0-9a-fA-F]+|#[0-9]+|[A-Za-z_][\w.-]*);/g, function (m, e) {
      if (e[0] === '#') return String.fromCodePoint(e[1] === 'x' ? parseInt(e.slice(2), 16) : parseInt(e.slice(1), 10));
      if (ENT[e] === undefined) throw new XMLError('Entity \'' + e + '\' not defined', pos, src);
      return ENT[e];
    });
  }
  function parseXML(src, contentType) {
    var doc = mk(Doc, 9, null); doc._p.doc = doc; doc._p.contentType = contentType;
    var i = 0, n = src.length, stack = [], cur = doc, rootSeen = false;
    if (src.charCodeAt(0) === 0xFEFF) i = 1;
    function fail(msg, at) { throw new XMLError(msg, at === undefined ? i : at, src); }
    function nsLookup(el, prefix) {
      if (prefix === 'xml') return XML_NS;
      if (prefix === 'xmlns') return XMLNS;
      for (var e = el; e; e = e._p.parent) {
        if (!e._p.nsDecl) continue;
        var d = e._p.nsDecl[prefix || ''];
        if (d !== undefined) return d || null;
      }
      return null;
    }
    var nameRe = /[A-Za-z_:À-￿][\w:.·À-￿-]*/y;
    function readName() { nameRe.lastIndex = i; var m = nameRe.exec(src); if (!m) fail('StartTag: invalid element name'); i = nameRe.lastIndex; return m[0]; }
    function skipWs() { while (i < n && (src[i] === ' ' || src[i] === '\t' || src[i] === '\n' || src[i] === '\r')) i++; }
    function addText(t, type) {
      if (!t) return;
      if (cur === doc) { if (/\S/.test(t)) fail('Start tag expected, \'<\' not found'); return; }
      var k = cur._p.kids, last = k[k.length - 1];
      if (type === 3 && last && last._p.type === 3) { last._p.data += t; return; }
      var tn = mk(type === 4 ? CDATA : Text, type, doc); tn._p.data = t; tn._p.parent = cur; k.push(tn);
    }
    while (i < n) {
      var lt = src.indexOf('<', i);
      if (lt < 0) { addText(decode(src.slice(i), i, src), 3); i = n; break; }
      if (lt > i) addText(decode(src.slice(i, lt), i, src), 3);
      i = lt;
      if (src.startsWith('<!--', i)) {
        var e = src.indexOf('-->', i + 4); if (e < 0) fail('Comment not terminated');
        var c = mk(Comment, 8, doc); c._p.data = src.slice(i + 4, e); c._p.parent = cur; cur._p.kids.push(c); i = e + 3;
      } else if (src.startsWith('<![CDATA[', i)) {
        var e2 = src.indexOf(']]>', i + 9); if (e2 < 0) fail('CData section not finished');
        if (cur === doc) fail('Start tag expected, \'<\' not found');
        addText(src.slice(i + 9, e2), 4); i = e2 + 3;
      } else if (src.startsWith('<?', i)) {
        var e3 = src.indexOf('?>', i + 2); if (e3 < 0) fail('PI not finished');
        var body = src.slice(i + 2, e3), sp = body.search(/\s/), target = sp < 0 ? body : body.slice(0, sp);
        if (!/^[A-Za-z_:]/.test(target)) fail('xmlParsePI : invalid name');
        if (target.toLowerCase() !== 'xml') { var pi = mk(PI, 7, doc); pi._p.target = target; pi._p.data = sp < 0 ? '' : body.slice(sp).trim(); pi._p.parent = cur; cur._p.kids.push(pi); }
        i = e3 + 2;
      } else if (src.startsWith('<!', i)) {
        // DOCTYPE: skip it, including an internal subset
        var depth = 0, j = i + 2;
        for (; j < n; j++) { if (src[j] === '[') depth++; else if (src[j] === ']') depth--; else if (src[j] === '>' && depth <= 0) break; }
        if (j >= n) fail('DOCTYPE improperly terminated');
        i = j + 1;
      } else if (src[i + 1] === '/') {
        i += 2; var cn = readName(); skipWs();
        if (src[i] !== '>') fail('expected \'>\'');
        i++;
        if (cur === doc || cur._p.name !== cn) fail('Opening and ending tag mismatch: ' + (cur === doc ? 'none' : cur._p.name) + ' line ' + 0 + ' and ' + cn);
        cur = stack.pop();
      } else {
        i++;
        var qname = readName(), attrs = [], selfClose = false, decls = null;
        for (;;) {
          skipWs();
          if (i >= n) fail('Couldn\'t find end of Start Tag ' + qname);
          if (src[i] === '>') { i++; break; }
          if (src[i] === '/' && src[i + 1] === '>') { i += 2; selfClose = true; break; }
          var an = readName(); skipWs();
          if (src[i] !== '=') fail('Specification mandates value for attribute ' + an);
          i++; skipWs();
          var q = src[i]; if (q !== '"' && q !== "'") fail('AttValue: " or \' expected');
          var qe = src.indexOf(q, i + 1); if (qe < 0) fail('AttValue: \' expected');
          var raw = src.slice(i + 1, qe); if (raw.indexOf('<') >= 0) fail('Unescaped \'<\' not allowed in attributes values');
          var av = decode(raw.replace(/[\t\n\r]/g, ' '), i, src);
          i = qe + 1;
          if (attrs.some(function (a) { return a[0] === an; })) fail('Attribute ' + an + ' redefined');
          attrs.push([an, av]);
          if (an === 'xmlns') (decls || (decls = {}))[''] = av;
          else if (an.slice(0, 6) === 'xmlns:') (decls || (decls = {}))[an.slice(6)] = av;
        }
        if (cur === doc && rootSeen) fail('Extra content at the end of the document');
        var el = mk(Element, 1, doc);
        el._p.name = qname; var ci = qname.indexOf(':');
        el._p.local = ci < 0 ? qname : qname.slice(ci + 1); el._p.prefix = ci < 0 ? null : qname.slice(0, ci);
        el._p.nsDecl = decls; el._p.attrs = []; el._p.parent = cur;
        el._p.ns = nsLookup(el, el._p.prefix);
        if (el._p.prefix && !el._p.ns) fail('Namespace prefix ' + el._p.prefix + ' on ' + el._p.local + ' is not defined');
        attrs.forEach(function (a) {
          var ai = a[0].indexOf(':'), pre = ai < 0 ? null : a[0].slice(0, ai), loc = ai < 0 ? a[0] : a[0].slice(ai + 1);
          var ans = a[0] === 'xmlns' || pre === 'xmlns' ? XMLNS : pre ? nsLookup(el, pre) : null;
          if (pre && !ans) fail('Namespace prefix ' + pre + ' for ' + loc + ' on ' + qname + ' is not defined');
          el._p.attrs.push(mkAttr(a[0], loc, pre, ans, a[1], el));
        });
        cur._p.kids.push(el);
        if (cur === doc) rootSeen = true;
        if (!selfClose) { stack.push(cur); cur = el; }
      }
    }
    if (cur !== doc) fail('Premature end of data in tag ' + cur._p.name);
    if (!rootSeen) fail('Document is empty');
    return doc;
  }
  function errorDocument(msg, contentType) {
    var doc = mk(Doc, 9, null); doc._p.doc = doc; doc._p.contentType = contentType;
    var root = doc.createElementNS(XHTML, 'parsererror');
    root.textContent = msg;
    root._p.parent = doc; doc._p.kids.push(root);
    return doc;
  }

  // instanceof Element / Node / Text ... must hold for these nodes too (Shaka's findChildren filters childNodes
  // with it); the check only widens the exact global, never its subclasses (HTMLElement stays HTML)
  var origHasInstance = Function.prototype[Symbol.hasInstance];
  function widen(name, types) {
    var G = global[name];
    if (typeof G !== 'function') return;
    Object.defineProperty(G, Symbol.hasInstance, { configurable: true, writable: true, value: function (o) {
      if (origHasInstance.call(this, o)) return true;
      return this === G && !!o && typeof o === 'object' && !!o._p && types.indexOf(o._p.type) >= 0;
    } });
  }
  widen('Node', [1, 2, 3, 4, 7, 8, 9]); widen('Element', [1]); widen('CharacterData', [3, 4, 8]); widen('Text', [3, 4]);
  widen('CDATASection', [4]); widen('Comment', [8]); widen('ProcessingInstruction', [7]); widen('Attr', [2]);
  widen('Document', [9]); widen('XMLDocument', [9]);

  var XML_TYPES = /^(text\/xml|application\/xml|[a-z]+\/[a-z0-9.+-]+\+xml)$/i;
  function isXML(type) { type = String(type || '').trim().toLowerCase(); return XML_TYPES.test(type) && !/xhtml\+xml|svg\+xml/.test(type); }
  var htmlParse = global.DOMParser.prototype.parseFromString;
  Object.defineProperty(global.DOMParser.prototype, 'parseFromString', { configurable: true, writable: true, enumerable: true, value: function parseFromString(str, type) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'parseFromString' on 'DOMParser': 2 arguments required, but only " + arguments.length + ' present.');
    if (!isXML(type)) return htmlParse.call(this, str, type);
    var ct = String(type).trim().toLowerCase();
    try { return parseXML(String(str), ct); } catch (e) { if (e instanceof XMLError) return errorDocument(e.message, ct); throw e; }
  } });

  var ser = global.XMLSerializer && global.XMLSerializer.prototype.serializeToString;
  if (ser) Object.defineProperty(global.XMLSerializer.prototype, 'serializeToString', { configurable: true, writable: true, enumerable: true, value: function serializeToString(n) {
    if (n && n._p && typeof n._p.type === 'number') return serialize(n);
    return ser.call(this, n);
  } });

})(globalThis);
