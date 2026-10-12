/* Tiny DOM helpers, the bridge to Go (window.sweef), the state store and theme. */
(function () {
  var SW = window.SW = window.SW || {};
  SW.$ = function (s, r) { return (r || document).querySelector(s); };
  SW.$$ = function (s, r) { return Array.prototype.slice.call((r || document).querySelectorAll(s)); };

  /* h("div.a.b#id", {onclick, class, style, aria-*, data-*, value}, ...children) */
  SW.h = function h(tag, a) {
    var m = /^([a-z0-9]+)?((?:[.#][\w-]+)*)$/i.exec(tag) || [];
    var el = document.createElement(m[1] || "div");
    (m[2] || "").replace(/([.#])([\w-]+)/g, function (_, t, n) { if (t === "#") el.id = n; else el.classList.add(n); return ""; });
    var i = 1;
    if (a && typeof a === "object" && !a.nodeType && !Array.isArray(a)) {
      i = 2;
      for (var k in a) {
        var v = a[k];
        if (v == null || v === false) continue;
        if (k.slice(0, 2) === "on" && typeof v === "function") el.addEventListener(k.slice(2), v);
        else if (k === "class") v.split(" ").forEach(function (c) { if (c) el.classList.add(c); });
        else if (k === "text") el.textContent = v;
        else if (k === "value") el.value = v;
        else if (k === "style" && typeof v === "object") for (var s in v) el.style.setProperty(s.replace(/[A-Z]/g, function (c) { return "-" + c.toLowerCase(); }), v[s]);
        else el.setAttribute(k, v === true ? "" : v);
      }
    }
    for (; i < arguments.length; i++) add(el, arguments[i]);
    return el;
  };
  function add(el, c) {
    if (c == null || c === false) return;
    if (Array.isArray(c)) c.forEach(function (x) { add(el, x); });
    else el.appendChild(c.nodeType ? c : document.createTextNode(String(c)));
  }
  SW.add = add;
  SW.clear = function (el) { while (el.firstChild) el.removeChild(el.firstChild); return el; };
  SW.esc = function (s) { return String(s).replace(/[&<>"']/g, function (c) { return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]; }); };
  SW.clamp = function (v, a, b) { return Math.max(a, Math.min(b, v)); };
  SW.raf = function (f) { return (window.requestAnimationFrame || function (g) { return setTimeout(g, 16); })(f); };
  SW.debounce = function (f, ms) { var t; return function () { var a = arguments, s = this; clearTimeout(t); t = setTimeout(function () { f.apply(s, a); }, ms); }; };
  SW.schedule = function (f) { var p = false; return function () { if (p) return; p = true; SW.raf(function () { p = false; f(); }); }; };

  /* urls */
  SW.host = function (u) { try { var m = /^[a-z][\w+.-]*:\/\/([^\/?#]*)/i.exec(u || ""); return m ? m[1].replace(/^www\./, "") : (u || ""); } catch (e) { return u || ""; } };
  SW.origin = function (u) { var m = /^([a-z][\w+.-]*:\/\/[^\/?#]*)/i.exec(u || ""); return m ? m[1] : ""; };
  SW.looksLikeURL = function (t) {
    t = (t || "").trim();
    if (!t || /\s/.test(t)) return false;
    if (/^[a-z][\w+.-]*:\/\//i.test(t) || /^(about|mailto|data|file|sweef):/i.test(t)) return true;
    if (/^localhost(:\d+)?([\/?#]|$)/i.test(t) || /^\d{1,3}(\.\d{1,3}){3}(:\d+)?([\/?#]|$)/.test(t)) return true;
    return /^[^\s\/?#.]+(\.[^\s\/?#.]+)*\.[a-z]{2,}(:\d+)?([\/?#]\S*)?$/i.test(t);
  };
  SW.prettyURL = function (u) {
    if (!u) return "";
    if (/^sweef:\/\/newtab/.test(u)) return "";
    return u.replace(/^https?:\/\//, "").replace(/^www\./, "").replace(/\/$/, "");
  };
  SW.fmtBytes = function (n) { if (n == null) return ""; var u = ["B", "KB", "MB", "GB", "TB"], i = 0; while (n >= 1024 && i < 4) { n /= 1024; i++; } return (i ? n.toFixed(n < 10 ? 1 : 0) : Math.round(n)) + " " + u[i]; };
  SW.fmtMs = function (ms) { return ms < 1000 ? Math.round(ms) + " ms" : (ms / 1000).toFixed(ms < 10000 ? 1 : 0) + " s"; };
  SW.hue = function (s) { var h = 0; s = s || ""; for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) % 360; return h; };
  SW.mod = /Mac|iPhone|iPad/.test((navigator && navigator.platform) || "") ? "Cmd" : "Ctrl";

  /* favicon: image when we have one, else a coloured letter, else the Sweef mark for internal pages */
  SW.favicon = function (url, src, title) {
    var el = SW.h("span.fav");
    if (/^sweef:/.test(url || "")) { el.innerHTML = SW.logoSVG(16, "f" + Math.floor(Math.random() * 1e6)); el.style.background = "none"; return el; }
    if (src) { var im = new Image(); im.alt = ""; im.src = src; el.appendChild(im); return el; }
    var host = SW.host(url) || title || "?";
    el.textContent = (host.replace(/^[^a-z0-9]+/i, "")[0] || "?").toUpperCase();
    var hue = SW.hue(host);
    el.style.background = "hsl(" + hue + ",52%,48%)";
    return el;
  };

  /* bridge ----------------------------------------------------------- */
  if (!window.sweef) window.sweef = { post: function () {}, onMessage: null, _stub: true };
  var handlers = {};
  SW.on = function (type, fn) { (handlers[type] = handlers[type] || []).push(fn); };
  SW.emit = function (type, p) { (handlers[type] || []).slice().forEach(function (f) { try { f(p); } catch (e) { console.error("handler " + type + ": " + (e && e.stack || e)); } }); };
  SW.post = function (type, payload) { try { window.sweef.post(type, payload || {}); } catch (e) { console.error("post " + type + ": " + e); } };
  SW.state = {};
  function merge(t, p) {  // RFC 7386
    if (p === null || typeof p !== "object" || Array.isArray(p)) return p;
    if (t === null || typeof t !== "object" || Array.isArray(t)) t = {};
    for (var k in p) { if (p[k] === null) delete t[k]; else t[k] = merge(t[k], p[k]); }
    return t;
  }
  SW.merge = merge;
  SW.get = function (path, d) { var o = SW.state, a = path.split("."); for (var i = 0; i < a.length; i++) { if (o == null) return d; o = o[a[i]]; } return o == null ? d : o; };
  SW.setting = function (key, d) { var s = SW.state.settings || {}; return s[key] != null ? s[key] : d; };
  SW.set = function (key, value) {  // optimistic settings.set
    SW.state.settings = SW.state.settings || {}; SW.state.settings[key] = value;
    SW.post("settings.set", { key: key, value: value }); SW.emit("state", SW.state); SW.render();
  };
  var ping = SW.render = SW.schedule(function () { SW.emit("render", SW.state); });
  window.sweef.onMessage = function (type, p) {
    if (type === "state") { SW.state = p || {}; SW.applyTheme(); SW.emit("state", SW.state); ping(); }
    else if (type === "state.patch") { SW.state = merge(SW.state, p) || {}; SW.applyTheme(); SW.emit("state", SW.state); ping(); }
    else if (type === "downloads.update" && p) {  // one item, an array, or {items}
      var l = p.items || (Array.isArray(p) ? p : (p.id ? [p] : [])), by = {}, ord = [];
      (SW.state.downloads || []).forEach(function (d) { by[d.id] = d; ord.push(d.id); });
      l.forEach(function (d) { if (!by[d.id]) ord.unshift(d.id); by[d.id] = Object.assign(by[d.id] || {}, d); });
      SW.state.downloads = ord.map(function (k) { return by[k]; }); SW.emit("downloads.update", p); ping();
    } else SW.emit(type, p);
  };
  /* lazy loading of rarely-needed code (sidebar, palette): keeps the always-parsed startup set small */
  var lz = {};
  SW.lazy = function (files, cb) {
    var n = files.length;
    function one() { if (--n === 0 && cb) cb(); }
    files.forEach(function (f) {
      if (lz[f] === true) return one();
      if (lz[f]) { lz[f].push(one); return; }
      lz[f] = [one];
      var el = document.createElement(/\.css$/.test(f) ? "link" : "script");
      if (el.tagName === "LINK") { el.rel = "stylesheet"; el.href = f; } else el.src = f;
      var fin = function () { var q = lz[f]; if (q === true) return; lz[f] = true; q.forEach(function (g) { g(); }); };
      el.onload = fin; el.onerror = function () { console.error("load failed: " + f); fin(); };
      document.head.appendChild(el);
    });
  };
  SW.ready = function (page) { SW.post("ui.ready", { page: page }); };

  /* theme ------------------------------------------------------------ */
  SW.applyTheme = function () {
    var s = SW.state, r = document.documentElement, t = s.theme || (s.settings && s.settings.theme) || "system";
    var sys = s.systemDark != null ? !!s.systemDark : !!(window.matchMedia && matchMedia("(prefers-color-scheme: dark)").matches);
    var dark = t === "dark" || (t === "system" && sys);
    var name = s.incognito ? "incognito" : dark ? "dark" : "light";
    if (r.getAttribute("data-theme") !== name) r.setAttribute("data-theme", name);
    if (s.platform && r.getAttribute("data-platform") !== s.platform) r.setAttribute("data-platform", s.platform);
    var d = (s.settings && s.settings.density) || "comfortable";
    if (r.getAttribute("data-density") !== d) r.setAttribute("data-density", d);
  };
  SW.applyTheme();
  /* a page can force a theme with ?theme=dark (used by previews and error pages) */
  try { var q = /[?&]theme=(\w+)/.exec(location.search); if (q) document.documentElement.setAttribute("data-theme", q[1]); var pf = /[?&]platform=(\w+)/.exec(location.search); if (pf) document.documentElement.setAttribute("data-platform", pf[1]); } catch (e) {}
  SW.query = function (k) { var m = new RegExp("[?&]" + k + "=([^&#]*)").exec(location.search); return m ? decodeURIComponent(m[1].replace(/\+/g, " ")) : ""; };
})();
