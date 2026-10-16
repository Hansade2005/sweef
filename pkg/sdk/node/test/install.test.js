'use strict';
// Offline tests for engine resolution/download. The HTTP server below is a LOCAL FAKE RELEASE
// (a stub `sweef` script in a tarball/zip plus SHA256SUMS), used only so CI needs no network.
// The real-release check is a separate manual step.
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const os = require('node:os');
const fs = require('node:fs');
const path = require('node:path');
const zlib = require('node:zlib');
const crypto = require('node:crypto');
const { spawn, execFileSync, execFile } = require('node:child_process');
const run = require('node:util').promisify(execFile);
const inst = require('../install.js');

const STUB = '#!/bin/sh\necho "sweef stub 9.9.9"\n';
const VER = '9.9.9';

function tarOf(entries) { // [{name, data, type}]
  const blocks = [];
  for (const e of entries) {
    const h = Buffer.alloc(512);
    h.write(e.name, 0, 100, 'utf8');
    h.write('0000755\0', 100); h.write('0000000\0', 108); h.write('0000000\0', 116);
    h.write(e.data.length.toString(8).padStart(11, '0') + '\0', 124);
    h.write('00000000000\0', 136);
    h.write('        ', 148);
    h.write(e.type || '0', 156);
    h.write('ustar\0' + '00', 257);
    let sum = 0; for (const b of h) sum += b;
    h.write(sum.toString(8).padStart(6, '0') + '\0 ', 148);
    blocks.push(h, e.data, Buffer.alloc((512 - (e.data.length % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return zlib.gzipSync(Buffer.concat(blocks));
}

function crc32(buf) {
  let c, crc = 0xffffffff;
  for (const b of buf) { c = (crc ^ b) & 0xff; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; crc = (crc >>> 8) ^ c; }
  return (crc ^ 0xffffffff) >>> 0;
}

function zipOf(entries, method = 8) {
  const parts = [], cd = [];
  let off = 0;
  for (const e of entries) {
    const name = Buffer.from(e.name), data = method === 8 ? zlib.deflateRawSync(e.data) : e.data, crc = crc32(e.data);
    const lh = Buffer.alloc(30);
    lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt16LE(method, 8);
    lh.writeUInt32LE(crc, 14); lh.writeUInt32LE(data.length, 18); lh.writeUInt32LE(e.data.length, 22); lh.writeUInt16LE(name.length, 26);
    const ch = Buffer.alloc(46);
    ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(20, 4); ch.writeUInt16LE(20, 6); ch.writeUInt16LE(method, 10);
    ch.writeUInt32LE(crc, 16); ch.writeUInt32LE(data.length, 20); ch.writeUInt32LE(e.data.length, 24); ch.writeUInt16LE(name.length, 28); ch.writeUInt32LE(off, 42);
    parts.push(lh, name, data); cd.push(ch, name);
    off += 30 + name.length + data.length;
  }
  const dir = Buffer.concat(cd);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(dir.length, 12); end.writeUInt32LE(off, 16);
  return Buffer.concat([...parts, dir, end]);
}

const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

function release(version, { tamper = false, stub = STUB } = {}) { // archive for THIS platform + SHA256SUMS
  const t = inst.target(process.platform, process.arch, version);
  const data = Buffer.from(stub);
  const top = `sweef-${version}-${t.os}-${t.arch}`;
  let archive;
  if (t.ext === 'zip') {
    archive = zipOf(process.platform === 'darwin' ? [{ name: 'Sweef.app/Contents/MacOS/sweef', data }] : [{ name: `${top}/sweef.exe`, data }]);
  } else {
    archive = tarOf([{ name: `${top}/NOTICE`, data: Buffer.from('n') }, { name: `${top}/bin/sweef`, data }]);
  }
  const sums = `${sha(archive)}  ${t.asset}\n${'0'.repeat(64)}  other.zip\n`;
  const served = tamper ? Buffer.concat([archive, Buffer.from([0])]) : archive;
  return { t, archive: served, sums, hash: sha(archive) };
}

function fakeServer(rel) {
  const hits = { archive: 0, sums: 0 };
  const srv = http.createServer((req, res) => {
    if (req.url === `/v${VER}/SHA256SUMS`) { hits.sums++; res.end(rel.sums); return; }
    if (req.url === `/v${VER}/${rel.t.asset}`) { hits.archive++; res.end(rel.archive); return; }
    res.statusCode = 404; res.end('nope');
  });
  return new Promise((r) => srv.listen(0, '127.0.0.1', () => r({ srv, hits, base: `http://127.0.0.1:${srv.address().port}` })));
}

// run fn with a hermetic environment: empty PATH, no SWEEF_*, temp cache.
async function hermetic(fn, extra = {}) {
  const keys = ['PATH', 'SWEEF_BIN', 'SWEEF_VERSION', 'SWEEF_DOWNLOAD_BASE', 'SWEEF_SKIP_DOWNLOAD', 'SWEEF_ENGINE_CACHE', 'HTTP_PROXY', 'http_proxy'];
  const saved = Object.fromEntries(keys.map((k) => [k, process.env[k]]));
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-inst-'));
  const empty = path.join(tmp, 'emptybin'); fs.mkdirSync(empty);
  for (const k of keys) delete process.env[k];
  Object.assign(process.env, { PATH: empty, SWEEF_ENGINE_CACHE: path.join(tmp, 'cache'), SWEEF_VERSION: VER }, extra);
  try { return await fn(tmp, empty); } finally {
    for (const k of keys) { if (saved[k] === undefined) delete process.env[k]; else process.env[k] = saved[k]; }
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

const quiet = { log: () => {} };
const posixOnly = process.platform === 'win32' ? { skip: 'stub is a shell script' } : {};

test('download succeeds, verifies, installs and runs', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    const logs = [];
    const p = await inst.ensureBinary({ base: f.base, log: (m) => logs.push(m) });
    assert.equal(p, path.join(process.env.SWEEF_ENGINE_CACHE, VER, rel.t.exe));
    assert.equal(execFileSync(p, ['--version']).toString().trim(), 'sweef stub 9.9.9');
    assert.equal(fs.statSync(p).mode & 0o111, 0o111);
    assert.equal(logs.length, 1);
    assert.match(logs[0], /downloading engine 9\.9\.9/);
    assert.deepEqual(fs.readdirSync(path.join(process.env.SWEEF_ENGINE_CACHE, VER)), [rel.t.exe], 'no temp or lock files left');
  } finally { f.srv.close(); }
}));

test('SWEEF_DOWNLOAD_BASE env is used and second call is a cache hit', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    process.env.SWEEF_DOWNLOAD_BASE = f.base + '/';
    const logs = [];
    const a = await inst.ensureBinary({ log: (m) => logs.push(m) });
    const b = await inst.ensureBinary({ log: (m) => logs.push(m) });
    assert.equal(a, b);
    assert.deepEqual(f.hits, { archive: 1, sums: 1 });
    assert.equal(logs.length, 1);
  } finally { f.srv.close(); }
}));

test('checksum mismatch is rejected and nothing is installed', posixOnly, async () => hermetic(async () => {
  const rel = release(VER, { tamper: true }); const f = await fakeServer(rel);
  try {
    await assert.rejects(inst.ensureBinary({ base: f.base, ...quiet }), /checksum mismatch/);
    const dir = path.join(process.env.SWEEF_ENGINE_CACHE, VER);
    assert.deepEqual(fs.readdirSync(dir), []);
  } finally { f.srv.close(); }
}));

test('pinned checksum wins over SHA256SUMS (no SHA256SUMS fetch)', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    await assert.rejects(inst.ensureBinary({ base: f.base, pins: { [rel.t.asset]: 'f'.repeat(64) }, ...quiet }), /checksum mismatch/);
    assert.equal(f.hits.sums, 0);
    const p = await inst.ensureBinary({ base: f.base, pins: { [rel.t.asset]: rel.hash }, ...quiet });
    assert.ok(fs.existsSync(p));
  } finally { f.srv.close(); }
}));

test('asset missing from SHA256SUMS and HTTP errors are actionable', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); rel.sums = `${'0'.repeat(64)}  other.zip\n`;
  const f = await fakeServer(rel);
  try {
    await assert.rejects(inst.ensureBinary({ base: f.base, ...quiet }), /not listed in .*SHA256SUMS/);
    process.env.SWEEF_VERSION = '1.0.0'; // server has no such release: 404
    await assert.rejects(inst.ensureBinary({ base: f.base, ...quiet }), /could not download the sweef engine.*HTTP 404.*SWEEF_BIN/s);
  } finally { f.srv.close(); }
}));

test('offline gives an actionable error', async () => hermetic(async () => {
  await assert.rejects(inst.ensureBinary({ base: 'http://127.0.0.1:9', ...quiet }), /could not download.*HTTPS_PROXY.*SWEEF_SKIP_DOWNLOAD/s);
}));

test('SWEEF_SKIP_DOWNLOAD behaves as before: clear error, no network, no cache writes', async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    process.env.SWEEF_SKIP_DOWNLOAD = '1';
    await assert.rejects(inst.ensureBinary({ base: f.base, ...quiet }), /sweef binary not found: set SWEEF_BIN, put sweef on PATH/);
    assert.deepEqual(f.hits, { archive: 0, sums: 0 });
    assert.equal(fs.existsSync(process.env.SWEEF_ENGINE_CACHE), false);
    process.env.SWEEF_SKIP_DOWNLOAD = '0'; // 0/false do not disable
    assert.equal(inst.lookupBinary(), null);
  } finally { f.srv.close(); }
}));

test('precedence: explicit binary > SWEEF_BIN > PATH > cache', posixOnly, async () => hermetic(async (tmp, empty) => {
  const mk = (dir, name = 'sweef') => { fs.mkdirSync(dir, { recursive: true }); const p = path.join(dir, name); fs.writeFileSync(p, STUB, { mode: 0o755 }); return p; };
  const cached = mk(path.join(process.env.SWEEF_ENGINE_CACHE, VER), inst.target(process.platform, process.arch, VER).exe);
  assert.equal(inst.lookupBinary(), cached);
  const onPath = mk(path.join(tmp, 'pathbin'));
  process.env.PATH = [empty, path.join(tmp, 'pathbin')].join(path.delimiter);
  assert.equal(inst.lookupBinary(), onPath);
  process.env.SWEEF_BIN = '/env/sweef';
  assert.equal(inst.lookupBinary(), '/env/sweef');
  assert.equal(await inst.ensureBinary({ binary: '/explicit/sweef' }), '/explicit/sweef');
  assert.equal(await inst.ensureBinary(), '/env/sweef');
}));

test('the SDK launcher shim on PATH is skipped', posixOnly, async () => hermetic(async (tmp, empty) => {
  const shimDir = path.join(tmp, 'shim'); fs.mkdirSync(shimDir);
  const shim = path.join(shimDir, 'sweef');
  fs.writeFileSync(shim, `#!/usr/bin/env node\n// ${inst.LAUNCHER_MARKER}\n`, { mode: 0o755 });
  const link = path.join(shimDir, 'link'); fs.mkdirSync(link); fs.symlinkSync(shim, path.join(link, 'sweef'));
  process.env.PATH = [shimDir, link].join(path.delimiter);
  assert.equal(inst.lookupBinary(), null);
}));

test('platform/arch mapping table', () => {
  const rows = [
    ['linux', 'x64', 'sweef_1.2.3_linux_amd64.tar.gz', 'sweef'],
    ['linux', 'arm64', 'sweef_1.2.3_linux_arm64.tar.gz', 'sweef'],
    ['darwin', 'x64', 'sweef_1.2.3_darwin_universal.zip', 'sweef'],
    ['darwin', 'arm64', 'sweef_1.2.3_darwin_universal.zip', 'sweef'],
    ['win32', 'x64', 'sweef_1.2.3_windows_amd64.zip', 'sweef.exe'],
    ['win32', 'arm64', 'sweef_1.2.3_windows_arm64.zip', 'sweef.exe'],
  ];
  for (const [p, a, asset, exe] of rows) { const t = inst.target(p, a, '1.2.3'); assert.equal(t.asset, asset); assert.equal(t.exe, exe); }
  assert.ok(inst.target('linux', 'x64', '1.2.3').member.test('sweef-1.2.3-linux-amd64/bin/sweef'));
  assert.ok(inst.target('darwin', 'arm64', '1.2.3').member.test('Sweef.app/Contents/MacOS/sweef'));
  assert.ok(inst.target('win32', 'x64', '1.2.3').member.test('sweef-1.2.3-windows-amd64/sweef.exe'));
  assert.ok(!inst.target('linux', 'x64', '1.2.3').member.test('sweef-1.2.3-linux-amd64/bin/sweef-browser'));
  for (const [p, a] of [['linux', 'ia32'], ['freebsd', 'x64'], ['linux', 'arm'], ['win32', 'ia32']]) assert.throws(() => inst.target(p, a, '1.2.3'), /no prebuilt sweef engine/);
});

test('cache locations per platform', () => {
  assert.equal(inst.cacheRoot({ XDG_CACHE_HOME: '/x' }, 'linux', '/h'), '/x/sweef');
  assert.equal(inst.cacheRoot({}, 'linux', '/h'), '/h/.cache/sweef');
  assert.equal(inst.cacheRoot({}, 'darwin', '/Users/u'), '/Users/u/Library/Caches/sweef');
  assert.equal(inst.cacheRoot({ LOCALAPPDATA: 'C:\\L' }, 'win32', 'C:\\U'), 'C:\\L\\sweef\\cache');
  assert.equal(inst.cacheRoot({ SWEEF_ENGINE_CACHE: '/c' }, 'linux', '/h'), '/c');
});

test('SWEEF_VERSION is validated', async () => hermetic(async () => {
  process.env.SWEEF_VERSION = 'v1.2.3-rc.1'; assert.equal(inst.engineVersion(), '1.2.3-rc.1');
  for (const bad of ['../x', 'a/b', '1 2', '..']) { process.env.SWEEF_VERSION = bad; assert.throws(() => inst.engineVersion(), /invalid SWEEF_VERSION/); }
}));

test('archive extraction: tar and zip happy paths (incl. stored zip)', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-x-'));
  try {
    const data = Buffer.from(STUB);
    const files = {
      tgz: tarOf([{ name: 'top/', data: Buffer.alloc(0), type: '5' }, { name: 'top/bin/sweef', data }]),
      zip: zipOf([{ name: 'top/sweef.exe', data }]),
      stored: zipOf([{ name: 'top/sweef.exe', data }], 0),
    };
    const w = (n) => { const p = path.join(tmp, n); fs.writeFileSync(p, files[n]); return p; };
    assert.deepEqual(inst.extractMember(w('tgz'), 'tar.gz', /^[^/]+\/bin\/sweef$/), data);
    assert.deepEqual(inst.extractMember(w('zip'), 'zip', /^[^/]+\/sweef\.exe$/), data);
    assert.deepEqual(inst.extractMember(w('stored'), 'zip', /^[^/]+\/sweef\.exe$/), data);
    assert.throws(() => inst.extractMember(w('tgz'), 'tar.gz', /nothing/), /does not contain/);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});

test('archive extraction rejects path traversal and duplicates', () => {
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-x-'));
  try {
    const d = Buffer.from('x');
    const cases = [
      ['t1', 'tar.gz', tarOf([{ name: 'top/bin/sweef', data: d }, { name: '../evil', data: d }])],
      ['t2', 'tar.gz', tarOf([{ name: '/abs/evil', data: d }, { name: 'top/bin/sweef', data: d }])],
      ['t3', 'tar.gz', tarOf([{ name: 'a/../../evil', data: d }])],
      ['z1', 'zip', zipOf([{ name: 'top/sweef.exe', data: d }, { name: '../../evil', data: d }])],
      ['z2', 'zip', zipOf([{ name: 'C:\\evil.exe', data: d }])],
      ['dup', 'tar.gz', tarOf([{ name: 'a/bin/sweef', data: d }, { name: 'b/bin/sweef', data: d }])],
    ];
    for (const [n, ext, buf] of cases) {
      const p = path.join(tmp, n); fs.writeFileSync(p, buf);
      assert.throws(() => inst.extractMember(p, ext, /^[^/]+\/(bin\/sweef|sweef\.exe)$/), /unsafe path|more than one/, n);
    }
    assert.equal(fs.existsSync(path.join(tmp, '..', 'evil')), false);
  } finally { fs.rmSync(tmp, { recursive: true, force: true }); }
});

test('concurrent first runs in separate processes download once', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    const code = `require(${JSON.stringify(path.join(__dirname, '..', 'install.js'))}).ensureBinary({log:()=>{}}).then(p=>console.log(p),e=>{console.error(e.message);process.exit(1)})`;
    const env = { ...process.env, SWEEF_DOWNLOAD_BASE: f.base };
    const runs = Array.from({ length: 5 }, () => new Promise((resolve) => {
      const c = spawn(process.execPath, ['-e', code], { env }); let out = '', err = '';
      c.stdout.on('data', (d) => { out += d; }); c.stderr.on('data', (d) => { err += d; });
      c.on('close', (status) => resolve({ status, out: out.trim(), err }));
    }));
    const res = await Promise.all(runs);
    for (const r of res) assert.equal(r.status, 0, r.err);
    assert.equal(new Set(res.map((r) => r.out)).size, 1);
    assert.equal(f.hits.archive, 1);
    // in-process dedupe too
    fs.rmSync(path.join(process.env.SWEEF_ENGINE_CACHE), { recursive: true, force: true });
    const [a, b] = await Promise.all([inst.ensureBinary({ base: f.base, ...quiet }), inst.ensureBinary({ base: f.base, ...quiet })]);
    assert.equal(a, b);
    assert.equal(f.hits.archive, 2);
  } finally { f.srv.close(); }
}));

test('a stale lock from a dead process is broken', posixOnly, async () => hermetic(async () => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    const dir = path.join(process.env.SWEEF_ENGINE_CACHE, VER); fs.mkdirSync(dir, { recursive: true });
    fs.writeFileSync(path.join(dir, '.lock'), `2147483646 ${Date.now()}\n`);
    const p = await inst.ensureBinary({ base: f.base, ...quiet });
    assert.ok(fs.existsSync(p));
  } finally { f.srv.close(); }
}));

test('launcher and sweef-install run the stub (global command path)', posixOnly, async () => hermetic(async (tmp) => {
  const rel = release(VER); const f = await fakeServer(rel);
  try {
    const env = { ...process.env, SWEEF_DOWNLOAD_BASE: f.base };
    const bin = (n) => path.join(__dirname, '..', 'bin', n);
    assert.equal((await run(process.execPath, [bin('sweef.js'), '--version'], { env })).stdout.trim(), 'sweef stub 9.9.9');
    const p = (await run(process.execPath, [bin('sweef-install.js')], { env })).stdout.trim();
    assert.equal(p, path.join(process.env.SWEEF_ENGINE_CACHE, VER, rel.t.exe));
    assert.equal((await run(process.execPath, [bin('sweef-install.js'), '--print-path'], { env })).stdout.trim(), p);
    assert.equal(f.hits.archive, 1);
    // postinstall never fails, even when the download fails
    const bad = { ...env, SWEEF_DOWNLOAD_BASE: 'http://127.0.0.1:9', SWEEF_ENGINE_CACHE: path.join(tmp, 'c2') };
    const post = path.join(__dirname, '..', 'scripts', 'postinstall.js');
    const r = await run(process.execPath, [post], { env: bad }); // resolves = exit code 0
    assert.match(r.stderr, /not downloaded at install time/);
    const skip = await run(process.execPath, [post], { env: { ...bad, SWEEF_SKIP_DOWNLOAD: '1' } });
    assert.equal(skip.stderr, '');
    const ok = await run(process.execPath, [post], { env: { ...env, SWEEF_ENGINE_CACHE: path.join(tmp, 'c3') } });
    assert.match(ok.stderr, /downloading engine/);
    assert.ok(fs.existsSync(path.join(tmp, 'c3', VER, rel.t.exe)));
  } finally { f.srv.close(); }
}));
