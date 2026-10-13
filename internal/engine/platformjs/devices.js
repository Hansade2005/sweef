// Device and platform objects for a desktop browser without sensors, credential providers or
// payment handlers. Everything here is honest: nothing pretends to be hardware that is absent.
var ET = g.EventTarget;
function illegal() { throw new TypeError('Illegal constructor'); }
function rej(name, msg) { return Promise.reject(H.exc(msg, name)); }
function cls(name, proto) {
  var C = function () { illegal(); };
  Object.defineProperty(C, 'name', { value: name, configurable: true });
  if (proto) { Object.setPrototypeOf(C.prototype, proto.prototype); Object.setPrototypeOf(C, proto); }
  H.tag(C, name);
  return C;
}
function make(C, props) { var o = Object.create(C.prototype); if (C.prototype instanceof ET) ET.call(o); Object.defineProperty(o, '_', { value: props || {}, writable: true }); return o; }
function viewport() { try { return g.__sw.viewport() || {}; } catch (e) { return {}; } }

// ---- ScreenOrientation ----------------------------------------------------------------------------
var ScreenOrientation = cls('ScreenOrientation', ET);
H.attrs(ScreenOrientation.prototype, {
  type: function () { var v = viewport(); return (v.width || 1280) >= (v.height || 800) ? 'landscape-primary' : 'portrait-primary'; },
  angle: function () { return 0; }
});
H.define(ScreenOrientation.prototype, 'lock', function lock(orientation) {
  if (arguments.length < 1) return Promise.reject(new TypeError("Failed to execute 'lock' on 'ScreenOrientation': 1 argument required, but only 0 present."));
  var ok = ['any', 'natural', 'landscape', 'portrait', 'portrait-primary', 'portrait-secondary', 'landscape-primary', 'landscape-secondary'];
  if (ok.indexOf(String(orientation)) < 0) return Promise.reject(new TypeError("Failed to execute 'lock' on 'ScreenOrientation': The provided value '" + orientation + "' is not a valid enum value of type OrientationLockType."));
  return rej('NotSupportedError', 'screen.orientation.lock() is not available on this device.');
});
H.define(ScreenOrientation.prototype, 'unlock', function unlock() { });
H.handlers(ScreenOrientation.prototype, ['change']);
var orientation = make(ScreenOrientation);
H.attrs(g.Screen.prototype, { orientation: function () { return orientation; } });

// ---- DeviceOrientation / DeviceMotion: classes only, no sensor ever reports -------------------------
var DeviceOrientationEvent = H.eventClass('DeviceOrientationEvent', { alpha: null, beta: null, gamma: null, absolute: false });
H.define(DeviceOrientationEvent, 'requestPermission', function requestPermission() { return Promise.resolve('denied'); });
var Accel = cls('DeviceMotionEventAcceleration'), Rot = cls('DeviceMotionEventRotationRate');
['x', 'y', 'z'].forEach(function (k) { Object.defineProperty(Accel.prototype, k, { get: function () { return this._[k]; }, enumerable: true, configurable: true }); });
['alpha', 'beta', 'gamma'].forEach(function (k) { Object.defineProperty(Rot.prototype, k, { get: function () { return this._[k]; }, enumerable: true, configurable: true }); });
function conv(C, keys, init) { if (init === null || init === undefined) return null; var p = {}; keys.forEach(function (k) { p[k] = init[k] === undefined ? null : init[k]; }); return make(C, p); }
var DeviceMotionEvent = H.eventClass('DeviceMotionEvent', { acceleration: null, accelerationIncludingGravity: null, rotationRate: null, interval: 0 });
(function () {
  var Orig = DeviceMotionEvent;
  var W = function DeviceMotionEvent(type, init) {
    if (!new.target) throw new TypeError("Failed to construct 'DeviceMotionEvent': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
    var e = Reflect.construct(Orig, arguments, Orig);
    init = init || {};
    e.acceleration = conv(Accel, ['x', 'y', 'z'], init.acceleration);
    e.accelerationIncludingGravity = conv(Accel, ['x', 'y', 'z'], init.accelerationIncludingGravity);
    e.rotationRate = conv(Rot, ['alpha', 'beta', 'gamma'], init.rotationRate);
    return e;
  };
  W.prototype = Orig.prototype;
  Object.defineProperty(Orig.prototype, 'constructor', { value: W, writable: true, configurable: true });
  Object.setPrototypeOf(W, g.Event);
  H.define(W, 'requestPermission', function requestPermission() { return Promise.resolve('denied'); });
  H.glob('DeviceMotionEvent', W);
})();

// ---- Gamepad ---------------------------------------------------------------------------------------------
var Gamepad = cls('Gamepad'), GamepadButton = cls('GamepadButton');
var GamepadEvent = H.eventClass('GamepadEvent', { gamepad: null }, null, 'gamepad');
H.define(g.Navigator.prototype, 'getGamepads', function getGamepads() { return [null, null, null, null]; });

// ---- Screen Wake Lock ------------------------------------------------------------------------------------------
var WakeLockSentinel = cls('WakeLockSentinel', ET), WakeLock = cls('WakeLock');
H.attrs(WakeLockSentinel.prototype, {
  released: function () { return this._.released; },
  type: function () { return this._.type; }
});
H.define(WakeLockSentinel.prototype, 'release', function release() {
  var s = this._;
  if (!s.released) {
    s.released = true;
    var self = this;
    self.dispatchEvent(new g.Event('release'));
  }
  return Promise.resolve(undefined);
});
H.handlers(WakeLockSentinel.prototype, ['release']);
H.define(WakeLock.prototype, 'request', function request(type) {
  type = type === undefined ? 'screen' : String(type);
  if (type !== 'screen') return Promise.reject(new TypeError("Failed to execute 'request' on 'WakeLock': The provided value '" + type + "' is not a valid enum value of type WakeLockType."));
  if (g.document.visibilityState !== 'visible') return rej('NotAllowedError', 'Wake Lock is only available for visible documents.');
  return Promise.resolve(make(WakeLockSentinel, { released: false, type: 'screen' }));
});
var wakeLock = make(WakeLock);
H.attrs(g.Navigator.prototype, { wakeLock: function () { return wakeLock; } });

// ---- Media Session -----------------------------------------------------------------------------------------------
var MediaMetadata = function MediaMetadata(init) {
  if (!new.target) throw new TypeError("Failed to construct 'MediaMetadata': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  init = init === undefined ? {} : init;
  if (init === null || typeof init !== 'object') throw new TypeError("Failed to construct 'MediaMetadata': The provided value is not of type 'MediaMetadataInit'.");
  Object.defineProperty(this, '_', { value: { title: '', artist: '', album: '', artwork: [] }, writable: true });
  var self = this;
  ['title', 'artist', 'album'].forEach(function (k) { if (init[k] !== undefined) self._[k] = String(init[k]); });
  if (init.artwork !== undefined) this.artwork = init.artwork;
};
function artworkList(list) {
  if (list === null || typeof list !== 'object' || typeof list.length !== 'number') throw new TypeError("Failed to set the 'artwork' property on 'MediaMetadata': The provided value cannot be converted to a sequence.");
  return Array.prototype.map.call(list, function (a) {
    if (a === null || typeof a !== 'object' || a.src === undefined) throw new TypeError("Failed to set the 'artwork' property on 'MediaMetadata': Required member src is undefined.");
    var src;
    try { src = new g.URL(String(a.src), g.location.href).href; } catch (e) { throw new TypeError("Failed to set the 'artwork' property on 'MediaMetadata': Invalid URL '" + a.src + "'."); }
    return { src: src, sizes: a.sizes === undefined ? '' : String(a.sizes), type: a.type === undefined ? '' : String(a.type) };
  });
}
['title', 'artist', 'album'].forEach(function (k) {
  Object.defineProperty(MediaMetadata.prototype, k, { get: function () { return this._[k]; }, set: function (v) { this._[k] = String(v); }, enumerable: true, configurable: true });
});
Object.defineProperty(MediaMetadata.prototype, 'artwork', { get: function () { return this._.artwork.map(function (a) { return { src: a.src, sizes: a.sizes, type: a.type }; }); }, set: function (v) { this._.artwork = artworkList(v); }, enumerable: true, configurable: true });
H.tag(MediaMetadata, 'MediaMetadata');
var MediaSession = cls('MediaSession');
var ACTIONS = ['play', 'pause', 'seekbackward', 'seekforward', 'previoustrack', 'nexttrack', 'skipad', 'stop', 'seekto', 'togglemicrophone', 'togglecamera', 'hangup', 'previousslide', 'nextslide', 'enterpictureinpicture'];
H.attrs(MediaSession.prototype, {
  metadata: {
    get: function () { return this._.metadata; },
    set: function (v) {
      if (v !== null && !(v instanceof MediaMetadata)) throw new TypeError("Failed to set the 'metadata' property on 'MediaSession': Failed to convert value to 'MediaMetadata'.");
      this._.metadata = v;
    }
  },
  playbackState: {
    get: function () { return this._.playbackState; },
    set: function (v) { v = String(v); if (['none', 'paused', 'playing'].indexOf(v) >= 0) this._.playbackState = v; }
  }
});
H.define(MediaSession.prototype, 'setActionHandler', function setActionHandler(action, handler) {
  if (arguments.length < 2) throw new TypeError("Failed to execute 'setActionHandler' on 'MediaSession': 2 arguments required, but only " + arguments.length + ' present.');
  action = String(action);
  if (ACTIONS.indexOf(action) < 0) throw new TypeError("Failed to execute 'setActionHandler' on 'MediaSession': The provided value '" + action + "' is not a valid enum value of type MediaSessionAction.");
  if (handler !== null && typeof handler !== 'function') throw new TypeError("Failed to execute 'setActionHandler' on 'MediaSession': The callback provided as parameter 2 is not a function.");
  if (handler === null) delete this._.handlers[action]; else this._.handlers[action] = handler;
});
H.define(MediaSession.prototype, 'getActionHandler', function getActionHandler(action) { return this._.handlers[String(action)] || null; });
H.define(MediaSession.prototype, 'setPositionState', function setPositionState(state) {
  if (state === undefined) { this._.position = null; return; }
  var d = Number(state.duration), p = state.position === undefined ? 0 : Number(state.position), r = state.playbackRate === undefined ? 1 : Number(state.playbackRate);
  if (!(d >= 0) || !(p >= 0) || p > d || r === 0 || r !== r) throw new TypeError("Failed to execute 'setPositionState' on 'MediaSession': The provided position state is invalid.");
  this._.position = { duration: d, position: p, playbackRate: r };
});
H.define(MediaSession.prototype, 'setCameraActive', function setCameraActive(active) { this._.camera = !!active; });
H.define(MediaSession.prototype, 'setMicrophoneActive', function setMicrophoneActive(active) { this._.mic = !!active; });
var mediaSession = make(MediaSession, { metadata: null, playbackState: 'none', handlers: {}, position: null });
H.attrs(g.Navigator.prototype, { mediaSession: function () { return mediaSession; } });

// ---- Credential Management / WebAuthn: no credential provider ------------------------------------------------------
var Credential = cls('Credential'), PasswordCredential = cls('PasswordCredential', Credential), FederatedCredential = cls('FederatedCredential', Credential);
var PublicKeyCredential = cls('PublicKeyCredential', Credential);
H.define(PublicKeyCredential, 'isUserVerifyingPlatformAuthenticatorAvailable', function () { return Promise.resolve(false); });
H.define(PublicKeyCredential, 'isConditionalMediationAvailable', function () { return Promise.resolve(false); });
H.define(PublicKeyCredential, 'getClientCapabilities', function () { return Promise.resolve({}); });
var CredentialsContainer = cls('CredentialsContainer');
function credCheck(options, what) {
  if (options !== undefined && options !== null && typeof options !== 'object') return Promise.reject(new TypeError("Failed to execute '" + what + "' on 'CredentialsContainer': The provided value is not of type 'CredentialRequestOptions'."));
  var sig = options && options.signal;
  if (sig && sig.aborted) return Promise.reject(sig.reason);
  return null;
}
H.define(CredentialsContainer.prototype, 'get', function get(options) {
  var bad = credCheck(options, 'get'); if (bad) return bad;
  if (options && options.publicKey) return rej('NotAllowedError', 'The operation either timed out or was not allowed: no authenticator is available.');
  return Promise.resolve(null);
});
H.define(CredentialsContainer.prototype, 'create', function create(options) {
  var bad = credCheck(options, 'create'); if (bad) return bad;
  if (options && options.publicKey) return rej('NotAllowedError', 'The operation either timed out or was not allowed: no authenticator is available.');
  if (options && (options.password || options.federated)) return rej('NotAllowedError', 'No credential provider is available to create this credential.');
  return Promise.resolve(null);
});
H.define(CredentialsContainer.prototype, 'store', function store(credential) {
  if (!(credential instanceof Credential)) return Promise.reject(new TypeError("Failed to execute 'store' on 'CredentialsContainer': parameter 1 is not of type 'Credential'."));
  return rej('NotAllowedError', 'No credential provider is available to store this credential.');
});
H.define(CredentialsContainer.prototype, 'preventSilentAccess', function preventSilentAccess() { return Promise.resolve(undefined); });
var credentials = make(CredentialsContainer);
H.attrs(g.Navigator.prototype, { credentials: function () { return credentials; } });

// ---- Payment Request -----------------------------------------------------------------------------------------------------
var PaymentResponse = cls('PaymentResponse', ET);
var PaymentRequestUpdateEvent = H.eventClass('PaymentRequestUpdateEvent', {});
H.define(PaymentRequestUpdateEvent.prototype, 'updateWith', function updateWith(p) { if (!this._dispatching) throw H.exc('updateWith() may only be called while the event is being dispatched.', 'InvalidStateError'); });
function PaymentRequest(methodData, details, options) {
  if (!new.target) throw new TypeError("Failed to construct 'PaymentRequest': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  if (arguments.length < 2) throw new TypeError("Failed to construct 'PaymentRequest': 2 arguments required, but only " + arguments.length + ' present.');
  if (methodData === null || typeof methodData !== 'object' || typeof methodData.length !== 'number' || methodData.length === 0) throw new TypeError("Failed to construct 'PaymentRequest': At least one payment method is required.");
  Array.prototype.forEach.call(methodData, function (m) {
    if (m === null || typeof m !== 'object' || typeof m.supportedMethods !== 'string' || m.supportedMethods === '') throw new TypeError("Failed to construct 'PaymentRequest': Payment method identifier is required.");
  });
  if (details === null || typeof details !== 'object' || details.total === undefined) throw new TypeError("Failed to construct 'PaymentRequest': Required member total is undefined.");
  var amt = details.total.amount;
  if (amt === null || typeof amt !== 'object' || typeof amt.currency !== 'string' || !/^[A-Za-z]{3}$/.test(amt.currency)) throw new RangeError("Failed to construct 'PaymentRequest': Invalid currency code.");
  if (typeof amt.value !== 'string' || !/^-?[0-9]+(\.[0-9]+)?$/.test(amt.value)) throw new TypeError("Failed to construct 'PaymentRequest': '" + amt.value + "' is not a valid amount format.");
  if (/^-/.test(amt.value)) throw new TypeError("Failed to construct 'PaymentRequest': Total amount value should be non-negative.");
  ET.call(this);
  var uuid = g.crypto && g.crypto.randomUUID ? g.crypto.randomUUID() : String(Date.now());
  options = options || {};
  Object.defineProperty(this, '_', { value: { id: details.id !== undefined ? String(details.id) : uuid, state: 'created', options: options } });
}
Object.setPrototypeOf(PaymentRequest.prototype, ET.prototype);
Object.setPrototypeOf(PaymentRequest, ET);
H.attrs(PaymentRequest.prototype, {
  id: function () { return this._.id; },
  shippingAddress: function () { return null; },
  shippingOption: function () { return null; },
  shippingType: function () { return this._.options.requestShipping ? (this._.options.shippingType || 'shipping') : null; }
});
H.handlers(PaymentRequest.prototype, ['merchantvalidation', 'shippingaddresschange', 'shippingoptionchange', 'paymentmethodchange']);
H.define(PaymentRequest.prototype, 'canMakePayment', function canMakePayment() { return Promise.resolve(false); });
H.define(PaymentRequest.prototype, 'hasEnrolledInstrument', function hasEnrolledInstrument() { return Promise.resolve(false); });
H.define(PaymentRequest.prototype, 'show', function show() {
  var s = this._;
  if (s.state !== 'created') return rej('InvalidStateError', "Failed to execute 'show' on 'PaymentRequest': The payment request is already showing or has been closed.");
  s.state = 'closed';
  return rej('NotSupportedError', 'The payment method is not supported: no payment handler is installed.');
});
H.define(PaymentRequest.prototype, 'abort', function abort() { return rej('InvalidStateError', "Failed to execute 'abort' on 'PaymentRequest': The payment request is not showing."); });
H.tag(PaymentRequest, 'PaymentRequest');

// ---- Storage Access API: the engine only runs top-level documents -----------------------------------------------------------
var DP = g.Document.prototype;
H.define(DP, 'hasStorageAccess', function hasStorageAccess() { return Promise.resolve(g.top === g); });
H.define(DP, 'requestStorageAccess', function requestStorageAccess() {
  if (g.top === g) return Promise.resolve(undefined);
  return rej('NotAllowedError', 'requestStorageAccess not allowed: third-party storage access is not granted.');
});

// ---- Trusted Types: policies pass values through, enforcement is not implemented ---------------------------------------------------
var TrustedHTML = cls('TrustedHTML'), TrustedScript = cls('TrustedScript'), TrustedScriptURL = cls('TrustedScriptURL');
[TrustedHTML, TrustedScript, TrustedScriptURL].forEach(function (C) {
  H.define(C.prototype, 'toString', function toString() { return this._.v; });
  H.define(C.prototype, 'toJSON', function toJSON() { return this._.v; });
});
var TrustedTypePolicy = cls('TrustedTypePolicy');
var TrustedTypePolicyFactory = cls('TrustedTypePolicyFactory');
H.attrs(TrustedTypePolicy.prototype, { name: function () { return this._.name; } });
function policyMethod(name, C, key) {
  H.define(TrustedTypePolicy.prototype, name, function (input) {
    var fn = this._.rules[key];
    if (!fn) throw new TypeError("Failed to execute '" + name + "' on 'TrustedTypePolicy': Policy " + this._.name + "'s TrustedTypePolicyOptions did not specify a '" + key + "' member.");
    var args = Array.prototype.slice.call(arguments, 1);
    var out = fn.apply(undefined, [String(input)].concat(args));
    if (out === null || out === undefined) throw new TypeError("Failed to execute '" + name + "' on 'TrustedTypePolicy': Policy " + this._.name + ' returned ' + out + '.');
    return make(C, { v: String(out) });
  });
}
policyMethod('createHTML', TrustedHTML, 'createHTML');
policyMethod('createScript', TrustedScript, 'createScript');
policyMethod('createScriptURL', TrustedScriptURL, 'createScriptURL');
var policies = {}, defaultPolicy = null;
H.define(TrustedTypePolicyFactory.prototype, 'createPolicy', function createPolicy(name, rules) {
  name = String(name);
  if (name === 'default' && defaultPolicy) throw new TypeError("Failed to execute 'createPolicy' on 'TrustedTypePolicyFactory': Policy with name \"default\" already exists.");
  rules = rules === undefined || rules === null ? {} : rules;
  var r = {};
  ['createHTML', 'createScript', 'createScriptURL'].forEach(function (k) { if (rules[k] !== undefined) { if (typeof rules[k] !== 'function') throw new TypeError("Failed to execute 'createPolicy' on 'TrustedTypePolicyFactory': " + k + ' is not a function.'); r[k] = rules[k]; } });
  var p = make(TrustedTypePolicy, { name: name, rules: r });
  if (name === 'default') defaultPolicy = p;
  policies[name] = p;
  return p;
});
H.define(TrustedTypePolicyFactory.prototype, 'isHTML', function isHTML(v) { return v instanceof TrustedHTML; });
H.define(TrustedTypePolicyFactory.prototype, 'isScript', function isScript(v) { return v instanceof TrustedScript; });
H.define(TrustedTypePolicyFactory.prototype, 'isScriptURL', function isScriptURL(v) { return v instanceof TrustedScriptURL; });
var emptyHTML = make(TrustedHTML, { v: '' }), emptyScript = make(TrustedScript, { v: '' });
H.attrs(TrustedTypePolicyFactory.prototype, {
  emptyHTML: function () { return emptyHTML; },
  emptyScript: function () { return emptyScript; },
  defaultPolicy: function () { return defaultPolicy; }
});
var HTML_SINKS = { innerHTML: 1, outerHTML: 1 }, SCRIPT_SINKS = { text: 1, textContent: 1, innerText: 1 };
H.define(TrustedTypePolicyFactory.prototype, 'getAttributeType', function getAttributeType(tag, attr, ns, attrNs) {
  tag = String(tag).toLowerCase(); attr = String(attr).toLowerCase();
  if (tag === 'iframe' && attr === 'srcdoc') return 'TrustedHTML';
  if (tag === 'script' && attr === 'src') return 'TrustedScriptURL';
  if (/^on/.test(attr)) return 'TrustedScript';
  return null;
});
H.define(TrustedTypePolicyFactory.prototype, 'getPropertyType', function getPropertyType(tag, prop, ns) {
  tag = String(tag).toLowerCase(); prop = String(prop);
  if (prop === 'innerHTML' || prop === 'outerHTML') return 'TrustedHTML';
  if (tag === 'iframe' && prop === 'srcdoc') return 'TrustedHTML';
  if (tag === 'script' && (prop === 'src')) return 'TrustedScriptURL';
  if (tag === 'script' && (prop === 'text' || prop === 'textContent' || prop === 'innerText')) return 'TrustedScript';
  return null;
});
H.tag(TrustedTypePolicyFactory, 'TrustedTypePolicyFactory');
var trustedTypes = make(TrustedTypePolicyFactory);

// ---- Picture-in-Picture: unavailable ---------------------------------------------------------------------------------------------------------
var PictureInPictureWindow = cls('PictureInPictureWindow', ET);
H.attrs(PictureInPictureWindow.prototype, { width: function () { return 0; }, height: function () { return 0; } });
H.handlers(PictureInPictureWindow.prototype, ['resize']);
var PictureInPictureEvent = H.eventClass('PictureInPictureEvent', { pictureInPictureWindow: null }, null, 'pictureInPictureWindow');
// The media prelude (when present) already defines the Picture-in-Picture members, matched against Chromium;
// these only fill in whatever is still missing.
if (H.missing(DP, 'pictureInPictureEnabled')) H.attrs(DP, { pictureInPictureEnabled: function () { return false; }, pictureInPictureElement: function () { return null; } });
if (H.missing(DP, 'exitPictureInPicture')) H.define(DP, 'exitPictureInPicture', function exitPictureInPicture() { return rej('InvalidStateError', 'There is no Picture-in-Picture element in this document.'); });
if (g.HTMLVideoElement) {
  if (H.missing(g.HTMLVideoElement.prototype, 'requestPictureInPicture')) H.define(g.HTMLVideoElement.prototype, 'requestPictureInPicture', function requestPictureInPicture() { return rej('NotSupportedError', 'Picture-in-Picture is not supported on this device.'); });
  if (H.missing(g.HTMLVideoElement.prototype, 'disablePictureInPicture')) Object.defineProperty(g.HTMLVideoElement.prototype, 'disablePictureInPicture', {
    get: function () { return this.hasAttribute('disablepictureinpicture'); },
    set: function (v) { if (v) this.setAttribute('disablepictureinpicture', ''); else this.removeAttribute('disablepictureinpicture'); },
    enumerable: true, configurable: true
  });
}
Object.defineProperty(g, 'trustedTypes', { get: function () { return trustedTypes; }, set: function (v) { Object.defineProperty(g, 'trustedTypes', { value: v, writable: true, configurable: true, enumerable: true }); }, configurable: true, enumerable: true });

var G = { ScreenOrientation: ScreenOrientation, DeviceMotionEventAcceleration: Accel, DeviceMotionEventRotationRate: Rot, Gamepad: Gamepad, GamepadButton: GamepadButton, WakeLockSentinel: WakeLockSentinel, WakeLock: WakeLock, MediaMetadata: MediaMetadata, MediaSession: MediaSession, Credential: Credential, PasswordCredential: PasswordCredential, FederatedCredential: FederatedCredential, PublicKeyCredential: PublicKeyCredential, CredentialsContainer: CredentialsContainer, PaymentRequest: PaymentRequest, PaymentResponse: PaymentResponse, TrustedHTML: TrustedHTML, TrustedScript: TrustedScript, TrustedScriptURL: TrustedScriptURL, TrustedTypePolicy: TrustedTypePolicy, TrustedTypePolicyFactory: TrustedTypePolicyFactory, PictureInPictureWindow: PictureInPictureWindow };
Object.keys(G).forEach(function (n) { H.glob(n, G[n]); });
