// Turns the raw JSON of mcp_bench, sdk_bench and cli_bench into one markdown report.
//   node report.mjs --mcp mcp.json --sdk sdk.json --cli cli.json > report.md
import fs from 'node:fs';

const argv = process.argv.slice(2);
const opt = (k) => { const i = argv.indexOf(k); return i >= 0 ? argv[i + 1] : null; };
const load = (k) => (opt(k) && fs.existsSync(opt(k)) ? JSON.parse(fs.readFileSync(opt(k), 'utf8')) : null);
const mcp = load('--mcp');
const sdk = load('--sdk');
const cli = load('--cli');
const cliWarm = load('--cli-warm');
const out = [];
const w = (s = '') => out.push(s);
const f = (x, d = 1) => (x === null || x === undefined || Number.isNaN(x) ? 'n/a' : typeof x === 'number' ? (Math.abs(x) >= 100 ? Math.round(x).toString() : x.toFixed(d)) : String(x));
const ratio = (a, b) => (a > 0 && b > 0 ? (b / a >= 1 ? `${f(b / a)}x less` : `${f(a / b)}x more`) : 'n/a');
const faster = (a, b) => (a > 0 && b > 0 ? (b / a >= 1 ? `${f(b / a)}x faster` : `**${f(a / b)}x slower**`) : 'n/a');
const P = (j, name) => j && j.products.find((p) => p.product === name);

if (mcp) {
  const s = P(mcp, 'sweef');
  const pw = P(mcp, 'playwright');
  const b = P(mcp, 'sweef-before');
  w('## MCP: `sweef mcp` vs `@playwright/mcp`');
  w();
  w(`Fixture flow (bench/site), ${mcp.iterations} iterations in one session; medians of iterations 2..${mcp.iterations}. ${mcp.host.cpus} vCPU ${mcp.host.model}, Node ${mcp.host.node}.`);
  w();
  w('| | Sweef | @playwright/mcp | Sweef vs Playwright |');
  w('|---|---:|---:|---|');
  w(`| cold start: spawn to first snapshot (ms) | ${f(s.coldStartToFirstSnapshotMs)} | ${f(pw.coldStartToFirstSnapshotMs)} | ${faster(s.coldStartToFirstSnapshotMs, pw.coldStartToFirstSnapshotMs)} |`);
  w(`| tools/list (bytes / ~tokens, ${s.toolCount} vs ${pw.toolCount} tools) | ${s.toolsListBytes} / ${s.toolsListTokens} | ${pw.toolsListBytes} / ${pw.toolsListTokens} | ${ratio(s.toolsListBytes, pw.toolsListBytes)} |`);
  w(`| whole flow, sum of median call latencies (ms) | ${f(s.fixture.flowMedianMs)} | ${f(pw.fixture.flowMedianMs)} | ${faster(s.fixture.flowMedianMs, pw.fixture.flowMedianMs)} |`);
  w(`| whole flow, text tokens returned (~bytes/4) | ${s.fixture.flowTextTokens} | ${pw.fixture.flowTextTokens} | ${ratio(s.fixture.flowTextTokens, pw.fixture.flowTextTokens)} |`);
  w(`| peak RSS of the process tree (MiB) | ${f(s.fixtureResources.peakRssMiB)} | ${f(pw.fixtureResources.peakRssMiB)} | ${ratio(s.fixtureResources.peakRssMiB, pw.fixtureResources.peakRssMiB)} |`);
  w(`| CPU seconds, start + ${mcp.iterations} flows | ${f(s.fixtureResources.cpuSec, 2)} | ${f(pw.fixtureResources.cpuSec, 2)} | ${ratio(s.fixtureResources.cpuSec, pw.fixtureResources.cpuSec)} |`);
  w(`| flows that succeeded | ${s.fixture.succeeded}/${s.fixture.iterations} | ${pw.fixture.succeeded}/${pw.fixture.iterations} | |`);
  w();
  w('Per call (median ms; median response text ~tokens; image KB):');
  w();
  w('| call | Sweef ms | Playwright ms | speed | Sweef tok | Playwright tok | Sweef img KB | Playwright img KB |');
  w('|---|---:|---:|---|---:|---:|---:|---:|');
  for (const k of Object.keys(s.fixture.steps)) {
    const a = s.fixture.steps[k];
    const c = pw.fixture.steps[k] || {};
    w(`| ${k} | ${f(a.medianMs, 2)} | ${f(c.medianMs, 2)} | ${faster(a.medianMs, c.medianMs)} | ${a.medianTextTokens} | ${c.medianTextTokens} | ${f(a.medianImageBytes / 1024)} | ${f(c.medianImageBytes / 1024)} |`);
  }
  w();
  const sites = Object.keys(s.real || {});
  if (sites.length) {
    w(`Real pages (${mcp.realIterations} iterations per site, medians of 2..${mcp.realIterations}; live network, so latencies include the sites' own response times):`);
    w();
    w('| site | navigate ms S / P | snapshot ms S / P | snapshot ~tokens S / P | evaluate ms S / P | screenshot ms S / P | ok S / P |');
    w('|---|---:|---:|---:|---:|---:|---|');
    for (const u of sites) {
      const a = s.real[u].steps;
      const c = (pw.real[u] || {}).steps || {};
      const g = (x, k, m = 'medianMs') => (x[k] ? f(x[k][m]) : 'n/a');
      w(`| ${u.replace(/^https?:\/\//, '').slice(0, 48)} | ${g(a, 'navigate')} / ${g(c, 'navigate')} | ${g(a, 'snapshot')} / ${g(c, 'snapshot')} | ${g(a, 'snapshot', 'medianTextTokens')} / ${g(c, 'snapshot', 'medianTextTokens')} | ${g(a, 'evaluate')} / ${g(c, 'evaluate')} | ${g(a, 'screenshot')} / ${g(c, 'screenshot')} | ${s.real[u].succeeded} / ${(pw.real[u] || {}).succeeded ?? 'n/a'} |`);
    }
    w();
    w(`Real-page run resources: Sweef peak RSS ${f(s.realResources?.peakRssMiB)} MiB, CPU ${f(s.realResources?.cpuSec, 2)} s; Playwright peak RSS ${f(pw.realResources?.peakRssMiB)} MiB, CPU ${f(pw.realResources?.cpuSec, 2)} s.`);
    w();
  }
  if (b) {
    w('### Sweef MCP output size, before (0.1, a140c23) and after this change');
    w();
    w('| call | before ~tokens | after ~tokens | before bytes | after bytes |');
    w('|---|---:|---:|---:|---:|');
    for (const k of Object.keys(s.fixture.steps)) {
      const a = b.fixture.steps[k] || {};
      const c = s.fixture.steps[k];
      w(`| ${k} | ${a.medianTextTokens} | ${c.medianTextTokens} | ${a.medianBytes} | ${c.medianBytes} |`);
    }
    w(`| tools/list | ${b.toolsListTokens} | ${s.toolsListTokens} | ${b.toolsListBytes} | ${s.toolsListBytes} |`);
    if (sites.length && b.real) {
      for (const u of sites) {
        const a = (b.real[u] || {}).steps || {};
        const c = s.real[u].steps;
        w(`| ${u.replace(/^https?:\/\//, '').slice(0, 40)}: snapshot / screenshot text | ${a.snapshot?.medianTextTokens ?? 'n/a'} / ${a.screenshot?.medianTextTokens ?? 'n/a'} | ${c.snapshot.medianTextTokens} / ${c.screenshot.medianTextTokens} | | |`);
      }
    }
    w();
  }
  const errs = mcp.products.flatMap((p) => (p.errors || []).map((e) => `${p.product}: ${e}`));
  if (errs.length) { w('Errors recorded:'); w(); for (const e of errs.slice(0, 30)) w(`- ${e.replace(/\n/g, ' ').slice(0, 220)}`); w(); }
}

if (sdk) {
  const s = P(sdk, 'sweef');
  const pw = P(sdk, 'playwright');
  w('## SDK: `@sweef/browser` vs `playwright-core`');
  w();
  w(`${sdk.rounds} fresh Node processes per SDK, ${sdk.iterations} flow iterations each; cold numbers are medians over processes, per-call numbers medians over warm iterations.`);
  w();
  w('| | Sweef SDK | playwright-core | Sweef vs Playwright |');
  w('|---|---:|---:|---|');
  for (const [k, label] of [['importMs', 'import the SDK (ms)'], ['launchMs', 'launch (ms)'], ['newPageMs', 'new page (ms)'], ['processStartToFirstSnapshotMs', 'process start to first snapshot (ms)'], ['closeMs', 'close (ms)']]) {
    w(`| ${label} | ${f(s[k])} | ${f(pw[k])} | ${faster(s[k], pw[k])} |`);
  }
  w(`| peak RSS of the process tree (MiB) | ${f(s.peakRssMiB)} | ${f(pw.peakRssMiB)} | ${ratio(s.peakRssMiB, pw.peakRssMiB)} |`);
  w(`| CPU seconds per process | ${f(s.cpuSecPerRound, 2)} | ${f(pw.cpuSecPerRound, 2)} | ${ratio(s.cpuSecPerRound, pw.cpuSecPerRound)} |`);
  w(`| flows that succeeded | ${s.succeeded}/${s.attempted} | ${pw.succeeded}/${pw.attempted} | |`);
  w();
  w('| call | Sweef ms | playwright-core ms | speed |');
  w('|---|---:|---:|---|');
  for (const k of Object.keys(s.steps)) w(`| ${k} | ${f(s.steps[k].medianMs, 2)} | ${f(pw.steps[k]?.medianMs, 2)} | ${faster(s.steps[k].medianMs, pw.steps[k]?.medianMs)} |`);
  w();
}

function cliSection(cli, title) {
  const s = P(cli, 'sweef');
  const sc = P(cli, 'sweef-cli');
  const ab = P(cli, 'agent-browser');
  w(title);
  w();
  if (!ab || ab.error) {
    w(`agent-browser: not measured (${ab ? ab.error : 'missing'}).`);
  } else {
    w(`${cli.rounds} rounds; each round starts with no daemon, so \`open\` includes daemon (and browser) start. Every command is its own process. ${cli.warmCache ? 'Caches and browser profile are kept across rounds (primed by one untimed round): a second session on the same machine.' : 'Every round starts with empty caches (Sweef bytecode/HTTP/font caches, a fresh agent-browser HOME).'}`);
    w();
    w('| | sweef | agent-browser | sweef vs agent-browser |');
    w('|---|---:|---:|---|');
    w(`| whole flow, wall (ms) | ${f(s.flowTotalMs)} | ${f(ab.flowTotalMs)} | ${faster(s.flowTotalMs, ab.flowTotalMs)} |`);
    w(`| peak RSS of the daemon tree (MiB) | ${f(s.peakRssMiB)} | ${f(ab.peakRssMiB)} | ${ratio(s.peakRssMiB, ab.peakRssMiB)} |`);
    w(`| CPU seconds per round | ${f(s.cpuSecPerRound, 2)} | ${f(ab.cpuSecPerRound, 2)} | ${ratio(s.cpuSecPerRound, ab.cpuSecPerRound)} |`);
    w(`| rounds that succeeded | ${s.succeeded}/${s.rounds} | ${ab.succeeded}/${ab.rounds} | |`);
    w();
    w('| command | sweef ms | agent-browser ms | speed | sweef ~tokens | agent-browser ~tokens |');
    w('|---|---:|---:|---|---:|---:|');
    for (const k of Object.keys(s.steps)) {
      const a = s.steps[k];
      const c = ab.steps[k] || {};
      w(`| ${k} | ${f(a.medianMs)} | ${f(c.medianMs)} | ${faster(a.medianMs, c.medianMs)} | ${a.medianTokens} | ${c.medianTokens} |`);
    }
    if (sc && !sc.error) {
      w();
      w('`sweef-cli`, the session client without the engine linked in (same commands, same daemon):');
      w();
      w('| command | sweef-cli ms | sweef ms | agent-browser ms | sweef-cli vs agent-browser |');
      w('|---|---:|---:|---:|---|');
      w(`| whole flow | ${f(sc.flowTotalMs)} | ${f(s.flowTotalMs)} | ${f(ab.flowTotalMs)} | ${faster(sc.flowTotalMs, ab.flowTotalMs)} |`);
      for (const k of Object.keys(sc.steps)) {
        const c = ab.steps[k] || {};
        w(`| ${k} | ${f(sc.steps[k].medianMs)} | ${f(s.steps[k]?.medianMs)} | ${f(c.medianMs)} | ${faster(sc.steps[k].medianMs, c.medianMs)} |`);
      }
    }
  }
  w();
}
if (cli) cliSection(cli, '## CLI: `sweef` vs Vercel `agent-browser` (cold caches)');
if (cliWarm) cliSection(cliWarm, '## CLI: `sweef` vs Vercel `agent-browser` (warm caches)');
process.stdout.write(out.join('\n') + '\n');
