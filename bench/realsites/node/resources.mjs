// Resource use per site and tool, each in a fresh process: wall time and CPU (user+sys) for the load and for
// load + interactions, peak resident memory (sum over the process tree, sampled every 50 ms), and CPU burnt
// while the page sits idle for 5 s after the interactions. Chromium = the whole browser process tree
// (browser, renderer, GPU, utility processes); Sweef = the session daemon (the CLI client processes live a
// few milliseconds per command and are not counted).
//
//   node resources.mjs --sweef /path/to/sweef [--runs 3] [--out ../results/resources-X.json]
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright-core';
import { CHROMIUM, sleep, rmTree } from './lib.mjs';

const opt = { sweef: 'sweef', runs: 3, out: '', idleMs: 5000 };
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i], nx = () => process.argv[++i];
  if (a === '--sweef') opt.sweef = nx(); else if (a === '--runs') opt.runs = +nx(); else if (a === '--out') opt.out = nx();
}
const TICK = 100 / 1000 * 0 + (spawnSync('getconf', ['CLK_TCK'], { encoding: 'utf8' }).stdout.trim() | 0 || 100);
const SITES = [
  { id: 't3a', url: 'https://the3rdacademy.com/', steps: [{ click: 'header a[href="/platform"]' }, { scroll: 2400 }] },
  { id: 'pipilot', url: 'https://www.pipilot.dev/', steps: [{ click: 'a[href="/pricing"]' }, { scroll: 2400 }] },
];

function procs(match) { // pids whose command line contains match
  const out = [];
  for (const d of fs.readdirSync('/proc')) {
    if (!/^\d+$/.test(d)) continue;
    try { if (fs.readFileSync(`/proc/${d}/cmdline`, 'utf8').includes(match)) out.push(+d); } catch { /* gone */ }
  }
  return out;
}
function cpuOf(pids) { // seconds of user+sys, children included once they are reaped
  let t = 0;
  for (const p of pids) {
    try { const f = fs.readFileSync(`/proc/${p}/stat`, 'utf8').split(') ')[1].split(' '); t += (+f[11] + +f[12]) / TICK; } catch { /* gone */ }
  }
  return t;
}
function rssOf(pids) { // MiB
  let k = 0;
  for (const p of pids) { try { const m = /VmRSS:\s+(\d+)/.exec(fs.readFileSync(`/proc/${p}/status`, 'utf8')); if (m) k += +m[1]; } catch { /* gone */ } }
  return k / 1024;
}
function sampler(match) {
  let peak = 0, seen = new Set(), cpuGone = 0, last = new Map();
  const tick = () => {
    const ps = procs(match);
    for (const p of ps) { seen.add(p); last.set(p, cpuOf([p])); }
    for (const p of seen) if (!ps.includes(p) && last.has(p)) { cpuGone += last.get(p); last.delete(p); seen.delete(p); }
    peak = Math.max(peak, rssOf(ps));
  };
  const h = setInterval(tick, 50);
  return { tick, cpu: () => { tick(); return cpuGone + cpuOf([...seen]); }, peak: () => peak, stop: () => clearInterval(h) };
}

async function runChromium(site) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'res-chr-'));
  const s = sampler(dir);
  const t0 = performance.now();
  const ctx = await chromium.launchPersistentContext(dir, { executablePath: CHROMIUM, headless: true, args: ['--no-sandbox'], viewport: { width: 1280, height: 800 } });
  const page = ctx.pages()[0] || await ctx.newPage();
  await page.goto(site.url, { waitUntil: 'load', timeout: 30000 });
  const r = { load_wall_ms: performance.now() - t0, load_cpu_s: s.cpu() };
  for (const st of site.steps) {
    if (st.click) await page.click(st.click, { timeout: 15000 }); else if (st.scroll) await page.evaluate((y) => window.scrollTo(0, y), st.scroll);
    await page.waitForLoadState('load');
  }
  r.all_wall_ms = performance.now() - t0; r.all_cpu_s = s.cpu();
  const c0 = s.cpu(); await sleep(opt.idleMs); r.idle_cpu_s = s.cpu() - c0;
  r.peak_rss_mib = s.peak();
  s.stop(); await ctx.close(); rmTree(dir);
  return r;
}

async function runSweef(site) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'res-sw-'));
  const env = { ...process.env, SWEEF_HOME: home, SWEEF_CACHE_DIR: path.join(home, 'cache') };
  const run = (...a) => spawnSync(opt.sweef, [...a, '--timeout', '30s'], { env, encoding: 'utf8' });
  const s = sampler(path.join(home, 'default.sock'));
  const t0 = performance.now();
  run('open', 'about:blank'); run('set', 'viewport', '1280', '800');
  const o = run('open', site.url);
  const r = { load_wall_ms: performance.now() - t0, load_cpu_s: s.cpu(), load_ok: o.status === 0 };
  for (const st of site.steps) { if (st.click) run('click', st.click); else if (st.scroll) run('eval', `window.scrollTo(0, ${st.scroll})`); }
  r.all_wall_ms = performance.now() - t0; r.all_cpu_s = s.cpu();
  const c0 = s.cpu(); await sleep(opt.idleMs); r.idle_cpu_s = s.cpu() - c0;
  r.peak_rss_mib = s.peak();
  s.stop(); run('close'); rmTree(home);
  return r;
}

const med = (a) => { const b = a.filter((x) => x != null).sort((x, y) => x - y); return b.length ? b[Math.floor(b.length / 2)] : null; };
const rows = [];
for (const site of SITES) {
  for (const tool of ['chromium', 'sweef']) {
    const runs = [];
    for (let i = 0; i < opt.runs; i++) { runs.push(tool === 'sweef' ? await runSweef(site) : await runChromium(site)); await sleep(1000); }
    const m = {}; for (const k of Object.keys(runs[0])) if (typeof runs[0][k] === 'number') m[k] = med(runs.map((x) => x[k]));
    rows.push({ site: site.id, tool, median: m, runs });
    console.error(`${site.id.padEnd(8)} ${tool.padEnd(9)} load ${Math.round(m.load_wall_ms)} ms ${m.load_cpu_s.toFixed(2)} s cpu | all ${Math.round(m.all_wall_ms)} ms ${m.all_cpu_s.toFixed(2)} s | idle ${m.idle_cpu_s.toFixed(2)} s/5s | peak ${Math.round(m.peak_rss_mib)} MiB`);
  }
}
const res = { when: new Date().toISOString(), host: `${os.cpus().length} CPUs (${os.cpus()[0].model}), ${(os.totalmem() / 2 ** 30).toFixed(1)} GB`, runs: opt.runs, idle_ms: opt.idleMs, rows };
if (opt.out) fs.writeFileSync(opt.out, JSON.stringify(res, null, 1));
