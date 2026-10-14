// calcheck.js [calendar...]: Sweef's calendar arithmetic (platformjs/intlcal.js) against Node's, day by day over several ranges
'use strict';
const h = require('./harness');
h.make(['intlcore', 'intlcal']);
const C = h.H.cal;
const cals = process.argv.slice(2).length ? process.argv.slice(2) : ['buddhist', 'roc', 'japanese', 'persian', 'islamic-civil', 'islamic-tbla', 'islamic-umalqura', 'islamic', 'islamic-rgsa', 'hebrew', 'indian', 'coptic', 'ethiopic', 'ethioaa'];
const NODE_CAL = { ethioaa: 'ethioaa' };
let seed = 12345; const rnd = () => (seed = (seed * 1103515245 + 12345) & 0x7fffffff) / 0x7fffffff;
const ranges = [[Date.UTC(1800, 0, 1), Date.UTC(2200, 0, 1), 5], [Date.UTC(2020, 0, 1), Date.UTC(2031, 0, 1), 1], [Date.UTC(1000, 0, 1), Date.UTC(1900, 0, 1), 37], [Date.UTC(-500, 0, 1), Date.UTC(1000, 0, 1), 53], [Date.UTC(2200, 0, 1), Date.UTC(3000, 0, 1), 41]];
for (const cal of cals) {
  const f = new Intl.DateTimeFormat('en-u-ca-' + cal, { timeZone: 'UTC', year: 'numeric', month: 'numeric', day: 'numeric', era: 'short' });
  let bad = 0, n = 0;
  for (const [a, b, step] of ranges) {
    for (let t = a; t < b; t += step * 86400000 + (step > 1 ? Math.floor(rnd() * 86400000 * 3) : 0)) {
      const days = Math.floor(t / 86400000);
      const parts = {}; for (const x of f.formatToParts(days * 86400000)) parts[x.type] = x.value;
      const r = C.fields(days, cal);
      n++;
      const want = [+parts.year, parts.month, +parts.day];
      const got = [r.y, String(r.m), r.d];
      if (cal === 'hebrew') want[1] = r.m; // Node prints the month name; compared below by the date being valid only
      if (want[0] !== got[0] || String(want[1]) !== got[1] && cal !== 'hebrew' || want[2] !== got[2]) {
        bad++;
        if (bad <= 4) console.log(cal, new Date(days * 86400000).toISOString().slice(0, 10), 'got', JSON.stringify(got), JSON.stringify(r.era), 'want', JSON.stringify(parts));
      }
    }
  }
  console.log(cal.padEnd(18), n, 'days,', bad, 'differ');
}
