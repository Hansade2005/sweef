// Writes internal/engine/testdata/intl_golden.json: expressions and the value Node's Intl (ICU 77, CLDR 47) gives
// for each. engine/intl_test.go evaluates the same expressions in Sweef's engine and compares.
//   node golden.js --keep-passing > ../../internal/engine/testdata/intl_golden.json
'use strict';
const D = 'Date.UTC(2025, 9, 14, 15, 4, 5, 678)', D2 = 'Date.UTC(2024, 1, 29, 0, 0, 0)';
const exprs = [];
const add = (e) => exprs.push(e);
for (const l of ['en-US', 'en-GB', 'de', 'fr', 'es', 'ja', 'ar', 'hi', 'ru', 'zh-CN', 'pt-BR', 'tr', 'ko', 'he', 'th', 'sv', 'pl']) {
  add(`new Intl.NumberFormat('${l}').format(1234567.891)`);
  add(`new Intl.NumberFormat('${l}', {style:'currency', currency:'EUR'}).format(-1234.5)`);
  add(`new Intl.NumberFormat('${l}', {style:'percent', maximumFractionDigits:1}).format(0.4567)`);
  add(`new Intl.NumberFormat('${l}', {notation:'compact'}).format(1234567)`);
  add(`new Intl.NumberFormat('${l}', {style:'unit', unit:'kilometer-per-hour', unitDisplay:'long'}).format(72)`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', dateStyle:'full', timeStyle:'long'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'America/New_York', dateStyle:'medium', timeStyle:'short'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'Asia/Tokyo', year:'numeric', month:'long', day:'numeric', weekday:'long'}).format(${D2})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'Europe/Paris', hour:'numeric', minute:'2-digit'}).format(${D})`);
  add(`new Intl.RelativeTimeFormat('${l}', {numeric:'auto'}).format(-1, 'day')`);
  add(`new Intl.RelativeTimeFormat('${l}').format(3, 'week')`);
  add(`new Intl.ListFormat('${l}').format(['a','b','c'])`);
  add(`new Intl.PluralRules('${l}').select(2)`);
  add(`new Intl.PluralRules('${l}', {type:'ordinal'}).select(3)`);
  add(`new Intl.DisplayNames('${l}', {type:'region'}).of('JP')`);
  add(`new Intl.DisplayNames('${l}', {type:'language'}).of('fr')`);
  add(`['z','a','ä','Z','A','é','e','10','9'].sort(new Intl.Collator('${l}').compare).join(',')`);
}
add(`new Intl.NumberFormat('en', {style:'currency', currency:'USD', currencyDisplay:'name'}).format(2)`);
add(`new Intl.NumberFormat('en', {minimumFractionDigits:2, maximumFractionDigits:2, roundingMode:'floor'}).format(1.239)`);
add(`new Intl.NumberFormat('en', {maximumSignificantDigits:3}).formatToParts(123456).map(p=>p.type+':'+p.value).join('|')`);
add(`new Intl.NumberFormat('en').formatRange(3, 5)`);
add(`new Intl.NumberFormat('en-IN').format(12345678)`);
add(`new Intl.NumberFormat('de-CH').format(1234567.5)`);
add(`new Intl.DateTimeFormat('en', {timeZone:'UTC', dateStyle:'short'}).formatRange(${D}, ${D2})`);
add(`new Intl.DateTimeFormat('en-US', {timeZone:'America/Los_Angeles', timeZoneName:'short', hour:'numeric'}).format(${D})`);
add(`new Intl.DateTimeFormat('en-US', {timeZone:'Asia/Kolkata', timeZoneName:'longOffset', hour:'numeric'}).format(${D})`);
add(`JSON.stringify(new Intl.DateTimeFormat('en-US', {timeZone:'UTC'}).formatToParts(${D}))`);
add(`JSON.stringify(new Intl.DateTimeFormat('en-US', {timeZone:'UTC', hour12:false, timeStyle:'medium'}).resolvedOptions())`);
add(`new Intl.DateTimeFormat('en-US', {timeZone:'UTC', hourCycle:'h23', hour:'numeric', minute:'numeric'}).format(${D})`);
add(`new Date(${D}).toLocaleString('de-DE', {timeZone:'UTC'})`);
add(`new Date(${D}).toLocaleDateString('en-GB', {timeZone:'UTC'})`);
add(`new Date(${D}).toLocaleTimeString('en-US', {timeZone:'UTC'})`);
add(`(1234.5).toLocaleString('de-DE')`);
add(`(1234.5).toLocaleString('en-US', {style:'currency', currency:'JPY'})`);
add(`123456789n.toLocaleString('en-US')`);
add(`'a'.localeCompare('b')`);
add(`'ä'.localeCompare('z', 'de')`);
add(`'ä'.localeCompare('z', 'sv')`);
add(`'a'.localeCompare('A', undefined, {sensitivity:'base'})`);
add(`['item10','item9','item1'].sort(new Intl.Collator('en',{numeric:true}).compare).join()`);
add(`[...new Intl.Segmenter('en', {granularity:'word'}).segment('Hello, world! 3.14')].map(s=>s.segment+(s.isWordLike?'*':'')).join('|')`);
add(`[...new Intl.Segmenter('en').segment('e\\u0301a👨‍👩‍👧🇺🇸')].map(s=>s.segment.length).join()`);
add(`[...new Intl.Segmenter('en', {granularity:'sentence'}).segment('Hi there. How are you? Fine!')].map(s=>s.segment).join('|')`);
add(`Intl.getCanonicalLocales(['EN-us', 'zh-hans-cn']).join()`);
add(`Intl.supportedValuesOf('calendar').length > 10`);
add(`Intl.NumberFormat.supportedLocalesOf(['en-US', 'xx', 'de']).join()`);
add(`new Intl.Locale('en-Latn-US-u-ca-gregory').maximize().toString()`);
add(`String(Intl.DateTimeFormat().resolvedOptions().locale).length > 0`);
add(`(() => { try { new Intl.NumberFormat('en', {style:'currency'}); } catch (e) { return e.constructor.name; } })()`);
add(`(() => { try { new Intl.DateTimeFormat('en', {timeZone:'Nope/Zone'}); } catch (e) { return e.constructor.name + ':' + e.message; } })()`);
add(`(() => { try { Intl.PluralRules(); } catch (e) { return e.constructor.name + ':' + e.message; } })()`);
add(`Object.prototype.toString.call(new Intl.NumberFormat())`);

// ---- the parity work: every shipped locale, the calendars, zone names, numbering systems, ranges, collation ---------------------------------------------
const ALL = ['en', 'en-US', 'en-001', 'en-GB', 'en-AU', 'en-IN', 'en-CA', 'es', 'es-ES', 'es-419', 'es-MX', 'es-US', 'pt', 'pt-BR', 'pt-PT', 'fr', 'fr-FR', 'fr-CA', 'de', 'de-DE', 'de-AT', 'de-CH', 'it', 'nl', 'ru', 'uk', 'pl', 'cs', 'sk', 'hu', 'ro', 'bg', 'hr', 'sr', 'el', 'tr',
  'ar', 'ar-SA', 'ar-EG', 'ar-AE', 'he', 'fa', 'hi', 'bn', 'ur', 'id', 'ms', 'vi', 'th', 'ja', 'ko', 'zh', 'zh-CN', 'zh-Hant', 'zh-TW', 'zh-Hant-HK', 'zh-HK', 'sv', 'da', 'nb', 'fi',
  'ca', 'ta', 'te', 'mr', 'fil', 'sw', 'lt', 'lv', 'et', 'sl', 'af', 'gu', 'kn', 'ml', 'pa', 'ne', 'si', 'km', 'my', 'az', 'kk', 'uz'];
const D3 = 'Date.UTC(2025, 9, 16, 9, 30, 0)', D4 = 'Date.UTC(2025, 9, 14, 17, 45, 0)', D5 = 'Date.UTC(2026, 1, 3, 8, 0, 0)';
for (const l of ALL) {
  // the date-time separator and the patterns of every locale
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', year:'numeric', month:'short', day:'numeric', hour:'numeric', minute:'2-digit'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', dateStyle:'long', timeStyle:'short'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', weekday:'long', month:'long', day:'numeric', hour:'2-digit', minute:'2-digit', hour12:false}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'Europe/Berlin', hour:'numeric', dayPeriod:'long'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'America/Los_Angeles', timeZoneName:'long', hour:'numeric'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'America/New_York', timeStyle:'full'}).format(${D})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'Asia/Tokyo', timeZoneName:'shortGeneric', hour:'numeric'}).format(${D})`);
  // ranges
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', year:'numeric', month:'short', day:'numeric'}).formatRange(${D}, ${D3})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', dateStyle:'medium'}).formatRange(${D}, ${D5})`);
  add(`new Intl.DateTimeFormat('${l}', {timeZone:'UTC', hour:'numeric', minute:'2-digit'}).formatRange(${D}, ${D4})`);
  add(`JSON.stringify(new Intl.DateTimeFormat('${l}', {timeZone:'UTC', month:'long', day:'numeric', hour:'numeric', minute:'numeric'}).formatRangeToParts(${D}, ${D4}).map(p=>p.type+':'+p.value+':'+p.source))`);
  // numbers
  add(`new Intl.NumberFormat('${l}', {style:'currency', currency:'USD', notation:'compact'}).format(1234567)`);
  add(`new Intl.NumberFormat('${l}', {notation:'compact', compactDisplay:'long'}).format(98765432)`);
  add(`new Intl.NumberFormat('${l}', {style:'percent', notation:'compact'}).format(123.45)`);
  add(`new Intl.NumberFormat('${l}', {style:'currency', currency:'EUR'}).formatRange(1234.5, 6789.25)`);
  add(`new Intl.NumberFormat('${l}', {style:'unit', unit:'kilometer', unitDisplay:'long'}).formatRange(3, 5)`);
  add(`JSON.stringify(new Intl.NumberFormat('${l}', {style:'currency', currency:'CHF', signDisplay:'always'}).formatToParts(-1234.5))`);
  add(`new Intl.NumberFormat('${l}', {style:'currency', currency:'JPY', currencySign:'accounting'}).format(-5000)`);
  add(`new Intl.NumberFormat('${l}', {maximumFractionDigits:2, roundingMode:'halfEven', roundingPriority:'lessPrecision', maximumSignificantDigits:3}).format(1234.5678)`);
}
for (const nu of ['arab', 'arabext', 'beng', 'deva', 'thai', 'gujr', 'guru', 'knda', 'khmr', 'laoo', 'mlym', 'mymr', 'orya', 'tamldec', 'telu', 'tibt', 'hanidec', 'fullwide']) {
  for (const l of ['en', 'de', 'fr', 'ar', 'hi', 'ja']) {
    add(`new Intl.NumberFormat('${l}-u-nu-${nu}', {style:'currency', currency:'EUR'}).format(-1234567.891)`);
    add(`new Intl.NumberFormat('${l}', {numberingSystem:'${nu}', style:'percent', maximumFractionDigits:1}).format(0.4567)`);
  }
  add(`new Intl.NumberFormat('en', {numberingSystem:'${nu}', notation:'scientific'}).format(-12345.6)`);
  add(`new Intl.DateTimeFormat('en', {timeZone:'UTC', numberingSystem:'${nu}', dateStyle:'medium', timeStyle:'short'}).format(${D})`);
}
// calendars
for (const [cal, ls] of Object.entries({ buddhist: ['en', 'th'], japanese: ['en', 'ja'], roc: ['en', 'zh-TW'], persian: ['en', 'fa'], 'islamic-umalqura': ['en', 'ar-SA'], 'islamic-civil': ['en', 'ar'], 'islamic-tbla': ['en'], islamic: ['en', 'ar'], hebrew: ['en', 'he'], indian: ['en'], coptic: ['en'], ethiopic: ['en'], ethioaa: ['en'] })) {
  for (const l of ls) for (const dd of [D, D2, 'Date.UTC(1999, 11, 31, 12)', 'Date.UTC(2100, 5, 15, 12)']) {
    add(`new Intl.DateTimeFormat('${l}-u-ca-${cal}', {timeZone:'UTC', dateStyle:'full'}).format(${dd})`);
    add(`new Intl.DateTimeFormat('${l}-u-ca-${cal}', {timeZone:'UTC', year:'numeric', month:'numeric', day:'numeric', era:'short'}).format(${dd})`);
  }
  add(`JSON.stringify(new Intl.DateTimeFormat('en-u-ca-${cal}', {timeZone:'UTC', dateStyle:'long'}).formatToParts(${D}))`);
}
// time zone names
for (const z of ['America/New_York', 'Europe/London', 'Asia/Kolkata', 'Australia/Sydney', 'America/Sao_Paulo', 'Pacific/Auckland', 'Africa/Cairo', 'Asia/Tehran', 'UTC']) {
  for (const l of ['en', 'de', 'fr', 'es', 'ja', 'zh', 'ar', 'ru', 'pt']) for (const n of ['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric']) {
    add(`new Intl.DateTimeFormat('${l}', {timeZone:'${z}', timeZoneName:'${n}', hour:'numeric'}).format(${D})`);
  }
}
// collation
for (const [l, words] of Object.entries({
  zh: ['中文', '汉字', '北京', '上海', '字', '上', '文', '海'], 'zh-Hant': ['中文', '漢字', '北京', '上海', '字', '上', '文', '海'], 'zh-TW': ['臺灣', '國語', '學', '中', '字'], ja: ['あ', 'ア', 'か', 'ガ', 'カ', 'ー', 'ん'],
  ko: ['가', '나', '다', '한', '국어', 'a', 'Z'], ar: ['ب', 'ا', 'ت', 'أ', 'ث', 'ء', 'z', 'ي'], he: ['ב', 'א', 'ת', 'ך', 'כ', 'a'], hi: ['क', 'अ', 'ख', 'आ', 'ज्ञ', 'a'], th: ['ก', 'เก', 'ข', 'แข', 'ไก', 'a', 'ฮ'],
  da: ['aa', 'å', 'z', 'æ', 'ø', 'Aa', 'ab'], nb: ['aa', 'å', 'z', 'æ', 'ø'], sv: ['å', 'ä', 'ö', 'z', 'a'], de: ['ä', 'a', 'z', 'ss', 'ß'], es: ['n', 'ñ', 'o', 'nz'], ru: ['я', 'а', 'ё', 'е', 'a'], el: ['ω', 'α', 'ά', 'β', 'a'], tr: ['i', 'ı', 'İ', 'I', 'ç', 'c'], pl: ['z', 'ż', 'ź', 'ł', 'l'],
  bn: ['ক', 'অ', 'খ', 'আ', 'a'], ta: ['க', 'அ', 'ங', 'ஆ'], te: ['క', 'అ', 'ఖ', 'ఆ'], my: ['က', 'အ', 'ခ'], km: ['ក', 'អ', 'ខ'], fa: ['ب', 'ا', 'پ', 'ت'], uk: ['я', 'а', 'і', 'ї', 'є', 'г', 'ґ'],
})) {
  add(`${JSON.stringify(words)}.sort(new Intl.Collator('${l}').compare).join(',')`);
  add(`${JSON.stringify(words)}.sort(new Intl.Collator('${l}', {sensitivity:'base'}).compare).join(',')`);
  add(`${JSON.stringify(words)}.sort(new Intl.Collator('${l}', {caseFirst:'upper'}).compare).join(',')`);
  add(`new Intl.Collator('${l}').resolvedOptions().locale`);
}
for (const t of ['en-GB', 'fr-CA', 'fr-CH', 'de-AT', 'de-CH', 'es-MX', 'pt-BR', 'zh-CN', 'zh-HK', 'ar-EG', 'ar-SA', 'sv-FI']) add(`new Intl.Collator('${t}').resolvedOptions().locale`);
add(`['a1', 'a10', 'a2', 'A-1', 'a 1', 'b'].sort(new Intl.Collator('en', {numeric:true}).compare).join(',')`);
add(`['a-b', 'ab', 'a b', 'a_b', 'aB'].sort(new Intl.Collator('en', {ignorePunctuation:true}).compare).join(',')`);
add(`['été', 'ete', 'Été', 'étè'].sort(new Intl.Collator('fr-CA').compare).join(',')`);
add(`['a', 'A', 'b', 'B'].sort(new Intl.Collator('en', {caseFirst:'upper'}).compare).join(',')`);
// segmentation of scripts without spaces: the engine has no dictionary (docs/web-platform.md)
for (const t of ['ภาษาไทยเป็นภาษาที่สวยงาม ฉันรักประเทศไทย', '我喜欢吃苹果，也喜欢吃香蕉。北京是中国的首都。', '日本語のテキストを分割します。', 'ສະບາຍດີ ຂ້ອຍຮັກປະເທດລາວ', 'ខ្ញុំស្រលាញ់ប្រទេសកម្ពុជា', 'ကျွန်တော် မြန်မာစကား ပြောတတ်ပါတယ်']) {
  add(`[...new Intl.Segmenter('th', {granularity:'word'}).segment(${JSON.stringify(t)})].map(s=>s.segment).join('|') /* dictionary */`);
}
// --keep-passing: of the cases above only those Sweef's own modules (run in this Node by harness.js) get right are written, so the file is a
// regression guard; the others are what tools/intlgen/matrix.js measures and docs/web-platform.md lists. Segmentation cases stay in:
// the engine segments in Go (internal/intl/segment), which the harness cannot run, and intl_test.go names the ones it cannot match.
const keep = process.argv.includes('--keep-passing');
let ours = null;
if (keep) ours = require('./harness').make(['intlnum', 'intldate', 'intlmisc']);
const norm = (e, v) => (typeof v === 'string' && /DateTimeFormat|Date\(/.test(e) ? v.replace(/\u202f/g, ' ') : v);
const out = [];
let dropped = 0;
for (const e of exprs) {
  let v; try { v = eval(e); } catch (err) { continue; }
  if (keep && !/Segmenter/.test(e)) {
    let w; try { w = new Function('Intl', 'return ' + e)(ours); } catch (err) { w = 'THROW ' + err.message; }
    if (JSON.stringify(norm(e, w)) !== JSON.stringify(norm(e, v))) { dropped++; continue; }
  }
  out.push({ e, v });
}
if (keep) process.stderr.write(`golden: ${out.length} cases kept, ${dropped} left to the matrix\n`);
process.stdout.write('[' + out.map((x) => JSON.stringify(x)).join(',\n') + ']\n');
