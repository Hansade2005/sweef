// SDK benchmark: @sweef/browser (Node SDK + `sweef serve`) vs playwright-core
// driving the preinstalled Chromium, same scripted flow on the local fixture.
//
//   node sdk_bench.mjs --sweef /path/to/sweef [--base URL] [-r 5] [-n 9] --out sdk.json
//
// Every round is a fresh Node process (so script startup and browser launch are
// measured cold); the parent samples the RSS and CPU of the child's process tree.
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { TreeSampler, median, round } from './lib.mjs';

const HERE = path.dirname(new URL(import.meta.url).pathname);
const argv = process.argv.slice(2);
const opt = (k, d) => { const i = argv.indexOf(k); return i >= 0 ? argv[i + 1] : d; };

if (argv[0] === '--child') {
  const [, product, base, n, sweefBin] = argv;
  const T = { scriptStart: performance.now() };
  const steps = {};
  const time = async (k, fn) => { const t0 = performance.now(); const v = await fn(); (steps[k] ||= []).push(performance.now() - t0); return v; };
  let api;
  if (product === 'sweef') {
    const { Sweef } = await import(path.join(HERE, '../../../pkg/sdk/node/index.js'));
    T.imported = performance.now();
    const sw = await Sweef.launch({ binary: sweefBin, env: { SWEEF_CACHE_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'sdkc-')) } });
    T.launched = performance.now();
    const page = await sw.newPage();
    api = {
      page, close: () => sw.close(),
      snapshot: () => page.snapshot(),
      click: () => page.getByRole('link', { name: 'Sign in' }).click(),
      fill: () => page.getByLabel('Email').fill('ada@example.com'),
    };
  } else {
    const { chromium } = await import(path.join(HERE, 'node_modules/playwright-core/index.mjs'));
    T.imported = performance.now();
    const browser = await chromium.launch({ executablePath: '/opt/pw-browsers/chromium', headless: true, args: ['--no-sandbox'] });
    T.launched = performance.now();
    const page = await browser.newPage();
    api = {
      page, close: () => browser.close(),
      snapshot: () => page.locator('body').ariaSnapshot(),
      click: () => page.getByRole('link', { name: 'Sign in' }).click(),
      fill: () => page.getByLabel('Email').fill('ada@example.com'),
    };
  }
  T.pageReady = performance.now();
  const p = api.page;
  let ok = 0;
  for (let i = 0; i < +n; i++) {
    await time('goto', () => p.goto(base + '/'));
    if (i === 0) T.firstGoto = performance.now();
    const s = await time('snapshot', () => api.snapshot());
    if (i === 0) T.firstSnapshot = performance.now();
    await time('click', () => api.click());
    await time('snapshot-2', () => api.snapshot());
    await time('fill', () => api.fill());
    const title = await time('evaluate', () => p.evaluate('document.title'));
    const png = await time('screenshot', () => p.screenshot());
    await time('back', () => p.goBack());
    if (/Sign in/.test(s) && title === 'Sign in' && png.length > 1000) ok++;
  }
  const tc = performance.now();
  await api.close();
  T.closeMs = performance.now() - tc;
  process.stdout.write(JSON.stringify({ T, steps, ok }));
  process.exit(0);
}

const SWEEF = opt('--sweef');
const BASE = opt('--base', 'http://127.0.0.1:18080');
const R = +opt('-r', 5);
const N = +opt('-n', 9);
const OUT = opt('--out', 'sdk-results.json');

function runChild(product) {
  return new Promise((resolve) => {
    const c = spawn(process.execPath, [new URL(import.meta.url).pathname, '--child', product, BASE, String(N), SWEEF], { stdio: ['ignore', 'pipe', 'pipe'] });
    const s = new TreeSampler(c.pid);
    let out = '';
    let err = '';
    c.stdout.on('data', (d) => { out += d; });
    c.stderr.on('data', (d) => { err += d; });
    c.on('exit', () => {
      const res = s.stop();
      try { resolve({ ...JSON.parse(out), ...res }); } catch (_) { resolve({ error: err.slice(-500), ...res }); }
    });
  });
}

const results = { date: new Date().toISOString(), rounds: R, iterations: N, products: [] };
for (const product of (opt('--only', 'sweef,playwright')).split(',')) {
  process.stderr.write(`sdk: ${product}...\n`);
  const rounds = [];
  for (let r = 0; r < R; r++) rounds.push(await runChild(product));
  const good = rounds.filter((x) => !x.error);
  const steps = {};
  for (const x of good) for (const [k, v] of Object.entries(x.steps)) (steps[k] ||= []).push(...v.slice(1)); // warm iterations
  const med = (f) => round(median(good.map(f)), 1);
  results.products.push({
    product,
    errors: rounds.filter((x) => x.error).map((x) => x.error),
    succeeded: good.reduce((s, x) => s + x.ok, 0), attempted: R * N,
    scriptStartMs: med((x) => x.T.scriptStart),
    importMs: med((x) => x.T.imported - x.T.scriptStart),
    launchMs: med((x) => x.T.launched - x.T.imported),
    newPageMs: med((x) => x.T.pageReady - x.T.launched),
    processStartToFirstSnapshotMs: med((x) => x.T.firstSnapshot),
    closeMs: med((x) => x.T.closeMs),
    peakRssMiB: med((x) => x.peakRssMiB),
    cpuSecPerRound: round(median(good.map((x) => x.cpuSec)), 2),
    steps: Object.fromEntries(Object.entries(steps).map(([k, v]) => [k, { n: v.length, medianMs: round(median(v), 2) }])),
  });
}
fs.writeFileSync(OUT, JSON.stringify(results, null, 1));
process.stderr.write(`wrote ${OUT}\n`);
