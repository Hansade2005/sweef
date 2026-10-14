// Compares Sweef's Intl.NumberFormat with Node's over a matrix of locales, options and values.
//   node cmp-num.js [--all] [--write <golden.json>]
'use strict';
const { make, NativeIntl } = require('./harness');
const ours = make(['intlnum']);
const LOCALES = ['en-US', 'en-GB', 'en-IN', 'en-AU', 'en-CA', 'de', 'de-CH', 'de-AT', 'fr', 'fr-CA', 'es', 'es-MX', 'pt-BR', 'pt-PT', 'it', 'nl', 'ru', 'pl', 'tr', 'ar', 'ar-AE', 'he', 'fa', 'hi', 'bn', 'id', 'th', 'ja', 'ko', 'zh-CN', 'zh-TW', 'sv', 'nb', 'fi', 'cs', 'uk', 'el', 'hu', 'ro', 'vi'];
const VALUES = [0, 1, -1, 0.5, 1.5, 12, 123, 1234, 12345.678, 123456, 1234567.891, 0.000123, 1e15, -0, NaN, Infinity, -Infinity, 999999, 1e-7, 123456789012, 0.995, 2.5, 1.005, -1234.5, 99.99];
const OPTS = [
  {}, { style: 'percent' }, { style: 'percent', maximumFractionDigits: 1 }, { style: 'currency', currency: 'USD' }, { style: 'currency', currency: 'EUR' }, { style: 'currency', currency: 'JPY' },
  { style: 'currency', currency: 'CHF' }, { style: 'currency', currency: 'INR' }, { style: 'currency', currency: 'GBP' }, { style: 'currency', currency: 'USD', currencyDisplay: 'code' },
  { style: 'currency', currency: 'USD', currencyDisplay: 'name' }, { style: 'currency', currency: 'EUR', currencyDisplay: 'narrowSymbol' }, { style: 'currency', currency: 'USD', currencySign: 'accounting' },
  { notation: 'compact' }, { notation: 'compact', compactDisplay: 'long' }, { notation: 'compact', style: 'currency', currency: 'USD' }, { notation: 'scientific' }, { notation: 'engineering' },
  { minimumFractionDigits: 2 }, { maximumFractionDigits: 0 }, { maximumSignificantDigits: 3 }, { minimumSignificantDigits: 4 }, { minimumIntegerDigits: 3 },
  { signDisplay: 'always' }, { signDisplay: 'exceptZero' }, { signDisplay: 'never' }, { signDisplay: 'negative' }, { useGrouping: false }, { useGrouping: 'min2' }, { useGrouping: 'always' },
  { style: 'unit', unit: 'kilometer-per-hour' }, { style: 'unit', unit: 'megabyte' }, { style: 'unit', unit: 'megabyte', unitDisplay: 'long' }, { style: 'unit', unit: 'celsius', unitDisplay: 'narrow' },
  { style: 'unit', unit: 'percent' }, { style: 'unit', unit: 'liter', unitDisplay: 'long' }, { style: 'unit', unit: 'second', unitDisplay: 'long' }, { style: 'unit', unit: 'kilobyte-per-second' },
  { roundingMode: 'floor', maximumFractionDigits: 0 }, { roundingMode: 'halfEven', maximumFractionDigits: 0 }, { roundingMode: 'ceil', maximumFractionDigits: 1 }, { roundingIncrement: 5, minimumFractionDigits: 2, maximumFractionDigits: 2 },
  { trailingZeroDisplay: 'stripIfInteger', minimumFractionDigits: 2 }, { maximumFractionDigits: 2, minimumFractionDigits: 0, roundingPriority: 'morePrecision', maximumSignificantDigits: 2 },
  { numberingSystem: 'arab' }, { numberingSystem: 'deva' }, { numberingSystem: 'thai' },
];
const all = process.argv.includes('--all');
let bad = 0, total = 0;
const golden = [];
const seen = {};
const nn = (s) => s; // number output keeps U+202F (French grouping); only date output has it replaced
for (const loc of LOCALES) for (const o of OPTS) {
  let nf, of;
  try { nf = new NativeIntl.NumberFormat(loc, o); } catch (e) { continue; }
  try { of = new ours.NumberFormat(loc, o); } catch (e) { bad++; console.log('CTOR', loc, JSON.stringify(o), e.message); continue; }
  for (const v of VALUES) {
    const exp = nn(nf.format(v)); let got;
    try { got = of.format(v); } catch (e) { got = 'THROW ' + e.message; }
    total++;
    golden.push({ k: 'nf', l: loc, o, v: Object.is(v, -0) ? '-0' : Number.isNaN(v) ? 'NaN' : v === Infinity ? 'Infinity' : v === -Infinity ? '-Infinity' : v, e: exp });
    if (got !== exp) {
      bad++;
      const key = JSON.stringify(o) + '|' + (all ? loc + '|' + v : '');
      seen[key] = (seen[key] || 0) + 1;
      if (seen[key] <= (all ? 1 : 2)) console.log(loc, JSON.stringify(o), String(v), 'got', JSON.stringify(got), 'want', JSON.stringify(exp));
    }
  }
}
console.log(`${bad} of ${total} differ`);
const wi = process.argv.indexOf('--write');
if (wi > 0) require('fs').writeFileSync(process.argv[wi + 1], JSON.stringify(golden));
