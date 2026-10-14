// WebCodecs: VideoDecoder, VideoEncoder, AudioDecoder, AudioEncoder, VideoFrame, AudioData, EncodedVideoChunk,
// EncodedAudioChunk, VideoColorSpace, ImageDecoder (+ ImageTrack, ImageTrackList). The codecs and the pixel/sample
// storage are Go (webcodecs.go, webcodecs_data.go); this file is the API surface and the control flow around it.
// Free variables: g (globalThis), H (platform helpers).
var sw = H.sw;
var N = {};
['fromBuf', 'copy', 'audioNew', 'audioCopy', 'decode', 'take', 'imageOpen'].forEach(function (k) {
  N[k] = g['__swWc_' + k];
  try { delete g['__swWc_' + k]; } catch (e) { /* ignore */ }
});
if (typeof N.fromBuf !== 'function' || typeof sw.wcNew !== 'function') return;

function ex(name, msg) { return new g.DOMException(msg, name); }
function host(fn) { try { return fn(); } catch (e) { throw H.hostErr(e); } }
// natives report failures as "!Name: message"
function nat(r) {
  if (typeof r === 'string' && r.charCodeAt(0) === 33) {
    var m = /^!(\w+): ([\s\S]*)$/.exec(r);
    if (m[1] === 'TypeError') throw new TypeError(m[2]);
    if (m[1] === 'RangeError') throw new RangeError(m[2]);
    throw ex(m[1], m[2]);
  }
  return r;
}
function J(r) { return JSON.parse(nat(r)); }
function hide(o, props) { for (var k in props) Object.defineProperty(o, k, { value: props[k], writable: true, configurable: true, enumerable: false }); }
function tag(C, n) { H.tag(C, n); }
function isObj(v) { return v !== null && (typeof v === 'object' || typeof v === 'function'); }

// ---- WebIDL-ish helpers -------------------------------------------------------------------------------------
function requireArgs(n, args, who) {
  if (args.length < n) throw new TypeError(who + ': ' + n + ' argument' + (n > 1 ? 's' : '') + ' required, but only ' + args.length + ' present.');
}
function dict(v, who, dictName) {
  if (v === undefined || v === null) return {};
  if (!isObj(v)) throw new TypeError(who + ": The provided value is not of type '" + dictName + "'.");
  return v;
}
function req(d, name, who, dictName) {
  if (d[name] === undefined) throw new TypeError(who + ": Failed to read the '" + name + "' property from '" + dictName + "': Required member is undefined.");
  return d[name];
}
function toU64(v, name, who, dictName) {
  var n = Number(v);
  if (!isFinite(n) || n < 0) throw new TypeError(who + ": Failed to read the '" + name + "' property from '" + dictName + "': Value is outside the 'unsigned long' value range.");
  return Math.trunc(n);
}
function toI64(v, name, who, dictName) {
  var n = Number(v);
  if (!isFinite(n)) throw new TypeError(who + ": Failed to read the '" + name + "' property from '" + dictName + "': The provided double value is non-finite.");
  return Math.trunc(n);
}
function bufferSource(v, who) {
  if (v instanceof ArrayBuffer || (g.__swSAB && v instanceof g.__swSAB)) return { buf: v, off: 0, len: v.byteLength };
  if (ArrayBuffer.isView(v)) return { buf: v.buffer, off: v.byteOffset, len: v.byteLength };
  throw new TypeError(who + ": The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
}
function b64(src) {
  if (src === undefined || src === null) return '';
  var b = bufferSource(src, 'description');
  return new Uint8Array(b.buf, b.off, b.len).toBase64();
}
function bytesOfBuf(buf) { return new Uint8Array(buf); }
function rectOf(x, y, w, h) { return typeof g.DOMRectReadOnly === 'function' ? new g.DOMRectReadOnly(x, y, w, h) : Object.freeze({ x: x, y: y, width: w, height: h, top: y, left: x, right: x + w, bottom: y + h }); }

var registry = typeof g.FinalizationRegistry === 'function' ? new g.FinalizationRegistry(function (h) { try { sw.wcRelease(h); } catch (e) { /* the VM is going */ } }) : null;

// ---- VideoColorSpace -----------------------------------------------------------------------------------------
var PRIM = ['bt709', 'bt470bg', 'smpte170m', 'bt2020', 'smpte432'], TRC = ['bt709', 'smpte170m', 'iec61966-2-1', 'linear', 'pq', 'hlg'], MTX = ['rgb', 'bt709', 'bt470bg', 'smpte170m', 'bt2020-ncl'];
function oneOf(v, list, name, who) {
  if (v === undefined || v === null) return null;
  v = String(v);
  if (list.indexOf(v) < 0) throw new TypeError(who + ": Failed to read the '" + name + "' property from 'VideoColorSpaceInit': The provided value '" + v + "' is not a valid enum value of type VideoColorPrimaries.");
  return v;
}
class VideoColorSpace {
  constructor(init) {
    var who = "Failed to construct 'VideoColorSpace'";
    init = dict(init, who, 'VideoColorSpaceInit');
    hide(this, {
      _p: oneOf(init.primaries, PRIM, 'primaries', who), _t: oneOf(init.transfer, TRC, 'transfer', who),
      _m: oneOf(init.matrix, MTX, 'matrix', who), _f: init.fullRange === undefined || init.fullRange === null ? null : !!init.fullRange
    });
  }
  get primaries() { return this._p; }
  get transfer() { return this._t; }
  get matrix() { return this._m; }
  get fullRange() { return this._f; }
  toJSON() { return { primaries: this._p, transfer: this._t, matrix: this._m, fullRange: this._f }; }
}
tag(VideoColorSpace, 'VideoColorSpace');

// ---- encoded chunks -------------------------------------------------------------------------------------------
function chunkClass(name, types) {
  var C = function (init) {
    if (!(this instanceof C)) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    var who = "Failed to construct '" + name + "'";
    requireArgs(1, arguments, who);
    init = dict(init, who, name + 'Init');
    var type = req(init, 'type', who, name + 'Init');
    if (type !== 'key' && type !== 'delta') throw new TypeError(who + ": Failed to read the 'type' property from '" + name + "Init': The provided value '" + type + "' is not a valid enum value of type EncodedVideoChunkType.");
    var ts = toI64(req(init, 'timestamp', who, name + 'Init'), 'timestamp', who, name + 'Init');
    var dur = init.duration === undefined || init.duration === null ? null : toU64(init.duration, 'duration', who, name + 'Init');
    var src = bufferSource(req(init, 'data', who, name + 'Init'), who);
    hide(this, { _type: type, _ts: ts, _dur: dur, _u8: new Uint8Array(src.buf.slice(src.off, src.off + src.len)) });
  };
  Object.defineProperty(C, 'name', { value: name, configurable: true });
  Object.defineProperties(C.prototype, {
    type: { get: function () { return this._type; }, enumerable: true, configurable: true },
    timestamp: { get: function () { return this._ts; }, enumerable: true, configurable: true },
    duration: { get: function () { return this._dur; }, enumerable: true, configurable: true },
    byteLength: { get: function () { return this._u8.byteLength; }, enumerable: true, configurable: true }
  });
  H.define(C.prototype, 'copyTo', function copyTo(dest) {
    var who = "Failed to execute 'copyTo' on '" + name + "'";
    requireArgs(1, arguments, who);
    var d = bufferSource(dest, who);
    if (d.len < this._u8.byteLength) throw new TypeError(who + ': destination is not large enough.');
    new Uint8Array(d.buf, d.off, d.len).set(this._u8);
  });
  tag(C, name);
  H.glob(name, C);
  return C;
}
var EncodedVideoChunk = chunkClass('EncodedVideoChunk'), EncodedAudioChunk = chunkClass('EncodedAudioChunk');

// ---- VideoFrame ---------------------------------------------------------------------------------------------
var FORMATS = ['I420', 'I420A', 'I422', 'I444', 'NV12', 'RGBA', 'RGBX', 'BGRA', 'BGRX'];
function parseRect(r, who, nm) {
  if (r === undefined || r === null) return null;
  r = dict(r, who, 'DOMRectInit');
  var x = toI64(r.x === undefined ? 0 : r.x, 'x', who, 'DOMRectInit'), y = toI64(r.y === undefined ? 0 : r.y, 'y', who, 'DOMRectInit');
  var w = toI64(req(r, 'width', who, 'DOMRectInit'), 'width', who, 'DOMRectInit'), h = toI64(req(r, 'height', who, 'DOMRectInit'), 'height', who, 'DOMRectInit');
  return { x: x, y: y, w: w, h: h };
}
function parseColorSpace(cs) {
  if (cs === undefined || cs === null) return null;
  return new VideoColorSpace(cs);
}
function matrixName(cs) { return cs && cs.matrix ? cs.matrix : ''; }

function setupFrame(f, info, ts, dur, dw, dh, cs) {
  hide(f, { _h: info.h, _i: info, _closed: false, _ts: ts, _dur: dur, _dw: dw, _dh: dh, _cs: cs, _isVF: true });
  if (registry) registry.register(f, info.h, f);
}
function colorSpaceOf(info, given) {
  if (given) return given;
  var rgb = /^(RGBA|RGBX|BGRA|BGRX)$/.test(info.f);
  return new VideoColorSpace(rgb ? { primaries: 'bt709', transfer: 'iec61966-2-1', matrix: 'rgb', fullRange: true } :
    { primaries: info.m === 'bt709' ? 'bt709' : 'smpte170m', transfer: info.m === 'bt709' ? 'bt709' : 'smpte170m', matrix: info.m || 'smpte170m', fullRange: !!info.fr });
}
// a frame made from a Go-side payload (decoder output, canvas, adopted from a message)
function adoptFrame(info, ts, dur, dw, dh, cs) {
  var f = Object.create(VideoFrame.prototype);
  setupFrame(f, info, ts, dur, dw || info.vw, dh || info.vh, colorSpaceOf(info, cs));
  return f;
}
function isCanvasSource(x) {
  return (typeof g.HTMLCanvasElement === 'function' && x instanceof g.HTMLCanvasElement) ||
    (typeof g.OffscreenCanvas === 'function' && x instanceof g.OffscreenCanvas) ||
    (x && x._isBitmap) ||
    (typeof g.HTMLImageElement === 'function' && x instanceof g.HTMLImageElement) ||
    (typeof g.HTMLVideoElement === 'function' && x instanceof g.HTMLVideoElement);
}

class VideoFrame {
  constructor(source, init) {
    var who = "Failed to construct 'VideoFrame'";
    requireArgs(1, arguments, who);
    var info, ts, dur = null, dw, dh, cs = null;
    if (source instanceof VideoFrame) {
      if (source._closed) throw ex('InvalidStateError', who + ": Cannot construct a VideoFrame from a closed VideoFrame.");
      init = dict(init, who, 'VideoFrameInit');
      var rect = parseRect(init.visibleRect, who);
      var vr = source._i;
      if (rect) {
        if (rect.w <= 0 || rect.h <= 0 || rect.x < 0 || rect.y < 0 || rect.x + rect.w > vr.cw || rect.y + rect.h > vr.ch) throw new RangeError(who + ': visibleRect is not contained within the coded size.');
        info = host(function () { return JSON.parse(sw.wcClone(source._h, rect.x, rect.y, rect.w, rect.h)); });
      } else info = host(function () { return JSON.parse(sw.wcClone(source._h)); });
      ts = init.timestamp !== undefined ? toI64(init.timestamp, 'timestamp', who, 'VideoFrameInit') : source._ts;
      dur = init.duration !== undefined && init.duration !== null ? toU64(init.duration, 'duration', who, 'VideoFrameInit') : (init.duration === undefined ? source._dur : null);
      dw = init.displayWidth !== undefined ? toU64(init.displayWidth, 'displayWidth', who, 'VideoFrameInit') : info.vw;
      dh = init.displayHeight !== undefined ? toU64(init.displayHeight, 'displayHeight', who, 'VideoFrameInit') : info.vh;
      cs = source._cs;
    } else if (isCanvasSource(source)) {
      init = dict(init, who, 'VideoFrameInit');
      ts = toI64(req(init, 'timestamp', who, 'VideoFrameInit'), 'timestamp', who, 'VideoFrameInit');
      if (init.duration !== undefined && init.duration !== null) dur = toU64(init.duration, 'duration', who, 'VideoFrameInit');
      var r = typeof g.__swCvSource === 'function' ? g.__swCvSource(source, 'VideoFrame') : null;
      var s = r && typeof sw.cvToFrame === 'function' ? host(function () { return sw.cvToFrame(r[0], r[1]); }) : null;
      if (!s) throw ex('InvalidStateError', who + ': The image source is not usable (it is empty, detached or has a zero size).');
      info = JSON.parse(s);
      dw = init.displayWidth !== undefined ? toU64(init.displayWidth, 'displayWidth', who, 'VideoFrameInit') : info.vw;
      dh = init.displayHeight !== undefined ? toU64(init.displayHeight, 'displayHeight', who, 'VideoFrameInit') : info.vh;
    } else if (ArrayBuffer.isView(source) || source instanceof ArrayBuffer || (g.__swSAB && source instanceof g.__swSAB)) {
      requireArgs(2, arguments, who);
      init = dict(init, who, 'VideoFrameBufferInit');
      var format = req(init, 'format', who, 'VideoFrameBufferInit');
      if (FORMATS.indexOf(format) < 0) throw new TypeError(who + ": Failed to read the 'format' property from 'VideoFrameBufferInit': The provided value '" + format + "' is not a valid enum value of type VideoPixelFormat.");
      var cw = toU64(req(init, 'codedWidth', who, 'VideoFrameBufferInit'), 'codedWidth', who, 'VideoFrameBufferInit');
      var ch = toU64(req(init, 'codedHeight', who, 'VideoFrameBufferInit'), 'codedHeight', who, 'VideoFrameBufferInit');
      ts = toI64(req(init, 'timestamp', who, 'VideoFrameBufferInit'), 'timestamp', who, 'VideoFrameBufferInit');
      if (init.duration !== undefined && init.duration !== null) dur = toU64(init.duration, 'duration', who, 'VideoFrameBufferInit');
      if (cw === 0 || ch === 0) throw new TypeError(who + ': codedWidth and codedHeight must be nonzero.');
      var vrect = parseRect(init.visibleRect, who);
      if (vrect && (vrect.w <= 0 || vrect.h <= 0 || vrect.x < 0 || vrect.y < 0 || vrect.x + vrect.w > cw || vrect.y + vrect.h > ch)) throw new TypeError(who + ': visibleRect is not contained within the coded size.');
      cs = parseColorSpace(init.colorSpace);
      var layout = null;
      if (init.layout !== undefined) {
        layout = Array.from(init.layout).map(function (p) { return { offset: toU64(p.offset, 'offset', who, 'PlaneLayout'), stride: toU64(p.stride, 'stride', who, 'PlaneLayout') }; });
      }
      var b = bufferSource(source, who);
      info = JSON.parse(nat(N.fromBuf(b.buf, b.off, b.len, JSON.stringify({
        format: format, w: cw, h: ch, layout: layout, matrix: matrixName(cs), full: !!(cs && cs.fullRange),
        prim: cs && cs.primaries || '', trc: cs && cs.transfer || '',
        vx: vrect ? vrect.x : 0, vy: vrect ? vrect.y : 0, vw: vrect ? vrect.w : cw, vh: vrect ? vrect.h : ch
      }))));
      dw = init.displayWidth !== undefined ? toU64(init.displayWidth, 'displayWidth', who, 'VideoFrameBufferInit') : info.vw;
      dh = init.displayHeight !== undefined ? toU64(init.displayHeight, 'displayHeight', who, 'VideoFrameBufferInit') : info.vh;
    } else {
      throw new TypeError(who + ': Overload resolution failed.');
    }
    setupFrame(this, info, ts, dur, dw, dh, colorSpaceOf(info, cs));
  }
  get format() { return this._closed ? null : this._i.f; }
  get codedWidth() { return this._closed ? 0 : this._i.cw; }
  get codedHeight() { return this._closed ? 0 : this._i.ch; }
  get codedRect() { return this._closed ? null : rectOf(0, 0, this._i.cw, this._i.ch); }
  get visibleRect() { return this._closed ? null : rectOf(this._i.vx, this._i.vy, this._i.vw, this._i.vh); }
  get displayWidth() { return this._closed ? 0 : this._dw; }
  get displayHeight() { return this._closed ? 0 : this._dh; }
  get duration() { return this._closed ? null : this._dur; }
  get timestamp() { return this._closed ? 0 : this._ts; }
  get colorSpace() { return this._cs; }
  allocationSize(options) {
    var who = "Failed to execute 'allocationSize' on 'VideoFrame'";
    if (this._closed) throw ex('InvalidStateError', who + ": Cannot call 'allocationSize' on a closed VideoFrame.");
    var o = copyOpts(options, who);
    return host(function () { return JSON.parse(sw.wcAlloc(this._h, JSON.stringify(o))).size; }.bind(this));
  }
  copyTo(destination, options) {
    var who = "Failed to execute 'copyTo' on 'VideoFrame'";
    var self = this, args = arguments;
    return new Promise(function (resolve, reject) {
      try {
        requireArgs(1, args, who);
        if (self._closed) throw ex('InvalidStateError', who + ": Cannot call 'copyTo' on a closed VideoFrame.");
        var o = copyOpts(options, who), d = bufferSource(destination, who);
        resolve(J(N.copy(self._h, d.buf, d.off, d.len, JSON.stringify(o))));
      } catch (e) { reject(e); }
    });
  }
  clone() {
    if (this._closed) throw ex('InvalidStateError', "Failed to execute 'clone' on 'VideoFrame': Cannot clone a closed VideoFrame.");
    var info = host(function () { return JSON.parse(sw.wcClone(this._h)); }.bind(this));
    var f = Object.create(VideoFrame.prototype);
    setupFrame(f, info, this._ts, this._dur, this._dw, this._dh, this._cs);
    return f;
  }
  close() {
    if (this._closed) return;
    this._closed = true;
    if (registry) registry.unregister(this);
    sw.wcRelease(this._h);
  }
}
function copyOpts(options, who) {
  options = dict(options, who, 'VideoFrameCopyToOptions');
  var o = {};
  if (options.rect !== undefined) {
    var r = parseRect(options.rect, who);
    o.rect = { X: r.x, Y: r.y, W: r.w, H: r.h };
  }
  if (options.format !== undefined) o.format = String(options.format);
  if (options.layout !== undefined) o.layout = Array.from(options.layout).map(function (p) { return { offset: toU64(p.offset, 'offset', who, 'PlaneLayout'), stride: toU64(p.stride, 'stride', who, 'PlaneLayout') }; });
  return o;
}
tag(VideoFrame, 'VideoFrame');
H.glob('VideoFrame', VideoFrame);

// ---- AudioData -----------------------------------------------------------------------------------------------
var AFORMATS = ['u8', 's16', 's32', 'f32', 'u8-planar', 's16-planar', 's32-planar', 'f32-planar'];
function setupAudio(a, info, ts) {
  hide(a, { _h: info.h, _i: info, _closed: false, _ts: ts, _isAD: true });
  if (registry) registry.register(a, info.h, a);
}
function adoptAudio(info, ts) { var a = Object.create(AudioData.prototype); setupAudio(a, info, ts); return a; }
class AudioData {
  constructor(init) {
    var who = "Failed to construct 'AudioData'";
    requireArgs(1, arguments, who);
    init = dict(init, who, 'AudioDataInit');
    var format = req(init, 'format', who, 'AudioDataInit');
    if (AFORMATS.indexOf(format) < 0) throw new TypeError(who + ": Failed to read the 'format' property from 'AudioDataInit': The provided value '" + format + "' is not a valid enum value of type AudioSampleFormat.");
    var rate = Number(req(init, 'sampleRate', who, 'AudioDataInit')), nf = toU64(req(init, 'numberOfFrames', who, 'AudioDataInit'), 'numberOfFrames', who, 'AudioDataInit');
    var ch = toU64(req(init, 'numberOfChannels', who, 'AudioDataInit'), 'numberOfChannels', who, 'AudioDataInit');
    var ts = toI64(req(init, 'timestamp', who, 'AudioDataInit'), 'timestamp', who, 'AudioDataInit');
    var data = req(init, 'data', who, 'AudioDataInit');
    if (!(rate > 0) || nf === 0 || ch === 0) throw new TypeError(who + ': sampleRate, numberOfFrames and numberOfChannels must be nonzero.');
    var b = bufferSource(data, who);
    var info = J(N.audioNew(b.buf, b.off, b.len, JSON.stringify({ format: format, rate: rate, ch: ch, nf: nf })));
    setupAudio(this, info, ts);
  }
  get format() { return this._closed ? null : this._i.f; }
  get sampleRate() { return this._closed ? 0 : this._i.r; }
  get numberOfFrames() { return this._closed ? 0 : this._i.n; }
  get numberOfChannels() { return this._closed ? 0 : this._i.c; }
  get duration() { return this._closed ? 0 : Math.round(this._i.n / this._i.r * 1e6); }
  get timestamp() { return this._closed ? 0 : this._ts; }
  allocationSize(options) {
    var who = "Failed to execute 'allocationSize' on 'AudioData'";
    if (this._closed) throw ex('InvalidStateError', who + ": Cannot call 'allocationSize' on a closed AudioData.");
    var o = audioOpts(options, who, true);
    return host(function () { return sw.wcAudioAlloc(this._h, JSON.stringify(o)); }.bind(this));
  }
  copyTo(destination, options) {
    var who = "Failed to execute 'copyTo' on 'AudioData'";
    requireArgs(2, arguments, who);
    if (this._closed) throw ex('InvalidStateError', who + ": Cannot call 'copyTo' on a closed AudioData.");
    var o = audioOpts(options, who, true), d = bufferSource(destination, who);
    nat(N.audioCopy(this._h, d.buf, d.off, d.len, JSON.stringify(o)));
  }
  clone() {
    if (this._closed) throw ex('InvalidStateError', "Failed to execute 'clone' on 'AudioData': Cannot clone a closed AudioData.");
    var info = host(function () { return JSON.parse(sw.wcClone(this._h)); }.bind(this));
    var a = Object.create(AudioData.prototype);
    setupAudio(a, info, this._ts);
    return a;
  }
  close() {
    if (this._closed) return;
    this._closed = true;
    if (registry) registry.unregister(this);
    sw.wcRelease(this._h);
  }
}
function audioOpts(options, who, planeRequired) {
  options = dict(options, who, 'AudioDataCopyToOptions');
  if (planeRequired && options.planeIndex === undefined) throw new TypeError(who + ": Failed to read the 'planeIndex' property from 'AudioDataCopyToOptions': Required member is undefined.");
  var o = { planeIndex: options.planeIndex === undefined ? 0 : toU64(options.planeIndex, 'planeIndex', who, 'AudioDataCopyToOptions'), frameOffset: options.frameOffset === undefined ? 0 : toU64(options.frameOffset, 'frameOffset', who, 'AudioDataCopyToOptions'), frameCount: options.frameCount === undefined ? -1 : toU64(options.frameCount, 'frameCount', who, 'AudioDataCopyToOptions') };
  if (options.format !== undefined) {
    if (AFORMATS.indexOf(options.format) < 0) throw new TypeError(who + ": Failed to read the 'format' property from 'AudioDataCopyToOptions': The provided value '" + options.format + "' is not a valid enum value of type AudioSampleFormat.");
    o.format = options.format;
  }
  return o;
}
tag(AudioData, 'AudioData');
H.glob('AudioData', AudioData);

// ---- codecs ---------------------------------------------------------------------------------------------------
var live = new Map(); // Go codec id -> JS object
var flushSeq = 0;

class CodecBase extends g.EventTarget {
  constructor(init, who, dictName) {
    super();
    init = dict(init, who, dictName);
    var out = req(init, 'output', who, dictName), err = req(init, 'error', who, dictName);
    if (typeof out !== 'function') throw new TypeError(who + ": Failed to read the 'output' property from '" + dictName + "': The provided callback is not a function.");
    if (typeof err !== 'function') throw new TypeError(who + ": Failed to read the 'error' property from '" + dictName + "': The provided callback is not a function.");
    hide(this, { _out: out, _err: err, _state: 'unconfigured', _id: 0, _queue: 0, _flushes: new Map(), _needKey: true, _cfg: null });
  }
  get state() { return this._state; }
  _name() { return this.constructor.name; }
  _drop() {
    if (this._id) { live.delete(this._id); sw.wcClose(this._id); this._id = 0; }
  }
  _abortFlushes(reason) {
    var fl = this._flushes; this._flushes = new Map();
    fl.forEach(function (p) { p.reject(reason); });
  }
  _release(newCfgJSON) {
    this._drop();
    this._abortFlushes(ex('AbortError', 'Aborted due to configure()'));
  }
  _configure(kind, cfg, who) {
    if (this._state === 'closed') throw ex('InvalidStateError', "Failed to execute 'configure' on '" + this._name() + "': Cannot call 'configure' on a closed codec.");
    this._drop();
    this._abortFlushes(ex('AbortError', 'Aborted due to configure()'));
    var self = this, id;
    try { id = host(function () { return sw.wcNew(kind, JSON.stringify(cfg)); }); }
    catch (e) {
      this._state = 'closed';
      var err = e instanceof g.DOMException ? e : ex('NotSupportedError', String(e && e.message || e));
      g.setTimeout(function () { try { self._err(err); } catch (x) { g.reportError && g.reportError(x); } }, 0);
      return;
    }
    this._id = id;
    this._state = 'configured';
    this._queue = 0;
    this._needKey = true;
    this._cfg = cfg;
    live.set(id, this);
  }
  _requireConfigured(method) {
    if (this._state === 'closed') throw ex('InvalidStateError', "Failed to execute '" + method + "' on '" + this._name() + "': Cannot call '" + method + "' on a closed codec.");
    if (this._state !== 'configured') throw ex('InvalidStateError', "Failed to execute '" + method + "' on '" + this._name() + "': Cannot call '" + method + "' on an unconfigured codec.");
  }
  flush() {
    var self = this;
    return new Promise(function (resolve, reject) {
      if (self._state !== 'configured') { reject(ex('InvalidStateError', "Failed to execute 'flush' on '" + self._name() + "': Cannot call 'flush' on " + (self._state === 'closed' ? 'a closed' : 'an unconfigured') + ' codec.')); return; }
      var f = ++flushSeq;
      self._flushes.set(f, { resolve: resolve, reject: reject });
      self._needKey = true;
      sw.wcFlush(self._id, f);
    });
  }
  reset() {
    if (this._state === 'closed') throw ex('InvalidStateError', "Failed to execute 'reset' on '" + this._name() + "': Cannot call 'reset' on a closed codec.");
    this._drop();
    this._abortFlushes(ex('AbortError', 'Aborted due to reset()'));
    this._state = 'unconfigured';
    this._queue = 0;
    this._needKey = true;
  }
  close() {
    if (this._state === 'closed') throw ex('InvalidStateError', "Failed to execute 'close' on '" + this._name() + "': Cannot call 'close' on a closed codec.");
    this._drop();
    this._abortFlushes(ex('AbortError', 'Aborted due to close()'));
    this._state = 'closed';
    this._queue = 0;
  }
  _fail(name, msg) {
    if (this._state === 'closed') return;
    var e = ex(name, msg);
    this._drop();
    this._abortFlushes(e);
    this._state = 'closed';
    this._queue = 0;
    try { this._err(e); } catch (x) { g.reportError && g.reportError(x); }
  }
  _dequeue() {
    if (this._queue > 0) this._queue--;
    var ev = new g.Event('dequeue');
    try { this.dispatchEvent(ev); } catch (x) { g.reportError && g.reportError(x); }
  }
  _event(m) {
    switch (m.t) {
      case 'err': this._fail(m.n, m.m); return;
      case 'deq': this._dequeue(); return;
      case 'fl': { var p = this._flushes.get(m.f); if (p) { this._flushes.delete(m.f); p.resolve(); } return; }
    }
    this._output(m);
  }
}
H.handlers(CodecBase.prototype, ['dequeue']);
function callOut(self, f) { try { f(); } catch (x) { g.reportError && g.reportError(x); } }

function vcfg(c, who, dictName, decoder) {
  c = dict(c, who, dictName);
  var codec = req(c, 'codec', who, dictName);
  if (typeof codec !== 'string' || codec === '') throw new TypeError(who + ": Failed to read the 'codec' property from '" + dictName + "': The provided value is empty.");
  return c;
}

class VideoDecoder extends CodecBase {
  constructor(init) { requireArgs(1, arguments, "Failed to construct 'VideoDecoder'"); super(init, "Failed to construct 'VideoDecoder'", 'VideoDecoderInit'); }
  get decodeQueueSize() { return this._queue; }
  configure(config) {
    var who = "Failed to execute 'configure' on 'VideoDecoder'";
    requireArgs(1, arguments, who);
    var c = vcfg(config, who, 'VideoDecoderConfig');
    var cfg = { codec: c.codec, desc: b64(c.description), w: c.codedWidth | 0, h: c.codedHeight | 0 };
    if ((c.codedWidth !== undefined && !(c.codedWidth > 0)) || (c.codedHeight !== undefined && !(c.codedHeight > 0))) throw new TypeError(who + ': codedWidth and codedHeight must be positive.');
    this._configure('vd', cfg, who);
  }
  decode(chunk) {
    var who = "Failed to execute 'decode' on 'VideoDecoder'";
    requireArgs(1, arguments, who);
    this._requireConfigured('decode');
    if (!(chunk instanceof EncodedVideoChunk)) throw new TypeError(who + ": Failed to convert value to 'EncodedVideoChunk'.");
    if (this._needKey) {
      if (chunk.type !== 'key') throw ex('DataError', who + ': A key frame is required after configure() or flush().');
      this._needKey = false;
    }
    this._queue++;
    var u = chunk._u8;
    N.decode(this._id, u.buffer, u.byteOffset, u.byteLength, JSON.stringify({ ts: chunk._ts, dur: chunk._dur || 0, hasDur: chunk._dur !== null }));
  }
  _output(m) {
    if (m.t !== 'vf') return;
    var f = adoptFrame(m.i, m.ts, m.hd ? m.dur : null);
    var self = this;
    callOut(this, function () { self._out(f); });
  }
  static isConfigSupported(config) {
    return supported('vd', config, "Failed to execute 'isConfigSupported' on 'VideoDecoder'", 'VideoDecoderConfig', function (c) {
      vcfg(c, "Failed to execute 'isConfigSupported' on 'VideoDecoder'", 'VideoDecoderConfig');
      return { cfg: { codec: c.codec, desc: b64(c.description), w: c.codedWidth | 0, h: c.codedHeight | 0 }, out: pick(c, ['codec', 'codedWidth', 'codedHeight', 'colorSpace', 'description', 'displayAspectWidth', 'displayAspectHeight', 'hardwareAcceleration', 'optimizeForLatency']) };
    });
  }
}
tag(VideoDecoder, 'VideoDecoder');

class VideoEncoder extends CodecBase {
  constructor(init) { requireArgs(1, arguments, "Failed to construct 'VideoEncoder'"); super(init, "Failed to construct 'VideoEncoder'", 'VideoEncoderInit'); }
  get encodeQueueSize() { return this._queue; }
  configure(config) {
    var who = "Failed to execute 'configure' on 'VideoEncoder'";
    requireArgs(1, arguments, who);
    this._configure('ve', veCfg(config, who), who);
  }
  encode(frame, options) {
    var who = "Failed to execute 'encode' on 'VideoEncoder'";
    requireArgs(1, arguments, who);
    this._requireConfigured('encode');
    if (!(frame instanceof VideoFrame)) throw new TypeError(who + ": Failed to convert value to 'VideoFrame'.");
    if (frame._closed) throw new TypeError(who + ': Cannot encode a closed VideoFrame.');
    options = dict(options, who, 'VideoEncoderEncodeOptions');
    this._queue++;
    var ok = host(function () { return sw.wcEncodeV(this._id, frame._h, JSON.stringify({ ts: frame._ts, dur: frame._dur || 0, hasDur: frame._dur !== null, key: !!options.keyFrame })); }.bind(this));
    if (!ok) this._queue--;
  }
  _output(m) {
    if (m.t !== 'ec') return;
    var chunk = new EncodedVideoChunk({ type: m.k ? 'key' : 'delta', timestamp: m.ts, duration: m.hd ? m.dur : undefined, data: new Uint8Array(N.take(m.b)) });
    var meta = {};
    if (m.dc) meta.decoderConfig = { codec: m.dc.codec, codedWidth: m.dc.cw, codedHeight: m.dc.ch, displayAspectWidth: m.dc.cw, displayAspectHeight: m.dc.ch };
    var self = this;
    callOut(this, function () { self._out(chunk, meta); });
  }
  static isConfigSupported(config) {
    return supported('ve', config, "Failed to execute 'isConfigSupported' on 'VideoEncoder'", 'VideoEncoderConfig', function (c) {
      return { cfg: veCfg(c, "Failed to execute 'isConfigSupported' on 'VideoEncoder'"), out: pick(c, ['codec', 'width', 'height', 'displayWidth', 'displayHeight', 'bitrate', 'framerate', 'hardwareAcceleration', 'alpha', 'scalabilityMode', 'bitrateMode', 'latencyMode', 'avc', 'hevc']) };
    });
  }
}
tag(VideoEncoder, 'VideoEncoder');
function veCfg(config, who) {
  var c = vcfg(config, who, 'VideoEncoderConfig');
  var w = toU64(req(c, 'width', who, 'VideoEncoderConfig'), 'width', who, 'VideoEncoderConfig'), h = toU64(req(c, 'height', who, 'VideoEncoderConfig'), 'height', who, 'VideoEncoderConfig');
  if (w === 0 || h === 0) throw new TypeError(who + ': width and height must be nonzero.');
  return { codec: c.codec, w: w, h: h, bitrate: c.bitrate === undefined ? 0 : toU64(c.bitrate, 'bitrate', who, 'VideoEncoderConfig'), fps: c.framerate === undefined ? 0 : Number(c.framerate), keyInt: 0 };
}

function acfg(c, who, dictName) {
  c = dict(c, who, dictName);
  var codec = req(c, 'codec', who, dictName);
  if (typeof codec !== 'string' || codec === '') throw new TypeError(who + ": Failed to read the 'codec' property from '" + dictName + "': The provided value is empty.");
  var rate = Number(req(c, 'sampleRate', who, dictName)), ch = toU64(req(c, 'numberOfChannels', who, dictName), 'numberOfChannels', who, dictName);
  if (!(rate > 0) || ch === 0) throw new TypeError(who + ': sampleRate and numberOfChannels must be nonzero.');
  return { c: c, rate: rate, ch: ch };
}

class AudioDecoder extends CodecBase {
  constructor(init) { requireArgs(1, arguments, "Failed to construct 'AudioDecoder'"); super(init, "Failed to construct 'AudioDecoder'", 'AudioDecoderInit'); }
  get decodeQueueSize() { return this._queue; }
  configure(config) {
    var who = "Failed to execute 'configure' on 'AudioDecoder'";
    requireArgs(1, arguments, who);
    this._configure('ad', adCfg(config, who), who);
  }
  decode(chunk) {
    var who = "Failed to execute 'decode' on 'AudioDecoder'";
    requireArgs(1, arguments, who);
    this._requireConfigured('decode');
    if (!(chunk instanceof EncodedAudioChunk)) throw new TypeError(who + ": Failed to convert value to 'EncodedAudioChunk'.");
    this._queue++;
    var u = chunk._u8;
    N.decode(this._id, u.buffer, u.byteOffset, u.byteLength, JSON.stringify({ ts: chunk._ts, dur: chunk._dur || 0, hasDur: chunk._dur !== null }));
  }
  _output(m) {
    if (m.t !== 'ad') return;
    var a = adoptAudio(m.i, m.ts);
    var self = this;
    callOut(this, function () { self._out(a); });
  }
  static isConfigSupported(config) {
    return supported('ad', config, "Failed to execute 'isConfigSupported' on 'AudioDecoder'", 'AudioDecoderConfig', function (c) {
      return { cfg: adCfg(c, "Failed to execute 'isConfigSupported' on 'AudioDecoder'"), out: pick(c, ['codec', 'sampleRate', 'numberOfChannels', 'description']) };
    });
  }
}
tag(AudioDecoder, 'AudioDecoder');
function adCfg(config, who) {
  var a = acfg(config, who, 'AudioDecoderConfig');
  return { codec: a.c.codec, desc: b64(a.c.description), rate: a.rate, ch: a.ch, adts: !!(a.c.aac && a.c.aac.format === 'adts') };
}

class AudioEncoder extends CodecBase {
  constructor(init) { requireArgs(1, arguments, "Failed to construct 'AudioEncoder'"); super(init, "Failed to construct 'AudioEncoder'", 'AudioEncoderInit'); }
  get encodeQueueSize() { return this._queue; }
  configure(config) {
    var who = "Failed to execute 'configure' on 'AudioEncoder'";
    requireArgs(1, arguments, who);
    this._configure('ae', aeCfg(config, who), who);
  }
  encode(data) {
    var who = "Failed to execute 'encode' on 'AudioEncoder'";
    requireArgs(1, arguments, who);
    this._requireConfigured('encode');
    if (!(data instanceof AudioData)) throw new TypeError(who + ": Failed to convert value to 'AudioData'.");
    if (data._closed) throw new TypeError(who + ': Cannot encode a closed AudioData.');
    this._queue++;
    var ok = host(function () { return sw.wcEncodeA(this._id, data._h, data._ts); }.bind(this));
    if (!ok) this._queue--;
  }
  _output(m) {
    if (m.t !== 'ec') return;
    var chunk = new EncodedAudioChunk({ type: 'key', timestamp: m.ts, duration: m.dur, data: new Uint8Array(N.take(m.b)) });
    var meta = {};
    if (m.dc) meta.decoderConfig = { codec: m.dc.codec, sampleRate: m.dc.rate, numberOfChannels: m.dc.ch, description: Uint8Array.fromBase64(m.dc.desc).buffer };
    var self = this;
    callOut(this, function () { self._out(chunk, meta); });
  }
  static isConfigSupported(config) {
    return supported('ae', config, "Failed to execute 'isConfigSupported' on 'AudioEncoder'", 'AudioEncoderConfig', function (c) {
      return { cfg: aeCfg(c, "Failed to execute 'isConfigSupported' on 'AudioEncoder'"), out: pick(c, ['codec', 'sampleRate', 'numberOfChannels', 'bitrate', 'bitrateMode', 'opus']) };
    });
  }
}
tag(AudioEncoder, 'AudioEncoder');
function aeCfg(config, who) {
  var a = acfg(config, who, 'AudioEncoderConfig'), c = a.c, o = c.opus || {};
  return { codec: c.codec, rate: a.rate, ch: a.ch, bitrate: c.bitrate === undefined ? 0 : toU64(c.bitrate, 'bitrate', who, 'AudioEncoderConfig'), frameUs: o.frameDuration === undefined ? 0 : toU64(o.frameDuration, 'frameDuration', who, 'OpusEncoderConfig'), app: o.application || '' };
}

function pick(o, keys) {
  var r = {};
  keys.forEach(function (k) {
    if (o[k] === undefined) return;
    if (k === 'description') { var b = bufferSource(o[k], 'description'); r[k] = b.buf.slice(b.off, b.off + b.len); }
    else r[k] = o[k];
  });
  return r;
}
function supported(kind, config, who, dictName, prep) {
  return new Promise(function (resolve, reject) {
    try {
      if (config === undefined) throw new TypeError(who + ': 1 argument required, but only 0 present.');
      dict(config, who, dictName);
      var p = prep(config);
      var ok = !!sw.wcSupported(kind, JSON.stringify(p.cfg));
      resolve({ supported: ok, config: p.out });
    } catch (e) { reject(e); }
  });
}

// events from Go
Object.defineProperty(g, '__swWcEvent', { value: function (json) {
  var m = JSON.parse(json), c = live.get(m.c);
  if (!c) {
    // the codec was closed or reset while a job was running: a frame it produced still has to be released
    if (m.i && m.i.h) { try { sw.wcRelease(m.i.h); } catch (e) { /* ignore */ } }
    if (m.b) { try { N.take(m.b); } catch (e) { /* ignore */ } }
    return;
  }
  c._event(m);
}, configurable: true });

// ---- postMessage (idb_prelude.js ser/de call these) -----------------------------------------------------
Object.defineProperty(g, '__swMediaSer', { value: function (v, share) {
  var isV = v instanceof VideoFrame, isA = v instanceof AudioData;
  if (!isV && !isA) return undefined;
  var name = isV ? 'VideoFrame' : 'AudioData';
  if (v._closed) throw ex('DataCloneError', 'Failed to execute \'postMessage\': ' + name + ' is closed and cannot be cloned.');
  if (!share) throw ex('DataCloneError', '#<' + name + '> could not be cloned.');
  var tok = host(function () { return sw.wcShare(v._h, false); });
  return isV ? { k: 'vf', t: tok, ts: v._ts, dur: v._dur, dw: v._dw, dh: v._dh, cs: v._cs ? v._cs.toJSON() : null } : { k: 'ad', t: tok, ts: v._ts };
}, configurable: true });
Object.defineProperty(g, '__swMediaDe', { value: function (d) {
  var info = host(function () { return JSON.parse(sw.wcAdopt(d.t)); });
  if (d.k === 'ad') return adoptAudio(info, d.ts);
  return adoptFrame(info, d.ts, d.dur, d.dw, d.dh, d.cs ? new VideoColorSpace(d.cs) : null);
}, configurable: true });

// ---- ImageDecoder ------------------------------------------------------------------------------------------
var IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/gif', 'image/webp', 'image/bmp', 'image/apng'];
class ImageTrack {
  constructor() { throw new TypeError('Illegal constructor'); }
  get animated() { return this._a; }
  get frameCount() { return this._n; }
  get repetitionCount() { return this._r; }
  get selected() { return true; }
  set selected(v) { /* a single track */ }
}
tag(ImageTrack, 'ImageTrack');
class ImageTrackList {
  constructor() { throw new TypeError('Illegal constructor'); }
  get ready() { return this._ready; }
  get length() { return this._tracks.length; }
  get selectedIndex() { return this._tracks.length ? 0 : -1; }
  get selectedTrack() { return this._tracks.length ? this._tracks[0] : null; }
  item(i) { return this._tracks[i]; }
}
tag(ImageTrackList, 'ImageTrackList');
function mkTrack(animated, n, rep) { var t = Object.create(ImageTrack.prototype); hide(t, { _a: animated, _n: n, _r: rep }); return t; }

class ImageDecoder {
  constructor(init) {
    var who = "Failed to construct 'ImageDecoder'";
    requireArgs(1, arguments, who);
    init = dict(init, who, 'ImageDecoderInit');
    var type = req(init, 'type', who, 'ImageDecoderInit'), data = req(init, 'data', who, 'ImageDecoderInit');
    if (typeof type !== 'string' || type === '') throw new TypeError(who + ": Failed to read the 'type' property from 'ImageDecoderInit': The provided value is empty.");
    if (IMAGE_TYPES.indexOf(type) < 0) throw ex('NotSupportedError', who + ': Unsupported image type: ' + type);
    var self = this, res, rej, ready;
    var tracks = Object.create(ImageTrackList.prototype);
    hide(tracks, { _tracks: [] });
    hide(tracks, { _ready: new Promise(function (a, b) { ready = { res: a, rej: b }; }) });
    tracks._ready.catch(function () { /* surfaced through `completed` */ });
    hide(this, { _type: type, _closed: false, _frames: [], _loops: 1, _complete: false, _tracks: tracks, _completed: null });
    this._completed = new Promise(function (a, b) { res = a; rej = b; });
    this._completed.catch(function () { /* the caller may not look */ });
    var gotBytes;
    if (data && typeof data.getReader === 'function') {
      gotBytes = readAll(data);
    } else {
      var b = bufferSource(data, who);
      gotBytes = Promise.resolve(b.buf.slice(b.off, b.off + b.len));
    }
    gotBytes.then(function (buf) {
      if (self._closed) return;
      var r = J(N.imageOpen(buf, 0, buf.byteLength, type));
      self._frames = r.frames; self._loops = r.loops ? Infinity : 1; self._mime = r.type;
      self._tracks._tracks.push(mkTrack(r.animated, r.frames.length, r.animated ? (r.loops ? Infinity : 0) : 0));
      self._complete = true;
      ready.res();
      res();
    }).catch(function (e) {
      var err = e instanceof g.DOMException || e instanceof TypeError ? e : ex('EncodingError', String(e && e.message || e));
      ready.rej(err); rej(err);
    });
  }
  get type() { return this._type; }
  get complete() { return this._complete; }
  get completed() { return this._completed; }
  get tracks() { return this._tracks; }
  decode(options) {
    var self = this;
    return new Promise(function (resolve, reject) {
      try {
        if (self._closed) throw ex('InvalidStateError', "Failed to execute 'decode' on 'ImageDecoder': The decoder is closed.");
        options = dict(options, "Failed to execute 'decode' on 'ImageDecoder'", 'ImageDecodeOptions');
        var idx = options.frameIndex === undefined ? 0 : toU64(options.frameIndex, 'frameIndex', 'decode', 'ImageDecodeOptions');
        self._completed.then(function () {
          if (self._closed) { reject(ex('AbortError', 'Aborted due to close()')); return; }
          if (idx >= self._frames.length) { reject(new RangeError("Failed to execute 'decode' on 'ImageDecoder': Frame index " + idx + ' is out of range (' + self._frames.length + ' frames).')); return; }
          var fr = self._frames[idx], ts = 0;
          for (var k = 0; k < idx; k++) ts += Math.round(self._frames[k].d * 1000);
          var info = JSON.parse(sw.wcClone(fr.i.h));
          resolve({ image: adoptFrame(info, ts, Math.round(fr.d * 1000) || null), complete: true });
        }, reject);
      } catch (e) { reject(e); }
    });
  }
  reset() { /* decoding is stateless here */ }
  close() {
    if (this._closed) return;
    this._closed = true;
    var fr = this._frames; this._frames = [];
    fr.forEach(function (f) { try { sw.wcRelease(f.i.h); } catch (e) { /* ignore */ } });
  }
  static isTypeSupported(type) { return Promise.resolve(IMAGE_TYPES.indexOf(String(type)) >= 0); }
}
tag(ImageDecoder, 'ImageDecoder');
function readAll(stream) {
  var rd = stream.getReader(), parts = [], n = 0;
  return (function pump() {
    return rd.read().then(function (r) {
      if (r.done) { var out = new Uint8Array(n), o = 0; parts.forEach(function (p) { out.set(p, o); o += p.length; }); return out.buffer; }
      var u = r.value instanceof ArrayBuffer ? new Uint8Array(r.value) : new Uint8Array(r.value.buffer, r.value.byteOffset, r.value.byteLength);
      parts.push(u); n += u.length;
      return pump();
    });
  })();
}

[VideoDecoder, VideoEncoder, AudioDecoder, AudioEncoder, ImageDecoder, ImageTrack, ImageTrackList].forEach(function (C) { H.glob(C.name, C); });
H.glob('VideoColorSpace', VideoColorSpace);

// ---- events from Go: frames and audio of the processors -----------------------------------------------------
var subs = new Map();
Object.defineProperty(g, '__swWcsEvent', { value: function (id, type, data) {
  var s = subs.get(id), d = data ? JSON.parse(data) : null;
  if (!s) { if (d && d.i && d.i.h) { try { sw.wcRelease(d.i.h); } catch (e) { /* ignore */ } } return; }
  if (type === 'video') s.onVideo(d);
  else if (type === 'audio') s.onAudio(d);
  else if (type === 'end') s.onEnd();
}, configurable: true });

// ---- MediaStreamTrackProcessor ---------------------------------------------------------------------------------------------
function sourceSpec(track) {
  var t = track._t || {};
  if (t.cap) return { cap: track.id };
  if (t.rtc) return { rtc: [t.rtc.pc, t.rtc.tr] };
  if (track.canvas && g.__swIdOf) return { canvas: g.__swIdOf(track.canvas), fps: (t.extra && t.extra.frameRate) || 30 };
  return null;
}
function MediaStreamTrackProcessor(init) {
  if (!(this instanceof MediaStreamTrackProcessor)) throw new TypeError("Failed to construct 'MediaStreamTrackProcessor': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (!init || typeof init !== 'object' || init.track === undefined) throw new TypeError("Failed to construct 'MediaStreamTrackProcessor': Failed to read the 'track' property from 'MediaStreamTrackProcessorInit': Required member is undefined.");
  var track = init.track;
  if (!g.MediaStreamTrack || !(track instanceof g.MediaStreamTrack)) throw new TypeError("Failed to construct 'MediaStreamTrackProcessor': Failed to read the 'track' property from 'MediaStreamTrackProcessorInit': The provided value is not of type 'MediaStreamTrack'.");
  if (init.maxBufferSize !== undefined && !(init.maxBufferSize >= 0)) throw new TypeError("Failed to construct 'MediaStreamTrackProcessor': maxBufferSize must be a non-negative integer.");
  if (track.readyState === 'ended') throw ex("Failed to construct 'MediaStreamTrackProcessor': The track has ended.", 'InvalidStateError');
  var st = { track: track, max: init.maxBufferSize === undefined ? (track.kind === 'video' ? 1 : 100) : Math.max(1, init.maxBufferSize >>> 0), readable: null };
  Object.defineProperty(this, '__p', { value: st });
}
H.attrs(MediaStreamTrackProcessor.prototype, {
  readable: function () {
    var st = this.__p;
    if (st.readable) return st.readable;
    var track = st.track, video = track.kind === 'video', queue = [], waiter = null, ended = false, sub = 0, ctrlRef = null;
    function drop(item) { try { item.close(); } catch (e) { /* ignore */ } }
    function wake() { if (waiter) { var w = waiter; waiter = null; w(); } }
    function offer(item) {
      queue.push(item);
      while (queue.length > st.max) drop(queue.shift());
      wake();
    }
    function start() {
      var spec = sourceSpec(track);
      if (!spec) throw ex("Failed to read from the MediaStreamTrackProcessor: this kind of track cannot be read as raw frames.", 'InvalidStateError');
      sub = sw.wcSubscribe(track.kind, JSON.stringify(spec));
      var frameCount = 0;
      subs.set(sub, {
        onVideo: function (d) {
          offer(adoptFrame(d.i, d.ts));
        },
        onAudio: function (d) { offer(adoptAudio(d.i, d.ts)); },
        onEnd: function () { ended = true; wake(); }
      });
      track.addEventListener('ended', function () { ended = true; wake(); });
    }
    var stream = new g.ReadableStream({
      pull: function (c) {
        if (!sub) start();
        return new Promise(function (resolve) {
          (function check() {
            if (queue.length) { c.enqueue(queue.shift()); resolve(); return; }
            if (ended || track.readyState === 'ended') {
              try { c.close(); } catch (e) { /* closed */ }
              resolve(); return;
            }
            waiter = check;
          })();
        });
      },
      cancel: function () {
        if (sub) { subs.delete(sub); try { sw.wcUnsubscribe(sub); } catch (e) { /* ignore */ } }
        queue.forEach(drop); queue = [];
      }
    }, { highWaterMark: 0 });
    st.readable = stream;
    return stream;
  }
});
H.tag(MediaStreamTrackProcessor, 'MediaStreamTrackProcessor');
H.glob('MediaStreamTrackProcessor', MediaStreamTrackProcessor);

// ---- generators --------------------------------------------------------------------------------------------------------------
function newGenerated(kind) {
  var info = JSON.parse(sw.wcGenNew(kind, ''));
  if (typeof g.__swCapTrack !== 'function') throw ex('track generators need media support', 'NotSupportedError');
  return g.__swCapTrack(info, {});
}
function writableFor(track, kind) {
  return new g.WritableStream({
    write: function (chunk) {
      if (track.readyState === 'ended') { if (chunk && typeof chunk.close === 'function') { try { chunk.close(); } catch (e) { /* closed */ } } return; }
      if (kind === 'video') {
        if (!(chunk instanceof VideoFrame)) throw new TypeError('The chunk is not a VideoFrame.');
        if (chunk._closed) throw ex('InvalidStateError', 'The VideoFrame is closed.');
        try { sw.wcGenVideo(track.id, chunk._h); } finally { chunk.close(); }
      } else {
        if (!(chunk instanceof AudioData)) throw new TypeError('The chunk is not an AudioData.');
        if (chunk._closed) throw ex('InvalidStateError', 'The AudioData is closed.');
        try { sw.wcGenAudio(track.id, chunk._h); } finally { chunk.close(); }
      }
    },
    close: function () { track.stop(); },
    abort: function () { track.stop(); }
  });
}
function MediaStreamTrackGenerator(init) {
  if (!(this instanceof MediaStreamTrackGenerator)) throw new TypeError("Failed to construct 'MediaStreamTrackGenerator': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (!init || typeof init !== 'object' || init.kind === undefined) throw new TypeError("Failed to construct 'MediaStreamTrackGenerator': Failed to read the 'kind' property from 'MediaStreamTrackGeneratorInit': Required member is undefined.");
  if (init.kind !== 'audio' && init.kind !== 'video') throw new TypeError("Failed to construct 'MediaStreamTrackGenerator': The provided value '" + init.kind + "' is not a valid enum value of type MediaStreamTrackGeneratorKind.");
  var track = newGenerated(init.kind);
  Object.setPrototypeOf(track, MediaStreamTrackGenerator.prototype);
  var w = null;
  Object.defineProperty(track, 'writable', { get: function () { return w || (w = writableFor(track, init.kind)); }, enumerable: true, configurable: true });
  return track;
}
MediaStreamTrackGenerator.prototype = Object.create(g.MediaStreamTrack.prototype, { constructor: { value: MediaStreamTrackGenerator, writable: true, configurable: true } });
Object.setPrototypeOf(MediaStreamTrackGenerator, g.MediaStreamTrack);
H.tag(MediaStreamTrackGenerator, 'MediaStreamTrackGenerator');
H.glob('MediaStreamTrackGenerator', MediaStreamTrackGenerator);

function VideoTrackGenerator() {
  if (!(this instanceof VideoTrackGenerator)) throw new TypeError("Failed to construct 'VideoTrackGenerator': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  var track = newGenerated('video'), w = null, muted = false;
  Object.defineProperty(this, '__t', { value: track });
  H.attrs(this, {
    writable: function () { return w || (w = writableFor(track, 'video')); },
    track: function () { return track; }
  });
  Object.defineProperty(this, 'muted', { get: function () { return muted; }, set: function (v) { muted = !!v; if (track._t) track._t.muted = muted; }, enumerable: true, configurable: true });
}
H.tag(VideoTrackGenerator, 'VideoTrackGenerator');
H.glob('VideoTrackGenerator', VideoTrackGenerator);
