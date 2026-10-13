// CompressionStream, DecompressionStream, TextEncoderStream, TextDecoderStream.
H.load('streams');
var sw = H.sw;
var $ = Symbol('codec');

// QuickJS has native base64 for typed arrays; the btoa/atob loops are the fallback.
var NATIVE = typeof Uint8Array.fromBase64 === 'function' && typeof Uint8Array.prototype.toBase64 === 'function';
function toB64(u8) {
  if (NATIVE) return u8.toBase64();
  var s = '';
  for (var i = 0; i < u8.length; i += 0x6000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x6000));
  return btoa(s);
}
function fromB64(b) {
  if (NATIVE) return Uint8Array.fromBase64(b);
  var s = atob(b), u = new Uint8Array(s.length);
  for (var i = 0; i < s.length; i++) u[i] = s.charCodeAt(i);
  return u;
}
function bytesOf(chunk) {
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  if (ArrayBuffer.isView(chunk)) return new Uint8Array(chunk.buffer, chunk.byteOffset, chunk.byteLength);
  throw new TypeError('The provided value is not of type BufferSource');
}
function hostCall(fn) {
  try { return fn(); } catch (e) { throw H.hostErr(e); }
}

// A TransformStream-shaped pair driven by the Go codec. The readable side has a
// high-water mark of 0: output beyond the first window is only pulled from Go
// while the consumer is reading, and the write promise stays pending meanwhile,
// so back-pressure reaches the producer of the compressed bytes.
function codecPair(format, decompress) {
  var id = hostCall(function () { return sw.zNew(format, decompress); });
  var rctrl, wctrl, waiter = null, finished = false;
  function wake() { if (waiter) { var w = waiter; waiter = null; w(); } }
  function release() { if (!finished) { finished = true; try { sw.zClose(id); } catch (e) { } } }
  function fail(e) { release(); try { rctrl.error(e); } catch (x) { } wake(); }
  async function emit(r) {
    for (;;) {
      if (r.err) throw new TypeError(r.err);
      if (r.d) { var u = fromB64(r.d); if (u.length) rctrl.enqueue(u); }
      if (!r.more) return;
      if (rctrl.desiredSize !== null && rctrl.desiredSize <= 0) await new Promise(function (res) { waiter = res; });
      if (finished) return;
      r = sw.zPull(id);
    }
  }
  var readable = new g.ReadableStream({
    start: function (c) { rctrl = c; },
    pull: wake,
    cancel: function (reason) { release(); wake(); try { wctrl.error(reason); } catch (e) { } }
  }, { highWaterMark: 0 });
  var writable = new g.WritableStream({
    start: function (c) { wctrl = c; },
    write: function (chunk) {
      var u8 = bytesOf(chunk);
      return emit(sw.zPush(id, toB64(u8))).catch(function (e) { fail(e); throw e; });
    },
    close: function () {
      return emit(sw.zEnd(id)).then(function () { release(); rctrl.close(); }, function (e) { fail(e); throw e; });
    },
    abort: function (reason) { fail(reason); }
  }, { highWaterMark: 1 });
  return { readable: readable, writable: writable };
}

function makeCodec(Name, decompress, formats) {
  var C = {
    [Name]: function (format) {
      if (!new.target) throw new TypeError("Failed to construct '" + Name + "': Please use the 'new' operator");
      if (arguments.length < 1) throw new TypeError("Failed to construct '" + Name + "': 1 argument required, but only 0 present.");
      format = String(format);
      if (formats.indexOf(format) < 0) throw new TypeError("Failed to construct '" + Name + "': Unsupported compression format: '" + format + "'");
      Object.defineProperty(this, $, { value: codecPair(format, decompress) });
    }
  }[Name];
  function slot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
  H.attrs(C.prototype, {
    readable: function () { return slot(this).readable; },
    writable: function () { return slot(this).writable; }
  });
  H.tag(C, Name);
  H.glob(Name, C);
}
makeCodec('CompressionStream', false, ['gzip', 'deflate', 'deflate-raw']);
makeCodec('DecompressionStream', true, ['gzip', 'deflate', 'deflate-raw', 'brotli']);

// ---- UTF-8 (WHATWG decoder, incremental) ------------------------------------------------
function Utf8Decoder(fatal, ignoreBOM) {
  this.fatal = fatal; this.ignoreBOM = ignoreBOM;
  this.reset();
}
Utf8Decoder.prototype.reset = function () {
  this.cp = 0; this.needed = 0; this.seen = 0; this.lower = 0x80; this.upper = 0xBF; this.bomDone = false;
};
Utf8Decoder.prototype.error = function () { if (this.fatal) throw new TypeError('The encoded data was not valid for encoding utf-8'); return '�'; };
Utf8Decoder.prototype.push = function (u8, stream) {
  var out = [], i = 0, str = '';
  function emit(d, cp) {
    if (!d.bomDone) { d.bomDone = true; if (cp === 0xFEFF && !d.ignoreBOM) return; }
    str += cp < 0x10000 ? String.fromCharCode(cp) : String.fromCodePoint(cp);
    if (str.length > 8192) { out.push(str); str = ''; }
  }
  while (i < u8.length) {
    var b = u8[i];
    if (this.needed === 0) {
      i++;
      if (b <= 0x7F) emit(this, b);
      else if (b >= 0xC2 && b <= 0xDF) { this.needed = 1; this.cp = b & 0x1F; }
      else if (b >= 0xE0 && b <= 0xEF) { if (b === 0xE0) this.lower = 0xA0; if (b === 0xED) this.upper = 0x9F; this.needed = 2; this.cp = b & 0xF; }
      else if (b >= 0xF0 && b <= 0xF4) { if (b === 0xF0) this.lower = 0x90; if (b === 0xF4) this.upper = 0x8F; this.needed = 3; this.cp = b & 7; }
      else { this.bomDone = true; str += this.error(); }
    } else if (b < this.lower || b > this.upper) {
      this.cp = this.needed = this.seen = 0; this.lower = 0x80; this.upper = 0xBF;
      this.bomDone = true; str += this.error();
      // the offending byte is processed again as a fresh start
    } else {
      i++;
      this.lower = 0x80; this.upper = 0xBF;
      this.cp = (this.cp << 6) | (b & 0x3F);
      if (++this.seen === this.needed) { var cp = this.cp; this.cp = this.needed = this.seen = 0; emit(this, cp); }
    }
  }
  if (!stream) {
    if (this.needed !== 0) { this.cp = this.needed = this.seen = 0; this.lower = 0x80; this.upper = 0xBF; str += this.error(); }
    this.bomDone = false;
  }
  out.push(str);
  return out.join('');
};

// Single-byte legacy labels the prelude's TextDecoder also accepts.
var W1252 = '€\u0081‚ƒ„…†‡ˆ‰Š‹Œ\u008DŽ\u008F\u0090‘’“”•–—˜™š›œ\u009DžŸ';
function Latin1Decoder(win) { this.win = win; }
Latin1Decoder.prototype.push = function (u8) {
  var s = '';
  for (var i = 0; i < u8.length; i++) { var b = u8[i]; s += this.win && b >= 0x80 && b < 0xA0 ? W1252[b - 0x80] : String.fromCharCode(b); }
  return s;
};
function labelEncoding(label) {
  var l = String(label).trim().toLowerCase();
  if (l === 'utf-8' || l === 'utf8' || l === 'unicode-1-1-utf-8') return 'utf-8';
  if (l === 'latin1' || l === 'iso-8859-1' || l === 'l1' || l === 'ascii' || l === 'us-ascii') return 'windows-1252';
  if (l === 'windows-1252' || l === 'cp1252') return 'windows-1252';
  return null;
}

function TextDecoderStream(label, options) {
  if (!new.target) throw new TypeError("Failed to construct 'TextDecoderStream': Please use the 'new' operator");
  var enc = labelEncoding(label === undefined ? 'utf-8' : label);
  if (!enc) throw new RangeError("Failed to construct 'TextDecoderStream': The encoding label provided ('" + label + "') is invalid.");
  var fatal = !!(options && options.fatal), ignoreBOM = !!(options && options.ignoreBOM);
  var dec = enc === 'utf-8' ? new Utf8Decoder(fatal, ignoreBOM) : new Latin1Decoder(true);
  var ts = new g.TransformStream({
    transform: function (chunk, c) {
      var s = dec.push(bytesOf(chunk), true);
      if (s) c.enqueue(s);
    },
    flush: function (c) {
      var s = dec.push(new Uint8Array(0), false);
      if (s) c.enqueue(s);
    }
  });
  Object.defineProperty(this, $, { value: { ts: ts, enc: enc, fatal: fatal, ignoreBOM: ignoreBOM } });
}
function dslot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
H.attrs(TextDecoderStream.prototype, {
  encoding: function () { return dslot(this).enc; },
  fatal: function () { return dslot(this).fatal; },
  ignoreBOM: function () { return dslot(this).ignoreBOM; },
  readable: function () { return dslot(this).ts.readable; },
  writable: function () { return dslot(this).ts.writable; }
});
H.tag(TextDecoderStream, 'TextDecoderStream');
H.glob('TextDecoderStream', TextDecoderStream);

function TextEncoderStream() {
  if (!new.target) throw new TypeError("Failed to construct 'TextEncoderStream': Please use the 'new' operator");
  var enc = new g.TextEncoder(), lead = '';
  var ts = new g.TransformStream({
    transform: function (chunk, c) {
      var s = lead + String(chunk);
      lead = '';
      if (s.length) {
        var last = s.charCodeAt(s.length - 1);
        if (last >= 0xD800 && last <= 0xDBFF) { lead = s.charAt(s.length - 1); s = s.slice(0, -1); }
      }
      if (s.length) c.enqueue(enc.encode(s));
    },
    flush: function (c) { if (lead) { lead = ''; c.enqueue(new Uint8Array([0xEF, 0xBF, 0xBD])); } }
  });
  Object.defineProperty(this, $, { value: { ts: ts } });
}
H.attrs(TextEncoderStream.prototype, {
  encoding: function () { dslot(this); return 'utf-8'; },
  readable: function () { return dslot(this).ts.readable; },
  writable: function () { return dslot(this).ts.writable; }
});
H.tag(TextEncoderStream, 'TextEncoderStream');
H.glob('TextEncoderStream', TextEncoderStream);
