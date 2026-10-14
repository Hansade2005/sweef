// MediaRecorder and BlobEvent (W3C MediaStream Recording). The encoders and containers are Go (mediarecorder.go,
// internal/mediarec); this file is the state machine, the events and the option handling.
// Free variables: g (globalThis), H (platform helpers).
var sw = H.sw;
if (typeof sw.recStart !== 'function') return;

function ex(name, msg) { return new g.DOMException(msg, name); }
function host(fn) { try { return fn(); } catch (e) { throw H.hostErr(e); } }
var BlobEvent = H.eventClass('BlobEvent', { data: null, timecode: 0 }, g.Event, 'data');

var live = new Map(); // Go recording id -> MediaRecorder

function takeBytes(tok) {
  try { void g.VideoEncoder; } catch (e) { /* no WebCodecs natives in this VM: fall back to base64 */ }
  if (typeof H.wcTake === 'function') { var ab = H.wcTake(tok); if (ab) return new Uint8Array(ab); }
  var bin = g.atob(sw.recTake(tok)), u = new Uint8Array(bin.length);
  for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i);
  return u;
}

function uint(v) { v = Number(v); return v > 0 && isFinite(v) ? Math.floor(v) : 0; }

function dictOf(options, who) {
  if (options === undefined || options === null) return {};
  if (typeof options !== 'object' && typeof options !== 'function') throw new TypeError(who + ": The provided value is not of type 'MediaRecorderOptions'.");
  return options;
}

class MediaRecorder extends g.EventTarget {
  constructor(stream, options) {
    var who = "Failed to construct 'MediaRecorder'";
    if (arguments.length < 1) throw new TypeError(who + ': 1 argument required, but only 0 present.');
    if (!(stream instanceof g.MediaStream)) throw new TypeError(who + ": Failed to convert value to 'MediaStream'.");
    super();
    var o = dictOf(options, who), mime = o.mimeType === undefined ? '' : String(o.mimeType);
    if (mime !== '' && !sw.recMime(mime)) throw ex('NotSupportedError', who + ": Unsupported MIME type '" + mime + "'.");
    var mode = o.audioBitrateMode === undefined ? 'variable' : String(o.audioBitrateMode);
    if (mode !== 'constant' && mode !== 'variable') throw new TypeError(who + ": Failed to read the 'audioBitrateMode' property from 'MediaRecorderOptions': The provided value '" + mode + "' is not a valid enum value of type BitrateMode.");
    var tr = stream.getTracks(), hasA = tr.some(function (t) { return t.kind === 'audio'; }), hasV = tr.some(function (t) { return t.kind === 'video'; });
    var abps = uint(o.audioBitsPerSecond), vbps = uint(o.videoBitsPerSecond), bps = uint(o.bitsPerSecond);
    if (bps && !abps && !vbps) {
      if (hasA && hasV) { abps = Math.min(128000, Math.floor(bps / 4)); vbps = bps - abps; } else if (hasA) abps = bps; else vbps = bps;
    }
    if (!abps && hasA) abps = 128000;
    if (!vbps && hasV) vbps = 2500000;
    Object.defineProperty(this, '_r', { value: { stream: stream, mime: mime, want: mime, state: 'inactive', abps: abps, vbps: vbps, mode: mode, id: 0, off: null, watch: null }, configurable: true });
  }
  get stream() { return this._r.stream; }
  get mimeType() { return this._r.mime; }
  get state() { return this._r.state; }
  get audioBitsPerSecond() { return this._r.abps; }
  get videoBitsPerSecond() { return this._r.vbps; }
  get audioBitrateMode() { return this._r.mode; }

  start(timeslice) {
    var r = this._r, self = this, who = "Failed to execute 'start' on 'MediaRecorder'";
    if (r.state !== 'inactive') throw ex('InvalidStateError', who + ": The MediaRecorder's state is '" + r.state + "'.");
    var slice = timeslice === undefined ? 0 : uint(timeslice);
    r.state = 'recording';
    var tracks = r.stream.getTracks().filter(function (t) { return t.readyState === 'live'; });
    var descs = tracks.map(function (t) {
      var st = {};
      try { st = t.getSettings() || {}; } catch (e) { st = {}; }
      return { id: t.id, kind: t.kind, canvas: t.canvas && g.__swIdOf ? g.__swIdOf(t.canvas) : 0, w: st.width | 0, h: st.height | 0, fps: +st.frameRate || 0 };
    });
    var res = JSON.parse(host(function () {
      return sw.recStart(JSON.stringify({ mime: r.want, abps: r.abps, vbps: r.vbps, cbr: r.mode === 'constant', slice: slice, tracks: descs }));
    }));
    if (res.err) {
      g.setTimeout(function () { fail(self, res.name, res.err); stopEvent(self); }, 0);
      return;
    }
    r.id = res.id;
    if (!r.want) r.mime = res.mime;
    live.set(res.id, this);
    // end of recording when every track ended, and an error when the track set changes
    var left = tracks.length, ended = new Set();
    function trackEnded(t) {
      if (ended.has(t)) return;
      ended.add(t);
      if (r.id) { try { sw.recEnd(r.id, t.id); } catch (e) { /* the recording is gone */ } }
      if (--left <= 0 && r.state !== 'inactive') self.stop();
    }
    tracks.forEach(function (t) { g.__swTrackWatch(t, trackEnded); if (g.__swWaPull) g.__swWaPull(t, true); });
    r.pulled = tracks;
    var changed = function () { if (r.state !== 'inactive') { self.stop(); fail(self, 'InvalidModificationError', 'Tracks in MediaStream were added or removed.'); } };
    r.stream.addEventListener('addtrack', changed);
    r.stream.addEventListener('removetrack', changed);
    r.off = function () { r.stream.removeEventListener('addtrack', changed); r.stream.removeEventListener('removetrack', changed); };
    later(self, 'start');
  }

  stop() {
    var r = this._r;
    if (r.state === 'inactive') return;
    r.state = 'inactive';
    release(r);
    if (r.id) sw.recCtl(r.id, 'stop');
  }

  pause() {
    var r = this._r, self = this;
    if (r.state === 'inactive') throw ex('InvalidStateError', "Failed to execute 'pause' on 'MediaRecorder': The MediaRecorder's state is 'inactive'.");
    if (r.state === 'paused') return;
    r.state = 'paused';
    sw.recCtl(r.id, 'pause');
    later(self, 'pause');
  }

  resume() {
    var r = this._r, self = this;
    if (r.state === 'inactive') throw ex('InvalidStateError', "Failed to execute 'resume' on 'MediaRecorder': The MediaRecorder's state is 'inactive'.");
    if (r.state === 'recording') return;
    r.state = 'recording';
    sw.recCtl(r.id, 'resume');
    later(self, 'resume');
  }

  requestData() {
    var r = this._r;
    if (r.state === 'inactive') throw ex('InvalidStateError', "Failed to execute 'requestData' on 'MediaRecorder': The MediaRecorder's state is 'inactive'.");
    sw.recCtl(r.id, 'data');
  }

  static isTypeSupported(type) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'isTypeSupported' on 'MediaRecorder': 1 argument required, but only 0 present.");
    return !!sw.recMime(String(type));
  }
}
H.tag(MediaRecorder, 'MediaRecorder');
H.handlers(MediaRecorder.prototype, ['start', 'stop', 'pause', 'resume', 'error', 'dataavailable']);
H.glob('MediaRecorder', MediaRecorder);

// start, pause and resume events are tasks of their own; host events (data, stop) must never overtake them
function later(rec, type) {
  var r = rec._r;
  (r.pend || (r.pend = [])).push(type);
  g.setTimeout(function () { flush(rec); }, 0);
}

function flush(rec) {
  var r = rec._r, q = r.pend;
  if (!q || !q.length) return;
  r.pend = [];
  q.forEach(function (type) { rec.dispatchEvent(new g.Event(type)); });
}

function fail(rec, name, msg) {
  var d = ex(name, msg), ev;
  if (typeof g.ErrorEvent === 'function') ev = new g.ErrorEvent('error', { error: d, message: msg });
  else { ev = new g.Event('error'); ev.error = d; }
  rec._r.state = 'inactive';
  rec.dispatchEvent(ev);
}

function release(r) {
  if (r.off) { r.off(); r.off = null; }
  if (r.pulled) { var p = r.pulled; r.pulled = null; p.forEach(function (t) { if (g.__swWaPull) g.__swWaPull(t, false); }); }
}

function stopEvent(rec) {
  flush(rec);
  var r = rec._r;
  r.state = 'inactive';
  release(r);
  if (r.id) live.delete(r.id);
  r.id = 0;
  rec.dispatchEvent(new g.Event('stop'));
}

// events from Go: {r, t:'data', b, n, tc} and {r, t:'stop'}
Object.defineProperty(g, '__swRecEvent', {
  value: function (json) {
    var m = JSON.parse(json), rec = live.get(m.r);
    if (!rec) { if (m.b) { try { takeBytes(m.b); } catch (e) { /* ignore */ } } return; }
    flush(rec);
    if (m.t === 'data') {
      var blob = new g.Blob([takeBytes(m.b)], { type: rec._r.mime });
      var ev = new BlobEvent('dataavailable', { data: blob, timecode: m.tc });
      rec.dispatchEvent(ev);
    } else if (m.t === 'stop') {
      stopEvent(rec);
    }
  }, configurable: true
});
