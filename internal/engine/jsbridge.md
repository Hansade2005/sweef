# Sweef JavaScript bridge contract

The Go DOM (`internal/net.Doc`, a `golang.org/x/net/html` tree) is the single
source of truth. The QuickJS VM (`modernc.org/quickjs`, pure Go) runs page
scripts against a prelude, `dom_prelude.js`, that implements the browser API
in JavaScript and reaches the Go tree through ONE host object, `__sw`.

Every host function is registered with `RegisterHostFunc`: it receives all JS
arguments as `[]any` (numbers arrive as `int` or `float64`, strings as
`string`, booleans as `bool`, null/undefined as `nil`, arrays as `[]any`,
objects as `map[string]any`) and returns ONE value (`nil`, `bool`, `int`,
`float64`, `string`, `[]any`, `map[string]any`). A returned Go `error`
becomes a thrown JS exception.

Node ids are small positive integers from the document id table. `0` means
"no node" / null. The document node is id `__sw.docId()`.

## Host functions on `__sw`

### Tree
| call | returns | notes |
|---|---|---|
| `docId()` | id of the Document node | |
| `nodeType(id)` | 1 element, 3 text, 8 comment, 9 document, 10 doctype | 0 when id unknown |
| `tagName(id)` | lower-case tag | elements only, else "" |
| `parent(id)` | parent id or 0 | |
| `children(id)` | `[ids]` of ALL child nodes incl. text | |
| `firstChild(id)`, `lastChild(id)`, `nextSibling(id)`, `prevSibling(id)` | id or 0 | |
| `createElement(tag)` | new id (detached) | tag lower-cased |
| `createText(data)` | new id | |
| `createComment(data)` | new id | |
| `appendChild(parentId, childId)` | childId | detaches child from old parent first |
| `insertBefore(parentId, childId, refId)` | childId | refId 0 == append |
| `removeChild(parentId, childId)` | childId | |
| `cloneNode(id, deep)` | new id | |
| `attached(id)` | bool | connected to document root |

### Content
| call | returns | notes |
|---|---|---|
| `nodeValue(id)` | text/comment data | |
| `setNodeValue(id, s)` | nil | |
| `textContent(id)` | concatenated text of subtree | |
| `setTextContent(id, s)` | nil | replaces children with one text node |
| `innerHTML(id)` | serialised children | |
| `setInnerHTML(id, html)` | nil | parsed as a fragment in context of the element; new nodes get ids |
| `outerHTML(id)` | serialised node | |
| `insertAdjacentHTML(id, position, html)` | nil | beforebegin/afterbegin/beforeend/afterend |

### Attributes
| call | returns | notes |
|---|---|---|
| `getAttr(id, name)` | string or null | null when absent |
| `setAttr(id, name, value)` | nil | |
| `removeAttr(id, name)` | nil | |
| `hasAttr(id, name)` | bool | |
| `attrNames(id)` | `[names]` in source order | |

### Query
| call | returns | notes |
|---|---|---|
| `querySelector(rootId, sel)` | id or 0 | rootId 0 == document; throws SyntaxError on bad selector |
| `querySelectorAll(rootId, sel)` | `[ids]` | |
| `matches(id, sel)` | bool | |
| `getElementById(id)` | id or 0 | |
| `elementsByTag(rootId, tag)`, `elementsByClass(rootId, cls)`, `elementsByName(name)` | `[ids]` | |

### Form state (live value separate from the `value` attribute)
| call | returns |
|---|---|
| `getValue(id)` | current value of input/textarea/select |
| `setValue(id, v)` | nil |
| `getChecked(id)` | bool |
| `setChecked(id, bool)` | nil |
| `selectedIndex(id)`, `setSelectedIndex(id, i)` | int / nil |
| `submitForm(id)` | nil — Go performs the navigation (GET/POST) |

### Environment
| call | returns | notes |
|---|---|---|
| `log(level, text)` | nil | into the console ring: log/info/warn/error/debug |
| `url()` | current document URL | |
| `navigate(url)` | nil | schedules a navigation after the current script turn |
| `userAgent()` | string | |
| `viewport()` | `{width,height,dsf,mobile}` | |
| `now()` | ms since page start (float) | |
| `http(method, url, headersObj, body)` | `{status, statusText, headers:{...}, body, url}` | synchronous, through the tab client with cookies; recorded in the network ring |
| `storageGet(kind, key)` | string or null | kind: local / session |
| `storageSet(kind, key, value)`, `storageRemove(kind, key)`, `storageClear(kind)` | nil | |
| `storageKeys(kind)` | `[keys]` | |
| `cookie()` / `setCookie(str)` | string / nil | document.cookie |
| `dialog(kind, message, defaultValue)` | `{accepted:bool, value:string}` | alert/confirm/prompt through the armed response |
| `mutation(op, id)` | nil | optional telemetry hook; prelude may call after writes |

## Globals the prelude must define

`window` (=== `globalThis`, `self`, `top`, `parent`, `frames`), `document`,
`Node`, `Element`, `HTMLElement` and common subclasses (`HTMLInputElement`,
`HTMLSelectElement`, `HTMLTextAreaElement`, `HTMLFormElement`,
`HTMLAnchorElement`, `HTMLButtonElement`, `HTMLImageElement`,
`HTMLScriptElement`, `HTMLStyleElement`, `HTMLDivElement`, `HTMLSpanElement`,
`HTMLTemplateElement`, `HTMLCanvasElement` with a stub `getContext` returning
null, `SVGElement`), `Text`, `Comment`, `DocumentFragment`, `Document`,
`EventTarget`, `Event`, `CustomEvent`, `MouseEvent`, `KeyboardEvent`,
`InputEvent`, `FocusEvent`, `UIEvent`, `PointerEvent`, `TouchEvent`,
`ProgressEvent`, `ErrorEvent`, `PromiseRejectionEvent`, `NodeList`,
`HTMLCollection`, `DOMTokenList`, `NamedNodeMap` (minimal), `CSSStyleDeclaration`,
`Storage` (`localStorage`, `sessionStorage`), `Location` (`location`,
`document.location`), `History` (`history` with pushState/replaceState updating
the URL via `__sw`), `Navigator` (`navigator`: userAgent, language, languages,
platform, onLine, cookieEnabled, hardwareConcurrency, userAgentData undefined,
clipboard stub, sendBeacon), `Screen` (`screen`), `MutationObserver`
(records mutations made through the prelude and delivers them as microtasks),
`IntersectionObserver` and `ResizeObserver` (stubs that call back once with
the element visible), `requestAnimationFrame`/`cancelAnimationFrame` (timer at
16 ms), `requestIdleCallback`, `setTimeout`/`setInterval`/`clearTimeout`/
`clearInterval`, `queueMicrotask`, `fetch` (returning a real `Response` with
`text()`, `json()`, `arrayBuffer()`, `blob()` minimal, `ok`, `status`,
`headers` as `Headers`), `Headers`, `Request`, `Response`, `AbortController`,
`AbortSignal`, `XMLHttpRequest` (sync and async via timers), `FormData`,
`URL`, `URLSearchParams`, `TextEncoder`, `TextDecoder`, `atob`, `btoa`,
`structuredClone`, `matchMedia` (returns `{matches:false, media, addListener,
removeListener, addEventListener, removeEventListener}` except for
`prefers-color-scheme` which consults `__sw.viewport()`), `getComputedStyle`
(returns the element's inline style object with `getPropertyValue`),
`scrollTo`/`scrollBy`/`scroll` no-ops, `innerWidth`/`innerHeight`/
`outerWidth`/`outerHeight`/`devicePixelRatio` from viewport,
`performance` (`now`, `mark`, `measure`, `getEntriesByType` returning []),
`crypto.getRandomValues` and `crypto.randomUUID`, `console` (all levels through
`__sw.log`, with `%s`-style formatting and object pretty-printing via
JSON.stringify with cycle guard), `alert`/`confirm`/`prompt` via
`__sw.dialog`, `CustomElementRegistry` stub `customElements.define` that
records definitions and upgrades elements created later (connectedCallback
on append), `DOMParser` (`parseFromString` using `setInnerHTML` on a detached
element), `Image`, `Option`, `Audio` stubs, `getSelection` stub,
`requestFullscreen` no-op, `Intl` minimal (`DateTimeFormat`, `NumberFormat`
with `format` returning `String(x)` / `toLocaleString`), `WebSocket` stub
whose constructor sets readyState 3 and fires `error` then `close`, `Worker`
stub that throws, `Notification` stub.

## Element wrappers

`__sw` returns ids. The prelude keeps `const wrappers = new Map()` from id to
wrapper object and returns the SAME object for the same id every time, so
expando properties (React's `__reactFiber$…`, jQuery data) persist. A wrapper
is created lazily by `wrap(id)` which picks the class from `nodeType` and
`tagName`. `Element.prototype` getters read through to `__sw` on every access
(no caching of children or attributes), so tree changes made from Go (actions,
`setInnerHTML` from another script) are always visible.

Properties that reflect attributes (`id`, `className`, `href`, `src`, `type`,
`name`, `value` for the attribute part, `title`, `alt`, `placeholder`, `role`,
`tabIndex`, `disabled`, `checked` (live via getChecked), `selected`,
`hidden`, `lang`, `dir`, `htmlFor`, `action`, `method`, `target`, `rel`,
`download`, `width`, `height`, `loading`, `srcset`, `sizes`, `for`,
`content`, `charset`, `async`, `defer`, `crossOrigin`, `integrity`,
`referrerPolicy`, `nonce`, `autocomplete`, `autofocus`, `required`,
`readOnly`, `multiple`, `min`, `max`, `step`, `pattern`, `maxLength`,
`minLength`, `size`, `rows`, `cols`, `wrap`, `colSpan`, `rowSpan`, `scope`,
`headers`, `open`, `label`, `default`, `cite`, `dateTime`, `data`,
`controls`, `autoplay`, `loop`, `muted`, `poster`, `preload`, `playsInline`,
`kind`, `srclang`, `isMap`, `useMap`, `shape`, `coords`, `ping`, `hreflang`,
`media`, `sandbox`, `allow`, `allowFullscreen`, `scrolling`, `frameBorder`,
`align`, `valign`, `bgColor`, `border`, `cellPadding`, `cellSpacing`,
`noValidate`, `formAction`, `formMethod`, `formTarget`, `formNoValidate`,
`enctype`, `encoding`, `acceptCharset`, `accept`, `capture`, `dirName`,
`inputMode`, `list`, `translate`, `draggable`, `spellcheck`,
`contentEditable`, `isContentEditable`, `accessKey`, `slot`, `part`,
`popover`, `inert`) are generated from a table, not written by hand.

`dataset` is a Proxy over `data-*` attributes. `style` is a
`CSSStyleDeclaration` backed by the `style` attribute, with camelCase and
`setProperty`/`getPropertyValue`/`removeProperty`/`cssText`. `classList` is a
`DOMTokenList` backed by `class`. `innerText` === `textContent` for reads and
writes. `getBoundingClientRect` returns a deterministic box from the Go side
(`__sw.box(id)` → `{x,y,width,height}`, may be all zeros) and
`offsetWidth/Height/Top/Left`, `clientWidth/Height`, `scrollWidth/Height/Top/Left`
return values from it. `focus()`/`blur()` track `document.activeElement` and
dispatch focus/blur/focusin/focusout. `click()` dispatches a MouseEvent
`click` and then runs the default action through `__swDefaultAction(id)`
exported for Go (link navigation, submit, checkbox toggle) unless
`preventDefault` was called.

## Events

Full `EventTarget` with capture/bubble/`stopPropagation`/
`stopImmediatePropagation`/`preventDefault`/`defaultPrevented`/`composedPath`,
`once`/`passive`/`capture` options and `on*` handler properties for all
common events on elements, `document` and `window`. The propagation path is
`[target, ...ancestors from __sw.parent, document, window]`.

Exported for Go (defined on `globalThis`):

- `__swDispatch(id, type, init)` → bool `true` when the default action should
  proceed (not prevented). `init` is a plain object with `bubbles`,
  `cancelable`, `key`, `code`, `button`, `clientX`, `clientY`, `detail`,
  `data`, `inputType`, `relatedTarget` as needed. The prelude chooses the
  event class from the type.
- `__swDispatchKey(id, key)` → dispatches keydown/keypress/keyup with proper
  `key`/`code` and, for printable keys on inputs, updates the value and
  dispatches `input` (`beforeinput` first). Returns `true` unless prevented.
- `__swMouse(type, x, y)` → dispatches to `document.elementFromPoint(x,y)`
  (which uses `__sw.elementFromPoint(x,y)`), returns bool.
- `__swTouch(id, type)`, `__swDrag(srcId, dstId)` (dragstart, dragenter,
  dragover, drop, dragend with a `DataTransfer`), `__swWheel(dx, dy)`.
- `__swSetValueFromGo(id, value)` → sets the live value and dispatches
  `input` and `change` (used by fill/select actions).
- `__swRunTimers(nowMs)` → runs every timer whose deadline ≤ nowMs (in
  deadline order, re-arming intervals), returns the ms until the next timer
  or -1 when none are pending.
- `__swFlushMutationObservers()` → delivers queued MutationObserver records.
- `__swFireLifecycle(name)` → `name` is `DOMContentLoaded` (dispatch on
  document, set `readyState=interactive`), `load` (set `readyState=complete`,
  dispatch `load` on window, then `pageshow`), `beforeunload`/`unload`.
- `__swSetDocument(url, readyState)` → called by Go after a navigation
  before running scripts; resets wrappers, timers, listeners and observers.
- `__swEvalResult(fnBodyOrExpr)` → NOT needed; Go evaluates directly.

## Timers

The prelude owns the timer heap. `setTimeout` records `{id, when: now+delay,
fn, args, interval}` and never calls back into Go. Go pumps the loop:

```
for budget not exhausted:
    vm.ExecutePendingJobs()        // microtasks
    next := __swRunTimers(nowMs)   // fire due timers
    vm.ExecutePendingJobs()
    if no timers pending and no jobs: break
    sleep(min(next, remaining budget))
```

## Evaluation from agents

`Runtime.evaluate(expr)` wraps the expression so that an `await` works and
the completion value is JSON-serialised: the prelude exports
`__swSerialize(v)` which turns DOM nodes into `{nodeType, tagName, id,
sweefRef}`, functions into `"function"`, errors into `{name, message, stack}`,
Maps/Sets into arrays, cycles into `"[Circular]"`, and caps depth at 8.
Promises are awaited by Go through `.Then` plus the job pump.

## Platform layer host functions

`platformjs/boot.js` (registered as the `platform` prelude, see `platform.go`) adds three host
functions and one lazy accessor per newer web API; the implementation of an API is fetched and
evaluated on first use.

| call | returns | notes |
|---|---|---|
| `platformSrc(name)` | module source or `""` | `platformjs/<name>.js`, evaluated as `function (g, H)`; `h` is the shared helper module |
| `codec(op, ...)` | map | CompressionStream / DecompressionStream engine: `new(format, decompress)`, `push(id, b64)`, `pull(id)`, `end(id)`, `close(id)`; replies `{d: base64, more?: bool, err?: string}` |
| `opfs(op, path, ...)` | varies | Origin Private File System store for the tab's origin: `stat`, `get`, `list`, `remove`, `read`, `write`, `move`, `usage`; errors are `"Name: message"` and become DOMExceptions |

The prelude also exports `__swHistory` (the document's session-history entry list, used by the
Navigation API) and `__swHooks.formData` (called by `FormData(form)` so form-associated custom
elements can contribute values). Host functions may append to `jsVM.closers` to release goroutines
and handles when the VM closes.

