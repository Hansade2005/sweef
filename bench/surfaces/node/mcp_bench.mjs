// MCP benchmark: Sweef (`sweef mcp`) vs Microsoft's @playwright/mcp, both over
// stdio JSON-RPC, same scripted flow, same machine, run one after the other.
//
//   node mcp_bench.mjs --sweef /path/to/sweef [--base http://127.0.0.1:18080] [-n 9]
//        [--real 7] [--only sweef|playwright|sweef-before] [--before /path/to/old-sweef] --out results.json
//
// Per call: wall latency (request written -> response parsed), response bytes
// (the whole JSON-RPC line), text bytes and approximate tokens (text/4), image
// bytes. Per product: cold start (spawn -> initialize -> navigate -> first
// snapshot), peak RSS and CPU seconds of the whole process tree.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { McpClient, TreeSampler, findRef, median, round, tokens, realSites } from './lib.mjs';

const argv = process.argv.slice(2);
const opt = (k, d) => { const i = argv.indexOf(k); return i >= 0 ? argv[i + 1] : d; };
const SWEEF = opt('--sweef');
const BEFORE = opt('--before');
const BASE = opt('--base', 'http://127.0.0.1:18080');
const N = +opt('-n', 9);
const REAL = +opt('--real', 7);
const ONLY = opt('--only', '');
const OUT = opt('--out', 'mcp-results.json');
const CHROME = opt('--chrome', '/opt/pw-browsers/chromium');
const HERE = path.dirname(new URL(import.meta.url).pathname);

// Adapters map the common flow onto each server's tools.
const products = {
  sweef: {
    spawn: (tmp) => [SWEEF, ['mcp'], { ...process.env, SWEEF_CACHE_DIR: path.join(tmp, 'cache') }],
    navigate: (c, url) => c.tool('browser_navigate', { url }),
    snapshot: (c) => c.tool('browser_snapshot', {}),
    click: (c, ref) => c.tool('browser_click', { selector: ref }),
    fill: (c, ref, text) => c.tool('browser_fill', { selector: ref, value: text }),
    evaluate: (c) => c.tool('browser_evaluate', { expression: 'document.title' }),
    screenshot: (c) => c.tool('browser_screenshot', {}),
    back: (c) => c.tool('browser_back', {}),
  },
  playwright: {
    spawn: (tmp) => ['node', [path.join(HERE, 'node_modules/@playwright/mcp/cli.js'), '--headless', '--isolated', '--no-sandbox',
      '--executable-path', CHROME, '--output-dir', path.join(tmp, 'out')], { ...process.env, PLAYWRIGHT_BROWSERS_PATH: '/opt/pw-browsers' }],
    navigate: (c, url) => c.tool('browser_navigate', { url }),
    snapshot: (c) => c.tool('browser_snapshot', {}),
    click: (c, ref, label) => c.tool('browser_click', { element: label, target: ref }),
    fill: (c, ref, text, label) => c.tool('browser_type', { element: label, target: ref, text }),
    evaluate: (c) => c.tool('browser_evaluate', { function: '() => document.title' }),
    screenshot: (c) => c.tool('browser_take_screenshot', { type: 'png' }),
    back: (c) => c.tool('browser_navigate_back', {}),
  },
};
if (BEFORE) {
  products['sweef-before'] = {
    ...products.sweef,
    spawn: (tmp) => [BEFORE, ['mcp'], { ...process.env, SWEEF_CACHE_DIR: path.join(tmp, 'cache') }],
    snapshot: (c) => c.tool('browser_aria_snapshot', {}), // 0.1: browser_snapshot was the JSON node list
  };
}

function rec(store, step, r) {
  (store[step] ||= []).push({ ms: r.ms, bytes: r.bytes, textBytes: r.textBytes, imageBytes: r.imageBytes, isError: r.isError });
}

function summarize(store, skipFirst) {
  const out = {};
  for (const [step, xs] of Object.entries(store)) {
    const warm = skipFirst && xs.length > 1 ? xs.slice(1) : xs;
    out[step] = {
      n: warm.length,
      medianMs: round(median(warm.map((x) => x.ms)), 2),
      firstMs: round(xs[0].ms, 2),
      medianBytes: median(warm.map((x) => x.bytes)),
      medianTextBytes: median(warm.map((x) => x.textBytes)),
      medianTextTokens: tokens(median(warm.map((x) => x.textBytes))),
      medianImageBytes: median(warm.map((x) => x.imageBytes)),
      errors: xs.filter((x) => x.isError).length,
    };
  }
  return out;
}

async function runProduct(name) {
  const a = products[name];
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), `surf-${name}-`));
  const [cmd, args, env] = a.spawn(tmp);
  const t0 = performance.now();
  const c = new McpClient(cmd, args, { env, cwd: tmp });
  const sampler = new TreeSampler(c.proc.pid);
  const result = { product: name, fixture: {}, real: {}, errors: [] };
  try {
    const init = await c.initialize();
    result.initializeMs = round(init.ms, 1);
    const tl = await c.request('tools/list', {});
    result.toolsListBytes = tl.bytes;
    result.toolsListTokens = tokens(tl.bytes);
    result.toolCount = tl.msg.result.tools.length;

    // cold start: spawn -> first snapshot of the fixture landing page
    const nav0 = await a.navigate(c, BASE + '/');
    const snap0 = await a.snapshot(c);
    result.coldStartToFirstSnapshotMs = round(performance.now() - t0, 1);
    result.coldFirstNavigateMs = round(nav0.ms, 1);
    if (!findRef(snap0.text, 'link', 'Sign in')) result.errors.push('first snapshot has no Sign in ref');

    // fixture flow, N iterations in one session (iteration 1 is reported as first, medians skip it)
    const store = {};
    let ok = 0;
    for (let i = 0; i < N; i++) {
      const nav = await a.navigate(c, BASE + '/'); rec(store, 'navigate', nav);
      const s1 = await a.snapshot(c); rec(store, 'snapshot', s1);
      const signIn = findRef(s1.text, 'link', 'Sign in');
      const ck = await a.click(c, signIn, 'Sign in link'); rec(store, 'click', ck);
      const s2 = await a.snapshot(c); rec(store, 'snapshot-2', s2);
      const email = findRef(s2.text, 'textbox', 'Email');
      const fl = await a.fill(c, email, 'ada@example.com', 'Email textbox'); rec(store, 'fill', fl);
      const ev = await a.evaluate(c); rec(store, 'evaluate', ev);
      const sh = await a.screenshot(c); rec(store, 'screenshot', sh);
      const bk = await a.back(c); rec(store, 'back', bk);
      const good = signIn && email && !ck.isError && !fl.isError && /Sign in/.test(ev.text) && sh.imageBytes > 0 && !bk.isError;
      if (good) ok++;
      else result.errors.push(`fixture iteration ${i}: signIn=${signIn} email=${email} click=${ck.isError} fill=${fl.isError} eval=${ev.text.slice(0, 80)} shot=${sh.imageBytes}`);
    }
    result.fixture = { iterations: N, succeeded: ok, steps: summarize(store, true) };
    const flowMs = (k) => Object.values(result.fixture.steps).reduce((s, x) => s + x[k], 0);
    result.fixture.flowMedianMs = round(flowMs('medianMs'), 1);
    result.fixture.flowTextTokens = Object.values(result.fixture.steps).reduce((s, x) => s + x.medianTextTokens, 0);
    result.fixture.flowBytes = Object.values(result.fixture.steps).reduce((s, x) => s + x.medianBytes, 0);
    result.fixtureResources = sampler.stop();

    // real pages: navigate, snapshot, evaluate, screenshot; REAL iterations per site
    const rs = new TreeSampler(c.proc.pid);
    for (const url of REAL > 0 ? realSites : []) {
      const st = {};
      let okSite = 0;
      for (let i = 0; i < REAL; i++) {
        const nav = await a.navigate(c, url); rec(st, 'navigate', nav);
        const sn = await a.snapshot(c); rec(st, 'snapshot', sn);
        const ev = await a.evaluate(c); rec(st, 'evaluate', ev);
        const sh = await a.screenshot(c); rec(st, 'screenshot', sh);
        if (!nav.isError && !sn.isError && sn.textBytes > 50 && sh.imageBytes > 0) okSite++;
        else result.errors.push(`${url} #${i}: nav=${nav.isError ? nav.text.slice(0, 100) : 'ok'} snap=${sn.textBytes} shot=${sh.imageBytes}`);
      }
      result.real[url] = { succeeded: okSite, steps: summarize(st, true) };
    }
    result.realResources = rs.stop();
  } catch (e) {
    result.errors.push(String(e && e.stack || e).slice(0, 500));
    result.fixtureResources ||= sampler.stop();
  } finally {
    await c.close();
    fs.rmSync(tmp, { recursive: true, force: true });
  }
  return result;
}

const names = ONLY ? ONLY.split(',') : Object.keys(products);
const results = { date: new Date().toISOString(), base: BASE, iterations: N, realIterations: REAL, host: { cpus: os.cpus().length, model: os.cpus()[0].model, node: process.version }, products: [] };
for (const n of names) {
  process.stderr.write(`mcp: ${n}...\n`);
  results.products.push(await runProduct(n));
}
fs.writeFileSync(OUT, JSON.stringify(results, null, 1));
process.stderr.write(`wrote ${OUT}\n`);
