// Media behaviour fixtures. Each scenario logs the event sequence (name, ms,
// currentTime, readyState, networkState, paused) and key properties into
// window.__result; window.__done flips to true when finished. The same page
// is run in Chromium (reference) and Sweef.
(function () {
  var P = new URLSearchParams(location.search), name = P.get('s');
  var T0 = performance.now(), R = { events: [], x: {} };
  window.__result = R; window.__done = false; window.__name = name;
  function ms() { return Math.round(performance.now() - T0); }
  var EV = ['loadstart', 'progress', 'suspend', 'abort', 'error', 'emptied', 'stalled', 'loadedmetadata', 'loadeddata', 'canplay', 'canplaythrough', 'playing', 'waiting', 'seeking', 'seeked', 'ended', 'durationchange', 'timeupdate', 'play', 'pause', 'ratechange', 'volumechange', 'resize'];
  function watch(el, label, skip) {
    EV.forEach(function (n) {
      if (skip && skip.indexOf(n) >= 0) return;
      el.addEventListener(n, function () { R.events.push([(label || '') + n, ms(), +el.currentTime.toFixed(3), el.readyState, el.networkState, el.paused ? 1 : 0]); });
    });
  }
  function finish() { window.__done = true; }
  function after(t, f) { setTimeout(f, t); }
  function onGo(f) { document.getElementById('go').addEventListener('click', f); }
  function props(a, keys) { keys.forEach(function (k) { var v = a[k]; R.x[k] = (typeof v === 'number' && isNaN(v)) ? 'NaN' : (v === Infinity ? 'Infinity' : v); }); }
  function pr(p, label) { return p.then(function () { R.x[label] = 'resolved'; }, function (e) { R.x[label] = e.name; }); }
  function ranges(r) { var o = []; for (var i = 0; i < r.length; i++) o.push([+r.start(i).toFixed(2), +r.end(i).toFixed(2)]); return o; }
  var S = {};

  S.load_auto = function () { var a = new Audio('tone.wav'); watch(a); after(700, function () { props(a, ['duration', 'readyState', 'networkState', 'paused', 'ended', 'seeking', 'currentTime', 'preload']); R.x.src = a.currentSrc.split('/').pop(); R.x.buffered = ranges(a.buffered); R.x.seekable = ranges(a.seekable); finish(); }); };
  S.load_meta = function () { var a = document.createElement('audio'); a.preload = 'metadata'; watch(a); a.src = 'tone.mp3'; after(700, function () { props(a, ['duration', 'readyState', 'networkState', 'preload']); finish(); }); };
  S.load_none = function () { var a = document.createElement('audio'); a.preload = 'none'; watch(a); a.src = 'tone.mp3'; after(500, function () { props(a, ['duration', 'readyState', 'networkState']); finish(); }); };
  S.play_wav = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']); var tu = [];
    a.addEventListener('timeupdate', function () { tu.push(ms()); });
    var t0;
    onGo(function () { t0 = ms(); pr(a.play(), 'play'); });
    a.addEventListener('ended', function () {
      R.x.endedAfter = ms() - t0; props(a, ['currentTime', 'ended', 'paused', 'duration']);
      R.x.timeupdates = tu.length; R.x.tuGaps = tu.slice(1).map(function (v, i) { return v - tu[i]; });
      R.x.played = ranges(a.played); after(100, finish);
    });
    after(6000, finish);
  };
  S.play_muted_audio = function () { var a = new Audio('tone.wav'); a.muted = true; watch(a); pr(a.play(), 'play'); after(400, finish); };
  S.play_muted_video = function () { var v = document.createElement('video'); v.muted = true; v.src = 'clip.webm'; watch(v, '', ['timeupdate']); pr(v.play(), 'play'); after(700, function () { props(v, ['paused', 'readyState', 'videoWidth', 'videoHeight', 'duration']); finish(); }); };
  S.autoplay_blocked = function () { var a = new Audio('tone.wav'); a.autoplay = true; watch(a); after(600, function () { props(a, ['paused', 'readyState']); finish(); }); };
  S.autoplay_gesture = function () { var a = new Audio('tone.wav'); a.autoplay = true; watch(a, '', ['timeupdate']); onGo(function () { }); after(200, function () { }); a.addEventListener('ended', function () { finish(); }); after(4000, finish); };
  S.seek = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']);
    var did = false; a.addEventListener('canplaythrough', function () { if (did) return; did = true; R.x.seekRet = (a.currentTime = 1.0); R.x.seekingNow = a.seeking; R.x.ctNow = a.currentTime; });
    a.addEventListener('seeked', function () { props(a, ['currentTime', 'seeking']); after(150, finish); });
    after(3000, finish);
  };
  S.seek_playing = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']);
    onGo(function () { a.play().then(function () { after(300, function () { a.currentTime = 1.0; }); }); });
    a.addEventListener('seeked', function () { after(250, function () { props(a, ['currentTime', 'paused']); finish(); }); });
    after(4000, finish);
  };
  S.loop = function () {
    var a = new Audio('tone.wav'); a.loop = true; a.playbackRate = 3; watch(a, '', ['timeupdate']);
    onGo(function () { a.play(); });
    after(2200, function () { props(a, ['paused', 'ended', 'loop', 'playbackRate']); R.x.ctMod = a.currentTime < 1.5; finish(); });
  };
  S.rate = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']); var t0;
    onGo(function () { t0 = ms(); a.playbackRate = 2; a.play(); });
    a.addEventListener('ended', function () { R.x.endedAfter = ms() - t0; props(a, ['currentTime', 'playbackRate', 'defaultPlaybackRate']); finish(); });
    after(5000, finish);
  };
  S.pause_resume = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']);
    onGo(function () { a.play(); after(300, function () { a.pause(); R.x.ctAtPause = +a.currentTime.toFixed(1); after(300, function () { R.x.ctAfterPause = +a.currentTime.toFixed(1); pr(a.play(), 'play2'); after(300, function () { R.x.ctAfterResume = +a.currentTime.toFixed(1); finish(); }); }); }); });
    after(4000, finish);
  };
  S.attrs = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate', 'progress', 'suspend', 'loadstart']);
    a.addEventListener('canplaythrough', function () {
      a.volume = 0.5; a.volume = 0.5; a.muted = true; a.muted = true; a.playbackRate = 1.5; a.defaultPlaybackRate = 0.5;
      try { a.volume = 2; } catch (e) { R.x.volErr = e.name; }
      try { a.playbackRate = 100; } catch (e) { R.x.rateErr = e.name; }
      try { a.currentTime = NaN; } catch (e) { R.x.ctErr = e.name; }
      props(a, ['volume', 'muted', 'defaultMuted', 'playbackRate', 'defaultPlaybackRate', 'preservesPitch', 'controls', 'loop', 'autoplay', 'crossOrigin', 'preload', 'networkState']);
      after(100, finish);
    });
  };
  S.error_404 = function () { var a = new Audio('nope.mp3'); watch(a); after(600, function () { props(a, ['readyState', 'networkState']); R.x.errCode = a.error && a.error.code; R.x.errCtor = a.error && a.error.constructor.name; pr(a.play(), 'play'); after(100, finish); }); };
  S.error_garbage = function () { var a = new Audio('garbage.mp3'); watch(a); after(600, function () { props(a, ['readyState', 'networkState']); R.x.errCode = a.error && a.error.code; finish(); }); };
  S.error_nosrc = function () { var a = document.createElement('audio'); watch(a); onGo(function () { pr(a.play(), 'play'); after(200, function () { props(a, ['networkState', 'readyState']); R.x.error = a.error; finish(); }); }); };
  S.source_select = function () {
    var a = document.createElement('audio'); watch(a, '', ['timeupdate']);
    a.innerHTML = '<source id="s1" type="audio/x-bogus" src="tone.wav"><source id="s2" type="audio/wav" src="missing.wav"><source id="s3" src="tone.wav">';
    ['s1', 's2', 's3'].forEach(function (id) { a.querySelector('#' + id).addEventListener('error', function () { R.events.push(['source-error-' + id, ms()]); }); });
    document.body.appendChild(a); a.load();
    after(900, function () { R.x.currentSrc = a.currentSrc.split('/').pop(); props(a, ['readyState', 'networkState', 'duration']); finish(); });
  };
  S.reload = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate', 'progress', 'suspend']);
    a.addEventListener('canplaythrough', function once() { a.removeEventListener('canplaythrough', once); a.src = 'tone.mp3'; });
    after(1200, function () { props(a, ['readyState', 'networkState', 'duration']); R.x.src = a.currentSrc.split('/').pop(); finish(); });
  };
  S.video_webm = function () {
    var v = document.createElement('video'); v.muted = true; v.preload = 'auto'; v.src = 'clip.webm'; document.body.appendChild(v);
    watch(v, '', ['timeupdate', 'progress', 'suspend']); var frames = 0, meta = null;
    v.addEventListener('loadedmetadata', function () { props(v, ['videoWidth', 'videoHeight', 'duration']); });
    v.requestVideoFrameCallback(function cb(now, m) { frames++; meta = m; v.requestVideoFrameCallback(cb); });
    v.addEventListener('canplay', function () { v.play(); });
    after(1500, function () { R.x.rvfc = frames > 5; R.x.rvfcMeta = meta ? [meta.width, meta.height, typeof meta.mediaTime] : null; var q = v.getVideoPlaybackQuality(); R.x.quality = [q.totalVideoFrames > 5, q.droppedVideoFrames, q.corruptedVideoFrames]; props(v, ['paused', 'ended', 'readyState']); R.x.ct = v.currentTime > 1 && v.currentTime < 1.7; finish(); });
  };
  S.tracks = function () {
    var v = document.createElement('video'); v.muted = true; v.innerHTML = '<track id="t1" kind="subtitles" srclang="en" label="English" src="sub.vtt" default>'; v.src = 'clip.webm'; document.body.appendChild(v);
    var t = v.textTracks[0]; var t0; var cc = [];
    R.x.tracksLen = v.textTracks.length; R.x.mode0 = t.mode; R.x.kind = t.kind; R.x.label = t.label; R.x.lang = t.language;
    t.addEventListener('cuechange', function () { cc.push([ms() - t0, t.activeCues ? t.activeCues.length : -1, +v.currentTime.toFixed(1)]); });
    document.getElementById('t1').addEventListener('load', function () { R.events.push(['track-load', ms()]); });
    v.addEventListener('canplay', function () { t0 = ms(); v.play(); });
    after(2300, function () { R.x.cues = t.cues ? t.cues.length : -1; R.x.cue0 = t.cues && t.cues[0] ? [t.cues[0].id, t.cues[0].startTime, t.cues[0].endTime, t.cues[0].text, t.cues[0].align, t.cues[0].line, t.cues[0].snapToLines] : null; R.x.cueChanges = cc.map(function (c) { return [Math.round(c[0] / 100) * 100, c[1]]; }); R.x.trackReady = document.getElementById('t1').readyState; finish(); });
  };
  S.ranges = function () {
    var a = new Audio('tone.wav');
    a.addEventListener('canplaythrough', function () { R.x.buffered = ranges(a.buffered); R.x.seekable = ranges(a.seekable); R.x.playedLen = a.played.length; try { a.buffered.start(5); } catch (e) { R.x.idxErr = e.name; } finish(); });
    after(2000, finish);
  };
  S.misc_apis = function () {
    var a = document.createElement('video'); R.x = {};
    pr(a.requestPictureInPicture(), 'pip'); pr(a.setMediaKeys(null), 'mediaKeys');
    R.x.pipEnabled = document.pictureInPictureEnabled; R.x.mk = a.mediaKeys;
    pr(a.setSinkId('nonexistent'), 'sink'); R.x.sinkId = a.sinkId;
    pr(navigator.requestMediaKeySystemAccess('com.widevine.alpha', [{}]), 'eme');
    pr(navigator.mediaDevices.getUserMedia({ audio: true }), 'gum');
    pr(navigator.mediaDevices.getUserMedia({}), 'gumEmpty');
    var s = a.captureStream(); R.x.capTracks = s.getTracks().map(function (t) { return t.kind; });
    R.x.remote = typeof a.remote; R.x.cps = [a.canPlayType('video/webm; codecs="vp8"'), a.canPlayType('audio/mpeg'), a.canPlayType('application/vnd.apple.mpegurl'), a.canPlayType('video/x-bogus')];
    navigator.mediaDevices.enumerateDevices().then(function (d) { R.x.devices = d.length; });
    R.x.recorder = typeof MediaRecorder; R.x.voices = speechSynthesis.getVoices().length;
    R.x.audioTracks = typeof a.audioTracks; R.x.vtt = typeof VTTCue;
    after(300, finish);
  };
  S.mse_basic = function () {
    R.x.supported = [MediaSource.isTypeSupported('video/webm; codecs="vp8, vorbis"'), MediaSource.isTypeSupported('video/webm; codecs="vp8"'), MediaSource.isTypeSupported('video/x-bogus')];
    var ms = new MediaSource(), v = document.createElement('video'); v.muted = true;
    var log = function (n) { R.events.push([n, ms2()]); }; function ms2() { return ms_(); } function ms_() { return Math.round(performance.now() - T0); }
    ['sourceopen', 'sourceended', 'sourceclose'].forEach(function (n) { ms.addEventListener(n, function () { log('ms-' + n); }); });
    watch(v, '', ['timeupdate', 'progress', 'suspend', 'resize']);
    R.x.rs0 = ms.readyState;
    v.src = URL.createObjectURL(ms);
    ms.addEventListener('sourceopen', function () {
      R.x.rsOpen = ms.readyState;
      var sb = ms.addSourceBuffer('video/webm; codecs="vp8, vorbis"');
      ['updatestart', 'update', 'updateend', 'error'].forEach(function (n) { sb.addEventListener(n, function () { log('sb-' + n); }); });
      var xhr = new XMLHttpRequest(); xhr.open('GET', 'clip.webm'); xhr.responseType = 'arraybuffer';
      xhr.onload = function () {
        var buf = xhr.response; R.x.bytes = buf.byteLength;
        var off = 0;
        (function next() {
          if (off >= buf.byteLength) { ms.endOfStream(); R.x.dur = ms.duration; R.x.buffered = ranges(sb.buffered); R.x.rsEnded = ms.readyState; after(300, function () { props(v, ['readyState', 'duration', 'videoWidth']); finish(); }); return; }
          var end = Math.min(buf.byteLength, off + 12000);
          sb.addEventListener('updateend', function h() { sb.removeEventListener('updateend', h); next(); });
          sb.appendBuffer(buf.slice(off, end)); R.x.updatingAfterAppend = sb.updating; off = end;
        })();
      };
      xhr.send();
    });
    after(5000, finish);
  };

  S.seek_tu = function () {
    var a = new Audio('tone.wav'); watch(a);
    onGo(function () { a.play().then(function () { after(300, function () { a.currentTime = 1.0; }); }); });
    a.addEventListener('seeked', function () { after(300, function () { a.pause(); after(100, finish); }); });
    after(4000, finish);
  };
  S.loop_events = function () {
    var a = new Audio('tone.wav'); a.loop = true; watch(a);
    onGo(function () { a.currentTime = 1.2; a.play(); });
    after(1800, function () { a.pause(); after(100, finish); });
  };
  S.seek_paused_tu = function () {
    var a = new Audio('tone.wav'); watch(a);
    a.addEventListener('canplaythrough', function once() { a.removeEventListener('canplaythrough', once); a.currentTime = 0.5; });
    a.addEventListener('seeked', function () { after(200, finish); });
    after(3000, finish);
  };
  S.rate_events = function () { var a = new Audio('tone.wav'); watch(a); a.addEventListener('canplaythrough', function once() { a.removeEventListener('canplaythrough', once); a.playbackRate = 2; a.volume = 0.3; a.muted = true; a.muted = false; a.defaultPlaybackRate = 1.5; after(100, finish); }); after(3000, finish); };
  S.ended_replay = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']); var n = 0;
    onGo(function () { a.playbackRate = 4; a.play(); });
    a.addEventListener('ended', function () { if (n++ === 0) { R.x.endedState = [a.ended, a.paused, a.currentTime]; a.play(); } else { after(50, finish); } });
    after(4000, finish);
  };
  S.play_pause_promise = function () {
    var a = new Audio('tone.wav'); watch(a, '', ['timeupdate']);
    a.addEventListener('canplaythrough', function once() { a.removeEventListener('canplaythrough', once); });
    onGo(function () { var p = a.play(); pr(p, 'p1'); a.pause(); after(100, finish); });
    after(3000, finish);
  };
  if (S[name]) { try { S[name](); } catch (e) { R.x.exception = String(e && e.stack || e); finish(); } } else { R.x.unknown = name; finish(); }
})();
