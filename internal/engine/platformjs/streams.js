// Streams Standard: ReadableStream (default and byte controllers), WritableStream,
// TransformStream, readers/writers, queuing strategies. Loaded on first use.
var $ = Symbol('slots');
function slots(o, name) {
  var s = o !== null && typeof o === 'object' ? o[$] : undefined;
  if (!s || s.brand !== name) throw new TypeError('Illegal invocation: not a ' + name);
  return s;
}
function slotsOpt(o, name) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; return s && s.brand === name ? s : undefined; }
function putSlots(o, s) { Object.defineProperty(o, $, { value: s }); }
function defer() {
  var d = { settled: false };
  d.promise = new Promise(function (res, rej) { d.resolve = function (v) { d.settled = true; res(v); }; d.reject = function (e) { d.settled = true; rej(e); }; });
  return d;
}
function handled(p) { p.then(undefined, function () { }); return p; }
function isObj(x) { return x !== null && (typeof x === 'object' || typeof x === 'function'); }
function rejected(e) { return Promise.reject(e); }
function method(o, k, v) { H.define(o, k, v); }
function getAlg(obj, name) {
  var f = obj[name];
  if (f === undefined || f === null) return undefined;
  if (typeof f !== 'function') throw new TypeError(name + ' is not a function');
  return f;
}
function promiseCall(fn, thisArg, args) {
  try { return Promise.resolve(fn.apply(thisArg, args)); } catch (e) { return rejected(e); }
}
function extractHWM(strategy, dflt) {
  var h = strategy === undefined || strategy === null ? undefined : strategy.highWaterMark;
  if (h === undefined) return dflt;
  h = Number(h);
  if (h !== h || h < 0) throw new RangeError('Invalid highWaterMark');
  return h;
}
function extractSize(strategy) {
  var s = strategy === undefined || strategy === null ? undefined : strategy.size;
  if (s === undefined) return undefined;
  if (typeof s !== 'function') throw new TypeError('strategy.size must be a function');
  return s;
}
function validSize(n) { if (typeof n !== 'number' || n !== n || n < 0 || n === Infinity) throw new RangeError('Invalid chunk size'); return n; }

// ---- queuing strategies -------------------------------------------------------
function CountQueuingStrategy(init) {
  if (!new.target) throw new TypeError("Constructor CountQueuingStrategy requires 'new'");
  if (!isObj(init)) throw new TypeError('init must be an object');
  Object.defineProperty(this, 'highWaterMark', { value: +init.highWaterMark, enumerable: true, configurable: true, writable: true });
}
Object.defineProperty(CountQueuingStrategy.prototype, 'size', { get: function () { return function () { return 1; }; }, enumerable: true, configurable: true });
H.tag(CountQueuingStrategy, 'CountQueuingStrategy');
function ByteLengthQueuingStrategy(init) {
  if (!new.target) throw new TypeError("Constructor ByteLengthQueuingStrategy requires 'new'");
  if (!isObj(init)) throw new TypeError('init must be an object');
  Object.defineProperty(this, 'highWaterMark', { value: +init.highWaterMark, enumerable: true, configurable: true, writable: true });
}
Object.defineProperty(ByteLengthQueuingStrategy.prototype, 'size', { get: function () { return function (c) { return c.byteLength; }; }, enumerable: true, configurable: true });
H.tag(ByteLengthQueuingStrategy, 'ByteLengthQueuingStrategy');

// ---- value-with-size queue ------------------------------------------------------
function Q(o) { o.queue = []; o.total = 0; }
function qEnqueue(o, value, size) { size = validSize(Number(size)); o.queue.push({ value: value, size: size }); o.total += size; }
function qDequeue(o) { var p = o.queue.shift(); o.total -= p.size; if (o.total < 0) o.total = 0; return p.value; }
function qReset(o) { o.queue = []; o.total = 0; }

// ======================================================================================
// ReadableStream
// ======================================================================================
function ReadableStream(src, strategy) {
  if (!new.target) throw new TypeError("Constructor ReadableStream requires 'new'");
  var s = { brand: 'RS', state: 'readable', reader: null, storedError: undefined, disturbed: false, ctrl: null, self: this };
  putSlots(this, s);
  if (src === undefined) src = null;
  var srcObj = src === null ? {} : src;
  if (!isObj(srcObj)) throw new TypeError('underlyingSource must be an object');
  var type = srcObj.type;
  if (type !== undefined) type = String(type);
  if (type === 'bytes') {
    if (strategy && strategy.size !== undefined) throw new RangeError('size is not allowed for byte streams');
    setupByteController(s, srcObj, extractHWM(strategy, 0));
  } else if (type === undefined) {
    var size = extractSize(strategy);
    setupDefaultController(s, srcObj, extractHWM(strategy, 1), size);
  } else throw new RangeError('Invalid type: ' + type);
}
function rsLocked(s) { return s.reader !== null; }
function rsError(s, e) {
  s.state = 'errored'; s.storedError = e;
  var r = s.reader;
  if (!r) return;
  r.closed.reject(e); handled(r.closed.promise);
  var reqs = r.readRequests; r.readRequests = [];
  reqs.forEach(function (q) { q.error(e); });
}
function rsClose(s) {
  s.state = 'closed';
  var r = s.reader;
  if (!r) return;
  r.closed.resolve(undefined);
  var reqs = r.readRequests; r.readRequests = [];
  reqs.forEach(function (q) { q.close(); });
  if (s.ctrl && s.ctrl.isByte) s.ctrl.flushClosed();
}
function rsCancel(s, reason) {
  s.disturbed = true;
  if (s.state === 'closed') return Promise.resolve(undefined);
  if (s.state === 'errored') return rejected(s.storedError);
  rsClose(s);
  return s.ctrl.cancelSteps(reason).then(function () { return undefined; });
}
function rsDesired(s) { return s.state === 'errored' ? null : s.state === 'closed' ? 0 : s.ctrl.desired(); }

Object.defineProperties(ReadableStream.prototype, {
  locked: { get: function () { return rsLocked(slots(this, 'RS')); }, enumerable: true, configurable: true }
});
method(ReadableStream.prototype, 'cancel', function cancel(reason) {
  var s; try { s = slots(this, 'RS'); } catch (e) { return rejected(e); }
  if (rsLocked(s)) return rejected(new TypeError('Cannot cancel a stream that is locked to a reader'));
  return rsCancel(s, reason);
});
method(ReadableStream.prototype, 'getReader', function getReader(options) {
  var s = slots(this, 'RS');
  var mode = options === undefined || options === null ? undefined : options.mode;
  if (mode === undefined) return new ReadableStreamDefaultReader(this);
  if (String(mode) === 'byob') return new ReadableStreamBYOBReader(this);
  throw new TypeError('Invalid reader mode');
});
method(ReadableStream.prototype, 'pipeThrough', function pipeThrough(transform, options) {
  var s = slots(this, 'RS');
  if (!isObj(transform)) throw new TypeError('transform must be an object');
  var readable = transform.readable, writable = transform.writable;
  var ws = slotsOpt(writable, 'WS');
  if (!slotsOpt(readable, 'RS')) throw new TypeError('transform.readable is not a ReadableStream');
  if (!ws) throw new TypeError('transform.writable is not a WritableStream');
  options = options || {};
  if (rsLocked(s)) throw new TypeError('ReadableStream is locked');
  if (ws.writer) throw new TypeError('WritableStream is locked');
  handled(pipeTo(this, writable, !!options.preventClose, !!options.preventAbort, !!options.preventCancel, options.signal));
  return readable;
});
method(ReadableStream.prototype, 'pipeTo', function pipeToM(dest, options) {
  try {
    var s = slots(this, 'RS');
    if (!slotsOpt(dest, 'WS')) throw new TypeError('destination is not a WritableStream');
    options = options || {};
    var signal = options.signal;
    if (signal !== undefined && !(signal instanceof g.AbortSignal)) throw new TypeError('signal is not an AbortSignal');
    if (rsLocked(s)) throw new TypeError('ReadableStream is locked');
    if (slots(dest, 'WS').writer) throw new TypeError('WritableStream is locked');
    return pipeTo(this, dest, !!options.preventClose, !!options.preventAbort, !!options.preventCancel, signal);
  } catch (e) { return rejected(e); }
});
method(ReadableStream.prototype, 'tee', function tee() {
  var s = slots(this, 'RS');
  return teeStream(this, s);
});
function asyncIterValues(options) {
  var s = slots(this, 'RS');
  var preventCancel = !!(options && options.preventCancel);
  var reader = new ReadableStreamDefaultReader(this), finished = false, ongoing = null;
  function nextSteps() {
    if (finished) return Promise.resolve({ value: undefined, done: true });
    return reader.read().then(function (r) {
      if (r.done) { finished = true; reader.releaseLock(); return { value: undefined, done: true }; }
      return { value: r.value, done: false };
    }, function (e) { finished = true; reader.releaseLock(); throw e; });
  }
  function returnSteps(value) {
    if (finished) return Promise.resolve({ value: value, done: true });
    finished = true;
    if (!preventCancel) { var c = reader.cancel(value); reader.releaseLock(); return c.then(function () { return { value: value, done: true }; }); }
    reader.releaseLock();
    return Promise.resolve({ value: value, done: true });
  }
  var it = Object.create(asyncIterProto);
  it.next = function () { ongoing = ongoing ? ongoing.then(nextSteps, nextSteps) : nextSteps(); return ongoing; };
  it.return = function (v) { var f = function () { return returnSteps(v); }; ongoing = ongoing ? ongoing.then(f, f) : f(); return ongoing; };
  return it;
}
var asyncIterProto = Object.getPrototypeOf(Object.getPrototypeOf(async function* () { }.prototype));
method(ReadableStream.prototype, 'values', asyncIterValues);
H.define(ReadableStream.prototype, Symbol.asyncIterator, ReadableStream.prototype.values);
method(ReadableStream, 'from', function from(iterable) {
  var it, isAsync = false, m = iterable == null ? undefined : iterable[Symbol.asyncIterator];
  if (m != null) { it = m.call(iterable); isAsync = true; }
  else {
    m = iterable == null ? undefined : iterable[Symbol.iterator];
    if (m == null) throw new TypeError('object is not iterable');
    it = m.call(iterable);
  }
  if (!isObj(it)) throw new TypeError('iterator is not an object');
  var next = it.next;
  return new ReadableStream({
    pull: function (c) {
      return Promise.resolve(next.call(it)).then(function (r) {
        if (!isObj(r)) throw new TypeError('iterator result is not an object');
        if (r.done) c.close(); else if (isAsync) c.enqueue(r.value); else return Promise.resolve(r.value).then(function (v) { c.enqueue(v); });
      });
    },
    cancel: function (reason) {
      var ret = it.return;
      if (ret == null) return undefined;
      return Promise.resolve(ret.call(it, reason)).then(function (r) { if (!isObj(r)) throw new TypeError('return() result is not an object'); });
    }
  }, { highWaterMark: 0 });
});
H.tag(ReadableStream, 'ReadableStream');

// ---- readers ----------------------------------------------------------------------
function initReader(reader, stream, byob) {
  var s = slots(stream, 'RS');
  if (rsLocked(s)) throw new TypeError('ReadableStream is already locked');
  var r = { brand: byob ? 'BYOB' : 'DR', stream: s, readRequests: [], closed: defer(), byob: byob, self: reader };
  putSlots(reader, r);
  s.reader = r;
  if (s.state === 'closed') r.closed.resolve(undefined);
  else if (s.state === 'errored') { r.closed.reject(s.storedError); handled(r.closed.promise); }
}
function readerRelease(r) {
  var s = r.stream;
  if (!s) return;
  var err = new TypeError('Reader was released');
  if (s.state === 'readable') r.closed.reject(err); else r.closed = { promise: rejected(err) };
  handled(r.closed.promise);
  var reqs = r.readRequests; r.readRequests = [];
  reqs.forEach(function (q) { q.error(err); });
  if (s.ctrl && s.ctrl.isByte && s.ctrl.pending.length) { var pend = s.ctrl.pending; s.ctrl.pending = []; pend.forEach(function (en) { en.req.error(err); }); }
  s.reader = null; r.stream = null;
}
function ReadableStreamDefaultReader(stream) {
  if (!new.target) throw new TypeError("Constructor ReadableStreamDefaultReader requires 'new'");
  initReader(this, stream, false);
}
Object.defineProperty(ReadableStreamDefaultReader.prototype, 'closed', { get: function () { try { return slots(this, 'DR').closed.promise; } catch (e) { return rejected(e); } }, enumerable: true, configurable: true });
method(ReadableStreamDefaultReader.prototype, 'read', function read() {
  var r; try { r = slots(this, 'DR'); } catch (e) { return rejected(e); }
  if (!r.stream) return rejected(new TypeError('Reader has been released'));
  var d = defer(), s = r.stream;
  var req = { chunk: function (v) { d.resolve({ value: v, done: false }); }, close: function () { d.resolve({ value: undefined, done: true }); }, error: function (e) { d.reject(e); } };
  s.disturbed = true;
  if (s.state === 'closed') req.close();
  else if (s.state === 'errored') req.error(s.storedError);
  else s.ctrl.pullSteps(req, r);
  return d.promise;
});
method(ReadableStreamDefaultReader.prototype, 'releaseLock', function releaseLock() { var r = slots(this, 'DR'); readerRelease(r); });
method(ReadableStreamDefaultReader.prototype, 'cancel', function cancel(reason) {
  var r; try { r = slots(this, 'DR'); } catch (e) { return rejected(e); }
  if (!r.stream) return rejected(new TypeError('Reader has been released'));
  return rsCancel(r.stream, reason);
});
H.tag(ReadableStreamDefaultReader, 'ReadableStreamDefaultReader');

function ReadableStreamBYOBReader(stream) {
  if (!new.target) throw new TypeError("Constructor ReadableStreamBYOBReader requires 'new'");
  var s = slots(stream, 'RS');
  if (!s.ctrl.isByte) throw new TypeError('Cannot construct a BYOB reader for a non-byte stream');
  initReader(this, stream, true);
}
Object.defineProperty(ReadableStreamBYOBReader.prototype, 'closed', { get: function () { try { return slots(this, 'BYOB').closed.promise; } catch (e) { return rejected(e); } }, enumerable: true, configurable: true });
method(ReadableStreamBYOBReader.prototype, 'read', function read(view, options) {
  var r; try { r = slots(this, 'BYOB'); } catch (e) { return rejected(e); }
  if (!ArrayBuffer.isView(view)) return rejected(new TypeError('view must be an ArrayBufferView'));
  if (view.byteLength === 0) return rejected(new TypeError('view must have non-zero byteLength'));
  if (view.buffer.byteLength === 0) return rejected(new TypeError('view buffer must have non-zero byteLength'));
  var min = options && options.min !== undefined ? Number(options.min) : 1;
  if (!(min >= 1)) return rejected(new TypeError('options.min must be greater than 0'));
  if (min > view.byteLength / (view.BYTES_PER_ELEMENT || 1)) return rejected(new RangeError('options.min is larger than the view'));
  if (!r.stream) return rejected(new TypeError('Reader has been released'));
  var d = defer(), s = r.stream;
  var req = { chunk: function (v) { d.resolve({ value: v, done: false }); }, close: function (v) { d.resolve({ value: v, done: true }); }, error: function (e) { d.reject(e); } };
  s.disturbed = true;
  if (s.state === 'errored') req.error(s.storedError);
  else s.ctrl.pullInto(view, min, req);
  return d.promise;
});
method(ReadableStreamBYOBReader.prototype, 'releaseLock', function releaseLock() { readerRelease(slots(this, 'BYOB')); });
method(ReadableStreamBYOBReader.prototype, 'cancel', function cancel(reason) {
  var r; try { r = slots(this, 'BYOB'); } catch (e) { return rejected(e); }
  if (!r.stream) return rejected(new TypeError('Reader has been released'));
  return rsCancel(r.stream, reason);
});
H.tag(ReadableStreamBYOBReader, 'ReadableStreamBYOBReader');

// ---- default controller ----------------------------------------------------------
function ReadableStreamDefaultController() { throw new TypeError('Illegal constructor'); }
function setupDefaultController(s, src, hwm, sizeFn) {
  var c = { brand: 'RDC', stream: s, started: false, pulling: false, pullAgain: false, closeRequested: false, hwm: hwm, sizeFn: sizeFn, isByte: false };
  Q(c);
  var self = Object.create(ReadableStreamDefaultController.prototype);
  putSlots(self, c);
  c.self = self;
  var start = getAlg(src, 'start'), pull = getAlg(src, 'pull'), cancel = getAlg(src, 'cancel');
  c.pullAlg = pull ? function () { return promiseCall(pull, src, [self]); } : function () { return Promise.resolve(); };
  c.cancelAlg = cancel ? function (r) { return promiseCall(cancel, src, [r]); } : function () { return Promise.resolve(); };
  c.desired = function () { return c.hwm - c.total; };
  c.cancelSteps = function (reason) { qReset(c); var res = c.cancelAlg(reason); c.pullAlg = c.cancelAlg = null; return res; };
  c.pullSteps = function (req) {
    if (c.queue.length) {
      var chunk = qDequeue(c);
      if (c.closeRequested && !c.queue.length) { clear(c); rsClose(s); } else callPull(c);
      req.chunk(chunk);
    } else { s.reader.readRequests.push(req); callPull(c); }
  };
  s.ctrl = c;
  var sr;
  try { sr = start ? start.call(src, self) : undefined; } catch (e) { throw e; }
  Promise.resolve(sr).then(function () { c.started = true; callPull(c); }, function (e) { dcError(c, e); });
}
function clear(c) { c.pullAlg = null; c.cancelAlg = null; c.sizeFn = null; }
function canCloseOrEnqueue(c) { return !c.closeRequested && c.stream.state === 'readable'; }
function shouldPull(c) {
  var s = c.stream;
  if (!canCloseOrEnqueue(c) || !c.started) return false;
  if (rsLocked(s) && s.reader.readRequests.length > 0) return true;
  var d = c.desired();
  return d !== null && d > 0;
}
function callPull(c) {
  if (!shouldPull(c)) return;
  if (c.pulling) { c.pullAgain = true; return; }
  c.pulling = true;
  c.pullAlg().then(function () { c.pulling = false; if (c.pullAgain) { c.pullAgain = false; callPull(c); } }, function (e) { dcError(c, e); });
}
function dcError(c, e) {
  var s = c.stream;
  if (s.state !== 'readable') return;
  qReset(c); clear(c); rsError(s, e);
}
Object.defineProperty(ReadableStreamDefaultController.prototype, 'desiredSize', { get: function () { return rsDesired(slots(this, 'RDC').stream); }, enumerable: true, configurable: true });
method(ReadableStreamDefaultController.prototype, 'close', function close() {
  var c = slots(this, 'RDC');
  if (!canCloseOrEnqueue(c)) throw new TypeError('The stream is not in a state that permits close');
  c.closeRequested = true;
  if (!c.queue.length) { clear(c); rsClose(c.stream); }
});
method(ReadableStreamDefaultController.prototype, 'enqueue', function enqueue(chunk) {
  var c = slots(this, 'RDC'), s = c.stream;
  if (!canCloseOrEnqueue(c)) throw new TypeError('The stream is not in a state that permits enqueue');
  if (rsLocked(s) && s.reader.readRequests.length > 0) s.reader.readRequests.shift().chunk(chunk);
  else {
    var size = 1;
    if (c.sizeFn) { try { size = c.sizeFn(chunk); } catch (e) { dcError(c, e); throw e; } }
    try { qEnqueue(c, chunk, size); } catch (e) { dcError(c, e); throw e; }
  }
  callPull(c);
});
method(ReadableStreamDefaultController.prototype, 'error', function error(e) { dcError(slots(this, 'RDC'), e); });
H.tag(ReadableStreamDefaultController, 'ReadableStreamDefaultController');

// ---- byte controller (chunks are copied on enqueue; no auto-allocation) -----------------
function ReadableByteStreamController() { throw new TypeError('Illegal constructor'); }
function ReadableStreamBYOBRequest() { throw new TypeError('Illegal constructor'); }
function setupByteController(s, src, hwm) {
  var c = { brand: 'RBC', stream: s, started: false, pulling: false, pullAgain: false, closeRequested: false, hwm: hwm, isByte: true, pending: [], bytes: 0, auto: src.autoAllocateChunkSize };
  Q(c);
  if (c.auto !== undefined) { c.auto = Number(c.auto); if (!(c.auto > 0) || c.auto !== Math.floor(c.auto)) throw new TypeError('autoAllocateChunkSize must be a positive integer'); }
  var self = Object.create(ReadableByteStreamController.prototype);
  putSlots(self, c); c.self = self;
  var start = getAlg(src, 'start'), pull = getAlg(src, 'pull'), cancel = getAlg(src, 'cancel');
  c.pullAlg = pull ? function () { return promiseCall(pull, src, [self]); } : function () { return Promise.resolve(); };
  c.cancelAlg = cancel ? function (r) { return promiseCall(cancel, src, [r]); } : function () { return Promise.resolve(); };
  c.desired = function () { return c.hwm - c.bytes; };
  c.cancelSteps = function (reason) { c.queue = []; c.bytes = 0; var res = c.cancelAlg(reason); c.pullAlg = c.cancelAlg = null; return res; };
  function shouldPullB() {
    if (c.stream.state !== 'readable' || c.closeRequested || !c.started) return false;
    var r = c.stream.reader;
    if (r && r.readRequests.length > 0) return true;
    if (c.pending.length > 0) return true;
    return c.desired() > 0;
  }
  c.callPull = function () {
    if (!shouldPullB()) return;
    if (c.pulling) { c.pullAgain = true; return; }
    c.pulling = true;
    c.pullAlg().then(function () { c.pulling = false; if (c.pullAgain) { c.pullAgain = false; c.callPull(); } }, function (e) { bcError(c, e); });
  };
  // Serve readers from the byte queue.
  c.take = function (n) {
    var out = new Uint8Array(n), off = 0;
    while (off < n && c.queue.length) {
      var h = c.queue[0], k = Math.min(n - off, h.bytes.length);
      out.set(h.bytes.subarray(0, k), off); off += k;
      if (k === h.bytes.length) c.queue.shift(); else h.bytes = h.bytes.subarray(k);
      c.bytes -= k;
    }
    return out.subarray(0, off);
  };
  c.pullSteps = function (req) {
    if (c.bytes > 0) {
      var chunk = c.take(c.queue[0].bytes.length);
      afterTake();
      req.chunk(chunk); return;
    }
    if (c.auto) {
      var buf = new Uint8Array(c.auto);
      c.pending.push({ view: buf, filled: 0, min: 1, req: req, autoAlloc: true });
    } else s.reader.readRequests.push(req);
    c.callPull();
  };
  function afterTake() {
    if (c.closeRequested && c.bytes === 0) { bcClearAlgs(c); rsClose(s); } else c.callPull();
  }
  c.pullInto = function (view, min, req) {
    var elem = view.BYTES_PER_ELEMENT || 1;
    var u8 = new Uint8Array(view.buffer, view.byteOffset, view.byteLength);
    var minBytes = min * elem;
    if (c.bytes >= Math.min(minBytes, u8.length) && c.bytes > 0) {
      var n = Math.min(u8.length, c.bytes);
      n -= n % elem;
      if (n > 0) {
        u8.set(c.take(n), 0);
        afterTake();
        req.chunk(new view.constructor(view.buffer, view.byteOffset, n / elem)); return;
      }
    }
    if (s.state === 'closed' || (c.closeRequested && c.bytes === 0)) {
      if (c.closeRequested && c.bytes === 0) { bcClearAlgs(c); rsClose(s); }
      req.close(new view.constructor(view.buffer, view.byteOffset, 0)); return;
    }
    var entry = { view: u8, ctor: view.constructor, elem: elem, byteOffset: view.byteOffset, buffer: view.buffer, filled: 0, min: minBytes, req: req };
    c.pending.push(entry);
    c.callPull();
  };
  function respondEntry(entry, n) {
    entry.filled += n;
    if (entry.autoAlloc) {
      c.pending.shift();
      entry.req.chunk(entry.view.subarray(0, entry.filled)); return;
    }
    if (entry.filled < entry.min && s.state === 'readable') return;
    c.pending.shift();
    var whole = entry.filled - entry.filled % entry.elem;
    entry.req.chunk(new entry.ctor(entry.buffer, entry.byteOffset, whole / entry.elem));
  }
  c.respond = function (n) {
    var entry = c.pending[0];
    if (!entry) throw new TypeError('No pending BYOB request');
    if (s.state === 'closed') { if (n !== 0) throw new TypeError('bytesWritten must be 0 for a closed stream'); c.pending.shift(); entry.req.close(new (entry.ctor || Uint8Array)(entry.buffer || entry.view.buffer, entry.byteOffset || 0, 0)); return; }
    if (n === 0) throw new TypeError('bytesWritten must be greater than 0');
    if (entry.filled + n > entry.view.length) throw new RangeError('bytesWritten out of range');
    respondEntry(entry, n);
    c.callPull();
  };
  c.flushClosed = function () {
    var pend = c.pending; c.pending = [];
    pend.forEach(function (en) { en.req.close(en.autoAlloc ? undefined : new en.ctor(en.buffer, en.byteOffset, 0)); });
  };
  c.getRequest = function () {
    var entry = c.pending[0];
    if (!entry) return null;
    if (!entry.reqObj) {
      var o = Object.create(ReadableStreamBYOBRequest.prototype);
      var view = new Uint8Array(entry.view.buffer, entry.view.byteOffset + entry.filled, entry.view.length - entry.filled);
      putSlots(o, { brand: 'BREQ', view: view, entry: entry, ctrl: c });
      entry.reqObj = o;
    }
    return entry.reqObj;
  };
  s.ctrl = c;
  var sr = start ? start.call(src, self) : undefined;
  Promise.resolve(sr).then(function () { c.started = true; c.callPull(); }, function (e) { bcError(c, e); });
}
function bcClearAlgs(c) { c.pullAlg = null; c.cancelAlg = null; }
function bcError(c, e) {
  var s = c.stream;
  if (s.state !== 'readable') return;
  var pend = c.pending;
  c.pending = []; c.queue = []; c.bytes = 0; bcClearAlgs(c); rsError(s, e);
  pend.forEach(function (en) { en.req.error(e); });
}
Object.defineProperty(ReadableByteStreamController.prototype, 'desiredSize', { get: function () { return rsDesired(slots(this, 'RBC').stream); }, enumerable: true, configurable: true });
Object.defineProperty(ReadableByteStreamController.prototype, 'byobRequest', { get: function () { return slots(this, 'RBC').getRequest(); }, enumerable: true, configurable: true });
method(ReadableByteStreamController.prototype, 'close', function close() {
  var c = slots(this, 'RBC');
  if (c.closeRequested || c.stream.state !== 'readable') throw new TypeError('The stream is not in a state that permits close');
  c.closeRequested = true;
  if (c.bytes > 0) return;
  var first = c.pending[0];
  if (first && first.filled % (first.elem || 1) !== 0) { var e = new TypeError('Insufficient bytes to fill elements in the given buffer'); bcError(c, e); throw e; }
  bcClearAlgs(c); rsClose(c.stream);
});
method(ReadableByteStreamController.prototype, 'enqueue', function enqueue(chunk) {
  var c = slots(this, 'RBC'), s = c.stream;
  if (!ArrayBuffer.isView(chunk)) throw new TypeError('chunk must be an ArrayBufferView');
  if (chunk.byteLength === 0) throw new TypeError('chunk must have non-zero byteLength');
  if (chunk.buffer.byteLength === 0) throw new TypeError('chunk buffer must have non-zero byteLength');
  if (c.closeRequested) throw new TypeError('stream is closed or draining');
  if (s.state !== 'readable') throw new TypeError('stream is not readable');
  var bytes = new Uint8Array(chunk.byteLength);
  bytes.set(new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength));
  var r = s.reader;
  if (r && !r.byob && r.readRequests.length > 0) { r.readRequests.shift().chunk(bytes); c.callPull(); return; }
  if (r && c.pending.length > 0) {
    // Feed pending BYOB / auto-allocated reads first.
    var off = 0;
    while (off < bytes.length && c.pending.length) {
      var en = c.pending[0], k = Math.min(en.view.length - en.filled, bytes.length - off);
      en.view.set(bytes.subarray(off, off + k), en.filled); off += k;
      respondEntry(en, k);
    }
    if (off < bytes.length) { c.queue.push({ bytes: bytes.subarray(off) }); c.bytes += bytes.length - off; }
    c.callPull(); return;
  }
  c.queue.push({ bytes: bytes }); c.bytes += bytes.length;
  c.callPull();
});
method(ReadableByteStreamController.prototype, 'error', function error(e) { bcError(slots(this, 'RBC'), e); });
H.tag(ReadableByteStreamController, 'ReadableByteStreamController');
Object.defineProperty(ReadableStreamBYOBRequest.prototype, 'view', { get: function () { return slots(this, 'BREQ').view; }, enumerable: true, configurable: true });
method(ReadableStreamBYOBRequest.prototype, 'respond', function respond(n) { var b = slots(this, 'BREQ'); b.ctrl.respond(Number(n)); b.entry.reqObj = null; });
method(ReadableStreamBYOBRequest.prototype, 'respondWithNewView', function respondWithNewView(view) {
  var b = slots(this, 'BREQ'), en = b.entry;
  if (!ArrayBuffer.isView(view)) throw new TypeError('view must be an ArrayBufferView');
  var u8 = new Uint8Array(view.buffer, view.byteOffset, view.byteLength);
  if (en.view.buffer === view.buffer) { b.ctrl.respond(view.byteLength); } else { en.view.set(u8, en.filled); b.ctrl.respond(view.byteLength); }
  en.reqObj = null;
});
H.tag(ReadableStreamBYOBRequest, 'ReadableStreamBYOBRequest');

// ---- tee ----------------------------------------------------------------------------------
function teeStream(stream, s) {
  var reader = new ReadableStreamDefaultReader(stream);
  var reading = false, readAgain = false, canceled1 = false, canceled2 = false, reason1, reason2, b1, b2;
  var cancelP = defer();
  function pullAlg() {
    if (reading) { readAgain = true; return Promise.resolve(); }
    reading = true;
    reader.read().then(function (r) {
      readAgain = false;
      if (r.done) {
        reading = false;
        if (!canceled1) b1.ctrl.self.close();
        if (!canceled2) b2.ctrl.self.close();
        if (!canceled1 || !canceled2) cancelP.resolve(undefined);
        return;
      }
      var v = r.value, v2 = v;
      if (!canceled1) b1.ctrl.self.enqueue(v);
      if (!canceled2) b2.ctrl.self.enqueue(v2);
      reading = false;
      if (readAgain) pullAlg();
    }, function () { reading = false; });
    return Promise.resolve();
  }
  function cancel1(r) {
    canceled1 = true; reason1 = r;
    if (canceled2) { cancelP.resolve(rsCancel(s, [reason1, reason2])); }
    return cancelP.promise;
  }
  function cancel2(r) {
    canceled2 = true; reason2 = r;
    if (canceled1) { cancelP.resolve(rsCancel(s, [reason1, reason2])); }
    return cancelP.promise;
  }
  var br1 = new ReadableStream({ pull: pullAlg, cancel: cancel1 }, { highWaterMark: 0 });
  var br2 = new ReadableStream({ pull: pullAlg, cancel: cancel2 }, { highWaterMark: 0 });
  b1 = br1[$]; b2 = br2[$];
  reader[$].closed.promise.then(undefined, function (e) {
    b1.ctrl.self.error(e); b2.ctrl.self.error(e);
    if (!canceled1 || !canceled2) cancelP.resolve(undefined);
  });
  return [br1, br2];
}

// ======================================================================================
// WritableStream
// ======================================================================================
function WritableStream(sink, strategy) {
  if (!new.target) throw new TypeError("Constructor WritableStream requires 'new'");
  var s = { brand: 'WS', state: 'writable', storedError: undefined, writer: null, ctrl: null, inFlightWrite: null, inFlightClose: null, closeReq: null, pendingAbort: null, writeRequests: [], backpressure: false };
  putSlots(this, s);
  if (sink === undefined) sink = null;
  var sinkObj = sink === null ? {} : sink;
  if (!isObj(sinkObj)) throw new TypeError('underlyingSink must be an object');
  if (sinkObj.type !== undefined) throw new RangeError('Invalid type is specified');
  var size = extractSize(strategy), hwm = extractHWM(strategy, 1);
  setupWritableController(s, sinkObj, hwm, size);
}
function wsLocked(s) { return s.writer !== null; }
function wsCloseQueuedOrInFlight(s) { return s.closeReq !== null || s.inFlightClose !== null; }
function wsHasOp(s) { return s.inFlightWrite !== null || s.inFlightClose !== null; }
Object.defineProperty(WritableStream.prototype, 'locked', { get: function () { return wsLocked(slots(this, 'WS')); }, enumerable: true, configurable: true });
method(WritableStream.prototype, 'abort', function abort(reason) {
  var s; try { s = slots(this, 'WS'); } catch (e) { return rejected(e); }
  if (wsLocked(s)) return rejected(new TypeError('Cannot abort a stream that is locked to a writer'));
  return wsAbort(s, reason);
});
method(WritableStream.prototype, 'close', function close() {
  var s; try { s = slots(this, 'WS'); } catch (e) { return rejected(e); }
  if (wsLocked(s)) return rejected(new TypeError('Cannot close a stream that is locked to a writer'));
  if (wsCloseQueuedOrInFlight(s)) return rejected(new TypeError('Cannot close an already-closing stream'));
  return wsClose(s);
});
method(WritableStream.prototype, 'getWriter', function getWriter() { slots(this, 'WS'); return new WritableStreamDefaultWriter(this); });
H.tag(WritableStream, 'WritableStream');

function wsAbort(s, reason) {
  if (s.state === 'closed' || s.state === 'errored') return Promise.resolve(undefined);
  s.ctrl.abortCtl.abort(reason);
  if (s.state === 'closed' || s.state === 'errored') return Promise.resolve(undefined);
  if (s.pendingAbort) return s.pendingAbort.promise.promise;
  var wasErroring = s.state === 'erroring';
  if (wasErroring) reason = undefined;
  var d = defer();
  s.pendingAbort = { promise: d, reason: reason, wasAlreadyErroring: wasErroring };
  if (!wasErroring) wsStartErroring(s, reason);
  return d.promise;
}
function wsClose(s) {
  if (s.state === 'closed' || s.state === 'errored') return rejected(new TypeError('The stream is not writable'));
  var d = defer();
  s.closeReq = d;
  var w = s.writer;
  if (w && s.backpressure && s.state === 'writable') w.ready.resolve(undefined);
  wcClose(s.ctrl);
  return d.promise;
}
function wsAddWrite(s) {
  var d = defer();
  s.writeRequests.push(d);
  return d.promise;
}
function wsDealWithRejection(s, e) {
  if (s.state === 'writable') { wsStartErroring(s, e); return; }
  wsFinishErroring(s);
}
function wsStartErroring(s, reason) {
  s.state = 'erroring'; s.storedError = reason;
  var w = s.writer;
  if (w) writerEnsureReadyRejected(w, reason);
  if (!wsHasOp(s) && s.ctrl.started) wsFinishErroring(s);
}
function wsFinishErroring(s) {
  s.state = 'errored';
  s.ctrl.errorSteps();
  var e = s.storedError;
  s.writeRequests.forEach(function (d) { d.reject(e); });
  s.writeRequests = [];
  if (!s.pendingAbort) { wsRejectCloseAndClosedIfNeeded(s); return; }
  var pa = s.pendingAbort; s.pendingAbort = null;
  if (pa.wasAlreadyErroring) { pa.promise.reject(e); wsRejectCloseAndClosedIfNeeded(s); return; }
  s.ctrl.abortSteps(pa.reason).then(function () { pa.promise.resolve(undefined); wsRejectCloseAndClosedIfNeeded(s); }, function (er) { pa.promise.reject(er); wsRejectCloseAndClosedIfNeeded(s); });
}
function wsFinishInFlightWrite(s) { s.inFlightWrite.resolve(undefined); s.inFlightWrite = null; }
function wsFinishInFlightWriteWithError(s, e) { s.inFlightWrite.reject(e); s.inFlightWrite = null; wsDealWithRejection(s, e); }
function wsFinishInFlightClose(s) {
  s.inFlightClose.resolve(undefined); s.inFlightClose = null;
  if (s.state === 'erroring') {
    s.storedError = undefined;
    if (s.pendingAbort) { s.pendingAbort.promise.resolve(undefined); s.pendingAbort = null; }
  }
  s.state = 'closed';
  var w = s.writer;
  if (w) w.closed.resolve(undefined);
}
function wsFinishInFlightCloseWithError(s, e) {
  s.inFlightClose.reject(e); s.inFlightClose = null;
  if (s.pendingAbort) { s.pendingAbort.promise.reject(e); s.pendingAbort = null; }
  wsDealWithRejection(s, e);
}
function wsMarkCloseRequestInFlight(s) { s.inFlightClose = s.closeReq; s.closeReq = null; }
function wsMarkFirstWriteInFlight(s) { s.inFlightWrite = s.writeRequests.shift(); }
function wsRejectCloseAndClosedIfNeeded(s) {
  if (s.closeReq) { s.closeReq.reject(s.storedError); s.closeReq = null; }
  var w = s.writer;
  if (w) { w.closed.reject(s.storedError); handled(w.closed.promise); }
}
function wsUpdateBackpressure(s, bp) {
  var w = s.writer;
  if (w && bp !== s.backpressure) {
    if (bp) w.ready = defer(); else w.ready.resolve(undefined);
  }
  s.backpressure = bp;
}
function writerEnsureReadyRejected(w, e) {
  if (w.ready.settled) w.ready = defer();
  w.ready.reject(e); handled(w.ready.promise);
}

function WritableStreamDefaultWriter(stream) {
  if (!new.target) throw new TypeError("Constructor WritableStreamDefaultWriter requires 'new'");
  var s = slots(stream, 'WS');
  if (wsLocked(s)) throw new TypeError('WritableStream is already locked');
  var w = { brand: 'WW', stream: s, closed: defer(), ready: defer() };
  putSlots(this, w); s.writer = w;
  // `settled` bookkeeping lets us decide whether a fresh ready promise is needed.
  if (s.state === 'writable') {
    if (!wsCloseQueuedOrInFlight(s) && s.backpressure) { /* ready stays pending */ } else w.ready.resolve(undefined);
  } else if (s.state === 'erroring') { w.ready.reject(s.storedError); handled(w.ready.promise); }
  else if (s.state === 'closed') { w.ready.resolve(undefined); w.closed.resolve(undefined); }
  else { w.ready.reject(s.storedError); handled(w.ready.promise); w.closed.reject(s.storedError); handled(w.closed.promise); }
}
function wDesired(w) {
  var s = w.stream;
  if (s.state === 'errored' || s.state === 'erroring') return null;
  if (s.state === 'closed') return 0;
  return s.ctrl.desired();
}
Object.defineProperties(WritableStreamDefaultWriter.prototype, {
  closed: { get: function () { try { return slots(this, 'WW').closed.promise; } catch (e) { return rejected(e); } }, enumerable: true, configurable: true },
  ready: { get: function () { try { return slots(this, 'WW').ready.promise; } catch (e) { return rejected(e); } }, enumerable: true, configurable: true },
  desiredSize: { get: function () { var w = slots(this, 'WW'); if (!w.stream) throw new TypeError('Writer has been released'); return wDesired(w); }, enumerable: true, configurable: true }
});
method(WritableStreamDefaultWriter.prototype, 'abort', function abort(reason) {
  var w; try { w = slots(this, 'WW'); } catch (e) { return rejected(e); }
  if (!w.stream) return rejected(new TypeError('Writer has been released'));
  return wsAbort(w.stream, reason);
});
method(WritableStreamDefaultWriter.prototype, 'close', function close() {
  var w; try { w = slots(this, 'WW'); } catch (e) { return rejected(e); }
  var s = w.stream;
  if (!s) return rejected(new TypeError('Writer has been released'));
  if (wsCloseQueuedOrInFlight(s)) return rejected(new TypeError('Cannot close an already-closing stream'));
  return wsClose(s);
});
method(WritableStreamDefaultWriter.prototype, 'releaseLock', function releaseLock() {
  var w = slots(this, 'WW'), s = w.stream;
  if (!s) return;
  var err = new TypeError('Writer was released');
  writerEnsureReadyRejected(w, err);
  if (w.closed.settled) w.closed = defer();
  w.closed.reject(err); handled(w.closed.promise);
  s.writer = null; w.stream = null;
});
method(WritableStreamDefaultWriter.prototype, 'write', function write(chunk) {
  var w; try { w = slots(this, 'WW'); } catch (e) { return rejected(e); }
  var s = w.stream;
  if (!s) return rejected(new TypeError('Writer has been released'));
  return wcWrite(s.ctrl, chunk, w);
});
H.tag(WritableStreamDefaultWriter, 'WritableStreamDefaultWriter');

function WritableStreamDefaultController() { throw new TypeError('Illegal constructor'); }
function setupWritableController(s, sink, hwm, sizeFn) {
  var c = { brand: 'WDC', stream: s, started: false, hwm: hwm, sizeFn: sizeFn, abortCtl: new g.AbortController() };
  Q(c);
  var self = Object.create(WritableStreamDefaultController.prototype);
  putSlots(self, c); c.self = self;
  var start = getAlg(sink, 'start'), write = getAlg(sink, 'write'), close = getAlg(sink, 'close'), abort = getAlg(sink, 'abort');
  c.writeAlg = write ? function (ch) { return promiseCall(write, sink, [ch, self]); } : function () { return Promise.resolve(); };
  c.closeAlg = close ? function () { return promiseCall(close, sink, []); } : function () { return Promise.resolve(); };
  c.abortAlg = abort ? function (r) { return promiseCall(abort, sink, [r]); } : function () { return Promise.resolve(); };
  c.desired = function () { return c.hwm - c.total; };
  c.abortSteps = function (reason) { var r = c.abortAlg(reason); wcClearAlgs(c); return r; };
  c.errorSteps = function () { qReset(c); };
  s.ctrl = c;
  wsUpdateBackpressure(s, wcBackpressure(c));
  var sr = start ? start.call(sink, self) : undefined;
  Promise.resolve(sr).then(function () {
    c.started = true;
    wcAdvance(c);
  }, function (e) {
    c.started = true;
    wsDealWithRejection(s, e);
  });
}
function wcClearAlgs(c) { c.writeAlg = c.closeAlg = c.abortAlg = null; c.sizeFn = null; }
var CLOSE = { close: true };
function wcBackpressure(c) { return c.desired() <= 0; }
function wcClose(c) { qEnqueue(c, CLOSE, 0); wcAdvance(c); }
function wcGetChunkSize(c, chunk) {
  if (!c.sizeFn) return 1;
  try { return c.sizeFn(chunk); } catch (e) { wcErrorIfNeeded(c, e); return 1; }
}
function wcWrite(c, chunk, w) {
  var s = c.stream;
  var size = wcGetChunkSize(c, chunk);
  if (s !== w.stream) return rejected(new TypeError('Writer has been released'));
  var st = s.state;
  if (st === 'errored') return rejected(s.storedError);
  if (wsCloseQueuedOrInFlight(s) || st === 'closed') return rejected(new TypeError('The stream is closing or closed'));
  if (st === 'erroring') return rejected(s.storedError);
  var p = wsAddWrite(s);
  try { qEnqueue(c, chunk, size); } catch (e) { wcErrorIfNeeded(c, e); return p; }
  if (!wsCloseQueuedOrInFlight(s) && s.state === 'writable') wsUpdateBackpressure(s, wcBackpressure(c));
  wcAdvance(c);
  return p;
}
function wcAdvance(c) {
  var s = c.stream;
  if (!c.started) return;
  if (s.inFlightWrite !== null) return;
  var st = s.state;
  if (st === 'closed' || st === 'errored') return;
  if (st === 'erroring') { wsFinishErroring(s); return; }
  if (!c.queue.length) return;
  var v = c.queue[0].value;
  if (v === CLOSE) wcProcessClose(c); else wcProcessWrite(c, v);
}
function wcErrorIfNeeded(c, e) { if (c.stream.state === 'writable') wcError(c, e); }
function wcError(c, e) {
  var s = c.stream;
  wcClearAlgs(c);
  wsStartErroring(s, e);
}
function wcProcessClose(c) {
  var s = c.stream;
  wsMarkCloseRequestInFlight(s);
  qDequeue(c);
  var p = c.closeAlg();
  wcClearAlgs(c);
  p.then(function () { wsFinishInFlightClose(s); }, function (e) { wsFinishInFlightCloseWithError(s, e); });
}
function wcProcessWrite(c, chunk) {
  var s = c.stream;
  wsMarkFirstWriteInFlight(s);
  c.writeAlg(chunk).then(function () {
    wsFinishInFlightWrite(s);
    var st = s.state;
    qDequeue(c);
    if (!wsCloseQueuedOrInFlight(s) && st === 'writable') wsUpdateBackpressure(s, wcBackpressure(c));
    wcAdvance(c);
  }, function (e) {
    if (s.state === 'writable') wcClearAlgs(c);
    wsFinishInFlightWriteWithError(s, e);
  });
}
Object.defineProperty(WritableStreamDefaultController.prototype, 'signal', { get: function () { return slots(this, 'WDC').abortCtl.signal; }, enumerable: true, configurable: true });
method(WritableStreamDefaultController.prototype, 'error', function error(e) {
  var c = slots(this, 'WDC');
  if (c.stream.state !== 'writable') return;
  wcError(c, e);
});
H.tag(WritableStreamDefaultController, 'WritableStreamDefaultController');

// ======================================================================================
// pipeTo
// ======================================================================================
function pipeTo(source, dest, preventClose, preventAbort, preventCancel, signal) {
  var rs = source[$], ws = dest[$];
  var reader = new ReadableStreamDefaultReader(source), writer = new WritableStreamDefaultWriter(dest);
  rs.disturbed = true;
  var shuttingDown = false, abortAlgorithm;
  var result = defer(), currentWrite = Promise.resolve(undefined);
  function finalize(isError, err) {
    writer.releaseLock(); reader.releaseLock();
    if (signal) signal.removeEventListener('abort', abortAlgorithm);
    if (isError) result.reject(err); else result.resolve(undefined);
  }
  function waitForWrites() {
    var cw = currentWrite;
    return cw.then(function () { return cw !== currentWrite ? waitForWrites() : undefined; });
  }
  function shutdownAction(action, origErr, hasErr) {
    if (shuttingDown) return;
    shuttingDown = true;
    if (ws.state === 'writable' && !wsCloseQueuedOrInFlight(ws)) waitForWrites().then(go, go); else go();
    function go() {
      action().then(function () { finalize(hasErr, origErr); }, function (e) { finalize(true, e); });
    }
  }
  function shutdown(isError, err) {
    if (shuttingDown) return;
    shuttingDown = true;
    if (ws.state === 'writable' && !wsCloseQueuedOrInFlight(ws)) waitForWrites().then(function () { finalize(isError, err); }); else finalize(isError, err);
  }
  if (signal) {
    abortAlgorithm = function () {
      var error = signal.reason !== undefined ? signal.reason : H.exc('This operation was aborted', 'AbortError');
      var actions = [];
      if (!preventAbort) actions.push(function () { return ws.state === 'writable' ? wsAbort(ws, error) : Promise.resolve(); });
      if (!preventCancel) actions.push(function () { return rs.state === 'readable' ? rsCancel(rs, error) : Promise.resolve(); });
      shutdownAction(function () { return Promise.all(actions.map(function (a) { return a(); })); }, error, true);
    };
    if (signal.aborted) { abortAlgorithm(); } else signal.addEventListener('abort', abortAlgorithm);
  }
  // errors/closure propagation
  function watch(p, fn) { p.then(fn, fn); }
  reader[$].closed.promise.then(function () {
    if (!rs.storedError && rs.state === 'closed') {
      if (!preventClose) shutdownAction(function () { return wsCloseForPipe(ws, writer); }, undefined, false); else shutdown(false);
    }
  }, function (e) {
    if (!preventAbort) shutdownAction(function () { return wsAbort(ws, e); }, e, true); else shutdown(true, e);
  });
  writer[$].closed.promise.then(function () {
    if (wsCloseQueuedOrInFlight(ws) || ws.state === 'closed') {
      var d = new TypeError('the destination writable stream closed before all data could be piped to it');
      if (!preventCancel) shutdownAction(function () { return rsCancel(rs, d); }, d, true); else shutdown(true, d);
    }
  }, function (e) {
    if (!preventCancel) shutdownAction(function () { return rsCancel(rs, e); }, e, true); else shutdown(true, e);
  });
  // main loop
  function loop() {
    if (shuttingDown) return;
    writer[$].ready.promise.then(function () {
      if (shuttingDown) return;
      reader.read().then(function (r) {
        if (r.done) return;
        currentWrite = handled(writer.write(r.value));
        loop();
      }, function () { });
    }, function () { });
  }
  loop();
  return result.promise;
}
function wsCloseForPipe(ws, writer) {
  var st = ws.state;
  if (wsCloseQueuedOrInFlight(ws) || st === 'closed') return Promise.resolve();
  if (st === 'errored') return rejected(ws.storedError);
  return writer.close();
}

// ======================================================================================
// TransformStream
// ======================================================================================
function TransformStream(transformer, writableStrategy, readableStrategy) {
  if (!new.target) throw new TypeError("Constructor TransformStream requires 'new'");
  if (transformer === undefined) transformer = null;
  var t = transformer === null ? {} : transformer;
  if (!isObj(t)) throw new TypeError('transformer must be an object');
  if (t.readableType !== undefined) throw new RangeError('Invalid readableType specified');
  if (t.writableType !== undefined) throw new RangeError('Invalid writableType specified');
  var wHWM = extractHWM(writableStrategy, 1), wSize = extractSize(writableStrategy);
  var rHWM = extractHWM(readableStrategy, 0), rSize = extractSize(readableStrategy);
  var startD = defer();
  var ts = { brand: 'TS', backpressure: undefined, backpressureChange: null };
  putSlots(this, ts);
  var transformFn = getAlg(t, 'transform'), flushFn = getAlg(t, 'flush'), cancelFn = getAlg(t, 'cancel'), startFn = getAlg(t, 'start');
  var ctlObj = Object.create(TransformStreamDefaultController.prototype);
  var cs = { brand: 'TDC', ts: ts };
  putSlots(ctlObj, cs);
  ts.ctrl = cs; cs.self = ctlObj;
  function transformAlg(chunk) {
    if (!transformFn) {
      try { tsEnqueue(ts, chunk); } catch (e) { return rejected(e); }
      return Promise.resolve();
    }
    return promiseCall(transformFn, t, [chunk, ctlObj]);
  }
  cs.transformAlg = transformAlg;
  cs.flushAlg = flushFn ? function () { return promiseCall(flushFn, t, [ctlObj]); } : function () { return Promise.resolve(); };
  cs.cancelAlg = cancelFn ? function (r) { return promiseCall(cancelFn, t, [r]); } : function () { return Promise.resolve(); };
  function setBackpressure(bp) {
    if (ts.backpressureChange) ts.backpressureChange.resolve(undefined);
    ts.backpressureChange = defer();
    ts.backpressure = bp;
  }
  ts.setBackpressure = setBackpressure;
  var sink = {
    start: function () { return startD.promise; },
    write: function (chunk) {
      var w = ts.writable[$];
      if (w.state !== 'writable') throw w.storedError;
      if (ts.backpressure) {
        return ts.backpressureChange.promise.then(function () {
          if (w.state === 'erroring') throw w.storedError;
          return cs.transformAlg(chunk).then(undefined, function (e) { tsError(ts, e); throw e; });
        });
      }
      return cs.transformAlg(chunk).then(undefined, function (e) { tsError(ts, e); throw e; });
    },
    close: function () {
      var r = ts.readable[$];
      var fp = cs.flushAlg();
      cs.transformAlg = cs.flushAlg = cs.cancelAlg = null;
      return fp.then(function () {
        if (r.state === 'errored') throw r.storedError;
        if (canCloseOrEnqueue(r.ctrl)) r.ctrl.self.close();
      }, function (e) { tsError(ts, e); throw r.storedError; });
    },
    abort: function (reason) {
      var ca = cs.cancelAlg;
      var p = ca ? ca(reason) : Promise.resolve();
      cs.transformAlg = cs.flushAlg = cs.cancelAlg = null;
      return p.then(function () {
        var r = ts.readable[$];
        if (r.state === 'errored') return;
        r.ctrl.self.error(reason);
      }, function (e) { tsError(ts, e); throw e; });
    }
  };
  var source = {
    start: function () { return startD.promise; },
    pull: function () { setBackpressure(false); return ts.backpressureChange.promise; },
    cancel: function (reason) {
      var ca = cs.cancelAlg;
      var p = ca ? ca(reason) : Promise.resolve();
      cs.transformAlg = cs.flushAlg = cs.cancelAlg = null;
      return p.then(function () {
        var w = ts.writable[$];
        if (w.state === 'errored') return;
        wcErrorIfNeeded(w.ctrl, reason);
        tsUnblockWrite(ts);
      }, function (e) { tsError(ts, e); throw e; });
    }
  };
  ts.writable = new WritableStream(sink, { highWaterMark: wHWM, size: wSize });
  ts.readable = new ReadableStream(source, { highWaterMark: rHWM, size: rSize });
  setBackpressure(true);
  var sr = startFn ? startFn.call(t, ctlObj) : undefined;
  startD.resolve(sr);
}
function tsEnqueue(ts, chunk) {
  var r = ts.readable[$], c = r.ctrl;
  if (!canCloseOrEnqueue(c)) throw new TypeError('Readable side is not in a state that permits enqueue');
  try { c.self.enqueue(chunk); } catch (e) { tsErrorWritableAndUnblock(ts, e); throw r.storedError; }
  var hasBP = !shouldPull(c);
  if (hasBP !== ts.backpressure) ts.setBackpressure(true);
}
function tsError(ts, e) {
  var r = ts.readable[$];
  r.ctrl.self.error(e);
  tsErrorWritableAndUnblock(ts, e);
}
function tsErrorWritableAndUnblock(ts, e) {
  var w = ts.writable[$];
  wcErrorIfNeeded(w.ctrl, e);
  tsUnblockWrite(ts);
}
function tsUnblockWrite(ts) { if (ts.backpressure) ts.setBackpressure(false); }
Object.defineProperties(TransformStream.prototype, {
  readable: { get: function () { return slots(this, 'TS').readable; }, enumerable: true, configurable: true },
  writable: { get: function () { return slots(this, 'TS').writable; }, enumerable: true, configurable: true }
});
H.tag(TransformStream, 'TransformStream');
function TransformStreamDefaultController() { throw new TypeError('Illegal constructor'); }
Object.defineProperty(TransformStreamDefaultController.prototype, 'desiredSize', { get: function () { var ts = slots(this, 'TDC').ts; return rsDesired(ts.readable[$]); }, enumerable: true, configurable: true });
method(TransformStreamDefaultController.prototype, 'enqueue', function enqueue(chunk) { tsEnqueue(slots(this, 'TDC').ts, chunk); });
method(TransformStreamDefaultController.prototype, 'error', function error(e) { tsError(slots(this, 'TDC').ts, e); });
method(TransformStreamDefaultController.prototype, 'terminate', function terminate() {
  var ts = slots(this, 'TDC').ts, r = ts.readable[$];
  if (canCloseOrEnqueue(r.ctrl)) r.ctrl.self.close();
  var e = new TypeError('TransformStream terminated');
  tsErrorWritableAndUnblock(ts, e);
});
H.tag(TransformStreamDefaultController, 'TransformStreamDefaultController');

// Exports. Byte-stream plumbing is shared through H for the compression module.
['ReadableStream', 'ReadableStreamDefaultReader', 'ReadableStreamBYOBReader', 'ReadableStreamDefaultController', 'ReadableByteStreamController', 'ReadableStreamBYOBRequest',
  'WritableStream', 'WritableStreamDefaultWriter', 'WritableStreamDefaultController', 'TransformStream', 'TransformStreamDefaultController',
  'CountQueuingStrategy', 'ByteLengthQueuingStrategy'].forEach(function (n) {
  H.glob(n, eval(n));
});
