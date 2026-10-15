// Type definitions for @sweef/browser 0.2

export declare const MAGIC: 'SWF1';
export declare const VERSION: string;

export declare class RpcError extends Error {
  code: number;
  data?: unknown;
}

export interface ConnectOptions {
  /** "unix:/path/to.sock", "/path/to.sock", "host:port" or a port number */
  addr?: string | number;
  host?: string;
  port?: number;
  path?: string;
  /** per-call timeout in ms (default 60000; 0 = none) */
  timeout?: number;
}

export interface LaunchOptions {
  /** sweef binary (default: $SWEEF_BIN, then PATH) */
  binary?: string;
  /** extra `sweef serve` flags, e.g. ['--settle-ms', '500'] */
  args?: string[];
  /** listen address (default: a private unix socket; a loopback port on Windows) */
  addr?: string;
  /** startup timeout in ms (default 10000) */
  timeout?: number;
  env?: Record<string, string>;
}

export declare class Connection {
  static open(opts?: ConnectOptions): Promise<Connection>;
  call<T = any>(method: string, params?: Record<string, unknown>, opts?: { timeout?: number }): Promise<T>;
  close(): void;
}

export interface SnapshotOptions {
  selector?: string;
  /** controls and headings only */
  interactiveOnly?: boolean;
  /** only the lines changed since the previous snapshot of this tab */
  delta?: boolean;
  /** output cap (default 60000) */
  maxChars?: number;
  /** per text node (default 200) */
  maxTextLen?: number;
  maxDepth?: number;
}

export interface ScreenshotOptions {
  path?: string;
  fullPage?: boolean;
  width?: number;
  height?: number;
  deviceScaleFactor?: number;
}

export interface PdfOptions {
  path?: string;
  format?: 'A4' | 'Letter' | 'Legal' | 'A3' | 'A5';
  landscape?: boolean;
  /** points */
  margin?: number;
  scale?: number;
  displayHeaderFooter?: boolean;
}

export interface RouteOptions {
  action?: 'fulfill' | 'abort' | 'continue';
  status?: number;
  body?: string;
  json?: unknown;
  contentType?: string;
  headers?: Record<string, string>;
  /** continue: rewrite the URL */
  url?: string;
  newMethod?: string;
  newBody?: string;
  /** match only this method / bodies containing this text */
  method?: string;
  bodyContains?: string;
  times?: number;
}

/** A batch step: a CLI-style verb with arguments, a raw RPC call, or a string such as "fill #email a@b.c". */
export type BatchStep =
  | string
  | { cmd: string; args?: Array<string | number>; optional?: boolean }
  | { method: string; params?: Record<string, unknown>; optional?: boolean };

export interface BatchResult {
  ok: boolean;
  ran: number;
  total: number;
  failed: number;
  totalMs: number;
  url?: string;
  title?: string;
  observation?: string;
  steps: Array<{ step: number; cmd: string; ok: boolean; ms: number; result?: any; error?: string }>;
}

export interface SweefEvent {
  sid: number;
  tab: string;
  kind: string;
  at: string;
  data?: any;
}

export declare class Locator {
  readonly page: Page;
  readonly selector: string;
  locator(selector: string): Locator;
  nth(i: number): Locator;
  readonly first: Locator;
  readonly last: Locator;
  click(): Promise<void>;
  dblclick(): Promise<void>;
  fill(value: string): Promise<void>;
  type(text: string, opts?: { delay?: number }): Promise<void>;
  pressSequentially(text: string, opts?: { delay?: number }): Promise<void>;
  press(key?: string): Promise<void>;
  check(): Promise<void>;
  uncheck(): Promise<void>;
  setChecked(checked: boolean): Promise<void>;
  selectOption(value: string): Promise<void>;
  hover(): Promise<void>;
  focus(): Promise<void>;
  tap(): Promise<void>;
  scrollIntoViewIfNeeded(): Promise<void>;
  dragTo(target: Locator | string): Promise<void>;
  textContent(): Promise<string>;
  innerText(): Promise<string>;
  innerHTML(): Promise<string>;
  outerHTML(): Promise<string>;
  getAttribute(name: string): Promise<string | null>;
  attributes(): Promise<Record<string, string>>;
  inputValue(): Promise<string>;
  count(): Promise<number>;
  isVisible(): Promise<boolean>;
  isHidden(): Promise<boolean>;
  isEnabled(): Promise<boolean>;
  isDisabled(): Promise<boolean>;
  isEditable(): Promise<boolean>;
  isChecked(): Promise<boolean>;
  boundingBox(): Promise<{ x: number; y: number; width: number; height: number }>;
  waitFor(opts?: { state?: 'attached' | 'detached' | 'visible' | 'hidden'; timeout?: number }): Promise<void>;
  snapshot(opts?: SnapshotOptions): Promise<string>;
}

export declare class Keyboard {
  type(text: string, opts?: { delay?: number }): Promise<void>;
  insertText(text: string): Promise<void>;
  /** a key or combination: "Enter", "Control+a", "Shift+Tab" */
  press(key: string): Promise<void>;
}

export declare class Mouse {
  click(x: number, y: number): Promise<void>;
  move(x: number, y: number): Promise<void>;
  down(x?: number, y?: number): Promise<void>;
  up(x?: number, y?: number): Promise<void>;
  wheel(deltaX: number, deltaY: number): Promise<void>;
}

export declare class Page {
  readonly tab: string;
  readonly keyboard: Keyboard;
  readonly mouse: Mouse;
  context(): BrowserContext | null;

  goto(url: string, opts?: { headers?: Record<string, string> }): Promise<{ url: string; title: string }>;
  goBack(): Promise<void>;
  goForward(): Promise<void>;
  reload(): Promise<void>;
  url(): Promise<string>;
  title(): Promise<string>;
  frames(): Promise<any[]>;
  popups(): Promise<any[]>;

  content(): Promise<string>;
  textContent(selector?: string): Promise<string>;
  innerText(selector?: string): Promise<string>;
  /** YAML accessibility tree; controls carry [ref=eN], usable as selectors */
  snapshot(opts?: SnapshotOptions): Promise<string>;
  ariaSnapshot(opts?: SnapshotOptions): Promise<string>;
  /** the JSON accessibility node list */
  accessibilityTree(): Promise<any[]>;
  /** PNG bytes */
  screenshot(opts?: ScreenshotOptions): Promise<Buffer>;
  /** the page rendered as text */
  screenshotText(width?: number): Promise<string>;
  pdf(opts?: PdfOptions): Promise<Buffer>;

  locator(selector: string): Locator;
  getByRole(role: string, opts?: { name?: string }): Locator;
  getByText(text: string): Locator;
  getByLabel(text: string): Locator;
  getByPlaceholder(text: string): Locator;
  getByAltText(text: string): Locator;
  getByTestId(id: string): Locator;
  click(selector: string): Promise<void>;
  dblclick(selector: string): Promise<void>;
  fill(selector: string, value: string): Promise<void>;
  type(selector: string, text: string, opts?: { delay?: number }): Promise<void>;
  press(selector: string, key?: string): Promise<void>;
  check(selector: string): Promise<void>;
  uncheck(selector: string): Promise<void>;
  selectOption(selector: string, value: string): Promise<void>;
  hover(selector: string): Promise<void>;
  focus(selector: string): Promise<void>;
  tap(selector: string): Promise<void>;
  dragAndDrop(source: string, target: string): Promise<void>;
  scroll(x?: number, y?: number): Promise<void>;
  /** many steps, one round trip */
  batch(steps: BatchStep[], opts?: { observe?: 'none' | 'interactive' | 'full' | 'delta' | 'text'; stopOnError?: boolean }): Promise<BatchResult>;

  evaluate<T = any>(expression: string | ((arg?: any) => T | Promise<T>), arg?: unknown): Promise<T>;
  addInitScript(source: string | (() => void)): Promise<string>;
  removeInitScript(id: string): Promise<void>;
  exposeBinding(name: string, reply?: unknown): Promise<void>;
  setBindingReply(name: string, reply: unknown): Promise<void>;
  bindingCalls(since?: number): Promise<SweefEvent[]>;

  waitForSelector(selector: string, opts?: { state?: 'attached' | 'detached' | 'visible' | 'hidden'; timeout?: number }): Promise<{ count: number }>;
  waitForFunction(expression: string | (() => unknown), opts?: { timeout?: number; polling?: number }): Promise<void>;
  waitForLoadState(state?: 'load' | 'domcontentloaded' | 'networkidle', opts?: { timeout?: number }): Promise<void>;
  /** glob (** any, * one segment) or substring */
  waitForURL(pattern: string, opts?: { timeout?: number }): Promise<void>;
  waitForTimeout(ms: number): Promise<void>;

  consoleMessages(since?: number): Promise<SweefEvent[]>;
  networkLog(since?: number): Promise<SweefEvent[]>;
  events(since?: number): Promise<SweefEvent[]>;
  stateLog(since?: number): Promise<any>;
  har(): Promise<any>;
  route(pattern: string, opts?: RouteOptions): Promise<void>;
  unroute(pattern?: string): Promise<void>;
  unrouteAll(): Promise<void>;
  routes(): Promise<any[]>;
  setExtraHTTPHeaders(headers: Record<string, string>): Promise<void>;
  downloads(): Promise<any[]>;
  download(url: string, filename?: string): Promise<any>;
  scripts(): Promise<any>;

  armDialog(action?: 'accept' | 'dismiss', promptText?: string): Promise<void>;
  dialogs(since?: number): Promise<SweefEvent[]>;

  setViewportSize(size: { width: number; height: number; deviceScaleFactor?: number }): Promise<void>;
  setViewportSize(width: number, height: number): Promise<void>;
  setOffline(offline: boolean): Promise<void>;
  setUserAgent(userAgent: string): Promise<void>;
  setGeolocation(geo: { latitude: number; longitude: number }): Promise<void>;
  grantPermissions(permissions: string[]): Promise<string[]>;
  emulateMedia(opts: { colorScheme?: 'light' | 'dark' | 'no-preference' }): Promise<void>;

  close(): Promise<void>;
}

export interface Cookie {
  name: string;
  value: string;
  domain?: string;
  path?: string;
  url?: string;
  expires?: number;
  httpOnly?: boolean;
  secure?: boolean;
  sameSite?: 'Strict' | 'Lax' | 'None';
}

export declare class BrowserContext {
  readonly name: string | null;
  newPage(url?: string): Promise<Page>;
  storageState(opts?: { path?: string }): Promise<{ cookies: Cookie[]; origins: any[] }>;
  setStorageState(opts: { state?: string | object; path?: string }): Promise<void>;
  addCookies(cookies: Cookie[]): Promise<void>;
  clearCookies(): Promise<void>;
  cookies(): Promise<Cookie[]>;
  request(method: string, url: string, opts?: { headers?: Record<string, string>; body?: string }): Promise<{ status: number; headers: Record<string, string>; body: string }>;
  close(): Promise<void>;
}

export interface NewContextOptions {
  name?: string;
  userAgent?: string;
  viewport?: { width: number; height: number };
  deviceScaleFactor?: number;
  mobile?: boolean;
  offline?: boolean;
  colorScheme?: 'light' | 'dark' | 'no-preference';
  proxy?: string;
  extraHTTPHeaders?: Record<string, string>;
  permissions?: string[];
  storagePath?: string;
}

export declare class Sweef {
  readonly conn: Connection;
  static connect(opts?: ConnectOptions): Promise<Sweef>;
  static launch(opts?: LaunchOptions): Promise<Sweef>;
  /** attach to the session CLI's daemon (`sweef open ...`), starting it when absent */
  static session(name?: string, opts?: { binary?: string; timeout?: number }): Promise<Sweef>;
  newPage(url?: string): Promise<Page>;
  newContext(opts?: NewContextOptions): Promise<BrowserContext>;
  pages(): Promise<Page[]>;
  page(tabId: string): Page;
  contexts(): Promise<any[]>;
  version(): Promise<{ product: string; userAgent: string; jsRuntime: string }>;
  stats(): Promise<{ rssMiB: number; heapMiB: number; goroutines: number; tabs: number }>;
  call<T = any>(method: string, params?: Record<string, unknown>, opts?: { timeout?: number }): Promise<T>;
  close(): Promise<void>;
}
