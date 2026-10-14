// Runs Sweef's Intl modules (internal/engine/platformjs/intl*.js) inside Node, next to Node's own Intl, so that
// output can be compared case by case during development. The modules are the same files the engine loads; the
// host functions (CLDR data, time zones) are reimplemented here with Node's help.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib');
const root = path.join(__dirname, '..', '..');
const dataDir = path.join(root, 'internal', 'intl', 'data');
const pjs = path.join(root, 'internal', 'engine', 'platformjs');

function load(name) { const f = path.join(dataDir, name + '.json.gz'); return fs.existsSync(f) ? zlib.gunzipSync(fs.readFileSync(f)).toString() : null; }
const NativeIntl = Intl;

function zoneOffset(name, ms) { // seconds east of UTC and daylight saving, derived from Node's Intl
  try {
    const f = new NativeIntl.DateTimeFormat('en-US', { timeZone: name, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric', timeZoneName: 'short' });
    const p = Object.fromEntries(f.formatToParts(ms).map((x) => [x.type, x.value]));
    const asUTC = Date.UTC(+p.year, +p.month - 1, +p.day, +p.hour, +p.minute, +p.second);
    const off = Math.round((asUTC - Math.floor(ms / 1000) * 1000) / 1000);
    // daylight saving: the offset differs from the January/July minimum
    const y = new Date(ms).getUTCFullYear();
    const o = (m) => { const q = Object.fromEntries(f.formatToParts(m).map((x) => [x.type, x.value])); return Math.round((Date.UTC(+q.year, +q.month - 1, +q.day, +q.hour, +q.minute, +q.second) - Math.floor(m / 1000) * 1000) / 1000); };
    const jan = o(Date.UTC(y, 0, 1)), jul = o(Date.UTC(y, 6, 1));
    return [off, jan !== jul && off === Math.max(jan, jul)];
  } catch (e) { return null; }
}

function make(modules) {
  const g = { Intl: {}, Symbol, Object, Array, JSON, Math, Date, RegExp, Error, TypeError, RangeError, Number, String, BigInt, Map, Set, Promise, parseInt, NaN, Infinity, isNaN };
  g.globalThis = g;
  g.DOMException = class DOMException extends Error { constructor(m, n) { super(m); this.name = n; } };
  g.eval = (s) => eval(s);
  const sw = {
    intlLocale: (t) => load(t), intlGlobal: () => load('global'), intlPlurals: () => load('plurals'),
    intlZoneOffset: zoneOffset, intlZoneValid: (n) => { try { new NativeIntl.DateTimeFormat('en', { timeZone: n }); return true; } catch (e) { return false; } },
    intlLocalZone: () => NativeIntl.DateTimeFormat().resolvedOptions().timeZone,
    intlSegment: (kind, text) => { const out = []; for (const x of new NativeIntl.Segmenter('en', { granularity: kind }).segment(text)) out.push(x.index); if (text.length) out.push(text.length); return out; },
  };
  g.__sw = sw;
  const loaded = {};
  let H;
  const loader = (m) => {
    if (loaded[m]) return;
    loaded[m] = 1;
    const src = fs.readFileSync(path.join(pjs, m + '.js'), 'utf8');
    new Function('g', 'H', '"use strict";' + src)(g, H);
  };
  H = (new Function('g', 'load', 'lazyGets', '"use strict";' + fs.readFileSync(path.join(pjs, 'h.js'), 'utf8')))(g, loader, new Set());
  g.Event = class {}; g.EventTarget = class {};
  for (const m of modules) loader(m);
  return g.Intl;
}
module.exports = { make, NativeIntl };
if (require.main === module) {
  const ours = make(['intlnum']);
  const f = new ours.NumberFormat('en-US', { style: 'currency', currency: 'USD' });
  console.log(f.format(1234.5), new NativeIntl.NumberFormat('en-US', { style: 'currency', currency: 'USD' }).format(1234.5));
}
