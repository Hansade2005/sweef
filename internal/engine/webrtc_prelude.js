/*
 * webrtc_prelude.js: RTCPeerConnection and friends.
 *
 * Go (webrtc.go, pion) owns the peers, ICE, DTLS, SCTP and RTP. This file is
 * the page-facing layer: classes, validation, promises, RTCRtpTransceiver
 * bookkeeping and event dispatch. Host calls are synchronous and return JSON
 * strings; asynchronous pion callbacks arrive through __swRtcEvent, which the
 * event-loop pump calls on the page thread (realtime queue, kind "rtc").
 * docs/webrtc.md lists what is supported and what is not.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || typeof sw.rtcEnabled !== 'function' || !sw.rtcEnabled()) return;
  var ET = global.EventTarget, Ev = global.Event;

  // ---- helpers ----------------------------------------------------------------
  function hid(o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true, enumerable: false }); }
  function defGetter(o, n, g, s) { Object.defineProperty(o, n, { get: g, set: s, configurable: true, enumerable: true }); }
  function defMethod(o, n, f) { Object.defineProperty(o, n, { value: f, writable: true, configurable: true, enumerable: true }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  function dex(msg, name) { return new global.DOMException(msg, name); }
  function handlerProps(proto, names) {
    names.forEach(function (n) {
      var key = '__on' + n;
      Object.defineProperty(proto, 'on' + n, {
        configurable: true, enumerable: true,
        get: function () { return this[key] || null; },
        set: function (fn) {
          if (this[key]) this.removeEventListener(n, this[key]);
          hid(this, key, typeof fn === 'function' ? fn : null);
          if (this[key]) this.addEventListener(n, this[key]);
        }
      });
    });
  }
  function hostErr(e) {
    var msg = String((e && e.message) || e);
    var m = /^([A-Za-z]+(?:Error|Exception)): ([\s\S]*)$/.exec(msg);
    if (!m) return dex(msg, 'OperationError');
    if (m[1] === 'TypeError') return new TypeError(m[2]);
    if (m[1] === 'RangeError') return new RangeError(m[2]);
    return dex(m[2], m[1]);
  }
  function call(name) {
    try { return sw[name].apply(null, Array.prototype.slice.call(arguments, 1)); } catch (e) { throw hostErr(e); }
  }
  function callJSON(name) { var r = call.apply(null, arguments); return r ? JSON.parse(r) : r; }
  function b64ToBytes(b64) {
    var bin = global.atob(b64), u = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u;
  }
  function bytesToB64(u) {
    var s = '';
    for (var i = 0; i < u.length; i += 0x8000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000));
    return global.btoa(s);
  }
  function fire(target, type, props) {
    var e = new Ev(type);
    if (props) for (var k in props) Object.defineProperty(e, k, { value: props[k], enumerable: true });
    target.dispatchEvent(e);
    return e;
  }
  function task(fn) { global.setTimeout(fn, 0); }
  function utf8Len(s) { return unescape(encodeURIComponent(s)).length; }

  // ---- event classes ------------------------------------------------------------
  function evClass(name, keys, defaults) {
    var C = class extends Ev {
      constructor(type, init) {
        if (arguments.length === 0) throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
        super(type, init);
        init = init || {};
        var self = this;
        keys.forEach(function (k) {
          Object.defineProperty(self, k, { value: init[k] === undefined ? (defaults && defaults[k] !== undefined ? defaults[k] : null) : init[k], enumerable: true });
        });
      }
    };
    Object.defineProperty(C, 'name', { value: name });
    tag(C, name);
    global[name] = C;
    return C;
  }
  var RTCPeerConnectionIceEvent = evClass('RTCPeerConnectionIceEvent', ['candidate', 'url']);
  var RTCTrackEvent = evClass('RTCTrackEvent', ['receiver', 'track', 'streams', 'transceiver'], { streams: [] });
  var RTCDataChannelEvent = evClass('RTCDataChannelEvent', ['channel']);
  var RTCDTMFToneChangeEvent = evClass('RTCDTMFToneChangeEvent', ['tone'], { tone: '' });
  evClass('RTCPeerConnectionIceErrorEvent', ['address', 'port', 'url', 'errorCode', 'errorText']);

  class RTCError extends global.DOMException {
    constructor(init, message) {
      super(message || '', 'OperationError');
      Object.setPrototypeOf(this, new.target.prototype); // DOMException is a factory: restore the subclass prototype
      init = init || {};
      Object.defineProperty(this, 'errorDetail', { value: init.errorDetail, enumerable: true });
      Object.defineProperty(this, 'sdpLineNumber', { value: init.sdpLineNumber === undefined ? null : init.sdpLineNumber, enumerable: true });
      Object.defineProperty(this, 'httpRequestStatusCode', { value: init.httpRequestStatusCode === undefined ? null : init.httpRequestStatusCode, enumerable: true });
      Object.defineProperty(this, 'sctpCauseCode', { value: init.sctpCauseCode === undefined ? null : init.sctpCauseCode, enumerable: true });
      Object.defineProperty(this, 'receivedAlert', { value: init.receivedAlert === undefined ? null : init.receivedAlert, enumerable: true });
      Object.defineProperty(this, 'sentAlert', { value: init.sentAlert === undefined ? null : init.sentAlert, enumerable: true });
    }
  }
  global.RTCError = RTCError;
  var RTCErrorEvent = evClass('RTCErrorEvent', ['error']);

  // ---- RTCSessionDescription ----------------------------------------------------
  var SDP_TYPES = ['offer', 'pranswer', 'answer', 'rollback'];
  class RTCSessionDescription {
    constructor(init) {
      if (init === undefined || init === null || typeof init !== 'object') throw new TypeError("Failed to construct 'RTCSessionDescription': The provided value is not of type 'RTCSessionDescriptionInit'.");
      if (init.type === undefined || SDP_TYPES.indexOf(init.type) < 0) throw new TypeError("Failed to construct 'RTCSessionDescription': The provided value '" + init.type + "' is not a valid enum value of type RTCSdpType.");
      hid(this, '_type', init.type);
      hid(this, '_sdp', init.sdp === undefined ? '' : String(init.sdp));
    }
    get type() { return this._type; }
    set type(v) { if (SDP_TYPES.indexOf(v) >= 0) this._type = v; }
    get sdp() { return this._sdp; }
    set sdp(v) { this._sdp = String(v); }
    toJSON() { return { type: this._type, sdp: this._sdp }; }
  }
  tag(RTCSessionDescription, 'RTCSessionDescription');
  global.RTCSessionDescription = RTCSessionDescription;
  function mkDesc(d) { return d ? new RTCSessionDescription({ type: d.type, sdp: d.sdp }) : null; }

  // ---- RTCIceCandidate ------------------------------------------------------------
  function parseCand(c) {
    var s = String(c).replace(/^a=/, '');
    if (!/^candidate:/.test(s)) return null;
    var f = s.slice(10).split(/\s+/);
    if (f.length < 8 || f[6] !== 'typ') return null;
    var o = { foundation: f[0], component: f[1] === '1' ? 'rtp' : f[1] === '2' ? 'rtcp' : null, protocol: f[2].toLowerCase(), priority: +f[3], address: f[4], port: +f[5], type: f[7], tcpType: null, relatedAddress: null, relatedPort: null };
    for (var i = 8; i + 1 < f.length; i += 2) {
      if (f[i] === 'raddr') o.relatedAddress = f[i + 1];
      else if (f[i] === 'rport') o.relatedPort = +f[i + 1];
      else if (f[i] === 'tcptype') o.tcpType = f[i + 1];
    }
    return o;
  }
  class RTCIceCandidate {
    constructor(init) {
      if (init === undefined || init === null || typeof init !== 'object') throw new TypeError("Failed to construct 'RTCIceCandidate': The provided value is not of type 'RTCIceCandidateInit'.");
      var cand = init.candidate === undefined ? '' : String(init.candidate);
      var mid = init.sdpMid === undefined ? null : init.sdpMid;
      var idx = init.sdpMLineIndex === undefined ? null : init.sdpMLineIndex;
      if (mid === null && idx === null) throw new TypeError("Failed to construct 'RTCIceCandidate': sdpMid and sdpMLineIndex are both null.");
      hid(this, '_c', { candidate: cand, sdpMid: mid === null ? null : String(mid), sdpMLineIndex: idx === null ? null : (idx & 0xffff), usernameFragment: init.usernameFragment === undefined ? null : init.usernameFragment, p: cand ? parseCand(cand) : null });
    }
    get candidate() { return this._c.candidate; }
    get sdpMid() { return this._c.sdpMid; }
    get sdpMLineIndex() { return this._c.sdpMLineIndex; }
    get usernameFragment() { return this._c.usernameFragment; }
    toJSON() { return { candidate: this._c.candidate, sdpMid: this._c.sdpMid, sdpMLineIndex: this._c.sdpMLineIndex, usernameFragment: this._c.usernameFragment }; }
  }
  ['foundation', 'component', 'protocol', 'priority', 'address', 'port', 'type', 'tcpType', 'relatedAddress', 'relatedPort'].forEach(function (k) {
    defGetter(RTCIceCandidate.prototype, k, function () { return this._c.p ? this._c.p[k] : null; });
  });
  tag(RTCIceCandidate, 'RTCIceCandidate');
  global.RTCIceCandidate = RTCIceCandidate;

  // ---- RTCCertificate ---------------------------------------------------------------
  function RTCCertificate() { illegal(); }
  tag(RTCCertificate, 'RTCCertificate');
  defGetter(RTCCertificate.prototype, 'expires', function () { return this._k.expires; });
  defMethod(RTCCertificate.prototype, 'getFingerprints', function () { return this._k.fingerprints.map(function (f) { return { algorithm: f.algorithm, value: f.value }; }); });
  global.RTCCertificate = RTCCertificate;
  function mkCert(info) { var c = Object.create(RTCCertificate.prototype); hid(c, '_k', info); return c; }

  // ---- RTCStatsReport ------------------------------------------------------------------
  class RTCStatsReport extends Map {
    constructor(entries) { super(entries); }
  }
  tag(RTCStatsReport, 'RTCStatsReport');
  global.RTCStatsReport = RTCStatsReport;

  // ---- SDP parsing (media sections only) ------------------------------------------------
  function parseSdp(sdp) {
    var media = [], cur = null;
    String(sdp || '').split(/\r?\n/).forEach(function (l) {
      if (l.indexOf('m=') === 0) {
        var p = l.slice(2).split(' ');
        cur = { kind: p[0], port: +p[1], mid: null, dir: 'sendrecv', msids: [] };
        media.push(cur);
      } else if (cur) {
        if (l.indexOf('a=mid:') === 0) cur.mid = l.slice(6);
        else if (/^a=(sendrecv|sendonly|recvonly|inactive)$/.test(l)) cur.dir = l.slice(2);
        else if (l.indexOf('a=msid:') === 0) { var q = l.slice(7).split(' '); addMsid(cur, q[0], q[1]); }
        else { var m = /^a=ssrc:\d+ msid:(\S+)(?: (\S+))?/.exec(l); if (m) addMsid(cur, m[1], m[2]); }
      }
    });
    return media;
  }
  function addMsid(m, stream, track) {
    for (var i = 0; i < m.msids.length; i++) if (m.msids[i].stream === stream && m.msids[i].track === (track || '')) return;
    m.msids.push({ stream: stream, track: track || '' });
  }
  var REVERSE = { sendrecv: 'sendrecv', sendonly: 'recvonly', recvonly: 'sendonly', inactive: 'inactive' };

  // ---- codec capabilities (what the engine's media engine negotiates) ------------------
  var capCache = {};
  function caps(kind) {
    if (kind !== 'audio' && kind !== 'video') return null;
    if (!capCache[kind]) capCache[kind] = callJSON('rtcCaps', kind);
    return capCache[kind];
  }
  function capabilities(kind) {
    var c = caps(kind);
    if (!c) return null;
    return { codecs: c.codecs.map(function (x) { var o = {}; for (var k in x) o[k] = x[k]; return o; }), headerExtensions: c.headerExtensions.map(function (x) { return { uri: x.uri }; }) };
  }
  var AUX = /^(audio\/telephone-event|video\/rtx|video\/red|video\/ulpfec|video\/flexfec-03|audio\/red|audio\/cn)$/i;

  // ---- tracks: description for the engine, stop and enabled hooks -----------------------------------
  var hookMap = new WeakMap();
  function isTrack(t) { return t instanceof global.MediaStreamTrack || (!!t && typeof t === 'object' && (t.kind === 'audio' || t.kind === 'video') && 'readyState' in t); }
  function hooksOf(track, key) {
    var t = track._t;
    if (t) return t[key] || (t[key] = []);
    var m = hookMap.get(track);
    if (!m) { m = {}; hookMap.set(track, m); }
    return m[key] || (m[key] = []);
  }
  function hookStop(track, fn) {
    if (!track._t && !hookMap.has(track) && typeof track.stop === 'function') {
      var orig = track.stop;
      track.stop = function () { orig.apply(this, arguments); (hooksOf(track, 'stopHooks')).forEach(function (f) { try { f(); } catch (e) { /* ignore */ } }); };
    }
    hooksOf(track, 'stopHooks').push(fn);
  }
  function hookEnabled(track, fn) { hooksOf(track, 'enabledHooks').push(fn); }
  function trackDesc(track) {
    if (!track) return '';
    var ex = (track._t && track._t.extra) || {};
    var st = {};
    try { if (typeof track.getSettings === 'function') st = track.getSettings() || {}; } catch (e) { st = {}; }
    return JSON.stringify({
      id: track.id, kind: track.kind, deviceId: ex.deviceId || st.deviceId || '', label: track.label || '',
      width: (st.width | 0) || (ex.width | 0), height: (st.height | 0) || (ex.height | 0), frameRate: +(st.frameRate || ex.frameRate) || 0,
      canvas: track.canvas ? (global.__swIdOf ? global.__swIdOf(track.canvas) : 0) : 0, enabled: track.enabled !== false
    });
  }
  function watchTrack(rec, x, track) {
    var s = x.sender._s;
    hookStop(track, function () { if (s.track === track && !rec.closed) call('rtcSenderLive', rec.id, x.id, false); });
    hookEnabled(track, function (en) { if (s.track === track && !rec.closed) call('rtcTrackEnabled', rec.id, x.id, !!en); });
  }

  // ---- registry -----------------------------------------------------------------------------
  var pcs = new Map();

  // A track stopped by the page stops feeding its sender.
  (function () {
    var MST = global.MediaStreamTrack;
    if (!MST || !MST.prototype.stop || MST.prototype.stop.__rtc) return;
    var orig = MST.prototype.stop;
    var stop = function () {
      orig.apply(this, arguments);
      var h = this._t && this._t.stopHooks;
      if (h) h.forEach(function (f) { try { f(); } catch (e) { /* ignore */ } });
    };
    stop.__rtc = true;
    MST.prototype.stop = stop;
    // track.enabled = false makes the sender transmit black frames / silence
    var d = Object.getOwnPropertyDescriptor(MST.prototype, 'enabled');
    if (d && d.set && !d.set.__rtc) {
      var set = function (v) {
        d.set.call(this, v);
        var h = this._t && this._t.enabledHooks, en = !!v;
        if (h) h.forEach(function (f) { try { f(en); } catch (e) { /* ignore */ } });
      };
      set.__rtc = true;
      Object.defineProperty(MST.prototype, 'enabled', { get: d.get, set: set, configurable: true, enumerable: d.enumerable });
    }
  })();

  // ---- RTCDtlsTransport / RTCIceTransport / RTCSctpTransport -------------------------------
  function transportInfo(rec) {
    if (rec.closed) return rec.finalTransport || {};
    try { return callJSON('rtcTransport', rec.id) || {}; } catch (e) { return {}; }
  }
  function RTCDtlsTransport() { illegal(); }
  derive(RTCDtlsTransport);
  defGetter(RTCDtlsTransport.prototype, 'state', function () { var d = transportInfo(this._rec).dtls; return rec_closed(this._rec) ? 'closed' : (d ? d.state : 'new'); });
  defGetter(RTCDtlsTransport.prototype, 'iceTransport', function () { return iceTransportOf(this._rec); });
  defMethod(RTCDtlsTransport.prototype, 'getRemoteCertificates', function () { return []; });
  handlerProps(RTCDtlsTransport.prototype, ['statechange', 'error']);
  tag(RTCDtlsTransport, 'RTCDtlsTransport'); global.RTCDtlsTransport = RTCDtlsTransport;

  function RTCIceTransport() { illegal(); }
  derive(RTCIceTransport);
  function ice(self) { return transportInfo(self._rec).ice || {}; }
  defGetter(RTCIceTransport.prototype, 'state', function () { return rec_closed(this._rec) ? 'closed' : (ice(this).state || 'new'); });
  defGetter(RTCIceTransport.prototype, 'gatheringState', function () { return sts(this._rec)[1]; });
  defGetter(RTCIceTransport.prototype, 'role', function () { return ice(this).role || 'unknown'; });
  defGetter(RTCIceTransport.prototype, 'component', function () { return 'rtp'; });
  defMethod(RTCIceTransport.prototype, 'getLocalCandidates', function () { return this._rec.localCands.slice(); });
  defMethod(RTCIceTransport.prototype, 'getRemoteCandidates', function () { return this._rec.remoteCands.slice(); });
  defMethod(RTCIceTransport.prototype, 'getSelectedCandidatePair', function () {
    var i = ice(this);
    if (!i.local || !i.remote) return null;
    function mk(c) { var s = c.candidate; var o = { candidate: s, sdpMid: '0', sdpMLineIndex: 0 }; return new RTCIceCandidate(o); }
    return { local: mk(i.local), remote: mk(i.remote) };
  });
  defMethod(RTCIceTransport.prototype, 'getLocalParameters', function () { var p = ice(this).localParams; return p ? { usernameFragment: p.usernameFragment, password: p.password } : null; });
  defMethod(RTCIceTransport.prototype, 'getRemoteParameters', function () { var p = ice(this).remoteParams; return p ? { usernameFragment: p.usernameFragment, password: p.password } : null; });
  handlerProps(RTCIceTransport.prototype, ['statechange', 'gatheringstatechange', 'selectedcandidatepairchange']);
  tag(RTCIceTransport, 'RTCIceTransport'); global.RTCIceTransport = RTCIceTransport;

  function RTCSctpTransport() { illegal(); }
  derive(RTCSctpTransport);
  function sctp(self) { return transportInfo(self._rec).sctp || {}; }
  defGetter(RTCSctpTransport.prototype, 'state', function () { return rec_closed(this._rec) ? 'closed' : (sctp(this).state || 'connecting'); });
  defGetter(RTCSctpTransport.prototype, 'maxMessageSize', function () { var m = sctp(this).maxMessageSize; return m === undefined ? 65536 : (m || Infinity); });
  defGetter(RTCSctpTransport.prototype, 'maxChannels', function () { var m = sctp(this).maxChannels; return m === undefined ? null : m; });
  defGetter(RTCSctpTransport.prototype, 'transport', function () { return dtlsTransportOf(this._rec); });
  handlerProps(RTCSctpTransport.prototype, ['statechange']);
  tag(RTCSctpTransport, 'RTCSctpTransport'); global.RTCSctpTransport = RTCSctpTransport;

  function derive(C) { Object.setPrototypeOf(C.prototype, ET.prototype); Object.setPrototypeOf(C, ET); }
  function rec_closed(rec) { return rec.closed; }
  function mkTransport(C, rec) { var t = Object.create(C.prototype); ET.call(t); hid(t, '_rec', rec); return t; }
  function dtlsTransportOf(rec) { return rec.dtls || (rec.dtls = mkTransport(RTCDtlsTransport, rec)); }
  function iceTransportOf(rec) { return rec.iceT || (rec.iceT = mkTransport(RTCIceTransport, rec)); }
  function sctpTransportOf(rec) { return rec.sctpT || (rec.sctpT = mkTransport(RTCSctpTransport, rec)); }

  function sts(rec) {
    if (rec.closed) return rec.finalStates;
    var s = call('rtcStates', rec.id).split('|');
    if (rec.at) s[rec.at.i] = rec.at.v; // inside a state-change event: the value that event reports
    return s;
  }

  // ---- RTCDTMFSender -------------------------------------------------------------------------
  function RTCDTMFSender() { illegal(); }
  Object.setPrototypeOf(RTCDTMFSender.prototype, ET.prototype); Object.setPrototypeOf(RTCDTMFSender, ET);
  defGetter(RTCDTMFSender.prototype, 'toneBuffer', function () { return this._d.buf; });
  defGetter(RTCDTMFSender.prototype, 'canInsertDTMF', function () { return canDtmf(this._d.sender); });
  defMethod(RTCDTMFSender.prototype, 'insertDTMF', function (tones, duration, gap) {
    if (!canDtmf(this._d.sender)) throw dex("Failed to execute 'insertDTMF' on 'RTCDTMFSender': The 'canInsertDTMF' attribute is false: this sender cannot send DTMF.", 'InvalidStateError');
    tones = String(tones);
    if (!/^[0-9A-Da-d#*,]*$/.test(tones)) throw dex("Failed to execute 'insertDTMF' on 'RTCDTMFSender': Tones contain an invalid character.", 'InvalidCharacterError');
    duration = duration === undefined ? 100 : Math.min(6000, Math.max(40, +duration || 0));
    gap = gap === undefined ? 70 : Math.max(30, +gap || 0);
    var d = this._d, self = this;
    d.buf = tones.toUpperCase();
    if (d.running) return;
    d.running = true;
    // NOTE: tones are scheduled and reported (tonechange) but no RFC 4733 telephone-event packets are sent.
    (function next() {
      if (!d.buf) { d.running = false; self.dispatchEvent(new RTCDTMFToneChangeEvent('tonechange', { tone: '' })); return; }
      var t = d.buf[0]; d.buf = d.buf.slice(1);
      self.dispatchEvent(new RTCDTMFToneChangeEvent('tonechange', { tone: t }));
      global.setTimeout(next, (t === ',' ? 2000 : duration) + gap);
    })();
  });
  handlerProps(RTCDTMFSender.prototype, ['tonechange']);
  tag(RTCDTMFSender, 'RTCDTMFSender'); global.RTCDTMFSender = RTCDTMFSender;
  function canDtmf(s) {
    var x = s._s.tr._x;
    return !x.rec.closed && !x.stopped && x.kind === 'audio' && !!s._s.track && s._s.track.readyState === 'live' && (x.dir === 'sendrecv' || x.dir === 'sendonly');
  }

  // ---- RTCRtpSender / RTCRtpReceiver / RTCRtpTransceiver --------------------------------------
  function RTCRtpSender() { illegal(); }
  tag(RTCRtpSender, 'RTCRtpSender');
  defGetter(RTCRtpSender.prototype, 'track', function () { return this._s.track; });
  defGetter(RTCRtpSender.prototype, 'transport', function () { var r = this._s.tr._x.rec; return r.hasDesc ? dtlsTransportOf(r) : null; });
  defGetter(RTCRtpSender.prototype, 'dtmf', function () {
    var s = this._s;
    if (s.tr._x.kind !== 'audio') return null;
    if (!s.dtmf) { var d = Object.create(RTCDTMFSender.prototype); ET.call(d); hid(d, '_d', { sender: this, buf: '', running: false }); s.dtmf = d; }
    return s.dtmf;
  });
  defMethod(RTCRtpSender.prototype, 'replaceTrack', function (track) {
    var self = this, s = this._s, x = s.tr._x;
    return new Promise(function (res, rej) {
      try {
        if (track !== null && !isTrack(track)) throw new TypeError("Failed to execute 'replaceTrack' on 'RTCRtpSender': parameter 1 is not of type 'MediaStreamTrack'.");
        if (x.rec.closed || x.stopped) throw dex("Failed to execute 'replaceTrack' on 'RTCRtpSender': The transceiver is stopped.", 'InvalidStateError');
        if (track && track.kind !== x.kind) throw new TypeError("Failed to execute 'replaceTrack' on 'RTCRtpSender': Track kind does not match the sender's.");
        attach(x, track, s.streams);
        res(undefined);
      } catch (e) { rej(e); }
    });
  });
  defMethod(RTCRtpSender.prototype, 'getParameters', function () {
    var s = this._s, x = s.tr._x;
    s.tx = String(Math.random()).slice(2);
    var enc = s.encodings.map(function (e) { var o = {}; for (var k in e) o[k] = e[k]; return o; });
    var hp = { codecs: [], headerExtensions: [] };
    if (!x.rec.closed && x.mid !== null) { try { hp = callJSON('rtcSenderParams', x.rec.id, x.id) || hp; } catch (e) { /* keep empty */ } }
    return { transactionId: s.tx, encodings: enc, headerExtensions: hp.headerExtensions || [], rtcp: { cname: 'sweef', reducedSize: false }, codecs: hp.codecs || [] };
  });
  defMethod(RTCRtpSender.prototype, 'setParameters', function (p) {
    var s = this._s, x = s.tr._x;
    return new Promise(function (res, rej) {
      try {
        if (!p || typeof p !== 'object') throw new TypeError("Failed to execute 'setParameters' on 'RTCRtpSender': The provided value is not of type 'RTCRtpSendParameters'.");
        if (x.rec.closed || x.stopped) throw dex("Failed to execute 'setParameters' on 'RTCRtpSender': The transceiver is stopped.", 'InvalidStateError');
        if (!s.tx || p.transactionId !== s.tx) throw dex("Failed to execute 'setParameters' on 'RTCRtpSender': getParameters() needs to be called before setParameters().", 'InvalidStateError');
        var enc = p.encodings || [];
        if (enc.length !== s.encodings.length) throw dex("Failed to execute 'setParameters' on 'RTCRtpSender': Attempted to change the number of encodings.", 'InvalidModificationError');
        enc.forEach(function (e) {
          if (e.maxBitrate !== undefined && !(e.maxBitrate >= 0)) throw new RangeError('maxBitrate must be non-negative');
          if (e.maxFramerate !== undefined && !(e.maxFramerate >= 0)) throw new RangeError('maxFramerate must be non-negative');
          if (e.scaleResolutionDownBy !== undefined && !(e.scaleResolutionDownBy >= 1)) throw new RangeError('scaleResolutionDownBy must be >= 1');
        });
        s.encodings = enc.map(function (e) { var o = {}; for (var k in e) o[k] = e[k]; if (o.active === undefined) o.active = true; return o; });
        s.tx = null;
        pushParams(x);
        var any = s.encodings.some(function (e) { return e.active !== false; });
        if (s.track) call('rtcSenderLive', x.rec.id, x.id, any && s.track.readyState === 'live');
        res(undefined);
      } catch (e) { rej(e); }
    });
  });
  // a single layer is sent: the first encoding decides (the others, if the page asked for simulcast, are ignored)
  function pushParams(x) {
    var e = x.sender._s.encodings[0] || {};
    call('rtcSetParams', x.rec.id, x.id, JSON.stringify({ active: e.active !== false, maxBitrate: e.maxBitrate, maxFramerate: e.maxFramerate, scaleResolutionDownBy: e.scaleResolutionDownBy }));
  }
  defMethod(RTCRtpSender.prototype, 'setStreams', function () {
    var x = this._s.tr._x;
    if (x.rec.closed) throw dex("Failed to execute 'setStreams' on 'RTCRtpSender': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
    this._s.streams = Array.prototype.slice.call(arguments);
  });
  defMethod(RTCRtpSender.prototype, 'getStats', function () { return statsFor(this._s.tr._x.rec, 'outbound-rtp', this._s.tr._x.kind); });
  defMethod(RTCRtpSender, 'getCapabilities', function (kind) { return capabilities(kind); });
  global.RTCRtpSender = RTCRtpSender;

  function RTCRtpReceiver() { illegal(); }
  tag(RTCRtpReceiver, 'RTCRtpReceiver');
  defGetter(RTCRtpReceiver.prototype, 'track', function () { return this._r.track; });
  defGetter(RTCRtpReceiver.prototype, 'transport', function () { var r = this._r.tr._x.rec; return r.hasDesc ? dtlsTransportOf(r) : null; });
  defGetter(RTCRtpReceiver.prototype, 'jitterBufferTarget', function () { return null; }, function () { });
  defMethod(RTCRtpReceiver.prototype, 'getParameters', function () { var c = capabilities(this._r.tr._x.kind) || { codecs: [], headerExtensions: [] }; return { headerExtensions: c.headerExtensions, rtcp: { cname: '', reducedSize: false }, codecs: c.codecs.map(function (k) { return k; }) }; });
  defMethod(RTCRtpReceiver.prototype, 'getContributingSources', function () { return []; });
  defMethod(RTCRtpReceiver.prototype, 'getSynchronizationSources', function () {
    var x = this._r.tr._x;
    if (x.rec.closed) return [];
    try { return callJSON('rtcSources', x.rec.id, x.id) || []; } catch (e) { return []; }
  });
  defMethod(RTCRtpReceiver.prototype, 'getStats', function () { return statsFor(this._r.tr._x.rec, 'inbound-rtp', this._r.tr._x.kind); });
  defMethod(RTCRtpReceiver, 'getCapabilities', function (kind) { return capabilities(kind); });
  global.RTCRtpReceiver = RTCRtpReceiver;

  function RTCRtpTransceiver() { illegal(); }
  tag(RTCRtpTransceiver, 'RTCRtpTransceiver');
  defGetter(RTCRtpTransceiver.prototype, 'mid', function () { return this._x.mid; });
  defGetter(RTCRtpTransceiver.prototype, 'sender', function () { return this._x.sender; });
  defGetter(RTCRtpTransceiver.prototype, 'receiver', function () { return this._x.receiver; });
  defGetter(RTCRtpTransceiver.prototype, 'stopped', function () { return this._x.stopped; });
  defGetter(RTCRtpTransceiver.prototype, 'currentDirection', function () { return this._x.stopped ? 'stopped' : this._x.curDir; });
  var DIRS = ['sendrecv', 'sendonly', 'recvonly', 'inactive'];
  defGetter(RTCRtpTransceiver.prototype, 'direction', function () { return this._x.stopped ? 'stopped' : this._x.dir; }, function (v) {
    var x = this._x;
    if (DIRS.indexOf(v) < 0) return;
    if (x.rec.closed) throw dex("Failed to set the 'direction' property on 'RTCRtpTransceiver': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
    if (x.stopped) throw dex("Failed to set the 'direction' property on 'RTCRtpTransceiver': The transceiver is stopped.", 'InvalidStateError');
    if (x.dir === v) return;
    call('rtcSetDirection', x.rec.id, x.id, v);
    x.dir = v;
    queueNegotiation(x.rec);
  });
  defGetter(RTCRtpTransceiver.prototype, 'headerExtensionsToNegotiate', function () { return []; });
  defGetter(RTCRtpTransceiver.prototype, 'headerExtensionsNegotiated', function () { return []; });
  defMethod(RTCRtpTransceiver.prototype, 'stop', function () {
    var x = this._x;
    if (x.rec.closed) throw dex("Failed to execute 'stop' on 'RTCRtpTransceiver': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
    if (x.stopped) return;
    call('rtcStopTransceiver', x.rec.id, x.id);
    x.stopped = true;
    x.dir = 'inactive';
    endReceiver(x);
    queueNegotiation(x.rec);
  });
  defMethod(RTCRtpTransceiver.prototype, 'setCodecPreferences', function (codecs) {
    var x = this._x;
    if (!Array.isArray(codecs)) throw new TypeError("Failed to execute 'setCodecPreferences' on 'RTCRtpTransceiver': The provided value cannot be converted to a sequence.");
    var have = caps(x.kind).codecs;
    codecs.forEach(function (c) {
      var ok = c && have.some(function (k) { return k.mimeType.toLowerCase() === String(c.mimeType).toLowerCase() && k.clockRate === c.clockRate && (k.channels || 0) === (c.channels || 0) && (!c.sdpFmtpLine || c.sdpFmtpLine === k.sdpFmtpLine); });
      if (!ok) throw dex("Failed to execute 'setCodecPreferences' on 'RTCRtpTransceiver': Invalid codec.", 'InvalidModificationError');
    });
    if (codecs.length && codecs.every(function (c) { return AUX.test(c.mimeType); })) throw dex("Failed to execute 'setCodecPreferences' on 'RTCRtpTransceiver': Invalid codec preferences: no media codec left.", 'InvalidModificationError');
    x.codecPrefs = codecs.slice();
    call('rtcSetCodecPrefs', x.rec.id, x.id, JSON.stringify(codecs));
  });
  global.RTCRtpTransceiver = RTCRtpTransceiver;

  function endReceiver(x) {
    var t = x.receiver._r.track;
    if (t._t.ready !== 'ended') { t._t.ready = 'ended'; fire(t, 'ended'); }
    x.curDir = x.stopped ? 'stopped' : x.curDir;
  }

  function newTransceiver(rec, id, kind, dir) {
    var tr = Object.create(RTCRtpTransceiver.prototype);
    var x = { rec: rec, id: id, kind: kind, mid: null, dir: dir, curDir: null, stopped: false, recvActive: false, codecPrefs: null, els: [], vw: 0, vh: 0 };
    var snd = Object.create(RTCRtpSender.prototype);
    hid(snd, '_s', { tr: tr, track: null, streams: [], encodings: [{ active: true }], tx: null, dtmf: null });
    var rcv = Object.create(RTCRtpReceiver.prototype);
    var track = global.__swMakeTrack(kind, '', { remote: true });
    track._t.muted = true;
    track._t.rtc = { pc: rec.id, tr: id };
    Object.defineProperty(track, 'muted', { get: function () { return track._t.muted === true; }, configurable: true, enumerable: true });
    hid(rcv, '_r', { tr: tr, track: track });
    x.sender = snd; x.receiver = rcv;
    hid(tr, '_x', x);
    rec.trs.push(tr);
    rec.byId.set(id, tr);
    return tr;
  }

  function syncSnap(rec, snap) {
    var created = [];
    snap.forEach(function (s) {
      var tr = rec.byId.get(s.id);
      if (!tr) { tr = newTransceiver(rec, s.id, s.kind, s.direction); created.push(tr); }
      var x = tr._x;
      x.mid = s.mid || null;
      if (!x.stopped) x.dir = s.direction;
    });
    return created;
  }
  function refresh(rec) { return syncSnap(rec, callJSON('rtcTransceivers', rec.id)); }
  function byMid(rec, mid) {
    for (var i = 0; i < rec.trs.length; i++) if (rec.trs[i]._x.mid === mid && mid !== null) return rec.trs[i];
    return null;
  }

  // attach a page track to a sender (null detaches).
  function attach(x, track, streams) {
    var s = x.sender._s, rec = x.rec;
    var sid = streams && streams.length ? streams[0].id : '';
    if (track) {
      var live = track.readyState === 'live';
      s.track = track;
      call('rtcSetSender', rec.id, x.id, x.kind, trackDesc(track), sid, live);
      watchTrack(rec, x, track);
    } else {
      call('rtcSetSender', rec.id, x.id, x.kind, '', '', false);
    }
    s.track = track;
  }

  function queueNegotiation(rec) {
    if (rec.nnQueued || rec.closed) return;
    rec.nnQueued = true;
    task(function () {
      rec.nnQueued = false;
      if (rec.closed || sts(rec)[0] !== 'stable') return;
      fire(rec.pc, 'negotiationneeded');
    });
  }

  // ---- RTCDataChannel ----------------------------------------------------------------------------
  function RTCDataChannel() { illegal(); }
  Object.setPrototypeOf(RTCDataChannel.prototype, ET.prototype); Object.setPrototypeOf(RTCDataChannel, ET);
  tag(RTCDataChannel, 'RTCDataChannel');
  [['label', 'label'], ['ordered', 'ordered'], ['maxPacketLifeTime', 'maxPacketLifeTime'], ['maxRetransmits', 'maxRetransmits'], ['protocol', 'protocol'], ['negotiated', 'negotiated'], ['id', 'id'], ['readyState', 'state']].forEach(function (p) {
    defGetter(RTCDataChannel.prototype, p[0], function () { var v = this._d[p[1]]; return v === undefined ? null : v; });
  });
  defGetter(RTCDataChannel.prototype, 'priority', function () { return 'low'; });
  defGetter(RTCDataChannel.prototype, 'reliable', function () { return this._d.maxPacketLifeTime === undefined && this._d.maxRetransmits === undefined; });
  defGetter(RTCDataChannel.prototype, 'bufferedAmount', function () {
    var d = this._d;
    if (d.state === 'closed' || d.rec.closed) return 0;
    try { return callJSON('rtcDCInfo', d.rec.id, d.dc).buffered; } catch (e) { return 0; }
  });
  defGetter(RTCDataChannel.prototype, 'bufferedAmountLowThreshold', function () { return this._d.low; }, function (v) {
    v = Number(v) >>> 0;
    this._d.low = v;
    if (!this._d.rec.closed && this._d.state !== 'closed') call('rtcDCThreshold', this._d.rec.id, this._d.dc, v);
  });
  defGetter(RTCDataChannel.prototype, 'binaryType', function () { return this._d.bt; }, function (v) {
    if (v === 'blob' || v === 'arraybuffer') this._d.bt = v;
    else throw new TypeError("Failed to set the 'binaryType' property on 'RTCDataChannel': The provided value '" + v + "' is not a valid enum value of type BinaryType.");
  });
  defMethod(RTCDataChannel.prototype, 'send', function (data) {
    var d = this._d;
    if (d.state !== 'open' || d.rec.closed) throw dex("Failed to execute 'send' on 'RTCDataChannel': RTCDataChannel.readyState is not 'open'", 'InvalidStateError');
    if (typeof data === 'string') {
      call('rtcDCSend', d.rec.id, d.dc, data, false);
      return;
    }
    var bytes;
    if (data && data._bytes instanceof Uint8Array && typeof data.size === 'number') bytes = data._bytes;
    else if (data instanceof ArrayBuffer) bytes = new Uint8Array(data);
    else if (ArrayBuffer.isView(data)) bytes = new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    else { call('rtcDCSend', d.rec.id, d.dc, String(data), false); return; }
    call('rtcDCSend', d.rec.id, d.dc, bytesToB64(bytes), true);
  });
  defMethod(RTCDataChannel.prototype, 'close', function () {
    var d = this._d;
    if (d.state === 'closing' || d.state === 'closed') return;
    d.state = 'closing';
    fire(this, 'closing');
    if (!d.rec.closed) call('rtcDCClose', d.rec.id, d.dc);
  });
  handlerProps(RTCDataChannel.prototype, ['open', 'message', 'bufferedamountlow', 'error', 'closing', 'close']);
  global.RTCDataChannel = RTCDataChannel;

  function newDC(rec, info, state) {
    var c = Object.create(RTCDataChannel.prototype);
    ET.call(c);
    var d = { rec: rec, dc: info.dc, label: info.label, ordered: info.ordered !== false, protocol: info.protocol || '', negotiated: !!info.negotiated, id: info.id === undefined ? null : info.id, state: state, low: 0, bt: 'blob' };
    if (info.maxPacketLifeTime !== undefined) d.maxPacketLifeTime = info.maxPacketLifeTime;
    if (info.maxRetransmits !== undefined) d.maxRetransmits = info.maxRetransmits;
    hid(c, '_d', d);
    rec.dcs.set(info.dc, c);
    return c;
  }

  // ---- RTCPeerConnection ------------------------------------------------------------------------------
  var BUNDLE = ['balanced', 'max-compat', 'max-bundle'], POLICY = ['all', 'relay'], MUX = ['negotiate', 'require'];
  function checkEnum(v, list, what, ctor) {
    if (v !== undefined && list.indexOf(v) < 0) throw new TypeError("Failed to construct 'RTCPeerConnection': The provided value '" + v + "' is not a valid enum value of type " + what + '.');
  }
  function normConfig(cfg, ctor) {
    cfg = cfg === undefined || cfg === null ? {} : cfg;
    if (typeof cfg !== 'object') throw new TypeError("Failed to construct 'RTCPeerConnection': The provided value is not of type 'RTCConfiguration'.");
    checkEnum(cfg.bundlePolicy, BUNDLE, 'RTCBundlePolicy');
    checkEnum(cfg.iceTransportPolicy, POLICY, 'RTCIceTransportPolicy');
    checkEnum(cfg.rtcpMuxPolicy, MUX, 'RTCRtcpMuxPolicy');
    var servers = [];
    var list = cfg.iceServers === undefined ? [] : cfg.iceServers;
    if (!list || typeof list[Symbol.iterator] !== 'function') throw new TypeError("Failed to construct 'RTCPeerConnection': Failed to read the 'iceServers' property from 'RTCConfiguration': The provided value cannot be converted to a sequence.");
    Array.from(list).forEach(function (s) {
      if (!s || typeof s !== 'object' || s.urls === undefined) throw new TypeError("Failed to construct 'RTCPeerConnection': Failed to read the 'iceServers' property from 'RTCConfiguration': Failed to read the 'urls' property from 'RTCIceServer': required member urls is undefined.");
      var urls = Array.isArray(s.urls) ? s.urls.map(String) : [String(s.urls)];
      if (!urls.length) throw dex("Failed to construct 'RTCPeerConnection': 'urls' is empty.", 'SyntaxError');
      urls.forEach(function (u) {
        var m = /^(stuns?|turns?):([^?]+)(\?.*)?$/i.exec(u);
        if (!m || !m[2] || /^\/\//.test(m[2])) throw dex("Failed to construct 'RTCPeerConnection': '" + u + "' is not a valid STUN/TURN URL.", 'SyntaxError');
        if (/^turns?$/i.test(m[1])) {
          if (typeof s.username !== 'string' || typeof s.credential !== 'string' || s.username === '' && s.credential === '') throw dex("Failed to construct 'RTCPeerConnection': Both username and credential are required when the URL scheme is \"turn\" or \"turns\".", 'InvalidAccessError');
        }
      });
      if (s.credential !== undefined && typeof s.credential !== 'string') throw dex("Failed to construct 'RTCPeerConnection': credential must be a string.", 'NotSupportedError');
      servers.push({ urls: urls, username: s.username === undefined ? '' : String(s.username), credential: s.credential === undefined ? '' : s.credential });
    });
    var certs = [];
    if (cfg.certificates !== undefined) {
      Array.from(cfg.certificates).forEach(function (c) {
        if (!(c instanceof RTCCertificate)) throw new TypeError("Failed to construct 'RTCPeerConnection': Failed to read the 'certificates' property from 'RTCConfiguration': Failed to convert value to 'RTCCertificate'.");
        if (c.expires <= Date.now()) throw dex("Failed to construct 'RTCPeerConnection': Expired certificate(s).", 'InvalidAccessError');
        certs.push(c);
      });
    }
    return {
      iceServers: servers,
      iceTransportPolicy: cfg.iceTransportPolicy || 'all',
      bundlePolicy: cfg.bundlePolicy || 'balanced',
      rtcpMuxPolicy: cfg.rtcpMuxPolicy || 'require',
      iceCandidatePoolSize: cfg.iceCandidatePoolSize === undefined ? 0 : (cfg.iceCandidatePoolSize >>> 0),
      certificates: certs
    };
  }
  function hostConfig(c) {
    return JSON.stringify({ iceServers: c.iceServers, iceTransportPolicy: c.iceTransportPolicy, bundlePolicy: c.bundlePolicy, rtcpMuxPolicy: c.rtcpMuxPolicy, iceCandidatePoolSize: c.iceCandidatePoolSize, certificates: c.certificates.map(function (x) { return x._k.id; }) });
  }

  class RTCPeerConnection extends ET {
    constructor(config) {
      super();
      var cfg = normConfig(config);
      var id = call('rtcNew', hostConfig(cfg));
      var rec = { id: id, pc: this, closed: false, cfg: cfg, trs: [], byId: new Map(), dcs: new Map(), remoteStreams: new Map(), localCands: [], remoteCands: [], dtls: null, iceT: null, sctpT: null, nnQueued: false, tok: 0, tickets: new Map(), at: null, held: [], releasing: false, hasDesc: false, finalStates: null, finalState: null, finalTransport: null };
      hid(this, '_p', rec);
      pcs.set(id, rec);
    }
    static generateCertificate(alg) {
      return new Promise(function (res, rej) {
        try {
          var name = typeof alg === 'string' ? alg : (alg && alg.name);
          var info;
          if (/^ecdsa$/i.test(name)) {
            if (alg.namedCurve !== undefined && alg.namedCurve !== 'P-256') throw dex("Failed to execute 'generateCertificate' on 'RTCPeerConnection': Only P-256 is supported.", 'NotSupportedError');
            info = callJSON('rtcGenCert', 'ECDSA', 0);
          } else if (/^RSASSA-PKCS1-v1_5$/i.test(name)) {
            info = callJSON('rtcGenCert', 'RSA', alg.modulusLength || 2048);
          } else throw dex("Failed to execute 'generateCertificate' on 'RTCPeerConnection': The 1st argument provided is an unsupported algorithm.", 'NotSupportedError');
          res(mkCert(info));
        } catch (e) { rej(e); }
      });
    }
    get signalingState() { return sts(this._p)[0]; }
    get iceGatheringState() { return sts(this._p)[1]; }
    get iceConnectionState() { return sts(this._p)[2]; }
    get connectionState() { return sts(this._p)[3]; }
    get canTrickleIceCandidates() { var r = this._p; if (r.closed) return r.finalState && r.finalState.remote ? true : null; return stateOf(r).remote ? !!stateOf(r).trickle : null; }
    get localDescription() { return mkDesc(stateOf(this._p).local); }
    get currentLocalDescription() { return mkDesc(stateOf(this._p).currentLocal); }
    get pendingLocalDescription() { return mkDesc(stateOf(this._p).pendingLocal); }
    get remoteDescription() { return mkDesc(stateOf(this._p).remote); }
    get currentRemoteDescription() { return mkDesc(stateOf(this._p).currentRemote); }
    get pendingRemoteDescription() { return mkDesc(stateOf(this._p).pendingRemote); }
    get sctp() { var r = this._p; return r.hasSctp ? sctpTransportOf(r) : null; }
    get idpErrorInfo() { return null; }
    get idpLoginUrl() { return null; }
    get peerIdentity() { return new Promise(function () { }); }
    get onaddstream() { return null; }
    set onaddstream(v) { }

    getConfiguration() {
      var c = this._p.cfg;
      return { iceServers: c.iceServers.map(function (s) { return { urls: s.urls.slice(), username: s.username, credential: s.credential }; }), iceTransportPolicy: c.iceTransportPolicy, bundlePolicy: c.bundlePolicy, rtcpMuxPolicy: c.rtcpMuxPolicy, iceCandidatePoolSize: c.iceCandidatePoolSize, certificates: c.certificates.slice() };
    }
    setConfiguration(config) {
      var rec = this._p;
      if (rec.closed) throw dex("Failed to execute 'setConfiguration' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
      var merged = Object.assign({}, rec.cfg, config || {});
      if (config && config.certificates === undefined) merged.certificates = rec.cfg.certificates;
      var cfg = normConfig(merged);
      if (cfg.bundlePolicy !== rec.cfg.bundlePolicy) throw dex("Failed to execute 'setConfiguration' on 'RTCPeerConnection': Attempted to modify the PeerConnection's configuration in an unsupported way.", 'InvalidModificationError');
      call('rtcSetConfig', rec.id, hostConfig(cfg));
      rec.cfg = cfg;
    }
    close() {
      var rec = this._p;
      if (rec.closed) return;
      try { rec.finalState = callJSON('rtcState', rec.id); rec.finalTransport = callJSON('rtcTransport', rec.id); } catch (e) { rec.finalState = {}; rec.finalTransport = {}; }
      var s = sts(rec);
      rec.finalStates = ['closed', s[1], 'closed', 'closed'];
      rec.closed = true;
      pcs.delete(rec.id);
      try { sw.rtcClose(rec.id); } catch (e) { /* ignore */ }
      rec.trs.forEach(function (tr) { var x = tr._x; x.stopped = true; endReceiver(x); });
      rec.dcs.forEach(function (c) { c._d.state = 'closed'; });
      rec.tickets.forEach(function (f) { f(dex("The RTCPeerConnection was closed before the operation finished.", 'InvalidStateError')); });
      rec.tickets.clear();
    }
    restartIce() {
      var rec = this._p;
      if (rec.closed) return;
      rec.iceRestart = true; // the next offer carries new ICE credentials (pion restarts via OfferOptions)
      queueNegotiation(rec);
    }

    createOffer(options, failure) {
      var rec = this._p;
      var p = op(rec, 'createOffer', function () {
        options = (typeof options === 'object' && options) || {};
        ['audio', 'video'].forEach(function (k) {
          var v = options['offerToReceive' + k[0].toUpperCase() + k.slice(1)];
          if (v && !rec.trs.some(function (t) { return t._x.kind === k && !t._x.stopped; })) {
            var r = callJSON('rtcAddTransceiver', rec.id, k, 'recvonly', '', '');
            syncSnap(rec, r.all);
          }
        });
        var sdp = call('rtcCreateOffer', rec.id, JSON.stringify({ iceRestart: !!options.iceRestart || !!rec.iceRestart }));
        return new RTCSessionDescription({ type: 'offer', sdp: sdp });
      });
      return legacy(p, arguments);
    }
    createAnswer(options, failure) {
      var rec = this._p;
      var p = op(rec, 'createAnswer', function () {
        var st = sts(rec)[0];
        if (st !== 'have-remote-offer' && st !== 'have-local-pranswer') throw dex("Failed to execute 'createAnswer' on 'RTCPeerConnection': PeerConnection cannot create an answer in a state other than have-remote-offer or have-local-pranswer.", 'InvalidStateError');
        var sdp = call('rtcCreateAnswer', rec.id, '{}');
        return new RTCSessionDescription({ type: 'answer', sdp: sdp });
      });
      return legacy(p, arguments);
    }
    setLocalDescription(desc) {
      var rec = this._p;
      var p = op(rec, 'setLocalDescription', function () {
        var st = sts(rec)[0];
        var type = desc && desc.type;
        var sdp = desc && desc.sdp;
        if (type !== undefined && SDP_TYPES.indexOf(type) < 0) throw new TypeError("Failed to execute 'setLocalDescription' on 'RTCPeerConnection': The provided value '" + type + "' is not a valid enum value of type RTCSdpType.");
        if (!type) type = (st === 'stable' || st === 'have-local-offer' || st === 'have-remote-pranswer') ? 'offer' : 'answer';
        if (type !== 'rollback' && !sdp) {
          if (type === 'offer') sdp = call('rtcCreateOffer', rec.id, JSON.stringify({ iceRestart: !!rec.iceRestart }));
          else {
            if (st !== 'have-remote-offer' && st !== 'have-local-pranswer') throw dex("Failed to execute 'setLocalDescription' on 'RTCPeerConnection': Called in wrong signalingState: " + st, 'InvalidStateError');
            sdp = call('rtcCreateAnswer', rec.id, '{}');
          }
        }
        call('rtcSetLocal', rec.id, type, sdp || '');
        if (type === 'offer') rec.iceRestart = false;
        rec.hasDesc = rec.hasDesc || type !== 'rollback';
        refresh(rec);
        if (type === 'answer' || type === 'pranswer') {
          parseSdp(sdp).forEach(function (m) {
            var tr = byMid(rec, m.mid);
            if (!tr) return;
            if (m.port === 0) { tr._x.stopped = true; tr._x.curDir = 'stopped'; endReceiver(tr._x); } else tr._x.curDir = m.dir;
          });
        }
        noteSctp(rec, sdp);
      });
      return legacy(p, arguments);
    }
    setRemoteDescription(desc) {
      var rec = this._p;
      var p = op(rec, 'setRemoteDescription', function () {
        if (desc === undefined || desc === null || typeof desc !== 'object') throw new TypeError("Failed to execute 'setRemoteDescription' on 'RTCPeerConnection': parameter 1 ('description') is not an object.");
        if (SDP_TYPES.indexOf(desc.type) < 0) throw new TypeError("Failed to execute 'setRemoteDescription' on 'RTCPeerConnection': The provided value '" + desc.type + "' is not a valid enum value of type RTCSdpType.");
        var sdp = desc.sdp === undefined ? '' : String(desc.sdp);
        call('rtcSetRemote', rec.id, desc.type, sdp);
        rec.hasDesc = rec.hasDesc || desc.type !== 'rollback';
        refresh(rec);
        if (desc.type !== 'rollback') processRemote(rec, desc.type, sdp);
        noteSctp(rec, sdp);
      });
      return legacy(p, arguments);
    }
    addIceCandidate(cand, ok, failure) {
      var rec = this._p;
      var p = op(rec, 'addIceCandidate', function () {
        if (cand === null || cand === undefined || cand.candidate === '' || (typeof cand === 'object' && cand.candidate === undefined)) {
          if (!stateOf(rec).remote) throw dex("Failed to execute 'addIceCandidate' on 'RTCPeerConnection': The remote description was null", 'InvalidStateError');
          return undefined;
        }
        var init = cand instanceof RTCIceCandidate ? cand.toJSON() : cand;
        if (typeof init !== 'object') throw new TypeError("Failed to execute 'addIceCandidate' on 'RTCPeerConnection': The provided value is not of type 'RTCIceCandidateInit'.");
        if ((init.sdpMid === undefined || init.sdpMid === null) && (init.sdpMLineIndex === undefined || init.sdpMLineIndex === null)) throw new TypeError("Failed to execute 'addIceCandidate' on 'RTCPeerConnection': Candidate missing values for both sdpMid and sdpMLineIndex");
        if (!stateOf(rec).remote) throw dex("Failed to execute 'addIceCandidate' on 'RTCPeerConnection': The remote description was null", 'InvalidStateError');
        var line = String(init.candidate).replace(/^a=/, '');
        if (!parseCand(line)) throw dex("Failed to execute 'addIceCandidate' on 'RTCPeerConnection': Error processing ICE candidate", 'OperationError');
        call('rtcAddIce', rec.id, JSON.stringify({ candidate: line, sdpMid: init.sdpMid === undefined ? null : init.sdpMid, sdpMLineIndex: init.sdpMLineIndex === undefined ? null : init.sdpMLineIndex, usernameFragment: init.usernameFragment === undefined ? null : init.usernameFragment }));
        rec.remoteCands.push(new RTCIceCandidate({ candidate: line, sdpMid: init.sdpMid === undefined ? null : init.sdpMid, sdpMLineIndex: init.sdpMLineIndex === undefined ? null : init.sdpMLineIndex }));
      });
      return legacy(p, arguments, 1);
    }

    addTrack(track) {
      var rec = this._p;
      if (rec.closed) throw dex("Failed to execute 'addTrack' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
      if (!isTrack(track)) throw new TypeError("Failed to execute 'addTrack' on 'RTCPeerConnection': parameter 1 is not of type 'MediaStreamTrack'.");
      var streams = Array.prototype.slice.call(arguments, 1);
      for (var i = 0; i < rec.trs.length; i++) {
        var o = rec.trs[i]._x;
        if (!o.stopped && o.sender._s.track === track) throw dex("Failed to execute 'addTrack' on 'RTCPeerConnection': A sender already exists for the track.", 'InvalidAccessError');
      }
      var r = callJSON('rtcAddTrack', rec.id, track.kind, trackDesc(track), streams.length ? streams[0].id : '');
      syncSnap(rec, r.all);
      var tr = rec.byId.get(r.id);
      var s = tr._x.sender._s;
      s.track = track; s.streams = streams;
      var x = tr._x;
      watchTrack(rec, x, track);
      if (track.readyState !== 'live') call('rtcSenderLive', rec.id, x.id, false);
      return x.sender;
    }
    removeTrack(sender) {
      var rec = this._p;
      if (rec.closed) throw dex("Failed to execute 'removeTrack' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
      if (!(sender instanceof RTCRtpSender)) throw new TypeError("Failed to execute 'removeTrack' on 'RTCPeerConnection': parameter 1 is not of type 'RTCRtpSender'.");
      var x = sender._s.tr._x;
      if (x.rec !== rec) throw dex("Failed to execute 'removeTrack' on 'RTCPeerConnection': The sender was not created by this peer connection.", 'InvalidAccessError');
      if (!sender._s.track) return;
      call('rtcRemoveTrack', rec.id, x.id);
      sender._s.track = null;
      refresh(rec);
    }
    addTransceiver(trackOrKind, init) {
      var rec = this._p;
      if (rec.closed) throw dex("Failed to execute 'addTransceiver' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
      var kind, track = null;
      if (isTrack(trackOrKind)) { track = trackOrKind; kind = track.kind; }
      else if (trackOrKind === 'audio' || trackOrKind === 'video') kind = trackOrKind;
      else throw new TypeError("Failed to execute 'addTransceiver' on 'RTCPeerConnection': The provided value '" + trackOrKind + "' is not a valid enum value of type RTCRtpTransceiverKind.");
      init = init || {};
      var dir = init.direction === undefined ? 'sendrecv' : init.direction;
      if (DIRS.indexOf(dir) < 0) throw new TypeError("Failed to execute 'addTransceiver' on 'RTCPeerConnection': The provided value '" + dir + "' is not a valid enum value of type RTCRtpTransceiverDirection.");
      var streams = init.streams ? Array.from(init.streams) : [];
      var encs = init.sendEncodings ? Array.from(init.sendEncodings) : [];
      var r = callJSON('rtcAddTransceiver', rec.id, kind, dir, track ? trackDesc(track) : '', streams.length ? streams[0].id : '');
      syncSnap(rec, r.all);
      var tr = rec.byId.get(r.id), x = tr._x, s = x.sender._s;
      if (encs.length) { s.encodings = encs.map(function (e) { var o = {}; for (var k in e) o[k] = e[k]; if (o.active === undefined) o.active = true; return o; }); pushParams(x); }
      s.streams = streams;
      if (track) {
        s.track = track;
        watchTrack(rec, x, track);
        if (track.readyState !== 'live') call('rtcSenderLive', rec.id, x.id, false);
      }
      return tr;
    }
    getTransceivers() { return this._p.trs.slice(); }
    getSenders() { return this._p.trs.filter(function (t) { return !t._x.stopped; }).map(function (t) { return t._x.sender; }); }
    getReceivers() { return this._p.trs.filter(function (t) { return !t._x.stopped; }).map(function (t) { return t._x.receiver; }); }

    createDataChannel(label, init) {
      var rec = this._p;
      if (rec.closed) throw dex("Failed to execute 'createDataChannel' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
      if (arguments.length === 0) throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': 1 argument required, but only 0 present.");
      label = String(label);
      init = init || {};
      if (typeof init !== 'object') throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': The provided value is not of type 'RTCDataChannelInit'.");
      var proto = init.protocol === undefined ? '' : String(init.protocol);
      if (utf8Len(label) > 65535 || utf8Len(proto) > 65535) throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': label or protocol is too long.");
      if (init.maxPacketLifeTime !== undefined && init.maxRetransmits !== undefined) throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': Both maxPacketLifeTime and maxRetransmits cannot be set.");
      var negotiated = !!init.negotiated;
      if (negotiated && (init.id === undefined || init.id === null)) throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': negotiated channels need an id.");
      if (init.id !== undefined && init.id !== null && (init.id < 0 || init.id > 65534)) throw new TypeError("Failed to execute 'createDataChannel' on 'RTCPeerConnection': id must be between 0 and 65534.");
      var o = { ordered: init.ordered === undefined ? true : !!init.ordered, protocol: proto, negotiated: negotiated };
      if (init.maxPacketLifeTime !== undefined) o.maxPacketLifeTime = init.maxPacketLifeTime & 0xffff;
      if (init.maxRetransmits !== undefined) o.maxRetransmits = init.maxRetransmits & 0xffff;
      if (negotiated) o.id = init.id;
      var info = callJSON('rtcCreateDC', rec.id, label, JSON.stringify(o));
      rec.hasSctp = true;
      return newDC(rec, info, 'connecting');
    }

    getStats(selector) {
      var rec = this._p;
      return op(rec, 'getStats', function () {
        var kind = null, type = null;
        if (selector !== undefined && selector !== null) {
          if (!isTrack(selector)) throw new TypeError("Failed to execute 'getStats' on 'RTCPeerConnection': parameter 1 is not of type 'MediaStreamTrack'.");
          var found = rec.trs.filter(function (t) { return t._x.sender._s.track === selector || t._x.receiver._r.track === selector; })[0];
          if (!found) throw dex("Failed to execute 'getStats' on 'RTCPeerConnection': Cannot get stats for a track that is not associated with this connection.", 'InvalidAccessError');
          kind = found._x.kind; type = found._x.sender._s.track === selector ? 'outbound-rtp' : 'inbound-rtp';
        }
        return buildStats(rec, type, kind);
      });
    }
    getIdentityAssertion() { return Promise.reject(dex('Identity providers are not supported.', 'NotSupportedError')); }
    setIdentityProvider() { }
  }
  tag(RTCPeerConnection, 'RTCPeerConnection');
  handlerProps(RTCPeerConnection.prototype, ['negotiationneeded', 'icecandidate', 'icecandidateerror', 'signalingstatechange', 'iceconnectionstatechange', 'icegatheringstatechange', 'connectionstatechange', 'track', 'datachannel']);
  global.RTCPeerConnection = RTCPeerConnection;

  // ---- operations ---------------------------------------------------------------------------------------
  // op runs fn at once (so operations apply in call order) and settles the promise
  // through a ticket queued behind every event the operation caused: handlers for
  // signalingstatechange and friends run before the promise resolves, as in browsers.
  function op(rec, name, fn) {
    return new Promise(function (res, rej) {
      try {
        if (rec.closed) throw dex("Failed to execute '" + name + "' on 'RTCPeerConnection': The RTCPeerConnection's signalingState is 'closed'.", 'InvalidStateError');
        var v = fn();
        var tok = ++rec.tok;
        rec.tickets.set(tok, function (err) { if (err) rej(err); else res(v); });
        call('rtcTicket', rec.id, tok);
      } catch (e) { rej(e); }
    });
  }
  function legacy(p, args, base) {
    base = base || 0;
    var ok = args[base], bad = args[base + 1];
    if (typeof ok === 'function') return p.then(function (v) { ok(v); }, function (e) { if (typeof bad === 'function') bad(e); });
    return p;
  }
  function stateOf(rec) {
    if (rec.closed) return rec.finalState || {};
    return callJSON('rtcState', rec.id) || {};
  }
  function noteSctp(rec, sdp) { if (/(^|\n)m=application /.test(String(sdp || ''))) rec.hasSctp = true; }

  function remoteStream(rec, sid) {
    var s = rec.remoteStreams.get(sid);
    if (!s) { s = new global.MediaStream(); s._s.id = sid; rec.remoteStreams.set(sid, s); }
    return s;
  }
  function processRemote(rec, type, sdp) {
    var events = [];
    parseSdp(sdp).forEach(function (m) {
      if (m.kind !== 'audio' && m.kind !== 'video') return;
      var tr = byMid(rec, m.mid);
      if (!tr) return;
      var x = tr._x;
      if (m.port === 0) { x.stopped = true; x.curDir = 'stopped'; endReceiver(x); return; }
      if (type === 'answer' || type === 'pranswer') x.curDir = REVERSE[m.dir];
      var sending = m.dir === 'sendrecv' || m.dir === 'sendonly';
      var track = x.receiver._r.track;
      if (sending && !x.recvActive) {
        x.recvActive = true;
        var sids = [];
        m.msids.forEach(function (q) { if (q.stream !== '-' && sids.indexOf(q.stream) < 0) sids.push(q.stream); });
        if (m.msids.length && m.msids[0].track) track._t.id = m.msids[0].track;
        var streams = sids.map(function (sid) {
          var st = remoteStream(rec, sid);
          if (st._s.tracks.indexOf(track) < 0) st._s.tracks.push(track);
          return st;
        });
        events.push(function () {
          rec.pc.dispatchEvent(new RTCTrackEvent('track', { receiver: x.receiver, track: track, streams: streams, transceiver: tr }));
        });
      } else if (!sending && x.recvActive) {
        x.recvActive = false;
        events.push(function () { if (!track._t.muted) { track._t.muted = true; fire(track, 'mute'); } });
      }
    });
    events.forEach(function (f) { try { f(); } catch (e) { if (global.reportError) global.reportError(e); } });
  }

  // ---- stats ------------------------------------------------------------------------------------------------
  function buildStats(rec, type, kind) {
    var all = rec.closed ? {} : callJSON('rtcStats', rec.id);
    var rep = new RTCStatsReport();
    Object.keys(all).forEach(function (id) {
      var s = all[id];
      if (type && s.type !== type && ['transport', 'codec', 'certificate', 'local-candidate', 'remote-candidate', 'candidate-pair'].indexOf(s.type) < 0 && s.type !== 'remote-' + type) return;
      if (type && (s.type === 'inbound-rtp' || s.type === 'outbound-rtp') && s.kind && kind && s.kind !== kind) return;
      rep.set(id, s);
    });
    return rep;
  }
  function statsFor(rec, type, kind) { return op(rec, 'getStats', function () { return buildStats(rec, type, kind); }); }

  // ---- events from Go --------------------------------------------------------------------------------------------
  global.__swRtcEvent = function (id, type, data) {
    var rec = pcs.get(id);
    if (!rec || rec.closed) return false;
    // Candidates and gathering progress that arrive while an operation is still
    // outstanding are held until the code awaiting it has continued: a browser
    // resolves setLocalDescription() before the first icecandidate event, and pages
    // rely on that (they apply the description to the peer before trickling).
    if ((type === 'icecandidate' || type === 'icegatheringstatechange') && rec.tickets.size > 0) {
      rec.held.push([type, data]);
      return true;
    }
    return handle(rec, type, data);
  };
  function releaseHeld(rec) {
    if (rec.tickets.size > 0 || rec.held.length === 0 || rec.releasing) return;
    rec.releasing = true;
    Promise.resolve().then(function () {
      rec.releasing = false;
      var h = rec.held; rec.held = [];
      if (!rec.closed) h.forEach(function (e) { handle(rec, e[0], e[1]); });
    });
  }
  function handle(rec, type, data) {
    var v = data ? JSON.parse(data) : null;
    try {
      var pc = rec.pc;
      switch (type) {
        case 'signalingstatechange': case 'iceconnectionstatechange': case 'connectionstatechange':
          rec.at = { i: type === 'signalingstatechange' ? 0 : type === 'iceconnectionstatechange' ? 2 : 3, v: v };
          try { fire(pc, type); } finally { rec.at = null; }
          break;
        case 'icegatheringstatechange':
          rec.at = { i: 1, v: v };
          try { fire(pc, type); if (rec.iceT) fire(rec.iceT, 'gatheringstatechange'); } finally { rec.at = null; }
          break;
        case 'ticket': {
          var f = rec.tickets.get(v);
          rec.tickets.delete(v);
          if (f) f();
          releaseHeld(rec);
          break;
        }
        case 'negotiationneeded':
          if (sts(rec)[0] === 'stable') { rec.nnQueued = false; fire(pc, 'negotiationneeded'); }
          break;
        case 'icecandidate': {
          var cand = null;
          if (v) { cand = new RTCIceCandidate(v); rec.localCands.push(cand); }
          pc.dispatchEvent(new RTCPeerConnectionIceEvent('icecandidate', { candidate: cand }));
          break;
        }
        case 'datachannel': {
          rec.hasSctp = true;
          var ch = newDC(rec, v, 'connecting');
          pc.dispatchEvent(new RTCDataChannelEvent('datachannel', { channel: ch }));
          break;
        }
        case 'trackmute': {
          var mx = rec.byId.get(v.tr);
          if (mx) {
            var mt = mx._x.receiver._r.track;
            if (mt._t.muted !== v.muted && mt._t.ready !== 'ended') { mt._t.muted = v.muted; fire(mt, v.muted ? 'mute' : 'unmute'); }
          }
          break;
        }
        case 'vsize': {
          var vx = rec.byId.get(v.tr);
          if (vx) { vx._x.vw = v.w; vx._x.vh = v.h; vx._x.els.slice().forEach(function (att) { attStart(att, v.w, v.h); }); }
          break;
        }
        case 'dtlsstate': if (rec.dtls) fire(rec.dtls, 'statechange'); if (rec.sctpT) fire(rec.sctpT, 'statechange'); break;
        case 'icetstate': if (rec.iceT) fire(rec.iceT, 'statechange'); break;
        case 'icetpair': if (rec.iceT) fire(rec.iceT, 'selectedcandidatepairchange'); break;
        default:
          if (type.indexOf('dc') === 0) dcEvent(rec, type, v);
      }
    } catch (e) { if (global.reportError) global.reportError(e); }
    return true;
  };

  // A <video>/<audio> given a MediaStream with received tracks plays them: video frames are bound to the
  // element's pixels and the element becomes ready (readyState, videoWidth/Height, resize) with the first
  // decoded frame; audio goes to the audio sink while the element holds the stream. Returns true when the
  // element's readiness is decided here (video), false to let media_prelude.js use its default for streams.
  function attStart(att, w, h) {
    var s = att.api.state, el = att.el;
    if (!att.ready) {
      att.ready = true;
      s.video = { width: w, height: h, frameRate: 30 }; s.videoW = w; s.videoH = h; s.fps = 30; s.duration = Infinity;
      att.api.ev(el, 'durationchange');
      s.ready = 1;
      att.api.ev(el, 'loadedmetadata');
      s.net = 1;
      att.api.ev(el, 'resize');
      att.api.setReady(el, 4);
    } else if (s.videoW !== w || s.videoH !== h) {
      s.videoW = w; s.videoH = h;
      att.api.ev(el, 'resize');
    }
  }
  global.__swStreamAttach = function (el, ms, api) {
    var vt = null, at = null;
    ms.getTracks().forEach(function (t) {
      if (!t._t || !t._t.rtc) return;
      if (t.kind === 'video' && !vt) vt = t; else if (t.kind === 'audio' && !at) at = t;
    });
    if (!vt && !at) return false;
    var undo = [], deferred = false;
    if (at) {
      var ra = pcs.get(at._t.rtc.pc);
      if (ra && !ra.closed) {
        call('rtcBindAudio', ra.id, at._t.rtc.tr, true);
        undo.push(function () { if (!ra.closed) call('rtcBindAudio', ra.id, at._t.rtc.tr, false); });
      }
    }
    if (vt) {
      var rec = pcs.get(vt._t.rtc.pc), tr = rec && !rec.closed ? rec.byId.get(vt._t.rtc.tr) : null;
      if (tr) {
        var x = tr._x, node = global.__swIdOf(el);
        var info = callJSON('rtcBindVideo', rec.id, x.id, node);
        var att = { el: el, api: api, ready: false };
        x.els.push(att);
        undo.push(function () { x.els = x.els.filter(function (a) { return a !== att; }); if (!rec.closed) call('rtcUnbindVideo', rec.id, x.id, node); });
        if (info && info.w > 0) attStart(att, info.w, info.h);
        deferred = true;
      }
    }
    api.state.rtcDetach = function () { api.state.rtcDetach = null; undo.forEach(function (f) { try { f(); } catch (e) { /* ignore */ } }); };
    return deferred;
  };

  function dcEvent(rec, type, v) {
    var c = rec.dcs.get(v.dc);
    if (!c) return;
    var d = c._d;
    switch (type) {
      case 'dcopen':
        if (d.state === 'connecting') { d.state = 'open'; if (v.id !== undefined && v.id !== null) d.id = v.id; fire(c, 'open'); }
        break;
      case 'dcclose':
        if (d.state !== 'closed') {
          if (d.state !== 'closing') { d.state = 'closing'; fire(c, 'closing'); }
          d.state = 'closed';
          fire(c, 'close');
        }
        break;
      case 'dcerror': {
        var err = new RTCError({ errorDetail: 'sctp-failure' }, v.message || 'data channel error');
        c.dispatchEvent(new RTCErrorEvent('error', { error: err }));
        break;
      }
      case 'dcbufferedlow': fire(c, 'bufferedamountlow'); break;
      case 'dcmessage': {
        if (d.state !== 'open') return;
        var payload;
        if (v.text !== undefined) payload = v.text;
        else {
          var u = b64ToBytes(v.bin);
          payload = d.bt === 'arraybuffer' ? u.buffer : new global.Blob([u]);
        }
        c.dispatchEvent(new global.MessageEvent('message', { data: payload, origin: '' }));
        break;
      }
    }
  }
})(globalThis);
