/*
 * Sweef Web Audio prelude. The graph, DSP and AudioParam automation live in Go
 * (internal/media/webaudio); this file is the JS-visible API: BaseAudioContext,
 * AudioContext, OfflineAudioContext, the node classes, AudioParam, AudioBuffer,
 * PeriodicWave, AudioWorklet and ScriptProcessor.
 */
(function (global) {
  'use strict';
  if (global.__swWebAudioInstalled) return;
  Object.defineProperty(global, '__swWebAudioInstalled', { value: true });
  var sw = global.__sw;
  if (!sw || typeof sw.waNew !== 'function') return;
  var _setTimeout = global.setTimeout, _clearTimeout = global.clearTimeout;
  var Event = global.Event, EventTarget = global.EventTarget;
  var _now = function () { return global.performance.now(); };

  function defMethod(o, n, f) { Object.defineProperty(o, n, { value: f, writable: true, configurable: true, enumerable: true }); }
  function defGetter(o, n, g, s) { Object.defineProperty(o, n, { get: g, set: s, configurable: true, enumerable: true }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function dex(msg, name) { return new global.DOMException(msg, name); }
  function derive(Sub, Sup) { Object.setPrototypeOf(Sub.prototype, Sup.prototype); Object.setPrototypeOf(Sub, Sup); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  function reportErr(e) { try { sw.log('error', 'Uncaught ' + (e && e.stack ? e.stack : e)); } catch (x) { /* ignore */ } }
  function handlerProps(proto, names) {
    names.forEach(function (n) {
      defGetter(proto, 'on' + n, function () { return (this.__h && this.__h[n]) || null; }, function (v) {
        if (!this.__h) Object.defineProperty(this, '__h', { value: {} });
        var self = this;
        if (!this.__h['r' + n]) { this.__h['r' + n] = true; this.addEventListener(n, function (ev) { var f = self.__h[n]; if (typeof f === 'function') f.call(self, ev); }); }
        this.__h[n] = (typeof v === 'function') ? v : null;
      });
    });
  }

  // ---- base64 <-> Float32Array --------------------------------------------------------------
  function f32ToB64(f) {
    var u = new Uint8Array(f.buffer, f.byteOffset, f.byteLength), bin = '';
    for (var i = 0; i < u.length; i += 0x6000) bin += String.fromCharCode.apply(null, u.subarray(i, i + 0x6000));
    return global.btoa(bin);
  }
  function b64ToF32(s) {
    var bin = global.atob(s), n = bin.length, u = new Uint8Array(n);
    for (var i = 0; i < n; i++) u[i] = bin.charCodeAt(i);
    return new Float32Array(u.buffer);
  }
  function u8FromB64(s) { var bin = global.atob(s), u = new Uint8Array(bin.length); for (var i = 0; i < u.length; i++) u[i] = bin.charCodeAt(i); return u; }

  // ---- AudioBuffer --------------------------------------------------------------------------------
  var BUF = new WeakMap(); var bufSeq = 0;
  function AudioBuffer(opts) {
    if (!(this instanceof AudioBuffer)) throw new TypeError("Failed to construct 'AudioBuffer': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (!opts || typeof opts !== 'object') throw new TypeError("Failed to construct 'AudioBuffer': 1 argument required, but only 0 present.");
    initBuffer(this, opts.numberOfChannels === undefined ? 1 : opts.numberOfChannels, opts.length, opts.sampleRate, 'AudioBuffer');
  }
  tag(AudioBuffer, 'AudioBuffer');
  function initBuffer(b, ch, len, rate, who) {
    ch = +ch; len = +len; rate = +rate;
    if (!(ch >= 1 && ch <= 32)) throw dex("Failed to construct '" + who + "': The number of channels provided (" + ch + ") is outside the range [1, 32].", 'NotSupportedError');
    if (!(len >= 1)) throw dex("Failed to construct '" + who + "': The number of frames provided (" + len + ") is less than or equal to the minimum bound (0).", 'NotSupportedError');
    if (!(rate >= 3000 && rate <= 768000)) throw dex("Failed to construct '" + who + "': The sample rate provided (" + rate + ") is outside the range [3000, 768000].", 'NotSupportedError');
    BUF.set(b, { id: ++bufSeq, ch: ch | 0, len: len | 0, rate: rate, data: null, pcm: 0, sig: '' });
  }
  function bufState(b) { return BUF.get(b); }
  function bufData(b) {
    var st = BUF.get(b);
    if (!st.data) {
      st.data = [];
      for (var c = 0; c < st.ch; c++) {
        if (st.pcm) { var s = sw.mediaPCMData(st.pcm, c); st.data.push(s ? b64ToF32(s) : new Float32Array(st.len)); } else st.data.push(new Float32Array(st.len));
      }
      st.touched = true;
    }
    return st.data;
  }
  defGetter(AudioBuffer.prototype, 'sampleRate', function () { return bufState(this).rate; });
  defGetter(AudioBuffer.prototype, 'length', function () { return bufState(this).len; });
  defGetter(AudioBuffer.prototype, 'duration', function () { var s = bufState(this); return s.len / s.rate; });
  defGetter(AudioBuffer.prototype, 'numberOfChannels', function () { return bufState(this).ch; });
  defMethod(AudioBuffer.prototype, 'getChannelData', function (c) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'getChannelData' on 'AudioBuffer': 1 argument required, but only 0 present.");
    c = c >>> 0; var st = bufState(this);
    if (c >= st.ch) throw dex("Failed to execute 'getChannelData' on 'AudioBuffer': channel index (" + c + ") exceeds number of channels (" + st.ch + ")", 'IndexSizeError');
    return bufData(this)[c];
  });
  defMethod(AudioBuffer.prototype, 'copyFromChannel', function (dst, c, start) {
    start = start >>> 0; c = c >>> 0; var st = bufState(this);
    if (c >= st.ch) throw dex("Failed to execute 'copyFromChannel' on 'AudioBuffer': channelNumber provided (" + c + ") is outside the range [0, " + st.ch + ")", 'IndexSizeError');
    if (start > st.len) throw dex("Failed to execute 'copyFromChannel' on 'AudioBuffer': startInChannel provided (" + start + ") is outside the range [0, " + st.len + "]", 'IndexSizeError');
    var d = bufData(this)[c]; var n = Math.min(dst.length, st.len - start);
    for (var i = 0; i < n; i++) dst[i] = d[start + i];
  });
  defMethod(AudioBuffer.prototype, 'copyToChannel', function (src, c, start) {
    start = start >>> 0; c = c >>> 0; var st = bufState(this);
    if (c >= st.ch) throw dex("Failed to execute 'copyToChannel' on 'AudioBuffer': channelNumber provided (" + c + ") is outside the range [0, " + st.ch + ")", 'IndexSizeError');
    if (start > st.len) throw dex("Failed to execute 'copyToChannel' on 'AudioBuffer': startInChannel provided (" + start + ") is outside the range [0, " + st.len + "]", 'IndexSizeError');
    var d = bufData(this)[c]; var n = Math.min(src.length, st.len - start);
    for (var i = 0; i < n; i++) d[start + i] = src[i];
  });
  function bufFromPCM(desc) {
    var b = Object.create(AudioBuffer.prototype);
    BUF.set(b, { id: ++bufSeq, ch: desc.channels, len: desc.length, rate: desc.sampleRate, data: null, pcm: desc.id, sig: '' });
    return b;
  }
  function makeBuffer(ch, len, rate) { var b = Object.create(AudioBuffer.prototype); initBuffer(b, ch, len, rate, 'BaseAudioContext.createBuffer'); return b; }
  global.AudioBuffer = AudioBuffer;
  // make the buffer's contents known to a Go context (cheap when unchanged)
  function uploadBuffer(ctx, b) {
    var st = bufState(b), key = ctx._id + ':' + st.id;
    if (st.pcm && !st.data) {
      if (st.up !== ctx._id) { sw.waSetBufferPCM(ctx._id, st.id, st.pcm); st.up = ctx._id; }
      return st.id;
    }
    var d = bufData(b), sig = st.len + ':';
    for (var c = 0; c < d.length; c++) { var a = d[c]; sig += (a[0] || 0) + ',' + (a[a.length - 1] || 0) + ',' + (a[a.length >> 1] || 0) + ',' + (a[a.length >> 2] || 0) + ',' + (a[(a.length * 3) >> 2] || 0) + ';'; }
    if (st.sigFor !== key || st.sig !== sig) {
      sw.waSetBuffer(ctx._id, st.id, st.rate, JSON.stringify(d.map(f32ToB64)));
      st.sigFor = key; st.sig = sig;
    }
    return st.id;
  }

  // ---- PeriodicWave ----------------------------------------------------------------------------------
  var WAVE = new WeakMap(), waveSeq = 0;
  function PeriodicWave(ctx, opts) {
    if (!(this instanceof PeriodicWave)) throw new TypeError("Failed to construct 'PeriodicWave': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    opts = opts || {};
    var re = opts.real ? Array.from(opts.real) : null, im = opts.imag ? Array.from(opts.imag) : null;
    if (!re && !im) { re = [0, 0]; im = [0, 1]; }
    if (!re) re = new Array(im.length).fill(0);
    if (!im) im = new Array(re.length).fill(0);
    if (re.length !== im.length) throw dex("Failed to construct 'PeriodicWave': The real and imaginary arrays must have the same length.", 'IndexSizeError');
    if (re.length < 2) throw dex("Failed to construct 'PeriodicWave': The length of the real array (" + re.length + ") must be at least 2.", 'IndexSizeError');
    WAVE.set(this, { id: ++waveSeq, re: re, im: im, norm: !!opts.disableNormalization });
  }
  tag(PeriodicWave, 'PeriodicWave'); global.PeriodicWave = PeriodicWave;

  // ---- AudioParam ---------------------------------------------------------------------------------------
  var PARAM = new WeakMap();
  function AudioParam() { illegal(); }
  tag(AudioParam, 'AudioParam');
  function makeParam(node, name, info) {
    var p = Object.create(AudioParam.prototype);
    PARAM.set(p, { node: node, name: name, def: info.def, min: info.min, max: info.max, arate: info.arate, fixedRate: !!info.fixed, events: [] });
    return p;
  }
  function ps(p) { return PARAM.get(p); }
  function pctx(p) { return ps(p).node._ctx; }
  function chkTime(who, t) {
    t = +t;
    if (!isFinite(t)) throw new TypeError("Failed to execute '" + who + "' on 'AudioParam': The provided double value is non-finite.");
    if (t < 0) throw new RangeError("Failed to execute '" + who + "' on 'AudioParam': The provided time (" + t + ") is negative.");
    return t;
  }
  function chkVal(who, v) { v = +v; if (!isFinite(v)) throw new TypeError("Failed to execute '" + who + "' on 'AudioParam': The provided float value is non-finite."); return v; }
  function overlapsCurve(st, t, who) {
    for (var i = 0; i < st.events.length; i++) {
      var e = st.events[i];
      if (e.type === 4 && t >= e.time && t < e.time + e.dur && !(t === e.time && false)) throw dex("Failed to execute '" + who + "' on 'AudioParam': " + who + " overlaps setValueCurveAtTime at time " + e.time, 'NotSupportedError');
    }
  }
  function pushEvent(p, e) {
    var st = ps(p), ctx = st.node._ctx;
    st.events.push(e); st.events.sort(function (a, b) { return a.time - b.time; });
    sw.waParamEvent(ctx._id, st.node._gid, st.name, e.type, e.time, e.value || 0, e.tc || 0, e.dur || 0, e.curve ? f32ToB64(e.curve) : null);
    ctx._touch();
  }
  function implicitStart(p) {
    var st = ps(p), ctx = st.node._ctx;
    if (!st.events.length) pushEvent(p, { type: 0, time: ctx.currentTime, value: p.value });
  }
  defGetter(AudioParam.prototype, 'value', function () {
    var st = ps(this), ctx = st.node._ctx;
    ctx._catchUp();
    return sw.waParamValue(ctx._id, st.node._gid, st.name);
  }, function (v) {
    v = chkVal('value', v); var st = ps(this), ctx = st.node._ctx;
    if (!st.events.length) { sw.waParamSet(ctx._id, st.node._gid, st.name, v); ctx._touch(); }
    else this.setValueAtTime(v, ctx.currentTime);
  });
  defGetter(AudioParam.prototype, 'defaultValue', function () { return ps(this).def; });
  defGetter(AudioParam.prototype, 'minValue', function () { return ps(this).min; });
  defGetter(AudioParam.prototype, 'maxValue', function () { return ps(this).max; });
  defGetter(AudioParam.prototype, 'automationRate', function () { return ps(this).arate ? 'a-rate' : 'k-rate'; }, function (v) {
    var st = ps(this); v = String(v);
    if (v !== 'a-rate' && v !== 'k-rate') return;
    if (st.fixedRate) throw dex("Failed to set the 'automationRate' property on 'AudioParam': automationRate cannot be changed for this param", 'InvalidStateError');
    st.arate = v === 'a-rate'; sw.waParamRate(st.node._ctx._id, st.node._gid, st.name, st.arate);
  });
  defMethod(AudioParam.prototype, 'setValueAtTime', function (v, t) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'setValueAtTime' on 'AudioParam': 2 arguments required, but only " + arguments.length + " present.");
    v = chkVal('setValueAtTime', v); t = chkTime('setValueAtTime', t); overlapsCurve(ps(this), t, 'setValueAtTime');
    pushEvent(this, { type: 0, time: t, value: v }); return this;
  });
  defMethod(AudioParam.prototype, 'linearRampToValueAtTime', function (v, t) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'linearRampToValueAtTime' on 'AudioParam': 2 arguments required, but only " + arguments.length + " present.");
    v = chkVal('linearRampToValueAtTime', v); t = chkTime('linearRampToValueAtTime', t); overlapsCurve(ps(this), t, 'linearRampToValueAtTime');
    implicitStart(this); pushEvent(this, { type: 1, time: t, value: v }); return this;
  });
  defMethod(AudioParam.prototype, 'exponentialRampToValueAtTime', function (v, t) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'exponentialRampToValueAtTime' on 'AudioParam': 2 arguments required, but only " + arguments.length + " present.");
    v = chkVal('exponentialRampToValueAtTime', v); t = chkTime('exponentialRampToValueAtTime', t);
    if (v === 0) throw new RangeError("Failed to execute 'exponentialRampToValueAtTime' on 'AudioParam': The float target value provided (0) should not be in the range (" + (-1e-300) + ", 1e-300).");
    overlapsCurve(ps(this), t, 'exponentialRampToValueAtTime');
    implicitStart(this); pushEvent(this, { type: 2, time: t, value: v }); return this;
  });
  defMethod(AudioParam.prototype, 'setTargetAtTime', function (target, t, tc) {
    if (arguments.length < 3) throw new TypeError("Failed to execute 'setTargetAtTime' on 'AudioParam': 3 arguments required, but only " + arguments.length + " present.");
    target = chkVal('setTargetAtTime', target); t = chkTime('setTargetAtTime', t); tc = chkVal('setTargetAtTime', tc);
    if (tc < 0) throw new RangeError("Failed to execute 'setTargetAtTime' on 'AudioParam': The time constant provided (" + tc + ") is negative.");
    overlapsCurve(ps(this), t, 'setTargetAtTime');
    if (tc === 0) { pushEvent(this, { type: 0, time: t, value: target }); return this; }
    pushEvent(this, { type: 3, time: t, value: target, tc: tc }); return this;
  });
  defMethod(AudioParam.prototype, 'setValueCurveAtTime', function (values, t, dur) {
    if (arguments.length < 3) throw new TypeError("Failed to execute 'setValueCurveAtTime' on 'AudioParam': 3 arguments required, but only " + arguments.length + " present.");
    t = chkTime('setValueCurveAtTime', t); dur = +dur;
    var arr = Float32Array.from(values);
    if (arr.length < 2) throw dex("Failed to execute 'setValueCurveAtTime' on 'AudioParam': The curve length provided (" + arr.length + ") is less than the minimum bound (2).", 'InvalidStateError');
    if (!(dur > 0) || !isFinite(dur)) throw new RangeError("Failed to execute 'setValueCurveAtTime' on 'AudioParam': The duration provided (" + dur + ") should be greater than zero.");
    for (var i = 0; i < arr.length; i++) if (!isFinite(arr[i])) throw new TypeError("Failed to execute 'setValueCurveAtTime' on 'AudioParam': The curve contains a non-finite value.");
    var st = ps(this);
    for (var j = 0; j < st.events.length; j++) { var e = st.events[j]; if (e.time >= t && e.time < t + dur || (e.type === 4 && t >= e.time && t < e.time + e.dur)) throw dex("Failed to execute 'setValueCurveAtTime' on 'AudioParam': setValueCurveAtTime(..., " + t + ", " + dur + ") overlaps an existing event.", 'NotSupportedError'); }
    pushEvent(this, { type: 4, time: t, dur: dur, curve: arr }); return this;
  });
  defMethod(AudioParam.prototype, 'cancelScheduledValues', function (t) {
    t = chkTime('cancelScheduledValues', t); var st = ps(this);
    st.events = st.events.filter(function (e) { return e.time < t; });
    sw.waParamCancel(st.node._ctx._id, st.node._gid, st.name, t, false); st.node._ctx._touch(); return this;
  });
  defMethod(AudioParam.prototype, 'cancelAndHoldAtTime', function (t) {
    t = chkTime('cancelAndHoldAtTime', t); var st = ps(this);
    st.events = st.events.filter(function (e) { return e.time <= t && !(e.type === 4 && e.time + e.dur > t); });
    sw.waParamCancel(st.node._ctx._id, st.node._gid, st.name, t, true);
    st.events.push({ type: 0, time: t, value: 0 }); st.node._ctx._touch(); return this;
  });
  global.AudioParam = AudioParam;
  function AudioParamMap() { illegal(); } tag(AudioParamMap, 'AudioParamMap');
  global.AudioParamMap = AudioParamMap;

  // ---- AudioNode base ---------------------------------------------------------------------------------------------
  var NODES = new WeakMap();
  function AudioNode() { illegal(); }
  derive(AudioNode, EventTarget); tag(AudioNode, 'AudioNode');
  function nodeInfo(n) { return NODES.get(n); }
  function sameCtx(a, b, who) { if (a._ctx !== b._ctx) throw dex("Failed to execute '" + who + "' on 'AudioNode': cannot connect to a destination belonging to a different audio context.", 'InvalidAccessError'); }
  defMethod(AudioNode.prototype, 'connect', function (dest, out, inp) {
    var me = this; out = out === undefined ? 0 : out >>> 0; inp = inp === undefined ? 0 : inp >>> 0;
    if (!(dest instanceof AudioNode) && !(dest instanceof AudioParam)) throw new TypeError("Failed to execute 'connect' on 'AudioNode': Overload resolution failed.");
    if (out >= me.numberOfOutputs) throw dex("Failed to execute 'connect' on 'AudioNode': output index (" + out + ") exceeds number of outputs (" + me.numberOfOutputs + ").", 'IndexSizeError');
    if (dest instanceof AudioParam) {
      var ds = ps(dest); if (ds.node._ctx !== me._ctx) throw dex("Failed to execute 'connect' on 'AudioNode': cannot connect to a destination belonging to a different audio context.", 'InvalidAccessError');
      sw.waConnect(me._ctx._id, me._gid, out, ds.node._gid, 0, ds.name); me._ctx._touch(); return undefined;
    }
    sameCtx(me, dest, 'connect');
    if (inp >= dest.numberOfInputs) throw dex("Failed to execute 'connect' on 'AudioNode': input index (" + inp + ") exceeds number of inputs (" + dest.numberOfInputs + ").", 'IndexSizeError');
    sw.waConnect(me._ctx._id, me._gid, out, dest._gid, inp, '');
    var c = nodeInfo(me).conns; if (!c.some(function (x) { return x[0] === dest && x[1] === out && x[2] === inp; })) c.push([dest, out, inp]);
    me._ctx._touch();
    return dest;
  });
  defMethod(AudioNode.prototype, 'disconnect', function (a, b, c) {
    var me = this, id = me._ctx._id;
    if (a === undefined) { sw.waDisconnect(id, me._gid, -1, -1, -1, ''); nodeInfo(me).conns.length = 0; }
    else if (typeof a === 'number') { sw.waDisconnect(id, me._gid, a >>> 0, -1, -1, ''); nodeInfo(me).conns = nodeInfo(me).conns.filter(function (x) { return x[1] !== a; }); }
    else if (a instanceof AudioParam) { var s = ps(a); sw.waDisconnect(id, me._gid, b === undefined ? -1 : b >>> 0, s.node._gid, -1, s.name); }
    else if (a instanceof AudioNode) {
      var conns = nodeInfo(me).conns;
      if (!conns.some(function (x) { return x[0] === a; })) throw dex("Failed to execute 'disconnect' on 'AudioNode': the given destination is not connected.", 'InvalidAccessError');
      sw.waDisconnect(id, me._gid, b === undefined ? -1 : b >>> 0, a._gid, c === undefined ? -1 : c >>> 0, '');
      nodeInfo(me).conns = conns.filter(function (x) { return !(x[0] === a && (b === undefined || x[1] === b) && (c === undefined || x[2] === c)); });
    }
    me._ctx._touch();
  });
  defGetter(AudioNode.prototype, 'context', function () { return this._ctx; });
  defGetter(AudioNode.prototype, 'numberOfInputs', function () { return nodeInfo(this).def.ins; });
  defGetter(AudioNode.prototype, 'numberOfOutputs', function () { return nodeInfo(this).numOut !== undefined ? nodeInfo(this).numOut : nodeInfo(this).def.outs; });
  defGetter(AudioNode.prototype, 'channelCount', function () { return nodeInfo(this).cc; }, function (v) {
    var d = nodeInfo(this); v = v >>> 0;
    if (v < 1 || v > 32) throw dex("Failed to set the 'channelCount' property on 'AudioNode': The channel count provided (" + v + ") is outside the range [1, 32].", 'NotSupportedError');
    if (d.def.maxCC && v > d.def.maxCC) throw dex("Failed to set the 'channelCount' property on 'AudioNode': The channel count provided (" + v + ") is greater than the maximum supported (" + d.def.maxCC + ").", 'NotSupportedError');
    if (d.def.fixedCC) throw dex("Failed to set the 'channelCount' property on 'AudioNode': Cannot change channel count on this node.", 'InvalidStateError');
    d.cc = v; sw.waSet(this._ctx._id, this._gid, 'channelCount', v);
  });
  defGetter(AudioNode.prototype, 'channelCountMode', function () { return nodeInfo(this).cm; }, function (v) {
    var d = nodeInfo(this); v = String(v);
    if (!/^(max|clamped-max|explicit)$/.test(v)) return;
    if (d.def.fixedCM && v !== d.cm) throw dex("Failed to set the 'channelCountMode' property on 'AudioNode': channelCountMode cannot be changed from '" + d.cm + "' on this node.", 'InvalidStateError');
    d.cm = v; sw.waSet(this._ctx._id, this._gid, 'channelCountMode', v);
  });
  defGetter(AudioNode.prototype, 'channelInterpretation', function () { return nodeInfo(this).ci; }, function (v) {
    var d = nodeInfo(this); v = String(v);
    if (v !== 'speakers' && v !== 'discrete') return;
    if (d.def.fixedCI) throw dex("Failed to set the 'channelInterpretation' property on 'AudioNode': channelInterpretation cannot be changed on this node.", 'InvalidStateError');
    d.ci = v; sw.waSet(this._ctx._id, this._gid, 'channelInterpretation', v);
  });
  global.AudioNode = AudioNode;

  // node type table: [goType, ins, outs, channelCount, mode, interp]
  var DEFS = {};
  function P(def, min, max, arate, fixed) { return { def: def, min: min, max: max, arate: arate, fixed: fixed }; }
  var BIG = 3.4028234663852886e38;
  function makeNodeClass(name, def) {
    DEFS[name] = def;
    var C = function () { throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present."); };
    def.cls = C;
    def.base = def.base || AudioNode;
    C = Object.defineProperty(function (ctx, opts) {
      if (!(this instanceof C)) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
      if (arguments.length < 1) throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
      if (!(ctx instanceof BaseAudioContext)) throw new TypeError("Failed to construct '" + name + "': parameter 1 is not of type 'BaseAudioContext'.");
      initNode(this, ctx, name, opts || {});
    }, 'name', { value: name });
    def.cls = C;
    derive(C, def.base); tag(C, name);
    global[name] = C;
    return C;
  }
  function initNode(node, ctx, name, opts) {
    var def = DEFS[name];
    EventTarget.call(node);
    if (def.pre) def.pre(opts, ctx);
    var goOpts = def.goOpts ? def.goOpts(opts, ctx) : {};
    var r = sw.waNode(ctx._id, def.go, JSON.stringify(goOpts));
    var info = { def: def, cc: def.cc, cm: def.cm, ci: def.ci, conns: [], params: {} };
    NODES.set(node, info);
    Object.defineProperty(node, '_ctx', { value: ctx });
    Object.defineProperty(node, '_gid', { value: r.id });
    if (def.numOut) info.numOut = def.numOut(opts);
    Object.keys(def.params || {}).forEach(function (pn) {
      var pd = def.params[pn];
      var gi = r.params[pn] || { def: pd.def, min: pd.min, max: pd.max, arate: pd.arate };
      var p = makeParam(node, pn, { def: pd.def, min: gi.min, max: gi.max, arate: gi.arate, fixed: pd.fixed });
      info.params[pn] = p; Object.defineProperty(node, pn, { value: p, enumerable: true, configurable: true });
    });
    // options common to all nodes
    if (opts.channelCount !== undefined) node.channelCount = opts.channelCount;
    if (opts.channelCountMode !== undefined) node.channelCountMode = opts.channelCountMode;
    if (opts.channelInterpretation !== undefined) node.channelInterpretation = opts.channelInterpretation;
    // initial values
    Object.keys(def.params || {}).forEach(function (pn) { if (opts[pn] !== undefined) info.params[pn].value = opts[pn]; });
    if (def.init) def.init(node, opts, ctx);
    ctx._register(node);
  }

  function attr(C, name, get, set) { defGetter(C.prototype, name, get, set); }

  // ---- scheduled sources ------------------------------------------------------------------------------------------------
  function AudioScheduledSourceNode() { illegal(); }
  derive(AudioScheduledSourceNode, AudioNode); tag(AudioScheduledSourceNode, 'AudioScheduledSourceNode');
  handlerProps(AudioScheduledSourceNode.prototype, ['ended']);
  global.AudioScheduledSourceNode = AudioScheduledSourceNode;
  function srcStart(node, when, offset, dur, who) {
    var d = nodeInfo(node);
    when = when === undefined ? 0 : +when;
    if (!isFinite(when)) throw new TypeError("Failed to execute '" + who + "' on 'AudioScheduledSourceNode': The provided double value is non-finite.");
    if (when < 0) throw new RangeError("Failed to execute '" + who + "' on 'AudioScheduledSourceNode': The provided time (" + when + ") is negative.");
    if (d.started) throw dex("Failed to execute '" + who + "' on 'AudioScheduledSourceNode': cannot call start more than once.", 'InvalidStateError');
    d.started = true;
    return when;
  }
  function wantsEnded(node) { var d = nodeInfo(node); return true && d.started; }
  function srcMethods(C) {
    defMethod(C.prototype, 'start', function (when) {
      when = srcStart(this, when, 0, -1, 'start');
      this._ctx._prepSource(this);
      sw.waStart(this._ctx._id, this._gid, when, 0, -1); this._ctx._sourceStarted(this); this._ctx._touch();
    });
    defMethod(C.prototype, 'stop', function (when) {
      var d = nodeInfo(this); when = when === undefined ? 0 : +when;
      if (!d.started) throw dex("Failed to execute 'stop' on 'AudioScheduledSourceNode': cannot call stop without calling start first.", 'InvalidStateError');
      if (when < 0) throw new RangeError("Failed to execute 'stop' on 'AudioScheduledSourceNode': The provided time (" + when + ") is negative.");
      sw.waStop(this._ctx._id, this._gid, when); d.stopAt = when; this._ctx._sourceStarted(this); this._ctx._touch();
    });
  }
  srcMethods(AudioScheduledSourceNode);

  // GainNode
  makeNodeClass('GainNode', { go: 'gain', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: { gain: P(1, -BIG, BIG, true) } });
  makeNodeClass('ConstantSourceNode', { go: 'constantsource', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', base: AudioScheduledSourceNode, params: { offset: P(1, -BIG, BIG, true) } });
  makeNodeClass('StereoPannerNode', { go: 'stereopanner', ins: 1, outs: 1, cc: 2, cm: 'clamped-max', ci: 'speakers', maxCC: 2, fixedCM: false, params: { pan: P(0, -1, 1, true) } });
  makeNodeClass('DelayNode', {
    go: 'delay', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: { delayTime: P(0, 0, 1, true) },
    pre: function (o) { if (o.maxDelayTime !== undefined && !(o.maxDelayTime > 0 && o.maxDelayTime < 180)) throw dex("Failed to construct 'DelayNode': The max delay time provided (" + o.maxDelayTime + ") is outside the range (0, 180).", 'NotSupportedError'); },
    goOpts: function (o) { return { maxDelayTime: o.maxDelayTime === undefined ? 1 : o.maxDelayTime }; },
    init: function (n, o) { n.__max = o.maxDelayTime === undefined ? 1 : o.maxDelayTime; var p = nodeInfo(n).params.delayTime; ps(p).max = n.__max; }
  });
  attr(DEFS.DelayNode.cls, 'maxDelayTime', function () { return this.__max; });
  makeNodeClass('BiquadFilterNode', {
    go: 'biquad', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers',
    params: { frequency: P(350, 0, 24000, true), detune: P(0, -153600, 153600, true), Q: P(1, -BIG, BIG, true), gain: P(0, -BIG, 1541, true) },
    init: function (n, o) { n.__type = 'lowpass'; if (o.type !== undefined) n.type = o.type; }
  });
  attr(DEFS.BiquadFilterNode.cls, 'type', function () { return this.__type; }, function (v) {
    v = String(v); if (!/^(lowpass|highpass|bandpass|lowshelf|highshelf|peaking|notch|allpass)$/.test(v)) return;
    this.__type = v; sw.waSet(this._ctx._id, this._gid, 'type', v); this._ctx._touch();
  });
  defMethod(DEFS.BiquadFilterNode.cls.prototype, 'getFrequencyResponse', function (f, m, p) {
    if (arguments.length < 3) throw new TypeError("Failed to execute 'getFrequencyResponse' on 'BiquadFilterNode': 3 arguments required, but only " + arguments.length + " present.");
    if (m.length !== f.length || p.length !== f.length) throw dex("Failed to execute 'getFrequencyResponse' on 'BiquadFilterNode': The magResponse and phaseResponse arrays must be the same length as the frequencyHz array.", 'InvalidAccessError');
    this._ctx._catchUp();
    var r = sw.waFreqResp(this._ctx._id, this._gid, JSON.stringify(Array.from(f)));
    for (var i = 0; i < f.length; i++) { m[i] = r.mag[i] === 'NaN' ? NaN : r.mag[i]; p[i] = r.phase[i] === 'NaN' ? NaN : r.phase[i]; }
  });
  makeNodeClass('IIRFilterNode', {
    go: 'iir', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: {},
    pre: function (o) {
      if (!o.feedforward || !o.feedback) throw new TypeError("Failed to construct 'IIRFilterNode': required member feedforward is undefined.");
      var ff = Array.from(o.feedforward), fb = Array.from(o.feedback);
      if (ff.length < 1 || ff.length > 20) throw dex("Failed to construct 'IIRFilterNode': The feedforward array length (" + ff.length + ") is outside the range [1, 20].", 'NotSupportedError');
      if (fb.length < 1 || fb.length > 20) throw dex("Failed to construct 'IIRFilterNode': The feedback array length (" + fb.length + ") is outside the range [1, 20].", 'NotSupportedError');
      if (fb[0] === 0) throw dex("Failed to construct 'IIRFilterNode': First feedback coefficient cannot be zero.", 'InvalidStateError');
      if (!ff.some(function (x) { return x !== 0; })) throw dex("Failed to construct 'IIRFilterNode': At least one feedforward coefficient must be non-zero.", 'InvalidStateError');
    },
    goOpts: function (o) { return { feedforward: Array.from(o.feedforward), feedback: Array.from(o.feedback) }; }
  });
  defMethod(DEFS.IIRFilterNode.cls.prototype, 'getFrequencyResponse', DEFS.BiquadFilterNode.cls.prototype.getFrequencyResponse);

  makeNodeClass('WaveShaperNode', { go: 'waveshaper', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers', fixedCM: false, params: {}, init: function (n, o) { n.__curve = null; n.__over = 'none'; if (o.curve) n.curve = o.curve; if (o.oversample) n.oversample = o.oversample; } });
  attr(DEFS.WaveShaperNode.cls, 'curve', function () { return this.__curve; }, function (v) {
    if (v === null || v === undefined) { this.__curve = null; sw.waSet(this._ctx._id, this._gid, 'curve', null); return; }
    var a = Float32Array.from(v);
    if (a.length < 2) throw dex("Failed to set the 'curve' property on 'WaveShaperNode': The curve length (" + a.length + ") is less than 2.", 'InvalidStateError');
    if (this.__curve) throw dex("Failed to set the 'curve' property on 'WaveShaperNode': The curve has already been set.", 'InvalidStateError');
    this.__curve = a; sw.waSet(this._ctx._id, this._gid, 'curve', f32ToB64(a)); this._ctx._touch();
  });
  attr(DEFS.WaveShaperNode.cls, 'oversample', function () { return this.__over; }, function (v) { v = String(v); if (v !== 'none' && v !== '2x' && v !== '4x') return; this.__over = v; sw.waSet(this._ctx._id, this._gid, 'oversample', v); });

  makeNodeClass('ConvolverNode', { go: 'convolver', ins: 1, outs: 1, cc: 2, cm: 'clamped-max', ci: 'speakers', maxCC: 2, params: {}, init: function (n, o) { n.__buf = null; n.__norm = true; if (o.disableNormalization) n.normalize = false; if (o.buffer) n.buffer = o.buffer; } });
  attr(DEFS.ConvolverNode.cls, 'buffer', function () { return this.__buf; }, function (b) {
    if (b === null || b === undefined) { this.__buf = null; sw.waSet(this._ctx._id, this._gid, 'buffer', null); return; }
    if (!(b instanceof AudioBuffer)) throw new TypeError("Failed to set the 'buffer' property on 'ConvolverNode': The provided value is not of type 'AudioBuffer'.");
    var st = bufState(b);
    if ([1, 2, 4].indexOf(st.ch) < 0) throw dex("Failed to set the 'buffer' property on 'ConvolverNode': The buffer must have 1, 2, or 4 channels.", 'NotSupportedError');
    if (st.rate !== this._ctx.sampleRate) throw dex("Failed to set the 'buffer' property on 'ConvolverNode': The buffer sample rate of " + st.rate + " does not match the context rate of " + this._ctx.sampleRate + " Hz.", 'NotSupportedError');
    this.__buf = b; sw.waSet(this._ctx._id, this._gid, 'buffer', uploadBuffer(this._ctx, b)); this._ctx._touch();
  });
  attr(DEFS.ConvolverNode.cls, 'normalize', function () { return this.__norm; }, function (v) { this.__norm = !!v; sw.waSet(this._ctx._id, this._gid, 'normalize', v ? 1 : 0); });

  makeNodeClass('DynamicsCompressorNode', {
    go: 'compressor', ins: 1, outs: 1, cc: 2, cm: 'clamped-max', ci: 'speakers', maxCC: 2, fixedCM: false,
    params: { threshold: P(-24, -100, 0, false, true), knee: P(30, 0, 40, false, true), ratio: P(12, 1, 20, false, true), attack: P(0.003, 0, 1, false, true), release: P(0.25, 0, 1, false, true) }
  });
  attr(DEFS.DynamicsCompressorNode.cls, 'reduction', function () { this._ctx._catchUp(); return sw.waReduction(this._ctx._id, this._gid); });

  makeNodeClass('ChannelSplitterNode', {
    go: 'channelsplitter', ins: 1, outs: 6, cc: 6, cm: 'explicit', ci: 'discrete', fixedCC: true, fixedCM: true, fixedCI: true, params: {},
    pre: function (o) { var n = o.numberOfOutputs === undefined ? 6 : o.numberOfOutputs; if (!(n >= 1 && n <= 32)) throw dex("Failed to construct 'ChannelSplitterNode': number of outputs (" + n + ") must be between 1 and 32.", 'IndexSizeError'); },
    goOpts: function (o) { return { outputs: o.numberOfOutputs === undefined ? 6 : o.numberOfOutputs }; },
    numOut: function (o) { return o.numberOfOutputs === undefined ? 6 : o.numberOfOutputs; },
    init: function (n, o) { nodeInfo(n).cc = o.numberOfOutputs === undefined ? 6 : o.numberOfOutputs; }
  });
  makeNodeClass('ChannelMergerNode', {
    go: 'channelmerger', ins: 6, outs: 1, cc: 1, cm: 'explicit', ci: 'speakers', fixedCC: true, fixedCM: true, params: {},
    pre: function (o) { var n = o.numberOfInputs === undefined ? 6 : o.numberOfInputs; if (!(n >= 1 && n <= 32)) throw dex("Failed to construct 'ChannelMergerNode': number of inputs (" + n + ") must be between 1 and 32.", 'IndexSizeError'); },
    goOpts: function (o) { return { inputs: o.numberOfInputs === undefined ? 6 : o.numberOfInputs }; },
    init: function (n, o) { nodeInfo(n).def = Object.create(DEFS.ChannelMergerNode); nodeInfo(n).def.ins = o.numberOfInputs === undefined ? 6 : o.numberOfInputs; }
  });

  makeNodeClass('AnalyserNode', {
    go: 'analyser', ins: 1, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: {},
    init: function (n, o) {
      n.__fft = 2048; n.__sm = 0.8; n.__min = -100; n.__max = -30;
      if (o.fftSize !== undefined) n.fftSize = o.fftSize; if (o.minDecibels !== undefined) n.minDecibels = o.minDecibels; if (o.maxDecibels !== undefined) n.maxDecibels = o.maxDecibels; if (o.smoothingTimeConstant !== undefined) n.smoothingTimeConstant = o.smoothingTimeConstant;
    }
  });
  var AN = DEFS.AnalyserNode.cls.prototype;
  attr(DEFS.AnalyserNode.cls, 'fftSize', function () { return this.__fft; }, function (v) {
    v = +v; if (!(v >= 32 && v <= 32768 && (v & (v - 1)) === 0)) throw dex("Failed to set the 'fftSize' property on 'AnalyserNode': The FFT size provided (" + v + ") is not a power of two in the range 32 to 32768.", 'IndexSizeError');
    this.__fft = v; sw.waSet(this._ctx._id, this._gid, 'fftSize', v);
  });
  attr(DEFS.AnalyserNode.cls, 'frequencyBinCount', function () { return this.__fft / 2; });
  attr(DEFS.AnalyserNode.cls, 'minDecibels', function () { return this.__min; }, function (v) { v = +v; if (v >= this.__max) throw dex("Failed to set the 'minDecibels' property on 'AnalyserNode': The minDecibels provided (" + v + ") is greater than or equal to the maxDecibels (" + this.__max + ").", 'IndexSizeError'); this.__min = v; sw.waSet(this._ctx._id, this._gid, 'minDecibels', v); });
  attr(DEFS.AnalyserNode.cls, 'maxDecibels', function () { return this.__max; }, function (v) { v = +v; if (v <= this.__min) throw dex("Failed to set the 'maxDecibels' property on 'AnalyserNode': The maxDecibels provided (" + v + ") is less than or equal to the minDecibels (" + this.__min + ").", 'IndexSizeError'); this.__max = v; sw.waSet(this._ctx._id, this._gid, 'maxDecibels', v); });
  attr(DEFS.AnalyserNode.cls, 'smoothingTimeConstant', function () { return this.__sm; }, function (v) { v = +v; if (!(v >= 0 && v <= 1)) throw dex("Failed to set the 'smoothingTimeConstant' property on 'AnalyserNode': The smoothing value provided (" + v + ") is outside the range [0, 1].", 'IndexSizeError'); this.__sm = v; sw.waSet(this._ctx._id, this._gid, 'smoothing', v); });
  function anRead(node, kind, dst, who) {
    node._ctx._catchUp();
    var s = sw.waAnalyser(node._ctx._id, node._gid, kind);
    if (kind.indexOf('float') === 0) { var f = b64ToF32(s); var n = Math.min(dst.length, f.length); for (var i = 0; i < n; i++) dst[i] = f[i]; }
    else { var u = u8FromB64(s); var m = Math.min(dst.length, u.length); for (var j = 0; j < m; j++) dst[j] = u[j]; }
  }
  defMethod(AN, 'getFloatFrequencyData', function (a) { anRead(this, 'float-freq', a); });
  defMethod(AN, 'getByteFrequencyData', function (a) { anRead(this, 'byte-freq', a); });
  defMethod(AN, 'getFloatTimeDomainData', function (a) { anRead(this, 'float-time', a); });
  defMethod(AN, 'getByteTimeDomainData', function (a) { anRead(this, 'byte-time', a); });

  // OscillatorNode
  makeNodeClass('OscillatorNode', {
    go: 'oscillator', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', base: AudioScheduledSourceNode,
    params: { frequency: P(440, -24000, 24000, true), detune: P(0, -153600, 153600, true) },
    init: function (n, o) { n.__type = 'sine'; if (o.periodicWave) n.setPeriodicWave(o.periodicWave); else if (o.type !== undefined) n.type = o.type; }
  });
  attr(DEFS.OscillatorNode.cls, 'type', function () { return this.__type; }, function (v) {
    v = String(v);
    if (v === 'custom') throw dex("Failed to set the 'type' property on 'OscillatorNode': 'type' cannot be set directly to 'custom'.  The PeriodicWave must be set explicitly.", 'InvalidStateError');
    if (!/^(sine|square|sawtooth|triangle)$/.test(v)) return;
    this.__type = v; sw.waSet(this._ctx._id, this._gid, 'type', v); this._ctx._touch();
  });
  defMethod(DEFS.OscillatorNode.cls.prototype, 'setPeriodicWave', function (w) {
    if (!(w instanceof PeriodicWave)) throw new TypeError("Failed to execute 'setPeriodicWave' on 'OscillatorNode': parameter 1 is not of type 'PeriodicWave'.");
    var s = WAVE.get(w); sw.waWave(this._ctx._id, s.id, JSON.stringify(s.re), JSON.stringify(s.im), s.norm);
    this.__type = 'custom'; sw.waSet(this._ctx._id, this._gid, 'type', 'custom'); sw.waSet(this._ctx._id, this._gid, 'wave', s.id); this._ctx._touch();
  });

  // AudioBufferSourceNode
  makeNodeClass('AudioBufferSourceNode', {
    go: 'buffersource', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', base: AudioScheduledSourceNode,
    params: { playbackRate: P(1, -BIG, BIG, false, false), detune: P(0, -BIG, BIG, false, false) },
    init: function (n, o) { n.__buf = null; n.__loop = false; n.__ls = 0; n.__le = 0; if (o.buffer) n.buffer = o.buffer; if (o.loop !== undefined) n.loop = o.loop; if (o.loopStart !== undefined) n.loopStart = o.loopStart; if (o.loopEnd !== undefined) n.loopEnd = o.loopEnd; }
  });
  var ABS = DEFS.AudioBufferSourceNode.cls.prototype;
  attr(DEFS.AudioBufferSourceNode.cls, 'buffer', function () { return this.__buf; }, function (b) {
    if (b === null || b === undefined) { this.__buf = null; return; }
    if (!(b instanceof AudioBuffer)) throw new TypeError("Failed to set the 'buffer' property on 'AudioBufferSourceNode': The provided value is not of type 'AudioBuffer'.");
    if (this.__buf && nodeInfo(this).acquired) throw dex("Failed to set the 'buffer' property on 'AudioBufferSourceNode': Cannot set buffer to non-null after it has been already been set to a non-null buffer", 'InvalidStateError');
    this.__buf = b; nodeInfo(this).acquired = true; sw.waSet(this._ctx._id, this._gid, 'buffer', uploadBuffer(this._ctx, b)); this._ctx._touch();
  });
  attr(DEFS.AudioBufferSourceNode.cls, 'loop', function () { return this.__loop; }, function (v) { this.__loop = !!v; sw.waSet(this._ctx._id, this._gid, 'loop', v ? 1 : 0); });
  attr(DEFS.AudioBufferSourceNode.cls, 'loopStart', function () { return this.__ls; }, function (v) { this.__ls = +v; sw.waSet(this._ctx._id, this._gid, 'loopStart', +v); });
  attr(DEFS.AudioBufferSourceNode.cls, 'loopEnd', function () { return this.__le; }, function (v) { this.__le = +v; sw.waSet(this._ctx._id, this._gid, 'loopEnd', +v); });
  defMethod(ABS, 'start', function (when, offset, duration) {
    when = srcStart(this, when, offset, duration, 'start');
    offset = offset === undefined ? 0 : +offset;
    if (offset < 0) throw new RangeError("Failed to execute 'start' on 'AudioBufferSourceNode': The offset provided (" + offset + ") is negative.");
    if (duration !== undefined && +duration < 0) throw new RangeError("Failed to execute 'start' on 'AudioBufferSourceNode': The duration provided (" + duration + ") is negative.");
    if (this.__buf) sw.waSet(this._ctx._id, this._gid, 'buffer', uploadBuffer(this._ctx, this.__buf));
    sw.waStart(this._ctx._id, this._gid, when, offset, duration === undefined ? -1 : +duration);
    this._ctx._sourceStarted(this); this._ctx._touch();
  });

  // PannerNode
  makeNodeClass('PannerNode', {
    go: 'panner', ins: 1, outs: 1, cc: 2, cm: 'clamped-max', ci: 'speakers', maxCC: 2,
    params: { positionX: P(0, -BIG, BIG, true), positionY: P(0, -BIG, BIG, true), positionZ: P(0, -BIG, BIG, true), orientationX: P(1, -BIG, BIG, true), orientationY: P(0, -BIG, BIG, true), orientationZ: P(0, -BIG, BIG, true) },
    init: function (n, o) {
      n.__pm = 'equalpower'; n.__dm = 'inverse'; n.__ref = 1; n.__max = 10000; n.__ro = 1; n.__ci = 360; n.__co = 360; n.__cog = 0;
      ['panningModel', 'distanceModel', 'refDistance', 'maxDistance', 'rolloffFactor', 'coneInnerAngle', 'coneOuterAngle', 'coneOuterGain'].forEach(function (k) { if (o[k] !== undefined) n[k] = o[k]; });
    }
  });
  function pannerAttr(name, key, check, goKey) {
    attr(DEFS.PannerNode.cls, name, function () { return this[key]; }, function (v) {
      if (check) v = check.call(this, v);
      if (v === undefined) return;
      this[key] = v; sw.waSet(this._ctx._id, this._gid, goKey || name, v);
    });
  }
  pannerAttr('panningModel', '__pm', function (v) { v = String(v); return v === 'equalpower' || v === 'HRTF' ? v : undefined; });
  pannerAttr('distanceModel', '__dm', function (v) { v = String(v); return /^(linear|inverse|exponential)$/.test(v) ? v : undefined; });
  pannerAttr('refDistance', '__ref', function (v) { v = +v; if (v < 0) throw new RangeError("Failed to set the 'refDistance' property on 'PannerNode': The refDistance provided (" + v + ") is negative."); return v; });
  pannerAttr('maxDistance', '__max', function (v) { v = +v; if (v <= 0) throw new RangeError("Failed to set the 'maxDistance' property on 'PannerNode': The maxDistance provided (" + v + ") is not positive."); return v; });
  pannerAttr('rolloffFactor', '__ro', function (v) { v = +v; if (v < 0) throw new RangeError("Failed to set the 'rolloffFactor' property on 'PannerNode': The rolloffFactor provided (" + v + ") is negative."); return v; });
  pannerAttr('coneInnerAngle', '__ci', function (v) { return +v; });
  pannerAttr('coneOuterAngle', '__co', function (v) { return +v; });
  pannerAttr('coneOuterGain', '__cog', function (v) { v = +v; if (v < 0 || v > 1) throw dex("Failed to set the 'coneOuterGain' property on 'PannerNode': The coneOuterGain provided (" + v + ") is outside the range [0, 1].", 'InvalidStateError'); return v; });
  defMethod(DEFS.PannerNode.cls.prototype, 'setPosition', function (x, y, z) { this.positionX.value = x; this.positionY.value = y; this.positionZ.value = z; });
  defMethod(DEFS.PannerNode.cls.prototype, 'setOrientation', function (x, y, z) { this.orientationX.value = x; this.orientationY.value = y; this.orientationZ.value = z; });
  var _panWarn = DEFS.PannerNode.cls;

  // MediaStream nodes
  makeNodeClass('MediaStreamAudioSourceNode', {
    go: 'mediastreamsource', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: {},
    pre: function (o) { if (!o.mediaStream || !(o.mediaStream instanceof global.MediaStream)) throw new TypeError("Failed to construct 'MediaStreamAudioSourceNode': required member mediaStream is undefined."); if (!o.mediaStream.getAudioTracks().length) throw dex("Failed to construct 'MediaStreamAudioSourceNode': MediaStream has no audio track", 'InvalidStateError'); },
    init: function (n, o) { n.__ms = o.mediaStream; }
  });
  attr(DEFS.MediaStreamAudioSourceNode.cls, 'mediaStream', function () { return this.__ms; });
  makeNodeClass('MediaStreamAudioDestinationNode', {
    go: 'mediastreamdest', ins: 1, outs: 0, cc: 2, cm: 'explicit', ci: 'speakers', params: {}, numOut: function () { return 0; },
    init: function (n) { var ms = new global.MediaStream(); ms.addTrack(global.__swMakeTrack('audio', 'MediaStreamAudioDestinationNode')); n.__stream = ms; }
  });
  attr(DEFS.MediaStreamAudioDestinationNode.cls, 'stream', function () { return this.__stream; });
  makeNodeClass('MediaStreamTrackAudioSourceNode', { go: 'mediastreamsource', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: {} });

  // MediaElementAudioSourceNode
  makeNodeClass('MediaElementAudioSourceNode', {
    go: 'mediaelement', ins: 0, outs: 1, cc: 2, cm: 'max', ci: 'speakers', params: {},
    pre: function (o) { if (!o.mediaElement || !(o.mediaElement instanceof global.HTMLMediaElement)) throw new TypeError("Failed to construct 'MediaElementAudioSourceNode': required member mediaElement is undefined."); },
    init: function (n, o, ctx) { n.__el = o.mediaElement; ctx._bindElement(n); }
  });
  attr(DEFS.MediaElementAudioSourceNode.cls, 'mediaElement', function () { return this.__el; });

  // AudioDestinationNode
  function AudioDestinationNode() { illegal(); }
  derive(AudioDestinationNode, AudioNode); tag(AudioDestinationNode, 'AudioDestinationNode');
  global.AudioDestinationNode = AudioDestinationNode;
  attr(AudioDestinationNode, 'maxChannelCount', function () { return this._ctx instanceof OfflineAudioContext ? this._ctx.__ch : 2; });
  DEFS.AudioDestinationNode = { go: 'destination', ins: 1, outs: 0, cc: 2, cm: 'explicit', ci: 'speakers', fixedCM: true, params: {}, numOut: function () { return 0; } };

  // AudioListener
  function AudioListener() { illegal(); } tag(AudioListener, 'AudioListener');
  function makeListener(ctx) {
    var l = Object.create(AudioListener.prototype);
    var r = sw.waListener(ctx._id);
    var node = { _ctx: ctx, _gid: r.id };
    NODES.set(node, { params: {} });
    ['positionX', 'positionY', 'positionZ', 'forwardX', 'forwardY', 'forwardZ', 'upX', 'upY', 'upZ'].forEach(function (pn) {
      var gi = r.params[pn]; var p = makeParam(node, pn, { def: gi.def, min: gi.min, max: gi.max, arate: gi.arate, fixed: false });
      Object.defineProperty(l, pn, { value: p, enumerable: true, configurable: true });
    });
    return l;
  }
  defMethod(AudioListener.prototype, 'setPosition', function (x, y, z) { this.positionX.value = x; this.positionY.value = y; this.positionZ.value = z; });
  defMethod(AudioListener.prototype, 'setOrientation', function (x, y, z, ux, uy, uz) { this.forwardX.value = x; this.forwardY.value = y; this.forwardZ.value = z; this.upX.value = ux; this.upY.value = uy; this.upZ.value = uz; });
  global.AudioListener = AudioListener;

  // ======================= contexts ==========================================================================================
  var CTX_SEQ = 0;
  function BaseAudioContext() { illegal(); }
  derive(BaseAudioContext, EventTarget); tag(BaseAudioContext, 'BaseAudioContext');
  handlerProps(BaseAudioContext.prototype, ['statechange']);
  global.BaseAudioContext = BaseAudioContext;
  var Q = 128;

  function initContext(ctx, rate, offline, ch, len) {
    EventTarget.call(ctx);
    var r = sw.waNew(rate, offline, ch, len || 0);
    Object.defineProperty(ctx, '_id', { value: r.id });
    var dest = Object.create(AudioDestinationNode.prototype);
    EventTarget.call(dest);
    Object.defineProperty(dest, '_ctx', { value: ctx }); Object.defineProperty(dest, '_gid', { value: r.dest });
    NODES.set(dest, { def: DEFS.AudioDestinationNode, cc: ch, cm: 'explicit', ci: 'speakers', conns: [], params: {} });
    Object.defineProperty(ctx, 'destination', { value: dest, enumerable: true, configurable: true });
    ctx.__rate = rate; ctx.__ch = ch; ctx.__state = 'suspended'; ctx.__srcs = {}; ctx.__els = []; ctx.__ext = {}; ctx.__live = 0; ctx.__goFrame = 0; ctx.__wall = 0; ctx.__baseFrames = 0; ctx.__timer = null; ctx.__closed = false;
    ctx.__listener = null;
  }
  defGetter(BaseAudioContext.prototype, 'sampleRate', function () { return this.__rate; });
  defGetter(BaseAudioContext.prototype, 'state', function () { return this.__state; });
  defGetter(BaseAudioContext.prototype, 'listener', function () { return this.__listener || (this.__listener = makeListener(this)); });
  defGetter(BaseAudioContext.prototype, 'currentTime', function () { this._catchUp(); return this.__closed ? this.__lastTime || 0 : sw.waTime(this._id); });
  defGetter(BaseAudioContext.prototype, 'audioWorklet', function () { return this.__worklet || (this.__worklet = makeWorklet(this)); });
  BaseAudioContext.prototype._register = function (node) { };
  BaseAudioContext.prototype._touch = function () { };
  BaseAudioContext.prototype._prepSource = function (node) { };
  BaseAudioContext.prototype._sourceStarted = function (node) {
    var ctx = this, d = nodeInfo(node);
    if (!ctx.__srcs[node._gid]) ctx.__srcs[node._gid] = node;
    ctx._ensureTimer();
  };
  BaseAudioContext.prototype._needTimer = function () {
    var k, any = false;
    for (k in this.__srcs) {
      var n = this.__srcs[k];
      if (n.__endedL || (n.__h && n.__h.ended) || n.onended || nodeInfo(n).stopAt !== undefined) { any = true; break; }
    }
    return any || Object.keys(this.__ext).length > 0 || (this.__suspends && this.__suspends.length > 0);
  };
  BaseAudioContext.prototype._ensureTimer = function () {
    var ctx = this;
    if (ctx.__timer || ctx.__state !== 'running' || ctx.__closed || ctx.__offline) return;
    if (!ctx._needTimer()) return;
    ctx.__timer = _setTimeout(function () { ctx.__timer = null; ctx._catchUp(); ctx._ensureTimer(); }, 20);
  };
  BaseAudioContext.prototype._syncElements = function () {
    var ctx = this;
    ctx.__els.forEach(function (n) {
      var info = global.__swMediaEl && global.__swMediaEl(n.__el);
      if (!info) return;
      if (info.h && n.__hdec !== info.h) {
        n.__hdec = info.h;
        try {
          var pcm = sw.mediaDecodeAudio(info.h, ctx.__rate);
          sw.waSetBufferPCM(ctx._id, 1000000 + n._gid, pcm.id);
          sw.waSet(ctx._id, n._gid, 'clip', 1000000 + n._gid);
        } catch (e) { /* undecodable audio stays silent */ }
      }
      sw.waSet(ctx._id, n._gid, 'pos', info.pos);
      sw.waSet(ctx._id, n._gid, 'playing', (!info.paused && info.ready >= 3) ? 1 : 0);
      sw.waSet(ctx._id, n._gid, 'rate', info.rate);
      sw.waSet(ctx._id, n._gid, 'volume', 1);
    });
  };
  BaseAudioContext.prototype._bindElement = function (node) {
    var el = node.__el;
    if (el.__swSourceNode) throw dex("Failed to construct 'MediaElementAudioSourceNode': HTMLMediaElement already connected previously to a different MediaElementSourceNode.", 'InvalidStateError');
    el.__swSourceNode = node; this.__els.push(node);
  };
  function dispatchEnded(ctx, ids) {
    ids.forEach(function (gid) {
      var n = ctx.__srcs[gid]; if (!n) return;
      delete ctx.__srcs[gid];
      var ev = new Event('ended');
      _setTimeout(function () { try { n.dispatchEvent(ev); } catch (e) { reportErr(e); } }, 0);
    });
  }
  // advance the Go graph by `frames` (multiple of 128)
  BaseAudioContext.prototype._advance = function (frames) {
    var ctx = this;
    if (ctx.__closed || frames <= 0) return;
    ctx._syncElements();
    if (!sw.waHasExternal(ctx._id)) {
      var r = sw.waRender(ctx._id, frames);
      ctx.__goFrame = r.frame; dispatchEnded(ctx, r.ended);
      return;
    }
    var target = ctx.__goFrame + frames;
    for (;;) {
      var s = sw.waStep(ctx._id);
      if (s.done) {
        ctx.__goFrame = s.frame; dispatchEnded(ctx, s.ended);
        if (ctx.__goFrame >= target) break;
        continue;
      }
      var h = ctx.__ext[s.node], out = null;
      if (h) { try { out = h(s, ctx.__goFrame); } catch (e) { reportErr(e); } }
      sw.waProvide(ctx._id, s.node, JSON.stringify(out || [f32ToB64(new Float32Array(Q))]));
    }
  };
  BaseAudioContext.prototype._catchUp = function () {
    var ctx = this;
    if (ctx.__closed || ctx.__offline || ctx.__state !== 'running') return;
    var target = ctx.__baseFrames + Math.floor((_now() - ctx.__wall) * ctx.__rate / 1000);
    target = Math.floor(target / Q) * Q;
    var delta = target - ctx.__goFrame;
    if (delta < Q) return;
    if (delta > ctx.__rate * 120) { ctx.__baseFrames -= delta - ctx.__rate * 120; delta = ctx.__rate * 120; delta -= delta % Q; }
    ctx._advance(delta);
  };
  function setState(ctx, s) {
    if (ctx.__state === s) return;
    ctx.__state = s;
    _setTimeout(function () { try { ctx.dispatchEvent(new Event('statechange')); } catch (e) { reportErr(e); } }, 0);
  }
  BaseAudioContext.prototype._run = function () {
    var ctx = this;
    ctx.__wall = _now(); ctx.__baseFrames = ctx.__goFrame;
    setState(ctx, 'running'); ctx._ensureTimer();
  };

  // factory methods
  var FACT = { createGain: 'GainNode', createOscillator: 'OscillatorNode', createBufferSource: 'AudioBufferSourceNode', createBiquadFilter: 'BiquadFilterNode', createConvolver: 'ConvolverNode', createDynamicsCompressor: 'DynamicsCompressorNode', createWaveShaper: 'WaveShaperNode', createStereoPanner: 'StereoPannerNode', createPanner: 'PannerNode', createAnalyser: 'AnalyserNode', createConstantSource: 'ConstantSourceNode' };
  Object.keys(FACT).forEach(function (m) { defMethod(BaseAudioContext.prototype, m, function () { return new global[FACT[m]](this); }); });
  defMethod(BaseAudioContext.prototype, 'createDelay', function (max) { return new global.DelayNode(this, max === undefined ? {} : { maxDelayTime: max }); });
  defMethod(BaseAudioContext.prototype, 'createChannelSplitter', function (n) { return new global.ChannelSplitterNode(this, n === undefined ? {} : { numberOfOutputs: n }); });
  defMethod(BaseAudioContext.prototype, 'createChannelMerger', function (n) { return new global.ChannelMergerNode(this, n === undefined ? {} : { numberOfInputs: n }); });
  defMethod(BaseAudioContext.prototype, 'createIIRFilter', function (ff, fb) { return new global.IIRFilterNode(this, { feedforward: ff, feedback: fb }); });
  defMethod(BaseAudioContext.prototype, 'createBuffer', function (ch, len, rate) {
    if (arguments.length < 3) throw new TypeError("Failed to execute 'createBuffer' on 'BaseAudioContext': 3 arguments required, but only " + arguments.length + " present.");
    return makeBuffer(ch, len, rate);
  });
  defMethod(BaseAudioContext.prototype, 'createPeriodicWave', function (re, im, c) {
    if (arguments.length < 2) throw new TypeError("Failed to execute 'createPeriodicWave' on 'BaseAudioContext': 2 arguments required, but only " + arguments.length + " present.");
    return new PeriodicWave(this, { real: re, imag: im, disableNormalization: c && c.disableNormalization });
  });
  defMethod(BaseAudioContext.prototype, 'createMediaElementSource', function (el) { return new global.MediaElementAudioSourceNode(this, { mediaElement: el }); });
  defMethod(BaseAudioContext.prototype, 'createMediaStreamSource', function (ms) { return new global.MediaStreamAudioSourceNode(this, { mediaStream: ms }); });
  defMethod(BaseAudioContext.prototype, 'createMediaStreamDestination', function () { return new global.MediaStreamAudioDestinationNode(this); });
  defMethod(BaseAudioContext.prototype, 'createScriptProcessor', function (bs, inCh, outCh) {
    bs = bs === undefined ? 0 : bs; inCh = inCh === undefined ? 2 : inCh; outCh = outCh === undefined ? 2 : outCh;
    if ([0, 256, 512, 1024, 2048, 4096, 8192, 16384].indexOf(bs) < 0) throw dex("Failed to execute 'createScriptProcessor' on 'BaseAudioContext': buffer size (" + bs + ") must be 0 or a power of two between 256 and 16384.", 'IndexSizeError');
    if (inCh === 0 && outCh === 0) throw dex("Failed to execute 'createScriptProcessor' on 'BaseAudioContext': number of input channels and output channels cannot both be zero.", 'IndexSizeError');
    if (inCh > 32 || outCh > 32) throw dex("Failed to execute 'createScriptProcessor' on 'BaseAudioContext': number of channels exceeds 32.", 'IndexSizeError');
    return makeScriptProcessor(this, bs || 4096, inCh, outCh);
  });
  defMethod(BaseAudioContext.prototype, 'decodeAudioData', function (data, ok, err) {
    var ctx = this;
    if (!(data instanceof ArrayBuffer) && !ArrayBuffer.isView(data)) return Promise.reject(new TypeError("Failed to execute 'decodeAudioData' on 'BaseAudioContext': parameter 1 is not of type 'ArrayBuffer'."));
    var u = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
    if (ctx.__closed) { var ce = dex("Failed to execute 'decodeAudioData' on 'BaseAudioContext': Cannot decode audio data: The AudioContext has been closed.", 'InvalidStateError'); if (err) _setTimeout(function () { err(ce); }, 0); return Promise.reject(ce); }
    var b64 = null, bytes = u.slice();
    return new Promise(function (res, rej) {
      _setTimeout(function () {
        var buf;
        try {
          var bin = '';
          for (var i = 0; i < bytes.length; i += 0x6000) bin += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x6000));
          b64 = global.btoa(bin);
          var desc = sw.mediaDecodeBytes(b64, ctx.__rate);
          buf = bufFromPCM(desc);
        } catch (e) {
          var ex = dex(bytes.length === 0 ? 'Unable to decode audio data' : 'Unable to decode audio data', 'EncodingError');
          if (err) try { err(ex); } catch (e2) { reportErr(e2); }
          rej(ex); return;
        }
        if (ok) try { ok(buf); } catch (e3) { reportErr(e3); }
        res(buf);
      }, 0);
    });
  });
  function noPrefixClose(ctx) { if (ctx.__closed) throw dex('Cannot operate on a closed AudioContext.', 'InvalidStateError'); }

  // ---- AudioContext ---------------------------------------------------------------------------------------------------------------
  function validRate(r, who) { if (!(r >= 3000 && r <= 768000)) throw dex("Failed to construct '" + who + "': The sample rate provided (" + r + ") is outside the range [3000, 768000].", 'NotSupportedError'); return r; }
  function AudioContext(opts) {
    if (!(this instanceof AudioContext)) throw new TypeError("Failed to construct 'AudioContext': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    opts = opts || {};
    var rate = opts.sampleRate !== undefined ? validRate(+opts.sampleRate, 'AudioContext') : 44100;
    if (opts.latencyHint !== undefined && typeof opts.latencyHint === 'string' && !/^(interactive|balanced|playback)$/.test(opts.latencyHint)) throw new TypeError("Failed to construct 'AudioContext': The provided value '" + opts.latencyHint + "' is not a valid enum value of type AudioContextLatencyCategory.");
    initContext(this, rate, false, 2, 0);
    this.__offline = false; this.__latency = opts.latencyHint;
    var self = this;
    if (global.__swUserActivation.has()) self._run();
  }
  derive(AudioContext, BaseAudioContext); tag(AudioContext, 'AudioContext');
  defGetter(AudioContext.prototype, 'baseLatency', function () { return typeof this.__latency === 'number' ? this.__latency : (this.__latency === 'playback' ? 0.02 : 0.01); });
  defGetter(AudioContext.prototype, 'outputLatency', function () { return 0; });
  defGetter(AudioContext.prototype, 'sinkId', function () { return ''; });
  defMethod(AudioContext.prototype, 'setSinkId', function (id) { id = typeof id === 'string' ? id : (id && id.type) || ''; if (id === '' || id === 'default') return Promise.resolve(); return Promise.reject(dex('No audio device found with the requested ID.', 'NotFoundError')); });
  defMethod(AudioContext.prototype, 'getOutputTimestamp', function () { this._catchUp(); return { contextTime: sw.waTime(this._id), performanceTime: _now() }; });
  defMethod(AudioContext.prototype, 'resume', function () {
    var ctx = this;
    if (ctx.__closed) return Promise.reject(dex('Cannot resume a closed AudioContext', 'InvalidStateError'));
    if (ctx.__state === 'running') return Promise.resolve();
    return new Promise(function (res, rej) {
      global.__swUserActivation.onActivate(function () {
        if (ctx.__closed) { rej(dex('Cannot resume a closed AudioContext', 'InvalidStateError')); return; }
        _setTimeout(function () { if (ctx.__state !== 'running' && !ctx.__closed) ctx._run(); res(); }, 0);
      });
    });
  });
  defMethod(AudioContext.prototype, 'suspend', function () {
    var ctx = this;
    if (ctx.__closed) return Promise.reject(dex('Cannot suspend a closed AudioContext', 'InvalidStateError'));
    ctx._catchUp();
    if (ctx.__timer) { _clearTimeout(ctx.__timer); ctx.__timer = null; }
    setState(ctx, 'suspended');
    return new Promise(function (res) { _setTimeout(res, 0); });
  });
  defMethod(AudioContext.prototype, 'close', function () {
    var ctx = this;
    if (ctx.__closed) return Promise.reject(dex('Cannot close a closed AudioContext', 'InvalidStateError'));
    ctx._catchUp(); ctx.__lastTime = sw.waTime(ctx._id);
    ctx.__closed = true; if (ctx.__timer) { _clearTimeout(ctx.__timer); ctx.__timer = null; }
    try { sw.waClose(ctx._id); } catch (e) { /* ignore */ }
    setState(ctx, 'closed');
    return new Promise(function (res) { _setTimeout(res, 0); });
  });
  defMethod(AudioContext.prototype, 'createMediaStreamTrackSource', function (t) { return new global.MediaStreamTrackAudioSourceNode(this); });
  global.AudioContext = AudioContext; global.webkitAudioContext = AudioContext;

  // ---- OfflineAudioContext ---------------------------------------------------------------------------------------------------------------
  function OfflineAudioContext(a, b, c) {
    if (!(this instanceof OfflineAudioContext)) throw new TypeError("Failed to construct 'OfflineAudioContext': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    var ch, len, rate;
    if (a && typeof a === 'object') { ch = a.numberOfChannels === undefined ? 1 : a.numberOfChannels; len = a.length; rate = a.sampleRate; }
    else { if (arguments.length < 3) throw new TypeError("Failed to construct 'OfflineAudioContext': 3 arguments required, but only " + arguments.length + " present."); ch = a; len = b; rate = c; }
    ch = +ch; len = +len; rate = +rate;
    if (!(ch >= 1 && ch <= 32)) throw dex("Failed to construct 'OfflineAudioContext': The number of channels provided (" + ch + ") is outside the range [1, 32].", 'NotSupportedError');
    if (!(len >= 1)) throw dex("Failed to construct 'OfflineAudioContext': The number of frames provided (" + len + ") is less than or equal to the minimum bound (0).", 'NotSupportedError');
    validRate(rate, 'OfflineAudioContext');
    initContext(this, rate, true, ch | 0, len | 0);
    this.__offline = true; this.__len = len | 0; this.__rendering = false; this.__suspends = []; this.__state = 'suspended';
  }
  derive(OfflineAudioContext, BaseAudioContext); tag(OfflineAudioContext, 'OfflineAudioContext');
  handlerProps(OfflineAudioContext.prototype, ['complete']);
  defGetter(OfflineAudioContext.prototype, 'length', function () { return this.__len; });
  defMethod(OfflineAudioContext.prototype, 'startRendering', function () {
    var ctx = this;
    if (ctx.__rendering) return Promise.reject(dex("Failed to execute 'startRendering' on 'OfflineAudioContext': cannot call startRendering more than once", 'InvalidStateError'));
    ctx.__rendering = true;
    return new Promise(function (res, rej) {
      _setTimeout(function () {
        setState(ctx, 'running');
        var pos = 0, total = Math.ceil(ctx.__len / Q) * Q;
        (function step() {
          var next = total, sus = null;
          ctx.__suspends.sort(function (a, b) { return a.frame - b.frame; });
          if (ctx.__suspends.length && ctx.__suspends[0].frame < total) { sus = ctx.__suspends.shift(); next = Math.max(sus.frame, pos); }
          try { if (next > pos) { ctx._advance(next - pos); pos = next; } } catch (e) { rej(e); return; }
          if (sus) {
            setState(ctx, 'suspended'); sus.res();
            ctx.__resume = function () { ctx.__resume = null; setState(ctx, 'running'); _setTimeout(step, 0); };
            return;
          }
          var d = sw.waOutput(ctx._id), buf = bufFromPCM({ id: d.id, channels: d.channels, length: ctx.__len, sampleRate: ctx.__rate });
          // output was rendered in whole quanta; trim to the requested length
          if (d.length !== ctx.__len) { var st = bufState(buf); st.len = ctx.__len; var cd = bufData(buf); for (var c = 0; c < cd.length; c++) cd[c] = cd[c].subarray(0, ctx.__len); }
          setState(ctx, 'closed');
          var ev = new Event('complete'); Object.setPrototypeOf(ev, OfflineAudioCompletionEvent.prototype); ev.renderedBuffer = buf;
          try { ctx.dispatchEvent(ev); } catch (e2) { reportErr(e2); }
          res(buf);
        })();
      }, 0);
    });
  });
  defMethod(OfflineAudioContext.prototype, 'suspend', function (t) {
    var ctx = this;
    if (arguments.length < 1) return Promise.reject(new TypeError("Failed to execute 'suspend' on 'OfflineAudioContext': 1 argument required, but only 0 present."));
    t = +t;
    if (t < 0) return Promise.reject(new RangeError("Failed to execute 'suspend' on 'OfflineAudioContext': negative suspend time (" + t + ") is not allowed"));
    var frame = Math.ceil(t * ctx.__rate / Q) * Q;
    if (frame >= Math.ceil(ctx.__len / Q) * Q) return Promise.reject(new RangeError("Failed to execute 'suspend' on 'OfflineAudioContext': suspend time (" + t + ") is beyond the length of the context"));
    if (ctx.__suspends.some(function (s) { return s.frame === frame; })) return Promise.reject(dex("Failed to execute 'suspend' on 'OfflineAudioContext': there is already a suspend scheduled for the given frame", 'InvalidStateError'));
    return new Promise(function (res) { ctx.__suspends.push({ frame: frame, res: res }); });
  });
  defMethod(OfflineAudioContext.prototype, 'resume', function () {
    var ctx = this;
    if (ctx.__state !== 'suspended' || !ctx.__resume) return Promise.reject(dex("Failed to execute 'resume' on 'OfflineAudioContext': cannot resume a context that is not suspended", 'InvalidStateError'));
    ctx.__resume(); return Promise.resolve();
  });
  defMethod(OfflineAudioContext.prototype, 'close', function () { return Promise.reject(new TypeError('close is not a function')); });
  delete OfflineAudioContext.prototype.close;
  global.OfflineAudioContext = OfflineAudioContext; global.webkitOfflineAudioContext = OfflineAudioContext;
  function OfflineAudioCompletionEvent() { illegal(); } derive(OfflineAudioCompletionEvent, Event); global.OfflineAudioCompletionEvent = OfflineAudioCompletionEvent;
  function AudioProcessingEvent() { illegal(); } derive(AudioProcessingEvent, Event); tag(AudioProcessingEvent, 'AudioProcessingEvent'); global.AudioProcessingEvent = AudioProcessingEvent;

  // endpoint used by the media element code to expose its state to Web Audio
  var ON_ENDED = AudioScheduledSourceNode.prototype.addEventListener;
  AudioScheduledSourceNode.prototype.addEventListener = function (type) { if (type === 'ended') { this.__endedL = true; if (this._ctx) this._ctx._ensureTimer(); } return ON_ENDED.apply(this, arguments); };

  // ======================= ScriptProcessorNode ============================================================================================
  function ScriptProcessorNode() { illegal(); }
  derive(ScriptProcessorNode, AudioNode); tag(ScriptProcessorNode, 'ScriptProcessorNode');
  handlerProps(ScriptProcessorNode.prototype, ['audioprocess']);
  attr(ScriptProcessorNode, 'bufferSize', function () { return this.__bs; });
  DEFS.ScriptProcessorNode = { go: 'external', ins: 1, outs: 1, cc: 2, cm: 'explicit', ci: 'speakers', fixedCC: true, fixedCM: true, params: {} };
  function makeScriptProcessor(ctx, bs, inCh, outCh) {
    var node = Object.create(ScriptProcessorNode.prototype); EventTarget.call(node);
    var r = sw.waNode(ctx._id, 'external', JSON.stringify({ inputs: 1, outputs: 1 }));
    Object.defineProperty(node, '_ctx', { value: ctx }); Object.defineProperty(node, '_gid', { value: r.id });
    NODES.set(node, { def: DEFS.ScriptProcessorNode, cc: inCh, cm: 'explicit', ci: 'speakers', conns: [], params: {} });
    sw.waSet(ctx._id, r.id, 'channelCount', Math.max(inCh, 1)); sw.waSet(ctx._id, r.id, 'channelCountMode', 'explicit');
    node.__bs = bs;
    var inAcc = [], outQ = [], filled = 0, outPos = 0, pend = null;
    for (var c = 0; c < Math.max(inCh, 1); c++) inAcc.push(new Float32Array(bs));
    var curOut = null;
    ctx.__ext[r.id] = function (s, frame) {
      var chans = s.inputs[0].map(b64ToF32);
      for (var c = 0; c < inAcc.length; c++) { var src = chans[c] || new Float32Array(Q); inAcc[c].set(src, filled); }
      filled += Q;
      var out = new Float32Array(Q);
      if (curOut && outPos < bs) { out = curOut[0].subarray(outPos, outPos + Q); outPos += Q; }
      var outChans = []; for (var k = 0; k < Math.max(outCh, 1); k++) outChans.push(curOut && curOut[k] ? curOut[k].subarray(outPos - Q >= 0 ? outPos - Q : 0, outPos) : new Float32Array(Q));
      if (!curOut) outChans = outChans.map(function () { return new Float32Array(Q); });
      if (filled >= bs) {
        filled = 0;
        var inb = makeBuffer(Math.max(inCh, 1), bs, ctx.__rate), ob = makeBuffer(Math.max(outCh, 1), bs, ctx.__rate);
        var idat = bufData(inb); for (var i = 0; i < idat.length; i++) idat[i].set(inAcc[i]);
        var ev = new Event('audioprocess'); Object.setPrototypeOf(ev, AudioProcessingEvent.prototype);
        ev.playbackTime = (frame + Q) / ctx.__rate + bs / ctx.__rate; ev.inputBuffer = inb; ev.outputBuffer = ob;
        try { node.dispatchEvent(ev); } catch (e) { reportErr(e); }
        curOut = bufData(ob); outPos = 0;
      }
      return outChans.map(f32ToB64);
    };
    return node;
  }
  global.ScriptProcessorNode = ScriptProcessorNode;

  // ======================= AudioWorklet ====================================================================================================
  function AudioWorkletProcessor() {
    var o = global.__swWorkletInit; global.__swWorkletInit = null;
    if (!o) throw new TypeError('Illegal constructor');
    this.port = o.port;
  }
  var REG = new WeakMap();
  function makeWorklet(ctx) {
    var w = {};
    Object.defineProperty(w, 'addModule', { value: function (url) {
      return new Promise(function (res, rej) {
        _setTimeout(function () {
          var u = String(url), text;
          try {
            var abs = new global.URL(u, global.location.href).href;
            var r = sw.http('GET', abs, {}, '');
            if (r.status !== 200) throw new Error('HTTP ' + r.status);
            text = r.body;
          } catch (e) { rej(dex("Unable to load a worklet's module.", 'AbortError')); return; }
          var reg = REG.get(ctx) || new Map(); REG.set(ctx, reg);
          try { runWorkletCode(ctx, text, reg); } catch (e2) { rej(dex(String(e2 && e2.message || e2), 'AbortError')); return; }
          res();
        }, 0);
      });
    } });
    return w;
  }
  var WL_GLOBALS = ['sampleRate', 'currentFrame', 'currentTime', 'AudioWorkletProcessor', 'registerProcessor'];
  function withWorkletGlobals(ctx, fn, extra) {
    var saved = {};
    WL_GLOBALS.forEach(function (k) { saved[k] = Object.getOwnPropertyDescriptor(global, k); });
    var frame = ctx.__goFrame;
    var vals = { sampleRate: ctx.__rate, currentFrame: frame, currentTime: frame / ctx.__rate, AudioWorkletProcessor: AudioWorkletProcessor, registerProcessor: extra && extra.register };
    WL_GLOBALS.forEach(function (k) { Object.defineProperty(global, k, { value: vals[k], configurable: true, writable: true, enumerable: false }); });
    try { return fn(); } finally {
      WL_GLOBALS.forEach(function (k) { if (saved[k]) Object.defineProperty(global, k, saved[k]); else delete global[k]; });
    }
  }
  function runWorkletCode(ctx, text, reg) {
    var register = function (name, ctor) {
      if (typeof name !== 'string' || name === '') throw dex("Failed to execute 'registerProcessor' on 'AudioWorkletGlobalScope': The processor name is empty.", 'NotSupportedError');
      if (typeof ctor !== 'function' || !ctor.prototype || typeof ctor.prototype.process !== 'function') throw new TypeError("Failed to execute 'registerProcessor' on 'AudioWorkletGlobalScope': The class definition does not have a process() method.");
      if (reg.has(name)) throw dex("Failed to execute 'registerProcessor' on 'AudioWorkletGlobalScope': A processor with name '" + name + "' has already been registered in this scope.", 'NotSupportedError');
      reg.set(name, ctor);
    };
    withWorkletGlobals(ctx, function () { (0, eval)(text); }, { register: register });
  }
  function AudioWorkletNode(ctx, name, opts) {
    if (!(this instanceof AudioWorkletNode)) throw new TypeError("Failed to construct 'AudioWorkletNode': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 2) throw new TypeError("Failed to construct 'AudioWorkletNode': 2 arguments required, but only " + arguments.length + " present.");
    opts = opts || {};
    var reg = REG.get(ctx), Ctor = reg && reg.get(String(name));
    if (!Ctor) throw dex("Failed to construct 'AudioWorkletNode': AudioWorkletNode cannot be created: The node name '" + name + "' is not defined in AudioWorkletGlobalScope.", 'InvalidStateError');
    var nin = opts.numberOfInputs === undefined ? 1 : opts.numberOfInputs, nout = opts.numberOfOutputs === undefined ? 1 : opts.numberOfOutputs;
    if (nin === 0 && nout === 0) throw dex("Failed to construct 'AudioWorkletNode': Number of inputs and outputs cannot be both zero.", 'NotSupportedError');
    var descs = (Ctor.parameterDescriptors && Array.from(Ctor.parameterDescriptors)) || [];
    EventTarget.call(this);
    var r = sw.waNode(ctx._id, 'external', JSON.stringify({ inputs: nin, outputs: nout, params: descs.map(function (d) { return { name: d.name, def: d.defaultValue === undefined ? 0 : d.defaultValue, min: d.minValue === undefined ? -BIG : d.minValue, max: d.maxValue === undefined ? BIG : d.maxValue, arate: d.automationRate !== 'k-rate' }; }) }));
    var self = this;
    Object.defineProperty(this, '_ctx', { value: ctx }); Object.defineProperty(this, '_gid', { value: r.id });
    var def = { go: 'external', ins: nin, outs: nout, cc: 2, cm: 'max', ci: 'speakers', params: {} };
    NODES.set(this, { def: def, cc: 2, cm: 'max', ci: 'speakers', conns: [], params: {} });
    var pm = Object.create(AudioParamMap.prototype), pmap = new Map();
    descs.forEach(function (d) {
      var gi = r.params[d.name];
      var p = makeParam(self, d.name, { def: gi.def, min: gi.min, max: gi.max, arate: gi.arate, fixed: false });
      pmap.set(d.name, p);
      if (opts.parameterData && opts.parameterData[d.name] !== undefined) p.value = opts.parameterData[d.name];
    });
    pm.get = function (k) { return pmap.get(k); }; pm.has = function (k) { return pmap.has(k); }; pm.forEach = function (f) { pmap.forEach(function (v, k) { f(v, k, pm); }); };
    pm.entries = function () { return pmap.entries(); }; pm.keys = function () { return pmap.keys(); }; pm.values = function () { return pmap.values(); };
    pm[Symbol.iterator] = function () { return pmap.entries(); };
    Object.defineProperty(pm, 'size', { get: function () { return pmap.size; } });
    Object.defineProperty(this, 'parameters', { value: pm, enumerable: true });
    var ch = new global.MessageChannel();
    Object.defineProperty(this, 'port', { value: ch.port1, enumerable: true });
    var procOpts = { numberOfInputs: nin, numberOfOutputs: nout, outputChannelCount: opts.outputChannelCount, parameterData: opts.parameterData || {}, processorOptions: opts.processorOptions || {} };
    var proc;
    global.__swWorkletInit = { port: ch.port2 };
    try { proc = withWorkletGlobals(ctx, function () { return new Ctor(procOpts); }); } catch (e) { global.__swWorkletInit = null; throw e; }
    global.__swWorkletInit = null;
    var outCh = [];
    for (var o = 0; o < nout; o++) outCh.push((opts.outputChannelCount && opts.outputChannelCount[o]) || 0);
    var alive = true;
    ctx.__ext[r.id] = function (s, frame) {
      if (!alive) return null;
      var inputs = s.inputs.map(function (chs) { return chs.map(b64ToF32); });
      var outputs = [];
      for (var o2 = 0; o2 < nout; o2++) {
        var c2 = outCh[o2] || (inputs[0] && inputs[0].length) || 1; var arr = []; for (var k = 0; k < c2; k++) arr.push(new Float32Array(Q)); outputs.push(arr);
      }
      var params = {};
      Object.keys(s.params).forEach(function (k) { params[k] = b64ToF32(s.params[k]); });
      var keep = true;
      try { keep = withWorkletGlobals(ctx, function () { return proc.process(inputs, outputs, params); }); } catch (e) { reportErr(e); keep = false; var ee = new Event('processorerror'); try { self.dispatchEvent(ee); } catch (x) { /* ignore */ } }
      if (!keep && !s.inputs.some(function (c3) { return c3.length; })) { /* node may stop once inactive */ }
      if (!keep) alive = false;
      if (!outputs.length) return null;
      return outputs[0].map(f32ToB64);
    };
    ctx._register(this);
  }
  derive(AudioWorkletNode, AudioNode); tag(AudioWorkletNode, 'AudioWorkletNode');
  handlerProps(AudioWorkletNode.prototype, ['processorerror']);
  global.AudioWorkletNode = AudioWorkletNode; global.AudioWorklet = function AudioWorklet() { illegal(); };
})(globalThis);
