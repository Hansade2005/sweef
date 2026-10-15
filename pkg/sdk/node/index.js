'use strict';
// Sweef Browser SDK for Node.js. Zero dependencies. Speaks the SWF1 framed
// JSON-RPC protocol to a `sweef serve` process (or the session CLI's daemon).
//
//   const { Sweef } = require('@sweef/browser');
//   const sw = await Sweef.launch();                 // starts `sweef serve` on a private socket
//   const page = await sw.newPage('https://example.com');
//   console.log(await page.title(), await page.snapshot({ interactiveOnly: true }));
//   await page.click('text=Learn more');
//   await sw.close();
const net = require('net');
const os = require('os');
const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');

const MAGIC = 'SWF1';
const VERSION = '0.2.1';
const { ensureBinary, SweefInstallError } = require('./install.js');

class RpcError extends Error {
  constructor(code, message, data) {
    super(message);
    this.name = 'RpcError';
    this.code = code;
    this.data = data;
  }
}

// parseAddr accepts "unix:/path", "/path/to.sock", "host:port" or a port number.
function parseAddr(addr) {
  if (typeof addr === 'number') return { host: '127.0.0.1', port: addr };
  if (addr.startsWith('unix:')) return { path: addr.slice(5) };
  if (addr.startsWith('/') || addr.startsWith('\\\\')) return { path: addr };
  const i = addr.lastIndexOf(':');
  return { host: addr.slice(0, i) || '127.0.0.1', port: Number(addr.slice(i + 1)) };
}

class Connection {
  // Use Connection.open(); the constructor does not wait for the socket.
  constructor(sock, { timeout = 60000 } = {}) {
    this.sock = sock;
    this.timeout = timeout;
    this.chunks = [];
    this.buffered = 0;
    this.id = 0;
    this.pending = new Map();
    this.closed = false;
    sock.setNoDelay && sock.setNoDelay(true);
    sock.on('data', (d) => this._onData(d));
    sock.on('error', (e) => this._rejectAll(e));
    sock.on('close', () => { this.closed = true; this._rejectAll(new Error('sweef connection closed')); });
  }

  static open(opts = {}) {
    const target = opts.addr !== undefined ? parseAddr(opts.addr)
      : opts.path ? { path: opts.path } : { host: opts.host || '127.0.0.1', port: opts.port || 9222 };
    return new Promise((resolve, reject) => {
      const sock = net.connect(target);
      const onErr = (e) => { sock.destroy(); reject(e); };
      sock.once('error', onErr);
      sock.once('connect', () => { sock.off('error', onErr); resolve(new Connection(sock, opts)); });
    });
  }

  _onData(chunk) {
    this.chunks.push(chunk);
    this.buffered += chunk.length;
    if (this.buffered < 8) return;
    let buf = this.chunks.length === 1 ? this.chunks[0] : Buffer.concat(this.chunks, this.buffered);
    let off = 0;
    while (buf.length - off >= 8) {
      if (buf.toString('latin1', off, off + 4) !== MAGIC) { this._rejectAll(new Error('sweef: bad frame magic')); this.sock.destroy(); return; }
      const len = buf.readUInt32LE(off + 4);
      if (buf.length - off < 8 + len) break;
      let msg = null;
      try { msg = JSON.parse(buf.toString('utf8', off + 8, off + 8 + len)); } catch (_) { /* skip a bad frame */ }
      off += 8 + len;
      if (!msg || msg.id === undefined) continue; // notifications
      const p = this.pending.get(msg.id);
      if (!p) continue;
      this.pending.delete(msg.id);
      clearTimeout(p.timer);
      if (msg.error) p.reject(new RpcError(msg.error.code, msg.error.message, msg.error.data));
      else p.resolve(msg.result);
    }
    const rest = buf.subarray(off);
    this.chunks = rest.length ? [rest] : [];
    this.buffered = rest.length;
  }

  _rejectAll(e) {
    for (const [, p] of this.pending) { clearTimeout(p.timer); p.reject(e); }
    this.pending.clear();
  }

  call(method, params, { timeout } = {}) {
    if (this.closed) return Promise.reject(new Error('sweef connection closed'));
    return new Promise((resolve, reject) => {
      const id = ++this.id;
      const ms = timeout ?? this.timeout;
      const timer = ms > 0 ? setTimeout(() => { this.pending.delete(id); reject(new Error(`sweef: ${method} timed out after ${ms} ms`)); }, ms) : null;
      if (timer && timer.unref) timer.unref();
      this.pending.set(id, { resolve, reject, timer });
      const body = Buffer.from(JSON.stringify(params === undefined ? { jsonrpc: '2.0', id, method } : { jsonrpc: '2.0', id, method, params }), 'utf8');
      const frame = Buffer.allocUnsafe(8 + body.length);
      frame.write(MAGIC, 0, 'latin1');
      frame.writeUInt32LE(body.length, 4);
      body.copy(frame, 8);
      this.sock.write(frame);
    });
  }

  close() { this.closed = true; this.sock.destroy(); }
}

// Waits a timed step longer than the connection timeout would need.
const waitOpts = (timeoutMs) => ({ timeout: Math.max(60000, (timeoutMs || 30000) + 5000) });

class Locator {
  constructor(page, selector) { this.page = page; this.selector = selector; }
  locator(sub) { return new Locator(this.page, `${this.selector} >> ${sub}`); }
  nth(i) { return new Locator(this.page, `${this.selector} >> nth=${i}`); }
  get first() { return this.nth(0); }
  get last() { return this.nth(-1); }
  _p(extra) { return { selector: this.selector, ...extra }; }
  _r(method, extra, opts) { return this.page._rpc(method, this._p(extra), opts); }
  click() { return this._r('Input.click').then(() => undefined); }
  dblclick() { return this.page.batch([{ cmd: 'dblclick', args: [this.selector] }], { observe: 'none' }).then(() => undefined); }
  fill(value) { return this._r('Input.fill', { value }).then(() => undefined); }
  type(text, { delay = 0 } = {}) { return this._r('Input.type', { text, delayMs: delay }).then(() => undefined); }
  pressSequentially(text, opts) { return this.type(text, opts); }
  press(key = 'Enter') { return this._r('Input.press', { key }).then(() => undefined); }
  check() { return this._r('Input.check').then(() => undefined); }
  uncheck() { return this._r('Input.uncheck').then(() => undefined); }
  setChecked(v) { return v ? this.check() : this.uncheck(); }
  selectOption(value) { return this._r('Input.selectOption', { value }).then(() => undefined); }
  hover() { return this._r('Input.hover').then(() => undefined); }
  focus() { return this._r('Input.focus').then(() => undefined); }
  tap() { return this._r('Input.tap').then(() => undefined); }
  scrollIntoViewIfNeeded() { return this._r('Input.scroll').then(() => undefined); }
  dragTo(target) { return this.page._rpc('Input.drag', { source: this.selector, target: target instanceof Locator ? target.selector : target }).then(() => undefined); }
  textContent() { return this._r('Locator.textContent').then((r) => r.text); }
  innerText() { return this.textContent(); }
  innerHTML() { return this._r('Locator.innerHTML').then((r) => r.innerHTML); }
  outerHTML() { return this._r('DOM.getOuterHTML').then((r) => r.outerHTML); }
  getAttribute(name) { return this._r('Locator.getAttribute', { name }).then((r) => (r.present === false ? null : r.value)); }
  attributes() { return this._r('DOM.getAttributes'); }
  inputValue() { return this.page._stepResult({ cmd: 'get', args: ['value', this.selector] }); }
  count() { return this._r('Locator.count').then((r) => r.count); }
  isVisible() { return this._r('Locator.isVisible').then((r) => !!r.value); }
  isHidden() { return this.isVisible().then((v) => !v); }
  isEnabled() { return this._r('Locator.isEnabled').then((r) => !!r.value); }
  isDisabled() { return this.isEnabled().then((v) => !v); }
  isEditable() { return this._r('Locator.isEditable').then((r) => !!r.value); }
  isChecked() { return this._r('Locator.isChecked').then((r) => !!r.value); }
  boundingBox() { return this._r('Locator.boundingBox'); }
  waitFor({ state = 'visible', timeout = 30000 } = {}) { return this._r('Page.waitForSelector', { state, timeoutMs: timeout }, waitOpts(timeout)).then(() => undefined); }
  snapshot(opts = {}) { return this.page.snapshot({ ...opts, selector: this.selector }); }
}

const q = (s) => JSON.stringify(String(s));

class Keyboard {
  constructor(page) { this.page = page; }
  type(text, { delay = 0 } = {}) { return this.page._rpc('Input.keyboard', { text, delayMs: delay }).then(() => undefined); }
  insertText(text) { return this.type(text); }
  press(key) { return this.page._rpc('Input.keyboard', { key }).then(() => undefined); }
}

class Mouse {
  constructor(page) { this.page = page; }
  _m(action, x, y) { return this.page._rpc('Input.mouse', { action, x, y }).then(() => undefined); }
  click(x, y) { return this._m('click', x, y); }
  move(x, y) { return this._m('move', x, y); }
  down(x = 0, y = 0) { return this._m('down', x, y); }
  up(x = 0, y = 0) { return this._m('up', x, y); }
  wheel(deltaX, deltaY) { return this.page._rpc('Input.scroll', { x: deltaX, y: deltaY }).then(() => undefined); }
}

class Page {
  constructor(conn, tab, context = null) {
    this.conn = conn;
    this.tab = tab;
    this._context = context;
    this.keyboard = new Keyboard(this);
    this.mouse = new Mouse(this);
  }
  _rpc(method, params = {}, opts) { return this.conn.call(method, { tab: this.tab, ...params }, opts); }
  context() { return this._context; }

  // navigation
  goto(url, { headers } = {}) { return this._rpc('Page.navigate', headers ? { url, headers } : { url }); }
  goBack() { return this._rpc('Page.goBack').then(() => undefined); }
  goForward() { return this._rpc('Page.goForward').then(() => undefined); }
  reload() { return this._rpc('Page.reload').then(() => undefined); }
  url() { return this._rpc('Page.url').then((r) => r.url); }
  title() { return this._rpc('Page.title').then((r) => r.title); }
  frames() { return this._rpc('Page.frames').then((r) => r.frames); }
  popups() { return this._rpc('Page.popups').then((r) => r.popups); }

  // reading the page
  content() { return this._rpc('Page.content').then((r) => r.html); }
  textContent(selector = 'body') { return this._rpc('Page.textContent', { selector }).then((r) => r.text); }
  innerText(selector = 'body') { return this.textContent(selector); }
  // snapshot returns the YAML accessibility tree; controls carry [ref=eN] usable as selectors.
  // opts: selector, interactiveOnly, delta, maxChars, maxTextLen, maxDepth
  snapshot(opts = {}) { return this._rpc('Page.ariaSnapshot', opts).then((r) => r.yaml); }
  ariaSnapshot(opts = {}) { return this.snapshot(opts); }
  accessibilityTree() { return this._rpc('Page.snapshot'); }
  // screenshot returns PNG bytes (Buffer); opts: fullPage, width, height, deviceScaleFactor, path
  async screenshot({ path: file, fullPage, width, height, deviceScaleFactor } = {}) {
    const r = await this._rpc('Page.screenshot', { fullPage: !!fullPage, width, height, deviceScaleFactor, noText: true });
    const buf = Buffer.from(r.pngBase64 || '', 'base64');
    if (file) fs.writeFileSync(file, buf);
    return buf;
  }
  screenshotText(width) { return this._rpc('Page.screenshot', { width, type: 'text' }).then((r) => r.text); }
  async pdf({ path: file, format, landscape, margin, scale, displayHeaderFooter } = {}) {
    const r = await this._rpc('Page.pdf', { path: file, format, landscape, margin, scale, displayHeaderFooter });
    return r.pdfBase64 ? Buffer.from(r.pdfBase64, 'base64') : (file ? fs.readFileSync(file) : Buffer.alloc(0));
  }

  // acting
  locator(selector) { return new Locator(this, selector); }
  getByRole(role, { name } = {}) { return this.locator(name === undefined ? `role=${role}` : `role=${role}[name=${q(name)}]`); }
  getByText(text) { return this.locator(`text=${text}`); }
  getByLabel(text) { return this.locator(`label=${text}`); }
  getByPlaceholder(text) { return this.locator(`placeholder=${text}`); }
  getByAltText(text) { return this.locator(`alt=${text}`); }
  getByTestId(id) { return this.locator(`testid=${id}`); }
  click(sel) { return this.locator(sel).click(); }
  dblclick(sel) { return this.locator(sel).dblclick(); }
  fill(sel, v) { return this.locator(sel).fill(v); }
  type(sel, t, o) { return this.locator(sel).type(t, o); }
  press(sel, k) { return this.locator(sel).press(k); }
  check(sel) { return this.locator(sel).check(); }
  uncheck(sel) { return this.locator(sel).uncheck(); }
  selectOption(sel, v) { return this.locator(sel).selectOption(v); }
  hover(sel) { return this.locator(sel).hover(); }
  focus(sel) { return this.locator(sel).focus(); }
  tap(sel) { return this.locator(sel).tap(); }
  dragAndDrop(source, target) { return this._rpc('Input.drag', { source, target }).then(() => undefined); }
  scroll(x = 0, y = 0) { return this._rpc('Input.scroll', { x, y }).then(() => undefined); }
  // batch runs many steps in one round trip: [{cmd, args}] or [{method, params}] (see README).
  batch(steps, { observe = 'none', stopOnError = true } = {}) {
    return this._rpc('Batch.run', { steps: steps.map(normStep), observe, stopOnError }, { timeout: 300000 });
  }

  async _stepResult(step) {
    const r = await this.batch([step]);
    const st = r.steps && r.steps[0];
    if (!st || !st.ok) throw new RpcError(-32000, (st && st.error) || 'step failed');
    const v = st.result;
    return v && typeof v === 'object' && 'value' in v ? v.value : v;
  }

  // JavaScript
  evaluate(expression, arg) {
    if (typeof expression === 'function') expression = `(${expression.toString()})(${arg === undefined ? '' : JSON.stringify(arg)})`;
    return this._rpc('Runtime.evaluate', { expression }).then((r) => r.result);
  }
  addInitScript(source) {
    if (typeof source === 'function') source = `(${source.toString()})()`;
    return this._rpc('Page.addInitScript', { source }).then((r) => r.id);
  }
  removeInitScript(id) { return this._rpc('Page.removeInitScript', { id }).then(() => undefined); }
  exposeBinding(name, reply) { return this._rpc('Runtime.addBinding', reply === undefined ? { name } : { name, reply }).then(() => undefined); }
  setBindingReply(name, reply) { return this._rpc('Runtime.setBindingReply', { name, reply }).then(() => undefined); }
  bindingCalls(since = 0) { return this._rpc('Runtime.bindingCalls', { since }).then((r) => r.calls || []); }

  // waiting (the page keeps running meanwhile)
  waitForSelector(selector, { state = 'visible', timeout = 30000 } = {}) { return this._rpc('Page.waitForSelector', { selector, state, timeoutMs: timeout }, waitOpts(timeout)); }
  waitForFunction(expression, { timeout = 30000, polling = 100 } = {}) {
    if (typeof expression === 'function') expression = `(${expression.toString()})()`;
    return this._rpc('Page.waitForFunction', { expression, timeoutMs: timeout, pollMs: polling }, waitOpts(timeout)).then(() => undefined);
  }
  waitForLoadState(state = 'load', { timeout = 30000 } = {}) { return this._rpc('Page.waitForLoadState', { state, timeoutMs: timeout }, waitOpts(timeout)).then(() => undefined); }
  waitForURL(pattern, { timeout = 30000 } = {}) { return this._rpc('Page.waitForURL', { pattern: String(pattern), timeoutMs: timeout }, waitOpts(timeout)).then(() => undefined); }
  waitForTimeout(ms) { return this._rpc('Page.waitForTimeout', { ms }, waitOpts(ms)).then(() => undefined); }

  // logs and network
  consoleMessages(since = 0) { return this._rpc('Log.getConsole', { since }).then((r) => r.messages || []); }
  networkLog(since = 0) { return this._rpc('Network.getLogs', { since }).then((r) => r.entries || []); }
  events(since = 0) { return this._rpc('Events.poll', { since }); }
  stateLog(since = 0) { return this._rpc('Page.getStateLog', { since }); }
  har() { return this._rpc('Network.har'); }
  // route: { status, body, json, contentType, headers, action: 'fulfill'|'abort'|'continue', url, newMethod, newBody, method, bodyContains, times }
  route(pattern, opts = {}) { return this._rpc('Network.route', { pattern: String(pattern), action: 'fulfill', ...opts }).then(() => undefined); }
  unroute(pattern = '') { return this._rpc('Network.unroute', { pattern: String(pattern) }).then(() => undefined); }
  unrouteAll() { return this._rpc('Network.unrouteAll').then(() => undefined); }
  routes() { return this._rpc('Network.routes').then((r) => r.routes || []); }
  setExtraHTTPHeaders(headers) { return this._rpc('Network.setExtraHTTPHeaders', { headers }).then(() => undefined); }
  downloads() { return this._rpc('Page.downloads').then((r) => r.downloads || []); }
  download(url, filename) { return this._rpc('Page.download', { url, filename }); }
  scripts() { return this._rpc('Storage.getScripts'); }

  // dialogs
  armDialog(action = 'accept', promptText = '') { return this._rpc('Page.armDialog', { action, promptText }).then(() => undefined); }
  dialogs(since = 0) { return this._rpc('Page.getDialogs', { since }).then((r) => (Array.isArray(r) ? r : r.dialogs || [])); }

  // emulation
  setViewportSize({ width, height, deviceScaleFactor } = {}, h) {
    if (typeof arguments[0] === 'number') { width = arguments[0]; height = h; }
    return this._rpc('Emulation.setDeviceMetrics', { width, height, deviceScaleFactor }).then(() => undefined);
  }
  setOffline(offline) { return this._rpc('Emulation.setOffline', { offline: !!offline }).then(() => undefined); }
  setUserAgent(userAgent) { return this._rpc('Emulation.setUserAgentOverride', { userAgent }).then(() => undefined); }
  setGeolocation({ latitude, longitude }) { return this._rpc('Emulation.setGeolocation', { latitude, longitude }).then(() => undefined); }
  grantPermissions(permissions) { return this._rpc('Emulation.grantPermissions', { permissions }).then((r) => r.granted); }
  emulateMedia({ colorScheme } = {}) { return this._rpc('Emulation.setColorScheme', { colorScheme }).then(() => undefined); }

  close() { return this.conn.call('Browser.closeTab', { tab: this.tab }).then(() => undefined); }
}

function normStep(s) {
  if (typeof s === 'string') {
    const toks = s.match(/"[^"]*"|'[^']*'|\S+/g) || [];
    const unq = toks.map((t) => (/^(['"]).*\1$/.test(t) ? t.slice(1, -1) : t));
    return { cmd: unq[0], args: unq.slice(1) };
  }
  return s.args ? { ...s, args: s.args.map(String) } : s;
}

class BrowserContext {
  constructor(conn, name) { this.conn = conn; this.name = name || null; }
  async newPage(url = 'about:blank') {
    const r = this.name ? await this.conn.call('Browser.newTabInContext', { context: this.name, url })
      : await this.conn.call('Browser.newTab', { url });
    return new Page(this.conn, r.tab, this);
  }
  _c(extra) { return this.name ? { context: this.name, ...extra } : { ...extra }; }
  async storageState({ path: file } = {}) {
    if (file) await this.conn.call('BrowserContext.saveStorageState', this._c({ path: file }));
    return (await this.conn.call('BrowserContext.storageState', this._c())).state;
  }
  setStorageState({ state, path: file } = {}) {
    return this.conn.call('BrowserContext.loadStorageState', this._c({ state: typeof state === 'string' || state === undefined ? state : JSON.stringify(state), path: file })).then(() => undefined);
  }
  addCookies(cookies) { return this.conn.call('BrowserContext.addCookies', this._c({ cookies })).then(() => undefined); }
  clearCookies() { return this.conn.call('BrowserContext.clearCookies', this._c()).then(() => undefined); }
  cookies() { return this.storageState().then((s) => s.cookies || []); }
  request(method, url, { headers, body } = {}) { return this.conn.call('Fetch.request', this._c({ method, url, headers, body })); }
  close() { return this.name ? this.conn.call('Browser.closeContext', { context: this.name }).then(() => undefined) : Promise.resolve(); }
}

function sessionDir() {
  if (process.env.SWEEF_HOME) return process.env.SWEEF_HOME;
  if (process.env.XDG_RUNTIME_DIR) return path.join(process.env.XDG_RUNTIME_DIR, 'sweef');
  return path.join(os.tmpdir(), `sweef-${typeof process.getuid === 'function' ? process.getuid() : 0}`);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function dialUntil(opts, deadline, proc) {
  let last;
  while (Date.now() < deadline) {
    if (proc && proc.exitCode !== null) throw new Error(`sweef serve exited with code ${proc.exitCode}`);
    try { return await Connection.open(opts); } catch (e) { last = e; await sleep(10); }
  }
  throw new Error(`sweef did not start: ${last && last.message}`);
}

class Sweef {
  constructor(conn, proc = null, cleanup = null) { this.conn = conn; this._proc = proc; this._cleanup = cleanup; }

  // connect attaches to a running `sweef serve` ({ addr: 'unix:/path' | 'host:port' }, or host/port/path).
  static async connect(opts = {}) { return new Sweef(await Connection.open(opts)); }

  // ensureBinary resolves the engine (binary option, SWEEF_BIN, PATH, per-user cache) and downloads it
  // into the cache when absent; returns its path. Unless SWEEF_SKIP_DOWNLOAD is set.
  static ensureBinary(opts = {}) { return ensureBinary(opts); }

  // launch starts a private `sweef serve` (a unix socket, or a loopback port on Windows) and connects.
  static async launch({ binary, args = [], addr, timeout = 10000, env } = {}) {
    const exe = await ensureBinary({ binary });
    let cleanup = null;
    if (!addr) {
      if (process.platform === 'win32') {
        addr = `127.0.0.1:${20000 + Math.floor(Math.random() * 40000)}`;
      } else {
        const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sweef-sdk-'));
        addr = `unix:${path.join(dir, 's.sock')}`;
        cleanup = () => fs.rmSync(dir, { recursive: true, force: true });
      }
    }
    const proc = spawn(exe, ['serve', '--addr', addr, ...args], { stdio: 'ignore', env: env ? { ...process.env, ...env } : process.env });
    try {
      const conn = await dialUntil({ addr }, Date.now() + timeout, proc);
      return new Sweef(conn, proc, cleanup);
    } catch (e) {
      proc.kill();
      if (cleanup) cleanup();
      throw e;
    }
  }

  // session attaches to the session CLI's daemon (`sweef open ...`), starting it when absent,
  // so a script and the CLI drive the same pages.
  static async session(name = process.env.SWEEF_SESSION || 'default', { binary, timeout = 10000 } = {}) {
    const sock = path.join(sessionDir(), `${name}.sock`);
    try { return new Sweef(await Connection.open({ path: sock })); } catch (_) { /* start it */ }
    fs.mkdirSync(path.dirname(sock), { recursive: true, mode: 0o700 });
    try { fs.unlinkSync(sock); } catch (_) { /* absent */ }
    const proc = spawn(await ensureBinary({ binary }), ['serve', '--addr', `unix:${sock}`, '--idle-timeout', process.env.SWEEF_IDLE_TIMEOUT || '1h'], { stdio: 'ignore', detached: true });
    proc.unref();
    return new Sweef(await dialUntil({ path: sock }, Date.now() + timeout, null));
  }

  async newPage(url = 'about:blank') { return new BrowserContext(this.conn, null).newPage(url); }
  async newContext(opts = {}, more = {}) {
    if (typeof opts === 'string') opts = { ...more, name: opts }; // 0.1 signature: newContext(name, opts)
    const { name = '', viewport, ...rest } = opts;
    const p = { name, ...rest };
    if (viewport) { p.viewportWidth = viewport.width; p.viewportHeight = viewport.height; }
    const r = await this.conn.call('Browser.newContext', p);
    return new BrowserContext(this.conn, r.context || r.id);
  }
  async pages() {
    const tabs = await this.conn.call('Browser.listTabs');
    return tabs.map((t) => new Page(this.conn, t.id));
  }
  page(tabId) { return new Page(this.conn, tabId); }
  contexts() { return this.conn.call('Browser.listContexts'); }
  version() { return this.conn.call('Browser.getVersion'); }
  stats() { return this.conn.call('Browser.stats'); }
  // call sends any RPC method (see the method list in the README).
  call(method, params, opts) { return this.conn.call(method, params, opts); }

  async close() {
    if (this._proc) {
      try { await this.conn.call('Browser.shutdown', {}, { timeout: 2000 }); } catch (_) { /* exiting */ }
    }
    this.conn.close();
    if (this._proc) {
      const p = this._proc;
      if (p.exitCode === null) {
        await Promise.race([new Promise((r) => p.once('exit', r)), sleep(2000)]);
        if (p.exitCode === null) p.kill('SIGKILL');
      }
    }
    if (this._cleanup) this._cleanup();
  }
}

module.exports = { Sweef, Page, Locator, BrowserContext, Keyboard, Mouse, Connection, RpcError, MAGIC, VERSION, ensureBinary, SweefInstallError };
