// A larger, seeded, randomized and adversarial comparison of Sweef's Intl with Node's (ICU 77 / CLDR 47).
//   node matrix.js [dtf|nf|coll|seg|range] [--n 20000] [--seed 1] [--show 3] [--tags] [--only <tag>] [--locale <l>]
// Every case is generated from the seed, so the same command before and after a change measures the same cases
// (INTL_ROOT=<other checkout> measures a baseline). Output: match rate per service, failures bucketed by feature tag.
'use strict';
const { make, NativeIntl } = require('./harness');
const ours = make(['intlnum', 'intldate', 'intlmisc']);
const arg = (n, d) => { const i = process.argv.indexOf('--' + n); return i > 0 ? process.argv[i + 1] : d; };
const which = process.argv[2] && !process.argv[2].startsWith('--') ? process.argv[2] : 'all';
const WITHOUT = (arg('without', '') || '').split(',').filter(Boolean);
const N = +arg('n', 20000), SEED = +arg('seed', 1), SHOW = +arg('show', 2), ONLY = arg('only', null), LOC = arg('locale', null);
let st = SEED >>> 0;
const rnd = () => { st = (st + 0x6D2B79F5) >>> 0; let t = st; t = Math.imul(t ^ (t >>> 15), t | 1); t ^= t + Math.imul(t ^ (t >>> 7), t | 61); return ((t ^ (t >>> 14)) >>> 0) / 4294967296; };
const pick = (a) => a[Math.floor(rnd() * a.length)];
const chance = (p) => rnd() < p;

// the 73 shipped locales (tools/intlgen/gen.js), plus a few region variants that resolve to them
const LOCALES = ['en', 'en-US', 'en-001', 'en-GB', 'en-AU', 'en-IN', 'en-CA', 'es', 'es-ES', 'es-419', 'es-MX', 'es-US', 'pt', 'pt-BR', 'pt-PT', 'fr', 'fr-FR', 'fr-CA', 'de', 'de-DE', 'de-AT', 'de-CH', 'it', 'nl', 'ru', 'uk', 'pl', 'tr',
  'ar', 'ar-SA', 'ar-EG', 'ar-AE', 'he', 'fa', 'hi', 'bn', 'ur', 'id', 'ms', 'vi', 'th', 'ja', 'ko', 'zh', 'zh-CN', 'zh-Hant', 'zh-TW', 'zh-Hant-HK', 'zh-HK', 'sv', 'da', 'nb', 'fi', 'cs', 'sk', 'hu', 'ro', 'el', 'bg', 'hr', 'sr',
  'ca', 'ta', 'te', 'mr', 'fil', 'sw', 'lt', 'lv', 'et', 'sl', 'af', 'gu', 'kn', 'ml', 'pa', 'ne', 'si', 'km', 'my', 'az', 'kk', 'uz'];
const ZONES = ['UTC', 'America/New_York', 'America/Los_Angeles', 'America/Chicago', 'America/Denver', 'America/Sao_Paulo', 'America/Mexico_City', 'America/Argentina/Buenos_Aires', 'America/St_Johns', 'America/Caracas', 'Europe/London', 'Europe/Paris', 'Europe/Berlin', 'Europe/Moscow',
  'Europe/Istanbul', 'Europe/Dublin', 'Europe/Lisbon', 'Africa/Cairo', 'Africa/Lagos', 'Africa/Johannesburg', 'Africa/Nairobi', 'Asia/Dubai', 'Asia/Tehran', 'Asia/Kolkata', 'Asia/Kathmandu', 'Asia/Dhaka', 'Asia/Bangkok', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Tokyo', 'Asia/Seoul',
  'Asia/Jerusalem', 'Asia/Riyadh', 'Asia/Karachi', 'Asia/Singapore', 'Asia/Jakarta', 'Asia/Manila', 'Australia/Sydney', 'Australia/Adelaide', 'Australia/Perth', 'Pacific/Auckland', 'Pacific/Honolulu', 'Pacific/Chatham', 'Atlantic/Reykjavik', '+05:30', '-03:00'];
const CALS = ['gregory', 'buddhist', 'japanese', 'roc', 'islamic', 'islamic-umalqura', 'islamic-civil', 'islamic-tbla', 'islamic-rgsa', 'hebrew', 'indian', 'ethiopic', 'coptic', 'persian', 'chinese', 'dangi', 'iso8601'];
const COMMON_CALS = ['gregory', 'buddhist', 'japanese', 'roc', 'islamic', 'islamic-umalqura', 'islamic-civil', 'hebrew', 'persian'];
// the calendars Sweef ships names for, with the locales they are shipped for (tools/intlgen/gen.js CAL_LOCALES)
const REAL_CALS = { buddhist: ['en', 'th'], japanese: ['en', 'ja'], roc: ['en', 'zh-Hant', 'zh-TW', 'zh'], persian: ['en', 'fa'], 'islamic-umalqura': ['en', 'ar', 'ar-SA', 'fa', 'tr', 'id', 'ms', 'ur'], 'islamic-civil': ['en', 'ar', 'fa', 'tr', 'id', 'ms', 'ur'], islamic: ['en', 'ar', 'tr', 'id'], 'islamic-tbla': ['en', 'ar'], hebrew: ['en', 'he'], indian: ['en', 'hi'], coptic: ['en', 'ar'], ethiopic: ['en'] };
const NUS = ['latn', 'arab', 'arabext', 'beng', 'deva', 'thai', 'gujr', 'guru', 'knda', 'khmr', 'laoo', 'mlym', 'mymr', 'orya', 'tamldec', 'telu', 'tibt', 'hanidec', 'fullwide'];
const day = 86400000;
const INSTANT_POOL = [() => Date.UTC(2025, 9, 14, 15, 4, 5, 678), () => Date.UTC(2020, 0, 1) + Math.floor(rnd() * 10 * 365 * day), () => Date.UTC(1950, 0, 1) + Math.floor(rnd() * 100 * 365 * day), () => Date.UTC(1850, 0, 1) + Math.floor(rnd() * 400 * 365 * day),
  () => Date.UTC(2025, 2, 9, 7, 0, 0) + Math.floor(rnd() * 6) * 3600000, () => Date.UTC(2025, 9, 26, 0, 0, 0) + Math.floor(rnd() * 8) * 1800000, () => Date.UTC(2024, 1, 29) + Math.floor(rnd() * 3) * day, () => Date.UTC(1999, 11, 31, 23, 59, 59) + Math.floor(rnd() * 4000),
  () => Date.UTC(2025, 0, 1) + Math.floor(rnd() * 365 * day), () => Date.UTC(2025, 0, 1) + Math.floor(rnd() * 365 * day), () => Date.UTC(2025, 0, 1) + Math.floor(rnd() * 365 * day)];
const instant = () => Math.floor(pick(INSTANT_POOL)());

const results = {};
function rec(svc, tags, ok, info) {
  if (WITHOUT.some((w) => tags.some((t) => t === w || t.startsWith(w)))) return;
  const r = results[svc] || (results[svc] = { n: 0, bad: 0, tags: {}, shown: {} });
  r.n++;
  for (const t of tags) { const x = r.tags[t] || (r.tags[t] = [0, 0]); x[0]++; if (!ok) x[1]++; }
  if (!ok) {
    r.bad++;
    const k = tags.join(' ');
    r.shown[k] = (r.shown[k] || 0) + 1;
    if (ONLY ? tags.includes(ONLY) && r.shown[k] <= SHOW : r.shown[k] <= 1 && r.bad < 4 + SHOW) console.log(svc, info);
  }
}
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
const nn = (s) => s.replace(/\u202f/g, ' '); // Chromium shows a plain space where ICU 77 has U+202F in date patterns
const tryit = (f) => { try { return f(); } catch (e) { return 'THROW ' + e.constructor.name; } };

// ---- DateTimeFormat -------------------------------------------------------------------------------------------------------------
function dtfOptions() {
  const o = {};
  const m = rnd();
  if (m < 0.3) {
    if (chance(0.7)) o.dateStyle = pick(['full', 'long', 'medium', 'short']);
    if (!o.dateStyle || chance(0.6)) o.timeStyle = pick(['full', 'long', 'medium', 'short']);
  } else if (m < 0.34) {
    // defaults
  } else {
    if (chance(0.5)) o.weekday = pick(['long', 'short', 'narrow']);
    if (chance(0.12)) o.era = pick(['long', 'short', 'narrow']);
    if (chance(0.6)) o.year = pick(['numeric', 'numeric', '2-digit']);
    if (chance(0.65)) o.month = pick(['numeric', '2-digit', 'long', 'short', 'narrow', 'long', 'short']);
    if (chance(0.6)) o.day = pick(['numeric', '2-digit']);
    if (chance(0.5)) {
      o.hour = pick(['numeric', '2-digit']);
      if (chance(0.8)) o.minute = pick(['numeric', '2-digit', '2-digit']);
      if (o.minute && chance(0.5)) o.second = pick(['numeric', '2-digit']);
      if (o.second && chance(0.15)) o.fractionalSecondDigits = pick([1, 2, 3]);
    } else if (chance(0.06)) { o.minute = '2-digit'; if (chance(0.5)) o.second = '2-digit'; }
    if (chance(0.06)) o.dayPeriod = pick(['long', 'short', 'narrow']);
  }
  if (o.timeStyle || o.hour !== undefined || o.dayPeriod) {
    const h = rnd();
    if (h < 0.2) o.hour12 = chance(0.5);
    else if (h < 0.35) o.hourCycle = pick(['h11', 'h12', 'h23', 'h24']);
  }
  if (!o.dateStyle && !o.timeStyle && chance(0.3) && (o.hour !== undefined || o.year || o.day || !Object.keys(o).length)) o.timeZoneName = pick(['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric']);
  if (chance(0.5)) o.timeZone = pick(ZONES);
  else o.timeZone = pick(ZONES.slice(0, 12));
  return o;
}
function dtfTags(loc, o, extra) {
  const t = [];
  if (o.dateStyle && o.timeStyle) t.push('dateStyle+timeStyle');
  else if (o.dateStyle) t.push('dateStyle'); else if (o.timeStyle) t.push('timeStyle:' + o.timeStyle);
  if (!o.dateStyle && !o.timeStyle) {
    const hasD = o.weekday || o.year || o.month || o.day, hasT = o.hour !== undefined || o.minute || o.second;
    t.push(hasD && hasT ? 'components:date+time' : hasD ? 'components:date' : hasT ? 'components:time' : 'components:other');
  }
  if (o.timeZoneName) t.push('tzname:' + o.timeZoneName);
  if (o.timeStyle === 'full' || o.timeStyle === 'long') t.push('zone-in-style');
  if (o.dayPeriod) t.push('dayPeriod');
  if (o.era) t.push('era');
  if (o.fractionalSecondDigits) t.push('fsd');
  if (o.hour12 !== undefined || o.hourCycle) t.push('hourcycle-opt');
  const cal = o.calendar || (/-u-.*ca-([a-z0-9-]+)/.exec(loc) || [])[1];
  if (cal) t.push('cal:' + cal); else t.push('cal:default');
  if (o.numberingSystem || /-nu-/.test(loc)) t.push('nu:custom');
  t.push('lang:' + loc.split('-')[0]);
  return t.concat(extra || []);
}
function oracleDtf(cases) { // Node's answers, through a worker that is restarted when V8 aborts on a case
  const fs = require('fs'), os = require('os'), path = require('path'), { spawnSync } = require('child_process');
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'matrix-')), cf = path.join(dir, 'cases.jsonl'), of = path.join(dir, 'out.jsonl');
  fs.writeFileSync(cf, cases.map((c) => JSON.stringify(c)).join('\n') + '\n');
  const results = new Array(cases.length);
  let start = 0, crashes = 0;
  while (start < cases.length) {
    fs.writeFileSync(of, '');
    spawnSync(process.execPath, [path.join(__dirname, 'matrix-oracle.js'), cf, of, String(start)], { stdio: 'ignore' });
    const lines = fs.readFileSync(of, 'utf8').split('\n').filter(Boolean);
    lines.forEach((l, i) => { results[start + i] = JSON.parse(l); });
    start += lines.length;
    if (start < cases.length) { results[start] = { err: 1, crash: 1 }; start++; crashes++; }
  }
  fs.rmSync(dir, { recursive: true, force: true });
  if (crashes) console.log(`(Node's V8 aborted on ${crashes} DateTimeFormat case(s); they are skipped)`);
  return results;
}
function runDtf(calMode) {
  const cases = [];
  for (let i = 0; i < N; i++) {
    let loc = pick(LOCALES);
    const o = dtfOptions();
    if (calMode === 'real') { const c = pick(Object.keys(REAL_CALS)); loc = pick(REAL_CALS[c]); if (chance(0.5)) o.calendar = c; else loc += '-u-ca-' + c; }
    else if (calMode) { const c = pick(calMode === 'common' ? COMMON_CALS : CALS); if (chance(0.5)) o.calendar = c; else loc += '-u-ca-' + c; } else if (chance(0.06)) o.numberingSystem = pick(NUS);
    if (LOC && loc.split('-u-')[0] !== LOC) continue;
    const v = instant(), c = { loc, o, v, parts: chance(0.25), resolved: chance(0.15) };
    if (chance(0.2)) c.w = v + Math.floor(pick([0, 1, 1000, 60000, 3600000, 3 * 3600000, day, 3 * day, 40 * day, 400 * day, 4000 * day]) * (0.5 + rnd()));
    cases.push(c);
  }
  const want = oracleDtf(cases);
  cases.forEach((c, i) => {
    const w = want[i], { loc, o, v } = c;
    if (w.err) return;
    let of;
    try { of = new ours.DateTimeFormat(loc, o); } catch (e) { rec('dtf', dtfTags(loc, o), false, `CTOR ${loc} ${JSON.stringify(o)} ${e.message}`); return; }
    const exp = nn(w.f), got = tryit(() => of.format(v));
    rec('dtf', dtfTags(loc, o), got === exp, `${loc} ${JSON.stringify(o)} ${v}\n   got  ${esc(got)}\n   want ${esc(exp)}`);
    if (c.parts) {
      const pg = tryit(() => JSON.stringify(of.formatToParts(v)));
      rec('dtf-parts', dtfTags(loc, o), pg === nn(w.p), `${loc} ${JSON.stringify(o)} ${v}\n   got  ${esc(pg)}\n   want ${esc(w.p)}`);
    }
    if (c.resolved) {
      const rg = tryit(() => JSON.stringify(of.resolvedOptions()));
      rec('dtf-resolved', dtfTags(loc, o), rg === w.r, `${loc} ${JSON.stringify(o)}\n   got  ${rg}\n   want ${w.r}`);
    }
    if (c.w !== undefined) {
      const g2 = tryit(() => of.formatRange(v, c.w));
      rec('dtf-range', dtfTags(loc, o), g2 === nn(w.rg), `${loc} ${JSON.stringify(o)} ${v} ${c.w}\n   got  ${esc(g2)}\n   want ${esc(w.rg)}`);
      const pg = tryit(() => JSON.stringify(of.formatRangeToParts(v, c.w)));
      rec('dtf-rangeparts', dtfTags(loc, o), pg === nn(w.rp), `${loc} ${JSON.stringify(o)} ${v} ${c.w}\n   got  ${esc(pg)}\n   want ${esc(w.rp)}`);
    }
  });
}

// ---- NumberFormat -----------------------------------------------------------------------------------------------------------------
const CURS = ['USD', 'EUR', 'GBP', 'JPY', 'CNY', 'INR', 'KRW', 'BRL', 'MXN', 'CAD', 'AUD', 'CHF', 'RUB', 'TRY', 'SEK', 'SAR', 'EGP', 'KWD', 'BHD', 'IDR', 'VND', 'XAF', 'CLP', 'ISK', 'NGN', 'PKR', 'ILS', 'THB'];
const UNITS = ['kilometer', 'meter', 'centimeter', 'mile', 'foot', 'inch', 'kilogram', 'gram', 'pound', 'ounce', 'liter', 'milliliter', 'gallon', 'celsius', 'fahrenheit', 'percent', 'second', 'minute', 'hour', 'day', 'week', 'month', 'year', 'byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte', 'bit', 'degree', 'acre', 'hectare', 'kilometer-per-hour', 'mile-per-hour', 'meter-per-second', 'liter-per-kilometer', 'mile-per-gallon', 'millisecond'];
const VALS = () => pick([() => Math.floor(rnd() * 1000), () => rnd() * 1000, () => (rnd() - 0.5) * 1e6, () => rnd() * 1e9, () => rnd() * 10 ** (Math.floor(rnd() * 30) - 12), () => pick([0, -0, 1, -1, 0.5, 1.5, 2.5, 0.005, 0.015, 0.0049, 1e21, 123456789012345680000, 5e-7, 0.1 + 0.2, 999.995, 99999.5, 9.995, 1e6, 999999.5, NaN, Infinity, -Infinity]), () => Math.round(rnd() * 100) / 4, () => Math.round(rnd() * 1e5) / 100])();
function nfOptions() {
  const o = {};
  const st = pick(['decimal', 'decimal', 'percent', 'currency', 'currency', 'unit']);
  if (st !== 'decimal') o.style = st;
  if (st === 'currency') { o.currency = pick(CURS); if (chance(0.4)) o.currencyDisplay = pick(['code', 'name', 'narrowSymbol', 'symbol']); if (chance(0.25)) o.currencySign = 'accounting'; }
  if (st === 'unit') { o.unit = pick(UNITS); if (chance(0.5)) o.unitDisplay = pick(['long', 'short', 'narrow']); }
  if (chance(0.25)) { o.notation = pick(['compact', 'compact', 'scientific', 'engineering']); if (o.notation === 'compact' && chance(0.4)) o.compactDisplay = 'long'; }
  if (chance(0.25)) o.signDisplay = pick(['always', 'exceptZero', 'never', 'negative', 'auto']);
  if (chance(0.3)) {
    const k = rnd();
    if (k < 0.4) { o.minimumFractionDigits = Math.floor(rnd() * 4); if (chance(0.5)) o.maximumFractionDigits = o.minimumFractionDigits + Math.floor(rnd() * 3); }
    else if (k < 0.6) { o.maximumFractionDigits = Math.floor(rnd() * 5); }
    else if (k < 0.85) { o.maximumSignificantDigits = 1 + Math.floor(rnd() * 8); if (chance(0.4)) o.minimumSignificantDigits = 1 + Math.floor(rnd() * o.maximumSignificantDigits); }
    else { o.minimumIntegerDigits = 1 + Math.floor(rnd() * 5); }
  }
  if (chance(0.12)) o.roundingMode = pick(['ceil', 'floor', 'expand', 'trunc', 'halfCeil', 'halfFloor', 'halfExpand', 'halfTrunc', 'halfEven']);
  if (chance(0.06)) o.roundingPriority = pick(['morePrecision', 'lessPrecision']);
  if (chance(0.05)) o.trailingZeroDisplay = 'stripIfInteger';
  if (chance(0.05)) o.useGrouping = pick([false, 'min2', 'always', 'auto', true]);
  if (chance(0.04)) { o.roundingIncrement = pick([2, 5, 10, 25, 50, 100]); o.maximumFractionDigits = o.minimumFractionDigits = 2; }
  return o;
}
function nfTags(loc, o) {
  const t = ['style:' + (o.style || 'decimal')];
  if (o.notation) t.push('notation:' + o.notation);
  if (o.currencyDisplay) t.push('curdisplay:' + o.currencyDisplay);
  if (o.currencySign) t.push('accounting');
  if (o.signDisplay) t.push('sign:' + o.signDisplay);
  if (o.roundingMode) t.push('roundingMode'); if (o.roundingPriority) t.push('roundingPriority'); if (o.roundingIncrement) t.push('roundingIncrement');
  if (o.useGrouping !== undefined) t.push('grouping');
  if (o.numberingSystem || /-nu-/.test(loc)) t.push('nu:' + (o.numberingSystem || /-nu-([a-z]+)/.exec(loc)[1]));
  if (o.unit) t.push('unit:' + o.unit);
  t.push('lang:' + loc.split('-')[0]);
  return t;
}
function runNf() {
  for (let i = 0; i < N; i++) {
    let loc = pick(LOCALES);
    const o = nfOptions();
    if (chance(0.12)) { if (chance(0.5)) o.numberingSystem = pick(NUS); else loc += '-u-nu-' + pick(NUS); }
    if (LOC && loc.split('-u-')[0] !== LOC) continue;
    let nf, of;
    try { nf = new NativeIntl.NumberFormat(loc, o); } catch (e) { continue; }
    try { of = new ours.NumberFormat(loc, o); } catch (e) { rec('nf', nfTags(loc, o), false, `CTOR ${loc} ${JSON.stringify(o)} ${e.message}`); continue; }
    const v = VALS();
    const exp = nf.format(v), got = tryit(() => of.format(v));
    rec('nf', nfTags(loc, o), got === exp, `${loc} ${JSON.stringify(o)} ${v}\n   got  ${esc(got)}\n   want ${esc(exp)}`);
    if (chance(0.15)) { const pe = JSON.stringify(nf.formatToParts(v)), pg = tryit(() => JSON.stringify(of.formatToParts(v))); rec('nf-parts', nfTags(loc, o), pg === pe, `${loc} ${JSON.stringify(o)} ${v}\n   got  ${esc(pg)}\n   want ${esc(pe)}`); }
    if (chance(0.1)) { const w = v + Math.abs(v) * rnd() + 1; const re = tryit2(() => nf.formatRange(v, w)), rg = tryit(() => of.formatRange(v, w)); if (typeof re === 'string' && !re.startsWith('THROW')) rec('nf-range', nfTags(loc, o), rg === re, `${loc} ${JSON.stringify(o)} ${v}-${w}\n   got  ${esc(rg)}\n   want ${esc(re)}`); }
    if (chance(0.08)) { const re = JSON.stringify(nf.resolvedOptions()), rg = tryit(() => JSON.stringify(of.resolvedOptions())); rec('nf-resolved', nfTags(loc, o), rg === re, `${loc} ${JSON.stringify(o)}\n   got  ${rg}\n   want ${re}`); }
  }
}
const tryit2 = (f) => { try { return f(); } catch (e) { return 'THROW'; } };

// ---- Collator ----------------------------------------------------------------------------------------------------------------------
const ALPHA = {
  latin: 'aAbBcCdDeEfFgGhHiIjJkKlLmMnNoOpPqQrRsStTuUvVwWxXyYzZ áàâäãåāăąæçćčďđéèêëēėęěğíìîïıłñńňóòôöõøőœřśšşßťúùûüůűýÿźżž0123456789 -_.,\'/&',
  de: 'aäoöuüßsAÄ bz', fr: 'eéèêëaàâcçoôœ- ', sv: 'aåäoözAÅÄ', es: 'nñcCll', tr: 'iıİIsşgğoöuüç', pl: 'ąćęłńóśźż aznsc', cs: 'cčhrřsšzžch', hu: 'cs sz zs gy ny ly ty', da: 'aåæøoö', ru: 'абвгдеёжзийклмнопрстуфхцчшщъыьэюя', el: 'αβγδεζηθικλμνξοπρστυφχψωάέήίόύώ', ja: 'あいうえおかきくけこアイウエオカキクケコ漢字日本語ー', ko: '가나다라한국어', th: 'กขคงจเแโใไ', zh: '中文汉字北京上海',
  ar: 'ابتثجحخدذرزسشصضطظعغفقكلمنهوي', he: 'אבגדהוזחטיכלמנסעפצקרשת', hi: 'अआइईउऊकखगघ',
};
function collStrings(loc) {
  const base = loc.split('-')[0], al = ALPHA[base] || ALPHA.latin, pool = [...al];
  const out = [];
  for (let i = 0; i < 6; i++) { let s = ''; const L = 1 + Math.floor(rnd() * 5); for (let j = 0; j < L; j++) s += rnd() < 0.7 ? pick(pool) : pick([...ALPHA.latin]); out.push(s); }
  return out;
}
function runColl() {
  for (let i = 0; i < Math.max(1, N / 10); i++) {
    const loc = pick(LOCALES);
    const o = {};
    if (chance(0.4)) o.sensitivity = pick(['base', 'accent', 'case', 'variant']);
    if (chance(0.25)) o.numeric = true;
    if (chance(0.15)) o.caseFirst = pick(['upper', 'lower', 'false']);
    if (chance(0.15)) o.ignorePunctuation = true;
    if (chance(0.05)) o.usage = 'search';
    if (LOC && loc !== LOC) continue;
    let nf, of;
    try { nf = new NativeIntl.Collator(loc, o); } catch (e) { continue; }
    try { of = new ours.Collator(loc, o); } catch (e) { rec('coll', ['ctor'], false, `CTOR ${loc} ${JSON.stringify(o)}`); continue; }
    const ss = collStrings(loc).concat(chance(0.5) ? ['a1', 'a10', 'a2', 'A-1', 'a 1', 'b'] : []);
    const tags = (a, b) => { const t = ['lang:' + loc.split('-')[0]]; for (const k of Object.keys(o)) t.push(k + ':' + o[k]); return t; };
    for (let a = 0; a < ss.length; a++) for (let b = 0; b < ss.length; b++) {
      const x = Math.sign(of.compare(ss[a], ss[b])), y = Math.sign(nf.compare(ss[a], ss[b]));
      rec('coll', tags(), x === y, `${loc} ${JSON.stringify(o)} ${JSON.stringify(ss[a])} ${JSON.stringify(ss[b])} got ${x} want ${y}`);
    }
    const ro = JSON.stringify(of.resolvedOptions()), rn = JSON.stringify(nf.resolvedOptions());
    rec('coll-resolved', tags(), ro === rn, `${loc} ${JSON.stringify(o)} got ${ro} want ${rn}`);
  }
}

// ---- Segmenter ---------------------------------------------------------------------------------------------------------------------
const SEG_TEXTS = ['Hello, world! How are you? I am fine.', 'éa👨‍👩‍👧x🇺🇸🇩🇪y', "can't stop; 3.14 is pi. Mr. Smith went to Washington.", '日本語のテキストを分割します。今日は天気がいいです。', '我喜欢吃苹果，也喜欢吃香蕉。北京是中国的首都。', 'ภาษาไทยเป็นภาษาที่สวยงาม ฉันรักประเทศไทย', 'ສະບາຍດີ ຂ້ອຍຮັກປະເທດລາວ', 'ខ្ញុំស្រលាញ់ប្រទេសកម្ពុជា', 'ကျွန်တော် မြန်မာစကား ပြောတတ်ပါတယ်', '한국어 문장입니다. 안녕하세요!', 'a\r\nb\nc', '😀😀', 'Привет, мир! Как дела?', 'The U.S.A. is big... Really? Yes!', 'नमस्ते दुनिया। आप कैसे हैं?', 'مرحبا بالعالم. كيف حالك؟', 'one-two three_four 1,234.56 foo@bar.com', 'ひらがなカタカナ漢字ＡＢＣ１２３', '"Quoted." (Parens.) Next sentence.', 'é̂ a‍b क्ष'];
function runSeg() {
  for (const gr of ['grapheme', 'word', 'sentence']) for (const t of SEG_TEXTS) for (const loc of ['en', 'ja', 'zh', 'th', 'ko', 'km']) {
    const a = new ours.Segmenter(loc, { granularity: gr }), b = new NativeIntl.Segmenter(loc, { granularity: gr });
    const ea = JSON.stringify([...a.segment(t)]), eb = JSON.stringify([...b.segment(t)]);
    const script = /[฀-๿]/.test(t) ? 'thai' : /[຀-໿]/.test(t) ? 'lao' : /[ក-៿]/.test(t) ? 'khmer' : /[က-႟]/.test(t) ? 'burmese' : /[一-鿿぀-ヿ]/.test(t) ? 'cjk' : 'other';
    rec('seg', [gr, script], ea === eb, `${gr} ${loc} ${JSON.stringify(t)}\n   got  ${ea}\n   want ${eb}`);
  }
}

const all = which === 'all';
if (all || which === 'dtf') runDtf(false);
if (all || which === 'cal') runDtf('common');
if (which === 'calreal') runDtf('real');
if (which === 'calall') runDtf('all');
if (all || which === 'nf') runNf();
if (all || which === 'coll') runColl();
if (all || which === 'seg') runSeg();
for (const svc of Object.keys(results)) {
  const r = results[svc];
  console.log(`${svc.padEnd(15)} ${String(r.n - r.bad).padStart(7)} / ${String(r.n).padEnd(7)} match  ${(100 * (r.n - r.bad) / r.n).toFixed(2)}%`);
  if (process.argv.includes('--tags')) {
    const rows = Object.entries(r.tags).filter(([, v]) => v[1]).sort((a, b) => b[1][1] - a[1][1]).slice(0, 25);
    for (const [k, v] of rows) console.log(`      ${k.padEnd(28)} ${String(v[1]).padStart(6)} of ${String(v[0]).padEnd(6)} differ (${(100 * v[1] / v[0]).toFixed(1)}%)`);
  }
}
