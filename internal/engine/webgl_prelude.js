/*
 * Sweef WebGL prelude: WebGLRenderingContext / WebGL2RenderingContext and the
 * object, extension and event classes around them. All GL state lives in Go
 * (internal/gl); this file keeps ids, validates argument types, converts
 * typed arrays to base64 for the host boundary and implements the DOM-facing
 * behaviour (context attributes, loss/restore events, texture sources).
 */
(function (global) {
  'use strict';
  var sw = global.__sw;
  if (!sw || !sw.glCreate) return;
  var factories = global.__swCanvasFactories = global.__swCanvasFactories || {};
  var K = sw.glConsts();
  var call = sw.glc;
  var hasB64 = typeof Uint8Array.prototype.toBase64 === 'function' && typeof Uint8Array.fromBase64 === 'function';

  function def(o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true, enumerable: true }); }
  function hid(o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true, enumerable: false }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function illegal() { throw new TypeError('Illegal constructor'); }
  function u32(x) { return x >>> 0; }
  function i32(x) { return x | 0; }

  /* ---- base64 bridging ---- */
  function bytesOf(v) {
    if (v instanceof Uint8Array) return v;
    if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
    if (v instanceof ArrayBuffer) return new Uint8Array(v);
    return new Uint8Array(0);
  }
  function toB64(u8) {
    if (hasB64) return u8.toBase64();
    var s = '';
    for (var i = 0; i < u8.length; i += 0x2000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x2000));
    return global.btoa(s);
  }
  function fromB64(s) {
    if (hasB64) return Uint8Array.fromBase64(s);
    var bin = global.atob(s), u = new Uint8Array(bin.length);
    for (var i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
    return u;
  }
  // numbers: short arrays go as plain arrays, long ones as base64 of the
  // 32-bit little endian representation.
  function numArr(v, off, len, F32) {
    var n = v.length;
    off = off | 0; if (len === undefined || len === 0) len = n - off; else len = len | 0;
    if (off < 0 || off + len > n || len < 0) return null;
    if (len <= 64) {
      var a = new Array(len);
      for (var i = 0; i < len; i++) a[i] = v[off + i];
      return a;
    }
    var t = F32 ? new Float32Array(len) : new Int32Array(len);
    for (var j = 0; j < len; j++) t[j] = v[off + j];
    return toB64(new Uint8Array(t.buffer));
  }

  /* ---- object wrappers ---- */
  var classes = {};
  function mkClass(name, kind) {
    var C = function () { illegal(); };
    Object.defineProperty(C, 'name', { value: name });
    tag(C, name);
    classes[kind] = C;
    return C;
  }
  var WebGLBuffer = mkClass('WebGLBuffer', 'buffer');
  var WebGLTexture = mkClass('WebGLTexture', 'texture');
  var WebGLProgram = mkClass('WebGLProgram', 'program');
  var WebGLShader = mkClass('WebGLShader', 'shader');
  var WebGLFramebuffer = mkClass('WebGLFramebuffer', 'framebuffer');
  var WebGLRenderbuffer = mkClass('WebGLRenderbuffer', 'renderbuffer');
  var WebGLVertexArrayObject = mkClass('WebGLVertexArrayObject', 'vao');
  var WebGLQuery = mkClass('WebGLQuery', 'query');
  var WebGLSampler = mkClass('WebGLSampler', 'sampler');
  var WebGLSync = mkClass('WebGLSync', 'sync');
  var WebGLTransformFeedback = mkClass('WebGLTransformFeedback', 'tf');
  var WebGLUniformLocation = function () { illegal(); }; tag(WebGLUniformLocation, 'WebGLUniformLocation');
  var WebGLActiveInfo = function () { illegal(); }; tag(WebGLActiveInfo, 'WebGLActiveInfo');
  var WebGLShaderPrecisionFormat = function () { illegal(); }; tag(WebGLShaderPrecisionFormat, 'WebGLShaderPrecisionFormat');
  function WebGLContextEvent(type, init) {
    if (!(this instanceof WebGLContextEvent)) throw new TypeError("Failed to construct 'WebGLContextEvent': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    global.Event.call(this, type, init);
    hid(this, 'statusMessage', (init && init.statusMessage) || '');
  }
  WebGLContextEvent.prototype = Object.create(global.Event.prototype, { constructor: { value: WebGLContextEvent, writable: true, configurable: true } });
  Object.setPrototypeOf(WebGLContextEvent, global.Event);
  Object.defineProperty(WebGLContextEvent.prototype, 'statusMessage', { get: function () { return this._sm || ''; }, configurable: true, enumerable: true });
  tag(WebGLContextEvent, 'WebGLContextEvent');

  function mkObj(C, ctx, id) {
    var o = Object.create(C.prototype);
    hid(o, '_id', id); hid(o, '_ctx', ctx); hid(o, '_gen', ctx._gen); hid(o, '_del', false);
    return o;
  }

  var EXT1 = ['ANGLE_instanced_arrays', 'EXT_blend_minmax', 'EXT_color_buffer_half_float', 'EXT_float_blend', 'EXT_frag_depth', 'EXT_shader_texture_lod',
    'EXT_texture_filter_anisotropic', 'OES_element_index_uint', 'OES_standard_derivatives', 'OES_texture_float', 'OES_texture_float_linear',
    'OES_texture_half_float', 'OES_texture_half_float_linear', 'OES_vertex_array_object', 'WEBGL_color_buffer_float', 'WEBGL_debug_renderer_info',
    'WEBGL_debug_shaders', 'WEBGL_depth_texture', 'WEBGL_draw_buffers', 'WEBGL_lose_context'];
  var EXT2 = ['EXT_color_buffer_float', 'EXT_color_buffer_half_float', 'EXT_float_blend', 'EXT_texture_filter_anisotropic', 'OES_texture_float_linear',
    'WEBGL_debug_renderer_info', 'WEBGL_debug_shaders', 'WEBGL_lose_context'];

  var GL_INVALID_ENUM = 0x500, GL_INVALID_VALUE = 0x501, GL_INVALID_OPERATION = 0x502, GL_CONTEXT_LOST = 0x9242;
  var TYPE_VIEW = { 0x1401: [Uint8Array, Uint8ClampedArray], 0x1400: [Int8Array], 0x1403: [Uint16Array], 0x1402: [Int16Array], 0x1405: [Uint32Array], 0x1404: [Int32Array],
    0x1406: [Float32Array], 0x140B: [Uint16Array], 0x8D61: [Uint16Array], 0x8033: [Uint16Array], 0x8034: [Uint16Array], 0x8363: [Uint16Array],
    0x8368: [Uint32Array], 0x84FA: [Uint32Array], 0x8C3B: [Uint32Array], 0x8C3E: [Uint32Array], 0x8DAD: [Float32Array] };

  function viewOk(type, view) {
    var ok = TYPE_VIEW[type];
    if (!ok) return true;
    if (view instanceof DataView) return true;
    for (var i = 0; i < ok.length; i++) if (view instanceof ok[i]) return true;
    return false;
  }

  /* ---- context construction ---- */
  function createContext(canvas, attrs, version) {
    attrs = attrs && typeof attrs === 'object' ? attrs : {};
    var A = {
      alpha: attrs.alpha === undefined ? true : !!attrs.alpha,
      antialias: attrs.antialias === undefined ? true : !!attrs.antialias,
      depth: attrs.depth === undefined ? true : !!attrs.depth,
      desynchronized: !!attrs.desynchronized,
      failIfMajorPerformanceCaveat: !!attrs.failIfMajorPerformanceCaveat,
      powerPreference: attrs.powerPreference === 'high-performance' || attrs.powerPreference === 'low-power' ? attrs.powerPreference : 'default',
      premultipliedAlpha: attrs.premultipliedAlpha === undefined ? true : !!attrs.premultipliedAlpha,
      preserveDrawingBuffer: !!attrs.preserveDrawingBuffer,
      stencil: !!attrs.stencil,
      xrCompatible: !!attrs.xrCompatible
    };
    var w = canvas.width, h = canvas.height;
    var nodeId = 0;
    try { if (canvas && canvas.nodeType === 1) nodeId = global.__swIdOf(canvas); } catch (e) { }
    var hid_ = sw.glCreate(version, A.alpha, A.depth, A.stencil, A.antialias, A.premultipliedAlpha, A.preserveDrawingBuffer, w, h, nodeId);
    if (!hid_) return null;
    var Ctor = version === 2 ? WebGL2RenderingContext : WebGLRenderingContext;
    var ctx = Object.create(Ctor.prototype);
    hid(ctx, '_h', hid_); hid(ctx, '_v', version); hid(ctx, '_canvas', canvas); hid(ctx, '_attrs', A);
    hid(ctx, '_gen', 0); hid(ctx, '_lost', false); hid(ctx, '_lostReported', false); hid(ctx, '_pend', false); hid(ctx, '_prog', null);
    hid(ctx, '_reg', {}); hid(ctx, '_exts', {}); hid(ctx, '_nodeId', nodeId); hid(ctx, '_restorable', false); hid(ctx, '_w', w); hid(ctx, '_hh', h);
    hid(ctx, '_vao', null); hid(ctx, '_fb', null); hid(ctx, '_fbRead', null);
    hid(ctx, '_resize', function (nw, nh) {
      if (ctx._lost) return;
      if (sw.glResize(ctx._h, nw, nh)) { ctx._w = nw; ctx._hh = nh; }
    });
    return ctx;
  }

  function reg(ctx, kind, id) {
    var m = ctx._reg[kind] || (ctx._reg[kind] = new Map());
    var o = m.get(id);
    if (!o) { o = mkObj(classes[kind], ctx, id); m.set(id, o); }
    return o;
  }
  function stub(C, ctx) { var o = mkObj(C, ctx, 0); o._del = true; return o; }
  function unreg(ctx, kind, id) { var m = ctx._reg[kind]; if (m) m.delete(id); }
  function objFor(ctx, kind, id) { if (!id) return null; return reg(ctx, kind, id); }

  function setErr(ctx, e) { call(ctx._h, 'setError', e); }
  function touch(ctx) {
    if (ctx._pend) return;
    ctx._pend = true;
    global.setTimeout(function () { ctx._pend = false; if (!ctx._lost) sw.glPresent(ctx._h); }, 0);
  }
  function typeErr(ctx, fn, n, what) {
    return new TypeError("Failed to execute '" + fn + "' on '" + (ctx._v === 2 ? 'WebGL2RenderingContext' : 'WebGLRenderingContext') + "': parameter " + n + ' is not of type \'' + what + "'.");
  }
  // validates an object argument. Returns the id (0 for null), or -1 after
  // raising INVALID_OPERATION when the object is deleted/foreign.
  function oid(ctx, fn, n, o, Class, kind) {
    if (o === null || o === undefined) return 0;
    if (!(o instanceof Class)) throw typeErr(ctx, fn, n, Class.name);
    if (o._ctx !== ctx || o._gen !== ctx._gen || o._del) { setErr(ctx, GL_INVALID_OPERATION); return -1; }
    return o._id;
  }

  function WebGLRenderingContext() { illegal(); }
  function WebGL2RenderingContext() { illegal(); }
  var P1 = WebGLRenderingContext.prototype, P2 = WebGL2RenderingContext.prototype;
  tag(WebGLRenderingContext, 'WebGLRenderingContext');
  tag(WebGL2RenderingContext, 'WebGL2RenderingContext');
  function constants(C, maps) {
    maps.forEach(function (m) {
      Object.keys(m).forEach(function (k) {
        var d = { value: m[k], enumerable: true, writable: false, configurable: false };
        Object.defineProperty(C, k, d);
        Object.defineProperty(C.prototype, k, d);
      });
    });
  }
  K.c1.HALF_FLOAT = undefined; delete K.c1.HALF_FLOAT;
  constants(WebGLRenderingContext, [K.c1]);
  constants(WebGL2RenderingContext, [K.c1, K.c2]);
  Object.defineProperty(WebGL2RenderingContext, 'TIMEOUT_IGNORED', { value: -1, enumerable: true });
  Object.defineProperty(WebGL2RenderingContext.prototype, 'TIMEOUT_IGNORED', { value: -1, enumerable: true });

  // lost-context result values
  function method(protos, name, fn, lostVal) {
    var wrapped = function () {
      if (this._lost) return typeof lostVal === 'function' ? lostVal.apply(this, arguments) : lostVal;
      return fn.apply(this, arguments);
    };
    Object.defineProperty(wrapped, 'name', { value: name });
    Object.defineProperty(wrapped, 'length', { value: fn.length });
    protos.forEach(function (p) { def(p, name, wrapped); });
  }
  var B = [P1, P2], V2 = [P2];
  function getter(protos, name, g) { protos.forEach(function (p) { Object.defineProperty(p, name, { get: g, configurable: true, enumerable: true }); }); }

  getter(B, 'canvas', function () { return this._canvas; });
  getter(B, 'drawingBufferWidth', function () { return this._w; });
  getter(B, 'drawingBufferHeight', function () { return this._hh; });
  getter(B, 'drawingBufferColorSpace', function () { return 'srgb'; });
  getter(B, 'drawingBufferFormat', function () { return this._attrs.alpha ? 'rgba8' : 'rgb8'; });
  B.forEach(function (p) { Object.defineProperty(p, 'unpackColorSpace', { get: function () { return this._ucs || 'srgb'; }, set: function (v) { v = String(v); if (v === 'srgb' || v === 'display-p3') this._ucs = v; }, configurable: true, enumerable: true }); });
  method(B, 'makeXRCompatible', function () { return Promise.resolve(); }, function () { return Promise.resolve(); });
  method(B, 'drawingBufferStorage', function () { }, undefined);
  method(B, 'isContextLost', function () { return this._lost; }, true);
  method(B, 'getContextAttributes', function () {
    var a = this._attrs, o = {};
    ['alpha', 'antialias', 'depth', 'desynchronized', 'failIfMajorPerformanceCaveat', 'powerPreference', 'premultipliedAlpha', 'preserveDrawingBuffer', 'stencil', 'xrCompatible'].forEach(function (k) { o[k] = a[k]; });
    return o;
  }, null);
  method(B, 'getError', function () {
    return call(this._h, 'getError');
  }, function () { if (this._lostReported) return 0; this._lostReported = true; return GL_CONTEXT_LOST; });

  /* ---- simple state ---- */
  var simple = {
    enable: 'enable', disable: 'disable', clearColor: 'clearColor', clearDepth: 'clearDepth', clearStencil: 'clearStencil',
    depthFunc: 'depthFunc', depthMask: 'depthMask', cullFace: 'cullFace', frontFace: 'frontFace', lineWidth: 'lineWidth',
    polygonOffset: 'polygonOffset', sampleCoverage: 'sampleCoverage', hint: 'hint', blendColor: 'blendColor', depthRange: 'depthRange'
  };
  var conv = {
    enable: [u32], disable: [u32], clearColor: [Number, Number, Number, Number], clearDepth: [Number], clearStencil: [i32], depthFunc: [u32], depthMask: [Boolean],
    cullFace: [u32], frontFace: [u32], lineWidth: [Number], polygonOffset: [Number, Number], sampleCoverage: [Number, Boolean], hint: [u32, u32],
    blendColor: [Number, Number, Number, Number], depthRange: [Number, Number]
  };
  Object.keys(simple).forEach(function (n) {
    var cv = conv[n], op = simple[n];
    method(B, n, function () {
      var a = [this._h, op];
      if (arguments.length < cv.length) throw new TypeError("Failed to execute '" + n + "' on 'WebGLRenderingContext': " + cv.length + ' arguments required, but only ' + arguments.length + ' present.');
      for (var i = 0; i < cv.length; i++) a.push(cv[i](arguments[i]));
      call.apply(null, a);
    }, undefined);
  });
  method(B, 'isEnabled', function (c) { return call(this._h, 'isEnabled', u32(c)); }, false);
  method(B, 'viewport', function (x, y, w, h) { call(this._h, 'viewport', i32(x), i32(y), i32(w), i32(h)); }, undefined);
  method(B, 'scissor', function (x, y, w, h) { call(this._h, 'scissor', i32(x), i32(y), i32(w), i32(h)); }, undefined);
  method(B, 'colorMask', function (r, g, b, a) { call(this._h, 'colorMask', !!r, !!g, !!b, !!a); }, undefined);
  method(B, 'stencilFunc', function (f, r, m) { call(this._h, 'stencilFuncSeparate', 0x408, u32(f), i32(r), u32(m)); }, undefined);
  method(B, 'stencilFuncSeparate', function (face, f, r, m) { call(this._h, 'stencilFuncSeparate', u32(face), u32(f), i32(r), u32(m)); }, undefined);
  method(B, 'stencilOp', function (a, b, c) { call(this._h, 'stencilOpSeparate', 0x408, u32(a), u32(b), u32(c)); }, undefined);
  method(B, 'stencilOpSeparate', function (face, a, b, c) { call(this._h, 'stencilOpSeparate', u32(face), u32(a), u32(b), u32(c)); }, undefined);
  method(B, 'stencilMask', function (m) { call(this._h, 'stencilMaskSeparate', 0x408, u32(m)); }, undefined);
  method(B, 'stencilMaskSeparate', function (f, m) { call(this._h, 'stencilMaskSeparate', u32(f), u32(m)); }, undefined);
  method(B, 'blendEquation', function (m) { m = u32(m); call(this._h, 'blendEquationSeparate', m, m); }, undefined);
  method(B, 'blendEquationSeparate', function (a, b) { call(this._h, 'blendEquationSeparate', u32(a), u32(b)); }, undefined);
  method(B, 'blendFunc', function (s, d) { s = u32(s); d = u32(d); call(this._h, 'blendFuncSeparate', s, d, s, d); }, undefined);
  method(B, 'blendFuncSeparate', function (a, b, c, d) { call(this._h, 'blendFuncSeparate', u32(a), u32(b), u32(c), u32(d)); }, undefined);
  method(B, 'pixelStorei', function (p, v) { call(this._h, 'pixelStorei', u32(p), typeof v === 'boolean' ? (v ? 1 : 0) : i32(v)); }, undefined);
  method(B, 'flush', function () { }, undefined);
  method(B, 'finish', function () { }, undefined);

  method(B, 'clear', function (mask) { call(this._h, 'clear', u32(mask)); touch(this); }, undefined);

  function decodeVal(ctx, r) {
    if (!r) return null;
    switch (r[0]) {
      case 'n': return r[1];
      case 'b': return r[1];
      case 's': return r[1];
      case 'f': return new Float32Array(r[1]);
      case 'i': return new Int32Array(r[1]);
      case 'u': return new Uint32Array(r[1]);
      case 'bv': return r[1].slice();
      case 'o': return objFor(ctx, r[1], r[2]);
    }
    return null;
  }
  method(B, 'getParameter', function (p) {
    p = u32(p);
    var r = call(this._h, 'getParameter', p);
    return decodeVal(this, r);
  }, null);
  method(B, 'getShaderPrecisionFormat', function (s, p) {
    var r = call(this._h, 'getShaderPrecisionFormat', u32(s), u32(p));
    if (!r) return null;
    var o = Object.create(WebGLShaderPrecisionFormat.prototype);
    def(o, 'rangeMin', r[0]); def(o, 'rangeMax', r[1]); def(o, 'precision', r[2]);
    return o;
  }, null);

  /* ---- buffers ---- */
  function bufferSource(data, srcOff, len) {
    var u8;
    if (ArrayBuffer.isView(data)) {
      var el = data.BYTES_PER_ELEMENT || 1;
      srcOff = srcOff | 0;
      var n = data.byteLength / el;
      var cnt = len ? len | 0 : n - srcOff;
      if (srcOff < 0 || cnt < 0 || srcOff + cnt > n) return null;
      u8 = new Uint8Array(data.buffer, data.byteOffset + srcOff * el, cnt * el);
    } else if (data instanceof ArrayBuffer) u8 = new Uint8Array(data);
    else return undefined;
    return toB64(u8);
  }
  function bufId(ctx, fn, n, b) { return oid(ctx, fn, n, b, WebGLBuffer, 'buffer'); }
  method(B, 'createBuffer', function () { var id = call(this._h, 'createBuffer'); return reg(this, 'buffer', id); }, function () { return stub(WebGLBuffer, this); });
  method(B, 'deleteBuffer', function (b) {
    var id = oid(this, 'deleteBuffer', 1, b, WebGLBuffer);
    if (id > 0) { call(this._h, 'deleteBuffer', id); b._del = true; unreg(this, 'buffer', id); }
  }, undefined);
  method(B, 'isBuffer', function (b) { return b instanceof WebGLBuffer && b._ctx === this && !b._del && call(this._h, 'isBuffer', b._id); }, false);
  method(B, 'bindBuffer', function (t, b) {
    var id = bufId(this, 'bindBuffer', 2, b); if (id < 0) return;
    call(this._h, 'bindBuffer', u32(t), id);
  }, undefined);
  method(B, 'bufferData', function (t, d, usage, srcOff, len) {
    if (arguments.length < 3) throw new TypeError("Failed to execute 'bufferData' on 'WebGLRenderingContext': 3 arguments required, but only " + arguments.length + ' present.');
    t = u32(t); usage = u32(usage);
    if (d === null) { setErr(this, GL_INVALID_VALUE); return; }
    if (typeof d === 'number' || (typeof d !== 'object' && typeof d !== 'function')) { call(this._h, 'bufferData', t, +d, null, usage); return; }
    var s = bufferSource(d, srcOff, len);
    if (s === undefined) throw typeErr(this, 'bufferData', 2, 'ArrayBuffer');
    if (s === null) { setErr(this, GL_INVALID_VALUE); return; }
    call(this._h, 'bufferData', t, 0, s, usage);
  }, undefined);
  method(B, 'bufferSubData', function (t, off, d, srcOff, len) {
    var s = bufferSource(d, srcOff, len);
    if (s === undefined) throw typeErr(this, 'bufferSubData', 3, 'ArrayBuffer');
    if (s === null) { setErr(this, GL_INVALID_VALUE); return; }
    call(this._h, 'bufferSubData', u32(t), +off, s);
  }, undefined);
  method(B, 'getBufferParameter', function (t, p) { var r = call(this._h, 'getBufferParameter', u32(t), u32(p)); return r === undefined ? null : r; }, null);

  /* ---- vertex attributes ---- */
  method(B, 'vertexAttribPointer', function (i, size, type, norm, stride, off) { call(this._h, 'vertexAttribPointer', u32(i), i32(size), u32(type), !!norm, i32(stride), +off); }, undefined);
  method(B, 'enableVertexAttribArray', function (i) { call(this._h, 'enableVertexAttribArray', u32(i)); }, undefined);
  method(B, 'disableVertexAttribArray', function (i) { call(this._h, 'disableVertexAttribArray', u32(i)); }, undefined);
  [1, 2, 3, 4].forEach(function (n) {
    method(B, 'vertexAttrib' + n + 'f', function (i, a, b, c, d) { var args = [this._h, 'vertexAttrib', u32(i), 'f']; for (var k = 0; k < n; k++) args.push(+arguments[1 + k]); call.apply(null, args); }, undefined);
    method(B, 'vertexAttrib' + n + 'fv', function (i, v) {
      if (!v || v.length < n) { setErr(this, GL_INVALID_VALUE); return; }
      var args = [this._h, 'vertexAttrib', u32(i), 'f']; for (var k = 0; k < n; k++) args.push(+v[k]); call.apply(null, args);
    }, undefined);
  });
  method(B, 'getVertexAttrib', function (i, p) {
    var r = call(this._h, 'getVertexAttrib', u32(i), u32(p));
    return decodeVal(this, r);
  }, null);
  method(B, 'getVertexAttribOffset', function (i, p) { return call(this._h, 'getVertexAttribOffset', u32(i)); }, 0);

  /* ---- shaders and programs ---- */
  method(B, 'createShader', function (t) { var id = call(this._h, 'createShader', u32(t)); return id ? reg(this, 'shader', id) : null; }, function (t) { return stub(WebGLShader, this); });
  method(B, 'shaderSource', function (s, src) { var id = oid(this, 'shaderSource', 1, s, WebGLShader); if (id <= 0) { if (id === 0) throw typeErr(this, 'shaderSource', 1, 'WebGLShader'); return; } call(this._h, 'shaderSource', id, String(src)); }, undefined);
  method(B, 'compileShader', function (s) { var id = oid(this, 'compileShader', 1, s, WebGLShader); if (id > 0) call(this._h, 'compileShader', id); }, undefined);
  method(B, 'getShaderParameter', function (s, p) {
    var id = oid(this, 'getShaderParameter', 1, s, WebGLShader); if (id <= 0) return null;
    p = u32(p);
    var r = call(this._h, 'getShaderParameter', id, p);
    if (r === undefined || r === null) return null;
    if (p === 0x8B80 || p === 0x8B81) return !!r;
    return r;
  }, null);
  method(B, 'getShaderInfoLog', function (s) { var id = oid(this, 'getShaderInfoLog', 1, s, WebGLShader); if (id <= 0) return null; return call(this._h, 'getShaderInfoLog', id); }, null);
  method(B, 'getShaderSource', function (s) { var id = oid(this, 'getShaderSource', 1, s, WebGLShader); if (id <= 0) return null; return call(this._h, 'getShaderSource', id); }, null);
  method(B, 'deleteShader', function (s) { var id = oid(this, 'deleteShader', 1, s, WebGLShader); if (id > 0) { call(this._h, 'deleteShader', id); s._del = true; } }, undefined);
  method(B, 'isShader', function (s) { return s instanceof WebGLShader && s._ctx === this && !s._del && call(this._h, 'isShader', s._id); }, false);
  method(B, 'createProgram', function () { return reg(this, 'program', call(this._h, 'createProgram')); }, function () { return stub(WebGLProgram, this); });
  method(B, 'deleteProgram', function (p) { var id = oid(this, 'deleteProgram', 1, p, WebGLProgram); if (id > 0) { call(this._h, 'deleteProgram', id); p._del = true; } }, undefined);
  method(B, 'isProgram', function (p) { return p instanceof WebGLProgram && p._ctx === this && !p._del && call(this._h, 'isProgram', p._id); }, false);
  method(B, 'attachShader', function (p, s) {
    var a = oid(this, 'attachShader', 1, p, WebGLProgram), b = oid(this, 'attachShader', 2, s, WebGLShader);
    if (a <= 0 || b <= 0) { if (a === 0 || b === 0) throw typeErr(this, 'attachShader', a === 0 ? 1 : 2, a === 0 ? 'WebGLProgram' : 'WebGLShader'); return; }
    call(this._h, 'attachShader', a, b);
  }, undefined);
  method(B, 'detachShader', function (p, s) {
    var a = oid(this, 'detachShader', 1, p, WebGLProgram), b = oid(this, 'detachShader', 2, s, WebGLShader);
    if (a <= 0 || b <= 0) return;
    call(this._h, 'detachShader', a, b);
  }, undefined);
  method(B, 'getAttachedShaders', function (p) {
    var a = oid(this, 'getAttachedShaders', 1, p, WebGLProgram); if (a <= 0) return null;
    var self = this; return (call(this._h, 'getAttachedShaders', a) || []).map(function (id) { return reg(self, 'shader', id); });
  }, null);
  method(B, 'bindAttribLocation', function (p, i, n) { var a = oid(this, 'bindAttribLocation', 1, p, WebGLProgram); if (a > 0) call(this._h, 'bindAttribLocation', a, u32(i), String(n)); }, undefined);
  method(B, 'linkProgram', function (p) { var a = oid(this, 'linkProgram', 1, p, WebGLProgram); if (a > 0) { call(this._h, 'linkProgram', a); if (this._prog === p) { /* keep old program in use until useProgram */ } } }, undefined);
  method(B, 'validateProgram', function (p) { var a = oid(this, 'validateProgram', 1, p, WebGLProgram); if (a > 0) call(this._h, 'validateProgram', a); }, undefined);
  method(B, 'useProgram', function (p) {
    var a = oid(this, 'useProgram', 1, p, WebGLProgram); if (a < 0) return;
    call(this._h, 'useProgram', a);
    this._prog = decodeVal(this, call(this._h, 'getParameter', 0x8B8D));
  }, undefined);
  method(B, 'getProgramParameter', function (p, pn) {
    var a = oid(this, 'getProgramParameter', 1, p, WebGLProgram); if (a <= 0) return null;
    pn = u32(pn);
    var r = call(this._h, 'getProgramParameter', a, pn);
    if (r === undefined || r === null) return null;
    if (pn === 0x8B80 || pn === 0x8B82 || pn === 0x8B83) return !!r;
    return r;
  }, null);
  method(B, 'getProgramInfoLog', function (p) { var a = oid(this, 'getProgramInfoLog', 1, p, WebGLProgram); if (a <= 0) return null; return call(this._h, 'getProgramInfoLog', a); }, null);
  method(B, 'getAttribLocation', function (p, n) { var a = oid(this, 'getAttribLocation', 1, p, WebGLProgram); if (a <= 0) return -1; return call(this._h, 'getAttribLocation', a, String(n)); }, -1);
  method(B, 'getUniformLocation', function (p, n) {
    var a = oid(this, 'getUniformLocation', 1, p, WebGLProgram); if (a <= 0) return null;
    var loc = call(this._h, 'getUniformLocation', a, String(n));
    if (loc < 0) return null;
    var o = Object.create(WebGLUniformLocation.prototype);
    hid(o, '_loc', loc); hid(o, '_prog', p); hid(o, '_ctx', this); hid(o, '_gen', this._gen);
    return o;
  }, null);
  function activeInfo(r) {
    if (!r) return null;
    var o = Object.create(WebGLActiveInfo.prototype);
    def(o, 'name', r[0]); def(o, 'size', r[2]); def(o, 'type', r[1]);
    return o;
  }
  method(B, 'getActiveAttrib', function (p, i) { var a = oid(this, 'getActiveAttrib', 1, p, WebGLProgram); if (a <= 0) return null; return activeInfo(call(this._h, 'getActiveAttrib', a, u32(i))); }, null);
  method(B, 'getActiveUniform', function (p, i) { var a = oid(this, 'getActiveUniform', 1, p, WebGLProgram); if (a <= 0) return null; return activeInfo(call(this._h, 'getActiveUniform', a, u32(i))); }, null);
  method(B, 'getUniform', function (p, loc) {
    var a = oid(this, 'getUniform', 1, p, WebGLProgram); if (a <= 0) return null;
    if (!(loc instanceof WebGLUniformLocation)) throw typeErr(this, 'getUniform', 2, 'WebGLUniformLocation');
    if (loc._prog !== p) { setErr(this, GL_INVALID_OPERATION); return null; }
    var r = call(this._h, 'getUniform', a, loc._loc); if (!r) return null;
    var t = r[0], kind = r[1], v = r[2];
    if (v.length === 1 && t !== 0x8B5A && t !== 0x8B5B && t !== 0x8B5C) return v[0];
    if (kind === 'float') return new Float32Array(v);
    if (kind === 'int') return new Int32Array(v);
    if (kind === 'uint') return new Uint32Array(v);
    return v;
  }, null);

  // uniforms
  function locOk(ctx, fn, loc) {
    if (loc === null || loc === undefined) return -2; // silently ignored
    if (!(loc instanceof WebGLUniformLocation)) throw typeErr(ctx, fn, 1, 'WebGLUniformLocation');
    if (loc._ctx !== ctx || loc._gen !== ctx._gen || loc._prog._del) { setErr(ctx, GL_INVALID_OPERATION); return -2; }
    if (loc._prog !== ctx._prog) { setErr(ctx, GL_INVALID_OPERATION); return -2; }
    return loc._loc;
  }
  [1, 2, 3, 4].forEach(function (n) {
    method(B, 'uniform' + n + 'f', function (loc) {
      var l = locOk(this, 'uniform' + n + 'f', loc); if (l === -2) return;
      var a = []; for (var k = 0; k < n; k++) a.push(+arguments[1 + k]);
      call(this._h, 'uniformF', l, n, a);
    }, undefined);
    method(B, 'uniform' + n + 'i', function (loc) {
      var l = locOk(this, 'uniform' + n + 'i', loc); if (l === -2) return;
      var a = []; for (var k = 0; k < n; k++) a.push(+arguments[1 + k] | 0);
      call(this._h, 'uniformI', l, n, a);
    }, undefined);
    method(B, 'uniform' + n + 'fv', function (loc, v, off, len) {
      var l = locOk(this, 'uniform' + n + 'fv', loc); if (l === -2) return;
      if (!v || typeof v.length !== 'number') throw new TypeError("Failed to execute 'uniform" + n + "fv': parameter 2 is not a sequence.");
      var a = numArr(v, off, len, true); if (a === null || a.length % n || a.length === 0) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'uniformF', l, n, a);
    }, undefined);
    method(B, 'uniform' + n + 'iv', function (loc, v, off, len) {
      var l = locOk(this, 'uniform' + n + 'iv', loc); if (l === -2) return;
      if (!v || typeof v.length !== 'number') throw new TypeError("Failed to execute 'uniform" + n + "iv': parameter 2 is not a sequence.");
      var a = numArr(v, off, len, false); if (a === null || a.length % n || a.length === 0) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'uniformI', l, n, a);
    }, undefined);
    method(V2, 'uniform' + n + 'ui', function (loc) {
      var l = locOk(this, 'uniform' + n + 'ui', loc); if (l === -2) return;
      var a = []; for (var k = 0; k < n; k++) a.push(+arguments[1 + k] | 0);
      call(this._h, 'uniformU', l, n, a);
    }, undefined);
    method(V2, 'uniform' + n + 'uiv', function (loc, v, off, len) {
      var l = locOk(this, 'uniform' + n + 'uiv', loc); if (l === -2) return;
      var a = numArr(v, off, len, false); if (a === null || a.length % n || a.length === 0) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'uniformU', l, n, a);
    }, undefined);
  });
  [[2, 2, ''], [3, 3, ''], [4, 4, ''], [2, 3, 'x'], [2, 4, 'x'], [3, 2, 'x'], [3, 4, 'x'], [4, 2, 'x'], [4, 3, 'x']].forEach(function (d) {
    var c = d[0], r = d[1];
    var name = 'uniformMatrix' + (d[2] ? c + 'x' + r : c) + 'fv';
    method(d[2] ? V2 : B, name, function (loc, tr, v, off, len) {
      var l = locOk(this, name, loc); if (l === -2) return;
      if (!v || typeof v.length !== 'number') throw new TypeError("Failed to execute '" + name + "': parameter 3 is not a sequence.");
      var a = numArr(v, off, len, true); if (a === null || a.length % (c * r) || a.length === 0) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'uniformMatrix', l, c, r, !!tr, a);
    }, undefined);
  });

  /* ---- textures ---- */
  function texId(ctx, fn, n, t) { return oid(ctx, fn, n, t, WebGLTexture, 'texture'); }
  method(B, 'createTexture', function () { return reg(this, 'texture', call(this._h, 'createTexture')); }, function () { return stub(WebGLTexture, this); });
  method(B, 'deleteTexture', function (t) { var id = texId(this, 'deleteTexture', 1, t); if (id > 0) { call(this._h, 'deleteTexture', id); t._del = true; unreg(this, 'texture', id); } }, undefined);
  method(B, 'isTexture', function (t) { return t instanceof WebGLTexture && t._ctx === this && !t._del && call(this._h, 'isTexture', t._id); }, false);
  method(B, 'bindTexture', function (target, t) { var id = texId(this, 'bindTexture', 2, t); if (id < 0) return; call(this._h, 'bindTexture', u32(target), id); }, undefined);
  method(B, 'activeTexture', function (u) { call(this._h, 'activeTexture', u32(u)); }, undefined);
  method(B, 'texParameteri', function (t, p, v) { call(this._h, 'texParameter', u32(t), u32(p), +v); }, undefined);
  method(B, 'texParameterf', function (t, p, v) { call(this._h, 'texParameter', u32(t), u32(p), +v); }, undefined);
  method(B, 'getTexParameter', function (t, p) { var r = call(this._h, 'getTexParameter', u32(t), u32(p)); if (r === undefined || r === null) return null; if (u32(p) === 0x912F) return !!r; return r; }, null);
  method(B, 'generateMipmap', function (t) { call(this._h, 'generateMipmap', u32(t)); }, undefined);

  var scratch = null;
  function srcDims(src) {
    if (src instanceof global.ImageData) return [src.width, src.height];
    if (typeof global.HTMLImageElement === 'function' && src instanceof global.HTMLImageElement) return [src.naturalWidth || 0, src.naturalHeight || 0];
    if (typeof global.HTMLVideoElement === 'function' && src instanceof global.HTMLVideoElement) return [src.videoWidth || 0, src.videoHeight || 0];
    return [src.width || 0, src.height || 0];
  }
  function isTexSource(s) {
    if (!s || typeof s !== 'object') return false;
    if (s instanceof global.ImageData) return true;
    if (typeof global.HTMLImageElement === 'function' && s instanceof global.HTMLImageElement) return true;
    if (typeof global.HTMLCanvasElement === 'function' && s instanceof global.HTMLCanvasElement) return true;
    if (typeof global.HTMLVideoElement === 'function' && s instanceof global.HTMLVideoElement) return true;
    if (typeof global.OffscreenCanvas === 'function' && s instanceof global.OffscreenCanvas) return true;
    if (typeof global.ImageBitmap === 'function' && s instanceof global.ImageBitmap) return true;
    return false;
  }
  // returns {w,h,b64} of straight-alpha RGBA8 pixels
  function srcPixels(ctx, src) {
    var d = srcDims(src), w = d[0], h = d[1];
    if (!w || !h) return null;
    if (src instanceof global.ImageData) return { w: w, h: h, b64: toB64(bytesOf(src.data)) };
    var oc = new global.OffscreenCanvas(w, h);
    var c2 = oc.getContext('2d', { willReadFrequently: true });
    if (!c2) return null;
    c2.drawImage(src, 0, 0);
    var id = c2.getImageData(0, 0, w, h);
    return { w: w, h: h, b64: toB64(bytesOf(id.data)) };
  }
  function pixView(ctx, fn, type, view, off) {
    if (view === null || view === undefined) return null;
    if (!ArrayBuffer.isView(view)) throw typeErr(ctx, fn, 9, 'ArrayBufferView');
    if (!viewOk(type, view)) { setErr(ctx, GL_INVALID_OPERATION); return false; }
    var el = view.BYTES_PER_ELEMENT || 1;
    off = (off | 0) * el;
    if (off > view.byteLength) { setErr(ctx, GL_INVALID_VALUE); return false; }
    return toB64(new Uint8Array(view.buffer, view.byteOffset + off, view.byteLength - off));
  }
  method(B, 'texImage2D', function (target, level, ifmt, a3, a4, a5, a6, a7, a8, a9) {
    var n = arguments.length;
    target = u32(target); level = i32(level); ifmt = i32(ifmt);
    if (n === 6) {
      var src = a6;
      if (!isTexSource(src)) { if (src === null || src === undefined) throw typeErr(this, 'texImage2D', 6, 'TexImageSource'); throw typeErr(this, 'texImage2D', 6, 'TexImageSource'); }
      var px = srcPixels(this, src);
      if (!px) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'texImage2D', target, level, ifmt, px.w, px.h, 0, u32(a3), u32(a4), px.b64, 0, true);
      return;
    }
    if (n < 9) throw new TypeError("Failed to execute 'texImage2D' on 'WebGLRenderingContext': 9 arguments required, but only " + n + ' present.');
    var w = i32(a3), h = i32(a4), border = i32(a5), format = u32(a6), type = u32(a7);
    if (this._v === 2 && n >= 9 && typeof a8 === 'number' ) { call(this._h, 'texImage2D', target, level, ifmt, w, h, border, format, type, null, a8, false); return; }
    if (n >= 9 && isTexSource(a8)) {
      if (this._v !== 2) throw typeErr(this, 'texImage2D', 9, 'ArrayBufferView');
      var px2 = srcPixels(this, a8); if (!px2) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'texImage2D', target, level, ifmt, w, h, border, format, type, px2.b64, 0, true); return;
    }
    var data = pixView(this, 'texImage2D', type, a8, this._v === 2 ? a9 : 0);
    if (data === false) return;
    call(this._h, 'texImage2D', target, level, ifmt, w, h, border, format, type, data, 0, false);
  }, undefined);
  method(B, 'texSubImage2D', function (target, level, x, y, a5, a6, a7, a8, a9, a10) {
    var n = arguments.length;
    target = u32(target); level = i32(level); x = i32(x); y = i32(y);
    if (n === 7) {
      var px = srcPixels(this, a7);
      if (!isTexSource(a7)) throw typeErr(this, 'texSubImage2D', 7, 'TexImageSource');
      if (!px) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'texSubImage2D', target, level, x, y, px.w, px.h, u32(a5), u32(a6), px.b64, 0, true);
      return;
    }
    if (n < 9) throw new TypeError("Failed to execute 'texSubImage2D' on 'WebGLRenderingContext': 9 arguments required, but only " + n + ' present.');
    var w = i32(a5), h = i32(a6), format = u32(a7), type = u32(a8);
    if (typeof a9 === 'number' && this._v === 2) { call(this._h, 'texSubImage2D', target, level, x, y, w, h, format, type, null, a9, false); return; }
    if (isTexSource(a9)) {
      var px2 = srcPixels(this, a9); if (!px2) { setErr(this, GL_INVALID_VALUE); return; }
      call(this._h, 'texSubImage2D', target, level, x, y, w, h, format, type, px2.b64, 0, true); return;
    }
    if (a9 === null || a9 === undefined) throw typeErr(this, 'texSubImage2D', 9, 'ArrayBufferView');
    var data = pixView(this, 'texSubImage2D', type, a9, this._v === 2 ? a10 : 0);
    if (data === false) return;
    call(this._h, 'texSubImage2D', target, level, x, y, w, h, format, type, data, 0, false);
  }, undefined);
  method(B, 'copyTexImage2D', function (t, l, f, x, y, w, h, b) { call(this._h, 'copyTexImage2D', u32(t), i32(l), u32(f), i32(x), i32(y), i32(w), i32(h), i32(b)); }, undefined);
  method(B, 'copyTexSubImage2D', function (t, l, xo, yo, x, y, w, h) { call(this._h, 'copyTexSubImage2D', u32(t), i32(l), i32(xo), i32(yo), i32(x), i32(y), i32(w), i32(h)); }, undefined);
  var compressedStub = function (name) { return function () { setErr(this, GL_INVALID_ENUM); }; };
  method(B, 'compressedTexImage2D', compressedStub(), undefined);
  method(B, 'compressedTexSubImage2D', compressedStub(), undefined);

  /* ---- renderbuffers & framebuffers ---- */
  function fbId(ctx, fn, n, f) { return oid(ctx, fn, n, f, WebGLFramebuffer, 'framebuffer'); }
  method(B, 'createRenderbuffer', function () { return reg(this, 'renderbuffer', call(this._h, 'createRenderbuffer')); }, function () { return stub(WebGLRenderbuffer, this); });
  method(B, 'deleteRenderbuffer', function (r) { var id = oid(this, 'deleteRenderbuffer', 1, r, WebGLRenderbuffer); if (id > 0) { call(this._h, 'deleteRenderbuffer', id); r._del = true; unreg(this, 'renderbuffer', id); } }, undefined);
  method(B, 'isRenderbuffer', function (r) { return r instanceof WebGLRenderbuffer && r._ctx === this && !r._del && call(this._h, 'isRenderbuffer', r._id); }, false);
  method(B, 'bindRenderbuffer', function (t, r) { var id = oid(this, 'bindRenderbuffer', 2, r, WebGLRenderbuffer); if (id < 0) return; call(this._h, 'bindRenderbuffer', u32(t), id); }, undefined);
  method(B, 'renderbufferStorage', function (t, f, w, h) { call(this._h, 'renderbufferStorage', u32(t), u32(f), i32(w), i32(h), 0); }, undefined);
  method(B, 'getRenderbufferParameter', function (t, p) { var r = call(this._h, 'getRenderbufferParameter', u32(t), u32(p)); return r === undefined ? null : r; }, null);
  method(B, 'createFramebuffer', function () { return reg(this, 'framebuffer', call(this._h, 'createFramebuffer')); }, function () { return stub(WebGLFramebuffer, this); });
  method(B, 'deleteFramebuffer', function (f) { var id = fbId(this, 'deleteFramebuffer', 1, f); if (id > 0) { call(this._h, 'deleteFramebuffer', id); f._del = true; unreg(this, 'framebuffer', id); } }, undefined);
  method(B, 'isFramebuffer', function (f) { return f instanceof WebGLFramebuffer && f._ctx === this && !f._del && call(this._h, 'isFramebuffer', f._id); }, false);
  method(B, 'bindFramebuffer', function (t, f) { var id = fbId(this, 'bindFramebuffer', 2, f); if (id < 0) return; call(this._h, 'bindFramebuffer', u32(t), id); }, undefined);
  method(B, 'framebufferTexture2D', function (t, a, tt, tex, level) {
    var id = texId(this, 'framebufferTexture2D', 4, tex); if (id < 0) return;
    call(this._h, 'framebufferTexture2D', u32(t), u32(a), u32(tt), id, i32(level));
  }, undefined);
  method(B, 'framebufferRenderbuffer', function (t, a, rt, r) {
    var id = oid(this, 'framebufferRenderbuffer', 4, r, WebGLRenderbuffer); if (id < 0) return;
    call(this._h, 'framebufferRenderbuffer', u32(t), u32(a), u32(rt), id);
  }, undefined);
  method(B, 'checkFramebufferStatus', function (t) { return call(this._h, 'checkFramebufferStatus', u32(t)); }, 0x8CDD);
  method(B, 'getFramebufferAttachmentParameter', function (t, a, p) {
    var r = call(this._h, 'getFramebufferAttachmentParameter', u32(t), u32(a), u32(p));
    if (r === undefined || r === null) return null;
    p = u32(p);
    if (p === 0x8CD1) {
      var ty = call(this._h, 'getFramebufferAttachmentParameter', u32(t), u32(a), 0x8CD0);
      return objFor(this, ty === 0x1702 ? 'texture' : 'renderbuffer', r);
    }
    return r;
  }, null);

  method(B, 'readPixels', function (x, y, w, h, format, type, dst, dstOff) {
    var self = this;
    format = u32(format); type = u32(type);
    if (this._v === 2 && typeof dst === 'number') { call(this._h, 'readPixels', i32(x), i32(y), i32(w), i32(h), format, type, dst); return; }
    if (dst === null || dst === undefined) { setErr(this, GL_INVALID_VALUE); return; }
    if (!ArrayBuffer.isView(dst)) throw typeErr(this, 'readPixels', 7, 'ArrayBufferView');
    var types = TYPE_VIEW[type];
    if (types && !viewOk(type, dst)) { setErr(this, GL_INVALID_OPERATION); return; }
    var r = call(this._h, 'readPixels', i32(x), i32(y), i32(w), i32(h), format, type, 0);
    if (!r) return;
    var bytes = fromB64(r);
    var el = dst.BYTES_PER_ELEMENT || 1;
    var off = ((dstOff | 0) * el);
    if (off + bytes.length > dst.byteLength) { setErr(this, GL_INVALID_OPERATION); return; }
    new Uint8Array(dst.buffer, dst.byteOffset + off, bytes.length).set(bytes);
  }, undefined);

  /* ---- draw ---- */
  function drawCheck(ctx) { return true; }
  method(B, 'drawArrays', function (mode, first, count) { call(this._h, 'drawArrays', u32(mode), i32(first), i32(count)); touch(this); }, undefined);
  method(B, 'drawElements', function (mode, count, type, off) { call(this._h, 'drawElements', u32(mode), i32(count), u32(type), +off); touch(this); }, undefined);

  /* ---- WebGL2 only ---- */
  method(V2, 'drawArraysInstanced', function (mode, first, count, n) { call(this._h, 'drawArraysInstanced', u32(mode), i32(first), i32(count), i32(n)); touch(this); }, undefined);
  method(V2, 'drawElementsInstanced', function (mode, count, type, off, n) { call(this._h, 'drawElementsInstanced', u32(mode), i32(count), u32(type), +off, i32(n)); touch(this); }, undefined);
  method(V2, 'drawRangeElements', function (mode, s, e, count, type, off) { if (e < s) { setErr(this, GL_INVALID_VALUE); return; } call(this._h, 'drawElements', u32(mode), i32(count), u32(type), +off); touch(this); }, undefined);
  method(V2, 'vertexAttribDivisor', function (i, d) { call(this._h, 'vertexAttribDivisor', u32(i), u32(d)); }, undefined);
  method(V2, 'vertexAttribIPointer', function (i, size, type, stride, off) { call(this._h, 'vertexAttribIPointer', u32(i), i32(size), u32(type), i32(stride), +off); }, undefined);
  method(V2, 'vertexAttribI4i', function (i, x, y, z, w) { call(this._h, 'vertexAttrib', u32(i), 'i', x | 0, y | 0, z | 0, w | 0); }, undefined);
  method(V2, 'vertexAttribI4ui', function (i, x, y, z, w) { call(this._h, 'vertexAttrib', u32(i), 'u', x >>> 0, y >>> 0, z >>> 0, w >>> 0); }, undefined);
  method(V2, 'vertexAttribI4iv', function (i, v) { call(this._h, 'vertexAttrib', u32(i), 'i', v[0] | 0, v[1] | 0, v[2] | 0, v[3] | 0); }, undefined);
  method(V2, 'vertexAttribI4uiv', function (i, v) { call(this._h, 'vertexAttrib', u32(i), 'u', v[0] >>> 0, v[1] >>> 0, v[2] >>> 0, v[3] >>> 0); }, undefined);
  method(V2, 'drawBuffers', function (b) { call(this._h, 'drawBuffers', Array.prototype.map.call(b, u32)); }, undefined);
  method(V2, 'readBuffer', function (s) { call(this._h, 'readBuffer', u32(s)); }, undefined);
  method(V2, 'renderbufferStorageMultisample', function (t, s, f, w, h) { call(this._h, 'renderbufferStorage', u32(t), u32(f), i32(w), i32(h), i32(s) || 0); }, undefined);
  method(V2, 'blitFramebuffer', function (a, b, c, d, e, f, g, h, mask, filter) { call(this._h, 'blitFramebuffer', i32(a), i32(b), i32(c), i32(d), i32(e), i32(f), i32(g), i32(h), u32(mask), u32(filter)); touch(this); }, undefined);
  method(V2, 'framebufferTextureLayer', function (t, a, tex, level, layer) { var id = texId(this, 'framebufferTextureLayer', 3, tex); if (id < 0) return; call(this._h, 'framebufferTextureLayer', u32(t), u32(a), id, i32(level), i32(layer)); }, undefined);
  method(V2, 'invalidateFramebuffer', function (t, atts) { call(this._h, 'invalidateFramebuffer', u32(t), Array.prototype.map.call(atts, u32)); }, undefined);
  method(V2, 'invalidateSubFramebuffer', function (t, atts) { call(this._h, 'invalidateFramebuffer', u32(t), Array.prototype.map.call(atts, u32)); }, undefined);
  method(V2, 'getInternalformatParameter', function (t, f, p) { var r = call(this._h, 'getInternalformatParameter', u32(t), u32(f), u32(p)); return r ? new Int32Array(r) : null; }, null);
  method(V2, 'clearBufferfv', function (b, i, v, off) { off = off | 0; call(this._h, 'clearBuffer', 'f', u32(b), i32(i), Array.prototype.slice.call(v, off, off + 4)); touch(this); }, undefined);
  method(V2, 'clearBufferiv', function (b, i, v, off) { off = off | 0; call(this._h, 'clearBuffer', 'i', u32(b), i32(i), Array.prototype.slice.call(v, off, off + 4)); touch(this); }, undefined);
  method(V2, 'clearBufferuiv', function (b, i, v, off) { off = off | 0; call(this._h, 'clearBuffer', 'u', u32(b), i32(i), Array.prototype.slice.call(v, off, off + 4)); touch(this); }, undefined);
  method(V2, 'clearBufferfi', function (b, i, d, s) { call(this._h, 'clearBuffer', 'fi', u32(b), i32(i), +d, s | 0); touch(this); }, undefined);

  // 3D textures
  method(V2, 'texStorage2D', function (t, l, f, w, h) { call(this._h, 'texStorage', u32(t), i32(l), u32(f), i32(w), i32(h), 1); }, undefined);
  method(V2, 'texStorage3D', function (t, l, f, w, h, d) { call(this._h, 'texStorage', u32(t), i32(l), u32(f), i32(w), i32(h), i32(d)); }, undefined);
  method(V2, 'texImage3D', function (target, level, ifmt, w, h, d, border, format, type, src, srcOff) {
    format = u32(format); type = u32(type);
    if (typeof src === 'number') { call(this._h, 'texImage3D', u32(target), i32(level), u32(ifmt), i32(w), i32(h), i32(d), i32(border), format, type, null, src); return; }
    if (isTexSource(src)) { var px = srcPixels(this, src); if (!px) { setErr(this, GL_INVALID_VALUE); return; } call(this._h, 'texImage3D', u32(target), i32(level), u32(ifmt), px.w, px.h, i32(d), i32(border), format, type, px.b64, 0); return; }
    var data = pixView(this, 'texImage3D', type, src, srcOff);
    if (data === false) return;
    call(this._h, 'texImage3D', u32(target), i32(level), u32(ifmt), i32(w), i32(h), i32(d), i32(border), format, type, data, 0);
  }, undefined);
  method(V2, 'texSubImage3D', function (target, level, x, y, z, w, h, d, format, type, src, srcOff) {
    format = u32(format); type = u32(type);
    if (typeof src === 'number') { call(this._h, 'texSubImage3D', u32(target), i32(level), i32(x), i32(y), i32(z), i32(w), i32(h), i32(d), format, type, null, src, false); return; }
    if (isTexSource(src)) { var px = srcPixels(this, src); if (!px) { setErr(this, GL_INVALID_VALUE); return; } call(this._h, 'texSubImage3D', u32(target), i32(level), i32(x), i32(y), i32(z), px.w, px.h, i32(d), format, type, px.b64, 0, true); return; }
    var data = pixView(this, 'texSubImage3D', type, src, srcOff);
    if (data === false || data === null) { if (data === null) setErr(this, GL_INVALID_VALUE); return; }
    call(this._h, 'texSubImage3D', u32(target), i32(level), i32(x), i32(y), i32(z), i32(w), i32(h), i32(d), format, type, data, 0, false);
  }, undefined);
  method(V2, 'copyTexSubImage3D', function (t, l, xo, yo, zo, x, y, w, h) { call(this._h, 'copyTexSubImage3D', u32(t), i32(l), i32(xo), i32(yo), i32(zo), i32(x), i32(y), i32(w), i32(h)); }, undefined);
  method(V2, 'compressedTexImage3D', compressedStub(), undefined);
  method(V2, 'compressedTexSubImage3D', compressedStub(), undefined);

  // buffers
  method(V2, 'copyBufferSubData', function (r, w, ro, wo, s) { call(this._h, 'copyBufferSubData', u32(r), u32(w), +ro, +wo, +s); }, undefined);
  method(V2, 'getBufferSubData', function (t, off, dst, dstOff, len) {
    if (!ArrayBuffer.isView(dst) && !(dst instanceof ArrayBuffer)) throw typeErr(this, 'getBufferSubData', 3, 'ArrayBufferView');
    var el = ArrayBuffer.isView(dst) ? (dst.BYTES_PER_ELEMENT || 1) : 1;
    var total = dst.byteLength;
    var o = (dstOff | 0) * el, n = len ? (len | 0) * el : total - o;
    if (o + n > total || n < 0) { setErr(this, GL_INVALID_VALUE); return; }
    var r = call(this._h, 'getBufferSubData', u32(t), +off, n);
    if (!r) return;
    var u8 = ArrayBuffer.isView(dst) ? new Uint8Array(dst.buffer, dst.byteOffset, dst.byteLength) : new Uint8Array(dst);
    u8.set(fromB64(r), o);
  }, undefined);
  method(V2, 'bindBufferBase', function (t, i, b) { var id = bufId(this, 'bindBufferBase', 3, b); if (id < 0) return; call(this._h, 'bindBufferBase', u32(t), u32(i), id); }, undefined);
  method(V2, 'bindBufferRange', function (t, i, b, off, size) { var id = bufId(this, 'bindBufferRange', 3, b); if (id < 0) return; call(this._h, 'bindBufferRange', u32(t), u32(i), id, +off, +size); }, undefined);
  method(V2, 'getIndexedParameter', function (p, i) { return decodeVal(this, call(this._h, 'getIndexedParameter', u32(p), u32(i))); }, null);

  // uniform blocks
  method(V2, 'getUniformBlockIndex', function (p, n) { var a = oid(this, 'getUniformBlockIndex', 1, p, WebGLProgram); if (a <= 0) return 0xFFFFFFFF; return call(this._h, 'getUniformBlockIndex', a, String(n)); }, 0xFFFFFFFF);
  method(V2, 'getActiveUniformBlockParameter', function (p, i, pn) { var a = oid(this, 'getActiveUniformBlockParameter', 1, p, WebGLProgram); if (a <= 0) return null; return decodeVal(this, call(this._h, 'getActiveUniformBlockParameter', a, u32(i), u32(pn))); }, null);
  method(V2, 'getActiveUniformBlockName', function (p, i) { var a = oid(this, 'getActiveUniformBlockName', 1, p, WebGLProgram); if (a <= 0) return null; return call(this._h, 'getActiveUniformBlockName', a, u32(i)); }, null);
  method(V2, 'uniformBlockBinding', function (p, i, b) { var a = oid(this, 'uniformBlockBinding', 1, p, WebGLProgram); if (a > 0) call(this._h, 'uniformBlockBinding', a, u32(i), u32(b)); }, undefined);
  method(V2, 'getUniformIndices', function (p, names) { var a = oid(this, 'getUniformIndices', 1, p, WebGLProgram); if (a <= 0) return null; return call(this._h, 'getUniformIndices', a, Array.prototype.map.call(names, String)); }, null);
  method(V2, 'getActiveUniforms', function (p, idx, pn) {
    var a = oid(this, 'getActiveUniforms', 1, p, WebGLProgram); if (a <= 0) return null;
    var r = call(this._h, 'getActiveUniforms', a, Array.prototype.map.call(idx, u32), u32(pn)); if (!r) return null;
    pn = u32(pn);
    if (pn === 0x8A3E) return r.map(function (x) { return !!x; });
    return r;
  }, null);
  method(V2, 'getFragDataLocation', function (p, n) { var a = oid(this, 'getFragDataLocation', 1, p, WebGLProgram); if (a <= 0) return -1; return call(this._h, 'getFragDataLocation', a, String(n)); }, -1);

  // VAOs (core in WebGL2)
  function vaoMethods(protos, sfx) {
    method(protos, 'createVertexArray' + sfx, function () { return reg(this, 'vao', call(this._h, 'createVertexArray')); }, function () { return stub(WebGLVertexArrayObject, this); });
    method(protos, 'deleteVertexArray' + sfx, function (v) { var id = oid(this, 'deleteVertexArray', 1, v, WebGLVertexArrayObject); if (id > 0) { call(this._h, 'deleteVertexArray', id); v._del = true; unreg(this, 'vao', id); } }, undefined);
    method(protos, 'isVertexArray' + sfx, function (v) { return v instanceof WebGLVertexArrayObject && v._ctx === this && !v._del && call(this._h, 'isVertexArray', v._id); }, false);
    method(protos, 'bindVertexArray' + sfx, function (v) { var id = oid(this, 'bindVertexArray', 1, v, WebGLVertexArrayObject); if (id < 0) return; call(this._h, 'bindVertexArray', id); }, undefined);
  }
  vaoMethods(V2, '');

  // samplers
  method(V2, 'createSampler', function () { return reg(this, 'sampler', call(this._h, 'createSampler')); }, function () { return stub(WebGLSampler, this); });
  method(V2, 'deleteSampler', function (s) { var id = oid(this, 'deleteSampler', 1, s, WebGLSampler); if (id > 0) { call(this._h, 'deleteSampler', id); s._del = true; unreg(this, 'sampler', id); } }, undefined);
  method(V2, 'isSampler', function (s) { return s instanceof WebGLSampler && s._ctx === this && !s._del && call(this._h, 'isSampler', s._id); }, false);
  method(V2, 'bindSampler', function (u, s) { var id = oid(this, 'bindSampler', 2, s, WebGLSampler); if (id < 0) return; call(this._h, 'bindSampler', u32(u), id); }, undefined);
  method(V2, 'samplerParameteri', function (s, p, v) { var id = oid(this, 'samplerParameteri', 1, s, WebGLSampler); if (id > 0) call(this._h, 'samplerParameter', id, u32(p), +v); }, undefined);
  method(V2, 'samplerParameterf', function (s, p, v) { var id = oid(this, 'samplerParameterf', 1, s, WebGLSampler); if (id > 0) call(this._h, 'samplerParameter', id, u32(p), +v); }, undefined);
  method(V2, 'getSamplerParameter', function (s, p) { var id = oid(this, 'getSamplerParameter', 1, s, WebGLSampler); if (id <= 0) return null; var r = call(this._h, 'getSamplerParameter', id, u32(p)); return r === undefined ? null : r; }, null);

  // queries
  method(V2, 'createQuery', function () { return reg(this, 'query', call(this._h, 'createQuery')); }, function () { return stub(WebGLQuery, this); });
  method(V2, 'deleteQuery', function (q) { var id = oid(this, 'deleteQuery', 1, q, WebGLQuery); if (id > 0) { call(this._h, 'deleteQuery', id); q._del = true; unreg(this, 'query', id); } }, undefined);
  method(V2, 'isQuery', function (q) { return q instanceof WebGLQuery && q._ctx === this && !q._del && call(this._h, 'isQuery', q._id); }, false);
  method(V2, 'beginQuery', function (t, q) { var id = oid(this, 'beginQuery', 2, q, WebGLQuery); if (id <= 0) return; call(this._h, 'beginQuery', u32(t), id); }, undefined);
  method(V2, 'endQuery', function (t) { call(this._h, 'endQuery', u32(t)); touch(this); }, undefined);
  method(V2, 'getQuery', function (t, p) { var r = call(this._h, 'getQuery', u32(t), u32(p)); return r ? reg(this, 'query', r) : null; }, null);
  method(V2, 'getQueryParameter', function (q, p) {
    var id = oid(this, 'getQueryParameter', 1, q, WebGLQuery); if (id <= 0) return null;
    var r = call(this._h, 'getQueryParameter', id, u32(p)); if (r === undefined || r === null) return null;
    return u32(p) === 0x8867 ? !!r : r;
  }, null);

  // sync
  method(V2, 'fenceSync', function (cond, flags) { if (u32(cond) !== 0x9117 || flags !== 0) { setErr(this, GL_INVALID_ENUM); return null; } touch(this); return reg(this, 'sync', call(this._h, 'fenceSync')); }, null);
  method(V2, 'isSync', function (s) { return s instanceof WebGLSync && s._ctx === this && !s._del && call(this._h, 'isSync', s._id); }, false);
  method(V2, 'deleteSync', function (s) { var id = oid(this, 'deleteSync', 1, s, WebGLSync); if (id > 0) { call(this._h, 'deleteSync', id); s._del = true; } }, undefined);
  method(V2, 'clientWaitSync', function (s, flags, t) { var id = oid(this, 'clientWaitSync', 1, s, WebGLSync); if (id <= 0) return 0x911D; return call(this._h, 'clientWaitSync', id); }, 0x911D);
  method(V2, 'waitSync', function (s, flags, t) { oid(this, 'waitSync', 1, s, WebGLSync); }, undefined);
  method(V2, 'getSyncParameter', function (s, p) {
    var id = oid(this, 'getSyncParameter', 1, s, WebGLSync); if (id <= 0) return null;
    p = u32(p);
    switch (p) { case 0x9112: return 0x9116; case 0x9113: return 0x9117; case 0x9114: return call(this._h, 'syncSignaled', id) ? 0x9119 : 0x9118; case 0x9115: return 0; }
    setErr(this, GL_INVALID_ENUM); return null;
  }, null);

  // transform feedback
  method(V2, 'createTransformFeedback', function () { return reg(this, 'tf', call(this._h, 'createTransformFeedback')); }, function () { return stub(WebGLTransformFeedback, this); });
  method(V2, 'deleteTransformFeedback', function (t) { var id = oid(this, 'deleteTransformFeedback', 1, t, WebGLTransformFeedback); if (id > 0) { call(this._h, 'deleteTransformFeedback', id); t._del = true; unreg(this, 'tf', id); } }, undefined);
  method(V2, 'isTransformFeedback', function (t) { return t instanceof WebGLTransformFeedback && t._ctx === this && !t._del && call(this._h, 'isTransformFeedback', t._id); }, false);
  method(V2, 'bindTransformFeedback', function (t, f) { var id = oid(this, 'bindTransformFeedback', 2, f, WebGLTransformFeedback); if (id < 0) return; if (u32(t) !== 0x8E22) { setErr(this, GL_INVALID_ENUM); return; } call(this._h, 'bindTransformFeedback', id); }, undefined);
  method(V2, 'beginTransformFeedback', function (m) { call(this._h, 'beginTransformFeedback', u32(m)); }, undefined);
  method(V2, 'endTransformFeedback', function () { call(this._h, 'endTransformFeedback'); }, undefined);
  method(V2, 'pauseTransformFeedback', function () { call(this._h, 'pauseTransformFeedback'); }, undefined);
  method(V2, 'resumeTransformFeedback', function () { call(this._h, 'resumeTransformFeedback'); }, undefined);
  method(V2, 'transformFeedbackVaryings', function (p, v, m) { var a = oid(this, 'transformFeedbackVaryings', 1, p, WebGLProgram); if (a > 0) call(this._h, 'transformFeedbackVaryings', a, Array.prototype.map.call(v, String), u32(m)); }, undefined);
  method(V2, 'getTransformFeedbackVarying', function (p, i) { var a = oid(this, 'getTransformFeedbackVarying', 1, p, WebGLProgram); if (a <= 0) return null; return activeInfo(call(this._h, 'getTransformFeedbackVarying', a, u32(i))); }, null);

  /* ---- extensions ---- */
  function listFor(ctx) { return ctx._v === 2 ? EXT2 : EXT1; }
  method(B, 'getSupportedExtensions', function () { return listFor(this).slice(); }, null);
  function Ext(name, init) {
    var C = function () { illegal(); };
    Object.defineProperty(C, 'name', { value: name });
    tag(C, name);
    init(C.prototype);
    return Object.create(C.prototype);
  }
  function consts(o, names) { names.forEach(function (n) { var v = K.ext[n]; if (v === undefined) v = K.c1[n]; if (v === undefined) v = K.c2[n]; Object.defineProperty(o, n, { value: v, enumerable: true }); }); }
  function mkExt(ctx, name) {
    var h = ctx;
    switch (name) {
      case 'ANGLE_instanced_arrays': return Ext('ANGLEInstancedArrays', function (o) {
        consts(o, ['VERTEX_ATTRIB_ARRAY_DIVISOR_ANGLE']);
        def(o, 'drawArraysInstancedANGLE', function (m, f, c, n) { if (h._lost) return; call(h._h, 'drawArraysInstanced', u32(m), i32(f), i32(c), i32(n)); touch(h); });
        def(o, 'drawElementsInstancedANGLE', function (m, c, t, off, n) { if (h._lost) return; call(h._h, 'drawElementsInstanced', u32(m), i32(c), u32(t), +off, i32(n)); touch(h); });
        def(o, 'vertexAttribDivisorANGLE', function (i, d) { if (h._lost) return; call(h._h, 'vertexAttribDivisor', u32(i), u32(d)); });
      });
      case 'EXT_blend_minmax': return Ext('EXTBlendMinMax', function (o) { consts(o, ['MIN_EXT', 'MAX_EXT']); });
      case 'EXT_color_buffer_half_float': return Ext('EXTColorBufferHalfFloat', function (o) { consts(o, ['RGBA16F_EXT', 'RGB16F_EXT', 'FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE_EXT', 'UNSIGNED_NORMALIZED_EXT']); });
      case 'EXT_color_buffer_float': return Ext('EXTColorBufferFloat', function (o) { });
      case 'EXT_float_blend': return Ext('EXTFloatBlend', function (o) { });
      case 'EXT_frag_depth': return Ext('EXTFragDepth', function (o) { });
      case 'EXT_shader_texture_lod': return Ext('EXTShaderTextureLOD', function (o) { });
      case 'EXT_texture_filter_anisotropic': return Ext('EXTTextureFilterAnisotropic', function (o) { consts(o, ['TEXTURE_MAX_ANISOTROPY_EXT', 'MAX_TEXTURE_MAX_ANISOTROPY_EXT']); });
      case 'OES_element_index_uint': return Ext('OESElementIndexUint', function (o) { });
      case 'OES_standard_derivatives': return Ext('OESStandardDerivatives', function (o) { consts(o, ['FRAGMENT_SHADER_DERIVATIVE_HINT_OES']); });
      case 'OES_texture_float': return Ext('OESTextureFloat', function (o) { });
      case 'OES_texture_float_linear': return Ext('OESTextureFloatLinear', function (o) { });
      case 'OES_texture_half_float': return Ext('OESTextureHalfFloat', function (o) { consts(o, ['HALF_FLOAT_OES']); });
      case 'OES_texture_half_float_linear': return Ext('OESTextureHalfFloatLinear', function (o) { });
      case 'OES_vertex_array_object': return Ext('OESVertexArrayObject', function (o) {
        consts(o, ['VERTEX_ARRAY_BINDING_OES']);
        def(o, 'createVertexArrayOES', function () { if (h._lost) return null; return reg(h, 'vao', call(h._h, 'createVertexArray')); });
        def(o, 'deleteVertexArrayOES', function (v) { var id = oid(h, 'deleteVertexArrayOES', 1, v, WebGLVertexArrayObject); if (id > 0) { call(h._h, 'deleteVertexArray', id); v._del = true; unreg(h, 'vao', id); } });
        def(o, 'isVertexArrayOES', function (v) { return v instanceof WebGLVertexArrayObject && v._ctx === h && !v._del && call(h._h, 'isVertexArray', v._id); });
        def(o, 'bindVertexArrayOES', function (v) { var id = oid(h, 'bindVertexArrayOES', 1, v, WebGLVertexArrayObject); if (id < 0) return; call(h._h, 'bindVertexArray', id); });
      });
      case 'WEBGL_color_buffer_float': return Ext('WEBGLColorBufferFloat', function (o) { consts(o, ['RGBA32F_EXT', 'FRAMEBUFFER_ATTACHMENT_COMPONENT_TYPE_EXT', 'UNSIGNED_NORMALIZED_EXT']); });
      case 'WEBGL_debug_renderer_info': return Ext('WEBGLDebugRendererInfo', function (o) { consts(o, ['UNMASKED_VENDOR_WEBGL', 'UNMASKED_RENDERER_WEBGL']); });
      case 'WEBGL_debug_shaders': return Ext('WEBGLDebugShaders', function (o) {
        def(o, 'getTranslatedShaderSource', function (s) { var id = oid(h, 'getTranslatedShaderSource', 1, s, WebGLShader); if (id <= 0) return null; return call(h._h, 'getTranslatedShaderSource', id); });
      });
      case 'WEBGL_depth_texture': return Ext('WEBGLDepthTexture', function (o) { consts(o, ['UNSIGNED_INT_24_8_WEBGL']); });
      case 'WEBGL_draw_buffers': return Ext('WEBGLDrawBuffers', function (o) {
        var names = ['MAX_DRAW_BUFFERS_WEBGL', 'MAX_COLOR_ATTACHMENTS_WEBGL'];
        Object.defineProperty(o, 'MAX_COLOR_ATTACHMENTS_WEBGL', { value: 0x8CDF, enumerable: true });
        Object.defineProperty(o, 'MAX_DRAW_BUFFERS_WEBGL', { value: 0x8824, enumerable: true });
        for (var i = 0; i < 16; i++) {
          Object.defineProperty(o, 'COLOR_ATTACHMENT' + i + '_WEBGL', { value: 0x8CE0 + i, enumerable: true });
          Object.defineProperty(o, 'DRAW_BUFFER' + i + '_WEBGL', { value: 0x8825 + i, enumerable: true });
        }
        def(o, 'drawBuffersWEBGL', function (b) { if (h._lost) return; call(h._h, 'drawBuffers', Array.prototype.map.call(b, u32)); });
      });
      case 'WEBGL_lose_context': return Ext('WEBGLLoseContext', function (o) {
        def(o, 'loseContext', function () { loseContext(h); });
        def(o, 'restoreContext', function () { restoreContext(h); });
      });
    }
    return null;
  }
  var AFTER_ENABLE = { OES_standard_derivatives: 1, EXT_frag_depth: 1, EXT_shader_texture_lod: 1, WEBGL_draw_buffers: 1 };
  method(B, 'getExtension', function (name) {
    name = String(name);
    var lname = name.toLowerCase(), real = null;
    var list = listFor(this);
    for (var i = 0; i < list.length; i++) if (list[i].toLowerCase() === lname) { real = list[i]; break; }
    if (!real) return null;
    if (this._exts[real]) return this._exts[real];
    var e = mkExt(this, real);
    if (!e) return null;
    call(this._h, 'enableExt', real);
    if (real === 'OES_texture_float') { /* float textures usable immediately */ }
    this._exts[real] = e;
    return e;
  }, null);

  /* ---- context loss ---- */
  function fire(ctx, type, cancelable) {
    var ev = new WebGLContextEvent(type, { cancelable: !!cancelable, bubbles: false });
    var target = ctx._canvas;
    try { target.dispatchEvent(ev); } catch (e) { }
    return ev;
  }
  function loseContext(ctx) {
    if (ctx._lost) { return; }
    ctx._lost = true; ctx._lostReported = false; ctx._restorable = false;
    call(ctx._h, 'lose');
    global.setTimeout(function () {
      var ev = fire(ctx, 'webglcontextlost', true);
      if (ev.defaultPrevented) ctx._restorable = true;
    }, 0);
  }
  function restoreContext(ctx) {
    if (!ctx._lost || !ctx._restorable) {
      if (ctx._lost && !ctx._restorable) { /* INVALID_OPERATION is generated per spec; no state to flag */ }
      return;
    }
    global.setTimeout(function () {
      var a = ctx._attrs, c = ctx._canvas;
      sw.glDestroy(ctx._h);
      var nh = sw.glCreate(ctx._v, a.alpha, a.depth, a.stencil, a.antialias, a.premultipliedAlpha, a.preserveDrawingBuffer, c.width, c.height, ctx._nodeId);
      ctx._h = nh; ctx._gen++; ctx._reg = {}; ctx._exts = {}; ctx._prog = null; ctx._lost = false; ctx._restorable = false;
      ctx._w = c.width; ctx._hh = c.height;
      fire(ctx, 'webglcontextrestored', false);
    }, 0);
  }

  /* ---- factories ---- */
  factories.webgl = factories['experimental-webgl'] = function (canvas, attrs) { return createContext(canvas, attrs, 1); };
  factories.webgl2 = function (canvas, attrs) { return createContext(canvas, attrs, 2); };

  var exp = {
    WebGLRenderingContext: WebGLRenderingContext, WebGL2RenderingContext: WebGL2RenderingContext, WebGLBuffer: WebGLBuffer, WebGLTexture: WebGLTexture,
    WebGLProgram: WebGLProgram, WebGLShader: WebGLShader, WebGLFramebuffer: WebGLFramebuffer, WebGLRenderbuffer: WebGLRenderbuffer,
    WebGLVertexArrayObject: WebGLVertexArrayObject, WebGLQuery: WebGLQuery, WebGLSampler: WebGLSampler, WebGLSync: WebGLSync,
    WebGLTransformFeedback: WebGLTransformFeedback, WebGLUniformLocation: WebGLUniformLocation, WebGLActiveInfo: WebGLActiveInfo,
    WebGLShaderPrecisionFormat: WebGLShaderPrecisionFormat, WebGLContextEvent: WebGLContextEvent
  };
  Object.keys(exp).forEach(function (k) { Object.defineProperty(global, k, { value: exp[k], writable: true, configurable: true, enumerable: false }); });
})(globalThis);
