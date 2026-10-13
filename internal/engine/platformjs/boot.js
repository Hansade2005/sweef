/*
 * Sweef platform layer, bootstrap. Evaluated in every VM, so it stays tiny:
 * it declares which names belong to which module and installs one lazy
 * accessor per name. The first touch of a name evaluates platformjs/<module>.js
 * (fetched through __sw.platformSrc), which replaces the accessors with the
 * real properties. See platform.go.
 */
(function (g) {
  'use strict';
  var sw = g.__sw;
  if (!sw || typeof sw.platformSrc !== 'function') return;
  var loaded = Object.create(null);
  var H = {
    sw: sw,
    define: function (o, k, v) { Object.defineProperty(o, k, { value: v, writable: true, configurable: true, enumerable: false }); },
    tag: function (C, n) { Object.defineProperty(C.prototype, Symbol.toStringTag, { value: n, configurable: true }); },
    glob: function (n, v) { Object.defineProperty(g, n, { value: v, writable: true, configurable: true, enumerable: false }); },
    exc: function (m, n) { return new g.DOMException(m, n); },
    // Go reports errors as "Name: message"; turn them into DOMExceptions.
    hostErr: function (e) {
      var m = /^([A-Za-z]+Error): ([\s\S]*)$/.exec(String(e && e.message || e));
      if (!m) return e;
      return m[1] === 'TypeError' ? new TypeError(m[2]) : new g.DOMException(m[2], m[1]);
    },
    // accessor properties with enumerable getters, like WebIDL attributes
    attrs: function (proto, map) {
      Object.keys(map).forEach(function (k) {
        var d = map[k];
        Object.defineProperty(proto, k, { get: typeof d === 'function' ? d : d.get, set: d.set, enumerable: true, configurable: true });
      });
    },
    // true when a name is absent or still only our lazy accessor
    missing: function (o, k) { var d = Object.getOwnPropertyDescriptor(o, k); return !d || !!(d.get && d.get.lazy); },
    // event classes in the prelude's style: plain constructors with own data fields
    eventClass: function (name, fields, sup, required) {
      sup = sup || g.Event;
      var C = function (type, init) {
        if (!(this instanceof C)) throw new TypeError("Failed to construct '" + name + "': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
        if (arguments.length === 0) throw new TypeError("Failed to construct '" + name + "': 1 argument required, but only 0 present.");
        if (required && (init === null || typeof init !== 'object' || init[required] === undefined)) throw new TypeError("Failed to construct '" + name + "': Failed to read the '" + required + "' property from '" + name + "Init': Required member is undefined.");
        sup.call(this, type, init);
        init = init || {};
        for (var f in fields) this[f] = f in init ? init[f] : fields[f];
      };
      Object.setPrototypeOf(C.prototype, sup.prototype);
      Object.setPrototypeOf(C, sup);
      Object.defineProperty(C, 'name', { value: name, configurable: true });
      H.tag(C, name);
      H.glob(name, C);
      return C;
    },
    // on<type> handler properties backed by addEventListener
    handlers: function (proto, types) {
      types.forEach(function (n) {
        var key = '__on' + n;
        Object.defineProperty(proto, 'on' + n, {
          configurable: true, enumerable: true,
          get: function () { return this[key] || null; },
          set: function (fn) {
            if (this[key]) this.removeEventListener(n, this[key]);
            Object.defineProperty(this, key, { value: typeof fn === 'function' ? fn : null, writable: true, configurable: true });
            if (this[key]) this.addEventListener(n, this[key]);
          }
        });
      });
    },
    load: load
  };
  function load(m) {
    if (loaded[m]) return;
    loaded[m] = 1;
    try {
      var src = sw.platformSrc(m);
      if (!src) throw new Error('platform module missing: ' + m);
      (0, g.eval)('(function(g,H){"use strict";' + src + '\n})')(g, H);
    } catch (e) { loaded[m] = 0; throw e; }
  }
  function lazy(obj, name, mod, enumerable, onlyMissing) {
    if (onlyMissing && name in obj) return;
    var desc = {
      configurable: true, enumerable: !!enumerable,
      get: function () {
        load(mod);
        var d = Object.getOwnPropertyDescriptor(obj, name);
        if (d && d.get === desc.get) throw new Error('platform: ' + name + ' was not defined by module ' + mod);
        return Reflect.get(obj, name, this);
      },
      set: function (v) { Object.defineProperty(obj === g ? g : this, name, { value: v, writable: true, configurable: true, enumerable: !!enumerable }); }
    };
    desc.get.lazy = true;
    Object.defineProperty(obj, name, desc);
  }
  function names(mod, list, obj, enumerable, onlyMissing) {
    list.split(' ').forEach(function (n) { lazy(obj || g, n, mod, enumerable, onlyMissing); });
  }
  // The streams stubs of the base prelude are replaced by the real thing.
  names('streams', 'ReadableStream ReadableStreamDefaultReader ReadableStreamBYOBReader ReadableStreamDefaultController ReadableByteStreamController ReadableStreamBYOBRequest WritableStream WritableStreamDefaultWriter WritableStreamDefaultController TransformStream TransformStreamDefaultController CountQueuingStrategy ByteLengthQueuingStrategy');
  names('codecs', 'CompressionStream DecompressionStream TextEncoderStream TextDecoderStream');
  names('xpath', 'XPathEvaluator XPathExpression XPathResult XPathNSResolver');
  names('xpath', 'evaluate createExpression createNSResolver', g.Document.prototype, true);
  names('urlpattern', 'URLPattern');
  names('nav', 'navigation Navigation NavigationHistoryEntry NavigateEvent NavigationDestination NavigationTransition NavigationActivation NavigationCurrentEntryChangeEvent');
  names('vt', 'ViewTransition ViewTransitionTypeSet');
  names('vt', 'startViewTransition activeViewTransition', g.Document.prototype);
  names('internals', 'ElementInternals CustomStateSet');
  names('internals', 'ValidityState', g, false, true);
  names('internals', 'attachInternals', g.Element.prototype);
  names('css', 'AbstractRange Highlight HighlightRegistry CSSPropertyRule ContentVisibilityAutoStateChangeEvent');
  names('css', 'CSSRule', g, false, true);
  names('css', 'highlights registerProperty registeredProperty', g.CSS, true);
  names('es', 'fromAsync', Array, false, true);
  names('es', 'dispose asyncDispose', Symbol, false, true);
  names('es', 'DisposableStack AsyncDisposableStack SuppressedError', g, false, true);
  if (typeof Atomics === 'object') names('es', 'waitAsync', Atomics, false, true);
  names('intl', 'Locale', g.Intl, false, true);
  names('sched', 'scheduler Scheduler TaskController TaskSignal TaskPriorityChangeEvent');
  Object.defineProperty(g, '__swPlatformLoad', { value: load });
  // V8 stack-trace API that libraries probe for.
  if (!Error.captureStackTrace) {
    H.define(Error, 'captureStackTrace', function captureStackTrace(o) {
      if (o === null || (typeof o !== 'object' && typeof o !== 'function')) throw new TypeError('Invalid argument');
      var s = new Error().stack;
      Object.defineProperty(o, 'stack', { value: (o.name || 'Error') + (o.message ? ': ' + o.message : '') + '\n' + String(s || '').split('\n').slice(2).join('\n'), writable: true, configurable: true });
    });
  }
})(globalThis);
