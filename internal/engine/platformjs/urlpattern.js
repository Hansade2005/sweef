// URLPattern (https://urlpattern.spec.whatwg.org/): pattern strings, constructor strings, init dicts,
// baseURL inheritance, test/exec with component results, normalised component getters.
var COMPONENTS = ['protocol', 'username', 'password', 'hostname', 'port', 'pathname', 'search', 'hash'];
var SPECIAL = { ftp: '21', file: '', http: '80', https: '443', ws: '80', wss: '443' };
var $ = Symbol('urlpattern');
var NAME_FIRST = /^[\p{ID_Start}$_]$/u, NAME_REST = /^[\p{ID_Continue}$‌‍]$/u;

function isNameCP(cp, first) { return (first ? NAME_FIRST : NAME_REST).test(cp); }
function escapeRegexp(s) { return s.replace(/[.+*?^${}()[\]|/\\]/g, '\\$&'); }
function escapePattern(s) { return s.replace(/[+*?:{}()\\]/g, '\\$&'); }
function typeError(m) { return new TypeError(m); }

// ---- tokenizer -------------------------------------------------------------------------
function tokenize(input, strict) {
  var cps = Array.from(input), toks = [], i = 0;
  function add(type, start, end, value) { toks.push({ type: type, index: start, value: value === undefined ? cps.slice(start, end).join('') : value }); }
  function bad(start, at, msg) {
    if (strict) throw typeError('Invalid URL pattern: ' + msg + ' at ' + at);
    add('invalid-char', start, at);
  }
  while (i < cps.length) {
    var c = cps[i];
    if (c === '*') { add('asterisk', i, i + 1); i++; continue; }
    if (c === '+' || c === '?') { add('other-modifier', i, i + 1); i++; continue; }
    if (c === '\\') {
      if (i === cps.length - 1) { bad(i, i + 1, 'trailing escape'); i++; continue; }
      add('escaped-char', i, i + 2, cps[i + 1]); i += 2; continue;
    }
    if (c === '{') { add('open', i, i + 1); i++; continue; }
    if (c === '}') { add('close', i, i + 1); i++; continue; }
    if (c === ':') {
      var p = i + 1;
      while (p < cps.length && isNameCP(cps[p], p === i + 1)) p++;
      if (p <= i + 1) { bad(i, i + 1, 'missing parameter name'); i++; continue; }
      add('name', i, p, cps.slice(i + 1, p).join('')); i = p; continue;
    }
    if (c === '(') {
      var depth = 1, q = i + 1, start = q, err = false;
      while (q < cps.length) {
        var d = cps[q];
        if (d.codePointAt(0) > 0x7f) { err = true; break; }
        if (q === start && d === '?') { err = true; break; }
        if (d === '\\') {
          if (q === cps.length - 1) { err = true; break; }
          q++;
          if (cps[q].codePointAt(0) > 0x7f) { err = true; break; }
          q++; continue;
        }
        if (d === ')') { depth--; if (depth === 0) { q++; break; } }
        else if (d === '(') {
          depth++;
          if (q === cps.length - 1 || cps[q + 1] !== '?') { err = true; break; }
        }
        q++;
      }
      if (err || depth !== 0 || q - start - 1 === 0) { bad(i, i + 1, 'invalid regexp group'); i++; continue; }
      add('regexp', i, q, cps.slice(start, q - 1).join('')); i = q; continue;
    }
    add('char', i, i + 1); i++;
  }
  toks.push({ type: 'end', index: cps.length, value: '' });
  return { tokens: toks, cps: cps };
}

// ---- pattern string parser ----------------------------------------------------------------
var HOST_OPTS = { delimiter: '.', prefix: '' }, PATH_OPTS = { delimiter: '/', prefix: '/' }, DEFAULT_OPTS = { delimiter: '', prefix: '' };
function segmentRegexp(o) { return '[^' + escapeRegexp(o.delimiter) + ']+?'; }
var FULL = '.*';

function parsePattern(input, opts, encode) {
  var toks = tokenize(input, true).tokens, idx = 0, parts = [], pending = '', nextNum = 0, seg = segmentRegexp(opts);
  function tryConsume(type) { if (toks[idx].type === type) return toks[idx++]; return null; }
  function mustConsume(type) { var t = tryConsume(type); if (!t) throw typeError('Invalid URL pattern: expected ' + type + ' at ' + toks[idx].index); return t; }
  function consumeText() {
    var r = '';
    for (;;) { var t = tryConsume('char') || tryConsume('escaped-char'); if (!t) break; r += t.value; }
    return r;
  }
  function flushFixed() {
    if (!pending) return;
    var enc = encode(pending);
    pending = '';
    parts.push({ type: 'fixed-text', value: enc, modifier: 'none', name: '', prefix: '', suffix: '' });
  }
  function addPart(prefix, nameTok, reTok, suffix, modTok) {
    var modifier = 'none';
    if (modTok) modifier = modTok.value === '?' ? 'optional' : modTok.value === '*' ? 'zero-or-more' : 'one-or-more';
    if (!nameTok && !reTok && modifier === 'none') { pending += prefix; return; }
    flushFixed();
    if (!nameTok && !reTok) {
      if (!prefix) return;
      parts.push({ type: 'fixed-text', value: encode(prefix), modifier: modifier, name: '', prefix: '', suffix: '' });
      return;
    }
    var value = !reTok ? seg : reTok.type === 'asterisk' ? FULL : reTok.value, type = 'regexp';
    if (value === seg) { type = 'segment-wildcard'; value = ''; }
    else if (value === FULL) { type = 'full-wildcard'; value = ''; }
    var name = '';
    if (nameTok) name = nameTok.value; else if (reTok) name = String(nextNum++);
    for (var k = 0; k < parts.length; k++) if (parts[k].name === name) throw typeError('Invalid URL pattern: duplicate name ' + name);
    parts.push({ type: type, value: value, modifier: modifier, name: name, prefix: encode(prefix), suffix: encode(suffix) });
  }
  function reOrWild(nameTok) {
    var t = tryConsume('regexp');
    if (!nameTok && !t) t = tryConsume('asterisk');
    return t;
  }
  function modifierTok() { return tryConsume('other-modifier') || tryConsume('asterisk'); }
  while (idx < toks.length) {
    var charTok = tryConsume('char'), nameTok = tryConsume('name'), reTok = reOrWild(nameTok);
    if (nameTok || reTok) {
      var prefix = charTok ? charTok.value : '';
      if (prefix !== '' && prefix !== opts.prefix) { pending += prefix; prefix = ''; }
      flushFixed();
      addPart(prefix, nameTok, reTok, '', modifierTok());
      continue;
    }
    var fixed = charTok || tryConsume('escaped-char');
    if (fixed) { pending += fixed.value; continue; }
    if (tryConsume('open')) {
      var pre = consumeText(), n2 = tryConsume('name'), r2 = reOrWild(n2), suf = consumeText();
      mustConsume('close');
      addPart(pre, n2, r2, suf, modifierTok());
      continue;
    }
    flushFixed();
    mustConsume('end');
  }
  return parts;
}

function modStr(m) { return m === 'optional' ? '?' : m === 'zero-or-more' ? '*' : m === 'one-or-more' ? '+' : ''; }
function regexpFor(parts, opts, flags) {
  var seg = segmentRegexp(opts), names = [], re = '^';
  parts.forEach(function (p) {
    if (p.type === 'fixed-text') {
      re += p.modifier === 'none' ? escapeRegexp(p.value) : '(?:' + escapeRegexp(p.value) + ')' + modStr(p.modifier);
      return;
    }
    names.push(p.name);
    var v = p.type === 'segment-wildcard' ? seg : p.type === 'full-wildcard' ? FULL : p.value, m = p.modifier;
    if (!p.prefix && !p.suffix) {
      re += m === 'none' || m === 'optional' ? '(' + v + ')' + modStr(m) : '((?:' + v + ')' + modStr(m) + ')';
    } else if (m === 'none' || m === 'optional') {
      re += '(?:' + escapeRegexp(p.prefix) + '(' + v + ')' + escapeRegexp(p.suffix) + ')' + modStr(m);
    } else {
      re += '(?:' + escapeRegexp(p.prefix) + '((?:' + v + ')(?:' + escapeRegexp(p.suffix) + escapeRegexp(p.prefix) + '(?:' + v + '))*)' + escapeRegexp(p.suffix) + ')';
      if (m === 'zero-or-more') re += '?';
    }
  });
  return { regexp: new RegExp(re + '$', flags), names: names };
}
function patternString(parts, opts) {
  var seg = segmentRegexp(opts), out = '';
  for (var i = 0; i < parts.length; i++) {
    var p = parts[i], prev = i > 0 ? parts[i - 1] : null, next = i < parts.length - 1 ? parts[i + 1] : null;
    if (p.type === 'fixed-text') {
      out += p.modifier === 'none' ? escapePattern(p.value) : '{' + escapePattern(p.value) + '}' + modStr(p.modifier);
      continue;
    }
    var custom = !/^[0-9]/.test(p.name);
    var group = p.suffix !== '' || (p.prefix !== '' && p.prefix !== opts.prefix);
    if (!group && custom && p.type === 'segment-wildcard' && p.modifier === 'none' && next && next.prefix === '' && next.suffix === '') {
      if (next.type === 'fixed-text') group = isNameCP(Array.from(next.value)[0], false);
      else group = /^[0-9]/.test(next.name);
    }
    if (!group && p.prefix === '' && prev && prev.type === 'fixed-text') {
      var lastCP = Array.from(prev.value).pop();
      if (lastCP === opts.prefix) group = true;
    }
    if (group) out += '{';
    out += escapePattern(p.prefix);
    if (custom) out += ':' + p.name;
    if (p.type === 'regexp') out += '(' + p.value + ')';
    else if (p.type === 'segment-wildcard' && !custom) out += '(' + seg + ')';
    else if (p.type === 'full-wildcard') {
      if (!custom && (!prev || prev.type === 'fixed-text' || prev.modifier !== 'none' || group || p.prefix !== '')) out += '*';
      else out += '(' + FULL + ')';
    }
    if (p.type === 'segment-wildcard' && custom && p.suffix !== '' && isNameCP(Array.from(p.suffix)[0], false)) out += '\\';
    out += escapePattern(p.suffix);
    if (group) out += '}';
    if (p.modifier !== 'none') out += modStr(p.modifier);
  }
  return out;
}
function compileComponent(input, encode, opts, ignoreCase) {
  var parts = parsePattern(input, opts, encode), r = regexpFor(parts, opts, ignoreCase ? 'ui' : 'u');
  return { pattern: patternString(parts, opts), regexp: r.regexp, names: r.names, hasRegExp: parts.some(function (p) { return p.type === 'regexp'; }) };
}

// ---- canonicalisation (uses the engine's URL setters) --------------------------------------------
function dummy() { return new g.URL('https://dummy.test'); }
var canon = {
  protocol: function (v) { if (v === '') return v; return new g.URL(v + '://dummy.test').protocol.slice(0, -1); },
  username: function (v) { if (v === '') return v; var u = dummy(); u.username = v; return u.username; },
  password: function (v) { if (v === '') return v; var u = dummy(); u.password = v; return u.password; },
  hostname: function (v) {
    if (v === '') return v;
    var u = dummy();
    if (/[\u0000-\u0020#%/:<>?@[\\\]^|\u007f]/.test(v)) throw typeError('Invalid hostname: ' + v);
    if (/^[\u0021-\u007e]+$/.test(v)) return v.toLowerCase();
    try { u.hostname = v; } catch (e) { throw typeError('Invalid hostname: ' + v); }
    if (u.hostname === '') throw typeError('Invalid hostname: ' + v);
    return u.hostname;
  },
  ipv6: function (v) {
    if (v === '') return v;
    if (!/^[0-9a-fA-F:[\]]*$/.test(v)) throw typeError('Invalid IPv6 hostname: ' + v);
    return v.toLowerCase();
  },
  port: function (v, protocol) {
    if (v === '') return v;
    if (!/^[0-9]+$/.test(v) || parseInt(v, 10) > 65535) throw typeError('Invalid port: ' + v);
    var n = String(parseInt(v, 10));
    if (protocol && SPECIAL[protocol] === n) return '';
    return n;
  },
  pathname: function (v) {
    if (v === '') return v;
    var lead = v.charAt(0) === '/', u = new g.URL('https://example.com');
    u.pathname = lead ? v : '/-' + v;
    var r = u.pathname;
    return lead ? r : r.slice(2);
  },
  opaquePathname: function (v) {
    if (v === '') return v;
    var out = '';
    Array.from(v).forEach(function (ch) {
      var c = ch.codePointAt(0);
      if (c <= 0x1f || c > 0x7e) out += encodeURIComponent(ch).replace(/%(?![0-9A-F]{2})/g, '%25');
      else out += ch;
    });
    return out;
  },
  search: function (v) { if (v === '') return v; var u = new g.URL('https://example.com'); u.search = v; return u.search.slice(1); },
  hash: function (v) { if (v === '') return v; var u = new g.URL('https://example.com'); u.hash = v; return u.hash.slice(1); }
};

// ---- init dictionaries -----------------------------------------------------------------------------------
function isAbsPath(v, type) {
  if (v === '') return false;
  if (v.charAt(0) === '/') return true;
  if (type === 'url') return false;
  if (v.length < 2) return false;
  return (v.charAt(0) === '\\' || v.charAt(0) === '{') && v.charAt(1) === '/';
}
function processInit(init, type, base) {
  var r = {}, has = function (k) { return init[k] !== undefined; };
  COMPONENTS.forEach(function (k) { if (base && base[k] !== undefined) r[k] = base[k]; });
  var baseURL = null, esc = function (s) { return type === 'pattern' ? escapePattern(s) : s; };
  if (has('baseURL')) {
    try { baseURL = new g.URL(init.baseURL); } catch (e) { throw typeError('Invalid baseURL: ' + init.baseURL); }
    if (!has('protocol')) r.protocol = esc(baseURL.protocol.slice(0, -1));
    if (type !== 'pattern' && !has('protocol') && !has('hostname') && !has('port') && !has('username')) r.username = esc(baseURL.username);
    if (type !== 'pattern' && !has('protocol') && !has('hostname') && !has('port') && !has('username') && !has('password')) r.password = esc(baseURL.password);
    if (!has('protocol') && !has('hostname')) r.hostname = esc(baseURL.hostname);
    if (!has('protocol') && !has('hostname') && !has('port')) r.port = esc(baseURL.port);
    if (!has('protocol') && !has('hostname') && !has('port') && !has('pathname')) r.pathname = esc(baseURL.pathname);
    if (!has('protocol') && !has('hostname') && !has('port') && !has('pathname') && !has('search')) r.search = esc(baseURL.search.slice(1));
    if (!has('protocol') && !has('hostname') && !has('port') && !has('pathname') && !has('search') && !has('hash')) r.hash = esc(baseURL.hash.slice(1));
  }
  if (has('protocol')) { var p = init.protocol.replace(/:$/, ''); r.protocol = type === 'pattern' ? p : canon.protocol(p); }
  if (has('username')) r.username = type === 'pattern' ? init.username : canon.username(init.username);
  if (has('password')) r.password = type === 'pattern' ? init.password : canon.password(init.password);
  if (has('hostname')) r.hostname = type === 'pattern' ? init.hostname : (/^\[/.test(init.hostname) ? canon.ipv6 : canon.hostname)(init.hostname);
  if (has('port')) r.port = type === 'pattern' ? init.port : canon.port(init.port, r.protocol);
  if (has('pathname')) {
    var pn = init.pathname;
    if (baseURL && baseURL.pathname.charAt(0) === '/' && !isAbsPath(pn, type)) {
      var slash = baseURL.pathname.lastIndexOf('/');
      if (slash >= 0) pn = esc(baseURL.pathname.slice(0, slash + 1)) + pn;
    }
    if (pn !== '' && type !== 'pattern') pn = (SPECIAL[r.protocol] !== undefined || r.protocol === undefined || r.protocol === '') ? canon.pathname(pn) : canon.opaquePathname(pn);
    r.pathname = pn;
  }
  if (has('search')) { var s = init.search.replace(/^\?/, ''); r.search = type === 'pattern' ? s : canon.search(s); }
  if (has('hash')) { var h = init.hash.replace(/^#/, ''); r.hash = type === 'pattern' ? h : canon.hash(h); }
  return r;
}

// ---- constructor string parser -----------------------------------------------------------------------------
function parseConstructorString(input) {
  var tk = tokenize(input, false), toks = tk.tokens, cps = tk.cps;
  var result = {}, start = 0, idx = 0, inc = 1, depth = 0, v6 = 0, special = false, state = 'init';
  function safe(i) { return i < toks.length ? toks[i] : toks[toks.length - 1]; }
  function nonSpecial(i, v) { var t = safe(i); if (t.value !== v) return false; return t.type === 'char' || t.type === 'escaped-char' || t.type === 'invalid-char'; }
  function rewind() { idx = start; inc = 0; }
  function component() {
    var t = safe(idx), st = safe(start);
    return cps.slice(st.index, t.index).join('');
  }
  function change(next, skip) {
    if (state !== 'init' && state !== 'authority' && state !== 'done') result[state] = component();
    if (state !== 'init' && next !== 'done') {
      var pre = ['protocol', 'authority', 'username', 'password'];
      if (pre.indexOf(state) >= 0 && ['port', 'pathname', 'search', 'hash'].indexOf(next) >= 0 && result.hostname === undefined) result.hostname = '';
      if (pre.concat(['hostname', 'port']).indexOf(state) >= 0 && (next === 'search' || next === 'hash') && result.pathname === undefined) result.pathname = special ? '/' : '';
      if (pre.concat(['hostname', 'port', 'pathname']).indexOf(state) >= 0 && next === 'hash' && result.search === undefined) result.search = '';
    }
    state = next; idx += skip; start = idx; inc = 0;
  }
  function isSearchPrefix() {
    if (nonSpecial(idx, '?')) return true;
    if (toks[idx].value !== '?') return false;
    var pi = idx - 1;
    if (pi < 0) return true;
    var pt = safe(pi);
    return !(pt.type === 'name' || pt.type === 'regexp' || pt.type === 'close' || pt.type === 'asterisk');
  }
  function computeSpecial() {
    var c = compileComponent(component(), canon.protocol, DEFAULT_OPTS, false);
    special = Object.keys(SPECIAL).some(function (s) { return c.regexp.test(s); });
  }
  while (idx < toks.length) {
    inc = 1;
    var t = toks[idx];
    if (t.type === 'end') {
      if (state === 'init') {
        rewind();
        if (nonSpecial(idx, '#')) change('hash', 1);
        else if (isSearchPrefix()) change('search', 1);
        else change('pathname', 0);
        idx += inc; continue;
      }
      if (state === 'authority') { rewind(); state = 'hostname'; idx += inc; continue; }
      change('done', 0);
      break;
    }
    if (t.type === 'open') { depth++; idx += inc; continue; }
    if (depth > 0) {
      if (t.type === 'close') depth--;
      else { idx += inc; continue; }
    }
    switch (state) {
      case 'init':
        if (nonSpecial(idx, ':')) { rewind(); state = 'protocol'; }
        break;
      case 'protocol':
        if (nonSpecial(idx, ':')) {
          computeSpecial();
          var next = 'pathname', skip = 1;
          if (nonSpecial(idx + 1, '/') && nonSpecial(idx + 2, '/')) { next = 'authority'; skip = 3; }
          else if (special) next = 'authority';
          change(next, skip);
        }
        break;
      case 'authority':
        if (nonSpecial(idx, '@')) { rewind(); state = 'username'; }
        else if (nonSpecial(idx, '/') || isSearchPrefix() || nonSpecial(idx, '#')) { rewind(); state = 'hostname'; }
        break;
      case 'username':
        if (nonSpecial(idx, ':')) change('password', 1);
        else if (nonSpecial(idx, '@')) change('hostname', 1);
        break;
      case 'password':
        if (nonSpecial(idx, '@')) change('hostname', 1);
        break;
      case 'hostname':
        if (nonSpecial(idx, '[')) v6++;
        else if (nonSpecial(idx, ']')) v6--;
        else if (nonSpecial(idx, ':') && v6 === 0) change('port', 1);
        else if (nonSpecial(idx, '/')) change('pathname', 0);
        else if (isSearchPrefix()) change('search', 1);
        else if (nonSpecial(idx, '#')) change('hash', 1);
        break;
      case 'port':
        if (isSearchPrefix()) change('search', 1);
        else if (nonSpecial(idx, '#')) change('hash', 1);
        else if (nonSpecial(idx, '/')) change('pathname', 0);
        break;
      case 'pathname':
        if (isSearchPrefix()) change('search', 1);
        else if (nonSpecial(idx, '#')) change('hash', 1);
        break;
      case 'search':
        if (nonSpecial(idx, '#')) change('hash', 1);
        break;
    }
    idx += inc;
  }
  if (result.hostname !== undefined && result.port === undefined) result.port = '';
  return result;
}

// ---- URLPattern ----------------------------------------------------------------------------------------------
function dictOf(o, what) {
  var d = {};
  if (o === null || typeof o !== 'object') throw typeError('Invalid ' + what);
  COMPONENTS.concat(['baseURL']).forEach(function (k) { if (o[k] !== undefined) d[k] = String(o[k]); });
  return d;
}
function URLPattern(input, baseURL, options) {
  if (!new.target) throw typeError("Failed to construct 'URLPattern': Please use the 'new' operator.");
  if (input === undefined) input = {};
  var base;
  if (typeof baseURL === 'string') base = baseURL;
  else if (baseURL !== undefined) options = baseURL;
  if (options !== undefined && options !== null && typeof options !== 'object') throw typeError('Invalid options');
  var ignoreCase = !!(options && options.ignoreCase), init;
  if (typeof input === 'string') {
    init = parseConstructorString(input);
    if (base === undefined && init.protocol === undefined) throw typeError('Relative constructor string must have a base URL.');
    if (base !== undefined) init.baseURL = base;
  } else {
    if (base !== undefined) throw typeError('A baseURL string is only allowed with a string input.');
    init = dictOf(input, 'URLPattern input');
  }
  var p = processInit(init, 'pattern');
  COMPONENTS.forEach(function (k) { if (p[k] === undefined) p[k] = '*'; });
  if (SPECIAL[p.protocol] !== undefined && SPECIAL[p.protocol] === p.port) p.port = '';
  var c = {};
  c.protocol = compileComponent(p.protocol, canon.protocol, DEFAULT_OPTS, ignoreCase);
  c.username = compileComponent(p.username, canon.username, DEFAULT_OPTS, ignoreCase);
  c.password = compileComponent(p.password, canon.password, DEFAULT_OPTS, ignoreCase);
  c.hostname = /^(\[|\{\[|\\\[)/.test(p.hostname) ? compileComponent(p.hostname, canon.ipv6, DEFAULT_OPTS, ignoreCase) : compileComponent(p.hostname, canon.hostname, HOST_OPTS, ignoreCase);
  c.port = compileComponent(p.port, function (v) { return canon.port(v); }, DEFAULT_OPTS, ignoreCase);
  var isSpecial = Object.keys(SPECIAL).some(function (s) { return c.protocol.regexp.test(s); });
  c.pathname = isSpecial ? compileComponent(p.pathname, canon.pathname, PATH_OPTS, ignoreCase) : compileComponent(p.pathname, canon.opaquePathname, DEFAULT_OPTS, ignoreCase);
  c.search = compileComponent(p.search, canon.search, DEFAULT_OPTS, ignoreCase);
  c.hash = compileComponent(p.hash, canon.hash, DEFAULT_OPTS, ignoreCase);
  Object.defineProperty(this, $, { value: c });
}
function slot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
COMPONENTS.forEach(function (k) {
  Object.defineProperty(URLPattern.prototype, k, { get: function () { return slot(this)[k].pattern; }, enumerable: true, configurable: true });
});
Object.defineProperty(URLPattern.prototype, 'hasRegExpGroups', { get: function () { var c = slot(this); return COMPONENTS.some(function (k) { return c[k].hasRegExp; }); }, enumerable: true, configurable: true });

function match(pattern, input, baseURL) {
  var c = slot(pattern), inputs, v = {};
  if (input === undefined) input = {};
  if (typeof input === 'string') {
    inputs = [input];
    var u;
    try {
      if (baseURL !== undefined) { baseURL = String(baseURL); inputs.push(baseURL); u = new g.URL(input, baseURL); } else u = new g.URL(input);
    } catch (e) { return null; }
    v = { protocol: u.protocol.slice(0, -1), username: u.username, password: u.password, hostname: u.hostname, port: u.port, pathname: u.pathname, search: u.search.slice(1), hash: u.hash.slice(1) };
  } else {
    if (baseURL !== undefined) throw typeError('A baseURL is only allowed with a string input.');
    inputs = [input];
    try {
      var blank = {}; COMPONENTS.forEach(function (k) { blank[k] = ''; });
      v = processInit(dictOf(input, 'URLPatternInit'), 'url', blank);
    } catch (e) { if (e instanceof TypeError) return null; throw e; }
  }
  var res = { inputs: inputs };
  for (var i = 0; i < COMPONENTS.length; i++) {
    var k = COMPONENTS[i], m = c[k].regexp.exec(v[k]);
    if (!m) return null;
    var groups = {};
    c[k].names.forEach(function (n, j) { groups[n] = m[j + 1]; });
    res[k] = { input: v[k], groups: groups };
  }
  return res;
}
H.define(URLPattern.prototype, 'test', function test(input, baseURL) { return match(this, input, baseURL) !== null; });
H.define(URLPattern.prototype, 'exec', function exec(input, baseURL) { return match(this, input, baseURL); });
H.tag(URLPattern, 'URLPattern');
H.glob('URLPattern', URLPattern);
