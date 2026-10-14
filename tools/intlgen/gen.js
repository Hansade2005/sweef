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
const MZ_LOCALES = new Set(['en', 'en-001', 'en-GB', 'en-AU', 'en-IN', 'en-CA', 'de', 'de-AT', 'de-CH', 'fr', 'fr-CA', 'es', 'es-419', 'es-MX', 'es-US', 'it', 'pt', 'pt-PT', 'ru', 'ja', 'zh', 'zh-Hant', 'zh-Hant-HK', 'ko', 'nl', 'pl', 'tr', 'ar', 'ar-AE', 'sv']); // metazone names (Pacific Standard Time): the rest use GMT offsets

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
function altAscii(obj) { // CLDR 44+ keeps U+202F in some time patterns and offers an ASCII alternative; Chromium ships the ASCII one
  const out = {};
  for (const k of Object.keys(obj)) if (!k.includes('-alt-')) out[k] = obj[k];
  for (const k of Object.keys(obj)) if (k.endsWith('-alt-ascii')) out[k.slice(0, -10)] = obj[k];
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
  const df = n['decimalFormats-numberSystem-' + nu] || n['decimalFormats-numberSystem-latn'];
  const cf = n['currencyFormats-numberSystem-' + nu] || n['currencyFormats-numberSystem-latn'];
  const pf = n['percentFormats-numberSystem-' + nu] || n['percentFormats-numberSystem-latn'];
  const sf = n['scientificFormats-numberSystem-' + nu] || n['scientificFormats-numberSystem-latn'];
  out.dec = df.standard; out.pct = pf.standard; out.sci = sf.standard;
  out.cur = cf.standard; out.acc = cf.accounting;
  if (cf['standard-alphaNextToNumber']) out.curA = cf['standard-alphaNextToNumber'];
  if (cf['accounting-alphaNextToNumber']) out.accA = cf['accounting-alphaNextToNumber'];
  const compact = (src) => {
    const o = {};
    for (const k of Object.keys(src)) {
      const m = /^(\d+)-count-(\w+)$/.exec(k);
      if (!m) continue;
      (o[m[1]] = o[m[1]] || {})[m[2]] = src[k];
    }
    return o;
  };
  out.cpd = { s: compact(df.short.decimalFormat), l: compact(df.long.decimalFormat) };
  const cs = cf.short && cf.short.standard ? cf.short.standard : null;
  if (cs) {
    out.cpc = compact(cs);
    const alt = {};
    for (const k of Object.keys(cs)) { const m = /^(\d+)-count-(\w+)-alt-alphaNextToNumber$/.exec(k); if (m) (alt[m[1]] = alt[m[1]] || {})[m[2]] = cs[k]; }
    if (Object.keys(alt).length) out.cpcA = alt;
  }
  out.cup = counts(cf, 'unitPattern'); // currencyDisplay: "name" -> "{0} {1}"
  const mp = n['miscPatterns-numberSystem-' + nu] || n['miscPatterns-numberSystem-latn'];
  if (mp) out.mp = { ap: mp.approximately, al: mp.atLeast, am: mp.atMost, r: mp.range };
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
      const d = f[(nm === 'weekOfYear' ? 'week' : nm === 'timeZoneName' ? 'zone' : nm) + suffix];
      if (d && d.displayName) (out.dn = out.dn || {})[nm + ':' + w] = d.displayName;
    }
  }
  return out;
}

function calendar(tag, cal) {
  const id = cldrId(tag);
  const file = cal === 'gregory' ? `cldr-dates-full/main/${id}/ca-gregorian.json` : `cldr-cal-${cal}-full/main/${id}/ca-${cal}.json`;
  if (!has(file)) return null;
  const j = rd(file).main[id].dates.calendars[cal === 'gregory' ? 'gregorian' : cal];
  const arr = (o, n, start) => { const a = []; for (let i = start; i < start + n; i++) a.push(o[i]); return a; };
  const dayKeys = ['sun', 'mon', 'tue', 'wed', 'thu', 'fri', 'sat'];
  const out = {};
  const ctx = { f: 'format', s: 'stand-alone' }, wd = { w: 'wide', a: 'abbreviated', n: 'narrow', s: 'short' };
  out.mo = {}; out.da = {}; out.qu = {}; out.dp = {};
  for (const c of Object.keys(ctx)) {
    for (const w of ['w', 'a', 'n']) {
      const m = j.months && j.months[ctx[c]] && j.months[ctx[c]][wd[w]]; if (m) out.mo[c + w] = arr(m, 12, 1);
      const q = j.quarters && j.quarters[ctx[c]] && j.quarters[ctx[c]][wd[w]]; if (q) out.qu[c + w] = arr(q, 4, 1);
      const p = j.dayPeriods && j.dayPeriods[ctx[c]] && j.dayPeriods[ctx[c]][wd[w]]; if (p) out.dp[c + w] = Object.fromEntries(Object.entries(p).filter(([k]) => !k.includes('-alt-')));
    }
    for (const w of ['w', 'a', 'n', 's']) {
      const d = j.days && j.days[ctx[c]] && j.days[ctx[c]][wd[w]]; if (d) out.da[c + w] = dayKeys.map((k) => d[k]);
    }
  }
  const era = (o) => o ? Object.fromEntries(Object.entries(o).filter(([k]) => /^\d$/.test(k))) : {};
  if (j.eras) out.er = { w: era(j.eras.eraNames), a: era(j.eras.eraAbbr), n: era(j.eras.eraNarrow) };
  out.df = altAscii(j.dateFormats || {}); out.tf = altAscii(j.timeFormats || {});
  const dtf = j.dateTimeFormats || {};
  out.dtf = { full: dtf.full, long: dtf.long, medium: dtf.medium, short: dtf.short };
  const at = j['dateTimeFormats-atTime'] && j['dateTimeFormats-atTime'].standard;
  if (at) out.dtfAt = { full: at.full, long: at.long, medium: at.medium, short: at.short };
  const af = altAscii(dtf.availableFormats || {});
  out.af = {};
  for (const k of Object.keys(af)) if (!/-count-/.test(k)) out.af[k] = af[k];
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

function zoneNames(tag) {
  const id = cldrId(tag), z = rd(`cldr-dates-full/main/${id}/timeZoneNames.json`).main[id].dates.timeZoneNames;
  const out = { hf: z.hourFormat, gmt: z.gmtFormat, gz: z.gmtZeroFormat, rf: z.regionFormat, rfd: z['regionFormat-type-daylight'], rfs: z['regionFormat-type-standard'], ff: z.fallbackFormat };
  if (MZ_LOCALES.has(tag)) {
    out.mz = {};
    for (const [k, v] of Object.entries(z.metazone || {})) {
      const rec = {};
      for (const w of ['long', 'short']) if (v[w]) rec[w[0]] = [v[w].generic || '', v[w].standard || '', v[w].daylight || ''];
      out.mz[k] = rec;
    }
    if (tag === 'en') {
      out.zn = {}; // exemplar cities (English only)
      for (const [region, zones] of Object.entries(z.zone || {})) for (const [name, v] of Object.entries(zones)) {
        if (v.exemplarCity) out.zn[region + '/' + name] = v.exemplarCity;
        for (const [sub, v2] of Object.entries(v)) if (v2 && typeof v2 === 'object' && v2.exemplarCity) out.zn[region + '/' + name + '/' + sub] = v2.exemplarCity;
      }
    }
  }
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

function perLocale(tag) {
  const d = { n: numbers(tag), u: units(tag), r: dateFields(tag), l: lists(tag), z: zoneNames(tag), dn: names(tag), c: {} };
  for (const cal of ['gregory', 'buddhist', 'persian']) {
    if (cal === 'buddhist' && !['th'].includes(tag)) continue;
    if (cal === 'persian' && !['fa'].includes(tag)) continue;
    const c = calendar(tag, cal); if (c) d.c[cal] = c;
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
  // calendar names Intl.supportedValuesOf knows about, and collations
  g.currencies = Object.keys(rd('cldr-numbers-full/main/en/currencies.json').main.en.numbers.currencies);
  return g;
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
