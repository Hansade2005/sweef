// Compares Sweef's PluralRules, RelativeTimeFormat, ListFormat, DisplayNames, Collator and Segmenter with Node's.
//   node cmp-misc.js [plural|rtf|list|dn|coll|seg]
'use strict';
const { make, NativeIntl } = require('./harness');
const ours = make(['intlnum', 'intlmisc']);
const LOCALES = ['en-US', 'en-GB', 'de', 'fr', 'es', 'es-MX', 'pt-BR', 'it', 'nl', 'ru', 'pl', 'tr', 'ar', 'he', 'fa', 'hi', 'bn', 'id', 'th', 'ja', 'ko', 'zh-CN', 'zh-TW', 'sv', 'nb', 'fi', 'cs', 'uk', 'el', 'hu', 'ro', 'vi'];
let bad = 0, total = 0;
const only = process.argv[2];
function check(kind, loc, what, got, want) {
  total++;
  if (JSON.stringify(got) !== JSON.stringify(want)) { bad++; if (bad < 60) console.log(kind, loc, what, '\n   got ', JSON.stringify(got), '\n   want', JSON.stringify(want)); }
}
function run(kind, f) { if (only && only !== kind) return; f(); }
const tryit = (f) => { try { return f(); } catch (e) { return 'THROW ' + e.constructor.name; } };

run('plural', () => {
  for (const l of LOCALES) for (const type of ['cardinal', 'ordinal']) {
    const a = new ours.PluralRules(l, { type }), b = new NativeIntl.PluralRules(l, { type });
    check('plural', l, type + ' ro', a.resolvedOptions(), b.resolvedOptions());
    for (const n of [0, 1, 2, 3, 4, 5, 6, 7, 10, 11, 12, 13, 14, 20, 21, 22, 23, 25, 100, 101, 102, 111, 1000000, 1.5, 0.5, 2.5, 11.5, -1]) check('plural', l, type + ' ' + n, a.select(n), b.select(n));
  }
  const a = new ours.PluralRules('en', { minimumFractionDigits: 2 }), b = new NativeIntl.PluralRules('en', { minimumFractionDigits: 2 });
  for (const n of [1, 1.0, 1.5, 2]) check('plural', 'en', 'mfd2 ' + n, a.select(n), b.select(n));
  for (const l of ['en', 'ar', 'ru', 'fr']) for (const [x, y] of [[0, 1], [1, 2], [1, 5], [2, 11], [21, 22]]) check('plural', l, `range ${x}-${y}`, tryit(() => new ours.PluralRules(l).selectRange(x, y)), tryit(() => new NativeIntl.PluralRules(l).selectRange(x, y)));
});
run('rtf', () => {
  for (const l of LOCALES) for (const style of ['long', 'short', 'narrow']) for (const numeric of ['always', 'auto']) {
    const a = new ours.RelativeTimeFormat(l, { style, numeric }), b = new NativeIntl.RelativeTimeFormat(l, { style, numeric });
    check('rtf', l, 'ro', a.resolvedOptions(), b.resolvedOptions());
    for (const u of ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year', 'days']) for (const v of [-100, -2, -1, -0, 0, 1, 2, 3, 5, 11, 1.5, 1234567]) {
      check('rtf', l, `${style}/${numeric} ${v} ${u}`, tryit(() => a.format(v, u)), tryit(() => b.format(v, u)));
    }
    check('rtf', l, 'parts', a.formatToParts(-1234.5, 'day'), b.formatToParts(-1234.5, 'day'));
  }
});
run('list', () => {
  const lists = [[], ['a'], ['a', 'b'], ['a', 'b', 'c'], ['a', 'b', 'c', 'd']];
  for (const l of LOCALES) for (const type of ['conjunction', 'disjunction', 'unit']) for (const style of ['long', 'short', 'narrow']) {
    const a = new ours.ListFormat(l, { type, style }), b = new NativeIntl.ListFormat(l, { type, style });
    for (const x of lists) check('list', l, `${type}/${style} ${x.length}`, a.format(x), b.format(x));
    check('list', l, 'parts', a.formatToParts(['x', 'y', 'z']), b.formatToParts(['x', 'y', 'z']));
  }
});
run('dn', () => {
  const sets = { region: ['US', 'GB', 'DE', 'JP', '419', 'ZZ', '150'], language: ['en', 'en-US', 'en-GB', 'fr-CA', 'de-AT', 'zh-Hans', 'zh-Hant-TW', 'pt-BR', 'es-419', 'xx', 'ja', 'sr-Latn'], script: ['Latn', 'Cyrl', 'Arab', 'Hans', 'Hant', 'Xxxx'], currency: ['USD', 'EUR', 'JPY', 'GBP', 'XYZ'], calendar: ['gregory', 'buddhist', 'chinese', 'islamic'], dateTimeField: ['year', 'month', 'day', 'weekday', 'hour', 'minute', 'second', 'timeZoneName', 'quarter', 'era', 'dayPeriod', 'weekOfYear'] };
  for (const l of LOCALES) for (const type of Object.keys(sets)) for (const style of ['long', 'short', 'narrow']) for (const fallback of ['code', 'none']) {
    const a = new ours.DisplayNames(l, { type, style, fallback }), b = new NativeIntl.DisplayNames(l, { type, style, fallback });
    check('dn', l, 'ro', a.resolvedOptions(), b.resolvedOptions());
    for (const c of sets[type]) check('dn', l, `${type}/${style}/${fallback} ${c}`, tryit(() => a.of(c)), tryit(() => b.of(c)));
  }
  for (const c of ['en-US', 'en-GB']) check('dn', 'en', 'standard ' + c, new ours.DisplayNames('en', { type: 'language', languageDisplay: 'standard' }).of(c), new NativeIntl.DisplayNames('en', { type: 'language', languageDisplay: 'standard' }).of(c));
});
run('coll', () => {
  const words = ['a', 'A', 'á', 'b', 'B', 'z', 'Z', 'ä', 'å', 'ö', 'ø', 'æ', 'ñ', 'n', 'o', 'ß', 'ss', 'st', 'ch', 'c', 'h', 'č', 'ć', 'ç', 'ğ', 'g', 'ı', 'i', 'I', 'İ', 'ł', 'l', 'ś', 's', 'ž', '1', '2', '10', '9', 'a1', 'a10', 'a2', ' ', '-', '_', '.', 'a b', 'ab', 'a-b', 'a.b', 'résumé', 'resume', 'Resume', 'résume', 'Äpfel', 'Zebra', 'apple', 'Apple', 'APPLE', 'ａ', 'あ', 'ア', 'か', '漢', '한', 'α', 'б', 'я', 'ä1', 'Ö', 'ü', 'u', 'ú', '€', '$', '@'];
  const optsets = [{}, { sensitivity: 'base' }, { sensitivity: 'accent' }, { sensitivity: 'case' }, { numeric: true }, { caseFirst: 'upper' }, { ignorePunctuation: true }, { usage: 'search' }];
  const perLoc = {};
  let errs = 0;
  for (const l of ['en', 'de', 'fr', 'fr-CA', 'es', 'sv', 'da', 'nb', 'fi', 'pl', 'cs', 'tr', 'hu', 'ro', 'hr', 'ru', 'el', 'ja', 'ko', 'th']) for (const o of optsets) {
    const a = new ours.Collator(l, o), b = new NativeIntl.Collator(l, o);
    check('coll', l, JSON.stringify(o) + ' ro', a.resolvedOptions(), b.resolvedOptions());
    for (let i = 0; i < words.length; i++) for (let j = 0; j < words.length; j++) {
      const x = Math.sign(a.compare(words[i], words[j])), y = Math.sign(b.compare(words[i], words[j]));
      total++;
      if (x !== y) { bad++; perLoc[l] = (perLoc[l] || 0) + 1; if ((perLoc[l] <= 6) && (!process.argv[3] || process.argv[3] === l)) console.log('coll', l, JSON.stringify(o), JSON.stringify(words[i]), JSON.stringify(words[j]), 'got', x, 'want', y); }
    }
  }
  console.log('collation mismatches by locale', JSON.stringify(perLoc));
});
run('seg', () => {
  const texts = ['Hello, world! How are you? I am fine.', 'éa👨‍👩‍👧x🇺🇸🇩🇪y', "can't stop; 3.14 is pi. Mr. Smith went to Washington.", '日本語のテキスト', 'a\r\nb', '', '😀😀'];
  for (const gr of ['grapheme', 'word', 'sentence']) for (const t of texts) {
    const a = new ours.Segmenter('en', { granularity: gr }), b = new NativeIntl.Segmenter('en', { granularity: gr });
    check('seg', 'en', gr + ' ' + t, [...a.segment(t)], [...b.segment(t)]);
    for (const i of [0, 1, 3, 100]) check('seg', 'en', 'containing ' + i, a.segment(t).containing(i), b.segment(t).containing(i));
  }
});
console.log(`${bad} of ${total} differ`);
