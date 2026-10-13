/*
 * Cache Storage: caches (CacheStorage) and Cache.
 *
 * Entries live in internal/sitestore (metadata in memory, bodies on disk). A
 * body crosses the host boundary as a binary string (one character per byte,
 * every character <= \xFF). Vary is ignored when matching.
 */
(function (global, H) {
  'use strict';
  if (!H || typeof H.cachesOpen !== 'function') return;

  function mkErr(name, msg) { return new global.DOMException(msg || name, name); }
  function hostErr(e) {
    var m = e && typeof e.message === 'string' ? /^([A-Za-z]+Error): ([\s\S]*)$/.exec(e.message) : null;
    return m ? mkErr(m[1], m[2]) : e;
  }
  function host(name) {
    var args = Array.prototype.slice.call(arguments, 1);
    try { return H[name].apply(null, args); } catch (e) { throw hostErr(e); }
  }
  var TOKEN = {};
  function defMethods(proto, map) { Object.keys(map).forEach(function (k) { Object.defineProperty(proto, k, { value: map[k], writable: true, enumerable: true, configurable: true }); }); }
  function tag(C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); }
  function exportClass(C, name) { Object.defineProperty(C, 'name', { value: name, configurable: true }); Object.defineProperty(global, name, { value: C, writable: true, configurable: true, enumerable: false }); }

  function binStr(u8) {
    var s = '';
    for (var i = 0; i < u8.length; i += 8192) s += String.fromCharCode.apply(null, u8.subarray(i, Math.min(i + 8192, u8.length)));
    return s;
  }
  function strBin(s) {
    var u = new Uint8Array(s.length);
    for (var i = 0; i < s.length; i++) u[i] = s.charCodeAt(i);
    return u;
  }

  // the body of a Response/Request as a binary string, marking it used. Text goes through Go: the prelude's
  // TextEncoder builds a JS array per byte, which a megabyte body cannot afford.
  function bodyBin(r) {
    if (r._used) throw new TypeError('body is already used');
    r._used = true;
    if (r._rawBytes) return binStr(r._rawBytes);
    var b = r._body;
    if (b === null || b === undefined) return '';
    if (global.Blob && b instanceof global.Blob) return binStr(b._bytes);
    if (b instanceof ArrayBuffer) return binStr(new Uint8Array(b));
    if (ArrayBuffer.isView(b)) return binStr(new Uint8Array(b.buffer, b.byteOffset, b.byteLength));
    return H.utf8ToBin(r._text());
  }
  function pairs(h) {
    var out = [];
    h.forEach(function (v, k) { out.push([k, v]); });
    return out;
  }
  // bytes: a Uint8Array, or a binary string (decoded to bytes here)
  function responseFrom(bytes, init) {
    var hdr = new global.Headers();
    (init.headers || []).forEach(function (p) { hdr.append(p[0], p[1]); });
    var st = init.status | 0;
    var r = new global.Response(null, { status: st >= 200 && st <= 599 ? st : 200, statusText: init.statusText || '', headers: hdr });
    var raw = typeof bytes === 'string' ? strBin(bytes) : bytes, text;
    // text() decodes on first use, so a big binary body that is only read as an ArrayBuffer costs nothing
    r._body = { toString: function () { return text === undefined ? (text = H.binToUtf8(binStr(raw))) : text; } };
    Object.defineProperty(r, '_rawBytes', { value: raw, writable: true });
    r.url = init.url || '';
    r.type = init.type || 'basic';
    return r;
  }
  // Responses built from bytes keep them when cloned
  (function () {
    var orig = global.Response.prototype.clone;
    Object.defineProperty(global.Response.prototype, 'clone', { value: function () {
      var c = orig.call(this);
      if (this._rawBytes) Object.defineProperty(c, '_rawBytes', { value: this._rawBytes, writable: true });
      return c;
    }, writable: true, configurable: true });
  })();
  Object.defineProperty(global, '__swResponseFromBytes', { value: responseFrom });
  Object.defineProperty(global, '__swBodyBin', { value: bodyBin });
  Object.defineProperty(global, '__swBinStr', { value: binStr });
  Object.defineProperty(global, '__swStrBin', { value: strBin });

  function reqOf(input) {
    if (input instanceof global.Request) return input;
    return new global.Request(String(input));
  }
  function httpURL(u) { return /^https?:\/\//i.test(u); }
  function queryOf(req, opts) {
    return [req.url, req.method, !!(opts && opts.ignoreSearch), !!(opts && opts.ignoreMethod)];
  }
  function requestFrom(m) {
    var h = new global.Headers();
    (m.reqHeaders || []).forEach(function (p) { h.append(p[0], p[1]); });
    return new global.Request(m.url, { method: m.method || 'GET', headers: h });
  }
  function responseOfMatch(m) {
    return responseFrom(m.body, { status: m.status, statusText: m.statusText, headers: m.resHeaders, url: m.resURL, type: m.type });
  }

  // ---- Cache -----------------------------------------------------------------------------
  function Cache(tok, name) {
    if (tok !== TOKEN) throw new TypeError('Illegal constructor');
    Object.defineProperty(this, '_name', { value: name });
  }
  function async_(f) {
    return new Promise(function (resolve, reject) { try { resolve(f()); } catch (e) { reject(e); } });
  }
  function putOne(name, request, response) {
    var req = reqOf(request);
    if (!httpURL(req.url)) throw new TypeError("Failed to execute 'put' on 'Cache': Request scheme '" + req.url.split(':')[0] + "' is unsupported");
    if (req.method !== 'GET') throw new TypeError("Failed to execute 'put' on 'Cache': Request method '" + req.method + "' is unsupported");
    if (response.status === 206) throw new TypeError("Failed to execute 'put' on 'Cache': Partial response (status code 206) is unsupported");
    var vary = response.headers.get('vary');
    if (vary && vary.split(',').some(function (v) { return v.trim() === '*'; })) throw new TypeError("Failed to execute 'put' on 'Cache': Vary header contains *");
    if (response._used) throw new TypeError("Failed to execute 'put' on 'Cache': Response body is already used");
    var bin = bodyBin(response);
    host('cachePut', name, {
      url: req.url, method: req.method, reqHeaders: pairs(req.headers), status: response.status, statusText: response.statusText,
      resHeaders: pairs(response.headers), type: response.type, resURL: response.url
    }, bin);
  }
  defMethods(Cache.prototype, {
    match: function (request, opts) {
      var name = this._name;
      return async_(function () {
        var m = host('cacheMatch', name, queryOf(reqOf(request), opts), false, false);
        return m.length ? responseOfMatch(m[0]) : undefined;
      });
    },
    matchAll: function (request, opts) {
      var name = this._name;
      return async_(function () {
        if (request === undefined) {
          return host('cacheMatch', name, ['', '', false, false], true, false).map(responseOfMatch);
        }
        return host('cacheMatch', name, queryOf(reqOf(request), opts), true, false).map(responseOfMatch);
      });
    },
    add: function (request) { return this.addAll([request]); },
    addAll: function (requests) {
      var self = this, name = this._name;
      return async_(function () {
        var reqs = Array.from(requests).map(reqOf);
        reqs.forEach(function (r) {
          if (!httpURL(r.url) || r.method !== 'GET') throw new TypeError("Failed to execute 'addAll' on 'Cache': Request " + (httpURL(r.url) ? 'method' : 'scheme') + ' is unsupported');
        });
        return Promise.all(reqs.map(function (r) { return global.fetch(r); })).then(function (resps) {
          resps.forEach(function (res) {
            if (!res.ok) throw new TypeError("Failed to execute 'addAll' on 'Cache': Request failed (status " + res.status + ')');
            if (res.status === 206) throw new TypeError("Failed to execute 'addAll' on 'Cache': Partial response (status code 206) is unsupported");
          });
          resps.forEach(function (res, i) { putOne(name, reqs[i], res); });
        });
      });
    },
    put: function (request, response) {
      var name = this._name;
      return async_(function () {
        if (!(response instanceof global.Response)) throw new TypeError("Failed to execute 'put' on 'Cache': parameter 2 is not of type 'Response'.");
        putOne(name, request, response);
      });
    },
    delete: function (request, opts) {
      var name = this._name;
      return async_(function () { return host('cacheDelete', name, queryOf(reqOf(request), opts)) > 0; });
    },
    keys: function (request, opts) {
      var name = this._name;
      return async_(function () {
        var q = request === undefined ? ['', '', false, false] : queryOf(reqOf(request), opts);
        return host('cacheKeys', name, q).map(requestFrom);
      });
    }
  });
  tag(Cache, 'Cache');
  exportClass(Cache, 'Cache');

  // ---- CacheStorage -------------------------------------------------------------------------------
  function CacheStorage(tok) { if (tok !== TOKEN) throw new TypeError('Illegal constructor'); }
  defMethods(CacheStorage.prototype, {
    open: function (name) {
      name = String(name);
      return async_(function () { host('cachesOpen', name); return new Cache(TOKEN, name); });
    },
    has: function (name) { name = String(name); return async_(function () { return !!host('cachesHas', name); }); },
    delete: function (name) { name = String(name); return async_(function () { return !!host('cachesDelete', name); }); },
    keys: function () { return async_(function () { return host('cachesKeys'); }); },
    match: function (request, opts) {
      return async_(function () {
        var named = opts && opts.cacheName !== undefined;
        var m = host('cacheMatch', named ? String(opts.cacheName) : '', queryOf(reqOf(request), opts), false, !named);
        return m.length ? responseOfMatch(m[0]) : undefined;
      });
    }
  });
  tag(CacheStorage, 'CacheStorage');
  exportClass(CacheStorage, 'CacheStorage');
  var storage = new CacheStorage(TOKEN);
  Object.defineProperty(global, 'caches', {
    get: function () { return H.idbAvail() && (!H.secureContext || H.secureContext()) ? storage : undefined; },
    set: function () { }, configurable: true, enumerable: true
  });
})(globalThis, globalThis.__sw);
