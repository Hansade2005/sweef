/*
 * Sweef canvas prelude: HTMLCanvasElement 2D/bitmaprenderer contexts,
 * OffscreenCanvas, ImageData, ImageBitmap, Path2D, CanvasGradient/Pattern,
 * TextMetrics, DOMMatrix/DOMPoint. Drawing happens in Go (internal/raster)
 * through the __sw.cv* host functions (canvas.go).
 */
globalThis.__swCanvasFactories = globalThis.__swCanvasFactories || {};
Object.defineProperty(globalThis, '__swCanvasFactories', { enumerable: false });
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || !sw.cvNew || !global.HTMLCanvasElement) return;
  var factories = global.__swCanvasFactories;

  function dex(msg, name) { return new global.DOMException(msg, name); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  function def(o, n, f) { Object.defineProperty(o, n, { value: f, writable: true, configurable: true, enumerable: true }); }
  function defs(o, map) { for (var k in map) def(o, k, map[k]); }
  function getter(o, n, g, s) { Object.defineProperty(o, n, { get: g, set: s, configurable: true, enumerable: true }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  var isFin = Number.isFinite;
  function num(v) { return +v; }
  function fin() { for (var i = 0; i < arguments.length; i++) if (!isFin(arguments[i])) return false; return true; }
  var registry = typeof global.FinalizationRegistry === 'function' ? new global.FinalizationRegistry(function (h) { try { sw.cvFree(h); } catch (e) { } }) : null;
  function track(obj, h) { if (registry && h) registry.register(obj, h); }

  /* ---------------------------------------------------------------- DOMMatrix */
  var IDM = function () { return [1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]; };
  function mmul(a, b) { // a*b, column-major
    var r = new Array(16);
    for (var c = 0; c < 4; c++) for (var rr = 0; rr < 4; rr++) {
      var s = 0;
      for (var k = 0; k < 4; k++) s += a[k * 4 + rr] * b[c * 4 + k];
      r[c * 4 + rr] = s;
    }
    return r;
  }
  function minv(m) {
    var inv = new Array(16), i;
    inv[0] = m[5] * m[10] * m[15] - m[5] * m[11] * m[14] - m[9] * m[6] * m[15] + m[9] * m[7] * m[14] + m[13] * m[6] * m[11] - m[13] * m[7] * m[10];
    inv[4] = -m[4] * m[10] * m[15] + m[4] * m[11] * m[14] + m[8] * m[6] * m[15] - m[8] * m[7] * m[14] - m[12] * m[6] * m[11] + m[12] * m[7] * m[10];
    inv[8] = m[4] * m[9] * m[15] - m[4] * m[11] * m[13] - m[8] * m[5] * m[15] + m[8] * m[7] * m[13] + m[12] * m[5] * m[11] - m[12] * m[7] * m[9];
    inv[12] = -m[4] * m[9] * m[14] + m[4] * m[10] * m[13] + m[8] * m[5] * m[14] - m[8] * m[6] * m[13] - m[12] * m[5] * m[10] + m[12] * m[6] * m[9];
    inv[1] = -m[1] * m[10] * m[15] + m[1] * m[11] * m[14] + m[9] * m[2] * m[15] - m[9] * m[3] * m[14] - m[13] * m[2] * m[11] + m[13] * m[3] * m[10];
    inv[5] = m[0] * m[10] * m[15] - m[0] * m[11] * m[14] - m[8] * m[2] * m[15] + m[8] * m[3] * m[14] + m[12] * m[2] * m[11] - m[12] * m[3] * m[10];
    inv[9] = -m[0] * m[9] * m[15] + m[0] * m[11] * m[13] + m[8] * m[1] * m[15] - m[8] * m[3] * m[13] - m[12] * m[1] * m[11] + m[12] * m[3] * m[9];
    inv[13] = m[0] * m[9] * m[14] - m[0] * m[10] * m[13] - m[8] * m[1] * m[14] + m[8] * m[2] * m[13] + m[12] * m[1] * m[10] - m[12] * m[2] * m[9];
    inv[2] = m[1] * m[6] * m[15] - m[1] * m[7] * m[14] - m[5] * m[2] * m[15] + m[5] * m[3] * m[14] + m[13] * m[2] * m[7] - m[13] * m[3] * m[6];
    inv[6] = -m[0] * m[6] * m[15] + m[0] * m[7] * m[14] + m[4] * m[2] * m[15] - m[4] * m[3] * m[14] - m[12] * m[2] * m[7] + m[12] * m[3] * m[6];
    inv[10] = m[0] * m[5] * m[15] - m[0] * m[7] * m[13] - m[4] * m[1] * m[15] + m[4] * m[3] * m[13] + m[12] * m[1] * m[7] - m[12] * m[3] * m[5];
    inv[14] = -m[0] * m[5] * m[14] + m[0] * m[6] * m[13] + m[4] * m[1] * m[14] - m[4] * m[2] * m[13] - m[12] * m[1] * m[6] + m[12] * m[2] * m[5];
    inv[3] = -m[1] * m[6] * m[11] + m[1] * m[7] * m[10] + m[5] * m[2] * m[11] - m[5] * m[3] * m[10] - m[9] * m[2] * m[7] + m[9] * m[3] * m[6];
    inv[7] = m[0] * m[6] * m[11] - m[0] * m[7] * m[10] - m[4] * m[2] * m[11] + m[4] * m[3] * m[10] + m[8] * m[2] * m[7] - m[8] * m[3] * m[6];
    inv[11] = -m[0] * m[5] * m[11] + m[0] * m[7] * m[9] + m[4] * m[1] * m[11] - m[4] * m[3] * m[9] - m[8] * m[1] * m[7] + m[8] * m[3] * m[5];
    inv[15] = m[0] * m[5] * m[10] - m[0] * m[6] * m[9] - m[4] * m[1] * m[10] + m[4] * m[2] * m[9] + m[8] * m[1] * m[6] - m[8] * m[2] * m[5];
    var det = m[0] * inv[0] + m[1] * inv[4] + m[2] * inv[8] + m[3] * inv[12];
    if (det === 0 || !isFin(det)) return null;
    det = 1 / det;
    for (i = 0; i < 16; i++) inv[i] *= det;
    return inv;
  }
  var NAMES = ['m11', 'm12', 'm13', 'm14', 'm21', 'm22', 'm23', 'm24', 'm31', 'm32', 'm33', 'm34', 'm41', 'm42', 'm43', 'm44'];
  var ALIAS = { a: 0, b: 1, c: 4, d: 5, e: 12, f: 13 };
  function fromList(v) {
    if (v.length === 6) return { m: [v[0], v[1], 0, 0, v[2], v[3], 0, 0, 0, 0, 1, 0, v[4], v[5], 0, 1], is2D: true };
    if (v.length === 16) return { m: Array.prototype.slice.call(v).map(Number), is2D: false };
    throw new TypeError("Failed to construct 'DOMMatrix': The sequence must contain 6 or 16 elements");
  }
  function parseTransformList(s) {
    s = String(s).trim();
    var m = IDM(), is2D = true;
    if (s === '' || s === 'none') return { m: m, is2D: true };
    var re = /([a-zA-Z0-9]+)\s*\(([^)]*)\)/g, mt, used = 0;
    while ((mt = re.exec(s))) {
      used += mt[0].length;
      var fn = mt[1].toLowerCase();
      var args = mt[2].split(',').map(function (x) { x = x.trim(); var u = 1; if (/deg$/.test(x)) x = x.slice(0, -3); else if (/rad$/.test(x)) { x = x.slice(0, -3); u = 180 / Math.PI; } else if (/turn$/.test(x)) { x = x.slice(0, -4); u = 360; } else if (/grad$/.test(x)) { x = x.slice(0, -4); u = 0.9; } else if (/px$/.test(x)) x = x.slice(0, -2); return parseFloat(x) * u; });
      var t = IDM();
      switch (fn) {
        case 'matrix': if (args.length !== 6) throw dex('Failed to parse the transform', 'SyntaxError'); t = fromList(args).m; break;
        case 'matrix3d': if (args.length !== 16) throw dex('Failed to parse the transform', 'SyntaxError'); t = args; is2D = false; break;
        case 'translate': t[12] = args[0]; t[13] = args[1] || 0; break;
        case 'translatex': t[12] = args[0]; break;
        case 'translatey': t[13] = args[0]; break;
        case 'translatez': t[14] = args[0]; is2D = false; break;
        case 'translate3d': t[12] = args[0]; t[13] = args[1]; t[14] = args[2]; is2D = false; break;
        case 'scale': t[0] = args[0]; t[5] = args.length > 1 ? args[1] : args[0]; break;
        case 'scalex': t[0] = args[0]; break;
        case 'scaley': t[5] = args[0]; break;
        case 'scalez': t[10] = args[0]; is2D = false; break;
        case 'scale3d': t[0] = args[0]; t[5] = args[1]; t[10] = args[2]; is2D = false; break;
        case 'rotate': case 'rotatez': { var r = args[0] * Math.PI / 180; t[0] = Math.cos(r); t[1] = Math.sin(r); t[4] = -Math.sin(r); t[5] = Math.cos(r); break; }
        case 'skewx': t[4] = Math.tan(args[0] * Math.PI / 180); break;
        case 'skewy': t[1] = Math.tan(args[0] * Math.PI / 180); break;
        case 'skew': t[4] = Math.tan(args[0] * Math.PI / 180); t[1] = Math.tan((args[1] || 0) * Math.PI / 180); break;
        case 'perspective': t[11] = args[0] ? -1 / args[0] : 0; is2D = false; break;
        default: throw dex('Failed to parse the transform', 'SyntaxError');
      }
      m = mmul(m, t);
    }
    if (used === 0) throw dex('Failed to parse the transform', 'SyntaxError');
    return { m: m, is2D: is2D };
  }
  function DOMMatrixReadOnly(init) {
    if (!(this instanceof DOMMatrixReadOnly)) throw new TypeError("Failed to construct 'DOMMatrixReadOnly': Please use the 'new' operator");
    var st = { m: IDM(), is2D: true };
    if (init !== undefined && init !== null) {
      if (typeof init === 'string') st = parseTransformList(init);
      else if (typeof init === 'object' && typeof init.length === 'number') st = fromList(init);
      else throw new TypeError("Failed to construct 'DOMMatrix': The provided value is not of type '(DOMString or sequence<unrestricted double>)'");
    }
    Object.defineProperty(this, '_st', { value: st, writable: true });
  }
  var RP = DOMMatrixReadOnly.prototype;
  NAMES.forEach(function (n, i) { getter(RP, n, function () { return this._st.m[i]; }, undefined); });
  Object.keys(ALIAS).forEach(function (n) { getter(RP, n, function () { return this._st.m[ALIAS[n]]; }, undefined); });
  getter(RP, 'is2D', function () { return this._st.is2D; });
  getter(RP, 'isIdentity', function () { var m = this._st.m, id = IDM(); for (var i = 0; i < 16; i++) if (m[i] !== id[i]) return false; return true; });
  function mk(Ctor, m, is2D) { var r = new Ctor(); r._st.m = m; r._st.is2D = is2D; return r; }
  function mkOf(self, m, is2D) { return mk(global.DOMMatrix, m, is2D); }
  function transMat(x, y, z) { var t = IDM(); t[12] = x; t[13] = y; t[14] = z; return t; }
  function scaleMat(x, y, z, ox, oy, oz) { var t = IDM(); t[0] = x; t[5] = y; t[10] = z; return mmul(mmul(transMat(ox, oy, oz), t), transMat(-ox, -oy, -oz)); }
  function rotMat(rx, ry, rz) {
    var m = IDM();
    if (rz) { var c = Math.cos(rz * Math.PI / 180), s = Math.sin(rz * Math.PI / 180); m = mmul(m, [c, s, 0, 0, -s, c, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]); }
    if (ry) { var c2 = Math.cos(ry * Math.PI / 180), s2 = Math.sin(ry * Math.PI / 180); m = mmul(m, [c2, 0, -s2, 0, 0, 1, 0, 0, s2, 0, c2, 0, 0, 0, 0, 1]); }
    if (rx) { var c3 = Math.cos(rx * Math.PI / 180), s3 = Math.sin(rx * Math.PI / 180); m = mmul(m, [1, 0, 0, 0, 0, c3, s3, 0, 0, -s3, c3, 0, 0, 0, 0, 1]); }
    return m;
  }
  function axisMat(x, y, z, deg) {
    var l = Math.hypot(x, y, z); if (l === 0) return IDM(); x /= l; y /= l; z /= l;
    var a = deg * Math.PI / 180, c = Math.cos(a), s = Math.sin(a), t = 1 - c;
    return [t * x * x + c, t * x * y + s * z, t * x * z - s * y, 0, t * x * y - s * z, t * y * y + c, t * y * z + s * x, 0, t * x * z + s * y, t * y * z - s * x, t * z * z + c, 0, 0, 0, 0, 1];
  }
  function d0(v, d) { return v === undefined ? d : +v; }
  function ops(self) {
    return {
      translate: function (x, y, z) { return transMat(d0(x, 0), d0(y, 0), d0(z, 0)); },
      scale: function (sx, sy, sz, ox, oy, oz) { sx = d0(sx, 1); return scaleMat(sx, d0(sy, sx), d0(sz, 1), d0(ox, 0), d0(oy, 0), d0(oz, 0)); },
      scale3d: function (s, ox, oy, oz) { s = d0(s, 1); return scaleMat(s, s, s, d0(ox, 0), d0(oy, 0), d0(oz, 0)); },
      scaleNonUniform: function (sx, sy) { return scaleMat(d0(sx, 1), d0(sy, 1), 1, 0, 0, 0); },
      rotate: function (rx, ry, rz) { if (ry === undefined && rz === undefined) { rz = rx; rx = 0; ry = 0; } return rotMat(d0(rx, 0), d0(ry, 0), d0(rz, 0)); },
      rotateAxisAngle: function (x, y, z, a) { return axisMat(d0(x, 0), d0(y, 0), d0(z, 0), d0(a, 0)); },
      rotateFromVector: function (x, y) { x = d0(x, 0); y = d0(y, 0); return (x === 0 && y === 0) ? IDM() : rotMat(0, 0, Math.atan2(y, x) * 180 / Math.PI); },
      skewX: function (d) { var t = IDM(); t[4] = Math.tan(d0(d, 0) * Math.PI / 180); return t; },
      skewY: function (d) { var t = IDM(); t[1] = Math.tan(d0(d, 0) * Math.PI / 180); return t; }
    };
  }
  var OPS = ops();
  var needs3D = { translate: function (a) { return a[2] !== undefined && +a[2] !== 0; }, scale: function (a) { return (a[2] !== undefined && +a[2] !== 1) || (a[5] !== undefined && +a[5] !== 0); }, scale3d: function () { return true; }, rotate: function (a) { return a.length >= 2 && (+a[0] !== 0 || +a[1] !== 0); }, rotateAxisAngle: function (a) { return +a[0] !== 0 || +a[1] !== 0; }, rotateFromVector: function () { return false }, skewX: function () { return false; }, skewY: function () { return false; }, scaleNonUniform: function () { return false; } };
  Object.keys(OPS).forEach(function (n) {
    def(RP, n, function () {
      var t = OPS[n].apply(null, arguments);
      var is2D = this._st.is2D && !needs3D[n](arguments);
      return mkOf(this, mmul(this._st.m, t), is2D);
    });
  });
  function toMatrixInit(o) {
    if (o instanceof DOMMatrixReadOnly) return o._st;
    o = o || {};
    var m = IDM(), is2D = true;
    var has3d = false;
    NAMES.forEach(function (n, i) { if (o[n] !== undefined) { m[i] = +o[n]; if (i !== 0 && i !== 1 && i !== 4 && i !== 5 && i !== 12 && i !== 13 && m[i] !== IDM()[i]) has3d = true; } });
    Object.keys(ALIAS).forEach(function (n) { if (o[n] !== undefined && o['m' + (['11', '12', '21', '22', '41', '42'][['a', 'b', 'c', 'd', 'e', 'f'].indexOf(n)])] === undefined) m[ALIAS[n]] = +o[n]; });
    if (o.is2D === false || has3d) is2D = false;
    return { m: m, is2D: is2D };
  }
  def(RP, 'multiply', function (o) { var b = toMatrixInit(o); return mkOf(this, mmul(this._st.m, b.m), this._st.is2D && b.is2D); });
  def(RP, 'flipX', function () { return mkOf(this, mmul(this._st.m, [-1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]), this._st.is2D); });
  def(RP, 'flipY', function () { return mkOf(this, mmul(this._st.m, [1, 0, 0, 0, 0, -1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1]), this._st.is2D); });
  def(RP, 'inverse', function () {
    var i = minv(this._st.m);
    if (!i) { var n = []; for (var k = 0; k < 16; k++) n.push(NaN); return mkOf(this, n, this._st.is2D); }
    return mkOf(this, i, this._st.is2D);
  });
  def(RP, 'transformPoint', function (p) {
    p = p || {}; var x = +(p.x || 0), y = +(p.y || 0), z = +(p.z || 0), w = p.w === undefined ? 1 : +p.w, m = this._st.m;
    return new global.DOMPoint(m[0] * x + m[4] * y + m[8] * z + m[12] * w, m[1] * x + m[5] * y + m[9] * z + m[13] * w, m[2] * x + m[6] * y + m[10] * z + m[14] * w, m[3] * x + m[7] * y + m[11] * z + m[15] * w);
  });
  def(RP, 'toFloat32Array', function () { return new Float32Array(this._st.m); });
  def(RP, 'toFloat64Array', function () { return new Float64Array(this._st.m); });
  def(RP, 'toJSON', function () { var o = {}; Object.keys(ALIAS).forEach(function (n) { o[n] = this[n]; }, this); NAMES.forEach(function (n) { o[n] = this[n]; }, this); o.is2D = this.is2D; o.isIdentity = this.isIdentity; return o; });
  def(RP, 'toString', function () {
    var m = this._st.m;
    for (var i = 0; i < 16; i++) if (!isFin(m[i])) throw dex('Failed to execute \'toString\' on \'DOMMatrix\': cannot serialize a non-finite matrix', 'InvalidStateError');
    if (this._st.is2D) return 'matrix(' + [m[0], m[1], m[4], m[5], m[12], m[13]].join(', ') + ')';
    return 'matrix3d(' + m.join(', ') + ')';
  });
  RP.constructor = DOMMatrixReadOnly;
  tag(DOMMatrixReadOnly, 'DOMMatrixReadOnly');
  DOMMatrixReadOnly.fromMatrix = function (o) { var s = toMatrixInit(o); return mk(DOMMatrixReadOnly, s.m.slice(), s.is2D); };
  DOMMatrixReadOnly.fromFloat32Array = function (a) { var s = fromList(a); return mk(DOMMatrixReadOnly, s.m, s.is2D); };
  DOMMatrixReadOnly.fromFloat64Array = DOMMatrixReadOnly.fromFloat32Array;

  function DOMMatrix(init) {
    if (!(this instanceof DOMMatrix)) throw new TypeError("Failed to construct 'DOMMatrix': Please use the 'new' operator");
    DOMMatrixReadOnly.call(this, init);
  }
  DOMMatrix.prototype = Object.create(RP, { constructor: { value: DOMMatrix, writable: true, configurable: true } });
  Object.setPrototypeOf(DOMMatrix, DOMMatrixReadOnly);
  var MP = DOMMatrix.prototype;
  NAMES.forEach(function (n, i) {
    getter(MP, n, function () { return this._st.m[i]; }, function (v) { v = +v; this._st.m[i] = v; if (v !== IDM()[i] && i !== 0 && i !== 1 && i !== 4 && i !== 5 && i !== 12 && i !== 13) this._st.is2D = false; });
  });
  Object.keys(ALIAS).forEach(function (n) { getter(MP, n, function () { return this._st.m[ALIAS[n]]; }, function (v) { this._st.m[ALIAS[n]] = +v; }); });
  getter(MP, 'is2D', function () { return this._st.is2D; }, function (v) { if (!v) this._st.is2D = false; });
  function self(name, fn) { def(MP, name, fn); }
  Object.keys(OPS).forEach(function (n) {
    self(n + 'Self', function () { var t = OPS[n].apply(null, arguments); if (needs3D[n](arguments)) this._st.is2D = false; this._st.m = mmul(this._st.m, t); return this; });
  });
  self('multiplySelf', function (o) { var b = toMatrixInit(o); this._st.m = mmul(this._st.m, b.m); this._st.is2D = this._st.is2D && b.is2D; return this; });
  self('preMultiplySelf', function (o) { var b = toMatrixInit(o); this._st.m = mmul(b.m, this._st.m); this._st.is2D = this._st.is2D && b.is2D; return this; });
  self('invertSelf', function () { var i = minv(this._st.m); if (i) this._st.m = i; else { this._st.m = this._st.m.map(function () { return NaN; }); this._st.is2D = false; } return this; });
  self('setMatrixValue', function (s) { this._st = parseTransformList(s); return this; });
  DOMMatrix.fromMatrix = function (o) { var s = toMatrixInit(o); return mk(DOMMatrix, s.m.slice(), s.is2D); };
  DOMMatrix.fromFloat32Array = function (a) { var s = fromList(a); return mk(DOMMatrix, s.m, s.is2D); };
  DOMMatrix.fromFloat64Array = DOMMatrix.fromFloat32Array;
  tag(DOMMatrix, 'DOMMatrix');
  global.DOMMatrixReadOnly = DOMMatrixReadOnly; global.DOMMatrix = DOMMatrix; global.WebKitCSSMatrix = DOMMatrix;

  function DOMPointReadOnly(x, y, z, w) {
    Object.defineProperty(this, '_p', { value: { x: x === undefined ? 0 : +x, y: y === undefined ? 0 : +y, z: z === undefined ? 0 : +z, w: w === undefined ? 1 : +w }, writable: true });
  }
  ['x', 'y', 'z', 'w'].forEach(function (k) { getter(DOMPointReadOnly.prototype, k, function () { return this._p[k]; }, undefined); });
  def(DOMPointReadOnly.prototype, 'matrixTransform', function (m) { return m ? DOMPoint.fromPoint(DOMMatrixReadOnly.prototype.transformPoint.call(m instanceof DOMMatrixReadOnly ? m : new DOMMatrix(m), this)) : new DOMPoint(this.x, this.y, this.z, this.w); });
  def(DOMPointReadOnly.prototype, 'toJSON', function () { return { x: this.x, y: this.y, z: this.z, w: this.w }; });
  DOMPointReadOnly.fromPoint = function (p) { p = p || {}; return new DOMPointReadOnly(p.x, p.y, p.z, p.w); };
  tag(DOMPointReadOnly, 'DOMPointReadOnly');
  function DOMPoint(x, y, z, w) { if (!(this instanceof DOMPoint)) throw new TypeError("Failed to construct 'DOMPoint': Please use the 'new' operator"); DOMPointReadOnly.call(this, x, y, z, w); }
  DOMPoint.prototype = Object.create(DOMPointReadOnly.prototype, { constructor: { value: DOMPoint, writable: true, configurable: true } });
  Object.setPrototypeOf(DOMPoint, DOMPointReadOnly);
  ['x', 'y', 'z', 'w'].forEach(function (k) { getter(DOMPoint.prototype, k, function () { return this._p[k]; }, function (v) { this._p[k] = +v; }); });
  DOMPoint.fromPoint = function (p) { p = p || {}; return new DOMPoint(p.x, p.y, p.z, p.w); };
  tag(DOMPoint, 'DOMPoint');
  global.DOMPointReadOnly = DOMPointReadOnly; global.DOMPoint = DOMPoint;

  /* --------------------------------------------------------------- path core */
  // A path is {a: flat command array, dx,dy: current point (device/canvas
  // space), sx,sy: subpath start, has}. Commands: 0 M x y, 1 L x y, 2 Q 4 nums,
  // 3 C 6 nums, 4 Z. `m` (6-array or null) maps user space to the stored space.
  function newPath() { return { a: [], dx: 0, dy: 0, sx: 0, sy: 0, has: false }; }
  function tx(m, x, y) { return m ? [m[0] * x + m[2] * y + m[4], m[1] * x + m[3] * y + m[5]] : [x, y]; }
  function userOf(p, m) { // current point mapped back into user space
    if (!m) return [p.dx, p.dy];
    var det = m[0] * m[3] - m[1] * m[2];
    if (!det || !isFin(det)) return null;
    var x = p.dx - m[4], y = p.dy - m[5];
    return [(m[3] * x - m[2] * y) / det, (-m[1] * x + m[0] * y) / det];
  }
  function pMove(p, m, x, y) { var q = tx(m, x, y); p.a.push(0, q[0], q[1]); p.dx = p.sx = q[0]; p.dy = p.sy = q[1]; p.has = true; }
  function pEnsure(p, m, x, y) { if (!p.has) pMove(p, m, x, y); }
  function pLine(p, m, x, y) { if (!p.has) { pMove(p, m, x, y); return; } var q = tx(m, x, y); p.a.push(1, q[0], q[1]); p.dx = q[0]; p.dy = q[1]; }
  function pQuad(p, m, cx, cy, x, y) { pEnsure(p, m, cx, cy); var c = tx(m, cx, cy), q = tx(m, x, y); p.a.push(2, c[0], c[1], q[0], q[1]); p.dx = q[0]; p.dy = q[1]; }
  function pCubic(p, m, c1x, c1y, c2x, c2y, x, y) { pEnsure(p, m, c1x, c1y); var a = tx(m, c1x, c1y), b = tx(m, c2x, c2y), q = tx(m, x, y); p.a.push(3, a[0], a[1], b[0], b[1], q[0], q[1]); p.dx = q[0]; p.dy = q[1]; }
  function pClose(p) { if (!p.has) return; p.a.push(4); p.dx = p.sx; p.dy = p.sy; }
  function pRect(p, m, x, y, w, h) { pMove(p, m, x, y); pLine(p, m, x + w, y); pLine(p, m, x + w, y + h); pLine(p, m, x, y + h); pClose(p); pMove(p, m, x, y); }
  var KAPPA = 4 / 3 * Math.tan(Math.PI / 8);
  function pArcSeg(p, m, cx, cy, rx, ry, rot, a0, sweep) {
    var n = Math.max(1, Math.ceil(Math.abs(sweep) / (Math.PI / 2) - 1e-9)), step = sweep / n, t = 4 / 3 * Math.tan(step / 4);
    var sr = Math.sin(rot), cr = Math.cos(rot), a = a0;
    function pt(x, y) { x *= rx; y *= ry; return [cx + x * cr - y * sr, cy + x * sr + y * cr]; }
    for (var i = 0; i < n; i++) {
      var b = a + step, sa = Math.sin(a), ca = Math.cos(a), sb = Math.sin(b), cb = Math.cos(b);
      var c1 = pt(ca - t * sa, sa + t * ca), c2 = pt(cb + t * sb, sb - t * cb), e = pt(cb, sb);
      pCubic(p, m, c1[0], c1[1], c2[0], c2[1], e[0], e[1]);
      a = b;
    }
  }
  function pEllipse(p, m, cx, cy, rx, ry, rot, a0, a1, ccw) {
    var TWO = 2 * Math.PI, sweep;
    if (!ccw) { sweep = a1 - a0; if (sweep >= TWO) sweep = TWO; else if (sweep < 0) { sweep = sweep % TWO; if (sweep < 0) sweep += TWO; } }
    else { sweep = a1 - a0; if (sweep <= -TWO) sweep = -TWO; else if (sweep > 0) { sweep = sweep % TWO; if (sweep > 0) sweep -= TWO; } }
    var sr = Math.sin(rot), cr = Math.cos(rot);
    var sx = cx + rx * Math.cos(a0) * cr - ry * Math.sin(a0) * sr, sy = cy + rx * Math.cos(a0) * sr + ry * Math.sin(a0) * cr;
    if (p.has) pLine(p, m, sx, sy); else pMove(p, m, sx, sy);
    if (rx === 0 || ry === 0 || sweep === 0 && a1 === a0) { return; }
    pArcSeg(p, m, cx, cy, rx, ry, rot, a0, sweep);
  }
  function pArcTo(p, m, x1, y1, x2, y2, r) {
    if (!p.has) { pMove(p, m, x1, y1); return; }
    var u = userOf(p, m); if (!u) return;
    var x0 = u[0], y0 = u[1];
    if ((x0 === x1 && y0 === y1) || (x1 === x2 && y1 === y2) || r === 0) { pLine(p, m, x1, y1); return; }
    var v1x = x0 - x1, v1y = y0 - y1, v2x = x2 - x1, v2y = y2 - y1;
    var l1 = Math.hypot(v1x, v1y), l2 = Math.hypot(v2x, v2y);
    v1x /= l1; v1y /= l1; v2x /= l2; v2y /= l2;
    var cross = v1x * v2y - v1y * v2x, dot = v1x * v2x + v1y * v2y;
    if (Math.abs(cross) < 1e-12) { pLine(p, m, x1, y1); return; }
    var half = Math.acos(Math.max(-1, Math.min(1, dot))) / 2;
    var dist = r / Math.tan(half);
    var t0x = x1 + v1x * dist, t0y = y1 + v1y * dist, t1x = x1 + v2x * dist, t1y = y1 + v2y * dist;
    var bx = v1x + v2x, by = v1y + v2y, bl = Math.hypot(bx, by); bx /= bl; by /= bl;
    var cd = r / Math.sin(half), cx = x1 + bx * cd, cy = y1 + by * cd;
    var a0 = Math.atan2(t0y - cy, t0x - cx), a1 = Math.atan2(t1y - cy, t1x - cx);
    var ccw = (-v1x * v2y + v1y * v2x) < 0;
    pLine(p, m, t0x, t0y);
    var TWO = 2 * Math.PI, sweep = a1 - a0;
    if (!ccw) { sweep = sweep % TWO; if (sweep < 0) sweep += TWO; } else { sweep = sweep % TWO; if (sweep > 0) sweep -= TWO; }
    pArcSeg(p, m, cx, cy, r, r, 0, a0, sweep);
  }
  function radiiOf(r) {
    var list;
    if (r === undefined) list = [0];
    else if (Array.isArray(r) || (r && typeof r === 'object' && typeof r.length === 'number' && !('x' in r))) list = Array.prototype.slice.call(r);
    else list = [r];
    if (list.length < 1 || list.length > 4) throw new RangeError("Failed to execute 'roundRect' on 'CanvasRenderingContext2D': " + list.length + ' radii provided. Between one and four radii are necessary.');
    var out = list.map(function (v) {
      var x, y;
      if (v !== null && typeof v === 'object') { x = +(v.x === undefined ? 0 : v.x); y = +(v.y === undefined ? 0 : v.y); } else { x = y = +v; }
      return [x, y];
    });
    return out;
  }
  function pRoundRect(p, m, x, y, w, h, radii) {
    var rs = radiiOf(radii), i;
    for (i = 0; i < rs.length; i++) { if (!fin(rs[i][0], rs[i][1])) return; }
    for (i = 0; i < rs.length; i++) if (rs[i][0] < 0 || rs[i][1] < 0) throw new RangeError("Failed to execute 'roundRect' on 'CanvasRenderingContext2D': Radius value " + Math.min(rs[i][0], rs[i][1]) + ' is negative.');
    var tl, tr, br, bl;
    switch (rs.length) {
      case 1: tl = tr = br = bl = rs[0]; break;
      case 2: tl = br = rs[0]; tr = bl = rs[1]; break;
      case 3: tl = rs[0]; tr = bl = rs[1]; br = rs[2]; break;
      default: tl = rs[0]; tr = rs[1]; br = rs[2]; bl = rs[3];
    }
    if (!fin(x, y, w, h)) return;
    // normalise negative sizes
    var flipX = w < 0, flipY = h < 0;
    if (flipX) { x += w; w = -w; var t = tl; tl = tr; tr = t; t = bl; bl = br; br = t; }
    if (flipY) { y += h; h = -h; var t2 = tl; tl = bl; bl = t2; t2 = tr; tr = br; br = t2; }
    var f = 1, chk = function (sum, avail) { if (sum > avail && sum > 0) f = Math.min(f, avail / sum); };
    chk(tl[0] + tr[0], w); chk(bl[0] + br[0], w); chk(tl[1] + bl[1], h); chk(tr[1] + br[1], h);
    function sc(c) { return [c[0] * f, c[1] * f]; }
    tl = sc(tl); tr = sc(tr); br = sc(br); bl = sc(bl);
    pMove(p, m, x + tl[0], y);
    pLine(p, m, x + w - tr[0], y);
    if (tr[0] || tr[1]) pCubic(p, m, x + w - tr[0] + tr[0] * KAPPA, y, x + w, y + tr[1] - tr[1] * KAPPA, x + w, y + tr[1]);
    pLine(p, m, x + w, y + h - br[1]);
    if (br[0] || br[1]) pCubic(p, m, x + w, y + h - br[1] + br[1] * KAPPA, x + w - br[0] + br[0] * KAPPA, y + h, x + w - br[0], y + h);
    pLine(p, m, x + bl[0], y + h);
    if (bl[0] || bl[1]) pCubic(p, m, x + bl[0] - bl[0] * KAPPA, y + h, x, y + h - bl[1] + bl[1] * KAPPA, x, y + h - bl[1]);
    pLine(p, m, x, y + tl[1]);
    if (tl[0] || tl[1]) pCubic(p, m, x, y + tl[1] - tl[1] * KAPPA, x + tl[0] - tl[0] * KAPPA, y, x + tl[0], y);
    pClose(p);
    pMove(p, m, x, y);
  }
  // CanvasPath mixin: `pm()` returns [path, matrix].
  function installPathMethods(proto) {
    defs(proto, {
      closePath: function () { pClose(this._pp()[0]); },
      moveTo: function (x, y) { x = +x; y = +y; if (!fin(x, y)) return; var q = this._pp(); if (!q) return; pMove(q[0], q[1], x, y); },
      lineTo: function (x, y) { x = +x; y = +y; if (!fin(x, y)) return; var q = this._pp(); if (!q) return; pLine(q[0], q[1], x, y); },
      quadraticCurveTo: function (cx, cy, x, y) { cx = +cx; cy = +cy; x = +x; y = +y; if (!fin(cx, cy, x, y)) return; var q = this._pp(); if (!q) return; pQuad(q[0], q[1], cx, cy, x, y); },
      bezierCurveTo: function (a, b, c, d, e, f) { a = +a; b = +b; c = +c; d = +d; e = +e; f = +f; if (!fin(a, b, c, d, e, f)) return; var q = this._pp(); if (!q) return; pCubic(q[0], q[1], a, b, c, d, e, f); },
      arcTo: function (x1, y1, x2, y2, r) {
        x1 = +x1; y1 = +y1; x2 = +x2; y2 = +y2; r = +r; if (!fin(x1, y1, x2, y2, r)) return;
        if (r < 0) throw dex("Failed to execute 'arcTo' on '" + this._cls + "': The radius provided (" + r + ') is negative.', 'IndexSizeError');
        var q = this._pp(); if (!q) return; pArcTo(q[0], q[1], x1, y1, x2, y2, r);
      },
      arc: function (x, y, r, a0, a1, ccw) {
        x = +x; y = +y; r = +r; a0 = +a0; a1 = +a1; if (!fin(x, y, r, a0, a1)) return;
        if (r < 0) throw dex("Failed to execute 'arc' on '" + this._cls + "': The radius provided (" + r + ') is negative.', 'IndexSizeError');
        var q = this._pp(); if (!q) return; pEllipse(q[0], q[1], x, y, r, r, 0, a0, a1, !!ccw);
      },
      ellipse: function (x, y, rx, ry, rot, a0, a1, ccw) {
        x = +x; y = +y; rx = +rx; ry = +ry; rot = +rot; a0 = +a0; a1 = +a1; if (!fin(x, y, rx, ry, rot, a0, a1)) return;
        if (rx < 0) throw dex("Failed to execute 'ellipse' on '" + this._cls + "': The major-axis radius provided (" + rx + ') is negative.', 'IndexSizeError');
        if (ry < 0) throw dex("Failed to execute 'ellipse' on '" + this._cls + "': The minor-axis radius provided (" + ry + ') is negative.', 'IndexSizeError');
        var q = this._pp(); if (!q) return; pEllipse(q[0], q[1], x, y, rx, ry, rot, a0, a1, !!ccw);
      },
      rect: function (x, y, w, h) { x = +x; y = +y; w = +w; h = +h; if (!fin(x, y, w, h)) return; var q = this._pp(); if (!q) return; pRect(q[0], q[1], x, y, w, h); },
      roundRect: function (x, y, w, h, r) { x = +x; y = +y; w = +w; h = +h; var q = this._pp(); if (!q) return; pRoundRect(q[0], q[1], x, y, w, h, r); }
    });
  }

  /* ------------------------------------------------------------------ Path2D */
  function Path2D(init) {
    if (!(this instanceof Path2D)) throw new TypeError("Failed to construct 'Path2D': Please use the 'new' operator");
    var p = newPath();
    Object.defineProperty(this, '_p', { value: p, writable: true });
    if (init instanceof Path2D) { p.a = init._p.a.slice(); p.dx = init._p.dx; p.dy = init._p.dy; p.sx = init._p.sx; p.sy = init._p.sy; p.has = init._p.has; }
    else if (init !== undefined && init !== null) {
      var arr = sw.cvParseSVG(String(init));
      p.a = arr;
      // recompute current point / start
      var i = 0, a = arr;
      while (i < a.length) {
        var op = a[i++];
        if (op === 0) { p.sx = p.dx = a[i]; p.sy = p.dy = a[i + 1]; i += 2; p.has = true; }
        else if (op === 1) { p.dx = a[i]; p.dy = a[i + 1]; i += 2; }
        else if (op === 2) { p.dx = a[i + 2]; p.dy = a[i + 3]; i += 4; }
        else if (op === 3) { p.dx = a[i + 4]; p.dy = a[i + 5]; i += 6; }
        else if (op === 4) { p.dx = p.sx; p.dy = p.sy; }
      }
    }
  }
  Path2D.prototype._pp = function () { return [this._p, null]; };
  Path2D.prototype._cls = 'Path2D';
  Object.defineProperty(Path2D.prototype, '_pp', { enumerable: false });
  Object.defineProperty(Path2D.prototype, '_cls', { enumerable: false });
  installPathMethods(Path2D.prototype);
  def(Path2D.prototype, 'addPath', function (path, m) {
    if (!(path instanceof Path2D)) throw new TypeError("Failed to execute 'addPath' on 'Path2D': parameter 1 is not of type 'Path2D'.");
    var mm = null;
    if (m !== undefined && m !== null) { var s = toMatrixInit(m); var a = s.m; mm = [a[0], a[1], a[4], a[5], a[12], a[13]]; for (var i = 0; i < 6; i++) if (!isFin(mm[i])) return; }
    var src = path._p.a, out = this._p, i2 = 0, n;
    while (i2 < src.length) {
      var op = src[i2++]; n = op === 0 || op === 1 ? 2 : op === 2 ? 4 : op === 3 ? 6 : 0;
      out.a.push(op);
      for (var k = 0; k < n; k += 2) { var q = tx(mm, src[i2 + k], src[i2 + k + 1]); out.a.push(q[0], q[1]); }
      i2 += n;
    }
    if (path._p.has) { var c = tx(mm, path._p.dx, path._p.dy), st = tx(mm, path._p.sx, path._p.sy); out.dx = c[0]; out.dy = c[1]; out.sx = st[0]; out.sy = st[1]; out.has = true; }
  });
  tag(Path2D, 'Path2D');
  global.Path2D = Path2D;

  /* ----------------------------------------------- gradients, patterns, data */
  function CanvasGradient() { illegal(); }
  def(CanvasGradient.prototype, 'addColorStop', function (off, color) {
    off = +off;
    if (!isFin(off) || off < 0 || off > 1) throw dex("Failed to execute 'addColorStop' on 'CanvasGradient': The provided value (" + off + ") is outside the range (0.0, 1.0).", 'IndexSizeError');
    if (!sw.cvGradStop(this._h, off, String(color))) throw dex("Failed to execute 'addColorStop' on 'CanvasGradient': The value provided ('" + color + "') could not be parsed as a color.", 'SyntaxError');
    this._v++;
  });
  tag(CanvasGradient, 'CanvasGradient');
  function mkGradient(h) { var g = Object.create(CanvasGradient.prototype); Object.defineProperty(g, '_h', { value: h }); Object.defineProperty(g, '_v', { value: 0, writable: true }); track(g, h); return g; }
  function CanvasPattern() { illegal(); }
  def(CanvasPattern.prototype, 'setTransform', function (m) {
    var s = m === undefined ? { m: IDM() } : toMatrixInit(m), a = s.m;
    var v = [a[0], a[1], a[4], a[5], a[12], a[13]];
    for (var i = 0; i < 6; i++) if (!isFin(v[i])) return;
    sw.cvPatternMatrix(this._h, v[0], v[1], v[2], v[3], v[4], v[5]); this._v++;
  });
  tag(CanvasPattern, 'CanvasPattern');

  function ImageData(a, b, c, d) {
    if (!(this instanceof ImageData)) throw new TypeError("Failed to construct 'ImageData': Please use the 'new' operator");
    var data, w, h, settings;
    if (arguments.length < 2) throw new TypeError("Failed to construct 'ImageData': 2 arguments required, but only " + arguments.length + ' present.');
    if (typeof a === 'object' && a !== null && ArrayBuffer.isView(a)) {
      if (!(a instanceof Uint8ClampedArray)) throw new TypeError("Failed to construct 'ImageData': The provided value is not of type 'Uint8ClampedArray'.");
      w = b >>> 0; if (c !== undefined && typeof c === 'object' && c !== null) { settings = c; h = undefined; } else { h = c === undefined ? undefined : c >>> 0; settings = d; }
      if (w === 0) throw dex("Failed to construct 'ImageData': The source width is zero or not a number.", 'IndexSizeError');
      if (a.length === 0 || a.length % 4) throw dex("Failed to construct 'ImageData': The input data length is not a multiple of 4.", 'InvalidStateError');
      var pix = a.length / 4;
      if (pix % w) throw dex("Failed to construct 'ImageData': The input data length is not a multiple of (4 * width).", 'IndexSizeError');
      var hh = pix / w;
      if (h !== undefined && h !== hh) throw dex("Failed to construct 'ImageData': The input data length is not equal to (4 * width * height).", 'IndexSizeError');
      h = hh; data = a;
    } else {
      w = a >>> 0; h = b >>> 0; settings = c;
      if (!isFin(+a) || !isFin(+b) || +a !== +a) throw new TypeError("Failed to construct 'ImageData': The provided value is non-finite.");
      if (w === 0) throw dex("Failed to construct 'ImageData': The source width is zero or not a number.", 'IndexSizeError');
      if (h === 0) throw dex("Failed to construct 'ImageData': The source height is zero or not a number.", 'IndexSizeError');
      if (w * h * 4 > 536870912) throw dex("Failed to construct 'ImageData': The requested image size exceeds the supported range.", 'IndexSizeError');
      data = new Uint8ClampedArray(w * h * 4);
    }
    Object.defineProperty(this, 'width', { value: w, enumerable: true });
    Object.defineProperty(this, 'height', { value: h, enumerable: true });
    Object.defineProperty(this, 'data', { value: data, enumerable: true });
    Object.defineProperty(this, 'colorSpace', { value: (settings && settings.colorSpace) || 'srgb', enumerable: true });
    Object.defineProperty(this, 'pixelFormat', { value: 'rgba-unorm8', enumerable: true });
  }
  tag(ImageData, 'ImageData');
  global.ImageData = ImageData;

  function TextMetrics() { illegal(); }
  var TMK = ['width', 'actualBoundingBoxLeft', 'actualBoundingBoxRight', 'actualBoundingBoxAscent', 'actualBoundingBoxDescent', 'fontBoundingBoxAscent', 'fontBoundingBoxDescent', 'emHeightAscent', 'emHeightDescent', 'hangingBaseline', 'alphabeticBaseline', 'ideographicBaseline'];
  TMK.forEach(function (k, i) { getter(TextMetrics.prototype, k, function () { return this._v[i]; }, undefined); });
  tag(TextMetrics, 'TextMetrics');
  global.TextMetrics = TextMetrics;
  global.CanvasGradient = CanvasGradient; global.CanvasPattern = CanvasPattern;

  var binChunk = 0x2000;
  // binary data crosses the host boundary as latin-1 strings (one char per byte)
  function toBin(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i += binChunk) s += String.fromCharCode.apply(null, u8.subarray(i, Math.min(u8.length, i + binChunk)));
    return s;
  }
  function fromBin(str) {
    var n = str.length, u = new Uint8ClampedArray(n);
    for (var i = 0; i < n; i++) u[i] = str.charCodeAt(i);
    return u;
  }
  var toB64 = toBin, fromB64 = fromBin;

  /* ------------------------------------------------------------- colors/fonts */
  var colorCache = new Map();
  function parseColor(str) {
    str = String(str);
    var key = str.trim();
    if (colorCache.has(key)) return colorCache.get(key);
    var r = sw.cvColor(key) || null;
    if (colorCache.size > 800) colorCache.clear();
    colorCache.set(key, r);
    return r;
  }
  var LEN_UNITS = { px: 1, em: 16, rem: 16, pt: 4 / 3, pc: 16, in: 96, cm: 96 / 2.54, mm: 96 / 25.4, q: 96 / 101.6, ex: 8, ch: 8 };
  function parseLength(v, fontPx) {
    var m = /^\s*([+-]?(?:\d+\.?\d*|\.\d+)(?:e[+-]?\d+)?)\s*(px|em|rem|pt|pc|in|cm|mm|q|ex|ch)?\s*$/i.exec(String(v));
    if (!m) return null;
    var n = parseFloat(m[1]), u = (m[2] || '').toLowerCase();
    if (!u) return n === 0 ? 0 : null;
    return n * (u === 'em' || u === 'ex' || u === 'ch' ? (fontPx || 10) * (u === 'em' ? 1 : 0.5) : LEN_UNITS[u]);
  }
  var OPS_OK = {};
  ['source-over', 'source-in', 'source-out', 'source-atop', 'destination-over', 'destination-in', 'destination-out', 'destination-atop', 'lighter', 'copy', 'xor', 'multiply', 'screen', 'overlay', 'darken', 'lighten', 'color-dodge', 'color-burn', 'hard-light', 'soft-light', 'difference', 'exclusion', 'hue', 'saturation', 'color', 'luminosity'].forEach(function (n) { OPS_OK[n] = true; });
  var CAPS = ['butt', 'round', 'square'], JOINS = ['miter', 'round', 'bevel'];

  /* ------------------------------------------------------------- image sources */
  var SLOT = '__swcv';
  function idOfEl(el) { return global.__swIdOf(el); }
  // returns [kind, ref] or null when the source has nothing to draw
  function srcOf(x, fn) {
    if (x && x._isBitmap) {
      if (x._closed) throw dex("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': The image source is detached.", 'InvalidStateError');
      return ['img', x._h];
    }
    if (x instanceof global.HTMLCanvasElement) {
      if (x.width === 0 || x.height === 0) throw dex("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': The image argument is a canvas element with a width or height of 0.", 'InvalidStateError');
      var st = x[SLOT];
      if (!st) return null;
      if (st.ctx && st.ctx._id) return ['ctx', st.ctx._id];
      return ['node', idOfEl(x)];
    }
    if (global.OffscreenCanvas && x instanceof global.OffscreenCanvas) {
      if (x.width === 0 || x.height === 0) throw dex("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': The image argument is a canvas element with a width or height of 0.", 'InvalidStateError');
      return x._ctx && x._ctx._id ? ['ctx', x._ctx._id] : null;
    }
    if (x instanceof global.HTMLImageElement) {
      var u = x.currentSrc || x.src;
      return u ? ['url', u] : null;
    }
    if (global.HTMLVideoElement && x instanceof global.HTMLVideoElement) return ['node', idOfEl(x)];
    throw new TypeError("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': The provided value is not of type '(CSSImageValue or HTMLImageElement or SVGImageElement or HTMLVideoElement or HTMLCanvasElement or ImageBitmap or OffscreenCanvas or VideoFrame)'.");
  }
  function srcSize(x) {
    if (x && x._isBitmap) return [x.width, x.height];
    if (x instanceof global.HTMLImageElement) return [x.naturalWidth, x.naturalHeight];
    if (x && x.videoWidth !== undefined) return [x.videoWidth, x.videoHeight];
    return [x.width, x.height];
  }

  /* ------------------------------------------------------------------- context */
  function freshState() {
    return {
      ctm: [1, 0, 0, 1, 0, 0], fill: '#000000', fillObj: null, fillSync: -1, stroke: '#000000', strokeObj: null, strokeSync: -1,
      lineWidth: 1, lineCap: 'butt', lineJoin: 'miter', miterLimit: 10, dash: [], dashOffset: 0, alpha: 1, gco: 'source-over',
      smooth: true, quality: 'low', shadowColor: 'rgba(0, 0, 0, 0)', srgba: [0, 0, 0, 0], blur: 0, sdx: 0, sdy: 0, filter: 'none',
      font: '10px sans-serif', fontPx: 10, align: 'start', baseline: 'alphabetic', dir: 'ltr', ls: '0px', ws: '0px', kern: 'auto', stretch: 'normal', caps: 'normal', rendering: 'auto'
    };
  }
  function copyState(s) { var c = Object.assign({}, s); c.ctm = s.ctm.slice(); c.dash = s.dash.slice(); c.srgba = s.srgba.slice(); return c; }

  function CanvasRenderingContext2D() { illegal(); }
  var CP = CanvasRenderingContext2D.prototype;
  CP._cls = 'CanvasRenderingContext2D';
  Object.defineProperty(CP, '_cls', { enumerable: false });
  Object.defineProperty(CP, '_pp', { enumerable: false, value: function () { var m = this._s.ctm; return [this._path, (m[0] === 1 && m[1] === 0 && m[2] === 0 && m[3] === 1 && m[4] === 0 && m[5] === 0) ? null : m]; } });
  installPathMethods(CP);
  tag(CanvasRenderingContext2D, 'CanvasRenderingContext2D');

  function makeCtx2D(owner, attrs) {
    var d = ownerDims(owner);
    var id = sw.cvNew(d[0], d[1], !(attrs && attrs.alpha === false));
    var ctx = Object.create(CP);
    var cs = (attrs && attrs.colorSpace === 'display-p3') ? 'display-p3' : 'srgb';
    Object.defineProperty(ctx, '_id', { value: id, writable: true });
    Object.defineProperty(ctx, '_s', { value: freshState(), writable: true });
    Object.defineProperty(ctx, '_stack', { value: [], writable: true });
    Object.defineProperty(ctx, '_path', { value: newPath(), writable: true });
    Object.defineProperty(ctx, '_owner', { value: owner });
    Object.defineProperty(ctx, '_attrs', { value: { alpha: !(attrs && attrs.alpha === false), colorSpace: cs, desynchronized: !!(attrs && attrs.desynchronized), willReadFrequently: !!(attrs && attrs.willReadFrequently) } });
    if (id) { track(ctx, id); if (owner instanceof global.HTMLCanvasElement) sw.cvBind(id, idOfEl(owner)); }
    ctx._resize = function (w, h) {
      this._s = freshState(); this._stack.length = 0; this._path = newPath();
      if (this._id) { if (!sw.cvResize(this._id, w, h)) { /* too large: stays blank */ } }
      else { this._id = sw.cvNew(w, h, this._attrs.alpha); if (this._id && owner instanceof global.HTMLCanvasElement) sw.cvBind(this._id, idOfEl(owner)); }
    };
    return ctx;
  }
  function ownerDims(o) { return [o.width, o.height]; }

  function pf(c) { var s = c._s; if (s.fillObj && s.fillSync !== s.fillObj._v) { sw.cvSet(c._id, 'fillObj', s.fillObj._h); s.fillSync = s.fillObj._v; } }
  function ps(c) { var s = c._s; if (s.strokeObj && s.strokeSync !== s.strokeObj._v) { sw.cvSet(c._id, 'strokeObj', s.strokeObj._h); s.strokeSync = s.strokeObj._v; } }
  function sendLine(c) { var s = c._s; sw.cvSet(c._id, 'line', s.lineWidth, CAPS.indexOf(s.lineCap), JOINS.indexOf(s.lineJoin), s.miterLimit, s.dash, s.dashOffset); }
  function sendShadow(c) { var s = c._s; sw.cvSet(c._id, 'shadow', s.srgba[0], s.srgba[1], s.srgba[2], s.srgba[3], s.blur, s.sdx, s.sdy); }
  function sendCTM(c) { var m = c._s.ctm; sw.cvSet(c._id, 'transform', m[0], m[1], m[2], m[3], m[4], m[5]); }
  function ruleOf(r, fn) { r = String(r); if (r !== 'nonzero' && r !== 'evenodd') throw new TypeError("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': The provided value '" + r + "' is not a valid enum value of type CanvasFillRule."); return r; }
  function enumAttr(name, list, key, send) {
    getter(CP, name, function () { return this._s[key]; }, function (v) {
      v = String(v); if (list.indexOf(v) < 0) return;
      this._s[key] = v; if (this._id) send(this, v);
    });
  }
  function styleAttr(name, k, obj, syncK, sendKey) {
    getter(CP, name, function () { return this._s[obj] || this._s[k]; }, function (v) {
      var s = this._s;
      if (v instanceof CanvasGradient || v instanceof CanvasPattern) { s[obj] = v; s[syncK] = -1; return; }
      var c = parseColor(v); if (!c) return;
      s[obj] = null; s[k] = c[4];
      if (this._id) sw.cvSet(this._id, sendKey, c[0], c[1], c[2], c[3]);
    });
  }
  styleAttr('fillStyle', 'fill', 'fillObj', 'fillSync', 'fillColor');
  styleAttr('strokeStyle', 'stroke', 'strokeObj', 'strokeSync', 'strokeColor');
  getter(CP, 'canvas', function () { return this._owner; }, undefined);
  getter(CP, 'lineWidth', function () { return this._s.lineWidth; }, function (v) { v = +v; if (!isFin(v) || v <= 0) return; this._s.lineWidth = v; if (this._id) sendLine(this); });
  getter(CP, 'lineCap', function () { return this._s.lineCap; }, function (v) { v = String(v); if (CAPS.indexOf(v) < 0) return; this._s.lineCap = v; if (this._id) sendLine(this); });
  getter(CP, 'lineJoin', function () { return this._s.lineJoin; }, function (v) { v = String(v); if (JOINS.indexOf(v) < 0) return; this._s.lineJoin = v; if (this._id) sendLine(this); });
  getter(CP, 'miterLimit', function () { return this._s.miterLimit; }, function (v) { v = +v; if (!isFin(v) || v <= 0) return; this._s.miterLimit = v; if (this._id) sendLine(this); });
  getter(CP, 'lineDashOffset', function () { return this._s.dashOffset; }, function (v) { v = +v; if (!isFin(v)) return; this._s.dashOffset = v; if (this._id) sendLine(this); });
  getter(CP, 'globalAlpha', function () { return this._s.alpha; }, function (v) { v = +v; if (!isFin(v) || v < 0 || v > 1) return; this._s.alpha = v; if (this._id) sw.cvSet(this._id, 'globalAlpha', v); });
  getter(CP, 'globalCompositeOperation', function () { return this._s.gco; }, function (v) { v = String(v); if (!OPS_OK[v]) return; this._s.gco = v; if (this._id) sw.cvSet(this._id, 'gco', v); });
  getter(CP, 'imageSmoothingEnabled', function () { return this._s.smooth; }, function (v) { this._s.smooth = !!v; if (this._id) sw.cvSet(this._id, 'smoothing', this._s.smooth, ['low', 'medium', 'high'].indexOf(this._s.quality)); });
  getter(CP, 'imageSmoothingQuality', function () { return this._s.quality; }, function (v) { v = String(v); if (['low', 'medium', 'high'].indexOf(v) < 0) return; this._s.quality = v; if (this._id) sw.cvSet(this._id, 'smoothing', this._s.smooth, ['low', 'medium', 'high'].indexOf(v)); });
  getter(CP, 'shadowColor', function () { return this._s.shadowColor; }, function (v) { var c = parseColor(v); if (!c) return; this._s.shadowColor = c[4]; this._s.srgba = [c[0], c[1], c[2], c[3]]; if (this._id) sendShadow(this); });
  getter(CP, 'shadowBlur', function () { return this._s.blur; }, function (v) { v = +v; if (!isFin(v) || v < 0) return; this._s.blur = v; if (this._id) sendShadow(this); });
  getter(CP, 'shadowOffsetX', function () { return this._s.sdx; }, function (v) { v = +v; if (!isFin(v)) return; this._s.sdx = v; if (this._id) sendShadow(this); });
  getter(CP, 'shadowOffsetY', function () { return this._s.sdy; }, function (v) { v = +v; if (!isFin(v)) return; this._s.sdy = v; if (this._id) sendShadow(this); });
  getter(CP, 'filter', function () { return this._s.filter; }, function (v) { v = String(v); if (!this._id) return; if (sw.cvSet(this._id, 'filter', v)) this._s.filter = v.trim() === '' ? 'none' : v; });
  getter(CP, 'font', function () { return this._s.font; }, function (v) {
    var canon = sw.cvFont(String(v), 16); if (!canon) return;
    this._s.font = canon; var m = /(?:^|\s)([\d.]+)px/.exec(canon); this._s.fontPx = m ? parseFloat(m[1]) : 10;
    if (this._id) sw.cvSet(this._id, 'font', String(v));
  });
  enumAttr('textAlign', ['start', 'end', 'left', 'right', 'center'], 'align', function (c, v) { sw.cvSet(c._id, 'textAlign', v); });
  enumAttr('textBaseline', ['top', 'hanging', 'middle', 'alphabetic', 'ideographic', 'bottom'], 'baseline', function (c, v) { sw.cvSet(c._id, 'textBaseline', v); });
  enumAttr('direction', ['ltr', 'rtl', 'inherit'], 'dir', function (c, v) { sw.cvSet(c._id, 'direction', v === 'inherit' ? 'ltr' : v); });
  enumAttr('fontKerning', ['auto', 'normal', 'none'], 'kern', function (c, v) { sw.cvSet(c._id, 'fontKerning', v); });
  enumAttr('fontStretch', ['ultra-condensed', 'extra-condensed', 'condensed', 'semi-condensed', 'normal', 'semi-expanded', 'expanded', 'extra-expanded', 'ultra-expanded'], 'stretch', function () { });
  enumAttr('fontVariantCaps', ['normal', 'small-caps', 'all-small-caps', 'petite-caps', 'all-petite-caps', 'unicase', 'titling-caps'], 'caps', function () { });
  enumAttr('textRendering', ['auto', 'optimizeSpeed', 'optimizeLegibility', 'geometricPrecision'], 'rendering', function () { });
  getter(CP, 'letterSpacing', function () { return this._s.ls; }, function (v) { var px = parseLength(v, this._s.fontPx); if (px === null) return; this._s.ls = String(v).trim(); if (this._id) sw.cvSet(this._id, 'letterSpacing', px); });
  getter(CP, 'wordSpacing', function () { return this._s.ws; }, function (v) { var px = parseLength(v, this._s.fontPx); if (px === null) return; this._s.ws = String(v).trim(); if (this._id) sw.cvSet(this._id, 'wordSpacing', px); });
  getter(CP, 'lang', function () { return this._s.lang || 'inherit'; }, function (v) { this._s.lang = String(v); });

  function guardArgs(fn, n, args) { if (args.length < n) throw new TypeError("Failed to execute '" + fn + "' on 'CanvasRenderingContext2D': " + n + ' arguments required, but only ' + args.length + ' present.'); }

  defs(CP, {
    save: function () { this._stack.push({ s: copyState(this._s), path: null }); if (this._id) sw.cvSave(this._id); },
    restore: function () { if (!this._stack.length) return; this._s = this._stack.pop().s; if (this._id) sw.cvRestore(this._id); },
    reset: function () { this._s = freshState(); this._stack.length = 0; this._path = newPath(); if (this._id) sw.cvResetCtx(this._id); },
    isContextLost: function () { return false; },
    getContextAttributes: function () { var a = this._attrs; return { alpha: a.alpha, colorSpace: a.colorSpace, desynchronized: a.desynchronized, willReadFrequently: a.willReadFrequently }; },
    beginPath: function () { this._path = newPath(); },
    setLineDash: function (arr) {
      guardArgs('setLineDash', 1, arguments);
      var a = Array.prototype.slice.call(arr === null || arr === undefined ? [] : arr, 0).map(Number);
      for (var i = 0; i < a.length; i++) if (!isFin(a[i]) || a[i] < 0) return;
      if (a.length % 2) a = a.concat(a);
      this._s.dash = a; if (this._id) sendLine(this);
    },
    getLineDash: function () { return this._s.dash.slice(); },
    fill: function (a, b) {
      var path = null, rule = 'nonzero';
      if (a instanceof Path2D) { path = a; if (b !== undefined) rule = ruleOf(b, 'fill'); } else if (a !== undefined) rule = ruleOf(a, 'fill');
      if (!this._id) return;
      var arr = path ? path._p.a : this._path.a; if (!arr.length) return;
      pf(this); sw.cvFillPath(this._id, arr, rule, path ? 0 : 1);
    },
    stroke: function (p) {
      var path = p instanceof Path2D ? p : null;
      if (p !== undefined && !path) throw new TypeError("Failed to execute 'stroke' on 'CanvasRenderingContext2D': parameter 1 is not of type 'Path2D'.");
      if (!this._id) return;
      var arr = path ? path._p.a : this._path.a; if (!arr.length) return;
      ps(this); sw.cvStrokePath(this._id, arr, 'nonzero', path ? 0 : 1);
    },
    clip: function (a, b) {
      var path = null, rule = 'nonzero';
      if (a instanceof Path2D) { path = a; if (b !== undefined) rule = ruleOf(b, 'clip'); } else if (a !== undefined) rule = ruleOf(a, 'clip');
      if (!this._id) return;
      var arr = path ? path._p.a : this._path.a;
      sw.cvClipPath(this._id, arr.length ? arr : [0, -1e9, -1e9, 4], rule, path ? 0 : 1);
    },
    isPointInPath: function (a, b, c, d) {
      var path = null, x, y, rule = 'nonzero';
      if (a instanceof Path2D) { path = a; x = +b; y = +c; if (d !== undefined) rule = ruleOf(d, 'isPointInPath'); } else { guardArgs('isPointInPath', 2, arguments); x = +a; y = +b; if (c !== undefined) rule = ruleOf(c, 'isPointInPath'); }
      if (!fin(x, y) || !this._id) return false;
      var arr = path ? path._p.a : this._path.a; if (!arr.length) return false;
      return sw.cvPointInPath(this._id, arr, x, y, rule, path ? 0 : 1);
    },
    isPointInStroke: function (a, b, c) {
      var path = null, x, y;
      if (a instanceof Path2D) { path = a; x = +b; y = +c; } else { guardArgs('isPointInStroke', 2, arguments); x = +a; y = +b; }
      if (!fin(x, y) || !this._id) return false;
      var arr = path ? path._p.a : this._path.a; if (!arr.length) return false;
      return sw.cvPointInStroke(this._id, arr, x, y, path ? 0 : 1);
    },
    scale: function (x, y) { guardArgs('scale', 2, arguments); x = +x; y = +y; if (!fin(x, y)) return; this._mul([x, 0, 0, y, 0, 0]); },
    rotate: function (a) { guardArgs('rotate', 1, arguments); a = +a; if (!isFin(a)) return; var c = Math.cos(a), s = Math.sin(a); this._mul([c, s, -s, c, 0, 0]); },
    translate: function (x, y) { guardArgs('translate', 2, arguments); x = +x; y = +y; if (!fin(x, y)) return; this._mul([1, 0, 0, 1, x, y]); },
    transform: function (a, b, c, d, e, f) { guardArgs('transform', 6, arguments); a = +a; b = +b; c = +c; d = +d; e = +e; f = +f; if (!fin(a, b, c, d, e, f)) return; this._mul([a, b, c, d, e, f]); },
    setTransform: function (a, b, c, d, e, f) {
      var v;
      if (arguments.length === 0) v = [1, 0, 0, 1, 0, 0];
      else if (arguments.length === 1 || (arguments.length > 1 && typeof a === 'object' && a !== null)) {
        var s = toMatrixInit(a), m = s.m; v = [m[0], m[1], m[4], m[5], m[12], m[13]];
      } else { guardArgs('setTransform', 6, arguments); v = [+a, +b, +c, +d, +e, +f]; }
      if (!fin.apply(null, v)) return;
      this._s.ctm = v; if (this._id) sendCTM(this);
    },
    resetTransform: function () { this._s.ctm = [1, 0, 0, 1, 0, 0]; if (this._id) sendCTM(this); },
    getTransform: function () { var m = this._s.ctm; return new DOMMatrix([m[0], m[1], m[2], m[3], m[4], m[5]]); },
    fillRect: function (x, y, w, h) { guardArgs('fillRect', 4, arguments); x = +x; y = +y; w = +w; h = +h; if (!fin(x, y, w, h) || !this._id) return; pf(this); sw.cvFillRect(this._id, x, y, w, h); },
    strokeRect: function (x, y, w, h) { guardArgs('strokeRect', 4, arguments); x = +x; y = +y; w = +w; h = +h; if (!fin(x, y, w, h) || !this._id) return; ps(this); sw.cvStrokeRect(this._id, x, y, w, h); },
    clearRect: function (x, y, w, h) { guardArgs('clearRect', 4, arguments); x = +x; y = +y; w = +w; h = +h; if (!fin(x, y, w, h) || !this._id) return; sw.cvClearRect(this._id, x, y, w, h); },
    fillText: function (t, x, y, mw) {
      guardArgs('fillText', 3, arguments); t = String(t); x = +x; y = +y; if (!fin(x, y) || !this._id) return;
      if (mw !== undefined) { mw = +mw; if (!(mw > 0)) return; } else mw = 0;
      pf(this); sw.cvFillText(this._id, t, x, y, mw);
    },
    strokeText: function (t, x, y, mw) {
      guardArgs('strokeText', 3, arguments); t = String(t); x = +x; y = +y; if (!fin(x, y) || !this._id) return;
      if (mw !== undefined) { mw = +mw; if (!(mw > 0)) return; } else mw = 0;
      ps(this); sw.cvStrokeText(this._id, t, x, y, mw);
    },
    measureText: function (t) {
      guardArgs('measureText', 1, arguments);
      var tm = Object.create(TextMetrics.prototype);
      Object.defineProperty(tm, '_v', { value: this._id ? sw.cvMeasure(this._id, String(t)) : [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0] });
      return tm;
    },
    drawImage: function (img) {
      var n = arguments.length;
      if (n < 3) throw new TypeError("Failed to execute 'drawImage' on 'CanvasRenderingContext2D': 3 arguments required, but only " + n + ' present.');
      if (n !== 3 && n !== 5 && n !== 9) throw new TypeError("Failed to execute 'drawImage' on 'CanvasRenderingContext2D': Valid arities are: [3, 5, 9], but " + n + ' arguments provided.');
      var r = srcOf(img, 'drawImage'); var v = [];
      for (var i = 1; i < n; i++) { var x = +arguments[i]; if (!isFin(x)) return; v.push(x); }
      if (!r || !this._id) return;
      while (v.length < 8) v.push(0);
      pf(this);
      sw.cvDrawImage(this._id, r[0], r[1], n === 3 ? 3 : n === 5 ? 5 : 9, v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7]);
    },
    createLinearGradient: function (x0, y0, x1, y1) { guardArgs('createLinearGradient', 4, arguments); x0 = +x0; y0 = +y0; x1 = +x1; y1 = +y1; if (!fin(x0, y0, x1, y1)) throw new TypeError("Failed to execute 'createLinearGradient' on 'CanvasRenderingContext2D': The provided double value is non-finite."); return mkGradient(sw.cvGradient('linear', x0, y0, x1, y1)); },
    createRadialGradient: function (x0, y0, r0, x1, y1, r1) {
      guardArgs('createRadialGradient', 6, arguments); x0 = +x0; y0 = +y0; r0 = +r0; x1 = +x1; y1 = +y1; r1 = +r1;
      if (!fin(x0, y0, r0, x1, y1, r1)) throw new TypeError("Failed to execute 'createRadialGradient' on 'CanvasRenderingContext2D': The provided double value is non-finite.");
      if (r0 < 0 || r1 < 0) throw dex("Failed to execute 'createRadialGradient' on 'CanvasRenderingContext2D': The radius provided (" + Math.min(r0, r1) + ') is negative.', 'IndexSizeError');
      return mkGradient(sw.cvGradient('radial', x0, y0, r0, x1, y1, r1));
    },
    createConicGradient: function (a, x, y) { guardArgs('createConicGradient', 3, arguments); a = +a; x = +x; y = +y; if (!fin(a, x, y)) throw new TypeError("Failed to execute 'createConicGradient' on 'CanvasRenderingContext2D': The provided double value is non-finite."); return mkGradient(sw.cvGradient('conic', a, x, y)); },
    createPattern: function (img, rep) {
      guardArgs('createPattern', 2, arguments);
      rep = rep === null || rep === undefined || rep === '' ? 'repeat' : String(rep);
      if (['repeat', 'repeat-x', 'repeat-y', 'no-repeat'].indexOf(rep) < 0) throw dex("Failed to execute 'createPattern' on 'CanvasRenderingContext2D': The provided type ('" + rep + "') is not recognized.", 'SyntaxError');
      var r = srcOf(img, 'createPattern'); if (!r) return null;
      var h = sw.cvPattern(r[0], r[1], rep); if (!h) return null;
      var p = Object.create(CanvasPattern.prototype);
      Object.defineProperty(p, '_h', { value: h }); Object.defineProperty(p, '_v', { value: 0, writable: true }); track(p, h);
      return p;
    },
    createImageData: function (a, b) {
      guardArgs('createImageData', 1, arguments);
      if (a instanceof ImageData) return new ImageData(a.width, a.height);
      guardArgs('createImageData', 2, arguments);
      a = +a; b = +b; if (!fin(a, b)) throw new TypeError("Failed to execute 'createImageData' on 'CanvasRenderingContext2D': The provided float value is non-finite.");
      if (a === 0 || b === 0) throw dex("Failed to execute 'createImageData' on 'CanvasRenderingContext2D': The source " + (a === 0 ? 'width' : 'height') + ' is 0.', 'IndexSizeError');
      var w = Math.abs(Math.trunc(a)), h = Math.abs(Math.trunc(b));
      if (w * h * 4 > 536870912) throw new RangeError("Failed to execute 'createImageData' on 'CanvasRenderingContext2D': Out of memory at ImageData creation");
      return new ImageData(Math.max(w, 1), Math.max(h, 1));
    },
    getImageData: function (x, y, w, h, settings) {
      guardArgs('getImageData', 4, arguments);
      x = +x; y = +y; w = +w; h = +h;
      if (!fin(x, y, w, h)) throw new TypeError("Failed to execute 'getImageData' on 'CanvasRenderingContext2D': The provided float value is non-finite.");
      if (w === 0 || h === 0) throw dex("Failed to execute 'getImageData' on 'CanvasRenderingContext2D': The source " + (w === 0 ? 'width' : 'height') + ' is 0.', 'IndexSizeError');
      if (w < 0) { x += w; w = -w; } if (h < 0) { y += h; h = -h; }
      x = Math.floor(x); y = Math.floor(y); w = Math.max(1, Math.trunc(w)); h = Math.max(1, Math.trunc(h));
      if (w * h * 4 > 536870912) throw new RangeError("Failed to execute 'getImageData' on 'CanvasRenderingContext2D': Out of memory at ImageData creation");
      var data;
      if (!this._id) data = new Uint8ClampedArray(w * h * 4); else data = fromB64(sw.cvGetImageData(this._id, x, y, w, h));
      return new ImageData(data, w, h, settings);
    },
    putImageData: function (img, dx, dy, ix, iy, iw, ih) {
      guardArgs('putImageData', 3, arguments);
      if (!(img instanceof ImageData)) throw new TypeError("Failed to execute 'putImageData' on 'CanvasRenderingContext2D': parameter 1 is not of type 'ImageData'.");
      dx = +dx; dy = +dy; if (!fin(dx, dy)) throw new TypeError("Failed to execute 'putImageData' on 'CanvasRenderingContext2D': The provided float value is non-finite.");
      var x0 = 0, y0 = 0, w0 = img.width, h0 = img.height;
      if (arguments.length >= 7) { ix = +ix; iy = +iy; iw = +iw; ih = +ih; if (!fin(ix, iy, iw, ih)) throw new TypeError("Failed to execute 'putImageData' on 'CanvasRenderingContext2D': The provided float value is non-finite."); x0 = ix; y0 = iy; w0 = iw; h0 = ih; }
      if (!this._id) return;
      sw.cvPutImageData(this._id, toB64(new Uint8Array(img.data.buffer, img.data.byteOffset, img.data.length)), img.width, img.height, Math.trunc(dx), Math.trunc(dy), Math.trunc(x0), Math.trunc(y0), Math.trunc(w0), Math.trunc(h0));
    },
    drawFocusIfNeeded: function () { },
    scrollPathIntoView: function () { }
  });
  def(CP, '_mul', function (m) {
    var c = this._s.ctm;
    this._s.ctm = [c[0] * m[0] + c[2] * m[1], c[1] * m[0] + c[3] * m[1], c[0] * m[2] + c[2] * m[3], c[1] * m[2] + c[3] * m[3], c[0] * m[4] + c[2] * m[5] + c[4], c[1] * m[4] + c[3] * m[5] + c[5]];
    if (this._id) sendCTM(this);
  });
  Object.defineProperty(CP, '_mul', { enumerable: false });
  global.CanvasRenderingContext2D = CanvasRenderingContext2D;

  /* ------------------------------------------------------------ ImageBitmap */
  function ImageBitmap() { illegal(); }
  getter(ImageBitmap.prototype, 'width', function () { return this._closed ? 0 : this._w; }, undefined);
  getter(ImageBitmap.prototype, 'height', function () { return this._closed ? 0 : this._h2; }, undefined);
  def(ImageBitmap.prototype, 'close', function () { if (!this._closed) { this._closed = true; if (this._h) sw.cvFree(this._h); this._h = 0; } });
  Object.defineProperty(ImageBitmap.prototype, '_isBitmap', { value: true });
  tag(ImageBitmap, 'ImageBitmap');
  function mkBitmap(h, w, hh) {
    var b = Object.create(ImageBitmap.prototype);
    Object.defineProperty(b, '_h', { value: h, writable: true, enumerable: false });
    Object.defineProperty(b, '_w', { value: w, writable: true, enumerable: false });
    Object.defineProperty(b, '_h2', { value: hh, writable: true, enumerable: false });
    Object.defineProperty(b, '_closed', { value: false, writable: true, enumerable: false });
    track(b, h);
    return b;
  }
  global.ImageBitmap = ImageBitmap;

  function blobBytes(blob) { return blob && blob._bytes ? blob._bytes : null; }
  global.createImageBitmap = function createImageBitmap(src) {
    var args = arguments;
    return new Promise(function (resolve, reject) {
      try {
        var sx, sy, sw_, sh_, opts;
        if (args.length >= 5) { sx = +args[1]; sy = +args[2]; sw_ = +args[3]; sh_ = +args[4]; opts = args[5]; }
        else opts = args[1];
        opts = opts || {};
        if (args.length >= 5) {
          if (sw_ === 0 || sh_ === 0) throw dex("The crop rect width or height is 0.", 'InvalidStateError');
        }
        var base = null, size;
        if (src instanceof global.Blob) {
          var bytes = blobBytes(src);
          var r = bytes ? sw.cvImgDecode(toB64(bytes)) : null;
          if (!r) throw dex('The source image could not be decoded.', 'InvalidStateError');
          base = ['img', r[0]]; size = [r[1], r[2]];
        } else if (src instanceof ImageData) {
          var h = sw.cvImgFromData(toB64(new Uint8Array(src.data.buffer, src.data.byteOffset, src.data.length)), src.width, src.height);
          base = ['img', h]; size = [src.width, src.height];
        } else {
          base = srcOf(src, 'createImageBitmap'); size = srcSize(src);
          if (!base) throw dex('The source image is not valid.', 'InvalidStateError');
        }
        if (src instanceof global.HTMLImageElement && (!size[0] || !size[1])) {
          var ld = sw.cvImgLoad(src.currentSrc || src.src); if (!ld) throw dex('The source image could not be decoded.', 'InvalidStateError');
          size = [ld[1], ld[2]]; sw.cvFree(ld[0]);
        }
        var cw = args.length >= 5 ? sw_ : size[0], ch = args.length >= 5 ? sh_ : size[1];
        var ox = args.length >= 5 ? sx : 0, oy = args.length >= 5 ? sy : 0;
        var rw = opts.resizeWidth, rh = opts.resizeHeight, outW = Math.abs(cw), outH = Math.abs(ch);
        if (rw !== undefined && rh !== undefined) { outW = rw; outH = rh; }
        else if (rw !== undefined) { outH = Math.round(outH * rw / outW); outW = rw; }
        else if (rh !== undefined) { outW = Math.round(outW * rh / outH); outH = rh; }
        outW = Math.max(1, Math.round(outW)); outH = Math.max(1, Math.round(outH));
        var tmp = sw.cvNew(outW, outH, true);
        if (!tmp) throw new RangeError('The ImageBitmap is too large.');
        var q = { pixelated: 0, low: 0, medium: 1, high: 2 }[opts.resizeQuality || 'low'];
        sw.cvSet(tmp, 'smoothing', opts.resizeQuality !== 'pixelated', q || 0);
        if (opts.imageOrientation === 'flipY') sw.cvSet(tmp, 'transform', 1, 0, 0, -1, 0, outH);
        sw.cvDrawImage(tmp, base[0], base[1], 9, ox, oy, cw, ch, 0, 0, outW, outH);
        var hh = sw.cvImgFromCtx(tmp); sw.cvFree(tmp);
        if (src instanceof global.Blob || src instanceof ImageData) sw.cvFree(base[1]);
        resolve(mkBitmap(hh, outW, outH));
      } catch (e) { reject(e); }
    });
  };
  global.self && (global.self.createImageBitmap = global.createImageBitmap);

  /* ------------------------------------------------- bitmaprenderer context */
  function ImageBitmapRenderingContext() { illegal(); }
  tag(ImageBitmapRenderingContext, 'ImageBitmapRenderingContext');
  getter(ImageBitmapRenderingContext.prototype, 'canvas', function () { return this._owner; }, undefined);
  def(ImageBitmapRenderingContext.prototype, 'transferFromImageBitmap', function (bm) {
    if (bm === null || bm === undefined) { if (this._id) sw.cvClear(this._id); return; }
    if (!bm || !bm._isBitmap) throw new TypeError("Failed to execute 'transferFromImageBitmap' on 'ImageBitmapRenderingContext': parameter 1 is not of type 'ImageBitmap'.");
    if (bm._closed) throw dex("Failed to execute 'transferFromImageBitmap' on 'ImageBitmapRenderingContext': The input ImageBitmap has been detached", 'InvalidStateError');
    if (this._id) {
      sw.cvResize(this._id, bm._w, bm._h2);
      this._owner._brResize = true;
      sw.cvDrawImage(this._id, 'img', bm._h, 3, 0, 0, 0, 0, 0, 0, 0, 0);
    }
    bm.close();
  });
  def(ImageBitmapRenderingContext.prototype, 'getContextAttributes', function () { return { alpha: this._alpha }; });
  function makeBR(owner, attrs) {
    var d = ownerDims(owner);
    var c = Object.create(ImageBitmapRenderingContext.prototype);
    Object.defineProperty(c, '_id', { value: sw.cvNew(d[0], d[1], !(attrs && attrs.alpha === false)), writable: true });
    Object.defineProperty(c, '_owner', { value: owner });
    Object.defineProperty(c, '_alpha', { value: !(attrs && attrs.alpha === false) });
    if (c._id) { track(c, c._id); if (owner instanceof global.HTMLCanvasElement) sw.cvBind(c._id, idOfEl(owner)); }
    c._resize = function (w, h) { if (this._id) sw.cvResize(this._id, w, h); };
    return c;
  }

  /* ----------------------------------------------------------- canvas element */
  var CEP = global.HTMLCanvasElement.prototype;
  var origSet = CEP.setAttribute, origRem = CEP.removeAttribute;
  function parseDim(el, name, dflt) {
    var a = el.getAttribute(name);
    if (a === null) return dflt;
    var m = /^[\t\n\f\r ]*\+?(\d+)/.exec(a);
    if (!m) return dflt;
    var v = parseInt(m[1], 10);
    return v > 2147483647 ? dflt : v;
  }
  function onResize(el) {
    var st = el[SLOT];
    if (st && st.ctx && st.ctx._resize) st.ctx._resize(el.width, el.height);
  }
  def(CEP, 'setAttribute', function (n, v) { var r = origSet.apply(this, arguments); var l = String(n).toLowerCase(); if (l === 'width' || l === 'height') onResize(this); return r; });
  def(CEP, 'removeAttribute', function (n) { var r = origRem.apply(this, arguments); var l = String(n).toLowerCase(); if (l === 'width' || l === 'height') onResize(this); return r; });
  getter(CEP, 'width', function () { return parseDim(this, 'width', 300); }, function (v) {
    v = Number(v); v = isFin(v) ? Math.trunc(v) : 0; v = v < 0 ? v + 4294967296 : v;
    this.setAttribute('width', String(v > 2147483647 ? 300 : v));
  });
  getter(CEP, 'height', function () { return parseDim(this, 'height', 150); }, function (v) {
    v = Number(v); v = isFin(v) ? Math.trunc(v) : 0; v = v < 0 ? v + 4294967296 : v;
    this.setAttribute('height', String(v > 2147483647 ? 150 : v));
  });
  function typeGroup(t) { return t === 'experimental-webgl' ? 'webgl' : t; }
  function dimsOk(w, h) { return w >= 0 && h >= 0 && w <= 16384 && h <= 16384 && w * h <= 134217728; }
  def(CEP, 'getContext', function (type, attrs) {
    if (arguments.length < 1) throw new TypeError("Failed to execute 'getContext' on 'HTMLCanvasElement': 1 argument required, but only 0 present.");
    type = String(type);
    if (this.__swTransferred) throw dex("Failed to execute 'getContext' on 'HTMLCanvasElement': Cannot get context from a canvas that has transferred its control to offscreen.", 'InvalidStateError');
    var grp = typeGroup(type), st = this[SLOT];
    if (st) return st.type === grp ? st.ctx : null;
    var ctx = null;
    if (type === '2d') ctx = makeCtx2D(this, attrs);
    else if (type === 'bitmaprenderer') ctx = makeBR(this, attrs);
    else if (Object.prototype.hasOwnProperty.call(factories, type)) ctx = factories[type](this, attrs);
    if (!ctx) return null;
    Object.defineProperty(this, SLOT, { value: { type: grp, ctx: ctx }, configurable: true, enumerable: false });
    return ctx;
  });
  function normMime(t) { t = String(t === undefined ? 'image/png' : t).toLowerCase(); return t === 'image/jpeg' || t === 'image/jpg' ? 'image/jpeg' : 'image/png'; }
  function encodeOwner(el, type, q, bin) {
    var w = el.width, h = el.height;
    if (!w || !h) return null;
    var mime = normMime(type); var quality = typeof q === 'number' && q >= 0 && q <= 1 ? q : 0.92;
    var st = el[SLOT] || (el._ctx && { ctx: el._ctx });
    var id = st && st.ctx && st.ctx._id;
    if (st && st.ctx && !st.ctx._id && st.ctx._isGL === undefined && st.type === '2d') return null;
    var tmp = 0;
    if (!id && st && st.ctx && el instanceof global.HTMLCanvasElement) {
      var nb = sw.cvEncodeNode(idOfEl(el), mime, quality, bin);
      return nb ? { mime: mime, data: nb } : null;
    }
    if (!id) { tmp = sw.cvNew(w, h, true); id = tmp; if (!id) return null; }
    var b = sw.cvEncode(id, mime, quality, bin);
    if (tmp) sw.cvFree(tmp);
    return b ? { mime: mime, data: b } : null;
  }
  def(CEP, 'toDataURL', function (type, q) {
    var r = encodeOwner(this, type, q);
    return r ? 'data:' + r.mime + ';base64,' + r.data : 'data:,';
  });
  def(CEP, 'toBlob', function (cb, type, q) {
    if (typeof cb !== 'function') throw new TypeError("Failed to execute 'toBlob' on 'HTMLCanvasElement': The callback provided as parameter 1 is not a function.");
    var r = encodeOwner(this, type, q, true);
    global.setTimeout(function () { cb(r ? new global.Blob([fromBin(r.data)], { type: r.mime }) : null); }, 0);
  });
  def(CEP, 'captureStream', function (fps) {
    if (typeof global.MediaStream !== 'function') return null;
    var ms = new global.MediaStream(); var el = this;
    var track = { kind: 'video', id: 'canvas-capture', label: 'canvas', enabled: true, muted: false, readyState: 'live', canvas: el, stop: function () { track.readyState = 'ended'; }, getSettings: function () { return { width: el.width, height: el.height, frameRate: fps || 0 }; }, addEventListener: function () { }, removeEventListener: function () { }, clone: function () { return track; }, requestFrame: function () { } };
    try { if (typeof ms.addTrack === 'function') ms.addTrack(track); } catch (e) { }
    return ms;
  });
  def(CEP, 'transferControlToOffscreen', function () {
    if (this[SLOT]) throw dex("Failed to execute 'transferControlToOffscreen' on 'HTMLCanvasElement': Cannot transfer control from a canvas that has a rendering context.", 'InvalidStateError');
    if (this.__swTransferred) throw dex("Failed to execute 'transferControlToOffscreen' on 'HTMLCanvasElement': Cannot transfer control from a canvas for more than one time.", 'InvalidStateError');
    var oc = new global.OffscreenCanvas(this.width, this.height);
    Object.defineProperty(oc, '_placeholder', { value: this });
    Object.defineProperty(this, '__swTransferred', { value: true, configurable: true, enumerable: false });
    return oc;
  });

  /* ------------------------------------------------------------ OffscreenCanvas */
  function OffscreenCanvas(w, h) {
    if (!(this instanceof OffscreenCanvas)) throw new TypeError("Failed to construct 'OffscreenCanvas': Please use the 'new' operator");
    if (arguments.length < 2) throw new TypeError("Failed to construct 'OffscreenCanvas': 2 arguments required, but only " + arguments.length + ' present.');
    global.EventTarget.call(this);
    Object.defineProperty(this, '_w', { value: w >>> 0, writable: true });
    Object.defineProperty(this, '_hh', { value: h >>> 0, writable: true });
    Object.defineProperty(this, '_ctx', { value: null, writable: true });
    Object.defineProperty(this, '_ctxType', { value: '', writable: true });
  }
  OffscreenCanvas.prototype = Object.create(global.EventTarget.prototype, { constructor: { value: OffscreenCanvas, writable: true, configurable: true } });
  Object.setPrototypeOf(OffscreenCanvas, global.EventTarget);
  var OCP = OffscreenCanvas.prototype;
  function ocResize(o) { if (o._ctx && o._ctx._resize) o._ctx._resize(o._w, o._hh); }
  getter(OCP, 'width', function () { return this._w; }, function (v) { this._w = v >>> 0; ocResize(this); });
  getter(OCP, 'height', function () { return this._hh; }, function (v) { this._hh = v >>> 0; ocResize(this); });
  def(OCP, 'getContext', function (type, attrs) {
    type = String(type); var grp = typeGroup(type);
    if (this._ctxType) return this._ctxType === grp ? this._ctx : null;
    var ctx = null;
    if (type === '2d') ctx = makeCtx2D(this, attrs);
    else if (type === 'bitmaprenderer') ctx = makeBR(this, attrs);
    else if (Object.prototype.hasOwnProperty.call(factories, type)) ctx = factories[type](this, attrs);
    if (!ctx) return null;
    this._ctx = ctx; this._ctxType = grp;
    if (this._placeholder && ctx._id) sw.cvBind(ctx._id, idOfEl(this._placeholder));
    return ctx;
  });
  def(OCP, 'convertToBlob', function (opts) {
    var self = this;
    return new Promise(function (resolve, reject) {
      if (!self._w || !self._hh) { reject(dex("Failed to execute 'convertToBlob' on 'OffscreenCanvas': The size of the OffscreenCanvas is zero.", 'InvalidStateError')); return; }
      var r = encodeOwner(self, opts && opts.type, opts && opts.quality, true);
      if (!r) { reject(dex('Failed to execute \'convertToBlob\' on \'OffscreenCanvas\': Blob creation failed.', 'EncodingError')); return; }
      resolve(new global.Blob([fromBin(r.data)], { type: r.mime }));
    });
  });
  def(OCP, 'transferToImageBitmap', function () {
    if (!this._ctx || !this._ctx._id) throw dex("Failed to execute 'transferToImageBitmap' on 'OffscreenCanvas': Cannot transfer an ImageBitmap from an OffscreenCanvas with no context", 'InvalidStateError');
    var h = sw.cvImgFromCtx(this._ctx._id);
    sw.cvClear(this._ctx._id);
    return mkBitmap(h, this._w, this._hh);
  });
  tag(OffscreenCanvas, 'OffscreenCanvas');
  global.OffscreenCanvas = OffscreenCanvas;
  global.CanvasRenderingContext2D = CanvasRenderingContext2D;
  global.ImageBitmapRenderingContext = ImageBitmapRenderingContext;
})(globalThis);
