// CLI benchmark: the Sweef session CLI vs Vercel's agent-browser CLI. Both keep a
// daemon between commands; every command is a separate process, as an agent's
// shell tool would run it.
//
//   node cli_bench.mjs --sweef /path/to/sweef [--ab path/to/agent-browser] [-r 7] --out cli.json
//
// Per command: wall time of the process (spawn -> exit) and stdout bytes. Per
// round: the first `open` starts the daemon (and, for agent-browser, Chromium).
// Peak RSS and CPU are of the daemon's process tree (found by command line).
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { median, round, tokens } from './lib.mjs';

const HERE = path.dirname(new URL(import.meta.url).pathname);
const argv = process.argv.slice(2);
const opt = (k, d) => { const i = argv.indexOf(k); return i >= 0 ? argv[i + 1] : d; };
const SWEEF = opt('--sweef');
const AB = opt('--ab', path.join(HERE, 'node_modules/agent-browser/bin/agent-browser-linux-x64'));
const BASE = opt('--base', 'http://127.0.0.1:18080');
const R = +opt('-r', 7);
const OUT = opt('--out', 'cli-results.json');
const CHROME = opt('--chrome', '/opt/pw-browsers/chromium');
const WARM = argv.includes('--warm-cache');

const PAGE = 4096;
function procs() {
  const out = [];
  for (const d of fs.readdirSync('/proc')) {
    if (!/^\d+$/.test(d)) continue;
    try {
      const s = fs.readFileSync(`/proc/${d}/stat`, 'utf8');
      const rest = s.slice(s.lastIndexOf(')') + 2).split(' ');
      let cmd = fs.readFileSync(`/proc/${d}/cmdline`, 'utf8').replace(/\0/g, ' ');
      if (/agent-browser|chrom/.test(cmd)) { // agent-browser's daemon names no path of ours on its command line; its environment does
        try { cmd += ' ENV ' + fs.readFileSync(`/proc/${d}/environ`, 'utf8').replace(/\0/g, ' '); } catch (_) { /* gone */ }
      }
      out.push({ pid: +d, ppid: +rest[1], cpu: (+rest[11] + +rest[12] + +rest[13] + +rest[14]) / 100, rss: +rest[21] * PAGE, cmd });
    } catch (_) { /* gone */ }
  }
  return out;
}

// PatternSampler: peak RSS and CPU of every process tree whose root's command line matches.
class PatternSampler {
  constructor(match) { this.match = match; this.peak = 0; this.cpu = new Map(); this.t = setInterval(() => this.sample(), 50); }
  sample() {
    const ps = procs();
    const kids = new Map();
    for (const p of ps) { if (!kids.has(p.ppid)) kids.set(p.ppid, []); kids.get(p.ppid).push(p); }
    const seen = new Set();
    let rss = 0;
    const stack = ps.filter((p) => this.match(p.cmd));
    while (stack.length) {
      const p = stack.pop();
      if (seen.has(p.pid)) continue;
      seen.add(p.pid);
      rss += p.rss;
      this.cpu.set(p.pid, Math.max(this.cpu.get(p.pid) || 0, p.cpu));
      for (const k of kids.get(p.pid) || []) stack.push(k);
    }
    this.peak = Math.max(this.peak, rss);
  }
  stop() { this.sample(); clearInterval(this.t); let c = 0; for (const v of this.cpu.values()) c += v; return { peakRssMiB: round(this.peak / 1048576), cpuSec: round(c, 2) }; }
}

// run is asynchronous so the resource sampler keeps sampling while a command runs.
function run(bin, args, env) {
  return new Promise((resolve) => {
    const t0 = performance.now();
    const c = spawn(bin, args, { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '';
    let err = '';
    c.stdout.setEncoding('utf8').on('data', (d) => { out += d; });
    c.stderr.setEncoding('utf8').on('data', (d) => { err += d; });
    const timer = setTimeout(() => c.kill('SIGKILL'), 60000);
    c.on('close', (code) => { clearTimeout(timer); resolve({ ms: performance.now() - t0, out, err, code }); });
  });
}

const refOf = (snap, role, name) => {
  for (const line of snap.split('\n')) if (line.includes(`${role} "${name}"`)) { const m = line.match(/ref=(e\d+)/); if (m) return m[1]; }
  return null;
};

const products = {
  sweef: {
    env: (tmp) => ({ ...process.env, SWEEF_HOME: path.join(tmp, 'home'), SWEEF_CACHE_DIR: path.join(tmp, 'cache') }),
    bin: SWEEF,
    match: (tmp) => (cmd) => cmd.includes(' serve ') && cmd.includes(tmp),
    flow: (sh, png) => [
      ['open', ['open', BASE + '/']],
      ['snapshot-i', ['snapshot', '-i']],
      ['click', (prev) => ['click', '@' + refOf(prev['snapshot-i'], 'link', 'Sign in')]],
      ['snapshot', ['snapshot']],
      ['fill', ['fill', '#email', 'ada@example.com']],
      ['eval', ['eval', 'document.title']],
      ['screenshot', ['screenshot', png]],
      ['back', ['back']],
      ['get-title', ['get', 'title']],
      ['close', ['close']],
    ],
  },
  'agent-browser': {
    env: (tmp) => ({ ...process.env, AGENT_BROWSER_EXECUTABLE_PATH: CHROME, AGENT_BROWSER_SOCKET_DIR: path.join(tmp, 'ab'), HOME: path.join(tmp, 'abhome') }),
    bin: AB,
    match: (tmp) => (cmd) => cmd.includes('agent-browser') && cmd.includes(tmp) && !cmd.includes('cli_bench'),
    flow: (sh, png) => [
      ['open', ['open', BASE + '/']],
      ['snapshot-i', ['snapshot', '-i']],
      ['click', (prev) => ['click', '@' + refOf(prev['snapshot-i'], 'link', 'Sign in')]],
      ['snapshot', ['snapshot']],
      ['fill', ['fill', '#email', 'ada@example.com']],
      ['eval', ['eval', 'document.title']],
      ['screenshot', ['screenshot', png]],
      ['back', ['back']],
      ['get-title', ['get', 'title']],
      ['close', ['close']],
    ],
  },
};

const results = { date: new Date().toISOString(), rounds: R, warmCache: WARM, products: [] };
for (const name of (opt('--only', 'sweef,agent-browser')).split(',')) {
  const p = products[name];
  if (!p.bin || !fs.existsSync(p.bin)) { results.products.push({ product: name, error: `binary not found: ${p.bin}` }); continue; }
  process.stderr.write(`cli: ${name}...\n`);
  const steps = {};
  const rounds = [];
  let ok = 0;
  // --warm-cache: one cache/profile directory for all rounds, primed by an untimed round
  // (a second session on the same machine); default: every round starts with empty caches.
  const shared = WARM ? fs.mkdtempSync(path.join(os.tmpdir(), `cli-${name}-w-`)) : null;
  for (let r = WARM ? -1 : 0; r < R; r++) {
    const tmp = shared || fs.mkdtempSync(path.join(os.tmpdir(), `cli-${name}-`));
    fs.mkdirSync(path.join(tmp, 'abhome'), { recursive: true });
    const env = p.env(tmp);
    const sampler = new PatternSampler(p.match(tmp));
    const prev = {};
    const png = path.join(tmp, 'shot.png');
    const t0 = performance.now();
    let good = true;
    for (const [k, a] of p.flow(null, png)) {
      const args = typeof a === 'function' ? a(prev) : a;
      const res = await run(p.bin, args, env);
      if (r === 0 && !res.out) process.stderr.write(`  ${name} ${k}: empty stdout, stderr ${res.err.slice(0, 200)}\n`);
      prev[k] = res.out;
      (steps[k] ||= []).push({ ms: res.ms, bytes: Buffer.byteLength(res.out) });
      if (res.code !== 0) { good = false; process.stderr.write(`  ${name} ${k} exit ${res.code}: ${(res.err || res.out).slice(0, 200)}\n`); }
    }
    const total = performance.now() - t0;
    const titleOK = /Acme/.test(prev['get-title'] || '');
    const shotOK = fs.existsSync(png) && fs.statSync(png).size > 1000;
    const res = sampler.stop();
    if (r < 0) { for (const k of Object.keys(steps)) steps[k] = []; await new Promise((z) => setTimeout(z, 500)); continue; } // priming round
    if (good && titleOK && shotOK) ok++;
    rounds.push({ totalMs: total, ...res });
    if (!shared) fs.rmSync(tmp, { recursive: true, force: true });
    await new Promise((z) => setTimeout(z, 500)); // let the closed daemon and browser exit before the next round
  }
  if (shared) fs.rmSync(shared, { recursive: true, force: true });
  results.products.push({
    product: name, succeeded: ok, rounds: R,
    flowTotalMs: round(median(rounds.map((x) => x.totalMs)), 1),
    peakRssMiB: round(median(rounds.map((x) => x.peakRssMiB)), 1),
    cpuSecPerRound: round(median(rounds.map((x) => x.cpuSec)), 2),
    steps: Object.fromEntries(Object.entries(steps).map(([k, v]) => [k, {
      medianMs: round(median(v.map((x) => x.ms)), 1), medianBytes: median(v.map((x) => x.bytes)), medianTokens: tokens(median(v.map((x) => x.bytes))) }])),
  });
}
fs.writeFileSync(OUT, JSON.stringify(results, null, 1));
process.stderr.write(`wrote ${OUT}\n`);
