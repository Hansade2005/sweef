#!/usr/bin/env node
// Markdown report for a Mode B results file. All tasks, failures included.
import fs from 'node:fs';
import { median } from './lib.mjs';
const file = process.argv[2];
if (!file) { console.error('usage: report_b.mjs results.json'); process.exit(2); }
const { meta, runs } = JSON.parse(fs.readFileSync(file, 'utf8'));
const tools = meta.config.tools, ids = [...new Set(runs.map((r) => r.task))];
const f0 = (x) => (x == null ? '-' : String(Math.round(x)));
const L = [];
if (meta.mock) L.push(`> **TEST-ONLY PLUMBING RUN AGAINST A MOCK API. These numbers are meaningless and must not be reported as results.**\n`);
L.push(`# Real-site benchmark, Mode B (real LLM agents)\n`);
L.push(`Generated from \`${file.split('/').pop()}\` (${meta.started}). Model \`${meta.config.model}\`, effort ${meta.config.effort}, thinking ${meta.config.thinking}, max ${meta.config.max_turns} turns, max_tokens ${meta.config.max_tokens}, cost cap $${meta.config.max_cost_usd}, ${meta.config.runs} run(s) per task.`);
L.push(`Cost is computed from reported token usage with \`prices.json\` for both agents.\n`);
L.push(`## Totals (all tasks, failures included)\n`);
L.push(`| agent | success | median wall s | median turns | median input tok | median output tok | total cost $ | cost per success $ |`);
L.push(`|---|---|---|---|---|---|---|---|`);
for (const t of tools) {
  const rs = runs.filter((r) => r.tool === t), ok = rs.filter((r) => r.answer_ok);
  const tok = (r) => r.usage ? r.usage.input_tokens + r.usage.cache_read_input_tokens + r.usage.cache_creation_input_tokens : null;
  const tot = rs.reduce((a, r) => a + (r.cost_usd || 0), 0);
  L.push(`| ${t} | ${ok.length}/${rs.length} | ${(median(rs.map((r) => r.wall_ms)) / 1000).toFixed(1)} | ${median(rs.map((r) => r.turns))} | ${f0(median(rs.map(tok)))} | ${f0(median(rs.map((r) => r.usage?.output_tokens)))} | ${tot.toFixed(4)} | ${ok.length ? (tot / ok.length).toFixed(4) : '-'} |`);
}
L.push(`\nMedians are over all runs of the agent (successful or not) so that failures cannot flatter the numbers; see the per-task table for outcomes.\n`);
L.push(`## Per task\n`);
L.push(`| task | ${tools.map((t) => `${t}: result, wall s, turns, cost $`).join(' | ')} |`);
L.push(`|---|${tools.map(() => '---').join('|')}|`);
for (const id of ids) {
  const cells = tools.map((t) => {
    const rs = runs.filter((r) => r.task === id && r.tool === t);
    if (!rs.length) return 'n/a';
    const ok = rs.filter((r) => r.answer_ok).length;
    return `${ok}/${rs.length} ok, ${(median(rs.map((r) => r.wall_ms)) / 1000).toFixed(1)}, ${median(rs.map((r) => r.turns))}, ${(median(rs.map((r) => r.cost_usd || 0))).toFixed(4)}`;
  });
  L.push(`| ${id} | ${cells.join(' | ')} |`);
}
L.push(`\n## Failures\n`);
L.push(`| task | agent | stop | error / answer excerpt |`);
L.push(`|---|---|---|---|`);
let n = 0;
for (const r of runs) if (!r.answer_ok) { n++; L.push(`| ${r.task} | ${r.tool} | ${r.stop} | ${String(r.error || r.answer || '').replace(/\|/g, '/').replace(/\n/g, ' ').slice(0, 140)} |`); }
if (!n) L.push(`| - | - | - | none |`);
console.log(L.join('\n'));
