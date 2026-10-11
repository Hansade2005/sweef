'use strict';
// Sweef Browser — Node.js SDK. Talks SWF1 framed JSON-RPC to `sweef serve`.
//   const { Sweef } = require('@sweef/browser');
//   const sw = await Sweef.connect(); const page = await sw.newPage('https://example.com');
const net = require('net');

const MAGIC = 'SWF1';

class RpcError extends Error {
  constructor(code, message, data) { super(`rpc ${code}: ${message}`); this.code = code; this.data = data; }
}

class Connection {
  constructor({ host = '127.0.0.1', port = 9222, path = null, timeout = 30000 } = {}) {
    this.sock = path ? net.connect({ path }) : net.connect({ host, port });
    this.timeout = timeout;
    this.buf = Buffer.alloc(0);
    this.id = 0;
    this.pending = new Map();
    this.sock.on('data', (d) => this._onData(d));
    this.sock.on('error', (e) => this._rejectAll(e));
  }
  _onData(chunk) {
    this.buf = Buffer.concat([this.buf, chunk]);
    while (this.buf.length >= 8) {
      if (this.buf.slice(0, 4).toString() !== MAGIC) { this._rejectAll(new Error('bad magic')); return; }
      const len = this.buf.readUInt32LE(4);
      if (this.buf.length < 8 + len) return;
      const payload = this.buf.slice(8, 8 + len);
      this.buf = this.buf.slice(8 + len);
      let msg; try { msg = JSON.parse(payload.toString('utf8')); } catch (e) { continue; }
      const p = this.pending.get(msg.id);
      if (p) { this.pending.delete(msg.id); msg.error ? p.reject(new RpcError(msg.error.code, msg.error.message, msg.error.data)) : p.resolve(msg.result); }
    }
  }
  _rejectAll(e) { for (const [, p] of this.pending) p.reject(e); this.pending.clear(); }
  call(method, params) {
    return new Promise((resolve, reject) => {
      const id = ++this.id;
      this.pending.set(id, { resolve, reject });
      const frame = Buffer.from(JSON.stringify({ jsonrpc: '2.0', id, method, ...(params !== undefined ? { params } : {}) }), 'utf8');
      const hdr = Buffer.alloc(8); hdr.write(MAGIC, 0); hdr.writeUInt32LE(frame.length, 4);
      this.sock.write(Buffer.concat([hdr, frame]), (err) => { if (err) { this.pending.delete(id); reject(err); } });
    });
  }
  close() { this.sock.destroy(); }
}

class Locator {
  constructor(page, selector) { this.page = page; this.selector = selector; }
  nth(i) { return new Locator(this.page, `${this.selector} >> nth=${i}`); }
  get first() { return this.nth(0); }
  get last() { return new Locator(this.page, `${this.selector} >> nth=-1`); }
  _p(extra = {}) { return { selector: this.selector, ...extra }; }
  click() { return this.page._rpc('Input.click', this._p()); }
  fill(value) { return this.page._rpc('Input.fill', this._p({ value })); }
  type(text, delayMs = 0) { return this.page._rpc('Input.type', this._p({ text, delayMs })); }
  press(key = 'Enter') { return this.page._rpc('Input.press', this._p({ key })); }
  check() { return this.page._rpc('Input.check', this._p()); }
  uncheck() { return this.page._rpc('Input.uncheck', this._p()); }
  selectOption(value) { return this.page._rpc('Input.selectOption', this._p({ value })); }
  hover() { return this.page._rpc('Input.hover', this._p()); }
  focus() { return this.page._rpc('Input.focus', this._p()); }
  tap() { return this.page._rpc('Input.tap', this._p()); }
  textContent() { return this.page._rpc('Locator.textContent', this._p()).then(r => (r || {}).text ?? r); }
  innerHTML() { return this.page._rpc('Locator.innerHTML', this._p()).then(r => (r || {}).html ?? r); }
  getAttribute(name) { return this.page._rpc('Locator.getAttribute', this._p({ name })).then(r => (r || {}).value ?? r); }
  count() { return this.page._rpc('Locator.count', this._p()).then(r => (r || {}).count ?? 0); }
  isVisible() { return this.page._rpc('Locator.isVisible', this._p()).then(r => !!(r || {}).visible); }
  isEnabled() { return this.page._rpc('Locator.isEnabled', this._p()).then(r => !!(r || {}).enabled); }
  isChecked() { return this.page._rpc('Locator.isChecked', this._p()).then(r => !!(r || {}).checked); }
  boundingBox() { return this.page._rpc('Locator.boundingBox', this._p()); }
  waitFor(state = 'visible', timeoutMs = 30000) { return this.page._rpc('Page.waitForSelector', this._p({ state, timeoutMs })); }
}

class Page {
  constructor(conn, tab) { this.conn = conn; this.tab = tab; }
  _rpc(method, params = {}) { return this.conn.call(method, { tab: this.tab, ...params }); }
  info() { return this._rpc('Page.info'); }
  url() { return this._rpc('Page.info').then(r => r.url); }
  title() { return this._rpc('Page.info').then(r => r.title); }
  content() { return this._rpc('Page.content').then(r => r.html); }
  textContent(selector = 'body') { return this._rpc('Page.textContent', { selector }).then(r => r.text); }
  ariaSnapshot(opts = {}) { return this._rpc('Page.ariaSnapshot', opts).then(r => r.yaml); }
  snapshot() { return this._rpc('Page.snapshot'); }
  screenshot(width = 80) { return this._rpc('Page.screenshot', { width }).then(r => r.text); }
  pdf() { return this._rpc('Page.pdf'); }
  goto(url) { return this._rpc('Page.navigate', { url }); }
  goBack() { return this._rpc('Page.goBack'); }
  goForward() { return this._rpc('Page.goForward'); }
  reload() { return this._rpc('Page.reload'); }
  locator(selector) { return new Locator(this, selector); }
  click(sel) { return this.locator(sel).click(); }
  fill(sel, v) { return this.locator(sel).fill(v); }
  type(sel, t, d) { return this.locator(sel).type(t, d); }
  press(sel, k) { return this.locator(sel).press(k); }
  check(sel) { return this.locator(sel).check(); }
  uncheck(sel) { return this.locator(sel).uncheck(); }
  selectOption(sel, v) { return this.locator(sel).selectOption(v); }
  hover(sel) { return this.locator(sel).hover(); }
  evaluate(expression) { return this._rpc('Runtime.evaluate', { expression }).then(r => r.result); }
  addInitScript(source) { return this._rpc('Page.addInitScript', { source }); }
  exposeBinding(name) { return this._rpc('Runtime.addBinding', { name }); }
  waitForSelector(selector, { state = 'visible', timeout = 30000 } = {}) { return this._rpc('Page.waitForSelector', { selector, state, timeoutMs: timeout }); }
  waitForFunction(expression, { timeout = 30000, pollMs = 100 } = {}) { return this._rpc('Page.waitForFunction', { expression, timeoutMs: timeout, pollMs }); }
  waitForLoadState(state = 'load', timeout = 30000) { return this._rpc('Page.waitForLoadState', { state, timeoutMs: timeout }); }
  waitForTimeout(ms) { return this._rpc('Page.waitForTimeout', { timeoutMs: ms }); }
  consoleMessages(since = 0) { return this._rpc('Log.getConsole', { since }).then(r => r.messages || []); }
  networkLog(since = 0) { return this._rpc('Network.getLogs', { since }).then(r => r.entries || []); }
  har() { return this._rpc('Network.har'); }
  route(pattern, { status = 200, body = '', action = 'fulfill' } = {}) { return this._rpc('Network.route', { pattern, status, body, action }); }
  unroute(pattern) { return this._rpc('Network.unroute', { pattern }); }
  setOffline(v) { return this._rpc('Emulation.setOffline', { offline: v }); }
  setUserAgent(ua) { return this._rpc('Emulation.setUserAgentOverride', { userAgent: ua }); }
  setViewportSize(width, height) { return this._rpc('Emulation.setDeviceMetrics', { width, height }); }
  setGeolocation(latitude, longitude) { return this._rpc('Emulation.setGeolocation', { latitude, longitude }); }
  grantPermissions(permissions) { return this._rpc('Emulation.grantPermissions', { permissions }); }
  setColorScheme(scheme) { return this._rpc('Emulation.setColorScheme', { colorScheme: scheme }); }
  setExtraHTTPHeaders(headers) { return this._rpc('Network.setExtraHTTPHeaders', { headers }); }
  armDialog(action = 'accept', promptText = '') { return this._rpc('Page.armDialog', { action, promptText }); }
  dialogs(since = 0) { return this._rpc('Page.getDialogs', { since }).then(r => r.dialogs || []); }
  mouse(action, x, y, button = 'left', clickCount = 1) { return this._rpc('Input.mouse', { action, x, y, button, clickCount }); }
  keyboardType(text, delayMs = 0) { return this._rpc('Input.keyboard', { text, delayMs }); }
  keyboardPress(key) { return this._rpc('Input.keyboard', { key }); }
  drag(source, target) { return this._rpc('Input.drag', { source, target }); }
  scroll(x = 0, y = 0) { return this._rpc('Input.scroll', { x, y }); }
  events(since = 0) { return this._rpc('Events.poll', { since }); }
  close() { return this._rpc('Browser.closeTab', { tab: this.tab }); }
}

class BrowserContext {
  constructor(conn, name) { this.conn = conn; this.name = name || null; }
  async newPage(url = 'about:blank') {
    const r = this.name ? await this.conn.call('Browser.newTabInContext', { context: this.name, url })
                        : await this.conn.call('Browser.newTab', { url });
    return new Page(this.conn, r.tab);
  }
  storageState(path) { return this.conn.call('BrowserContext.storageState', { context: this.name, path }); }
  setStorageState({ state, path } = {}) { return this.conn.call('BrowserContext.loadStorageState', { context: this.name, state, path }); }
  addCookies(cookies) { return this.conn.call('BrowserContext.addCookies', { context: this.name, cookies }); }
  clearCookies() { return this.conn.call('BrowserContext.clearCookies', { context: this.name }); }
  request(method, url, headers, body) { return this.conn.call('Fetch.request', { context: this.name, method, url, headers, body }); }
  close() { return this.name ? this.conn.call('Browser.closeContext', { context: this.name }) : Promise.resolve(); }
}

class Sweef {
  constructor(conn) { this.conn = conn; }
  static connect(opts = {}) { return Promise.resolve(new Sweef(new Connection(opts))); }
  async newPage(url = 'about:blank') { return new BrowserContext(this.conn, null).newPage(url); }
  async newContext(name, opts = {}) {
    const r = await this.conn.call('Browser.newContext', { name: name || '', ...opts });
    return new BrowserContext(this.conn, r.context || r.id);
  }
  contexts() { return this.conn.call('Browser.listContexts'); }
  version() { return this.conn.call('Browser.getVersion'); }
  stats() { return this.conn.call('Browser.stats'); }
  close() { this.conn.close(); }
}

module.exports = { Sweef, Page, Locator, BrowserContext, Connection, RpcError, MAGIC };
