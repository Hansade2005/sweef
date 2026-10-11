/*
 * Worker global scope for Sweef Web Workers. Runs in its own QuickJS VM with
 * no DOM. Host object __w (registered by Go): post(json), log(level, text),
 * http(method, url, headers, body), fetchText(url), now(), url(), close().
 */
(function (global) {
  'use strict';
  var w = global.__w;
  global.self = global;
  global.globalThis = global;
  var location = { href: w.url() };
  try { var U = w.url(); location.origin = U.replace(/^(\w+:\/\/[^/]+).*$/, '$1'); } catch (e) { }
  global.location = location;
  global.navigator = { userAgent: 'Sweef Worker', hardwareConcurrency: 1, onLine: true };

  // events
  var listeners = {};
  global.addEventListener = function (t, fn) { (listeners[t] = listeners[t] || []).push(fn); };
  global.removeEventListener = function (t, fn) { var l = listeners[t]; if (l) listeners[t] = l.filter(function (f) { return f !== fn; }); };
  function emit(t, ev) {
    var h = global['on' + t];
    try { if (typeof h === 'function') h.call(global, ev); } catch (e) { report(e); }
    (listeners[t] || []).slice().forEach(function (f) { try { f.call(global, ev); } catch (e) { report(e); } });
  }
  function report(e) { w.log('error', (e && e.stack) ? String(e) + '\n' + e.stack : String(e)); w.post(JSON.stringify({ __swError: String(e && e.message || e) })); }

  global.postMessage = function (msg) { w.post(JSON.stringify(msg === undefined ? null : msg)); };
  global.close = function () { w.close(); };

  // console
  function fmt(args) { return Array.prototype.map.call(args, function (a) { if (typeof a === 'string') return a; try { return JSON.stringify(a); } catch (e) { return String(a); } }).join(' '); }
  global.console = {};
  ['log', 'info', 'warn', 'error', 'debug'].forEach(function (k) { global.console[k] = function () { w.log(k === 'warn' ? 'warning' : k, '[worker] ' + fmt(arguments)); }; });

  // timers (pumped by Go through __swwRunTimers)
  var timers = [], seq = 0;
  function add(fn, ms, args, repeat) { var id = ++seq; timers.push({ id: id, at: w.now() + Math.max(0, +ms || 0), fn: fn, args: args, every: repeat ? Math.max(1, +ms || 0) : 0 }); return id; }
  global.setTimeout = function (fn, ms) { return add(fn, ms, Array.prototype.slice.call(arguments, 2), false); };
  global.setInterval = function (fn, ms) { return add(fn, ms, Array.prototype.slice.call(arguments, 2), true); };
  global.clearTimeout = global.clearInterval = function (id) { timers = timers.filter(function (t) { return t.id !== id; }); };
  global.queueMicrotask = function (fn) { Promise.resolve().then(fn); };
  Object.defineProperty(global, '__swwRunTimers', { value: function (now) {
    for (var guard = 0; guard < 1000; guard++) {
      timers.sort(function (a, b) { return a.at - b.at; });
      var t = timers[0];
      if (!t || t.at > now) break;
      if (t.every) t.at = now + t.every; else timers.shift();
      try { if (typeof t.fn === 'function') t.fn.apply(global, t.args); else (0, eval)(String(t.fn)); } catch (e) { report(e); }
    }
    timers.sort(function (a, b) { return a.at - b.at; });
    return timers.length ? Math.max(0, timers[0].at - now) : -1;
  } });
  Object.defineProperty(global, '__swwDeliver', { value: function (json) { emit('message', { data: JSON.parse(json), type: 'message', target: global }); } });

  // network
  global.importScripts = function () { for (var i = 0; i < arguments.length; i++) { var src = w.fetchText(String(arguments[i])); (0, eval)(src); } };
  function Response(r) { this.status = r.status; this.ok = r.status >= 200 && r.status < 300; this.statusText = r.statusText || ''; this.url = r.url; this._body = r.body; var h = r.headers || {}; this.headers = { get: function (k) { var v = h[String(k).toLowerCase()]; return v === undefined ? null : v; }, has: function (k) { return String(k).toLowerCase() in h; } }; }
  Response.prototype.text = function () { return Promise.resolve(this._body); };
  Response.prototype.json = function () { var b = this._body; return new Promise(function (res) { res(JSON.parse(b)); }); };
  global.Response = Response;
  global.fetch = function (input, init) {
    init = init || {};
    return new Promise(function (resolve, reject) {
      try { resolve(new Response(w.http(String(init.method || 'GET'), String(input && input.url || input), init.headers || {}, init.body === undefined ? null : String(init.body)))); }
      catch (e) { reject(new TypeError(String(e && e.message || e))); }
    });
  };
  global.atob = global.atob || function (s) { return w.atob(String(s)); };
  global.btoa = global.btoa || function (s) { return w.btoa(String(s)); };
})(globalThis);
