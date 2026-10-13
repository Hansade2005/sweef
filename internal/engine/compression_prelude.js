/*
 * CompressionStream / DecompressionStream. The data is gathered while the stream is written and
 * transformed in one piece when it ends (the host does the work with Go's compress packages), so the
 * output arrives as a single chunk.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || typeof sw.compressBytes !== 'function' || typeof global.TransformStream !== 'function' || global.CompressionStream) return;

  var FORMATS = { gzip: 1, deflate: 1, 'deflate-raw': 1 };
  function b64(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i += 0x6000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x6000));
    return global.btoa(s);
  }
  function unb64(s) { var bin = global.atob(s), u = new Uint8Array(bin.length); for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i); return u; }
  function bytesOf(chunk, who) {
    if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
    if (ArrayBuffer.isView(chunk)) return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
    throw new TypeError("Failed to execute 'write' on '" + who + "': The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
  }

  function make(name, mode) {
    function C(format) {
      if (!(this instanceof C)) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
      if (arguments.length < 1) throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
      format = String(format);
      if (!FORMATS[format]) throw new TypeError("Failed to construct '" + name + "': Unsupported compression format: '" + format + "'");
      var parts = [], total = 0;
      var ts = new global.TransformStream({
        transform: function (chunk) { var u = bytesOf(chunk, name); parts.push(u.slice()); total += u.length; },
        flush: function (controller) {
          var all = new Uint8Array(total), o = 0;
          parts.forEach(function (p) { all.set(p, o); o += p.length; });
          var out;
          try { out = unb64(sw.compressBytes(format, mode, b64(all))); } catch (e) { throw new TypeError(mode === 'd' ? 'The compressed data is not valid ' + format + ' data.' : String(e && e.message || e)); }
          if (out.length) controller.enqueue(out);
        }
      });
      Object.defineProperty(this, 'readable', { value: ts.readable, enumerable: true });
      Object.defineProperty(this, 'writable', { value: ts.writable, enumerable: true });
    }
    Object.defineProperty(C, 'name', { value: name });
    Object.defineProperty(C.prototype, Symbol.toStringTag, { value: name, configurable: true });
    global[name] = C;
  }
  make('CompressionStream', 'c');
  make('DecompressionStream', 'd');
})(globalThis);
