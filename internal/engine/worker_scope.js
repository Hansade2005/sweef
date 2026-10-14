/*
 * DedicatedWorkerGlobalScope / SharedWorkerGlobalScope, evaluated once in a worker's VM before its script runs.
 * The VM is the page VM (the hidden tab of bgvm.go), so fetch, Request/Response, timers, caches, indexedDB, OPFS,
 * BroadcastChannel, navigator.locks, WebAssembly, structuredClone and nested Workers are the document ones; this
 * file takes away what a worker does not have and adds what only a worker has:
 *
 *   - no window, document, localStorage, history ... ; self, name, location (WorkerLocation), importScripts()
 *   - postMessage() to the owner (dedicated) or onconnect with MessagePorts (shared)
 *   - __swWkDeliver / __swWConnect, which Go calls on the worker's own goroutine
 *   - uncaught errors are forwarded to the Worker object in the owner unless an error handler prevented them
 */
(function (global, H, info) {
  'use strict';
  function mkErr(name, msg) { return name === 'TypeError' ? new TypeError(msg) : new global.DOMException(msg || name, name); }
  function hostErr(e) {
    var m = e && typeof e.message === 'string' ? /^([A-Za-z]+Error): ([\s\S]*)$/.exec(e.message) : null;
    return m ? mkErr(m[1], m[2]) : e;
  }
  function host(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    try { return H[name].apply(null, args); } catch (e) { throw hostErr(e); }
  }
  function exportClass(C, name) { Object.defineProperty(C, 'name', { value: name, configurable: true }); Object.defineProperty(global, name, { value: C, writable: true, configurable: true, enumerable: false }); }
  var shared = info.kind === 'shared';
  Object.defineProperty(global, '__swIsWorker', { value: true });

  // ---- a worker has no document ---------------------------------------------------------------------
  ['document', 'localStorage', 'sessionStorage', 'history', 'screen', 'alert', 'confirm', 'prompt', 'open', 'opener', 'frames', 'parent', 'top', 'window',
    'frameElement', 'requestAnimationFrame', 'cancelAnimationFrame', 'matchMedia', 'getComputedStyle', 'innerWidth', 'innerHeight', 'outerWidth', 'outerHeight',
    'scrollX', 'scrollY', 'pageXOffset', 'pageYOffset', 'devicePixelRatio', 'print', 'stop', 'focus', 'blur', 'scroll', 'scrollTo', 'scrollBy', 'moveTo', 'resizeTo',
    'visualViewport'].forEach(function (k) {
    try { delete global[k]; } catch (e) { /* not configurable */ }
  });
  try {
    var proto = Object.getPrototypeOf(global);
    var WGS = function WorkerGlobalScope() { throw new TypeError('Illegal constructor'); };
    WGS.prototype = Object.create(proto, { constructor: { value: WGS, writable: true, configurable: true } });
    var kindName = shared ? 'SharedWorkerGlobalScope' : 'DedicatedWorkerGlobalScope';
    var KGS = { SharedWorkerGlobalScope: function SharedWorkerGlobalScope() { throw new TypeError('Illegal constructor'); }, DedicatedWorkerGlobalScope: function DedicatedWorkerGlobalScope() { throw new TypeError('Illegal constructor'); } }[kindName];
    KGS.prototype = Object.create(WGS.prototype, { constructor: { value: KGS, writable: true, configurable: true } });
    Object.defineProperty(KGS.prototype, Symbol.toStringTag, { value: kindName, configurable: true });
    Object.setPrototypeOf(global, KGS.prototype);
    exportClass(WGS, 'WorkerGlobalScope');
    exportClass(KGS, kindName);
  } catch (e) { /* keep the plain global */ }
  Object.defineProperty(global, 'self', { get: function () { return global; }, set: function () { }, configurable: true, enumerable: true });
  Object.defineProperty(global, 'name', { value: info.name || '', writable: false, configurable: true, enumerable: true });

  // ---- location ----------------------------------------------------------------------------------------
  function WorkerLocation() { throw new TypeError('Illegal constructor'); }
  var wl = Object.create(WorkerLocation.prototype), U = null;
  try { U = new global.URL(info.scriptURL); } catch (e) { U = null; }
  ['href', 'origin', 'protocol', 'host', 'hostname', 'port', 'pathname', 'search', 'hash'].forEach(function (k) {
    Object.defineProperty(WorkerLocation.prototype, k, { get: function () { return U ? U[k] : ''; }, enumerable: true, configurable: true });
  });
  Object.defineProperty(WorkerLocation.prototype, 'toString', { value: function () { return U ? U.href : ''; }, writable: true, configurable: true });
  Object.defineProperty(WorkerLocation.prototype, Symbol.toStringTag, { value: 'WorkerLocation', configurable: true });
  exportClass(WorkerLocation, 'WorkerLocation');
  Object.defineProperty(global, 'location', { get: function () { return wl; }, set: function () { }, configurable: true, enumerable: true });

  // ---- messaging -----------------------------------------------------------------------------------------
  if (!shared) {
    Object.defineProperty(global, 'postMessage', { value: function postMessage(message, transfer) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'DedicatedWorkerGlobalScope': 1 argument required, but only 0 present.");
      var p = global.__swWkPrepare(message, transfer);
      var eps = p.commit();
      host('wkOut', p.data, eps);
    }, writable: true, configurable: true, enumerable: true });
  } else {
    try { delete global.postMessage; } catch (e) { /* ignore */ }
  }
  Object.defineProperty(global, 'close', { value: function close() { host('wkClose'); }, writable: true, configurable: true, enumerable: true });
  Object.defineProperty(global, 'importScripts', { value: function importScripts() {
    var base = /^https?:/i.test(info.scriptURL) ? info.scriptURL : info.baseURL;
    for (var i = 0; i < arguments.length; i++) {
      var abs = new global.URL(String(arguments[i]), base).href;
      var src = host('wkImport', abs);
      (0, eval)(src + '\n//# sourceURL=' + abs);
    }
  }, writable: true, configurable: true, enumerable: true });
  if (shared) {
    var key = '__onconnect';
    Object.defineProperty(global, 'onconnect', {
      configurable: true, enumerable: true,
      get: function () { return this[key] || null; },
      set: function (fn) {
        if (this[key]) global.removeEventListener('connect', this[key]);
        this[key] = typeof fn === 'function' ? fn : null;
        if (this[key]) global.addEventListener('connect', this[key]);
      }
    });
  }

  Object.defineProperty(global, '__swWkDeliver', { value: function (data, eps) {
    var ports = (eps || []).map(global.__swPorts.adopt), v;
    try { v = global.__swDe(data, ports); } catch (e) { global.dispatchEvent(new global.MessageEvent('messageerror', {})); return; }
    // an RTCRtpScriptTransform of the owner was attached to a sender or receiver: not a message, an rtctransform event
    if (v && typeof v === 'object' && v.__swRtcTransform === 1 && ports.length && typeof global.__swMakeTransformEvent === 'function') {
      global.dispatchEvent(global.__swMakeTransformEvent(ports[0], v));
      return;
    }
    global.dispatchEvent(new global.MessageEvent('message', { data: v, origin: '', ports: ports, source: null }));
  } });
  (function () {
    var key = '__onrtctransform';
    Object.defineProperty(global, 'onrtctransform', {
      configurable: true, enumerable: true,
      get: function () { return this[key] || null; },
      set: function (fn) {
        if (this[key]) global.removeEventListener('rtctransform', this[key]);
        this[key] = typeof fn === 'function' ? fn : null;
        if (this[key]) global.addEventListener('rtctransform', this[key]);
      }
    });
  })();
  Object.defineProperty(global, '__swWConnect', { value: function (ep) {
    var port = global.__swPorts.adopt(ep);
    var ev = new global.MessageEvent('connect', { data: '', origin: '', ports: [port], source: port });
    global.dispatchEvent(ev);
  } });
  Object.defineProperty(global, '__swUncaught', { value: function (ev) {
    try { H.wkError(String(ev.message || ''), ev.filename || info.scriptURL, ev.lineno | 0, ev.colno | 0); } catch (e) { /* the owner is gone */ }
  }, configurable: true });
})(globalThis, globalThis.__sw, __WK_INFO__);
