// View Transitions: document.startViewTransition and ViewTransition. There is no compositor, so the
// transition has no visual effect: the update callback runs, ready resolves, finished follows.
var $ = Symbol('vt');
var active = null;

function ViewTransitionTypeSet() { throw new TypeError('Illegal constructor'); }
ViewTransitionTypeSet.prototype = Object.create(Set.prototype, { constructor: { value: ViewTransitionTypeSet, writable: true, configurable: true } });
Object.defineProperty(ViewTransitionTypeSet.prototype, Symbol.toStringTag, { value: 'ViewTransitionTypeSet', configurable: true });
function typeSet(initial) {
  var s = new Set();
  Object.setPrototypeOf(s, ViewTransitionTypeSet.prototype);
  (initial || []).forEach(function (t) { Set.prototype.add.call(s, String(t)); });
  return s;
}
H.define(ViewTransitionTypeSet.prototype, 'add', function add(v) { Set.prototype.add.call(this, String(v)); return this; });

function ViewTransition() { throw new TypeError('Illegal constructor'); }
function slot(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
H.attrs(ViewTransition.prototype, {
  ready: function () { return slot(this).ready.promise; },
  finished: function () { return slot(this).finished.promise; },
  updateCallbackDone: function () { return slot(this).updateDone.promise; },
  types: function () { return slot(this).types; }
});
function skip(vt, s, reason) {
  if (s.state === 'done') return;
  var wasActive = s.state !== 'skipped';
  s.skipped = true;
  if (!s.readySettled) { s.readySettled = true; s.ready.reject(reason || H.exc('Transition was skipped', 'AbortError')); }
  if (active === vt) active = null;
  // finished follows the update callback: it fulfils or rejects with it
  s.state = 'skipped';
  s.whenUpdated.then(function () { if (!s.finishedSettled) { s.finishedSettled = true; s.finished.resolve(undefined); } }, function (e) { if (!s.finishedSettled) { s.finishedSettled = true; s.finished.reject(e); } });
}
H.define(ViewTransition.prototype, 'skipTransition', function skipTransition() { var s = slot(this); skip(this, s); });
H.tag(ViewTransition, 'ViewTransition');

function mk() { var d = {}; d.promise = new Promise(function (res, rej) { d.resolve = res; d.reject = rej; }); return d; }
function startViewTransition(arg) {
  var cb = null, types = [];
  if (typeof arg === 'function') cb = arg;
  else if (arg !== undefined && arg !== null) {
    if (typeof arg !== 'object') throw new TypeError("Failed to execute 'startViewTransition' on 'Document': The provided value is not of type 'StartViewTransitionOptions'.");
    if (arg.update !== undefined && arg.update !== null) {
      if (typeof arg.update !== 'function') throw new TypeError("Failed to execute 'startViewTransition' on 'Document': The provided callback is not a function.");
      cb = arg.update;
    }
    if (arg.types !== undefined && arg.types !== null) types = Array.from(arg.types);
  }
  var vt = Object.create(ViewTransition.prototype);
  var s = { state: 'pending', ready: mk(), finished: mk(), updateDone: mk(), types: typeSet(types), skipped: false, readySettled: false, finishedSettled: false };
  s.ready.promise.then(undefined, function () { }); // ready is marked handled, as in the spec
  Object.defineProperty(vt, $, { value: s });
  if (active) { var prev = active, ps = prev[$]; skip(prev, ps, H.exc('Skipped by a newer view transition', 'AbortError')); }
  active = vt;
  var self = this;
  function runUpdate() {
    var r;
    try { r = cb ? cb() : undefined; } catch (e) { r = Promise.reject(e); }
    return Promise.resolve(r);
  }
  s.whenUpdated = new Promise(function (res, rej) {
    setTimeout(function () {
      runUpdate().then(function () { s.updateDone.resolve(undefined); res(); }, function (e) { s.updateDone.reject(e); rej(e); });
    }, 0);
  });
  s.whenUpdated.then(function () {
    if (s.skipped) return;
    s.readySettled = true; s.ready.resolve(undefined);
    // no animations: the transition finishes as soon as it is ready
    Promise.resolve().then(function () {
      if (s.skipped) return;
      s.state = 'done'; s.finishedSettled = true; s.finished.resolve(undefined);
      if (active === vt) active = null;
    });
  }, function (e) {
    if (!s.readySettled) { s.readySettled = true; s.ready.reject(e); }
    if (!s.finishedSettled) { s.finishedSettled = true; s.finished.reject(e); }
    s.state = 'done';
    if (active === vt) active = null;
  });
  return vt;
}
H.define(g.Document.prototype, 'startViewTransition', startViewTransition);
H.attrs(g.Document.prototype, { activeViewTransition: function () { return active; } });
H.glob('ViewTransition', ViewTransition);
H.glob('ViewTransitionTypeSet', ViewTransitionTypeSet);
