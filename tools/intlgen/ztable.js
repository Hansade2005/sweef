// ztable.js <locale>: which zone-bearing pattern Node picks for hour/minute/second widths x zone styles (to infer ICU's distances)
const loc = process.argv[2] || 'ca';
const v = Date.UTC(2025, 9, 14, 15, 4, 5, 678);
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
for (const z of ['short', 'long', 'shortOffset', 'longOffset', 'shortGeneric', 'longGeneric'])
  for (const h of ['numeric', '2-digit']) for (const m of ['numeric', '2-digit']) for (const s of [undefined, 'numeric', '2-digit']) {
    const o = { hour: h, minute: m, timeZoneName: z, timeZone: 'America/St_Johns', hourCycle: 'h23' };
    if (s) o.second = s;
    console.log((z + '       ').slice(0, 13), 'h:' + h.slice(0, 1), 'm:' + m.slice(0, 1), 's:' + (s ? s.slice(0, 1) : '-'), '|', esc(new Intl.DateTimeFormat(loc, o).format(v)));
  }
