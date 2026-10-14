/*
 * Sweef prelude extensions: the real WebSocket implementation (Workers live in workers_prelude.js).
 * Evaluated right after dom_prelude.js (same bytecode unit). Uses only public
 * globals, so it layers cleanly on the base prelude.
 *
 * Go owns the sockets. Events flow back through __swWsEvent, which Go calls from the event-loop pump.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  var ET = global.EventTarget;

  function def(o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true }); }
  function b64ToBuf(b64) {
    var bin = global.atob(b64), u = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u.buffer;
  }
  function bufToB64(buf) {
    var u = buf instanceof Uint8Array ? buf : new Uint8Array(buf.buffer ? buf.buffer : buf), s = '';
    for (var i = 0; i < u.length; i += 0x8000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000));
    return global.btoa(s);
  }
  function handlerProps(proto, names) {
    names.forEach(function (n) {
      var key = '__on' + n;
      Object.defineProperty(proto, 'on' + n, {
        configurable: true, enumerable: true,
        get: function () { return this[key] || null; },
        set: function (fn) {
          if (this[key]) this.removeEventListener(n, this[key]);
          this[key] = typeof fn === 'function' ? fn : null;
          if (this[key]) this.addEventListener(n, this[key]);
        }
      });
    });
  }
  function absURL(u) { try { return new global.URL(String(u), global.location.href).href; } catch (e) { return String(u); } }

  // ---- WebSocket -------------------------------------------------------------
  var sockets = new Map();
  function WebSocket(url, protocols) {
    if (!(this instanceof WebSocket)) throw new TypeError("Constructor WebSocket requires 'new'");
    ET.call(this);
    var u = absURL(url).replace(/^http/, 'ws');
    if (!/^wss?:\/\//.test(u)) throw new global.DOMException("Failed to construct 'WebSocket': The URL's scheme must be either 'ws' or 'wss'.", 'SyntaxError');
    def(this, 'url', u);
    def(this, 'readyState', 0);
    def(this, 'protocol', '');
    def(this, 'extensions', '');
    def(this, 'bufferedAmount', 0);
    def(this, 'binaryType', 'blob');
    var list = Array.isArray(protocols) ? protocols : (protocols ? [protocols] : []);
    var id = sw.wsOpen(u, list.join(','));
    def(this, '_id', id);
    sockets.set(id, this);
  }
  WebSocket.prototype = Object.create(ET.prototype);
  WebSocket.prototype.constructor = WebSocket;
  WebSocket.prototype.send = function (data) {
    if (this.readyState === 0) throw new global.DOMException("Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.", 'InvalidStateError');
    if (this.readyState !== 1) return;
    if (typeof data === 'string') { sw.wsSend(this._id, data, false); return; }
    if (data && data._bytes) { sw.wsSend(this._id, bufToB64(data._bytes), true); return; }
    if (data instanceof ArrayBuffer || ArrayBuffer.isView(data)) { sw.wsSend(this._id, bufToB64(data), true); return; }
    sw.wsSend(this._id, String(data), false);
  };
  WebSocket.prototype.close = function (code, reason) {
    if (this.readyState >= 2) return;
    this.readyState = 2;
    sw.wsClose(this._id, code === undefined ? 1000 : code, reason === undefined ? '' : String(reason));
  };
  handlerProps(WebSocket.prototype, ['open', 'message', 'error', 'close']);
  [['CONNECTING', 0], ['OPEN', 1], ['CLOSING', 2], ['CLOSED', 3]].forEach(function (p) {
    Object.defineProperty(WebSocket, p[0], { value: p[1] }); Object.defineProperty(WebSocket.prototype, p[0], { value: p[1] });
  });
  Object.defineProperty(WebSocket.prototype, Symbol.toStringTag, { value: 'WebSocket' });
  global.WebSocket = WebSocket;

  def(global, '__swWsEvent', function (id, type, data, binary, code, reason) {
    var ws = sockets.get(id);
    if (!ws) return false;
    try {
      if (type === 'open') {
        ws.readyState = 1; ws.protocol = data || '';
        ws.dispatchEvent(new global.Event('open'));
      } else if (type === 'message') {
        var payload = data;
        if (binary) {
          var buf = b64ToBuf(data);
          payload = ws.binaryType === 'arraybuffer' ? buf : new global.Blob([new Uint8Array(buf)]);
        }
        ws.dispatchEvent(new global.MessageEvent('message', { data: payload, origin: ws.url }));
      } else if (type === 'error') {
        ws.dispatchEvent(new global.Event('error'));
      } else if (type === 'close') {
        ws.readyState = 3;
        sockets.delete(id);
        ws.dispatchEvent(new global.CloseEvent('close', { wasClean: code === 1000, code: code || 1006, reason: reason || '' }));
      }
    } catch (e) { if (global.reportError) global.reportError(e); }
    return true;
  });
})(globalThis);
