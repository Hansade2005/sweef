// matrix-oracle.js <cases.jsonl> <out.jsonl> <start>: Node's answers for the DateTimeFormat cases of matrix.js, one JSON line per case,
// written as it goes: V8 aborts the process on a few combinations (formatToParts / formatRangeToParts with some non-Gregorian
// calendars), and matrix.js restarts the worker after the case that killed it.
'use strict';
const fs = require('fs');
const [casesFile, outFile, start] = process.argv.slice(2);
const cases = fs.readFileSync(casesFile, 'utf8').split('\n').filter(Boolean).map((l) => JSON.parse(l));
for (let i = +start; i < cases.length; i++) {
  const c = cases[i], r = {};
  try {
    const f = new Intl.DateTimeFormat(c.loc, c.o);
    r.f = f.format(c.v);
    if (c.parts) r.p = JSON.stringify(f.formatToParts(c.v));
    if (c.resolved) r.r = JSON.stringify(f.resolvedOptions());
    if (c.w !== undefined) { r.rg = f.formatRange(c.v, c.w); r.rp = JSON.stringify(f.formatRangeToParts(c.v, c.w)); }
  } catch (e) { r.err = 1; }
  fs.appendFileSync(outFile, JSON.stringify(r) + '\n');
}
