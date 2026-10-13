/*
 * IndexedDB, structured clone for storage and navigator.storage.
 *
 * Evaluated after the base prelude in every VM that has a document (pages and
 * the hidden tab a service worker runs in). Go owns the data and the rules for
 * which transactions may run together (internal/sitestore); this file owns what
 * the platform defines in terms of script: requests, events, transaction
 * activity and auto-commit, cursors, key extraction, structured cloning.
 *
 * Keys cross to Go as order-preserving strings (plain string comparison of two
 * encoded keys is the IndexedDB key order):
 *   number  \x10 + 16 hex digits of the bit pattern, sign-corrected
 *   date    \x20 + the same for the time value
 *   string  \x30 + UTF-16 code units, each as UTF-8 style bytes, + \x01
 *   binary  \x40 + bytes + \x01
 *   array   \x50 + the encoded elements + \x01
 * Inside strings and binaries the bytes 0, 1 and 2 are written \x02 followed by
 * byte+1, so the terminator \x01 sorts before every payload byte and no NUL is
 * ever sent to Go. Every character of an encoded key is <= \xFF.
 *
 * Values cross as one JSON string, see ser()/de() below.
 */
(function (global, H) {
  'use strict';
  if (!H || typeof H.idbOpen !== 'function') return;

  // ---- small helpers ---------------------------------------------------------------
  function hide(o, props) { for (var k in props) Object.defineProperty(o, k, { value: props[k], writable: true, configurable: true, enumerable: false }); }
  function defAttrs(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { get: map[k], enumerable: true, configurable: true }); }); }
  function defMethods(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { value: map[k], writable: true, enumerable: true, configurable: true }); }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function consts(C, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(C, k, { value: map[k], enumerable: true }); }); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  var TOKEN = {};
  function exportClass(C, name) { Object.defineProperty(C, 'name', { value: name, configurable: true }); Object.defineProperty(global, name, { value: C, writable: true, configurable: true, enumerable: false }); }
  function mkErr(name, msg) { return new global.DOMException(msg || name, name); }
  function report(e) { if (typeof global.reportError === 'function') { try { global.reportError(e); } catch (x) { /* ignore */ } } }
  // errors the Go side threw arrive as TypeError("Name: message")
  function hostErr(e) {
    var m = e && typeof e.message === 'string' ? /^([A-Za-z]+Error): ([\s\S]*)$/.exec(e.message) : null;
    return m ? mkErr(m[1], m[2]) : e;
  }
  function host(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    try { return H[name].apply(null, args); } catch (e) { throw hostErr(e); }
  }
  function isObj(v) { return v !== null && (typeof v === 'object' || typeof v === 'function'); }

  // ---- tasks: one Go event per queued function, microtasks drain between them ------------
  // One Go event drives the whole queue: Go calls __swIdbTask until it reports nothing left (draining microtasks
  // between the calls), then the next schedule() raises a new event.
  var tasks = [], driven = false;
  function schedule(fn) {
    tasks.push(fn);
    if (!driven) { driven = true; H.idbSched(); }
  }
  Object.defineProperty(global, '__swIdbTask', { value: function () {
    var f = tasks.shift();
    if (f) { try { f(); } catch (e) { report(e); } }
    if (!tasks.length) driven = false;
    return tasks.length;
  } });

  // ---- keys ------------------------------------------------------------------------
  var dv = new DataView(new ArrayBuffer(8));
  function hex8(n) { return ('00000000' + (n >>> 0).toString(16)).slice(-8); }
  function encNum(tagc, f) {
    if (f === 0) f = 0;
    dv.setFloat64(0, f);
    var hi = dv.getUint32(0), lo = dv.getUint32(4);
    if (hi >>> 31) { hi = ~hi >>> 0; lo = ~lo >>> 0; } else hi = (hi | 0x80000000) >>> 0;
    return tagc + hex8(hi) + hex8(lo);
  }
  function decNum(s, i) {
    var hi = parseInt(s.substr(i, 8), 16), lo = parseInt(s.substr(i + 8, 8), 16);
    if (hi >>> 31) hi = (hi & 0x7fffffff) >>> 0; else { hi = ~hi >>> 0; lo = ~lo >>> 0; }
    dv.setUint32(0, hi); dv.setUint32(4, lo);
    return dv.getFloat64(0);
  }
  var PLAIN = /^[\x03-\x7f]*$/;
  function escUnits(s) {
    if (PLAIN.test(s)) return s;
    var out = '';
    for (var i = 0; i < s.length; i++) {
      var u = s.charCodeAt(i);
      if (u < 3) out += '\x02' + String.fromCharCode(u + 1);
      else if (u < 0x80) out += s[i];
      else if (u < 0x800) out += String.fromCharCode(0xc0 | (u >> 6), 0x80 | (u & 63));
      else out += String.fromCharCode(0xe0 | (u >> 12), 0x80 | ((u >> 6) & 63), 0x80 | (u & 63));
    }
    return out;
  }
  function bytesOf(v) {
    if (v instanceof ArrayBuffer) return new Uint8Array(v);
    return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  }
  function isBinary(v) { return v instanceof ArrayBuffer || ArrayBuffer.isView(v); }
  // encodeKey returns the encoded key, or undefined when v is not a valid key
  function encodeKey(v, depth, seen) {
    switch (typeof v) {
      case 'number': return v !== v ? undefined : encNum('\x10', v);
      case 'string': return '\x30' + escUnits(v) + '\x01';
      case 'object':
        if (v === null) return undefined;
        if (v instanceof Date) { var t = v.getTime(); return t !== t ? undefined : encNum('\x20', t); }
        if (isBinary(v)) {
          var b = bytesOf(v), out = '\x40';
          for (var i = 0; i < b.length; i++) out += b[i] < 3 ? '\x02' + String.fromCharCode(b[i] + 1) : String.fromCharCode(b[i]);
          return out + '\x01';
        }
        if (Array.isArray(v)) {
          seen = seen || [];
          if (seen.indexOf(v) >= 0 || (depth || 0) > 64) return undefined;
          seen.push(v);
          var r = '\x50';
          for (var j = 0; j < v.length; j++) {
            if (!(j in v)) { seen.pop(); return undefined; }
            var e = encodeKey(v[j], (depth || 0) + 1, seen);
            if (e === undefined) { seen.pop(); return undefined; }
            r += e;
          }
          seen.pop();
          return r + '\x01';
        }
    }
    return undefined;
  }
  function decodeKey(s) {
    var pos = 0;
    function one() {
      var t = s.charCodeAt(pos++);
      switch (t) {
        case 0x10: { var f = decNum(s, pos); pos += 16; return f; }
        case 0x20: { var d = new Date(decNum(s, pos)); pos += 16; return d; }
        case 0x30: {
          var out = '';
          for (;;) {
            var c = s.charCodeAt(pos++);
            if (c === 1 || c !== c) break;
            if (c === 2) out += String.fromCharCode(s.charCodeAt(pos++) - 1);
            else if (c < 0x80) out += String.fromCharCode(c);
            else if (c < 0xe0) out += String.fromCharCode(((c & 31) << 6) | (s.charCodeAt(pos++) & 63));
            else { var c2 = s.charCodeAt(pos++), c3 = s.charCodeAt(pos++); out += String.fromCharCode(((c & 15) << 12) | ((c2 & 63) << 6) | (c3 & 63)); }
          }
          return out;
        }
        case 0x40: {
          var bytes = [];
          for (;;) {
            var x = s.charCodeAt(pos++);
            if (x === 1 || x !== x) break;
            bytes.push(x === 2 ? s.charCodeAt(pos++) - 1 : x);
          }
          return new Uint8Array(bytes).buffer;
        }
        case 0x50: {
          var arr = [];
          while (s.charCodeAt(pos) !== 1 && pos < s.length) arr.push(one());
          pos++;
          return arr;
        }
      }
      throw mkErr('DataError', 'corrupt key');
    }
    return one();
  }
  function keyOrThrow(v) {
    var k = encodeKey(v);
    if (k === undefined) throw mkErr('DataError', 'The parameter is not a valid key.');
    return k;
  }

  // ---- key paths -----------------------------------------------------------------------
  var IDENT;
  try { IDENT = new RegExp('^[$_\\p{ID_Start}][$\\u200c\\u200d\\p{ID_Continue}]*$', 'u'); } catch (e) { IDENT = /^[$_A-Za-z][$\w]*$/; }
  function validKeyPath(kp) {
    if (typeof kp === 'string') return kp === '' || kp.split('.').every(function (p) { return IDENT.test(p); });
    if (Array.isArray(kp)) return kp.length > 0 && kp.every(function (p) { return typeof p === 'string' && validKeyPath(p); });
    return false;
  }
  function normKeyPath(kp) {
    if (kp === undefined || kp === null) return null;
    if (Array.isArray(kp) || (kp && typeof kp !== 'string' && typeof kp[Symbol.iterator] === 'function')) kp = Array.prototype.slice.call(kp).map(String);
    else kp = String(kp);
    return kp;
  }
  // evaluates a key path against a value: {ok, v}
  var MISSING = { ok: false };
  function evalPath(value, path) {
    if (path === '') return { ok: true, v: value };
    var parts = path.split('.');
    for (var i = 0; i < parts.length; i++) {
      var p = parts[i];
      if (typeof value === 'string' && p === 'length') { value = value.length; continue; }
      if (Array.isArray(value) && p === 'length') { value = value.length; continue; }
      if (global.Blob && value instanceof global.Blob && (p === 'size' || p === 'type')) { value = value[p]; continue; }
      if (global.File && value instanceof global.File && (p === 'name' || p === 'lastModified')) { value = value[p]; continue; }
      if (!isObj(value) || !(p in value)) return MISSING;
      value = value[p];
    }
    return { ok: true, v: value };
  }
  function extractKey(value, kp) {
    if (typeof kp === 'string') return evalPath(value, kp);
    var out = [];
    for (var i = 0; i < kp.length; i++) {
      var r = evalPath(value, kp[i]);
      if (!r.ok) return MISSING;
      out.push(r.v);
    }
    return { ok: true, v: out };
  }
  // can key be injected at path in value (the part of the path that exists must be objects)
  function canInject(value, path) {
    var parts = path.split('.');
    for (var i = 0; i < parts.length - 1; i++) {
      if (!isObj(value)) return false;
      if (!(parts[i] in value)) return true;
      value = value[parts[i]];
    }
    return isObj(value);
  }
  function inject(value, path, key) {
    var parts = path.split('.');
    for (var i = 0; i < parts.length - 1; i++) {
      if (!(parts[i] in value)) value[parts[i]] = {};
      value = value[parts[i]];
    }
    Object.defineProperty(value, parts[parts.length - 1], { value: key, writable: true, enumerable: true, configurable: true });
  }

  // ---- structured clone to/from a JSON string ---------------------------------------------------
  function binStr(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i += 8192) s += String.fromCharCode.apply(null, u8.subarray(i, Math.min(i + 8192, u8.length)));
    return s;
  }
  function strBin(s) {
    var u = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) u[i] = s.charCodeAt(i);
    return u;
  }
  var ERR_NAMES = ['Error', 'EvalError', 'RangeError', 'ReferenceError', 'SyntaxError', 'TypeError', 'URIError'];
  function cloneFail(what) { return mkErr('DataCloneError', (what || 'value') + ' could not be cloned.'); }
  function ser(value) {
    var ids = new Map(), next = 0;
    function node(v) {
      switch (typeof v) {
        case 'undefined': return ['u'];
        case 'boolean': case 'string': return v;
        case 'number':
          if (v !== v) return ['n', 'NaN'];
          if (v === Infinity) return ['n', 'Infinity'];
          if (v === -Infinity) return ['n', '-Infinity'];
          if (v === 0 && 1 / v < 0) return ['n', '-0'];
          return v;
        case 'bigint': return ['b', v.toString()];
        case 'symbol': throw cloneFail('Symbol()');
        case 'function': throw cloneFail(String(v).slice(0, 60));
      }
      if (v === null) return null;
      if (ids.has(v)) return ['#', ids.get(v)];
      if (typeof v.nodeType === 'number' && typeof v.nodeName === 'string') throw cloneFail('#<' + v.nodeName + '>');
      var id = next++;
      if (v instanceof Date) { ids.set(v, id); return ['d', id, v.getTime() === v.getTime() ? v.getTime() : null]; }
      if (v instanceof RegExp) { ids.set(v, id); return ['r', id, v.source, v.flags]; }
      if (v instanceof ArrayBuffer) { ids.set(v, id); return ['AB', id, binStr(new Uint8Array(v))]; }
      if (ArrayBuffer.isView(v)) {
        ids.set(v, id);
        var isDV = v instanceof DataView;
        return ['TA', id, isDV ? 'DataView' : v.constructor.name, node(v.buffer), v.byteOffset, isDV ? v.byteLength : v.length];
      }
      if (global.Blob && v instanceof global.Blob) {
        ids.set(v, id);
        var raw = binStr(v._bytes || new Uint8Array(0));
        if (global.File && v instanceof global.File) return ['F', id, v.name, v.type, v.lastModified, raw];
        return ['Bl', id, v.type, raw];
      }
      if (v instanceof Promise || v instanceof WeakMap || v instanceof WeakSet || (global.WeakRef && v instanceof global.WeakRef)) throw cloneFail('#<' + (v.constructor && v.constructor.name || 'Object') + '>');
      if (v instanceof Boolean) { ids.set(v, id); return ['W', id, 'B', v.valueOf()]; }
      if (v instanceof Number) { ids.set(v, id); return ['W', id, 'N', node(v.valueOf())]; }
      if (v instanceof String) { ids.set(v, id); return ['W', id, 'S', v.valueOf()]; }
      if (v instanceof Error) {
        ids.set(v, id);
        var nm = ERR_NAMES.indexOf(v.name) >= 0 ? v.name : 'Error';
        return ['E', id, nm, String(v.message), typeof v.stack === 'string' ? v.stack : ''];
      }
      if (v instanceof Map) {
        ids.set(v, id);
        var me = [];
        v.forEach(function (val, k) { me.push([node(k), node(val)]); });
        return ['M', id, me];
      }
      if (v instanceof Set) {
        ids.set(v, id);
        var se = [];
        v.forEach(function (val) { se.push(node(val)); });
        return ['S', id, se];
      }
      if (Array.isArray(v)) {
        ids.set(v, id);
        var ae = [], ks = Object.keys(v);
        for (var i = 0; i < ks.length; i++) ae.push([ks[i], node(v[ks[i]])]);
        return ['A', id, v.length, ae];
      }
      ids.set(v, id);
      var oe = [], keys = Object.keys(v);
      for (var j = 0; j < keys.length; j++) oe.push([keys[j], node(v[keys[j]])]);
      return ['O', id, oe];
    }
    return JSON.stringify(node(value));
  }
  function de(s) {
    var objs = [];
    function build(n) {
      if (n === null || typeof n !== 'object') return n;
      switch (n[0]) {
        case 'u': return undefined;
        case 'n': return n[1] === 'NaN' ? NaN : n[1] === 'Infinity' ? Infinity : n[1] === '-Infinity' ? -Infinity : -0;
        case 'b': return BigInt(n[1]);
        case '#': return objs[n[1]];
        case 'd': return (objs[n[1]] = new Date(n[2] === null ? NaN : n[2]));
        case 'r': return (objs[n[1]] = new RegExp(n[2], n[3]));
        case 'AB': { var u = strBin(n[2]); return (objs[n[1]] = u.buffer); }
        case 'TA': {
          var buf = build(n[3]), C = n[2] === 'DataView' ? DataView : global[n[2]];
          return (objs[n[1]] = n[2] === 'DataView' ? new DataView(buf, n[4], n[5]) : new C(buf, n[4], n[5]));
        }
        case 'Bl': { var b = new global.Blob([strBin(n[3])], { type: n[2] }); return (objs[n[1]] = b); }
        case 'F': { var f = new global.File([strBin(n[5])], n[2], { type: n[3], lastModified: n[4] }); return (objs[n[1]] = f); }
        case 'W': return (objs[n[1]] = n[2] === 'B' ? new Boolean(n[3]) : n[2] === 'N' ? new Number(build(n[3])) : new String(n[3]));
        case 'E': {
          var E = global[n[2]] || Error, e = new E(n[3]);
          if (n[4]) { try { Object.defineProperty(e, 'stack', { value: n[4], writable: true, configurable: true }); } catch (x) { /* ignore */ } }
          return (objs[n[1]] = e);
        }
        case 'M': { var m = (objs[n[1]] = new Map()); n[2].forEach(function (p) { m.set(build(p[0]), build(p[1])); }); return m; }
        case 'S': { var st = (objs[n[1]] = new Set()); n[2].forEach(function (x) { st.add(build(x)); }); return st; }
        case 'A': { var a = (objs[n[1]] = new Array(n[2])); n[3].forEach(function (p) { a[p[0]] = build(p[1]); }); return a; }
        case 'O': { var o = (objs[n[1]] = {}); n[2].forEach(function (p) { o[p[0]] = build(p[1]); }); return o; }
      }
      return undefined;
    }
    return build(JSON.parse(s));
  }
  Object.defineProperty(global, '__swSer', { value: ser });
  Object.defineProperty(global, '__swDe', { value: de });

  // ---- events (IDB objects dispatch their own, so a throwing handler can abort the transaction) ---
  var ET = global.EventTarget;
  function EvtTarget() { illegal(); }
  if (ET) Object.setPrototypeOf(EvtTarget.prototype, ET.prototype);
  function listeners(t) { return t._ls || (Object.defineProperty(t, '_ls', { value: {}, writable: true, configurable: true }), t._ls); }
  defMethods(EvtTarget.prototype, {
    addEventListener: function (type, fn, opts) {
      if (typeof fn !== 'function' && !(fn && typeof fn.handleEvent === 'function')) return;
      type = String(type);
      var capture = typeof opts === 'boolean' ? opts : !!(opts && opts.capture), once = !!(opts && typeof opts === 'object' && opts.once);
      var l = listeners(this)[type] || (listeners(this)[type] = []);
      for (var i = 0; i < l.length; i++) if (l[i].fn === fn && l[i].capture === capture) return;
      l.push({ fn: fn, capture: capture, once: once });
    },
    removeEventListener: function (type, fn, opts) {
      var capture = typeof opts === 'boolean' ? opts : !!(opts && opts.capture), l = listeners(this)[String(type)];
      if (!l) return;
      for (var i = 0; i < l.length; i++) if (l[i].fn === fn && l[i].capture === capture) { l.splice(i, 1); return; }
    },
    dispatchEvent: function (ev) { return !dispatch(this, ev, [this]).canceled; }
  });
  function handlers(proto, types) {
    types.forEach(function (t) {
      Object.defineProperty(proto, 'on' + t, {
        get: function () { return (this._on && this._on[t]) || null; },
        set: function (f) { if (!this._on) Object.defineProperty(this, '_on', { value: {}, writable: true, configurable: true }); this._on[t] = isObj(f) ? f : null; },
        enumerable: true, configurable: true
      });
    });
  }
  function Evt(type, init, bubbles, cancelable) {
    init = init || {};
    this.type = String(type);
    this.bubbles = bubbles !== undefined ? bubbles : !!init.bubbles;
    this.cancelable = cancelable !== undefined ? cancelable : !!init.cancelable;
    this.target = null; this.currentTarget = null; this.eventPhase = 0;
    this.defaultPrevented = false; this.isTrusted = true; this.composed = false;
    this.timeStamp = typeof global.performance === 'object' && global.performance.now ? global.performance.now() : 0;
    hide(this, { _stop: false, _stopImm: false });
  }
  if (global.Event) Object.setPrototypeOf(Evt.prototype, global.Event.prototype);
  defMethods(Evt.prototype, {
    preventDefault: function () { if (this.cancelable) this.defaultPrevented = true; },
    stopPropagation: function () { this._stop = true; },
    stopImmediatePropagation: function () { this._stop = true; this._stopImm = true; },
    composedPath: function () { return []; }
  });
  consts(Evt, { NONE: 0, CAPTURING_PHASE: 1, AT_TARGET: 2, BUBBLING_PHASE: 3 });
  tag(Evt, 'Event');
  function IDBVersionChangeEvent(type, init) {
    if (type === TOKEN) { Evt.call(this, arguments[1], null, false, false); this.oldVersion = arguments[2]; this.newVersion = arguments[3]; return; }
    if (arguments.length < 1) throw new TypeError("Failed to construct 'IDBVersionChangeEvent': 1 argument required, but only 0 present.");
    Evt.call(this, type, init);
    this.oldVersion = init && init.oldVersion !== undefined ? Number(init.oldVersion) : 0;
    this.newVersion = init && init.newVersion !== undefined && init.newVersion !== null ? Number(init.newVersion) : null;
  }
  IDBVersionChangeEvent.prototype = Object.create(Evt.prototype, { constructor: { value: IDBVersionChangeEvent, writable: true, configurable: true } });
  tag(IDBVersionChangeEvent, 'IDBVersionChangeEvent');
  exportClass(IDBVersionChangeEvent, 'IDBVersionChangeEvent');

  var handlerCalls = 0;
  function invoke(t, ev, phase) {
    var threw = false;
    ev.currentTarget = t; ev.eventPhase = phase;
    function call(fn) {
      handlerCalls++;
      try { if (typeof fn === 'function') fn.call(t, ev); else fn.handleEvent(ev); } catch (e) { threw = true; report(e); }
    }
    if (phase !== 1 && t._on && t._on[ev.type]) call(t._on[ev.type]);
    var l = t._ls && t._ls[ev.type];
    if (l) {
      l = l.slice();
      for (var i = 0; i < l.length; i++) {
        var e = l[i];
        if ((phase === 1 && !e.capture) || (phase === 3 && e.capture)) continue;
        if (e.once) { var cur = t._ls[ev.type]; var ix = cur.indexOf(e); if (ix >= 0) cur.splice(ix, 1); }
        call(e.fn);
        if (ev._stopImm) break;
      }
    }
    return threw;
  }
  // path[0] is the target, path[1..] its ancestors; returns {threw, canceled}
  function dispatch(target, ev, path) {
    var threw = false;
    ev.target = target;
    for (var i = path.length - 1; i >= 1 && !ev._stop; i--) threw = invoke(path[i], ev, 1) || threw;
    if (!ev._stop) threw = invoke(path[0], ev, 2) || threw;
    if (ev.bubbles) for (var j = 1; j < path.length && !ev._stop; j++) threw = invoke(path[j], ev, 3) || threw;
    ev.eventPhase = 0; ev.currentTarget = null;
    return { threw: threw, canceled: ev.defaultPrevented };
  }
  function stringList(arr) {
    var l = arr.slice();
    Object.defineProperty(l, 'contains', { value: function (s) { return l.indexOf(String(s)) >= 0; } });
    Object.defineProperty(l, 'item', { value: function (i) { return i >= 0 && i < l.length ? l[i] : null; } });
    return l;
  }

  // ---- IDBRequest ---------------------------------------------------------------------------------
  function IDBRequest(tok, source, tx) {
    if (tok !== TOKEN) illegal();
    hide(this, { _result: undefined, _error: null, _source: source || null, _tx: tx || null, _done: false });
  }
  IDBRequest.prototype = Object.create(EvtTarget.prototype, { constructor: { value: IDBRequest, writable: true, configurable: true } });
  defAttrs(IDBRequest.prototype, {
    result: function () { if (!this._done) throw mkErr('InvalidStateError', "Failed to read the 'result' property from 'IDBRequest': The request has not finished."); return this._result; },
    error: function () { if (!this._done) throw mkErr('InvalidStateError', "Failed to read the 'error' property from 'IDBRequest': The request has not finished."); return this._error; },
    source: function () { return this._source; },
    transaction: function () { return this._tx; },
    readyState: function () { return this._done ? 'done' : 'pending'; }
  });
  handlers(IDBRequest.prototype, ['success', 'error']);
  tag(IDBRequest, 'IDBRequest');
  exportClass(IDBRequest, 'IDBRequest');
  function IDBOpenDBRequest(tok) { IDBRequest.call(this, tok); }
  IDBOpenDBRequest.prototype = Object.create(IDBRequest.prototype, { constructor: { value: IDBOpenDBRequest, writable: true, configurable: true } });
  handlers(IDBOpenDBRequest.prototype, ['blocked', 'upgradeneeded']);
  tag(IDBOpenDBRequest, 'IDBOpenDBRequest');
  exportClass(IDBOpenDBRequest, 'IDBOpenDBRequest');

  // ---- connections --------------------------------------------------------------------------------
  var dbs = new Map();       // conn id -> IDBDatabase
  var opens = new Map();     // request id -> {req, name, version, del}
  var txs = new Map();       // Go tx id -> IDBTransaction
  var reqSeq = 0;

  function IDBDatabase(tok, conn, name, version, meta) {
    if (tok !== TOKEN) illegal();
    hide(this, { _conn: conn, _name: name, _version: version, _closePending: false, _closed: false, _upgradeTx: null, _names: meta.stores.map(function (s) { return s.name; }).sort() });
    dbs.set(conn, this);
  }
  IDBDatabase.prototype = Object.create(EvtTarget.prototype, { constructor: { value: IDBDatabase, writable: true, configurable: true } });
  defAttrs(IDBDatabase.prototype, {
    name: function () { return this._name; },
    version: function () { return this._version; },
    objectStoreNames: function () { return stringList(this._names); }
  });
  handlers(IDBDatabase.prototype, ['abort', 'close', 'error', 'versionchange']);
  tag(IDBDatabase, 'IDBDatabase');
  exportClass(IDBDatabase, 'IDBDatabase');
  function closeDb(db) {
    if (db._closePending) return;
    db._closePending = true;
    try { H.idbClose(db._conn); } catch (e) { /* ignore */ }
  }
  defMethods(IDBDatabase.prototype, {
    close: function () { closeDb(this); },
    createObjectStore: function (name, options) {
      name = String(name);
      var tx = this._upgradeTx;
      if (!tx || tx._finished) throw mkErr('InvalidStateError', "Failed to execute 'createObjectStore' on 'IDBDatabase': The database is not running a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to execute 'createObjectStore' on 'IDBDatabase': The transaction is not active.");
      var kp = options ? normKeyPath(options.keyPath) : null, auto = !!(options && options.autoIncrement);
      if (kp !== null && !validKeyPath(kp)) throw mkErr('SyntaxError', "Failed to execute 'createObjectStore' on 'IDBDatabase': The keyPath option is not a valid key path.");
      if (this._names.indexOf(name) >= 0) throw mkErr('ConstraintError', "Failed to execute 'createObjectStore' on 'IDBDatabase': An object store with the specified name already exists.");
      if (auto && (kp === '' || Array.isArray(kp))) throw mkErr('InvalidAccessError', "Failed to execute 'createObjectStore' on 'IDBDatabase': The autoIncrement option was set but the keyPath option was empty or an array.");
      host('idbCreateStore', tx._id, name, kp === null ? '' : JSON.stringify(kp), kp !== null, auto);
      this._names.push(name); this._names.sort();
      tx._scope = this._names.slice();
      var st = new IDBObjectStore(TOKEN, tx, { name: name, keyPath: kp === null ? '' : JSON.stringify(kp), hasKeyPath: kp !== null, autoIncrement: auto, indexes: [] });
      tx._stores[name] = st;
      return st;
    },
    deleteObjectStore: function (name) {
      name = String(name);
      var tx = this._upgradeTx;
      if (!tx || tx._finished) throw mkErr('InvalidStateError', "Failed to execute 'deleteObjectStore' on 'IDBDatabase': The database is not running a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to execute 'deleteObjectStore' on 'IDBDatabase': The transaction is not active.");
      var i = this._names.indexOf(name);
      if (i < 0) throw mkErr('NotFoundError', "Failed to execute 'deleteObjectStore' on 'IDBDatabase': The specified object store was not found.");
      host('idbDeleteStore', tx._id, name);
      this._names.splice(i, 1);
      tx._scope = this._names.slice();
      if (tx._stores[name]) { tx._stores[name]._deleted = true; delete tx._stores[name]; }
    },
    transaction: function (storeNames, mode, options) {
      var names = typeof storeNames === 'string' ? [storeNames] : Array.prototype.slice.call(storeNames, 0).map(String);
      if (mode === undefined) mode = 'readonly';
      mode = String(mode);
      if (mode !== 'readonly' && mode !== 'readwrite' && mode !== 'versionchange') throw new TypeError("Failed to execute 'transaction' on 'IDBDatabase': The provided value '" + mode + "' is not a valid enum value of type IDBTransactionMode.");
      if (mode === 'versionchange') throw new TypeError("Failed to execute 'transaction' on 'IDBDatabase': The provided value 'versionchange' is not a valid enum value of type IDBTransactionMode.");
      if (this._upgradeTx && !this._upgradeTx._finished) throw mkErr('InvalidStateError', "Failed to execute 'transaction' on 'IDBDatabase': A version change transaction is running.");
      if (this._closePending) throw mkErr('InvalidStateError', "Failed to execute 'transaction' on 'IDBDatabase': The database connection is closing.");
      if (names.length === 0) throw mkErr('InvalidAccessError', "Failed to execute 'transaction' on 'IDBDatabase': The storeNames parameter was empty.");
      for (var i = 0; i < names.length; i++) if (this._names.indexOf(names[i]) < 0) throw mkErr('NotFoundError', "Failed to execute 'transaction' on 'IDBDatabase': One of the specified object stores was not found.");
      names = names.filter(function (n, ix) { return names.indexOf(n) === ix; }).sort();
      var r = host('idbBegin', this._conn, names, mode === 'readonly' ? 0 : 1);
      var tx = new IDBTransaction(TOKEN, this, names, mode, r[0], !!r[1]);
      if (options && options.durability) tx._durability = String(options.durability);
      return tx;
    }
  });

  // ---- transactions ------------------------------------------------------------------------------
  function IDBTransaction(tok, db, scope, mode, id, started) {
    if (tok !== TOKEN) illegal();
    hide(this, { _db: db, _scope: scope, _mode: mode, _id: id, _started: started, _state: 'active', _queue: [], _busy: false,
      _error: null, _finished: false, _aborted: false, _stores: {}, _durability: 'default', _upgradeReq: null });
    txs.set(id, this);
    var self = this;
    schedule(function () { if (!self._finished && self._state === 'active') { self._state = 'inactive'; settle(self); } });
  }
  IDBTransaction.prototype = Object.create(EvtTarget.prototype, { constructor: { value: IDBTransaction, writable: true, configurable: true } });
  defAttrs(IDBTransaction.prototype, {
    db: function () { return this._db; },
    mode: function () { return this._mode; },
    durability: function () { return this._durability; },
    error: function () { return this._error; },
    objectStoreNames: function () { return stringList(this._scope); }
  });
  handlers(IDBTransaction.prototype, ['abort', 'complete', 'error']);
  tag(IDBTransaction, 'IDBTransaction');
  exportClass(IDBTransaction, 'IDBTransaction');
  defMethods(IDBTransaction.prototype, {
    objectStore: function (name) {
      name = String(name);
      if (this._finished) throw mkErr('InvalidStateError', "Failed to execute 'objectStore' on 'IDBTransaction': The transaction has finished.");
      if (this._scope.indexOf(name) < 0) throw mkErr('NotFoundError', "Failed to execute 'objectStore' on 'IDBTransaction': The specified object store was not found.");
      var st = this._stores[name];
      if (!st) { st = new IDBObjectStore(TOKEN, this, host('idbStoreMeta', this._id, name)); this._stores[name] = st; }
      return st;
    },
    abort: function () {
      if (this._finished || this._state === 'committing') throw mkErr('InvalidStateError', "Failed to execute 'abort' on 'IDBTransaction': The transaction has already been committed or aborted.");
      abortTx(this, null);
    },
    commit: function () {
      if (this._state !== 'active' || this._finished) throw mkErr('InvalidStateError', "Failed to execute 'commit' on 'IDBTransaction': The transaction is not active.");
      this._state = 'inactive';
      settle(this);
    }
  });

  function kick(tx) {
    if (tx._started && !tx._busy && tx._queue.length && !tx._finished) {
      tx._busy = true;
      schedule(function () { step(tx); });
    }
  }
  // inactive, started, nothing queued: commit; something queued: process it
  function settle(tx) {
    if (tx._finished || tx._busy || !tx._started || tx._state === 'active' || tx._state === 'committing') return;
    if (tx._queue.length) { kick(tx); return; }
    commitTx(tx);
  }
  function step(tx) {
    tx._busy = false;
    if (tx._finished) return;
    tx._state = 'inactive';
    var item = tx._queue.shift();
    if (!item) { settle(tx); return; }
    // requests nobody listens to run back to back: no script ran, so there is no microtask to wait for
    for (;;) {
      var before = handlerCalls;
      runItem(tx, item);
      if (tx._finished) return;
      if (handlerCalls !== before || !tx._queue.length) break;
      item = tx._queue.shift();
    }
    tx._busy = true;
    schedule(function () { step(tx); });
  }
  function runItem(tx, item) {
    var req = item.req, result, err = null;
    try { result = item.exec(); } catch (e) { err = hostErr(e); if (!(err instanceof global.DOMException) && !(err && err.name)) { err = mkErr('UnknownError', String(err && err.message || err)); } }
    req._done = true;
    tx._state = 'active';
    var ev;
    if (err) {
      req._result = undefined; req._error = err;
      ev = new Evt('error', null, true, true);
    } else {
      req._result = result; req._error = null;
      ev = new Evt('success', null, false, false);
    }
    var path = [req, tx, tx._db];
    var r = dispatch(req, ev, path);
    if (tx._finished) return;
    if (r.threw) { abortTx(tx, mkErr('AbortError', 'An exception was thrown in an event handler.')); return; }
    if (err && !r.canceled) abortTx(tx, err);
  }
  function abortTx(tx, err) {
    if (tx._finished) return;
    tx._finished = true; tx._aborted = true; tx._state = 'finished'; tx._error = err;
    try { H.idbAbort(tx._id); } catch (e) { /* ignore */ }
    txs.delete(tx._id);
    var pending = tx._queue;
    tx._queue = [];
    pending.forEach(function (it) { it.req._done = true; it.req._error = mkErr('AbortError', 'The transaction was aborted, so the request cannot be fulfilled.'); });
    var up = tx._upgradeReq;
    if (up) tx._db._upgradeTx = null;
    schedule(function () {
      pending.forEach(function (it) { dispatch(it.req, new Evt('error', null, true, false), [it.req, tx, tx._db]); });
      dispatch(tx, new Evt('abort', null, true, false), [tx, tx._db]);
      if (up) {
        closeDb(tx._db);
        up.req._done = true; up.req._result = undefined; up.req._tx = null; up.req._error = mkErr('AbortError', 'The upgrade transaction was aborted.');
        dispatch(up.req, new Evt('error', null, true, false), [up.req]);
      }
    });
  }
  function commitTx(tx) {
    tx._state = 'committing';
    try { H.idbCommit(tx._id); } catch (e) { abortTx(tx, hostErr(e)); return; }
    tx._finished = true; tx._state = 'finished';
    txs.delete(tx._id);
    var up = tx._upgradeReq;
    if (up) tx._db._upgradeTx = null;
    var r = dispatch(tx, new Evt('complete', null, false, false), [tx]);
    void r;
    if (up) {
      schedule(function () {
        var req = up.req;
        if (tx._db._closePending) {
          req._done = true; req._result = undefined; req._tx = null; req._error = mkErr('AbortError', 'The connection was closed.');
          dispatch(req, new Evt('error', null, true, false), [req]);
          return;
        }
        req._tx = null; req._done = true; req._result = tx._db; req._error = null;
        dispatch(req, new Evt('success', null, false, false), [req]);
      });
    }
  }

  function checkActive(tx, what) {
    if (tx._finished || tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to execute '" + what + "' on 'IDBObjectStore': The transaction is " + (tx._finished ? 'finished.' : 'not active.'));
  }
  function place(source, tx, exec, req) {
    req = req || new IDBRequest(TOKEN, source, tx);
    req._done = false; req._result = undefined; req._error = null;
    tx._queue.push({ req: req, exec: exec });
    kick(tx);
    return req;
  }

  // ---- key ranges ---------------------------------------------------------------------------------
  function IDBKeyRange(tok, lower, upper, lo, hi, lopen, hopen) {
    if (tok !== TOKEN) illegal();
    hide(this, { _lo: lo, _hi: hi, _lower: lower, _upper: upper, _lopen: lopen, _hopen: hopen });
  }
  defAttrs(IDBKeyRange.prototype, {
    lower: function () { return this._lower; },
    upper: function () { return this._upper; },
    lowerOpen: function () { return this._lopen; },
    upperOpen: function () { return this._hopen; }
  });
  defMethods(IDBKeyRange.prototype, {
    includes: function (key) {
      var k = keyOrThrow(key);
      if (this._lo !== '' && (k < this._lo || (this._lopen && k === this._lo))) return false;
      if (this._hi !== '' && (k > this._hi || (this._hopen && k === this._hi))) return false;
      return true;
    }
  });
  defMethods(IDBKeyRange, {
    only: function (v) { var k = keyOrThrow(v); return new IDBKeyRange(TOKEN, v, v, k, k, false, false); },
    lowerBound: function (v, open) { return new IDBKeyRange(TOKEN, v, undefined, keyOrThrow(v), '', !!open, true); },
    upperBound: function (v, open) { return new IDBKeyRange(TOKEN, undefined, v, '', keyOrThrow(v), true, !!open); },
    bound: function (l, u, lo, uo) {
      var a = keyOrThrow(l), b = keyOrThrow(u);
      if (a > b || (a === b && (lo || uo))) throw mkErr('DataError', "Failed to execute 'bound' on 'IDBKeyRange': The lower key is greater than the upper key.");
      return new IDBKeyRange(TOKEN, l, u, a, b, !!lo, !!uo);
    }
  });
  tag(IDBKeyRange, 'IDBKeyRange');
  exportClass(IDBKeyRange, 'IDBKeyRange');
  // query argument -> [lo, hi, loOpen, hiOpen] (encoded); null when absent and not required
  function rangeOf(q, required) {
    if (q === undefined || q === null) {
      if (required) throw mkErr('DataError', 'The parameter is not a valid key.');
      return ['', '', false, false];
    }
    if (q instanceof IDBKeyRange) return [q._lo, q._hi, q._lopen, q._hopen];
    var k = keyOrThrow(q);
    return [k, k, false, false];
  }

  // ---- object stores ---------------------------------------------------------------------------
  function IDBObjectStore(tok, tx, meta) {
    if (tok !== TOKEN) illegal();
    var kp = meta.hasKeyPath ? JSON.parse(meta.keyPath) : null;
    hide(this, { _tx: tx, _name: meta.name, _kp: kp, _auto: !!meta.autoIncrement, _deleted: false, _indexes: {}, _inames: meta.indexes.map(function (i) { return i.name; }), _imeta: {} });
    var self = this;
    meta.indexes.forEach(function (i) { self._imeta[i.name] = i; });
  }
  defAttrs(IDBObjectStore.prototype, {
    name: function () { return this._name; },
    keyPath: function () { return this._kp; },
    indexNames: function () { return stringList(this._inames.slice().sort()); },
    transaction: function () { return this._tx; },
    autoIncrement: function () { return this._auto; }
  });
  Object.defineProperty(IDBObjectStore.prototype, 'name', {
    get: function () { return this._name; },
    set: function (n) {
      n = String(n);
      var tx = this._tx;
      if (this._deleted || !tx._db._upgradeTx || tx._mode !== 'versionchange') throw mkErr('InvalidStateError', "Failed to set the 'name' property on 'IDBObjectStore': The object store is not in a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to set the 'name' property on 'IDBObjectStore': The transaction is not active.");
      if (n === this._name) return;
      host('idbRenameStore', tx._id, this._name, n);
      var names = tx._db._names, i = names.indexOf(this._name);
      if (i >= 0) names[i] = n;
      names.sort();
      tx._scope = names.slice();
      delete tx._stores[this._name];
      this._name = n; tx._stores[n] = this;
    },
    enumerable: true, configurable: true
  });
  tag(IDBObjectStore, 'IDBObjectStore');
  exportClass(IDBObjectStore, 'IDBObjectStore');

  function storeCheck(st, what, write) {
    if (st._deleted) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on 'IDBObjectStore': The object store has been deleted.");
    checkActive(st._tx, what);
    if (write && st._tx._mode === 'readonly') throw mkErr('ReadOnlyError', "Failed to execute '" + what + "' on 'IDBObjectStore': The transaction is read-only.");
  }
  function indexMetas(st) { return st._inames.map(function (n) { return st._imeta[n]; }); }
  // index keys of a value, as {indexName: [encoded keys]}
  function indexKeysOf(st, value) {
    var out = {}, any = false;
    indexMetas(st).forEach(function (m) {
      var kp = m._kp || (m._kp = JSON.parse(m.keyPath)), r = extractKey(value, kp), keys = [];
      if (r.ok) {
        if (m.multiEntry && Array.isArray(r.v)) {
          var seen = {};
          r.v.forEach(function (x) { var k = encodeKey(x); if (k !== undefined && !seen[k]) { seen[k] = 1; keys.push(k); } });
        } else {
          var k2 = encodeKey(r.v);
          if (k2 !== undefined) keys.push(k2);
        }
      }
      if (keys.length) { out[m.name] = keys; any = true; }
    });
    return any ? out : {};
  }
  function writeOp(st, what, value, key, noOverwrite) {
    storeCheck(st, what, true);
    var kp = st._kp;
    if (kp !== null && key !== undefined) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBObjectStore': The object store uses in-line keys and the key parameter was provided.");
    if (kp === null && !st._auto && key === undefined) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBObjectStore': The object store uses out-of-line keys and has no key generator and the key parameter was not provided.");
    var enc = '';
    if (key !== undefined) enc = keyOrThrow(key);
    var s = ser(value), needGen = false;
    if (kp !== null) {
      var r = extractKey(value, kp);
      if (r.ok) {
        enc = encodeKey(r.v);
        if (enc === undefined) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBObjectStore': The key path yielded a value that is not a valid key.");
      } else if (st._auto) {
        if (typeof kp !== 'string' || !canInject(value, kp)) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBObjectStore': A generated key could not be inserted into the value.");
        needGen = true;
      } else throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBObjectStore': Evaluating the object store's key path did not yield a value.");
    }
    var tx = st._tx, name = st._name, ik = needGen ? null : indexKeysOf(st, value);
    return place(st, tx, function () {
      var k = enc, val = s;
      if (needGen) {
        k = host('idbGenKey', tx._id, name);
        var c = de(s);
        inject(c, kp, decodeKey(k));
        val = ser(c);
        ik = indexKeysOf(st, c);
      }
      return decodeKey(host('idbPut', tx._id, name, k, val, noOverwrite, ik));
    });
  }
  function readOp(st, index, what, kind, query, count, convert, required) {
    var tx = st._tx, name = st._name, ixName = index ? index._name : '';
    var rg;
    if (index) { if (index._deleted) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on 'IDBIndex': The index has been deleted."); }
    if (st._deleted) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on '" + (index ? 'IDBIndex' : 'IDBObjectStore') + "': The object store has been deleted.");
    checkActive(tx, what);
    rg = rangeOf(query, required);
    if (count !== undefined && count !== null) { count = Number(count); if (!(count >= 0) || count > 4294967295) throw new TypeError("Failed to execute '" + what + "': Value is outside the 'unsigned long' value range."); count = Math.floor(count); } else count = 0;
    return place(index || st, tx, function () {
      return convert(host('idbQuery', tx._id, name, ixName, kind, rg[0], rg[1], rg[2], rg[3], count));
    });
  }
  var cvValue = function (r) { return r.length ? de(r[0]) : undefined; };
  var cvKey = function (r) { return r.length ? decodeKey(r[0]) : undefined; };
  var cvValues = function (r) { return r.map(de); };
  var cvKeys = function (r) { return r.map(decodeKey); };
  var cvCount = function (r) { return Number(r[0]); };
  defMethods(IDBObjectStore.prototype, {
    put: function (value, key) { return writeOp(this, 'put', value, key, false); },
    add: function (value, key) { return writeOp(this, 'add', value, key, true); },
    delete: function (query) {
      var st = this;
      storeCheck(st, 'delete', true);
      var rg = rangeOf(query, true), tx = st._tx;
      return place(st, tx, function () { host('idbDelRange', tx._id, st._name, rg[0], rg[1], rg[2], rg[3]); return undefined; });
    },
    clear: function () {
      var st = this;
      storeCheck(st, 'clear', true);
      var tx = st._tx;
      return place(st, tx, function () { host('idbClear', tx._id, st._name); return undefined; });
    },
    get: function (q) { return readOp(this, null, 'get', 'v', q, 1, cvValue, true); },
    getKey: function (q) { return readOp(this, null, 'getKey', 'k', q, 1, cvKey, true); },
    getAll: function (q, count) { return readOp(this, null, 'getAll', 'V', q, count, cvValues, false); },
    getAllKeys: function (q, count) { return readOp(this, null, 'getAllKeys', 'K', q, count, cvKeys, false); },
    count: function (q) { return readOp(this, null, 'count', 'n', q, 0, cvCount, false); },
    openCursor: function (q, dir) { return openCursor(this, null, q, dir, false); },
    openKeyCursor: function (q, dir) { return openCursor(this, null, q, dir, true); },
    index: function (name) {
      name = String(name);
      if (this._deleted || this._tx._finished) throw mkErr('InvalidStateError', "Failed to execute 'index' on 'IDBObjectStore': The " + (this._deleted ? 'object store has been deleted.' : 'transaction has finished.'));
      if (this._inames.indexOf(name) < 0) throw mkErr('NotFoundError', "Failed to execute 'index' on 'IDBObjectStore': The specified index was not found.");
      var ix = this._indexes[name];
      if (!ix) { ix = new IDBIndex(TOKEN, this, this._imeta[name]); this._indexes[name] = ix; }
      return ix;
    },
    createIndex: function (name, keyPath, options) {
      name = String(name);
      var st = this, tx = st._tx, db = tx._db;
      if (st._deleted || tx._mode !== 'versionchange' || !db._upgradeTx) throw mkErr('InvalidStateError', "Failed to execute 'createIndex' on 'IDBObjectStore': The database is not running a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to execute 'createIndex' on 'IDBObjectStore': The transaction is not active.");
      if (st._inames.indexOf(name) >= 0) throw mkErr('ConstraintError', "Failed to execute 'createIndex' on 'IDBObjectStore': An index with the specified name already exists.");
      var kp = normKeyPath(keyPath);
      if (kp === null || !validKeyPath(kp)) throw mkErr('SyntaxError', "Failed to execute 'createIndex' on 'IDBObjectStore': The keyPath argument contains an invalid key path.");
      var unique = !!(options && options.unique), multi = !!(options && options.multiEntry);
      if (multi && Array.isArray(kp)) throw mkErr('InvalidAccessError', "Failed to execute 'createIndex' on 'IDBObjectStore': The keyPath argument was an array and the multiEntry option is true.");
      var meta = { name: name, keyPath: JSON.stringify(kp), unique: unique, multiEntry: multi };
      // index the records already in the store
      var flat = host('idbAllRecords', tx._id, st._name), entries = [];
      var probe = { _inames: [name], _imeta: {} };
      probe._imeta[name] = meta;
      for (var i = 0; i < flat.length; i += 2) {
        var ks = indexKeysOf(probe, de(flat[i + 1]))[name];
        if (ks) entries.push([flat[i], ks]);
      }
      var failure = null;
      try { host('idbCreateIndex', tx._id, st._name, name, meta.keyPath, unique, multi, entries); } catch (e) { failure = e; }
      st._inames.push(name); st._imeta[name] = meta;
      var ix = new IDBIndex(TOKEN, st, meta);
      st._indexes[name] = ix;
      if (failure) {
        // the index could not be built (unique constraint): the upgrade fails once the handler returns
        ix._deleted = true;
        schedule(function () { abortTx(tx, failure); });
      }
      return ix;
    },
    deleteIndex: function (name) {
      name = String(name);
      var st = this, tx = st._tx;
      if (st._deleted || tx._mode !== 'versionchange' || !tx._db._upgradeTx) throw mkErr('InvalidStateError', "Failed to execute 'deleteIndex' on 'IDBObjectStore': The database is not running a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to execute 'deleteIndex' on 'IDBObjectStore': The transaction is not active.");
      var i = st._inames.indexOf(name);
      if (i < 0) throw mkErr('NotFoundError', "Failed to execute 'deleteIndex' on 'IDBObjectStore': The specified index was not found.");
      host('idbDeleteIndex', tx._id, st._name, name);
      st._inames.splice(i, 1);
      delete st._imeta[name];
      if (st._indexes[name]) { st._indexes[name]._deleted = true; delete st._indexes[name]; }
    }
  });

  // ---- indexes ------------------------------------------------------------------------------------
  function IDBIndex(tok, store, meta) {
    if (tok !== TOKEN) illegal();
    hide(this, { _store: store, _name: meta.name, _meta: meta, _deleted: false });
  }
  defAttrs(IDBIndex.prototype, {
    objectStore: function () { return this._store; },
    keyPath: function () { return JSON.parse(this._meta.keyPath); },
    multiEntry: function () { return this._meta.multiEntry; },
    unique: function () { return this._meta.unique; }
  });
  Object.defineProperty(IDBIndex.prototype, 'name', {
    get: function () { return this._name; },
    set: function (n) {
      n = String(n);
      var st = this._store, tx = st._tx;
      if (this._deleted || st._deleted || tx._mode !== 'versionchange') throw mkErr('InvalidStateError', "Failed to set the 'name' property on 'IDBIndex': The index is not in a version change transaction.");
      if (tx._state !== 'active') throw mkErr('TransactionInactiveError', "Failed to set the 'name' property on 'IDBIndex': The transaction is not active.");
      if (n === this._name) return;
      host('idbRenameIndex', tx._id, st._name, this._name, n);
      var i = st._inames.indexOf(this._name);
      st._inames[i] = n;
      delete st._imeta[this._name]; delete st._indexes[this._name];
      this._meta.name = n; st._imeta[n] = this._meta; st._indexes[n] = this;
      this._name = n;
    },
    enumerable: true, configurable: true
  });
  defMethods(IDBIndex.prototype, {
    get: function (q) { return readOp(this._store, this, 'get', 'v', q, 1, cvValue, true); },
    getKey: function (q) { return readOp(this._store, this, 'getKey', 'k', q, 1, cvKey, true); },
    getAll: function (q, count) { return readOp(this._store, this, 'getAll', 'V', q, count, cvValues, false); },
    getAllKeys: function (q, count) { return readOp(this._store, this, 'getAllKeys', 'K', q, count, cvKeys, false); },
    count: function (q) { return readOp(this._store, this, 'count', 'n', q, 0, cvCount, false); },
    openCursor: function (q, dir) { return openCursor(this._store, this, q, dir, false); },
    openKeyCursor: function (q, dir) { return openCursor(this._store, this, q, dir, true); }
  });
  tag(IDBIndex, 'IDBIndex');
  exportClass(IDBIndex, 'IDBIndex');

  // ---- cursors -----------------------------------------------------------------------------------------
  var DIRS = { next: 0, nextunique: 1, prev: 2, prevunique: 3 };
  function IDBCursor(tok, req, store, index, dir, rg, keyOnly) {
    if (tok !== TOKEN) illegal();
    hide(this, { _req: req, _store: store, _index: index, _dir: String(dir), _rg: rg, _keyOnly: keyOnly, _key: undefined, _pk: undefined, _val: undefined, _kEnc: '', _pEnc: '', _got: false, _has: false });
  }
  function IDBCursorWithValue(tok) { IDBCursor.apply(this, arguments); }
  IDBCursorWithValue.prototype = Object.create(IDBCursor.prototype, { constructor: { value: IDBCursorWithValue, writable: true, configurable: true } });
  defAttrs(IDBCursor.prototype, {
    source: function () { return this._index || this._store; },
    direction: function () { return this._dir; },
    key: function () { return this._key; },
    primaryKey: function () { return this._pk; },
    request: function () { return this._req; }
  });
  defAttrs(IDBCursorWithValue.prototype, { value: function () { return this._val; } });
  tag(IDBCursor, 'IDBCursor'); tag(IDBCursorWithValue, 'IDBCursorWithValue');
  exportClass(IDBCursor, 'IDBCursor'); exportClass(IDBCursorWithValue, 'IDBCursorWithValue');

  // one cursor step as a queued operation on the cursor's request
  function stepCursor(c, target, targetPK, advance) {
    var st = c._store, tx = st._tx, ix = c._index, req = c._req, dir = DIRS[c._dir];
    return place(ix || st, tx, function () {
      var r = host('idbCursor', tx._id, st._name, ix ? ix._name : '', c._rg[0], c._rg[1], c._rg[2], c._rg[3], dir,
        c._has, c._kEnc, c._pEnc, target, targetPK, advance, c._keyOnly);
      if (!r.length) { c._got = false; c._key = c._pk = c._val = undefined; return null; }
      c._has = true; c._kEnc = r[0]; c._pEnc = r[1];
      c._key = decodeKey(r[0]); c._pk = ix ? decodeKey(r[1]) : c._key;
      c._val = c._keyOnly ? undefined : de(r[2]);
      c._got = true;
      return c;
    }, req);
  }
  function openCursor(st, index, q, dir, keyOnly) {
    var what = keyOnly ? 'openKeyCursor' : 'openCursor';
    if (index && index._deleted) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on 'IDBIndex': The index has been deleted.");
    if (st._deleted) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on '" + (index ? 'IDBIndex' : 'IDBObjectStore') + "': The object store has been deleted.");
    checkActive(st._tx, what);
    var rg = rangeOf(q, false);
    dir = dir === undefined ? 'next' : String(dir);
    if (!(dir in DIRS)) throw new TypeError("Failed to execute '" + what + "': The provided value '" + dir + "' is not a valid enum value of type IDBCursorDirection.");
    var req = new IDBRequest(TOKEN, index || st, st._tx);
    var C = keyOnly ? IDBCursor : IDBCursorWithValue;
    var c = new C(TOKEN, req, st, index, dir, rg, keyOnly);
    return stepCursor(c, '', '', 1);
  }
  function cursorMove(c, what, key, pkey, count) {
    var tx = c._store._tx;
    if (c._store._deleted || (c._index && c._index._deleted)) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on 'IDBCursor': The cursor's source has been deleted.");
    checkActive(tx, what);
    if (!c._got) throw mkErr('InvalidStateError', "Failed to execute '" + what + "' on 'IDBCursor': The cursor is being iterated or has iterated past its end.");
    var target = '', tpk = '', forward = c._dir === 'next' || c._dir === 'nextunique';
    if (key !== undefined) {
      target = keyOrThrow(key);
      if (pkey === undefined && (forward ? target <= c._kEnc : target >= c._kEnc)) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBCursor': The parameter is less than or equal to this cursor's position.");
      if (pkey !== undefined) {
        tpk = keyOrThrow(pkey);
        if (forward ? (target < c._kEnc || (target === c._kEnc && tpk <= c._pEnc)) : (target > c._kEnc || (target === c._kEnc && tpk >= c._pEnc))) throw mkErr('DataError', "Failed to execute '" + what + "' on 'IDBCursor': The parameters are less than or equal to this cursor's position.");
      }
    }
    c._got = false;
    return stepCursor(c, target, tpk, count || 1);
  }
  defMethods(IDBCursor.prototype, {
    continue: function (key) { return void cursorMove(this, 'continue', key, undefined, 1); },
    continuePrimaryKey: function (key, pkey) {
      if (!this._index) throw mkErr('InvalidAccessError', "Failed to execute 'continuePrimaryKey' on 'IDBCursor': The cursor's source is not an index.");
      if (this._dir !== 'next' && this._dir !== 'prev') throw mkErr('InvalidAccessError', "Failed to execute 'continuePrimaryKey' on 'IDBCursor': The cursor's direction is not 'next' or 'prev'.");
      if (key === undefined || pkey === undefined) throw mkErr('DataError', "Failed to execute 'continuePrimaryKey' on 'IDBCursor': A key and a primary key are required.");
      return void cursorMove(this, 'continuePrimaryKey', key, pkey, 1);
    },
    advance: function (n) {
      n = Number(n);
      if (!(n >= 1) || n > 4294967295) throw new TypeError("Failed to execute 'advance' on 'IDBCursor': A count argument with value 0 (zero) was supplied, must be greater than 0.");
      return void cursorMove(this, 'advance', undefined, undefined, Math.floor(n));
    },
    update: function (value) {
      var c = this, st = c._store, tx = st._tx;
      if (st._deleted || (c._index && c._index._deleted)) throw mkErr('InvalidStateError', "Failed to execute 'update' on 'IDBCursor': The cursor's source has been deleted.");
      checkActive(tx, 'update');
      if (tx._mode === 'readonly') throw mkErr('ReadOnlyError', "Failed to execute 'update' on 'IDBCursor': The transaction is read-only.");
      if (!c._got || c._keyOnly) throw mkErr('InvalidStateError', "Failed to execute 'update' on 'IDBCursor': The cursor is " + (c._keyOnly ? 'a key cursor.' : 'being iterated or has iterated past its end.'));
      var s = ser(value), pk = c._pEnc, name = st._name;
      if (st._kp !== null) {
        var r = extractKey(value, st._kp);
        if (!r.ok || encodeKey(r.v) !== pk) throw mkErr('DataError', "Failed to execute 'update' on 'IDBCursor': The effective key of the value does not match the cursor's primary key.");
      }
      var ik = indexKeysOf(st, value);
      return place(c, tx, function () { return decodeKey(host('idbPut', tx._id, name, pk, s, false, ik)); });
    },
    delete: function () {
      var c = this, st = c._store, tx = st._tx;
      if (st._deleted || (c._index && c._index._deleted)) throw mkErr('InvalidStateError', "Failed to execute 'delete' on 'IDBCursor': The cursor's source has been deleted.");
      checkActive(tx, 'delete');
      if (tx._mode === 'readonly') throw mkErr('ReadOnlyError', "Failed to execute 'delete' on 'IDBCursor': The transaction is read-only.");
      if (!c._got || c._keyOnly) throw mkErr('InvalidStateError', "Failed to execute 'delete' on 'IDBCursor': The cursor is " + (c._keyOnly ? 'a key cursor.' : 'being iterated or has iterated past its end.'));
      var pk = c._pEnc, name = st._name;
      return place(c, tx, function () { host('idbDelRange', tx._id, name, pk, pk, false, false); return undefined; });
    }
  });

  // ---- the factory ---------------------------------------------------------------------------------
  function checkAvail() { if (!H.idbAvail()) throw mkErr('SecurityError', "Failed to execute on 'IDBFactory': access to the database is denied for this document's origin."); }
  function toVersion(v) {
    var n = Number(v);
    if (n !== n || n === Infinity || n === -Infinity) throw new TypeError("Failed to execute 'open' on 'IDBFactory': Value is not of type 'unsigned long long'.");
    n = Math.trunc(n);
    if (n < 1 || n > 9007199254740991) throw new TypeError("Failed to execute 'open' on 'IDBFactory': Value is outside the 'unsigned long long' value range" + (n === 0 ? ' (the version must be a positive integer).' : '.'));
    return n;
  }
  function IDBFactory(tok) { if (tok !== TOKEN) illegal(); }
  defMethods(IDBFactory.prototype, {
    open: function (name, version) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'open' on 'IDBFactory': 1 argument required, but only 0 present.");
      name = String(name);
      var v = version === undefined ? 0 : toVersion(version);
      checkAvail();
      var req = new IDBOpenDBRequest(TOKEN), id = ++reqSeq;
      opens.set(id, { req: req, name: name });
      host('idbOpen', id, name, v);
      return req;
    },
    deleteDatabase: function (name) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'deleteDatabase' on 'IDBFactory': 1 argument required, but only 0 present.");
      name = String(name);
      checkAvail();
      var req = new IDBOpenDBRequest(TOKEN), id = ++reqSeq;
      opens.set(id, { req: req, name: name, del: true });
      host('idbDelete', id, name);
      return req;
    },
    databases: function () {
      try { checkAvail(); } catch (e) { return Promise.reject(e); }
      return new Promise(function (resolve) {
        resolve(host('idbDatabases').map(function (p) { return { name: p[0], version: p[1] }; }));
      });
    },
    cmp: function (a, b) {
      if (arguments.length < 2) throw new TypeError("Failed to execute 'cmp' on 'IDBFactory': 2 arguments required, but only " + arguments.length + ' present.');
      var x = keyOrThrow(a), y = keyOrThrow(b);
      return x < y ? -1 : x > y ? 1 : 0;
    }
  });
  tag(IDBFactory, 'IDBFactory');
  exportClass(IDBFactory, 'IDBFactory');
  var factory = new IDBFactory(TOKEN);

  // ---- notifications from Go ----------------------------------------------------------------------------
  function fire(target, ev, path) { return dispatch(target, ev, path || [target]); }
  var notify = {
    'open-error': function (m) {
      var o = opens.get(m.req); opens.delete(m.req);
      if (!o) return;
      o.req._done = true; o.req._error = mkErr(m.name, m.msg);
      fire(o.req, new Evt('error', null, true, true));
    },
    'open-ready': function (m) {
      var o = opens.get(m.req); opens.delete(m.req);
      if (!o) { try { H.idbClose(m.conn); } catch (e) { /* ignore */ } return; }
      var req = o.req, meta = host('idbMeta', m.conn);
      var db = new IDBDatabase(TOKEN, m.conn, o.name, m.version, meta);
      if (!m.upgrade) {
        req._done = true; req._result = db;
        var r = fire(req, new Evt('success', null, false, false));
        void r;
        return;
      }
      var tx = new IDBTransaction(TOKEN, db, db._names.slice(), 'versionchange', m.tx, true);
      tx._upgradeReq = { req: req };
      db._upgradeTx = tx;
      req._done = true; req._result = db; req._tx = tx;
      var res = fire(req, new IDBVersionChangeEvent(TOKEN, 'upgradeneeded', m.oldVersion, m.version));
      if (res.threw) abortTx(tx, mkErr('AbortError', 'An exception was thrown in the upgradeneeded handler.'));
    },
    'blocked': function (m) {
      var o = opens.get(m.req);
      if (o) fire(o.req, new IDBVersionChangeEvent(TOKEN, 'blocked', m.oldVersion, m.newVersion === undefined ? null : m.newVersion));
    },
    'delete-done': function (m) {
      var o = opens.get(m.req); opens.delete(m.req);
      if (!o) return;
      o.req._done = true; o.req._result = undefined;
      fire(o.req, new IDBVersionChangeEvent(TOKEN, 'success', m.oldVersion, null));
    },
    'versionchange': function (m) {
      var db = dbs.get(m.conn);
      if (db && !db._closePending) fire(db, new IDBVersionChangeEvent(TOKEN, 'versionchange', m.oldVersion, m.newVersion === undefined ? null : m.newVersion));
      try { H.idbVcDone(m.conn); } catch (e) { /* ignore */ }
    },
    'txstart': function (m) {
      var tx = txs.get(m.tx);
      if (tx) { tx._started = true; settle(tx); }
    },
    'closed': function (m) {
      var db = dbs.get(m.conn);
      if (!db || db._closed) return;
      db._closed = true; db._closePending = true;
      txs.forEach(function (tx) { if (tx._db === db) abortTx(tx, mkErr('AbortError', 'The connection was closed by the browser.')); });
      fire(db, new Evt('close', null, false, false));
    }
  };
  Object.defineProperty(global, '__swStorageEvent', { value: function (json) {
    var m = typeof json === 'string' ? JSON.parse(json) : json, f = notify[m.t];
    if (f) { try { f(m); } catch (e) { report(e); } }
  } });

  // ---- globals -----------------------------------------------------------------------------------------------
  Object.defineProperty(global, 'indexedDB', { get: function () { return factory; }, set: function () { }, configurable: true, enumerable: true });

  // ---- navigator.storage ----------------------------------------------------------------------------------
  function StorageManager(tok) { if (tok !== TOKEN) illegal(); }
  defMethods(StorageManager.prototype, {
    estimate: function () {
      return new Promise(function (resolve) {
        var e = H.storageEstimate();
        resolve({ usage: e.usage, quota: e.quota, usageDetails: { indexedDB: e.indexedDB, caches: e.caches, serviceWorkerRegistrations: e.serviceWorkers } });
      });
    },
    persist: function () { return new Promise(function (resolve) { resolve(!!H.storagePersist()); }); },
    persisted: function () { return new Promise(function (resolve) { resolve(!!H.storagePersisted()); }); },
    getDirectory: function () { return Promise.reject(mkErr('NotSupportedError', 'The Origin Private File System is not supported.')); }
  });
  tag(StorageManager, 'StorageManager');
  exportClass(StorageManager, 'StorageManager');
  var storageMgr = new StorageManager(TOKEN);
  if (global.Navigator) {
    Object.defineProperty(global.Navigator.prototype, 'storage', { get: function () { return storageMgr; }, enumerable: true, configurable: true });
  }
  // localhost and 127.0.0.1 are potentially trustworthy origins
  if (typeof H.secureContext === 'function') {
    Object.defineProperty(global, 'isSecureContext', { get: function () { return !!H.secureContext(); }, enumerable: true, configurable: true });
  }
})(globalThis, globalThis.__sw);
