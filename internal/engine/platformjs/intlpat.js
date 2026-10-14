// Date-time patterns for Intl.DateTimeFormat: a port of ICU's DateTimePatternGenerator (the thing V8 asks for the
// pattern that fits the requested options). A skeleton such as "yMMMMEEEEdjm" is matched against the locale's
// patterns (CLDR availableFormats plus the standard date and time patterns), fields the best match lacks are
// appended with the locale's appendItems, date and time halves are glued with the "at time" date-time pattern, and the
// field widths are adjusted to the request. Published as H.pat.
var P = H.pat = {};

// ---- patterns as tokens: [{lit: text} | {l: letter, n: count}] -------------------------------------------------------------------
var patCache = Object.create(null);
function parsePattern(p0) {
  if (patCache[p0]) return patCache[p0];
  // "pattern@numbers=hebr": numbers CLDR writes in another numbering system, for all fields or "d=hanidays;y=jpanyear" for some
  var p = p0, num = null, nm = /@numbers=([A-Za-z0-9=;]+)/.exec(p0);
  if (nm) { num = nm[1]; p = p0.replace(nm[0], ''); }
  var out = [], i = 0, lit = '';
  // Chromium shows an ordinary space where CLDR has U+202F (narrow no-break space) in date and time patterns
  function flush() { if (lit) { out.push({ lit: lit.replace(/ /g, ' ') }); lit = ''; } }
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
  if (num) out.push({ num: num });
  return (patCache[p0] = out);
}
function patternString(toks) {
  return toks.map(function (t) {
    if (t.num !== undefined) return '@numbers=' + t.num;
    if (t.lit !== undefined) return /[A-Za-z]/.test(t.lit) ? "'" + t.lit.replace(/'/g, "''") + "'" : t.lit.replace(/'/g, "''");
    return new Array(t.n + 1).join(t.l);
  }).join('');
}
P.parse = parsePattern; P.str = patternString;

// ---- ICU's field table (dtTypes) ------------------------------------------------------------------------------------------------------
// [pattern letter, field, type, minimum length]; text types are negative, numeric ones carry the length
var ERA = 0, YEAR = 1, QUARTER = 2, MONTH = 3, WOY = 4, WOM = 5, WEEKDAY = 6, DOY = 7, DOWIM = 8, DAY = 9, DAYPERIOD = 10, HOUR = 11, MINUTE = 12, SECOND = 13, FRAC = 14, ZONE = 15, NFIELDS = 16;
var DATE_MASK = (1 << DAYPERIOD) - 1, ALL_MASK = (1 << NFIELDS) - 1, TIME_MASK = ALL_MASK & ~DATE_MASK, FRAC_MASK = 1 << FRAC, SEC_FRAC_MASK = (1 << SECOND) | FRAC_MASK;
var DT = [
  ['G', ERA, -0x103, 1], ['G', ERA, -0x104, 4], ['G', ERA, -0x101, 5],
  ['y', YEAR, 0x100, 1], ['Y', YEAR, 0x110, 1], ['u', YEAR, 0x120, 1], ['r', YEAR, 0x130, 1], ['U', YEAR, -0x103, 1], ['U', YEAR, -0x104, 4], ['U', YEAR, -0x101, 5],
  ['Q', QUARTER, 0x100, 1], ['Q', QUARTER, -0x103, 3], ['Q', QUARTER, -0x104, 4], ['Q', QUARTER, -0x101, 5],
  ['q', QUARTER, 0x110, 1], ['q', QUARTER, -0x113, 3], ['q', QUARTER, -0x114, 4], ['q', QUARTER, -0x111, 5],
  ['M', MONTH, 0x100, 1], ['M', MONTH, -0x103, 3], ['M', MONTH, -0x104, 4], ['M', MONTH, -0x101, 5],
  ['L', MONTH, 0x110, 1], ['L', MONTH, -0x113, 3], ['L', MONTH, -0x114, 4], ['L', MONTH, -0x111, 5],
  ['w', WOY, 0x100, 1], ['W', WOM, 0x100, 1],
  ['E', WEEKDAY, -0x103, 1], ['E', WEEKDAY, -0x104, 4], ['E', WEEKDAY, -0x101, 5], ['E', WEEKDAY, -0x102, 6],
  ['c', WEEKDAY, 0x120, 1], ['c', WEEKDAY, -0x123, 3], ['c', WEEKDAY, -0x124, 4], ['c', WEEKDAY, -0x121, 5], ['c', WEEKDAY, -0x122, 6],
  ['e', WEEKDAY, 0x110, 1], ['e', WEEKDAY, -0x113, 3], ['e', WEEKDAY, -0x114, 4], ['e', WEEKDAY, -0x111, 5], ['e', WEEKDAY, -0x112, 6],
  ['d', DAY, 0x100, 1], ['g', DAY, 0x110, 1], ['D', DOY, 0x100, 1], ['F', DOWIM, 0x100, 1],
  ['a', DAYPERIOD, -0x103, 1], ['a', DAYPERIOD, -0x104, 4], ['a', DAYPERIOD, -0x101, 5],
  ['b', DAYPERIOD, -0x113, 1], ['b', DAYPERIOD, -0x114, 4], ['b', DAYPERIOD, -0x111, 5],
  ['B', DAYPERIOD, -0x123, 1], ['B', DAYPERIOD, -0x124, 4], ['B', DAYPERIOD, -0x121, 5],
  ['H', HOUR, 0x1a0, 1], ['k', HOUR, 0x1b0, 1], ['h', HOUR, 0x100, 1], ['K', HOUR, 0x110, 1],
  ['m', MINUTE, 0x100, 1], ['s', SECOND, 0x100, 1], ['A', SECOND, 0x110, 1], ['S', FRAC, 0x100, 1],
  ['v', ZONE, -0x104, 1], ['v', ZONE, -0x114, 4], ['z', ZONE, -0x101, 1], ['z', ZONE, -0x102, 4],
  ['Z', ZONE, -0x111, 1], ['Z', ZONE, -0x114, 4], ['Z', ZONE, -0x113, 5],
  ['O', ZONE, -0x103, 1], ['O', ZONE, -0x10a, 4],
  ['V', ZONE, -0x113, 1], ['V', ZONE, -0x114, 2], ['V', ZONE, -0x115, 3], ['V', ZONE, -0x116, 4],
  ['X', ZONE, -0x111, 1], ['X', ZONE, -0x113, 2], ['X', ZONE, -0x114, 4],
  ['x', ZONE, -0x111, 1], ['x', ZONE, -0x113, 2], ['x', ZONE, -0x114, 4]
];
var ROWS = Object.create(null);
DT.forEach(function (r) { (ROWS[r[0]] || (ROWS[r[0]] = [])).push(r); });
function rowFor(ch, len) {
  var rows = ROWS[ch];
  if (!rows) return null;
  var r = rows[0];
  for (var i = 1; i < rows.length; i++) { if (rows[i][3] <= len) r = rows[i]; else break; }
  return r;
}
// a skeleton (or the skeleton of a pattern): per field the type, the letters as written and the base letters
function skelOf(toks) {
  var sk = { t: [], oc: [], ol: [], bc: [], bl: [] }, i;
  for (i = 0; i < NFIELDS; i++) sk.t.push(0);
  toks.forEach(function (tk) {
    if (tk.l === undefined) return;
    var row = rowFor(tk.l, tk.n);
    if (!row) return;
    var f = row[1];
    sk.oc[f] = tk.l; sk.ol[f] = tk.n; sk.bc[f] = row[0]; sk.bl[f] = row[3];
    sk.t[f] = row[2] > 0 ? row[2] + tk.n : row[2];
  });
  // a 12-hour skeleton without a day period gets the plain AM/PM marker, so 12- and 24-hour patterns stay apart (ICU's addedDefaultDayPeriod)
  if ((sk.oc[HOUR] === 'h' || sk.oc[HOUR] === 'K') && !sk.oc[DAYPERIOD]) { sk.oc[DAYPERIOD] = 'a'; sk.ol[DAYPERIOD] = 1; sk.bc[DAYPERIOD] = 'a'; sk.bl[DAYPERIOD] = 1; sk.t[DAYPERIOD] = -0x103; sk.addedA = true; }
  sk.key = sk.t.join();
  var b = '';
  for (i = 0; i < NFIELDS; i++) if (sk.bc[i]) b += new Array(sk.bl[i] + 1).join(sk.bc[i]);
  sk.base = b;
  sk.first = b.charAt(0);
  return sk;
}
function mask(sk) { var m = 0; for (var i = 0; i < NFIELDS; i++) if (sk.t[i]) m |= 1 << i; return m; }
function topBit(m) { var i = -1; while (m) { m >>= 1; i++; } return i; }
function skeletonOfPattern(p) { // ICU's DateTimePatternGenerator::staticGetSkeleton
  var sk = skelOf(parsePattern(p)), out = '';
  for (var i = 0; i < NFIELDS; i++) if (sk.oc[i] && !(i === DAYPERIOD && sk.addedA)) out += new Array(sk.ol[i] + 1).join(sk.oc[i]);
  return out;
}
P.skeletonOf = skeletonOfPattern;
P.setType = function (ch, minLen, type) { var r = rowFor(ch, minLen); if (r) r[2] = type; }; // (for tools/intlgen/fit.js)
var BOOT = function (c) { return c >= 'a' ? 26 + c.charCodeAt(0) - 97 : c.charCodeAt(0) - 65; };

// ---- the generator for one locale and calendar -----------------------------------------------------------------------------------------
var DEFAULT_APPEND = '{0} ├{2}: {1}┤';
var APPEND_KEYS = { Era: ERA, Year: YEAR, Quarter: QUARTER, Month: MONTH, Week: WOY, 'Day-Of-Week': WEEKDAY, Day: DAY, Hour: HOUR, Minute: MINUTE, Second: SECOND, Timezone: ZONE };
var NAME_KEYS = [['era', ERA], ['year', YEAR], ['quarter', QUARTER], ['month', MONTH], ['weekOfYear', WOY], ['weekday', WEEKDAY], ['day', DAY], ['dayPeriod', DAYPERIOD], ['hour', HOUR], ['minute', MINUTE], ['second', SECOND], ['timeZoneName', ZONE]];

// cd: calendar data (df, tf, dtf, dtfAt, af, ai); names: the locale's field names (data.r.dn); decimal: decimal separator;
// hourChar: the locale's preferred hour letter (what 'j' stands for)
function Gen(cd, names, decimal, hourChar, glue) {
  var self = this;
  this.cd = cd; this.glue = glue; this.decimal = decimal; this.hourChar = hourChar;
  this.append = [];
  this.names = [];
  var i;
  for (i = 0; i < NFIELDS; i++) { this.append.push(DEFAULT_APPEND); this.names.push(''); }
  var ai = cd.ai || {};
  Object.keys(ai).forEach(function (k) { if (k in APPEND_KEYS) self.append[APPEND_KEYS[k]] = ai[k]; });
  NAME_KEYS.forEach(function (nk) { var v = names && names[nk[0] + ':l']; if (v) self.names[nk[1]] = v; });
  // the entries, in the order ICU's PatternMap iterates them: by first letter (A-Z, then a-z), then by insertion
  var entries = [], byKey = Object.create(null);
  function add(pattern, skeleton, override) {
    var sk = skelOf(parsePattern(skeleton === null ? pattern : skeleton)), ex = byKey[sk.key];
    if (!sk.first) return;
    if (!override) {
      for (var j = 0; j < entries.length; j++) if (entries[j].sk.base === sk.base && !entries[j].specified) return; // a derived pattern with that base letters exists
    }
    if (ex) { if (override) { ex.pattern = pattern; ex.specified = skeleton !== null; } return; }
    byKey[sk.key] = { sk: sk, pattern: pattern, specified: skeleton !== null, seq: entries.length };
    entries.push(byKey[sk.key]);
  }
  'GyQMwWEDFdaHmsSv'.split('').forEach(function (c) { add(c, null, false); });
  ['full', 'long', 'medium', 'short'].forEach(function (k) {
    // (the numbering system a style pattern asks for is not part of the pattern DateTimePatternGenerator sees)
    if (cd.df && cd.df[k]) add(cd.df[k].replace(/@numbers=[A-Za-z0-9=;]+/, ''), null, false);
    if (cd.tf && cd.tf[k]) add(cd.tf[k].replace(/@numbers=[A-Za-z0-9=;]+/, ''), null, false);
  });
  var af = cd.af || {};
  Object.keys(af).forEach(function (k) { add(af[k], k, true); }); // in ICU's order (tools/intlgen)
  entries.sort(function (a, b) { return BOOT(a.sk.first) - BOOT(b.sk.first) || a.seq - b.seq; });
  this.entries = entries;
  this.cache = Object.create(null);
}
// the pattern for a skeleton string; '' when there is nothing to show
// noHour: the hour keeps the width of the matched pattern (ICU's default options; V8's own formatter asks for MATCH_HOUR_FIELD_LENGTH)
Gen.prototype.best = function (skeleton, noHour) {
  var key = noHour ? skeleton + '|' : skeleton, hit = this.cache[key];
  if (hit !== undefined) return hit;
  return (this.cache[key] = new Request(this, skeleton, noHour).run());
};

function Request(gen, skeleton, noHour) {
  this.gen = gen;
  var flags = noHour ? 2 : 0, toks = [];
  parsePattern(skeleton).forEach(function (t) { // hour metacharacter j: the locale's preferred hour letter
    if (t.l === 'j') { toks.push({ l: gen.hourChar, n: 1 + ((t.n - 1) & 1) }); }
    else toks.push(t);
  });
  this.req = skelOf(toks);
  this.flags = flags;
}
var NO_FLAGS = 0, FIX_FRACTIONAL = 1, NO_HOUR_MATCH = 2;
// the entry closest to the request; includeMask limits which requested fields count
Request.prototype.raw = function (includeMask) {
  var req = this.req, best = null, bestD = 0x7fffffff, bestMissing = -1, i, e, entries = this.gen.entries;
  for (var k = 0; k < entries.length; k++) {
    e = entries[k];
    var d = 0, missing = 0, extra = 0;
    for (i = 0; i < NFIELDS; i++) {
      var mine = (includeMask >> i) & 1 ? req.t[i] : 0, other = e.sk.t[i];
      if (mine === other) continue;
      if (mine === 0) { d += 0x10000; extra |= 1 << i; }
      else if (other === 0) { d += 0x1000; missing |= 1 << i; }
      else d += Math.abs(mine - other);
    }
    if (d < bestD || (d === bestD && bestMissing < missing)) {
      bestD = d; best = { e: e, missing: missing, extra: extra };
      bestMissing = missing;
      if (d === 0) break;
    }
  }
  return best;
};
Request.prototype.adjust = function (pattern, spec, flags) {
  var req = this.req, decimal = this.gen.decimal;
  var toks = parsePattern(pattern).map(function (t) {
    if (t.l === undefined) return t;
    var row = rowFor(t.l, t.n);
    if (!row) return t;
    var f = row[1];
    if ((flags & FIX_FRACTIONAL) && f === SECOND) { return [t, { lit: decimal }, { l: 'S', n: req.ol[FRAC] || 1 }]; }
    if (!req.t[f]) return t;
    var rc = req.oc[f], rl = req.ol[f];
    if (rc === 'E' && rl < 3) rl = 3;
    var n = rl;
    if (f === MINUTE || f === SECOND || (f === HOUR && (flags & NO_HOUR_MATCH))) n = t.n; // the minute and second widths never follow the request, the hour's only when asked to
    else if (spec && rc !== 'c' && rc !== 'e') {
      var skl = spec.ol[f], patNum = row[2] > 0, skNum = spec.t[f] > 0;
      if (skl === rl || (patNum && !skNum) || (skNum && !patNum)) n = t.n;
    }
    var c = f !== HOUR && f !== MONTH && f !== WEEKDAY && (f !== YEAR || rc === 'Y') ? rc : t.l;
    if (c === 'E' && n < 3) c = 'e';
    return { l: c, n: n };
  });
  var flat = [];
  toks.forEach(function (t) { if (Array.isArray(t)) flat = flat.concat(t); else flat.push(t); });
  return patternString(flat);
};
Request.prototype.appending = function (missingFields, flags) {
  var result = '';
  if (!missingFields) return result;
  var r = this.raw(missingFields), spec = r.e.specified ? r.e.sk : null;
  result = this.adjust(r.e.pattern, spec, flags);
  if (r.missing === 0) return result;
  var last = 0, missing = r.missing;
  while (missing) {
    if (last === missing) break;
    if ((missing & SEC_FRAC_MASK) === FRAC_MASK && (missingFields & SEC_FRAC_MASK) === SEC_FRAC_MASK) {
      result = this.adjust(result, spec, flags | FIX_FRACTIONAL);
      missing &= ~FRAC_MASK;
      continue;
    }
    var starting = missing, r2 = this.raw(missing), sp2 = r2.e.specified ? r2.e.sk : null;
    var temp = this.adjust(r2.e.pattern, sp2, flags);
    missing = r2.missing;
    var found = starting & ~missing, top = topBit(found);
    if (top < 0) break;
    var fmt = this.gen.append[top], name = "'" + this.gen.names[top].replace(/'/g, "''") + "'", res = result;
    result = fmt.replace(/\{([012])\}/g, function (m, d) { return d === '0' ? res : d === '1' ? temp : name; });
    last = missing;
    spec = sp2;
  }
  return result;
};
Request.prototype.run = function () {
  var req = this.req, gen = this.gen, cd = gen.cd;
  if (!gen.entries.length) return '';
  var r = this.raw(-1);
  if (r.missing === 0 && r.extra === 0) return this.adjust(r.e.pattern, r.e.specified ? r.e.sk : null, this.flags);
  var needed = mask(req);
  var dp = this.appending(needed & DATE_MASK, this.flags), tp = this.appending(needed & TIME_MASK, this.flags);
  if (!dp) return tp;
  if (!tp) return dp;
  var m = req.ol[MONTH] ? req.t[MONTH] : 0, style = 'short';
  // the month width picks the length of the date-time pattern: wide month with weekday is "full", wide is "long", abbreviated is "medium"
  var mlen = req.oc[MONTH] === 'M' || req.oc[MONTH] === 'L' ? req.ol[MONTH] : 0;
  if (mlen === 4) style = req.ol[WEEKDAY] ? 'full' : 'long'; else if (mlen === 3) style = 'medium';
  var glue = gen.glue[style] || '{1} {0}';
  return glue.replace(/\{([01])\}/g, function (x, d) { return d === '0' ? tp : dp; });
};

P.raw = function (gen, skeleton, mask) { var r = new Request(gen, skeleton), b = r.raw(mask === undefined ? -1 : mask); return b && { pattern: b.e.pattern, key: b.e.sk.key, missing: b.missing, extra: b.extra, specified: b.e.specified }; }; // (for tools/intlgen/pat.js)
// generators are cached on the calendar data object
P.gen = function (cd, names, decimal, hourChar, glue) {
  var k = hourChar + decimal, m = cd.$gens;
  if (!m) { m = Object.create(null); Object.defineProperty(cd, '$gens', { value: m }); } // not enumerable: locale data is merged with for-in
  return m[k] || (m[k] = new Gen(cd, names, decimal, hourChar, glue));
};
// V8's ReplaceHourCycleInPattern / ReplaceHourCycleInSkeleton
P.replaceHourPattern = function (p, hc) {
  var to = { h11: 'K', h12: 'h', h23: 'H', h24: 'k' }[hc];
  return patternString(parsePattern(p).map(function (t) { return t.l && 'hHKk'.indexOf(t.l) >= 0 ? { l: to, n: t.n } : t; }));
};
P.replaceHourSkeleton = function (sk, hc) {
  var to = { h11: 'K', h12: 'h', h23: 'H', h24: 'k' }[hc], out = '';
  parsePattern(sk).forEach(function (t) {
    if (t.l === undefined) return;
    if ('abB'.indexOf(t.l) >= 0) return;
    out += new Array(t.n + 1).join('hHKk'.indexOf(t.l) >= 0 ? to : t.l);
  });
  return out;
};
