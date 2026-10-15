#!/usr/bin/env node
// Mode B: real LLM agent runs. The same task, model, turn limit, cost cap, max_tokens,
// thinking/effort settings and snapshot size cap are given to
//   (1) `sweef agent`           (Sweef's engine + Sweef's own agent loop), and
//   (2) pw_agent.mjs            (Chromium via Playwright + a generic Messages-API tool loop).
// This compares complete stacks (browser + agent design). See README for what that does
// and does not prove.
//
//   export ANTHROPIC_API_KEY=...   # required; never passed as a flag, never logged
//   node modeb.mjs --sweef /path/to/sweef [--model claude-haiku-5-5] [--runs 1] [--only ids]
//
// TEST-ONLY plumbing mode (never produces results to report):
//   node modeb.mjs --plumbing-test --base-url http://127.0.0.1:PORT ...   (see test_plumbing.sh)
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawn, spawnSync } from 'node:child_process';
import { performance } from 'node:perf_hooks';
import { HERE, loadTasks, re, sleep } from './lib.mjs';
import { loadPrices, costUSD } from './cost.mjs';
import { runPwAgent } from './pw_agent.mjs';

const KEY_VAR = 'ANTHROPIC_API_KEY';
const args = process.argv.slice(2);
const opt = {
  model: 'claude-haiku-5-5', runs: 1, only: '', sweef: process.env.SWEEF || 'sweef', tools: 'sweef-agent,playwright-agent',
  maxTurns: 40, maxTokens: 8192, effort: 'low', thinking: 'adaptive', maxCost: 0.5, snapshotMaxTokens: 6000, taskTimeout: 300, paceMs: 1000,
  baseUrl: process.env.ANTHROPIC_BASE_URL || '', out: '', plumbing: false, observe: 'snapshot', navTimeout: 30,
};
for (let i = 0; i < args.length; i++) {
  const a = args[i], nx = () => args[++i];
  if (a === '--model') opt.model = nx();
  else if (a === '--runs') opt.runs = +nx();
  else if (a === '--only') opt.only = nx();
  else if (a === '--sweef') opt.sweef = nx();
  else if (a === '--tools') opt.tools = nx();
  else if (a === '--max-turns') opt.maxTurns = +nx();
  else if (a === '--max-tokens') opt.maxTokens = +nx();
  else if (a === '--effort') opt.effort = nx();
  else if (a === '--thinking') opt.thinking = nx();
  else if (a === '--max-cost') opt.maxCost = +nx();
  else if (a === '--task-timeout') opt.taskTimeout = +nx();
  else if (a === '--pace-ms') opt.paceMs = +nx();
  else if (a === '--base-url') opt.baseUrl = nx();
  else if (a === '--pw-observe') opt.observe = nx();
  else if (a === '--out') opt.out = nx();
  else if (a === '--plumbing-test') opt.plumbing = true;
  else { console.error('unknown arg ' + a); process.exit(2); }
}

// ---- refuse without the key ------------------------------------------------------
if (!process.env[KEY_VAR]) {
  if (opt.plumbing) process.env[KEY_VAR] = 'plumbing-test-dummy-key'; // only allowed with the mock
  else {
    console.error(`Mode B calls the real Anthropic API and needs a key in the environment variable ${KEY_VAR}.`);
    console.error(`It is not set. Export it in the shell that runs this script, for example:`);
    console.error(`  export ${KEY_VAR}=<your key>`);
    console.error(`The key is read from the environment only (never a flag), is never printed, and is not written to results.`);
    console.error(`No requests were made. Mode A (no LLM) does not need a key: node modea.mjs`);
    process.exit(2);
  }
}
if (opt.plumbing && !/^https?:\/\/(127\.0\.0\.1|localhost)[:/]/.test(opt.baseUrl)) {
  console.error('--plumbing-test only runs against a local mock (--base-url http://127.0.0.1:PORT).'); process.exit(2);
}
const apiKey = process.env[KEY_VAR];
const prices = loadPrices();
const tasks = loadTasks(undefined, opt.only ? opt.only.split(',') : null).filter((t) => t.mode_b);
const tools = opt.tools.split(',').filter(Boolean);
const TMS = opt.taskTimeout * 1000;

const promptFor = (t) => `${t.goal}\n\n(The browser is already open on ${t.url}.)`;

function execp(cmd, argv, env, timeoutMs) {
  return new Promise((resolve) => {
    const t = performance.now();
    const p = spawn(cmd, argv, { env, stdio: ['ignore', 'pipe', 'pipe'] });
    let out = '', err = '';
    p.stdout.on('data', (d) => (out += d)); p.stderr.on('data', (d) => (err += d));
    const to = setTimeout(() => p.kill('SIGKILL'), timeoutMs);
    p.on('close', (code) => { clearTimeout(to); resolve({ code, out, err, ms: performance.now() - t, timedOut: false }); });
  });
}

async function runSweefAgent(task) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-b-'));
  const a = ['agent', '-json', '-yes', '-model', opt.model, '-url', task.url, '-max-turns', String(opt.maxTurns), '-max-cost', String(opt.maxCost), '-effort', opt.effort];
  if (opt.thinking === 'disabled') a.push('-no-thinking');
  if (opt.baseUrl) a.push('-base-url', opt.baseUrl);
  a.push(promptFor(task));
  const env = { ...process.env, SWEEF_HOME: home, SWEEF_CACHE_DIR: path.join(home, 'cache') }; // empty caches, like the fresh Chromium profile
  const r = await execp(opt.sweef, a, env, TMS);
  fs.rmSync(home, { recursive: true, force: true });
  const out = { tool: 'sweef-agent', wall_ms: r.ms, exit: r.code };
  try {
    const j = JSON.parse(r.out.trim().split('\n').pop());
    const s = j.stats;
    out.stop = j.stop; out.answer = j.answer || j.text || ''; out.text = j.text || '';
    if (j.error) out.error = String(j.error).slice(0, 300).split(apiKey).join('[redacted]'); // why stop=error
    out.turns = s.turns; out.requests = s.requests; out.retries = s.retries; out.tool_calls = s.toolCalls;
    out.tool_ms = s.toolTotalMs; out.model_ms = null; out.snapshots = s.fullSnapshots + s.deltaSnapshots; out.delta_snapshots = s.deltaSnapshots;
    out.usage = { input_tokens: s.usage.input_tokens, output_tokens: s.usage.output_tokens, cache_read_input_tokens: s.usage.cache_read_input_tokens, cache_creation_input_tokens: s.usage.cache_creation_input_tokens };
    out.cost_usd = costUSD(prices, opt.model, out.usage); // same price table as the baseline
    out.cost_usd_sweef_estimate = s.costUsd;
    out.agent_elapsed_ms = s.elapsedMs;
  } catch {
    out.stop = 'error'; out.error = (r.out + r.err).trim().split('\n').pop().slice(0, 300).split(apiKey).join('[redacted]'); out.answer = '';
  }
  return out;
}

async function runPwAgentTask(task) {
  const r = await runPwAgent({
    task, prompt: promptFor(task), model: opt.model, apiKey, baseUrl: opt.baseUrl, maxTurns: opt.maxTurns, maxTokens: opt.maxTokens, effort: opt.effort,
    thinking: opt.thinking, maxCostUSD: opt.maxCost, snapshotMaxTokens: opt.snapshotMaxTokens, navTimeoutMs: opt.navTimeout * 1000, prices, observe: opt.observe,
  });
  if (r.error) r.error = String(r.error).split(apiKey).join('[redacted]');
  return r;
}

const runners = { 'sweef-agent': runSweefAgent, 'playwright-agent': runPwAgentTask };
const ver = (cmd, a) => { const r = spawnSync(cmd, a, { encoding: 'utf8' }); return (r.stdout || r.stderr || '').trim().split('\n')[0]; };
const meta = {
  started: new Date().toISOString(),
  mock: opt.plumbing,
  WARNING: opt.plumbing ? 'TEST-ONLY PLUMBING RUN AGAINST A MOCK API. Nothing in this file is a benchmark result.' : undefined,
  config: { model: opt.model, runs: opt.runs, max_turns: opt.maxTurns, max_tokens: opt.maxTokens, effort: opt.effort, thinking: opt.thinking, max_cost_usd: opt.maxCost, snapshot_max_tokens: opt.snapshotMaxTokens, task_timeout_s: opt.taskTimeout, pw_observe: opt.observe, tools, tasks: tasks.length, base_url: opt.baseUrl || 'https://api.anthropic.com' },
  versions: { sweef: ver(opt.sweef, ['version']), node: process.version },
  prices,
};
const runs = [];
const stamp = meta.started.replace(/[:.]/g, '-');
const out = opt.out || path.join(HERE, '..', 'results', `${opt.plumbing ? 'PLUMBING-TEST-NOT-RESULTS-' : 'modeb-'}${stamp}.json`);
fs.mkdirSync(path.dirname(out), { recursive: true });
const flush = () => fs.writeFileSync(out, JSON.stringify({ meta, runs }, null, 1));
console.error(`Mode B${opt.plumbing ? ' (PLUMBING TEST, mock API, not results)' : ''}: ${tasks.length} tasks x ${tools.join('+')} x ${opt.runs} runs, model ${opt.model} -> ${out}`);
for (const task of tasks) for (let r = 0; r < opt.runs; r++) {
  const order = tools.map((_, i) => tools[(i + r + tasks.indexOf(task)) % tools.length]);
  for (const tool of order) {
    const res = await runners[tool](task);
    res.task = task.id; res.run = r + 1; res.tool = tool;
    res.answer_ok = (res.stop === 'done' || res.stop === 'end_turn') && !!res.answer && re(task.answer_check).test(res.answer);
    res.answer = String(res.answer || '').slice(0, 600); res.text = undefined;
    runs.push(res);
    console.error(`${task.id.padEnd(24)} ${tool.padEnd(17)} ${res.answer_ok ? 'PASS' : 'FAIL'} stop=${res.stop} turns=${res.turns ?? '-'} wall=${Math.round(res.wall_ms)}ms cost=${res.cost_usd != null ? '$' + res.cost_usd.toFixed(4) : '-'}`);
    await sleep(opt.paceMs);
  }
  flush();
}
flush();
console.error('done: ' + out);
