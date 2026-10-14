// Calendar arithmetic for Intl.DateTimeFormat: from a day number (days since 1970-01-01) to the year, month and day of a calendar.
// Gregorian, Buddhist, Japanese and ROC (the last three follow V8: the proleptic calendar of ICU's GregorianCalendar, which
// switches to the Julian calendar before 15 October 1582), Persian, Islamic (civil, tabular, Umm al-Qura and astronomical;
// the last two from tables of month lengths made from ICU's output by tools/intlgen), Hebrew, Indian (Saka), Coptic and
// Ethiopic. Published as H.cal.
var C = H.cal = {};
function div(a, b) { return Math.floor(a / b); }
function mod(a, b) { return a - div(a, b) * b; }

// ---- Gregorian and Julian -----------------------------------------------------------------------------------------------------------------
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
function julianFromDays(z) { // the Julian calendar (JDN algorithm); z = days since 1970-01-01
  var c = z + 2440588 + 32082, d = div(4 * c + 3, 1461), e = c - div(1461 * d, 4), m = div(5 * e + 2, 153);
  return [d - 4800 + div(m, 10), m + 3 - 12 * div(m, 10), e - div(153 * m + 2, 5) + 1];
}
var SWITCH = -141427; // 1582-10-15
function hybrid(z) { return z < SWITCH ? julianFromDays(z) : civilFromDays(z); }
function isLeap(y) { return y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0); }
C.civil = civilFromDays; C.daysFromCivil = daysFromCivil;

// ---- Persian (as ICU 77 has it: the 33-year arithmetic up to 1502 AP, an irregular stretch to 1602, then a strict 33-year cycle) -----------
var PERSIAN_CUM = [0, 31, 62, 93, 124, 155, 186, 216, 246, 276, 306, 336];
var PERSIAN_LEAPS = {}; // leap years 1503..1601 (read off ICU)
'1503 1507 1511 1515 1519 1523 1527 1531 1535 1540 1544 1548 1552 1556 1560 1564 1568 1573 1577 1581 1585 1589 1593 1597'.split(' ').forEach(function (y) { PERSIAN_LEAPS[y] = 1; });
function persianLeap(y) { return y < 1503 ? div(8 * (y + 1) + 21, 33) - div(8 * y + 21, 33) === 1 : y < 1602 ? !!PERSIAN_LEAPS[y] : (mod(y - 1602, 33) % 4 === 0 && mod(y - 1602, 33) < 32 && y !== 2059) || y === 2060; }
function persianStart(y) { // day number of 1 Farvardin
  if (y <= 1503) return -492268 + 365 * (y - 1) + div(8 * y + 21, 33) - (y === 1503 ? 1 : 0);
  var d = persianStart(1503);
  if (y > 1602) { var k = y - 1602; d = persianStart(1602) + 365 * k + div(k, 33) * 8 + Math.min(8, Math.ceil(mod(k, 33) / 4)); return y === 2060 ? d - 1 : d; } // (ICU moves the leap day of 2059 to 2060)
  for (var i = 1503; i < y; i++) d += persianLeap(i) ? 366 : 365;
  return d;
}
function persianFromDays(days) {
  var y = 1 + div(33 * (days + 492268) + 3, 12053);
  while (persianStart(y + 1) <= days) y++;
  while (persianStart(y) > days) y--;
  var doy = days - persianStart(y), m = doy < 216 ? div(doy, 31) : div(doy - 6, 30);
  return [y, m + 1, doy - PERSIAN_CUM[m] + 1];
}

// ---- Islamic ------------------------------------------------------------------------------------------------------------------------------------
function islamicFixed(y, m, d, epoch) { return epoch - 1 + (y - 1) * 354 + div(3 + 11 * y, 30) + 29 * (m - 1) + div(m, 2) + d; }
function islamicArith(rd, epoch) {
  var y = div(30 * (rd - epoch) + 10646, 10631), start = islamicFixed(y, 1, 1, epoch);
  var m = Math.min(12, Math.ceil((rd - 29 - start) / 29.5) + 1);
  return [y, m, rd - islamicFixed(y, m, 1, epoch) + 1];
}
// tables: 12 bits per year from 1300 AH (bit 11 = month 1, set when the month has 30 days), and the day number of 1 Muharram 1300
function islamicTable(tab, days) {
  var G = H.intl.G(), t = G.cal && G.cal[tab];
  if (!t) return null;
  var start = t.s, n = t.m.length / 3;
  if (days < start) return null;
  var y = 0, d = start, v, m;
  for (; y < n; y++) {
    v = parseInt(t.m.substr(y * 3, 3), 16);
    var len = 0;
    for (m = 0; m < 12; m++) len += (v >> (11 - m)) & 1 ? 30 : 29;
    if (days < d + len) {
      for (m = 0; m < 12; m++) { var ml = (v >> (11 - m)) & 1 ? 30 : 29; if (days < d + ml) return [1300 + y, m + 1, days - d + 1]; d += ml; }
    }
    d += len;
  }
  return null;
}

// ---- Hebrew -------------------------------------------------------------------------------------------------------------------------------------
var HEB_EPOCH = -1373427;
function hebLeap(y) { return mod(7 * y + 1, 19) < 7; }
function hebNewYear(y) { // as ICU's HebrewCalendar::startOfYear (which postpones on a molad exactly at the limit, unlike the textbook rule)
  var months = div(235 * y - 234, 19), frac = months * 13753 + 12084, day = months * 29 + div(frac, 25920), wd;
  frac = mod(frac, 25920);
  wd = mod(day, 7);
  if (wd === 2 || wd === 4 || wd === 6) { day += 1; wd = mod(day, 7); }
  if (wd === 1 && frac > 16404 && !hebLeap(y)) day += 2;
  else if (wd === 0 && frac > 23269 && hebLeap(y - 1)) day += 1;
  return HEB_EPOCH + day;
}
function hebMonthDays(y, m) { // m: Reingold's numbering, Nisan = 1
  var len = hebNewYear(y + 1) - hebNewYear(y);
  if (m === 2 || m === 4 || m === 6 || m === 10 || m === 13) return 29;
  if (m === 12 && !hebLeap(y)) return 29;
  if (m === 8 && !(len === 355 || len === 385)) return 29;
  if (m === 9 && (len === 353 || len === 383)) return 29;
  return 30;
}
function hebrewFromRd(rd) {
  var y = div(rd - HEB_EPOCH, 366) + 1; // lower bound, then walk forward
  while (hebNewYear(y + 1) <= rd) y++;
  var last = hebLeap(y) ? 13 : 12, m = 7, d = rd - hebNewYear(y);
  for (;;) { var ml = hebMonthDays(y, m); if (d < ml) break; d -= ml; m = m === last ? 1 : m + 1; }
  // the month as ICU counts it: Tishri = 1 ... Shevat = 5, Adar I = 6 (leap years only), Adar = 7, Nisan = 8 ... Elul = 13
  var leap = hebLeap(y), im = m >= 7 ? m - 6 : m + 7;
  if (m === 12 && leap) im = 6; else if (m === 12) im = 7; else if (m === 13) im = 7;
  return { y: y, m: im, d: d + 1, ly: leap };
}

// ---- Indian (Saka) -----------------------------------------------------------------------------------------------------------------------------
function indianFromDays(days) {
  var gy = civilFromDays(days)[0], sy = gy - 78, start = daysFromCivil(gy, 3, isLeap(gy) ? 21 : 22);
  if (days < start) { sy--; start = daysFromCivil(gy - 1, 3, isLeap(gy - 1) ? 21 : 22); }
  var doy = days - start, leap = isLeap(sy + 78), lens = [leap ? 31 : 30, 31, 31, 31, 31, 31, 30, 30, 30, 30, 30, 30], m = 0;
  while (doy >= lens[m]) { doy -= lens[m]; m++; }
  return [sy, m + 1, doy + 1];
}

// ---- Coptic and Ethiopic ----------------------------------------------------------------------------------------------------------------------
function copticLike(days, epoch) {
  var rd = days + 719163, y = div(4 * (rd - epoch) + 1463, 1461), start = epoch + 365 * (y - 1) + div(y, 4), m = div(rd - start, 30) + 1;
  return [y, m, rd - (start + 30 * (m - 1)) + 1];
}

// ---- the fields of a day ----------------------------------------------------------------------------------------------------------------------
// result: era (index into the calendar's era names), y (year of the era), m (the month number the pattern letter M counts), d,
// plus ly (leap year, Hebrew) where it matters
C.fields = function (days, cal) {
  var c, r;
  switch (cal) {
    case 'buddhist': c = hybrid(days); return { era: 0, y: c[0] + 543, m: c[1], d: c[2], gy: c[0] };
    case 'roc': c = hybrid(days); r = c[0] - 1911; return { era: r > 0 ? 1 : 0, y: r > 0 ? r : 1 - r, m: c[1], d: c[2], gy: c[0] };
    case 'japanese': return japanese(days);
    case 'persian': c = persianFromDays(days); return { era: 0, y: c[0], m: c[1], d: c[2] };
    case 'islamic-civil': c = islamicArith(days + 719163, 227015); return { era: 0, y: c[0], m: c[1], d: c[2] };
    case 'islamic-tbla': c = islamicArith(days + 719163, 227014); return { era: 0, y: c[0], m: c[1], d: c[2] };
    case 'islamic-umalqura': case 'islamic': case 'islamic-rgsa':
      c = islamicTable(cal === 'islamic-umalqura' ? 'um' : 'is', days) || islamicArith(days + 719163, 227015);
      return { era: 0, y: c[0], m: c[1], d: c[2] };
    case 'hebrew': r = hebrewFromRd(days + 719163); return { era: 0, y: r.y, m: r.m, d: r.d, ly: r.ly };
    case 'indian': c = indianFromDays(days); return { era: 0, y: c[0], m: c[1], d: c[2] };
    case 'coptic': c = copticLike(days, 103605); return c[0] > 0 ? { era: 1, y: c[0], m: c[1], d: c[2] } : { era: 0, y: 1 - c[0], m: c[1], d: c[2] };
    case 'ethiopic': c = copticLike(days, 2796); return c[0] > 0 ? { era: 1, y: c[0], m: c[1], d: c[2] } : { era: 0, y: c[0] + 5500, m: c[1], d: c[2] };
    case 'ethioaa': c = copticLike(days, 2796); return { era: 0, y: c[0] + 5500, m: c[1], d: c[2] };
  }
  c = civilFromDays(days);
  return { era: c[0] > 0 ? 1 : 0, y: c[0] > 0 ? c[0] : 1 - c[0], m: c[1], d: c[2], gy: c[0] };
};

// Japanese eras: start dates as [year, month, day] in the hybrid calendar, from CLDR's calendarData (global data "jpe")
function japanese(days) {
  var G = H.intl.G(), eras = G.jpe, c = hybrid(days), lo = 0, hi = eras.length - 1, key = c[0] * 10000 + c[1] * 100 + c[2];
  while (lo < hi) { var mid = (lo + hi + 1) >> 1; if (eras[mid][0] * 10000 + eras[mid][1] * 100 + eras[mid][2] <= key) lo = mid; else hi = mid - 1; }
  return { era: lo, y: c[0] - eras[lo][0] + 1, m: c[1], d: c[2], gy: c[0] };
}
C.daysOfRd = function (rd) { return rd - 719163; };
