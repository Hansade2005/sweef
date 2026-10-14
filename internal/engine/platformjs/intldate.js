// Intl.DateTimeFormat on CLDR data: the options become a skeleton, platformjs/intlpat.js (a port of ICU's
// DateTimePatternGenerator) turns it into a pattern, and the pattern is rendered field by field. Time zones come from the
// embedded IANA database (host intlZoneOffset), calendar arithmetic from platformjs/intlcal.js.
H.load('intlcore');
H.load('intlcal');
H.load('intlpat');
var P = H.pat;
var I = H.intl, C = H.cal, Locale = g.Intl.Locale, sw = g.__sw;
var SLOT = Symbol('DateTimeFormat');
function div(a, b) { return Math.floor(a / b); }
function mod(a, b) { return a - div(a, b) * b; }
var civilFromDays = C.civil, daysFromCivil = C.daysFromCivil;

// ---- time zones -----------------------------------------------------------------------------------------------------------------------------
var tzLower = null, localZone = null;
function tzTable() {
  if (tzLower) return tzLower;
  var G = I.G(); tzLower = Object.create(null);
  G.tz.forEach(function (n) { tzLower[n.toLowerCase()] = n; });
  Object.keys(G.tzAlias).forEach(function (a) { tzLower[a.toLowerCase()] = G.tzAlias[a]; });
  ['utc', 'gmt', 'etc/utc', 'etc/gmt', 'etc/uct', 'uct', 'zulu', 'etc/zulu', 'etc/universal', 'universal', 'etc/greenwich', 'greenwich', 'gmt0', 'etc/gmt0', 'gmt+0', 'gmt-0', 'etc/gmt+0', 'etc/gmt-0'].forEach(function (n) { tzLower[n] = 'UTC'; });
  return tzLower;
}
function canonicalZone(name) {
  var s = String(name), m = /^([+-])(\d{2})(?::?(\d{2}))?$/.exec(s);
  if (m) { var h = +m[2], mi = m[3] ? +m[3] : 0; if (h > 23 || mi > 59) throw new RangeError('Invalid time zone specified: ' + s); return (m[1]) + ('0' + h).slice(-2) + ':' + ('0' + mi).slice(-2); }
  var c = tzTable()[s.toLowerCase()];
  if (!c && sw.intlZoneValid(s) && /^[A-Za-z_]+(\/[A-Za-z0-9_+-]+)*$/.test(s)) c = s;
  if (!c) throw new RangeError('Invalid time zone specified: ' + s);
  return c;
}
function defaultZone() {
  if (!localZone) { try { localZone = canonicalZone(sw.intlLocalZone()); } catch (e) { localZone = 'UTC'; } }
  return localZone;
}
function zoneOffset(zone, ms) { // seconds east of UTC, and whether daylight saving time is in effect
  if (zone === 'UTC') return [0, false];
  if (zone.charAt(0) === '+' || zone.charAt(0) === '-') { var sg = zone.charAt(0) === '-' ? -1 : 1; return [sg * (+zone.slice(1, 3) * 3600 + +zone.slice(4, 6) * 60), false]; }
  return sw.intlZoneOffset(zone, ms) || [0, false];
}

// ---- fields of an instant -------------------------------------------------------------------------------------------------------------
function fieldsOf(ms, zone, cal) {
  var off = zoneOffset(zone, ms), local = ms + off[0] * 1000, days = div(local, 86400000), rest = local - days * 86400000;
  var f = C.fields(days, cal);
  f.wd = mod(days + 4, 7); f.h = div(rest, 3600000); f.mi = div(rest % 3600000, 60000); f.s = div(rest % 60000, 1000); f.ms = rest % 1000;
  f.off = off[0]; f.dst = off[1]; f.days = days; f.ms0 = ms;
  if (f.gy === undefined) f.gy = civilFromDays(days)[0];
  return f;
}

// ---- patterns ------------------------------------------------------------------------------------------------------------------------------------
var parsePattern = P.parse, patternString = P.str;
// the class of each pattern letter
var CLASS = { G: 'G', y: 'y', Y: 'y', u: 'y', U: 'y', r: 'y', Q: 'Q', q: 'Q', M: 'M', L: 'M', w: 'w', W: 'W', E: 'E', e: 'E', c: 'E', d: 'd', D: 'D', F: 'F', a: 'a', b: 'a', B: 'B', h: 'h', H: 'h', K: 'h', k: 'h', j: 'h', J: 'h', m: 'm', s: 's', S: 'S', A: 'A', z: 'z', Z: 'z', O: 'z', v: 'z', V: 'z', x: 'z', X: 'z' };

// ---- locale data accessors ----------------------------------------------------------------------------------------------------------------
// The data of a calendar: the locale's Gregorian data with what the calendar changes. Calendars are shipped for the locales that
// use them (and English); a locale without data for one shows the era names and patterns of English next to its own month names.
var CAL_KEY = { 'islamic-civil': 'islamic', 'islamic-tbla': 'islamic', 'islamic-umalqura': 'islamic', 'islamic-rgsa': 'islamic', ethioaa: 'ethiopic-amete-alem' };
var GREGORIAN_BASED = { buddhist: 1, japanese: 1, roc: 1 };
function calData(s) {
  if (s.calData2) return s.calData2;
  var key = CAL_KEY[s.calendar] || s.calendar, g0 = s.data.c.gregory;
  if (key === 'gregory' || key === 'iso8601') return (s.calData2 = g0);
  var own = s.data.c[key], cached = own && own.$m;
  if (own && g0 === s.data.c.gregory) {
    var m = own.$merged;
    if (!m) { m = I.merge(g0, own); if (own.af) m.af = own.af; if (own.iv) m.iv = own.iv; Object.defineProperty(own, '$merged', { value: m }); }
    return (s.calData2 = m);
  }
  var en = I.localeData('en'), eg = en.c.gregory, eo = en.c[key];
  if (!eo) return (s.calData2 = g0);
  if (GREGORIAN_BASED[key]) { // the locale's own month and day names, English eras, and an era next to the year
    var c = Object.assign({}, g0);
    c.er = eo.er || (eo.er = {});
    var addEra = function (p) { return /y/.test(p) && !/G/.test(p) ? p + ' G' : p; };
    c.df = {}; Object.keys(g0.df).forEach(function (k) { c.df[k] = addEra(g0.df[k]); });
    c.af = {}; Object.keys(g0.af).forEach(function (k) { c.af[k] = /y/.test(k) && !/G/.test(k) && /^[yMdEQ]+$/.test(k) ? addEra(g0.af[k]) : g0.af[k]; });
    return (s.calData2 = c);
  }
  var m2 = eo.$merged;
  if (!m2) { m2 = I.merge(eg, eo); if (eo.af) m2.af = eo.af; if (eo.iv) m2.iv = eo.iv; Object.defineProperty(eo, '$merged', { value: m2 }); }
  return (s.calData2 = m2);
}
function hourCycleDefault(tag, dataTag) {
  var G = I.G(), l = new Locale(tag), reg = l.region || new Locale(tag).maximize().region, lang = l.language, h = G.hc;
  var pref = h[lang + '-' + reg] || h[reg] || h['001'] || 'H';
  return { h: 'h12', H: 'h23', K: 'h11', k: 'h24' }[pref] || 'h23';
}

// ---- construction ----------------------------------------------------------------------------------------------------------------------------------
var TEXT = { weekday: ['narrow', 'short', 'long'], era: ['narrow', 'short', 'long'], year: ['2-digit', 'numeric'], month: ['2-digit', 'numeric', 'narrow', 'short', 'long'], day: ['2-digit', 'numeric'],
  dayPeriod: ['narrow', 'short', 'long'], hour: ['2-digit', 'numeric'], minute: ['2-digit', 'numeric'], second: ['2-digit', 'numeric'],
  timeZoneName: ['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric'] };
var FIELD_ORDER = ['weekday', 'era', 'year', 'month', 'day', 'dayPeriod', 'hour', 'minute', 'second', 'fractionalSecondDigits', 'timeZoneName'];
function initialize(dtf, locales, options, required, defaults) {
  var requested = I.canonList(locales);
  if (options === undefined) options = Object.create(null); else if (options === null) throw new TypeError('Cannot convert undefined or null to object'); else options = Object(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var calendar = options.calendar, nu = options.numberingSystem;
  if (calendar !== undefined) { calendar = String(calendar); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(calendar)) throw new RangeError('Invalid calendar : ' + calendar); calendar = calendar.toLowerCase(); }
  if (nu !== undefined) { nu = String(nu); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(nu)) throw new RangeError('Invalid numberingSystem : ' + nu); nu = nu.toLowerCase(); }
  var hour12 = options.hour12;
  if (hour12 !== undefined) hour12 = !!hour12;
  var hourCycle = I.getOption(options, 'hourCycle', 'string', ['h11', 'h12', 'h23', 'h24'], undefined);
  if (hour12 !== undefined) hourCycle = undefined;
  var r = I.resolveLocale(requested, ['ca', 'hc', 'nu'], { ca: calendar, hc: hourCycle, nu: nu });
  var data = I.localeData(r.base), dataTag = I.dataTag(r.base);
  var s = { locale: r.locale, base: r.base, data: data, dataTag: dataTag };
  // calendar
  var cal = r.ext.ca;
  if (cal === 'gregorian') cal = 'gregory';
  if (cal === 'islamicc') cal = 'islamic-civil';
  if (cal === 'ethiopic-amete-alem') cal = 'ethioaa';
  if (!cal || CALENDARS.indexOf(cal) < 0) cal = defaultCalendar(r.base);
  s.calendar = cal;
  s.jpan = cal === 'japanese' && new Locale(r.base).language === 'ja';
  s.nu = r.ext.nu && I.digitsOf(r.ext.nu) ? r.ext.nu : I.defaultNu(r.base, data);
  var tz = options.timeZone;
  s.timeZone = tz === undefined ? defaultZone() : canonicalZone(tz);
  var hcExt = r.ext.hc;
  // fields
  var f = {};
  FIELD_ORDER.forEach(function (name) {
    var v = options[name];
    if (name === 'fractionalSecondDigits') { if (v !== undefined) { v = Number(v); if (v !== v || v < 1 || v > 3) throw new RangeError('fractionalSecondDigits value is out of range.'); f[name] = Math.floor(v); } return; }
    if (v === undefined) return;
    v = String(v);
    if (TEXT[name].indexOf(v) < 0) throw new RangeError('Value ' + v + ' out of range for Intl.DateTimeFormat options property ' + name);
    f[name] = v;
  });
  I.getOption(options, 'formatMatcher', 'string', ['basic', 'best fit'], 'best fit');
  var dateStyle = I.getOption(options, 'dateStyle', 'string', ['full', 'long', 'medium', 'short'], undefined);
  var timeStyle = I.getOption(options, 'timeStyle', 'string', ['full', 'long', 'medium', 'short'], undefined);
  if (dateStyle !== undefined || timeStyle !== undefined) {
    FIELD_ORDER.forEach(function (name) { if (f[name] !== undefined) throw new TypeError("Can't set option " + name + ' when ' + (dateStyle !== undefined ? 'dateStyle' : 'timeStyle') + ' is used'); });
    if (required === 'date' && timeStyle !== undefined) throw new TypeError('Invalid option : timeStyle');
    if (required === 'time' && dateStyle !== undefined) throw new TypeError('Invalid option : dateStyle');
    s.dateStyle = dateStyle; s.timeStyle = timeStyle;
  } else {
    var need = true;
    if (required === 'date' || required === 'any') ['weekday', 'year', 'month', 'day'].forEach(function (k) { if (f[k] !== undefined) need = false; });
    if (required === 'time' || required === 'any') ['dayPeriod', 'hour', 'minute', 'second', 'fractionalSecondDigits'].forEach(function (k) { if (f[k] !== undefined) need = false; });
    if (need && (defaults === 'date' || defaults === 'all')) { f.year = 'numeric'; f.month = 'numeric'; f.day = 'numeric'; }
    if (need && (defaults === 'time' || defaults === 'all')) { f.hour = 'numeric'; f.minute = 'numeric'; f.second = 'numeric'; }
  }
  s.fields = f;
  // hour cycle
  var hasHour = f.hour !== undefined || s.timeStyle !== undefined;
  if (hasHour) {
    var dflt = hourCycleDefault(r.base, dataTag), hc;
    if (hour12 === true) hc = dflt === 'h11' ? 'h11' : 'h12';
    else if (hour12 === false) hc = dflt === 'h24' ? 'h24' : 'h23';
    else hc = hourCycle || hcExt || dflt;
    s.hourCycle = hc;
    s.hour12Opt = hour12;
    if (hour12 !== undefined || hourCycle !== undefined || hcExt !== undefined) s.hcExplicit = hc; // otherwise the skeleton says 'j' and the locale decides
  }
  var kept = [];
  if (r.kept.ca && r.ext.ca && (s.calendar === r.ext.ca || (r.ext.ca === 'gregorian' && s.calendar === 'gregory') || (r.ext.ca === 'islamicc' && s.calendar === 'islamic-civil') || (r.ext.ca === 'ethiopic-amete-alem' && s.calendar === 'ethioaa'))) kept.push('ca-' + r.ext.ca);
  if (r.kept.hc && hcExt && hcExt === s.hourCycle && hourCycle === undefined && hour12 === undefined) kept.push('hc-' + hcExt);
  if (r.kept.nu && r.ext.nu && s.nu === r.ext.nu) kept.push('nu-' + s.nu);
  s.locale = r.base + (kept.length ? '-u-' + kept.join('-') : '');
  resolvePattern(s);
  dtf[SLOT] = s;
}
var CALENDARS = ['gregory', 'iso8601', 'buddhist', 'japanese', 'roc', 'persian', 'islamic', 'islamic-civil', 'islamic-tbla', 'islamic-umalqura', 'islamic-rgsa', 'hebrew', 'indian', 'coptic', 'ethiopic', 'ethioaa'];
function defaultCalendar(tag) {
  var l = new Locale(tag);
  if (l.language === 'th') return 'buddhist';
  if (l.language === 'fa' && (!l.region || l.region === 'IR' || l.region === 'AF')) return 'persian';
  return 'gregory';
}

// ---- choosing the pattern ----------------------------------------------------------------------------------------------------------------
var HC_LETTER = { h12: 'h', h23: 'H', h11: 'K', h24: 'k' }, LETTER_HC = { h: 'h12', H: 'h23', K: 'h11', k: 'h24' };
// the date-time glue: ICU takes the "at time" patterns of the Gregorian calendar for every calendar
function glueOf(s) { var cd = calData(s); return cd.dtfAt || cd.dtf || s.data.c.gregory.dtf; }
function generator(s) {
  var cd = calData(s), sym = s.data.n.sym, rs = I.G().rootSym[s.nu];
  return P.gen(cd, s.data.r.dn, (sym[s.nu] || rs || sym[s.data.n.nu] || sym.latn).d, HC_LETTER[hourCycleDefault(s.base, s.dataTag)] || 'h', glueOf(s));
}
// V8's skeleton for the requested fields: weekday, era, year, month, day, day period, hour, minute, second, fraction, zone
function v8Skeleton(s) {
  var f = s.fields, sk = '', ch;
  if (f.weekday) sk += { long: 'EEEE', short: 'EEE', narrow: 'EEEEE' }[f.weekday];
  if (f.era) sk += { long: 'GGGG', short: 'GGG', narrow: 'GGGGG' }[f.era];
  if (f.year) sk += f.year === '2-digit' ? 'yy' : 'y';
  if (f.month) sk += { '2-digit': 'MM', numeric: 'M', narrow: 'MMMMM', short: 'MMM', long: 'MMMM' }[f.month];
  if (f.day) sk += f.day === '2-digit' ? 'dd' : 'd';
  var h24 = s.hcExplicit ? s.hcExplicit === 'h23' || s.hcExplicit === 'h24' : (generator(s).hourChar === 'H' || generator(s).hourChar === 'k');
  if (f.dayPeriod && !(f.hour && h24)) sk += { narrow: 'BBBBB', short: 'B', long: 'BBBB' }[f.dayPeriod]; // a 24-hour clock shows no day period
  if (f.hour) { ch = s.hcExplicit ? HC_LETTER[s.hcExplicit] : 'j'; sk += ch + (f.hour === '2-digit' ? ch : ''); }
  if (f.minute) sk += f.minute === '2-digit' ? 'mm' : 'm';
  if (f.second) sk += f.second === '2-digit' ? 'ss' : 's';
  if (f.fractionalSecondDigits) sk += 'SSS'.slice(0, f.fractionalSecondDigits);
  if (f.timeZoneName) sk += { short: 'z', long: 'zzzz', shortOffset: 'O', longOffset: 'OOOO', shortGeneric: 'v', longGeneric: 'vvvv' }[f.timeZoneName];
  return sk;
}
function neededPattern(s) {
  var sk = v8Skeleton(s);
  if (!sk) return [];
  var pat = generator(s).best(sk);
  if (s.hcExplicit && /[hHKk]/.test(pat)) pat = P.replaceHourPattern(pat, s.hcExplicit);
  return P.parse(flexibleToAmPm(s, pat));
}
// flexible day periods (B) appear only when the dayPeriod option asks for them; otherwise the plain AM/PM marker stands in
function flexibleToAmPm(s, pat) {
  if (s.fields.dayPeriod || !/B/.test(pat)) return pat;
  return P.str(P.parse(pat).map(function (t) { return t.l === 'B' ? { l: 'a', n: t.n } : t; }));
}
function hourCycleOf(toks) { var hc = null; toks.forEach(function (t) { if (t.l && LETTER_HC[t.l]) hc = LETTER_HC[t.l]; }); return hc; }
function stylePattern(s) { // dateStyle / timeStyle: the locale's own patterns, glued with the "at time" date-time pattern
  var cd = calData(s), dt = s.dateStyle && cd.df[s.dateStyle], tt = s.timeStyle && cd.tf[s.timeStyle], pat;
  if (dt && tt) {
    var glue = glueOf(s)[s.dateStyle];
    pat = glue.replace(/\{([01])\}/g, function (m, d) { return d === '0' ? tt : dt; });
  } else pat = dt || tt || '';
  // another hour cycle than the pattern's: the way V8 does it, a skeleton of the pattern with the new hour letter is matched again
  if (tt && s.hcExplicit && hourCycleOf(P.parse(pat)) !== s.hcExplicit) {
    pat = generator(s).best(P.replaceHourSkeleton(P.skeletonOf(pat), s.hcExplicit));
    pat = flexibleToAmPm(s, P.replaceHourPattern(pat, s.hcExplicit));
  }
  return P.parse(pat);
}
function resolvePattern(s) {
  s.tokens = s.dateStyle || s.timeStyle ? stylePattern(s) : neededPattern(s);
  // the options as the resolved pattern shows them (resolvedOptions): V8 looks for the letters of each option in the text of the pattern
  // (quoted text included, so a literal "de" counts as a day), the widest first
  var r = {}, text = P.str(s.tokens.filter(function (t) { return t.num === undefined; }));
  var real = s.tokens.filter(function (t) { return t.l && CLASS[t.l] === 'h'; })[0], hc = real ? { h: 'h12', H: 'h23', K: 'h11', k: 'h24' }[real.l] : null;
  var hourPairs = { h11: [['KK', '2-digit'], ['K', 'numeric']], h12: [['hh', '2-digit'], ['h', 'numeric']], h23: [['HH', '2-digit'], ['H', 'numeric']], h24: [['kk', '2-digit'], ['k', 'numeric']] }[hc || 'h12'];
  [['weekday', [['EEEEE', 'narrow'], ['EEEE', 'long'], ['EEE', 'short'], ['ccccc', 'narrow'], ['cccc', 'long'], ['ccc', 'short']]],
    ['era', [['GGGGG', 'narrow'], ['GGGG', 'long'], ['GGG', 'short']]],
    ['year', [['yy', '2-digit'], ['y', 'numeric']]],
    ['month', [['MMMMM', 'narrow'], ['MMMM', 'long'], ['MMM', 'short'], ['MM', '2-digit'], ['M', 'numeric'], ['LLLLL', 'narrow'], ['LLLL', 'long'], ['LLL', 'short'], ['LL', '2-digit'], ['L', 'numeric']]],
    ['day', [['dd', '2-digit'], ['d', 'numeric']]],
    ['dayPeriod', [['BBBBB', 'narrow'], ['bbbbb', 'narrow'], ['BBBB', 'long'], ['bbbb', 'long'], ['B', 'short'], ['b', 'short']]],
    ['hour', hourPairs],
    ['minute', [['mm', '2-digit'], ['m', 'numeric']]],
    ['second', [['ss', '2-digit'], ['s', 'numeric']]],
    ['fractionalSecondDigits', [['SSS', 3], ['SS', 2], ['S', 1]]],
    ['timeZoneName', [['zzzz', 'long'], ['z', 'short'], ['OOOO', 'longOffset'], ['O', 'shortOffset'], ['vvvv', 'longGeneric'], ['v', 'shortGeneric']]]].forEach(function (item) {
    for (var i = 0; i < item[1].length; i++) if (text.indexOf(item[1][i][0]) >= 0) { r[item[0]] = item[1][i][1]; break; }
  });
  s.resolved = r;
  s.hasMinute = s.tokens.some(function (t) { return t.l === 'm'; }); s.hasSecond = s.tokens.some(function (t) { return t.l === 's'; });
  // a numeric minute next to an hour is always two digits in the pattern; with no hour it follows the pattern too
  s.realHour = !!real;
  if (real) s.hourCycle = hc || s.hourCycle;
}

// ---- rendering --------------------------------------------------------------------------------------------------------------------------------------
function pad(n, w) { var s = String(Math.abs(n)); while (s.length < w) s = '0' + s; return s; }
// ICU's TimeZoneFormat localized GMT format: the locale's hourFormat gives the "hours and minutes" pattern, the "hours, minutes
// and seconds" and the "hours only" patterns are derived from it; the short form uses a one-digit hour and drops zero minutes
var offPat = Object.create(null);
function offsetPatterns(hf) { // [positive HM, HMS, H, negative HM, HMS, H] as token lists
  if (offPat[hf]) return offPat[hf];
  var sides = hf.split(';'), out = [];
  [sides[0], sides[1] || '-' + sides[0].slice(1)].forEach(function (hm) {
    var mm = hm.indexOf('mm'), hIdx = hm.slice(0, mm).lastIndexOf('H'), sep = hm.slice(hIdx + 1, mm);
    var hms = hm.slice(0, mm + 2) + sep + 'ss' + hm.slice(mm + 2), h = hm.slice(0, hIdx + 1);
    [hm, hms, h].forEach(function (p) {
      var toks = [], lit = '', i = 0;
      while (i < p.length) {
        var c = p[i];
        if (c === 'H' || (c === 'm' && p[i + 1] === 'm') || (c === 's' && p[i + 1] === 's')) {
          if (lit) { toks.push({ lit: lit }); lit = ''; }
          var k = i; while (p[k] === c) k++;
          toks.push({ f: c }); i = k;
        } else { lit += c; i++; }
      }
      if (lit) toks.push({ lit: lit });
      out.push(toks);
    });
  });
  return (offPat[hf] = out);
}
function gmtOffsetText(s, offSec, long) {
  var z = s.data.z;
  if (offSec === 0) return z.gz || 'GMT';
  var neg = offSec < 0, a = Math.abs(offSec), h = div(a, 3600), m = div(a % 3600, 60), sec = a % 60;
  var pats = offsetPatterns(z.hf || '+HH:mm;-HH:mm'), base = neg ? 3 : 0;
  var toks = pats[base + (sec ? 1 : (m || long) ? 0 : 2)], text = '';
  toks.forEach(function (t) {
    if (t.lit !== undefined) text += t.lit;
    else if (t.f === 'H') text += pad(h, long ? 2 : 1);
    else if (t.f === 'm') text += pad(m, 2);
    else text += pad(sec, 2);
  });
  return (z.gmt || 'GMT{0}').replace('{0}', function () { return text; }).replace(/[0-9]/g, function (d) { return I.localDigits(d, s.nu); });
}
function metazoneAt(zone, ms) {
  var list = I.G().mz[zone];
  if (!list) return null;
  // (ICU's metazone mapping starts at 1970-01-01 where CLDR gives no start: earlier instants have no metazone names)
  for (var i = 0; i < list.length; i++) { var e = list[i]; if (ms >= (e[1] === null ? 0 : e[1]) && (e[2] === null || ms < e[2])) return e[0]; }
  return null;
}
// ICU's time zone names: specific ("Pacific Daylight Time", "PDT"), generic non-location ("Pacific Time"), generic location
// ("France Time"), and the localized GMT format when a locale has no name
var zoneIndex = null;
function zoneCountry(zone) { // the country of a canonical zone, and whether it is the one the country's name stands for
  var G = I.G();
  if (!zoneIndex) {
    zoneIndex = { cc: Object.create(null), n: Object.create(null) };
    G.tz.forEach(function (z, i) { var c = G.tzc.substr(i * 2, 2); if (c !== '--') { zoneIndex.cc[z] = c; zoneIndex.n[c] = (zoneIndex.n[c] || 0) + 1; } });
  }
  var c = zoneIndex.cc[zone];
  return c ? { cc: c, primary: G.pz[c] === zone || zoneIndex.n[c] === 1 } : null;
}
function namesOf(z, mz, zone, long) { // [generic, standard, daylight] of a zone (its own names first) or of its metazone
  var own = z.zo && z.zo[zone] && z.zo[zone][long ? 'l' : 's'], m = z.mz && mz && z.mz[mz] && z.mz[mz][long ? 'l' : 's'];
  return [own && own[0] || m && m[0] || '', own && own[1] || m && m[1] || '', own && own[2] || m && m[2] || ''];
}
function exemplarCity(z, zone) { return (z.zn && z.zn[zone]) || zone.split('/').pop().replace(/_/g, ' '); }
function locationName(s, zone) {
  var z = s.data.z, c = zoneCountry(zone);
  if (!c) return '';
  var where = c.primary ? (s.data.dn.rg[c.cc] || c.cc) : exemplarCity(z, zone);
  return (z.rf || '{0} Time').replace('{0}', function () { return where; });
}
function observesDst(zone, ms) { // daylight saving time within 184 days before or after (ICU looks for the next and previous transitions)
  for (var d = 15; d <= 184; d += 15) if (zoneOffset(zone, ms + d * 86400000)[1] || zoneOffset(zone, ms - d * 86400000)[1]) return true;
  return false;
}
function genericName(s, f, long) {
  var z = s.data.z, zone = s.timeZone, ms = f.ms0, mz = metazoneAt(zone, ms), n = namesOf(z, mz, zone, long), name = '';
  if (z.zo && z.zo[zone] && z.zo[zone][long ? 'l' : 's'] && z.zo[zone][long ? 'l' : 's'][0]) return z.zo[zone][long ? 'l' : 's'][0];
  if (mz) {
    if (!f.dst && !observesDst(zone, ms) && n[1] && n[1].toLowerCase() !== n[0].toLowerCase()) name = n[1]; // a zone without daylight saving time shows its standard name
    if (!name && n[0]) {
      var G = I.G(), c = zoneCountry(zone), golden = G.mzg[mz] && (G.mzg[mz][c && c.cc] || G.mzg[mz]['001']);
      if (golden && golden !== zone) {
        var go = zoneOffset(golden, ms);
        if (go[0] === f.off && go[1] === f.dst) name = n[0];
        else { // another zone with the same metazone: "Pacific Time (Canada)"
          var where = c && c.primary ? (s.data.dn.rg[c.cc] || c.cc) : exemplarCity(z, zone);
          name = (z.ff || '{1} ({0})').replace('{0}', function () { return where; }).replace('{1}', function () { return n[0]; });
        }
      } else name = n[0];
    }
  }
  return name || locationName(s, zone);
}
function specificName(s, f, long) {
  var z = s.data.z, n = namesOf(z, metazoneAt(s.timeZone, f.ms0), s.timeZone === 'UTC' ? 'Etc/UTC' : s.timeZone, long);
  return f.dst ? n[2] : n[1];
}
function zoneText(s, f, letter, count) {
  var zone = s.timeZone;
  var isOffsetZone = zone.charAt(0) === '+' || zone.charAt(0) === '-';
  if (letter === 'O') return gmtOffsetText(s, f.off, count >= 4);
  if (letter === 'V') return zone;
  if (letter === 'Z' || letter === 'x' || letter === 'X') {
    var a = Math.abs(f.off), sg = f.off < 0 ? '-' : '+', hh = pad(div(a, 3600), 2), mm = pad(div(a % 3600, 60), 2);
    if (letter === 'X' && f.off === 0) return 'Z';
    return count >= 4 && letter !== 'x' && letter !== 'X' ? gmtOffsetText(s, f.off, true) : sg + hh + (count === 1 && letter !== 'Z' ? '' : (count === 3 || letter === 'Z' && count === 5 ? ':' : '') + mm);
  }
  var generic = letter === 'v', long = count >= 4;
  if (isOffsetZone) return gmtOffsetText(s, f.off, long);
  if (zone === 'UTC') { var un = generic ? '' : specificName(s, f, long); return un || (generic ? gmtOffsetText(s, 0, long) : 'UTC'); }
  return (generic ? genericName(s, f, long) : specificName(s, f, long)) || gmtOffsetText(s, f.off, long);
}
function dayPeriodName(s, f, letter, count) {
  // ICU formats the plain AM/PM marker (pattern letter a, up to four letters) with the wide names: Korean shows "오전", not "AM"
  var cd = calData(s), w = count >= 5 ? 'n' : (count === 4 || letter === 'a') ? 'w' : 'a', dp = cd.dp['f' + w] || cd.dp.fa || {};
  var h = f.h, m = f.mi;
  if (letter === 'B') {
    // ICU: noon only on the full hour (minutes and seconds count only when the pattern shows them), midnight is not used
    var G = I.G(), lang = new Locale(s.base).language, rules = G.dpr[lang] || G.dpr[lang.split('-')[0]];
    var mm = s.hasMinute ? m : 0, ss = s.hasSecond ? f.s : 0, t = h * 60;
    if (rules) {
      if (h === 12 && mm === 0 && ss === 0 && rules.noon && dp.noon) return dp.noon;
      for (var nm in rules) {
        var rr = rules[nm];
        if (rr.length !== 2) continue;
        var inRange = rr[0] <= rr[1] ? t >= rr[0] && t < rr[1] : (t >= rr[0] || t < rr[1]);
        if (rr[1] === 1440 && t >= rr[0]) inRange = true;
        if (inRange && dp[nm]) return dp[nm];
      }
    }
  } else if (letter === 'b') {
    if (h === 12 && m === 0 && f.s === 0 && dp.noon) return dp.noon;
    if (h === 0 && m === 0 && f.s === 0 && dp.midnight) return dp.midnight;
  }
  return h < 12 ? dp.am : dp.pm;
}
var HEB_ONES = ['', '\u05d0', '\u05d1', '\u05d2', '\u05d3', '\u05d4', '\u05d5', '\u05d6', '\u05d7', '\u05d8'], HEB_TENS = ['', '\u05d9', '\u05db', '\u05dc', '\u05de', '\u05e0', '\u05e1', '\u05e2', '\u05e4', '\u05e6'], HEB_HUNDREDS = ['', '\u05e7', '\u05e8', '\u05e9', '\u05ea'];
function hebrewNumeral(n) { // Hebrew alphabetic numerals; the thousands of a year are left out (5785 is \u05ea\u05e9\u05e4\u05f4\u05d4)
  n %= 1000;
  if (n === 698) return '\u05ea\u05e8\u05d7\u05f4\u05e6'; // (two years ICU spells differently)
  if (n === 744) return '\u05ea\u05e9\u05d3\u05f4\u05de';
  var out = '';
  while (n >= 400) { out += HEB_HUNDREDS[4]; n -= 400; }
  if (n >= 100) { out += HEB_HUNDREDS[div(n, 100)]; n %= 100; }
  if (n === 15) out += '\u05d8\u05d5'; else if (n === 16) out += '\u05d8\u05d6'; else out += HEB_TENS[div(n, 10)] + HEB_ONES[n % 10];
  if (out.length === 1) return out + '\u05f3';
  var last = out.slice(-1);
  return out.slice(0, -1) + '\u05f4' + (last === '\u05e4' ? '\u05e3' : last); // (a closing pe takes its final form)
}
var HANIDAYS = '\u521d\u4e00 \u521d\u4e8c \u521d\u4e09 \u521d\u56db \u521d\u4e94 \u521d\u516d \u521d\u4e03 \u521d\u516b \u521d\u4e5d \u521d\u5341 \u5341\u4e00 \u5341\u4e8c \u5341\u4e09 \u5341\u56db \u5341\u4e94 \u5341\u516d \u5341\u4e03 \u5341\u516b \u5341\u4e5d \u4e8c\u5341 \u5eff\u4e00 \u5eff\u4e8c \u5eff\u4e09 \u5eff\u56db \u5eff\u4e94 \u5eff\u516d \u5eff\u4e03 \u5eff\u516b \u5eff\u4e5d \u4e09\u5341'.split(' ');
var DATE_NUMERIC = 'yuYrdMLQqDFwWec';
// the numbering system a pattern asks for a field in ("pattern@numbers=hebr"): only date fields are affected, not the time
function numberingOverride(s, tokens, letter) {
  var t = tokens[tokens.length - 1];
  if (s.jpan && letter === 'y' && !(t && t.num !== undefined)) return 'jpanyear'; // Japanese counts years of an era without padding, and the first is 元
  if (!t || t.num === undefined || DATE_NUMERIC.indexOf(letter) < 0) return null;
  if (t.num.indexOf('=') < 0) return t.num;
  var found = null;
  t.num.split(';').forEach(function (p) { var kv = p.split('='); if (kv[0] === letter) found = kv[1]; });
  return found;
}
function overridden(s, nu, value, width) { // a number in a numbering system a pattern chose
  switch (nu) {
    case 'hebr': return hebrewNumeral(value);
    case 'jpanyear': return value === 1 ? '\u5143' : I.localDigits(String(value), s.nu);
    case 'hanidays': return HANIDAYS[value - 1] || String(value);
  }
  return I.localDigits(pad(value, width), nu);
}
function renderTokens(s, tokens, f) {
  var cd = calData(s), parts = [], ds = function (str) { return I.localDigits(str, s.nu); };
  function push(type, value) { parts.push({ type: type, value: value }); }
  tokens.forEach(function (t, ti) {
    if (t.num !== undefined) return;
    if (t.lit !== undefined) { if (parts.length && parts[parts.length - 1].type === 'literal') parts[parts.length - 1].value += t.lit; else push('literal', t.lit); return; }
    var n = t.n, v, ov = numberingOverride(s, tokens, t.l);
    switch (t.l) {
      case 'G': {
        var ew = n >= 5 ? 'n' : n === 4 ? 'w' : 'a', er = cd.er && cd.er[ew], en = er && er[f.era];
        if (!en && s.calendar === 'japanese') { var ec = I.localeData('en').c.japanese; en = ec && ec.er && ec.er[ew] && ec.er[ew][f.era]; } // (the eras before Meiji are shipped for English and Japanese)
        push('era', en || ''); break;
      }
      case 'y': case 'u': case 'Y': case 'r':
        v = t.l === 'u' || t.l === 'r' ? (f.gy !== undefined && (s.calendar === 'gregory' || t.l === 'r') ? f.gy : f.y) : f.y;
        if (ov) { push('year', overridden(s, ov, n === 2 ? mod(v, 100) : v, n)); break; }
        if (n === 2) push('year', ds(pad(mod(v, 100), 2))); else push('year', ds(pad(v, n))); break;
      case 'Q': case 'q':
        v = div(f.m - 1, 3) + 1;
        if (n <= 2) push('literal', ds(pad(v, n))); else { var qn = (cd.qu[(t.l === 'q' ? 's' : 'f') + (n === 3 ? 'a' : n === 4 ? 'w' : 'n')] || cd.qu.fa || []); push('literal', qn[v - 1] || String(v)); }
        break;
      case 'M': case 'L':
        if (n <= 2 && ov) push('month', overridden(s, ov, f.m, n));
        else if (n <= 2) push('month', ds(pad(s.calendar === 'hebrew' && !f.ly && f.m >= 7 ? f.m - 1 : f.m, n))); // (ICU counts a Hebrew year without Adar I as having 12 months)
        else {
          var mk = (t.l === 'L' ? 's' : 'f') + (n === 3 ? 'a' : n === 4 ? 'w' : 'n'), arr = cd.mo[mk] || cd.mo['f' + (n === 3 ? 'a' : n === 4 ? 'w' : 'n')] || cd.mo.fw;
          push('month', f.ly && f.m === 7 && cd.ml && (cd.ml[mk] || cd.ml.fw) || arr[f.m - 1]); // (Hebrew: the seventh month of a leap year is Adar II)
        }
        break;
      case 'd': push('day', ov ? overridden(s, ov, f.d, n) : ds(pad(f.d, n))); break;
      case 'D': push('literal', ds(pad(div(f.days - daysFromCivil(f.gy, 1, 1), 1) + 1, n))); break;
      case 'E': case 'e': case 'c':
        if ((t.l === 'e' || t.l === 'c') && n <= 2) push('weekday', ds(pad(f.wd + 1, n)));
        else { var wk = (t.l === 'c' ? 's' : 'f') + (n >= 6 ? 's' : n === 5 ? 'n' : n === 4 ? 'w' : 'a'); var da = cd.da[wk] || cd.da['f' + wk.slice(1)] || cd.da.fa; push('weekday', da[f.wd]); }
        break;
      case 'a': case 'b': case 'B': push('dayPeriod', dayPeriodName(s, f, t.l, n)); break;
      case 'h': v = f.h % 12 || 12; push('hour', ds(pad(v, n))); break;
      case 'H': push('hour', ds(pad(f.h, n))); break;
      case 'K': push('hour', ds(pad(f.h % 12, n))); break;
      case 'k': push('hour', ds(pad(f.h || 24, n))); break;
      case 'm': push('minute', ds(pad(f.mi, n))); break;
      case 's': push('second', ds(pad(f.s, n))); break;
      case 'S': push('fractionalSecond', ds(pad(f.ms, 3).slice(0, n))); break;
      case 'z': case 'v': case 'O': case 'V': case 'Z': case 'x': case 'X': push('timeZoneName', zoneText(s, f, t.l, n)); break;
      case 'w': push('literal', ds(String(weekOfYear(f)))); break;
      case 'W': push('literal', ds(String(div(f.d - 1 + mod(f.wd - f.d + 1, 7), 7) + 1))); break;
      default: push('literal', '');
    }
  });
  // hour/minute/second parts adjacent: keep as-is. Day period parts of numeric patterns are marked literal-free.
  return parts;
}
function weekOfYear(f) { // ISO-like week number, Monday first, 4 days minimum (en-GB/ISO); good enough for the rare w/W patterns
  var jan1 = daysFromCivil(f.gy, 1, 1), doy = f.days - jan1, wd = mod(f.wd + 6, 7);
  var w = div(doy - wd + 10, 7);
  return w < 1 ? 52 : w;
}

// ---- values -----------------------------------------------------------------------------------------------------------------------------------------
function timeValue(x) {
  var t;
  if (x === undefined) t = Date.now();
  else if (x instanceof Date) t = x.getTime();
  else t = Number(x);
  if (t !== t || Math.abs(t) > 8.64e15) throw new RangeError('Invalid time value');
  return Math.trunc(t);
}
function formatParts(s, x) { var t = timeValue(x); return renderTokens(s, s.tokens, fieldsOf(t, s.timeZone, s.calendar)); }
function slot(o, m) {
  var s = o !== null && typeof o === 'object' ? o[SLOT] : undefined;
  if (!s) throw new TypeError('Method Intl.DateTimeFormat.prototype.' + m + ' called on incompatible receiver ' + (o === null ? 'null' : typeof o === 'object' ? '#<Object>' : String(o)));
  return s;
}
function joined(parts) { var out = ''; for (var i = 0; i < parts.length; i++) out += parts[i].value; return out; }

// ---- ranges --------------------------------------------------------------------------------------------------------------------------------------------
H.dt = { single: function (s, f) { return renderTokens(s, s.tokens, f).map(function (p) { return { type: p.type, value: p.value, source: 'shared' }; }); }, calData: calData, generator: generator, renderTokens: renderTokens, timeValue: timeValue, fieldsOf: fieldsOf };
function rangeParts(s, a, b) { H.load('intlrange'); return H.range.parts(s, a, b); }

// ---- the class ------------------------------------------------------------------------------------------------------------------------------------------
function DateTimeFormat(locales, options) {
  var self = this instanceof DateTimeFormat ? this : Object.create(DateTimeFormat.prototype);
  initialize(self, locales, options, 'any', 'date');
  return self;
}
H.attrs(DateTimeFormat.prototype, {
  format: function () {
    var s = slot(this, 'format');
    if (!s.bound) { s.bound = function (d) { return joined(formatParts(s, d)); }; Object.defineProperty(s.bound, 'name', { value: '', configurable: true }); }
    return s.bound;
  }
});
H.define(DateTimeFormat.prototype, 'formatToParts', function formatToParts(d) { return formatParts(slot(this, 'formatToParts'), d); });
H.define(DateTimeFormat.prototype, 'formatRange', function formatRange(a, b) {
  var s = slot(this, 'formatRange');
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('Invalid time value');
  return joined(rangeParts(s, a, b));
});
H.define(DateTimeFormat.prototype, 'formatRangeToParts', function formatRangeToParts(a, b) {
  var s = slot(this, 'formatRangeToParts');
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('Invalid time value');
  return rangeParts(s, a, b);
});
H.define(DateTimeFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slot(this, 'resolvedOptions'), o = { locale: s.locale, calendar: s.calendar, numberingSystem: s.nu, timeZone: s.timeZone };
  if (s.hourCycle && (s.realHour || s.timeStyle)) { o.hourCycle = s.hourCycle; o.hour12 = s.hourCycle === 'h11' || s.hourCycle === 'h12'; }
  var r = s.resolved;
  if (!s.dateStyle && !s.timeStyle) FIELD_ORDER.forEach(function (k) { if (r[k] !== undefined) o[k] = r[k]; });
  if (s.dateStyle) o.dateStyle = s.dateStyle;
  if (s.timeStyle) o.timeStyle = s.timeStyle;
  return o;
});
Object.defineProperty(DateTimeFormat.prototype, Symbol.toStringTag, { value: 'Intl.DateTimeFormat', configurable: true });
H.define(DateTimeFormat, 'supportedLocalesOf', function supportedLocalesOf(locales, options) { return I.supportedLocalesOf(locales, options); });
Object.defineProperty(DateTimeFormat, 'length', { value: 0, configurable: true });
H.define(g.Intl, 'DateTimeFormat', DateTimeFormat);
