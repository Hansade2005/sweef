/*
 * ServiceWorkerGlobalScope, evaluated once in a service worker's VM before its
 * script runs. The VM is the page VM (the hidden tab of sw_runtime.go), so
 * fetch, Request/Response, caches, indexedDB, timers and console are the
 * document ones; this file turns the global into a worker scope:
 *
 *   - removes what a worker does not have (window, document, localStorage ...)
 *   - self.registration, self.clients, self.skipWaiting(), importScripts()
 *   - install/activate/fetch/message events with waitUntil and respondWith
 *   - __swSwDispatch, which Go calls to fire an event and which reports back
 *     through __sw.swEventDone when the event's promises have settled
 */
(function (global, H, info) {
  'use strict';
  var ET = global.EventTarget;

  function mkErr(name, msg) { return name === 'TypeError' ? new TypeError(msg) : new global.DOMException(msg || name, name); }
  function hostErr(e) {
    var m = e && typeof e.message === 'string' ? /^([A-Za-z]+Error): ([\s\S]*)$/.exec(e.message) : null;
    return m ? mkErr(m[1], m[2]) : e;
  }
  function host(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    try { return H[name].apply(null, args); } catch (e) { throw hostErr(e); }
  }
  function hide(o, props) { for (var k in props) Object.defineProperty(o, k, { value: props[k], writable: true, configurable: true, enumerable: false }); }
  function defAttrs(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { get: map[k], enumerable: true, configurable: true }); }); }
  function defMethods(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { value: map[k], writable: true, enumerable: true, configurable: true }); }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function exportClass(C, name) { Object.defineProperty(C, 'name', { value: name, configurable: true }); Object.defineProperty(global, name, { value: C, writable: true, configurable: true, enumerable: false }); }
  function report(e) { if (typeof global.reportError === 'function') { try { global.reportError(e); } catch (x) { /* ignore */ } } }

  // ---- a worker has no document ------------------------------------------------------------------
  ['document', 'localStorage', 'sessionStorage', 'history', 'screen', 'alert', 'confirm', 'prompt', 'open', 'opener', 'frames', 'parent', 'top', 'window',
    'frameElement', 'requestAnimationFrame', 'cancelAnimationFrame', 'matchMedia', 'getComputedStyle', 'innerWidth', 'innerHeight', 'outerWidth', 'outerHeight', 'scrollX', 'scrollY'].forEach(function (k) {
    try { delete global[k]; } catch (e) { /* not configurable */ }
  });
  try {
    var proto = Object.getPrototypeOf(global);
    var WGS = function WorkerGlobalScope() { throw new TypeError('Illegal constructor'); };
    WGS.prototype = Object.create(proto, { constructor: { value: WGS, writable: true, configurable: true } });
    var SWGS = function ServiceWorkerGlobalScope() { throw new TypeError('Illegal constructor'); };
    SWGS.prototype = Object.create(WGS.prototype, { constructor: { value: SWGS, writable: true, configurable: true } });
    Object.defineProperty(SWGS.prototype, Symbol.toStringTag, { value: 'ServiceWorkerGlobalScope', configurable: true });
    Object.setPrototypeOf(global, SWGS.prototype);
    exportClass(WGS, 'WorkerGlobalScope');
    exportClass(SWGS, 'ServiceWorkerGlobalScope');
  } catch (e) { /* keep the plain global */ }
  Object.defineProperty(global, 'self', { get: function () { return global; }, set: function () { }, configurable: true, enumerable: true });
  try { Object.defineProperty(global.navigator, 'serviceWorker', { value: undefined, configurable: true }); } catch (e) { /* ignore */ }

  // ---- events -------------------------------------------------------------------------------------------
  function ExtendableEvent(type, init) {
    global.Event.call(this, type, init);
    hide(this, { _pending: [], _live: false });
  }
  ExtendableEvent.prototype = Object.create(global.Event.prototype, { constructor: { value: ExtendableEvent, writable: true, configurable: true } });
  defMethods(ExtendableEvent.prototype, {
    waitUntil: function (p) {
      if (!this._live) throw mkErr('InvalidStateError', "Failed to execute 'waitUntil' on 'ExtendableEvent': The event handler is already finished and no pending promises exist.");
      this._pending.push(Promise.resolve(p));
    }
  });
  tag(ExtendableEvent, 'ExtendableEvent');
  exportClass(ExtendableEvent, 'ExtendableEvent');
  function sub(name, base, init) {
    var C = function (type, i) { base.call(this, type, i); if (init) init.call(this, i || {}); };
    C.prototype = Object.create(base.prototype, { constructor: { value: C, writable: true, configurable: true } });
    tag(C, name);
    exportClass(C, name);
    return C;
  }
  sub('InstallEvent', ExtendableEvent);
  var ExtendableMessageEvent = sub('ExtendableMessageEvent', ExtendableEvent, function (i) {
    this.data = i.data === undefined ? null : i.data; this.origin = i.origin || ''; this.lastEventId = i.lastEventId || '';
    this.source = i.source || null; this.ports = i.ports || [];
  });
  var FetchEvent = sub('FetchEvent', ExtendableEvent, function (i) {
    if (!i.request) throw new TypeError("Failed to construct 'FetchEvent': Required member is undefined.");
    this.request = i.request; this.clientId = i.clientId || ''; this.resultingClientId = i.resultingClientId || '';
    this.replacesClientId = i.replacesClientId || ''; this.handled = Promise.resolve(undefined);
    this.preloadResponse = Promise.resolve(undefined);
    hide(this, { _responded: false, _promise: null, _canRespond: false });
  });
  defMethods(FetchEvent.prototype, {
    respondWith: function (p) {
      if (!this._canRespond) throw mkErr('InvalidStateError', "Failed to execute 'respondWith' on 'FetchEvent': The event handler is already finished.");
      if (this._responded) throw mkErr('InvalidStateError', "Failed to execute 'respondWith' on 'FetchEvent': The event has already been responded to.");
      this._responded = true;
      this._promise = Promise.resolve(p);
      this.stopImmediatePropagation();
    }
  });

  // ---- clients ---------------------------------------------------------------------------------------
  function Client(tok, d) {
    if (tok !== hide) throw new TypeError('Illegal constructor');
    hide(this, { _id: d.id });
    Object.defineProperty(this, 'id', { value: d.id, enumerable: true });
    Object.defineProperty(this, 'url', { value: d.url, enumerable: true });
    Object.defineProperty(this, 'type', { value: d.type || 'window', enumerable: true });
    Object.defineProperty(this, 'frameType', { value: d.frameType || 'top-level', enumerable: true });
  }
  defMethods(Client.prototype, {
    postMessage: function (message, transfer) {
      var ports = global.__swPorts.bridge(transfer);
      host('swClientPostMessage', this._id, global.__swSer(message === undefined ? null : message), ports);
    }
  });
  tag(Client, 'Client');
  exportClass(Client, 'Client');
  function WindowClient(tok, d) {
    Client.call(this, tok, d);
    Object.defineProperty(this, 'visibilityState', { value: d.visibilityState || 'visible', enumerable: true });
    Object.defineProperty(this, 'focused', { value: !!d.focused, enumerable: true });
  }
  WindowClient.prototype = Object.create(Client.prototype, { constructor: { value: WindowClient, writable: true, configurable: true } });
  defMethods(WindowClient.prototype, {
    focus: function () { return Promise.resolve(this); },
    navigate: function () { return Promise.reject(mkErr('NotAllowedError', 'Client.navigate() is not supported.')); }
  });
  tag(WindowClient, 'WindowClient');
  exportClass(WindowClient, 'WindowClient');
  var clientObjs = new Map();
  function clientFor(d) {
    var c = clientObjs.get(d.id);
    if (!c) { c = new WindowClient(hide, d); clientObjs.set(d.id, c); }
    return c;
  }

  function Clients() { throw new TypeError('Illegal constructor'); }
  defMethods(Clients.prototype, {
    get: function (id) {
      return new Promise(function (resolve) { var d = host('swClientsGet', String(id)); resolve(d ? clientFor(d) : undefined); });
    },
    matchAll: function (options) {
      return new Promise(function (resolve) {
        var o = options || {}, type = o.type || 'window';
        if (type !== 'window' && type !== 'all') { resolve([]); return; }
        resolve(host('swClientsMatchAll', !!o.includeUncontrolled).map(clientFor));
      });
    },
    openWindow: function () { return Promise.reject(mkErr('NotAllowedError', 'Clients.openWindow() is not supported.')); },
    claim: function () { return new Promise(function (resolve) { host('swClientsClaim'); resolve(); }); }
  });
  tag(Clients, 'Clients');
  exportClass(Clients, 'Clients');
  Object.defineProperty(global, 'clients', { value: Object.create(Clients.prototype), enumerable: true, configurable: true });

  // ---- registration and script loading ------------------------------------------------------------------
  Object.defineProperty(global, 'registration', { get: function () { return global.__swReg.objFor(host('swOwnRegistration')); }, enumerable: true, configurable: true });
  Object.defineProperty(global, 'serviceWorker', { get: function () { return global.__swReg.workerFor(host('swOwnWorker')); }, enumerable: true, configurable: true });
  Object.defineProperty(global, 'skipWaiting', { value: function skipWaiting() { return new Promise(function (resolve) { host('swSkipWaiting'); resolve(); }); }, writable: true, configurable: true, enumerable: true });
  Object.defineProperty(global, 'importScripts', { value: function importScripts() {
    for (var i = 0; i < arguments.length; i++) {
      var abs = new global.URL(String(arguments[i]), info.scriptURL).href;
      var src = host('swImportScript', abs);
      (0, eval)(src + '\n//# sourceURL=' + abs);
    }
  }, writable: true, configurable: true, enumerable: true });
  // handler attributes
  ['install', 'activate', 'fetch', 'message', 'messageerror', 'notificationclick', 'notificationclose', 'push', 'pushsubscriptionchange', 'sync', 'periodicsync', 'contentdelete'].forEach(function (t) {
    var key = '__on' + t;
    Object.defineProperty(global, 'on' + t, {
      configurable: true, enumerable: true,
      get: function () { return this[key] || null; },
      set: function (fn) {
        if (this[key]) global.removeEventListener(t, this[key]);
        this[key] = typeof fn === 'function' ? fn : null;
        if (this[key]) global.addEventListener(t, this[key]);
      }
    });
  });

  // ---- event dispatch from Go ------------------------------------------------------------------------------
  function done(id, payload, body) { try { H.swEventDone(id, JSON.stringify(payload), body || ''); } catch (e) { /* the worker is going away */ } }
  function fail(id, e) { done(id, { ok: false, error: String(e && (e.stack || e.message) || e) }); }

  // waits for the promises given to waitUntil, including ones added while waiting
  function settle(ev, id) {
    ev._live = true;
    var round = function () {
      var ps = ev._pending; ev._pending = [];
      if (!ps.length) { ev._live = false; done(id, { ok: true }); return; }
      Promise.all(ps).then(round, function (e) { ev._live = false; fail(id, e); });
    };
    // one more turn of the microtask queue, so waitUntil calls made by promise callbacks of the handlers count
    Promise.resolve().then(round);
  }
  function bodyOfResponse(r) {
    if (!(r instanceof global.Response)) throw new TypeError('The FetchEvent resulted in a network error response: the promise was resolved with something other than a Response.');
    if (r.type === 'error' || r.status === 0) throw new TypeError('The FetchEvent resulted in a network error response.');
    if (r._used) throw new TypeError('The response body has already been used.');
    var bin = global.__swBodyBin(r), pairs = [];
    r.headers.forEach(function (v, k) { pairs.push([k, v]); });
    return [{ ok: true, handled: true, status: r.status, statusText: r.statusText, headers: pairs, url: r.url, type: r.type }, bin];
  }
  function makeRequest(p) {
    var init = { method: p.method, headers: p.headers, mode: p.mode, credentials: p.credentials, redirect: 'follow', __destination: p.destination };
    if (p.body) init.body = p.body;
    return new global.Request(p.url, init);
  }
  Object.defineProperty(global, '__swSwDispatch', { value: function (kind, id, payload) {
    var p = payload ? JSON.parse(payload) : {}, ev;
    try {
      switch (kind) {
        case 'install': ev = new global.InstallEvent('install'); break;
        case 'activate': ev = new ExtendableEvent('activate'); break;
        case 'fetch':
          ev = new FetchEvent('fetch', { request: makeRequest(p), clientId: p.clientId, resultingClientId: p.resultingClientId, cancelable: true });
          ev._canRespond = true;
          break;
        case 'message': {
          var src = p.source ? clientFor(p.source) : null;
          ev = new ExtendableMessageEvent('message', { data: global.__swDe(p.data), origin: p.origin, source: src, ports: (p.ports || []).map(global.__swPorts.adopt) });
          break;
        }
        default: done(id, { ok: true }); return 'ok';
      }
    } catch (e) { fail(id, e); return 'error'; }
    ev._live = true;
    try { global.dispatchEvent(ev); } catch (e) { report(e); }
    if (kind === 'fetch') {
      ev._canRespond = false;
      if (!ev._responded) { ev._live = false; done(id, { ok: true, handled: false }); return 'unhandled'; }
      ev._promise.then(function (r) { var o = bodyOfResponse(r); done(id, o[0], o[1]); }, function (e) { fail(id, e); }).catch(function (e) { fail(id, e); });
      // waitUntil on a fetch event extends the worker, nothing waits for it here
      ev._live = true;
      return 'responding';
    }
    settle(ev, id);
    return 'pending';
  } });
})(globalThis, globalThis.__sw, __SW_INFO__);
