// Helpers shared by the platform modules (see boot.js). Evaluated once, on the first module load.
// Free variables: g (globalThis), load (module loader), lazyGets (Set of our lazy getters).
var sw = g.__sw;
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
    missing: function (o, k) { var d = Object.getOwnPropertyDescriptor(o, k); return !d || !!(d.get && lazyGets.has(d.get)); },
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
return H;
