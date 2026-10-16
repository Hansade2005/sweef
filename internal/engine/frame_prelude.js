/*
 * Nested browsing contexts as seen from script: HTMLIFrameElement.contentWindow / contentDocument, window.parent,
 * top, frames, frameElement, length, postMessage between windows with origin checks.
 *
 * Every window is a Tab with a VM of its own (frames.go). A window other than this one is a FrameWindow, a Proxy:
 * postMessage, closed, location (writable), parent, top, frames, length work across origins; anything else works
 * only for a same-origin window and is forwarded to that window's VM (frames_host.go: frOp). Objects of the other
 * window are not copied: a Remote is a proxy for an object that stays in the other VM, addressed by id, so
 * `iframe.contentDocument.body.appendChild(x)` and `contentWindow.myFunction(1, 'a')` act on the real thing.
 * Values cross as structured clones, functions you pass become callbacks that run later in your VM (their result is
 * not returned), objects of a third VM or DOM nodes of this VM cannot be passed.
 */
(function (global, H) {
  'use strict';
  if (!H || typeof H.frSelf !== 'function') return;

  function mkErr(name, msg) {
    if (name === 'TypeError') return new TypeError(msg);
    if (name === 'RangeError') return new RangeError(msg);
    if (name === 'ReferenceError') return new ReferenceError(msg);
    if (name === 'SyntaxError') return new SyntaxError(msg);
    if (name === 'Error') return new Error(msg);
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
  function report(e) { if (typeof global.reportError === 'function') { try { global.reportError(e); } catch (x) { /* ignore */ } } }
  var REMOTE = Symbol('remote');
  var selfInfo = null;
  function me() { return selfInfo || (selfInfo = host('frSelf')); }

  // ---- calls into another window's VM ---------------------------------------------------------------------
  var remoteCache = new Map(), cbs = new Map(), cbSeq = 0;

  function encode(v, win) {
    if (v === null || (typeof v !== 'object' && typeof v !== 'function')) return { k: 'j', s: global.__swSer(v) };
    var r = v[REMOTE];
    if (r) {
      if (r.win !== win) throw mkErr('DataCloneError', 'An object of another window cannot be passed to this window.');
      return { k: 'r', id: r.id };
    }
    if (typeof v === 'function') { var id = ++cbSeq; cbs.set(id, v); return { k: 'cb', win: me().win, id: id }; }
    return { k: 'j', s: global.__swSer(v, []) };
  }
  function decode(win, d, parentId, name) {
    switch (d.k) {
      case 'j': return global.__swDe(d.s, []);
      case 'o': return remoteProxy(win, d.id, d.t, parentId, name);
      case 'e': throw mkErr(d.n, d.m);
    }
    return undefined;
  }
  function remoteCall(win, op, id, prop, args, thisId) {
    var encoded = [];
    if (args) for (var i = 0; i < args.length; i++) encoded.push(encode(args[i], win));
    var r = host('frOp', win, op, id, prop === undefined ? '' : String(prop), JSON.stringify(encoded), thisId || 0);
    return decode(win, JSON.parse(r), id, op === 'get' ? String(prop) : undefined);
  }
  function remoteProxy(win, id, type, thisId, name) {
    var key = win + ':' + id;
    if (type !== 'function') { var c = remoteCache.get(key); if (c) return c; }
    var base = type === 'function' ? function () { }.bind(null) : (type === 'array' ? [] : {});
    var p = new Proxy(base, {
      get: function (t, k) {
        if (k === REMOTE) return { win: win, id: id };
        if (typeof k === 'symbol') {
          if (k === Symbol.toPrimitive) return function () { return String(remoteCall(win, 'str', id)); };
          if (k === Symbol.toStringTag) return type === 'function' ? 'Function' : 'Object';
          return undefined;
        }
        if (k === 'then') return undefined; // never a thenable: it would be unwrapped by await
        // bind/call/apply of a remote function run here, on the proxy: the call then reaches the apply trap with the caller's
        // `this` (a remote bind would copy that `this` into the other window instead)
        if (type === 'function' && (k === 'bind' || k === 'call' || k === 'apply')) return Function.prototype[k];
        return remoteCall(win, 'get', id, k);
      },
      set: function (t, k, v) { if (typeof k === 'symbol') return false; remoteCall(win, 'set', id, k, [v]); return true; },
      has: function (t, k) { return typeof k === 'string' && !!remoteCall(win, 'has', id, k); },
      deleteProperty: function (t, k) { if (typeof k === 'symbol') return false; return !!remoteCall(win, 'del', id, k); },
      ownKeys: function () { return remoteCall(win, 'keys', id) || []; },
      getOwnPropertyDescriptor: function (t, k) {
        if (typeof k !== 'string' || !remoteCall(win, 'has', id, k)) return undefined;
        return { value: remoteCall(win, 'get', id, k), enumerable: true, configurable: true, writable: true };
      },
      apply: function (t, thisArg, args) {
        var tr = thisArg !== null && thisArg !== undefined ? thisArg[REMOTE] : null;
        // A platform method taken from another window and applied to an object of this one (a page borrows an iframe's
        // pristine History.prototype.replaceState and binds it to its own window.history) runs on that object, as a
        // cross-realm method does in a browser: run this window's implementation of the same method.
        if (name && !tr && thisId && thisArg !== null && typeof thisArg === 'object') {
          var iface = remoteCall(win, 'tag', thisId), C = typeof iface === 'string' ? global[iface] : undefined;
          if (typeof C === 'function' && C.prototype && thisArg instanceof C && typeof C.prototype[name] === 'function') return C.prototype[name].apply(thisArg, args);
        }
        return remoteCall(win, 'call', id, '', args, tr && tr.win === win ? tr.id : thisId);
      },
      construct: function (t, args) { return remoteCall(win, 'new', id, '', args); },
      getPrototypeOf: function () { return type === 'function' ? Function.prototype : Object.prototype; }
    });
    if (type !== 'function') {
      remoteCache.set(key, p);
      if (remoteCache.size > 4096) remoteCache.delete(remoteCache.keys().next().value);
    }
    return p;
  }

  // ---- the target side: objects other windows hold by id --------------------------------------------------
  var robjs = new Map(), rids = new WeakMap(), rseq = 0;
  function reg(o) {
    if (o === global) return 0;
    var id = rids.get(o);
    if (id) return id;
    id = ++rseq;
    robjs.set(id, o);
    rids.set(o, id);
    if (robjs.size > 8192) { var first = robjs.keys().next().value; var old = robjs.get(first); robjs.delete(first); if (old !== undefined) rids.delete(old); }
    return id;
  }
  function objOf(id) { return id === 0 ? global : robjs.get(id); }
  function safeArg(a) {
    try { global.__swSer(a, []); return a; } catch (e) { return a && a.type !== undefined ? { type: String(a.type) } : String(a); }
  }
  function stub(caller, id) {
    return function () {
      var args = Array.prototype.map.call(arguments, safeArg);
      try { H.frCallback(caller, id, global.__swSer(args, [])); } catch (e) { /* the caller is gone */ }
    };
  }
  function decodeArg(a) {
    switch (a.k) {
      case 'j': return global.__swDe(a.s, []);
      case 'r': { var o = objOf(a.id); if (o === undefined) throw mkErr('InvalidStateError', 'The object no longer exists.'); return o; }
      case 'cb': return stub(a.win, a.id);
    }
    return undefined;
  }
  function result(res) {
    if (res === null || (typeof res !== 'object' && typeof res !== 'function')) return { k: 'j', s: global.__swSer(res) };
    return { k: 'o', id: reg(res), t: typeof res === 'function' ? 'function' : (Array.isArray(res) ? 'array' : 'object') };
  }
  Object.defineProperty(global, '__swRemote', { value: function (op, id, prop, argsJSON, thisId) {
    try {
      var obj = objOf(id);
      if (obj === undefined) throw mkErr('InvalidStateError', 'The object no longer exists.');
      var args = argsJSON ? JSON.parse(argsJSON).map(decodeArg) : [], res;
      switch (op) {
        case 'get': res = obj[prop]; break;
        case 'set': obj[prop] = args[0]; res = true; break;
        case 'has': res = prop in obj; break;
        case 'del': res = delete obj[prop]; break;
        case 'keys': res = Object.keys(obj); break;
        case 'call': { var self = objOf(thisId); res = Function.prototype.apply.call(obj, self === undefined ? undefined : self, args); break; }
        case 'new': res = Reflect.construct(obj, args); break;
        case 'str': res = String(obj); break;
        case 'tag': res = Object.prototype.toString.call(obj).slice(8, -1); break;
        default: throw mkErr('TypeError', 'unknown operation');
      }
      return JSON.stringify(result(res));
    } catch (e) {
      return JSON.stringify({ k: 'e', n: (e && e.name) || 'Error', m: String(e && e.message !== undefined ? e.message : e) });
    }
  } });
  Object.defineProperty(global, '__swRemoteElem', { value: function (elem) {
    try { return JSON.stringify(result(global.__swWrap(elem))); } catch (e) { return JSON.stringify({ k: 'e', n: 'Error', m: String(e) }); }
  } });

  // ---- windows ---------------------------------------------------------------------------------------------
  var wins = new Map();
  function windowFor(win) {
    if (!win) return null;
    if (win === me().win) return global;
    var w = wins.get(win);
    if (!w) { w = makeWindow(win); wins.set(win, w); }
    return w;
  }
  function originArg(a, b) {
    // postMessage(message, targetOrigin, transfer) or postMessage(message, { targetOrigin, transfer })
    if (a !== null && typeof a === 'object' && !Array.isArray(a) && b === undefined) return { target: a.targetOrigin === undefined ? '/' : String(a.targetOrigin), transfer: a.transfer };
    if (a === undefined) return { target: '/', transfer: b };
    return { target: String(a), transfer: b };
  }
  var CROSS = ['postMessage', 'closed', 'frames', 'length', 'location', 'opener', 'parent', 'self', 'top', 'window', 'focus', 'blur', 'close'];
  function locationFor(win) {
    // across origins a window's location can only be assigned to
    var loc = { replace: function (u) { host('frNav', win, String(u), true); }, assign: function (u) { host('frNav', win, String(u), false); } };
    return new Proxy(loc, {
      get: function (t, k) {
        if (k === 'replace' || k === 'assign') return t[k];
        if (typeof k === 'symbol') return k === Symbol.toStringTag ? 'Location' : undefined;
        throw mkErr('SecurityError', 'Blocked a frame from accessing a cross-origin frame.');
      },
      set: function (t, k, v) {
        if (k === 'href') { host('frNav', win, String(v), false); return true; }
        throw mkErr('SecurityError', 'Blocked a frame from accessing a cross-origin frame.');
      }
    });
  }
  function makeWindow(win) {
    var proxy;
    var handler = {
      get: function (t, p) {
        if (p === REMOTE) return { win: win, id: 0 };
        if (typeof p === 'symbol') return p === Symbol.toStringTag ? 'Window' : undefined;
        switch (p) {
          case 'postMessage': return function postMessage(message, a, b) {
            if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'Window': 1 argument required, but only 0 present.");
            var o = originArg(a, b), pr = global.__swWkPrepare(message, o.transfer);
            var eps = pr.commit();
            host('frPost', win, pr.data, o.target, eps);
          };
          case 'closed': return !!host('frInfo', win).closed;
          case 'self': case 'window': case 'frames': return proxy;
          case 'parent': { var i1 = host('frInfo', win); return windowFor(i1.parent || win); }
          case 'top': return windowFor(host('frInfo', win).top);
          case 'opener': { var oid = host('frInfo', win).opener; return oid ? windowFor(oid) : null; }
          case 'length': return host('frInfo', win).length;
          case 'location': return host('frAccess', win) ? remoteCall(win, 'get', 0, 'location') : locationFor(win);
          case 'focus': return function () { host('frName', 'focus', win); };
          case 'close': return function () { host('frName', 'close', win); };
          case 'blur': return function () { };
        }
        if (/^\d+$/.test(p)) { var kids = host('frChildren', win); return kids[+p] !== undefined ? windowFor(kids[+p]) : undefined; }
        if (host('frAccess', win)) return remoteCall(win, 'get', 0, p);
        throw mkErr('SecurityError', 'Blocked a frame from accessing a cross-origin frame.');
      },
      set: function (t, p, v) {
        if (typeof p === 'symbol') return false;
        if (p === 'location') { host('frNav', win, String(v), false); return true; }
        if (host('frAccess', win)) { remoteCall(win, 'set', 0, p, [v]); return true; }
        throw mkErr('SecurityError', 'Blocked a frame from accessing a cross-origin frame.');
      },
      has: function (t, p) {
        if (typeof p !== 'string') return false;
        if (CROSS.indexOf(p) >= 0) return true;
        return host('frAccess', win) ? !!remoteCall(win, 'has', 0, p) : false;
      },
      ownKeys: function () { return host('frAccess', win) ? remoteCall(win, 'keys', 0) : CROSS.slice(); },
      getOwnPropertyDescriptor: function (t, p) {
        if (typeof p !== 'string') return undefined;
        if (CROSS.indexOf(p) >= 0 || (host('frAccess', win) && remoteCall(win, 'has', 0, p))) return { value: handler.get(t, p), enumerable: true, configurable: true, writable: true };
        return undefined;
      },
      getPrototypeOf: function () { return global.Window ? global.Window.prototype : Object.prototype; }
    };
    proxy = new Proxy({}, handler);
    return proxy;
  }

  // ---- this window's relations -------------------------------------------------------------------------------
  function define(name, get, set) { Object.defineProperty(global, name, { get: get, set: set || function () { }, configurable: true, enumerable: true }); }
  define('parent', function () { var s = me(); return s.framed ? windowFor(s.parent) : global; });
  define('top', function () { var s = me(); return s.framed ? windowFor(s.top) : global; });
  define('opener', function () { var o = host('frInfo', me().win).opener; return o ? windowFor(o) : null; }, function () { });
  define('frameElement', function () {
    var s = me();
    if (!s.framed || !host('frAccess', s.parent)) return null;
    var d = JSON.parse(host('frElement'));
    return decode(s.parent, d, 0);
  });
  define('length', function () { return host('frChildren', me().win).length; });
  define('frames', function () {
    var self = me().win;
    return new Proxy({}, {
      get: function (t, k) {
        if (k === 'length') return host('frChildren', self).length;
        if (typeof k === 'string' && /^\d+$/.test(k)) { var kids = host('frChildren', self); return kids[+k] !== undefined ? windowFor(kids[+k]) : undefined; }
        if (k === 'window' || k === 'self') return global;
        return global[k];
      },
      has: function (t, k) { return k in global; }
    });
  });
  define('name', function () { return host('frName'); }, function (v) { host('frName', 'setName', String(v)); });
  // window.open / close / focus (windows.go): popups are windows of the same group, reached like frames
  global.open = function open(url, target, features) {
    var u = url === undefined || url === null ? '' : String(url);
    var tg = target === undefined || target === null || String(target) === '' ? '_blank' : String(target);
    var id = host('frName', 'open', u, tg, features === undefined || features === null ? '' : String(features));
    return id ? windowFor(id) : null;
  };
  global.close = function close() { host('frName', 'close', me().win); };
  global.focus = function focus() { host('frName', 'focus', me().win); };
  Object.defineProperty(global, '__swFrameInit', { value: function (nm) { me().nameValue = nm || ''; return true; } });

  // ---- <iframe> --------------------------------------------------------------------------------------------------
  var IFR = global.HTMLIFrameElement && global.HTMLIFrameElement.prototype;
  if (IFR) {
    var idOf = function (el) { return global.__swIdOf(el); };
    Object.defineProperty(IFR, 'contentWindow', { get: function () { var w = host('frOf', idOf(this)); return w ? windowFor(w) : null; }, enumerable: true, configurable: true });
    Object.defineProperty(IFR, 'contentDocument', { get: function () {
      var w = host('frOf', idOf(this));
      if (!w || !host('frAccess', w)) return null;
      return remoteCall(w, 'get', 0, 'document');
    }, enumerable: true, configurable: true });
    // `src` only reflects the attribute: the shared image setter would fire a load event at once
    Object.defineProperty(IFR, 'src', {
      get: function () { var v = this.getAttribute('src'); if (v === null) return ''; try { return new global.URL(v, global.document.baseURI).href; } catch (e) { return v; } },
      set: function (v) { this.setAttribute('src', String(v)); }, enumerable: true, configurable: true
    });
    Object.defineProperty(IFR, 'getSVGDocument', { value: function () { return this.contentDocument; }, writable: true, configurable: true });
    var attr = function (prop, name) {
      if (prop in IFR && Object.getOwnPropertyDescriptor(IFR, prop)) return;
      Object.defineProperty(IFR, prop, {
        get: function () { var v = this.getAttribute(name || prop.toLowerCase()); return v === null ? '' : v; },
        set: function (v) { this.setAttribute(name || prop.toLowerCase(), String(v)); }, enumerable: true, configurable: true
      });
    };
    ['name', 'srcdoc', 'allow', 'width', 'height', 'loading', 'align', 'scrolling', 'frameBorder', 'longDesc', 'marginHeight', 'marginWidth'].forEach(function (p) { attr(p); });
    attr('referrerPolicy', 'referrerpolicy');
    Object.defineProperty(IFR, 'allowFullscreen', { get: function () { return this.hasAttribute('allowfullscreen'); }, set: function (v) { if (v) this.setAttribute('allowfullscreen', ''); else this.removeAttribute('allowfullscreen'); }, enumerable: true, configurable: true });
    Object.defineProperty(IFR, 'sandbox', { get: function () {
      var el = this;
      return {
        get value() { return el.getAttribute('sandbox') || ''; }, set value(v) { el.setAttribute('sandbox', String(v)); },
        get length() { return (el.getAttribute('sandbox') || '').split(/\s+/).filter(Boolean).length; },
        contains: function (t) { return (el.getAttribute('sandbox') || '').split(/\s+/).indexOf(String(t)) >= 0; },
        add: function () { var l = (el.getAttribute('sandbox') || '').split(/\s+/).filter(Boolean); for (var i = 0; i < arguments.length; i++) if (l.indexOf(arguments[i]) < 0) l.push(arguments[i]); el.setAttribute('sandbox', l.join(' ')); },
        remove: function () { var rm = Array.prototype.slice.call(arguments); el.setAttribute('sandbox', (el.getAttribute('sandbox') || '').split(/\s+/).filter(function (x) { return x && rm.indexOf(x) < 0; }).join(' ')); },
        toggle: function (t) { if (this.contains(t)) { this.remove(t); return false; } this.add(t); return true; },
        supports: function () { return true; },
        toString: function () { return el.getAttribute('sandbox') || ''; }
      };
    }, set: function (v) { this.setAttribute('sandbox', String(v)); }, enumerable: true, configurable: true });
  }

  // ---- events from Go ---------------------------------------------------------------------------------------------
  Object.defineProperty(global, '__swFrameEvent', { value: function (json) {
    var m = JSON.parse(json);
    try {
      switch (m.t) {
        case 'load': {
          var el = global.__swWrap(m.elem);
          if (el) el.dispatchEvent(new global.Event('load'));
          return true;
        }
        case 'msg': {
          var ports = (m.ports || []).map(global.__swPorts.adopt), data, bad = false;
          try { data = global.__swDe(m.data, ports); } catch (e) { bad = true; }
          global.dispatchEvent(bad ? new global.MessageEvent('messageerror', {}) : new global.MessageEvent('message', { data: data, origin: m.origin, source: windowFor(m.src), ports: ports }));
          return true;
        }
        case 'storage': {
          global.dispatchEvent(new global.StorageEvent('storage', { key: m.key, oldValue: m.old, newValue: m.new, url: m.url, storageArea: global.localStorage }));
          return true;
        }
        case 'cb': {
          var f = cbs.get(m.id);
          if (f) f.apply(undefined, global.__swDe(m.args, []));
          return true;
        }
      }
    } catch (e) { report(e); }
    return false;
  } });
})(globalThis, globalThis.__sw);
