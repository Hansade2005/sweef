#!/usr/bin/env node
// Time to a usable snapshot through bench/replay (recorded pages served from localhost): the noise-free
// companion of Mode A's live-network numbers. Per page and tool, N fresh-process runs with empty caches:
// Sweef = `sweef open` + `sweef snapshot` (fresh SWEEF_HOME), Chromium = a fresh launch, goto(load) and
// Playwright's AI snapshot, then (as Mode A does) snapshots every 250 ms until one holds the page's expected
// text, for up to 10 s. Reports median and spread (min..max) over the runs that got there.
//
//   node replay_table.mjs --sweef /path/to/sweef --replay 127.0.0.1:8898 --ca bundle.pem [--runs 5] [--out x.json]
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright-core';
import { CHROMIUM } from './lib.mjs';

const opt = { sweef: 'sweef', replay: '127.0.0.1:8898', ca: '', runs: 5, out: '' };
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i], nx = () => process.argv[++i];
  if (a === '--sweef') opt.sweef = nx(); else if (a === '--replay') opt.replay = nx(); else if (a === '--ca') opt.ca = nx();
  else if (a === '--runs') opt.runs = +nx(); else if (a === '--out') opt.out = nx();
}
const PAGES = [
  ['gh-repo', 'https://github.com/golang/go', /golang|The Go programming language/],
  ['gh-issue', 'https://github.com/golang/go/issues/9', /already used the name/],
  ['wiki', 'https://en.wikipedia.org/wiki/Go_(programming_language)', /Robert Griesemer/],
  ['mdn', 'https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Array/map', /map\(\)/],
  ['nextjs', 'https://nextjs.org/docs', /Next\.js/],
  ['pipilot', 'https://www.pipilot.dev/', /PiPilot/i],
  ['t3a', 'https://the3rdacademy.com/', /Beyond\s*Credentials/],
  ['react', 'https://react.dev/learn', /Quick Start/],
  ['pypi', 'https://pypi.org/project/requests/', /requests/],
  ['hn', 'https://news.ycombinator.com/', /Hacker News/],
];

function sweefRun(url, want) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'rt-sw-'));
  const env = { ...process.env, SWEEF_HOME: home, SWEEF_CACHE_DIR: path.join(home, 'cache'), HTTPS_PROXY: `http://${opt.replay}`, https_proxy: `http://${opt.replay}`, SSL_CERT_FILE: opt.ca };
  const t0 = performance.now();
  const o = spawnSync(opt.sweef, ['open', url, '--timeout', '30s'], { env, encoding: 'utf8' });
  let s;
  for (;;) {
    s = spawnSync(opt.sweef, ['snapshot'], { env, encoding: 'utf8', maxBuffer: 64 << 20 });
    if ((s.status === 0 && want.test(s.stdout)) || performance.now() - t0 > 10000) break;
    spawnSync('sleep', ['0.25']);
  }
  const ms = performance.now() - t0;
  spawnSync(opt.sweef, ['close'], { env });
  spawnSync('pkill', ['-f', `${home}/default.sock`]);
  fs.rmSync(home, { recursive: true, force: true });
  return { ms, ok: o.status === 0 && s.status === 0 && want.test(s.stdout) };
}

async function chromeRun(url, want) {
  const t0 = performance.now();
  const b = await chromium.launch({ executablePath: CHROMIUM, headless: true, args: ['--no-sandbox', `--proxy-server=http://${opt.replay}`, '--ignore-certificate-errors', '--proxy-bypass-list=<-loopback>'] });
  let ok = false;
  try {
    const p = await (await b.newContext({ ignoreHTTPSErrors: true, viewport: { width: 1280, height: 800 } })).newPage();
    await p.goto(url, { waitUntil: 'load', timeout: 30000 });
    for (;;) {
      let s = await p._snapshotForAI();
      s = typeof s === 'string' ? s : (s.full || '');
      ok = want.test(s);
      if (ok || performance.now() - t0 > 10000) break;
      await p.waitForTimeout(250);
    }
  } catch { /* counted as a failed run */ }
  const ms = performance.now() - t0;
  await b.close();
  return { ms, ok };
}

const stat = (rs) => {
  const v = rs.filter((r) => r.ok).map((r) => r.ms).sort((a, b) => a - b);
  return { passed: v.length, runs: rs.length, median: v.length ? v[Math.floor(v.length / 2)] : null, min: v[0] ?? null, max: v.at(-1) ?? null };
};
const rows = [];
for (const [id, url, want] of PAGES) {
  const sw = [], ch = [];
  for (let i = 0; i < opt.runs; i++) { sw.push(sweefRun(url, want)); ch.push(await chromeRun(url, want)); }
  const r = { id, url, sweef: stat(sw), chromium: stat(ch) };
  rows.push(r);
  const f = (s) => (s.median == null ? 'FAIL' : `${Math.round(s.median)} (${Math.round(s.min)}..${Math.round(s.max)}) ${s.passed}/${s.runs}`);
  console.error(`${id.padEnd(9)} sweef ${f(r.sweef).padEnd(26)} chromium ${f(r.chromium)}`);
}
const res = { when: new Date().toISOString(), network: 'bench/replay (local recording)', runs: opt.runs, sweef: spawnSync(opt.sweef, ['version'], { encoding: 'utf8' }).stdout.trim(), rows };
if (opt.out) fs.writeFileSync(opt.out, JSON.stringify(res, null, 1));
