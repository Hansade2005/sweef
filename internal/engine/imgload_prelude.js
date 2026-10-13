/*
 * imgload_prelude.js: real loading for images the document does not hold.
 *
 * `new Image().src = url` and `img.src = url` on a detached <img> ask Go for the
 * image (__sw.imageLoad); Go answers through __swImgEvent from the event-loop
 * pump, which dispatches load or error on the element and records the natural
 * size. Images that are in the document keep the base prelude's behaviour:
 * layout fetches them.
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || typeof sw.imageLoad !== 'function' || !global.HTMLImageElement) return;
  var P = global.HTMLImageElement.prototype;
  var origSrc = Object.getOwnPropertyDescriptor(P, 'src');
  var origW = Object.getOwnPropertyDescriptor(P, 'naturalWidth');
  var origH = Object.getOwnPropertyDescriptor(P, 'naturalHeight');
  var origC = Object.getOwnPropertyDescriptor(P, 'complete');
  var pending = new Map();      // token -> img
  var STATE = typeof Symbol === 'function' ? Symbol('swImg') : '__swImg';

  function st(img) {
    var s = img[STATE];
    if (!s) { s = { token: 0, w: -1, h: -1, done: false }; Object.defineProperty(img, STATE, { value: s, configurable: true }); }
    return s;
  }

  function start(img, value) {
    var s = st(img);
    var abs;
    try { abs = new global.URL(String(value), global.location.href).href; } catch (e) { abs = String(value); }
    s.token = 0; s.w = s.h = -1; s.done = false;
    try { s.token = sw.imageLoad(abs); } catch (e) { s.token = 0; }
    if (s.token) pending.set(s.token, img);
    else { s.done = true; Promise.resolve().then(function () { img.dispatchEvent(new global.Event('error')); }); }
  }

  Object.defineProperty(P, 'src', {
    configurable: true, enumerable: true,
    get: origSrc.get,
    set: function (v) {
      var id = global.__swIdOf(this);
      var attached = false;
      try { attached = !!sw.attached(id); } catch (e) { /* ignore */ }
      if (attached || String(v) === '') { origSrc.set.call(this, v); return; }
      this.setAttribute('src', String(v));
      start(this, v);
    }
  });
  Object.defineProperty(P, 'naturalWidth', { configurable: true, enumerable: true, get: function () { var s = this[STATE]; return s && s.w >= 0 ? s.w : origW.get.call(this); } });
  Object.defineProperty(P, 'naturalHeight', { configurable: true, enumerable: true, get: function () { var s = this[STATE]; return s && s.h >= 0 ? s.h : origH.get.call(this); } });
  Object.defineProperty(P, 'complete', { configurable: true, enumerable: true, get: function () { var s = this[STATE]; return s && s.token && !s.done ? false : origC.get.call(this); } });
  Object.defineProperty(P, 'decode', {
    configurable: true, writable: true, enumerable: false,
    value: function () {
      var img = this, s = st(img);
      if (!s.token || s.done) return Promise.resolve();
      return new Promise(function (resolve, reject) {
        img.addEventListener('load', function () { resolve(); }, { once: true });
        img.addEventListener('error', function () { reject(new global.DOMException('The source image cannot be decoded.', 'EncodingError')); }, { once: true });
      });
    }
  });

  Object.defineProperty(global, '__swImgEvent', {
    configurable: true, writable: true, enumerable: false,
    value: function (token, type, w, h) {
      var img = pending.get(token);
      if (!img) return false;
      pending.delete(token);
      var s = st(img);
      if (s.token !== token) return false; // src was set again meanwhile
      s.done = true;
      if (type === 'load') { s.w = w | 0; s.h = h | 0; }
      img.dispatchEvent(new global.Event(type));
      return true;
    }
  });
})(globalThis);
