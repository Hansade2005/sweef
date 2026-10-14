// nds.js <locale> '<option-sets json array>' [ms]: Node's and Sweef's output for several option sets, side by side
'use strict';
const h = require('./harness');
const ours = h.make(['intlcore', 'intlnum', 'intldate']);
const loc = process.argv[2], sets = JSON.parse(process.argv[3]), v = Number(process.argv[4] || Date.UTC(2025, 9, 14, 15, 4, 5, 678));
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
for (const o of sets) {
  const a = new Intl.DateTimeFormat(loc, o).format(v);
  let b; try { b = new ours.DateTimeFormat(loc, o).format(v); } catch (e) { b = 'THROW ' + e.message; }
  console.log(a === b ? 'ok  ' : 'DIFF', JSON.stringify(o), '\n      node:', esc(a), a === b ? '' : '\n      ours:' + esc(b));
}
