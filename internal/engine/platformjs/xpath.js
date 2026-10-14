// XPath 1.0 over the live DOM: document.evaluate, XPathEvaluator, XPathExpression, XPathResult.
var sw = H.sw;
var XHTML = 'http://www.w3.org/1999/xhtml';
var XML_NS = 'http://www.w3.org/XML/1998/namespace';
function err(name, msg, code) { var e = new g.DOMException(msg, name); if (code) Object.defineProperty(e, 'code', { value: code }); return e; }
function syntax(msg) { return err('SyntaxError', "The string '" + msg + "' is not a valid XPath expression.", 51); }

// ---- lexer ------------------------------------------------------------------------
var NCSTART = 'A-Za-z_\\u00C0-\\u00D6\\u00D8-\\u00F6\\u00F8-\\u02FF\\u0370-\\u037D\\u037F-\\u1FFF\\u200C-\\u200D\\u2070-\\u218F\\u2C00-\\u2FEF\\u3001-\\uD7FF\\uF900-\\uFDCF\\uFDF0-\\uFFFD';
var NCNAME = '[' + NCSTART + '][' + NCSTART + '0-9.\\-\\u00B7\\u0300-\\u036F\\u203F-\\u2040]*';
var TOKEN = new RegExp('^(?:(\\s+)|(\\d+(?:\\.\\d*)?|\\.\\d+)|("[^"]*"|\'[^\']*\')|(\\$)(?:(' + NCNAME + '):)?(' + NCNAME + ')|(//|\\.\\.|::|!=|<=|>=|[()\\[\\].@,|+\\-=<>/*])|(' + NCNAME + ':\\*)|(' + NCNAME + '(?::' + NCNAME + ')?))');
var OPS = { '/': 1, '//': 1, '|': 1, '+': 1, '-': 1, '=': 1, '!=': 1, '<': 1, '>': 1, '<=': 1, '>=': 1 };
var OPNAMES = { and: 1, or: 1, mod: 1, div: 1 };
var AXES = { ancestor: 1, 'ancestor-or-self': 1, attribute: 1, child: 1, descendant: 1, 'descendant-or-self': 1, following: 1, 'following-sibling': 1, namespace: 1, parent: 1, preceding: 1, 'preceding-sibling': 1, self: 1 };
var NODETYPES = { comment: 1, text: 1, 'processing-instruction': 1, node: 1 };

function lex(src) {
  var toks = [], s = src, pos = 0;
  while (s.length) {
    var m = TOKEN.exec(s);
    if (!m) throw syntax(src);
    s = s.slice(m[0].length);
    if (m[1]) continue;
    var prev = toks.length ? toks[toks.length - 1] : null;
    // XPath 1.0 3.7: after a token that ends an operand, '*' and names are operators
    var operandBefore = prev && !(prev.t === 'op' || prev.v === '@' || prev.v === '::' || prev.v === '(' || prev.v === '[' || prev.v === ',');
    var tok;
    if (m[2] !== undefined) tok = { t: 'num', v: parseFloat(m[2]) };
    else if (m[3] !== undefined) tok = { t: 'lit', v: m[3].slice(1, -1) };
    else if (m[4] !== undefined) tok = { t: 'var', v: (m[5] ? m[5] + ':' : '') + m[6] };
    else if (m[7] !== undefined) {
      var p = m[7];
      if (p === '*' && operandBefore) tok = { t: 'op', v: '*' };
      else if (p === '*') tok = { t: 'name', v: '*' };
      else if (OPS[p]) tok = { t: 'op', v: p };
      else tok = { t: 'p', v: p };
    } else if (m[8] !== undefined) tok = { t: 'name', v: m[8] };
    else {
      var nm = m[9];
      if (operandBefore && OPNAMES[nm]) tok = { t: 'op', v: nm };
      else {
        var rest = s.replace(/^\s+/, '');
        if (rest.charAt(0) === '(') tok = NODETYPES[nm] ? { t: 'nodetype', v: nm } : { t: 'func', v: nm };
        else if (rest.slice(0, 2) === '::' && AXES[nm]) tok = { t: 'axis', v: nm };
        else tok = { t: 'name', v: nm };
      }
    }
    toks.push(tok);
  }
  return toks;
}

// ---- parser (to closures) ---------------------------------------------------------------
function parse(src, resolver) {
  var toks = lex(src), i = 0;
  function peek() { return toks[i]; }
  function isOp(v) { var t = toks[i]; return t && t.t === 'op' && t.v === v; }
  function isP(v) { var t = toks[i]; return t && t.t === 'p' && t.v === v; }
  function expectP(v) { if (!isP(v)) throw syntax(src); i++; }
  function ns(prefix) {
    if (prefix === 'xml') return XML_NS;
    var u = null;
    if (resolver) u = typeof resolver === 'function' ? resolver(prefix) : resolver.lookupNamespaceURI(prefix);
    if (u === null || u === undefined || u === '') throw err('NamespaceError', "The string contains an unresolvable namespace prefix '" + prefix + "'.", 14);
    return String(u);
  }
  function expr() { return orExpr(); }
  function orExpr() {
    var l = andExpr();
    while (isOp('or')) { i++; l = (function (a, b) { return function (c) { return toBool(a(c)) || toBool(b(c)); }; })(l, andExpr()); }
    return l;
  }
  function andExpr() {
    var l = eqExpr();
    while (isOp('and')) { i++; l = (function (a, b) { return function (c) { return toBool(a(c)) && toBool(b(c)); }; })(l, eqExpr()); }
    return l;
  }
  function eqExpr() {
    var l = relExpr();
    while (isOp('=') || isOp('!=')) { var o = toks[i++].v; l = (function (a, b, o) { return function (c) { return compare(o, a(c), b(c)); }; })(l, relExpr(), o); }
    return l;
  }
  function relExpr() {
    var l = addExpr();
    while (isOp('<') || isOp('>') || isOp('<=') || isOp('>=')) { var o = toks[i++].v; l = (function (a, b, o) { return function (c) { return compare(o, a(c), b(c)); }; })(l, addExpr(), o); }
    return l;
  }
  function addExpr() {
    var l = mulExpr();
    while (isOp('+') || isOp('-')) { var o = toks[i++].v; l = (function (a, b, o) { return function (c) { var x = toNum(a(c)), y = toNum(b(c)); return o === '+' ? x + y : x - y; }; })(l, mulExpr(), o); }
    return l;
  }
  function mulExpr() {
    var l = unaryExpr();
    while (isOp('*') || isOp('div') || isOp('mod')) {
      var o = toks[i++].v;
      l = (function (a, b, o) { return function (c) { var x = toNum(a(c)), y = toNum(b(c)); return o === '*' ? x * y : o === 'div' ? x / y : x % y; }; })(l, unaryExpr(), o);
    }
    return l;
  }
  function unaryExpr() {
    if (isOp('-')) { i++; var e = unaryExpr(); return function (c) { return -toNum(e(c)); }; }
    return unionExpr();
  }
  function unionExpr() {
    var l = pathExpr();
    while (isOp('|')) {
      i++;
      l = (function (a, b) { return function (c) { var x = a(c), y = b(c); if (!Array.isArray(x) || !Array.isArray(y)) throw syntax(src); return docOrder(x.concat(y)); }; })(l, pathExpr());
    }
    return l;
  }
  function startsStep() {
    var t = peek();
    return t && (t.t === 'name' || t.t === 'axis' || t.t === 'nodetype' || (t.t === 'p' && (t.v === '.' || t.v === '..' || t.v === '@')));
  }
  function pathExpr() {
    var t = peek();
    if (!t) throw syntax(src);
    var base;
    if (t.t === 'op' && (t.v === '/' || t.v === '//')) {
      i++;
      var steps = [];
      if (t.v === '//') { steps.push(descOrSelfStep()); steps.push(step()); }
      else if (startsStep()) steps.push(step());
      steps = steps.concat(moreSteps());
      return function (c) { return runSteps([rootOf(c.node)], steps, c); };
    }
    if (t.t === 'num' || t.t === 'lit' || t.t === 'var' || t.t === 'func' || (t.t === 'p' && t.v === '(')) {
      base = filterExpr();
      if (isOp('/') || isOp('//')) {
        var st = [];
        var dbl = toks[i++].v === '//';
        if (dbl) st.push(descOrSelfStep());
        st.push(step());
        st = st.concat(moreSteps());
        return function (c) { var b = base(c); if (!Array.isArray(b)) throw syntax(src); return runSteps(b, st, c); };
      }
      return base;
    }
    var first = [step()].concat(moreSteps());
    return function (c) { return runSteps([c.node], first, c); };
  }
  function moreSteps() {
    var out = [];
    while (isOp('/') || isOp('//')) {
      var dbl = toks[i++].v === '//';
      if (dbl) out.push(descOrSelfStep());
      out.push(step());
    }
    return out;
  }
  function descOrSelfStep() { return { axis: 'descendant-or-self', test: function () { return true; }, preds: [] }; }
  function step() {
    var t = peek();
    if (!t) throw syntax(src);
    if (t.t === 'p' && t.v === '.') { i++; return { axis: 'self', test: function () { return true; }, preds: [] }; }
    if (t.t === 'p' && t.v === '..') { i++; return { axis: 'parent', test: function () { return true; }, preds: [] }; }
    var axis = 'child';
    if (t.t === 'axis') { axis = t.v; i++; expectP('::'); }
    else if (t.t === 'p' && t.v === '@') { axis = 'attribute'; i++; }
    var test = nodeTest(axis);
    var preds = [];
    while (isP('[')) { i++; preds.push(expr()); expectP(']'); }
    return { axis: axis, test: test, preds: preds };
  }
  function nodeTest(axis) {
    var t = toks[i++];
    if (!t) throw syntax(src);
    if (t.t === 'nodetype') {
      expectP('(');
      var lit = null;
      if (t.v === 'processing-instruction' && peek() && peek().t === 'lit') lit = toks[i++].v;
      expectP(')');
      switch (t.v) {
        case 'node': return function () { return true; };
        case 'text': return function (n) { return n.nodeType === 3 || n.nodeType === 4; };
        case 'comment': return function (n) { return n.nodeType === 8 && !/^\?/.test(n.data); };
        default: return function (n) { return n.nodeType === 8 && /^\?/.test(n.data) && (lit === null || n.data.slice(1).split(/\s/)[0] === lit); };
      }
    }
    if (t.t !== 'name') throw syntax(src);
    var principal = axis === 'attribute' ? 2 : 1;
    var name = t.v;
    if (name === '*') return function (n) { return n.nodeType === principal; };
    var colon = name.indexOf(':');
    if (colon > 0 && name.slice(colon + 1) === '*') {
      var u = ns(name.slice(0, colon));
      return function (n) { return n.nodeType === principal && nsOf(n) === u; };
    }
    if (colon > 0) {
      var uri = ns(name.slice(0, colon)), local = name.slice(colon + 1);
      return function (n) { return n.nodeType === principal && localOf(n) === local && nsOf(n) === uri; };
    }
    // unprefixed: no namespace; HTML elements in HTML documents match case-insensitively
    var lower = name.toLowerCase();
    return function (n) {
      if (n.nodeType !== principal) return false;
      if (principal === 2) return n.name === name && (n.namespaceURI === null || n.namespaceURI === undefined);
      var u2 = n.namespaceURI;
      if (u2 === XHTML) return n.localName === lower;
      return !u2 && localOf(n) === name;
    };
  }
  function filterExpr() {
    var p = primary();
    var preds = [];
    while (isP('[')) { i++; preds.push(expr()); expectP(']'); }
    if (!preds.length) return p;
    return function (c) {
      var v = p(c);
      if (!Array.isArray(v)) throw syntax(src);
      return applyPreds(v, preds, c, false);
    };
  }
  function primary() {
    var t = toks[i++];
    if (t.t === 'num') return function () { return t.v; };
    if (t.t === 'lit') return function () { return t.v; };
    if (t.t === 'var') throw err('NamespaceError', 'XPath variables are not supported.', 14);
    if (t.t === 'p' && t.v === '(') { var e = expr(); expectP(')'); return e; }
    if (t.t === 'func') {
      expectP('(');
      var args = [];
      if (!isP(')')) {
        args.push(expr());
        while (isP(',')) { i++; args.push(expr()); }
      }
      expectP(')');
      var f = FUNCS[t.v];
      if (!f) throw err('SyntaxError', "The function '" + t.v + "' is not a known XPath function.", 51);
      if (args.length < f.min || (f.max >= 0 && args.length > f.max)) throw err('SyntaxError', "Wrong number of arguments for the XPath function '" + t.v + "'.", 51);
      return function (c) { return f.fn(c, args); };
    }
    throw syntax(src);
  }
  var e = expr();
  if (i < toks.length) throw syntax(src);
  return e;
}

// ---- value helpers ----------------------------------------------------------------------
function localOf(n) { return n.nodeType === 2 ? n.name.replace(/^.*:/, '') : (n.localName || ''); }
function nsOf(n) { return (n.namespaceURI === undefined ? null : n.namespaceURI) || null; }
function rootOf(n) {
  if (n.nodeType === 2) n = n.ownerElement;
  while (n.parentNode) n = n.parentNode;
  return n;
}
function textOf(n) {
  switch (n.nodeType) {
    case 9: return n.documentElement ? n.documentElement.textContent || '' : '';
    case 1: case 11: return n.textContent || '';
    case 2: return n.value;
    default: return n.data !== undefined ? n.data : n.nodeValue || '';
  }
}
function strVal(n) { var t = n.nodeType; if (t === 8 && /^\?/.test(n.data)) return n.data.replace(/^\?\S*\s?/, '').replace(/\?$/, ''); return textOf(n); }
function numToStr(n) {
  if (n !== n) return 'NaN';
  if (n === Infinity) return 'Infinity';
  if (n === -Infinity) return '-Infinity';
  if (n === 0) return '0';
  var s = String(n);
  if (s.indexOf('e') < 0) return s;
  var m = /^(-?)(\d)(?:\.(\d+))?e([+-]\d+)$/.exec(s);
  var sign = m[1], digits = m[2] + (m[3] || ''), exp = parseInt(m[4], 10);
  if (exp >= 0) return sign + (digits.length > exp + 1 ? digits.slice(0, exp + 1) + '.' + digits.slice(exp + 1) : digits + new Array(exp + 2 - digits.length).join('0'));
  return sign + '0.' + new Array(-exp).join('0') + digits;
}
function toStr(v) {
  if (Array.isArray(v)) return v.length ? strVal(v[0]) : '';
  if (typeof v === 'number') return numToStr(v);
  if (typeof v === 'boolean') return v ? 'true' : 'false';
  return String(v);
}
function str2num(s) { s = s.replace(/^[ \t\r\n]+|[ \t\r\n]+$/g, ''); return /^-?(\d+(\.\d*)?|\.\d+)$/.test(s) ? parseFloat(s) : NaN; }
function toNum(v) {
  if (typeof v === 'number') return v;
  if (typeof v === 'boolean') return v ? 1 : 0;
  return str2num(toStr(v));
}
function toBool(v) {
  if (Array.isArray(v)) return v.length > 0;
  if (typeof v === 'number') return v !== 0 && v === v;
  return typeof v === 'string' ? v.length > 0 : !!v;
}
function cmpPrim(op, a, b) {
  switch (op) {
    case '=': return a === b;
    case '!=': return a !== b;
    case '<': return a < b;
    case '>': return a > b;
    case '<=': return a <= b;
    default: return a >= b;
  }
}
function compare(op, a, b) {
  var an = Array.isArray(a), bn = Array.isArray(b), eq = op === '=' || op === '!=', i, j;
  if (an && bn) {
    for (i = 0; i < a.length; i++) for (j = 0; j < b.length; j++) {
      if (eq ? cmpPrim(op, strVal(a[i]), strVal(b[j])) : cmpPrim(op, str2num(strVal(a[i])), str2num(strVal(b[j])))) return true;
    }
    return false;
  }
  if (an || bn) {
    var set = an ? a : b, other = an ? b : a;
    if (typeof other === 'boolean') { var sb = set.length > 0; return an ? cmpPrim(op, sb, other) : cmpPrim(op, other, sb); }
    for (i = 0; i < set.length; i++) {
      var sv = strVal(set[i]), r;
      if (typeof other === 'number') r = an ? cmpPrim(op, str2num(sv), other) : cmpPrim(op, other, str2num(sv));
      else if (eq) r = an ? cmpPrim(op, sv, other) : cmpPrim(op, other, sv);
      else r = an ? cmpPrim(op, str2num(sv), str2num(other)) : cmpPrim(op, str2num(other), str2num(sv));
      if (r) return true;
    }
    return false;
  }
  if (eq) {
    if (typeof a === 'boolean' || typeof b === 'boolean') return cmpPrim(op, toBool(a), toBool(b));
    if (typeof a === 'number' || typeof b === 'number') return cmpPrim(op, toNum(a), toNum(b));
    return cmpPrim(op, toStr(a), toStr(b));
  }
  return cmpPrim(op, toNum(a), toNum(b));
}

// ---- document order ----------------------------------------------------------------------------
var attrCache = new WeakMap();
function attributesOf(el) {
  var map = attrCache.get(el);
  if (!map) { map = new Map(); attrCache.set(el, map); }
  var out = [], it = el.attributes;
  for (var k = 0; k < it.length; k++) {
    var a = it[k], c = map.get(a.name);
    if (!c) { c = a; map.set(a.name, c); }
    if (a.name.indexOf('xmlns') !== 0) out.push(c);
  }
  return out;
}
function childIndex(n, memo) {
  var p = n.parentNode;
  if (!p) return 0;
  var m = memo.idx.get(p);
  if (!m) { m = new Map(); var k = 0; for (var c = p.firstChild; c; c = c.nextSibling) m.set(c, k++); memo.idx.set(p, m); }
  return m.get(n) || 0;
}
function keyOf(n, memo) {
  var k = memo.keys.get(n);
  if (k) return k;
  if (n.nodeType === 2) {
    var o = n.ownerElement, ok = keyOf(o, memo), idx = attributesOf(o).indexOf(n);
    k = ok.concat([-1e9 + (idx < 0 ? 0 : idx)]);
  } else if (!n.parentNode) k = [];
  else k = keyOf(n.parentNode, memo).concat([childIndex(n, memo)]);
  memo.keys.set(n, k);
  return k;
}
function cmpKeys(a, b) {
  var n = Math.min(a.length, b.length);
  for (var i = 0; i < n; i++) if (a[i] !== b[i]) return a[i] - b[i];
  return a.length - b.length;
}
function docOrder(nodes) {
  var seen = new Set(), uniq = [];
  for (var i = 0; i < nodes.length; i++) if (!seen.has(nodes[i])) { seen.add(nodes[i]); uniq.push(nodes[i]); }
  if (uniq.length < 2) return uniq;
  var memo = { keys: new Map(), idx: new Map() };
  var keyed = uniq.map(function (n) { return { n: n, k: keyOf(n, memo) }; });
  keyed.sort(function (x, y) { return cmpKeys(x.k, y.k); });
  return keyed.map(function (x) { return x.n; });
}

// ---- axes ---------------------------------------------------------------------------------------------
function descendants(n, out) {
  for (var c = n.firstChild; c; c = c.nextSibling) { out.push(c); if (c.firstChild) descendants(c, out); }
  return out;
}
function ancestors(n) {
  var out = [], p = n.nodeType === 2 ? n.ownerElement : n.parentNode;
  while (p) { out.push(p); p = p.parentNode; }
  return out;
}
function following(n) {
  var out = [];
  if (n.nodeType === 2) { descendants(n.ownerElement, out); n = n.ownerElement; }
  for (var a = n; a; a = a.parentNode) {
    for (var s = a.nextSibling; s; s = s.nextSibling) { out.push(s); descendants(s, out); }
  }
  return out;
}
function preceding(n) {
  var out = [];
  if (n.nodeType === 2) n = n.ownerElement;
  for (var a = n; a; a = a.parentNode) {
    for (var s = a.previousSibling; s; s = s.previousSibling) {
      var sub = descendants(s, []);
      for (var k = sub.length - 1; k >= 0; k--) out.push(sub[k]);
      out.push(s);
    }
  }
  return out;
}
function axisNodes(axis, n) {
  var out, s;
  switch (axis) {
    case 'child': out = []; if (n.nodeType !== 2) for (var c = n.firstChild; c; c = c.nextSibling) out.push(c); return out;
    case 'descendant': return n.nodeType === 2 ? [] : descendants(n, []);
    case 'descendant-or-self': return [n].concat(n.nodeType === 2 ? [] : descendants(n, []));
    case 'self': return [n];
    case 'parent': return n.nodeType === 2 ? [n.ownerElement] : n.parentNode ? [n.parentNode] : [];
    case 'ancestor': return ancestors(n);
    case 'ancestor-or-self': return [n].concat(ancestors(n));
    case 'following-sibling': out = []; if (n.nodeType !== 2) for (s = n.nextSibling; s; s = s.nextSibling) out.push(s); return out;
    case 'preceding-sibling': out = []; if (n.nodeType !== 2) for (s = n.previousSibling; s; s = s.previousSibling) out.push(s); return out;
    case 'following': return following(n);
    case 'preceding': return preceding(n);
    case 'attribute': return n.nodeType === 1 ? attributesOf(n) : [];
    default: return []; // namespace axis: namespace nodes are not exposed by DOM
  }
}
var REVERSE = { ancestor: 1, 'ancestor-or-self': 1, preceding: 1, 'preceding-sibling': 1 };

function applyPreds(nodes, preds, c, reverse) {
  for (var p = 0; p < preds.length; p++) {
    var pred = preds[p], kept = [], size = nodes.length;
    for (var i = 0; i < size; i++) {
      var v = pred({ node: nodes[i], pos: i + 1, size: size, doc: c.doc });
      if (typeof v === 'number' ? v === i + 1 : toBool(v)) kept.push(nodes[i]);
    }
    nodes = kept;
  }
  return nodes;
}
function runSteps(input, steps, c) {
  var cur = input;
  for (var s = 0; s < steps.length; s++) {
    var st = steps[s], next = [], rev = !!REVERSE[st.axis];
    for (var k = 0; k < cur.length; k++) {
      var cand = axisNodes(st.axis, cur[k]).filter(st.test);
      if (st.preds.length) cand = applyPreds(cand, st.preds, c, rev);
      for (var m = 0; m < cand.length; m++) next.push(cand[m]);
    }
    cur = (cur.length > 1 || rev || st.axis === 'following' || st.axis === 'preceding' || st.axis === 'descendant-or-self' || st.axis === 'descendant') ? docOrder(next) : next;
  }
  return cur.length > 1 && steps.length === 0 ? docOrder(cur) : cur;
}

// ---- function library ---------------------------------------------------------------------------------
function ctxArg(c, args, k) { return args.length > k ? args[k](c) : [c.node]; }
function F(min, max, fn) { return { min: min, max: max, fn: fn }; }
function nameOf(n, which) {
  if (!n) return '';
  if (n.nodeType === 2) return which === 'ns' ? n.namespaceURI || '' : which === 'local' ? localOf(n) : n.name;
  if (n.nodeType !== 1) return '';
  return which === 'ns' ? n.namespaceURI || '' : which === 'local' ? n.localName : (n.prefix ? n.prefix + ':' : '') + n.localName;
}
function idRefs(c, v) {
  var out = [], ids = [];
  if (Array.isArray(v)) v.forEach(function (n) { ids = ids.concat(strVal(n).split(/\s+/)); }); else ids = toStr(v).split(/\s+/);
  var doc = c.doc;
  ids.forEach(function (id) { if (id) { var e = doc.getElementById(id); if (e) out.push(e); } });
  return docOrder(out);
}
var FUNCS = {
  last: F(0, 0, function (c) { return c.size; }),
  position: F(0, 0, function (c) { return c.pos; }),
  count: F(1, 1, function (c, a) { var v = a[0](c); if (!Array.isArray(v)) throw syntax('count'); return v.length; }),
  id: F(1, 1, function (c, a) { return idRefs(c, a[0](c)); }),
  'local-name': F(0, 1, function (c, a) { var v = ctxArg(c, a, 0); return nameOf(Array.isArray(v) ? v[0] : null, 'local'); }),
  'namespace-uri': F(0, 1, function (c, a) { var v = ctxArg(c, a, 0); return nameOf(Array.isArray(v) ? v[0] : null, 'ns'); }),
  name: F(0, 1, function (c, a) { var v = ctxArg(c, a, 0); return nameOf(Array.isArray(v) ? v[0] : null, 'name'); }),
  string: F(0, 1, function (c, a) { return a.length ? toStr(a[0](c)) : strVal(c.node); }),
  concat: F(2, -1, function (c, a) { var s = ''; for (var i = 0; i < a.length; i++) s += toStr(a[i](c)); return s; }),
  'starts-with': F(2, 2, function (c, a) { return toStr(a[0](c)).indexOf(toStr(a[1](c))) === 0; }),
  contains: F(2, 2, function (c, a) { return toStr(a[0](c)).indexOf(toStr(a[1](c))) >= 0; }),
  'substring-before': F(2, 2, function (c, a) { var s = toStr(a[0](c)), k = s.indexOf(toStr(a[1](c))); return k < 0 ? '' : s.slice(0, k); }),
  'substring-after': F(2, 2, function (c, a) { var s = toStr(a[0](c)), t = toStr(a[1](c)), k = s.indexOf(t); return k < 0 ? '' : s.slice(k + t.length); }),
  substring: F(2, 3, function (c, a) {
    var s = toStr(a[0](c)), b = Math.round(toNum(a[1](c))), e = a.length > 2 ? b + Math.round(toNum(a[2](c))) : Infinity;
    if (b !== b || e !== e) return '';
    var lo = Math.max(1, b), hi = Math.min(s.length + 1, e);
    return lo >= hi ? '' : s.slice(lo - 1, hi - 1);
  }),
  'string-length': F(0, 1, function (c, a) { return (a.length ? toStr(a[0](c)) : strVal(c.node)).length; }),
  'normalize-space': F(0, 1, function (c, a) { return (a.length ? toStr(a[0](c)) : strVal(c.node)).replace(/[ \t\r\n]+/g, ' ').replace(/^ | $/g, ''); }),
  translate: F(3, 3, function (c, a) {
    var s = toStr(a[0](c)), f = toStr(a[1](c)), t = toStr(a[2](c)), o = '';
    for (var i = 0; i < s.length; i++) { var k = f.indexOf(s.charAt(i)); if (k < 0) o += s.charAt(i); else if (k < t.length) o += t.charAt(k); }
    return o;
  }),
  boolean: F(1, 1, function (c, a) { return toBool(a[0](c)); }),
  not: F(1, 1, function (c, a) { return !toBool(a[0](c)); }),
  true: F(0, 0, function () { return true; }),
  false: F(0, 0, function () { return false; }),
  lang: F(1, 1, function (c, a) {
    var want = toStr(a[0](c)).toLowerCase();
    for (var n = c.node.nodeType === 2 ? c.node.ownerElement : c.node; n && n.nodeType === 1; n = n.parentNode) {
      var l = n.getAttribute('xml:lang');
      if (l === null) l = n.getAttribute('lang');
      if (l !== null) { l = l.toLowerCase(); return l === want || l.indexOf(want + '-') === 0; }
    }
    return false;
  }),
  number: F(0, 1, function (c, a) { return a.length ? toNum(a[0](c)) : str2num(strVal(c.node)); }),
  sum: F(1, 1, function (c, a) { var v = a[0](c); if (!Array.isArray(v)) throw syntax('sum'); var t = 0; v.forEach(function (n) { t += str2num(strVal(n)); }); return t; }),
  floor: F(1, 1, function (c, a) { return Math.floor(toNum(a[0](c))); }),
  ceiling: F(1, 1, function (c, a) { return Math.ceil(toNum(a[0](c))); }),
  round: F(1, 1, function (c, a) { var x = toNum(a[0](c)); return x !== x || !isFinite(x) ? x : (x < 0 && x >= -0.5 ? -0 : Math.round(x)); })
};

// ---- API ---------------------------------------------------------------------------------------------
var ANY = 0, NUMBER = 1, STRING = 2, BOOLEAN = 3, UNORD_IT = 4, ORD_IT = 5, UNORD_SNAP = 6, ORD_SNAP = 7, ANY_NODE = 8, FIRST_NODE = 9;
var R = Symbol('result');
function XPathResult() { throw new TypeError('Illegal constructor'); }
var consts = { ANY_TYPE: 0, NUMBER_TYPE: 1, STRING_TYPE: 2, BOOLEAN_TYPE: 3, UNORDERED_NODE_ITERATOR_TYPE: 4, ORDERED_NODE_ITERATOR_TYPE: 5, UNORDERED_NODE_SNAPSHOT_TYPE: 6, ORDERED_NODE_SNAPSHOT_TYPE: 7, ANY_UNORDERED_NODE_TYPE: 8, FIRST_ORDERED_NODE_TYPE: 9 };
Object.keys(consts).forEach(function (k) {
  Object.defineProperty(XPathResult, k, { value: consts[k], enumerable: true });
  Object.defineProperty(XPathResult.prototype, k, { value: consts[k], enumerable: true });
});
function rslot(o) { var s = o !== null && typeof o === 'object' ? o[R] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
function typeErr(prop, what) { return err('TypeError', "Failed to read the '" + prop + "' property from 'XPathResult': The result type is not " + what + '.', 52); }
function genOf(doc) { try { return sw.docGen ? sw.docGen() : 0; } catch (e) { return 0; } }
H.attrs(XPathResult.prototype, {
  resultType: function () { return rslot(this).type; },
  numberValue: function () { var s = rslot(this); if (s.type !== NUMBER) throw typeErr('numberValue', 'a number'); return s.value; },
  stringValue: function () { var s = rslot(this); if (s.type !== STRING) throw typeErr('stringValue', 'a string'); return s.value; },
  booleanValue: function () { var s = rslot(this); if (s.type !== BOOLEAN) throw typeErr('booleanValue', 'a boolean'); return s.value; },
  singleNodeValue: function () { var s = rslot(this); if (s.type !== ANY_NODE && s.type !== FIRST_NODE) throw typeErr('singleNodeValue', 'a single node'); return s.nodes.length ? s.nodes[0] : null; },
  invalidIteratorState: function () { var s = rslot(this); return (s.type === UNORD_IT || s.type === ORD_IT) && s.gen !== genOf(s.doc); },
  snapshotLength: function () { var s = rslot(this); if (s.type !== UNORD_SNAP && s.type !== ORD_SNAP) throw typeErr('snapshotLength', 'a snapshot'); return s.nodes.length; }
});
H.define(XPathResult.prototype, 'iterateNext', function iterateNext() {
  var s = rslot(this);
  if (s.type !== UNORD_IT && s.type !== ORD_IT) throw typeErr('iterateNext', 'an iterator');
  if (s.gen !== genOf(s.doc)) throw err('InvalidStateError', 'The document has been mutated since the result was returned.', 11);
  return s.i < s.nodes.length ? s.nodes[s.i++] : null;
});
H.define(XPathResult.prototype, 'snapshotItem', function snapshotItem(i) {
  var s = rslot(this);
  if (s.type !== UNORD_SNAP && s.type !== ORD_SNAP) throw typeErr('snapshotItem', 'a snapshot');
  i = Math.trunc(Number(i)) || 0;
  return i >= 0 && i < s.nodes.length ? s.nodes[i] : null;
});
H.tag(XPathResult, 'XPathResult');

function makeResult(value, type, doc) {
  var r = Object.create(XPathResult.prototype), s = { doc: doc, gen: genOf(doc), i: 0 };
  if (type === ANY) type = Array.isArray(value) ? UNORD_IT : typeof value === 'number' ? NUMBER : typeof value === 'string' ? STRING : BOOLEAN;
  if (type >= UNORD_IT) {
    if (!Array.isArray(value)) throw err('TypeError', 'The expression cannot be converted to return the specified type.', 52);
    s.nodes = value;
  } else if (type === NUMBER) value = toNum(value);
  else if (type === STRING) value = toStr(value);
  else if (type === BOOLEAN) value = toBool(value);
  else throw err('NotSupportedError', 'The result type is invalid.', 9);
  s.type = type; s.value = value;
  Object.defineProperty(r, R, { value: s });
  return r;
}

var E = Symbol('expr');
function XPathExpression() { throw new TypeError('Illegal constructor'); }
function docOf(n) { return n.nodeType === 9 ? n : n.ownerDocument || g.document; }
H.define(XPathExpression.prototype, 'evaluate', function evaluate(contextNode, type, result) {
  var e = this !== null && typeof this === 'object' ? this[E] : null;
  if (!e) throw new TypeError('Illegal invocation');
  if (contextNode === null || typeof contextNode !== 'object' || typeof contextNode.nodeType !== 'number') throw new TypeError("Failed to execute 'evaluate' on 'XPathExpression': parameter 1 is not of type 'Node'.");
  type = type === undefined ? 0 : type >>> 0;
  if (type > 9) throw new TypeError("Failed to execute 'evaluate' on 'XPathExpression': The provided type is invalid.");
  if (contextNode.nodeType === 2 && !contextNode.ownerElement) throw err('NotSupportedError', 'The node is not supported as a context.', 9);
  var doc = docOf(contextNode.nodeType === 2 ? contextNode.ownerElement : contextNode);
  var v = e({ node: contextNode, pos: 1, size: 1, doc: doc });
  if (Array.isArray(v) && (type === ORD_IT || type === ORD_SNAP || type === FIRST_NODE || type === UNORD_IT || type === UNORD_SNAP || type === ANY_NODE || type === ANY)) v = docOrder(v);
  if (Array.isArray(v) && (type === ANY_NODE || type === FIRST_NODE)) v = v.slice(0, 1);
  return makeResult(v, type, doc);
});
H.tag(XPathExpression, 'XPathExpression');

function XPathNSResolver() { throw new TypeError('Illegal constructor'); }
H.define(XPathNSResolver.prototype, 'lookupNamespaceURI', function lookupNamespaceURI(prefix) { return null; });
H.tag(XPathNSResolver, 'XPathNSResolver');

function checkResolver(r) {
  if (r === null || r === undefined) return null;
  if (typeof r === 'function' || (typeof r === 'object' && typeof r.lookupNamespaceURI === 'function')) return r;
  throw new TypeError('The resolver is not a function or an object with lookupNamespaceURI.');
}
function compile(expression, resolver) {
  var fn = parse(String(expression), checkResolver(resolver));
  var x = Object.create(XPathExpression.prototype);
  Object.defineProperty(x, E, { value: fn });
  return x;
}
function nsResolverFor(node) {
  var r = Object.create(XPathNSResolver.prototype);
  H.define(r, 'lookupNamespaceURI', function (prefix) {
    if (prefix === null || prefix === undefined) prefix = '';
    prefix = String(prefix);
    if (prefix === 'xml') return XML_NS;
    for (var n = node; n; n = n.parentNode) {
      if (n.nodeType === 1) {
        if (prefix === '' && n.hasAttribute('xmlns')) return n.getAttribute('xmlns') || null;
        if (prefix !== '' && n.hasAttribute('xmlns:' + prefix)) return n.getAttribute('xmlns:' + prefix) || null;
      }
    }
    return node && node.lookupNamespaceURI ? node.lookupNamespaceURI(prefix || null) : null;
  });
  return r;
}
function XPathEvaluator() { if (!new.target) throw new TypeError("Failed to construct 'XPathEvaluator': Please use the 'new' operator"); }
H.define(XPathEvaluator.prototype, 'createExpression', function createExpression(expression, resolver) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'createExpression' on 'XPathEvaluator': 1 argument required, but only 0 present.");
  return compile(expression, resolver);
});
H.define(XPathEvaluator.prototype, 'createNSResolver', function createNSResolver(node) { return nsResolverFor(node); });
H.define(XPathEvaluator.prototype, 'evaluate', function evaluate(expression, contextNode, resolver, type, result) {
  if (arguments.length < 2) throw new TypeError("Failed to execute 'evaluate' on 'XPathEvaluator': 2 arguments required, but only " + arguments.length + ' present.');
  return compile(expression, resolver).evaluate(contextNode, type, result);
});
H.tag(XPathEvaluator, 'XPathEvaluator');

var DocProto = g.Document.prototype;
H.define(DocProto, 'createExpression', XPathEvaluator.prototype.createExpression);
H.define(DocProto, 'createNSResolver', function createNSResolver(node) { return nsResolverFor(node); });
H.define(DocProto, 'evaluate', XPathEvaluator.prototype.evaluate);

['XPathResult', 'XPathExpression', 'XPathEvaluator', 'XPathNSResolver'].forEach(function (n) {
  H.glob(n, { XPathResult: XPathResult, XPathExpression: XPathExpression, XPathEvaluator: XPathEvaluator, XPathNSResolver: XPathNSResolver }[n]);
});
