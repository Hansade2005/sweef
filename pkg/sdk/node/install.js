'use strict';
// Engine resolution and installation for the Sweef SDK. Built-in modules only.
//
// Order: explicit `binary`, $SWEEF_BIN, `sweef` on PATH, the per-user cache, then an automatic
// download from GitHub Releases into the cache (sha256-verified before anything is executed).
// Nothing here modifies PATH, the shell profile or any system location.
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const https = require('https');
const tls = require('tls');
const zlib = require('zlib');
const crypto = require('crypto');

const pkg = require('./package.json');
const DEFAULT_VERSION = (pkg.sweef && pkg.sweef.engineVersion) || '0.1.0-alpha.5';
const PINNED = (pkg.sweef && pkg.sweef.engineSha256) || {};
const DEFAULT_BASE = 'https://github.com/sweefbrowser/sweef/releases/download';
const LAUNCHER_MARKER = 'SWEEF_SDK_LAUNCHER';
const MAX_ARCHIVE = 512 * 1024 * 1024;
const MAX_MEMBER = 1024 * 1024 * 1024;

class SweefInstallError extends Error {}

const MANUAL = 'Install it manually from https://github.com/sweefbrowser/sweef/releases (or build it from a clone: `go build -o sweef ./cmd/sweef`), then put it on PATH, set SWEEF_BIN, or pass { binary }.';

// ---- platform ---------------------------------------------------------------------------

// target maps (process.platform, process.arch) to the release asset and the member to extract.
function target(platform = process.platform, arch = process.arch, version = engineVersion()) {
  const a = { x64: 'amd64', arm64: 'arm64' }[arch];
  let os_, cpu, ext, member, exe;
  if (platform === 'linux' && a) {
    os_ = 'linux'; cpu = a; ext = 'tar.gz'; exe = 'sweef';
    member = /^[^/]+\/bin\/sweef$/;
  } else if (platform === 'darwin' && a) {
    os_ = 'darwin'; cpu = 'universal'; ext = 'zip'; exe = 'sweef';
    member = /^Sweef\.app\/Contents\/MacOS\/sweef$/;
  } else if (platform === 'win32' && a) {
    os_ = 'windows'; cpu = a; ext = 'zip'; exe = 'sweef.exe';
    member = /^[^/]+\/sweef\.exe$/;
  } else {
    throw new SweefInstallError(`no prebuilt sweef engine for ${platform}/${arch}. ${MANUAL}`);
  }
  return { os: os_, arch: cpu, ext, exe, member, asset: `sweef_${version}_${os_}_${cpu}.${ext}` };
}

function engineVersion() {
  const v = String(process.env.SWEEF_VERSION || DEFAULT_VERSION).replace(/^v/, '');
  if (!/^[0-9A-Za-z][0-9A-Za-z.+_-]*$/.test(v) || v.includes('..')) throw new SweefInstallError(`invalid SWEEF_VERSION: ${JSON.stringify(v)}`);
  return v;
}

function cacheRoot(env = process.env, platform = process.platform, home = os.homedir()) {
  if (env.SWEEF_ENGINE_CACHE) return env.SWEEF_ENGINE_CACHE;
  if (platform === 'win32') return path.win32.join(env.LOCALAPPDATA || path.win32.join(home, 'AppData', 'Local'), 'sweef', 'cache');
  if (platform === 'darwin') return path.posix.join(home, 'Library', 'Caches', 'sweef');
  return path.posix.join(env.XDG_CACHE_HOME || path.posix.join(home, '.cache'), 'sweef');
}

function cachedBinaryPath(version = engineVersion(), platform = process.platform) {
  const t = target(platform, platform === process.platform ? process.arch : 'x64', version);
  const root = cacheRoot(process.env, platform);
  return (platform === 'win32' ? path.win32 : path.posix).join(root, version, t.exe);
}

// ---- lookup (no network) ------------------------------------------------------------------

function isOwnLauncher(p) {
  try {
    const real = fs.realpathSync(p);
    const st = fs.statSync(real);
    if (st.size > 16384) return false;
    return fs.readFileSync(real, 'utf8').includes(LAUNCHER_MARKER);
  } catch (_) { return false; }
}

function isExecutable(p) {
  try {
    if (!fs.statSync(p).isFile()) return false;
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch (_) { return false; }
}

// lookupBinary finds an existing engine without downloading; returns a path or null.
function lookupBinary(binary) {
  const exe = binary || process.env.SWEEF_BIN;
  if (exe) return exe;
  const names = process.platform === 'win32' ? ['sweef.exe'] : ['sweef'];
  for (const dir of (process.env.PATH || '').split(path.delimiter)) {
    if (!dir) continue;
    for (const n of names) {
      const p = path.join(dir, n);
      if (isExecutable(p) && !isOwnLauncher(p)) return p;
    }
  }
  try {
    const c = cachedBinaryPath();
    if (isExecutable(c)) return c;
  } catch (_) { /* unsupported platform: nothing cached */ }
  return null;
}

function skipDownload() {
  const v = process.env.SWEEF_SKIP_DOWNLOAD;
  return !!v && v !== '0' && v.toLowerCase() !== 'false';
}

// ---- archives -----------------------------------------------------------------------------

function safeName(name) {
  const n = name.replace(/\\/g, '/');
  if (n.startsWith('/') || /^[A-Za-z]:/.test(n) || n.split('/').includes('..') || n.includes('\0')) {
    throw new SweefInstallError(`unsafe path in archive: ${JSON.stringify(name)}`);
  }
  return n;
}

function tarMembers(buf, wanted) { // buf: uncompressed tar; returns the wanted member's bytes
  let off = 0, longName = null, paxPath = null, found = null;
  const str = (b) => b.toString('utf8').replace(/\0.*$/s, '');
  while (off + 512 <= buf.length) {
    const h = buf.subarray(off, off + 512);
    if (h.every((x) => x === 0)) break;
    let name = str(h.subarray(0, 100));
    const prefix = str(h.subarray(345, 500));
    if (prefix && h.subarray(257, 262).toString() === 'ustar') name = `${prefix}/${name}`;
    const size = parseInt(str(h.subarray(124, 136)).trim() || '0', 8);
    const type = String.fromCharCode(h[156]);
    if (!Number.isFinite(size) || size < 0 || size > MAX_MEMBER) throw new SweefInstallError('corrupt tar header');
    const body = buf.subarray(off + 512, off + 512 + size);
    if (body.length < size) throw new SweefInstallError('truncated tar archive');
    off += 512 + Math.ceil(size / 512) * 512;
    if (type === 'L') { longName = str(body); continue; }
    if (type === 'x') {
      const m = /(?:^|\n)\d+ path=([^\n]*)\n/.exec(body.toString('utf8'));
      if (m) paxPath = m[1];
      continue;
    }
    if (type === 'g') continue;
    if (paxPath) { name = paxPath; paxPath = null; }
    if (longName) { name = longName; longName = null; }
    name = safeName(name);
    if ((type === '0' || type === '\0') && wanted.test(name)) {
      if (found) throw new SweefInstallError(`archive has more than one ${wanted}`);
      found = Buffer.from(body);
    }
  }
  return found;
}

function zipMember(buf, wanted) {
  let e = -1;
  for (let i = buf.length - 22; i >= Math.max(0, buf.length - 22 - 65535); i--) {
    if (buf.readUInt32LE(i) === 0x06054b50) { e = i; break; }
  }
  if (e < 0) throw new SweefInstallError('not a zip archive');
  const count = buf.readUInt16LE(e + 10);
  let p = buf.readUInt32LE(e + 16);
  if (count === 0xffff || p === 0xffffffff) throw new SweefInstallError('zip64 archives are not supported');
  let found = null;
  for (let i = 0; i < count; i++) {
    if (buf.readUInt32LE(p) !== 0x02014b50) throw new SweefInstallError('corrupt zip directory');
    const method = buf.readUInt16LE(p + 10);
    const csize = buf.readUInt32LE(p + 20);
    const usize = buf.readUInt32LE(p + 24);
    const nlen = buf.readUInt16LE(p + 28), xlen = buf.readUInt16LE(p + 30), clen = buf.readUInt16LE(p + 32);
    const lho = buf.readUInt32LE(p + 42);
    const name = safeName(buf.toString('utf8', p + 46, p + 46 + nlen));
    p += 46 + nlen + xlen + clen;
    if (name.endsWith('/') || !wanted.test(name)) continue;
    if (found) throw new SweefInstallError(`archive has more than one ${wanted}`);
    if (usize > MAX_MEMBER) throw new SweefInstallError('archive member too large');
    if (buf.readUInt32LE(lho) !== 0x04034b50) throw new SweefInstallError('corrupt zip entry');
    const start = lho + 30 + buf.readUInt16LE(lho + 26) + buf.readUInt16LE(lho + 28);
    const data = buf.subarray(start, start + csize);
    if (method === 0) found = Buffer.from(data);
    else if (method === 8) found = zlib.inflateRawSync(data);
    else throw new SweefInstallError(`unsupported zip compression ${method}`);
    if (found.length !== usize) throw new SweefInstallError('zip member size mismatch');
  }
  return found;
}

// extractMember returns the bytes of the single archive member matching `wanted`; every member
// name is validated (no absolute paths, no "..") and nothing is written under an archive-chosen name.
function extractMember(archivePath, ext, wanted) {
  const raw = fs.readFileSync(archivePath);
  const data = ext === 'zip' ? zipMember(raw, wanted) : tarMembers(zlib.gunzipSync(raw), wanted);
  if (!data || data.length === 0) throw new SweefInstallError(`archive does not contain the sweef engine (${wanted})`);
  return data;
}

// ---- http ---------------------------------------------------------------------------------

function proxyFor(u, env = process.env) {
  const h = u.hostname.replace(/^\[|\]$/g, '');
  if (h === 'localhost' || h === '127.0.0.1' || h === '::1') return null;
  const np = (env.NO_PROXY || env.no_proxy || '').split(',').map((s) => s.trim().toLowerCase()).filter(Boolean);
  for (const n of np) {
    if (n === '*') return null;
    const d = n.replace(/^\*?\./, '');
    if (h.toLowerCase() === d || h.toLowerCase().endsWith('.' + d)) return null;
  }
  const v = u.protocol === 'https:' ? (env.HTTPS_PROXY || env.https_proxy || env.ALL_PROXY || env.all_proxy) : (env.HTTP_PROXY || env.http_proxy || env.ALL_PROXY || env.all_proxy);
  if (!v) return null;
  try { return new URL(/^[a-z]+:\/\//i.test(v) ? v : `http://${v}`); } catch (_) { return null; }
}

function proxyAuth(p) {
  return p.username ? { 'Proxy-Authorization': 'Basic ' + Buffer.from(`${decodeURIComponent(p.username)}:${decodeURIComponent(p.password)}`).toString('base64') } : {};
}

function tunnel(proxy, host, port, timeout) {
  return new Promise((resolve, reject) => {
    const lib = proxy.protocol === 'https:' ? https : http;
    const req = lib.request({ host: proxy.hostname, port: proxy.port || (proxy.protocol === 'https:' ? 443 : 80), method: 'CONNECT', path: `${host}:${port}`, headers: { Host: `${host}:${port}`, ...proxyAuth(proxy) }, timeout });
    req.on('connect', (res, socket) => {
      if (res.statusCode !== 200) { socket.destroy(); reject(new SweefInstallError(`proxy refused CONNECT to ${host}:${port}: HTTP ${res.statusCode}`)); return; }
      resolve(socket);
    });
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error('proxy connection timed out')));
    req.end();
  });
}

// release closes the connection (and a proxy tunnel's raw socket) so the process can exit promptly.
function release(res) {
  setImmediate(() => { if (res.socket) res.socket.destroy(); if (res.sweefRaw) res.sweefRaw.destroy(); });
}

async function open(url, redirects = 5, timeout = 60000) {
  const u = new URL(url);
  if (u.protocol !== 'https:' && u.protocol !== 'http:') throw new SweefInstallError(`unsupported URL scheme: ${u.protocol}`);
  const proxy = proxyFor(u);
  const headers = { 'User-Agent': `sweef-sdk-node/${pkg.version}`, Accept: '*/*' };
  let opts, raw = null;
  if (u.protocol === 'https:') {
    opts = { protocol: 'https:', hostname: u.hostname, port: u.port || 443, path: u.pathname + u.search, headers, timeout };
    if (proxy) {
      const socket = await tunnel(proxy, u.hostname, u.port || 443, timeout);
      opts.agent = false;
      raw = socket;
      socket.on('error', () => {}); // late resets after the response is complete must not crash the process
      opts.createConnection = () => { const ts = tls.connect({ socket, servername: u.hostname }); ts.on('error', () => {}); return ts; };
      headers.Connection = 'close';
    }
  } else if (proxy) {
    opts = { hostname: proxy.hostname, port: proxy.port || 80, path: url, headers: { ...headers, Host: u.host, ...proxyAuth(proxy) }, timeout };
  } else {
    opts = { hostname: u.hostname, port: u.port || 80, path: u.pathname + u.search, headers, timeout };
  }
  const res = await new Promise((resolve, reject) => {
    const req = (u.protocol === 'https:' ? https : http).request(opts, resolve);
    req.on('error', reject);
    req.on('timeout', () => req.destroy(new Error(`request timed out: ${u.host}`)));
    req.end();
  });
  res.sweefRaw = raw;
  if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location) {
    res.resume(); release(res);
    if (redirects <= 0) throw new SweefInstallError('too many redirects');
    const next = new URL(res.headers.location, url);
    if (u.protocol === 'https:' && next.protocol !== 'https:') throw new SweefInstallError('refusing https to http redirect');
    return open(next.href, redirects - 1, timeout);
  }
  if (res.statusCode !== 200) { res.resume(); release(res); throw new SweefInstallError(`GET ${url}: HTTP ${res.statusCode}`); }
  return res;
}

async function fetchText(url) {
  const res = await open(url);
  const chunks = [];
  let n = 0;
  try {
    for await (const c of res) { n += c.length; if (n > 1 << 20) throw new SweefInstallError('response too large'); chunks.push(c); }
  } finally { release(res); }
  return Buffer.concat(chunks).toString('utf8');
}

async function fetchFile(url, dest) { // returns sha256 hex of what was written
  const res = await open(url);
  const h = crypto.createHash('sha256');
  const out = fs.createWriteStream(dest, { mode: 0o600 });
  let n = 0;
  try {
    await new Promise((resolve, reject) => {
      out.on('error', reject);
      res.on('error', reject);
      res.on('aborted', () => reject(new Error('connection closed mid-download')));
      res.on('data', (c) => {
        n += c.length;
        if (n > MAX_ARCHIVE) { reject(new SweefInstallError('download too large')); res.destroy(); return; }
        h.update(c);
      });
      res.on('end', () => out.end(resolve));
      res.pipe(out, { end: false });
    });
  } catch (e) { out.destroy(); throw e; } finally { release(res); }
  return h.digest('hex');
}

// ---- lock ---------------------------------------------------------------------------------

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function pidAlive(pid) {
  try { process.kill(pid, 0); return true; } catch (e) { return e.code === 'EPERM'; }
}

async function withLock(dir, ready, fn, { staleMs = 10 * 60 * 1000, waitMs = 12 * 60 * 1000 } = {}) {
  const lock = path.join(dir, '.lock');
  const deadline = Date.now() + waitMs;
  for (;;) {
    let fd;
    try {
      fd = fs.openSync(lock, 'wx', 0o600);
      fs.writeSync(fd, `${process.pid} ${Date.now()}\n`);
      fs.closeSync(fd);
      break;
    } catch (e) {
      if (e.code !== 'EEXIST') throw e;
    }
    const r = ready();
    if (r) return r;
    try {
      const [pid, ts] = fs.readFileSync(lock, 'utf8').trim().split(' ').map(Number);
      const age = Date.now() - (ts || fs.statSync(lock).mtimeMs);
      if (age > staleMs || (pid && !pidAlive(pid))) { fs.unlinkSync(lock); continue; }
    } catch (_) { /* lock vanished or unreadable; retry */ }
    if (Date.now() > deadline) throw new SweefInstallError(`timed out waiting for another sweef install (${lock})`);
    await sleep(100);
  }
  try {
    const r = ready();
    if (r) return r;
    return await fn();
  } finally {
    try { fs.unlinkSync(lock); } catch (_) { /* gone */ }
  }
}

// ---- install ------------------------------------------------------------------------------

function parseSums(text) {
  const m = new Map();
  for (const line of text.split(/\r?\n/)) {
    const x = /^([0-9a-fA-F]{64}) [ *]?(\S.*)$/.exec(line.trim());
    if (x) m.set(x[2], x[1].toLowerCase());
  }
  return m;
}

const inflight = new Map();

// downloadEngine fetches, verifies and installs the engine into the cache; returns its path.
// opts: { version, base, cacheDir, pins, log } (all optional; for tests and mirrors)
async function downloadEngine(opts = {}) {
  const version = opts.version || engineVersion();
  const t = target(process.platform, process.arch, version);
  const root = opts.cacheDir || cacheRoot();
  const dir = path.join(root, version);
  const dest = path.join(dir, t.exe);
  const key = dest;
  if (inflight.has(key)) return inflight.get(key);
  const p = (async () => {
    const ready = () => (isExecutable(dest) ? dest : null);
    const hit = ready();
    if (hit) return hit;
    fs.mkdirSync(dir, { recursive: true, mode: 0o755 });
    return withLock(dir, ready, async () => {
      const base = (opts.base || process.env.SWEEF_DOWNLOAD_BASE || DEFAULT_BASE).replace(/\/+$/, '');
      const rel = `${base}/v${version}`;
      const log = opts.log || ((m) => process.stderr.write(m + '\n'));
      const pins = opts.pins || PINNED;
      log(`sweef: downloading engine ${version} (${t.os}/${t.arch}) to ${dir}`);
      const tmp = path.join(dir, `.dl-${process.pid}-${crypto.randomBytes(4).toString('hex')}`);
      try {
        let want = pins[t.asset];
        if (!want) {
          const sums = parseSums(await fetchText(`${rel}/SHA256SUMS`));
          want = sums.get(t.asset);
          if (!want) throw new SweefInstallError(`${t.asset} is not listed in ${rel}/SHA256SUMS`);
        }
        const got = await fetchFile(`${rel}/${t.asset}`, tmp);
        if (got !== want.toLowerCase()) throw new SweefInstallError(`checksum mismatch for ${t.asset}: expected ${want}, got ${got}; refusing to install`);
        const bin = extractMember(tmp, t.ext, t.member);
        const out = `${tmp}.bin`;
        fs.writeFileSync(out, bin, { mode: 0o755 });
        fs.chmodSync(out, 0o755);
        fs.renameSync(out, dest);
        return dest;
      } finally {
        for (const f of [tmp, `${tmp}.bin`]) { try { fs.unlinkSync(f); } catch (_) { /* absent */ } }
      }
    });
  })();
  inflight.set(key, p);
  try { return await p; } finally { inflight.delete(key); }
}

function downloadError(cause) {
  const e = new SweefInstallError(`could not download the sweef engine automatically: ${cause.message}. ${MANUAL} (Behind a proxy? set HTTPS_PROXY. Mirror? set SWEEF_DOWNLOAD_BASE. Disable auto-download: SWEEF_SKIP_DOWNLOAD=1.)`);
  e.cause = cause;
  return e;
}

// ensureBinary resolves the engine path, downloading it into the per-user cache when absent.
async function ensureBinary(opts = {}) {
  const found = lookupBinary(opts.binary);
  if (found) return found;
  if (skipDownload()) {
    throw new SweefInstallError(`sweef binary not found: set SWEEF_BIN, put sweef on PATH, or pass { binary } (automatic download disabled by SWEEF_SKIP_DOWNLOAD). ${MANUAL}`);
  }
  try { return await downloadEngine(opts); } catch (e) { throw e instanceof SweefInstallError && /unsupported|no prebuilt/.test(e.message) ? e : downloadError(e); }
}

module.exports = { ensureBinary, lookupBinary, downloadEngine, target, cacheRoot, cachedBinaryPath, engineVersion, extractMember, parseSums, proxyFor, isOwnLauncher, SweefInstallError, LAUNCHER_MARKER, DEFAULT_VERSION };
