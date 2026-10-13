/*
 * Sweef font API: FontFace and document.fonts (FontFaceSet).
 * Loading happens in Go (internal/engine/webfonts.go); this file keeps the
 * script-visible objects and polls the host for completion with timers, so a
 * settling page waits for pending fonts like it waits for any other timer.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || !sw.fontFaceNew || !global.Document) return;

  function dex(msg, name) { return new global.DOMException(msg, name); }
  var ET = global.EventTarget;

  var faces = {};        // id -> FontFace
  var watched = {};      // id -> {resolve, reject} for faces whose loaded promise is pending
  var polling = false;
  var wasLoading = false;
  var readyDeferred = null;
  var setObj = null;

  function b64(bytes) {
    var s = '';
    for (var i = 0; i < bytes.length; i += 8192) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 8192));
    return global.btoa(s);
  }

  var DESC = ['style', 'weight', 'stretch', 'unicodeRange', 'display', 'featureSettings', 'variationSettings'];

  function FontFace(family, source, descriptors) {
    if (!(this instanceof FontFace)) throw new TypeError("Failed to construct 'FontFace': Please use the 'new' operator");
    if (arguments.length < 2) throw new TypeError("Failed to construct 'FontFace': 2 arguments required, but only " + arguments.length + ' present.');
    var d = descriptors || {};
    var src = null, data = null;
    if (typeof source === 'string') src = source;
    else if (source instanceof global.ArrayBuffer) data = new global.Uint8Array(source);
    else if (global.ArrayBuffer.isView(source)) data = new global.Uint8Array(source.buffer, source.byteOffset, source.byteLength);
    else src = String(source);
    var r = sw.fontFaceNew(String(family), src, d, data ? b64(data) : null);
    var self = this;
    var def = {};
    def.promise = new Promise(function (res, rej) { def.resolve = res; def.reject = rej; });
    def.promise.catch(function () { }); // a face nobody waits for must not report an unhandled rejection
    Object.defineProperty(this, '_s', { value: def, enumerable: false });
    if (r.error) {
      if (r.kind === 'descriptor') throw dex("Failed to construct 'FontFace': " + r.error, 'SyntaxError');
      Object.defineProperty(this, '_id', { value: 0, enumerable: false });
      this._err = dex(r.error, 'SyntaxError');
      def.reject(this._err);
      return;
    }
    Object.defineProperty(this, '_id', { value: r.id, enumerable: false });
    faces[r.id] = this;
    if (data) watch(this);
  }
  FontFace.prototype = Object.create(Object.prototype, { constructor: { value: FontFace, writable: true, configurable: true } });
  Object.defineProperty(FontFace.prototype, Symbol.toStringTag, { value: 'FontFace', configurable: true });

  function info(f) { return f._id ? sw.fontFaceInfo(f._id) : null; }
  ['family'].concat(DESC).concat(['variant']).forEach(function (name) {
    Object.defineProperty(FontFace.prototype, name, {
      configurable: true, enumerable: true,
      get: function () { var i = info(this); return i ? i[name] : (name === 'family' ? '' : 'normal'); },
      set: function (v) {
        if (!this._id || name === 'variant') return;
        var o = {}; o[name] = String(v);
        var err = sw.fontFaceUpdate(this._id, o);
        if (err) throw dex(err, 'SyntaxError');
      }
    });
  });
  Object.defineProperty(FontFace.prototype, 'status', {
    configurable: true, enumerable: true,
    get: function () { return this._id ? sw.fontFaceStatus(this._id) : 'error'; }
  });
  Object.defineProperty(FontFace.prototype, 'loaded', { configurable: true, enumerable: true, get: function () { return this._s.promise; } });
  FontFace.prototype.load = function load() {
    var self = this;
    if (!this._id) return this._s.promise;
    var st = sw.fontFaceLoad(this._id);
    if (st === 'loaded') settle(this, st);
    else if (st === 'error') settle(this, st);
    else watch(this);
    return this._s.promise;
  };

  function wrap(id) {
    if (faces[id]) return faces[id];
    var f = Object.create(FontFace.prototype);
    var def = {};
    def.promise = new Promise(function (res, rej) { def.resolve = res; def.reject = rej; });
    def.promise.catch(function () { });
    Object.defineProperty(f, '_s', { value: def, enumerable: false });
    Object.defineProperty(f, '_id', { value: id, enumerable: false });
    faces[id] = f;
    var st = sw.fontFaceStatus(id);
    if (st === 'loaded' || st === 'error') settle(f, st);
    return f;
  }

  function settle(f, st) {
    if (f._settled) return;
    if (st === 'loaded') { f._settled = true; f._s.resolve(f); }
    else if (st === 'error') { f._settled = true; f._s.reject(dex('A network error occurred.', 'NetworkError')); }
  }

  function watch(f) {
    watched[f._id] = f;
    poll();
  }

  // ---- polling ---------------------------------------------------------------
  function poll() {
    if (polling) return;
    polling = true;
    global.setTimeout(tick, 4);
  }

  function tick() {
    polling = false;
    var any = false, done = [];
    for (var id in watched) {
      var f = watched[id];
      var st = sw.fontFaceStatus(+id);
      if (st === 'loaded' || st === 'error') { delete watched[id]; settle(f, st); done.push(f); }
      else any = true;
    }
    var pending = sw.fontsPending();
    var loading = any || pending > 0;
    if (setObj) {
      if (loading && !wasLoading) startLoading();
      if (done.length) {
        var ok = done.filter(function (f) { return f.status === 'loaded'; });
        var bad = done.filter(function (f) { return f.status === 'error'; });
        if (ok.length) setObj._fire('loadingdone', ok);
        if (bad.length) setObj._fire('loadingerror', bad);
      }
      if (!loading && wasLoading) finishLoading();
    }
    wasLoading = loading;
    if (loading) poll();
  }

  function startLoading() {
    wasLoading = true;
    if (!readyDeferred || readyDeferred.done) readyDeferred = newDeferred();
    setObj._fire('loading', []);
  }

  function finishLoading() {
    wasLoading = false;
    if (readyDeferred && !readyDeferred.done) { readyDeferred.done = true; readyDeferred.resolve(setObj); }
  }

  function newDeferred() {
    var d = { done: false };
    d.promise = new Promise(function (res) { d.resolve = res; });
    return d;
  }

  // ---- FontFaceSet -------------------------------------------------------------
  function FontFaceSetLoadEvent(type, init) {
    this.type = type; this.bubbles = false; this.cancelable = false; this.target = null;
    this.fontfaces = (init && init.fontfaces) || [];
  }
  if (global.Event) FontFaceSetLoadEvent.prototype = Object.create(global.Event.prototype, { constructor: { value: FontFaceSetLoadEvent, writable: true, configurable: true } });

  function FontFaceSet() {
    if (ET) ET.call(this);
    this._l = {};
  }
  FontFaceSet.prototype = Object.create(ET ? ET.prototype : Object.prototype, { constructor: { value: FontFaceSet, writable: true, configurable: true } });
  Object.defineProperty(FontFaceSet.prototype, Symbol.toStringTag, { value: 'FontFaceSet', configurable: true });
  if (!ET) {
    FontFaceSet.prototype.addEventListener = function (t, f) { (this._l[t] = this._l[t] || []).push(f); };
    FontFaceSet.prototype.removeEventListener = function (t, f) { var a = this._l[t] || []; var i = a.indexOf(f); if (i >= 0) a.splice(i, 1); };
    FontFaceSet.prototype.dispatchEvent = function (e) { (this._l[e.type] || []).slice().forEach(function (f) { f.call(this, e); }, this); return true; };
  }
  FontFaceSet.prototype._fire = function (type, fl) {
    var ev = new FontFaceSetLoadEvent(type, { fontfaces: fl });
    ev.target = ev.currentTarget = this;
    var h = this['on' + type];
    try { if (typeof h === 'function') h.call(this, ev); } catch (e) { global.reportError && global.reportError(e); }
    if (ET) { try { this.dispatchEvent(ev); } catch (e) { } } else this.dispatchEvent(ev);
  };
  ['loading', 'loadingdone', 'loadingerror'].forEach(function (n) {
    Object.defineProperty(FontFaceSet.prototype, 'on' + n, { configurable: true, enumerable: true, get: function () { return this['_on' + n] || null; }, set: function (v) { this['_on' + n] = typeof v === 'function' ? v : null; } });
  });
  function ids() { return sw.fontsList(); }
  Object.defineProperty(FontFaceSet.prototype, 'size', { configurable: true, enumerable: true, get: function () { return ids().length; } });
  Object.defineProperty(FontFaceSet.prototype, 'status', {
    configurable: true, enumerable: true,
    get: function () { return sw.fontsSettle() > 0 || anyWatched() ? 'loading' : 'loaded'; }
  });
  function anyWatched() { for (var k in watched) return true; return false; }
  Object.defineProperty(FontFaceSet.prototype, 'ready', {
    configurable: true, enumerable: true,
    get: function () {
      // lay the page out first: the fonts its text needs are requested by layout
      var pending = sw.fontsSettle();
      if (pending > 0 || anyWatched()) {
        if (!readyDeferred || readyDeferred.done) readyDeferred = newDeferred();
        if (!wasLoading) { wasLoading = true; setObj._fire('loading', []); }
        poll();
        return readyDeferred.promise;
      }
      if (readyDeferred && !readyDeferred.done) return readyDeferred.promise;
      return Promise.resolve(this);
    }
  });
  FontFaceSet.prototype.add = function add(face) {
    if (!(face instanceof FontFace)) throw new TypeError("Failed to execute 'add' on 'FontFaceSet': parameter 1 is not of type 'FontFace'.");
    if (!face._id) throw dex('Cannot add a FontFace that has failed to parse.', 'InvalidModificationError');
    sw.fontsAdd(face._id);
    return this;
  };
  FontFaceSet.prototype.delete = function (face) { return face && face._id ? !!sw.fontsDelete(face._id) : false; };
  FontFaceSet.prototype.clear = function () { sw.fontsClear(); };
  FontFaceSet.prototype.has = function (face) { return !!face && !!face._id && ids().indexOf(face._id) >= 0; };
  FontFaceSet.prototype.forEach = function (cb, thisArg) { var self = this; ids().forEach(function (id) { var f = wrap(id); cb.call(thisArg, f, f, self); }); };
  FontFaceSet.prototype.values = function () { return ids().map(wrap)[Symbol.iterator](); };
  FontFaceSet.prototype.keys = FontFaceSet.prototype.values;
  FontFaceSet.prototype.entries = function () { return ids().map(function (id) { var f = wrap(id); return [f, f]; })[Symbol.iterator](); };
  FontFaceSet.prototype[Symbol.iterator] = FontFaceSet.prototype.values;
  FontFaceSet.prototype.check = function check(font, text) {
    var r = sw.fontsCheck(String(font), text === undefined ? '' : String(text));
    if (r.error) throw dex(r.error, 'SyntaxError');
    return r.ok;
  };
  FontFaceSet.prototype.load = function load(font, text) {
    var r = sw.fontsLoad(String(font), text === undefined ? '' : String(text));
    if (r.error) return Promise.reject(dex(r.error, 'SyntaxError'));
    var fl = r.ids.map(wrap);
    fl.forEach(function (f) { if (sw.fontFaceStatus(f._id) === 'unloaded') f.load(); else if (!f._settled) watch(f); });
    if (fl.length && !wasLoading && setObj) { wasLoading = true; setObj._fire('loading', []); poll(); }
    return Promise.all(fl.map(function (f) { return f._s.promise; })).then(function () { return fl; });
  };

  setObj = new FontFaceSet();
  Object.defineProperty(global.Document.prototype, 'fonts', { configurable: true, enumerable: true, get: function () { return setObj; } });
  Object.defineProperty(global, 'FontFace', { value: FontFace, writable: true, configurable: true, enumerable: false });
  Object.defineProperty(global, 'FontFaceSet', { value: FontFaceSet, writable: true, configurable: true, enumerable: false });
  Object.defineProperty(global, 'FontFaceSetLoadEvent', { value: FontFaceSetLoadEvent, writable: true, configurable: true, enumerable: false });
  if (global.document && global.document.fonts !== setObj) {
    try { Object.defineProperty(global.document, 'fonts', { configurable: true, enumerable: true, get: function () { return setObj; } }); } catch (e) { }
  }
})(globalThis);
