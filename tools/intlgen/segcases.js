// Writes internal/intl/segment/testdata/node_segments.json: texts and the boundaries Node's Intl.Segmenter (ICU 77) finds in them at each
// granularity. segment_test.go compares the Go segmenter with them; the texts that need ICU's break dictionaries (Thai, Lao, Khmer, Burmese,
// Chinese, Japanese) are listed apart: Sweef cuts them where UAX #29 does (docs/web-platform.md).
//   node segcases.js > ../../internal/intl/segment/testdata/node_segments.json
'use strict';
const TEXTS = [
  'Hello, world! How are you? I am fine.', 'éa👨‍👩‍👧x🇺🇸🇩🇪y', "can't stop; 3.14 is pi. Mr. Smith went to Washington.", '"Quoted." (Parens.) Next sentence.',
  'a\r\nb\nc', '😀😀', 'Привет, мир! Как дела?', 'The U.S.A. is big... Really? Yes!', 'नमस्ते दुनिया। आप कैसे हैं?', 'مرحبا بالعالم. كيف حالك؟',
  'one-two three_four 1,234.56 foo@bar.com', 'é̂ a‍b क्ष', '한국어 문장입니다. 안녕하세요!', 'Ωμέγα και άλφα. Τι κάνεις;', 'שלום עולם. מה שלומך?',
  'Line one. Line two. Para two.', 'e.g. this, i.e. that; 12:30 p.m. on 3/4/2025.', '🏳️‍🌈 and 👍🏽 and 1️⃣', 'ǅ DŽ dž ß ẞ ﬁ',
];
const DICT = [
  'ภาษาไทยเป็นภาษาที่สวยงาม ฉันรักประเทศไทย', 'ສະບາຍດີ ຂ້ອຍຮັກປະເທດລາວ', 'ខ្ញុំស្រលាញ់ប្រទេសកម្ពុជា', 'ကျွန်တော် မြန်မာစကား ပြောတတ်ပါတယ်',
  '我喜欢吃苹果，也喜欢吃香蕉。北京是中国的首都。', '日本語のテキストを分割します。今日は天気がいいです。', 'ひらがなカタカナ漢字ＡＢＣ１２３',
];
const out = { cases: [], dictionary: [] };
for (const [list, key] of [[TEXTS, 'cases'], [DICT, 'dictionary']]) for (const text of list) for (const g of ['grapheme', 'word', 'sentence']) {
  const b = [];
  for (const s of new Intl.Segmenter('en', { granularity: g }).segment(text)) b.push(s.index);
  if (text.length) b.push(text.length);
  const words = g === 'word' ? [...new Intl.Segmenter('en', { granularity: g }).segment(text)].map((s) => !!s.isWordLike) : undefined;
  out[key].push({ text, granularity: g, boundaries: b, wordLike: words });
}
process.stdout.write(JSON.stringify(out) + '\n');
