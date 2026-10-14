// Intl.DateTimeFormat on CLDR data: options become a skeleton, the skeleton is matched against the locale's
// available formats (the algorithm of ICU's DateTimePatternGenerator, simplified), and the chosen pattern is
// rendered field by field. Time zones come from the embedded IANA database (host intlZoneOffset); calendars:
// gregory, buddhist (th) and persian (fa).
H.load('intlcore');
var I = H.intl, Locale = g.Intl.Locale, sw = g.__sw;
var SLOT = Symbol('DateTimeFormat');

// ---- calendar arithmetic ---------------------------------------------------------------------------------------------------------------
function div(a, b) { return Math.floor(a / b); }
function mod(a, b) { return a - div(a, b) * b; }
function civilFromDays(z) {
  z += 719468;
  var era = div(z, 146097), doe = z - era * 146097, yoe = div(doe - div(doe, 1460) + div(doe, 36524) - div(doe, 146096), 365),
    y = yoe + era * 400, doy = doe - (365 * yoe + div(yoe, 4) - div(yoe, 100)), mp = div(5 * doy + 2, 153), d = doy - div(153 * mp + 2, 5) + 1, m = mp < 10 ? mp + 3 : mp - 9;
  return [m <= 2 ? y + 1 : y, m, d];
}
function daysFromCivil(y, m, d) {
  y -= m <= 2 ? 1 : 0;
  var era = div(y, 400), yoe = y - era * 400, doy = div(153 * (m + (m > 2 ? -3 : 9)) + 2, 5) + d - 1, doe = yoe * 365 + div(yoe, 4) - div(yoe, 100) + doy;
  return era * 146097 + doe - 719468;
}
// Jalali (persian) calendar, after jalaali-js
var JBREAKS = [-61, 9, 38, 199, 426, 686, 756, 818, 1111, 1181, 1210, 1635, 2060, 2097, 2192, 2262, 2324, 2394, 2456, 3178];
function jalCal(jy) {
  var bl = JBREAKS.length, gy = jy + 621, leapJ = -14, jp = JBREAKS[0], jm, jump = 0, leap, leapG, march, n, i;
  for (i = 1; i < bl; i++) { jm = JBREAKS[i]; jump = jm - jp; if (jy < jm) break; leapJ += div(jump, 33) * 8 + div(mod(jump, 33), 4); jp = jm; }
  n = jy - jp;
  leapJ += div(n, 33) * 8 + div(mod(n, 33) + 3, 4);
  if (mod(jump, 33) === 4 && jump - n === 4) leapJ += 1;
  leapG = div(gy, 4) - div((div(gy, 100) + 1) * 3, 4) - 150;
  march = 20 + leapJ - leapG;
  if (jump - n < 6) n = n - jump + div(jump + 4, 33) * 33;
  leap = mod(mod(n + 1, 33) - 1, 4);
  if (leap === -1) leap = 4;
  return { leap: leap, gy: gy, march: march };
}
function g2d(gy, gm, gd) {
  var d = div((gy + div(gm - 8, 6) + 100100) * 1461, 4) + div(153 * mod(gm + 9, 12) + 2, 5) + gd - 34840408;
  return d - div(div(gy + 100100 + div(gm - 8, 6), 100) * 3, 4) + 752;
}
function persianFromDays(days, gy) {
  var jdn = days + 2440588, jy = gy - 621, r = jalCal(jy), jdn1f = g2d(r.gy, 3, r.march), k = jdn - jdn1f, jm, jd;
  if (k >= 0) { if (k <= 185) return [jy, 1 + div(k, 31), mod(k, 31) + 1]; k -= 186; }
  else { jy -= 1; k += 179; if (r.leap === 1) k += 1; }
  jm = 7 + div(k, 30); jd = mod(k, 30) + 1;
  return [jy, jm, jd];
}

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
  var c = civilFromDays(days), f = { y: c[0], m: c[1], d: c[2], wd: mod(days + 4, 7), h: div(rest, 3600000), mi: div(rest % 3600000, 60000), s: div(rest % 60000, 1000), ms: rest % 1000, off: off[0], dst: off[1], days: days, ms0: ms };
  f.gy = c[0]; f.era = c[0] > 0 ? 1 : 0; f.ey = c[0] > 0 ? c[0] : 1 - c[0];
  if (cal === 'buddhist') { f.era = 0; f.ey = c[0] + 543; f.y = f.ey; }
  else if (cal === 'persian') { var p = persianFromDays(days, c[0]); f.era = 0; f.ey = p[0]; f.y = p[0]; f.m = p[1]; f.d = p[2]; }
  return f;
}

// ---- patterns ------------------------------------------------------------------------------------------------------------------------------------
var patCache = Object.create(null);
function parsePattern(p) { // [{lit: text} | {l: letter, n: count}]
  if (patCache[p]) return patCache[p];
  var out = [], i = 0, lit = '';
  // Chromium shows an ordinary space where CLDR has U+202F (narrow no-break space) in date and time patterns
  function flush() { if (lit) { out.push({ lit: lit.replace(/\u202f/g, ' ') }); lit = ''; } }
  while (i < p.length) {
    var ch = p[i];
    if (ch === "'") {
      if (p[i + 1] === "'") { lit += "'"; i += 2; continue; }
      var j = i + 1, q = '';
      while (j < p.length) { if (p[j] === "'") { if (p[j + 1] === "'") { q += "'"; j += 2; continue; } break; } q += p[j]; j++; }
      lit += q; i = j + 1; continue;
    }
    if (/[A-Za-z]/.test(ch)) {
      flush();
      var k = i; while (p[k] === ch) k++;
      out.push({ l: ch, n: k - i }); i = k; continue;
    }
    lit += ch; i++;
  }
  flush();
  return (patCache[p] = out);
}
function patternString(toks) {
  return toks.map(function (t) {
    if (t.lit !== undefined) return /[A-Za-z]/.test(t.lit) ? "'" + t.lit.replace(/'/g, "''") + "'" : t.lit.replace(/'/g, "''");
    return new Array(t.n + 1).join(t.l);
  }).join('');
}
// skeleton field classes
var CLASS = { G: 'G', y: 'y', Y: 'y', u: 'y', U: 'y', r: 'y', Q: 'Q', q: 'Q', M: 'M', L: 'M', w: 'w', W: 'W', E: 'E', e: 'E', c: 'E', d: 'd', D: 'D', F: 'F', a: 'a', b: 'a', B: 'B', h: 'h', H: 'h', K: 'h', k: 'h', j: 'h', J: 'h', m: 'm', s: 's', S: 'S', A: 'A', z: 'z', Z: 'z', O: 'z', v: 'z', V: 'z', x: 'z', X: 'z' };
var DATE_CLASSES = 'GyQMwWEdDF', TIME_CLASSES = 'aBhmsSA';
function skelOf(key) { // parse an availableFormats key: {class: [letter, count]}
  var out = {}, i = 0;
  while (i < key.length) {
    var ch = key[i], k = i; while (key[k] === ch) k++;
    var c = CLASS[ch];
    if (c && !(c === 'a' && out[c])) out[c] = [ch, k - i];
    i = k;
  }
  return out;
}
var skelCache = Object.create(null);
function skelCached(key) { return skelCache[key] || (skelCache[key] = skelOf(key)); }
function fieldDistance(c, a, b) { // a: requested [letter,count], b: candidate [letter,count]
  var d = 0, ta = a[1] >= 3 || c === 'E' && false, tb = b[1] >= 3;
  if (c === 'M' || c === 'Q') { var ta2 = a[1] >= 3, tb2 = b[1] >= 3; if (ta2 !== tb2) d += 0x100; d += Math.abs(a[1] - b[1]); }
  else if (c === 'E' || c === 'G') { d += Math.abs((a[1] >= 4 ? a[1] : 3) - (b[1] >= 4 ? b[1] : 3)); }
  else d += Math.abs(a[1] - b[1]);
  if (c === 'h' && a[0] !== b[0]) d += 0x10;
  return d;
}
function distance(req, cand) {
  var d = 0, c;
  for (c in req) { if (!cand[c]) d += 0x1000; else d += fieldDistance(c, req[c], cand[c]); }
  for (c in cand) if (!req[c]) d += 0x10000;
  return d;
}
// ICU's adjustFieldTypes: the matched pattern's field widths follow the request, except that a width the matched
// skeleton already had stays as the pattern wrote it, and numeric/text mismatches between pattern and skeleton are kept.
function isNumericField(letter, n) {
  switch (CLASS[letter]) {
    case 'y': case 'd': case 'D': case 'F': case 'w': case 'W': case 'h': case 'm': case 's': case 'S': case 'A': return true;
    case 'M': case 'Q': return n <= 2;
    case 'E': return (letter === 'e' || letter === 'c') && n <= 2;
  }
  return false;
}
function adjustPattern(toks, req, cand) {
  return toks.map(function (t) {
    if (t.lit !== undefined) return t;
    var c = CLASS[t.l], r = req[c];
    if (!r) return t;
    var n = t.n, l = t.l;
    if (c === 'h') { l = r[0] === 'j' ? l : r[0]; if (r[1] === 1 && req.m && req.m[1] === 2 && !req.s) return { l: l, n: 1 }; } // numeric hour next to a 2-digit minute stays short (ICU/V8 quirk)
    if (c === 'S') return { l: 'S', n: r[1] };
    if (c === 'a' || c === 'z') return t;
    var sk = cand && cand[c];
    var skLen = sk ? sk[1] : r[1];
    var patNum = isNumericField(t.l, t.n), skNum = sk ? isNumericField(sk[0], sk[1]) : patNum;
    if (!(skLen === r[1] || patNum !== skNum)) n = r[1];
    return { l: l, n: n };
  });
}
function tokenFor(letter, count) { return { l: letter, n: count }; }

// ---- locale data accessors ----------------------------------------------------------------------------------------------------------------
var FALLBACK_ERAS = { buddhist: { w: { 0: 'BE' }, a: { 0: 'BE' }, n: { 0: 'BE' } }, persian: { w: { 0: 'AP' }, a: { 0: 'AP' }, n: { 0: 'AP' } } };
function calData(s) {
  var cd = s.data.c[s.calendar];
  if (cd) return cd;
  if (s.calData2) return s.calData2;
  var g0 = s.data.c.gregory, c = Object.assign({}, g0);
  if (FALLBACK_ERAS[s.calendar]) c.er = FALLBACK_ERAS[s.calendar];
  // other calendars show their era next to the year
  var addEra = function (p) { return /y/.test(p) && !/G/.test(p) ? p + ' G' : p; };
  c.df = {}; Object.keys(g0.df).forEach(function (k) { c.df[k] = addEra(g0.df[k]); });
  c.af = {}; Object.keys(g0.af).forEach(function (k) { c.af[k] = /y/.test(k) && !/G/.test(k) && /^[yMdEQ]+$/.test(k) ? addEra(g0.af[k]) : g0.af[k]; });
  return (s.calData2 = c);
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
  if (cal === 'iso8601') cal = 'gregory';
  if (!cal || ['gregory', 'buddhist', 'persian'].indexOf(cal) < 0) cal = defaultCalendar(r.base);
  s.calendar = cal;
  s.nu = r.ext.nu && I.digitsOf(r.ext.nu) ? r.ext.nu : data.n.nu;
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
  }
  var kept = [];
  if (r.ext.ca && s.calendar === r.ext.ca) kept.push('ca-' + s.calendar);
  if (hcExt && hcExt === s.hourCycle && hourCycle === undefined && hour12 === undefined) kept.push('hc-' + hcExt);
  if (r.ext.nu && s.nu === r.ext.nu) kept.push('nu-' + s.nu);
  s.locale = r.base + (kept.length ? '-u-' + kept.join('-') : '');
  resolvePattern(s);
  dtf[SLOT] = s;
}
function defaultCalendar(tag) {
  var l = new Locale(tag);
  if (l.language === 'th') return 'buddhist';
  if (l.language === 'fa' && (!l.region || l.region === 'IR' || l.region === 'AF')) return 'persian';
  return 'gregory';
}

// ---- choosing the pattern ----------------------------------------------------------------------------------------------------------------
function skeletonOfFields(s) { // the requested skeleton: class -> [letter, count]
  var f = s.fields, req = {};
  if (f.weekday) req.E = ['E', f.weekday === 'long' ? 4 : f.weekday === 'narrow' ? 5 : 3];
  if (f.era) req.G = ['G', f.era === 'long' ? 4 : f.era === 'narrow' ? 5 : 1];
  if (f.year) req.y = ['y', f.year === '2-digit' ? 2 : 1];
  if (f.month) req.M = ['M', { numeric: 1, '2-digit': 2, short: 3, long: 4, narrow: 5 }[f.month]];
  if (f.day) req.d = ['d', f.day === '2-digit' ? 2 : 1];
  if (f.dayPeriod) req.B = ['B', f.dayPeriod === 'long' ? 4 : f.dayPeriod === 'narrow' ? 5 : 1];
  if (f.hour) req.h = [{ h12: 'h', h23: 'H', h11: 'K', h24: 'k' }[s.hourCycle] || 'h', f.hour === '2-digit' ? 2 : 1];
  if (f.minute) req.m = ['m', f.minute === '2-digit' ? 2 : 1];
  if (f.second) req.s = ['s', f.second === '2-digit' ? 2 : 1];
  return req;
}
function zoneToken(f) {
  switch (f.timeZoneName) {
    case 'short': return tokenFor('z', 1);
    case 'long': return tokenFor('z', 4);
    case 'shortOffset': return tokenFor('O', 1);
    case 'longOffset': return tokenFor('O', 4);
    case 'shortGeneric': return tokenFor('v', 1);
    case 'longGeneric': return tokenFor('v', 4);
  }
  return null;
}
function bestMatch(cd, req, only) { // best availableFormats pattern for req among candidates whose classes are in `only` (a string of classes)
  var best = null, bestD = Infinity, key;
  for (key in cd.af) {
    var cand = skelCached(key);
    if (only) {
      var ok = true;
      for (var c in cand) if (only.indexOf(c) < 0) { ok = false; break; }
      if (!ok) continue;
    }
    var d = distance(req, cand);
    if (d < bestD) { bestD = d; best = key; }
  }
  return best === null ? null : { key: best, dist: bestD };
}
function pickGlue(cd, req) {
  var m = req.M ? req.M[1] : 0, k = m >= 4 ? (req.E ? 'full' : 'long') : m === 3 ? 'medium' : 'short';
  return cd.dtf[k];
}
function neededPattern(s) {
  var cd = calData(s), f = s.fields, req = skeletonOfFields(s), toks;
  var zt = zoneToken(f);
  var dateReq = {}, timeReq = {}, any = false, c;
  for (c in req) { if (DATE_CLASSES.indexOf(c) >= 0) dateReq[c] = req[c]; else timeReq[c] = req[c]; any = true; }
  var hasDate = Object.keys(dateReq).length > 0, hasTime = Object.keys(timeReq).length > 0;
  if (!any) toks = f.fractionalSecondDigits ? [] : [];
  else {
    var exact = bestMatch(cd, req, null);
    if (exact && exact.dist >= 0x10000 && Object.keys(req).length === 1) toks = [tokenFor(req[Object.keys(req)[0]][0], req[Object.keys(req)[0]][1])];
    else if (exact && exact.dist < 0x1000) toks = adjustPattern(parsePattern(cd.af[exact.key]), req, skelCached(exact.key));
    else if (hasDate && hasTime) {
      var dm = bestMatch(cd, dateReq, DATE_CLASSES), tm = bestMatch(cd, timeReq, TIME_CLASSES);
      var dp = dm ? appendMissing(adjustPattern(parsePattern(cd.af[dm.key]), dateReq, skelCached(dm.key)), dateReq, skelCached(dm.key)) : [];
      var tp = tm ? appendMissing(adjustPattern(parsePattern(cd.af[tm.key]), timeReq, skelCached(tm.key)), timeReq, skelCached(tm.key)) : [];
      var glue = parsePattern(pickGlue(cd, req)), out = [];
      glue.forEach(function (t) {
        if (t.lit !== undefined) out.push(t);
        else if (t.n === 1 && t.l === '{') out.push(t);
      });
      var glueSrc = pickGlue(cd, req), parts = glueSrc.split(/(\{0\}|\{1\})/);
      toks = [];
      parts.forEach(function (p) {
        if (p === '{0}') toks = toks.concat(tp); else if (p === '{1}') toks = toks.concat(dp);
        else if (p) toks = toks.concat(parsePattern(p));
      });
    } else {
      var bm = exact || bestMatch(cd, req, null);
      toks = bm ? appendMissing(adjustPattern(parsePattern(cd.af[bm.key]), req, skelCached(bm.key)), req, skelCached(bm.key)) : [];
    }
  }
  if (f.fractionalSecondDigits) toks = addFraction(s, toks, f.fractionalSecondDigits);
  if (zt) { if (toks.length) toks = toks.concat([{ lit: ' ' }, zt]); else toks = [zt]; }
  return toks;
}
function addFraction(s, toks, n) {
  var out = [], done = false, sym = s.data.n.sym, d = (sym[s.nu] || sym.latn).d;
  toks.forEach(function (t) {
    if (t.l === 'S') return; // a fraction the matched pattern carried is replaced
    out.push(t);
    if (t.l === 's' && !done) { out.push({ lit: d }); out.push({ l: 'S', n: n }); done = true; }
  });
  if (!done) out.push({ l: 'S', n: n });
  return out;
}
function appendMissing(toks, req, cand) { // fields the matched pattern lacks are appended with a space (ICU's appendItems)
  for (var c in req) {
    if (cand[c]) continue;
    toks = toks.concat([{ lit: ' ' }, tokenFor(req[c][0] === 'j' ? 'h' : req[c][0], req[c][1])]);
  }
  return toks;
}
function stylePattern(s) { // dateStyle / timeStyle
  var cd = calData(s), dt = null, tt = null;
  if (s.dateStyle) dt = parsePattern(cd.df[s.dateStyle]);
  if (s.timeStyle) {
    var src = cd.tf[s.timeStyle];
    tt = parsePattern(src);
    var want = s.hourCycle, have = null;
    tt.forEach(function (t) { if (t.l && CLASS[t.l] === 'h') have = { h: 'h12', H: 'h23', K: 'h11', k: 'h24' }[t.l]; });
    if (have && want && have !== want) tt = switchByRequest(cd, tt, want) || switchHourCycle(tt, want, have);
  }
  if (dt && tt) {
    var k = s.dateStyle, glueSrc = (k === 'full' || k === 'long') && cd.dtfAt ? cd.dtfAt[k] : cd.dtf[k], toks = [];
    glueSrc.split(/(\{0\}|\{1\})/).forEach(function (p) {
      if (p === '{0}') toks = toks.concat(tt); else if (p === '{1}') toks = toks.concat(dt); else if (p) toks = toks.concat(parsePattern(p));
    });
    return toks;
  }
  return dt || tt || [];
}
// a time pattern in another hour cycle: the hour letter changes (the count stays), a day period is dropped when going to
// 24 hours and appended when going to 12 (what ICU does to a style pattern for hour12 / hourCycle)
// the time style in another hour cycle, the way V8 does it: a skeleton of the style's fields with the new hour letter is matched
// against the locale's available formats (so the pattern may reorder, e.g. a leading day period), and the zone keeps its place
function switchByRequest(cd, tt, want) {
  var zi = -1, i;
  for (i = 0; i < tt.length; i++) if (tt[i].l && CLASS[tt[i].l] === 'z') zi = i;
  var rest = tt, zone = null, zlead = false, glue = null;
  if (zi >= 0) {
    zone = tt[zi]; zlead = zi === 0;
    if (zlead) { rest = tt.slice(1); if (rest[0] && rest[0].lit !== undefined) { glue = rest[0]; rest = rest.slice(1); } }
    else { rest = tt.slice(0, zi); if (rest.length && rest[rest.length - 1].lit !== undefined) { glue = rest[rest.length - 1]; rest = rest.slice(0, -1); } }
  }
  var req = {}, letter = { h12: 'h', h23: 'H', h11: 'K', h24: 'k' }[want];
  rest.forEach(function (t) {
    if (t.l === undefined) return;
    var c = CLASS[t.l];
    if (c === 'a') return;
    req[c] = c === 'h' ? [letter, 1] : c === 'm' || c === 's' ? [t.l, 1] : [t.l, t.n];
  });
  if (!req.h) return null;
  var bm = bestMatch(cd, req, TIME_CLASSES);
  if (!bm || bm.dist >= 0x1000) return null;
  var out = adjustPattern(parsePattern(cd.af[bm.key]), req, skelCached(bm.key));
  // a 24-hour style keeps its own hour width in the other cycle
  var oh = rest.filter(function (t) { return t.l === 'H' || t.l === 'k'; })[0];
  if (oh) out = out.map(function (t) { return t.l && CLASS[t.l] === 'h' ? { l: t.l, n: oh.n } : t; });
  if (zone) out = zlead ? [zone].concat(glue ? [glue] : [], out) : out.concat(glue ? [glue] : [], [zone]);
  return out;
}
function switchHourCycle(tt, want, have) {
  var letter = { h12: 'h', h23: 'H', h11: 'K', h24: 'k' }[want], to12 = want === 'h12' || want === 'h11', from12 = have === 'h12' || have === 'h11';
  var out = tt.map(function (t) { return t.l && CLASS[t.l] === 'h' ? { l: letter, n: t.n } : t; });
  if (from12 && !to12) {
    var i = out.findIndex(function (t) { return t.l && CLASS[t.l] === 'a'; });
    if (i >= 0) {
      out.splice(i, 1);
      if (out[i] && out[i].lit !== undefined) { out[i] = { lit: out[i].lit.replace(/^[\s\u00a0\u202f]+/, '') }; if (!out[i].lit) out.splice(i, 1); }
      else if (out[i - 1] && out[i - 1].lit !== undefined) { out[i - 1] = { lit: out[i - 1].lit.replace(/[\s\u00a0\u202f]+$/, '') }; if (!out[i - 1].lit) out.splice(i - 1, 1); }
    }
  } else if (!from12 && to12) {
    var z = out.findIndex(function (t) { return t.l && CLASS[t.l] === 'z'; });
    var ins = [{ lit: ' ' }, { l: 'a', n: 1 }];
    if (z >= 0) { var before = z; if (out[z - 1] && out[z - 1].lit !== undefined) before = z - 1; out.splice.apply(out, [before, 0].concat(ins)); }
    else out = out.concat(ins);
  }
  return out;
}
function resolvePattern(s) {
  s.tokens = s.dateStyle || s.timeStyle ? stylePattern(s) : neededPattern(s);
  // the options as the resolved pattern shows them (resolvedOptions)
  var r = {};
  s.tokens.forEach(function (t) {
    if (t.l === undefined) return;
    var c = CLASS[t.l], n = t.n;
    switch (c) {
      case 'E': r.weekday = n >= 5 ? 'narrow' : n === 4 ? 'long' : 'short'; break;
      case 'G': r.era = n >= 5 ? 'narrow' : n === 4 ? 'long' : 'short'; break;
      case 'y': r.year = n === 2 ? '2-digit' : 'numeric'; break;
      case 'M': r.month = n >= 5 ? 'narrow' : n === 4 ? 'long' : n === 3 ? 'short' : n === 2 ? '2-digit' : 'numeric'; break;
      case 'd': r.day = n === 2 ? '2-digit' : 'numeric'; break;
      case 'B': r.dayPeriod = n >= 5 ? 'narrow' : n === 4 ? 'long' : 'short'; break;
      case 'h': r.hour = n === 2 ? '2-digit' : 'numeric'; break;
      case 'm': r.minute = n === 2 ? '2-digit' : 'numeric'; break;
      case 's': r.second = n === 2 ? '2-digit' : 'numeric'; break;
      case 'S': r.fractionalSecondDigits = n; break;
      case 'z': r.timeZoneName = t.l === 'z' ? (n >= 4 ? 'long' : 'short') : t.l === 'O' ? (n >= 4 ? 'longOffset' : 'shortOffset') : (n >= 4 ? 'longGeneric' : 'shortGeneric'); break;
    }
  });
  s.resolved = r;
  // a numeric minute next to an hour is always two digits in the pattern; with no hour it follows the pattern too
  if (r.hour) { var hh = s.tokens.filter(function (t) { return t.l && CLASS[t.l] === 'h'; })[0]; if (hh) s.hourCycle = { h: 'h12', H: 'h23', K: 'h11', k: 'h24' }[hh.l] || s.hourCycle; }
}

// ---- rendering --------------------------------------------------------------------------------------------------------------------------------------
function pad(n, w) { var s = String(Math.abs(n)); while (s.length < w) s = '0' + s; return s; }
function gmtOffsetText(s, offSec, long) {
  var z = s.data.z;
  if (offSec === 0) return z.gz || 'GMT';
  var neg = offSec < 0, a = Math.abs(offSec), h = div(a, 3600), m = div(a % 3600, 60), sec = a % 60;
  var hf = (z.hf || '+HH:mm;-HH:mm').split(';'), pat = neg ? (hf[1] || '-' + hf[0].slice(1)) : hf[0];
  var text;
  if (long) text = pat.replace('HH', pad(h, 2)).replace('mm', pad(m, 2)).replace('ss', pad(sec, 2));
  else {
    // short form: hours without padding, minutes only when not zero
    text = (neg ? pat.charAt(0) : pat.charAt(0));
    text = pat.replace(/H+/, String(h)).replace(/[^\d٠-٩]*m+/, m ? pat.match(/[^Hm]*(?=m)/)[0] + pad(m, 2) : '').replace(/[^\d]*s+/, '');
    if (!m) text = pat.charAt(0) + String(h);
  }
  return (z.gmt || 'GMT{0}').replace('{0}', I.localDigits(text, s.nu));
}
function metazoneAt(zone, ms) {
  var list = I.G().mz[zone];
  if (!list) return null;
  for (var i = 0; i < list.length; i++) { var e = list[i]; if ((e[1] === null || ms >= e[1]) && (e[2] === null || ms < e[2])) return e[0]; }
  return null;
}
function zoneText(s, f, letter, count) {
  var z = s.data.z, ms = f.ms0, zone = s.timeZone;
  var isOffsetZone = zone.charAt(0) === '+' || zone.charAt(0) === '-';
  if (letter === 'O') return gmtOffsetText(s, f.off, count >= 4);
  if (letter === 'V') {
    if (count === 2) return zone;
    return zone;
  }
  if (letter === 'Z' || letter === 'x' || letter === 'X') {
    var a = Math.abs(f.off), sg = f.off < 0 ? '-' : '+', hh = pad(div(a, 3600), 2), mm = pad(div(a % 3600, 60), 2);
    if (letter === 'X' && f.off === 0) return 'Z';
    return count >= 4 && letter !== 'x' && letter !== 'X' ? gmtOffsetText(s, f.off, true) : sg + hh + (count === 1 && letter !== 'Z' ? '' : (count === 3 || letter === 'Z' && count === 5 ? ':' : '') + mm);
  }
  var generic = letter === 'v', long = count >= 4;
  if (zone === 'UTC') return generic ? 'GMT' : (long ? (s.data.z.mz ? 'Coordinated Universal Time' : 'UTC') : 'UTC');
  if (isOffsetZone) return gmtOffsetText(s, f.off, long);
  if (z.mz) {
    var mz = metazoneAt(zone, ms), rec = mz && z.mz[mz], names = rec && rec[long ? 'l' : 's'];
    if (names) {
      var nm = generic ? names[0] : (f.dst ? names[2] : names[1]);
      if (nm) return nm;
    }
    if (generic && rec && rec.l && rec.l[0]) return rec.l[0]; // no short generic name: the long one
    if (generic && !long) { var ex = z.zn && z.zn[zone]; if (ex) return (z.rf || '{0} Time').replace('{0}', ex); }
    if (generic && long) { var ex2 = z.zn && z.zn[zone]; if (ex2) return (z.rf || '{0} Time').replace('{0}', ex2); }
  }
  return gmtOffsetText(s, f.off, long);
}
function dayPeriodName(s, f, letter, count) {
  // ICU formats the plain AM/PM marker (pattern letter a, up to four letters) with the wide names: Korean shows "오전", not "AM"
  var cd = calData(s), w = count >= 5 ? 'n' : (count === 4 || letter === 'a') ? 'w' : 'a', dp = cd.dp['f' + w] || cd.dp.fa || {};
  var h = f.h, m = f.mi;
  if (letter === 'B') {
    var G = I.G(), lang = new Locale(s.base).language, rules = G.dpr[lang] || G.dpr[lang.split('-')[0]], t = h * 60 + m;
    if (rules) {
      for (var name in rules) { var r = rules[name]; if (r.length === 1 && r[0] === t && m === 0 && f.s === 0 && dp[name.replace(/\d+$/, '')] !== undefined) return dp[name]; }
      for (var nm in rules) { var rr = rules[nm]; if (rr.length === 2) { var inRange = rr[0] <= rr[1] ? t >= rr[0] && t < rr[1] : (t >= rr[0] || t < rr[1]); if (rr[1] === 1440 && t >= rr[0]) inRange = true; if (inRange && dp[nm]) return dp[nm]; } }
    }
  } else if (letter === 'b') {
    if (h === 12 && m === 0 && f.s === 0 && dp.noon) return dp.noon;
    if (h === 0 && m === 0 && f.s === 0 && dp.midnight) return dp.midnight;
  }
  return h < 12 ? dp.am : dp.pm;
}
function renderTokens(s, tokens, f) {
  var cd = calData(s), parts = [], ds = function (str) { return I.localDigits(str, s.nu); };
  function push(type, value) { parts.push({ type: type, value: value }); }
  tokens.forEach(function (t) {
    if (t.lit !== undefined) { if (parts.length && parts[parts.length - 1].type === 'literal') parts[parts.length - 1].value += t.lit; else push('literal', t.lit); return; }
    var n = t.n, v;
    switch (t.l) {
      case 'G': {
        var er = cd.er && cd.er[n >= 5 ? 'n' : n === 4 ? 'w' : 'a'];
        push('era', er ? er[f.era] || er[1] || '' : ''); break;
      }
      case 'y': case 'u': case 'Y': case 'r':
        v = f.y; if (s.calendar === 'gregory' && t.l !== 'u' && f.gy <= 0) v = f.ey;
        if (n === 2) push('year', ds(pad(mod(v, 100), 2))); else push('year', ds(pad(v, n))); break;
      case 'Q': case 'q':
        v = div(f.m - 1, 3) + 1;
        if (n <= 2) push('literal', ds(pad(v, n))); else { var qn = (cd.qu[(t.l === 'q' ? 's' : 'f') + (n === 3 ? 'a' : n === 4 ? 'w' : 'n')] || cd.qu.fa || []); push('literal', qn[v - 1] || String(v)); }
        break;
      case 'M': case 'L':
        if (n <= 2) push('month', ds(pad(f.m, n)));
        else { var arr = cd.mo[(t.l === 'L' ? 's' : 'f') + (n === 3 ? 'a' : n === 4 ? 'w' : 'n')] || cd.mo['f' + (n === 3 ? 'a' : n === 4 ? 'w' : 'n')] || cd.mo.fw; push('month', arr[f.m - 1]); }
        break;
      case 'd': push('day', ds(pad(f.d, n))); break;
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
function rangeParts(s, a, b) {
  var ta = timeValue(a), tb = timeValue(b);
  var fa = fieldsOf(ta, s.timeZone, s.calendar), fb = fieldsOf(tb, s.timeZone, s.calendar);
  var cd = calData(s), pa = renderTokens(s, s.tokens, fa), pb = renderTokens(s, s.tokens, fb);
  function tag(parts, src) { return parts.map(function (p) { return { type: p.type, value: p.value, source: src }; }); }
  if (joined(pa) === joined(pb)) return tag(pa, 'shared');
  var fallback = (cd.iv && cd.iv._) || '{0} – {1}';
  var useFallback = function () {
    var out = [], pieces = fallback.split(/(\{0\}|\{1\})/);
    pieces.forEach(function (p) { if (p === '{0}') out = out.concat(tag(pa, 'startRange')); else if (p === '{1}') out = out.concat(tag(pb, 'endRange')); else if (p) out.push({ type: 'literal', value: p, source: 'shared' }); });
    return out;
  };
  if (!cd.iv || s.dateStyle || s.timeStyle && false) return useFallback();
  // greatest difference: era, year, month, day, am/pm, hour, minute, second
  var diff = fa.era !== fb.era ? 'G' : fa.y !== fb.y ? 'y' : fa.m !== fb.m ? 'M' : fa.d !== fb.d ? 'd' : (fa.h >= 12) !== (fb.h >= 12) ? 'a' : fa.h !== fb.h ? 'h' : fa.mi !== fb.mi ? 'm' : fa.s !== fb.s ? 's' : 'S';
  var req = skeletonOfFields(s), best = null, bestD = Infinity, key;
  if (s.dateStyle || s.timeStyle) req = styleSkeleton(s);
  for (key in cd.iv) {
    if (key === '_') continue;
    var d = distance(req, skelCached(key));
    if (d < bestD) { bestD = d; best = key; }
  }
  if (best === null || bestD >= 0x1000) return useFallback();
  var rec = cd.iv[best], dl = diff === 'h' && rec.h === undefined ? 'H' : diff;
  var pat = rec[diff] || (diff === 'h' ? rec.H : undefined);
  if (!pat) return useFallback();
  // adjust field widths of the interval pattern to the request, then split it into the two halves at the first repeated letter
  var toks = adjustPattern(parsePattern(pat), req, skelCached(best)), seen = {}, split = -1;
  for (var i = 0; i < toks.length; i++) {
    var t = toks[i]; if (t.l === undefined) continue;
    var c = CLASS[t.l];
    if (seen[c]) { split = i; break; }
    seen[c] = 1;
  }
  if (split < 0) return useFallback();
  var first = toks.slice(0, split), second = toks.slice(split);
  // the first half keeps its trailing literal ("MMM d – "), as ICU does
  var zt = zoneToken(s.fields);
  var out = tag(renderTokens(s, first, fa), 'startRange').map(function (p, idx, arr) { return p; });
  // literals between the halves are shared
  var left = renderTokens(s, first, fa), right = renderTokens(s, second, fb);
  var res = [];
  left.forEach(function (p) { res.push({ type: p.type, value: p.value, source: p.type === 'literal' ? 'shared' : 'startRange' }); });
  right.forEach(function (p) { res.push({ type: p.type, value: p.value, source: p.type === 'literal' ? 'shared' : 'endRange' }); });
  return res;
}
function styleSkeleton(s) {
  var req = {}, cd = calData(s);
  [s.dateStyle && cd.df[s.dateStyle], s.timeStyle && cd.tf[s.timeStyle]].forEach(function (p) {
    if (!p) return;
    parsePattern(p).forEach(function (t) { if (t.l && CLASS[t.l] && !req[CLASS[t.l]]) req[CLASS[t.l]] = [t.l, t.n]; });
  });
  return req;
}

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
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('startDate or endDate is undefined');
  return joined(rangeParts(s, a, b));
});
H.define(DateTimeFormat.prototype, 'formatRangeToParts', function formatRangeToParts(a, b) {
  var s = slot(this, 'formatRangeToParts');
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('startDate or endDate is undefined');
  return rangeParts(s, a, b);
});
H.define(DateTimeFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slot(this, 'resolvedOptions'), o = { locale: s.locale, calendar: s.calendar, numberingSystem: s.nu, timeZone: s.timeZone };
  if (s.hourCycle && (s.resolved.hour || s.timeStyle)) { o.hourCycle = s.hourCycle; o.hour12 = s.hourCycle === 'h11' || s.hourCycle === 'h12'; }
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
