declare module '@sweef/browser' {
  export class RpcError extends Error { code: number; data?: unknown; }
  export class Connection {
    constructor(opts?: { host?: string; port?: number; path?: string | null; timeout?: number });
    call(method: string, params?: object): Promise<any>;
    close(): void;
  }
  export class Locator {
    nth(i: number): Locator; readonly first: Locator; readonly last: Locator;
    click(): Promise<any>; fill(v: string): Promise<any>; type(t: string, delayMs?: number): Promise<any>;
    press(key?: string): Promise<any>; check(): Promise<any>; uncheck(): Promise<any>;
    selectOption(v: string): Promise<any>; hover(): Promise<any>; focus(): Promise<any>; tap(): Promise<any>;
    textContent(): Promise<string|null>; innerHTML(): Promise<string|null>; getAttribute(n: string): Promise<string|null>;
    count(): Promise<number>; isVisible(): Promise<boolean>; isEnabled(): Promise<boolean>; isChecked(): Promise<boolean>;
    boundingBox(): Promise<{x:number;y:number;width:number;height:number}|null>;
    waitFor(state?: string, timeoutMs?: number): Promise<any>;
  }
  export class Page {
    info(): Promise<any>; url(): Promise<string>; title(): Promise<string>; content(): Promise<string>;
    textContent(sel?: string): Promise<string>; ariaSnapshot(): Promise<string>; screenshot(width?: number): Promise<string>;
    goto(url: string): Promise<any>; goBack(): Promise<any>; goForward(): Promise<any>; reload(): Promise<any>;
    locator(sel: string): Locator; click(sel: string): Promise<any>; fill(sel: string, v: string): Promise<any>;
    type(sel: string, t: string, d?: number): Promise<any>; press(sel: string, k?: string): Promise<any>;
    check(sel: string): Promise<any>; uncheck(sel: string): Promise<any>; selectOption(sel: string, v: string): Promise<any>;
    hover(sel: string): Promise<any>; evaluate(expr: string): Promise<any>; addInitScript(src: string): Promise<any>;
    waitForSelector(sel: string, opts?: {state?: string; timeout?: number}): Promise<any>;
    waitForFunction(expr: string, opts?: {timeout?: number; pollMs?: number}): Promise<any>;
    waitForLoadState(state?: string, timeout?: number): Promise<any>; waitForTimeout(ms: number): Promise<any>;
    consoleMessages(since?: number): Promise<any[]>; networkLog(since?: number): Promise<any[]>; har(): Promise<any>;
    route(pattern: string, opts?: {status?: number; body?: string; action?: string}): Promise<any>;
    unroute(pattern: string): Promise<any>; setOffline(v: boolean): Promise<any>;
    setUserAgent(ua: string): Promise<any>; setViewportSize(w: number, h: number): Promise<any>;
    armDialog(action?: string, promptText?: string): Promise<any>; mouse(a: string, x: number, y: number): Promise<any>;
    close(): Promise<any>; [k: string]: any;
  }
  export class BrowserContext {
    newPage(url?: string): Promise<Page>; storageState(path?: string): Promise<any>;
    setStorageState(o: {state?: string; path?: string}): Promise<any>;
    addCookies(c: object[]): Promise<any>; clearCookies(): Promise<any>;
    request(method: string, url: string, headers?: object, body?: string): Promise<any>; close(): Promise<any>;
  }
  export class Sweef {
    static connect(opts?: {host?: string; port?: number; path?: string; timeout?: number}): Promise<Sweef>;
    newPage(url?: string): Promise<Page>; newContext(name?: string, opts?: object): Promise<BrowserContext>;
    contexts(): Promise<any[]>; version(): Promise<any>; stats(): Promise<any>; close(): void;
  }
  export const MAGIC: string;
}
