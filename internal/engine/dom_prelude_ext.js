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
  var TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;
  function wsOrigin(u) { var m = /^(wss?):\/\/([^\/?#]*)/.exec(u); return m ? m[1] + '://' + m[2] : 'null'; }
  function utf8Len(s) { var n = 0; for (var i = 0; i < s.length; i++) { var c = s.charCodeAt(i); if (c < 0x80) n += 1; else if (c < 0x800) n += 2; else if (c >= 0xd800 && c <= 0xdbff && i + 1 < s.length && (s.charCodeAt(i + 1) & 0xfc00) === 0xdc00) { n += 4; i++; } else n += 3; } return n; }
  function WebSocket(url, protocols) {
    if (!(this instanceof WebSocket) || !new.target) throw new TypeError("Failed to construct 'WebSocket': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 1) throw new TypeError("Failed to construct 'WebSocket': 1 argument required, but only 0 present.");
    ET.call(this);
    var u;
    try { u = new global.URL(String(url), global.location.href); } catch (e) { throw new global.DOMException("Failed to construct 'WebSocket': The URL '" + url + "' is invalid.", 'SyntaxError'); }
    if (u.protocol === 'http:') u.protocol = 'ws:'; else if (u.protocol === 'https:') u.protocol = 'wss:';
    if (u.protocol !== 'ws:' && u.protocol !== 'wss:') throw new global.DOMException("Failed to construct 'WebSocket': The URL's scheme must be either 'ws' or 'wss'. '" + u.protocol.slice(0, -1) + "' is not allowed.", 'SyntaxError');
    if (u.hash) throw new global.DOMException("Failed to construct 'WebSocket': The URL contains a fragment identifier ('" + u.hash + "'). Fragment identifiers are not allowed in WebSocket URLs.", 'SyntaxError');
    if (global.isSecureContext && global.location.protocol === 'https:' && u.protocol === 'ws:' && !/^(localhost|127\.|\[::1\])/.test(u.hostname)) throw new global.DOMException("Failed to construct 'WebSocket': An insecure WebSocket connection may not be initiated from a page loaded over HTTPS.", 'SecurityError');
    var list = protocols === undefined ? [] : (typeof protocols === 'string' ? [protocols] : Array.prototype.map.call(protocols, String));
    var seen = {};
    list.forEach(function (p) {
      if (!TOKEN.test(p)) throw new global.DOMException("Failed to construct 'WebSocket': The subprotocol '" + p + "' is invalid.", 'SyntaxError');
      if (seen[p]) throw new global.DOMException("Failed to construct 'WebSocket': The subprotocol '" + p + "' is duplicated.", 'SyntaxError');
      seen[p] = 1;
    });
    var href = u.href;
    def(this, 'url', href);
    def(this, 'readyState', 0);
    def(this, 'protocol', '');
    def(this, 'extensions', '');
    def(this, '_bt', 'blob');
    var id = sw.wsOpen(href, list.join(','));
    def(this, '_id', id);
    sockets.set(id, this);
  }
  WebSocket.prototype = Object.create(ET.prototype);
  Object.defineProperty(WebSocket.prototype, 'constructor', { value: WebSocket, writable: true, configurable: true });
  Object.defineProperty(WebSocket.prototype, 'binaryType', {
    get: function () { return this._bt; },
    set: function (v) { v = String(v); if (v === 'blob' || v === 'arraybuffer') this._bt = v; },
    enumerable: true, configurable: true
  });
  Object.defineProperty(WebSocket.prototype, 'bufferedAmount', {
    get: function () { return this.readyState === 3 ? 0 : (sw.wsBuffered(this._id) || 0); }, enumerable: true, configurable: true
  });
  function viewBytes(v) { return new Uint8Array(v.buffer, v.byteOffset, v.byteLength); }
  WebSocket.prototype.send = function send(data) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'send' on 'WebSocket': 1 argument required, but only 0 present.");
    if (this.readyState === 0) throw new global.DOMException("Failed to execute 'send' on 'WebSocket': Still in CONNECTING state.", 'InvalidStateError');
    if (this.readyState !== 1) return;
    if (typeof data === 'string') { sw.wsSend(this._id, data, false); return; }
    if (data && data._bytes) { sw.wsSend(this._id, bufToB64(data._bytes), true); return; }
    if (data instanceof ArrayBuffer) { sw.wsSend(this._id, bufToB64(new Uint8Array(data)), true); return; }
    if (ArrayBuffer.isView(data)) { sw.wsSend(this._id, bufToB64(viewBytes(data)), true); return; }
    sw.wsSend(this._id, String(data), false);
  };
  WebSocket.prototype.close = function close(code, reason) {
    if (code !== undefined) {
      code = Number(code) & 0xffff;
      if (code !== 1000 && !(code >= 3000 && code <= 4999)) throw new global.DOMException("Failed to execute 'close' on 'WebSocket': The code must be either 1000, or between 3000 and 4999. " + code + " is neither.", 'InvalidAccessError');
    }
    if (reason !== undefined) {
      reason = String(reason);
      if (utf8Len(reason) > 123) throw new global.DOMException("Failed to execute 'close' on 'WebSocket': The message must not be greater than 123 bytes.", 'SyntaxError');
    }
    if (this.readyState >= 2) return;
    this.readyState = 2;
    sw.wsClose(this._id, code === undefined ? 0 : code, reason === undefined ? '' : reason);
  };
  handlerProps(WebSocket.prototype, ['open', 'message', 'error', 'close']);
  [['CONNECTING', 0], ['OPEN', 1], ['CLOSING', 2], ['CLOSED', 3]].forEach(function (p) {
    Object.defineProperty(WebSocket, p[0], { value: p[1], enumerable: true }); Object.defineProperty(WebSocket.prototype, p[0], { value: p[1], enumerable: true });
  });
  Object.defineProperty(WebSocket.prototype, Symbol.toStringTag, { value: 'WebSocket', configurable: true });
  Object.defineProperty(WebSocket, 'name', { value: 'WebSocket', configurable: true });
  global.WebSocket = WebSocket;

  def(global, '__swWsEvent', function (id, type, data, binary, code, reason, clean) {
    var ws = sockets.get(id);
    if (!ws) return false;
    try {
      if (type === 'open') {
        if (ws.readyState !== 0) return true;
        ws.readyState = 1; ws.protocol = data || '';
        ws.dispatchEvent(new global.Event('open'));
      } else if (type === 'message') {
        if (ws.readyState !== 1) return true;
        var payload = data;
        if (binary) {
          var buf = b64ToBuf(data);
          payload = ws._bt === 'arraybuffer' ? buf : new global.Blob([new Uint8Array(buf)]);
        }
        ws.dispatchEvent(new global.MessageEvent('message', { data: payload, origin: wsOrigin(ws.url) }));
      } else if (type === 'error') {
        ws.dispatchEvent(new global.Event('error'));
      } else if (type === 'close') {
        ws.readyState = 3;
        sockets.delete(id);
        ws.dispatchEvent(new global.CloseEvent('close', { wasClean: !!clean, code: code || 1006, reason: reason || '' }));
      }
    } catch (e) { if (global.reportError) global.reportError(e); }
    return true;
  });
})(globalThis);
