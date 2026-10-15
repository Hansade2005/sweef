// Visual fidelity and interaction parity: Sweef vs Chromium (Playwright) on the same pages, viewports and
// scripted interactions. For every state it saves both screenshots, compares them with bench/shotdiff
// (MAE, share of differing pixels, SSIM) and writes a small side-by-side PNG, and it compares the page state
// (URL, title, the state check). Results: <out>/fidelity.json, side-by-sides under <out>/<site>/.
//
//   node fidelity.mjs --sweef /path/to/sweef --shotdiff /path/to/shotdiff --out ../results/shots [--only t3a]
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { chromium } from 'playwright-core';
import { CHROMIUM, sleep, rmTree } from './lib.mjs';

const opt = { sweef: process.env.SWEEF || 'sweef', shotdiff: 'shotdiff', out: '../results/shots', only: '', settleMs: 2000, keep: false };
for (let i = 2; i < process.argv.length; i++) {
  const a = process.argv[i], nx = () => process.argv[++i];
  if (a === '--sweef') opt.sweef = nx(); else if (a === '--shotdiff') opt.shotdiff = nx(); else if (a === '--out') opt.out = nx();
  else if (a === '--only') opt.only = nx(); else if (a === '--settle-ms') opt.settleMs = +nx(); else if (a === '--keep-raw') opt.keep = true;
}

// Each state: an action (none for the first), then both browsers wait settleMs and take a viewport screenshot.
// check: a regex the page state (URL + innerText) must match in both.
const SITES = [
  { id: 't3a', url: 'https://the3rdacademy.com/', states: [
    { name: 'load', check: 'Beyond\\s+Credentials' },
    { name: 'scroll', act: { scroll: 2400 }, check: 'Beyond\\s+Credentials' },
    { name: 'nav-platform', act: { click: 'header a[href="/platform"]' }, check: '/platform[\\s\\S]*Future\\s+of\\s+Workplace', desktopOnly: true },
    { name: 'menu', act: { click: 'text=Open menu' }, check: 'Platform', mobileOnly: true },
  ] },
  { id: 'pipilot', url: 'https://www.pipilot.dev/', states: [
    { name: 'load', check: 'Describe\\s+it\\.' },
    { name: 'scroll', act: { scroll: 2400 }, check: 'Describe\\s+it\\.' },
    { name: 'nav-pricing', act: { click: 'a[href="/pricing"]' }, check: '/pricing[\\s\\S]*Start\\s+free', desktopOnly: true },
    { name: 'cta', act: { nav: 'https://www.pipilot.dev/', then: { desktop: 'text=Start building free', mobile: 'text=Start free' } }, check: '/app[\\s\\S]*sign\\s+in' },
  ] },
  { id: 'zaide', url: 'https://zaide.com/', states: [{ name: 'load', check: 'zaide' }] },
];
const VIEWPORTS = [{ id: 'desktop', w: 1280, h: 800 }, { id: 'mobile', w: 390, h: 844 }];

const pick = (v, vp) => (typeof v === 'string' ? v : v[vp.id]);
const stateText = 'location.href + "\\n" + document.title + "\\n" + document.body.innerText';

async function chromiumRun(site, vp, dir) {
  const b = await chromium.launch({ executablePath: CHROMIUM, headless: true, args: ['--no-sandbox'] });
  const out = [];
  try {
    const page = await b.newPage({ viewport: { width: vp.w, height: vp.h } });
    for (const st of site.states) {
      if ((st.desktopOnly && vp.id !== 'desktop') || (st.mobileOnly && vp.id !== 'mobile')) continue;
      const r = { state: st.name };
      try {
        if (!st.act) await page.goto(site.url, { waitUntil: 'load', timeout: 20000 });
        else if (st.act.scroll) await page.evaluate((y) => window.scrollTo(0, y), st.act.scroll);
        else if (st.act.click) await page.click(st.act.click, { timeout: 10000 });
        else if (st.act.nav) { await page.goto(st.act.nav, { waitUntil: 'load', timeout: 20000 }); await page.click(pick(st.act.then, vp), { timeout: 10000 }); }
        await page.waitForTimeout(opt.settleMs);
        r.shot = path.join(dir, `${site.id}-${vp.id}-${st.name}-chromium.png`);
        await page.screenshot({ path: r.shot });
        const txt = await page.evaluate(stateText);
        r.url = page.url(); r.check = new RegExp(st.check, 'i').test(txt); r.title = await page.title();
      } catch (e) { r.error = String(e.message || e).split('\n')[0].slice(0, 200); }
      out.push(r);
    }
  } finally { await b.close(); }
  return out;
}

function sweefRun(site, vp, dir) {
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-fid-'));
  const env = { ...process.env, SWEEF_HOME: home, SWEEF_CACHE_DIR: path.join(home, 'cache') };
  const run = (...a) => spawnSync(opt.sweef, [...a, '--timeout', '30s'], { env, encoding: 'utf8', timeout: 60000 });
  const out = [];
  try {
    run('open', 'about:blank'); // the viewport belongs to a tab: make one, size it, then load the page in it
    run('set', 'viewport', String(vp.w), String(vp.h));
    for (const st of site.states) {
      if ((st.desktopOnly && vp.id !== 'desktop') || (st.mobileOnly && vp.id !== 'mobile')) continue;
      const r = { state: st.name };
      let res;
      if (!st.act) res = run('open', site.url);
      else if (st.act.scroll) res = run('eval', `window.scrollTo(0, ${st.act.scroll})`);
      else if (st.act.click) res = run('click', st.act.click);
      else if (st.act.nav) { res = run('open', st.act.nav); if (res.status === 0) res = run('click', pick(st.act.then, vp)); }
      if (res.status !== 0) r.error = (res.stdout + res.stderr).trim().split('\n')[0].slice(0, 200);
      run('wait', String(opt.settleMs));
      r.shot = path.join(dir, `${site.id}-${vp.id}-${st.name}-sweef.png`);
      const s = run('screenshot', r.shot);
      if (s.status !== 0) r.shot_error = (s.stdout + s.stderr).trim().slice(0, 200);
      const e = run('eval', stateText);
      let txt = e.stdout.trim(); try { txt = JSON.parse(txt); } catch { /* raw */ }
      r.check = new RegExp(st.check, 'i').test(txt);
      const u = run('get', 'url'); r.url = u.stdout.trim();
      const t = run('get', 'title'); r.title = t.stdout.trim();
      out.push(r);
    }
  } finally {
    run('close');
    rmTree(home);
  }
  return out;
}

const raw = fs.mkdtempSync(path.join(os.tmpdir(), 'fid-raw-'));
const results = [];
fs.mkdirSync(opt.out, { recursive: true });
for (const site of SITES) {
  if (opt.only && !opt.only.split(',').includes(site.id)) continue;
  const sdir = path.join(opt.out, site.id);
  fs.mkdirSync(sdir, { recursive: true });
  for (const vp of VIEWPORTS) {
    const c = await chromiumRun(site, vp, raw);
    await sleep(500);
    const s = sweefRun(site, vp, raw);
    for (const cr of c) {
      const sr = s.find((x) => x.state === cr.state) || {};
      const row = { site: site.id, viewport: vp.id, state: cr.state, chromium: { url: cr.url, check: cr.check, error: cr.error }, sweef: { url: sr.url, check: sr.check, error: sr.error || sr.shot_error } };
      if (cr.shot && sr.shot && fs.existsSync(cr.shot) && fs.existsSync(sr.shot)) {
        const side = path.join(sdir, `${vp.id}-${cr.state}.png`);
        const d = spawnSync(opt.shotdiff, ['-a', cr.shot, '-b', sr.shot, '-out', side, '-scale', '0.4'], { encoding: 'utf8' });
        try { row.diff = JSON.parse(d.stdout.trim()); } catch { row.diff_error = (d.stderr || '').slice(0, 200); }
      }
      row.parity = !!(cr.check && sr.check && !cr.error && !sr.error);
      results.push(row);
      console.error(`${site.id.padEnd(8)} ${vp.id.padEnd(8)} ${cr.state.padEnd(14)} chromium=${cr.check ? 'ok' : 'FAIL'} sweef=${sr.check ? 'ok' : 'FAIL'} ssim=${row.diff ? row.diff.ssim : '-'} diff%=${row.diff ? row.diff.diff_pct : '-'}`);
    }
  }
}
fs.writeFileSync(path.join(opt.out, 'fidelity.json'), JSON.stringify({ when: new Date().toISOString(), settle_ms: opt.settleMs, results }, null, 1));
if (!opt.keep) rmTree(raw); else console.error('raw screenshots kept in ' + raw);
