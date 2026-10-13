// ES2024-2026 built-ins QuickJS lacks: Array.fromAsync, explicit resource management
// library pieces (Symbol.dispose, DisposableStack, AsyncDisposableStack, SuppressedError)
// and Atomics.waitAsync. Syntax (`using`, `await using`) cannot be polyfilled.
function isCtor(f) {
  if (typeof f !== 'function') return false;
  try { Reflect.construct(String, [], f); return true; } catch (e) { return false; }
}
function def(o, k, v) { H.define(o, k, v); }
function defineAccessorFreeze(o, k, v) { Object.defineProperty(o, k, { value: v, writable: false, enumerable: false, configurable: false }); }

// ---- Array.fromAsync ----------------------------------------------------------------------
if (H.missing(Array, 'fromAsync')) {
  def(Array, 'fromAsync', {
    fromAsync: async function (items, mapfn, thisArg) {
      var C = this;
      if (mapfn !== undefined && typeof mapfn !== 'function') throw new TypeError('Array.fromAsync: mapper is not a function');
      if (items === null || items === undefined) throw new TypeError('Array.fromAsync: items is null or undefined');
      var usingAsync = items[Symbol.asyncIterator], usingSync = usingAsync == null ? items[Symbol.iterator] : undefined, A, k = 0;
      if (usingAsync != null || usingSync != null) {
        A = isCtor(C) ? new C() : [];
        var it = (usingAsync != null ? usingAsync : usingSync).call(items), isAsync = usingAsync != null, next = it.next;
        for (;;) {
          var r = next.call(it);
          if (isAsync) r = await r;
          if (r === null || typeof r !== 'object') throw new TypeError('iterator result is not an object');
          if (r.done) { A.length = k; return A; }
          var v = r.value;
          try {
            if (!isAsync) v = await v;
            if (mapfn) v = await mapfn.call(thisArg, v, k);
            Object.defineProperty(A, k, { value: v, writable: true, enumerable: true, configurable: true });
          } catch (e) {
            try { if (typeof it.return === 'function') { var rr = it.return(); if (isAsync) await rr; } } catch (e2) { /* the original error wins */ }
            throw e;
          }
          k++;
        }
      }
      var arrayLike = Object(items), len = Math.min(Math.max(Math.trunc(Number(arrayLike.length)) || 0, 0), Number.MAX_SAFE_INTEGER);
      A = isCtor(C) ? new C(len) : new Array(len);
      for (; k < len; k++) {
        var x = await arrayLike[k];
        if (mapfn) x = await mapfn.call(thisArg, x, k);
        Object.defineProperty(A, k, { value: x, writable: true, enumerable: true, configurable: true });
      }
      A.length = len;
      return A;
    }
  }.fromAsync);
  Object.defineProperty(Array.fromAsync, 'length', { value: 1 });
}

// ---- explicit resource management (library part) --------------------------------------------------
if (H.missing(Symbol, 'dispose')) defineAccessorFreeze(Symbol, 'dispose', Symbol('Symbol.dispose'));
if (H.missing(Symbol, 'asyncDispose')) defineAccessorFreeze(Symbol, 'asyncDispose', Symbol('Symbol.asyncDispose'));
if (H.missing(g, 'SuppressedError')) {
  var SuppressedError = function SuppressedError(error, suppressed, message) {
    if (!new.target) return new SuppressedError(error, suppressed, message);
    var e = Reflect.construct(Error, message === undefined ? [] : [String(message)], new.target);
    Object.defineProperty(e, 'error', { value: error, writable: true, enumerable: false, configurable: true });
    Object.defineProperty(e, 'suppressed', { value: suppressed, writable: true, enumerable: false, configurable: true });
    return e;
  };
  Object.setPrototypeOf(SuppressedError, Error);
  SuppressedError.prototype = Object.create(Error.prototype, { constructor: { value: SuppressedError, writable: true, configurable: true }, name: { value: 'SuppressedError', writable: true, configurable: true }, message: { value: '', writable: true, configurable: true } });
  H.glob('SuppressedError', SuppressedError);
}
var SE = g.SuppressedError;
var $S = Symbol('disposable');
function stackOf(o, brand) { var s = o !== null && typeof o === 'object' ? o[$S] : undefined; if (!s || s.brand !== brand) throw new TypeError('Illegal invocation'); return s; }
function addError(st, e) { st.has = true; st.err = st.hasErr ? new SE(e, st.err) : e; st.hasErr = true; }
function makeStack(Name, isAsync) {
  var disposeSym = isAsync ? Symbol.asyncDispose : Symbol.dispose;
  var C = {
    [Name]: function () {
      if (!new.target) throw new TypeError("Constructor " + Name + " requires 'new'");
      Object.defineProperty(this, $S, { value: { brand: Name, state: 'pending', stack: [], err: undefined, hasErr: false } });
    }
  }[Name];
  function pending(st) { if (st.state === 'disposed') throw new ReferenceError(Name + ' is already disposed'); }
  function methodOf(v, hint) {
    if (isAsync) {
      var am = v[Symbol.asyncDispose];
      if (am != null) return am;
      var sm = v[Symbol.dispose];
      if (sm == null) return undefined;
      return function () { sm.call(this); };
    }
    return v[Symbol.dispose];
  }
  H.attrs(C.prototype, { disposed: function () { return stackOf(this, Name).state === 'disposed'; } });
  def(C.prototype, 'use', function use(value) {
    var st = stackOf(this, Name); pending(st);
    if (value === null || value === undefined) return value;
    if (typeof value !== 'object' && typeof value !== 'function') throw new TypeError('Value is not an object');
    var m = methodOf(value);
    if (typeof m !== 'function') throw new TypeError('Value is not ' + (isAsync ? 'async ' : '') + 'disposable');
    st.stack.push({ value: value, fn: m });
    return value;
  });
  def(C.prototype, 'adopt', function adopt(value, onDispose) {
    var st = stackOf(this, Name); pending(st);
    if (typeof onDispose !== 'function') throw new TypeError('onDispose is not a function');
    st.stack.push({ value: undefined, fn: function () { return onDispose(value); } });
    return value;
  });
  def(C.prototype, 'defer', function defer(onDispose) {
    var st = stackOf(this, Name); pending(st);
    if (typeof onDispose !== 'function') throw new TypeError('onDispose is not a function');
    st.stack.push({ value: undefined, fn: function () { return onDispose(); } });
  });
  def(C.prototype, 'move', function move() {
    var st = stackOf(this, Name); pending(st);
    var n = new C(), ns = n[$S];
    ns.stack = st.stack; st.stack = []; st.state = 'disposed';
    return n;
  });
  if (!isAsync) {
    def(C.prototype, 'dispose', function dispose() {
      var st = stackOf(this, Name);
      if (st.state === 'disposed') return;
      st.state = 'disposed';
      var s = { hasErr: false, err: undefined };
      while (st.stack.length) {
        var r = st.stack.pop();
        try { r.fn.call(r.value); } catch (e) { addError(s, e); }
      }
      if (s.hasErr) throw s.err;
    });
  } else {
    def(C.prototype, 'disposeAsync', function disposeAsync() {
      var st;
      try { st = stackOf(this, Name); } catch (e) { return Promise.reject(e); }
      if (st.state === 'disposed') return Promise.resolve();
      st.state = 'disposed';
      return (async function () {
        var s = { hasErr: false, err: undefined };
        while (st.stack.length) {
          var r = st.stack.pop();
          try { await r.fn.call(r.value); } catch (e) { addError(s, e); }
        }
        if (s.hasErr) throw s.err;
      })();
    });
  }
  Object.defineProperty(C.prototype, disposeSym, { value: C.prototype[isAsync ? 'disposeAsync' : 'dispose'], writable: true, configurable: true });
  H.tag(C, Name);
  H.glob(Name, C);
}
makeStack('DisposableStack', false);
makeStack('AsyncDisposableStack', true);

// ---- Atomics.waitAsync (single agent: only a same-agent notify can wake a waiter) -------------------------
if (typeof Atomics !== 'undefined' && H.missing(Atomics, 'waitAsync')) {
  var waiters = [];
  def(Atomics, 'waitAsync', function waitAsync(ta, index, value, timeout) {
    if (!(ta instanceof Int32Array || (typeof BigInt64Array !== 'undefined' && ta instanceof BigInt64Array)) || !(ta.buffer instanceof SharedArrayBuffer)) throw new TypeError('Atomics.waitAsync requires an Int32Array or BigInt64Array on a SharedArrayBuffer');
    index = Math.trunc(Number(index)) || 0;
    if (index < 0 || index >= ta.length) throw new RangeError('index out of range');
    var t = timeout === undefined ? Infinity : Number(timeout);
    if (t !== t) t = Infinity;
    if (t < 0) t = 0;
    if (Atomics.load(ta, index) != value) return { async: false, value: 'not-equal' };
    if (t === 0) return { async: false, value: 'timed-out' };
    var w = { ta: ta, index: index };
    w.promise = new Promise(function (res) {
      w.resolve = function (v) { var i = waiters.indexOf(w); if (i >= 0) waiters.splice(i, 1); if (w.timer) clearTimeout(w.timer); res(v); };
    });
    if (t !== Infinity) w.timer = setTimeout(function () { w.resolve('timed-out'); }, t);
    waiters.push(w);
    return { async: true, value: w.promise };
  });
  var nativeNotify = Atomics.notify;
  def(Atomics, 'notify', function notify(ta, index, count) {
    var n = nativeNotify.call(Atomics, ta, index, count);
    var left = count === undefined ? Infinity : Math.max(0, Math.trunc(Number(count)) || 0);
    for (var i = 0; i < waiters.length && left > 0;) {
      var w = waiters[i];
      if (w.ta.buffer === ta.buffer && w.ta.byteOffset + w.index * w.ta.BYTES_PER_ELEMENT === ta.byteOffset + (Math.trunc(Number(index)) || 0) * ta.BYTES_PER_ELEMENT) { w.resolve('ok'); n++; left--; } else i++;
    }
    return n;
  });
}
