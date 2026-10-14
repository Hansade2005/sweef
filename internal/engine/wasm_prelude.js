/*
 * WebAssembly JavaScript API for Sweef. Loaded lazily the first time a page (or worker) touches `WebAssembly`;
 * wasm.go registers the host functions it talks to as globals __swWasm_<name> just before this runs.
 *
 * Everything observable here follows the WebAssembly JS API spec as far as the engine allows. What is
 * deliberately missing (Tag/Exception, externref/v128 values, ref-typed tables, memory64) is listed in
 * docs/webassembly.md. Threads (shared memories, atomics, posting Modules and Memories to workers) are
 * wasm_threads.go.
 */
(function (global) {
  'use strict';
  var NAMES = ['instantiate', 'call', 'errInfo', 'customCount', 'memNew', 'memGrow', 'memPages',
    'tabNew', 'tabLen', 'tabGrow', 'tabSet', 'tabGet', 'globNew', 'globGet', 'globSet', 'compile', 'validate',
    'memBuf', 'custom', 'setDispatch', 'memShare', 'memOpen', 'modShare', 'modOpen'];
  var H = {};
  NAMES.forEach(function (n) {
    H[n] = global['__swWasm_' + n];
    try { delete global['__swWasm_' + n]; } catch (e) { /* ignore */ }
  });
  if (typeof H.compile !== 'function') return;

  var ERR = '\u0000wasm-error'; // wasm.go: wasmErrSentinel
  var lastThrown;

  // ---- errors ---------------------------------------------------------------------------------------------
  function defError(name) {
    var C = class extends Error { constructor(message, options) { super(message, options); } };
    Object.defineProperty(C, 'name', { value: name, configurable: true });
    Object.defineProperty(C.prototype, 'name', { value: name, writable: true, configurable: true });
    return C;
  }
  var CompileError = defError('CompileError'), LinkError = defError('LinkError'), RuntimeError = defError('RuntimeError');

  function trap() {
    var info = H.errInfo(), k = info.charCodeAt(0), msg = info.slice(2);
    if (k === 84) { var e = lastThrown; lastThrown = undefined; throw e; } // 'T': a JS exception crossed wasm frames
    if (k === 83) throw new RangeError(msg); // 'S': stack overflow
    throw new RuntimeError(msg);
  }

  // Go reports argument-level failures as "<ErrorClass>:<message>".
  function host(fn) {
    try { return fn(); } catch (e) {
      var m = e && /^(RangeError|TypeError|DataCloneError):([\s\S]*)$/.exec(String(e.message));
      if (m && m[1] === 'DataCloneError') throw new global.DOMException(m[2], 'DataCloneError');
      if (m) throw new (m[1] === 'RangeError' ? RangeError : TypeError)(m[2]);
      throw e;
    }
  }

  // ---- helpers --------------------------------------------------------------------------------------------
  function bufferSource(src, who) {
    if (src instanceof ArrayBuffer) return [src, 0, src.byteLength];
    if (ArrayBuffer.isView(src)) return [src.buffer, src.byteOffset, src.byteLength];
    throw new TypeError(who + ': Argument 0 must be a buffer source');
  }
  function toU32(v, name, who, max) {
    var n = Number(v);
    if (!isFinite(n) || n < 0 || n > 4294967295) throw new TypeError(who + ": Property '" + name + "': value " + String(v) + ' is not a valid number');
    n = Math.floor(n);
    if (max !== undefined && n > max) throw new RangeError(who + ": Property '" + name + "': value " + n + ' is above the upper bound ' + max);
    return n;
  }
  function toI64(x) {
    if (typeof x === 'bigint') return BigInt.asIntN(64, x);
    if (typeof x === 'number' || typeof x === 'symbol') throw new TypeError('Cannot convert ' + String(x) + ' to a BigInt');
    return BigInt.asIntN(64, BigInt(x));
  }
  var CONV = {
    i: function (x) { return x | 0; },
    l: toI64,
    f: function (x) { return Math.fround(x); },
    d: function (x) { return +x; }
  };
  var TYPE_OF = { i32: 'i', i64: 'l', f32: 'f', f64: 'd' }, NAME_OF = { i: 'i32', l: 'i64', f: 'f32', d: 'f64' };
  function def(o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true, enumerable: false }); }
  function tag(C, name) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: name, configurable: true }); }

  // ---- Module ---------------------------------------------------------------------------------------------
  var modInfo = new WeakMap();
  function infoOf(m, who) {
    var i = modInfo.get(m);
    if (!i) throw new TypeError(who + ': Argument 0 must be a WebAssembly.Module');
    return i;
  }
  function compileBytes(src, who) {
    var b = bufferSource(src, who), r = H.compile(b[0], b[1], b[2]);
    if (r.e) throw new CompileError(who + ': ' + r.e);
    return r;
  }
  class Module {
    constructor(bytes) {
      if (arguments.length < 1) throw new TypeError('WebAssembly.Module(): Argument 0 must be a buffer source');
      modInfo.set(this, compileBytes(bytes, 'WebAssembly.Module()'));
    }
    static imports(m) {
      return infoOf(m, 'WebAssembly.Module.imports()').imports.map(function (i) { return { module: i.m, name: i.n, kind: i.k }; });
    }
    static exports(m) {
      return infoOf(m, 'WebAssembly.Module.exports()').exports.map(function (e) { return { name: e.n, kind: e.k }; });
    }
    static customSections(m, name) {
      var i = infoOf(m, 'WebAssembly.Module.customSections()');
      if (name === undefined) throw new TypeError('WebAssembly.Module.customSections(): Argument 1 is required');
      name = String(name);
      var out = [], n = H.customCount(i.id, name);
      for (var k = 0; k < n; k++) out.push(H.custom(i.id, name, k));
      return out;
    }
  }
  tag(Module, 'WebAssembly.Module');

  // ---- registries -----------------------------------------------------------------------------------------
  var memKeys = new WeakMap(), tabKeys = new WeakMap(), globKeys = new WeakMap();
  var memObjs = new Map(), tabObjs = new Map(), globObjs = new Map();
  var emptyBufs = new Map();
  var fnMeta = new WeakMap();   // exported function -> {tok, idx, sig} | {ref, refVal, sig}
  var refFns = new Map();       // Table.get results by reference id
  var regs = new Map();         // instance token -> {fns, sigs} for wasm -> JS calls
  var tokSeq = 0;

  function memOf(key) {
    var m = memObjs.get(key);
    if (!m) { m = Object.create(Memory.prototype); memKeys.set(m, key); memObjs.set(key, m); }
    return m;
  }
  function tabOf(key) {
    var t = tabObjs.get(key);
    if (!t) { t = Object.create(Table.prototype); tabKeys.set(t, key); tabObjs.set(key, t); }
    return t;
  }
  function globOf(key, t, mut) {
    var g = globObjs.get(key);
    if (!g) { g = Object.create(Global.prototype); globKeys.set(g, { key: key, t: t, mut: mut }); globObjs.set(key, g); }
    return g;
  }

  // ---- exported functions ---------------------------------------------------------------------------------
  function makeFn(sig, fid) {
    var cut = sig.indexOf('>'), ps = sig.slice(0, cut), rs = sig.slice(cut + 1), n = ps.length, f;
    if (sig.indexOf('x') >= 0) return function () { throw new TypeError('type incompatibility when transforming from/to JS'); };
    var c0 = CONV[ps[0]], c1 = CONV[ps[1]], c2 = CONV[ps[2]], call = H.call, none = rs.length === 0;
    var conv = ps.split('').map(function (c) { return CONV[c]; });
    var fin = function (r) { if (r === ERR) trap(); return none ? undefined : r; };
    if (n === 0) f = function () { return fin(call(fid)); };
    else if (n === 1) f = function (a) { return fin(call(fid, c0(a))); };
    else if (n === 2) f = function (a, b) { return fin(call(fid, c0(a), c1(b))); };
    else if (n === 3) f = function (a, b, c) { return fin(call(fid, c0(a), c1(b), c2(c))); };
    else f = function () {
      var a = [fid];
      for (var i = 0; i < n; i++) a.push(conv[i](arguments[i]));
      return fin(call.apply(undefined, a));
    };
    return f;
  }
  function nameFn(f, name, n) {
    Object.defineProperty(f, 'name', { value: String(name), configurable: true });
    Object.defineProperty(f, 'length', { value: n, configurable: true });
    return f;
  }

  // wasm -> JS: Go calls this with (token, import index, ...wasm arguments)
  function coerceResult(sig, r) {
    var rs = sig.slice(sig.indexOf('>') + 1);
    if (rs.length === 0) return undefined;
    if (rs.length === 1) return CONV[rs](r);
    var a = Array.from(r), out = [];
    if (a.length !== rs.length) throw new TypeError('multi-return length mismatch');
    for (var i = 0; i < rs.length; i++) out.push(String(CONV[rs[i]](a[i])));
    return 'm:' + out.join(',');
  }
  function dispatch(tok, idx) {
    try {
      var reg = regs.get(tok), args = new Array(arguments.length - 2);
      for (var i = 2; i < arguments.length; i++) args[i - 2] = arguments[i];
      return coerceResult(reg.sigs[idx], reg.fns[idx].apply(undefined, args));
    } catch (e) {
      lastThrown = e;
      return ERR;
    }
  }
  H.setDispatch(dispatch);

  // ---- Instance -------------------------------------------------------------------------------------------
  var instExports = new WeakMap();
  function build(mi, importObject, who) {
    var imps = mi.imports, descs = new Array(imps.length), fns = [], sigs = [];
    if (imps.length > 0 && (importObject === undefined || importObject === null || (typeof importObject !== 'object' && typeof importObject !== 'function')))
      throw new TypeError(who + ': Imports argument must be present and must be an object');
    for (var i = 0; i < imps.length; i++) {
      var im = imps[i], where = who + ': Import #' + i + ' "' + im.m + '"';
      var ns = importObject[im.m];
      if (ns === null || (typeof ns !== 'object' && typeof ns !== 'function')) throw new TypeError(where + ': module is not an object or function');
      var v = ns[im.n];
      where += ' "' + im.n + '"';
      if (im.k === 'function') {
        if (typeof v !== 'function') throw new LinkError(where + ': function import requires a callable');
        var meta = fnMeta.get(v);
        if (meta && meta.sig !== im.s) throw new LinkError(where + ': imported function does not match the expected type');
        descs[i] = { k: 'f' }; fns[i] = v; sigs[i] = im.s;
      } else if (im.k === 'memory') {
        if (!(v instanceof Memory)) throw new LinkError(where + ': memory import must be a WebAssembly.Memory object');
        descs[i] = { k: 'm', key: memKeys.get(v) };
      } else if (im.k === 'table') {
        if (!(v instanceof Table)) throw new LinkError(where + ': table import requires a WebAssembly.Table');
        descs[i] = { k: 't', key: tabKeys.get(v) };
      } else if (im.k === 'global') {
        if (v instanceof Global) {
          var gi = globKeys.get(v);
          if (gi.t !== im.t || gi.mut !== im.mut) throw new LinkError(where + ': imported global does not match the expected ' + (gi.t !== im.t ? 'type' : 'mutability'));
          descs[i] = { k: 'g', key: gi.key };
        } else if (im.mut) {
          throw new LinkError(where + ': imported mutable global must be a WebAssembly.Global object');
        } else if (im.t === 'l' ? typeof v === 'bigint' : (im.t !== 'x' && typeof v === 'number')) {
          descs[i] = { k: 'g', key: globKeys.get(new Global({ value: NAME_OF[im.t], mutable: false }, v)).key };
        } else {
          throw new LinkError(where + ': global import must be a number, valid Wasm reference, or WebAssembly.Global object');
        }
      } else {
        throw new LinkError(where + ': ' + im.k + ' imports are not supported');
      }
    }
    var tok = ++tokSeq;
    regs.set(tok, { fns: fns, sigs: sigs, keep: imps.length ? importObject : null });
    var r = H.instantiate(mi.id, tok, JSON.stringify(descs));
    if (r.e) {
      regs.delete(tok);
      if (r.e === 'T') { var e = lastThrown; lastThrown = undefined; throw e; }
      if (r.e === 'L') throw new LinkError(who + ': ' + r.m);
      if (r.e === 'S') throw new RangeError(r.m);
      throw new RuntimeError(who + ': ' + r.m);
    }
    var ex = Object.create(null), byIdx = {};
    for (var k = 0; k < r.exports.length; k++) {
      var e = r.exports[k], val;
      if (e.k === 'f') {
        val = byIdx[e.i];
        if (!val) {
          val = byIdx[e.i] = nameFn(makeFn(e.s, e.f), e.i, e.s.indexOf('>'));
          fnMeta.set(val, { tok: tok, idx: e.i, sig: e.s });
        }
      } else if (e.k === 'm') val = memOf(e.key);
      else if (e.k === 't') val = tabOf(e.key);
      else val = globOf(e.key, e.t, e.m);
      ex[e.n] = val;
    }
    return Object.freeze(ex);
  }
  class Instance {
    constructor(module, importObject) {
      instExports.set(this, build(infoOf(module, 'WebAssembly.Instance()'), importObject, 'WebAssembly.Instance()'));
    }
  }
  Object.defineProperty(Instance.prototype, 'exports', {
    enumerable: true, configurable: true,
    get: function () {
      var e = instExports.get(this);
      if (!e) throw new TypeError('WebAssembly.Instance.exports: Receiver is not a WebAssembly.Instance');
      return e;
    }
  });
  tag(Instance, 'WebAssembly.Instance');

  // ---- Memory ---------------------------------------------------------------------------------------------
  class Memory {
    constructor(desc) {
      var who = 'WebAssembly.Memory()';
      if (desc === null || typeof desc !== 'object') throw new TypeError(who + ': Argument 0 must be a memory descriptor');
      if (desc.initial === undefined) throw new TypeError(who + ": Property 'initial' is required");
      var shared = !!desc.shared;
      if (shared && desc.maximum === undefined) throw new TypeError(who + ": Property 'maximum': If shared is true, maximum property should be defined.");
      var initial = toU32(desc.initial, 'initial', who, 65536);
      var maximum = desc.maximum === undefined ? -1 : toU32(desc.maximum, 'maximum', who, 65536);
      if (maximum >= 0 && maximum < initial) throw new RangeError(who + ": Property 'maximum': value " + maximum + ' is below the lower bound ' + initial);
      var key = H.memNew(initial, maximum, shared);
      if (typeof key !== 'string') throw new RangeError(key.m);
      memKeys.set(this, key); memObjs.set(key, this);
    }
    get buffer() {
      var key = memKeys.get(this);
      if (key === undefined) throw new TypeError('WebAssembly.Memory.buffer: Receiver is not a WebAssembly.Memory');
      var b = H.memBuf(key);
      if (b === undefined) { b = emptyBufs.get(key); if (!b) emptyBufs.set(key, b = new ArrayBuffer(0)); }
      return b;
    }
    grow(delta) {
      var key = memKeys.get(this);
      if (key === undefined) throw new TypeError('WebAssembly.Memory.grow(): Receiver is not a WebAssembly.Memory');
      var r = H.memGrow(key, toU32(delta, 'delta', 'WebAssembly.Memory.grow()'));
      if (r < 0) throw new RangeError('WebAssembly.Memory.grow(): Maximum memory size exceeded');
      return r;
    }
  }
  tag(Memory, 'WebAssembly.Memory');

  // ---- Table ----------------------------------------------------------------------------------------------
  // a JS import that an element segment (or ref.func) put in a table, read back with Table.get
  function fnFromImport(r) {
    var f = refFns.get('i' + r.r);
    if (!f) {
      var reg = regs.get(r.imp), idx = r.ii, sig = r.s, cut = sig.indexOf('>'), ps = sig.slice(0, cut);
      f = nameFn(function () {
        var a = [];
        for (var i = 0; i < ps.length; i++) a.push(CONV[ps[i]](arguments[i]));
        var out = reg.fns[idx].apply(undefined, a), rs = sig.slice(cut + 1);
        if (rs.length === 0) return undefined;
        if (rs.length === 1) return CONV[rs](out);
        return Array.from(out).map(function (v, k) { return CONV[rs[k]](v); });
      }, r.ii, ps.length);
      fnMeta.set(f, { ref: 0, refVal: r.r, sig: sig });
      refFns.set('i' + r.r, f);
    }
    return f;
  }
  function fnFromRef(r) {
    if (r.imp !== undefined) return fnFromImport(r);
    var f = refFns.get(r.id);
    if (!f) {
      f = nameFn(makeFn(r.s, r.id), r.id, r.s.indexOf('>'));
      fnMeta.set(f, { ref: r.id, refVal: r.r, sig: r.s });
      refFns.set(r.id, f);
    }
    return f;
  }
  function refArg(v, who) {
    if (v === null || v === undefined) return '';
    var m = typeof v === 'function' && fnMeta.get(v);
    if (!m) throw new TypeError(who + ': Argument 1 is invalid for table: function-typed object expected');
    return m.tok ? m.tok + ':' + m.idx : 'ref:' + m.refVal;
  }
  class Table {
    constructor(desc, value) {
      var who = 'WebAssembly.Table()';
      if (desc === null || typeof desc !== 'object') throw new TypeError(who + ': Argument 0 must be a table descriptor');
      var el = desc.element;
      if (el !== 'anyfunc' && el !== 'funcref') throw new TypeError(who + ": Descriptor property 'element' must be 'anyfunc' or 'funcref' (externref tables are not supported)");
      if (desc.initial === undefined) throw new TypeError(who + ": Property 'initial' is required");
      var initial = toU32(desc.initial, 'initial', who, 10000000);
      var maximum = desc.maximum === undefined ? -1 : toU32(desc.maximum, 'maximum', who);
      if (maximum >= 0 && maximum < initial) throw new RangeError(who + ": Property 'maximum': value " + maximum + ' is below the lower bound ' + initial);
      var key = H.tabNew(initial, maximum);
      if (typeof key !== 'string') throw new RangeError(key.m);
      tabKeys.set(this, key); tabObjs.set(key, this);
      if (value !== undefined && value !== null) { var s = refArg(value, who); for (var i = 0; i < initial; i++) host(function () { H.tabSet(key, i, s); }); }
    }
    get length() { return H.tabLen(tabKeys.get(this)); }
    get(index) {
      var key = tabKeys.get(this);
      if (key === undefined) throw new TypeError('WebAssembly.Table.get(): Receiver is not a WebAssembly.Table');
      var r = host(function () { return H.tabGet(key, toU32(index, 'index', 'WebAssembly.Table.get()')); });
      return r ? fnFromRef(r) : null;
    }
    set(index, value) {
      var key = tabKeys.get(this);
      if (key === undefined) throw new TypeError('WebAssembly.Table.set(): Receiver is not a WebAssembly.Table');
      var i = toU32(index, 'index', 'WebAssembly.Table.set()'), s = refArg(value, 'WebAssembly.Table.set()');
      host(function () { H.tabSet(key, i, s); });
    }
    grow(delta, value) {
      var key = tabKeys.get(this);
      if (key === undefined) throw new TypeError('WebAssembly.Table.grow(): Receiver is not a WebAssembly.Table');
      var r = H.tabGrow(key, toU32(delta, 'delta', 'WebAssembly.Table.grow()'), refArg(value, 'WebAssembly.Table.grow()'));
      if (r < 0) throw new RangeError('WebAssembly.Table.grow(): failed to grow table by ' + delta);
      return r;
    }
  }
  tag(Table, 'WebAssembly.Table');

  // ---- Global ---------------------------------------------------------------------------------------------
  class Global {
    constructor(desc, v) {
      var who = 'WebAssembly.Global()';
      if (desc === null || typeof desc !== 'object') throw new TypeError(who + ': Argument 0 must be a global descriptor');
      var t = TYPE_OF[desc.value];
      if (!t) throw new TypeError(who + ": Descriptor property 'value' must be 'i32', 'i64', 'f32', or 'f64'");
      var mut = !!desc.mutable;
      if (v === undefined) v = t === 'l' ? 0n : 0;
      var key = H.globNew(t, mut, CONV[t](v));
      if (typeof key !== 'string') throw new TypeError(key.m);
      globKeys.set(this, { key: key, t: t, mut: mut }); globObjs.set(key, this);
    }
    get value() {
      var g = globKeys.get(this);
      if (!g) throw new TypeError('WebAssembly.Global.value: Receiver is not a WebAssembly.Global');
      return H.globGet(g.key);
    }
    set value(v) {
      var g = globKeys.get(this);
      if (!g) throw new TypeError('WebAssembly.Global.value: Receiver is not a WebAssembly.Global');
      if (!g.mut) throw new TypeError("WebAssembly.Global.value: Can't set the value of an immutable global.");
      host(function () { H.globSet(g.key, CONV[g.t](v)); });
    }
    valueOf() { return this.value; }
  }
  tag(Global, 'WebAssembly.Global');

  // ---- namespace functions --------------------------------------------------------------------------------
  function validate(bytes) {
    if (arguments.length < 1) throw new TypeError('WebAssembly.validate(): Argument 0 must be a buffer source');
    var b = bufferSource(bytes, 'WebAssembly.validate()');
    return H.validate(b[0], b[1], b[2]);
  }
  function compile(bytes) {
    return new Promise(function (res, rej) {
      try { var m = Object.create(Module.prototype); modInfo.set(m, compileBytes(bytes, 'WebAssembly.compile()')); res(m); } catch (e) { rej(e); }
    });
  }
  function instantiate(src, imports) {
    return new Promise(function (res, rej) {
      try {
        if (src instanceof Module) {
          var inst = Object.create(Instance.prototype);
          instExports.set(inst, build(infoOf(src, 'WebAssembly.instantiate()'), imports, 'WebAssembly.instantiate()'));
          res(inst);
        } else {
          var m = Object.create(Module.prototype);
          modInfo.set(m, compileBytes(src, 'WebAssembly.instantiate()'));
          var i2 = Object.create(Instance.prototype);
          instExports.set(i2, build(modInfo.get(m), imports, 'WebAssembly.instantiate()'));
          res({ module: m, instance: i2 });
        }
      } catch (e) { rej(e); }
    });
  }
  function responseBytes(srcPromise, who) {
    return Promise.resolve(srcPromise).then(function (resp) {
      if (!resp || typeof resp.arrayBuffer !== 'function') throw new TypeError(who + ': Argument 0 must be a Response or Response promise');
      var ct = '';
      try { ct = String(resp.headers.get('content-type') || ''); } catch (e) { /* none */ }
      if (ct.split(';')[0].trim().toLowerCase() !== 'application/wasm') throw new TypeError(who + ": Incorrect response MIME type. Expected 'application/wasm'.");
      if (!resp.ok) throw new TypeError(who + ': HTTP status code is not ok');
      return resp.arrayBuffer();
    });
  }
  function compileStreaming(source) {
    return responseBytes(source, 'WebAssembly.compileStreaming()').then(function (buf) {
      var m = Object.create(Module.prototype);
      modInfo.set(m, compileBytes(buf, 'WebAssembly.compileStreaming()'));
      return m;
    });
  }
  function instantiateStreaming(source, imports) {
    return responseBytes(source, 'WebAssembly.instantiateStreaming()').then(function (buf) {
      var m = Object.create(Module.prototype);
      modInfo.set(m, compileBytes(buf, 'WebAssembly.instantiateStreaming()'));
      var inst = Object.create(Instance.prototype);
      instExports.set(inst, build(modInfo.get(m), imports, 'WebAssembly.instantiateStreaming()'));
      return { module: m, instance: inst };
    });
  }

  // ---- postMessage of Modules and shared Memories (idb_prelude.js ser/de call these) ---------------------
  function cloneErr(msg) { return new global.DOMException(msg, 'DataCloneError'); }
  Object.defineProperty(global, '__swWasmSer', { value: function (v, share) {
    if (v instanceof Module) {
      if (!share) throw cloneErr('#<Module> could not be cloned.');
      return { k: 'mod', s: host(function () { return H.modShare(infoOf(v, 'postMessage').id); }) };
    }
    if (v instanceof Memory) {
      if (!share) throw cloneErr('#<Memory> could not be cloned.');
      if (!global.crossOriginIsolated) throw cloneErr('SharedArrayBuffer transfer requires self.crossOriginIsolated.');
      return { k: 'mem', s: host(function () { return H.memShare(memKeys.get(v)); }) };
    }
    return undefined;
  } });
  Object.defineProperty(global, '__swWasmDe', { value: function (d) {
    if (d.k === 'mod') {
      var r = host(function () { return H.modOpen(d.s); });
      if (r.e) throw cloneErr('#<Module> could not be cloned.');
      var m = Object.create(Module.prototype);
      modInfo.set(m, r);
      return m;
    }
    return memOf(host(function () { return H.memOpen(d.s); }));
  } });

  var WA = {};
  def(WA, 'Module', Module); def(WA, 'Instance', Instance); def(WA, 'Memory', Memory);
  def(WA, 'Table', Table); def(WA, 'Global', Global);
  def(WA, 'CompileError', CompileError); def(WA, 'LinkError', LinkError); def(WA, 'RuntimeError', RuntimeError);
  def(WA, 'validate', validate); def(WA, 'compile', compile); def(WA, 'instantiate', instantiate);
  def(WA, 'compileStreaming', compileStreaming); def(WA, 'instantiateStreaming', instantiateStreaming);
  Object.defineProperty(WA, Symbol.toStringTag, { value: 'WebAssembly', configurable: true });
  def(global, 'WebAssembly', WA);
})(globalThis);
