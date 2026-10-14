// crypto.subtle (Web Cryptography). The primitives are Go's standard library (platform_crypto.go, host function
// `webcrypto`); this module is the WebCrypto contract on top: algorithm normalisation, usages, extractability,
// CryptoKey objects and the DOMException names pages test for.
var sw = H.sw;
var NATIVE = typeof Uint8Array.fromBase64 === 'function' && typeof Uint8Array.prototype.toBase64 === 'function';
function toB64(u8) {
  if (NATIVE) return u8.toBase64();
  var s = '';
  for (var i = 0; i < u8.length; i += 0x6000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x6000));
  return btoa(s);
}
function fromB64(b) {
  if (NATIVE) return Uint8Array.fromBase64(b);
  var s = atob(b), u = new Uint8Array(s.length);
  for (var i = 0; i < s.length; i++) u[i] = s.charCodeAt(i);
  return u;
}
function b64uEnc(u8) { return toB64(u8).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, ''); }
function b64uDec(s) {
  if (typeof s !== 'string' || /[^A-Za-z0-9_-]/.test(s)) throw H.exc('The JWK member is not valid base64url.', 'DataError');
  s = s.replace(/-/g, '+').replace(/_/g, '/'); while (s.length % 4) s += '=';
  return fromB64(s);
}
function utf8(s) { return new TextEncoder().encode(s); }
function exc(m, n) { return H.exc(m, n); }
function call(op, args) {
  var r;
  try { r = sw.webcrypto(op, JSON.stringify(args)); } catch (e) { throw H.hostErr(e); }
  return JSON.parse(r);
}
function bytes(x, what) {
  if (x instanceof ArrayBuffer || (typeof SharedArrayBuffer !== 'undefined' && x instanceof SharedArrayBuffer)) return new Uint8Array(x.slice(0));
  if (ArrayBuffer.isView(x)) return new Uint8Array(x.buffer.slice(x.byteOffset, x.byteOffset + x.byteLength));
  throw new TypeError("Failed to execute '" + (what || 'crypto') + "' on 'SubtleCrypto': parameter is not of type 'BufferSource'.");
}
function enc(u8) { return toB64(u8); }
function out(b64) { var u = fromB64(b64); return u.buffer.byteLength === u.length ? u.buffer : u.buffer.slice(u.byteOffset, u.byteOffset + u.length); }

// ---- algorithm registry
var NAMES = {};
var HASHES = ['SHA-1', 'SHA-256', 'SHA-384', 'SHA-512'];
// op -> algorithms
var OPS = {
  digest: HASHES,
  sign: ['HMAC', 'ECDSA', 'Ed25519', 'RSASSA-PKCS1-v1_5', 'RSA-PSS'],
  verify: ['HMAC', 'ECDSA', 'Ed25519', 'RSASSA-PKCS1-v1_5', 'RSA-PSS'],
  encrypt: ['AES-GCM', 'AES-CBC', 'AES-CTR', 'RSA-OAEP'],
  decrypt: ['AES-GCM', 'AES-CBC', 'AES-CTR', 'RSA-OAEP'],
  deriveBits: ['ECDH', 'X25519', 'HKDF', 'PBKDF2'],
  wrapKey: ['AES-KW', 'AES-GCM', 'AES-CBC', 'AES-CTR', 'RSA-OAEP'],
  unwrapKey: ['AES-KW', 'AES-GCM', 'AES-CBC', 'AES-CTR', 'RSA-OAEP'],
  generateKey: ['AES-GCM', 'AES-CBC', 'AES-CTR', 'AES-KW', 'HMAC', 'ECDSA', 'ECDH', 'Ed25519', 'X25519', 'RSASSA-PKCS1-v1_5', 'RSA-PSS', 'RSA-OAEP'],
  importKey: ['AES-GCM', 'AES-CBC', 'AES-CTR', 'AES-KW', 'HMAC', 'ECDSA', 'ECDH', 'Ed25519', 'X25519', 'RSASSA-PKCS1-v1_5', 'RSA-PSS', 'RSA-OAEP', 'HKDF', 'PBKDF2'],
  getKeyLength: ['AES-GCM', 'AES-CBC', 'AES-CTR', 'AES-KW', 'HMAC', 'HKDF', 'PBKDF2']
};
Object.keys(OPS).forEach(function (op) { OPS[op].forEach(function (n) { NAMES[n.toLowerCase()] = n; }); });

function member(o, k, type) {
  var v = o[k];
  if (v === undefined) throw new TypeError("Failed to execute 'subtle': Failed to read the '" + k + "' property: Required member is undefined.");
  return v;
}
function hashName(h, req) {
  if (h === undefined) { if (req) throw new TypeError("Required member 'hash' is undefined."); return undefined; }
  var n = typeof h === 'string' ? h : (h && typeof h === 'object' ? h.name : undefined);
  if (typeof n !== 'string') throw new TypeError('The hash member is not a valid algorithm identifier.');
  var c = NAMES[n.toLowerCase()];
  if (!c || HASHES.indexOf(c) < 0) throw exc('Unrecognized name.', 'NotSupportedError');
  return c;
}
function normalize(alg, op) {
  var o;
  if (typeof alg === 'string') o = { name: alg };
  else if (alg && typeof alg === 'object') o = alg;
  else throw new TypeError("Algorithm: Unrecognized name.");
  if (typeof o.name !== 'string') throw new TypeError("Failed to execute 'subtle': Algorithm: name: Missing or not a string");
  var name = NAMES[o.name.toLowerCase()];
  if (!name || OPS[op].indexOf(name) < 0) throw exc('Unrecognized name.', 'NotSupportedError');
  var r = { name: name };
  switch (name) {
    case 'AES-GCM':
      if (op === 'encrypt' || op === 'decrypt' || op === 'wrapKey' || op === 'unwrapKey') {
        member(o, 'iv'); r.iv = bytes(o.iv);
        if (o.additionalData !== undefined) r.aad = bytes(o.additionalData);
        r.tagLength = o.tagLength === undefined ? 128 : o.tagLength >>> 0;
        if ([32, 64, 96, 104, 112, 120, 128].indexOf(r.tagLength) < 0) throw exc('Invalid tag length.', 'OperationError');
      } else if (op === 'generateKey' || op === 'importKey' || op === 'getKeyLength') { if (op !== 'importKey') r.length = len(o, 'AES'); }
      break;
    case 'AES-CBC':
      if (op === 'encrypt' || op === 'decrypt' || op === 'wrapKey' || op === 'unwrapKey') { member(o, 'iv'); r.iv = bytes(o.iv); if (r.iv.length !== 16) throw exc('The iv must be 16 bytes.', 'OperationError'); }
      else if (op === 'generateKey' || op === 'getKeyLength') r.length = len(o, 'AES');
      break;
    case 'AES-CTR':
      if (op === 'encrypt' || op === 'decrypt' || op === 'wrapKey' || op === 'unwrapKey') {
        member(o, 'counter'); r.counter = bytes(o.counter); member(o, 'length'); r.length = o.length >>> 0;
        if (r.counter.length !== 16) throw exc('The counter must be 16 bytes.', 'OperationError');
        if (r.length < 1 || r.length > 128) throw exc('The length must be between 1 and 128.', 'OperationError');
      } else if (op === 'generateKey' || op === 'getKeyLength') r.length = len(o, 'AES');
      break;
    case 'AES-KW':
      if (op === 'generateKey' || op === 'getKeyLength') r.length = len(o, 'AES');
      break;
    case 'HMAC':
      if (op === 'generateKey' || op === 'importKey') { r.hash = hashName(o.hash, true); if (o.length !== undefined) r.length = o.length >>> 0; }
      else if (op === 'getKeyLength') { if (o.length !== undefined) { r.length = o.length >>> 0; if (!r.length) throw new TypeError('Invalid HMAC key length.'); } r.hash = hashName(o.hash, true); }
      break;
    case 'ECDSA':
      if (op === 'sign' || op === 'verify') r.hash = hashName(o.hash, true);
      else if (op === 'generateKey' || op === 'importKey') r.namedCurve = curveOf(o);
      break;
    case 'ECDH':
      if (op === 'deriveBits') { member(o, 'public'); r.public = o.public; }
      else if (op === 'generateKey' || op === 'importKey') r.namedCurve = curveOf(o);
      break;
    case 'X25519':
      if (op === 'deriveBits') { member(o, 'public'); r.public = o.public; }
      break;
    case 'RSA-PSS':
      if (op === 'sign' || op === 'verify') { member(o, 'saltLength'); r.saltLength = o.saltLength >>> 0; }
      else if (op === 'generateKey' || op === 'importKey') rsaGen(o, r, op);
      break;
    case 'RSASSA-PKCS1-v1_5':
      if (op === 'generateKey' || op === 'importKey') rsaGen(o, r, op);
      break;
    case 'RSA-OAEP':
      if (op === 'encrypt' || op === 'decrypt' || op === 'wrapKey' || op === 'unwrapKey') r.label = o.label === undefined ? new Uint8Array(0) : bytes(o.label);
      else if (op === 'generateKey' || op === 'importKey') rsaGen(o, r, op);
      break;
    case 'HKDF':
      if (op === 'deriveBits') { r.hash = hashName(o.hash, true); member(o, 'salt'); r.salt = bytes(o.salt); member(o, 'info'); r.info = bytes(o.info); }
      break;
    case 'PBKDF2':
      if (op === 'deriveBits') { r.hash = hashName(o.hash, true); member(o, 'salt'); r.salt = bytes(o.salt); member(o, 'iterations'); r.iterations = o.iterations >>> 0; }
      break;
  }
  return r;
}
function len(o, what) {
  if (o.length === undefined) throw new TypeError("Failed to execute 'subtle': AesKeyGenParams: required member length is undefined.");
  var n = o.length >>> 0;
  if (n !== 128 && n !== 192 && n !== 256) throw exc('Invalid key length', 'OperationError');
  return n;
}
function curveOf(o) {
  if (o.namedCurve === undefined) throw new TypeError("Required member 'namedCurve' is undefined.");
  var c = String(o.namedCurve);
  if (c !== 'P-256' && c !== 'P-384' && c !== 'P-521') throw exc('Unrecognized namedCurve', 'NotSupportedError');
  return c;
}
function rsaGen(o, r, op) {
  r.hash = hashName(o.hash, true);
  if (op === 'generateKey') {
    member(o, 'modulusLength'); member(o, 'publicExponent');
    r.modulusLength = o.modulusLength >>> 0;
    r.publicExponent = bytes(o.publicExponent);
  }
}

// ---- CryptoKey
var slots = new WeakMap();
var KEYS = Symbol('key');
function CryptoKey() { throw new TypeError('Illegal constructor'); }
function makeKey(type, extractable, algorithm, usages, data) {
  var k = Object.create(CryptoKey.prototype);
  slots.set(k, { type: type, extractable: extractable, algorithm: algorithm, usages: usages.slice(), data: data });
  return k;
}
function slot(k, what) {
  var s = k && typeof k === 'object' ? slots.get(k) : null;
  if (!s) throw new TypeError("Failed to execute '" + what + "' on 'SubtleCrypto': parameter is not of type 'CryptoKey'.");
  return s;
}
H.attrs(CryptoKey.prototype, {
  type: function () { return slot(this, 'type').type; },
  extractable: function () { return slot(this, 'extractable').extractable; },
  algorithm: function () { var s = slot(this, 'algorithm'); return s.algObj || (s.algObj = Object.assign({}, s.algorithm)); },
  usages: function () { var s = slot(this, 'usages'); return s.usages.slice(); }
});
H.tag(CryptoKey, 'CryptoKey');
H.glob('CryptoKey', CryptoKey);

function CryptoKeyPair() { }

// structured clone and IndexedDB: a key is immutable, so a copy carries the same slots (see idb_prelude.js)
H.define(CryptoKey.prototype, Symbol.for('sweef.structuredClone'), function () {
  var s = slot(this, 'structuredClone'), a = Object.assign({}, s.algorithm);
  if (a.publicExponent) a.publicExponent = Array.from(a.publicExponent);
  return { tag: 'CryptoKey', data: { t: s.type, e: s.extractable, a: a, u: s.usages, s: typeof s.data === 'string', d: typeof s.data === 'string' ? s.data : toB64(s.data) } };
});
if (g.__swCloneTypes) g.__swCloneTypes.CryptoKey = function (p) {
  var a = p.a;
  if (a.publicExponent) a.publicExponent = new Uint8Array(a.publicExponent);
  return makeKey(p.t, p.e, a, p.u, p.s ? p.d : fromB64(p.d));
};

// ---- usages
var SYM_USAGES = { 'AES-GCM': ['encrypt', 'decrypt', 'wrapKey', 'unwrapKey'], 'AES-CBC': ['encrypt', 'decrypt', 'wrapKey', 'unwrapKey'], 'AES-CTR': ['encrypt', 'decrypt', 'wrapKey', 'unwrapKey'], 'AES-KW': ['wrapKey', 'unwrapKey'], HMAC: ['sign', 'verify'], HKDF: ['deriveKey', 'deriveBits'], PBKDF2: ['deriveKey', 'deriveBits'] };
var ASYM = {
  ECDSA: { priv: ['sign'], pub: ['verify'] },
  ECDH: { priv: ['deriveKey', 'deriveBits'], pub: [] },
  X25519: { priv: ['deriveKey', 'deriveBits'], pub: [] },
  Ed25519: { priv: ['sign'], pub: ['verify'] },
  'RSASSA-PKCS1-v1_5': { priv: ['sign'], pub: ['verify'] },
  'RSA-PSS': { priv: ['sign'], pub: ['verify'] },
  'RSA-OAEP': { priv: ['decrypt', 'unwrapKey'], pub: ['encrypt', 'wrapKey'] }
};
function kindOf(name) {
  if (name === 'ECDSA' || name === 'ECDH') return 'ec';
  if (name === 'X25519') return 'x25519';
  if (name === 'Ed25519') return 'ed25519';
  return 'rsa';
}
function usageList(u) {
  if (u === undefined || u === null || typeof u[Symbol.iterator] !== 'function') throw new TypeError("Failed to execute 'subtle': The provided value cannot be converted to a sequence.");
  var a = Array.from(u, String), seen = {};
  a.forEach(function (x) {
    if (['encrypt', 'decrypt', 'sign', 'verify', 'deriveKey', 'deriveBits', 'wrapKey', 'unwrapKey'].indexOf(x) < 0) throw new TypeError("Failed to execute 'subtle': The provided value '" + x + "' is not a valid enum value of type KeyUsage.");
  });
  return a.filter(function (x) { if (seen[x]) return false; seen[x] = 1; return true; });
}
function checkUsages(allowed, usages) {
  for (var i = 0; i < usages.length; i++) if (allowed.indexOf(usages[i]) < 0) throw exc('Unsupported key usage for this algorithm', 'SyntaxError');
}
function sorted(a) { return a.slice().sort(function (x, y) { var o = ['encrypt', 'decrypt', 'sign', 'verify', 'deriveKey', 'deriveBits', 'wrapKey', 'unwrapKey']; return o.indexOf(x) - o.indexOf(y); }); }

function hmacDefaultLen(h) { return h === 'SHA-384' || h === 'SHA-512' ? 1024 : 512; }

function algDescriptor(n, extra) { var a = { name: n.name }; if (extra) Object.keys(extra).forEach(function (k) { a[k] = extra[k]; }); return a; }
function rsaAlg(n, bits, expU8) {
  var a = { name: n.name, modulusLength: bits, publicExponent: expU8, hash: { name: n.hash } };
  return a;
}

// ---- operations
function needKey(key, what, usage, algName) {
  var s = slot(key, what);
  if (algName && s.algorithm.name !== algName) throw exc('The requested operation is not valid for the provided key', 'InvalidAccessError');
  if (usage && s.usages.indexOf(usage) < 0) throw exc('The requested operation is not valid for the provided key', 'InvalidAccessError');
  return s;
}

function aesOp(n, key, data, isEnc, what) {
  var s = needKey(key, what, isEnc ? 'encrypt' : 'decrypt', n.name);
  var a = { mode: n.name.slice(4), enc: isEnc, key: enc(s.data), data: enc(data) };
  if (n.name === 'AES-GCM') { a.iv = enc(n.iv); if (n.aad) a.aad = enc(n.aad); a.tagLen = n.tagLength; }
  else if (n.name === 'AES-CBC') a.iv = enc(n.iv);
  else if (n.name === 'AES-CTR') { a.counter = enc(n.counter); a.length = n.length; }
  return out(call('aes', a).d);
}

function doEncrypt(n, key, data, isEnc, what) {
  if (n.name === 'RSA-OAEP') {
    var s = needKey(key, what, isEnc ? 'encrypt' : 'decrypt', 'RSA-OAEP');
    if (isEnc && s.type !== 'public' || !isEnc && s.type !== 'private') throw exc('The requested operation is not valid for the provided key', 'InvalidAccessError');
    return out(call('rsaoaep', { enc: isEnc, key: s.data, hash: s.algorithm.hash.name, label: enc(n.label), data: enc(data) }).d);
  }
  return aesOp(n, key, data, isEnc, what);
}

function keyGenerate(n, extractable, usages) {
  var name = n.name, kp, pub, priv, k;
  if (/^AES-/.test(name)) {
    checkUsages(SYM_USAGES[name], usages);
    if (!usages.length) throw exc('Usages cannot be empty when creating a key.', 'SyntaxError');
    var raw = fromB64(sw.cryptoRandom(n.length / 8));
    return makeKey('secret', extractable, { name: name, length: n.length }, sorted(usages), raw);
  }
  if (name === 'HMAC') {
    checkUsages(SYM_USAGES.HMAC, usages);
    if (!usages.length) throw exc('Usages cannot be empty when creating a key.', 'SyntaxError');
    var bits = n.length === undefined ? hmacDefaultLen(n.hash) : n.length;
    if (!bits) throw exc('HMAC key length must not be zero.', 'OperationError');
    var kb = fromB64(sw.cryptoRandom(Math.ceil(bits / 8)));
    if (bits % 8) kb[kb.length - 1] &= (0xff << (8 - bits % 8)) & 0xff;
    return makeKey('secret', extractable, { name: 'HMAC', length: bits, hash: { name: n.hash } }, sorted(usages), kb);
  }
  var al = ASYM[name];
  checkUsages(al.priv.concat(al.pub), usages);
  var pu = usages.filter(function (u) { return al.pub.indexOf(u) >= 0; }), pr = usages.filter(function (u) { return al.priv.indexOf(u) >= 0; });
  if (!pr.length) throw exc('Usages cannot be empty when creating a key.', 'SyntaxError');
  var gp = { kind: kindOf(name) }, desc;
  if (gp.kind === 'ec') { gp.curve = n.namedCurve; desc = { name: name, namedCurve: n.namedCurve }; }
  else if (gp.kind === 'rsa') {
    var e = 0; for (var i = 0; i < n.publicExponent.length; i++) e = e * 256 + n.publicExponent[i];
    gp.bits = n.modulusLength; gp.e = e; desc = rsaAlg(n, n.modulusLength, n.publicExponent);
  } else desc = { name: name };
  var r = call('generate', gp);
  var res = new CryptoKeyPair();
  res.publicKey = makeKey('public', true, desc, sorted(pu), r.pub);
  res.privateKey = makeKey('private', extractable, desc, sorted(pr), r.priv);
  return res;
}

var JWK_AES = { 128: 'A128', 192: 'A192', 256: 'A256' };
var JWK_AESMODE = { 'AES-GCM': 'GCM', 'AES-CBC': 'CBC', 'AES-CTR': 'CTR', 'AES-KW': 'KW' };
function jwkAlgFor(s) {
  var a = s.algorithm;
  if (/^AES-/.test(a.name)) return JWK_AES[a.length] + JWK_AESMODE[a.name];
  if (a.name === 'HMAC') return 'HS' + a.hash.name.slice(4);
  if (a.name === 'RSASSA-PKCS1-v1_5') return 'RS' + a.hash.name.slice(4);
  if (a.name === 'RSA-PSS') return 'PS' + a.hash.name.slice(4);
  if (a.name === 'RSA-OAEP') return a.hash.name === 'SHA-1' ? 'RSA-OAEP' : 'RSA-OAEP-' + a.hash.name.slice(4);
  return undefined;
}

function keyExport(format, key) {
  var s = slot(key, 'exportKey');
  if (!s.extractable) throw exc('key is not extractable', 'InvalidAccessError');
  var a = s.algorithm.name;
  if (format === 'jwk') {
    var jwk;
    if (s.type === 'secret') jwk = { kty: 'oct', k: b64uEnc(s.data) };
    else jwk = call('export', { format: 'jwk', key: s.data, private: s.type === 'private' }).jwk;
    var alg = jwkAlgFor(s);
    if (alg) jwk.alg = alg;
    jwk.ext = true;
    jwk.key_ops = s.usages.slice();
    return jwk;
  }
  if (format === 'raw') {
    if (s.type === 'secret') return out(enc(s.data));
    if (s.type !== 'public' || /^RSA/.test(a)) throw exc('Unable to export the key in raw format', 'InvalidAccessError');
    return out(call('export', { format: 'raw', key: s.data }).d);
  }
  if (format === 'spki') {
    if (s.type !== 'public') throw exc('Unable to export a non-public key in spki format', 'InvalidAccessError');
    return out(s.data);
  }
  if (format === 'pkcs8') {
    if (s.type !== 'private') throw exc('Unable to export a non-private key in pkcs8 format', 'InvalidAccessError');
    return out(s.data);
  }
  throw new TypeError("Failed to execute 'exportKey' on 'SubtleCrypto': The provided value '" + format + "' is not a valid enum value of type KeyFormat.");
}

function checkJwkOps(jwk, usages, algUse) {
  if (jwk.use !== undefined && algUse && jwk.use !== algUse) throw exc('The JWK "use" member is incompatible with the algorithm', 'DataError');
  if (jwk.key_ops !== undefined) {
    if (!Array.isArray(jwk.key_ops)) throw exc('The JWK "key_ops" member is invalid', 'DataError');
    usages.forEach(function (u) { if (jwk.key_ops.indexOf(u) < 0) throw exc('The JWK "key_ops" member does not permit the requested usages', 'DataError'); });
  }
}

function keyImport(format, data, n, extractable, usages) {
  var name = n.name, jwk = null;
  if (format === 'jwk') {
    if (data === null || typeof data !== 'object' || ArrayBuffer.isView(data) || data instanceof ArrayBuffer) throw new TypeError("Failed to execute 'importKey' on 'SubtleCrypto': parameter 2 is not of type 'JsonWebKey'.");
    jwk = data;
    if (typeof jwk.kty !== 'string') throw new TypeError("The required JWK member 'kty' was missing.");
    if (extractable && jwk.ext === false) throw exc('The JWK "ext" member was false but extractable was true', 'DataError');
  } else if (format !== 'raw' && format !== 'spki' && format !== 'pkcs8') {
    throw new TypeError("Failed to execute 'importKey' on 'SubtleCrypto': The provided value '" + format + "' is not a valid enum value of type KeyFormat.");
  } else data = bytes(data, 'importKey');

  if (SYM_USAGES[name]) {
    checkUsages(SYM_USAGES[name], usages);
    var raw;
    if (format === 'raw') raw = data;
    else if (format === 'jwk') {
      if (jwk.kty !== 'oct') throw exc('The JWK "kty" member must be "oct"', 'DataError');
      raw = b64uDec(jwk.k);
      checkJwkOps(jwk, usages, /^(HMAC|AES)/.test(name) ? (/^AES/.test(name) ? 'enc' : 'sig') : null);
      if (jwk.alg !== undefined) {
        var exp = /^AES/.test(name) ? JWK_AES[raw.length * 8] + JWK_AESMODE[name] : 'HS' + (n.hash || '').slice(4);
        if (jwk.alg !== exp) throw exc('The JWK "alg" member is incompatible with the algorithm', 'DataError');
      }
    } else throw exc('Unsupported import key format for this algorithm', 'NotSupportedError');
    if (name === 'HKDF' || name === 'PBKDF2') {
      if (format !== 'raw') throw exc('Unsupported import key format for this algorithm', 'NotSupportedError');
      if (extractable) throw exc('HKDF and PBKDF2 keys cannot be extractable', 'SyntaxError');
      return makeKey('secret', false, { name: name }, sorted(usages), raw);
    }
    if (!usages.length) throw exc('Usages cannot be empty when creating a key.', 'SyntaxError');
    if (/^AES/.test(name)) {
      if ([16, 24, 32].indexOf(raw.length) < 0) throw exc('Invalid key length', 'DataError');
      return makeKey('secret', extractable, { name: name, length: raw.length * 8 }, sorted(usages), raw);
    }
    // HMAC
    var bits = raw.length * 8;
    if (!raw.length) throw exc('HMAC key data must not be empty', 'DataError');
    if (n.length !== undefined) {
      if (n.length > bits || n.length <= bits - 8) throw exc('Invalid HMAC key length', 'DataError');
      bits = n.length;
    }
    return makeKey('secret', extractable, { name: 'HMAC', length: bits, hash: { name: n.hash } }, sorted(usages), raw);
  }

  var al = ASYM[name], kind = kindOf(name), r, type, desc, der;
  if (format === 'raw') {
    if (kind === 'rsa') throw exc('Unsupported import key format for this algorithm', 'NotSupportedError');
    if (name === 'ECDSA' || name === 'ECDH' || kind !== 'ec') { /* ok */ }
    r = call('import', { format: 'raw', kind: kind, curve: n.namedCurve, data: enc(data) });
    type = 'public';
  } else if (format === 'spki' || format === 'pkcs8') {
    r = call('import', { format: format, kind: kind, data: enc(data) });
    type = format === 'spki' ? 'public' : 'private';
    if (kind === 'ec' && r.curve !== n.namedCurve) throw exc('The key curve does not match', 'DataError');
  } else {
    var isPriv = jwk.d !== undefined;
    checkJwkOps(jwk, usages, kind === 'rsa' ? (name === 'RSA-OAEP' ? 'enc' : 'sig') : (name === 'ECDH' || name === 'X25519' ? 'enc' : 'sig'));
    if (jwk.alg !== undefined && kind === 'rsa') {
      var want = name === 'RSASSA-PKCS1-v1_5' ? 'RS' + n.hash.slice(4) : name === 'RSA-PSS' ? 'PS' + n.hash.slice(4) : (n.hash === 'SHA-1' ? 'RSA-OAEP' : 'RSA-OAEP-' + n.hash.slice(4));
      if (jwk.alg !== want) throw exc('The JWK "alg" member is incompatible with the algorithm', 'DataError');
    }
    if (kind === 'ec' && jwk.crv !== n.namedCurve) throw exc('The JWK "crv" member does not match the curve', 'DataError');
    r = call('import', { format: 'jwk', kind: kind, jwk: jwk });
    type = isPriv ? 'private' : 'public';
  }
  if (kind === 'ec' && r.curve === undefined) r.curve = n.namedCurve;
  if (type === 'private') {
    checkUsages(al.priv, usages);
    if (!usages.length) throw exc('Usages cannot be empty when creating a key.', 'SyntaxError');
    der = r.priv;
  } else {
    checkUsages(al.pub, usages);
    der = r.pub;
  }
  if (kind === 'ec') desc = { name: name, namedCurve: n.namedCurve };
  else if (kind === 'rsa') {
    var bitsN = r.bits, ex = new Uint8Array([1, 0, 1]);
    desc = rsaAlg(n, bitsN, ex);
  } else desc = { name: name };
  return makeKey(type, extractable, desc, sorted(usages), der);
}

function signOp(n, key, data, doSign) {
  var s = needKey(key, doSign ? 'sign' : 'verify', doSign ? 'sign' : 'verify', n.name);
  if (n.name === 'HMAC') {
    return { hash: s.algorithm.hash.name, key: enc(s.data) };
  }
  if (doSign && s.type !== 'private' || !doSign && s.type !== 'public') throw exc('The requested operation is not valid for the provided key', 'InvalidAccessError');
  return null;
}

function getKeyLength(n) {
  if (/^AES-/.test(n.name)) return n.length;
  if (n.name === 'HMAC') return n.length === undefined ? hmacDefaultLen(n.hash) : n.length;
  return null;
}

function deriveBitsImpl(n, key, length, usage) {
  var s = needKey(key, 'deriveBits', usage, n.name);
  if (n.name === 'ECDH' || n.name === 'X25519') {
    var ps = slot(n.public, 'deriveBits');
    if (ps.type !== 'public') throw exc('The public parameter is not a public key', 'InvalidAccessError');
    if (ps.algorithm.name !== n.name) throw exc('The public key algorithm does not match', 'InvalidAccessError');
    if (n.name === 'ECDH' && ps.algorithm.namedCurve !== s.algorithm.namedCurve) throw exc('The public key curve does not match', 'InvalidAccessError');
    if (s.type !== 'private') throw exc('The requested operation is not valid for the provided key', 'InvalidAccessError');
    var z = fromB64(call('derive', { priv: s.data, pub: ps.data }).d);
    if (length === null || length === undefined) return z;
    if (length > z.length * 8) throw exc('The length provided for ECDH is too large.', 'OperationError');
    var nb = Math.ceil(length / 8), o = z.slice(0, nb);
    if (length % 8) o[nb - 1] &= (0xff << (8 - length % 8)) & 0xff;
    return o;
  }
  if (length === null || length === undefined) throw exc('Length must not be null', 'OperationError');
  if (length % 8) throw exc('Length must be a multiple of 8', 'OperationError');
  if (n.name === 'HKDF') {
    if (length === 0) return new Uint8Array(0);
    return fromB64(call('hkdf', { hash: n.hash, ikm: enc(s.data), salt: enc(n.salt), info: enc(n.info), len: length / 8 }).d);
  }
  if (n.iterations === 0) throw exc('Iterations must be greater than zero', 'OperationError');
  if (length === 0) return new Uint8Array(0);
  return fromB64(call('pbkdf2', { hash: n.hash, pw: enc(s.data), salt: enc(n.salt), iter: n.iterations, len: length / 8 }).d);
}

function P(fn) { try { return Promise.resolve(fn()); } catch (e) { return Promise.reject(e); } }
function need(args, n, what) {
  if (args.length < n) throw new TypeError("Failed to execute '" + what + "' on 'SubtleCrypto': " + n + " arguments required, but only " + args.length + " present.");
}

function SubtleCrypto() { throw new TypeError('Illegal constructor'); }
var M = {
  digest: function (algorithm, data) {
    need(arguments, 2, 'digest');
    return P(function () { var n = normalize(algorithm, 'digest'), d = bytes(data, 'digest'); return out(call('digest', { hash: n.name, data: enc(d) }).d); });
  },
  generateKey: function (algorithm, extractable, keyUsages) {
    need(arguments, 3, 'generateKey');
    return P(function () { var n = normalize(algorithm, 'generateKey'); return keyGenerate(n, !!extractable, usageList(keyUsages)); });
  },
  importKey: function (format, keyData, algorithm, extractable, keyUsages) {
    need(arguments, 5, 'importKey');
    return P(function () { format = String(format); var n = normalize(algorithm, 'importKey'); return keyImport(format, keyData, n, !!extractable, usageList(keyUsages)); });
  },
  exportKey: function (format, key) {
    need(arguments, 2, 'exportKey');
    return P(function () { return keyExport(String(format), key); });
  },
  sign: function (algorithm, key, data) {
    need(arguments, 3, 'sign');
    return P(function () {
      var n = normalize(algorithm, 'sign'), d = bytes(data, 'sign'), h = signOp(n, key, d, true), s = slot(key, 'sign');
      if (n.name === 'HMAC') return out(call('hmac', { hash: h.hash, key: h.key, data: enc(d) }).d);
      var a = { alg: n.name, key: s.data, data: enc(d) };
      if (n.name === 'ECDSA') a.hash = n.hash;
      else if (n.name !== 'Ed25519') { a.hash = s.algorithm.hash.name; if (n.name === 'RSA-PSS') a.saltLen = n.saltLength; }
      return out(call('sign', a).d);
    });
  },
  verify: function (algorithm, key, signature, data) {
    need(arguments, 4, 'verify');
    return P(function () {
      var n = normalize(algorithm, 'verify'), sig = bytes(signature, 'verify'), d = bytes(data, 'verify'), h = signOp(n, key, d, false), s = slot(key, 'verify');
      if (n.name === 'HMAC') return call('hmacVerify', { hash: h.hash, key: h.key, data: enc(d), sig: enc(sig) }).ok;
      var a = { alg: n.name, key: s.data, data: enc(d), sig: enc(sig) };
      if (n.name === 'ECDSA') a.hash = n.hash;
      else if (n.name !== 'Ed25519') { a.hash = s.algorithm.hash.name; if (n.name === 'RSA-PSS') a.saltLen = n.saltLength; }
      return call('verify', a).ok;
    });
  },
  encrypt: function (algorithm, key, data) {
    need(arguments, 3, 'encrypt');
    return P(function () { var n = normalize(algorithm, 'encrypt'); return doEncrypt(n, key, bytes(data, 'encrypt'), true, 'encrypt'); });
  },
  decrypt: function (algorithm, key, data) {
    need(arguments, 3, 'decrypt');
    return P(function () { var n = normalize(algorithm, 'decrypt'); return doEncrypt(n, key, bytes(data, 'decrypt'), false, 'decrypt'); });
  },
  deriveBits: function (algorithm, baseKey, length) {
    need(arguments, 2, 'deriveBits');
    return P(function () {
      var n = normalize(algorithm, 'deriveBits');
      var u = deriveBitsImpl(n, baseKey, length === undefined || length === null ? null : length >>> 0, 'deriveBits');
      return out(enc(u));
    });
  },
  deriveKey: function (algorithm, baseKey, derivedKeyType, extractable, keyUsages) {
    need(arguments, 5, 'deriveKey');
    return P(function () {
      var n = normalize(algorithm, 'deriveBits'), dn = normalize(derivedKeyType, 'importKey'), dl = normalize(derivedKeyType, 'getKeyLength');
      var usages = usageList(keyUsages), length = getKeyLength(dl);
      var bits = deriveBitsImpl(n, baseKey, length === null ? null : length, 'deriveKey');
      if (length !== null && bits.length * 8 !== Math.ceil(length / 8) * 8) throw exc('Derived key length mismatch', 'OperationError');
      var imp = Object.assign({}, dn); if (/^AES-/.test(dn.name)) imp.length = length;
      if (dn.name === 'HMAC') { imp.length = length; imp.hash = dl.hash; }
      return keyImport('raw', bits, imp, !!extractable, usages);
    });
  },
  wrapKey: function (format, key, wrappingKey, wrapAlgorithm) {
    need(arguments, 4, 'wrapKey');
    return P(function () {
      var n = normalize(wrapAlgorithm, 'wrapKey');
      var ws = needKey(wrappingKey, 'wrapKey', 'wrapKey', n.name);
      var exported = keyExport(String(format), key);
      var bytesIn = format === 'jwk' ? utf8(JSON.stringify(exported)) : new Uint8Array(exported);
      if (n.name === 'AES-KW') return out(call('aes', { mode: 'KW', enc: true, key: enc(ws.data), data: enc(bytesIn) }).d);
      var nn = n; return doEncryptWrap(nn, wrappingKey, bytesIn, true);
    });
  },
  unwrapKey: function (format, wrappedKey, unwrappingKey, unwrapAlgorithm, unwrappedKeyAlgorithm, extractable, keyUsages) {
    need(arguments, 7, 'unwrapKey');
    return P(function () {
      var n = normalize(unwrapAlgorithm, 'unwrapKey'), kn = normalize(unwrappedKeyAlgorithm, 'importKey'), usages = usageList(keyUsages);
      var us = needKey(unwrappingKey, 'unwrapKey', 'unwrapKey', n.name);
      var w = bytes(wrappedKey, 'unwrapKey'), pt;
      if (n.name === 'AES-KW') pt = fromB64(call('aes', { mode: 'KW', enc: false, key: enc(us.data), data: enc(w) }).d);
      else pt = new Uint8Array(doEncryptWrap(n, unwrappingKey, w, false));
      var data;
      if (format === 'jwk') { try { data = JSON.parse(new TextDecoder('utf-8', { fatal: true }).decode(pt)); } catch (e) { throw exc('The unwrapped key is not valid JSON', 'DataError'); } }
      else data = pt;
      return keyImport(String(format), data, kn, !!extractable, usages);
    });
  }
};
function doEncryptWrap(n, key, data, isEnc) {
  var s = slot(key, 'wrapKey');
  if (n.name === 'RSA-OAEP') {
    return out(call('rsaoaep', { enc: isEnc, key: s.data, hash: s.algorithm.hash.name, label: enc(n.label), data: enc(data) }).d);
  }
  var a = { mode: n.name.slice(4), enc: isEnc, key: enc(s.data), data: enc(data) };
  if (n.name === 'AES-GCM') { a.iv = enc(n.iv); if (n.aad) a.aad = enc(n.aad); a.tagLen = n.tagLength; }
  else if (n.name === 'AES-CBC') a.iv = enc(n.iv);
  else if (n.name === 'AES-CTR') { a.counter = enc(n.counter); a.length = n.length; }
  return out(call('aes', a).d);
}
Object.keys(M).forEach(function (k) { H.define(SubtleCrypto.prototype, k, M[k]); Object.defineProperty(SubtleCrypto.prototype[k], 'name', { value: k, configurable: true }); });
H.tag(SubtleCrypto, 'SubtleCrypto');
var subtle = Object.create(SubtleCrypto.prototype);
H.glob('SubtleCrypto', SubtleCrypto);
Object.defineProperty(g.Crypto.prototype, 'subtle', { get: function () { return g.isSecureContext ? subtle : undefined; }, enumerable: true, configurable: true });
