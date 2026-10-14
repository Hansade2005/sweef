// Compares Sweef's Intl.DateTimeFormat with Node's over locales x options x instants x time zones.
//   node cmp-date.js [--all] [--write <golden.json>] [--zones]
'use strict';
const { make, NativeIntl } = require('./harness');
const ours = make(['intlnum', 'intldate']);
const LOCALES = ['en-US', 'en-GB', 'en-IN', 'en-AU', 'en-CA', 'de', 'de-AT', 'fr', 'fr-CA', 'es', 'es-MX', 'pt-BR', 'pt-PT', 'it', 'nl', 'ru', 'pl', 'tr', 'ar', 'ar-AE', 'he', 'fa', 'hi', 'bn', 'id', 'th', 'ja', 'ko', 'zh-CN', 'zh-TW', 'sv', 'nb', 'fi', 'cs', 'uk', 'el', 'hu', 'ro', 'vi'];
const INSTANTS = [Date.UTC(2025, 9, 14, 15, 4, 5, 678), Date.UTC(2024, 1, 29, 0, 0, 0), Date.UTC(2000, 0, 1, 12, 0, 0), Date.UTC(1999, 11, 31, 23, 59, 59), Date.UTC(2025, 6, 4, 0, 30, 0), Date.UTC(2025, 2, 30, 1, 30, 0), Date.UTC(2025, 0, 5, 9, 7, 0)];
const ZONES = ['UTC', 'America/New_York', 'Europe/Paris', 'Asia/Kolkata', 'Asia/Tokyo', 'Australia/Sydney', 'America/Los_Angeles', 'Asia/Kathmandu'];
const OPTS = [
  {}, { dateStyle: 'full' }, { dateStyle: 'long' }, { dateStyle: 'medium' }, { dateStyle: 'short' }, { timeStyle: 'full' }, { timeStyle: 'long' }, { timeStyle: 'medium' }, { timeStyle: 'short' },
  { dateStyle: 'full', timeStyle: 'short' }, { dateStyle: 'medium', timeStyle: 'medium' }, { dateStyle: 'long', timeStyle: 'long' }, { dateStyle: 'short', timeStyle: 'short' },
  { year: 'numeric', month: 'long', day: 'numeric' }, { month: 'short', day: 'numeric' }, { weekday: 'long' }, { weekday: 'short', month: 'short', day: 'numeric' }, { weekday: 'long', year: 'numeric', month: 'long', day: 'numeric' },
  { hour: 'numeric', minute: '2-digit' }, { hour: '2-digit', minute: '2-digit', hour12: false }, { hour: 'numeric', hourCycle: 'h23' }, { hour: 'numeric', minute: 'numeric', second: 'numeric' },
  { year: '2-digit', month: '2-digit', day: '2-digit' }, { month: 'long', year: 'numeric' }, { era: 'short', year: 'numeric' }, { year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric' },
  { timeZoneName: 'short', hour: 'numeric' }, { timeZoneName: 'long', hour: 'numeric', minute: 'numeric' }, { timeZoneName: 'shortOffset', hour: 'numeric' }, { timeZoneName: 'longOffset', hour: 'numeric' }, { timeZoneName: 'shortGeneric', hour: 'numeric' }, { timeZoneName: 'longGeneric', hour: 'numeric' },
  { year: 'numeric', month: 'short', day: 'numeric', hour: 'numeric', minute: 'numeric', second: 'numeric', fractionalSecondDigits: 3 }, { dayPeriod: 'long', hour: 'numeric' }, { hour: 'numeric' }, { minute: 'numeric' }, { second: 'numeric' },
  { month: 'narrow' }, { weekday: 'narrow' }, { year: 'numeric' }, { day: 'numeric' }, { month: 'numeric', day: 'numeric' }, { month: 'long' }, { month: 'short', year: 'numeric' }, { hour: 'numeric', minute: '2-digit', hour12: true },
  { numberingSystem: 'arab', dateStyle: 'short' }, { calendar: 'buddhist', dateStyle: 'medium' }, { hourCycle: 'h11', timeStyle: 'short' }, { hour12: false, timeStyle: 'medium' },
];
const all = process.argv.includes('--all');
const onlyIdx = process.argv.indexOf('--only'); const only = onlyIdx > 0 ? process.argv[onlyIdx + 1] : null;
let bad = 0, total = 0, ctorBad = 0;
const golden = [], seen = {};
const nn = (s) => s.replace(/ /g, ' ');
let zi = 0;
for (const loc of LOCALES) for (const o0 of OPTS) {
  zi++;
  if (only && JSON.stringify(o0) !== only) continue;
  const o = Object.assign({ timeZone: ZONES[zi % ZONES.length] }, o0);
  let nf, of;
  try { nf = new NativeIntl.DateTimeFormat(loc, o); } catch (e) { continue; }
  try { of = new ours.DateTimeFormat(loc, o); } catch (e) { ctorBad++; if (ctorBad < 10) console.log('CTOR', loc, JSON.stringify(o), e.stack.split('\n').slice(0, 2).join(' | ')); continue; }
  for (const v of INSTANTS) {
    const exp = nn(nf.format(v)); let got;
    try { got = of.format(v); } catch (e) { got = 'THROW ' + e.message; }
    total++;
    golden.push({ k: 'dtf', l: loc, o, v, e: exp });
    if (got !== exp) {
      bad++;
      const key = JSON.stringify(o0) + '|' + (all || only ? loc + '|' + (only ? '' : v) : '');
      seen[key] = (seen[key] || 0) + 1;
      if (seen[key] <= (only ? 1 : all ? 1 : 2)) console.log(loc, JSON.stringify(o), String(v), '\n   got ', JSON.stringify(got), '\n   want', JSON.stringify(exp));
    }
  }
}
console.log(`${bad} of ${total} differ (${ctorBad} constructor failures)`);
if (process.argv.includes('--summary')) { const rows = Object.entries(seen).sort((a, b) => b[1] - a[1]); for (const [k, n] of rows) console.log(String(n).padStart(5), k); }
const wi = process.argv.indexOf('--write');
if (wi > 0) require('fs').writeFileSync(process.argv[wi + 1], JSON.stringify(golden));
