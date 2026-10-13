/*
 * Sweef media prelude: HTMLMediaElement (<audio>/<video>/new Audio), TimeRanges,
 * MediaError, text tracks (WebVTT/SRT), MediaStream, MediaSource/SourceBuffer,
 * navigator.mediaDevices, EME/PiP/remote-playback rejections, speech stubs.
 * Container parsing / decoding / frames are host functions (media.go).
 * Playback is a real clock driven by the page event loop (timers).
 */
(function (global) {
  'use strict';
  if (global.__swMediaInstalled) return;
  Object.defineProperty(global, '__swMediaInstalled', { value: true });
  var sw = global.__sw;
  if (!sw || !global.HTMLMediaElement) return;

  var _setTimeout = global.setTimeout, _clearTimeout = global.clearTimeout;
  var _now = function () { return global.performance.now(); };
  var Event = global.Event, EventTarget = global.EventTarget;
  var hasOwn = Object.prototype.hasOwnProperty;

  function defMethod(o, n, f) { Object.defineProperty(o, n, { value: f, writable: true, configurable: true, enumerable: true }); }
  function defGetter(o, n, g, s) { Object.defineProperty(o, n, { get: g, set: s, configurable: true, enumerable: true }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function dex(msg, name) { return new global.DOMException(msg, name); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  function derive(Sub, Sup) { Object.setPrototypeOf(Sub.prototype, Sup.prototype); Object.setPrototypeOf(Sub, Sup); }

  // ---- user activation -----------------------------------------------------
  var activation = { has: false, listeners: [] };
  global.__swUserActivation = {
    has: function () { return activation.has; },
    onActivate: function (cb) { if (activation.has) cb(); else activation.listeners.push(cb); },
    grant: function () { grantActivation(); }
  };
  function grantActivation() {
    if (activation.has) return;
    activation.has = true;
    var l = activation.listeners; activation.listeners = [];
    l.forEach(function (cb) { try { cb(); } catch (e) { /* ignore */ } });
  }
  // Agent actions reach the page through these Go-called entry points; page
  // code calling el.click() does not.
  ['__swDispatch', '__swDispatchKey', '__swMouse', '__swTouch', '__swDrag', '__swWheel', '__swSetValueFromGo'].forEach(function (n) {
    var orig = global[n];
    if (typeof orig !== 'function') return;
    try {
      Object.defineProperty(global, n, {
        value: function (a, b) {
          var t = (n === '__swDispatch') ? b : (n === '__swMouse' ? a : n);
          if (n !== '__swWheel' && (n !== '__swDispatch' || /^(click|mousedown|mouseup|pointerdown|pointerup|keydown|keyup|touchstart|touchend|dblclick|contextmenu)$/.test(String(t)))) grantActivation();
          return orig.apply(this, arguments);
        }, writable: true, configurable: true, enumerable: false
      });
    } catch (e) { /* ignore */ }
  });
  if (global.Navigator && !('userActivation' in global.Navigator.prototype)) {
    defGetter(global.Navigator.prototype, 'userActivation', function () {
      return { get hasBeenActive() { return activation.has; }, get isActive() { return activation.has; } };
    });
  }

  // ---- TimeRanges / MediaError ----------------------------------------------
  function TimeRanges() { illegal(); }
  tag(TimeRanges, 'TimeRanges');
  function makeRanges(list) {
    var r = Object.create(TimeRanges.prototype);
    var l = list.map(function (x) { return [x[0], x[1]]; });
    Object.defineProperty(r, '_l', { value: l });
    return r;
  }
  defGetter(TimeRanges.prototype, 'length', function () { return this._l.length; });
  defMethod(TimeRanges.prototype, 'start', function (i) { if (arguments.length < 1) throw new TypeError("Failed to execute 'start' on 'TimeRanges': 1 argument required, but only 0 present."); i = i >>> 0; if (i >= this._l.length) throw dex("Failed to execute 'start' on 'TimeRanges': The index provided (" + i + ") is greater than or equal to the maximum bound (" + this._l.length + ").", 'IndexSizeError'); return this._l[i][0]; });
  defMethod(TimeRanges.prototype, 'end', function (i) { if (arguments.length < 1) throw new TypeError("Failed to execute 'end' on 'TimeRanges': 1 argument required, but only 0 present."); i = i >>> 0; if (i >= this._l.length) throw dex("Failed to execute 'end' on 'TimeRanges': The index provided (" + i + ") is greater than or equal to the maximum bound (" + this._l.length + ").", 'IndexSizeError'); return this._l[i][1]; });
  global.TimeRanges = TimeRanges;

  function MediaError() { illegal(); }
  tag(MediaError, 'MediaError');
  ['MEDIA_ERR_ABORTED:1', 'MEDIA_ERR_NETWORK:2', 'MEDIA_ERR_DECODE:3', 'MEDIA_ERR_SRC_NOT_SUPPORTED:4'].forEach(function (s) {
    var p = s.split(':'); Object.defineProperty(MediaError, p[0], { value: +p[1], enumerable: true }); Object.defineProperty(MediaError.prototype, p[0], { value: +p[1], enumerable: true });
  });
  defGetter(MediaError.prototype, 'code', function () { return this._code; });
  defGetter(MediaError.prototype, 'message', function () { return this._msg; });
  function makeError(code, msg) { var e = Object.create(MediaError.prototype); Object.defineProperty(e, '_code', { value: code }); Object.defineProperty(e, '_msg', { value: msg || '' }); return e; }
  global.MediaError = MediaError;

  // ---- constants --------------------------------------------------------------
  var MediaP = global.HTMLMediaElement.prototype, HME = global.HTMLMediaElement;
  [['NETWORK_EMPTY', 0], ['NETWORK_IDLE', 1], ['NETWORK_LOADING', 2], ['NETWORK_NO_SOURCE', 3], ['HAVE_NOTHING', 0], ['HAVE_METADATA', 1], ['HAVE_CURRENT_DATA', 2], ['HAVE_FUTURE_DATA', 3], ['HAVE_ENOUGH_DATA', 4]].forEach(function (c) {
    Object.defineProperty(HME, c[0], { value: c[1], enumerable: true }); Object.defineProperty(MediaP, c[0], { value: c[1], enumerable: true });
  });

  var EVENTS = ['abort', 'canplay', 'canplaythrough', 'durationchange', 'emptied', 'ended', 'error', 'loadeddata', 'loadedmetadata', 'loadstart', 'pause', 'play', 'playing', 'progress', 'ratechange', 'seeked', 'seeking', 'stalled', 'suspend', 'timeupdate', 'volumechange', 'waiting', 'resize', 'encrypted', 'waitingforkey', 'enterpictureinpicture', 'leavepictureinpicture'];

  // ---- per-element state ------------------------------------------------------------
  var STATE = new WeakMap();
  function st(el) {
    var s = STATE.get(el);
    if (!s) {
      s = {
        el: el, net: 0, ready: 0, paused: true, ended: false, seeking: false, error: null,
        pos: 0, wall: 0, rate: 1, defRate: 1, vol: 1, muted: null, loadId: 0,
        info: null, h: 0, duration: NaN, currentSrc: '', srcObject: null, mse: null,
        pendingPlay: [], autoplaying: true, handlers: {}, timer: null, timerAt: 0,
        lastTU: 0, defaultStart: 0, started: false, preloadDone: false, loadedOnce: false,
        sinkId: '', rvfc: [], rvfcSeq: 0, frameSeq: 0, lastFrameReq: -1, lastFrameAt: 0,
        tracks: null, activeCuesDirty: true, played: [], lastRun: 0, pip: false, resumeLoad: null,
        capture: [], node: 0, presented: 0
      };
      STATE.set(el, s);
    }
    return s;
  }
  function isVideo(el) { return el.localName === 'video' || (el.tagName && el.tagName.toLowerCase() === 'video'); }
  function isMedia(n) { return n && n.nodeType === 1 && (n instanceof HME); }

  // ---- async event queue (media element task source) ----------------------------------
  var taskQ = [], taskScheduled = false;
  function drain() {
    taskScheduled = false;
    var q = taskQ; taskQ = [];
    for (var i = 0; i < q.length; i++) { try { q[i](); } catch (e) { reportErr(e); } }
    if (taskQ.length && !taskScheduled) { taskScheduled = true; _setTimeout(drain, 0); }
  }
  function task(fn) { taskQ.push(fn); if (!taskScheduled) { taskScheduled = true; _setTimeout(drain, 0); } }
  function reportErr(e) { try { sw.log('error', 'Uncaught ' + (e && e.stack ? e.stack : e)); } catch (x) { /* ignore */ } }
  function fire(el, type, ctor, init) {
    var ev = ctor ? new ctor(type, init) : new Event(type);
    el.dispatchEvent(ev);
  }
  // queue an event; events of a load generation are dropped when the element reloads
  function ev(el, type) { var s = st(el), g = s.evGen || 0; task(function () { if ((s.evGen || 0) === g) fire(el, type); }); }
  function qev(el, type, ctor) {
    var s = st(el), id = s.loadId;
    task(function () { if (s.loadId !== id && type !== 'emptied' && type !== 'abort' && type !== 'error') { if (!(s.loadId === id + 0)) return; } fire(el, type, ctor); });
  }
  function qevAny(el, type) { ev(el, type); }

  // ---- time keeping ----------------------------------------------------------------------
  function playing(s) { return !s.paused && !s.ended && s.ready >= 3 && !s.seekingBlock; }
  function curTime(s) {
    if (!playing(s)) return s.pos;
    var el0 = _now() - s.wall; if (el0 < 0) el0 = 0;
    var t = s.pos + el0 / 1000 * s.rate;
    var d = s.duration;
    if (s.rate >= 0) { if (isFinite(d) && t > d) t = d; } else if (t < 0) t = 0;
    return t;
  }
  var LAT = 70; // ms of audio-renderer start-up latency Chromium shows after (re)starting playback
  function anchor(s) { s.pos = curTime(s); s.wall = _now(); }
  function durationOf(s) { return s.duration; }

  // ---- reflected attributes -----------------------------------------------------------------
  function boolAttr(name) { return { get: function () { return this.hasAttribute(name); }, set: function (v) { if (v) this.setAttribute(name, ''); else this.removeAttribute(name); } }; }
  function strAttr(name) { return { get: function () { var v = this.getAttribute(name); return v === null ? '' : v; }, set: function (v) { this.setAttribute(name, String(v)); } }; }

  var PROPS = {};
  PROPS.autoplay = boolAttr('autoplay');
  PROPS.loop = boolAttr('loop');
  PROPS.controls = boolAttr('controls');
  PROPS.defaultMuted = boolAttr('muted');
  PROPS.playsInline = boolAttr('playsinline');
  PROPS.disableRemotePlayback = boolAttr('disableremoteplayback');
  PROPS.crossOrigin = { get: function () { var v = this.getAttribute('crossorigin'); if (v === null) return null; v = v.toLowerCase(); return v === 'use-credentials' ? v : 'anonymous'; }, set: function (v) { if (v === null || v === undefined) this.removeAttribute('crossorigin'); else this.setAttribute('crossorigin', String(v)); } };
  PROPS.preload = {
    get: function () { var v = this.getAttribute('preload'); if (v === null) return 'metadata'; v = v.toLowerCase(); if (v === '' || v === 'auto') return 'auto'; if (v === 'none' || v === 'metadata') return v; return 'metadata'; },
    set: function (v) { this.setAttribute('preload', String(v)); }
  };
  PROPS.src = {
    get: function () { var v = this.getAttribute('src'); if (v === null) return ''; try { return new global.URL(v, global.document.baseURI || global.location.href).href; } catch (e) { return v; } },
    set: function (v) { this.setAttribute('src', String(v)); }
  };
  PROPS.currentSrc = { get: function () { return st(this).currentSrc; } };
  PROPS.srcObject = {
    get: function () { return st(this).srcObject; },
    set: function (v) {
      if (v !== null && v !== undefined && !(v instanceof global.MediaStream) && !(v instanceof global.MediaSource) && !(v instanceof global.Blob)) throw new TypeError("Failed to set the 'srcObject' property on 'HTMLMediaElement': The provided value is not of type '(MediaSource or MediaStream or Blob)'.");
      st(this).srcObject = v === undefined ? null : v; loadAlgorithm(this);
    }
  };
  PROPS.networkState = { get: function () { return st(this).net; } };
  PROPS.readyState = { get: function () { return st(this).ready; } };
  PROPS.error = { get: function () { return st(this).error; } };
  PROPS.paused = { get: function () { return st(this).paused; } };
  PROPS.ended = { get: function () { var s = st(this); return s.ended || (playing(s) && isFinite(s.duration) && s.rate >= 0 && curTime(s) >= s.duration && !this.loop); } };
  PROPS.seeking = { get: function () { return st(this).seeking; } };
  PROPS.duration = { get: function () { return st(this).duration; } };
  PROPS.currentTime = {
    get: function () { var s = st(this); if (s.ready === 0) return s.defaultStart; return curTime(s); },
    set: function (v) { v = +v; if (!isFinite(v)) throw new TypeError("Failed to set the 'currentTime' property on 'HTMLMediaElement': The provided double value is non-finite."); seekTo(this, v, false); }
  };
  PROPS.volume = {
    get: function () { return st(this).vol; },
    set: function (v) { v = +v; if (!(v >= 0 && v <= 1)) throw dex("Failed to set the 'volume' property on 'HTMLMediaElement': The volume provided (" + v + ") is outside the range [0, 1].", 'IndexSizeError'); var s = st(this); if (s.vol !== v) { s.vol = v; qevAny(this, 'volumechange'); } }
  };
  PROPS.muted = {
    get: function () { var s = st(this); return s.muted === null ? this.hasAttribute('muted') : s.muted; },
    set: function (v) { var s = st(this), old = this.muted; s.muted = !!v; if (old !== !!v) qevAny(this, 'volumechange'); }
  };
  function rateCheck(v) { v = +v; if (!isFinite(v)) throw new TypeError('The provided double value is non-finite.'); return v; }
  PROPS.playbackRate = {
    get: function () { return st(this).rate; },
    set: function (v) {
      v = rateCheck(v); if (v < 0.0625 || v > 16) throw dex("Failed to set the 'playbackRate' property on 'HTMLMediaElement': The provided playback rate (" + v + ") is not in the supported playback range.", 'NotSupportedError');
      var s = st(this); if (s.rate !== v) { anchor(s); s.rate = v; qevAny(this, 'ratechange'); reschedule(this); }
    }
  };
  PROPS.defaultPlaybackRate = {
    get: function () { return st(this).defRate; },
    set: function (v) { v = rateCheck(v); if (v < 0.0625 || v > 16) throw dex("Failed to set the 'defaultPlaybackRate' property on 'HTMLMediaElement': The provided playback rate (" + v + ") is not in the supported playback range.", 'NotSupportedError'); var s = st(this); if (s.defRate !== v) { s.defRate = v; qevAny(this, 'ratechange'); } }
  };
  PROPS.preservesPitch = { get: function () { var s = st(this); return s.pp === undefined ? true : s.pp; }, set: function (v) { st(this).pp = !!v; } };
  PROPS.buffered = { get: function () { return makeRanges(bufferedList(st(this))); } };
  PROPS.seekable = { get: function () { var s = st(this); if (s.ready === 0 || !isFinite(s.duration) && !s.mse) return makeRanges([]); if (s.live) return makeRanges([[0, s.duration]]); return makeRanges([[0, s.duration]]); } };
  PROPS.played = { get: function () { var s = st(this), l = s.played.slice(); if (!s.paused && s.ready >= 3 && s.playStart !== undefined) l.push([s.playStart, curTime(s)]); return makeRanges(mergeRanges(l)); } };
  PROPS.sinkId = { get: function () { return st(this).sinkId; } };
  PROPS.mediaKeys = { get: function () { return null; } };
  PROPS.remote = { get: function () { return remoteObj; } };
  PROPS.disablePictureInPicture = boolAttr('disablepictureinpicture');
  PROPS.controlsList = { get: function () { var self = this; return (st(this).cl) || (st(this).cl = (function () { var l = Object.create(global.DOMTokenList ? global.DOMTokenList.prototype : Object.prototype); return l; })()); } };
  PROPS.textTracks = { get: function () { return tracksOf(this).list; } };
  PROPS.audioTracks = { get: function () { return tracksOf(this).audio; } };
  PROPS.videoTracks = { get: function () { return tracksOf(this).video; } };
  PROPS.onencrypted = null; delete PROPS.onencrypted;
  Object.keys(PROPS).forEach(function (k) { Object.defineProperty(MediaP, k, { get: PROPS[k].get, set: PROPS[k].set, configurable: true, enumerable: true }); });

  EVENTS.forEach(function (name) {
    Object.defineProperty(MediaP, 'on' + name, {
      get: function () { var h = st(this).handlers[name]; return h ? h.fn : null; },
      set: function (v) {
        var s = st(this), h = s.handlers[name];
        if (!h) { h = s.handlers[name] = { fn: null, reg: false }; }
        h.fn = (typeof v === 'function' || (v && typeof v === 'object')) ? v : null;
        if (!h.reg) { h.reg = true; var el = this; this.addEventListener(name, function (ev) { var f = h.fn; if (typeof f === 'function') f.call(el, ev); else if (f && typeof f.handleEvent === 'function') f.handleEvent(ev); }); }
      }, configurable: true, enumerable: true
    });
  });

  function mergeRanges(l) {
    l = l.filter(function (r) { return r[1] >= r[0]; }).sort(function (a, b) { return a[0] - b[0]; });
    var o = [];
    l.forEach(function (r) { if (o.length && r[0] <= o[o.length - 1][1]) o[o.length - 1][1] = Math.max(o[o.length - 1][1], r[1]); else o.push([r[0], r[1]]); });
    return o;
  }

  function bufferedList(s) {
    if (s.mse) return s.mse.bufferedUnion();
    if (!s.h) return s.streamLike ? [[0, 0]] : [];
    if (s.bufCache && s.bufCache.at > _now() - 50) return s.bufCache.l;
    var l = [];
    try { l = sw.mediaBuffered(s.h) || []; } catch (e) { l = []; }
    s.bufCache = { at: _now(), l: l };
    return l;
  }

  // ---- blob URL registry (MediaSource objects and Blobs) ---------------------------------
  var blobMap = new Map();
  (function () {
    var U = global.URL; if (!U || !U.createObjectURL) return;
    var oc = U.createObjectURL, orv = U.revokeObjectURL;
    U.createObjectURL = function (o) {
      var isMS = global.MediaSource && o instanceof global.MediaSource;
      var u = oc.call(U, isMS ? new global.Blob([]) : o);
      blobMap.set(u, o); return u;
    };
    U.revokeObjectURL = function (u) { blobMap.delete(String(u)); return orv.call(U, u); };
  })();
  function b64OfBytes(bytes) {
    var bin = '';
    for (var i = 0; i < bytes.length; i += 0x6000) bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x6000));
    return global.btoa(bin);
  }

  // ---- playback policy ------------------------------------------------------------------------
  function hasAudioTrack(s) { return !s.info || s.info.tracks.some(function (t) { return t.kind === 'audio'; }); }
  function liteMedia() { try { return !!sw.mediaLite(); } catch (e) { return false; } }
  function playAllowed(el) {
    if (activation.has) return true;
    if (isVideo(el) && el.muted) return true;
    var s = st(el);
    if (isVideo(el) && s.info && !hasAudioTrack(s)) return true;
    return false;
  }

  // ---- resource selection ---------------------------------------------------------------------
  function hasSource(el) {
    if (st(el).srcObject) return true;
    if (el.hasAttribute('src')) return true;
    for (var c = el.firstChild; c; c = c.nextSibling) if (c.nodeType === 1 && c.localName === 'source') return true;
    return false;
  }
  function closeRes(s) {
    if (s.h) { try { sw.mediaClose(s.h); } catch (e) { /* ignore */ } }
    s.h = 0; s.info = null; s.bufCache = null;
    if (s.mse) { s.mse.detach(); s.mse = null; }
  }
  function rejectPending(s, name, msg) {
    var p = s.pendingPlay; s.pendingPlay = [];
    p.forEach(function (x) { x.rej(dex(msg, name)); });
  }
  function resolvePending(s) {
    var p = s.pendingPlay; s.pendingPlay = [];
    task(function () { p.forEach(function (x) { x.res(undefined); }); });
  }

  function loadAlgorithm(el) {
    var s = st(el);
    s.loadId++;
    s.evGen = (s.evGen || 0) + 1;
    var id = s.loadId;
    if (s.capPrev) { s.capPrev = false; try { sw.capUnbindPreview(s.node); } catch (e) { /* ignore */ } }
    s.adopted = true;
    if (s.timer) { _clearTimeout(s.timer); s.timer = null; }
    if (s.net === 1 || s.net === 2) ev(el, 'abort');
    if (s.net !== 0) {
      ev(el, 'emptied');
      closeRes(s);
      s.ready = 0;
      if (!s.paused) { s.paused = true; rejectPending(s, 'AbortError', 'The play() request was interrupted by a new load request. https://goo.gl/LdLk22'); }
      s.seeking = false; s.seekBlock = false;
      if (s.pos !== 0) { s.pos = 0; ev(el, 'timeupdate'); }
      s.ended = false; s.duration = NaN; s.currentSrc = ''; s.played = []; s.playStart = undefined;
      if (el.localName === 'video' || el.localName === 'audio') bindFrames(el, 0);
    } else { closeRes(s); }
    s.defaultStart = 0; s.rate = s.defRate; s.error = null; s.autoplaying = true; s.started = false; s.resumeLoad = null; s.preloadDone = false;
    s.net = 0;
    Promise.resolve().then(function () { if (s.loadId === id) selectResource(el, id); });
  }

  function candidates(el, s) {
    if (s.srcObject) return { mode: 'object' };
    if (el.hasAttribute('src')) return { mode: 'attr', url: el.getAttribute('src') };
    var kids = [];
    for (var c = el.firstChild; c; c = c.nextSibling) if (c.nodeType === 1 && c.localName === 'source') kids.push(c);
    if (kids.length) return { mode: 'children', kids: kids };
    return null;
  }

  function selectResource(el, id) {
    var s = st(el);
    var cand = candidates(el, s);
    if (!cand) { s.net = 0; return; }
    s.net = 3;
    s.net = 2;
    task(function () { if (s.loadId === id) fire(el, 'loadstart'); });
    s.sel = cand;
    if (cand.mode === 'object') { task(function () { if (s.loadId === id) streamSource(el, s, id); }); return; }
    var pre = el.preload;
    // Data Saver: no preload and no autoplay download; the load starts when play() is called
    var lite = liteMedia();
    if (lite) pre = 'none';
    var explicitPlay = !s.paused || (!lite && el.hasAttribute('autoplay') && s.autoplaying);
    var go = function () { if (s.loadId === id) startFetch(el, s, id, cand, 0); };
    if (pre === 'none' && !explicitPlay) {
      s.resumeLoad = go;
      task(function () { if (s.loadId === id) { s.net = 1; fire(el, 'suspend'); } });
      return;
    }
    task(go);
  }

  function resolveURLFor(el, raw) {
    try { return new global.URL(raw, global.document.baseURI || global.location.href).href; } catch (e) { return raw; }
  }

  function startFetch(el, s, id, cand, idx) {
    var raw, srcEl = null;
    if (cand.mode === 'attr') raw = cand.url;
    else {
      for (; idx < cand.kids.length; idx++) {
        var k = cand.kids[idx];
        var sv = k.getAttribute('src');
        if (sv === null || sv === '') continue;
        var ty = k.getAttribute('type');
        if (ty && global.HTMLMediaElement.prototype.canPlayType.call(el, ty) === '') { var bad = k; task(function () { if (s.loadId === id) fire(bad, 'error'); }); continue; }
        raw = sv; srcEl = k; break;
      }
      if (!srcEl) { s.net = 3; return; }
    }
    if (raw === '' || raw === null) { failLoad(el, s, id, cand, idx, srcEl, 4, 'MEDIA_ELEMENT_ERROR: Empty src attribute', 'Empty src attribute'); return; }
    var url = resolveURLFor(el, raw);
    var res = null;
    var blob = blobMap.get(url);
    if (blob && global.MediaSource && blob instanceof global.MediaSource) {
      s.currentSrc = url; attachMSE(el, s, id, blob); return;
    }
    try {
      if (blob) res = sw.mediaOpenBytes(b64OfBytes(blob._bytes || new Uint8Array(0)));
      else if (/^blob:/.test(url)) res = { ok: false, error: 'network', message: 'blob URL revoked' };
      else res = sw.mediaOpen(url);
    } catch (e) { res = { ok: false, error: 'network', message: String(e && e.message || e) }; }
    if (s.loadId !== id) { if (res && res.ok) try { sw.mediaClose(res.h); } catch (e) { /* ignore */ } return; }
    if (!res.ok) {
      var msg = res.error === 'network' ? 'MEDIA_ELEMENT_ERROR: Format error' : 'MEDIA_ELEMENT_ERROR: Format error';
      if (res.error !== 'network' && cand.mode === 'attr') { task(function () { if (s.loadId === id) fire(el, 'progress'); }); task(function () { if (s.loadId === id) fire(el, 'suspend'); }); }
      failLoad(el, s, id, cand, idx, srcEl, 4, msg, res.message);
      return;
    }
    s.currentSrc = url; s.h = res.h; s.info = res.info; s.frameMode = res.frameMode;
    s.rawInfo = res;
    onMetadata(el, s, id);
  }

  function failLoad(el, s, id, cand, idx, srcEl, code, msg) {
    if (cand.mode === 'children') {
      if (srcEl) task(function () { if (s.loadId === id) fire(srcEl, 'error'); });
      task(function () { if (s.loadId === id) startFetch(el, s, id, cand, idx + 1); });
      return;
    }
    s.error = makeError(code, msg); s.net = 3; s.ready = 0;
    task(function () { if (s.loadId === id) { fire(el, 'error'); } });
    rejectPending(s, 'NotSupportedError', 'Failed to load because no supported source was found.');
    s.paused = s.paused;
  }

  function streamSource(el, s, id) {
    var ms = s.srcObject;
    if (global.MediaSource && ms instanceof global.MediaSource) { attachMSE(el, s, id, ms); return; }
    s.streamLike = true; s.info = { container: 'stream', duration: Infinity, tracks: [] };
    s.duration = ms instanceof global.Blob ? NaN : Infinity;
    if (ms instanceof global.Blob) {
      var res = sw.mediaOpenBytes(b64OfBytes(ms._bytes || new Uint8Array(0)));
      if (res.ok) { s.h = res.h; s.info = res.info; s.streamLike = false; s.frameMode = res.frameMode; return onMetadata(el, s, id); }
      s.error = makeError(4, 'MEDIA_ELEMENT_ERROR: Format error'); s.net = 3; ev(el, 'error'); return;
    }
    if (ms instanceof global.MediaStream && isVideo(el)) { // a live camera or screen track: show its frames
      var vt = ms.getVideoTracks().filter(function (t) { return t._t.cap && t._t.ready === 'live'; })[0];
      if (vt) {
        s.videoW = vt._t.settings.width || 0; s.videoH = vt._t.settings.height || 0; s.fps = vt._t.settings.frameRate || 30;
        try { bindFrames(el, 0); s.capPrev = !!sw.capBindPreview(s.node, vt._t.id); } catch (e) { /* ignore */ }
      }
    }
    ev(el, 'durationchange');
    s.ready = 1; ev(el, 'loadedmetadata');
    s.net = 1;
    setReady(el, 4);
  }

  function onMetadata(el, s, id) {
    var info = s.info;
    s.duration = info.duration >= 0 ? info.duration : Infinity;
    var v = null; info.tracks.forEach(function (t) { if (t.kind === 'video' && !v) v = t; });
    s.video = v;
    s.videoW = v ? v.width : 0; s.videoH = v ? v.height : 0; s.fps = v && v.frameRate > 0 ? v.frameRate : 30;
    // Chromium order for a fully buffered small resource
    s.net = 1;
    task(function () { if (s.loadId !== id) return; fire(el, 'progress'); });
    task(function () { if (s.loadId !== id) return; fire(el, 'suspend'); });
    task(function () {
      if (s.loadId !== id) return;
      fire(el, 'durationchange');
      s.ready = 1;
      populateTracks(el, s);
      if (v) bindFrames(el, s.h);
      if (v) fire(el, 'resize');
      fire(el, 'loadedmetadata');
      if (s.defaultStart > 0) { s.pos = Math.min(s.defaultStart, isFinite(s.duration) ? s.duration : s.defaultStart); }
      var wantMore = el.preload !== 'metadata' || (s.rawInfo && s.rawInfo.info && s.rawInfo.info.size <= 4194304) || !s.paused || (el.hasAttribute('autoplay') && s.autoplaying);
      if (!wantMore) { s.resumeLoad = function () { s.resumeLoad = null; task(function () { if (s.loadId === id) finishLoad(el, s, id); }); }; return; }
      finishLoad(el, s, id);
    });
  }

  // progressive download for resources fetched in Range chunks
  function topUpBuffer(el, s) {
    if (!s.h || !s.rawInfo || s.mse) return;
    var now = _now(); if (s.bufTopAt && now - s.bufTopAt < 400) return; s.bufTopAt = now;
    var b = bufferedList(s), cur = curTime(s), end = 0, i;
    for (i = 0; i < b.length; i++) if (cur >= b[i][0] - 0.01 && cur <= b[i][1] + 0.01) end = b[i][1];
    if (!end) end = cur;
    if (isFinite(s.duration) && end < s.duration - 0.05 && end - cur < 15) {
      try { sw.mediaFetch(s.h, end, 20); } catch (e) { return; }
      s.bufCache = null;
      var nb = bufferedList(s), ne = end;
      for (i = 0; i < nb.length; i++) if (nb[i][1] > ne) ne = nb[i][1];
      if (ne > end + 0.01) ev(el, 'progress');
    }
  }
  function finishLoad(el, s, id) {
    topUpBuffer(el, s);
    s.ready = 2; s.loadedData = true;
    fire(el, 'loadeddata');
    requestFrame(el, s, true);
    setReady(el, 4);
  }

  function setReady(el, n) {
    var s = st(el), old = s.ready;
    if (n === old) return;
    if (old >= 3 && n < 3) { s.pos = curTime(s); s.wall = _now(); }
    if (old < 3 && n >= 3) s.wall = _now() + LAT;
    s.ready = n;
    if (old < 2 && n >= 2 && !s.loadedData) { s.loadedData = true; qevAny(el, 'loadeddata'); }
    if (old < 3 && n >= 3) {
      qevAny(el, 'canplay');
      if (!s.paused) { s.wall = _now() + LAT; notifyPlaying(el, s); }
    }
    if (old < 4 && n >= 4) {
      qevAny(el, 'canplaythrough');
      if (s.autoplaying && s.paused && el.hasAttribute('autoplay') && playAllowed(el)) {
        s.paused = false; s.ended = false; s.wall = _now() + LAT; s.playStart = s.pos;
        qevAny(el, 'play'); notifyPlaying(el, s);
      }
    }
    if (old >= 3 && n <= 2 && !s.paused && !s.ended) { qevAny(el, 'timeupdate'); qevAny(el, 'waiting'); }
    reschedule(el);
  }
  function notifyPlaying(el, s) {
    s.autoplaying = false;
    qevAny(el, 'playing');
    resolvePending(s);
    s.lastTU = _now();
    reschedule(el);
  }

  // ---- play / pause / seek -----------------------------------------------------------------------------
  defMethod(MediaP, 'load', function () { loadAlgorithm(this); });
  defMethod(MediaP, 'canPlayType', function (t) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'canPlayType' on 'HTMLMediaElement': 1 argument required, but only 0 present.");
    return sw.mediaCanPlay(String(t));
  });
  defMethod(MediaP, 'play', function () {
    var el = this, s = st(el);
    return new Promise(function (res, rej) {
      if (!s.adopted && hasSource(el)) loadAlgorithm(el);
      if (s.paused && !playAllowed(el)) {
        _setTimeout(function () { rej(dex("play() failed because the user didn't interact with the document first. https://goo.gl/xX8pDD", 'NotAllowedError')); }, 0);
        return;
      }
      if (s.error && s.error.code === 4) { _setTimeout(function () { rej(dex('The element has no supported sources.', 'NotSupportedError')); }, 0); return; }
      var wasResume = !!s.resumeLoad;
      if (s.resumeLoad) { var r = s.resumeLoad; s.resumeLoad = null; r(); }
      if (s.ready >= 1 && isFinite(s.duration) && (s.ended || (curTime(s) >= s.duration && s.rate >= 0))) seekTo(el, 0, false);
      s.pendingPlay.push({ res: res, rej: rej });
      if (s.paused) {
        var p0 = s.pos;
        s.paused = false; s.ended = false; s.started = true;
        s.pos = p0; s.wall = _now() + LAT; s.playStart = s.pos;
        ev(el, 'play');
        if (s.ready <= 2) { ev(el, 'waiting'); }
        else notifyPlaying(el, s);
      } else if (s.ready >= 3) { resolvePending(s); }
      void wasResume;
      reschedule(el);
    });
  });
  defMethod(MediaP, 'pause', function () {
    var el = this, s = st(el);
    if (!s.adopted && hasSource(el)) loadAlgorithm(el);
    doPause(el, s, true);
  });
  function doPause(el, s, fromApi) {
    if (!s.paused) {
      var t = curTime(s);
      if (s.playStart !== undefined && t >= s.playStart) s.played.push([s.playStart, t]);
      s.playStart = undefined;
      s.pos = t; s.wall = _now();
      s.paused = true; s.autoplaying = false;
      ev(el, 'timeupdate');
      ev(el, 'pause');
      rejectPending(s, 'AbortError', 'The play() request was interrupted by a call to pause(). https://goo.gl/LdLk22');
      reschedule(el);
    } else { s.autoplaying = false; }
  }
  defMethod(MediaP, 'fastSeek', function (t) { seekTo(this, +t, true); });

  function seekTo(el, t, approx) {
    var s = st(el);
    if (s.ready === 0) { s.defaultStart = Math.max(0, t); return; }
    var d = s.duration;
    if (t < 0) t = 0;
    if (isFinite(d) && t > d) t = d;
    if (s.resumeLoad) { var r = s.resumeLoad; s.resumeLoad = null; r(); }
    var run = playing(s), wasPlaying = !s.paused;
    s.pos = t; s.wall = _now();
    s.seekBlock = true;
    s.seeking = true; s.ended = false;
    if (s.ready > 1) { s.pos = t; s.ready = 1; }
    s.seekId = (s.seekId || 0) + 1;
    var sid = s.seekId, lid = s.loadId;
    task(function () {
      if (s.loadId !== lid) return;
      fire(el, 'seeking');
      if (wasPlaying && s.seekId === sid) fire(el, 'waiting');
    });
    var finishSeek = function () {
      if (s.seekId !== sid || s.loadId !== lid) return;
      if (s.h) { try { sw.mediaFetch(s.h, t, 10); s.bufCache = null; } catch (e) { /* ignore */ } }
      s.seekBlock = false; s.seeking = false; s.wall = _now() + LAT;
      s.ready = 4;
      requestFrame(el, s, true);
      cuesSeek(el);
      fire(el, 'timeupdate');
      fire(el, 'seeked');
      s.lastTU = _now();
      qevAny(el, 'canplay');
      if (!s.paused) qevAny(el, 'playing');
      qevAny(el, 'canplaythrough');
      reschedule(el);
    };
    task(function () { _setTimeout(function () { task(finishSeek); }, 0); });
    void run;
  }

  // ---- scheduler: timeupdate, ended/loop, cues, rVFC -------------------------------------------------------
  function reschedule(el) {
    var s = st(el);
    if (s.timer) { _clearTimeout(s.timer); s.timer = null; }
    var now = _now(), wake = Infinity;
    var run = playing(s) && !s.seekBlock;
    if (run) {
      wake = Math.min(wake, s.lastTU + 250 - now);
      if (isFinite(s.duration) && s.rate > 0) wake = Math.min(wake, (s.duration - curTime(s)) / s.rate * 1000);
      var cb = nextCueBoundary(el, s);
      if (cb < Infinity) wake = Math.min(wake, cb / s.rate * 1000);
      if (s.rvfc.length && s.video) wake = Math.min(wake, 1000 / s.fps);
      if (s.video && now - s.lastFrameAt >= 250) wake = Math.min(wake, 0);
    } else if (s.rvfc.length && s.video && s.ready >= 2) {
      wake = Math.min(wake, 16);
    }
    if (wake === Infinity) return;
    if (wake < 0) wake = 0;
    s.timer = (global.__swBgTimeout || _setTimeout)(function () { s.timer = null; tick(el); }, Math.ceil(wake));
  }

  function tick(el) {
    var s = st(el);
    var now = _now();
    if (playing(s) && !s.seekBlock) {
      var t = curTime(s);
      if (isFinite(s.duration) && s.rate > 0 && t >= s.duration - 0.0005) { return handleEnd(el, s); }
      if (now - s.lastTU >= 249) { s.lastTU = now; fire(el, 'timeupdate'); }
      updateCues(el, s, t);
      topUpBuffer(el, s);
      if (s.video && now - s.lastFrameAt >= 250) requestFrame(el, s, false);
    }
    runRvfc(el, s);
    reschedule(el);
  }

  function handleEnd(el, s) {
    var d = s.duration;
    if (el.loop && s.rate > 0) {
      s.played.push([s.playStart === undefined ? 0 : s.playStart, d]); s.playStart = 0;
      seekTo(el, 0, false);
      return;
    }
    s.pos = d; s.wall = _now();
    s.ended = true;
    if (s.playStart !== undefined) s.played.push([s.playStart, d]);
    s.playStart = undefined;
    updateCues(el, s, d);
    fire(el, 'timeupdate'); s.lastTU = _now();
    if (!s.paused) {
      s.paused = true;
      fire(el, 'pause');
      rejectPending(s, 'AbortError', 'The play() request was interrupted because playback ended.');
      fire(el, 'ended');
    }
    requestFrame(el, s, true);
    reschedule(el);
  }

  // ---- video frames -------------------------------------------------------------------------------------------------
  function bindFrames(el, h) {
    if (!isVideo(el)) return;
    var s = st(el);
    try {
      s.node = global.__swIdOf(el);
      sw.mediaBind(s.node, h);
      var poster = el.getAttribute('poster');
      sw.mediaPoster(s.node, poster ? poster : '');
    } catch (e) { /* ignore */ }
  }
  function requestFrame(el, s, force) {
    if (s.mseFrameBuf && s.video) { s.lastFrameAt = _now(); try { sw.mseFrame(s.mseFrameBuf, curTime(s)); } catch (e) { /* ignore */ } return; }
    if (!s.h || !s.video || !s.frameMode) return;
    if (el.hasAttribute('poster') && !s.started && !s.seekId && !force) return;
    if (el.hasAttribute('poster') && !s.started && !s.seekId) return;
    s.lastFrameAt = _now();
    try { sw.mediaFrame(s.h, curTime(s)); } catch (e) { /* ignore */ }
  }
  function runRvfc(el, s) {
    if (!s.rvfc.length || !s.video || s.ready < 2) return;
    var fps = s.fps, t = curTime(s);
    var frame = Math.floor(t * fps + 1e-6);
    if (frame === s.lastRvfcFrame && playing(s)) return;
    if (!playing(s) && s.lastRvfcFrame === frame) return;
    s.lastRvfcFrame = frame;
    var cbs = s.rvfc; s.rvfc = [];
    var now = _now();
    s.presented++;
    cbs.forEach(function (c) {
      if (c.cancelled) return;
      try { c.cb(now, { presentationTime: now, expectedDisplayTime: now + 16, width: s.videoW, height: s.videoH, mediaTime: frame / fps, presentedFrames: s.presented, processingDuration: 0.001 }); } catch (e) { reportErr(e); }
    });
  }
  var rvfcSeq = 0;
  defMethod(MediaP, 'requestVideoFrameCallback', function (cb) {
    if (typeof cb !== 'function') throw new TypeError("Failed to execute 'requestVideoFrameCallback' on 'HTMLVideoElement': The callback provided as parameter 1 is not a function.");
    var s = st(this), h = ++rvfcSeq;
    s.rvfc.push({ id: h, cb: cb });
    reschedule(this);
    return h;
  });
  defMethod(MediaP, 'cancelVideoFrameCallback', function (h) { var s = st(this); s.rvfc.forEach(function (c) { if (c.id === h) c.cancelled = true; }); s.rvfc = s.rvfc.filter(function (c) { return c.id !== h; }); });
  var VP = global.HTMLVideoElement.prototype;
  ['requestVideoFrameCallback', 'cancelVideoFrameCallback'].forEach(function (n) { Object.defineProperty(VP, n, Object.getOwnPropertyDescriptor(MediaP, n)); delete MediaP[n]; });
  defGetter(VP, 'videoWidth', function () { return st(this).ready >= 1 ? (st(this).videoW || 0) : 0; });
  defGetter(VP, 'videoHeight', function () { return st(this).ready >= 1 ? (st(this).videoH || 0) : 0; });
  defGetter(VP, 'poster', function () { var v = this.getAttribute('poster'); if (v === null) return ''; try { return new global.URL(v, global.document.baseURI || global.location.href).href; } catch (e) { return v; } }, function (v) { this.setAttribute('poster', String(v)); });
  defGetter(VP, 'webkitDecodedFrameCount', function () { return st(this).presented; });
  defGetter(VP, 'webkitDroppedFrameCount', function () { return 0; });
  defGetter(VP, 'webkitDisplayingFullscreen', function () { return false; });
  defGetter(VP, 'webkitSupportsFullscreen', function () { return true; });
  defMethod(VP, 'getVideoPlaybackQuality', function () {
    var s = st(this);
    var total = s.ready >= 2 && s.video ? Math.floor(curTime(s) * s.fps) : 0;
    return { creationTime: _now(), totalVideoFrames: total, droppedVideoFrames: 0, corruptedVideoFrames: 0, totalFrameDelay: 0 };
  });
  defMethod(VP, 'webkitEnterFullscreen', function () { });
  defMethod(VP, 'webkitExitFullscreen', function () { });
  defMethod(VP, 'requestPictureInPicture', function () { if (st(this).ready === 0) return Promise.reject(dex('Metadata for the video element are not loaded yet.', 'InvalidStateError')); return Promise.reject(dex('Picture-in-Picture is not supported.', 'NotSupportedError')); });
  if (global.document) {
    try {
      Object.defineProperty(global.Document.prototype, 'pictureInPictureEnabled', { get: function () { return false; }, configurable: true, enumerable: true });
      Object.defineProperty(global.Document.prototype, 'pictureInPictureElement', { get: function () { return null; }, configurable: true, enumerable: true });
      defMethod(global.Document.prototype, 'exitPictureInPicture', function () { return Promise.reject(dex('No Picture-in-Picture element is active.', 'InvalidStateError')); });
    } catch (e) { /* ignore */ }
  }

  // ---- misc element API -----------------------------------------------------------------------------------------------------------
  var remoteObj = (function () {
    var r = Object.create(EventTarget.prototype); EventTarget.call(r);
    defMethod(r, 'watchAvailability', function () { return Promise.reject(dex('Availability monitoring is not supported.', 'NotSupportedError')); });
    defMethod(r, 'cancelWatchAvailability', function () { return Promise.reject(dex('Availability monitoring is not supported.', 'NotSupportedError')); });
    defMethod(r, 'prompt', function () { return Promise.reject(dex('Remote playback is not supported.', 'NotSupportedError')); });
    Object.defineProperty(r, 'state', { get: function () { return 'disconnected'; }, enumerable: true });
    return r;
  })();
  defMethod(MediaP, 'setMediaKeys', function (k) { if (k === null) return Promise.resolve(); return Promise.reject(dex('Unsupported keySystem or supportedConfigurations.', 'NotSupportedError')); });
  defMethod(MediaP, 'setSinkId', function (id) {
    var s = st(this); id = String(id === undefined ? '' : id);
    if (id === '' || id === 'default') { s.sinkId = ''; return Promise.resolve(); }
    return Promise.reject(dex('No audio device found with the requested ID.', 'NotFoundError'));
  });
  defMethod(MediaP, 'captureStream', function () {
    var el = this, s = st(el);
    var ms = new global.MediaStream();
    if (s.info && s.info.tracks && s.info.tracks.length) {
      if (s.info.tracks.some(function (t) { return t.kind === 'audio'; })) ms.addTrack(global.__swMakeTrack('audio', 'silent'));
      if (isVideo(el) && s.video) ms.addTrack(global.__swMakeTrack('video', 'media-element', { width: s.videoW, height: s.videoH, frameRate: s.fps }));
    } else if (s.streamLike && s.srcObject instanceof global.MediaStream) {
      s.srcObject.getTracks().forEach(function (t) { ms.addTrack(t.clone()); });
    }
    return ms;
  });
  MediaP.mozCaptureStream = undefined; delete MediaP.mozCaptureStream;
  defMethod(MediaP, 'addTextTrack', function (kind, label, lang) {
    var tr = tracksOf(this);
    return tr.add(kind, label === undefined ? '' : label, lang === undefined ? '' : lang, 'hidden', null);
  });
  defMethod(MediaP, 'getStartDate', function () { return new Date(NaN); });
  defGetter(MediaP, 'webkitAudioDecodedByteCount', function () { return 0; });
  defGetter(MediaP, 'webkitVideoDecodedByteCount', function () { return 0; });
  defGetter(MediaP, 'audioTracks', PROPS.audioTracks.get);

  // ======================= text tracks ======================================================
  function parseTS(str) {
    var m = /^(?:(\d+):)?(\d{1,2}):(\d{2})[.,](\d{1,3})$/.exec(str.trim());
    if (!m) return NaN;
    var ms = m[4]; while (ms.length < 3) ms += '0';
    return (m[1] ? +m[1] * 3600 : 0) + (+m[2]) * 60 + (+m[3]) + (+ms) / 1000;
  }
  // returns { cues:[{id,start,end,text,settings}], isVTT } ; throws on a document that is neither
  function parseCueText(text) {
    text = String(text).replace(/^\uFEFF/, '').replace(/\r\n?/g, '\n');
    var isVTT = /^WEBVTT([ \t\n]|$)/.test(text);
    var blocks = text.split(/\n{2,}/), cues = [];
    blocks.forEach(function (blk, bi) {
      var lines = blk.split('\n');
      if (bi === 0 && isVTT) return;
      if (!lines.length || !lines.join('').trim()) return;
      if (/^(NOTE|STYLE|REGION)(\s|$)/.test(lines[0])) return;
      var ti = -1;
      for (var i = 0; i < Math.min(lines.length, 2); i++) if (lines[i].indexOf('-->') >= 0) { ti = i; break; }
      if (ti < 0) return;
      var m = /^\s*([\d:.,]+)\s*-->\s*([\d:.,]+)(.*)$/.exec(lines[ti]);
      if (!m) return;
      var a = parseTS(m[1]), b = parseTS(m[2]);
      if (isNaN(a) || isNaN(b)) return;
      if (isVTT && /,/.test(m[1] + m[2])) return;
      cues.push({ id: ti === 1 ? lines[0] : '', start: a, end: b, text: lines.slice(ti + 1).join('\n'), settings: m[3].trim() });
    });
    if (!isVTT && !cues.length) throw new Error('not a caption file');
    return { cues: cues, isVTT: isVTT };
  }

  function TextTrackCue() { illegal(); }
  tag(TextTrackCue, 'TextTrackCue');
  derive(TextTrackCue, EventTarget);
  function initCue(c, s, e) {
    EventTarget.call(c);
    c._track = null; c._id = ''; c._start = s; c._end = e; c._pause = false;
  }
  [['id', '_id'], ['startTime', '_start'], ['endTime', '_end'], ['pauseOnExit', '_pause']].forEach(function (p) {
    defGetter(TextTrackCue.prototype, p[0], function () { return this[p[1]]; }, function (v) {
      if (p[0] === 'startTime' || p[0] === 'endTime') { v = +v; if (!isFinite(v)) throw new TypeError("Failed to set the '" + p[0] + "' property on 'TextTrackCue': The provided double value is non-finite."); if (this[p[1]] !== v) { this[p[1]] = v; if (this._track) this._track._dirty(); } return; }
      this[p[1]] = p[0] === 'pauseOnExit' ? !!v : String(v);
    });
  });
  defGetter(TextTrackCue.prototype, 'track', function () { return this._track; });
  ['enter', 'exit'].forEach(function (n) {
    defGetter(TextTrackCue.prototype, 'on' + n, function () { return this['_on' + n] || null; }, function (v) {
      var self = this, key = '_on' + n;
      if (!this['_reg' + n]) { this['_reg' + n] = true; this.addEventListener(n, function (ev) { var f = self[key]; if (typeof f === 'function') f.call(self, ev); }); }
      this[key] = typeof v === 'function' ? v : null;
    });
  });
  function VTTCue(start, end, text) {
    if (!(this instanceof VTTCue)) throw new TypeError("Failed to construct 'VTTCue': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 3) throw new TypeError("Failed to construct 'VTTCue': 3 arguments required, but only " + arguments.length + " present.");
    start = +start; end = +end;
    if (!isFinite(start) || !isFinite(end)) throw new TypeError("Failed to construct 'VTTCue': The provided double value is non-finite.");
    initCue(this, start, end);
    this._text = String(text); this._vertical = ''; this._snap = true; this._line = 'auto'; this._lineAlign = 'start'; this._pos = 'auto'; this._posAlign = 'auto'; this._size = 100; this._align = 'center'; this._region = null;
  }
  derive(VTTCue, TextTrackCue); tag(VTTCue, 'VTTCue');
  [['text', '_text', String], ['vertical', '_vertical', String], ['snapToLines', '_snap', Boolean], ['lineAlign', '_lineAlign', String], ['positionAlign', '_posAlign', String], ['align', '_align', String], ['region', '_region', function (v) { return v; }]].forEach(function (p) {
    defGetter(VTTCue.prototype, p[0], function () { return this[p[1]]; }, function (v) { this[p[1]] = p[2](v); });
  });
  defGetter(VTTCue.prototype, 'line', function () { return this._line; }, function (v) { this._line = v === 'auto' ? 'auto' : +v; });
  defGetter(VTTCue.prototype, 'position', function () { return this._pos; }, function (v) { this._pos = v === 'auto' ? 'auto' : +v; });
  defGetter(VTTCue.prototype, 'size', function () { return this._size; }, function (v) { v = +v; if (v < 0 || v > 100) throw dex("Failed to set the 'size' property on 'VTTCue': The value provided (" + v + ") is outside the range [0, 100].", 'IndexSizeError'); this._size = v; });
  defMethod(VTTCue.prototype, 'getCueAsHTML', function () {
    var f = global.document.createDocumentFragment(), d = global.document.createElement('div');
    d.innerHTML = this._text.replace(/<(\d+:)?\d\d:\d\d\.\d{3}>/g, ''); while (d.firstChild) f.appendChild(d.firstChild);
    return f;
  });
  global.TextTrackCue = TextTrackCue; global.VTTCue = VTTCue;
  function DataCue() { illegal(); }
  global.VTTRegion = function VTTRegion() { this.id = ''; this.width = 100; this.lines = 3; this.regionAnchorX = 0; this.regionAnchorY = 100; this.viewportAnchorX = 0; this.viewportAnchorY = 100; this.scroll = ''; };

  function CueList() { illegal(); }
  tag(CueList, 'TextTrackCueList');
  function makeCueList(arr) { var l = Object.create(CueList.prototype); Object.defineProperty(l, '_a', { value: arr, writable: true }); syncIdx(l); return l; }
  function syncIdx(l) {
    var a = l._a, old = l._n || 0;
    for (var i = 0; i < a.length; i++) Object.defineProperty(l, i, { value: a[i], configurable: true, enumerable: true });
    for (var j = a.length; j < old; j++) delete l[j];
    Object.defineProperty(l, '_n', { value: a.length, configurable: true, writable: true });
  }
  defGetter(CueList.prototype, 'length', function () { return this._a.length; });
  defMethod(CueList.prototype, 'getCueById', function (id) { id = String(id); for (var i = 0; i < this._a.length; i++) if (this._a[i]._id === id && id !== '') return this._a[i]; return null; });
  CueList.prototype[Symbol.iterator] = function () { return this._a[Symbol.iterator](); };
  global.TextTrackCueList = CueList;

  function TextTrack() { illegal(); }
  derive(TextTrack, EventTarget); tag(TextTrack, 'TextTrack');
  global.TextTrack = TextTrack;
  var trackSeq = 0;
  function newTextTrack(owner, kind, label, lang, mode, trackEl) {
    var t = Object.create(TextTrack.prototype);
    EventTarget.call(t);
    Object.defineProperty(t, '_o', { value: { owner: owner, kind: kind, label: label, lang: lang, mode: mode, id: '', cues: [], active: [], el: trackEl, loaded: false, cueList: null, activeList: null, dirty: true, onch: null, regch: false } });
    Object.defineProperty(t, '_dirty', { value: function () { t._o.dirty = true; t._o.cues.sort(function (a, b) { return a._start - b._start || a._end - b._end; }); reschedule(owner); } });
    return t;
  }
  defGetter(TextTrack.prototype, 'kind', function () { return this._o.kind; });
  defGetter(TextTrack.prototype, 'label', function () { return this._o.label; });
  defGetter(TextTrack.prototype, 'language', function () { return this._o.lang; });
  defGetter(TextTrack.prototype, 'id', function () { return this._o.id; });
  defGetter(TextTrack.prototype, 'inBandMetadataTrackDispatchType', function () { return ''; });
  defGetter(TextTrack.prototype, 'mode', function () { return this._o.mode; }, function (v) {
    v = String(v); if (v !== 'disabled' && v !== 'hidden' && v !== 'showing') return;
    var o = this._o; if (o.mode === v) return; o.mode = v;
    if (v !== 'disabled' && o.el && !o.loaded) loadTrackElement(o.el);
    o.dirty = true;
    var tr = tracksOf(o.owner); task(function () { fire(tr.list, 'change'); });
    reschedule(o.owner);
  });
  defGetter(TextTrack.prototype, 'cues', function () { var o = this._o; if (o.mode === 'disabled') return null; return o.cueList || (o.cueList = makeCueList(o.cues)); });
  defGetter(TextTrack.prototype, 'activeCues', function () { var o = this._o; if (o.mode === 'disabled') return null; if (!o.activeList) o.activeList = makeCueList(o.active); return o.activeList; });
  defMethod(TextTrack.prototype, 'addCue', function (c) {
    if (!(c instanceof TextTrackCue)) throw new TypeError("Failed to execute 'addCue' on 'TextTrack': parameter 1 is not of type 'TextTrackCue'.");
    var o = this._o;
    if (c._track && c._track !== this) c._track.removeCue(c);
    c._track = this; o.cues.push(c); o.cues.sort(function (a, b) { return a._start - b._start || a._end - b._end; });
    if (o.cueList) syncIdx(o.cueList); o.dirty = true; reschedule(o.owner);
  });
  defMethod(TextTrack.prototype, 'removeCue', function (c) {
    var o = this._o, i = o.cues.indexOf(c);
    if (i < 0) throw dex("Failed to execute 'removeCue' on 'TextTrack': The specified cue is not listed in the TextTrack's list of cues.", 'NotFoundError');
    o.cues.splice(i, 1); c._track = null; var j = o.active.indexOf(c); if (j >= 0) o.active.splice(j, 1);
    if (o.cueList) syncIdx(o.cueList); if (o.activeList) syncIdx(o.activeList); o.dirty = true;
  });
  defGetter(TextTrack.prototype, 'oncuechange', function () { return this._o.onch; }, function (v) {
    var self = this, o = this._o; o.onch = typeof v === 'function' ? v : null;
    if (!o.regch) { o.regch = true; this.addEventListener('cuechange', function (ev) { if (o.onch) o.onch.call(self, ev); }); }
  });

  function ListBase() { illegal(); }
  derive(ListBase, EventTarget);
  function makeList(proto, owner) {
    var l = Object.create(proto); EventTarget.call(l);
    Object.defineProperty(l, '_a', { value: [] }); Object.defineProperty(l, '_n', { value: 0, writable: true, configurable: true });
    Object.defineProperty(l, '_owner', { value: owner });
    return l;
  }
  function listSync(l) {
    var a = l._a;
    for (var i = 0; i < a.length; i++) Object.defineProperty(l, i, { value: a[i], configurable: true, enumerable: true });
    for (var j = a.length; j < l._n; j++) delete l[j];
    l._n = a.length;
  }
  defGetter(ListBase.prototype, 'length', function () { return this._a.length; });
  defMethod(ListBase.prototype, 'getTrackById', function (id) { id = String(id); for (var i = 0; i < this._a.length; i++) if (this._a[i].id === id) return this._a[i]; return null; });
  ListBase.prototype[Symbol.iterator] = function () { return this._a[Symbol.iterator](); };
  ['addtrack', 'removetrack', 'change'].forEach(function (n) {
    defGetter(ListBase.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) {
      if (!this._h) Object.defineProperty(this, '_h', { value: {} });
      var self = this; if (!this._h['r' + n]) { this._h['r' + n] = true; this.addEventListener(n, function (ev) { var f = self._h[n]; if (typeof f === 'function') f.call(self, ev); }); }
      this._h[n] = typeof v === 'function' ? v : null;
    });
  });
  function TextTrackList() { illegal(); } derive(TextTrackList, ListBase); tag(TextTrackList, 'TextTrackList'); global.TextTrackList = TextTrackList;
  function AudioTrackList() { illegal(); } derive(AudioTrackList, ListBase); tag(AudioTrackList, 'AudioTrackList'); global.AudioTrackList = AudioTrackList;
  function VideoTrackList() { illegal(); } derive(VideoTrackList, ListBase); tag(VideoTrackList, 'VideoTrackList'); global.VideoTrackList = VideoTrackList;
  defGetter(VideoTrackList.prototype, 'selectedIndex', function () { for (var i = 0; i < this._a.length; i++) if (this._a[i].selected) return i; return -1; });
  function AudioTrack() { illegal(); } tag(AudioTrack, 'AudioTrack'); global.AudioTrack = AudioTrack;
  function VideoTrack() { illegal(); } tag(VideoTrack, 'VideoTrack'); global.VideoTrack = VideoTrack;
  function mkAV(proto, id, kind, label, lang, flag, flagName, list) {
    var t = Object.create(proto);
    Object.defineProperty(t, '_v', { value: { id: id, kind: kind, label: label, language: lang, flag: flag } });
    ['id', 'kind', 'label', 'language'].forEach(function (k) { defGetter(t, k, function () { return t._v[k]; }); });
    defGetter(t, flagName, function () { return t._v.flag; }, function (v) {
      v = !!v; if (t._v.flag === v) return;
      if (flagName === 'selected' && v) list._a.forEach(function (o) { if (o !== t) o._v.flag = false; });
      t._v.flag = v; task(function () { fire(list, 'change'); });
    });
    defGetter(t, 'sourceBuffer', function () { return null; });
    return t;
  }

  function tracksOf(el) {
    var s = st(el);
    if (s.tracks) return s.tracks;
    var tr = s.tracks = { list: makeList(TextTrackList.prototype, el), audio: makeList(AudioTrackList.prototype, el), video: makeList(VideoTrackList.prototype, el), byEl: new Map() };
    tr.add = function (kind, label, lang, mode, trackEl) {
      if (!/^(subtitles|captions|descriptions|chapters|metadata)$/.test(kind)) throw new TypeError("Failed to execute 'addTextTrack' on 'HTMLMediaElement': The provided value '" + kind + "' is not a valid enum value of type TextTrackKind.");
      var t = newTextTrack(el, kind, label, lang, mode, trackEl);
      tr.list._a.push(t); listSync(tr.list);
      task(function () { var ev = new Event('addtrack'); ev.track = t; tr.list.dispatchEvent(ev); });
      return t;
    };
    // existing <track> children
    for (var c = el.firstChild; c; c = c.nextSibling) if (c.nodeType === 1 && c.localName === 'track') attachTrackEl(el, c);
    return tr;
  }
  function attachTrackEl(media, tel) {
    var tr = st(media).tracks; if (!tr || tr.byEl.has(tel)) return;
    var kind = (tel.getAttribute('kind') || 'subtitles').toLowerCase();
    if (!/^(subtitles|captions|descriptions|chapters|metadata)$/.test(kind)) kind = 'metadata';
    var def = tel.hasAttribute('default');
    var mode = 'disabled';
    if (def) {
      var other = tr.list._a.some(function (t) { return t._o.mode === 'showing' && t._o.el; });
      mode = (kind === 'subtitles' || kind === 'captions') && !other ? 'showing' : 'hidden';
    }
    var t = tr.add(kind, tel.getAttribute('label') || '', tel.getAttribute('srclang') || '', 'disabled', tel);
    tr.byEl.set(tel, t);
    tel.__swTrack = t;
    if (mode !== 'disabled') task(function () { t._o.mode = mode; t._o.dirty = true; loadTrackElement(tel); reschedule(media); });
  }
  function detachTrackEl(media, tel) {
    var tr = st(media).tracks; if (!tr) return;
    var t = tr.byEl.get(tel); if (!t) return;
    tr.byEl.delete(tel);
    var i = tr.list._a.indexOf(t); if (i >= 0) { tr.list._a.splice(i, 1); listSync(tr.list); task(function () { var ev = new Event('removetrack'); ev.track = t; tr.list.dispatchEvent(ev); }); }
  }
  function loadTrackElement(tel) {
    var t = tel.__swTrack; if (!t || t._o.loaded) return;
    t._o.loaded = true; t._o.readyState = 1;
    var raw = tel.getAttribute('src');
    var fail = function () { t._o.readyState = 3; fire(tel, 'error'); };
    if (!raw) { return fail(); }
    task(function () {
      var url = resolveURLFor(tel, raw), body = null;
      try {
        if (/^data:/.test(url)) { var r0 = sw.http('GET', url, {}, ''); body = r0.body; } else { var r = sw.http('GET', url, {}, ''); if (r.status !== 200) return fail(); body = r.body; }
      } catch (e) { return fail(); }
      var parsed;
      try { parsed = parseCueText(body); } catch (e) { return fail(); }
      parsed.cues.forEach(function (c) {
        var cue = new VTTCue(c.start, c.end, c.text); cue._id = c.id;
        applyCueSettings(cue, c.settings);
        cue._track = t; t._o.cues.push(cue);
      });
      t._o.cues.sort(function (a, b) { return a._start - b._start || a._end - b._end; });
      if (t._o.cueList) syncIdx(t._o.cueList);
      t._o.readyState = 2; t._o.dirty = true;
      fire(tel, 'load');
      reschedule(t._o.owner);
    });
  }
  function applyCueSettings(cue, str) {
    str.split(/\s+/).forEach(function (kv) {
      var p = kv.split(':'); if (p.length !== 2) return;
      var k = p[0], v = p[1];
      try {
        if (k === 'vertical' && (v === 'rl' || v === 'lr')) cue._vertical = v;
        else if (k === 'line') { var lp = v.split(','); var n = parseFloat(lp[0]); if (!isNaN(n)) { cue._line = n; if (/%$/.test(lp[0])) cue._snap = false; } if (lp[1]) cue._lineAlign = lp[1]; }
        else if (k === 'position') { var pp = v.split(','); var q = parseFloat(pp[0]); if (!isNaN(q)) cue._pos = q; if (pp[1]) cue._posAlign = pp[1]; }
        else if (k === 'size') { var z = parseFloat(v); if (!isNaN(z)) cue._size = z; }
        else if (k === 'align') cue._align = v === 'middle' ? 'center' : v;
      } catch (e) { /* ignore */ }
    });
  }

  // HTMLTrackElement
  var TP = global.HTMLTrackElement.prototype;
  [['NONE', 0], ['LOADING', 1], ['LOADED', 2], ['ERROR', 3]].forEach(function (c) { Object.defineProperty(global.HTMLTrackElement, c[0], { value: c[1], enumerable: true }); Object.defineProperty(TP, c[0], { value: c[1], enumerable: true }); });
  defGetter(TP, 'kind', function () { var k = (this.getAttribute('kind') || 'subtitles').toLowerCase(); return /^(subtitles|captions|descriptions|chapters|metadata)$/.test(k) ? k : (this.hasAttribute('kind') ? 'metadata' : 'subtitles'); }, function (v) { this.setAttribute('kind', String(v)); });
  [TP, global.HTMLSourceElement.prototype].forEach(function (proto) { // plain reflection: the base src setter fires bogus load events
    defGetter(proto, 'src', function () { var v = this.getAttribute('src'); if (v === null) return ''; try { return new global.URL(v, global.document.baseURI || global.location.href).href; } catch (e) { return v; } }, function (v) { this.setAttribute('src', String(v)); });
  });
  defGetter(TP, 'srclang', function () { return this.getAttribute('srclang') || ''; }, function (v) { this.setAttribute('srclang', String(v)); });
  defGetter(TP, 'label', function () { return this.getAttribute('label') || ''; }, function (v) { this.setAttribute('label', String(v)); });
  defGetter(TP, 'default', boolAttr('default').get, boolAttr('default').set);
  defGetter(TP, 'readyState', function () { return this.__swTrack ? (this.__swTrack._o.readyState || 0) : 0; });
  defGetter(TP, 'track', function () { var p = this.parentNode; if (p && isMedia(p)) tracksOf(p); if (!this.__swTrack && p && isMedia(p)) attachTrackEl(p, this); return this.__swTrack || null; });

  function populateTracks(el, s) {
    var tr = tracksOf(el), info = s.info;
    tr.audio._a.length = 0; tr.video._a.length = 0;
    var ai = 0, vi = 0, selV = false;
    info.tracks.forEach(function (t) {
      if (t.kind === 'audio') {
        var a = mkAV(AudioTrack.prototype, String(t.id || ai), ai === 0 ? 'main' : 'alternative', t.name || '', t.language || '', ai === 0, 'enabled', tr.audio);
        tr.audio._a.push(a); ai++;
      } else if (t.kind === 'video') {
        var v = mkAV(VideoTrack.prototype, String(t.id || vi), vi === 0 ? 'main' : 'alternative', t.name || '', t.language || '', !selV, 'selected', tr.video);
        selV = true; tr.video._a.push(v); vi++;
      }
    });
    listSync(tr.audio); listSync(tr.video);
  }

  // cue timing ----------------------------------------------------------------------------------------------
  function activeTracks(s) {
    if (!s.tracks) return [];
    return s.tracks.list._a.filter(function (t) { return t._o.mode !== 'disabled'; });
  }
  function nextCueBoundary(el, s) {
    var ts = activeTracks(s); if (!ts.length) return Infinity;
    var t = curTime(s), best = Infinity;
    ts.forEach(function (tr) { tr._o.cues.forEach(function (c) { if (c._start > t && c._start - t < best) best = c._start - t; if (c._end > t && c._end - t < best) best = c._end - t; }); });
    return best;
  }
  function cuesSeek(el) { var s = st(el); if (s.tracks) updateCues(el, s, curTime(s)); }
  function updateCues(el, s, t) {
    if (!s.tracks) return;
    s.tracks.list._a.forEach(function (tr) {
      var o = tr._o; if (o.mode === 'disabled') return;
      var now = o.cues.filter(function (c) { return c._start <= t && t < c._end; });
      var prev = o.active;
      var same = prev.length === now.length && prev.every(function (c, i) { return c === now[i]; });
      if (same) return;
      var exits = prev.filter(function (c) { return now.indexOf(c) < 0; });
      var enters = now.filter(function (c) { return prev.indexOf(c) < 0; });
      o.active.length = 0; now.forEach(function (c) { o.active.push(c); });
      if (o.activeList) syncIdx(o.activeList);
      var evs = [];
      exits.forEach(function (c) { evs.push(function () { fire(c, 'exit'); }); });
      enters.forEach(function (c) { evs.push(function () { fire(c, 'enter'); }); });
      evs.push(function () { fire(tr, 'cuechange'); });
      task(function () { evs.forEach(function (f) { f(); }); });
      exits.forEach(function (c) { if (c._pause && !s.paused) { doPause(el, s, false); } });
    });
  }

  // ======================= MediaStream ====================================================
  function uuid() { try { return global.crypto.randomUUID(); } catch (e) { return String(Math.random()).slice(2) + String(Date.now()); } }
  function MediaStreamTrack() { illegal(); }
  derive(MediaStreamTrack, EventTarget); tag(MediaStreamTrack, 'MediaStreamTrack');
  // Tracks made by getUserMedia / getDisplayMedia carry _t.cap = true and live in Go (capture_host.go): their
  // settings and capabilities are what the device really delivers, and stop()/enabled/applyConstraints reach it.
  var capTracks = {};
  global.__swMakeTrack = function (kind, label, extra) {
    var t = Object.create(MediaStreamTrack.prototype); EventTarget.call(t);
    Object.defineProperty(t, '_t', { value: { kind: kind, label: label || '', id: uuid(), enabled: true, ready: 'live', extra: extra || {}, hint: '', cons: {} } });
    return t;
  };
  function capTrack(info, cons) {
    var t = Object.create(MediaStreamTrack.prototype); EventTarget.call(t);
    Object.defineProperty(t, '_t', { value: { kind: info.kind, label: info.label || '', id: info.id, enabled: true, ready: 'live', cap: true, settings: info.settings || {}, caps: info.capabilities || {}, extra: {}, hint: '', cons: cons || {}, source: info.source } });
    capTracks[info.id] = t;
    return t;
  }
  function endTrack(t, fire) {
    if (t._t.ready === 'ended') return;
    t._t.ready = 'ended'; delete capTracks[t._t.id];
    if (fire) { var ev = new Event('ended'); t.dispatchEvent(ev); }
  }
  function copyJSON(o) { try { return JSON.parse(JSON.stringify(o)); } catch (e) { return {}; } }
  [['kind', 'kind'], ['label', 'label'], ['id', 'id'], ['readyState', 'ready']].forEach(function (p) { defGetter(MediaStreamTrack.prototype, p[0], function () { return this._t[p[1]]; }); });
  defGetter(MediaStreamTrack.prototype, 'enabled', function () { return this._t.enabled; }, function (v) { v = !!v; this._t.enabled = v; if (this._t.cap && this._t.ready === 'live') { try { sw.capEnable(this._t.id, v); } catch (e) { /* ignore */ } } });
  defGetter(MediaStreamTrack.prototype, 'muted', function () { return false; });
  defGetter(MediaStreamTrack.prototype, 'contentHint', function () { return this._t.hint; }, function (v) { this._t.hint = String(v); });
  defGetter(MediaStreamTrack.prototype, 'isolated', function () { return false; });
  defMethod(MediaStreamTrack.prototype, 'stop', function () {
    var t = this._t; if (t.ready === 'ended') return;
    t.ready = 'ended';
    if (t.cap) { delete capTracks[t.id]; try { sw.capStop(t.id); } catch (e) { /* ignore */ } }
  });
  defMethod(MediaStreamTrack.prototype, 'clone', function () {
    var t = this._t;
    if (t.cap) {
      var info = t.ready === 'live' ? sw.capClone(t.id) : null;
      if (!info) { var dead = global.__swMakeTrack(t.kind, t.label, {}); dead._t.ready = 'ended'; return dead; }
      var c = capTrack(info, copyJSON(t.cons)); c._t.enabled = t.enabled; return c;
    }
    var c2 = global.__swMakeTrack(t.kind, t.label, t.extra); c2._t.enabled = t.enabled; return c2;
  });
  defMethod(MediaStreamTrack.prototype, 'getSettings', function () {
    var t = this._t;
    if (t.cap) return copyJSON(t.settings);
    if (t.kind === 'audio') return { deviceId: t.extra.deviceId || '', groupId: '', sampleRate: 48000, channelCount: 1, echoCancellation: true, autoGainControl: true, noiseSuppression: true, latency: 0.01, sampleSize: 16 };
    return { deviceId: t.extra.deviceId || '', groupId: '', width: t.extra.width || 640, height: t.extra.height || 480, frameRate: t.extra.frameRate || 30, aspectRatio: (t.extra.width || 640) / (t.extra.height || 480), facingMode: 'user', resizeMode: 'none' };
  });
  defMethod(MediaStreamTrack.prototype, 'getCapabilities', function () { return this._t.cap ? copyJSON(this._t.caps) : {}; });
  defMethod(MediaStreamTrack.prototype, 'getConstraints', function () { return copyJSON(this._t.cons); });
  defMethod(MediaStreamTrack.prototype, 'applyConstraints', function (c) {
    var t = this._t, self = this;
    if (c !== undefined && (c === null || typeof c !== 'object')) return Promise.reject(new TypeError("Failed to execute 'applyConstraints' on 'MediaStreamTrack': The provided value is not of type 'MediaTrackConstraints'."));
    c = c || {};
    if (!t.cap) { t.cons = copyJSON(c); return Promise.resolve(); }
    if (t.ready !== 'live') return Promise.reject(dex('The track has ended', 'InvalidStateError'));
    return capCall('apply', { id: t.id, constraints: c }).then(function (r) { t.settings = r.settings; t.cons = copyJSON(c); });
  });
  ['ended', 'mute', 'unmute'].forEach(function (n) { defGetter(MediaStreamTrack.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
  global.MediaStreamTrack = MediaStreamTrack;

  function MediaStream(init) {
    if (!(this instanceof MediaStream)) throw new TypeError("Failed to construct 'MediaStream': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    EventTarget.call(this);
    var tracks = [];
    if (init instanceof MediaStream) tracks = init.getTracks().slice();
    else if (init && typeof init[Symbol.iterator] === 'function') tracks = Array.from(init);
    Object.defineProperty(this, '_s', { value: { id: uuid(), tracks: [] } });
    var self = this; tracks.forEach(function (t) { if (self._s.tracks.indexOf(t) < 0) self._s.tracks.push(t); });
  }
  derive(MediaStream, EventTarget); tag(MediaStream, 'MediaStream');
  defGetter(MediaStream.prototype, 'id', function () { return this._s.id; });
  defGetter(MediaStream.prototype, 'active', function () { return this._s.tracks.some(function (t) { return t.readyState === 'live'; }); });
  defMethod(MediaStream.prototype, 'getTracks', function () { return this._s.tracks.slice(); });
  defMethod(MediaStream.prototype, 'getAudioTracks', function () { return this._s.tracks.filter(function (t) { return t.kind === 'audio'; }); });
  defMethod(MediaStream.prototype, 'getVideoTracks', function () { return this._s.tracks.filter(function (t) { return t.kind === 'video'; }); });
  defMethod(MediaStream.prototype, 'getTrackById', function (id) { return this._s.tracks.filter(function (t) { return t.id === String(id); })[0] || null; });
  defMethod(MediaStream.prototype, 'addTrack', function (t) { if (this._s.tracks.indexOf(t) < 0) { this._s.tracks.push(t); var ev = new Event('addtrack'); ev.track = t; this.dispatchEvent(ev); } });
  defMethod(MediaStream.prototype, 'removeTrack', function (t) { var i = this._s.tracks.indexOf(t); if (i >= 0) { this._s.tracks.splice(i, 1); var ev = new Event('removetrack'); ev.track = t; this.dispatchEvent(ev); } });
  defMethod(MediaStream.prototype, 'clone', function () { return new MediaStream(this._s.tracks.map(function (t) { return t.clone(); })); });
  ['addtrack', 'removetrack', 'active', 'inactive'].forEach(function (n) { defGetter(MediaStream.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
  global.MediaStream = MediaStream;

  // ======================= mediaDevices / permissions / EME / recorder / speech ==================
  // Requests go to Go (capture_host.go) and come back as __swCapEvent. Promises wait in capWait.
  var capSeq = 0, capWait = {};
  function capCall(kind, payload) {
    return new Promise(function (resolve, reject) {
      var tok = ++capSeq; capWait[tok] = { resolve: resolve, reject: reject };
      try { sw.capRequest(tok, kind, JSON.stringify(payload)); } catch (e) { delete capWait[tok]; reject(dex(String(e && e.message || e), 'NotReadableError')); }
    });
  }
  function OverconstrainedError(constraint, message) {
    if (!(this instanceof OverconstrainedError)) throw new TypeError("Failed to construct 'OverconstrainedError': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    var e = dex(message === undefined ? '' : String(message), 'OverconstrainedError');
    Object.setPrototypeOf(e, OverconstrainedError.prototype);
    Object.defineProperty(e, 'constraint', { value: String(constraint), enumerable: true, configurable: true });
    return e;
  }
  OverconstrainedError.prototype = Object.create(global.DOMException.prototype, { constructor: { value: OverconstrainedError, writable: true, configurable: true } });
  tag(OverconstrainedError, 'OverconstrainedError');
  global.OverconstrainedError = OverconstrainedError;
  function capError(d) {
    if (d.name === 'OverconstrainedError') return new OverconstrainedError(d.constraint, d.message);
    if (d.name === 'TypeError') return new TypeError(d.message);
    return dex(d.message, d.name);
  }
  var devices;
  global.__swCapEvent = function (id, type, data) {
    var d = data ? JSON.parse(data) : null;
    if (type === 'ok' || type === 'err') {
      var w = capWait[id]; delete capWait[id]; if (!w) return;
      if (type === 'ok') w.resolve(d); else w.reject(capError(d));
    } else if (type === 'end') {
      var t = capTracks[d.id]; if (t) endTrack(t, d.reason !== 'stopped');
    } else if (type === 'devicechange') {
      if (devices) devices.dispatchEvent(new Event('devicechange'));
    } else if (type === 'perm') {
      permRefresh();
    }
  };

  function MediaDeviceInfo() { illegal(); } tag(MediaDeviceInfo, 'MediaDeviceInfo');
  ['deviceId', 'kind', 'label', 'groupId'].forEach(function (n) { defGetter(MediaDeviceInfo.prototype, n, function () { return this._d[n]; }); });
  defMethod(MediaDeviceInfo.prototype, 'toJSON', function () { return { deviceId: this.deviceId, kind: this.kind, label: this.label, groupId: this.groupId }; });
  function InputDeviceInfo() { illegal(); } derive(InputDeviceInfo, MediaDeviceInfo); tag(InputDeviceInfo, 'InputDeviceInfo');
  defMethod(InputDeviceInfo.prototype, 'getCapabilities', function () { return {}; });
  global.MediaDeviceInfo = MediaDeviceInfo; global.InputDeviceInfo = InputDeviceInfo;
  function deviceInfo(d) {
    var o = Object.create(d.kind === 'audiooutput' ? MediaDeviceInfo.prototype : InputDeviceInfo.prototype);
    Object.defineProperty(o, '_d', { value: d });
    return o;
  }

  function streamOf(r) {
    var ms = new MediaStream();
    r.tracks.forEach(function (info) { ms.addTrack(capTrack(info, {})); });
    return ms;
  }
  function wantsAny(c) { return c && (c.audio || c.video); }
  function constraintsArg(c, fn) {
    if (c === undefined || c === null || typeof c !== 'object') throw new TypeError("Failed to execute '" + fn + "' on 'MediaDevices': The provided value is not of type 'MediaStreamConstraints'.");
  }
  function surfaceHint(v) {
    if (!v || typeof v !== 'object') return '';
    var s = v.displaySurface; if (s && typeof s === 'object') s = s.exact || s.ideal;
    if (Array.isArray(s)) s = s[0];
    return typeof s === 'string' ? s : '';
  }
  devices = (function () {
    var d = Object.create(EventTarget.prototype); EventTarget.call(d);
    defMethod(d, 'enumerateDevices', function () {
      try { return Promise.resolve(JSON.parse(sw.capEnumerate()).map(deviceInfo)); } catch (e) { return Promise.resolve([]); }
    });
    defMethod(d, 'getSupportedConstraints', function () { return { aspectRatio: true, autoGainControl: true, channelCount: true, deviceId: true, displaySurface: true, echoCancellation: true, facingMode: true, frameRate: true, groupId: true, height: true, latency: true, noiseSuppression: true, resizeMode: true, sampleRate: true, sampleSize: true, width: true }; });
    defMethod(d, 'getUserMedia', function (c) {
      try { constraintsArg(c, 'getUserMedia'); } catch (e) { return Promise.reject(e); }
      if (!wantsAny(c)) return Promise.reject(new TypeError("Failed to execute 'getUserMedia' on 'MediaDevices': At least one of audio and video must be requested"));
      return capCall('gum', { audio: c.audio === undefined ? false : c.audio, video: c.video === undefined ? false : c.video }).then(streamOf);
    });
    defMethod(d, 'getDisplayMedia', function (c) {
      c = c === undefined ? { video: true } : c;
      try { constraintsArg(c, 'getDisplayMedia'); } catch (e) { return Promise.reject(e); }
      var v = c.video === undefined ? true : c.video;
      if (v === false || v === null) return Promise.reject(new TypeError("Failed to execute 'getDisplayMedia' on 'MediaDevices': Audio only requests are not supported"));
      return capCall('gdm', { video: v, audio: c.audio === undefined ? false : c.audio, preferCurrentTab: !!c.preferCurrentTab, surface: surfaceHint(v) }).then(streamOf);
    });
    var handler = null, wired = false;
    defGetter(d, 'ondevicechange', function () { return handler; }, function (f) {
      handler = typeof f === 'function' ? f : null;
      if (!wired) { wired = true; d.addEventListener('devicechange', function (e) { if (handler) handler.call(d, e); }); }
    });
    var add = d.addEventListener;
    defMethod(d, 'addEventListener', function (type) { add.apply(this, arguments); if (type === 'devicechange') { try { sw.capWatch(true); } catch (e) { /* ignore */ } } });
    return d;
  })();
  function MediaDevices() { illegal(); }
  Object.setPrototypeOf(devices, MediaDevices.prototype); derive(MediaDevices, EventTarget); tag(MediaDevices, 'MediaDevices');
  Object.setPrototypeOf(MediaDevices.prototype, EventTarget.prototype);
  ['enumerateDevices', 'getSupportedConstraints', 'getUserMedia', 'getDisplayMedia'].forEach(function (n) { Object.defineProperty(MediaDevices.prototype, n, { value: devices[n], writable: true, configurable: true, enumerable: true }); delete devices[n]; });
  global.MediaDevices = MediaDevices;

  // ---- Permissions API for camera, microphone and display-capture (other names keep answering "prompt") ----
  var PERM = { camera: 1, microphone: 1, 'display-capture': 1 };
  var statuses = [];
  function PermissionStatus() { illegal(); } derive(PermissionStatus, EventTarget); tag(PermissionStatus, 'PermissionStatus');
  defGetter(PermissionStatus.prototype, 'state', function () { return this._p.state; });
  defGetter(PermissionStatus.prototype, 'name', function () { return this._p.name; });
  defGetter(PermissionStatus.prototype, 'onchange', function () { return this._p.onchange || null; }, function (f) {
    var p = this._p; p.onchange = typeof f === 'function' ? f : null; var self = this;
    if (!p.wired) { p.wired = true; this.addEventListener('change', function (e) { if (p.onchange) p.onchange.call(self, e); }); }
  });
  global.PermissionStatus = PermissionStatus;
  function permState(name) { if (!PERM[name]) return 'prompt'; try { return sw.capPerm(name) || 'prompt'; } catch (e) { return 'prompt'; } }
  function permRefresh() {
    statuses.slice().forEach(function (s) {
      var n = permState(s._p.name);
      if (n !== s._p.state) { s._p.state = n; s.dispatchEvent(new Event('change')); }
    });
  }
  function Permissions() { illegal(); } tag(Permissions, 'Permissions');
  defMethod(Permissions.prototype, 'query', function (desc) {
    if (desc === null || typeof desc !== 'object' || typeof desc.name !== 'string') return Promise.reject(new TypeError("Failed to execute 'query' on 'Permissions': Failed to read the 'name' property from 'PermissionDescriptor'."));
    var s = Object.create(PermissionStatus.prototype); EventTarget.call(s);
    Object.defineProperty(s, '_p', { value: { name: desc.name, state: permState(desc.name), onchange: null, wired: false } });
    if (PERM[desc.name]) { statuses.push(s); if (statuses.length > 64) statuses.shift(); }
    return Promise.resolve(s);
  });
  global.Permissions = Permissions;
  var permsObj = Object.create(Permissions.prototype);
  if (global.Navigator) {
    try { Object.defineProperty(global.Navigator.prototype, 'permissions', { get: function () { return permsObj; }, configurable: true, enumerable: true }); } catch (e) { /* keep the base stub */ }
    Object.defineProperty(global.Navigator.prototype, 'mediaDevices', { get: function () { return devices; }, configurable: true, enumerable: true });
    defMethod(global.Navigator.prototype, 'requestMediaKeySystemAccess', function () { return Promise.reject(dex('Unsupported keySystem or supportedConfigurations.', 'NotSupportedError')); });
    defMethod(global.Navigator.prototype, 'getUserMedia', function (c, ok, err) { devices.getUserMedia(c).then(ok, err); });
    defMethod(global.Navigator.prototype, 'webkitGetUserMedia', global.Navigator.prototype.getUserMedia);
  }
  if (!global.MediaRecorder) {
    var MR = function MediaRecorder() { throw dex('MediaRecorder is not supported in this browser.', 'NotSupportedError'); };
    MR.isTypeSupported = function () { return false; };
    global.MediaRecorder = MR;
  }
  // speech
  (function () {
    var synth = Object.create(EventTarget.prototype); EventTarget.call(synth);
    function SpeechSynthesisUtterance(text) {
      if (!(this instanceof SpeechSynthesisUtterance)) throw new TypeError("Failed to construct 'SpeechSynthesisUtterance': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
      EventTarget.call(this); this.text = text === undefined ? '' : String(text); this.lang = ''; this.voice = null; this.volume = 1; this.rate = 1; this.pitch = 1;
    }
    derive(SpeechSynthesisUtterance, EventTarget); tag(SpeechSynthesisUtterance, 'SpeechSynthesisUtterance');
    ['boundary', 'end', 'error', 'mark', 'pause', 'resume', 'start'].forEach(function (n) { defGetter(SpeechSynthesisUtterance.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
    global.SpeechSynthesisUtterance = SpeechSynthesisUtterance;
    function SpeechSynthesis() { illegal(); } derive(SpeechSynthesis, EventTarget); tag(SpeechSynthesis, 'SpeechSynthesis');
    Object.setPrototypeOf(synth, SpeechSynthesis.prototype);
    defMethod(SpeechSynthesis.prototype, 'getVoices', function () { return []; });
    defMethod(SpeechSynthesis.prototype, 'speak', function (u) {
      if (!(u instanceof SpeechSynthesisUtterance)) throw new TypeError("Failed to execute 'speak' on 'SpeechSynthesis': parameter 1 is not of type 'SpeechSynthesisUtterance'.");
      _setTimeout(function () { var ev = new Event('error'); ev.error = 'synthesis-unavailable'; ev.utterance = u; u.dispatchEvent(ev); }, 0);
    });
    ['cancel', 'pause', 'resume'].forEach(function (n) { defMethod(SpeechSynthesis.prototype, n, function () { }); });
    ['speaking', 'pending', 'paused'].forEach(function (n) { defGetter(SpeechSynthesis.prototype, n, function () { return false; }); });
    defGetter(SpeechSynthesis.prototype, 'onvoiceschanged', function () { return null; }, function () { });
    global.SpeechSynthesis = SpeechSynthesis;
    global.speechSynthesis = synth;
    function SpeechRecognition() {
      if (!(this instanceof SpeechRecognition)) throw new TypeError("Failed to construct 'SpeechRecognition': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
      EventTarget.call(this); this.lang = ''; this.continuous = false; this.interimResults = false; this.maxAlternatives = 1; this.grammars = null;
    }
    derive(SpeechRecognition, EventTarget); tag(SpeechRecognition, 'SpeechRecognition');
    defMethod(SpeechRecognition.prototype, 'start', function () {
      var self = this;
      _setTimeout(function () { var e = new Event('error'); e.error = 'not-allowed'; e.message = ''; self.dispatchEvent(e); self.dispatchEvent(new Event('end')); }, 0);
    });
    defMethod(SpeechRecognition.prototype, 'stop', function () { }); defMethod(SpeechRecognition.prototype, 'abort', function () { });
    ['audiostart', 'audioend', 'end', 'error', 'nomatch', 'result', 'soundstart', 'soundend', 'speechstart', 'speechend', 'start'].forEach(function (n) { defGetter(SpeechRecognition.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
    global.SpeechRecognition = SpeechRecognition; global.webkitSpeechRecognition = SpeechRecognition;
  })();

  // ======================= MediaSource Extensions =====================================================
  var MS_STATE = new WeakMap();
  function MediaSource() {
    if (!(this instanceof MediaSource)) throw new TypeError("Failed to construct 'MediaSource': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    EventTarget.call(this);
    MS_STATE.set(this, { ready: 'closed', dur: NaN, bufs: [], el: null, s: null, id: 0, seekable: null });
    var self = this;
    Object.defineProperty(this, 'sourceBuffers', { value: makeSBList(this, false), enumerable: true });
    Object.defineProperty(this, 'activeSourceBuffers', { value: makeSBList(this, true), enumerable: true });
    void self;
  }
  derive(MediaSource, EventTarget); tag(MediaSource, 'MediaSource');
  function msr(ms) { return MS_STATE.get(ms); }
  function makeSBList(ms, active) {
    var l = Object.create(SourceBufferList.prototype); EventTarget.call(l);
    Object.defineProperty(l, '_ms', { value: ms }); Object.defineProperty(l, '_active', { value: active }); Object.defineProperty(l, '_n', { value: 0, writable: true });
    return l;
  }
  function SourceBufferList() { illegal(); } derive(SourceBufferList, EventTarget); tag(SourceBufferList, 'SourceBufferList');
  function sbListItems(l) { var r = msr(l._ms); return r.bufs; }
  defGetter(SourceBufferList.prototype, 'length', function () { return sbListItems(this).length; });
  SourceBufferList.prototype[Symbol.iterator] = function () { return sbListItems(this)[Symbol.iterator](); };
  function sbSync(l) {
    var a = sbListItems(l);
    for (var i = 0; i < a.length; i++) Object.defineProperty(l, i, { value: a[i], configurable: true, enumerable: true });
    for (var j = a.length; j < l._n; j++) delete l[j];
    l._n = a.length;
  }
  ['addsourcebuffer', 'removesourcebuffer'].forEach(function (n) { defGetter(SourceBufferList.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
  ['sourceopen', 'sourceended', 'sourceclose'].forEach(function (n) { defGetter(MediaSource.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
  defGetter(MediaSource.prototype, 'readyState', function () { return msr(this).ready; });
  defGetter(MediaSource.prototype, 'duration', function () { return msr(this).dur; }, function (v) {
    v = +v; var r = msr(this);
    if (isNaN(v)) throw new TypeError("Failed to set the 'duration' property on 'MediaSource': The provided double value is non-finite.");
    if (v < 0) throw new TypeError("Failed to set the 'duration' property on 'MediaSource': The value provided (" + v + ") is less than zero.");
    if (r.ready !== 'open') throw dex("Failed to set the 'duration' property on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    if (r.bufs.some(function (b) { return b._r.updating; })) throw dex("Failed to set the 'duration' property on 'MediaSource': One or more SourceBuffers are still processing an append or remove operation.", 'InvalidStateError');
    setMSDuration(this, v);
  });
  function setMSDuration(ms, v) {
    var r = msr(ms); if (r.dur === v) return; r.dur = v;
    r.bufs.forEach(function (b) { b._truncate(v); });
    if (r.el) { var s = r.s; if (s.ready >= 1) { s.duration = v; task(function () { fire(r.el, 'durationchange'); }); } msUpdate(r.el); }
  }
  defMethod(MediaSource, 'isTypeSupported', function (t) { if (arguments.length < 1) throw new TypeError("Failed to execute 'isTypeSupported' on 'MediaSource': 1 argument required, but only 0 present."); return sw.mediaIsTypeSupported(String(t)); });
  Object.defineProperty(MediaSource, 'canConstructInDedicatedWorker', { value: true, enumerable: true });
  defMethod(MediaSource.prototype, 'addSourceBuffer', function (type) {
    var r = msr(this); type = String(type);
    if (type === '') throw new TypeError("Failed to execute 'addSourceBuffer' on 'MediaSource': The type provided ('') is empty.");
    if (!sw.mediaIsTypeSupported(type)) throw dex("Failed to execute 'addSourceBuffer' on 'MediaSource': The type provided ('" + type + "') is unsupported.", 'NotSupportedError');
    if (r.ready !== 'open') throw dex("Failed to execute 'addSourceBuffer' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    var sb = newSourceBuffer(this, type);
    r.bufs.push(sb); sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    task(function () { fire(sb._ms.sourceBuffers, 'addsourcebuffer'); });
    return sb;
  });
  defMethod(MediaSource.prototype, 'removeSourceBuffer', function (sb) {
    var r = msr(this), i = r.bufs.indexOf(sb);
    if (i < 0) throw dex("Failed to execute 'removeSourceBuffer' on 'MediaSource': The SourceBuffer provided is not contained in this MediaSource.", 'NotFoundError');
    r.bufs.splice(i, 1); sb._r.removed = true; sb._r.updating = false; try { sw.mseClose(sb._r.id); } catch (e) { /* ignore */ }
    sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    var self = this; task(function () { fire(self.sourceBuffers, 'removesourcebuffer'); });
    if (r.el) msUpdate(r.el);
  });
  defMethod(MediaSource.prototype, 'endOfStream', function (err) {
    var r = msr(this);
    if (r.ready !== 'open') throw dex("Failed to execute 'endOfStream' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    if (r.bufs.some(function (b) { return b._r.updating; })) throw dex("Failed to execute 'endOfStream' on 'MediaSource': The MediaSource's SourceBuffers are still updating.", 'InvalidStateError');
    if (err !== undefined && err !== 'network' && err !== 'decode') throw new TypeError("Failed to execute 'endOfStream' on 'MediaSource': The provided value '" + err + "' is not a valid enum value of type EndOfStreamError.");
    endStream(this, err);
  });
  function endStream(ms, err) {
    var r = msr(ms);
    r.ready = 'ended';
    task(function () { fire(ms, 'sourceended'); });
    if (!err) {
      var mx = 0; r.bufs.forEach(function (b) { b._ranges().forEach(function (x) { if (x[1] > mx) mx = x[1]; }); });
      if (isNaN(r.dur) || r.dur < mx) setMSDuration(ms, mx);
    } else if (r.el) {
      var s = r.s; s.error = makeError(err === 'network' ? 2 : 3, 'MEDIA_ELEMENT_ERROR: ' + (err === 'network' ? 'Network error' : 'Decode error')); task(function () { fire(r.el, 'error'); });
    }
    if (r.el) msUpdate(r.el);
  }
  defMethod(MediaSource.prototype, 'setLiveSeekableRange', function (a, b) { var r = msr(this); if (r.ready !== 'open') throw dex("Failed to execute 'setLiveSeekableRange' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError'); r.seekable = [+a, +b]; });
  defMethod(MediaSource.prototype, 'clearLiveSeekableRange', function () { msr(this).seekable = null; });
  defGetter(MediaSource.prototype, 'handle', function () { return undefined; });
  MediaSource.prototype._attach = function (el, s) {
    var r = msr(this);
    if (r.ready !== 'closed') return false;
    r.el = el; r.s = s; r.ready = 'open';
    var self = this;
    task(function () { fire(self, 'sourceopen'); });
    return true;
  };
  MediaSource.prototype._detach = function () {
    var r = msr(this), self = this; if (r.ready === 'closed') return;
    r.ready = 'closed'; r.el = null; r.s = null; r.dur = NaN;
    r.bufs.forEach(function (b) { b._r.removed = true; try { sw.mseClose(b._r.id); } catch (e) { /* ignore */ } });
    r.bufs.length = 0; sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    task(function () { fire(self, 'sourceclose'); });
  };
  MediaSource.prototype._buffered = function () {
    var r = msr(this), lists = r.bufs.map(function (b) { return b._ranges(); }).filter(function (l) { return l.length; });
    if (!lists.length) return [];
    var out = lists[0];
    for (var i = 1; i < lists.length; i++) out = intersect(out, lists[i]);
    if (r.ready === 'ended' && out.length) { var last = out[out.length - 1]; var mx = 0; lists.forEach(function (l) { mx = Math.max(mx, l[l.length - 1][1]); }); last[1] = Math.max(last[1], mx); }
    return out;
  };
  function intersect(a, b) {
    var o = [], i = 0, j = 0;
    while (i < a.length && j < b.length) {
      var s = Math.max(a[i][0], b[j][0]), e = Math.min(a[i][1], b[j][1]);
      if (e > s) o.push([s, e]);
      if (a[i][1] < b[j][1]) i++; else j++;
    }
    return o;
  }
  global.MediaSource = MediaSource; global.SourceBufferList = SourceBufferList;

  function SourceBuffer() { illegal(); } derive(SourceBuffer, EventTarget); tag(SourceBuffer, 'SourceBuffer'); global.SourceBuffer = SourceBuffer;
  function newSourceBuffer(ms, type) {
    var sb = Object.create(SourceBuffer.prototype); EventTarget.call(sb);
    var id = sw.mseNew(type);
    Object.defineProperty(sb, '_r', { value: { id: id, type: type, updating: false, removed: false, mode: /webm/.test(type) ? 'segments' : 'segments', tso: 0, tracks: {}, init: false, ws: 0, we: Infinity, queue: [] } });
    Object.defineProperty(sb, '_ms', { value: ms });
    return sb;
  }
  SourceBuffer.prototype._ranges = function () {
    var r = this._r, lists = Object.keys(r.tracks).map(function (k) { return r.tracks[k]; }).filter(function (l) { return l.length; });
    if (!lists.length) return [];
    var out = lists[0].map(function (x) { return [x[0], x[1]]; });
    for (var i = 1; i < lists.length; i++) out = intersect(out, lists[i]);
    return out;
  };
  SourceBuffer.prototype._truncate = function (d) { var r = this._r; Object.keys(r.tracks).forEach(function (k) { r.tracks[k] = subtract(r.tracks[k], d, Infinity); }); };
  function subtract(list, s, e) {
    var o = [];
    list.forEach(function (x) {
      if (x[1] <= s || x[0] >= e) { o.push(x); return; }
      if (x[0] < s) o.push([x[0], s]);
      if (x[1] > e) o.push([e, x[1]]);
    });
    return o;
  }
  function addRange(list, s, e) { return mergeRanges(list.concat([[s, e]])); }
  ['updatestart', 'update', 'updateend', 'error', 'abort'].forEach(function (n) { defGetter(SourceBuffer.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) { if (!this._h) Object.defineProperty(this, '_h', { value: {} }); var self = this; if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); } this._h[n] = v; }); });
  function sbCheck(sb, op) {
    var r = sb._r;
    if (r.removed) throw dex("Failed to execute '" + op + "' on 'SourceBuffer': This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    if (r.updating) throw dex("Failed to execute '" + op + "' on 'SourceBuffer': This SourceBuffer is still processing an 'appendBuffer' or 'remove' operation.", 'InvalidStateError');
  }
  defGetter(SourceBuffer.prototype, 'updating', function () { return this._r.updating; });
  defGetter(SourceBuffer.prototype, 'buffered', function () {
    if (this._r.removed) throw dex("Failed to read the 'buffered' property from 'SourceBuffer': This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    return makeRanges(this._ranges());
  });
  defGetter(SourceBuffer.prototype, 'mode', function () { return this._r.mode; }, function (v) { sbCheck(this, 'mode'); if (v === 'segments' || v === 'sequence') this._r.mode = v; });
  defGetter(SourceBuffer.prototype, 'timestampOffset', function () { return this._r.tso; }, function (v) { sbCheck(this, 'timestampOffset'); this._r.tso = +v; });
  defGetter(SourceBuffer.prototype, 'appendWindowStart', function () { return this._r.ws; }, function (v) { sbCheck(this, 'appendWindowStart'); v = +v; if (v < 0 || v >= this._r.we) throw new TypeError("Failed to set the 'appendWindowStart' property on 'SourceBuffer': The value provided (" + v + ") is outside the range."); this._r.ws = v; });
  defGetter(SourceBuffer.prototype, 'appendWindowEnd', function () { return this._r.we; }, function (v) { sbCheck(this, 'appendWindowEnd'); v = +v; if (!(v > this._r.ws)) throw new TypeError("Failed to set the 'appendWindowEnd' property on 'SourceBuffer': The value provided (" + v + ") must be greater than appendWindowStart."); this._r.we = v; });
  defGetter(SourceBuffer.prototype, 'audioTracks', function () { return undefined; });
  defGetter(SourceBuffer.prototype, 'videoTracks', function () { return undefined; });
  defGetter(SourceBuffer.prototype, 'textTracks', function () { return undefined; });
  defMethod(SourceBuffer.prototype, 'changeType', function (type) {
    sbCheck(this, 'changeType');
    if (!sw.mediaIsTypeSupported(String(type))) throw dex("Failed to execute 'changeType' on 'SourceBuffer': The type provided ('" + type + "') is not supported.", 'NotSupportedError');
    var r = this._r; try { sw.mseClose(r.id); } catch (e) { /* ignore */ } r.id = sw.mseNew(String(type)); r.type = String(type); r.init = false;
  });
  function bytesOf(d) {
    if (d instanceof ArrayBuffer) return new Uint8Array(d);
    if (ArrayBuffer.isView(d)) return new Uint8Array(d.buffer, d.byteOffset, d.byteLength);
    throw new TypeError("Failed to execute 'appendBuffer' on 'SourceBuffer': The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
  }
  defMethod(SourceBuffer.prototype, 'appendBuffer', function (data) {
    var sb = this, r = sb._r, ms = sb._ms, mr = msr(ms);
    if (arguments.length < 1) throw new TypeError("Failed to execute 'appendBuffer' on 'SourceBuffer': 1 argument required, but only 0 present.");
    var bytes = bytesOf(data);
    sbCheck(sb, 'appendBuffer');
    if (mr.ready === 'closed') throw dex("Failed to execute 'appendBuffer' on 'SourceBuffer': The MediaSource's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    if (mr.ready === 'ended') { mr.ready = 'open'; task(function () { fire(ms, 'sourceopen'); }); }
    r.updating = true;
    var payload = b64OfBytes(bytes);
    task(function () { fire(sb, 'updatestart'); });
    task(function () { _setTimeout(function () { task(processAppend); }, 0); });
    var processAppend = function () {
      if (r.removed) return;
      var res;
      try { res = sw.mseAppend(r.id, payload); } catch (e) { res = { ok: false, error: String(e && e.message || e) }; }
      r.updating = false;
      if (!res.ok) {
        fire(sb, 'error'); fire(sb, 'updateend');
        if (mr.ready === 'open') endStream(ms, 'decode');
        return;
      }
      if (res.init) {
        r.init = true;
        res.init.tracks.forEach(function (t) { if (!r.tracks[t.id]) r.tracks[t.id] = []; });
        if (mr.s && !mr.s.mseInit) { mr.s.mseInit = res.init; }
        if (isNaN(mr.dur) && res.init.duration > 0) { mr.dur = res.init.duration; }
        else if (isNaN(mr.dur)) mr.dur = Infinity;
        r.initInfo = res.init;
      }
      (res.spans || []).forEach(function (sp) {
        var s0 = sp[1] + r.tso, e0 = sp[2] + r.tso;
        if (e0 <= r.ws || s0 >= r.we) return;
        r.tracks[sp[0]] = addRange(r.tracks[sp[0]] || [], Math.max(s0, r.ws), Math.min(e0, r.we));
      });
      if (!isNaN(mr.dur) && isFinite(mr.dur)) { var mx = 0; sb._ranges().forEach(function (x) { mx = Math.max(mx, x[1]); }); if (mx > mr.dur && mr.ready === 'open') setMSDuration(ms, mx); }
      fire(sb, 'update'); fire(sb, 'updateend');
      if (mr.el) msUpdate(mr.el);
    };
  });
  defMethod(SourceBuffer.prototype, 'appendBufferAsync', undefined); delete SourceBuffer.prototype.appendBufferAsync;
  defMethod(SourceBuffer.prototype, 'abort', function () {
    var r = this._r, mr = msr(this._ms), sb = this;
    if (r.removed) throw dex("Failed to execute 'abort' on 'SourceBuffer': This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    if (mr.ready !== 'open') throw dex("Failed to execute 'abort' on 'SourceBuffer': The parent media source's readyState is not 'open'.", 'InvalidStateError');
    if (r.updating) { r.updating = false; task(function () { fire(sb, 'abort'); fire(sb, 'updateend'); }); }
    r.ws = 0; r.we = Infinity;
  });
  defMethod(SourceBuffer.prototype, 'remove', function (start, end) {
    var sb = this, r = sb._r, mr = msr(sb._ms);
    start = +start; end = +end;
    sbCheck(sb, 'remove');
    if (mr.ready === 'closed') throw dex("Failed to execute 'remove' on 'SourceBuffer': The MediaSource's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    if (isNaN(start) || start < 0 || start === Infinity) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': The start provided (" + start + ") is either negative or greater than the current duration (" + mr.dur + ").");
    if (isNaN(end) || end <= start) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': The end provided (" + end + ") is less than or equal to the start (" + start + ").");
    r.updating = true;
    task(function () { fire(sb, 'updatestart'); });
    task(function () {
      if (r.removed) return;
      Object.keys(r.tracks).forEach(function (k) { r.tracks[k] = subtract(r.tracks[k], start, end); });
      r.updating = false; fire(sb, 'update'); fire(sb, 'updateend');
      if (mr.el) msUpdate(mr.el);
    });
  });

  function attachMSE(el, s, id, ms) {
    var ok = ms._attach(el, s);
    if (!ok) { s.error = makeError(4, 'MEDIA_ELEMENT_ERROR: Format error'); s.net = 3; ev(el, 'error'); return; }
    s.mse = { ms: ms, detach: function () { ms._detach(); }, bufferedUnion: function () { return ms._buffered(); } };
    s.info = { container: 'mse', duration: NaN, tracks: [] };
    s.net = 2; s.frameMode = '';
  }
  // recompute duration/readyState from buffered data
  function msUpdate(el) {
    var s = st(el), ms = s.mse && s.mse.ms; if (!ms) return;
    var r = msr(ms);
    if (s.ready === 0) {
      var init = null; r.bufs.forEach(function (b) { if (b._r.initInfo && !init) init = b._r.initInfo; });
      var allInit = r.bufs.length > 0 && r.bufs.every(function (b) { return b._r.init; });
      if (!allInit) return;
      var tracks = []; r.bufs.forEach(function (b) { tracks = tracks.concat(b._r.initInfo.tracks); });
      s.info = { container: 'mse', duration: r.dur, tracks: tracks };
      s.duration = isNaN(r.dur) ? Infinity : r.dur;
      var v = null; tracks.forEach(function (t) { if (t.kind === 'video' && !v) v = t; });
      s.video = v; s.videoW = v ? v.width : 0; s.videoH = v ? v.height : 0; s.fps = v && v.frameRate > 0 ? v.frameRate : 30;
      s.net = 1; if (s.mseMetaPending) return; s.mseMetaPending = true;
      var vbuf = null; r.bufs.forEach(function (b) { if (!vbuf && b._r.initInfo && b._r.initInfo.tracks.some(function (t) { return t.kind === 'video'; })) vbuf = b; });
      if (v && vbuf && isVideo(el)) { try { s.node = global.__swIdOf(el); sw.mseBind(s.node, vbuf._r.id, s.videoW, s.videoH); s.mseFrameBuf = vbuf._r.id; } catch (e) { /* ignore */ } }
      task(function () { fire(el, 'durationchange'); s.ready = 1; populateTracks(el, s); if (v) fire(el, 'resize'); fire(el, 'loadedmetadata'); msUpdate(el); });
      return;
    }
    if (s.ready < 1) return;
    var b = ms._buffered(), t = curTime(s), target = 1, i;
    for (i = 0; i < b.length; i++) {
      if (t >= b[i][0] - 0.1 && t <= b[i][1] + 1e-6) {
        var ahead = b[i][1] - t;
        var atEnd = r.ready === 'ended' && isFinite(s.duration) && b[i][1] >= s.duration - 0.01;
        if (atEnd || ahead >= 3) target = 4; else if (ahead > 0.1) target = 3; else target = 2;
        if (atEnd && ahead <= 0) target = 4;
        break;
      }
    }
    if (target < 2 && s.loadedData) target = Math.min(target, 1);
    if (target >= 2 && s.ready < 2) { s.ready = 2; qevAny(el, 'loadeddata'); s.loadedData = true; requestFrame(el, s, true); }
    if (target !== s.ready) setReady(el, Math.max(1, target)); else reschedule(el);
  }
  // starvation check on tick for MSE playback
  var origTick = tick;
  tick = function (el) { var s = st(el); if (s.mse && s.ready >= 2) { msUpdate(el); } origTick(el); };

  // ======================= adoption of parser-created elements ===============================================
  function adopt(el) {
    var s = st(el);
    if (isVideo(el)) { try { s.node = global.__swIdOf(el); } catch (e) { /* ignore */ } }
    var hasTracks = false;
    for (var c = el.firstChild; c; c = c.nextSibling) if (c.nodeType === 1 && c.localName === 'track') hasTracks = true;
    if (hasTracks) tracksOf(el);
    if (!s.adopted && hasSource(el)) loadAlgorithm(el);
    else if (isVideo(el) && !s.adopted && el.hasAttribute('poster')) { bindFrames(el, 0); }
  }
  function adoptTree(root) {
    if (!root || root.nodeType !== 1) return;
    if (isMedia(root)) adopt(root);
    if (root.querySelectorAll) { var l = root.querySelectorAll('video,audio'); for (var i = 0; i < l.length; i++) adopt(l[i]); }
  }
  function onMutations(records) {
    for (var i = 0; i < records.length; i++) {
      var r = records[i];
      if (r.type !== 'childList') continue;
      var tgt = r.target;
      for (var j = 0; j < r.addedNodes.length; j++) {
        var n = r.addedNodes[j];
        if (n.nodeType !== 1) continue;
        if (isMedia(tgt)) {
          var ts = st(tgt);
          if (n.localName === 'track' && ts.tracks) attachTrackEl(tgt, n);
          if (n.localName === 'source' && ts.net === 0 && !tgt.hasAttribute('src') && !ts.srcObject) loadAlgorithm(tgt);
        }
        adoptTree(n);
      }
      for (var k = 0; k < r.removedNodes.length; k++) {
        var rn = r.removedNodes[k];
        if (rn.nodeType === 1 && rn.localName === 'track' && isMedia(tgt)) detachTrackEl(tgt, rn);
      }
    }
  }
  function installObserver() {
    try { var mo = new global.MutationObserver(onMutations); mo.observe(global.document, { childList: true, subtree: true }); } catch (e) { /* ignore */ }
  }
  installObserver();

  // attribute hooks (also for detached elements)
  (function () {
    var EP = global.Element.prototype, os = EP.setAttribute, ra = EP.removeAttribute;
    Object.defineProperty(EP, 'setAttribute', { value: function (n, v) {
      var r = os.apply(this, arguments);
      if (this instanceof HME) mediaAttr(this, String(n).toLowerCase());
      return r;
    }, writable: true, configurable: true, enumerable: true });
    void ra;
  })();
  function mediaAttr(el, name) {
    var s = st(el);
    if (name === 'src') { loadAlgorithm(el); }
    else if (name === 'poster' && isVideo(el)) { try { sw.mediaPoster(global.__swIdOf(el), el.getAttribute('poster') || ''); } catch (e) { /* ignore */ } }
    else if (name === 'preload' && s.resumeLoad && el.preload !== 'none') { var f = s.resumeLoad; s.resumeLoad = null; f(); }
  }

  // wrap lifecycle entry points
  (function () {
    var fl = global.__swFireLifecycle, sd = global.__swSetDocument;
    if (typeof sd === 'function') Object.defineProperty(global, '__swSetDocument', { value: function () {
      var r = sd.apply(this, arguments);
      try { sw.mediaReset(); } catch (e) { /* ignore */ }
      taskQ = []; taskScheduled = false; activation.has = false; activation.listeners = [];
      installObserver();
      return r;
    }, writable: true, configurable: true });
    if (typeof fl === 'function') Object.defineProperty(global, '__swFireLifecycle', { value: function (name) {
      var r = fl.apply(this, arguments);
      if (name === 'DOMContentLoaded' || name === 'load') { try { adoptTree(global.document.documentElement); } catch (e) { reportErr(e); } }
      return r;
    }, writable: true, configurable: true });
  })();

  Object.defineProperty(global, '__swMediaEl', { value: function (el) { var s = STATE.get(el); if (!s) return { h: 0, pos: 0, paused: true, ready: 0, rate: 1 }; return { h: s.h, pos: curTime(s), paused: s.paused, ready: s.ready, rate: s.rate }; }, configurable: true });
  // ======================= binary-safe fetch / XHR ===============================================
  // The base prelude moves HTTP bodies through UTF-8 strings, which corrupts
  // binary payloads. Media (decodeAudioData, MSE segments, blobs for <video>)
  // needs the exact bytes, so binary GETs go through a base64 host call.
  (function () {
    var BIN = /\.(mp3|wav|ogg|oga|ogv|opus|m4a|m4v|m4s|mp4|webm|aac|flac|ts|mkv|mov|bin|wasm|png|jpe?g|gif|webp|avif|ico|woff2?|ttf|otf|pdf|zip|gz|mid|midi)(\?|#|$)/i;
    var TEXTY = /(^text\/|json|xml|javascript|ecmascript|html|x-www-form|svg)/i;
    function bytesOfB64(s) { var bin = global.atob(s), u = new Uint8Array(bin.length); for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i); return u; }
    function headersOf(h) {
      var o = {};
      if (!h) return o;
      if (typeof h.forEach === 'function' && !Array.isArray(h)) h.forEach(function (v, k) { o[k] = v; });
      else if (Array.isArray(h)) h.forEach(function (p) { o[p[0]] = p[1]; });
      else Object.keys(h).forEach(function (k) { o[k] = h[k]; });
      return o;
    }
    function getBytes(method, url, hdr) {
      var r = sw.mediaHttpBytes(method, url, JSON.stringify(hdr || {}), '');
      return { status: r.status, statusText: r.statusText || '', url: r.url || url, headers: JSON.parse(r.headers || '{}'), bytes: bytesOfB64(r.b64) };
    }
    var origFetch = global.fetch;
    if (typeof origFetch === 'function') {
      var wrapped = function fetch(input, init) {
        var method = String((init && init.method) || (input && typeof input === 'object' && input.method) || 'GET').toUpperCase();
        var url = typeof input === 'string' ? input : (input && input.url) || String(input);
        var hasBody = !!(init && init.body) || (input && typeof input === 'object' && input.body);
        var abs = url; try { abs = new global.URL(url, global.document.baseURI || global.location.href).href; } catch (e) { /* keep */ }
        if (method === 'GET' && !hasBody && !(init && init.signal && init.signal.aborted) && BIN.test(abs)) {
          return new Promise(function (res, rej) {
            _setTimeout(function () {
              try {
                var hdr = Object.assign({}, headersOf(input && typeof input === 'object' ? input.headers : null), headersOf(init && init.headers));
                var r = getBytes('GET', abs, hdr);
                var rh = new global.Headers(r.headers); rh.delete('content-length'); rh.delete('content-encoding');
                var st = r.status === 204 || r.status === 205 || (r.status >= 100 && r.status < 200) ? 200 : r.status;
                var ctype = rh.get('content-type') || '';
                var resp = new global.Response(null, { status: st, statusText: r.statusText, headers: rh, url: r.url });
                var bytes = r.bytes, used = false;
                var take = function () { if (used) return Promise.reject(new TypeError("Failed to execute 'body stream' on 'Response': body stream already read")); used = true; return Promise.resolve(bytes); };
                var d2 = function (k, f) { Object.defineProperty(resp, k, { value: f, configurable: true, writable: true }); };
                d2('arrayBuffer', function () { return take().then(function (u) { return u.slice().buffer; }); });
                d2('bytes', function () { return take().then(function (u) { return u.slice(); }); });
                d2('blob', function () { return take().then(function (u) { return new global.Blob([u], { type: ctype }); }); });
                d2('text', function () { return take().then(function (u) { return new global.TextDecoder().decode(u); }); });
                d2('json', function () { return take().then(function (u) { return JSON.parse(new global.TextDecoder().decode(u)); }); });
                Object.defineProperty(resp, 'bodyUsed', { get: function () { return used; }, configurable: true });
                if (typeof global.ReadableStream === 'function') Object.defineProperty(resp, 'body', { get: function () { return new global.ReadableStream({ start: function (c) { c.enqueue(bytes); c.close(); } }); }, configurable: true });
                d2('clone', function () { return resp; });
                res(resp);
              } catch (e) { rej(new TypeError('Failed to fetch')); }
            }, 0);
          });
        }
        var p = origFetch.apply(this, arguments);
        if (method !== 'GET' || hasBody) return p;
        return p.then(function (resp) {
          var ct = ''; try { ct = resp.headers.get('content-type') || ''; } catch (e) { /* ignore */ }
          if (ct && !TEXTY.test(ct) && resp.ok) {
            var lazy = null;
            var fetchBin = function () { return lazy || (lazy = new Promise(function (res2, rej2) { _setTimeout(function () { try { res2(getBytes('GET', resp.url || abs, headersOf(init && init.headers)).bytes); } catch (e) { rej2(e); } }, 0); })); };
            var def = function (k, f) { Object.defineProperty(resp, k, { value: f, configurable: true, writable: true }); };
            def('arrayBuffer', function () { return fetchBin().then(function (u) { return u.slice().buffer; }); });
            def('bytes', function () { return fetchBin().then(function (u) { return u.slice(); }); });
            def('blob', function () { return fetchBin().then(function (u) { return new global.Blob([u], { type: ct }); }); });
          }
          return resp;
        });
      };
      Object.defineProperty(wrapped, 'name', { value: 'fetch' });
      global.fetch = wrapped;
    }
    var X = global.XMLHttpRequest;
    if (X && X.prototype) {
      var xsend = X.prototype.send;
      Object.defineProperty(X.prototype, 'send', { value: function (body) {
        var x = this, rt = x.responseType;
        if ((rt === 'arraybuffer' || rt === 'blob') && x._async !== false && (body === undefined || body === null) && String(x._method).toUpperCase() === 'GET' && x.readyState === 1 && !/^blob:/.test(x._url)) {
          x._sent = true;
          _setTimeout(function () {
            if (x._aborted) return;
            var abs = x._url; try { abs = new global.URL(x._url, global.document.baseURI || global.location.href).href; } catch (e) { /* keep */ }
            var r;
            try { r = getBytes('GET', abs, headersOf(x._reqHeaders)); } catch (e) {
              x.status = 0; x.readyState = 4; x.dispatchEvent(new Event('readystatechange'));
              ['error', 'loadend'].forEach(function (t) { x.dispatchEvent(new global.ProgressEvent(t)); }); return;
            }
            x.status = r.status; x.statusText = r.statusText; x.responseURL = r.url; x._resHeaders = new global.Headers(r.headers);
            var resp = rt === 'blob' ? new global.Blob([r.bytes], { type: x._resHeaders.get('content-type') || '' }) : r.bytes.slice().buffer;
            Object.defineProperty(x, 'response', { value: resp, configurable: true });
            var n = r.bytes.length;
            [2, 3, 4].forEach(function (st) { x.readyState = st; x.dispatchEvent(new Event('readystatechange')); });
            ['progress', 'load', 'loadend'].forEach(function (t) { x.dispatchEvent(new global.ProgressEvent(t, { lengthComputable: true, loaded: n, total: n })); });
          }, 0);
          return;
        }
        return xsend.apply(this, arguments);
      }, writable: true, configurable: true });
    }
  })();

  // Audio constructor
  global.Audio = function Audio(src) {
    if (!new.target) throw new TypeError("Failed to construct 'Audio': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    var a = global.document.createElement('audio');
    a.setAttribute('preload', 'auto');
    if (src !== undefined) a.setAttribute('src', String(src));
    return a;
  };
  global.Audio.prototype = global.HTMLAudioElement.prototype;
  Object.defineProperty(global.Audio, 'name', { value: 'Audio' });
  try { adoptTree(global.document.documentElement); } catch (e) { /* ignore */ }
})(globalThis);
