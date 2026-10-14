// Navigation API (window.navigation), layered on the session-history list the prelude keeps for
// history.pushState / replaceState / back / forward (window.__swHistory).
var hist = g.__swHistory;
var sw = H.sw;
var $ = Symbol('nav'), $ev = Symbol('navevent');
var ET = g.EventTarget;
var HP = g.History.prototype;
var origPush = HP.pushState, origReplace = HP.replaceState, origBack = HP.back, origForward = HP.forward, origGo = HP.go;

function illegal() { throw new TypeError('Illegal constructor'); }
function handled(p) { p.then(undefined, function () { }); return p; }
function defer() { var d = {}; d.promise = handled(new Promise(function (res, rej) { d.resolve = res; d.reject = rej; })); return d; }
function cloneState(v) {
  if (v === undefined) return undefined;
  try { return g.structuredClone(v); } catch (e) { throw H.exc('The state could not be cloned.', 'DataCloneError'); }
}
function parseURL(str) {
  var o = new g.URL(String(str), g.location.href);
  var m = /^[a-z][a-z0-9+.\-]*:\/\/([^\/?#]*)/i.exec(o.href);
  if (m && m[1].indexOf('[') >= 0 && m[1].indexOf(']') < 0) throw new TypeError('Invalid URL'); // the URL class accepts an unterminated IPv6 host
  return o.href;
}
function abortError() { return H.exc('The navigation was aborted', 'AbortError'); }

// ---- NavigationHistoryEntry ------------------------------------------------------------------------
function NavigationHistoryEntry() { illegal(); }
Object.setPrototypeOf(NavigationHistoryEntry.prototype, ET.prototype);
Object.setPrototypeOf(NavigationHistoryEntry, ET);
var wrappers = new Map(); // entry.id -> NavigationHistoryEntry
function entryWrapper(e) {
  if (!e) return null;
  var w = wrappers.get(e.id);
  if (w) return w;
  w = Object.create(NavigationHistoryEntry.prototype);
  ET.call(w);
  Object.defineProperty(w, $, { value: { e: e, key: e.key, id: e.id, url: e.url, disposed: false } });
  wrappers.set(e.id, w);
  return w;
}
function eslot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s || !s.e) throw new TypeError('Illegal invocation'); return s; }
H.attrs(NavigationHistoryEntry.prototype, {
  key: function () { return eslot(this).key; },
  id: function () { return eslot(this).id; },
  url: function () { var s = eslot(this); return s.disposed ? null : s.url; },
  index: function () { var s = eslot(this); return s.disposed || s.id !== s.e.id ? -1 : hist.entries().indexOf(s.e); },
  sameDocument: function () { eslot(this); return true; }
});
H.define(NavigationHistoryEntry.prototype, 'getState', function getState() {
  var s = eslot(this);
  return s.e.nstate === undefined ? undefined : cloneState(s.e.nstate);
});
H.handlers(NavigationHistoryEntry.prototype, ['dispose']);
H.tag(NavigationHistoryEntry, 'NavigationHistoryEntry');

// ---- destination, transition, activation ------------------------------------------------------------------
function NavigationDestination() { illegal(); }
H.attrs(NavigationDestination.prototype, {
  url: function () { return this[$].url; },
  key: function () { return this[$].key; },
  id: function () { return this[$].id; },
  index: function () { return this[$].index; },
  sameDocument: function () { return this[$].sameDocument; }
});
H.define(NavigationDestination.prototype, 'getState', function getState() { var s = this[$]; return s.entry ? s.entry.getState() : cloneState(s.nstate); });
H.tag(NavigationDestination, 'NavigationDestination');
function makeDestination(d) { var o = Object.create(NavigationDestination.prototype); Object.defineProperty(o, $, { value: d }); return o; }

function NavigationTransition() { illegal(); }
H.attrs(NavigationTransition.prototype, {
  navigationType: function () { return this[$].type; },
  from: function () { return this[$].from; },
  committed: function () { return this[$].committed; },
  finished: function () { return this[$].finished; }
});
H.tag(NavigationTransition, 'NavigationTransition');
function NavigationActivation() { illegal(); }
H.attrs(NavigationActivation.prototype, {
  entry: function () { return this[$].entry; },
  from: function () { return this[$].from; },
  navigationType: function () { return this[$].type; }
});
H.tag(NavigationActivation, 'NavigationActivation');

// ---- events ----------------------------------------------------------------------------------------------------------
var NavigateEvent = H.eventClass('NavigateEvent', { navigationType: 'push', destination: null, canIntercept: false, userInitiated: false, hashChange: false, signal: null, formData: null, downloadRequest: null, info: undefined, hasUiVisibility: false, sourceElement: null }, null, 'destination');
H.define(NavigateEvent.prototype, 'intercept', function intercept(options) {
  var s = this[$ev];
  if (!s || !s.dispatching) throw H.exc("Failed to execute 'intercept' on 'NavigateEvent': intercept() may only be called while the navigate event is being dispatched.", 'InvalidStateError');
  if (this.defaultPrevented) throw H.exc("Failed to execute 'intercept' on 'NavigateEvent': intercept() may not be called if the event has been canceled.", 'InvalidStateError');
  if (!this.canIntercept) throw H.exc("Failed to execute 'intercept' on 'NavigateEvent': A navigation with URL '" + this.destination.url + "' cannot be intercepted by a document with URL '" + g.location.href + "'.", 'SecurityError');
  options = options || {};
  if (options.handler !== undefined && typeof options.handler !== 'function') throw new TypeError("Failed to execute 'intercept' on 'NavigateEvent': The provided callback is not a function.");
  ['focusReset', 'scroll'].forEach(function (k) {
    if (options[k] !== undefined && options[k] !== 'after-transition' && options[k] !== 'manual') throw new TypeError("Failed to execute 'intercept' on 'NavigateEvent': The provided value '" + options[k] + "' is not a valid enum value of type NavigationInterceptOptions" + k + '.');
  });
  s.intercepted = true;
  if (options.handler) s.handlers.push(options.handler);
});
H.define(NavigateEvent.prototype, 'scroll', function scroll() {
  var s = this[$ev];
  if (!s || !s.committed || s.finished) throw H.exc("Failed to execute 'scroll' on 'NavigateEvent': scroll() may only be called after the navigation has committed and before it finishes.", 'InvalidStateError');
});
var CurrentEntryChange = H.eventClass('NavigationCurrentEntryChangeEvent', { navigationType: null, from: null }, null, 'from');

// ---- Navigation ----------------------------------------------------------------------------------------------------------
function Navigation() { illegal(); }
Object.setPrototypeOf(Navigation.prototype, ET.prototype);
Object.setPrototypeOf(Navigation, ET);
var nav = Object.create(Navigation.prototype);
ET.call(nav);
var state = { transition: null, ongoing: null, known: [], activation: null };

function curEntry() { return hist.entries()[hist.index()]; }
function curWrapper() { return entryWrapper(curEntry()); }
function syncKnown() { state.known = hist.entries().slice(); }
H.attrs(Navigation.prototype, {
  currentEntry: function () { return curWrapper(); },
  transition: function () { return state.transition; },
  canGoBack: function () { return hist.index() > 0; },
  canGoForward: function () { return hist.index() < hist.entries().length - 1; },
  activation: function () {
    if (!state.activation) {
      var o = Object.create(NavigationActivation.prototype);
      Object.defineProperty(o, $, { value: { entry: curWrapper(), from: null, type: 'push' } });
      state.activation = o;
    }
    return state.activation;
  }
});
H.define(Navigation.prototype, 'entries', function entries() { return hist.entries().map(entryWrapper); });
H.handlers(Navigation.prototype, ['navigate', 'navigatesuccess', 'navigateerror', 'currententrychange']);

function fragmentOnly(a, b) {
  var x = a.split('#'), y = b.split('#');
  return x[0] === y[0] && (b.indexOf('#') >= 0) && a !== b;
}
function sameOrigin(url) { try { return new g.URL(url).origin === g.location.origin; } catch (e) { return false; } }

function finishTransition(op, ok, err) {
  if (state.ongoing === op) state.ongoing = null;
  if (state.transition && state.transition[$].op === op) state.transition = null;
  op.ev[$ev].finished = true;
  if (ok) {
    nav.dispatchEvent(new g.Event('navigatesuccess'));
    op.finished.resolve(curWrapper());
    if (op.tfinished) op.tfinished.resolve(undefined);
  } else {
    var ee = new g.ErrorEvent('navigateerror', { error: err, message: String(err && err.message || err) });
    nav.dispatchEvent(ee);
    op.finished.reject(err);
    if (op.tfinished) op.tfinished.reject(err);
  }
}
function abortOngoing(reason) {
  var op = state.ongoing;
  if (!op) return;
  state.ongoing = null;
  var err = reason || abortError();
  if (op.ac && !op.ac.signal.aborted) op.ac.abort(err);
  if (op.committedFlag === false) op.committed.reject(err);
  finishTransition(op, false, err);
}

// Run the handlers once committed, then settle `finished`.
function afterCommit(op) {
  var s = op.ev[$ev];
  s.committed = true;
  op.committedFlag = true;
  op.committed.resolve(curWrapper());
  if (!s.intercepted) { op.ev[$ev].finished = true; if (state.ongoing === op) state.ongoing = null; op.finished.resolve(curWrapper()); return; }
  var results = s.handlers.map(function (h) {
    try { return Promise.resolve(h()); } catch (e) { return Promise.reject(e); }
  });
  Promise.all(results).then(function () { if (!op.ac.signal.aborted) finishTransition(op, true); }, function (e) { if (!op.ac.signal.aborted) finishTransition(op, false, e); });
}

function commitOp(op) {
  if (op.type === 'traverse') {
    op.waitingTraverse = true;
    hist.traverse(op.delta);
    return;
  }
  var u = op.url;
  state.pendingState = { nstate: op.nstate };
  try {
    if (op.type === 'push') origPush.call(g.history, null, '', u); else origReplace.call(g.history, null, '', u);
  } finally { state.pendingState = null; }
  if (op.fragment) {
    var ev = new g.HashChangeEvent('hashchange', { oldURL: op.fromURL, newURL: u });
    setTimeout(function () { g.dispatchEvent(ev); }, 0);
  }
  afterCommit(op);
}

function run(op) {
  abortOngoing();
  op.committed = defer(); op.finished = defer();
  op.committedFlag = false;
  op.ac = new g.AbortController();
  state.ongoing = op;
  var cur = curWrapper();
  var dest = makeDestination(op.dest);
  var ev = new NavigateEvent('navigate', {
    cancelable: true, navigationType: op.type === 'traverse' ? 'traverse' : op.type, destination: dest,
    canIntercept: op.canIntercept, userInitiated: false, hashChange: !!op.hashChange, signal: op.ac.signal, info: op.info
  });
  op.ev = ev;
  Object.defineProperty(ev, $ev, { value: { handlers: [], intercepted: false, dispatching: true, committed: false, finished: false } });
  var ok = nav.dispatchEvent(ev);
  ev[$ev].dispatching = false;
  if (state.ongoing !== op) return { committed: op.committed.promise, finished: op.finished.promise };
  if (!ok) {
    var err = abortError();
    state.ongoing = null;
    op.ac.abort(err);
    op.committed.reject(err);
    nav.dispatchEvent(new g.ErrorEvent('navigateerror', { error: err, message: err.message }));
    op.finished.reject(err);
    return { committed: op.committed.promise, finished: op.finished.promise };
  }
  if (ev[$ev].intercepted) {
    var tr = Object.create(NavigationTransition.prototype);
    op.tfinished = defer();
    Object.defineProperty(tr, $, { value: { op: op, type: op.type, from: cur, committed: op.committed.promise, finished: op.tfinished.promise } });
    state.transition = tr;
    if (op.type === 'reload') afterCommit(op); else commitOp(op);
  } else if (op.type === 'reload') {
    sw.navigate(g.location.href);
  } else if (op.type === 'traverse' || op.fragment) {
    commitOp(op);
  } else {
    sw.navigate(op.url);
  }
  return { committed: op.committed.promise, finished: op.finished.promise };
}

function destFor(url, extra) {
  var d = { url: url, key: null, id: null, index: -1, sameDocument: false, nstate: undefined, entry: null };
  for (var k in extra) d[k] = extra[k];
  return d;
}
H.define(Navigation.prototype, 'navigate', function navigate(url, options) {
  if (arguments.length < 1) throw new TypeError("Failed to execute 'navigate' on 'Navigation': 1 argument required, but only 0 present.");
  options = options === undefined || options === null ? {} : options;
  var u;
  try { u = parseURL(url); } catch (e) { throw H.exc("Failed to execute 'navigate' on 'Navigation': Invalid URL '" + url + "'.", 'SyntaxError'); }
  var h = options.history === undefined ? 'auto' : String(options.history);
  if (h !== 'auto' && h !== 'push' && h !== 'replace') throw new TypeError("Failed to execute 'navigate' on 'Navigation': The provided value '" + h + "' is not a valid enum value of type NavigationHistoryBehavior.");
  var nstate = cloneState(options.state);
  var type = h === 'replace' || (h === 'auto' && u === g.location.href) ? 'replace' : 'push';
  var fragment = fragmentOnly(g.location.href, u);
  return run({ fromURL: g.location.href, type: type, url: u, nstate: nstate, info: options.info, canIntercept: sameOrigin(u), hashChange: fragment, fragment: fragment, dest: destFor(u, { sameDocument: fragment, nstate: nstate }) });
});
H.define(Navigation.prototype, 'reload', function reload(options) {
  options = options || {};
  var nstate = options.state === undefined ? undefined : cloneState(options.state);
  return run({ type: 'reload', url: g.location.href, nstate: nstate, info: options.info, canIntercept: sameOrigin(g.location.href), dest: destFor(g.location.href, { sameDocument: false, nstate: nstate !== undefined ? nstate : curEntry().nstate }) });
});
function traverseTo(entry, options) {
  options = options || {};
  var es = hist.entries(), idx = es.indexOf(entry.e || entry);
  var target = es[idx];
  var cur = hist.index();
  if (idx < 0) throw H.exc('Invalid key', 'InvalidStateError');
  if (idx === cur) {
    var w = entryWrapper(target), d = defer();
    d.resolve(w);
    return { committed: d.promise, finished: d.promise };
  }
  return run({ type: 'traverse', delta: idx - cur, url: target.url, info: options.info, canIntercept: sameOrigin(target.url), hashChange: fragmentOnly(es[cur].url, target.url), dest: destFor(target.url, { key: target.key, id: target.id, index: idx, sameDocument: true, entry: entryWrapper(target) }) });
}
H.define(Navigation.prototype, 'traverseTo', function traverseToM(key, options) {
  var es = hist.entries(), t = null;
  for (var i = 0; i < es.length; i++) if (es[i].key === String(key)) t = es[i];
  if (!t) throw H.exc("Failed to execute 'traverseTo' on 'Navigation': Invalid key.", 'InvalidStateError');
  return traverseTo({ e: t }, options);
});
H.define(Navigation.prototype, 'back', function back(options) {
  if (hist.index() <= 0) throw H.exc("Failed to execute 'back' on 'Navigation': Cannot go back.", 'InvalidStateError');
  return traverseTo({ e: hist.entries()[hist.index() - 1] }, options);
});
H.define(Navigation.prototype, 'forward', function forward(options) {
  if (hist.index() >= hist.entries().length - 1) throw H.exc("Failed to execute 'forward' on 'Navigation': Cannot go forward.", 'InvalidStateError');
  return traverseTo({ e: hist.entries()[hist.index() + 1] }, options);
});
H.define(Navigation.prototype, 'updateCurrentEntry', function updateCurrentEntry(options) {
  if (options === null || typeof options !== 'object' || !('state' in options)) throw new TypeError("Failed to execute 'updateCurrentEntry' on 'Navigation': The required member state is undefined.");
  var e = curEntry(), from = entryWrapper(e);
  e.nstate = cloneState(options.state);
  nav.dispatchEvent(new CurrentEntryChange('currententrychange', { navigationType: null, from: from }));
});
H.tag(Navigation, 'Navigation');

// ---- history hook: bookkeeping and currententrychange ------------------------------------------------------------------------
function isCurrent(op) { return op && state.ongoing === op; }
hist.hooks.push(function (kind, entry, prev, info) {
  var ps = state.pendingState;
  if (kind === 'push') {
    entry.nstate = ps ? ps.nstate : undefined;
    entryWrapper(entry);
  } else if (kind === 'replace') {
    var oldW = wrappers.get(prev.id) || entryWrapper(prev);
    wrappers.delete(prev.id);
    entry.id = 'i' + Math.random().toString(36).slice(2) + Date.now().toString(36);
    if (ps) entry.nstate = ps.nstate;
    entryWrapper(entry);
    state.known = hist.entries().slice();
    nav.dispatchEvent(new CurrentEntryChange('currententrychange', { navigationType: 'replace', from: oldW }));
    oldW[$].disposed = true;
    oldW.dispatchEvent(new g.Event('dispose'));
    return;
  }
  var fromW = entryWrapper(prev);
  // dispose entries that fell out of the list
  var now = hist.entries();
  state.known.forEach(function (k) {
    if (now.indexOf(k) < 0) {
      var w = wrappers.get(k.id);
      if (w) { w[$].disposed = true; w.dispatchEvent(new g.Event('dispose')); wrappers.delete(k.id); }
    }
  });
  state.known = now.slice();
  nav.dispatchEvent(new CurrentEntryChange('currententrychange', { navigationType: kind === 'traverse' ? 'traverse' : 'push', from: fromW }));
  if (kind === 'traverse' && state.ongoing && state.ongoing.waitingTraverse) {
    var op = state.ongoing;
    op.waitingTraverse = false;
    afterCommit(op);
  } else if (kind === 'push' && !ps) {
    // location.hash and other writers that did not go through navigate(): nothing to settle
  }
});
syncKnown();

// history.pushState / replaceState / back / forward fire a navigate event first (not cancelable for push/replace)
function silentNavigate(type, url) {
  var u;
  try { u = new g.URL(String(url), g.location.href).href; } catch (e) { return; }
  var ac = new g.AbortController();
  var ev = new NavigateEvent('navigate', { cancelable: false, navigationType: type, destination: makeDestination(destFor(u, { sameDocument: true })), canIntercept: sameOrigin(u), hashChange: fragmentOnly(g.location.href, u), signal: ac.signal });
  Object.defineProperty(ev, $ev, { value: { handlers: [], intercepted: false, dispatching: true, committed: false, finished: false } });
  nav.dispatchEvent(ev);
  ev[$ev].dispatching = false;
  ev[$ev].committed = true;
  return ev;
}
function wrapHistory(name, type, orig) {
  H.define(HP, name, function (state_, title, url) {
    var ev = silentNavigate(type, url === undefined || url === null ? g.location.href : url);
    var r = orig.apply(this, arguments);
    if (ev) {
      var s = ev[$ev];
      if (s.intercepted) {
        var results = s.handlers.map(function (h) { try { return Promise.resolve(h()); } catch (e) { return Promise.reject(e); } });
        Promise.all(results).then(function () { s.finished = true; nav.dispatchEvent(new g.Event('navigatesuccess')); }, function (e) { s.finished = true; nav.dispatchEvent(new g.ErrorEvent('navigateerror', { error: e, message: String(e && e.message || e) })); });
      }
    }
    return r;
  });
}
wrapHistory('pushState', 'push', origPush);
wrapHistory('replaceState', 'replace', origReplace);
function wrapTraverse(name, orig, deltaOf) {
  H.define(HP, name, function () {
    var d = deltaOf.apply(null, arguments), i = hist.index(), n = hist.entries().length;
    if (!d || i + d < 0 || i + d >= n) return orig.apply(this, arguments);
    traverseTo({ e: hist.entries()[i + d] });
  });
}
wrapTraverse('back', origBack, function () { return -1; });
wrapTraverse('forward', origForward, function () { return 1; });
wrapTraverse('go', origGo, function (n) { return Math.trunc(+n) || 0; });

[['Navigation', Navigation], ['NavigationHistoryEntry', NavigationHistoryEntry], ['NavigationDestination', NavigationDestination], ['NavigationTransition', NavigationTransition], ['NavigationActivation', NavigationActivation]].forEach(function (p) { H.glob(p[0], p[1]); });
Object.defineProperty(g, 'navigation', { get: function () { return nav; }, set: function (v) { Object.defineProperty(g, 'navigation', { value: v, writable: true, configurable: true, enumerable: true }); }, configurable: true, enumerable: true });
