// pat.js <locale> <calendar> <skeleton...>: the pattern Sweef's DateTimePatternGenerator picks for skeletons (development aid).
'use strict';
const h = require('./harness');
const ours = h.make(['intlcore', 'intlnum', 'intldate']);
const H = h.H;
const [loc, cal, ...skels] = process.argv.slice(2);
const I = H.intl, data = I.localeData(loc), cd = data.c[cal] || data.c.gregory;
const sym = data.n.sym, gen = H.pat.gen(cd, data.r.dn, (sym[data.n.nu] || sym.latn).d, 'h', data.c.gregory.dtfAt);
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
for (const sk of skels) console.log(sk, '=>', esc(gen.best(sk)));
if (process.env.RAW) for (const sk of skels) console.log('raw', sk, JSON.stringify(H.pat.raw(gen, sk)), 'time:', JSON.stringify(H.pat.raw(gen, sk, 0xfc00)));
