/*
 * Worker, SharedWorker, BroadcastChannel and navigator.locks.
 *
 * Evaluated in every VM that has the page prelude: documents, the hidden VMs of workers (so a worker can start
 * workers, open channels and take locks) and nested browsing contexts. Go owns the worker VMs, the channel groups
 * and the lock queues (workers.go, webcoord.go); events come back through __swWkEvent, which Go calls from the VM's
 * own event loop.
 *
 * Messages are structured clones: __swSer / __swDe (idb_prelude.js) produce and read one string; the MessagePorts
 * of a transfer list travel as endpoints (sw_page_prelude.js bridges them), ArrayBuffers of a transfer list are
 * detached in the sender after the copy was taken.
 */
(function (global, H) {
  'use strict';
  if (!H || typeof H.wkCreate !== 'function' || !global.EventTarget) return;
  var ET = global.EventTarget;

  function mkErr(name, msg) {
    if (name === 'TypeError') return new TypeError(msg);
    return new global.DOMException(msg || name, name);
  }
  function hostErr(e) {
    var m = e && typeof e.message === 'string' ? /^([A-Za-z]+Error): ([\s\S]*)$/.exec(e.message) : null;
    return m ? mkErr(m[1], m[2]) : e;
  }
  function host(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    try { return H[name].apply(null, args); } catch (e) { throw hostErr(e); }
  }
  function hide(o, props) { for (var k in props) Object.defineProperty(o, k, { value: props[k], writable: true, configurable: true, enumerable: false }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function exportClass(C, name) { Object.defineProperty(C, 'name', { value: name, configurable: true }); Object.defineProperty(global, name, { value: C, writable: true, configurable: true, enumerable: false }); }
  function handlers(proto, types) {
    types.forEach(function (t) {
      var key = '__on' + t;
      Object.defineProperty(proto, 'on' + t, {
        configurable: true, enumerable: true,
        get: function () { return this[key] || null; },
        set: function (fn) {
          if (this[key]) this.removeEventListener(t, this[key]);
          this[key] = typeof fn === 'function' ? fn : null;
          if (this[key]) this.addEventListener(t, this[key]);
        }
      });
    });
  }
  function report(e) { if (typeof global.reportError === 'function') { try { global.reportError(e); } catch (x) { /* ignore */ } } }
  function baseURL() { try { return (global.document && global.document.baseURI) || global.location.href; } catch (e) { return 'about:blank'; } }
  function absURL(u) { try { return new global.URL(String(u), baseURL()).href; } catch (e) { throw mkErr('SyntaxError', "Failed to parse URL from " + u); } }
  function fire(target, type, init) {
    var ev = new global.Event(type, init || {});
    try { target.dispatchEvent(ev); } catch (e) { report(e); }
    return ev;
  }

  // ---- transfer lists --------------------------------------------------------------------------------
  function transferList(t) {
    if (t === undefined || t === null) return [];
    if (Array.isArray(t)) return t;
    if (typeof t === 'object' && t.transfer !== undefined) return Array.prototype.slice.call(t.transfer);
    if (typeof t === 'object' && typeof t.length === 'number') return Array.prototype.slice.call(t);
    throw new TypeError("Failed to execute 'postMessage': The provided value cannot be converted to a sequence.");
  }
  // serialize first (it can throw DataCloneError without side effects), then bridge the ports and detach the buffers
  function prepare(message, transfer) {
    var list = transferList(transfer), ports = [], bufs = [];
    list.forEach(function (x) {
      if (global.MessagePort && x instanceof global.MessagePort) {
        if (ports.indexOf(x) >= 0) throw mkErr('DataCloneError', 'Transfer list contains duplicate entries.');
        ports.push(x);
      } else if (x instanceof ArrayBuffer) {
        if (bufs.indexOf(x) >= 0) throw mkErr('DataCloneError', 'Transfer list contains duplicate entries.');
        if (x.detached) throw mkErr('DataCloneError', 'An ArrayBuffer is detached and could not be cloned.');
        bufs.push(x);
      } else {
        throw mkErr('DataCloneError', 'Value not transferable.');
      }
    });
    var data = global.__swSer(message, ports);
    return {
      data: data,
      commit: function () {
        var eps = ports.length ? global.__swPorts.bridge(ports) : [];
        bufs.forEach(function (b) { try { b.transfer(); } catch (e) { /* not detachable here */ } });
        return eps;
      }
    };
  }
  global.__swWkPrepare = prepare;

  // structuredClone(value, { transfer }): the buffers of the transfer list are detached once the copy exists
  (function () {
    var orig = global.structuredClone;
    if (typeof orig !== 'function') return;
    Object.defineProperty(global, 'structuredClone', { value: function structuredClone(value, options) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'structuredClone' on 'Window': 1 argument required, but only 0 present.");
      if (options === undefined || options === null || options.transfer === undefined) return orig(value);
      var list = transferList(options), bufs = [];
      list.forEach(function (x) {
        if (x instanceof ArrayBuffer) {
          if (bufs.indexOf(x) >= 0) throw mkErr('DataCloneError', 'Transfer list contains duplicate entries.');
          if (x.detached) throw mkErr('DataCloneError', 'An ArrayBuffer is detached and could not be cloned.');
          bufs.push(x);
        } else throw mkErr('DataCloneError', 'Value not transferable.');
      });
      var out = orig(value);
      bufs.forEach(function (b) { try { b.transfer(); } catch (e) { /* not detachable */ } });
      return out;
    }, writable: true, configurable: true, enumerable: true });
  })();

  // ---- Worker / SharedWorker ---------------------------------------------------------------------------
  var workers = new Map(); // id -> [objects]
  function track(id, o) { var l = workers.get(id); if (!l) workers.set(id, l = []); l.push(o); }

  function scriptSource(u) {
    // returns [absolute URL, source for blob/data URLs or '']
    if (/^blob:/i.test(u)) {
      var text = typeof global.__swBlobText === 'function' ? global.__swBlobText(u) : null;
      if (text === null || text === undefined) throw mkErr('SecurityError', "Failed to construct 'Worker': Script at '" + u + "' cannot be accessed from origin '" + global.location.origin + "'.");
      return [u, text];
    }
    if (/^data:/i.test(u)) {
      var comma = u.indexOf(','), meta = u.slice(5, comma), body = u.slice(comma + 1);
      return [u, /;base64$/i.test(meta) ? global.atob(body) : decodeURIComponent(body)];
    }
    return [absURL(u), ''];
  }

  function Worker(url, opts) {
    if (!(this instanceof Worker)) throw new TypeError("Failed to construct 'Worker': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 1) throw new TypeError("Failed to construct 'Worker': 1 argument required, but only 0 present.");
    ET.call(this);
    opts = opts || {};
    var s = scriptSource(String(url));
    var id = host('wkCreate', s[0], s[1], opts.type === 'module' ? 'module' : 'classic', opts.name === undefined ? '' : String(opts.name));
    hide(this, { _id: id });
    track(id, this);
  }
  Worker.prototype = Object.create(ET.prototype, { constructor: { value: Worker, writable: true, configurable: true } });
  Object.defineProperty(Worker.prototype, 'postMessage', { value: function postMessage(message, transfer) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'Worker': 1 argument required, but only 0 present.");
    var p = prepare(message, transfer);
    var eps = p.commit();
    host('wkPost', this._id, p.data, eps);
  }, writable: true, configurable: true, enumerable: true });
  Object.defineProperty(Worker.prototype, 'terminate', { value: function terminate() {
    host('wkTerminate', this._id);
    workers.delete(this._id);
  }, writable: true, configurable: true, enumerable: true });
  handlers(Worker.prototype, ['message', 'messageerror', 'error']);
  tag(Worker, 'Worker');
  exportClass(Worker, 'Worker');

  function SharedWorker(url, opts) {
    if (!(this instanceof SharedWorker)) throw new TypeError("Failed to construct 'SharedWorker': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 1) throw new TypeError("Failed to construct 'SharedWorker': 1 argument required, but only 0 present.");
    ET.call(this);
    var name = '', type = 'classic';
    if (typeof opts === 'string') name = opts;
    else if (opts && typeof opts === 'object') { name = opts.name === undefined ? '' : String(opts.name); type = opts.type === 'module' ? 'module' : 'classic'; }
    var s = scriptSource(String(url));
    var ch = new global.MessageChannel();
    var eps = global.__swPorts.bridge([ch.port2]);
    var id = host('wkConnect', s[0], s[1], type, name, eps[0]);
    hide(this, { _id: id });
    Object.defineProperty(this, 'port', { value: ch.port1, enumerable: true, configurable: true });
    track(id, this);
  }
  SharedWorker.prototype = Object.create(ET.prototype, { constructor: { value: SharedWorker, writable: true, configurable: true } });
  handlers(SharedWorker.prototype, ['error']);
  tag(SharedWorker, 'SharedWorker');
  exportClass(SharedWorker, 'SharedWorker');

  // ---- BroadcastChannel -----------------------------------------------------------------------------------
  var channels = new Map();
  function BroadcastChannel(name) {
    if (!(this instanceof BroadcastChannel)) throw new TypeError("Failed to construct 'BroadcastChannel': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 1) throw new TypeError("Failed to construct 'BroadcastChannel': 1 argument required, but only 0 present.");
    ET.call(this);
    var nm = String(name);
    var id = host('bcOpen', nm);
    hide(this, { _id: id, _name: nm, _closed: false });
    channels.set(id, this);
  }
  BroadcastChannel.prototype = Object.create(ET.prototype, { constructor: { value: BroadcastChannel, writable: true, configurable: true } });
  Object.defineProperty(BroadcastChannel.prototype, 'name', { get: function () { return this._name; }, enumerable: true, configurable: true });
  Object.defineProperty(BroadcastChannel.prototype, 'postMessage', { value: function postMessage(message) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'BroadcastChannel': 1 argument required, but only 0 present.");
    if (this._closed) throw mkErr('InvalidStateError', "Failed to execute 'postMessage' on 'BroadcastChannel': Channel is closed");
    host('bcPost', this._id, global.__swSer(message, []));
  }, writable: true, configurable: true, enumerable: true });
  Object.defineProperty(BroadcastChannel.prototype, 'close', { value: function close() {
    if (this._closed) return;
    this._closed = true;
    channels.delete(this._id);
    host('bcClose', this._id);
  }, writable: true, configurable: true, enumerable: true });
  handlers(BroadcastChannel.prototype, ['message', 'messageerror']);
  tag(BroadcastChannel, 'BroadcastChannel');
  exportClass(BroadcastChannel, 'BroadcastChannel');

  // ---- Web Locks ------------------------------------------------------------------------------------------
  var lockReqs = new Map();
  function Lock(tok, name, mode) {
    if (tok !== lockReqs) throw new TypeError('Illegal constructor');
    Object.defineProperty(this, 'name', { value: name, enumerable: true });
    Object.defineProperty(this, 'mode', { value: mode, enumerable: true });
  }
  tag(Lock, 'Lock');
  exportClass(Lock, 'Lock');
  function LockManager() { throw new TypeError('Illegal constructor'); }
  function abortError(signal) {
    if (signal && signal.reason !== undefined) return signal.reason;
    return mkErr('AbortError', "The request was aborted.");
  }
  Object.defineProperty(LockManager.prototype, 'request', { value: function request(name, a, b) {
    var options = {}, cb;
    if (arguments.length < 2) return Promise.reject(new TypeError("Failed to execute 'request' on 'LockManager': 2 arguments required, but only " + arguments.length + " present."));
    if (arguments.length === 2) cb = a; else { options = a === undefined || a === null ? {} : a; cb = b; }
    if (typeof cb !== 'function') return Promise.reject(new TypeError("Failed to execute 'request' on 'LockManager': The callback provided as parameter 2 is not a function."));
    name = String(name);
    var mode = options.mode === undefined ? 'exclusive' : String(options.mode);
    if (mode !== 'exclusive' && mode !== 'shared') return Promise.reject(new TypeError("Failed to execute 'request' on 'LockManager': The provided value '" + mode + "' is not a valid enum value of type LockMode."));
    var ifAvailable = !!options.ifAvailable, steal = !!options.steal, signal = options.signal || null;
    if (name.charAt(0) === '-') return Promise.reject(mkErr('NotSupportedError', "Failed to execute 'request' on 'LockManager': Names cannot start with '-'."));
    if (steal && ifAvailable) return Promise.reject(mkErr('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'steal' and 'ifAvailable' options cannot be used together."));
    if (steal && mode !== 'exclusive') return Promise.reject(mkErr('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'steal' option may only be used with 'exclusive' locks."));
    if (signal && (steal || ifAvailable)) return Promise.reject(mkErr('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'signal' option cannot be used with 'steal' or 'ifAvailable'."));
    if (signal && signal.aborted) return Promise.reject(abortError(signal));
    return new Promise(function (resolve, reject) {
      var id = host('lockRequest', name, mode, ifAvailable, steal);
      var rec = { name: name, mode: mode, cb: cb, resolve: resolve, reject: reject, held: false, done: false };
      lockReqs.set(id, rec);
      if (signal) {
        rec.onabort = function () {
          if (rec.held || rec.done) return;
          rec.done = true;
          lockReqs.delete(id);
          host('lockAbort', id);
          reject(abortError(signal));
        };
        signal.addEventListener('abort', rec.onabort);
        rec.signal = signal;
      }
    });
  }, writable: true, configurable: true, enumerable: true });
  Object.defineProperty(LockManager.prototype, 'query', { value: function query() {
    return new Promise(function (resolve) {
      var q = host('lockQuery');
      function conv(l) { return (l || []).map(function (x) { return { name: x.name, mode: x.mode, clientId: x.clientId }; }); }
      resolve({ held: conv(q.held), pending: conv(q.pending) });
    });
  }, writable: true, configurable: true, enumerable: true });
  tag(LockManager, 'LockManager');
  exportClass(LockManager, 'LockManager');
  var lockManager = Object.create(LockManager.prototype);
  function onLock(m) {
    var rec = lockReqs.get(m.req);
    if (!rec) return;
    if (rec.signal && rec.onabort) rec.signal.removeEventListener('abort', rec.onabort);
    if (m.st === 'granted') {
      rec.held = true;
      var ret;
      try { ret = Promise.resolve(rec.cb(new Lock(lockReqs, rec.name, rec.mode))); } catch (e) { ret = Promise.reject(e); }
      ret.then(function (v) { lockReqs.delete(m.req); host('lockRelease', m.req); if (!rec.stolen) rec.resolve(v); },
        function (e) { lockReqs.delete(m.req); host('lockRelease', m.req); if (!rec.stolen) rec.reject(e); });
    } else if (m.st === 'unavailable') {
      rec.done = true;
      lockReqs.delete(m.req);
      var r2;
      try { r2 = Promise.resolve(rec.cb(null)); } catch (e) { r2 = Promise.reject(e); }
      r2.then(rec.resolve, rec.reject);
    } else if (m.st === 'stolen') {
      rec.stolen = true;
      lockReqs.delete(m.req);
      rec.reject(mkErr('AbortError', 'Lock broken by another request with the \'steal\' option.'));
    }
  }
  try {
    // the VM is built before it is bound to a document, so whether this is a secure context is asked on use
    if (global.Navigator && global.Navigator.prototype) {
      Object.defineProperty(global.Navigator.prototype, 'locks', { get: function () { return (typeof H.secureContext === 'function' ? !!H.secureContext() : true) ? lockManager : undefined; }, enumerable: true, configurable: true });
    }
  } catch (e) { /* no navigator */ }

  // ---- events from Go --------------------------------------------------------------------------------------
  Object.defineProperty(global, '__swWkEvent', { value: function (json) {
    var m = JSON.parse(json);
    try {
      switch (m.t) {
        case 'msg': {
          var l = workers.get(m.id);
          if (!l) return false;
          var ports = (m.ports || []).map(global.__swPorts.adopt), data, bad = false;
          try { data = global.__swDe(m.data, ports); } catch (e) { bad = true; }
          l.slice().forEach(function (w) {
            w.dispatchEvent(bad ? new global.MessageEvent('messageerror', {}) : new global.MessageEvent('message', { data: data, origin: '', ports: ports, source: null }));
          });
          return true;
        }
        case 'err': {
          var ws = workers.get(m.id);
          if (!ws) return false;
          ws.slice().forEach(function (w) {
            var ev = m.net ? new global.Event('error', { cancelable: true }) : new global.ErrorEvent('error', { message: m.message || '', filename: m.filename || '', lineno: m.lineno | 0, colno: m.colno | 0, cancelable: true });
            if (m.net) ev.message = m.message;
            w.dispatchEvent(ev);
          });
          return true;
        }
        case 'bc': {
          var ch = channels.get(m.ch);
          if (!ch || ch._closed) return false;
          var d, badc = false;
          try { d = global.__swDe(m.data, []); } catch (e) { badc = true; }
          ch.dispatchEvent(badc ? new global.MessageEvent('messageerror', {}) : new global.MessageEvent('message', { data: d, origin: m.origin || '' }));
          return true;
        }
        case 'lock': onLock(m); return true;
      }
    } catch (e) { report(e); }
    return false;
  } });
})(globalThis, globalThis.__sw);
