/*
 * Service worker API of a document: navigator.serviceWorker (ServiceWorkerContainer),
 * ServiceWorkerRegistration and ServiceWorker, plus the message port bridge that
 * lets a MessageChannel port travel to a worker.
 *
 * Go owns the registrations and the worker lifecycle (sw_registry.go). This file
 * mirrors their state into objects: Go sends registration snapshots, and the
 * objects here compare them with what they last saw, firing statechange,
 * updatefound and controllerchange as the state moves. The same file runs inside
 * a service worker's VM, where `self.registration` is built from it.
 */
(function (global, H) {
  'use strict';
  if (!H || typeof H.swRegister !== 'function' || !global.EventTarget) return;
  var ET = global.EventTarget, TOKEN = {};

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
  function defAttrs(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { get: map[k], enumerable: true, configurable: true }); }); }
  function defMethods(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { value: map[k], writable: true, enumerable: true, configurable: true }); }); }
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
  function absURL(u, base) { return new global.URL(String(u), base || global.location.href).href; }
  function fire(target, type, init) {
    var ev = new global.Event(type, init || {});
    try { target.dispatchEvent(ev); } catch (e) { report(e); }
    return ev;
  }

  // ---- MessagePort bridge ------------------------------------------------------------------------
  var portsById = new Map();
  (function () {
    var MP = global.MessagePort;
    if (!MP) return;
    var orig = MP.prototype.postMessage;
    Object.defineProperty(MP.prototype, 'postMessage', { value: function (data) {
      if (this._ep) { H.swPortPost(this._ep, global.__swSer(data === undefined ? null : data)); return; }
      return orig.apply(this, arguments);
    }, writable: true, configurable: true });
  })();
  // a local port is going to another VM: its entangled partner stays here and talks through Go
  function bridgePort(p) {
    var other = p._other;
    if (!other) throw mkErr('DataCloneError', 'The port is closed and cannot be transferred.');
    var pair = host('swPortPair');
    other._ep = pair[0];
    portsById.set(pair[0], other);
    p._other = null;
    return pair[1];
  }
  function adoptPort(ep) {
    var ch = new global.MessageChannel(), port = ch.port1;
    port._ep = ep;
    portsById.set(ep, port);
    port._other = null;
    host('swPortAdopt', ep);
    return port;
  }
  function transferList(t) {
    if (!t) return [];
    if (Array.isArray(t)) return t;
    if (typeof t === 'object' && Array.isArray(t.transfer)) return t.transfer;
    return [];
  }
  function portIdsOf(transfer) {
    var ids = [];
    transferList(transfer).forEach(function (p) {
      if (global.MessagePort && p instanceof global.MessagePort) ids.push(bridgePort(p));
    });
    return ids;
  }
  Object.defineProperty(global, '__swPorts', { value: { bridge: portIdsOf, adopt: adoptPort } });

  // ---- ServiceWorker ---------------------------------------------------------------------------------
  var workers = new Map(), regs = new Map(), pend = new Map(), seq = 0;

  function ServiceWorker(tok, info) {
    if (tok !== TOKEN) throw new TypeError('Illegal constructor');
    ET.call(this);
    hide(this, { _id: info.id, _url: info.scriptURL, _state: info.state });
  }
  ServiceWorker.prototype = Object.create(ET.prototype, { constructor: { value: ServiceWorker, writable: true, configurable: true } });
  defAttrs(ServiceWorker.prototype, {
    scriptURL: function () { return this._url; },
    state: function () { return this._state; }
  });
  handlers(ServiceWorker.prototype, ['statechange', 'error']);
  defMethods(ServiceWorker.prototype, {
    postMessage: function (message, transfer) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'ServiceWorker': 1 argument required, but only 0 present.");
      if (this._state === 'redundant') throw mkErr('InvalidStateError', "Failed to execute 'postMessage' on 'ServiceWorker': ServiceWorker is in redundant state.");
      var data = global.__swSer(message === undefined ? null : message);
      host('swPostMessage', this._id, data, portIdsOf(transfer));
    }
  });
  tag(ServiceWorker, 'ServiceWorker');
  exportClass(ServiceWorker, 'ServiceWorker');

  function workerFor(info) {
    if (!info) return null;
    var w = workers.get(info.id);
    if (!w) { w = new ServiceWorker(TOKEN, info); workers.set(info.id, w); return w; }
    if (w._state !== info.state) { w._state = info.state; fire(w, 'statechange'); }
    return w;
  }

  // ---- ServiceWorkerRegistration -------------------------------------------------------------------------
  function ServiceWorkerRegistration(tok, snap) {
    if (tok !== TOKEN) throw new TypeError('Illegal constructor');
    ET.call(this);
    hide(this, { _id: snap.id, _scope: snap.scope, _uvc: snap.updateViaCache, _installing: null, _waiting: null, _active: null, _gone: false, _rev: -1 });
  }
  ServiceWorkerRegistration.prototype = Object.create(ET.prototype, { constructor: { value: ServiceWorkerRegistration, writable: true, configurable: true } });
  defAttrs(ServiceWorkerRegistration.prototype, {
    installing: function () { return this._installing; },
    waiting: function () { return this._waiting; },
    active: function () { return this._active; },
    scope: function () { return this._scope; },
    updateViaCache: function () { return this._uvc; },
    navigationPreload: function () {
      return { enable: function () { return Promise.resolve(); }, disable: function () { return Promise.resolve(); },
        setHeaderValue: function () { return Promise.resolve(); }, getState: function () { return Promise.resolve({ enabled: false, headerValue: 'true' }); } };
    },
    pushManager: function () { return global.__pwa.push(this); },
    sync: function () { return global.__pwa.sync(this); },
    periodicSync: function () { return global.__pwa.periodicSync(this); }
  });
  handlers(ServiceWorkerRegistration.prototype, ['updatefound']);
  function call(kind, args) {
    return new Promise(function (resolve, reject) {
      var token = ++seq;
      pend.set(token, { resolve: resolve, reject: reject });
      try { H[kind].apply(null, [token].concat(args)); } catch (e) { pend.delete(token); reject(hostErr(e)); }
    });
  }
  defMethods(ServiceWorkerRegistration.prototype, {
    update: function () {
      var self = this;
      if (this._gone) return Promise.reject(mkErr('InvalidStateError', "Failed to update a ServiceWorkerRegistration: The registration is unregistered."));
      return call('swUpdate', [this._id]).then(function () { return self; });
    },
    unregister: function () { return call('swUnregister', [this._id]); },
    getNotifications: function (filter) { return global.__pwa.list(this, filter); },
    showNotification: function (title, options) { return global.__pwa.show.apply(null, [this].concat(Array.prototype.slice.call(arguments))); }
  });
  tag(ServiceWorkerRegistration, 'ServiceWorkerRegistration');
  exportClass(ServiceWorkerRegistration, 'ServiceWorkerRegistration');

  // Brings the registration object for a snapshot up to date and returns it. Snapshots that arrive as events are
  // applied in order, so statechange fires for every step; a snapshot read directly (getRegistration, ready) only
  // creates an object that does not exist yet, and an event older than what an object has already seen is dropped.
  function regFor(snap, direct) {
    var r = regs.get(snap.id), fresh = !r;
    if (!r) { r = new ServiceWorkerRegistration(TOKEN, snap); regs.set(snap.id, r); r._rev = -1; }
    if (!fresh && direct) return r;
    if (snap.rev < r._rev) return r;
    r._rev = snap.rev;
    r._uvc = snap.updateViaCache;
    var prevInstalling = r._installing;
    // workers first, so statechange fires with the registration already pointing at them
    var inst = workerFor(snap.installing), wait = workerFor(snap.waiting), act = workerFor(snap.active);
    r._installing = inst; r._waiting = wait; r._active = act;
    (snap.redundant || []).forEach(workerFor);
    if (inst && inst !== prevInstalling) fire(r, 'updatefound');
    return r;
  }
  Object.defineProperty(global, '__swReg', { value: { objFor: regFor, workerFor: workerFor } });

  // ---- ServiceWorkerContainer -------------------------------------------------------------------------------
  var subscribed = false, ctlLoaded = false, ctl = null, readyP = null, readyRes = null;
  function subscribe() { if (!subscribed) { subscribed = true; host('swSubscribe'); } }
  function loadController() {
    if (ctlLoaded) return;
    ctlLoaded = true;
    subscribe();
    var c = host('swController');
    if (c) { regFor(c.reg, true); ctl = workerFor(c.worker); }
  }
  function checkReady() {
    if (!readyRes) return;
    var s = host('swGetRegistration', global.location.href);
    if (s && s.active) { var r = regFor(s, true); if (r._active) { var res = readyRes; readyRes = null; res(r); } }
  }

  function ServiceWorkerContainer(tok) {
    if (tok !== TOKEN) throw new TypeError('Illegal constructor');
    ET.call(this);
  }
  ServiceWorkerContainer.prototype = Object.create(ET.prototype, { constructor: { value: ServiceWorkerContainer, writable: true, configurable: true } });
  defAttrs(ServiceWorkerContainer.prototype, {
    controller: function () { loadController(); return ctl; },
    ready: function () {
      if (!readyP) { subscribe(); readyP = new Promise(function (res) { readyRes = res; checkReady(); }); }
      return readyP;
    }
  });
  handlers(ServiceWorkerContainer.prototype, ['controllerchange', 'message', 'messageerror']);
  defMethods(ServiceWorkerContainer.prototype, {
    register: function (scriptURL, options) {
      if (arguments.length < 1) return Promise.reject(new TypeError("Failed to execute 'register' on 'ServiceWorkerContainer': 1 argument required, but only 0 present."));
      var url, scope = '', type = 'classic', uvc = 'imports';
      try {
        url = absURL(scriptURL);
        if (options && options.scope !== undefined && options.scope !== null) scope = absURL(options.scope);
        if (options && options.type) type = String(options.type);
        if (options && options.updateViaCache) uvc = String(options.updateViaCache);
      } catch (e) { return Promise.reject(new TypeError("Failed to execute 'register' on 'ServiceWorkerContainer': Failed to parse URL from " + scriptURL)); }
      subscribe();
      return call('swRegister', [url, scope, type, uvc]);
    },
    getRegistration: function (clientURL) {
      return new Promise(function (resolve, reject) {
        try {
          subscribe();
          var s = host('swGetRegistration', clientURL === undefined ? global.location.href : absURL(clientURL));
          resolve(s ? regFor(s, true) : undefined);
        } catch (e) { reject(e); }
      });
    },
    getRegistrations: function () {
      return new Promise(function (resolve, reject) {
        try { subscribe(); resolve(host('swGetRegistrations').map(function (s) { return regFor(s, true); })); } catch (e) { reject(e); }
      });
    },
    startMessages: function () { }
  });
  tag(ServiceWorkerContainer, 'ServiceWorkerContainer');
  exportClass(ServiceWorkerContainer, 'ServiceWorkerContainer');
  var container = new ServiceWorkerContainer(TOKEN);

  // ---- events from Go ---------------------------------------------------------------------------------------------
  var handlersByType = {
    result: function (m) {
      var p = pend.get(m.token);
      if (!p) return;
      pend.delete(m.token);
      if (m.ok) p.resolve(m.reg ? regFor(m.reg, true) : m.value);
      else p.reject(mkErr(m.name, m.msg));
    },
    reg: function (m) { regFor(m.reg); checkReady(); },
    removed: function (m) {
      var r = regs.get(m.id);
      if (r) { r._gone = true; regs.delete(m.id); }
    },
    controller: function (m) {
      loadController();
      var next = m.worker ? (regFor(m.reg), workerFor(m.worker)) : null;
      if (next === ctl) return;
      ctl = next;
      fire(container, 'controllerchange');
    },
    message: function (m) {
      var data;
      try { data = global.__swDe(m.data); } catch (e) { fire(container, 'messageerror'); return; }
      var ports = (m.ports || []).map(adoptPort);
      var w = m.from ? workerFor(m.from) : null;
      var ev = new global.MessageEvent('message', { data: data, origin: global.location.origin, source: w, ports: ports });
      try { container.dispatchEvent(ev); } catch (e) { report(e); }
    },
    port: function (m) {
      var p = portsById.get(m.ep);
      if (!p) return;
      var ev = new global.MessageEvent('message', { data: global.__swDe(m.data), origin: '', ports: [] });
      try { p.dispatchEvent(ev); } catch (e) { report(e); }
    }
  };
  Object.defineProperty(global, '__swSwEvent', { value: function (json) {
    var m = JSON.parse(json), f = handlersByType[m.t];
    if (f) { try { f(m); } catch (e) { report(e); } }
  } });

  // ---- navigator.serviceWorker ------------------------------------------------------------------------------------------
  if (global.Navigator) {
    Object.defineProperty(global.Navigator.prototype, 'serviceWorker', {
      get: function () { return H.swAvailable() ? container : undefined; },
      enumerable: true, configurable: true
    });
  }
})(globalThis, globalThis.__sw);
