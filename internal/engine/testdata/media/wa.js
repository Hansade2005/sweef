// Offline Web Audio fixtures: each scenario renders a graph in an
// OfflineAudioContext and leaves base64 float32 channel data in
// window.__result. Run in Chromium (reference) and Sweef and compared
// numerically by media_test.go.
(function () {
  var name = new URLSearchParams(location.search).get('s');
  window.__done = false; window.__name = name;
  var SR = 44100;
  function b64(f) { var u = new Uint8Array(f.buffer, f.byteOffset, f.byteLength), s = ''; for (var i = 0; i < u.length; i += 0x6000) s += String.fromCharCode.apply(null, u.subarray(i, i + 0x6000)); return btoa(s); }
  function lcg(n, seed) { var a = new Float32Array(n), x = seed || 12345; for (var i = 0; i < n; i++) { x = (Math.imul(x, 1103515245) + 12345) >>> 0; a[i] = (x / 4294967296) * 2 - 1; } return a; }
  function noiseBuf(ctx, n, seed) { var b = ctx.createBuffer(1, n, ctx.sampleRate); b.getChannelData(0).set(lcg(n, seed)); return b; }
  function sineBuf(ctx, n, f) { var b = ctx.createBuffer(1, n, ctx.sampleRate), d = b.getChannelData(0); for (var i = 0; i < n; i++) d[i] = Math.sin(2 * Math.PI * f * i / ctx.sampleRate); return b; }
  function done(buf, extra) {
    var ch = []; for (var c = 0; c < buf.numberOfChannels; c++) ch.push(b64(buf.getChannelData(c)));
    window.__result = { name: name, rate: buf.sampleRate, length: buf.length, ch: ch, extra: extra || {} }; window.__done = true;
  }
  function fail(e) { window.__result = { name: name, error: String(e && e.stack || e) }; window.__done = true; }
  var S = {};
  function off(ch, len, rate) { return new OfflineAudioContext(ch, len, rate || SR); }

  S.osc_sine = function () { var c = off(1, 4096), o = c.createOscillator(); o.frequency.value = 440; o.connect(c.destination); o.start(); return c.startRendering(); };
  S.osc_types = function () {
    var c = off(4, 2048), m = c.createChannelMerger(4);
    ['sine', 'square', 'sawtooth', 'triangle'].forEach(function (t, i) { var o = c.createOscillator(); o.type = t; o.frequency.value = 440; o.connect(m, 0, i); o.start(); });
    m.connect(c.destination); return c.startRendering();
  };
  S.osc_types_low = function () {
    var c = off(4, 4096), m = c.createChannelMerger(4);
    ['sine', 'square', 'sawtooth', 'triangle'].forEach(function (t, i) { var o = c.createOscillator(); o.type = t; o.frequency.value = 55; o.detune.value = 100; o.connect(m, 0, i); o.start(); });
    m.connect(c.destination); return c.startRendering();
  };
  S.osc_freq_ramp = function () {
    var c = off(1, 8192), o = c.createOscillator(); o.frequency.setValueAtTime(200, 0); o.frequency.linearRampToValueAtTime(2000, 0.15); o.connect(c.destination); o.start(0); o.stop(0.17); return c.startRendering();
  };
  // automation scheduled before the source starts: Chromium restarts the ramp at the start frame
  S.osc_freq_ramp_late = function () {
    var c = off(1, 8192), o = c.createOscillator(); o.frequency.setValueAtTime(200, 0); o.frequency.linearRampToValueAtTime(2000, 0.15); o.connect(c.destination); o.start(0.01); o.stop(0.17); return c.startRendering();
  };
  S.osc_periodic = function () {
    var c = off(1, 2048), o = c.createOscillator(), w = c.createPeriodicWave(new Float32Array([0, 0.5, 0.25, 0]), new Float32Array([0, 1, 0, 0.3]));
    o.setPeriodicWave(w); o.frequency.value = 300; o.connect(c.destination); o.start(); return c.startRendering();
  };
  S.osc_biquad_gain = function () {
    var c = off(1, 8192), o = c.createOscillator(), f = c.createBiquadFilter(), g = c.createGain();
    o.type = 'sawtooth'; o.frequency.value = 220; f.type = 'lowpass'; f.frequency.value = 1000; f.Q.value = 2;
    g.gain.setValueAtTime(0, 0); g.gain.linearRampToValueAtTime(0.5, 0.05); g.gain.setTargetAtTime(0.1, 0.1, 0.02);
    o.connect(f); f.connect(g); g.connect(c.destination); o.start(); return c.startRendering();
  };
  S.biquad_types = function () {
    var types = ['lowpass', 'highpass', 'bandpass', 'lowshelf', 'highshelf', 'peaking', 'notch', 'allpass'];
    var c = off(8, 4096), src = c.createBufferSource(), m = c.createChannelMerger(8);
    src.buffer = noiseBuf(c, 4096, 7);
    types.forEach(function (t, i) { var f = c.createBiquadFilter(); f.type = t; f.frequency.value = 3000; f.Q.value = 3; f.gain.value = 9; src.connect(f); f.connect(m, 0, i); });
    m.connect(c.destination); src.start(); return c.startRendering();
  };
  S.biquad_response = function () {
    var c = off(1, 128), f = c.createBiquadFilter(); f.type = 'peaking'; f.frequency.value = 2000; f.Q.value = 2; f.gain.value = 12;
    var fr = new Float32Array([100, 1000, 2000, 5000, 20000, 30000]), mag = new Float32Array(6), ph = new Float32Array(6);
    f.getFrequencyResponse(fr, mag, ph);
    return c.startRendering().then(function (b) { return { buf: b, extra: { mag: Array.from(mag).map(function (x) { return isNaN(x) ? 'NaN' : x; }), phase: Array.from(ph).map(function (x) { return isNaN(x) ? 'NaN' : x; }) } }; });
  };
  S.bufsource_rate = function () {
    var c = off(1, 8192), s = c.createBufferSource(); s.buffer = sineBuf(c, 4000, 1000);
    s.playbackRate.setValueAtTime(1, 0); s.playbackRate.linearRampToValueAtTime(1.5, 0.1); s.connect(c.destination); s.start(0); return c.startRendering();
  };
  S.bufsource_loop = function () {
    var c = off(1, 8192), s = c.createBufferSource(); s.buffer = sineBuf(c, 1000, 441); s.loop = true; s.loopStart = 0.005; s.loopEnd = 0.015; s.playbackRate.value = 0.75; s.detune.value = 100;
    s.connect(c.destination); s.start(0); s.stop(0.15); return c.startRendering();
  };
  S.bufsource_offset = function () {
    var c = off(1, 4096), s = c.createBufferSource(); s.buffer = sineBuf(c, 4000, 700); s.connect(c.destination); s.start(0.01, 0.02, 0.03); return c.startRendering();
  };
  S.gain_automation = function () {
    var c = off(1, 8192), s = c.createConstantSource(), g = c.createGain(); s.connect(g); g.connect(c.destination); s.start();
    var p = g.gain; p.setValueAtTime(0.2, 0); p.exponentialRampToValueAtTime(1, 0.05); p.setValueCurveAtTime(new Float32Array([1, 0.2, 0.8, 0.1]), 0.06, 0.05); p.setTargetAtTime(0.7, 0.12, 0.01); p.cancelScheduledValues(0.16); p.linearRampToValueAtTime(0.3, 0.18);
    return c.startRendering();
  };
  S.param_audio_rate = function () {
    var c = off(1, 4096), car = c.createOscillator(), mod = c.createOscillator(), mg = c.createGain(), g = c.createGain();
    mod.frequency.value = 30; mg.gain.value = 0.5; g.gain.value = 0.5; mod.connect(mg); mg.connect(g.gain); car.frequency.value = 880; car.connect(g); g.connect(c.destination); car.start(); mod.start(); return c.startRendering();
  };
  S.delay = function () {
    var c = off(1, 2048), s = c.createBufferSource(), d = c.createDelay(1), b = c.createBuffer(1, 8, SR); b.getChannelData(0).set([1, 0.5, 0.25, 0, 0, 0, 0, 0]);
    s.buffer = b; d.delayTime.value = 100.5 / SR; s.connect(d); d.connect(c.destination); s.start(); return c.startRendering();
  };
  S.convolver = function () {
    var c = off(2, 4096), s = c.createBufferSource(), cv = c.createConvolver(); s.buffer = noiseBuf(c, 2000, 3);
    var ir = c.createBuffer(1, 300, SR); var d = ir.getChannelData(0); d.set(lcg(300, 9)); for (var i = 0; i < 300; i++) d[i] *= Math.exp(-i / 60);
    cv.buffer = ir; s.connect(cv); cv.connect(c.destination); s.start(); return c.startRendering();
  };
  S.convolver_raw = function () {
    var c = off(1, 4096), s = c.createBufferSource(), cv = c.createConvolver(); s.buffer = noiseBuf(c, 1000, 5);
    var ir = c.createBuffer(1, 500, SR); ir.getChannelData(0).set(lcg(500, 11)); cv.normalize = false; cv.buffer = ir; s.connect(cv); cv.connect(c.destination); s.start(); return c.startRendering();
  };
  S.waveshaper = function () {
    var c = off(3, 2048), o = c.createOscillator(), m = c.createChannelMerger(3); o.frequency.value = 500; o.start();
    var curve = new Float32Array(257); for (var i = 0; i < 257; i++) { var x = i * 2 / 256 - 1; curve[i] = Math.tanh(3 * x); }
    ['none', '2x', '4x'].forEach(function (ov, i) { var w = c.createWaveShaper(); w.curve = curve; w.oversample = ov; var g = c.createGain(); g.gain.value = 0.9; o.connect(g); g.connect(w); w.connect(m, 0, i); });
    m.connect(c.destination); return c.startRendering();
  };
  S.stereopanner = function () {
    var c = off(4, 1024), mono = c.createOscillator(), st = c.createBufferSource(), p1 = c.createStereoPanner(), p2 = c.createStereoPanner(), m = c.createChannelMerger(4), sp = c.createChannelSplitter(2);
    var b = c.createBuffer(2, 1024, SR); b.getChannelData(0).set(lcg(1024, 1)); b.getChannelData(1).set(lcg(1024, 2)); st.buffer = b;
    mono.frequency.value = 300; p1.pan.value = -0.5; p2.pan.value = 0.3;
    mono.connect(p1); p1.connect(sp); sp.connect(m, 0, 0); sp.connect(m, 1, 1); st.connect(p2); p2.connect(m, 0, 2);
    var sp2 = c.createChannelSplitter(2); p2.disconnect(); p2.connect(sp2); sp2.connect(m, 0, 2); sp2.connect(m, 1, 3);
    m.connect(c.destination); mono.start(); st.start(); return c.startRendering();
  };
  S.panner = function () {
    var c = off(2, 2048), o = c.createOscillator(), p = c.createPanner(); p.positionX.value = 3; p.positionY.value = 0; p.positionZ.value = -4; p.distanceModel = 'inverse'; p.refDistance = 1;
    o.frequency.value = 400; o.connect(p); p.connect(c.destination); o.start(); return c.startRendering();
  };
  S.iir = function () {
    var c = off(1, 2048), s = c.createBufferSource(), f = c.createIIRFilter([0.1, 0.2, 0.1], [1, -0.5, 0.2]); s.buffer = noiseBuf(c, 2048, 21); s.connect(f); f.connect(c.destination); s.start();
    return c.startRendering().then(function (b) { var m = new Float32Array(3), p = new Float32Array(3); f.getFrequencyResponse(new Float32Array([100, 5000, 20000]), m, p); return { buf: b, extra: { mag: Array.from(m), phase: Array.from(p) } }; });
  };
  S.mixing = function () {
    var c = off(2, 512), o = c.createOscillator(), g = c.createGain(); o.frequency.value = 600; g.channelCount = 4; g.channelCountMode = 'explicit'; o.connect(g); g.connect(c.destination); o.start(); return c.startRendering();
  };
  S.compressor = function () {
    var c = off(1, 16384), o = c.createOscillator(), g = c.createGain(), d = c.createDynamicsCompressor(); o.frequency.value = 300; g.gain.setValueAtTime(0.1, 0); g.gain.setValueAtTime(1, 0.1); g.gain.setValueAtTime(0.1, 0.25);
    o.connect(g); g.connect(d); d.connect(c.destination); o.start();
    return c.startRendering().then(function (b) { return { buf: b, extra: { reduction: d.reduction } }; });
  };
  S.analyser = function () {
    var c = off(1, 16384), o = c.createOscillator(), a = c.createAnalyser(); o.frequency.value = 1500; a.fftSize = 1024; a.smoothingTimeConstant = 0.5; o.connect(a); a.connect(c.destination); o.start();
    var extra = {};
    c.suspend(8192 / SR).then(function () {
      var f = new Float32Array(a.frequencyBinCount), bf = new Uint8Array(a.frequencyBinCount), t = new Float32Array(a.fftSize), bt = new Uint8Array(a.fftSize);
      a.getFloatFrequencyData(f); a.getByteFrequencyData(bf); a.getFloatTimeDomainData(t); a.getByteTimeDomainData(bt);
      extra = { freq: b64(f), bfreq: Array.from(bf), time: b64(t), btime: Array.from(bt), bins: a.frequencyBinCount, ct: c.currentTime };
      c.resume();
    });
    return c.startRendering().then(function (b) { return { buf: b, extra: extra }; });
  };
  S.suspend_resume = function () {
    var c = off(1, 4096), o = c.createOscillator(); o.connect(c.destination); o.start(); var times = [];
    c.suspend(0.03).then(function () { times.push(c.currentTime, c.state); c.resume(); });
    return c.startRendering().then(function (b) { return { buf: b, extra: { times: times, state: c.state } }; });
  };
  S.decode = function () {
    var c = off(1, 128); var x = new XMLHttpRequest(); x.open('GET', 'tone.wav'); x.responseType = 'arraybuffer';
    return new Promise(function (res) { x.onload = function () { res(x.response); }; x.send(); }).then(function (ab) { return c.decodeAudioData(ab); }).then(function (b) {
      return { buf: b, extra: { len: b.length, rate: b.sampleRate, ch: b.numberOfChannels, dur: b.duration } };
    });
  };
  S.decode_mp3 = function () {
    var c = off(1, 128); var x = new XMLHttpRequest(); x.open('GET', 'tone.mp3'); x.responseType = 'arraybuffer';
    return new Promise(function (res) { x.onload = function () { res(x.response); }; x.send(); }).then(function (ab) { return c.decodeAudioData(ab); }).then(function (b) {
      return { buf: b, extra: { len: b.length, rate: b.sampleRate, ch: b.numberOfChannels, dur: b.duration } };
    });
  };
  S.decode_bad = function () {
    var c = off(1, 128), extra = {};
    return c.decodeAudioData(new ArrayBuffer(100)).then(function () { extra.r = 'resolved'; }, function (e) { extra.r = e.name; }).then(function () {
      return new Promise(function (res) { c.decodeAudioData(new ArrayBuffer(10), function () { extra.cb = 'ok'; res(); }, function (e) { extra.cb = e.name; res(); }); });
    }).then(function () { return { buf: c.createBuffer(1, 1, SR), extra: extra }; });
  };
  S.api_shape = function () {
    var c = new AudioContext(), o = {};
    o.state0 = c.state; o.rate = c.sampleRate; o.dest = [c.destination.numberOfInputs, c.destination.numberOfOutputs, c.destination.channelCount, c.destination.channelCountMode, c.destination.maxChannelCount];
    var g = c.createGain(); o.gain = [g.gain.value, g.gain.defaultValue, g.gain.minValue > -1e38, g.gain.maxValue > 1e38, g.gain.automationRate, g.channelCount, g.channelCountMode, g.channelInterpretation, g.numberOfInputs, g.numberOfOutputs];
    var os = c.createOscillator(); o.osc = [os.type, os.frequency.value, os.detune.value, os.frequency.maxValue, os.frequency.minValue];
    var an = c.createAnalyser(); o.an = [an.fftSize, an.frequencyBinCount, an.minDecibels, an.maxDecibels, an.smoothingTimeConstant];
    var bq = c.createBiquadFilter(); o.bq = [bq.type, bq.frequency.value, bq.Q.value, bq.gain.value, bq.detune.value];
    var cp = c.createDynamicsCompressor(); o.cp = [cp.threshold.value, cp.knee.value, cp.ratio.value, cp.attack.value, cp.release.value, cp.reduction];
    var pn = c.createPanner(); o.pn = [pn.panningModel, pn.distanceModel, pn.refDistance, pn.maxDistance, pn.rolloffFactor, pn.coneInnerAngle, pn.coneOuterAngle, pn.coneOuterGain, pn.positionX.value, pn.orientationX.value, pn.channelCount, pn.channelCountMode];
    var sp = c.createChannelSplitter(); var mg = c.createChannelMerger(); o.sm = [sp.numberOfOutputs, sp.channelCount, sp.channelCountMode, sp.channelInterpretation, mg.numberOfInputs, mg.channelCount, mg.channelCountMode];
    var sd = c.createDelay(); o.dl = [sd.delayTime.maxValue, sd.delayTime.value];
    var cv = c.createConvolver(); o.cv = [cv.normalize, cv.buffer, cv.channelCount, cv.channelCountMode];
    var ws = c.createWaveShaper(); o.ws = [ws.curve, ws.oversample];
    var bs = c.createBufferSource(); o.bs = [bs.buffer, bs.loop, bs.loopStart, bs.loopEnd, bs.playbackRate.value, bs.detune.value];
    var errs = {};
    try { g.connect(c.createGain()); g.connect(c.createGain(), 5); } catch (e) { errs.connectIdx = e.name; }
    try { c.createBuffer(0, 1, 44100); } catch (e) { errs.buf0 = e.name; }
    try { c.createBuffer(1, 1, 100); } catch (e) { errs.bufRate = e.name; }
    try { g.gain.setValueAtTime(1, -1); } catch (e) { errs.negTime = e.name; }
    try { g.gain.exponentialRampToValueAtTime(0, 1); } catch (e) { errs.expZero = e.name; }
    try { os.type = 'custom'; } catch (e) { errs.customType = e.name; }
    try { os.stop(); } catch (e) { errs.stopFirst = e.name; }
    try { os.start(); os.start(); } catch (e) { errs.startTwice = e.name; }
    try { an.fftSize = 100; } catch (e) { errs.fft = e.name; }
    try { new AudioContext({ sampleRate: 100 }); } catch (e) { errs.badRate = e.name; }
    try { new OfflineAudioContext(1, 0, 44100); } catch (e) { errs.offLen = e.name; }
    o.errs = errs; o.proto = [Object.prototype.toString.call(c), Object.prototype.toString.call(g), Object.prototype.toString.call(g.gain), g instanceof AudioNode, os instanceof AudioScheduledSourceNode];
    return Promise.resolve({ buf: new OfflineAudioContext(1, 1, 44100).createBuffer(1, 1, 44100), extra: o });
  };
  if (!S[name]) { fail('unknown scenario ' + name); return; }
  try {
    S[name]().then(function (r) { if (r && r.buf) done(r.buf, r.extra); else done(r); }, fail);
  } catch (e) { fail(e); }
})();
