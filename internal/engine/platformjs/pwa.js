// Notifications, Permissions, Web Push, Background Sync, Periodic Background Sync, Badging and Web Locks.
// The state lives in Go (pwa*.go); this file is the script-facing API for documents and for service workers.
// Evaluated lazily (see boot.js) in whichever VM first touches one of its names, so it must work in both.
var sw = g.__sw, ET = g.EventTarget;
var IS_SW = !!(sw.swOwnWorker && sw.swOwnWorker());

function terr(m) { return new TypeError(m); }
function dex(name, msg) { return new g.DOMException(msg, name); }
function rej(name, msg) { return Promise.reject(dex(name, msg)); }
function report(e) { if (typeof g.reportError === 'function') { try { g.reportError(e); } catch (x) { /* ignore */ } } }
function illegal() { throw terr('Illegal constructor'); }
function cls(name, base, ctor) {
  var C = ctor || function () { illegal(); };
  if (base) { C.prototype = Object.create(base.prototype, { constructor: { value: C, writable: true, configurable: true } }); Object.setPrototypeOf(C, base); }
  Object.defineProperty(C, 'name', { value: name, configurable: true });
  H.tag(C, name);
  return C;
}
function secure() { try { return !!sw.pwaSecure(); } catch (e) { return false; } }
function hostErrorOf(e) { return H.hostErr(e); }
function host(name) {
  var a = Array.prototype.slice.call(arguments, 1);
  try { return sw[name].apply(null, a); } catch (e) { throw hostErrorOf(e); }
}

// ---- asynchronous host calls ----------------------------------------------------------------------------------
var pend = new Map(), seq = 0;
function call(name) {
  var a = Array.prototype.slice.call(arguments, 1);
  return new Promise(function (resolve, reject) {
    var token = ++seq;
    pend.set(token, { resolve: resolve, reject: reject });
    try { sw[name].apply(null, [token].concat(a)); } catch (e) { pend.delete(token); reject(hostErrorOf(e)); }
    subscribe();
  });
}
var subscribed = false;
function subscribe() { if (!subscribed) { subscribed = true; try { sw.pwaSubscribe(); } catch (e) { /* ignore */ } } }

// ---- base64url ------------------------------------------------------------------------------------------------
function toB64(buf) {
  var u8 = buf instanceof Uint8Array ? buf : new Uint8Array(buf.buffer || buf, buf.byteOffset || 0, buf.byteLength !== undefined ? buf.byteLength : buf.length), s = '';
  for (var i = 0; i < u8.length; i += 0x2000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x2000));
  return g.btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function fromB64(str) {
  var s = String(str).replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4) s += '=';
  var bin = g.atob(s), u8 = new Uint8Array(bin.length);
  for (var i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return u8;
}
function copyBuffer(u8) { var b = new ArrayBuffer(u8.length); new Uint8Array(b).set(u8); return b; }

// ---- Notification ---------------------------------------------------------------------------------------------------
var notes = new Map(); // id -> live Notification object of this document
var DIRS = ['auto', 'ltr', 'rtl'];
function absURL(v) {
  if (v === undefined || v === null || v === '') return '';
  try { return new g.URL(String(v), IS_SW ? g.location.href : (g.document && g.document.baseURI) || g.location.href).href; } catch (e) { return ''; }
}
function permName(p) { return p === 'prompt' ? 'default' : p; }

// Turns NotificationOptions into the record Go keeps; throws TypeError like the constructors do.
function normalize(fn, title, options, persistent) {
  var o = options === undefined || options === null ? {} : options;
  if (typeof o !== 'object') throw terr("Failed to execute '" + fn + "': parameter 2 ('options') is not an object.");
  var dir = o.dir === undefined ? 'auto' : String(o.dir);
  if (DIRS.indexOf(dir) < 0) throw terr("Failed to execute '" + fn + "': Failed to read the 'dir' property from 'NotificationOptions': The provided value '" + dir + "' is not a valid enum value of type NotificationDirection.");
  var tag = o.tag === undefined ? '' : String(o.tag);
  if (o.renotify && !tag) throw terr("Failed to execute '" + fn + "': Notifications which set the renotify flag must specify a non-empty tag.");
  var vibrate = o.vibrate;
  if (vibrate !== undefined && vibrate !== null) vibrate = typeof vibrate === 'number' ? [vibrate] : Array.prototype.slice.call(vibrate).map(Number);
  var silent = o.silent === undefined ? null : o.silent === null ? null : !!o.silent;
  if (silent && vibrate && vibrate.length) throw terr("Failed to execute '" + fn + "': Silent notifications must not specify vibration patterns.");
  var actions = [];
  if (o.actions !== undefined && o.actions !== null) {
    if (!persistent && o.actions.length) throw terr("Failed to construct 'Notification': Actions are only supported for persistent notifications.");
    Array.prototype.slice.call(o.actions).forEach(function (a) {
      if (a === null || typeof a !== 'object') throw terr("Failed to execute '" + fn + "': The provided value is not of type 'NotificationAction'.");
      if (a.action === undefined) throw terr("Failed to execute '" + fn + "': Failed to read the 'action' property from 'NotificationAction': Required member is undefined.");
      if (a.title === undefined) throw terr("Failed to execute '" + fn + "': Failed to read the 'title' property from 'NotificationAction': Required member is undefined.");
      if (actions.length < Notification.maxActions) actions.push({ action: String(a.action), title: String(a.title), icon: absURL(a.icon) });
    });
  }
  var data = null;
  if (o.data !== undefined) data = o.data;
  return {
    title: String(title), body: o.body === undefined ? '' : String(o.body), tag: tag, dir: dir, lang: o.lang === undefined ? '' : String(o.lang),
    icon: absURL(o.icon), image: absURL(o.image), badge: absURL(o.badge), renotify: !!o.renotify, silent: silent, requireInteraction: !!o.requireInteraction,
    timestamp: o.timestamp === undefined ? Date.now() : Number(o.timestamp), vibrate: vibrate || [], actions: actions,
    data: g.__swSer(data === undefined ? null : data), persistent: !!persistent
  };
}

function fill(n, rec) {
  var keys = ['title', 'body', 'tag', 'dir', 'lang', 'icon', 'image', 'badge', 'renotify', 'silent', 'requireInteraction', 'timestamp', 'vibrate', 'actions'];
  Object.defineProperty(n, '_', { value: { rec: rec, id: null, persistent: !!rec.persistent, data: rec.data, closing: false }, writable: true, configurable: true });
  keys.forEach(function (k) { Object.defineProperty(n, '_' + k, { value: rec[k], writable: false, configurable: true }); });
}

function Notification(title, options) {
  if (!new.target) throw terr("Failed to construct 'Notification': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (IS_SW) throw terr("Failed to construct 'Notification': Illegal constructor. Use ServiceWorkerRegistration.showNotification() instead.");
  if (arguments.length < 1) throw terr("Failed to construct 'Notification': 1 argument required, but only 0 present.");
  ET.call(this);
  var rec = normalize('Notification', title, options, false);
  fill(this, rec);
  var self = this;
  var queue = typeof g.queueMicrotask === 'function' ? function (f) { g.setTimeout(f, 0); } : function (f) { g.setTimeout(f, 0); };
  if (!secure() || sw.pwaPermission('notifications') !== 'granted') { queue(function () { fire(self, 'error'); }); return; }
  subscribe();
  call('pwaNotifyShow', JSON.stringify(rec)).then(function (id) {
    self._.id = id; notes.set(id, self);
    if (self._.closing) { sw.pwaNotifyClose(id); return; }
    fire(self, 'show');
  }, function () { fire(self, 'error'); });
}
Notification.prototype = Object.create(ET.prototype, { constructor: { value: Notification, writable: true, configurable: true } });
Object.defineProperty(Notification, 'name', { value: 'Notification', configurable: true });
H.tag(Notification, 'Notification');
function fire(target, type) {
  var ev = new g.Event(type);
  try { target.dispatchEvent(ev); } catch (e) { report(e); }
}
['title', 'body', 'tag', 'dir', 'lang', 'icon', 'image', 'badge', 'renotify', 'silent', 'requireInteraction', 'timestamp', 'vibrate', 'actions'].forEach(function (k) {
  Object.defineProperty(Notification.prototype, k, {
    get: function () { var v = this['_' + k]; return Array.isArray(v) ? v.slice() : v; }, enumerable: true, configurable: true
  });
});
Object.defineProperty(Notification.prototype, 'data', { get: function () { return this._.data === undefined || this._.data === null ? null : g.__swDe(this._.data); }, enumerable: true, configurable: true });
H.define(Notification.prototype, 'close', function close() {
  var s = this._;
  if (!s) return;
  if (s.id) sw.pwaNotifyClose(s.id); else s.closing = true;
});
H.handlers(Notification.prototype, ['click', 'show', 'error', 'close']);
Object.defineProperty(Notification, 'permission', { get: function () { return secure() ? permName(sw.pwaPermission('notifications')) : 'denied'; }, enumerable: true, configurable: true });
Object.defineProperty(Notification, 'maxActions', { get: function () { return 2; }, enumerable: true, configurable: true });
if (!IS_SW) {
  H.define(Notification, 'requestPermission', function requestPermission(callback) {
    if (callback !== undefined && callback !== null && typeof callback !== 'function') return Promise.reject(terr("Failed to execute 'requestPermission' on 'Notification': The callback provided as parameter 1 is not a function."));
    var p = secure() ? call('pwaRequestPermission', 'notifications').then(permName) : Promise.resolve('denied');
    if (typeof callback === 'function') p.then(function (v) { try { callback(v); } catch (e) { report(e); } });
    return p;
  });
}

// a Notification of a registration (showNotification / getNotifications / notificationclick): the object exists
// only to be read and closed
function persistentNotification(rec) {
  var n = Object.create(Notification.prototype);
  ET.call(n);
  rec.persistent = true;
  fill(n, rec);
  n._.id = rec.id;
  return n;
}

// ---- Permissions ------------------------------------------------------------------------------------------------------
var PERMISSION_NAMES = ['geolocation', 'notifications', 'push', 'midi', 'camera', 'microphone', 'speaker-selection', 'device-info', 'background-fetch', 'background-sync',
  'bluetooth', 'persistent-storage', 'ambient-light-sensor', 'accelerometer', 'gyroscope', 'magnetometer', 'screen-wake-lock', 'nfc', 'display-capture', 'clipboard-read',
  'clipboard-write', 'payment-handler', 'idle-detection', 'periodic-background-sync', 'system-wake-lock', 'storage-access', 'window-management', 'local-fonts', 'top-level-storage-access'];
// Other parts of the browser (camera, microphone...) can answer for their own names: g.__permHandlers[name] = function (descriptor) -> state
var handlers = (function () {
  if (!g.__permHandlers) Object.defineProperty(g, '__permHandlers', { value: {}, configurable: true });
  return g.__permHandlers;
})();
function stateOf(desc) {
  var n = desc.name;
  if (handlers[n]) { try { return handlers[n](desc); } catch (e) { /* fall through */ } }
  switch (n) {
    case 'notifications': case 'push': return secure() ? sw.pwaPermission('notifications') : 'denied';
    case 'periodic-background-sync': return secure() ? sw.pwaPermission('periodic-background-sync') : 'denied';
    case 'clipboard-read': return secure() ? sw.pwaPermission('clipboard-read') : 'denied';
    case 'clipboard-write': return secure() && g.document && g.document.visibilityState === 'visible' ? 'granted' : 'prompt';
    case 'background-sync': return secure() ? sw.pwaPermission('background-sync') : 'denied';
  }
  return 'prompt';
}
var statuses = [];
var PermissionStatus = cls('PermissionStatus', ET);
H.attrs(PermissionStatus.prototype, { name: function () { return this._.name; }, state: function () { return this._.state; } });
H.handlers(PermissionStatus.prototype, ['change']);
var Permissions = cls('Permissions');
H.define(Permissions.prototype, 'query', function query(desc) {
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'query' on 'Permissions': 1 argument required, but only 0 present."));
  if (desc === null || typeof desc !== 'object') return Promise.reject(terr("Failed to execute 'query' on 'Permissions': parameter 1 is not of type 'PermissionDescriptor'."));
  var name = desc.name === undefined ? undefined : String(desc.name);
  if (name === undefined) return Promise.reject(terr("Failed to execute 'query' on 'Permissions': Failed to read the 'name' property from 'PermissionDescriptor': Required member is undefined."));
  if (PERMISSION_NAMES.indexOf(name) < 0 && !handlers[name]) return Promise.reject(terr("Failed to execute 'query' on 'Permissions': Failed to read the 'name' property from 'PermissionDescriptor': The provided value '" + name + "' is not a valid enum value of type PermissionName."));
  var s = Object.create(PermissionStatus.prototype);
  ET.call(s);
  Object.defineProperty(s, '_', { value: { name: name, desc: { name: name, userVisibleOnly: !!desc.userVisibleOnly }, state: stateOf({ name: name, userVisibleOnly: !!desc.userVisibleOnly }) }, writable: true });
  subscribe();
  statuses.push(typeof WeakRef === 'function' ? new WeakRef(s) : { deref: function () { return s; } });
  return Promise.resolve(s);
});
var permissions = Object.create(Permissions.prototype);
// the media layer (camera, microphone, display-capture) asks for a refresh when the user changes a decision
Object.defineProperty(g, '__swPermRefresh', { value: function () { refreshStatuses(); }, configurable: true, writable: true });
function refreshStatuses() {
  statuses = statuses.filter(function (w) {
    var s = w.deref();
    if (!s) return false;
    var next = stateOf(s._.desc);
    if (next !== s._.state) { s._.state = next; fire(s, 'change'); }
    return true;
  });
}

// ---- push -------------------------------------------------------------------------------------------------------------------
var PushSubscriptionOptions = cls('PushSubscriptionOptions');
H.attrs(PushSubscriptionOptions.prototype, {
  userVisibleOnly: function () { return true; },
  applicationServerKey: function () { return this._.key ? copyBuffer(fromB64(this._.key)) : null; }
});
var PushSubscription = cls('PushSubscription');
function makeSubscription(reg, d) {
  var s = Object.create(PushSubscription.prototype);
  var opts = Object.create(PushSubscriptionOptions.prototype);
  Object.defineProperty(opts, '_', { value: { key: d.appKey } });
  Object.defineProperty(s, '_', { value: { reg: reg, d: d, options: opts } });
  return s;
}
H.attrs(PushSubscription.prototype, {
  endpoint: function () { return this._.d.endpoint; },
  expirationTime: function () { return null; },
  options: function () { return this._.options; }
});
H.define(PushSubscription.prototype, 'getKey', function getKey(name) {
  var n = String(name);
  if (n === 'p256dh') return copyBuffer(fromB64(this._.d.p256dh));
  if (n === 'auth') return copyBuffer(fromB64(this._.d.auth));
  return null;
});
H.define(PushSubscription.prototype, 'toJSON', function toJSON() {
  return { endpoint: this._.d.endpoint, expirationTime: null, keys: { p256dh: this._.d.p256dh, auth: this._.d.auth } };
});
H.define(PushSubscription.prototype, 'unsubscribe', function unsubscribe() { return call('pwaPushUnsubscribe', this._.reg._id); });

var PushManager = cls('PushManager');
Object.defineProperty(PushManager, 'supportedContentEncodings', { get: function () { return Object.freeze(['aes128gcm']); }, enumerable: true, configurable: true });
function keyOf(v) {
  if (v === undefined || v === null) return '';
  if (typeof v === 'string') { try { fromB64(v); return v.replace(/=+$/, ''); } catch (e) { return null; } }
  try { return toB64(v instanceof ArrayBuffer ? new Uint8Array(v) : v); } catch (e) { return null; }
}
H.define(PushManager.prototype, 'subscribe', function subscribe_(options) {
  var reg = this._.reg, o = options === undefined || options === null ? {} : options;
  if (typeof o !== 'object') return Promise.reject(terr("Failed to execute 'subscribe' on 'PushManager': parameter 1 is not of type 'PushSubscriptionOptionsInit'."));
  var key = keyOf(o.applicationServerKey);
  if (!o.userVisibleOnly) return rej('NotSupportedError', "Failed to execute 'subscribe' on 'PushManager': Chrome currently only supports the Push API for subscriptions that will result in user-visible messages. You can indicate this by calling pushManager.subscribe({userVisibleOnly: true}) instead.");
  if (key === null) return rej('InvalidAccessError', "Failed to execute 'subscribe' on 'PushManager': The provided applicationServerKey is not valid.");
  if (!secure()) return rej('NotAllowedError', 'Registration failed - permission denied');
  return call('pwaPushSubscribe', reg._id, key).then(function (d) { return makeSubscription(reg, d); });
});
H.define(PushManager.prototype, 'getSubscription', function getSubscription() {
  var reg = this._.reg;
  return new Promise(function (resolve, reject) {
    try { var d = host('pwaPushGet', reg._id); resolve(d ? makeSubscription(reg, d) : null); } catch (e) { reject(e); }
  });
});
H.define(PushManager.prototype, 'permissionState', function permissionState(options) {
  var o = options || {};
  if (!o.userVisibleOnly) return rej('NotSupportedError', "Failed to execute 'permissionState' on 'PushManager': Chrome currently only supports the Push API for subscriptions that will result in user-visible messages. You can indicate this by calling pushManager.permissionState({userVisibleOnly: true}) instead.");
  return Promise.resolve(secure() ? sw.pwaPermission('push') : 'denied');
});
var pushManagers = new WeakMap();
function pushManagerOf(reg) {
  var m = pushManagers.get(reg);
  if (!m) { m = Object.create(PushManager.prototype); Object.defineProperty(m, '_', { value: { reg: reg } }); pushManagers.set(reg, m); }
  return m;
}

// ---- Background Sync ---------------------------------------------------------------------------------------------------------
var SyncManager = cls('SyncManager');
H.define(SyncManager.prototype, 'register', function register(tag) {
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'register' on 'SyncManager': 1 argument required, but only 0 present."));
  var reg = this._.reg;
  return new Promise(function (resolve, reject) {
    try { host('pwaSyncRegister', reg._id, String(tag)); resolve(); } catch (e) { reject(e); }
  });
});
H.define(SyncManager.prototype, 'getTags', function getTags() {
  var reg = this._.reg;
  return new Promise(function (resolve, reject) { try { resolve(host('pwaSyncTags', reg._id)); } catch (e) { reject(e); } });
});
var PeriodicSyncManager = cls('PeriodicSyncManager');
H.define(PeriodicSyncManager.prototype, 'register', function register(tag, options) {
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'register' on 'PeriodicSyncManager': 1 argument required, but only 0 present."));
  var reg = this._.reg, min = options && options.minInterval !== undefined ? Number(options.minInterval) : 0;
  if (!(min >= 0) || !isFinite(min)) return Promise.reject(terr("Failed to execute 'register' on 'PeriodicSyncManager': Failed to read the 'minInterval' property from 'BackgroundSyncOptions': Value is not a valid unsigned long long."));
  return new Promise(function (resolve, reject) {
    try { host('pwaPeriodicRegister', reg._id, String(tag), Math.floor(min)); resolve(); } catch (e) { reject(e); }
  });
});
H.define(PeriodicSyncManager.prototype, 'unregister', function unregister(tag) {
  var reg = this._.reg;
  return new Promise(function (resolve, reject) { try { host('pwaPeriodicUnregister', reg._id, String(tag)); resolve(); } catch (e) { reject(e); } });
});
H.define(PeriodicSyncManager.prototype, 'getTags', function getTags() {
  var reg = this._.reg;
  return new Promise(function (resolve, reject) { try { resolve(host('pwaPeriodicTags', reg._id)); } catch (e) { reject(e); } });
});
var syncManagers = new WeakMap();
function managerOf(reg, C, kind) {
  var m = syncManagers.get(reg);
  if (!m) { m = {}; syncManagers.set(reg, m); }
  if (!m[kind]) { m[kind] = Object.create(C.prototype); Object.defineProperty(m[kind], '_', { value: { reg: reg } }); }
  return m[kind];
}

// ---- events of the worker -------------------------------------------------------------------------------------------------------
var Ext = g.ExtendableEvent || g.Event;
function evClass(name, init, required) {
  var C = function (type, i) {
    if (!new.target) throw terr("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    if (arguments.length < 1) throw terr("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
    Ext.call(this, type, i);
    var ii = i || {};
    if (required && ii[required] === undefined) throw terr("Failed to construct '" + name + "': Failed to read the '" + required + "' property from '" + name + "Init': Required member is undefined.");
    init.call(this, ii);
  };
  return cls(name, Ext, C);
}
var NotificationEvent = evClass('NotificationEvent', function (i) {
  Object.defineProperty(this, 'notification', { value: i.notification, enumerable: true });
  Object.defineProperty(this, 'action', { value: i.action === undefined ? '' : String(i.action), enumerable: true });
}, 'notification');
var SyncEvent = evClass('SyncEvent', function (i) {
  Object.defineProperty(this, 'tag', { value: String(i.tag), enumerable: true });
  Object.defineProperty(this, 'lastChance', { value: !!i.lastChance, enumerable: true });
}, 'tag');
var PeriodicSyncEvent = evClass('PeriodicSyncEvent', function (i) {
  Object.defineProperty(this, 'tag', { value: String(i.tag), enumerable: true });
}, 'tag');
var PushMessageData = cls('PushMessageData');
function makeData(b64) { var d = Object.create(PushMessageData.prototype); Object.defineProperty(d, '_', { value: { u8: fromB64(b64) } }); return d; }
H.define(PushMessageData.prototype, 'arrayBuffer', function arrayBuffer() { return Promise.resolve(copyBuffer(this._.u8)); });
H.define(PushMessageData.prototype, 'bytes', function bytes() { return Promise.resolve(new Uint8Array(copyBuffer(this._.u8))); });
H.define(PushMessageData.prototype, 'blob', function blob() { return Promise.resolve(new g.Blob([copyBuffer(this._.u8)])); });
H.define(PushMessageData.prototype, 'text', function text() { return Promise.resolve(new g.TextDecoder().decode(this._.u8)); });
H.define(PushMessageData.prototype, 'json', function json() {
  var u8 = this._.u8;
  return new Promise(function (resolve) { resolve(JSON.parse(new g.TextDecoder().decode(u8))); });
});
var PushEvent = evClass('PushEvent', function (i) {
  var d = i.data;
  if (d !== undefined && d !== null) {
    var u8 = typeof d === 'string' ? new g.TextEncoder().encode(d) : d instanceof ArrayBuffer ? new Uint8Array(d) : new Uint8Array(d.buffer || d, d.byteOffset || 0, d.byteLength);
    d = makeData(toB64(u8));
  } else d = null;
  Object.defineProperty(this, 'data', { value: d, enumerable: true, configurable: true });
});
var PushSubscriptionChangeEvent = evClass('PushSubscriptionChangeEvent', function (i) {
  Object.defineProperty(this, 'oldSubscription', { value: i.oldSubscription || null, enumerable: true });
  Object.defineProperty(this, 'newSubscription', { value: i.newSubscription || null, enumerable: true });
});

// Go asks the worker to build the event for one of its own dispatches (sw_scope.js calls this for unknown kinds)
function dispatchEvent_(kind, p) {
  var reg = g.registration;
  switch (kind) {
    case 'notificationclick': case 'notificationclose':
      return new NotificationEvent(kind, { notification: persistentNotification(p.notification), action: p.action || '' });
    case 'push': {
      var ev = new PushEvent('push', {});
      if (p.hasData) Object.defineProperty(ev, 'data', { value: makeData(p.data || ''), enumerable: true });
      return ev;
    }
    case 'sync': return new SyncEvent('sync', { tag: p.tag, lastChance: p.lastChance });
    case 'periodicsync': return new PeriodicSyncEvent('periodicsync', { tag: p.tag });
    case 'pushsubscriptionchange':
      return new PushSubscriptionChangeEvent('pushsubscriptionchange', {
        oldSubscription: p.old ? makeSubscription(reg, p.old) : null, newSubscription: p.new ? makeSubscription(reg, p.new) : null });
  }
  return null;
}
H.glob('__pwaDispatch', dispatchEvent_);

// ---- registration members (called by sw_page_prelude.js) -----------------------------------------------------------------
var pwa = {
  call: call,
  show: function (reg, title, options) {
    if (arguments.length < 2) return Promise.reject(terr("Failed to execute 'showNotification' on 'ServiceWorkerRegistration': 1 argument required, but only 0 present."));
    var rec;
    try { rec = normalize("showNotification' on 'ServiceWorkerRegistration", title, options, true); } catch (e) { return Promise.reject(e); }
    if (!IS_SW && !reg.active) return Promise.reject(terr("Failed to execute 'showNotification' on 'ServiceWorkerRegistration': No active registration available on the ServiceWorkerRegistration."));
    if (!secure() || sw.pwaPermission('notifications') !== 'granted') return Promise.reject(terr("Failed to execute 'showNotification' on 'ServiceWorkerRegistration': No notification permission has been granted for this origin."));
    rec.regId = reg._id;
    return call('pwaNotifyShow', JSON.stringify(rec)).then(function () { });
  },
  list: function (reg, filter) {
    return new Promise(function (resolve, reject) {
      try {
        var tag = filter && filter.tag !== undefined ? String(filter.tag) : '';
        resolve(host('pwaNotifyList', reg._id, tag).map(persistentNotification));
      } catch (e) { reject(e); }
    });
  },
  push: pushManagerOf,
  sync: function (reg) { return managerOf(reg, SyncManager, 'sync'); },
  periodicSync: function (reg) { return managerOf(reg, PeriodicSyncManager, 'periodic'); }
};
H.glob('__pwa', pwa);

// ---- Badging ---------------------------------------------------------------------------------------------------------------------
function badgeSet(contents) {
  return new Promise(function (resolve, reject) {
    if (!secure()) { reject(dex('SecurityError', 'The Badging API needs a secure context.')); return; }
    if (contents === undefined) { sw.pwaBadge('set', 0); resolve(); return; }
    var n = Number(contents);
    if (!isFinite(n) || n < 0 || Math.floor(n) !== n || n > 18446744073709551615) { reject(terr("Failed to execute 'setAppBadge' on 'Navigator': Value is outside the 'unsigned long long' value range.")); return; }
    if (n === 0) sw.pwaBadge('clear', 0); else sw.pwaBadge('set', n);
    resolve();
  });
}
function badgeClear() {
  return new Promise(function (resolve, reject) {
    if (!secure()) { reject(dex('SecurityError', 'The Badging API needs a secure context.')); return; }
    sw.pwaBadge('clear', 0);
    resolve();
  });
}

// ---- Web Locks ----------------------------------------------------------------------------------------------------------------------
var Lock = cls('Lock');
H.attrs(Lock.prototype, { name: function () { return this._.name; }, mode: function () { return this._.mode; } });
var LockManager = cls('LockManager');
var lockWaits = new Map(), heldLocks = new Map();
function releaseLock(id) { if (heldLocks.delete(id)) sw.locksRelease(id); }
H.define(LockManager.prototype, 'request', function request(name, a, b) {
  var n = arguments.length;
  if (n < 2) return Promise.reject(terr("Failed to execute 'request' on 'LockManager': 2 arguments required, but only " + n + " present."));
  var opts = {}, cb;
  if (n === 2) cb = a; else { opts = a === undefined || a === null ? {} : a; cb = b; }
  if (typeof cb !== 'function') return Promise.reject(terr("Failed to execute 'request' on 'LockManager': parameter " + n + " is not of type 'Function'."));
  if (typeof opts !== 'object') return Promise.reject(terr("Failed to execute 'request' on 'LockManager': parameter 2 ('options') is not an object."));
  var mode = opts.mode === undefined ? 'exclusive' : String(opts.mode);
  if (mode !== 'exclusive' && mode !== 'shared') return Promise.reject(terr("Failed to execute 'request' on 'LockManager': Failed to read the 'mode' property from 'LockOptions': The provided value '" + mode + "' is not a valid enum value of type LockMode."));
  var ifAvailable = !!opts.ifAvailable, steal = !!opts.steal, signal = opts.signal;
  name = String(name);
  if (!secure()) return rej('SecurityError', "Failed to execute 'request' on 'LockManager': Access to the Locks API is denied in this context.");
  if (name.charAt(0) === '-') return rej('NotSupportedError', "Failed to execute 'request' on 'LockManager': Names cannot start with '-'.");
  if (steal && ifAvailable) return rej('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'steal' and 'ifAvailable' options cannot be used together.");
  if (steal && mode !== 'exclusive') return rej('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'steal' option may only be used with 'exclusive' locks.");
  if (signal && (steal || ifAvailable)) return rej('NotSupportedError', "Failed to execute 'request' on 'LockManager': The 'signal' option cannot be used with 'steal' or 'ifAvailable'.");
  if (signal && signal.aborted) return Promise.reject(signal.reason !== undefined ? signal.reason : dex('AbortError', 'The request was aborted.'));
  subscribe();
  return new Promise(function (resolve, reject) {
    var token = ++seq, w = { granted: false };
    lockWaits.set(token, w);
    w.grant = function (lockId) {
      lockWaits.delete(token);
      w.granted = true;
      if (!lockId) { // ifAvailable and not available: the callback gets null
        var r0;
        try { r0 = cb(null); } catch (e) { reject(e); return; }
        Promise.resolve(r0).then(resolve, reject);
        return;
      }
      var lock = Object.create(Lock.prototype);
      Object.defineProperty(lock, '_', { value: { name: name, mode: mode } });
      heldLocks.set(lockId, { reject: reject });
      var r;
      try { r = cb(lock); } catch (e) { releaseLock(lockId); reject(e); return; }
      Promise.resolve(r).then(function (v) { var live = heldLocks.has(lockId); releaseLock(lockId); if (live) resolve(v); }, function (e) { var live = heldLocks.has(lockId); releaseLock(lockId); if (live) reject(e); });
    };
    if (signal) {
      signal.addEventListener('abort', function () {
        if (w.granted) return;
        lockWaits.delete(token);
        sw.locksAbort(token);
        reject(signal.reason !== undefined ? signal.reason : dex('AbortError', 'The request was aborted.'));
      });
    }
    try { sw.locksRequest(token, name, mode, ifAvailable, steal); } catch (e) { lockWaits.delete(token); reject(hostErrorOf(e)); }
  });
});
H.define(LockManager.prototype, 'query', function query() {
  return new Promise(function (resolve, reject) { try { resolve(host('locksQuery')); } catch (e) { reject(e); } });
});
var lockManager = Object.create(LockManager.prototype);

// ---- events from Go -----------------------------------------------------------------------------------------------------------------
var onEvent = {
  result: function (m) {
    var p = pend.get(m.token);
    if (!p) return;
    pend.delete(m.token);
    if (m.ok) p.resolve(m.value); else p.reject(m.name === 'TypeError' ? terr(m.msg) : dex(m.name, m.msg));
  },
  perm: function () { refreshStatuses(); },
  online: function (m) { if (!IS_SW) fire(g, m.online ? 'online' : 'offline'); },
  note: function (m) {
    var n = notes.get(m.id);
    if (!n) return;
    if (m.ev === 'close' || m.ev === 'error') notes.delete(m.id);
    fire(n, m.ev);
  },
  lock: function (m) { var w = lockWaits.get(m.token); if (w) w.grant(m.lockId); },
  lockstolen: function (m) {
    var h = heldLocks.get(m.lockId);
    if (!h) return;
    heldLocks.delete(m.lockId);
    h.reject(dex('AbortError', "Lock broken by another request with the 'steal' option."));
  }
};
H.glob('__pwaEvent', function (json) {
  var m = JSON.parse(json), f = onEvent[m.t];
  if (f) { try { f(m); } catch (e) { report(e); } }
});


// ---- Async Clipboard API ----------------------------------------------------------------------------------------------------
// navigator.clipboard (Clipboard, ClipboardItem). Writing needs a visible page; reading needs the clipboard-read
// permission (Permissions.query reports it, a read asks for it on a user gesture). Host side: clipboard.go.
var CB_STD = ['text/plain', 'text/html', 'image/png'];
function cbTypeOK(t) { return CB_STD.indexOf(t) >= 0 || /^web [a-z0-9][a-z0-9!#$&^_.+-]*\/[a-z0-9][a-z0-9!#$&^_.+-]*$/.test(t); }
var ClipboardItem = cls('ClipboardItem', null, function ClipboardItem(items, options) {
  if (!new.target) throw terr("Failed to construct 'ClipboardItem': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (arguments.length < 1) throw terr("Failed to construct 'ClipboardItem': 1 argument required, but only 0 present.");
  if (items === null || typeof items !== 'object') throw terr("Failed to construct 'ClipboardItem': The provided value is not of type 'record<DOMString, (DOMString or Blob or Promise<(DOMString or Blob)>)>'.");
  var keys = Object.keys(items);
  if (!keys.length) throw terr("Failed to construct 'ClipboardItem': At least one entry required.");
  var style = 'unspecified';
  if (options !== undefined && options !== null) {
    if (typeof options !== 'object') throw terr("Failed to construct 'ClipboardItem': The provided value is not of type 'ClipboardItemOptions'.");
    if (options.presentationStyle !== undefined) {
      style = String(options.presentationStyle);
      if (['unspecified', 'inline', 'attachment'].indexOf(style) < 0) throw terr("Failed to construct 'ClipboardItem': Failed to read the 'presentationStyle' property from 'ClipboardItemOptions': The provided value '" + style + "' is not a valid enum value of type PresentationStyle.");
    }
  }
  var map = new Map();
  keys.forEach(function (k) { map.set(String(k).toLowerCase(), Promise.resolve(items[k])); });
  Object.defineProperty(this, '_', { value: { map: map, style: style } });
});
H.attrs(ClipboardItem.prototype, {
  types: function () { return Object.freeze(Array.from(this._.map.keys())); },
  presentationStyle: function () { return this._.style; }
});
function asBlob(v, type) { return typeof v === 'string' ? new g.Blob([v], { type: type }) : v; }
H.define(ClipboardItem.prototype, 'getType', function getType(type) {
  var self = this;
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'getType' on 'ClipboardItem': 1 argument required, but only 0 present."));
  var t = String(type).toLowerCase(), p = this._.map.get(t);
  if (!p) return rej('NotFoundError', "Failed to execute 'getType' on 'ClipboardItem': The type was not found");
  return p.then(function (v) {
    var b = asBlob(v, t);
    if (!(b instanceof g.Blob)) throw terr("Failed to execute 'getType' on 'ClipboardItem': The provided value is not a Blob.");
    return b;
  });
});
H.define(ClipboardItem, 'supports', function supports(type) {
  if (arguments.length < 1) throw terr("Failed to execute 'supports' on 'ClipboardItem': 1 argument required, but only 0 present.");
  return cbTypeOK(String(type).toLowerCase());
});

function cbBytesToB64(buf) {
  var u8 = new Uint8Array(buf), s = '';
  for (var i = 0; i < u8.length; i += 0x2000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x2000));
  return g.btoa(s);
}
function cbVisible() { return g.document && g.document.visibilityState === 'visible'; }
function cbEnsureRead() {
  if (!cbVisible()) return rej('NotAllowedError', 'Document is not focused.');
  var st = sw.pwaPermission('clipboard-read');
  if (st === 'granted') return Promise.resolve();
  if (st === 'denied') return rej('NotAllowedError', 'Read permission denied.');
  return call('pwaRequestPermission', 'clipboard-read').then(function (r) { if (r !== 'granted') throw dex('NotAllowedError', 'Read permission denied.'); });
}
function cbReadEntries() {
  return cbEnsureRead().then(function () {
    try { return JSON.parse(sw.clipRead() || '[]'); } catch (e) { throw hostErrorOf(e); }
  });
}
function cbWrite(entries) {
  if (!cbVisible()) return rej('NotAllowedError', 'Document is not focused.');
  try { sw.clipWrite(JSON.stringify(entries)); } catch (e) { return Promise.reject(hostErrorOf(e)); }
  return Promise.resolve(undefined);
}
var Clipboard = cls('Clipboard', ET);
H.define(Clipboard.prototype, 'readText', function readText() {
  return cbReadEntries().then(function (es) {
    for (var i = 0; i < es.length; i++) if (es[i].type === 'text/plain') return new g.TextDecoder().decode(fromB64Std(es[i].b64));
    return '';
  });
});
H.define(Clipboard.prototype, 'writeText', function writeText(text) {
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'writeText' on 'Clipboard': 1 argument required, but only 0 present."));
  var u8 = new g.TextEncoder().encode(String(text));
  return cbWrite([{ type: 'text/plain', b64: cbBytesToB64(u8) }]);
});
H.define(Clipboard.prototype, 'read', function read(options) {
  return cbReadEntries().then(function (es) {
    var items = {}, n = 0;
    es.forEach(function (e) {
      if (!cbTypeOK(e.type)) return;
      items[e.type] = new g.Blob([fromB64Std(e.b64)], { type: e.type }); n++;
    });
    return n ? [new ClipboardItem(items)] : [];
  });
});
H.define(Clipboard.prototype, 'write', function write(data) {
  if (arguments.length < 1) return Promise.reject(terr("Failed to execute 'write' on 'Clipboard': 1 argument required, but only 0 present."));
  var list;
  try { list = Array.prototype.slice.call(data); } catch (e) { list = null; }
  if (!list || data === null || typeof data !== 'object' || typeof data.length !== 'number') return Promise.reject(terr("Failed to execute 'write' on 'Clipboard': The provided value cannot be converted to a sequence."));
  for (var i = 0; i < list.length; i++) if (!(list[i] instanceof ClipboardItem)) return Promise.reject(terr("Failed to execute 'write' on 'Clipboard': Failed to convert value to 'ClipboardItem'."));
  if (list.length > 1) return rej('NotAllowedError', 'Support for multiple ClipboardItems is not implemented.');
  if (!cbVisible()) return rej('NotAllowedError', 'Document is not focused.');
  var item = list[0];
  if (!item) return cbWrite([]);
  var types = Array.from(item._.map.keys());
  for (var k = 0; k < types.length; k++) if (!cbTypeOK(types[k])) return rej('NotAllowedError', 'Type ' + types[k] + ' does not match the blob\'s type');
  return Promise.all(types.map(function (t) {
    return item._.map.get(t).then(function (v) {
      var b = asBlob(v, t);
      if (!(b instanceof g.Blob)) throw dex('NotAllowedError', 'Type ' + t + ' does not match the blob\'s type');
      if (b.type !== t) throw dex('NotAllowedError', 'Type ' + t + " does not match the blob's type " + b.type);
      return b.arrayBuffer().then(function (ab) { return { type: t, b64: cbBytesToB64(ab) }; });
    });
  })).then(cbWrite);
});
var clipboardObj = Object.create(Clipboard.prototype);
ET.call(clipboardObj);
function fromB64Std(s) {
  var bin = g.atob(s), u8 = new Uint8Array(bin.length);
  for (var i = 0; i < bin.length; i++) u8[i] = bin.charCodeAt(i);
  return u8;
}

// ---- globals -------------------------------------------------------------------------------------------------------------------------
var N = g.Navigator.prototype;
function navMember(name, value) {
  Object.defineProperty(N, name, { get: function () { return value(); }, set: function (v) { Object.defineProperty(this, name, { value: v, writable: true, configurable: true, enumerable: true }); }, enumerable: true, configurable: true });
}
navMember('permissions', function () { return permissions; });
navMember('clipboard', function () { return secure() ? clipboardObj : undefined; });
navMember('locks', function () { return secure() ? lockManager : undefined; });
navMember('setAppBadge', function () { return secure() ? badgeSet : undefined; });
navMember('clearAppBadge', function () { return secure() ? badgeClear : undefined; });
var G = { Notification: Notification, NotificationEvent: NotificationEvent, Permissions: Permissions, PermissionStatus: PermissionStatus, PushManager: PushManager, PushSubscription: PushSubscription,
  PushSubscriptionOptions: PushSubscriptionOptions, Clipboard: Clipboard, ClipboardItem: ClipboardItem, PushEvent: PushEvent, PushMessageData: PushMessageData, PushSubscriptionChangeEvent: PushSubscriptionChangeEvent,
  SyncManager: SyncManager, SyncEvent: SyncEvent, PeriodicSyncManager: PeriodicSyncManager, PeriodicSyncEvent: PeriodicSyncEvent, Lock: Lock, LockManager: LockManager };
Object.keys(G).forEach(function (k) { H.glob(k, G[k]); });
