/*
 * Sweef prelude: windows. WindowProxy objects for other browsing contexts (frames, the
 * opener, popups), window.open / close / focus, window.parent / top / opener / name,
 * HTMLIFrameElement.contentWindow and cross-window postMessage.
 *
 * Every other window is reached through an id the host handed this document; the host
 * checks it on every call (frames_host.go). Messages are structured-cloned into a JSON
 * envelope that keeps Date, Map, Set, ArrayBuffer / typed arrays, undefined and
 * non-finite numbers; other class instances arrive as plain objects. Cross-window DOM
 * access (win.document, iframe.contentDocument) is not available.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || typeof sw.winId !== 'function') return;
  var selfId = null; // the host binds the VM to a tab after the prelude ran: ask lazily
  function SELF() { return selfId || (selfId = sw.winId()); }
  var proxies = new Map();
  var hasOwn = Object.prototype.hasOwnProperty;

  function def(o, k, d) {
    d.configurable = true;
    if (!('get' in d) && !('set' in d) && d.writable === undefined) d.writable = true;
    Object.defineProperty(o, k, d);
  }
  function secErr(msg) {
    try { return new global.DOMException(msg, 'SecurityError'); } catch (e) { return new Error(msg); }
  }
  function cloneErr(msg) {
    try { return new global.DOMException(msg, 'DataCloneError'); } catch (e) { return new Error(msg); }
  }
  function sameOrigin(id) {
    var o = '', mine = '';
    try { o = sw.winOrigin(id) || ''; mine = sw.winOrigin(SELF()) || ''; } catch (e) { return false; }
    return o !== '' && o !== 'null' && o === mine;
  }

  // ---- structured clone envelope -----------------------------------------------------
  function b64(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
    return global.btoa(s);
  }
  function unb64(s) {
    var bin = global.atob(s), u = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u;
  }
  function enc(v) {
    try {
      return JSON.stringify({ v: v }, function (k, val) {
        var o = this[k];
        if (o === undefined) return { $t: 'U' };
        if (typeof o === 'function' || typeof o === 'symbol') throw cloneErr('The object can not be cloned.');
        if (typeof o === 'number' && !isFinite(o)) return { $t: 'N', v: String(o) };
        if (o instanceof Date) return { $t: 'D', v: +o };
        if (o instanceof Map) return { $t: 'M', v: Array.from(o.entries()) };
        if (o instanceof Set) return { $t: 'S', v: Array.from(o.values()) };
        if (typeof ArrayBuffer !== 'undefined' && o instanceof ArrayBuffer) return { $t: 'AB', v: b64(new Uint8Array(o)) };
        if (typeof ArrayBuffer !== 'undefined' && ArrayBuffer.isView(o)) return { $t: 'TA', n: o.constructor.name, v: b64(new Uint8Array(o.buffer, o.byteOffset, o.byteLength)) };
        if (o instanceof Error) return { $t: 'E', n: o.name, v: o.message };
        return val;
      });
    } catch (e) {
      if (e && e.name === 'DataCloneError') throw e;
      throw cloneErr('The object can not be cloned.');
    }
  }
  function dec(s) {
    return JSON.parse(s, function (k, val) {
      if (val && typeof val === 'object' && typeof val.$t === 'string') {
        switch (val.$t) {
          case 'U': return undefined;
          case 'N': return Number(val.v);
          case 'D': return new Date(val.v);
          case 'M': return new Map(val.v);
          case 'S': return new Set(val.v);
          case 'AB': return unb64(val.v).buffer;
          case 'TA': {
            var u = unb64(val.v), C = global[val.n] || Uint8Array;
            return new C(u.buffer, 0, Math.floor(u.byteLength / (C.BYTES_PER_ELEMENT || 1)));
          }
          case 'E': { var E = global[val.n] || Error; var er = new E(val.v); return er; }
        }
      }
      return val;
    }).v;
  }

  function checkTargetOrigin(t) {
    if (t && typeof t === 'object') t = t.targetOrigin;
    if (t === undefined || t === null) return '/';
    return String(t);
  }

  // ---- WindowProxy ---------------------------------------------------------------------
  function proxyFor(id) {
    if (id === null || id === undefined || id === '') return null;
    if (id === SELF()) return global;
    var p = proxies.get(id);
    if (!p) { p = new WindowProxy(id); proxies.set(id, p); }
    return p;
  }
  function relProxy(id, which, fallback) {
    var r = sw.winRel(id, which);
    return r ? proxyFor(r) : fallback;
  }
  function WindowProxy(id) { Object.defineProperty(this, '__wid', { value: id }); }
  var WP = WindowProxy.prototype;
  def(WP, Symbol.toStringTag, { value: 'Window', writable: false });
  def(WP, 'closed', { get: function () { return !!sw.winClosed(this.__wid); }, enumerable: true });
  def(WP, 'parent', { get: function () { return relProxy(this.__wid, 'parent', this); }, enumerable: true });
  def(WP, 'top', { get: function () { return relProxy(this.__wid, 'top', this); }, enumerable: true });
  def(WP, 'opener', { get: function () { return relProxy(this.__wid, 'opener', null); }, enumerable: true });
  def(WP, 'self', { get: function () { return this; }, enumerable: true });
  def(WP, 'window', { get: function () { return this; }, enumerable: true });
  def(WP, 'frames', { get: function () { return this; }, enumerable: true });
  def(WP, 'length', { get: function () { return sw.winLen(this.__wid) | 0; }, enumerable: true });
  def(WP, 'name', {
    get: function () {
      if (!sameOrigin(this.__wid)) throw secErr('Blocked a frame from accessing a cross-origin frame.');
      return sw.winName(this.__wid);
    },
    enumerable: true
  });
  def(WP, 'document', {
    get: function () {
      if (!sameOrigin(this.__wid)) throw secErr('Blocked a frame from accessing a cross-origin frame.');
      throw new Error('Sweef does not support access to another window\'s document');
    },
    enumerable: true
  });
  def(WP, 'location', {
    get: function () {
      var id = this.__wid, loc = {};
      Object.defineProperty(loc, 'href', {
        get: function () {
          if (!sameOrigin(id)) throw secErr('Blocked a frame from accessing a cross-origin frame.');
          throw new Error('Sweef does not support reading another window\'s location');
        },
        set: function (v) { sw.winNavigate(id, String(v)); }, enumerable: true
      });
      loc.assign = loc.replace = function (v) { sw.winNavigate(id, String(v)); };
      return loc;
    },
    set: function (v) { sw.winNavigate(this.__wid, String(v)); },
    enumerable: true
  });
  def(WP, 'postMessage', {
    value: function postMessage(message, targetOrigin, transfer) {
      if (arguments.length < 1) throw new TypeError("Failed to execute 'postMessage' on 'Window': 1 argument required, but only 0 present.");
      sw.winPost(this.__wid, enc(message), checkTargetOrigin(targetOrigin));
    }, enumerable: true
  });
  def(WP, 'close', { value: function close() { sw.winClose(this.__wid); }, enumerable: true });
  def(WP, 'focus', { value: function focus() { sw.winFocus(this.__wid); }, enumerable: true });
  def(WP, 'blur', { value: function blur() { }, enumerable: true });
  global.WindowProxy = WindowProxy;

  // ---- the window itself --------------------------------------------------------------------
  def(global, 'parent', { get: function () { return relProxy(SELF(), 'parent', global); }, set: function () { }, enumerable: true });
  def(global, 'top', { get: function () { return relProxy(SELF(), 'top', global); }, set: function () { }, enumerable: true });
  def(global, 'opener', {
    get: function () { var o = sw.winRel(SELF(), 'opener'); return o ? proxyFor(o) : null; },
    set: function (v) { if (v === null) def(global, 'opener', { value: null, writable: true, enumerable: true }); },
    enumerable: true
  });
  def(global, 'length', { get: function () { return sw.winLen(SELF()) | 0; }, set: function () { }, enumerable: true });
  def(global, 'closed', { get: function () { return !!sw.winClosed(SELF()); }, set: function () { }, enumerable: true });
  def(global, 'name', { get: function () { return sw.winName(SELF()); }, set: function (v) { sw.setWinName(String(v)); }, enumerable: true });
  // window[0], window[1], ...: the child frames (window.frames is window itself)
  for (var fi = 0; fi < 16; fi++) {
    (function (i) {
      def(global, String(i), { get: function () { var id = sw.winFrameAt(SELF(), i); return id ? proxyFor(id) : undefined; }, set: function () { }, enumerable: false });
    })(fi);
  }
  global.open = function open(url, target, features) {
    var u = url === undefined || url === null ? '' : String(url);
    var tg = target === undefined || target === null || String(target) === '' ? '_blank' : String(target);
    var ft = features === undefined || features === null ? '' : String(features);
    var id = sw.winOpen(u, tg, ft);
    return id ? proxyFor(id) : null;
  };
  global.close = function close() { sw.winClose(SELF()); };
  global.focus = function focus() { sw.winFocus(SELF()); };

  // ---- <iframe> ---------------------------------------------------------------------------------
  var ifr = global.HTMLIFrameElement && global.HTMLIFrameElement.prototype;
  if (ifr) {
    def(ifr, 'contentWindow', {
      get: function () { var id = sw.frameWinOf(global.__swIdOf(this)); return id ? proxyFor(id) : null; },
      enumerable: true
    });
    def(ifr, 'contentDocument', { get: function () { return null; }, enumerable: true });
  }

  // ---- messages from other windows ----------------------------------------------------------
  Object.defineProperty(global, '__swDeliverMessage', {
    value: function (srcId, origin, json) {
      var data;
      try { data = dec(json); } catch (e) { return; }
      var ev = new global.MessageEvent('message', { data: data, origin: origin, source: srcId ? proxyFor(srcId) : null, lastEventId: '', ports: [] });
      global.dispatchEvent(ev);
    },
    configurable: true, writable: true, enumerable: false
  });

  // ---- navigator.userActivation -------------------------------------------------------------
  if (global.navigator) {
    try {
      Object.defineProperty(global.navigator, 'userActivation', {
        get: function () { var a = !!sw.activeNow(); return { isActive: a, hasBeenActive: a }; },
        configurable: true, enumerable: true
      });
    } catch (e) { /* read-only navigator */ }
  }
})(globalThis);
