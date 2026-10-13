/*
 * WHATWG Streams (default readers and writers only): ReadableStream, WritableStream, TransformStream with
 * queues, backpressure through the high water mark, pipeTo/pipeThrough, tee and async iteration.
 * Not implemented: byte streams and BYOB readers, queuing strategies other than count (a size() function
 * is honoured), pipeTo's signal option.
 * It replaces the minimal stand-ins of the base prelude; the stream-shaped Response.body objects of fetch
 * keep working because they only use getReader().read().
 */
(function (global) {
  'use strict';
  var Promise = global.Promise;
  function dex(m, n) { return new global.DOMException(m, n); }
  function defer() { var d = {}; d.promise = new Promise(function (res, rej) { d.resolve = res; d.reject = rej; }); return d; }
  function noop() { }
  function strategyOf(s, hwmDefault) {
    var hwm = s && s.highWaterMark !== undefined ? +s.highWaterMark : hwmDefault;
    if (isNaN(hwm) || hwm < 0) throw new RangeError('The strategy is not valid: highWaterMark must be a non-negative number');
    return { hwm: hwm, size: s && typeof s.size === 'function' ? s.size : function () { return 1; } };
  }

  // ---------------------------------------------------------------- ReadableStream
  var RS = new WeakMap();
  function ReadableStream(source, strategy) {
    if (!(this instanceof ReadableStream)) throw new TypeError("Failed to construct 'ReadableStream': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    source = source || {};
    var st = { src: source, queue: [], sizes: [], qsize: 0, closeReq: false, state: 'readable', err: undefined, reads: [], pulling: false, again: false, started: false, reader: null, strat: strategyOf(strategy, 1) };
    RS.set(this, st);
    var self = this;
    var ctrl = {
      get desiredSize() { return st.state === 'errored' ? null : st.state === 'closed' ? 0 : st.strat.hwm - st.qsize; },
      enqueue: function (chunk) {
        if (st.closeReq || st.state !== 'readable') throw new TypeError("Failed to execute 'enqueue' on 'ReadableStreamDefaultController': Cannot enqueue a chunk into a closed readable stream");
        if (st.reads.length) { st.reads.shift().resolve({ value: chunk, done: false }); }
        else { var sz = 1; try { sz = +st.strat.size(chunk); } catch (e) { rsError(st, e); throw e; } st.queue.push(chunk); st.sizes.push(sz); st.qsize += sz; }
        rsPull(self, st, ctrl);
      },
      close: function () {
        if (st.closeReq || st.state !== 'readable') throw new TypeError("Failed to execute 'close' on 'ReadableStreamDefaultController': Cannot close a readable stream that has already been requested to be closed");
        st.closeReq = true;
        if (!st.queue.length) rsClose(st);
      },
      error: function (e) { rsError(st, e); }
    };
    st.ctrl = ctrl;
    var started = new Promise(function (res) { res(source.start ? source.start(ctrl) : undefined); });
    started.then(function () { st.started = true; rsPull(self, st, ctrl); }, function (e) { rsError(st, e); });
  }
  function rsClose(st) {
    if (st.state !== 'readable') return;
    st.state = 'closed';
    st.reads.splice(0).forEach(function (r) { r.resolve({ value: undefined, done: true }); });
    if (st.reader) st.reader.closedD.resolve();
  }
  function rsError(st, e) {
    if (st.state !== 'readable') return;
    st.state = 'errored'; st.err = e; st.queue = []; st.sizes = []; st.qsize = 0;
    st.reads.splice(0).forEach(function (r) { r.reject(e); });
    if (st.reader) { st.reader.closedD.promise.catch(noop); st.reader.closedD.reject(e); }
  }
  function rsPull(stream, st, ctrl) {
    if (!st.started || st.state !== 'readable' || st.closeReq) return;
    if (!(st.reads.length || st.strat.hwm - st.qsize > 0)) return;
    if (st.pulling) { st.again = true; return; }
    if (!st.src.pull) return;
    st.pulling = true;
    var r;
    try { r = Promise.resolve(st.src.pull(ctrl)); } catch (e) { st.pulling = false; rsError(st, e); return; }
    r.then(function () { st.pulling = false; if (st.again) { st.again = false; rsPull(stream, st, ctrl); } }, function (e) { st.pulling = false; rsError(st, e); });
  }
  function rsRead(stream, st) {
    if (st.state === 'errored') return Promise.reject(st.err);
    if (st.queue.length) {
      var v = st.queue.shift(); st.qsize -= st.sizes.shift();
      if (st.closeReq && !st.queue.length) rsClose(st); else rsPull(stream, st, st.ctrl);
      return Promise.resolve({ value: v, done: false });
    }
    if (st.state === 'closed') return Promise.resolve({ value: undefined, done: true });
    var d = defer(); st.reads.push(d); rsPull(stream, st, st.ctrl);
    return d.promise;
  }
  function Reader(stream) {
    var st = RS.get(stream);
    if (st.reader) throw new TypeError("Failed to execute 'getReader' on 'ReadableStream': ReadableStreamDefaultReader constructor can only accept readable streams that are not yet locked to a reader");
    this._s = stream; st.reader = this;
    this.closedD = defer();
    if (st.state === 'closed') this.closedD.resolve(); else if (st.state === 'errored') { this.closedD.promise.catch(noop); this.closedD.reject(st.err); }
    Object.defineProperty(this, 'closed', { get: function () { return this.closedD.promise; } });
  }
  Reader.prototype.read = function () { if (!this._s) return Promise.reject(new TypeError('This readable stream reader has been released and cannot be used to read from its previous owner stream')); return rsRead(this._s, RS.get(this._s)); };
  Reader.prototype.releaseLock = function () { if (!this._s) return; var st = RS.get(this._s); st.reader = null; this._s = null; };
  Reader.prototype.cancel = function (r) { if (!this._s) return Promise.reject(new TypeError('This readable stream reader has been released and cannot be used to cancel its previous owner stream')); return rsCancel(this._s, r); };
  function rsCancel(stream, reason) {
    var st = RS.get(stream);
    if (st.state === 'closed') return Promise.resolve();
    if (st.state === 'errored') return Promise.reject(st.err);
    st.queue = []; st.sizes = []; st.qsize = 0;
    rsClose(st);
    try { return Promise.resolve(st.src.cancel ? st.src.cancel(reason) : undefined).then(function () { }); } catch (e) { return Promise.reject(e); }
  }
  Object.defineProperty(ReadableStream.prototype, 'locked', { get: function () { return !!RS.get(this).reader; }, configurable: true, enumerable: true });
  ReadableStream.prototype.getReader = function (opts) {
    if (opts && opts.mode === 'byob') throw new TypeError("Failed to execute 'getReader' on 'ReadableStream': BYOB readers are not supported");
    return new Reader(this);
  };
  ReadableStream.prototype.cancel = function (r) { if (RS.get(this).reader) return Promise.reject(new TypeError("Failed to execute 'cancel' on 'ReadableStream': Cannot cancel a locked stream")); return rsCancel(this, r); };
  ReadableStream.prototype.pipeTo = function (dest, opts) {
    var reader = this.getReader(), writer = dest.getWriter(), o = opts || {};
    return new Promise(function (res, rej) {
      function fail(e) { reader.releaseLock(); if (!o.preventAbort) writer.abort(e).catch(noop); writer.releaseLock(); rej(e); }
      function step() {
        writer.ready.then(function () {
          return reader.read();
        }).then(function (r) {
          if (r.done) {
            reader.releaseLock();
            if (o.preventClose) { writer.releaseLock(); return res(); }
            writer.close().then(function () { writer.releaseLock(); res(); }, rej);
            return;
          }
          writer.write(r.value).catch(noop);
          step();
        }).catch(fail);
      }
      step();
    });
  };
  ReadableStream.prototype.pipeThrough = function (pair, opts) { this.pipeTo(pair.writable, opts).catch(noop); return pair.readable; };
  ReadableStream.prototype.tee = function () {
    var reader = this.getReader(), c1, c2, done = false;
    function mk(which) {
      return new ReadableStream({
        start: function (c) { if (which === 1) c1 = c; else c2 = c; },
        pull: function () {
          if (done) return;
          return reader.read().then(function (r) {
            if (r.done) { done = true; try { c1.close(); } catch (e) { /* closed */ } try { c2.close(); } catch (e) { /* closed */ } return; }
            try { c1.enqueue(r.value); } catch (e) { /* cancelled */ } try { c2.enqueue(r.value); } catch (e) { /* cancelled */ }
          });
        },
        cancel: function (r) { return reader.cancel(r); }
      });
    }
    return [mk(1), mk(2)];
  };
  ReadableStream.prototype.values = function () {
    var reader = this.getReader();
    return {
      next: function () { return reader.read(); },
      return: function (v) { reader.releaseLock(); return Promise.resolve({ value: v, done: true }); },
      [Symbol.asyncIterator]: function () { return this; }
    };
  };
  ReadableStream.prototype[Symbol.asyncIterator] = ReadableStream.prototype.values;
  ReadableStream.from = function (it) {
    var iter = it && it[Symbol.asyncIterator] ? it[Symbol.asyncIterator]() : it[Symbol.iterator]();
    return new ReadableStream({
      pull: function (c) { return Promise.resolve(iter.next()).then(function (r) { if (r.done) c.close(); else c.enqueue(r.value); }); },
      cancel: function (r) { return iter.return ? iter.return(r) : undefined; }
    });
  };
  Object.defineProperty(ReadableStream.prototype, Symbol.toStringTag, { value: 'ReadableStream', configurable: true });

  // ---------------------------------------------------------------- WritableStream
  var WS = new WeakMap();
  function WritableStream(sink, strategy) {
    if (!(this instanceof WritableStream)) throw new TypeError("Failed to construct 'WritableStream': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    sink = sink || {};
    var st = { sink: sink, state: 'writable', err: undefined, chain: null, writer: null, inflight: 0, strat: strategyOf(strategy, 1), qsize: 0 };
    WS.set(this, st);
    var ctrl = { signal: undefined, error: function (e) { wsError(st, e); } };
    st.ctrl = ctrl;
    st.chain = new Promise(function (res) { res(sink.start ? sink.start(ctrl) : undefined); }).catch(function (e) { wsError(st, e); });
  }
  function wsError(st, e) {
    if (st.state === 'errored') return;
    st.state = 'errored'; st.err = e;
    if (st.writer) { st.writer.closedD.promise.catch(noop); st.writer.closedD.reject(e); }
  }
  function Writer(stream) {
    var st = WS.get(stream);
    if (st.writer) throw new TypeError("Failed to execute 'getWriter' on 'WritableStream': Cannot create writer when WritableStream is locked");
    this._s = stream; st.writer = this;
    this.closedD = defer(); this.closedD.promise.catch(noop);
    if (st.state === 'errored') this.closedD.reject(st.err);
    Object.defineProperty(this, 'closed', { get: function () { return this.closedD.promise; } });
  }
  Object.defineProperty(Writer.prototype, 'ready', { get: function () { var st = WS.get(this._s); if (st.state === 'errored') return Promise.reject(st.err); return st.qsize >= st.strat.hwm && st.strat.hwm > 0 ? st.chain.then(noop) : Promise.resolve(); } });
  Object.defineProperty(Writer.prototype, 'desiredSize', { get: function () { var st = WS.get(this._s); return st.state === 'errored' ? null : st.strat.hwm - st.qsize; } });
  Writer.prototype.write = function (chunk) {
    var st = WS.get(this._s);
    if (st.state === 'errored') return Promise.reject(st.err);
    if (st.state !== 'writable') return Promise.reject(new TypeError("Failed to execute 'write' on 'WritableStreamDefaultWriter': Cannot write to a closing writable stream"));
    var sz = 1; try { sz = +st.strat.size(chunk); } catch (e) { return Promise.reject(e); }
    st.qsize += sz;
    var p = st.chain.then(function () {
      if (st.state === 'errored') throw st.err;
      return st.sink.write ? st.sink.write(chunk, st.ctrl) : undefined;
    }).then(function () { st.qsize -= sz; }, function (e) { st.qsize -= sz; wsError(st, e); throw e; });
    st.chain = p.catch(noop);
    return p;
  };
  Writer.prototype.close = function () {
    var st = WS.get(this._s), w = this;
    if (st.state === 'errored') return Promise.reject(st.err);
    if (st.state !== 'writable') return Promise.reject(new TypeError("Failed to execute 'close' on 'WritableStreamDefaultWriter': Cannot close a closing writable stream"));
    st.state = 'closing';
    var p = st.chain.then(function () { if (st.state === 'errored') throw st.err; return st.sink.close ? st.sink.close() : undefined; }).then(function () { st.state = 'closed'; w.closedD.resolve(); }, function (e) { wsError(st, e); throw e; });
    st.chain = p.catch(noop);
    return p;
  };
  Writer.prototype.abort = function (reason) {
    var st = WS.get(this._s);
    if (st.state === 'closed' || st.state === 'errored') return Promise.resolve();
    wsError(st, reason);
    try { return Promise.resolve(st.sink.abort ? st.sink.abort(reason) : undefined).then(noop); } catch (e) { return Promise.reject(e); }
  };
  Writer.prototype.releaseLock = function () { var st = WS.get(this._s); if (st.writer === this) st.writer = null; };
  Object.defineProperty(WritableStream.prototype, 'locked', { get: function () { return !!WS.get(this).writer; }, configurable: true, enumerable: true });
  WritableStream.prototype.getWriter = function () { return new Writer(this); };
  WritableStream.prototype.abort = function (r) { if (WS.get(this).writer) return Promise.reject(new TypeError("Failed to execute 'abort' on 'WritableStream': Cannot abort a locked stream")); return Writer.prototype.abort.call({ _s: this }, r); };
  WritableStream.prototype.close = function () { if (WS.get(this).writer) return Promise.reject(new TypeError("Failed to execute 'close' on 'WritableStream': Cannot close a locked stream")); return Writer.prototype.close.call({ _s: this, closedD: defer() }); };
  Object.defineProperty(WritableStream.prototype, Symbol.toStringTag, { value: 'WritableStream', configurable: true });

  // ---------------------------------------------------------------- TransformStream
  function TransformStream(transformer, writableStrategy, readableStrategy) {
    if (!(this instanceof TransformStream)) throw new TypeError("Failed to construct 'TransformStream': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    transformer = transformer || {};
    var rc, started = defer();
    var tc = {
      get desiredSize() { return rc.desiredSize; },
      enqueue: function (c) { rc.enqueue(c); },
      error: function (e) { rc.error(e); },
      terminate: function () { try { rc.close(); } catch (e) { /* already closed */ } }
    };
    var readable = new ReadableStream({ start: function (c) { rc = c; }, pull: function () { }, cancel: function () { } }, readableStrategy || { highWaterMark: 0 });
    var writable = new WritableStream({
      start: function () { return started.promise; },
      write: function (chunk) { return new Promise(function (res) { res(transformer.transform ? transformer.transform(chunk, tc) : tc.enqueue(chunk)); }).catch(function (e) { rc.error(e); throw e; }); },
      close: function () { return new Promise(function (res) { res(transformer.flush ? transformer.flush(tc) : undefined); }).then(function () { try { rc.close(); } catch (e) { /* terminated */ } }, function (e) { rc.error(e); throw e; }); },
      abort: function (r) { rc.error(r); }
    }, writableStrategy);
    new Promise(function (res) { res(transformer.start ? transformer.start(tc) : undefined); }).then(started.resolve, function (e) { started.resolve(); rc.error(e); });
    Object.defineProperty(this, 'readable', { value: readable, enumerable: true });
    Object.defineProperty(this, 'writable', { value: writable, enumerable: true });
  }
  Object.defineProperty(TransformStream.prototype, Symbol.toStringTag, { value: 'TransformStream', configurable: true });

  global.ReadableStream = ReadableStream; global.WritableStream = WritableStream; global.TransformStream = TransformStream;
  global.ReadableStreamDefaultReader = Reader; global.WritableStreamDefaultWriter = Writer;
})(globalThis);
