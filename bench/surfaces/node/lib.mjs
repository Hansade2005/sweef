// Shared helpers for the surface benchmarks: process-tree resource sampling,
// statistics, and a minimal stdio MCP client.
import { spawn } from 'node:child_process';
import fs from 'node:fs';

const PAGE = 4096;
const HZ = 100; // USER_HZ on Linux

export const median = (xs) => {
  const a = xs.filter((x) => Number.isFinite(x)).sort((p, q) => p - q);
  if (!a.length) return NaN;
  const m = a.length >> 1;
  return a.length % 2 ? a[m] : (a[m - 1] + a[m]) / 2;
};
export const round = (x, d = 1) => (Number.isFinite(x) ? Math.round(x * 10 ** d) / 10 ** d : null);
export const tokens = (bytes) => Math.round(bytes / 4);

function readStat(pid) {
  try {
    const s = fs.readFileSync(`/proc/${pid}/stat`, 'utf8');
    const rest = s.slice(s.lastIndexOf(')') + 2).split(' ');
    // fields after comm: state(0) ppid(1) ... utime(11) stime(12) cutime(13) cstime(14) ... rss(21)
    return { ppid: +rest[1], cpu: (+rest[11] + +rest[12] + +rest[13] + +rest[14]) / HZ, rss: +rest[21] * PAGE };
  } catch (_) { return null; }
}

function treePids(root) {
  const kids = new Map();
  for (const d of fs.readdirSync('/proc')) {
    if (!/^\d+$/.test(d)) continue;
    const st = readStat(+d);
    if (!st) continue;
    if (!kids.has(st.ppid)) kids.set(st.ppid, []);
    kids.get(st.ppid).push(+d);
  }
  const out = [];
  const stack = [root];
  while (stack.length) {
    const p = stack.pop();
    out.push(p);
    for (const k of kids.get(p) || []) stack.push(k);
  }
  return out;
}

// TreeSampler tracks the peak RSS (sum over the process tree) and the CPU seconds
// used by the tree (max seen per pid, so exited children still count).
export class TreeSampler {
  constructor(rootPids, everyMs = 50) {
    this.roots = Array.isArray(rootPids) ? rootPids : [rootPids];
    this.peakRss = 0;
    this.cpu = new Map();
    this.base = new Map();
    this.timer = setInterval(() => this.sample(), everyMs);
    this.timer.unref();
    this.sample();
  }
  addRoot(pid) { this.roots.push(pid); }
  sample() {
    let rss = 0;
    const seen = new Set();
    for (const r of this.roots) {
      for (const p of treePids(r)) {
        if (seen.has(p)) continue;
        seen.add(p);
        const st = readStat(p);
        if (!st) continue;
        rss += st.rss;
        this.cpu.set(p, Math.max(this.cpu.get(p) || 0, st.cpu));
      }
    }
    this.peakRss = Math.max(this.peakRss, rss);
    return rss;
  }
  stop() {
    this.sample();
    clearInterval(this.timer);
    let cpu = 0;
    for (const v of this.cpu.values()) cpu += v;
    return { peakRssMiB: round(this.peakRss / 1048576), cpuSec: round(cpu, 2) };
  }
}

// McpClient speaks newline-delimited JSON-RPC over a child's stdio.
export class McpClient {
  constructor(cmd, args, { env, cwd } = {}) {
    this.t0 = performance.now();
    this.proc = spawn(cmd, args, { stdio: ['pipe', 'pipe', 'pipe'], env: env || process.env, cwd });
    this.proc.stderr.on('data', (d) => { this.stderr = ((this.stderr || '') + d).slice(-4000); });
    this.buf = '';
    this.id = 0;
    this.waiters = new Map();
    this.proc.stdout.setEncoding('utf8');
    this.proc.stdout.on('data', (d) => {
      this.buf += d;
      let i;
      while ((i = this.buf.indexOf('\n')) >= 0) {
        const line = this.buf.slice(0, i);
        this.buf = this.buf.slice(i + 1);
        if (!line.trim()) continue;
        let m;
        try { m = JSON.parse(line); } catch (_) { continue; }
        const w = this.waiters.get(m.id);
        if (w) { this.waiters.delete(m.id); w({ msg: m, bytes: Buffer.byteLength(line) + 1 }); }
      }
    });
  }
  request(method, params, timeoutMs = 120000) {
    const id = ++this.id;
    const t0 = performance.now();
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => { this.waiters.delete(id); reject(new Error(`${method} timed out; stderr: ${this.stderr || ''}`)); }, timeoutMs);
      this.waiters.set(id, ({ msg, bytes }) => { clearTimeout(timer); resolve({ msg, bytes, ms: performance.now() - t0 }); });
      this.proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
    });
  }
  notify(method, params) { this.proc.stdin.write(JSON.stringify({ jsonrpc: '2.0', method, params }) + '\n'); }
  async initialize() {
    const r = await this.request('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'bench', version: '1' } });
    this.notify('notifications/initialized', {});
    return r;
  }
  // tool returns {ms, bytes, text, textBytes, imageBytes, isError}
  async tool(name, args) {
    const r = await this.request('tools/call', { name, arguments: args });
    const res = r.msg.result || {};
    let text = '';
    let imageBytes = 0;
    for (const c of res.content || []) {
      if (c.type === 'text') text += c.text;
      else if (c.type === 'image') imageBytes += (c.data || '').length;
    }
    if (r.msg.error) text = JSON.stringify(r.msg.error);
    return { ms: r.ms, bytes: r.bytes, text, textBytes: Buffer.byteLength(text), imageBytes, isError: !!res.isError || !!r.msg.error };
  }
  async close() {
    this.proc.stdin.end();
    await Promise.race([new Promise((r) => this.proc.once('exit', r)), new Promise((r) => setTimeout(r, 3000))]);
    if (this.proc.exitCode === null) {
      try { process.kill(this.proc.pid, 'SIGKILL'); } catch (_) { /* gone */ }
    }
  }
}

// findRef finds the ref of `role "name"` in a YAML snapshot (Sweef: [level=1 ref=e7],
// Playwright: [ref=e7] [cursor=pointer]).
export function findRef(snapshot, role, name) {
  for (const line of snapshot.split('\n')) {
    if (line.includes(`${role} "${name}"`)) {
      const m = line.match(/ref=([A-Za-z0-9_]+)/);
      if (m) return m[1];
    }
  }
  return null;
}

export const realSites = [
  'https://en.wikipedia.org/wiki/Alan_Turing',
  'https://github.com/golang/go',
  'https://news.ycombinator.com/',
  'https://developer.mozilla.org/en-US/docs/Web/JavaScript/Reference/Global_Objects/Array/map',
  'https://docs.python.org/3/library/json.html',
  'https://books.toscrape.com/',
  'https://www.rust-lang.org/',
  'https://example.com',
];
