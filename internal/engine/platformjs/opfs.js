// Origin Private File System (navigator.storage.getDirectory()), the File System handle classes, and the
// user-facing pickers (which reject with AbortError: there is no native dialog integration yet).
H.load('streams');
var sw = H.sw;
var $ = Symbol('fs');
var NATIVE = typeof Uint8Array.fromBase64 === 'function' && typeof Uint8Array.prototype.toBase64 === 'function';
function b64(u8) {
  if (NATIVE) return u8.toBase64();
  var s = '';
  for (var i = 0; i < u8.length; i += 0x6000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x6000));
  return btoa(s);
}
function unb64(s) {
  if (NATIVE) return Uint8Array.fromBase64(s);
  var b = atob(s), u = new Uint8Array(b.length);
  for (var i = 0; i < b.length; i++) u[i] = b.charCodeAt(i);
  return u;
}
function op() {
  try { return sw.opfs.apply(null, arguments); } catch (e) { throw H.hostErr(e); }
}
function checkName(name, what) {
  name = String(name);
  if (name === '' || name === '.' || name === '..' || /[\/\\]/.test(name)) throw new TypeError("Failed to execute '" + what + "': Name is not allowed.");
  return name;
}
function illegal() { throw new TypeError('Illegal constructor'); }
function pr(fn) { return function () { try { return Promise.resolve(fn.apply(this, arguments)); } catch (e) { return Promise.reject(e); } }; }
var MIME = { txt: 'text/plain', html: 'text/html', htm: 'text/html', css: 'text/css', js: 'text/javascript', mjs: 'text/javascript', json: 'application/json', xml: 'text/xml', csv: 'text/csv', md: 'text/markdown', svg: 'image/svg+xml', png: 'image/png', jpg: 'image/jpeg', jpeg: 'image/jpeg', gif: 'image/gif', webp: 'image/webp', pdf: 'application/pdf', wasm: 'application/wasm', mp3: 'audio/mpeg', wav: 'audio/wav', mp4: 'video/mp4', webm: 'video/webm', zip: 'application/zip' };
function mimeOf(name) { var m = /\.([A-Za-z0-9]+)$/.exec(name); return m ? MIME[m[1].toLowerCase()] || '' : ''; }

// ---- handles -------------------------------------------------------------------------------------------
function FileSystemHandle() { illegal(); }
function hs(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s || !s.path) throw new TypeError('Illegal invocation'); return s; }
function mkHandle(C, path, name) { var h = Object.create(C.prototype); Object.defineProperty(h, $, { value: { path: path, name: name } }); return h; }
H.attrs(FileSystemHandle.prototype, {
  kind: function () { return hs(this).kind; },
  name: function () { return hs(this).name; }
});
H.define(FileSystemHandle.prototype, 'isSameEntry', pr(function isSameEntry(other) {
  var a = hs(this), b = other !== null && typeof other === 'object' ? other[$] : null;
  return !!b && b.path && a.kind === b.kind && a.path.join('/') === b.path.join('/');
}));
H.define(FileSystemHandle.prototype, 'queryPermission', pr(function queryPermission() { hs(this); return 'granted'; }));
H.define(FileSystemHandle.prototype, 'requestPermission', pr(function requestPermission() { hs(this); return 'granted'; }));
H.define(FileSystemHandle.prototype, 'remove', pr(function remove(options) {
  var s = hs(this);
  if (!s.path.length) throw H.exc('The root directory cannot be removed.', 'NoModificationAllowedError');
  op('remove', s.path.join('/'), !!(options && options.recursive));
}));
H.define(FileSystemHandle.prototype, 'move', pr(function move(a, b) {
  var s = hs(this);
  if (!s.path.length) throw H.exc('The root directory cannot be moved.', 'NoModificationAllowedError');
  var destDir, newName;
  if (arguments.length >= 2) { destDir = hs(a).path; newName = checkName(b, 'move'); }
  else { destDir = s.path.slice(0, -1); newName = checkName(a, 'move'); }
  var dest = destDir.concat([newName]);
  op('move', s.path.join('/'), dest.join('/'));
  s.path = dest; s.name = newName;
}));
H.tag(FileSystemHandle, 'FileSystemHandle');

function FileSystemFileHandle() { illegal(); }
Object.setPrototypeOf(FileSystemFileHandle.prototype, FileSystemHandle.prototype);
Object.setPrototypeOf(FileSystemFileHandle, FileSystemHandle);
function FileSystemDirectoryHandle() { illegal(); }
Object.setPrototypeOf(FileSystemDirectoryHandle.prototype, FileSystemHandle.prototype);
Object.setPrototypeOf(FileSystemDirectoryHandle, FileSystemHandle);
Object.defineProperty(FileSystemFileHandle.prototype, 'kind', { get: function () { hs(this); return 'file'; }, enumerable: true, configurable: true });
Object.defineProperty(FileSystemDirectoryHandle.prototype, 'kind', { get: function () { hs(this); return 'directory'; }, enumerable: true, configurable: true });
function fileHandle(path, name) { var h = mkHandle(FileSystemFileHandle, path, name); h[$].kind = 'file'; return h; }
function dirHandle(path, name) { var h = mkHandle(FileSystemDirectoryHandle, path, name); h[$].kind = 'directory'; return h; }

H.define(FileSystemFileHandle.prototype, 'getFile', pr(function getFile() {
  var s = hs(this), r = op('read', s.path.join('/'));
  return new g.File([unb64(r.d)], s.name, { type: mimeOf(s.name), lastModified: r.mtime });
}));
H.define(FileSystemFileHandle.prototype, 'createWritable', pr(function createWritable(options) {
  var s = hs(this);
  var keep = !!(options && options.keepExistingData), initial = new Uint8Array(0);
  var st = op('stat', s.path.join('/'));
  if (!st || st.kind !== 'file') throw H.exc('A requested file or directory could not be found.', 'NotFoundError');
  if (keep) initial = unb64(op('read', s.path.join('/')).d);
  return new FileSystemWritableFileStream(s.path.join('/'), initial);
}));
H.tag(FileSystemFileHandle, 'FileSystemFileHandle');

H.define(FileSystemDirectoryHandle.prototype, 'getFileHandle', pr(function getFileHandle(name, options) {
  var s = hs(this); name = checkName(name, 'getFileHandle');
  var p = s.path.concat([name]);
  op('get', p.join('/'), 'file', !!(options && options.create));
  return fileHandle(p, name);
}));
H.define(FileSystemDirectoryHandle.prototype, 'getDirectoryHandle', pr(function getDirectoryHandle(name, options) {
  var s = hs(this); name = checkName(name, 'getDirectoryHandle');
  var p = s.path.concat([name]);
  op('get', p.join('/'), 'directory', !!(options && options.create));
  return dirHandle(p, name);
}));
H.define(FileSystemDirectoryHandle.prototype, 'removeEntry', pr(function removeEntry(name, options) {
  var s = hs(this); name = checkName(name, 'removeEntry');
  op('remove', s.path.concat([name]).join('/'), !!(options && options.recursive));
}));
H.define(FileSystemDirectoryHandle.prototype, 'resolve', pr(function resolve(other) {
  var a = hs(this), b = hs(other);
  if (b.path.length < a.path.length) return null;
  for (var i = 0; i < a.path.length; i++) if (a.path[i] !== b.path[i]) return null;
  return b.path.slice(a.path.length);
}));
function listing(dir) {
  var s = hs(dir), rows = op('list', s.path.join('/')) || [];
  return rows.map(function (r) { var p = s.path.concat([r[0]]); return [r[0], r[1] === 'directory' ? dirHandle(p, r[0]) : fileHandle(p, r[0])]; });
}
function asyncIter(items, pick) {
  var i = 0, it = {
    next: function () { return Promise.resolve(i < items.length ? { value: pick(items[i++]), done: false } : { value: undefined, done: true }); },
    return: function (v) { i = items.length; return Promise.resolve({ value: v, done: true }); }
  };
  it[Symbol.asyncIterator] = function () { return it; };
  return it;
}
H.define(FileSystemDirectoryHandle.prototype, 'entries', function entries() { return asyncIter(listing(this), function (r) { return [r[0], r[1]]; }); });
H.define(FileSystemDirectoryHandle.prototype, 'keys', function keys() { return asyncIter(listing(this), function (r) { return r[0]; }); });
H.define(FileSystemDirectoryHandle.prototype, 'values', function values() { return asyncIter(listing(this), function (r) { return r[1]; }); });
H.define(FileSystemDirectoryHandle.prototype, Symbol.asyncIterator, FileSystemDirectoryHandle.prototype.entries);
H.tag(FileSystemDirectoryHandle, 'FileSystemDirectoryHandle');

// ---- FileSystemWritableFileStream -----------------------------------------------------------------------------------
function blobBytes(v) {
  if (typeof v === 'string') return new g.TextEncoder().encode(v);
  if (v instanceof ArrayBuffer) return new Uint8Array(v.slice(0));
  if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength));
  if (g.Blob && v instanceof g.Blob) return v.arrayBuffer().then(function (b) { return new Uint8Array(b); });
  return null;
}
class FileSystemWritableFileStream extends g.WritableStream {
  constructor(path, initial) {
    var st = { buf: initial.slice(), size: initial.length, pos: 0, path: path };
    function ensure(n) {
      if (n > st.buf.length) { var nb = new Uint8Array(Math.max(n, st.buf.length * 2, 64)); nb.set(st.buf.subarray(0, st.size)); st.buf = nb; }
    }
    function put(bytes, at) {
      ensure(at + bytes.length);
      if (at > st.size) st.buf.fill(0, st.size, at);
      st.buf.set(bytes, at);
      if (at + bytes.length > st.size) st.size = at + bytes.length;
    }
    function resize(n) {
      if (n > st.size) { ensure(n); st.buf.fill(0, st.size, n); }
      st.size = n;
      if (st.pos > n) st.pos = n;
    }
    function position(p) {
      p = Number(p);
      if (!(p >= 0) || p === Infinity) throw new TypeError('Invalid position');
      return Math.trunc(p);
    }
    function apply(chunk) {
      var params = chunk;
      if (chunk !== null && typeof chunk === 'object' && !ArrayBuffer.isView(chunk) && !(chunk instanceof ArrayBuffer) && !(g.Blob && chunk instanceof g.Blob) && typeof chunk.type === 'string' && ['write', 'seek', 'truncate'].indexOf(chunk.type) >= 0) {
        if (chunk.type === 'seek') { if (chunk.position === undefined) throw new TypeError("Failed to execute 'write' on 'FileSystemWritableFileStream': Invalid params: seek requires a position."); st.pos = Math.min(position(chunk.position), Infinity); return; }
        if (chunk.type === 'truncate') { if (chunk.size === undefined) throw new TypeError("Failed to execute 'write' on 'FileSystemWritableFileStream': Invalid params: truncate requires a size."); resize(position(chunk.size)); return; }
        if (chunk.data === undefined || chunk.data === null) throw new TypeError("Failed to execute 'write' on 'FileSystemWritableFileStream': Invalid params: write requires data.");
        if (chunk.position !== undefined && chunk.position !== null) st.pos = position(chunk.position);
        params = chunk.data;
      }
      var bytes = blobBytes(params);
      if (bytes === null) bytes = blobBytes(String(params));
      function commit(u8) { put(u8, st.pos); st.pos += u8.length; }
      if (bytes && typeof bytes.then === 'function') return bytes.then(commit);
      commit(bytes);
    }
    super({
      write: function (chunk) { return apply(chunk); },
      close: function () {
        var data = st.buf.subarray(0, st.size);
        try { op('write', st.path, b64(data)); } catch (e) { return Promise.reject(e); }
      }
    });
    Object.defineProperty(this, $, { value: { st: st, resize: resize, position: position } });
  }
  write(data) {
    if (this.locked) return Promise.reject(new TypeError("Failed to execute 'write' on 'FileSystemWritableFileStream': Cannot write to a locked stream."));
    var w = this.getWriter(), p = w.write(data);
    return p.then(function () { w.releaseLock(); }, function (e) { w.releaseLock(); throw e; });
  }
  seek(position) { return this.write({ type: 'seek', position: position }); }
  truncate(size) { return this.write({ type: 'truncate', size: size }); }
}
H.tag(FileSystemWritableFileStream, 'FileSystemWritableFileStream');

// ---- StorageManager.getDirectory --------------------------------------------------------------------------------------------
// navigator.storage and StorageManager belong to the storage layer (idb_prelude.js); its getDirectory() touches
// FileSystemDirectoryHandle (which loads this module) and then calls this root factory.
H.glob('__swOpfsRoot', function () { return Promise.resolve(dirHandle([], '')); });
H.glob('__swOpfsUsage', function () { return op('usage', '').usage || 0; });

// ---- pickers ---------------------------------------------------------------------------------------------------------------------------
function pickerCheck(name, options) {
  if (options !== undefined && options !== null && typeof options !== 'object') throw new TypeError("Failed to execute '" + name + "' on 'Window': The provided value is not of type 'options'.");
  options = options || {};
  if (options.types !== undefined) {
    Array.prototype.forEach.call(options.types, function (t) {
      if (t === null || typeof t !== 'object' || t.accept === null || typeof t.accept !== 'object') throw new TypeError("Failed to execute '" + name + "' on 'Window': Failed to read the 'accept' property: Required member is undefined.");
      Object.keys(t.accept).forEach(function (mime) {
        if (!/^[\w.+-]+\/[\w.+*-]+$/.test(mime)) throw new TypeError("Failed to execute '" + name + "' on 'Window': Invalid type: " + mime);
      });
    });
  }
  if (options.excludeAcceptAllOption && (!options.types || !options.types.length)) throw new TypeError("Failed to execute '" + name + "' on 'Window': Cannot set excludeAcceptAllOption to true with an empty types list.");
}
function picker(name) {
  H.glob(name, {
    [name]: function (options) {
      try { pickerCheck(name, options); } catch (e) { return Promise.reject(e); }
      return Promise.reject(H.exc('The user aborted a request.', 'AbortError'));
    }
  }[name]);
}
['showOpenFilePicker', 'showSaveFilePicker', 'showDirectoryPicker'].forEach(picker);

var G = { FileSystemHandle: FileSystemHandle, FileSystemFileHandle: FileSystemFileHandle, FileSystemDirectoryHandle: FileSystemDirectoryHandle, FileSystemWritableFileStream: FileSystemWritableFileStream };
Object.keys(G).forEach(function (n) { H.glob(n, G[n]); });
