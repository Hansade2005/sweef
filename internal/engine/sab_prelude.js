/*
 * SharedArrayBuffer and Atomics.wait / notify / waitAsync over memory that outlives a VM (sab.go).
 *
 * The constructor exists only in cross-origin isolated documents and workers (__swCoiBind, called when a VM is
 * bound to a document). Buffers made with `new SharedArrayBuffer(n)` live in Go memory and can be posted to
 * workers of the same agent cluster; typed arrays and every Atomics operation except wait/notify are QuickJS's own.
 */
(function (global) {
  'use strict';
  var N = {}, names = ['new', 'id', 'open', 'wait', 'notify', 'waitAsync'];
  names.forEach(function (k) {
    N[k] = global['__swSab_' + k];
    try { delete global['__swSab_' + k]; } catch (e) { /* ignore */ }
  });
  var Native = global.SharedArrayBuffer, sw = global.__sw;
  if (typeof Native !== 'function') return;
  try { delete global.SharedArrayBuffer; } catch (e) { /* ignore */ }
  if (typeof N.new !== 'function') return;
  Object.defineProperty(global, '__swSAB', { value: Native, configurable: true });

  function toIndex(v, what) {
    if (typeof v === 'bigint' || typeof v === 'symbol') throw new TypeError('Cannot convert ' + String(v) + ' to a number');
    var n = Math.trunc(Number(v));
    if (n !== n) n = 0;
    if (n < 0 || n > 9007199254740991) throw new RangeError(what);
    return n;
  }

  function SharedArrayBuffer(length) {
    if (new.target === undefined) throw new TypeError("Constructor SharedArrayBuffer requires 'new'");
    var n = toIndex(length, 'Invalid array buffer length');
    var buf = N.new(n);
    if (buf === undefined) throw new RangeError('Array buffer allocation failed');
    if (new.target !== SharedArrayBuffer) Object.setPrototypeOf(buf, new.target.prototype);
    return buf;
  }
  Object.defineProperty(SharedArrayBuffer, 'length', { value: 1, configurable: true });
  SharedArrayBuffer.prototype = Native.prototype;
  Object.defineProperty(Native.prototype, 'constructor', { value: SharedArrayBuffer, writable: true, configurable: true });
  Object.defineProperty(SharedArrayBuffer, Symbol.species, { get: function () { return this; }, configurable: true });

  // ---- Atomics.wait / notify / waitAsync -------------------------------------------------------------
  function tagOf(o) { return Object.prototype.toString.call(o); }
  function check(ta, index, needShared) {
    var big = typeof BigInt64Array === 'function' && ta instanceof BigInt64Array;
    if (!(ta instanceof Int32Array) && !big) throw new TypeError(tagOf(ta) + ' is not an int32 or BigInt64 typed array.');
    if (needShared && !(ta.buffer instanceof Native)) throw new TypeError(tagOf(ta) + ' is not an int32 or BigInt64 typed array.');
    var i = toIndex(index, 'Invalid atomic access index');
    if (i >= ta.length) throw new RangeError('Invalid atomic access index');
    return { big: big, i: i, size: big ? 8 : 4, off: ta.byteOffset + i * (big ? 8 : 4) };
  }
  function expected(c, value) { return c.big ? String(BigInt.asIntN(64, BigInt(value))) : String(value | 0); }
  function timeoutOf(t) { t = t === undefined ? Infinity : Number(t); return t !== t ? Infinity : t < 0 ? 0 : t; }
  function define(name, fn) { Object.defineProperty(Atomics, name, { value: fn, writable: true, configurable: true, enumerable: false }); }

  define('wait', function wait(ta, index, value, timeout) {
    var c = check(ta, index, true), e = expected(c, value), t = timeoutOf(timeout);
    if (!global.__swIsWorker) throw new TypeError('Atomics.wait cannot be called in this context');
    var r = N.wait(ta.buffer, c.off, c.size, e, t);
    if (r === 'invalid') throw new RangeError('Invalid atomic access index');
    return r;
  });
  define('notify', function notify(ta, index, count) {
    var c = check(ta, index, false);
    var n = count === undefined ? Infinity : Math.trunc(Number(count));
    if (n !== n || n < 0) n = 0;
    if (!(ta.buffer instanceof Native)) return 0;
    return N.notify(ta.buffer, c.off, c.size, n);
  });
  var pend = new Map();
  define('waitAsync', function waitAsync(ta, index, value, timeout) {
    var c = check(ta, index, true), e = expected(c, value), t = timeoutOf(timeout);
    var r = N.waitAsync(ta.buffer, c.off, c.size, e, t);
    if (r[0] === 'invalid') throw new RangeError('Invalid atomic access index');
    if (r[0] !== 'waiting') return { async: false, value: r[0] };
    var p = new Promise(function (res) { pend.set(r[1], res); });
    return { async: true, value: p };
  });
  Object.defineProperty(global, '__swSabEvent', { value: function (token, result) {
    var res = pend.get(token);
    if (res) { pend.delete(token); res(result); }
  } });

  // ---- sharing with other VMs ---------------------------------------------------------------------------
  Object.defineProperty(global, '__swSabId', { value: function (buf) { return N.id(buf); } });
  Object.defineProperty(global, '__swSabOpen', { value: function (id, len) { return N.open(id, len); } });
  Object.defineProperty(global, '__swSabShare', { value: function (buf) { var id = N.id(buf); return id < 0 ? buf : N.open(id, buf.byteLength); } });

  // ---- gating ---------------------------------------------------------------------------------------
  Object.defineProperty(global, '__swCoiBind', { value: function () {
    var on = false;
    try { on = !!(sw && sw.coi && sw.coi()); } catch (e) { on = false; }
    if (on) Object.defineProperty(global, 'SharedArrayBuffer', { value: SharedArrayBuffer, writable: true, configurable: true, enumerable: false });
    else { try { delete global.SharedArrayBuffer; } catch (e) { /* ignore */ } }
  } });
})(globalThis);
