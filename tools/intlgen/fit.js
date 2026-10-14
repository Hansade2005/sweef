// fit.js: finds the zone field types of ICU's DateTimePatternGenerator by matching Node's output (development aid)
// Usage: node fit.js   (searches over the types of z, v, O, V short/long rows)
'use strict';
const h = require('./harness');
const LOCS = ['ca', 'es', 'ro', 'de-AT', 'en', 'ko', 'zh', 'ja', 'fi', 'ru', 'pt', 'hu', 'cs', 'sv'];
const ZS = ['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric'];
const v = Date.UTC(2025, 9, 14, 15, 4, 5, 678);
const cases = [];
for (const loc of LOCS) for (const z of ZS) for (const hh of ['numeric', '2-digit']) for (const m of [undefined, 'numeric', '2-digit']) for (const s of [undefined, 'numeric', '2-digit']) {
  if (s && !m) continue;
  const o = { hour: hh, timeZoneName: z, timeZone: 'America/St_Johns', hourCycle: 'h23' };
  if (m) o.minute = m; if (s) o.second = s;
  cases.push([loc, o]);
}
for (const loc of LOCS) for (const z of ZS) for (const dd of [{ year: 'numeric', month: 'numeric', day: 'numeric' }, { month: 'long', day: 'numeric' }]) cases.push([loc, Object.assign({ timeZoneName: z, timeZone: 'America/St_Johns', hour: 'numeric', minute: '2-digit' }, dd)]);
const want = cases.map(([l, o]) => new Intl.DateTimeFormat(l, o).format(v).replace(/ /g, ' '));
const names = [['z', 1], ['z', 4], ['v', 1], ['v', 4], ['O', 1], ['O', 4]];
function score(vals, verbose) {
  const ours = h.make(['intlcore', 'intlnum', 'intldate']);
  names.forEach((n, i) => h.H.pat.setType(n[0], n[1], vals[i]));
  let ok = 0;
  const bad = [];
  cases.forEach(([l, o], i) => { let g; try { g = new ours.DateTimeFormat(l, o).format(v); } catch (e) { g = 'T'; } if (g === want[i]) ok++; else bad.push(i); });
  if (verbose) for (const i of bad.slice(0, 12)) console.log(cases[i][0], JSON.stringify(cases[i][1]), '\n  want', want[i]);
  return ok;
}
let vals = [-0x103, -0x104, -0x113, -0x114, -0x103, -0x104];
let best = score(vals);
console.log('start', vals.map((x) => x.toString(16)).join(' '), best, '/', cases.length);
for (let pass = 0; pass < 3; pass++) {
  let improved = false;
  for (let i = 0; i < vals.length; i++) {
    for (const cand of [-0x101, -0x102, -0x103, -0x104, -0x105, -0x10a, -0x10f, -0x111, -0x112, -0x113, -0x114, -0x115, -0x118, -0x11d, -0x121, -0x122, -0x123, -0x124, -0x125, -0x133, -0x134]) {
      if (cand === vals[i]) continue;
      const t = vals.slice(); t[i] = cand;
      const sc = score(t);
      if (sc > best) { best = sc; vals = t; improved = true; console.log('  better', vals.map((x) => x.toString(16)).join(' '), best); }
    }
  }
  if (!improved) break;
}
console.log('best', vals.map((x) => x.toString(16)).join(' '), best, '/', cases.length);
score(vals, true);
