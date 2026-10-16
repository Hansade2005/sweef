'use strict';
// Real-daemon tests: a local HTTP fixture and a real `sweef serve` (no mocks).
// SWEEF_BIN must point at a sweef binary (the Makefile target builds one).
const test = require('node:test');
const assert = require('node:assert/strict');
const http = require('node:http');
const os = require('node:os');
const fs = require('node:fs');
const path = require('node:path');
const { execFile } = require('node:child_process');
const run = require('node:util').promisify(execFile);
const { Sweef, RpcError } = require('..');

const pages = {
  '/': `<!doctype html><title>Acme</title><nav><a href="/login">Sign in</a></nav><h1>Ship faster</h1>
<button id="cta" onclick="document.getElementById('out').textContent='clicked'">Start</button><p id="out"></p>
<button id="ask" onclick="document.title = confirm('sure?') ? 'yes' : 'no'">Ask</button>
<script>console.log('ready')</script>`,
  '/login': `<!doctype html><title>Sign in</title><form action="/welcome" method="get">
<label for="email">Email</label><input id="email" name="email"><input id="keys">
<label><input type="checkbox" id="remember"> Remember me</label>
<select id="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
<button type="submit">Sign in</button></form>`,
  '/welcome': null,
  '/regress': `<!doctype html><title>
    Tester   regressions
</title><body style="margin:0"><form id="checkboxes"><input type="checkbox" id="c1"> one<br><input type="checkbox" id="c2" checked> two</form>
<input id="user" style="position:absolute;left:10px;top:100px;width:200px;height:30px"><input id="pass" style="position:absolute;left:10px;top:150px;width:200px;height:30px">
<select id="dropdown" style="position:absolute;left:10px;top:200px"><option value="" disabled selected>Please select an option</option><option value="1">Option 1</option><option value="2">Option 2</option></select>
<script>
var dd = document.getElementById('dropdown');
dd.onchange = function (e) {
  var o = dd.getElementsByTagName('option');
  for (var i = 0; i < o.length; i++) o[i].removeAttribute('selected');
  document.querySelector("#dropdown option[value='" + e.target.value + "']").setAttribute('selected', 'selected');
};
</script>`,
};

let server, base, sw;

test.before(async () => {
  server = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    if (u.pathname === '/api') { res.setHeader('Content-Type', 'application/json'); res.end('{"real":true}'); return; }
    if (u.pathname === '/cookie') { res.setHeader('Set-Cookie', 'sid=abc; Path=/'); res.end('<title>c</title>ok'); return; }
    const body = u.pathname === '/welcome' ? `<title>Welcome</title><h1>Welcome ${u.searchParams.get('email')}</h1>` : pages[u.pathname];
    if (body == null) { res.statusCode = 404; res.end('nope'); return; }
    res.setHeader('Content-Type', 'text/html; charset=utf-8');
    res.end(body);
  });
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  base = `http://127.0.0.1:${server.address().port}`;
  sw = await Sweef.launch();
});

test.after(async () => {
  if (sw) await sw.close();
  server.close();
});

test('navigate, title, url, snapshot refs, click', async () => {
  const page = await sw.newPage();
  const r = await page.goto(base + '/');
  assert.equal(r.title, 'Acme');
  assert.equal(await page.title(), 'Acme');
  assert.equal(await page.url(), base + '/');
  const snap = await page.snapshot();
  const m = snap.match(/button "Start" \[ref=(e\d+)\]/);
  assert.ok(m, snap);
  await page.click(m[1]);
  assert.equal(await page.locator('#out').textContent(), 'clicked');
  assert.match(await page.snapshot({ delta: true }), /clicked/);
  assert.equal(await page.snapshot({ delta: true }), '(no changes since last snapshot)');
  const msgs = await page.consoleMessages();
  assert.ok(msgs.some((e) => e.data.text === 'ready'));
  await page.close();
});

test('forms: fill, check, select, keyboard, getByRole, press Enter', async () => {
  const page = await sw.newPage(base + '/login');
  await page.getByLabel('Email').fill('ada@example.com');
  assert.equal(await page.locator('#email').inputValue(), 'ada@example.com');
  await page.check('#remember');
  assert.equal(await page.locator('#remember').isChecked(), true);
  await page.selectOption('#plan', 'Pro');
  await page.focus('#keys');
  await page.keyboard.type('hi');
  assert.equal(await page.evaluate(() => document.querySelector('#keys').value), 'hi');
  assert.equal(await page.evaluate((s) => document.querySelector(s).value, '#plan'), 'pro');
  assert.equal(await page.getByRole('button', { name: 'Sign in' }).count(), 1);
  await page.press('#email', 'Enter');
  await page.waitForURL('**/welcome**', { timeout: 5000 });
  assert.equal(await page.textContent('h1'), 'Welcome ada@example.com');
  await page.goBack();
  assert.equal(await page.title(), 'Sign in');
  await page.close();
});

test('tester regressions: title, first/last/nth, click then type, select with a rewriting handler', async () => {
  const page = await sw.newPage(`${base}/regress`);
  assert.equal(await page.title(), 'Tester regressions');
  const boxes = page.locator('input[type=checkbox]');
  assert.equal(await boxes.count(), 2);
  assert.equal(await boxes.first.count(), 1);
  await boxes.first.check();
  assert.equal(await page.locator('#c1').isChecked(), true);
  assert.equal(await boxes.last.isChecked(), true);
  assert.equal(await boxes.nth(1).getAttribute('id'), 'c2');
  assert.equal(await page.locator('#checkboxes').locator('input').nth(-1).getAttribute('id'), 'c2');
  await page.click('#user');
  await page.keyboard.type('tomsmith');
  await page.keyboard.press('Tab');
  await page.keyboard.type('secret');
  await page.mouse.click(50, 115);
  await page.keyboard.type('!');
  assert.equal(await page.evaluate("document.querySelector('#user').value + '|' + document.querySelector('#pass').value"), 'tomsmith!|secret');
  await page.selectOption('#dropdown', '2');
  assert.equal(await page.evaluate("(() => { const s = document.querySelector('#dropdown'); return s.value + '|' + s.selectedIndex })()"), '2|2');
  await page.selectOption('#dropdown', 'Option 1');
  assert.equal(await page.evaluate("document.querySelector('#dropdown').value"), '1');
  await page.close();
});

test('screenshot is a PNG with the viewport size', async () => {
  const page = await sw.newPage(base + '/');
  await page.setViewportSize({ width: 800, height: 600 });
  const png = await page.screenshot();
  assert.equal(png.subarray(1, 4).toString(), 'PNG');
  assert.equal(png.readUInt32BE(16), 800);
  assert.equal(png.readUInt32BE(20), 600);
  const file = path.join(os.tmpdir(), `sweef-sdk-${process.pid}.png`);
  await page.screenshot({ path: file, fullPage: true });
  assert.ok(fs.statSync(file).size > 100);
  fs.unlinkSync(file);
  assert.match(await page.screenshotText(), /Ship faster/);
  await page.close();
});

test('batch, routes, dialogs, waits', async () => {
  const page = await sw.newPage();
  const r = await page.batch([`open ${base}/login`, 'fill #email bo@x.io', { cmd: 'click', args: ['button[type=submit]'] },
    'expect url /welcome', { method: 'Page.title' }], { observe: 'interactive' });
  assert.equal(r.ok, true, JSON.stringify(r.steps));
  assert.equal(r.steps[4].result.title, 'Welcome');
  await page.route('**/api', { json: { mocked: 1 } });
  assert.deepEqual(await page.evaluate(`fetch('${base}/api').then(r => r.json())`), { mocked: 1 });
  assert.equal((await page.routes()).length, 1);
  await page.unrouteAll();
  assert.deepEqual(await page.evaluate(`fetch('${base}/api').then(r => r.json())`), { real: true });
  await page.goto(base + '/');
  await page.armDialog('accept');
  await page.click('#ask');
  assert.equal(await page.title(), 'yes');
  assert.ok((await page.dialogs()).length >= 1);
  const t0 = Date.now();
  await page.waitForTimeout(50);
  assert.ok(Date.now() - t0 < 1000);
  await assert.rejects(page.waitForSelector('#never', { timeout: 300 }), RpcError);
  await assert.rejects(page.click('#missing'), (e) => e instanceof RpcError && /missing/.test(e.message));
  await page.close();
});

test('contexts isolate cookies', async () => {
  const ctx = await sw.newContext({ name: 'iso' });
  const p = await ctx.newPage(base + '/cookie');
  assert.ok((await ctx.cookies()).some((c) => c.name === 'sid'));
  const other = await sw.newContext({ name: 'other' });
  assert.equal((await other.cookies()).length, 0);
  await p.close();
  await ctx.close();
  await other.close();
  const v = await sw.version();
  assert.match(v.product, /Sweef/);
});

test('Sweef.session shares pages with the session CLI', async () => {
  const bin = process.env.SWEEF_BIN;
  const home = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-home-'));
  const env = { ...process.env, SWEEF_HOME: home };
  const saved = process.env.SWEEF_HOME;
  process.env.SWEEF_HOME = home;
  try {
    await run(bin, ['open', base + '/login'], { env });
    const s = await Sweef.session();
    const [page] = await s.pages();
    assert.equal(await page.title(), 'Sign in');
    await page.fill('#email', 'cli@x.io');
    const out = (await run(bin, ['get', 'value', '#email'], { env })).stdout.trim();
    assert.equal(out, 'cli@x.io');
    s.close();
  } finally {
    try { await run(bin, ['close'], { env }); } catch (_) { /* gone */ }
    process.env.SWEEF_HOME = saved;
    if (saved === undefined) delete process.env.SWEEF_HOME;
    fs.rmSync(home, { recursive: true, force: true });
  }
});
