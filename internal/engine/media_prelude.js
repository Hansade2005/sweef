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
  var activation = { has: false, listeners: [], at: 0 };
  global.__swUserActivation = {
    has: function () { return activation.has; },
    onActivate: function (cb) { if (activation.has) cb(); else activation.listeners.push(cb); },
    grant: function () { grantActivation(); },
    transient: function () { return activation.has && Date.now() - activation.at < 5000; }
  };
  function grantActivation() {
    activation.at = Date.now(); // transient activation lasts 5 s (navigator.userActivation.isActive)
    if (activation.has) return;
    activation.has = true;
    var l = activation.listeners; activation.listeners = [];
    l.forEach(function (cb) { try { cb(); } catch (e) { /* ignore */ } });
  }
  // Agent actions reach the page through these Go-called entry points; page
  // code calling el.click() does not.
  // Only real gestures count: button press/release/click, key presses and touches. Moving the pointer, scrolling
  // and script-dispatched events do not activate the page.
  var GESTURE = /^(click|mousedown|mouseup|pointerdown|pointerup|keydown|keyup|touchstart|touchend|dblclick|contextmenu)$/;
  ['__swDispatch', '__swDispatchKey', '__swMouse', '__swTouch', '__swDrag', '__swSetValueFromGo'].forEach(function (n) {
    var orig = global[n];
    if (typeof orig !== 'function') return;
    try {
      Object.defineProperty(global, n, {
        value: function (a, b) {
          var gesture;
          if (n === '__swDispatch') gesture = GESTURE.test(String(b));
          else if (n === '__swMouse') gesture = GESTURE.test(String(a));
          else if (n === '__swTouch') gesture = GESTURE.test(String(b));
          else gesture = true; // key presses, drags, values typed by the agent
          if (gesture) grantActivation();
          return orig.apply(this, arguments);
        }, writable: true, configurable: true, enumerable: false
      });
    } catch (e) { /* ignore */ }
  });
  if (global.Navigator && !('userActivation' in global.Navigator.prototype)) {
    defGetter(global.Navigator.prototype, 'userActivation', function () {
      return { get hasBeenActive() { return activation.has; }, get isActive() { return activation.has && Date.now() - activation.at < 5000; } };
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
        capture: [], node: 0, presented: 0,
        pl: null, plOn: false, plRate: 1, plT: 0, plAt: -1e9, seekPending: null
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
  // The native player (audio device clock) is the master clock while it runs; the wall clock stands in
  // for elements it does not drive (undecodable tracks, MediaStream, ffmpeg-only codecs).
  function plClock(s) {
    var now = _now();
    if (now - s.plAt >= 1) {
      s.plAt = now;
      var c = null;
      try { c = sw.playerClock(s.pl.node); } catch (e) { c = null; }
      if (c && typeof c.t === 'number') { if (c.t >= s.pos || s.plFirst) s.plT = c.t; s.plFirst = false; }
      if (c && c.wk !== undefined) { if (c.wk && !s.wkFired) { s.wkFired = true; var wel = s.el; if (wel) task(function () { fire(wel, 'waitingforkey'); }); } else if (!c.wk) s.wkFired = false; }
    }
    return s.plT;
  }
  function curTime(s) {
    if (!playing(s)) return s.pos;
    if (s.pl && s.plOn) {
      var t0 = plClock(s), d0 = s.duration;
      if (isFinite(d0) && t0 > d0) t0 = d0;
      return t0;
    }
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
    set: function (v) { v = +v; if (!(v >= 0 && v <= 1)) throw dex("Failed to set the 'volume' property on 'HTMLMediaElement': The volume provided (" + v + ") is outside the range [0, 1].", 'IndexSizeError'); var s = st(this); if (s.vol !== v) { s.vol = v; plVolume(this); qevAny(this, 'volumechange'); } }
  };
  PROPS.muted = {
    get: function () { var s = st(this); return s.muted === null ? this.hasAttribute('muted') : s.muted; },
    set: function (v) { var s = st(this), old = this.muted; s.muted = !!v; plVolume(this); if (old !== !!v) qevAny(this, 'volumechange'); }
  };
  function rateCheck(v) { v = +v; if (!isFinite(v)) throw new TypeError('The provided double value is non-finite.'); return v; }
  PROPS.playbackRate = {
    get: function () { return st(this).rate; },
    set: function (v) {
      v = rateCheck(v); if (v !== 0 && (v < 0.0625 || v > 16)) throw dex("Failed to set the 'playbackRate' property on 'HTMLMediaElement': The provided playback rate (" + v + ") is not in the supported playback range.", 'NotSupportedError');
      var s = st(this); if (s.rate !== v) { anchor(s); s.rate = v; qevAny(this, 'ratechange'); reschedule(this); }
    }
  };
  PROPS.defaultPlaybackRate = {
    get: function () { return st(this).defRate; },
    set: function (v) { v = rateCheck(v); if (v !== 0 && (v < 0.0625 || v > 16)) throw dex("Failed to set the 'defaultPlaybackRate' property on 'HTMLMediaElement': The provided playback rate (" + v + ") is not in the supported playback range.", 'NotSupportedError'); var s = st(this); if (s.defRate !== v) { s.defRate = v; qevAny(this, 'ratechange'); } }
  };
  PROPS.preservesPitch = { get: function () { var s = st(this); return s.pp === undefined ? true : s.pp; }, set: function (v) { st(this).pp = !!v; } };
  PROPS.buffered = { get: function () { return makeRanges(bufferedList(st(this))); } };
  PROPS.seekable = { get: function () {
    var s = st(this);
    if (s.ready === 0) return makeRanges([]);
    if (s.mse) { // live streams: the live seekable range plus what is buffered
      if (isFinite(s.duration)) return makeRanges([[0, s.duration]]);
      var lv = msr(s.mse.ms).live;
      return makeRanges(mergeRanges((lv ? [lv] : []).concat(s.mse.bufferedUnion())));
    }
    if (!isFinite(s.duration)) return makeRanges([]);
    return makeRanges([[0, s.duration]]);
  } };
  PROPS.played = { get: function () { var s = st(this), l = s.played.slice(); if (!s.paused && s.ready >= 3 && s.playStart !== undefined) l.push([s.playStart, curTime(s)]); return makeRanges(mergeRanges(l)); } };
  PROPS.sinkId = { get: function () { return st(this).sinkId; } };
  PROPS.mediaKeys = { get: function () { return st(this).keys || null; } };
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
  // Autoplay policy (Chromium's default): playback may start when the page has had a user gesture, when the
  // element is a muted video, or when it is a video without an audio track. Otherwise play() rejects with NotAllowedError
  // and the autoplay attribute does nothing until the user interacts.
  function playAllowed(el) {
    if (activation.has) return true;
    if (isVideo(el) && (el.muted || el.volume === 0)) return true;
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
  function nodeOf(el) { var s = st(el); if (!s.node) { try { s.node = global.__swIdOf(el); } catch (e) { s.node = 0; } } return s.node; }
  // ---- native player glue ---------------------------------------------------------------------
  function plVolume(el) {
    var s = st(el);
    if (s.pl) { try { sw.playerVolume(s.pl.node, s.vol, !!el.muted); } catch (e) { /* ignore */ } }
  }
  // Mirrors the element's wish (playing or not, rate) onto the player. Called whenever playback state may
  // have changed; the player is only told about transitions.
  function plSync(el) {
    var s = st(el);
    if (!s.pl) return;
    var want = playing(s) && !s.seekBlock && !s.mseStalled;
    try {
      if (want && !s.plOn) {
        s.plOn = true; s.plRate = s.rate; s.plT = s.pos; s.plAt = _now(); s.plFirst = true;
        sw.playerHold(s.pl.node, false);
        sw.playerPlay(s.pl.node, s.pos, s.rate);
      } else if (!want && s.plOn) {
        s.plOn = false;
        var t = sw.playerPause(s.pl.node);
        if (typeof t === 'number' && isFinite(t)) { s.pos = Math.max(s.pos, t); s.plT = s.pos; }
      } else if (want && s.plOn && s.plRate !== s.rate) {
        s.plRate = s.rate; sw.playerRate(s.pl.node, s.rate);
      }
    } catch (e) { reportErr(e); }
  }
  function plStop(el) {
    var s = st(el);
    if (s.pl && s.plOn) { s.plOn = false; try { var t = sw.playerPause(s.pl.node); if (typeof t === 'number' && isFinite(t)) s.pos = t; } catch (e) { /* ignore */ } }
  }
  function plOpen(el, s) {
    if (s.pl || !s.h) return;
    var node = nodeOf(el), r = null;
    try { r = sw.playerOpen(node, s.h, isVideo(el)); } catch (e) { r = null; }
    if (!r || !r.ok) return;
    s.pl = { node: node, video: !!r.video, audio: !!r.audio, mse: false };
    if (isVideo(el) && r.video) {
      s.frameMode = 'player';
      // a poster stays up until playback or a seek asks for the first frame
      try { sw.playerHold(node, el.hasAttribute('poster') && !s.started && !s.seekId); } catch (e) { /* ignore */ }
    }
    plVolume(el);
    if (el.__swSourceNode) { try { sw.playerRoute(node, true); } catch (e) { /* ignore */ } }
  }
  function plClose(el, s) {
    if (s.pl) { try { sw.playerClose(s.pl.node); } catch (e) { /* ignore */ } }
    s.pl = null; s.plOn = false;
  }

  function closeRes(s) {
    if (s.pl) { plClose(s.el, s); }
    if (s.rtcDetach) { try { s.rtcDetach(); } catch (e) { /* ignore */ } } // WebRTC stream playback (webrtc_prelude.js)
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
    // WebRTC (webrtc_prelude.js): a stream with received video becomes ready with its first frame
    var rtcDeferred = false;
    try { rtcDeferred = !!(global.__swStreamAttach && global.__swStreamAttach(el, ms, { state: s, setReady: setReady, ev: ev })); } catch (e) { reportErr(e); }
    if (rtcDeferred) { s.net = 1; return; }
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
      plOpen(el, s);
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
    s.seekPending = null;
    plStop(el);
    s.pos = t; s.wall = _now();
    s.seekBlock = true;
    if (s.pl) { s.plT = t; s.plAt = _now(); try { sw.playerHold(s.pl.node, false); sw.playerSeek(s.pl.node, t); } catch (e) { /* ignore */ } }
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
      s.ready = s.mse ? 1 : 4; // MSE: the buffered data decides (msUpdate)
      requestFrame(el, s, true);
      cuesSeek(el);
      fire(el, 'timeupdate');
      fire(el, 'seeked');
      s.lastTU = _now();
      if (s.mse) { msUpdate(el); return; }
      qevAny(el, 'canplay');
      if (!s.paused) qevAny(el, 'playing');
      qevAny(el, 'canplaythrough');
      reschedule(el);
    };
    task(function () {
      _setTimeout(function () {
        task(function () {
          if (s.seekId !== sid || s.loadId !== lid) return;
          // inside the buffered data a seek completes at once; outside it waits for the page to append there
          if (s.mse && !inRanges(s.mse.bufferedUnion(), t)) { s.seekPending = finishSeek; msUpdate(el); } else finishSeek();
        });
      }, 0);
    });
    void run;
  }

  // ---- scheduler: timeupdate, ended/loop, cues, rVFC -------------------------------------------------------
  function reschedule(el) {
    var s = st(el);
    plSync(el);
    if (s.timer) { _clearTimeout(s.timer); s.timer = null; }
    var now = _now(), wake = Infinity;
    var run = playing(s) && !s.seekBlock;
    if (run) {
      wake = Math.min(wake, s.lastTU + 250 - now);
      if (s.pl) wake = Math.min(wake, 50); // follow the device clock: the end of the media, buffering, cues
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
    if (s.pl) { s.lastFrameAt = _now(); return; } // the player's video loop owns the picture
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
  function plStats(el) {
    var s = st(el);
    if (s.pl) { try { var q = sw.playerStats(s.pl.node); if (q) return q; } catch (e) { /* ignore */ } }
    return null;
  }
  defGetter(VP, 'webkitDecodedFrameCount', function () { var q = plStats(this); return q ? q.total : st(this).presented; });
  defGetter(VP, 'webkitDroppedFrameCount', function () { var q = plStats(this); return q ? q.dropped : 0; });
  defGetter(VP, 'webkitDisplayingFullscreen', function () { return false; });
  defGetter(VP, 'webkitSupportsFullscreen', function () { return true; });
  defMethod(VP, 'getVideoPlaybackQuality', function () {
    var s = st(this), q = plStats(this);
    if (q) return { creationTime: _now(), totalVideoFrames: q.total, droppedVideoFrames: q.dropped, corruptedVideoFrames: q.corrupted, totalFrameDelay: 0 };
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
  defMethod(MediaP, 'setMediaKeys', function setMediaKeys(k) {
    var el = this, s = st(el), who = "Failed to execute 'setMediaKeys' on 'HTMLMediaElement'";
    return new Promise(function (res, rej) {
      if (k !== null && !(global.MediaKeys && k instanceof global.MediaKeys)) { rej(new TypeError(who + ": The provided value is not of type 'MediaKeys'.")); return; }
      if ((s.keys || null) === k) { res(); return; }
      if (k && k._el && k._el !== el) { rej(dex(who + ': The MediaKeys object is already attached to another media element.', 'QuotaExceededError')); return; }
      if (s.keys) s.keys._el = null;
      s.keys = k || null;
      if (k) k._el = el;
      emeBindAll(el);
      res();
    });
  });
  // decryption keys follow the MediaKeys of the element to each of its SourceBuffers (eme.go)
  function emeBindAll(el) {
    var s = st(el), ms = s.mse && s.mse.ms; if (!ms) return;
    msr(ms).bufs.forEach(function (b) { try { sw.emeBind(b._r.id, s.keys ? s.keys._id : 0); } catch (e) { /* ignore */ } });
  }
  function emeEncrypted(el, list) {
    if (!list || !list.length) return;
    var seen = el.__emeSeen || (Object.defineProperty(el, '__emeSeen', { value: {}, configurable: true }), el.__emeSeen);
    list.forEach(function (d) {
      var key = d.type + ':' + d.data; if (seen[key]) return; seen[key] = 1;
      var bin = global.atob(d.data), u = new Uint8Array(bin.length); for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i);
      task(function () { fire(el, 'encrypted', global.MediaEncryptedEvent, { initDataType: d.type, initData: u }); });
    });
  }
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
  Object.defineProperty(global, '__swCapTrack', { value: capTrack, configurable: true });
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

  // ---- Permissions API for camera, microphone and display-capture ----------------------------------------------
  // navigator.permissions itself lives in the platform layer (platformjs/pwa.js, which also answers notifications,
  // push and sync); this file answers for its own names through __permHandlers and asks it to refresh live
  // PermissionStatus objects when the user changes a decision.
  var PERM = { camera: 1, microphone: 1, 'display-capture': 1 };
  function permState(name) { if (!PERM[name]) return 'prompt'; try { return sw.capPerm(name) || 'prompt'; } catch (e) { return 'prompt'; } }
  (function () {
    if (!global.__permHandlers) Object.defineProperty(global, '__permHandlers', { value: {}, configurable: true });
    ['camera', 'microphone', 'display-capture'].forEach(function (n) { global.__permHandlers[n] = function () { return permState(n); }; });
  })();
  function permRefresh() { if (typeof global.__swPermRefresh === 'function') global.__swPermRefresh(); }
  if (global.Navigator) {
    Object.defineProperty(global.Navigator.prototype, 'mediaDevices', { get: function () { return devices; }, configurable: true, enumerable: true });
    (function () {
      // ---- Encrypted Media Extensions: Clear Key only (eme.go, docs/media-streaming.md) -----------------------
      // Widevine, PlayReady and FairPlay need licensed CDMs: requestMediaKeySystemAccess rejects them.
      var CLEARKEY = 'org.w3.clearkey', INIT_TYPES = ['cenc', 'keyids', 'webm'];
      function emeHost(fn) {
        try { return fn(); } catch (e) {
          var m = /^(TypeError|InvalidStateError|NotSupportedError): ([\s\S]*)$/.exec(String(e && e.message));
          if (m) throw m[1] === 'TypeError' ? new TypeError(m[2]) : dex(m[2], m[1]);
          throw e;
        }
      }
      function emeBytes(d, who) {
        if (d instanceof ArrayBuffer) return new Uint8Array(d.slice(0));
        if (ArrayBuffer.isView(d)) return new Uint8Array(d.buffer.slice(d.byteOffset, d.byteOffset + d.byteLength));
        throw new TypeError(who + ": The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
      }
      function b64url(u8) { var b = ''; for (var i = 0; i < u8.length; i++) b += String.fromCharCode(u8[i]); return global.btoa(b).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); }
      function fromB64url(s) { s = String(s).replace(/=+$/, '').replace(/-/g, '+').replace(/_/g, '/'); while (s.length % 4) s += '='; var bin = global.atob(s), u = new Uint8Array(bin.length); for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i); return u; }
      function hex(u8) { var s = ''; for (var i = 0; i < u8.length; i++) s += (u8[i] < 16 ? '0' : '') + u8[i].toString(16); return s; }
      function tx(s) { return new TextEncoder().encode(s); }

      function MediaKeySystemAccess() { illegal(); }
      tag(MediaKeySystemAccess, 'MediaKeySystemAccess');
      defGetter(MediaKeySystemAccess.prototype, 'keySystem', function () { return CLEARKEY; });
      defMethod(MediaKeySystemAccess.prototype, 'getConfiguration', function () { return JSON.parse(JSON.stringify(this._cfg)); });
      defMethod(MediaKeySystemAccess.prototype, 'createMediaKeys', function () {
        var acc = this;
        return new Promise(function (res) {
          var k = Object.create(MediaKeys.prototype);
          Object.defineProperty(k, '_id', { value: emeHost(function () { return sw.emeNew(); }) });
          Object.defineProperty(k, '_el', { value: null, writable: true });
          Object.defineProperty(k, '_sessions', { value: new Map() });
          Object.defineProperty(k, '_cfg', { value: acc._cfg });
          res(k);
        });
      });
      global.MediaKeySystemAccess = MediaKeySystemAccess;

      // the first of the requested configurations Clear Key can satisfy, as the resolved configuration (or null)
      function emeConfig(c) {
        if (c === null || typeof c !== 'object') throw new TypeError("Failed to execute 'requestMediaKeySystemAccess' on 'Navigator': The provided value is not of type 'MediaKeySystemConfiguration'.");
        var out = { label: c.label === undefined ? '' : String(c.label), initDataTypes: [], audioCapabilities: [], videoCapabilities: [], distinctiveIdentifier: 'not-allowed', persistentState: 'not-allowed', sessionTypes: ['temporary'] };
        var init = c.initDataTypes === undefined ? [] : Array.from(c.initDataTypes).map(String);
        if (init.length) {
          out.initDataTypes = init.filter(function (t) { return INIT_TYPES.indexOf(t) >= 0; });
          if (!out.initDataTypes.length) return null;
        }
        if (c.distinctiveIdentifier === 'required' || c.persistentState === 'required') return null;
        if (c.sessionTypes !== undefined && Array.from(c.sessionTypes).some(function (t) { return String(t) !== 'temporary'; })) return null;
        function caps(list) {
          var r = [];
          Array.from(list || []).forEach(function (cap) {
            if (cap === null || typeof cap !== 'object') return;
            var ct = String(cap.contentType === undefined ? '' : cap.contentType), rob = cap.robustness === undefined ? '' : String(cap.robustness);
            if (rob !== '' || ct === '') return; // hardware-backed robustness levels need a CDM that has them
            if (!sw.emeSupportedType(ct) || !sw.mediaIsTypeSupported(ct)) return;
            r.push({ contentType: ct, robustness: '' });
          });
          return r;
        }
        var wantA = c.audioCapabilities !== undefined && Array.from(c.audioCapabilities).length > 0, wantV = c.videoCapabilities !== undefined && Array.from(c.videoCapabilities).length > 0;
        if (!wantA && !wantV) return null;
        if (wantA) { out.audioCapabilities = caps(c.audioCapabilities); if (!out.audioCapabilities.length) return null; }
        if (wantV) { out.videoCapabilities = caps(c.videoCapabilities); if (!out.videoCapabilities.length) return null; }
        return out;
      }
      defMethod(global.Navigator.prototype, 'requestMediaKeySystemAccess', function requestMediaKeySystemAccess(keySystem, configs) {
        var args = arguments;
        return new Promise(function (res, rej) {
          try {
            if (args.length < 2) throw new TypeError("Failed to execute 'requestMediaKeySystemAccess' on 'Navigator': 2 arguments required, but only " + args.length + ' present.');
            keySystem = String(keySystem);
            if (keySystem === '') throw new TypeError("Failed to execute 'requestMediaKeySystemAccess' on 'Navigator': The keySystem parameter is empty.");
            var list = Array.from(configs);
            if (list.length === 0) throw new TypeError("Failed to execute 'requestMediaKeySystemAccess' on 'Navigator': The supportedConfigurations parameter is empty.");
            if (keySystem !== CLEARKEY) { rej(dex('Unsupported keySystem or supportedConfigurations.', 'NotSupportedError')); return; }
            for (var i = 0; i < list.length; i++) {
              var r = emeConfig(list[i]);
              if (r) { var a = Object.create(MediaKeySystemAccess.prototype); Object.defineProperty(a, '_cfg', { value: r }); res(a); return; }
            }
            rej(dex('Unsupported keySystem or supportedConfigurations.', 'NotSupportedError'));
          } catch (e) { rej(e); }
        });
      });

      function MediaKeys() { illegal(); }
      tag(MediaKeys, 'MediaKeys');
      defMethod(MediaKeys.prototype, 'createSession', function createSession(type) {
        type = type === undefined ? 'temporary' : String(type);
        if (type !== 'temporary' && type !== 'persistent-license' && type !== 'persistent-usage-record') throw new TypeError("Failed to execute 'createSession' on 'MediaKeys': The provided value '" + type + "' is not a valid enum value of type MediaKeySessionType.");
        if (type !== 'temporary') throw dex("Failed to execute 'createSession' on 'MediaKeys': The session type '" + type + "' is not supported (Clear Key: temporary sessions only).", 'NotSupportedError');
        var s = Object.create(MediaKeySession.prototype);
        EventTarget.call(s);
        var closeRes;
        Object.defineProperty(s, '_k', { value: this });
        Object.defineProperty(s, '_st', { value: { state: 'new', id: '', statuses: new Map(), closeRes: null, closed: null } });
        s._st.closed = new Promise(function (r) { s._st.closeRes = r; });
        Object.defineProperty(s, '_ks', { value: Object.create(MediaKeyStatusMap.prototype) });
        Object.defineProperty(s._ks, '_m', { value: s._st.statuses });
        return s;
      });
      defMethod(MediaKeys.prototype, 'setServerCertificate', function setServerCertificate(cert) {
        return new Promise(function (res, rej) { try { emeBytes(cert, "Failed to execute 'setServerCertificate' on 'MediaKeys'"); res(false); } catch (e) { rej(e); } });
      });
      defMethod(MediaKeys.prototype, 'getStatusForPolicy', function getStatusForPolicy() { return Promise.reject(dex('Policy status checks are not supported by Clear Key.', 'NotSupportedError')); });
      global.MediaKeys = MediaKeys;

      function MediaKeyStatusMap() { illegal(); }
      tag(MediaKeyStatusMap, 'MediaKeyStatusMap');
      defGetter(MediaKeyStatusMap.prototype, 'size', function () { return this._m.size; });
      function kidKey(k) { return hex(emeBytes(k, 'MediaKeyStatusMap')); }
      defMethod(MediaKeyStatusMap.prototype, 'has', function has(k) { return this._m.has(kidKey(k)); });
      defMethod(MediaKeyStatusMap.prototype, 'get', function get(k) { var e = this._m.get(kidKey(k)); return e ? e.status : undefined; });
      function entriesOf(m) { var o = []; m.forEach(function (e) { o.push([e.kid.buffer.slice(0), e.status]); }); return o; }
      defMethod(MediaKeyStatusMap.prototype, 'entries', function entries() { return entriesOf(this._m)[Symbol.iterator](); });
      defMethod(MediaKeyStatusMap.prototype, 'keys', function keys() { return entriesOf(this._m).map(function (e) { return e[0]; })[Symbol.iterator](); });
      defMethod(MediaKeyStatusMap.prototype, 'values', function values() { return entriesOf(this._m).map(function (e) { return e[1]; })[Symbol.iterator](); });
      defMethod(MediaKeyStatusMap.prototype, 'forEach', function forEach(cb, thisArg) {
        if (typeof cb !== 'function') throw new TypeError("Failed to execute 'forEach' on 'MediaKeyStatusMap': The callback provided as parameter 1 is not a function.");
        var self = this; entriesOf(this._m).forEach(function (e) { cb.call(thisArg, e[1], e[0], self); });
      });
      Object.defineProperty(MediaKeyStatusMap.prototype, Symbol.iterator, { value: MediaKeyStatusMap.prototype.entries, writable: true, configurable: true });
      global.MediaKeyStatusMap = MediaKeyStatusMap;

      function MediaKeyMessageEvent(type, init) {
        if (!(this instanceof MediaKeyMessageEvent)) throw new TypeError("Failed to construct 'MediaKeyMessageEvent': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
        if (arguments.length < 2 || init === null || typeof init !== 'object') throw new TypeError("Failed to construct 'MediaKeyMessageEvent': 2 arguments required, but only " + arguments.length + ' present.');
        if (init.messageType === undefined) throw new TypeError("Failed to construct 'MediaKeyMessageEvent': Failed to read the 'messageType' property from 'MediaKeyMessageEventInit': Required member is undefined.");
        if (init.message === undefined) throw new TypeError("Failed to construct 'MediaKeyMessageEvent': Failed to read the 'message' property from 'MediaKeyMessageEventInit': Required member is undefined.");
        var ev = Reflect.construct(global.Event, [type, init], MediaKeyMessageEvent);
        var mt = String(init.messageType);
        if (['license-request', 'license-renewal', 'license-release', 'individualization-request'].indexOf(mt) < 0) throw new TypeError("Failed to construct 'MediaKeyMessageEvent': The provided value '" + mt + "' is not a valid enum value of type MediaKeyMessageType.");
        Object.defineProperty(ev, '_mt', { value: mt }); Object.defineProperty(ev, '_msg', { value: emeBytes(init.message, "Failed to construct 'MediaKeyMessageEvent'").buffer });
        return ev;
      }
      derive(MediaKeyMessageEvent, global.Event); tag(MediaKeyMessageEvent, 'MediaKeyMessageEvent');
      defGetter(MediaKeyMessageEvent.prototype, 'messageType', function () { return this._mt; });
      defGetter(MediaKeyMessageEvent.prototype, 'message', function () { return this._msg; });
      global.MediaKeyMessageEvent = MediaKeyMessageEvent;

      function MediaEncryptedEvent(type, init) {
        if (!(this instanceof MediaEncryptedEvent)) throw new TypeError("Failed to construct 'MediaEncryptedEvent': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
        if (arguments.length < 1) throw new TypeError("Failed to construct 'MediaEncryptedEvent': 1 argument required, but only 0 present.");
        var ev = Reflect.construct(global.Event, [type, init], MediaEncryptedEvent);
        init = init || {};
        Object.defineProperty(ev, '_t', { value: init.initDataType === undefined ? '' : String(init.initDataType) });
        Object.defineProperty(ev, '_d', { value: init.initData === undefined || init.initData === null ? null : emeBytes(init.initData, "Failed to construct 'MediaEncryptedEvent'").buffer });
        return ev;
      }
      derive(MediaEncryptedEvent, global.Event); tag(MediaEncryptedEvent, 'MediaEncryptedEvent');
      defGetter(MediaEncryptedEvent.prototype, 'initDataType', function () { return this._t; });
      defGetter(MediaEncryptedEvent.prototype, 'initData', function () { return this._d; });
      global.MediaEncryptedEvent = MediaEncryptedEvent;

      // key IDs named by initialization data
      function initKids(type, u8) {
        var kids = [];
        if (type === 'keyids') {
          var j;
          try { j = JSON.parse(new TextDecoder().decode(u8)); } catch (e) { return null; }
          if (!j || !Array.isArray(j.kids)) return null;
          for (var i = 0; i < j.kids.length; i++) { try { kids.push(fromB64url(j.kids[i])); } catch (e) { return null; } }
        } else if (type === 'webm') {
          if (u8.length === 0 || u8.length > 512) return null;
          kids.push(u8);
        } else { // cenc: pssh boxes
          var p = 0, COMMON = '1077efecc0b24d02ace33c1e52e2fb4b';
          while (p + 32 <= u8.length) {
            var size = ((u8[p] << 24) | (u8[p + 1] << 16) | (u8[p + 2] << 8) | u8[p + 3]) >>> 0;
            if (size < 32 || p + size > u8.length || String.fromCharCode(u8[p + 4], u8[p + 5], u8[p + 6], u8[p + 7]) !== 'pssh') return null;
            var ver = u8[p + 8];
            if (hex(u8.subarray(p + 12, p + 28)) === COMMON && ver === 1) {
              var n = ((u8[p + 28] << 24) | (u8[p + 29] << 16) | (u8[p + 30] << 8) | u8[p + 31]) >>> 0;
              if (p + 32 + 16 * n > p + size) return null;
              for (var k = 0; k < n; k++) kids.push(u8.slice(p + 32 + 16 * k, p + 48 + 16 * k));
            }
            p += size;
          }
          if (p !== u8.length) return null;
        }
        return kids.length ? kids : null;
      }
      var sessSeq = 0;
      function MediaKeySession() { illegal(); }
      derive(MediaKeySession, EventTarget); tag(MediaKeySession, 'MediaKeySession');
      defGetter(MediaKeySession.prototype, 'sessionId', function () { return this._st.id; });
      defGetter(MediaKeySession.prototype, 'expiration', function () { return NaN; });
      defGetter(MediaKeySession.prototype, 'closed', function () { return this._st.closed; });
      defGetter(MediaKeySession.prototype, 'keyStatuses', function () { return this._ks; });
      ['message', 'keystatuseschange'].forEach(function (t) {
        var key = '__on' + t;
        Object.defineProperty(MediaKeySession.prototype, 'on' + t, { configurable: true, enumerable: true,
          get: function () { return this[key] || null; },
          set: function (f) { if (this[key]) this.removeEventListener(t, this[key]); this[key] = typeof f === 'function' ? f : null; if (this[key]) this.addEventListener(t, this[key]); } });
      });
      defMethod(MediaKeySession.prototype, 'generateRequest', function generateRequest(initDataType, initData) {
        var s = this, who = "Failed to execute 'generateRequest' on 'MediaKeySession'", args = arguments;
        return new Promise(function (res, rej) {
          try {
            if (args.length < 2) throw new TypeError(who + ': 2 arguments required, but only ' + args.length + ' present.');
            initDataType = String(initDataType);
            var u8 = emeBytes(initData, who);
            if (s._st.state === 'closed') throw dex(who + ': The session is already closed.', 'InvalidStateError');
            if (s._st.state !== 'new') throw dex(who + ': The session is already initialized.', 'InvalidStateError');
            if (u8.length === 0) throw new TypeError(who + ': The initData parameter is empty.');
            if (INIT_TYPES.indexOf(initDataType) < 0) throw dex(who + ": The initialization data type '" + initDataType + "' is not supported by the key system.", 'NotSupportedError');
            var kids = initKids(initDataType, u8);
            if (!kids) throw new TypeError(who + ': The initialization data is not valid for the type ' + initDataType + '.');
            s._st.state = 'ready'; s._st.id = 'ck' + (++sessSeq).toString(16).padStart(6, '0');
            s._k._sessions.set(s._st.id, s);
            var msg = tx(JSON.stringify({ kids: kids.map(b64url), type: 'temporary' })).buffer;
            task(function () { if (s._st.state !== 'closed') fire(s, 'message', MediaKeyMessageEvent, { messageType: 'license-request', message: msg }); });
            res();
          } catch (e) { rej(e); }
        });
      });
      defMethod(MediaKeySession.prototype, 'load', function load() { return Promise.reject(new TypeError("Failed to execute 'load' on 'MediaKeySession': The session type is 'temporary', which cannot be loaded.")); });
      defMethod(MediaKeySession.prototype, 'update', function update(response) {
        var s = this, who = "Failed to execute 'update' on 'MediaKeySession'", args = arguments;
        return new Promise(function (res, rej) {
          try {
            if (args.length < 1) throw new TypeError(who + ': 1 argument required, but only 0 present.');
            var u8 = emeBytes(response, who);
            if (s._st.state === 'closed') throw dex(who + ': The session is already closed.', 'InvalidStateError');
            if (s._st.state !== 'ready') throw dex(who + ': The session has no pending request (call generateRequest first).', 'InvalidStateError');
            if (u8.length === 0) throw new TypeError(who + ': The response parameter is empty.');
            var kids = emeHost(function () { return sw.emeLicense(s._k._id, new TextDecoder().decode(u8)); }), changed = false;
            kids.forEach(function (k) {
              var raw = fromB64url(k), id = hex(raw);
              if (!s._st.statuses.has(id)) { s._st.statuses.set(id, { kid: raw, status: 'usable' }); changed = true; }
            });
            if (changed) task(function () { if (s._st.state !== 'closed') fire(s, 'keystatuseschange'); });
            res();
          } catch (e) { rej(e); }
        });
      });
      defMethod(MediaKeySession.prototype, 'close', function close() {
        var s = this;
        return new Promise(function (res) {
          if (s._st.state !== 'closed') {
            s._st.state = 'closed';
            if (s._st.id) s._k._sessions.delete(s._st.id);
            s._st.statuses.clear();
            s._st.closeRes('closed-by-application');
          }
          res();
        });
      });
      defMethod(MediaKeySession.prototype, 'remove', function remove() { return Promise.reject(new TypeError("Failed to execute 'remove' on 'MediaKeySession': The session type is 'temporary', which has nothing to remove.")); });
      global.MediaKeySession = MediaKeySession;
    })();
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
  // Byte-stream parsing, coded frame processing, buffered ranges, eviction and the quota live in Go
  // (internal/media/mse.go). This is the API surface with its events and the element's readyState logic.
  // Not implemented: MediaSource in workers (canConstructInDedicatedWorker is false),
  // MediaSourceHandle, SourceBuffer.audioTracks/videoTracks/textTracks, appendBufferAsync.
  var MS_STATE = new WeakMap();
  function MediaSource() {
    if (!(this instanceof MediaSource)) throw new TypeError("Failed to construct 'MediaSource': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    EventTarget.call(this);
    MS_STATE.set(this, { ready: 'closed', dur: NaN, bufs: [], el: null, s: null, live: null });
    Object.defineProperty(this, 'sourceBuffers', { value: makeSBList(this, false), enumerable: true });
    Object.defineProperty(this, 'activeSourceBuffers', { value: makeSBList(this, true), enumerable: true });
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
  function evHandlers(C, names) {
    names.forEach(function (n) {
      defGetter(C.prototype, 'on' + n, function () { return (this._h || {})[n] || null; }, function (v) {
        if (!this._h) Object.defineProperty(this, '_h', { value: {} });
        var self = this;
        if (!this._h['r' + n]) { this._h['r' + n] = 1; this.addEventListener(n, function (e) { var f = self._h[n]; if (typeof f === 'function') f.call(self, e); }); }
        this._h[n] = v;
      });
    });
  }
  evHandlers(SourceBufferList, ['addsourcebuffer', 'removesourcebuffer']);
  evHandlers(MediaSource, ['sourceopen', 'sourceended', 'sourceclose']);
  defGetter(MediaSource.prototype, 'readyState', function () { return msr(this).ready; });

  function copyRanges(l) { var o = []; if (l) for (var i = 0; i < l.length; i++) o.push([+l[i][0], +l[i][1]]); return o; }
  function highestEnd(r) { var mx = 0; r.bufs.forEach(function (b) { var l = b._r.ranges; if (l.length && l[l.length - 1][1] > mx) mx = l[l.length - 1][1]; }); return mx; }
  function anyUpdating(r) { return r.bufs.some(function (b) { return b._r.updating; }); }

  defGetter(MediaSource.prototype, 'duration', function () { return msr(this).dur; }, function (v) {
    v = +v; var r = msr(this);
    if (isNaN(v)) throw new TypeError("Failed to set the 'duration' property on 'MediaSource': The provided double value is non-finite.");
    if (v < 0) throw new TypeError("Failed to set the 'duration' property on 'MediaSource': The value provided (" + v + ") is less than zero.");
    if (r.ready !== 'open') throw dex("Failed to set the 'duration' property on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    if (anyUpdating(r)) throw dex("Failed to set the 'duration' property on 'MediaSource': One or more SourceBuffers are still processing an append or remove operation.", 'InvalidStateError');
    if (v < highestEnd(r) - 1e-6) throw dex("Failed to set the 'duration' property on 'MediaSource': Setting duration below the highest presentation timestamp of any buffered coded frames is disallowed. Instead, first do asynchronous remove(newDuration, oldDuration) on all sourceBuffers, where newDuration < oldDuration.", 'InvalidStateError');
    setMSDuration(this, v);
  });
  // the duration change algorithm
  function setMSDuration(ms, v) {
    var r = msr(ms); if (r.dur === v || (isNaN(v) && isNaN(r.dur))) return;
    r.dur = v;
    if (r.el) {
      var s = r.s;
      if (s.ready >= 1) {
        s.duration = v; task(function () { fire(r.el, 'durationchange'); });
        if (isFinite(v) && curTime(s) > v) seekTo(r.el, v, false);
      }
      msUpdate(r.el);
    }
  }
  defMethod(MediaSource, 'isTypeSupported', function (t) { if (arguments.length < 1) throw new TypeError("Failed to execute 'isTypeSupported' on 'MediaSource': 1 argument required, but only 0 present."); return !!sw.mediaIsTypeSupported(String(t)); });
  Object.defineProperty(MediaSource, 'canConstructInDedicatedWorker', { value: false, enumerable: true });
  defMethod(MediaSource.prototype, 'addSourceBuffer', function (type) {
    var r = msr(this); type = String(type);
    if (arguments.length < 1) throw new TypeError("Failed to execute 'addSourceBuffer' on 'MediaSource': 1 argument required, but only 0 present.");
    if (type === '') throw new TypeError("Failed to execute 'addSourceBuffer' on 'MediaSource': The type provided ('') is empty.");
    if (!sw.mediaIsTypeSupported(type)) throw dex("Failed to execute 'addSourceBuffer' on 'MediaSource': The type provided ('" + type + "') is unsupported.", 'NotSupportedError');
    if (r.ready !== 'open') throw dex("Failed to execute 'addSourceBuffer' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    if (r.bufs.length >= 8) throw dex("Failed to execute 'addSourceBuffer' on 'MediaSource': This MediaSource has reached the limit of SourceBuffer objects it can handle. No additional SourceBuffer objects may be added.", 'QuotaExceededError');
    var sb = newSourceBuffer(this, type);
    r.bufs.push(sb); sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    var self = this;
    task(function () { fire(self.sourceBuffers, 'addsourcebuffer'); });
    if (r.el) { plMSE(r.el, r.s); emeBindAll(r.el); }
    return sb;
  });
  defMethod(MediaSource.prototype, 'removeSourceBuffer', function (sb) {
    var r = msr(this), i = r.bufs.indexOf(sb);
    if (arguments.length < 1) throw new TypeError("Failed to execute 'removeSourceBuffer' on 'MediaSource': 1 argument required, but only 0 present.");
    if (i < 0) throw dex("Failed to execute 'removeSourceBuffer' on 'MediaSource': The SourceBuffer provided is not contained in this MediaSource.", 'NotFoundError');
    var rr = sb._r;
    if (rr.updating) { rr.updating = false; rr.token++; task(function () { fire(sb, 'abort'); fire(sb, 'updateend'); }); }
    r.bufs.splice(i, 1); rr.removed = true; try { sw.mseClose(rr.id); } catch (e) { /* ignore */ }
    sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    var self = this; task(function () { fire(self.sourceBuffers, 'removesourcebuffer'); });
    if (r.el) { plMSE(r.el, r.s); msUpdate(r.el); }
  });
  defMethod(MediaSource.prototype, 'endOfStream', function (err) {
    var r = msr(this);
    if (r.ready !== 'open') throw dex("Failed to execute 'endOfStream' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    if (anyUpdating(r)) throw dex("Failed to execute 'endOfStream' on 'MediaSource': The MediaSource's SourceBuffers are still updating.", 'InvalidStateError');
    if (err !== undefined && err !== 'network' && err !== 'decode') throw new TypeError("Failed to execute 'endOfStream' on 'MediaSource': The provided value '" + err + "' is not a valid enum value of type EndOfStreamError.");
    endStream(this, err);
  });
  function endStream(ms, err) {
    var r = msr(ms);
    r.ready = 'ended';
    if (r.s && r.s.pl) { try { sw.playerEOS(r.s.pl.node, true); } catch (e) { /* ignore */ } }
    task(function () { fire(ms, 'sourceended'); });
    if (!err) {
      var mx = highestEnd(r);
      if (mx > 0) setMSDuration(ms, mx);
    } else if (r.el) {
      var s = r.s; s.error = makeError(err === 'network' ? 2 : 3, 'MEDIA_ELEMENT_ERROR: ' + (err === 'network' ? 'Network error' : 'Decode error')); task(function () { fire(r.el, 'error'); });
    }
    if (r.el) msUpdate(r.el);
  }
  defMethod(MediaSource.prototype, 'setLiveSeekableRange', function (a, b) {
    var r = msr(this);
    if (arguments.length < 2) throw new TypeError("Failed to execute 'setLiveSeekableRange' on 'MediaSource': 2 arguments required, but only " + arguments.length + " present.");
    if (r.ready !== 'open') throw dex("Failed to execute 'setLiveSeekableRange' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    a = +a; b = +b;
    if (a < 0 || a > b) throw new TypeError("Failed to execute 'setLiveSeekableRange' on 'MediaSource': The provided start (" + a + ") is negative or greater than end (" + b + ").");
    r.live = [a, b];
  });
  defMethod(MediaSource.prototype, 'clearLiveSeekableRange', function () {
    var r = msr(this);
    if (r.ready !== 'open') throw dex("Failed to execute 'clearLiveSeekableRange' on 'MediaSource': The MediaSource's readyState is not 'open'.", 'InvalidStateError');
    r.live = null;
  });
  // MediaSource.handle exists only in dedicated workers (not supported)
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
    r.ready = 'closed'; r.el = null; r.s = null; r.dur = NaN; r.live = null;
    r.bufs.forEach(function (b) { b._r.removed = true; b._r.token++; b._r.updating = false; try { sw.mseClose(b._r.id); } catch (e) { /* ignore */ } });
    r.bufs.length = 0; sbSync(this.sourceBuffers); sbSync(this.activeSourceBuffers);
    task(function () { fire(self, 'sourceclose'); });
  };
  // the element's buffered ranges: the intersection over the SourceBuffers; at the end of the stream the last
  // range reaches the highest end time of any of them
  MediaSource.prototype._buffered = function () {
    var r = msr(this), lists = r.bufs.map(function (b) { return b._r.ranges; });
    if (!lists.length || lists.some(function (l) { return !l.length; })) return [];
    var out = lists[0].map(function (x) { return [x[0], x[1]]; });
    for (var i = 1; i < lists.length; i++) out = intersect(out, lists[i]);
    if (r.ready === 'ended' && out.length) { var mx = 0; lists.forEach(function (l) { mx = Math.max(mx, l[l.length - 1][1]); }); out[out.length - 1][1] = Math.max(out[out.length - 1][1], mx); }
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

  // ---- ManagedMediaSource: a MediaSource that tells the page when to stream (startstreaming/endstreaming) and its
  // SourceBuffers report bufferedchange. Sweef never asks a page to stop (there is no battery or radio to save), so
  // streaming is true from sourceopen on.
  function ManagedMediaSource() {
    if (!new.target) throw new TypeError("Failed to construct 'ManagedMediaSource': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    MediaSource.call(this);
    Object.defineProperty(this, '_streaming', { value: { on: false }, enumerable: false });
  }
  derive(ManagedMediaSource, MediaSource); tag(ManagedMediaSource, 'ManagedMediaSource');
  defGetter(ManagedMediaSource.prototype, 'streaming', function () { return this._streaming.on; });
  evHandlers(ManagedMediaSource, ['startstreaming', 'endstreaming']);
  var baseAttach = MediaSource.prototype._attach, baseDetach = MediaSource.prototype._detach;
  ManagedMediaSource.prototype._attach = function (el, s) {
    var ok = baseAttach.call(this, el, s), self = this;
    if (ok) { this._streaming.on = true; task(function () { fire(self, 'startstreaming'); }); }
    return ok;
  };
  ManagedMediaSource.prototype._detach = function () {
    var was = this._streaming.on, self = this;
    baseDetach.call(this);
    if (was) { this._streaming.on = false; task(function () { fire(self, 'endstreaming'); }); }
  };
  function BufferedChangeEvent(type, init) {
    if (!new.target) throw new TypeError("Failed to construct 'BufferedChangeEvent': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    Event.call(this, type, init); init = init || {};
    Object.defineProperty(this, '_r', { value: { added: init.addedRanges || makeRanges([]), removed: init.removedRanges || makeRanges([]) } });
  }
  derive(BufferedChangeEvent, Event); tag(BufferedChangeEvent, 'BufferedChangeEvent');
  defGetter(BufferedChangeEvent.prototype, 'addedRanges', function () { return this._r.added; });
  defGetter(BufferedChangeEvent.prototype, 'removedRanges', function () { return this._r.removed; });
  global.ManagedMediaSource = ManagedMediaSource; global.BufferedChangeEvent = BufferedChangeEvent;
  // set difference of two sorted range lists: what a is not covered by in b
  function subtractRanges(a, b) {
    var out = [];
    a.forEach(function (x) {
      var s = x[0];
      for (var i = 0; i < b.length && s < x[1]; i++) {
        if (b[i][1] <= s || b[i][0] >= x[1]) continue;
        if (b[i][0] > s) out.push([s, b[i][0]]);
        s = Math.max(s, b[i][1]);
      }
      if (s < x[1]) out.push([s, x[1]]);
    });
    return out;
  }
  // a managed SourceBuffer queues bufferedchange when its buffered ranges changed
  function bufferedChanged(sb, before, after) {
    if (!(sb._ms instanceof ManagedMediaSource)) return;
    var added = subtractRanges(after, before), removed = subtractRanges(before, after);
    if (!added.length && !removed.length) return;
    task(function () { fire(sb, 'bufferedchange', BufferedChangeEvent, { addedRanges: makeRanges(added), removedRanges: makeRanges(removed) }); });
  }

  function SourceBuffer() { illegal(); } derive(SourceBuffer, EventTarget); tag(SourceBuffer, 'SourceBuffer'); global.SourceBuffer = SourceBuffer;
  function newSourceBuffer(ms, type) {
    var sb = Object.create(SourceBuffer.prototype); EventTarget.call(sb);
    var id = sw.mseNew(type);
    Object.defineProperty(sb, '_r', { value: { id: id, type: type, updating: false, removed: false, mode: 'segments', tso: 0, ranges: [], init: false, initInfo: null, ws: 0, we: Infinity, token: 0 } });
    Object.defineProperty(sb, '_ms', { value: ms });
    return sb;
  }
  evHandlers(SourceBuffer, ['updatestart', 'update', 'updateend', 'error', 'abort', 'bufferedchange']);
  function sbCheck(sb, op, attr) {
    var r = sb._r, w = attr ? "Failed to set the '" + op + "' property on 'SourceBuffer': " : "Failed to execute '" + op + "' on 'SourceBuffer': ";
    if (r.removed) throw dex(w + "This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    if (r.updating) throw dex(w + "This SourceBuffer is still processing an 'appendBuffer' or 'remove' operation.", 'InvalidStateError');
  }
  // an ended MediaSource reopens when a SourceBuffer is changed
  function reopen(ms) { var mr = msr(ms); if (mr.ready === 'ended') { mr.ready = 'open'; if (mr.s && mr.s.pl) { try { sw.playerEOS(mr.s.pl.node, false); } catch (e) { /* ignore */ } } task(function () { fire(ms, 'sourceopen'); }); } }
  defGetter(SourceBuffer.prototype, 'updating', function () { return this._r.updating; });
  defGetter(SourceBuffer.prototype, 'buffered', function () {
    if (this._r.removed) throw dex("Failed to read the 'buffered' property from 'SourceBuffer': This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    return makeRanges(this._r.ranges);
  });
  defGetter(SourceBuffer.prototype, 'mode', function () { return this._r.mode; }, function (v) {
    v = String(v);
    if (v !== 'segments' && v !== 'sequence') return;
    sbCheck(this, 'mode', true);
    var mr = msr(this._ms);
    if (mr.ready === 'closed') throw dex("Failed to set the 'mode' property on 'SourceBuffer': The parent media source's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    reopen(this._ms);
    this._r.mode = v; try { sw.mseConfig(this._r.id, 'mode', v); } catch (e) { /* ignore */ }
  });
  defGetter(SourceBuffer.prototype, 'timestampOffset', function () { return this._r.tso; }, function (v) {
    v = +v;
    if (!isFinite(v)) throw new TypeError("Failed to set the 'timestampOffset' property on 'SourceBuffer': The provided double value is non-finite.");
    sbCheck(this, 'timestampOffset', true);
    var mr = msr(this._ms);
    if (mr.ready === 'closed') throw dex("Failed to set the 'timestampOffset' property on 'SourceBuffer': The parent media source's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    reopen(this._ms);
    this._r.tso = v; try { sw.mseConfig(this._r.id, 'tso', v); } catch (e) { /* ignore */ }
  });
  defGetter(SourceBuffer.prototype, 'appendWindowStart', function () { return this._r.ws; }, function (v) {
    v = +v;
    if (isNaN(v)) throw new TypeError("Failed to set the 'appendWindowStart' property on 'SourceBuffer': The provided double value is non-finite.");
    sbCheck(this, 'appendWindowStart', true);
    if (v < 0 || v >= this._r.we) throw new TypeError("Failed to set the 'appendWindowStart' property on 'SourceBuffer': The value provided (" + v + ") is outside the range (0, " + this._r.we + ").");
    this._r.ws = v; try { sw.mseConfig(this._r.id, 'window', v, this._r.we); } catch (e) { /* ignore */ }
  });
  defGetter(SourceBuffer.prototype, 'appendWindowEnd', function () { return this._r.we; }, function (v) {
    v = +v;
    if (isNaN(v)) throw new TypeError("Failed to set the 'appendWindowEnd' property on 'SourceBuffer': The provided double value is non-finite.");
    sbCheck(this, 'appendWindowEnd', true);
    if (v <= this._r.ws) throw new TypeError("Failed to set the 'appendWindowEnd' property on 'SourceBuffer': The value provided (" + v + ") must be greater than appendWindowStart (" + this._r.ws + ").");
    this._r.we = v; try { sw.mseConfig(this._r.id, 'window', this._r.ws, v); } catch (e) { /* ignore */ }
  });
  defGetter(SourceBuffer.prototype, 'audioTracks', function () { return undefined; });
  defGetter(SourceBuffer.prototype, 'videoTracks', function () { return undefined; });
  defGetter(SourceBuffer.prototype, 'textTracks', function () { return undefined; });
  defMethod(SourceBuffer.prototype, 'changeType', function (type) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'changeType' on 'SourceBuffer': 1 argument required, but only 0 present.");
    type = String(type);
    if (type === '') throw new TypeError("Failed to execute 'changeType' on 'SourceBuffer': The type provided ('') is empty.");
    sbCheck(this, 'changeType');
    if (!sw.mediaIsTypeSupported(type)) throw dex("Failed to execute 'changeType' on 'SourceBuffer': The type provided ('" + type + "') is not supported.", 'NotSupportedError');
    var mr = msr(this._ms);
    if (mr.ready === 'closed') throw dex("Failed to execute 'changeType' on 'SourceBuffer': The MediaSource's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    reopen(this._ms);
    var r = this._r; r.type = type; r.init = false;
    try { sw.mseConfig(r.id, 'type', type); } catch (e) { /* ignore */ }
  });
  function bytesOf(d) {
    if (d instanceof ArrayBuffer) return new Uint8Array(d);
    if (ArrayBuffer.isView(d)) return new Uint8Array(d.buffer, d.byteOffset, d.byteLength);
    throw new TypeError("Failed to execute 'appendBuffer' on 'SourceBuffer': The provided value is not of type '(ArrayBuffer or ArrayBufferView)'.");
  }
  // the first initialization segment after addSourceBuffer / changeType, or a new one with other dimensions
  function sbInit(sb, init) {
    var r = sb._r, mr = msr(sb._ms);
    r.init = true; r.initInfo = init;
    if (init.first || isNaN(mr.dur)) {
      if (init.duration > 0 && (isNaN(mr.dur) || init.duration > mr.dur)) setMSDuration(sb._ms, init.duration);
      else if (isNaN(mr.dur)) setMSDuration(sb._ms, Infinity);
    }
  }
  defMethod(SourceBuffer.prototype, 'appendBuffer', function (data) {
    var sb = this, r = sb._r, ms = sb._ms, mr = msr(ms);
    if (arguments.length < 1) throw new TypeError("Failed to execute 'appendBuffer' on 'SourceBuffer': 1 argument required, but only 0 present.");
    var bytes = bytesOf(data);
    sbCheck(sb, 'appendBuffer');
    if (mr.ready === 'closed') throw dex("Failed to execute 'appendBuffer' on 'SourceBuffer': The MediaSource's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    reopen(ms);
    var res;
    try { res = sw.mseAppend(r.id, b64OfBytes(bytes), mr.s ? curTime(mr.s) : 0); } catch (e) { res = { ok: false, error: 'decode', message: String(e && e.message || e) }; }
    if (!res.ok && res.error === 'quota') {
      throw dex("Failed to execute 'appendBuffer' on 'SourceBuffer': The SourceBuffer is full, and cannot free space to append additional buffers.", 'QuotaExceededError');
    }
    r.updating = true;
    var tok = ++r.token;
    task(function () { if (r.token === tok && !r.removed) fire(sb, 'updatestart'); });
    task(function () { _setTimeout(function () { task(finish); }, 0); });
    function finish() {
      if (r.removed || r.token !== tok) return; // aborted or removed meanwhile
      r.updating = false;
      if (!res.ok) { // the append error algorithm: the events are queued, the end of stream algorithm runs at once
        task(function () { fire(sb, 'error'); fire(sb, 'updateend'); });
        if (mr.ready === 'open' || mr.ready === 'ended') { mr.ready = 'open'; endStream(ms, 'decode'); }
        return;
      }
      if (res.init) sbInit(sb, res.init);
      if (mr.el) { if (res.init) emeEncrypted(mr.el, res.init.encrypted); emeEncrypted(mr.el, res.encrypted); }
      var before = r.ranges;
      if (res.buffered) r.ranges = copyRanges(res.buffered);
      bufferedChanged(sb, before, r.ranges);
      var mx = highestEnd(mr);
      if (isFinite(mr.dur) && mx > mr.dur && mr.ready === 'open') setMSDuration(ms, mx);
      fire(sb, 'update'); fire(sb, 'updateend');
      if (mr.el) { msUpdate(mr.el); progressSoon(mr.el); }
    }
  });
  defMethod(SourceBuffer.prototype, 'abort', function () {
    var r = this._r, mr = msr(this._ms), sb = this;
    if (r.removed) throw dex("Failed to execute 'abort' on 'SourceBuffer': This SourceBuffer has been removed from the parent media source.", 'InvalidStateError');
    if (mr.ready !== 'open') throw dex("Failed to execute 'abort' on 'SourceBuffer': The parent media source's readyState is not 'open'.", 'InvalidStateError');
    if (r.updating) { r.updating = false; r.token++; task(function () { fire(sb, 'abort'); fire(sb, 'updateend'); }); }
    try { sw.mseAbort(r.id); } catch (e) { /* ignore */ }
    r.ws = 0; r.we = Infinity;
  });
  defMethod(SourceBuffer.prototype, 'remove', function (start, end) {
    var sb = this, r = sb._r, mr = msr(sb._ms);
    if (arguments.length < 2) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': 2 arguments required, but only " + arguments.length + " present.");
    start = +start; end = +end;
    sbCheck(sb, 'remove');
    if (mr.ready === 'closed') throw dex("Failed to execute 'remove' on 'SourceBuffer': The MediaSource's readyState is not 'open' or 'ended'.", 'InvalidStateError');
    if (isNaN(mr.dur)) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': The MediaSource's duration is NaN.");
    if (isNaN(start) || start < 0 || start > mr.dur) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': The start provided (" + start + ") is either negative or greater than the current duration (" + mr.dur + ").");
    if (isNaN(end) || end <= start) throw new TypeError("Failed to execute 'remove' on 'SourceBuffer': The end provided (" + end + ") is less than or equal to the start (" + start + ").");
    reopen(sb._ms);
    r.updating = true;
    var tok = ++r.token;
    task(function () { if (r.token === tok && !r.removed) fire(sb, 'updatestart'); });
    task(function () {
      _setTimeout(function () {
        task(function () {
          if (r.removed || r.token !== tok) return;
          var before = r.ranges;
          try { r.ranges = copyRanges(sw.mseRemove(r.id, start, end)); } catch (e) { /* ignore */ }
          bufferedChanged(sb, before, r.ranges);
          r.updating = false; fire(sb, 'update'); fire(sb, 'updateend');
          if (mr.el) msUpdate(mr.el);
        });
      }, 0);
    });
  });
  // 'progress' when the buffered data changed, at most every 100 ms
  function progressSoon(el) {
    var s = st(el);
    if (s.progQueued) return;
    s.progQueued = true;
    _setTimeout(function () { s.progQueued = false; if (s.mse) fire(el, 'progress'); }, 0);
  }

  // a player over the SourceBuffers of the element's MediaSource
  function plMSE(el, s) {
    var ms = s.mse && s.mse.ms; if (!ms) return;
    var r = msr(ms), ids = r.bufs.map(function (b) { return b._r.id; });
    var node = nodeOf(el);
    try {
      var res = sw.playerMSE(node, JSON.stringify(ids), isVideo(el), s.videoW || 0, s.videoH || 0);
      if (res && res.ok && !s.pl) {
        s.pl = { node: node, video: isVideo(el), audio: true, mse: true };
        s.frameMode = 'player';
        plVolume(el);
        if (r.ready === 'ended') sw.playerEOS(node, true);
      }
    } catch (e) { reportErr(e); }
  }
  function attachMSE(el, s, id, ms) {
    var ok = ms._attach(el, s);
    if (!ok) { s.error = makeError(4, 'MEDIA_ELEMENT_ERROR: Format error'); s.net = 3; ev(el, 'error'); return; }
    s.mse = { ms: ms, detach: function () { ms._detach(); }, bufferedUnion: function () { return ms._buffered(); } };
    s.info = { container: 'mse', duration: NaN, tracks: [] };
    s.net = 2; s.frameMode = '';
  }
  function inRanges(b, t) {
    for (var i = 0; i < b.length; i++) if (t >= b[i][0] - 0.0001 && t < b[i][1] + 1e-6) return b[i];
    return null;
  }
  // recompute metadata, readyState and seeking from the buffered data
  function msUpdate(el) {
    var s = st(el), ms = s.mse && s.mse.ms; if (!ms) return;
    var r = msr(ms);
    if (s.ready === 0) {
      var allInit = r.bufs.length > 0 && r.bufs.every(function (b) { return b._r.init; });
      if (!allInit) return;
      var tracks = []; r.bufs.forEach(function (b) { tracks = tracks.concat(b._r.initInfo.tracks); });
      s.info = { container: 'mse', duration: r.dur, tracks: tracks };
      s.duration = r.dur;
      var v = null; tracks.forEach(function (t) { if (t.kind === 'video' && !v) v = t; });
      s.video = v; s.videoW = v ? v.width : 0; s.videoH = v ? v.height : 0; s.fps = v && v.frameRate > 0 ? v.frameRate : 30;
      s.net = 1; if (s.mseMetaPending) return; s.mseMetaPending = true;
      plMSE(el, s);
      // Chromium reports the metadata a task or two after the append that carried the init segment
      task(function () { task(function () { if (!s.mse) return; fire(el, 'durationchange'); s.ready = 1; populateTracks(el, s); if (v) fire(el, 'resize'); fire(el, 'loadedmetadata'); msUpdate(el); }); });
      return;
    }
    if (s.ready < 1) return;
    // a new initialization segment with another picture size (quality switch)
    var nv = null; r.bufs.forEach(function (b) { if (b._r.initInfo) b._r.initInfo.tracks.forEach(function (t) { if (t.kind === 'video') nv = t; }); });
    if (nv && (nv.width !== s.videoW || nv.height !== s.videoH) && nv.width > 0) { s.videoW = nv.width; s.videoH = nv.height; s.video = nv; task(function () { fire(el, 'resize'); }); }
    var b = ms._buffered(), t = playing(s) ? curTime(s) : s.pos, target = 1;
    var rg = inRanges(b, t);
    // Chromium treats the start of the stream as buffered when the first range begins within a second of 0
    // (streams rarely start exactly at 0): playback begins at the start of that range
    if (!rg && b.length && t <= 0.001 && b[0][0] <= 1 && !playing(s)) { rg = b[0]; s.pos = rg[0]; t = s.pos; }
    if (rg) {
      var ahead = rg[1] - t;
      var atEnd = r.ready === 'ended' && isFinite(s.duration) && rg[1] >= s.duration - 0.01;
      if (atEnd || ahead >= 3) target = 4; else if (ahead > 0.1) target = 3; else target = 2;
      if (atEnd && ahead <= 0) target = 4;
    }
    // a seek waiting for data completes as soon as the data is there
    if (s.seekPending) {
      if (rg) { var f = s.seekPending; s.seekPending = null; f(); b = ms._buffered(); }
      else return reschedule(el);
    }
    if (s.seeking) return;
    if (target < 2 && s.loadedData) target = Math.min(target, 1);
    if (target >= 2 && s.ready < 2) { s.ready = 2; qevAny(el, 'loadeddata'); s.loadedData = true; }
    if (target !== s.ready) setReady(el, Math.max(1, target)); else reschedule(el);
  }
  // starvation check on tick for MSE playback
  var origTick = tick;
  tick = function (el) { var s = st(el); if (s.mse && s.ready >= 2) { msUpdate(el); } origTick(el); };

  // ======================= navigator.mediaCapabilities =================================================
  // Players (YouTube, Shaka, dash.js, hls.js) pick formats with decodingInfo(). Support follows the codec
  // tables of internal/media/canplay.go; "smooth" is a pixel-rate budget measured on the pure-Go decoders
  // (docs/media-streaming.md), power efficiency is always false (software decoding).
  (function () {
    if (!global.navigator || 'mediaCapabilities' in global.navigator) return;
    // Megapixels per second the decoder sustains with dropped frames under about 2% on a 4-core machine, per codec
    // (measured with TestPlaybackQuality, see docs/media-streaming.md): H.264 1080p30 = 62.2 (with frame threads;
    // it was 720p30 = 27.6 before), VP9 1080p30 = 62.2; AV1 and HEVC are held at 480p30-and-a-bit because their
    // key frames cost several frame times (AV1 720p30 decodes at 33 fps with a 147 ms 95th percentile) or were
    // not measured (HEVC, VP8).
    function smoothMpx(ct) {
      ct = String(ct).toLowerCase();
      if (/avc[13]|h264/.test(ct)) return 63;
      if (/vp09|vp9/.test(ct)) return 63;
      if (/av01|av1/.test(ct)) return 14;
      if (/hev1|hvc1|hevc/.test(ct)) return 14;
      return 28;
    }
    // the codecs a peer connection negotiates (internal/engine/webrtc_sdp.go): VP8 video and Opus audio
    function rtcCodec(ct) {
      var m = String(ct).toLowerCase().split(';')[0].trim();
      return m === 'video/vp8' || m === 'audio/opus' || m === 'audio/red' || m === 'video/rtx' || m === 'audio/telephone-event' ? true : false;
    }
    function MediaCapabilities() { illegal(); } tag(MediaCapabilities, 'MediaCapabilities');
    var mc = Object.create(MediaCapabilities.prototype);
    function typeErr(m) { return Promise.reject(new TypeError("Failed to execute 'decodingInfo' on 'MediaCapabilities': " + m)); }
    function badMime(t) { return typeof t !== 'string' || !/^[a-z]+\/[a-z0-9.+-]+/i.test(t.trim()); }
    defMethod(MediaCapabilities.prototype, 'decodingInfo', function (cfg) {
      if (!cfg || typeof cfg !== 'object') return typeErr('The provided value is not of type \'MediaDecodingConfiguration\'.');
      var type = cfg.type;
      if (type !== 'file' && type !== 'media-source' && type !== 'webrtc') return typeErr("The provided value '" + type + "' is not a valid enum value of type MediaDecodingType.");
      if (!cfg.video && !cfg.audio) return typeErr('The configuration object has neither "video" nor "audio" field.');
      var supported = true, smooth = true;
      var check = function (ct) {
        if (badMime(ct)) return false;
        if (type === 'webrtc') return rtcCodec(ct);
        if (type === 'media-source') return !!sw.mediaIsTypeSupported(ct);
        return sw.mediaCanPlay(ct) !== '';
      };
      if (cfg.video) {
        var v = cfg.video;
        if (badMime(v.contentType)) return typeErr('Failed to parse video contentType: ' + v.contentType);
        if (!(v.width > 0) || !(v.height > 0) || !(v.framerate > 0) || !(v.bitrate >= 0)) return typeErr('The video configuration dictionary is missing a valid width, height, bitrate or framerate.');
        supported = supported && check(v.contentType);
        var mpx = v.width * v.height * v.framerate / 1e6;
        smooth = smooth && mpx <= smoothMpx(v.contentType);
      }
      if (cfg.audio) {
        if (badMime(cfg.audio.contentType)) return typeErr('Failed to parse audio contentType: ' + cfg.audio.contentType);
        supported = supported && check(cfg.audio.contentType);
      }
      if (cfg.keySystemConfiguration) { supported = false; smooth = false; }
      var info = { supported: supported, smooth: supported && smooth, powerEfficient: false };
      if (cfg.keySystemConfiguration) info.keySystemAccess = null;
      return Promise.resolve(info);
    });
    defMethod(MediaCapabilities.prototype, 'encodingInfo', function (cfg) {
      if (!cfg || typeof cfg !== 'object') return Promise.reject(new TypeError("Failed to execute 'encodingInfo' on 'MediaCapabilities': The provided value is not of type 'MediaEncodingConfiguration'."));
      var t = cfg.type;
      if (t !== 'record' && t !== 'webrtc') return Promise.reject(new TypeError("Failed to execute 'encodingInfo' on 'MediaCapabilities': The provided value '" + t + "' is not a valid enum value of type MediaEncodingType."));
      if (!cfg.video && !cfg.audio) return Promise.reject(new TypeError("Failed to execute 'encodingInfo' on 'MediaCapabilities': The configuration object has neither \"video\" nor \"audio\" field."));
      // WebRTC sends VP8 and Opus (pure-Go encoders, software); MediaRecorder does not exist, so 'record' is unsupported
      var ok = t === 'webrtc' && (!cfg.video || rtcCodec(cfg.video.contentType)) && (!cfg.audio || rtcCodec(cfg.audio.contentType));
      var smooth = ok;
      if (ok && cfg.video) smooth = (cfg.video.width || 0) * (cfg.video.height || 0) * (cfg.video.framerate || 0) / 1e6 <= 14; // about 640x360 at 30 fps: what the pure-Go VP8 encoder sustains in the call tests on 4 cores
      return Promise.resolve({ supported: ok, smooth: smooth, powerEfficient: false });
    });
    global.MediaCapabilities = MediaCapabilities;
    try { Object.defineProperty(global.Navigator.prototype, 'mediaCapabilities', { get: function () { return mc; }, configurable: true, enumerable: true }); } catch (e) { /* ignore */ }
  })();

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
    // segment URLs without a telling extension (YouTube's videoplayback, ?range= byte ranges)
    var BINQ = /(\/videoplayback|[?&]range=\d|\/(seg|segment|chunk|frag)[-_]?\d)/i;
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
        var binReq = BIN.test(abs) || BINQ.test(abs);
        if (!binReq && method === 'GET') { var h0 = Object.assign({}, headersOf(input && typeof input === 'object' ? input.headers : null), headersOf(init && init.headers)); binReq = Object.keys(h0).some(function (k) { return k.toLowerCase() === 'range'; }); }
        if (method === 'GET' && !hasBody && !(init && init.signal && init.signal.aborted) && binReq) {
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


  // ======================= Fullscreen API ===============================================================
  // requestFullscreen needs transient user activation (like Chrome). The element gets the internal attribute
  // data-sw-fullscreen, which the UA style sheet turns into "fixed, covering the viewport"; the host event
  // (Tab events: Media.fullscreen) makes the app give the page the whole window (chrome hidden, window
  // fullscreen). The user leaving fullscreen (Esc, F11) comes back through __swFsExit().
  (function () {
    var D = global.Document && global.Document.prototype, E = global.Element && global.Element.prototype;
    if (!D || !E) return;
    var cur = null, queue = null;
    // top-level documents only; looked up when asked, not at load (the window relations belong to the bound page)
    function enabled() { try { return global.top === global; } catch (e) { return false; } }
    function mark(el, on) { try { var id = global.__swIdOf(el); if (on) sw.setAttr(id, 'data-sw-fullscreen', ''); else sw.removeAttr(id, 'data-sw-fullscreen'); } catch (e) { /* ignore */ } }
    function fire(el, type) {
      var ev = new Event(type, { bubbles: true, composed: true });
      try { el.dispatchEvent(ev); } catch (e) { /* ignore */ }
      // the document sees the element's event through bubbling; a detached element's does not bubble to it
      if (!el.isConnected) { try { global.document.dispatchEvent(new Event(type, { bubbles: true })); } catch (e) { /* ignore */ } }
    }
    function change(el, was) {
      if (was && was !== el) fire(was, 'fullscreenchange');
      fire(el || was, 'fullscreenchange');
    }
    function host(on) { try { sw.fullscreen(!!on); } catch (e) { /* no tab */ } }
    function fail(el, msg) {
      _setTimeout(function () { fire(el, 'fullscreenerror'); }, 0);
      return Promise.reject(new TypeError(msg));
    }
    function request(options) {
      var el = this;
      if (!enabled()) return fail(el, "Failed to execute 'requestFullscreen' on 'Element': Fullscreen is not allowed in this frame.");
      if (!el.isConnected) return fail(el, "Failed to execute 'requestFullscreen' on 'Element': Element is not connected.");
      if (!global.__swUserActivation.transient()) return fail(el, "Failed to execute 'requestFullscreen' on 'Element': API can only be initiated by a user gesture.");
      if (options !== undefined && options !== null && typeof options !== 'object') return Promise.reject(new TypeError("Failed to execute 'requestFullscreen' on 'Element': The provided value is not of type 'FullscreenOptions'."));
      var was = cur;
      if (was === el) return Promise.resolve();
      if (was) mark(was, false);
      cur = el; mark(el, true); host(true);
      return new Promise(function (res) { _setTimeout(function () { change(el, was); res(); }, 0); });
    }
    function exit() {
      var was = cur;
      if (!was) return Promise.reject(new TypeError("Failed to execute 'exitFullscreen' on 'Document': Document not active"));
      cur = null; mark(was, false); host(false);
      return new Promise(function (res) { _setTimeout(function () { fire(was, 'fullscreenchange'); res(); }, 0); });
    }
    function def(o, n, v) { Object.defineProperty(o, n, { value: v, writable: true, configurable: true, enumerable: true }); }
    function getter(o, n, g) { Object.defineProperty(o, n, { get: g, set: undefined, configurable: true, enumerable: true }); }
    def(E, 'requestFullscreen', request); def(E, 'webkitRequestFullscreen', request); def(E, 'webkitRequestFullScreen', request);
    def(D, 'exitFullscreen', exit); def(D, 'webkitExitFullscreen', exit); def(D, 'webkitCancelFullScreen', exit);
    getter(D, 'fullscreenElement', function () { return cur && cur.isConnected ? cur : null; });
    getter(D, 'webkitFullscreenElement', function () { return cur && cur.isConnected ? cur : null; });
    getter(D, 'webkitCurrentFullScreenElement', function () { return cur && cur.isConnected ? cur : null; });
    getter(D, 'fullscreenEnabled', enabled); getter(D, 'webkitFullscreenEnabled', enabled);
    getter(D, 'webkitIsFullScreen', function () { return !!cur; }); getter(D, 'webkitFullscreen', function () { return !!cur; });
    // the host leaves fullscreen (Esc, F11, tab switch): the page is told like any exit
    Object.defineProperty(global, '__swFsExit', { value: function () { var was = cur; if (!was) return false; cur = null; mark(was, false); fire(was, 'fullscreenchange'); return true; }, configurable: true });
    var VP = global.HTMLVideoElement && global.HTMLVideoElement.prototype;
    if (VP) {
      def(VP, 'webkitEnterFullscreen', function () { return request.call(this).catch(function () { }); });
      def(VP, 'webkitEnterFullScreen', VP.webkitEnterFullscreen);
      def(VP, 'webkitExitFullscreen', function () { if (cur === this) exit(); });
      def(VP, 'webkitExitFullScreen', VP.webkitExitFullscreen);
      getter(VP, 'webkitDisplayingFullscreen', function () { return cur === this; });
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
