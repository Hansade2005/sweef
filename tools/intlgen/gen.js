#!/usr/bin/env node
// intlgen: turns Unicode CLDR JSON into the locale data internal/intl embeds for Intl.
//
//   cd tools/intlgen && ./fetch.sh /tmp/cldr          # downloads cldr-*-full@47.0.0 with npm
//   node gen.js /tmp/cldr ../../internal/intl/data    # writes <tag>.json.gz, global.json.gz, plurals.json.gz
//
// CLDR 47 is what Node 22 / ICU 77 uses, so the golden tests (internal/engine/intl_golden_test.go, produced by
// tools/intlgen/golden.js) compare Sweef against a real ICU. The output is deliberately small: only the sections
// Intl needs, only the units, currencies, languages and regions pages actually use, and regional locales as
// differences from their parent. See docs/web-platform.md for the list of locales.
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib');
const CLDR = process.argv[2], OUT = process.argv[3];
if (!CLDR || !OUT) { console.error('usage: gen.js <cldr dir> <out dir>'); process.exit(2); }
const rd = (p) => JSON.parse(fs.readFileSync(path.join(CLDR, p), 'utf8'));
const has = (p) => fs.existsSync(path.join(CLDR, p));

// ---- the locales we ship: tag -> parent (null: root-like data of its own) --------------------------------------------
const LOCALES = {
  en: null, 'en-001': 'en', 'en-GB': 'en-001', 'en-AU': 'en-001', 'en-IN': 'en-001', 'en-CA': 'en',
  es: null, 'es-419': 'es', 'es-MX': 'es-419', 'es-US': 'es-419',
  pt: null, 'pt-PT': 'pt', fr: null, 'fr-CA': 'fr', de: null, 'de-AT': 'de', 'de-CH': 'de', it: null, nl: null,
  ru: null, uk: null, pl: null, tr: null, ar: null, 'ar-AE': 'ar', he: null, fa: null, hi: null, bn: null, ur: null,
  id: null, ms: null, vi: null, th: null, ja: null, ko: null, zh: null, 'zh-Hant': 'zh', 'zh-Hant-HK': 'zh-Hant',
  sv: null, da: null, nb: null, fi: null, cs: null, sk: null, hu: null, ro: null, el: null, bg: null, hr: null, sr: null,
  ca: null, ta: null, te: null, mr: null, fil: null, sw: null, lt: null, lv: null, et: null, sl: null, af: null,
  gu: null, kn: null, ml: null, pa: null, ne: null, si: null, km: null, my: null, az: null, kk: null, uz: null,
};
const cldrId = (tag) => tag === 'zh' ? 'zh-Hans' : tag; // CLDR names the unmarked Chinese "zh"; its data lives in zh (resolved)
// the units Intl accepts (ECMA-402 sanctioned list)
const UNITS = { acre: 'area-acre', bit: 'digital-bit', byte: 'digital-byte', celsius: 'temperature-celsius', centimeter: 'length-centimeter', day: 'duration-day',
  degree: 'angle-degree', fahrenheit: 'temperature-fahrenheit', 'fluid-ounce': 'volume-fluid-ounce', foot: 'length-foot', gallon: 'volume-gallon', gigabit: 'digital-gigabit',
  gigabyte: 'digital-gigabyte', gram: 'mass-gram', hectare: 'area-hectare', hour: 'duration-hour', inch: 'length-inch', kilobit: 'digital-kilobit', kilobyte: 'digital-kilobyte',
  kilogram: 'mass-kilogram', kilometer: 'length-kilometer', liter: 'volume-liter', megabit: 'digital-megabit', megabyte: 'digital-megabyte', meter: 'length-meter',
  microsecond: 'duration-microsecond', mile: 'length-mile', 'mile-scandinavian': 'length-mile-scandinavian', milliliter: 'volume-milliliter', millimeter: 'length-millimeter',
  millisecond: 'duration-millisecond', minute: 'duration-minute', month: 'duration-month', nanosecond: 'duration-nanosecond', ounce: 'mass-ounce', percent: 'concentr-percent',
  petabyte: 'digital-petabyte', pound: 'mass-pound', second: 'duration-second', stone: 'mass-stone', terabit: 'digital-terabit', terabyte: 'digital-terabyte', week: 'duration-week',
  yard: 'length-yard', year: 'duration-year' };
const UNIT_COMPOUND = { 'kilometer-per-hour': 'speed-kilometer-per-hour', 'mile-per-hour': 'speed-mile-per-hour', 'meter-per-second': 'speed-meter-per-second',
  'liter-per-kilometer': 'consumption-liter-per-kilometer', 'mile-per-gallon': 'consumption-mile-per-gallon' };
const CURRENCIES = 'USD EUR GBP JPY CNY INR KRW BRL MXN CAD AUD CHF RUB TRY SEK NOK DKK PLN CZK HUF ILS AED SAR EGP ZAR NGN KES IDR MYR THB VND PHP SGD HKD TWD NZD ARS CLP COP PEN UAH RON BGN ISK RSD PKR BDT LKR QAR KWD BHD OMR JOD MAD DZD TND IRR IQD KZT UZS GEL AZN XAF XOF GHS ETB TZS UGX'.split(' ');
const SCRIPTS = 'Latn Cyrl Arab Hans Hant Deva Hebr Grek Jpan Kore Thai Beng Taml Telu Knda Mlym Gujr Guru Armn Geor Khmr Laoo Mymr Sinh Tibt Ethi Hira Kana Hang Zyyy Zzzz Orya'.split(' ');

const clean = (o) => { for (const k of Object.keys(o)) { const v = o[k]; if (v && typeof v === 'object' && !Array.isArray(v)) { clean(v); if (!Object.keys(v).length) delete o[k]; } else if (v === undefined) delete o[k]; } return o; };
const PLURALS = ['zero', 'one', 'two', 'few', 'many', 'other'];
function counts(obj, prefix) { // {"prefix-count-one": "..", ...} -> {one:..,other:..}
  const out = {};
  for (const k of Object.keys(obj)) {
    const m = k.startsWith(prefix + '-count-') ? k.slice(prefix.length + 7) : null;
    if (m && PLURALS.includes(m)) out[m] = obj[k];
  }
  return out;
}
// a pattern CLDR gives a numbering system for ({_value, _numbers}) is written as "pattern@numbers=hebr" ("@numbers=d=hanidays" for one field)
const withNumbers = (v) => v && typeof v === 'object' && v._value !== undefined ? v._value + '@numbers=' + v._numbers : v;
function altAscii(obj0) { // CLDR 44+ keeps U+202F in some time patterns and offers an ASCII alternative; Chromium ships the ASCII one
  const obj = {};
  for (const k of Object.keys(obj0)) obj[k] = withNumbers(obj0[k]);
  const out = {};
  for (const k of Object.keys(obj)) if (!k.includes('-alt-')) out[k] = obj[k];
  // the alternative is taken only where it is the same pattern with plain spaces: regional locales inherit the "-alt-ascii"
  // keys of "en" (en-GB: "HH:mm" next to an inherited "h:mm a"), which must not replace their own pattern
  for (const k of Object.keys(obj)) if (k.endsWith('-alt-ascii')) { const b = k.slice(0, -10); if (out[b] === undefined || out[b].replace(/\u202f/g, ' ') === obj[k]) out[b] = obj[k]; }
  return out;
}

function numbers(tag) {
  const id = cldrId(tag), n = rd(`cldr-numbers-full/main/${id}/numbers.json`).main[id].numbers;
  const nu = n.defaultNumberingSystem;
  const out = { nu, mgd: +n.minimumGroupingDigits, sym: {} };
  for (const k of Object.keys(n)) {
    const m = /^symbols-numberSystem-(.+)$/.exec(k);
    if (!m) continue;
    const s = n[k];
    const rec = { d: s.decimal, g: s.group, p: s.percentSign, pm: s.perMille, mi: s.minusSign, pl: s.plusSign, e: s.exponential, inf: s.infinity, nan: s.nan, ap: s.approximatelySign };
    if (s.currencyDecimal) rec.cd = s.currencyDecimal;
    if (s.currencyGroup) rec.cg = s.currencyGroup;
    out.sym[m[1]] = rec;
  }
  // the patterns of a numbering system (a locale has its own for each system it knows: Arabic's accounting format differs between 0-9 and ٠-٩)
  const formatSet = (sys) => {
    const o = {};
    const df = n['decimalFormats-numberSystem-' + sys] || n['decimalFormats-numberSystem-latn'];
    const cf = n['currencyFormats-numberSystem-' + sys] || n['currencyFormats-numberSystem-latn'];
    const pf = n['percentFormats-numberSystem-' + sys] || n['percentFormats-numberSystem-latn'];
    const sf = n['scientificFormats-numberSystem-' + sys] || n['scientificFormats-numberSystem-latn'];
    o.dec = df.standard; o.pct = pf.standard; o.sci = sf.standard;
    o.cur = cf.standard; o.acc = cf.accounting;
    if (cf['standard-alphaNextToNumber']) o.curA = cf['standard-alphaNextToNumber'];
    if (cf['accounting-alphaNextToNumber']) o.accA = cf['accounting-alphaNextToNumber'];
    const compact = (src) => {
      const c = {};
      for (const k of Object.keys(src)) {
        const m = /^(\d+)-count-(\w+)$/.exec(k);
        if (!m) continue;
        (c[m[1]] = c[m[1]] || {})[m[2]] = src[k];
      }
      return c;
    };
    o.cpd = { s: compact(df.short.decimalFormat), l: compact(df.long.decimalFormat) };
    const cs = cf.short && cf.short.standard ? cf.short.standard : null;
    if (cs) {
      o.cpc = compact(cs);
      const alt = {};
      for (const k of Object.keys(cs)) { const m = /^(\d+)-count-(\w+)-alt-alphaNextToNumber$/.exec(k); if (m) (alt[m[1]] = alt[m[1]] || {})[m[2]] = cs[k]; }
      if (Object.keys(alt).length) o.cpcA = alt;
    }
    o.cup = counts(cf, 'unitPattern'); // currencyDisplay: "name" -> "{0} {1}"
    const mp = n['miscPatterns-numberSystem-' + sys] || n['miscPatterns-numberSystem-latn'];
    if (mp) o.mp = { ap: mp.approximately, al: mp.atLeast, am: mp.atMost, r: mp.range };
    return o;
  };
  const cf = n['currencyFormats-numberSystem-' + nu] || n['currencyFormats-numberSystem-latn'];
  const base = formatSet(nu);
  Object.assign(out, base);
  for (const k of Object.keys(n)) { // other numbering systems of the locale, as the difference from the default
    const m = /^decimalFormats-numberSystem-(.+)$/.exec(k);
    if (!m || m[1] === nu) continue;
    const d = diff(base, formatSet(m[1]));
    if (d) (out.fm = out.fm || {})[m[1]] = d;
  }
  if (nu !== 'arab' && !n['decimalFormats-numberSystem-arab']) { // ICU's root has Arabic-Indic patterns of its own (number then currency, no grouping by lakh)
    const root = Object.assign({}, base, { dec: '#,##0.###', pct: '#,##0%', sci: '#E0', cur: '#,##0.00\u00a0¤', acc: '#,##0.00\u00a0¤', });
    delete root.curA; delete root.accA;
    const d = diff(base, root);
    if (d) (out.fm = out.fm || {}).arab = d;
  }
  const sp = cf.currencySpacing;
  if (sp) {
    out.cs = { b: sp.beforeCurrency.insertBetween, a: sp.afterCurrency.insertBetween };
  }
  // currencies
  const cur = rd(`cldr-numbers-full/main/${id}/currencies.json`).main[id].numbers.currencies;
  out.cur$ = {};
  for (const code of CURRENCIES) {
    const c = cur[code]; if (!c) continue;
    const cn = counts(c, 'displayName');
    const entry = [c.symbol || code, c['symbol-alt-narrow'] || c.symbol || code, c.displayName, cn];
    if (entry[1] === entry[0]) entry[1] = '';
    out.cur$[code] = entry;
  }
  return out;
}

function units(tag) {
  const id = cldrId(tag), u = rd(`cldr-units-full/main/${id}/units.json`).main[id].units;
  const out = { s: {}, per: {} };
  const width = { l: u.long, s: u.short, n: u.narrow };
  for (const w of Object.keys(width)) out.per[w] = width[w].per ? width[w].per.compoundUnitPattern : undefined;
  const ids = Object.assign({}, UNITS, UNIT_COMPOUND);
  for (const [name, key] of Object.entries(ids)) {
    const e = {};
    for (const w of Object.keys(width)) {
      const d = width[w]['' + key] || width[w][key];
      const x = d || (width[w][key]);
      if (!x) continue;
      const pats = counts(x, 'unitPattern');
      const rec = { p: pats };
      if (x.displayName) rec.d = x.displayName;
      if (x.perUnitPattern) rec.u = x.perUnitPattern;
      e[w] = rec;
    }
    if (Object.keys(e).length) out.s[name] = e;
  }
  return out;
}

function dateFields(tag) {
  const id = cldrId(tag), f = rd(`cldr-dates-full/main/${id}/dateFields.json`).main[id].dates.fields;
  const out = {};
  const units = ['year', 'quarter', 'month', 'week', 'day', 'hour', 'minute', 'second'];
  const names = ['era', 'year', 'quarter', 'month', 'weekOfYear', 'weekday', 'day', 'dayPeriod', 'hour', 'minute', 'second', 'timeZoneName'];
  const fkey = { weekOfYear: 'week', timeZoneName: 'zone', dayPeriod: 'dayperiod' };
  for (const [suffix, w] of [['', 'l'], ['-short', 's'], ['-narrow', 'n']]) {
    for (const u of units) {
      const d = f[u + suffix] || f[u];
      if (!d) continue;
      const rec = {};
      for (const k of Object.keys(d)) {
        const m = /^relative-type-(-?\d+)$/.exec(k);
        if (m) rec['r' + m[1]] = d[k];
      }
      if (d['relativeTime-type-future']) rec.f = counts(d['relativeTime-type-future'], 'relativeTimePattern');
      if (d['relativeTime-type-past']) rec.p = counts(d['relativeTime-type-past'], 'relativeTimePattern');
      if (d.displayName) rec.d = d.displayName;
      (out[u] = out[u] || {})[w] = rec;
    }
    for (const nm of names) {
      const d = f[(fkey[nm] || nm) + suffix];
      if (d && d.displayName) (out.dn = out.dn || {})[nm + ':' + w] = d.displayName;
    }
  }
  return out;
}

// ICU's DateTimePatternGenerator meets availableFormats in the order of the resource bundles: the locale's own keys (sorted),
// then its parent's, up to root. Ties between equally distant patterns go to the first one, so the order is part of the data.
const PARENT = rd('cldr-core/supplemental/parentLocales.json').supplemental.parentLocales.parentLocale;
const cldrParent = (id) => id === 'und' ? null : PARENT[id] ? (PARENT[id] === 'root' ? 'und' : PARENT[id]) : id.includes('-') ? id.slice(0, id.lastIndexOf('-')) : 'und';
const calFile = (id, cal) => cal === 'gregory' ? `cldr-dates-full/main/${id}/ca-gregorian.json` : `cldr-cal-${cal.split('-')[0]}-full/main/${id}/ca-${cal}.json`;
const afCache = {};
function resolvedAf(id, cal) { // availableFormats of a locale as CLDR resolves them
  const k = id + '|' + cal;
  if (afCache[k] !== undefined) return afCache[k];
  const f = calFile(id, cal);
  let out = null;
  if (has(f)) {
    const j = rd(f).main[id].dates.calendars[cal === 'gregory' ? 'gregorian' : cal];
    const af = altAscii((j.dateTimeFormats || {}).availableFormats || {});
    out = {};
    for (const key of Object.keys(af)) if (!/-count-/.test(key)) out[key] = af[key];
  }
  return (afCache[k] = out);
}
function orderedAf(tag, cal, af) {
  const out = {};
  let id = cldrId(tag), cur = af;
  while (id) {
    const parent = cldrParent(id);
    const pa = parent ? (resolvedAf(parent, cal) || {}) : {};
    const own = Object.keys(cur).filter((k) => !(k in out) && (!parent || pa[k] !== cur[k])).sort();
    for (const k of own) out[k] = cur[k];
    if (!parent) break;
    id = parent; cur = resolvedAf(parent, cal) || {};
    if (id === 'und' && !Object.keys(cur).length) break;
  }
  for (const k of Object.keys(af)) if (!(k in out)) out[k] = af[k]; // (never happens: root is the last link)
  return out;
}

function calendar(tag, cal) {
  const id = cldrId(tag);
  const file = calFile(id, cal);
  if (!has(file)) return null;
  const j = rd(file).main[id].dates.calendars[cal === 'gregory' ? 'gregorian' : cal];
  const arr = (o, n, start) => { const a = []; for (let i = start; i < start + n; i++) a.push(o[i]); return a; };
  const dayKeys = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'];
  const out = {};
  const ctx = { f: 'format', s: 'stand-alone' }, wd = { w: 'wide', a: 'abbreviated', n: 'narrow', s: 'short' };
  out.mo = {}; out.da = {}; out.qu = {}; out.dp = {};
  for (const c of Object.keys(ctx)) {
    for (const w of ['w', 'a', 'n']) {
      const m = j.months && j.months[ctx[c]] && j.months[ctx[c]][wd[w]];
      if (m) {
        out.mo[c + w] = arr(m, m['13'] !== undefined ? 13 : 12, 1); // (Hebrew, Coptic and Ethiopic have thirteen)
        if (m['7-yeartype-leap'] !== undefined) (out.ml = out.ml || {})[c + w] = m['7-yeartype-leap']; // Hebrew: the seventh month in a leap year is Adar II
      }
      const q = j.quarters && j.quarters[ctx[c]] && j.quarters[ctx[c]][wd[w]]; if (q) out.qu[c + w] = arr(q, 4, 1);
      const p = j.dayPeriods && j.dayPeriods[ctx[c]] && j.dayPeriods[ctx[c]][wd[w]]; if (p) out.dp[c + w] = Object.fromEntries(Object.entries(p).filter(([k]) => !k.includes('-alt-')));
    }
    for (const w of ['w', 'a', 'n', 's']) {
      const d = j.days && j.days[ctx[c]] && j.days[ctx[c]][wd[w]]; if (d) out.da[c + w] = dayKeys.map((k) => d[k]);
    }
  }
  const era = (o) => o ? Object.fromEntries(Object.entries(o).filter(([k]) => /^\d+$/.test(k))) : {};
  if (j.eras) out.er = { w: era(j.eras.eraNames), a: era(j.eras.eraAbbr), n: era(j.eras.eraNarrow) };
  out.df = altAscii(j.dateFormats || {}); out.tf = altAscii(j.timeFormats || {});
  const dtf = j.dateTimeFormats || {};
  out.dtf = { full: dtf.full, long: dtf.long, medium: dtf.medium, short: dtf.short };
  const at = j['dateTimeFormats-atTime'] && j['dateTimeFormats-atTime'].standard;
  if (at) out.dtfAt = { full: at.full, long: at.long, medium: at.medium, short: at.short };
  if (dtf.appendItems) out.ai = dtf.appendItems; // DateTimePatternGenerator's appendItems ("{0} ({2}: {1})")
  const af = altAscii(dtf.availableFormats || {});
  const afPlain = {};
  for (const k of Object.keys(af)) if (!/-count-/.test(k)) afPlain[k] = af[k];
  out.af = orderedAf(tag, cal, afPlain);
  if (dtf.intervalFormats) {
    const iv = {};
    for (const k of Object.keys(dtf.intervalFormats)) {
      if (k === 'intervalFormatFallback') { iv._ = dtf.intervalFormats[k]; continue; }
      iv[k] = altAscii(dtf.intervalFormats[k]);
    }
    out.iv = iv;
  }
  return out;
}

// ---- time zone names ---------------------------------------------------------------------------------------------------------------
// Names are kept for the metazones of the zones of the most populous countries (everything else shows a GMT offset, as it does for
// a locale without names): all 150 metazones in 73 locales would cost 200 KB. ZONETAB is the IANA zone.tab (tzdata package).
const ZONE_COUNTRIES = 100, ZONETAB = process.env.ZONETAB || '/usr/share/zoneinfo/zone.tab';
let zctx = null;
function zoneCtx() {
  if (zctx) return zctx;
  const tz = rd('cldr-bcp47/bcp47/timezone.json').keyword.u.tz, list = [], alias = {};
  for (const [k, v] of Object.entries(tz)) {
    if (k.startsWith('_') || !v._alias) continue;
    const names = v._alias.split(' ');
    if (v._deprecated === 'true') continue;
    list.push(names[0]);
    for (const a of names.slice(1)) alias[a] = names[0];
  }
  if (!fs.existsSync(ZONETAB)) { console.error('zone.tab not found; set ZONETAB to the IANA zone.tab'); process.exit(2); }
  const iana = {};
  for (const l of fs.readFileSync(ZONETAB, 'utf8').split('\n')) { if (!l || l[0] === '#') continue; const f = l.split('\t'); iana[f[2]] = f[0]; }
  const country = {}; // CLDR canonical id -> ISO country
  for (const c of list) {
    let cc = iana[c];
    if (!cc) for (const [a, canon] of Object.entries(alias)) if (canon === c && iana[a]) { cc = iana[a]; break; }
    if (cc) country[c] = cc;
  }
  const ti = rd('cldr-core/supplemental/territoryInfo.json').supplemental.territoryInfo;
  const top = new Set(Object.entries(ti).sort((a, b) => +b[1]._population - +a[1]._population).slice(0, ZONE_COUNTRIES).map((e) => e[0]));
  const mzInfo = rd('cldr-core/supplemental/metaZones.json').supplemental.metaZones.metazoneInfo.timezone;
  const keep = new Set(['GMT']);
  const walk = (o, prefix) => { for (const [k, v] of Object.entries(o)) { if (Array.isArray(v)) { const z = prefix + k; if (top.has(country[z]) || z === 'Etc/UTC') v.forEach((e) => keep.add(e.usesMetazone._mzone)); } else walk(v, prefix + k + '/'); } };
  walk(mzInfo, '');
  return (zctx = { list, alias, country, keep });
}

function zoneNames(tag) {
  const id = cldrId(tag), z = rd(`cldr-dates-full/main/${id}/timeZoneNames.json`).main[id].dates.timeZoneNames;
  const out = { hf: z.hourFormat, gmt: z.gmtFormat, gz: z.gmtZeroFormat, rf: z.regionFormat, rfd: z['regionFormat-type-daylight'], rfs: z['regionFormat-type-standard'], ff: z.fallbackFormat };
  const ctx = zoneCtx();
  out.mz = {};
  for (const [k, v] of Object.entries(z.metazone || {})) {
    if (!ctx.keep.has(k)) continue;
    const rec = {};
    for (const w of ['long', 'short']) if (v[w]) rec[w[0]] = [v[w].generic || '', v[w].standard || '', v[w].daylight || ''];
    out.mz[k] = rec;
  }
  // names of single zones (Etc/UTC, Europe/London in summer, ...) and, in English, exemplar cities that are not simply the zone id
  out.zo = {};
  const city = (id) => id.split('/').pop().replace(/_/g, ' ');
  const walkZones = (o, prefix) => {
    for (const [k, v] of Object.entries(o || {})) {
      if (!v || typeof v !== 'object') continue;
      const id = prefix + k;
      if (v.long || v.short) {
        const rec = {};
        for (const w of ['long', 'short']) if (v[w]) rec[w[0]] = [v[w].generic || '', v[w].standard || '', v[w].daylight || ''];
        out.zo[id] = rec;
      }
      if (tag === 'en' && v.exemplarCity && v.exemplarCity !== city(id)) (out.zn = out.zn || {})[id] = v.exemplarCity;
      walkZones(Object.fromEntries(Object.entries(v).filter(([kk, vv]) => vv && typeof vv === 'object' && !['long', 'short'].includes(kk))), id + '/');
    }
  };
  walkZones(z.zone, '');
  return out;
}

function lists(tag) {
  const id = cldrId(tag), l = rd(`cldr-misc-full/main/${id}/listPatterns.json`).main[id].listPatterns;
  const out = {};
  for (const type of ['standard', 'or', 'unit']) {
    for (const [suffix, w] of [['', 'l'], ['-short', 's'], ['-narrow', 'n']]) {
      const p = l['listPattern-type-' + type + suffix]; if (!p) continue;
      out[type + ':' + w] = [p.start, p.middle, p.end, p['2']];
    }
  }
  return out;
}

function names(tag) {
  const id = cldrId(tag);
  const base = `cldr-localenames-full/main/${id}/`;
  const out = {};
  const lang = rd(base + 'languages.json').main[id].localeDisplayNames.languages;
  const L2 = /^[a-z]{2,3}(-[A-Za-z0-9]+)*$/;
  const wantLang = (k) => L2.test(k) && !/-alt-/.test(k) && (/^[a-z]{2}$/.test(k) || k === 'fil' || /^(en|es|pt|fr|de|zh|ar|nl|sw|ro|fa|ru|sr|hi|ur|bn|ta|az)-[A-Za-z0-9]+$/.test(k));
  out.lg = {};
  for (const k of Object.keys(lang)) if (wantLang(k)) out.lg[k] = lang[k];
  for (const k of Object.keys(lang)) { const m = /^(.+)-alt-short$/.exec(k); if (m && out.lg[m[1]] !== undefined) (out.lgs = out.lgs || {})[m[1]] = lang[k]; }
  const terr = rd(base + 'territories.json').main[id].localeDisplayNames.territories;
  out.rg = {}; out.rgs = {};
  for (const k of Object.keys(terr)) {
    if (/-alt-/.test(k)) { const m = /^([A-Z0-9]{2,3})-alt-short$/.exec(k); if (m) out.rgs[m[1]] = terr[k]; continue; }
    if (/^([A-Z]{2}|\d{3})$/.test(k)) out.rg[k] = terr[k];
  }
  const scr = rd(base + 'scripts.json').main[id].localeDisplayNames.scripts;
  out.sc = {};
  for (const k of SCRIPTS) if (scr[k]) out.sc[k] = scr[k];
  const ldn = rd(base + 'localeDisplayNames.json').main[id].localeDisplayNames;
  out.pt = [ldn.localeDisplayPattern.localePattern, ldn.localeDisplayPattern.localeSeparator, ldn.localeDisplayPattern.localeKeyTypePattern];
  if (ldn.types && ldn.types.calendar) out.ca = ldn.types.calendar;
  return out;
}

// Calendars other than the Gregorian one are shipped for the locales that use them and for English (everything else shows English
// names for them): the data is kept as the difference from the same locale's Gregorian calendar, which most of it equals.
const CAL_LOCALES = {
  buddhist: ['en', 'th'], japanese: ['en', 'ja'], roc: ['en', 'zh', 'zh-Hant'], persian: ['en', 'fa'],
  islamic: ['en', 'ar', 'fa', 'tr', 'id', 'ms', 'ur'], hebrew: ['en', 'he'], indian: ['en', 'hi'], coptic: ['en', 'ar'],
  ethiopic: ['en'], 'ethiopic-amete-alem': ['en'],
};
const midOf = (pat) => { // the text between {1} and {0} of a date-time pattern, quotes removed; null when the time comes first
  if (!pat) return null;
  const m = /^(.*)\{1\}(.*)\{0\}(.*)$/.exec(pat);
  if (!m) return null;
  return m[2].replace(/'([^']*)'/g, '$1').replace(/''/g, "'");
};
function glueFor(tag, cal, style, own, greg) { // 'c' when ICU glues date and time with the calendar's pattern, otherwise 'g'
  if (!own || own === greg) return 'g';
  const nodeCal = cal === 'ethiopic-amete-alem' ? 'ethioaa' : cal;
  try {
    const t = tag === 'zh' ? 'zh-Hans' : tag, base = { timeZone: 'UTC' }, v = Date.UTC(2025, 9, 14, 15, 4, 5);
    const loc = t + '-u-ca-' + nodeCal;
    const strip = (x) => x.replace(/ /g, ' ');
    const d = strip(new Intl.DateTimeFormat(loc, Object.assign({ dateStyle: style }, base)).format(v));
    const tm = strip(new Intl.DateTimeFormat(loc, Object.assign({ timeStyle: 'short' }, base)).format(v));
    const both = strip(new Intl.DateTimeFormat(loc, Object.assign({ dateStyle: style, timeStyle: 'short' }, base)).format(v));
    if (!both.startsWith(d) || !both.endsWith(tm)) return 'g';
    const mid = both.slice(d.length, both.length - tm.length);
    if (midOf(own) === mid && midOf(greg) !== mid) return 'c';
    return 'g';
  } catch (e) { return 'g'; }
}
function perLocale(tag) {
  const d = { n: numbers(tag), u: units(tag), r: dateFields(tag), l: lists(tag), z: zoneNames(tag), dn: names(tag), c: {} };
  const g0 = calendar(tag, 'gregory');
  d.c.gregory = g0;
  for (const [cal, tags] of Object.entries(CAL_LOCALES)) {
    // regional locales (en-GB, ar-AE, zh-Hant-HK) carry what their language does; they differ in their own patterns
    if (!tags.includes(tag) && !tags.includes(LOCALES[tag]) && !(LOCALES[tag] && tags.includes(LOCALES[LOCALES[tag]])) ) continue;
    const c = calendar(tag, cal);
    if (!c) continue;
    // the date-time glue ("{1} 'at' {0}"): the calendar's own pattern or the Gregorian one, whichever ICU uses (CLDR makes non-Gregorian
    // calendars borrow the Gregorian glue unless a locale writes its own, which its resolved JSON cannot tell). Decided by what the Node
    // running this generator formats: the text between the date and the time.
    const gl = { dtfAt: {}, dtf: {} };
    for (const k of ['full', 'long', 'medium', 'short']) {
      const pick = glueFor(tag, cal, k, (c.dtfAt || {})[k], (g0.dtfAt || {})[k]);
      gl.dtfAt[k] = pick === 'c' ? c.dtfAt[k] : g0.dtfAt[k];
      gl.dtf[k] = pick === 'c' ? c.dtf[k] : g0.dtf[k];
    }
    c.dtfAt = gl.dtfAt; c.dtf = gl.dtf;
    c.ai = g0.ai;
    const df = diff(g0, c) || {};
    // the patterns DateTimePatternGenerator knows are the calendar's own table, not a mix with the Gregorian one (the Buddhist calendar
    // lists yyyyMMMd where the Gregorian one has yMMMd), so they are kept whole
    df.af = c.af;
    if (c.iv) df.iv = c.iv;
    d.c[cal] = df;
  }
  return d;
}

// ---- differences from the parent ------------------------------------------------------------------------------------------
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
function diff(parent, child) {
  if (same(parent, child)) return undefined;
  if (child && typeof child === 'object' && !Array.isArray(child) && parent && typeof parent === 'object' && !Array.isArray(parent)) {
    const out = {};
    for (const k of Object.keys(child)) { const d = diff(parent[k], child[k]); if (d !== undefined) out[k] = d; }
    for (const k of Object.keys(parent)) if (!(k in child) && parent[k] !== undefined) out[k] = null; // the child lacks what its parent has
    return Object.keys(out).length ? out : undefined;
  }
  return child;
}

// ---- global data ------------------------------------------------------------------------------------------------------------------
function globalData() {
  const g = {};
  const ns = rd('cldr-core/supplemental/numberingSystems.json').supplemental.numberingSystems;
  g.digits = {};
  for (const [k, v] of Object.entries(ns)) if (v._type === 'numeric') g.digits[k] = v._digits;
  const cd = rd('cldr-core/supplemental/currencyData.json').supplemental.currencyData;
  g.cdig = {};
  for (const [k, v] of Object.entries(cd.fractions)) if (k !== 'DEFAULT') g.cdig[k] = [+v._digits, +v._rounding];
  g.cdig.DEFAULT = [+cd.fractions.DEFAULT._digits, +cd.fractions.DEFAULT._rounding];
  const td = rd('cldr-core/supplemental/timeData.json').supplemental.timeData;
  g.hc = {};
  for (const [k, v] of Object.entries(td)) g.hc[k] = v._preferred;
  // flexible day periods (dayPeriod: "morning", "in the afternoon"): rules in minutes since midnight
  const dp = rd('cldr-core/supplemental/dayPeriods.json').supplemental.dayPeriodRuleSet;
  const mins = (t) => { const [h, m] = t.split(':').map(Number); return h * 60 + m; };
  g.dpr = {};
  for (const [lang, rules] of Object.entries(dp)) {
    g.dpr[lang] = {};
    for (const [name, r] of Object.entries(rules)) g.dpr[lang][name] = r._at !== undefined ? [mins(r._at)] : [mins(r._from), mins(r._before)];
  }
  const wd = rd('cldr-core/supplemental/weekData.json').supplemental.weekData;
  g.week = { md: wd.minDays, fd: wd.firstDay, ws: wd.weekendStart, we: wd.weekendEnd };
  // regional locales whose default numbering system differs from their language's (ar-EG counts in Arabic-Indic digits)
  g.nu = {};
  for (const d of fs.readdirSync(path.join(CLDR, 'cldr-numbers-full/main'))) {
    if (!d.includes('-') || !Object.keys(LOCALES).some((t) => t.split('-')[0] === d.split('-')[0])) continue;
    const nuOf = (id) => { while (id && !has(`cldr-numbers-full/main/${id}/numbers.json`)) id = cldrParent(id); return id ? rd(`cldr-numbers-full/main/${id}/numbers.json`).main[id].numbers.defaultNumberingSystem : 'latn'; };
    const own = nuOf(d), up = nuOf(cldrParent(d));
    if (own !== up) g.nu[d] = own;
  }
  g.parents = rd('cldr-core/supplemental/parentLocales.json').supplemental.parentLocales.parentLocale;
  g.locales = Object.keys(LOCALES);
  // symbols of every numbering system: CLDR root keeps them only for latn, so the first locale that defines a system
  // answers for it (locales that do not define a system use these with their own digits, as ICU does with root)
  g.rootSym = {};
  const order = ['ar', 'fa', 'ur', 'bn', 'hi', 'mr', 'ne', 'th', 'my', 'km', 'lo', 'ta', 'te', 'kn', 'ml', 'gu', 'pa', 'or'];
  const dirs = fs.readdirSync(path.join(CLDR, 'cldr-numbers-full/main')).filter((d) => d !== 'und');
  dirs.sort((x, y) => { const ix = order.indexOf(x), iy = order.indexOf(y); return (ix < 0 ? 999 : ix) - (iy < 0 ? 999 : iy) || (x < y ? -1 : 1); });
  for (const d of dirs) {
    const num = rd(`cldr-numbers-full/main/${d}/numbers.json`).main[d].numbers;
    for (const k of Object.keys(num)) {
      const m = /^symbols-numberSystem-(.+)$/.exec(k);
      if (!m || g.rootSym[m[1]]) continue;
      const s = num[k];
      if (m[1] === 'arab' || m[1] === 'arabext') g.rootSym[m[1]] = { d: s.decimal, g: s.group, p: s.percentSign, pm: s.perMille, mi: s.minusSign, pl: s.plusSign, e: s.exponential, inf: s.infinity, ap: s.approximatelySign };
    }
  }
  // what ICU's root itself says for these two systems where the borrowed locale's symbols differ (found by formatting with a locale that has none)
  Object.assign(g.rootSym.arab, { e: '\u0627\u0633' });
  Object.assign(g.rootSym.arabext, { mi: '\u200e-\u200e', pl: '\u200e+\u200e', p: '\u066a' });
  // time zones: canonical ids, aliases (first name is canonical), metazone history
  const tz = rd('cldr-bcp47/bcp47/timezone.json').keyword.u.tz;
  g.tz = [];
  g.tzAlias = {};
  for (const [k, v] of Object.entries(tz)) {
    if (k.startsWith('_') || !v._alias) continue;
    const names = v._alias.split(' ');
    g.tz.push(names[0]);
    for (const a of names.slice(1)) g.tzAlias[a] = names[0];
    if (v._deprecated === 'true') g.tz.pop();
  }
  const mz = rd('cldr-core/supplemental/metaZones.json').supplemental.metaZones.metazoneInfo.timezone;
  g.mz = {};
  const walk = (o, prefix) => {
    for (const [k, v] of Object.entries(o)) {
      if (Array.isArray(v)) {
        g.mz[prefix + k] = v.map((e) => { const u = e.usesMetazone; return [u._mzone, u._from ? Date.parse(u._from.replace(' ', 'T') + ':00Z') : null, u._to ? Date.parse(u._to.replace(' ', 'T') + ':00Z') : null]; });
      } else walk(v, prefix + k + '/');
    }
  };
  walk(mz, '');
  // country of each canonical zone (two letters per zone, in the order of "tz"), the primary zone of countries with several, and the
  // zone each metazone's generic name stands for
  const ctx = zoneCtx();
  g.tzc = g.tz.map((z) => ctx.country[z] || '--').join('');
  g.pz = rd('cldr-core/supplemental/primaryZones.json').supplemental.primaryZones;
  g.mzg = {};
  for (const e of rd('cldr-core/supplemental/metaZones.json').supplemental.metaZones.metazones) {
    const m = e.mapZone;
    if (ctx.keep.has(m._other)) (g.mzg[m._other] = g.mzg[m._other] || {})[m._territory] = m._type;
  }
  // calendars: start dates of the Japanese eras, and month lengths of the Islamic calendars that are not arithmetic (Umm al-Qura and
  // the astronomical one, which also serves islamic-rgsa) for 1300..1600 AH. CLDR has no such tables: they are read off the ICU of
  // the Node that runs this generator (ICU 77 with CLDR 47 for Node 22), because that is what the golden tests compare with.
  const eras = rd('cldr-core/supplemental/calendarData.json').supplemental.calendarData.japanese.eras;
  g.jpe = Object.keys(eras).sort((a, b) => a - b).map((k) => eras[k]._start.split('-').map(Number));
  g.cal = { um: islamicTable('islamic-umalqura'), is: islamicTable('islamic') };
  // calendar names Intl.supportedValuesOf knows about, and collations
  g.currencies = Object.keys(rd('cldr-numbers-full/main/en/currencies.json').main.en.numbers.currencies);
  return g;
}

function islamicTable(cal) { // {s: day number of 1 Muharram 1300, m: 3 hex digits per year, bit 11 = month 1, set for a month of 30 days}
  const f = new Intl.DateTimeFormat('en-u-ca-' + cal, { timeZone: 'UTC', year: 'numeric', month: 'numeric', day: 'numeric' });
  const get = (day) => { const p = {}; for (const x of f.formatToParts(day * 86400000)) p[x.type] = +x.value; return [p.year, p.month, p.day]; };
  let day = Math.floor(Date.UTC(1882, 10, 1) / 86400000);
  for (;; day++) { const g = get(day); if (g[0] === 1300 && g[1] === 1 && g[2] === 1) break; if (day > 0) throw new Error('islamic table: no start'); }
  const s = day, months = [];
  let len = 0;
  for (;;) {
    const g = get(day);
    if (g[0] === 1601) break;
    if (g[2] === 1 && len) { months.push(len); len = 0; }
    len++; day++;
  }
  months.push(len);
  let m = '';
  for (let y = 0; y < 301; y++) { let v = 0; for (let i = 0; i < 12; i++) v = v * 2 + (months[y * 12 + i] === 30 ? 1 : 0); m += v.toString(16).padStart(3, '0'); }
  return { s, m };
}

function pluralData() {
  const out = { c: {}, o: {}, r: {} };
  for (const [kind, key] of [['c', 'cardinal'], ['o', 'ordinal']]) {
    const file = kind === 'c' ? 'plurals.json' : 'ordinals.json';
    const src = rd('cldr-core/supplemental/' + file).supplemental['plurals-type-' + key];
    for (const [lang, rules] of Object.entries(src)) {
      const r = {};
      for (const [k, v] of Object.entries(rules)) { const m = /^pluralRule-count-(\w+)$/.exec(k); if (m && m[1] !== 'other') r[m[1]] = v.split('@')[0].trim(); }
      out[kind][lang] = r;
    }
  }
  const pr = rd('cldr-core/supplemental/pluralRanges.json').supplemental.plurals;
  for (const [lang, rules] of Object.entries(pr)) {
    const r = {};
    for (const [k, v] of Object.entries(rules)) { const m = /^pluralRange-start-(\w+)-end-(\w+)$/.exec(k); if (m) r[m[1] + ':' + m[2]] = v; }
    out.r[lang] = r;
  }
  return out;
}

function write(name, obj) {
  const json = JSON.stringify(obj);
  const gz = zlib.gzipSync(Buffer.from(json), { level: 9 });
  fs.writeFileSync(path.join(OUT, name + '.json.gz'), gz);
  return [json.length, gz.length];
}

fs.mkdirSync(OUT, { recursive: true });
for (const f of fs.readdirSync(OUT)) if (f.endsWith('.json.gz')) fs.unlinkSync(path.join(OUT, f));
const full = {};
for (const tag of Object.keys(LOCALES)) full[tag] = perLocale(tag);
let totalRaw = 0, totalGz = 0;
for (const tag of Object.keys(LOCALES)) {
  const parent = LOCALES[tag];
  let data = full[tag];
  if (parent) data = Object.assign({ parent }, diff(full[parent], full[tag]) || {});
  data = clean(data);
  const [raw, gz] = write(tag, data);
  totalRaw += raw; totalGz += gz;
}
const [gr, gg] = write('global', globalData());
const [pr, pg] = write('plurals', pluralData());
console.log(`locales: ${Object.keys(LOCALES).length}, raw ${totalRaw} B, gzip ${totalGz} B; global ${gr}/${gg}; plurals ${pr}/${pg}`);
// the collation tables (ICU's root order, taken from the Node running this generator): coll.json.gz
{
  const [cr, cg] = require('./collgen')(OUT, { han: true });
  console.log(`coll ${cr}/${cg}`);
}
