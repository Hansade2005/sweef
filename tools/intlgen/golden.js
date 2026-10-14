// Writes internal/engine/testdata/intl_golden.json: expressions and the value Node's Intl (ICU 77, CLDR 47) gives
// for each. engine/intl_test.go evaluates the same expressions in Sweef's engine and compares.
//   node golden.js > ../../internal/engine/testdata/intl_golden.json
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
const out = [];
for (const e of exprs) {
  let v; try { v = eval(e); } catch (err) { continue; }
  out.push({ e, v });
}
process.stdout.write('[' + out.map((x) => JSON.stringify(x)).join(',\n') + ']\n');
