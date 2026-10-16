/* Sweef chrome: tabs, toolbar, omnibox, popovers, find bar, toasts, keyboard. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post;
  var $ = function (id) { return document.getElementById(id); };
  var E = { tabs: $("tabs"), wrap: $("tabs-wrap"), strip: $("strip"), url: $("url"), omni: $("omni"), urlview: $("urlview"), secbtn: $("secbtn"), star: $("star"), reload: $("reload"), back: $("back"), fwd: $("fwd"), sb: $("sbtoggle"), agent: $("agentbtn"), dl: $("dlbtn"), data: $("databtn"), media: $("mediabtn"), np: $("npbtn"), sharebar: $("sharebar"), menu: $("menubtn"), progress: $("progress"), compat: $("compat"), layer: $("layer"), viewport: $("viewport"), toasts: $("toasts"), find: $("find"), bm: $("bmbar") };
  var ui = { sb: null, dismissed: {}, lastVp: "", zoom: 100 };
  /* capture icons live here, not in shared/icons.js: the design-system sheet draws every shared icon and its paint golden is pinned */
  SW.I.camera = '<rect x="3.500" y="6.500" width="12" height="11" rx="2.500"/><path d="M15.500 10.500l5-2.500v8l-5-2.500"/>';
  SW.I.screen = '<rect x="3.500" y="4.500" width="17" height="12" rx="2.500"/><path d="M8.500 20h7M12 16.500V20"/>';
  SW.I.code = '<path d="M8.500 7.500L4 12l4.500 4.500M15.500 7.500L20 12l-4.500 4.500M13.500 5.500l-3 13"/>';
  var st = function () { return SW.state; };
  function activeTab() { var s = st(), a = s.tabs || []; for (var i = 0; i < a.length; i++) if (a[i].id === s.active) return a[i]; return a[0] || {}; }
  function field(k, d) { var s = st(), t = activeTab(); return s[k] != null ? s[k] : (t[k] != null ? t[k] : d); }

  /* static icons + labels -------------------------------------------------- */
  function label(el, text) { el.setAttribute("aria-label", text); el.setAttribute("title", text); }
  function setIcon(el, name, size) { SW.clear(el); el.appendChild(icon(name, size || 18)); }
  setIcon(E.back, "back"); label(E.back, S.nav.back); setIcon(E.fwd, "forward"); label(E.fwd, S.nav.forward);
  setIcon(E.reload, "reload"); label(E.reload, S.nav.reload); setIcon(E.sb, "sidebar"); label(E.sb, S.f(S.nav.sidebar));
  setIcon($("newtab"), "plus", 16); setIcon($("tabsearch"), "search", 16); setIcon(E.menu, "menu"); label(E.menu, S.nav.menu);
  setIcon(E.dl, "download"); label(E.dl, S.nav.downloads); label(E.secbtn, S.nav.siteInfo); label(E.star, S.nav.bookmark);
  SW.clear(E.agent); E.agent.appendChild(icon("spark", 16)); E.agent.appendChild(h("span", { text: S.f(S.nav.askPill) })); E.agent.appendChild(h("i.dot.hide"));
  label(E.agent, S.f(S.nav.askPill));
  function buildWC() {
    var wc = $("wc"); SW.clear(wc);
    var b = function (cls, ic, lab, act) { return h("button", { class: cls, "aria-label": lab, title: lab, onclick: function () { post("window." + act); } }, icon(ic, 14)); };
    wc.appendChild(b("", "mini", S.win.min, "minimize")); wc.appendChild(b("mx", "maxi", S.win.max, "maximize")); wc.appendChild(b("x", "close", S.win.close, "close"));
  }
  buildWC(); $("sidebar").setAttribute("aria-label", S.f(S.agent.title));

  /* toasts ----------------------------------------------------------------- */
  function notifyToast(p) {
    var done = false, t = h("div.toast.notify", { role: "alert" }, icon("bell", 18), h("div.nt", h("b.trunc", { text: p.title || "" }), p.text ? h("span.trunc", { text: p.text }) : null, h("small.trunc", { text: SW.host(p.origin) })));
    var kill = function (how) { if (done) return; done = true; if (how !== "click") post("notify.closed", { id: p.id, user: how === "user" }); t.classList.add("out"); setTimeout(function () { if (t.parentNode) t.parentNode.removeChild(t); }, 220); };
    t.appendChild(h("button", { text: S.perm.open, onclick: function () { post("notify.click", { id: p.id }); kill("click"); } }));
    (p.actions || []).slice(0, 2).forEach(function (a) { t.appendChild(h("button", { text: a.title, onclick: function () { post("notify.action", { id: p.id, action: a.id }); kill("click"); } })); });
    t.appendChild(h("button.x", { "aria-label": S.perm.dismiss, onclick: function () { kill("user"); } }, icon("close", 14)));
    E.toasts.appendChild(t); while (E.toasts.children.length > 3) E.toasts.removeChild(E.toasts.firstChild);
    if (!p.sticky) setTimeout(function () { kill("time"); }, 8000);
  }
  function toast(text, kind, action) {
    if (typeof text === "object") { var p = text; if (p.kind === "notify") return notifyToast(p); text = p.text; kind = p.kind; action = p.action; }
    var t = h("div.toast" + (kind ? "." + kind : ""), { role: "status" }, icon(kind === "error" ? "alert" : kind === "warn" ? "warn" : "check", 16), h("span", { text: text }));
    if (action) t.appendChild(h("button", { text: action.label, onclick: function () { if (action.type) post(action.type, action.payload || {}); kill(); } }));
    E.toasts.appendChild(t);
    while (E.toasts.children.length > 3) E.toasts.removeChild(E.toasts.firstChild);
    var done = false, kill = function () { if (done) return; done = true; t.classList.add("out"); setTimeout(function () { if (t.parentNode) t.parentNode.removeChild(t); }, 220); };
    setTimeout(kill, kind === "error" ? 6500 : 4000);
  }
  SW.on("toast", toast);

  /* popovers --------------------------------------------------------------- */
  var cur = null;
  function closePop(noFocus) {
    if (!cur) return; var c = cur; cur = null;
    if (c.scrim.parentNode) c.scrim.parentNode.removeChild(c.scrim); if (c.node.parentNode) c.node.parentNode.removeChild(c.node);
    if (c.anchor) { c.anchor.setAttribute("aria-expanded", "false"); if (!noFocus && c.anchor.focus) c.anchor.focus(); }
    if (c.onClose) c.onClose();
  }
  function openPop(anchor, node, o) {
    o = o || {}; closePop(true);
    var scrim = h("div.scrim", { onmousedown: function () { closePop(true); }, oncontextmenu: function (e) { e.preventDefault(); closePop(true); } });
    node.classList.add("pop"); node.setAttribute("role", o.role || "dialog"); node.style.visibility = "hidden";
    E.layer.appendChild(scrim); E.layer.appendChild(node);
    var W = window.innerWidth, H = window.innerHeight, w = node.offsetWidth, hh = node.offsetHeight, x, y;
    if (o.at) { x = o.at.x; y = o.at.y; } else { var r = anchor.getBoundingClientRect(); x = o.align === "left" ? r.left : r.right - w; y = r.bottom + 8; }
    node.style.left = Math.round(SW.clamp(x, 8, W - w - 8)) + "px"; node.style.top = Math.round(SW.clamp(y, 8, Math.max(8, H - hh - 8))) + "px"; node.style.visibility = "";
    node.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePop(); }
      else if (e.key === "ArrowDown" || e.key === "ArrowUp") {
        var it = SW.$$(".mi,input,button.btn", node), i = it.indexOf(document.activeElement); if (!it.length) return; e.preventDefault();
        it[(i + (e.key === "ArrowDown" ? 1 : -1) + it.length) % it.length].focus();
      }
    });
    if (anchor) anchor.setAttribute("aria-expanded", "true");
    cur = { anchor: anchor, node: node, scrim: scrim, onClose: o.onClose };
    if (!o.noFocus) { var f = node.querySelector(".mi,input,button"); if (f && f.focus) f.focus(); }
    return node;
  }
  function mi(ic, text, o) {
    o = o || {};
    var b = h("button.mi" + (o.danger ? ".danger" : ""), { role: "menuitem", onclick: function (e) { if (o.keep !== true) closePop(true); if (o.on) o.on(e); } },
      ic ? icon(ic, 18) : h("span", { style: { width: "18px" } }), h("div.grow", text, o.sub ? h("div.sub", { text: o.sub }) : null), o.kbd ? h("span.kbd", { text: o.kbd }) : null);
    return b;
  }
  SW.chrome = { toast: toast, openPop: openPop, closePop: closePop, mi: mi };

  /* context menus load on first use (ctx.js: the component, ctxmenus.js: tab, strip, omnibox and bookmarks bar menus).
     The first right click on the chrome is held until they are loaded, then replayed at the same spot. */
  SW.ctxFiles = ["/chrome/ctx.css", "/chrome/ctx.js", "/chrome/ctxmenus.js"];
  SW.ctx = { show: function (spec) { SW.ctxReady(function () { SW.ctx.show(spec); }); }, close: function () {}, isOpen: function () { return false; } };
  SW.on("ctx.menu", function (p) { SW.ctx.show(p); });
  SW.on("ctx.close", function () { SW.ctx.close(true); });
  document.addEventListener("contextmenu", function (e) {
    if (SW.ctxLoaded) return;
    var t = e.target, x = e.clientX, y = e.clientY; e.preventDefault(); e.stopPropagation();
    SW.ctxReady(function () { try { t.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true, clientX: x, clientY: y, button: 2 })); } catch (err) {} });
  }, true);

  /* lazily loaded modules: the sidebar (and its markdown renderer) and the palette are only parsed when used */
  var skq = [], skBuf = [], skState = 0;
  function withSk(cb) {
    if (SW.sidekick) return cb(SW.sidekick);
    skq.push(cb); if (skState) return; skState = 1;
    SW.lazy(["/chrome/sidebar.css", "/shared/md.js", "/chrome/sidebar.js"], function () {
      skq.splice(0).forEach(function (f) { f(SW.sidekick); }); skBuf.splice(0).forEach(function (e) { SW.sidekick[e[0]](e[1]); }); SW.emit("render");
    });
  }
  SW.sk = { focus: function () { withSk(function (s) { s.focus(); }); }, ask: function (t, src) { withSk(function (s) { s.ask(t, src); }); }, newChat: function () { withSk(function (s) { s.newChat(); }); }, send: function (t, m) { withSk(function (s) { s.send(t, m); }); } };
  SW.on("agent.event", function (ev) { if (!SW.sidekick) { skBuf.push(["onEvent", ev]); withSk(function () {}); } });
  SW.on("agent.chat.load", function (p) { if (!SW.sidekick) { skBuf.push(["loadChat", p]); withSk(function () {}); } });
  /* developer tools: the docked panel is parsed when it is first opened (devtools.js) */
  var dtState = 0;
  function renderDevtools() {
    var d = st().devtools, open = !!(d && d.open), el = $("devtools");
    el.classList.toggle("hide", !open);
    if (open) {
      var mx = Math.max(160, (window.innerHeight || 700) - 220);
      if (!(SW.devtools && SW.devtools.drag)) el.style.height = SW.clamp(+SW.setting("devtools.height", 320) || 320, 160, mx) + "px";
    }
    if (!open && !SW.devtools) return;
    if (SW.devtools) return SW.devtools.sync();
    if (dtState) return; dtState = 1;
    SW.lazy(["/chrome/devtools.css", "/shared/vlist.js", "/chrome/devtools.js"], function () { if (SW.devtools) SW.devtools.sync(); });
  }
  SW.palette = { open: function (m) { SW.lazy(["/chrome/palette.js"], function () { SW.palette.open(m); }); } };

  /* tabs ------------------------------------------------------------------- */
  var tabEls = {}, MINW = 108, MAXW = 208;
  function mkTab(t) {
    var r = {};
    var el = h("div.tab", { role: "tab", "data-id": t.id, tabindex: "-1" });
    r.fav = h("span.fv"); r.tt = h("span.tt"); r.md = h("span.md.hide"); r.au = h("button.au", { tabindex: "-1" }); r.tx = h("button.tx", { tabindex: "-1" }, icon("close", 14));
    r.au.addEventListener("click", function (e) { e.stopPropagation(); post("tab.mute", { id: t.id }); });
    r.tx.addEventListener("click", function (e) { e.stopPropagation(); post("tab.close", { id: t.id }); });
    el.appendChild(r.fav); el.appendChild(r.tt); el.appendChild(r.md); el.appendChild(r.au); el.appendChild(r.tx);
    el._r = r; el._k = {}; return el;
  }
  function updTab(el, t, active) {
    var r = el._r, k = el._k, title = t.title || (/^sweef:\/\/newtab/.test(t.url || "") || !t.url ? S.tab.newTitle : SW.host(t.url) || S.tab.untitled);
    if (t.crashed) title = S.tab.crashed;
    var fk = (t.loading ? "L" : "F") + "|" + (t.favicon || "") + "|" + SW.host(t.url || "") + (/^sweef:/.test(t.url || "") ? "i" : "");
    if (k.fk !== fk) { k.fk = fk; SW.clear(r.fav); r.fav.appendChild(t.loading ? h("span.spin") : t.crashed ? icon("warn", 16) : SW.favicon(t.url, t.favicon, t.title)); }
    if (k.title !== title) { k.title = title; r.tt.textContent = title; el.setAttribute("title", title); el.setAttribute("aria-label", title + (t.audible ? ", " + S.tab.playing : "") + (t.loading ? ", " + S.tab.loading : "")); }
    var au = t.audible || t.muted ? (t.muted ? "m" : "a") : "";
    if (k.au !== au) { k.au = au; SW.clear(r.au); r.au.classList.toggle("hide", !au); if (au) { r.au.appendChild(icon(au === "m" ? "speakeroff" : "speaker", 14)); r.au.setAttribute("aria-label", au === "m" ? S.tab.unmute : S.tab.mute); r.au.setAttribute("title", au === "m" ? S.tab.unmute : S.tab.mute); } }
    var md = (t.camera ? "c" : "") + (t.mic ? "m" : "") + (t.sharing ? "s" : "");  /* capture in use: icon + dot, red for camera and sharing, green for a microphone alone */
    if (k.md !== md) {
      k.md = md; SW.clear(r.md); r.md.classList.toggle("hide", !md); r.md.className = "md" + (md ? "" : " hide") + (t.camera || t.sharing ? " rec" : " live");
      if (md) { var mt = t.camera && t.mic ? S.media.usingBoth : t.camera ? S.media.usingCam : t.mic ? S.media.usingMic : S.media.usingShare; r.md.appendChild(icon(t.camera ? "camera" : t.sharing ? "screen" : "mic", 13)); r.md.appendChild(h("i.mdot")); r.md.setAttribute("title", mt); r.md.setAttribute("role", "img"); r.md.setAttribute("aria-label", mt); }
    }
    r.tx.setAttribute("aria-label", S.tab.close); r.tx.setAttribute("title", S.tab.close);
    el.classList.toggle("pinned", !!t.pinned); el.classList.toggle("crashed", !!t.crashed); el.classList.toggle("popup", !!t.popup);
    el.setAttribute("aria-selected", active ? "true" : "false"); el.setAttribute("tabindex", active ? "0" : "-1");
  }
  function renderTabs() {
    var s = st(), tabs = s.tabs || [], seen = {}, prev = null;
    tabs.forEach(function (t) {
      var el = tabEls[t.id]; if (!el) el = tabEls[t.id] = mkTab(t);
      updTab(el, t, t.id === s.active); seen[t.id] = 1;
      var want = prev ? prev.nextSibling : E.tabs.firstChild; if (want !== el && !(drag && drag.t === el)) E.tabs.insertBefore(el, want);
      prev = el;
    });
    Object.keys(tabEls).forEach(function (id) { if (!seen[id]) { var e = tabEls[id]; if (e.parentNode) e.parentNode.removeChild(e); delete tabEls[id]; } });
  }
  /* The tab width depends on the tab count, the window width and the window controls. It is measured (a layout read)
     only when one of those changed, and after every other write of a render pass: a read in the middle of the writes
     makes the engine run style + layout again for each of them. */
  var lastN = -1, lastW = -1, lastQ = "";
  function sizeTabs() {
    var s = st(), tabs = s.tabs || [], pinned = tabs.filter(function (t) { return t.pinned; }).length, n = tabs.length - pinned;
    var q = n + "|" + pinned + "|" + window.innerWidth + "|" + (document.documentElement.getAttribute("data-maximized") || "") + "|" + (document.documentElement.getAttribute("data-platform") || "");
    if (q === lastQ && lastN !== -1) return;
    var sw = E.strip.clientWidth || window.innerWidth;
    var fixed = (E.strip.firstChild.offsetWidth || 0) + 34 + 34 + $("wc").offsetWidth + 64 + 16;
    var avail = Math.max(120, sw - fixed), tw = n ? SW.clamp(Math.floor((avail - pinned * 40) / n) - 2, MINW, MAXW) : MAXW;
    lastQ = q; var key = n + "|" + tw + "|" + pinned + "|" + sw; if (key === lastN) return; lastN = key;
    E.tabs.style.setProperty("--tw", tw + "px"); E.wrap.style.maxWidth = avail + "px";
    E.wrap.classList.toggle("ovf", n * (tw + 2) + pinned * 40 > avail);
    Object.keys(tabEls).forEach(function (id) { var e = tabEls[id]; e.classList.toggle("narrow", tw < 130); e.classList.toggle("tiny", tw < 70); });
    SW.raf(function () { tsc.refresh(); var a = tabEls[s.active]; if (a) tsc.reveal(a); });
  }
  var tsc = SW.chrome.tsc = SW.scroller(E.wrap, E.tabs, { horizontal: true, noThumb: true });
  window.addEventListener("resize", function () { lastN = -1; lastQ = ""; sizeTabs(); reportLayout(); });

  var drag = null;
  E.tabs.addEventListener("mousedown", function (e) {
    var t = e.target.closest ? e.target.closest(".tab") : null; if (!t) return;
    var id = t.getAttribute("data-id");
    if (e.button === 1) { e.preventDefault(); post("tab.close", { id: id }); return; }
    if (e.button !== 0 || (e.target.closest && e.target.closest(".tx,.au"))) return;
    if (id !== st().active) post("tab.activate", { id: id });
    var tabs = st().tabs || [], idx = tabs.findIndex(function (x) { return x.id === id; });
    drag = { t: t, id: id, x0: e.clientX, from: idx, to: idx, on: false, left: t.offsetLeft, w: t.offsetWidth + 2, pin: !!(tabs[idx] && tabs[idx].pinned) };
    window.addEventListener("mousemove", dragMove); window.addEventListener("mouseup", dragEnd);
  });
  function dragMove(e) {
    if (!drag) return; var dx = e.clientX - drag.x0;
    if (!drag.on) { if (Math.abs(dx) < 5) return; drag.on = true; drag.t.classList.add("dragging"); }
    var tabs = st().tabs || [], n = tabs.length, lo = 0, hi = n - 1, pins = tabs.filter(function (x) { return x.pinned; }).length;
    if (drag.pin) hi = pins - 1; else lo = pins;
    var to = SW.clamp(drag.from + Math.round(dx / drag.w), lo, hi); drag.to = to;
    var minX = -drag.left + 4 + (drag.pin ? 0 : 0), maxX = E.tabs.offsetWidth - drag.left - drag.w;
    drag.t.style.transform = "translateX(" + Math.round(SW.clamp(dx, minX, maxX)) + "px)";
    tabs.forEach(function (x, i) {
      var el = tabEls[x.id]; if (!el || el === drag.t) return; var sh = 0;
      if (to > drag.from && i > drag.from && i <= to) sh = -drag.w; else if (to < drag.from && i < drag.from && i >= to) sh = drag.w;
      el.style.transition = "transform var(--dur-2) var(--ease)"; el.style.transform = sh ? "translateX(" + sh + "px)" : "";
    });
  }
  function dragEnd() {
    window.removeEventListener("mousemove", dragMove); window.removeEventListener("mouseup", dragEnd);
    if (!drag) return; var d = drag; drag = null;
    SW.$$(".tab", E.tabs).forEach(function (el) { el.style.transform = ""; el.style.transition = ""; });
    d.t.classList.remove("dragging");
    if (d.on && d.to !== d.from) {
      var a = (st().tabs || []).slice(), m = a.splice(d.from, 1)[0]; a.splice(d.to, 0, m); st().tabs = a;
      post("tab.move", { id: d.id, to: d.to }); renderTabs();
    }
  }
  /* tab, strip, omnibox and bookmarks bar context menus: ctxmenus.js */
  E.tabs.addEventListener("keydown", function (e) {
    var t = e.target.closest ? e.target.closest(".tab") : null; if (!t) return; var k = e.key, tabs = st().tabs || [], i = tabs.findIndex(function (x) { return x.id === t.getAttribute("data-id"); });
    var go = function (j) { var x = tabs[SW.clamp(j, 0, tabs.length - 1)]; if (x) { e.preventDefault(); post("tab.activate", { id: x.id }); if (tabEls[x.id]) tabEls[x.id].focus(); } };
    if (k === "ArrowRight") go(i + 1); else if (k === "ArrowLeft") go(i - 1); else if (k === "Home") go(0); else if (k === "End") go(tabs.length - 1);
    else if (k === "Delete" || (k === "w" && (e.ctrlKey || e.metaKey))) { e.preventDefault(); post("tab.close", { id: tabs[i].id }); }
    else if (k === "Enter" || k === " ") { e.preventDefault(); post("tab.activate", { id: tabs[i].id }); }
  });
  $("newtab").addEventListener("click", function () { post("tab.new", {}); });
  $("tabsearch").addEventListener("click", function () { SW.palette.open("tabs"); });
  E.strip.addEventListener("mousedown", function (e) { if (e.button === 0 && (e.target === E.strip || e.target.id === "drag" || e.target.id === "inset")) post("window.drag", { x: e.screenX, y: e.screenY }); });
  E.strip.addEventListener("dblclick", function (e) { if (e.target === E.strip || e.target.id === "drag") post("window.maximize"); });

  /* toolbar ---------------------------------------------------------------- */
  function secInfo(level) { return { secure: ["lock", S.sec.secure, S.sec.secureSub], insecure: ["unlock", S.sec.insecure, S.sec.insecureSub], internal: ["logo", S.f(S.sec.internal), S.f(S.sec.internalSub)], warning: ["warn", S.sec.warning, S.sec.warningSub] }[level] || ["info", S.sec.none, ""]; }
  var lastSec = "", lastUrl = null;
  function renderToolbar() {
    var s = st(), t = activeTab(), url = field("url", ""), loading = !!field("loading", false), sec = field("security", {}) || {}, level = sec.level || (/^sweef:/.test(url) ? "internal" : /^https:/.test(url) ? "secure" : /^http:/.test(url) ? "insecure" : "none");
    var dis = function (el, off) { el.setAttribute("aria-disabled", off ? "true" : "false"); };
    dis(E.back, !field("canBack", false)); dis(E.fwd, !field("canForward", false));
    var rl = loading ? "close" : "reload", rk = rl + loading; if (E.reload._k !== rk) { E.reload._k = rk; setIcon(E.reload, rl); label(E.reload, loading ? S.nav.stop : S.nav.reload); }
    var sk = level + "|" + (sec.certValid === false); if (sk !== lastSec) {
      lastSec = sk; var info = secInfo(level); SW.clear(E.secbtn); E.secbtn.className = "secbtn " + level;
      E.secbtn.appendChild(info[0] === "logo" ? SW.logo(16, "sbl") : icon(info[0], 16));
      if (level === "insecure") E.secbtn.appendChild(h("span", { text: S.sec.notSecure })); if (level === "warning") E.secbtn.appendChild(h("span", { text: S.sec.warnShort }));
    }
    var bm = !!field("bookmarked", false); if (E.star._k !== bm) { E.star._k = bm; setIcon(E.star, bm ? "starfill" : "star", 16); E.star.setAttribute("aria-pressed", bm ? "true" : "false"); label(E.star, bm ? S.nav.unbookmark : S.nav.bookmark); }
    var focus = E.omni.classList.contains("focus");
    var disp = /^sweef:\/\/newtab/.test(url) ? "" : url;
    if (!focus && lastUrl !== disp) { lastUrl = disp; E.url.value = disp; if (omni) omni.typed = disp; renderUrlView(disp); }
    var open = sbOpen(); E.sb.setAttribute("aria-pressed", open ? "true" : "false"); E.sb.classList.toggle("on", open);
    renderAgentBtn(open); renderDl(); renderData(); renderMedia(); renderProgress(loading); renderCompat(); renderBmBar();
  }
  /* Data meter chip: a calm pill (ring + used / limit). It shows only when the meter is on, counting works, and a budget or a saver level is set.
     The popover (ring, this page, top sites) loads on first click. */
  function renderData() {
    var d = st().data || {}, b = d.budget || {}, el = E.data, T = S.data, on = d.showMeter !== false && d.available !== false && (b.enabled || (d.level && d.level !== "off"));
    el.classList.toggle("hide", !on); if (!on) { el._k = ""; return; }
    var used = d.used || 0, free = !b.enabled, lim = free ? 0 : (b.limitMB || 0), pct = free ? 0 : (b.pct || 0), tone = SW.dataTone(b);
    var limT = lim >= 1024 && lim % 1024 === 0 ? (lim / 1024) + " GB" : lim + " MB", usedT = SW.fmtBytes(used), per = T.period[b.period] || T.period.month;
    var k = [used, lim, tone, Math.round(pct * 2)].join("|"); if (el._k === k) return; el._k = k;
    SW.clear(el); el.className = "datachip" + (tone ? " " + tone : "");
    var fill = free ? 0 : Math.max(0, Math.min(100, pct)); el.style.setProperty("--p", fill.toFixed(1) + "%");  /* the pill fills as the budget is used */
    el.appendChild(icon("drop", 14)); el.appendChild(h("span.dt", { text: free ? usedT : usedT + " / " + limT }));
    label(el, free ? S.f(T.chipAriaFree, { period: per, used: usedT }) : S.f(T.chipAria, { period: per, used: usedT, limit: limT }));
    el.setAttribute("title", free ? S.f(T.chipTipFree, { period: per, used: usedT }) : S.f(T.chipTip, { period: per, used: usedT, limit: limT }));
  }
  /* Camera, microphone, screen sharing: the indicators are here; the permission prompt and the "in use" popover load on first use. */
  var shownPrompt = "";
  /* the load event can fire before the script has run, so wait until it has defined its entry points */
  var mq = [];
  function mediaLazy(cb) {
    if (SW.mediaPrompt && SW.mediaPop) return cb();
    mq.push(cb); if (mq.length > 1) return;
    function flush(n) { if (SW.mediaPrompt && SW.mediaPop) mq.splice(0).forEach(function (f) { f(); }); else if (n < 100) setTimeout(function () { flush(n + 1); }, 20); else mq = []; }
    SW.lazy(["/chrome/media.css", "/chrome/media.js"], function () { flush(0); });
  }
  /* the page's Media Session (navigator.mediaSession): a "now playing" button that opens the controls */
  function renderNowPlaying() {
    var ses = (st().media || {}).session, el = E.np, on = !!(ses && (ses.title || ses.playbackState !== "none" || (ses.actions || []).length));
    var k = on ? ses.playbackState : "";
    el.classList.toggle("hide", !on);
    if (el._k !== k) {
      el._k = k; SW.clear(el);
      if (on) { el.appendChild(icon(ses.playbackState === "playing" ? "pause" : "music", 16)); el.setAttribute("aria-label", S.media.nowPlaying); el.setAttribute("title", S.media.nowPlaying); }
    }
    if (SW.nowPlaying) SW.nowPlaying.refresh();
  }
  E.np.addEventListener("click", function () { mediaLazy(function () { SW.nowPlaying.open(E.np); }); });
  function renderMedia() {
    renderNowPlaying();
    var m = st().media || {}, ses = m.sessions || [], shares = m.shares || [], p = m.prompt || null, T = S.media;
    var cam = ses.some(function (s) { return s.kind === "camera"; }), mic = ses.some(function (s) { return s.kind === "microphone"; }), scr = ses.some(function (s) { return s.kind === "screen"; });
    var on = cam || mic || scr, el = E.media, k = (cam ? "c" : "") + (mic ? "m" : "") + (scr ? "s" : "");
    el.classList.toggle("hide", !on);
    if (el._k !== k) {
      el._k = k; SW.clear(el); el.className = "mediabtn" + (on ? "" : " hide") + (cam || scr ? " rec" : " live");
      if (on) {
        var tx = cam && mic ? T.usingBoth : cam ? T.usingCam : mic ? T.usingMic : T.usingShare;
        el.appendChild(icon(cam ? "camera" : scr ? "screen" : "mic", 16)); el.appendChild(h("i.mdot")); label(el, tx);
      }
    }
    var sk = shares.map(function (s) { return s.id; }).join(",");
    if (E.sharebar._k !== sk) {
      E.sharebar._k = sk; SW.clear(E.sharebar); E.sharebar.classList.toggle("hide", !shares.length);
      if (shares.length) {
        var s0 = shares[0], what = s0.surface === "window" ? T.whatWin : T.whatTab;
        E.sharebar.appendChild(icon("screen", 18)); E.sharebar.appendChild(h("span.t", h("b", { text: S.f(T.sharing, { host: s0.host, what: what }) }), shares.length > 1 ? h("span", { text: "+" + (shares.length - 1) }) : null));
        E.sharebar.appendChild(h("button.btn.sm.danger", { onclick: function () { shares.forEach(function (s) { post("media.stop", { id: s.id }); }); } }, T.stopSharing));
      }
    }
    var pid = p ? p.id : "";
    if (pid !== shownPrompt) { var had = shownPrompt; shownPrompt = pid; if (p) mediaLazy(function () { SW.mediaPrompt.show(st().media && st().media.prompt || null); }); else if (had && SW.mediaPrompt) SW.mediaPrompt.show(null); }
  }
  E.media.addEventListener("click", function () { mediaLazy(function () { SW.mediaPop.open(E.media); }); });
  E.data.addEventListener("click", function () { SW.lazy(["/shared/datasaver.css", "/chrome/data.css", "/shared/datasaver.js", "/chrome/data.js"], function () { if (SW.dataPop) SW.dataPop.open(E.data); }); });
  function renderUrlView(u) {
    SW.clear(E.urlview);
    if (!u) { E.urlview.appendChild(h("em", { text: S.f(S.omni.placeholder) })); return; }
    var m = /^([a-z][\w+.-]*:\/\/)?([^\/?#]*)(.*)$/i.exec(u) || [];
    var sch = m[1] || "", host = m[2] || u, rest = (m[3] || "").replace(/^\/$/, "");
    if (/^https:/i.test(sch) || /^sweef:/i.test(sch)) sch = ""; if (/^sweef:/i.test(m[1] || "")) host = "sweef://" + host;
    if (sch) E.urlview.appendChild(h("em", { text: sch })); E.urlview.appendChild(h("b", { text: host.replace(/^www\./, "") }));
    if (rest) E.urlview.appendChild(h("span", { text: rest }));
  }
  function renderAgentBtn(open) {
    var ag = st().agent || {}, state = ag.state || (ag.hasKey === false ? "off" : "idle");
    E.agent.className = "agentbtn" + (state === "working" ? " working" : state === "waiting" ? " waiting" : "");
    E.agent.setAttribute("aria-pressed", open && state === "idle" ? "true" : "false");
    var d = E.agent.querySelector(".dot"); d.className = "dot" + (state === "working" ? " busy" : state === "waiting" ? " warn" : state === "error" ? " err" : state === "off" ? "" : " ok") + (state === "idle" && !open ? " hide" : "");
    if (state === "off") d.classList.add("hide");
    E.agent.setAttribute("title", S.f(S.nav.askPill) + " · " + (S.agent.agentState[state] || ""));
  }
  function renderDl() {
    var dls = st().downloads || [], act = dls.filter(function (d) { return d.state === "progress" || d.state === "inprogress"; });
    E.dl.classList.toggle("hide", !dls.length); E.dl.classList.toggle("active", !!act.length);
    var pct = act.length ? act.reduce(function (a, d) { return a + (d.total ? d.received / d.total : 0); }, 0) / act.length : -1;
    var k = dls.length + "|" + (pct >= 0 ? Math.round(pct * 20) : -1); if (E.dl._k === k) return; E.dl._k = k;
    setIcon(E.dl, "download");
    if (pct >= 0) { var c = 2 * Math.PI * 14; E.dl.insertAdjacentHTML("beforeend", '<svg class="ring" viewBox="0 0 32 32"><circle cx="16" cy="16" r="14" fill="none" stroke="currentColor" stroke-opacity=".18" stroke-width="2"/><circle cx="16" cy="16" r="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-dasharray="' + (c * pct).toFixed(1) + " " + c.toFixed(1) + '" transform="rotate(-90 16 16)"/></svg>'); }
  }
  var pgT = 0;
  function renderProgress(loading) {
    var p = field("progress", null), pr = E.progress, bar = pr.firstChild;
    if (loading) { pr.classList.add("on"); clearTimeout(pgT); if (p == null) pr.classList.add("ind"); else { pr.classList.remove("ind"); bar.style.transform = "scaleX(" + Math.max(0.06, Math.min(1, p)) + ")"; } }
    else if (pr.classList.contains("on")) { pr.classList.remove("ind"); bar.style.transform = "scaleX(1)"; clearTimeout(pgT); pgT = setTimeout(function () { pr.classList.remove("on"); bar.style.transform = "scaleX(.05)"; }, 260); }
  }
  function renderCompat() {
    var c = st().compat, e = E.compat, key = c && !ui.dismissed[SW.origin(c.url || field("url", ""))] ? (c.url || "") + (c.message || "") : "";
    if (e._k === key) return; e._k = key; SW.clear(e); e.classList.toggle("hide", !key); if (!key) return;
    var url = c.url || field("url", "");
    e.appendChild(icon("warn", 18)); e.appendChild(h("div.t", h("b", { text: c.message || S.f(S.compat.text) }), h("span", { text: S.f(S.compat.sub) })));
    e.appendChild(h("button.btn.sm", { text: S.compat.open, onclick: function () { post("openExternal", { url: url }); } }));
    e.appendChild(h("button.btn.sm.ghost", { text: S.compat.dismiss, onclick: function () { ui.dismissed[SW.origin(url)] = 1; post("compat.dismiss", { origin: SW.origin(url) }); renderCompat(); reportLayout(); } }));
  }
  function renderBmBar() {
    var on = SW.setting("bookmarks.bar", false) && (st().bookmarksBar || []).length, bar = E.bm;
    bar.classList.toggle("hide", !on); var k = on ? JSON.stringify(st().bookmarksBar) : ""; if (bar._k === k) return; bar._k = k; SW.clear(bar);
    (st().bookmarksBar || []).slice(0, 14).forEach(function (b) { bar.appendChild(h("button", { "data-id": b.id, title: b.url, onclick: function () { post("nav.go", { tab: st().active, input: b.url }); } }, SW.favicon(b.url, b.favicon, b.title), h("span.trunc", { text: b.title || SW.host(b.url) }))); });
  }

  function renderPerm() { if ((st().perm || {}).prompt || SW.permPop) SW.lazy(["/chrome/perm.css", "/chrome/perm.js"], function () { SW.permPop.sync(); }); }

  /* toolbar events --------------------------------------------------------- */
  E.back.addEventListener("click", function () { post("nav.back"); }); E.fwd.addEventListener("click", function () { post("nav.forward"); });
  E.reload.addEventListener("click", function () { post(field("loading", false) ? "nav.stop" : "nav.reload"); });
  E.star.addEventListener("click", function () { var s = st(); var v = !field("bookmarked", false); s.bookmarked = v; post("bookmark.toggle"); toast(v ? S.toast.bookmarked : S.toast.unbookmarked); renderToolbar(); });
  E.sb.addEventListener("click", function () { setSidebar(!sbOpen()); });
  E.agent.addEventListener("click", function () { var s = st().agent || {}; if (!sbOpen()) setSidebar(true); SW.sk.focus(); });
  E.menu.addEventListener("click", openMenu); E.dl.addEventListener("click", openDownloads); E.secbtn.addEventListener("click", openSiteInfo);

  /* omnibox ---------------------------------------------------------------- */
  var omni = new SW.Omni({
    input: E.url, list: $("od"), ghost: $("ghost"), scope: "bar",
    getTab: function () { return st().active || ""; },
    onAsk: function (text) { SW.sk.ask(text, "omnibox"); E.url.blur(); },
    onDone: function () { E.url.blur(); },
    onEscape: function () { E.url.value = lastUrl || ""; E.url.blur(); },
    revert: function () { E.url.value = lastUrl || ""; E.url.blur(); }
  });
  E.url.addEventListener("focus", function () { E.url.setAttribute("placeholder", S.f(S.omni.placeholder)); E.omni.classList.add("focus"); E.url.select(); omni.prime(); });
  E.url.addEventListener("blur", function () { setTimeout(function () { if (document.activeElement !== E.url) { E.omni.classList.remove("focus"); E.url.removeAttribute("placeholder"); omni.close(false); E.url.value = lastUrl || ""; renderToolbar(); } }, 0); });
  function focusOmni() { E.url.focus(); E.url.select(); }

  /* site info -------------------------------------------------------------- */
  function pageVis() { var ag = st().agent || {}; return ag.pageVisibility || "ask"; }
  function visText(v) { return v === "allowed" ? S.vis.allowed : v === "blocked" ? S.vis.blocked : S.vis.ask; }
  function setVis(allowed) {
    var url = field("url", ""), o = SW.origin(url); st().agent = st().agent || {}; st().agent.pageVisibility = allowed ? "allowed" : "blocked";
    post("agent.pageVisibility", { origin: o, allowed: allowed }); toast(S.f(allowed ? S.vis.toastOn : S.vis.toastOff, { host: SW.host(url) || "this page" })); SW.emit("render");
  }
  function visChooser(onPick) {
    var v = pageVis(), box = h("div.vischoice", { role: "radiogroup", "aria-label": S.f(S.vis.title) });
    [["allowed", true], ["blocked", false]].forEach(function (o) {
      var b = h("button.mi", { role: "radio", "aria-checked": v === o[0] ? "true" : "false", onclick: function () { setVis(o[1]); if (onPick) onPick(); } }, h("span.grow", { text: visText(o[0]) }), v === o[0] ? icon("check", 16, "ck") : null); box.appendChild(b);
    });
    box.appendChild(h("p", { text: S.f(v === "allowed" ? S.vis.subAllowed : v === "blocked" ? S.vis.subBlocked : S.vis.subAsk) })); return box;
  }
  function permRow(ps) {
    var box = h("div"), open = false, P = S.perm;
    var row = h("button.mi", { "aria-expanded": "false", onclick: function () { open = !open; row.setAttribute("aria-expanded", open); SW.clear(box); if (open) box.appendChild(choose()); } }, icon("bell", 18), h("span.grow", { text: P.label }), h("span.valpill" + (ps.state === "allowed" ? ".on" : ps.state === "blocked" ? ".off" : ""), { text: P.st[ps.state] }), icon("chevr", 16));
    function choose() {
      var c = h("div.vischoice", { role: "radiogroup", "aria-label": P.label });
      [["ask", "", P.subAsk], ["allowed", "granted", P.subAllowed], ["blocked", "denied", P.subBlocked]].forEach(function (o) {
        c.appendChild(h("button.mi", { role: "radio", "aria-checked": ps.state === o[0] ? "true" : "false", onclick: function () { ps.state = o[0]; post("perm.set", { origin: ps.origin, name: "notifications", state: o[1] }); closePop(); } }, h("span.grow", { text: P.st[o[0]] }), ps.state === o[0] ? icon("check", 16, "ck") : null));
      });
      c.appendChild(h("p", { text: ps.state === "allowed" ? P.subAllowed : ps.state === "blocked" ? P.subBlocked : P.subAsk })); return c;
    }
    return h("div", row, box);
  }
  function openSiteInfo() {
    var url = field("url", ""), sec = field("security", {}) || {}, level = E.secbtn.className.replace(/^secbtn\s*/, "") || "none", info = secInfo(level), internal = level === "internal";
    var node = h("div.wide");
    node.appendChild(h("div.sitehead." + level, internal ? SW.logo(22, "shl") : icon(info[0], 22), h("div", h("b", { text: info[1] }), info[2] ? h("span.s", { text: info[2] }) : null)));
    if (!internal) {
      var vis = h("div"), val = pageVis(), open = false;
      var row = h("button.mi", { "aria-expanded": "false", onclick: function () { open = !open; row.setAttribute("aria-expanded", open); SW.clear(vis); if (open) vis.appendChild(visChooser(function () { closePop(); })); } }, icon("spark", 18), h("span.grow", { text: S.f(S.vis.title) }), h("span.valpill" + (val === "allowed" ? ".on" : val === "blocked" ? ".off" : ""), { text: visText(val) }), icon("chevr", 16));
      node.appendChild(row); node.appendChild(vis);
      var ms = (st().media || {}).site;
      if (ms) {  /* stored camera / microphone / screen decisions for this site, each with a way to take it back */
        var rows = [["camera", "camera", S.media.camera], ["microphone", "mic", S.media.mic], ["screen", "screen", S.media.screen]].filter(function (x) { return ms[x[0]] === "allow" || ms[x[0]] === "block"; });
        rows.forEach(function (x) {
          var allowed = ms[x[0]] === "allow";
          node.appendChild(h("div.mi.permrow", icon(x[1], 18), h("span.grow", { text: x[2] }), h("span.valpill" + (allowed ? ".on" : ".off"), { text: allowed ? S.media.allowed : S.media.blocked }),
            h("button.ibtn.sm", { "aria-label": S.media.reset + ": " + x[2], title: S.media.reset, onclick: function () { ms[x[0]] = "ask"; post("site.media", { origin: ms.origin, name: x[0], value: "" }); closePop(); } }, icon("close", 14))));
        });
      }
      var ps = (st().perm || {}).site;
      if (ps) node.appendChild(permRow(ps));
      node.appendChild(h("div.sep"));
      node.appendChild(mi("cert", sec.certValid === false ? S.sec.certInvalid : S.sec.certValid, { on: function () { post("menu.open", { name: "certificate", origin: SW.origin(url) }); } }));
      node.appendChild(mi("cookie", S.sec.cookies, { on: function () { post("menu.open", { name: "cookies", origin: SW.origin(url) }); } }));
      node.appendChild(mi("gear", S.sec.siteSettings, { on: function () { post("tab.new", { url: "sweef://settings#site/" + SW.host(url) }); } }));
    } else node.appendChild(mi("gear", S.menu.settings, { on: function () { post("tab.new", { url: "sweef://settings" }); } }));
    openPop(E.secbtn, node, { align: "left" });
  }
  SW.chrome.visChooser = visChooser; SW.chrome.pageVis = pageVis; SW.chrome.visText = visText; SW.chrome.setVis = setVis;

  /* main menu / downloads -------------------------------------------------- */
  var ZS = [25, 33, 50, 67, 75, 80, 90, 100, 110, 125, 150, 175, 200, 250, 300, 400, 500];
  function openMenu() {
    var page = function (u) { return function () { post("tab.new", { url: u }); }; };
    var zv = h("span.zv", { text: ui.zoom + "%" });
    var zoom = function (d) { var i = ZS.indexOf(ui.zoom); if (i < 0) i = 7; i = SW.clamp(i + d, 0, ZS.length - 1); ui.zoom = d === 0 ? 100 : ZS[i]; zv.textContent = ui.zoom + "%"; post("menu.open", { name: d === 0 ? "zoom.reset" : d > 0 ? "zoom.in" : "zoom.out" }); };
    var m = h("div", { style: { width: "304px" } },
      mi("plus", S.menu.newTab, { kbd: SW.mod + "+T", on: function () { post("tab.new", {}); } }), mi("hat", S.menu.newIncognito, { kbd: SW.mod + "+Shift+N", on: function () { post("incognito.new"); } }), h("div.sep"),
      mi("history", S.menu.history, { kbd: SW.mod + "+H", on: page("sweef://history") }), mi("download", S.menu.downloads, { kbd: SW.mod + "+J", on: page("sweef://downloads") }), mi("bookmark", S.menu.bookmarks, { on: page("sweef://bookmarks") }), h("div.sep"),
      h("div.zoomrow", icon("search", 18), h("span.grow", { text: S.menu.zoom }), h("button.ibtn.sm", { "aria-label": S.menu.zoomOut, onclick: function () { zoom(-1); } }, icon("minus", 16)), zv, h("button.ibtn.sm", { "aria-label": S.menu.zoomIn, onclick: function () { zoom(1); } }, icon("plus", 16)), h("button.ibtn.sm", { "aria-label": S.menu.fullscreen, onclick: function () { post("window.fullscreen"); closePop(); } }, icon("maxi", 16))),
      mi("search", S.menu.find, { kbd: SW.mod + "+F", on: openFind }), mi("cmd", S.menu.palette, { kbd: SW.mod + "+K", on: function () { SW.palette.open(); } }),
      mi("spark", sbOpen() ? S.f(S.menu.hideSidekick) : S.f(S.menu.sidekick), { kbd: SW.mod + "+Shift+A", on: function () { setSidebar(!sbOpen()); } }), h("div.sep"),
      (st().perm || {}).available ? mi((st().perm || {}).dnd ? "belloff" : "bell", S.perm.dnd, { sub: (st().perm || {}).dnd ? S.perm.dndOn : null, on: function () { post("settings.set", { key: "notifications.dnd", value: !(st().perm || {}).dnd }); } }) : null,
      h("div.moretools", mi("code", S.menu.moreTools, { keep: true, on: function (e) { var b = e.currentTarget, sub = b.nextSibling, open = sub.classList.toggle("hide") === false; b.setAttribute("aria-expanded", open ? "true" : "false"); } }), h("div.moresub.hide", mi("code", S.menu.devtools, { kbd: "F12", on: function () { post("devtools.open", {}); } }))),
      mi("gear", S.menu.settings, { on: page("sweef://settings") }), mi("info", S.f(S.menu.about), { on: page("sweef://settings#about") }), mi("flag", S.menu.reportSite, { on: function () { post("menu.open", { name: "reportSite" }); } }));
    openPop(E.menu, m, { role: "menu" });
  }
  function openDownloads() {
    var dls = st().downloads || [], m = h("div", { style: { width: "340px" } }, h("div", { style: { padding: "6px 12px 8px", fontWeight: "700", fontSize: "13px" }, text: S.dl.title }));
    if (!dls.length) m.appendChild(h("div", { style: { padding: "16px 12px 20px", color: "var(--text-3)" }, text: S.dl.empty }));
    dls.slice(0, 6).forEach(function (d) {
      var prog = d.state === "progress" || d.state === "inprogress", pct = d.total ? Math.min(1, d.received / d.total) : 0;
      var sub = prog ? SW.fmtBytes(d.received) + (d.total ? " " + S.dl.of + " " + SW.fmtBytes(d.total) : "") + (d.speed ? " · " + SW.fmtBytes(d.speed) + "/s" : "") : d.state === "failed" ? S.dl.failed : d.state === "paused" ? S.dl.paused : SW.fmtBytes(d.total || d.received);
      m.appendChild(h("button.dlrow", { style: { width: "100%", textAlign: "left" }, onclick: function () { post("downloads.open", { id: d.id }); closePop(true); } }, h("span.fi", icon("download", 16)), h("div.tx", h("div.nm.trunc", { text: d.name }), h("div.sb", { text: sub }), prog ? h("div.bar", h("i", { style: { width: Math.round(pct * 100) + "%" } })) : null), prog ? h("span.ibtn.sm", { role: "button", "aria-label": S.dl.cancel, onclick: function (e) { e.stopPropagation(); post("downloads.cancel", { id: d.id }); } }, icon("close", 14)) : null));
    });
    m.appendChild(h("div.sep")); m.appendChild(mi("folder", S.dl.all, { on: function () { post("tab.new", { url: "sweef://downloads" }); } }));
    openPop(E.dl, m);
  }

  /* find ------------------------------------------------------------------- */
  var fi = null, fc = null;
  function buildFind() {
    fi = h("input", { type: "text", placeholder: S.find.placeholder, "aria-label": S.find.placeholder, spellcheck: "false" }); fc = h("span.cnt", { "aria-live": "polite" });
    var q = function () { post("find.query", { text: fi.value }); if (!fi.value) { fc.textContent = ""; fc.className = "cnt"; } };
    fi.addEventListener("input", q);
    fi.addEventListener("keydown", function (e) { if (e.key === "Enter") { e.preventDefault(); post("find.step", { dir: e.shiftKey ? -1 : 1 }); } else if (e.key === "Escape") { e.preventDefault(); closeFind(); } });
    E.find.appendChild(icon("search", 16)); E.find.appendChild(fi); E.find.appendChild(fc); E.find.appendChild(h("span.sepv"));
    E.find.appendChild(h("button.ibtn.sm", { "aria-label": S.find.prev, title: S.find.prev, onclick: function () { post("find.step", { dir: -1 }); } }, icon("chevu", 16)));
    E.find.appendChild(h("button.ibtn.sm", { "aria-label": S.find.next, title: S.find.next, onclick: function () { post("find.step", { dir: 1 }); } }, icon("chev", 16)));
    E.find.appendChild(h("button.ibtn.sm", { "aria-label": S.find.close, title: S.find.close, onclick: closeFind }, icon("close", 16)));
  }
  function openFind() { if (!fi) buildFind(); E.find.classList.remove("hide"); fi.focus(); fi.select(); }
  function closeFind() { E.find.classList.add("hide"); post("find.close"); }
  SW.on("find.result", function (p) { if (!fc) return; var n = p.count || 0; fc.textContent = fi && fi.value ? (n ? (p.index || 1) + "/" + n : S.find.none) : ""; fc.className = "cnt" + (fi && fi.value && !n ? " none" : ""); });

  /* sidebar state ---------------------------------------------------------- */
  function sbOpen() { return ui.sb != null ? ui.sb : !!SW.get("sidebar.open", false); }
  function setSidebar(o) { ui.sb = o; SW.set("sidebar.open", o); SW.state.sidebar = SW.state.sidebar || {}; SW.state.sidebar.open = o; ui.sb = null; if (o) SW.sk.focus(); applyLayout(); }
  SW.chrome.setSidebar = setSidebar; SW.chrome.sbOpen = sbOpen;
  function applyLayout() {
    var sb = $("sidebar"), open = sbOpen(), w = SW.clamp(+SW.get("sidebar.width", 380) || 380, 320, 560);
    sb.classList.toggle("hide", !open || !SW.sidekick); if (open && !SW.sidekick) withSk(function () {}); document.documentElement.style.setProperty("--sidebar-w", w + "px");
    E.sb.setAttribute("aria-pressed", open ? "true" : "false"); E.sb.classList.toggle("on", open);
  }
  function reportLayout() {
    var r = E.viewport.getBoundingClientRect(), k = [r.left, r.top, r.width, r.height].map(Math.round).join(",");
    if (k === ui.lastVp) return; ui.lastVp = k; post("layout", { viewport: { x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) } });
  }

  /* commands --------------------------------------------------------------- */
  function run(id) {
    if (!id) return;
    if (id.slice(0, 5) === "page:") return post("tab.new", { url: id.slice(5) });
    if (id.slice(0, 6) === "theme:") return SW.set("theme", id.slice(6));
    switch (id) {
      case "tab.new": return post("tab.new", {}); case "incognito.new": return post("incognito.new");
      case "tab.closeActive": return post("tab.close", { id: st().active }); case "nav.reload": return post("nav.reload");
      case "sidebar.toggle": return setSidebar(!sbOpen()); case "agent.chat.new": setSidebar(true); return SW.sk.newChat();
      case "find.open": return openFind(); case "bookmark.toggle": return E.star.click(); case "palette": return SW.palette.open();
      case "devtools.toggle": return post(st().devtools ? "devtools.close" : "devtools.open", {});
      case "focusOmnibox": return focusOmni(); case "sidebar.focus": setSidebar(true); return SW.sk.focus();
    }
  }
  SW.chrome.run = run;
  SW.on("ui.command", function (p) { run(p && (p.name || p)); });
  document.addEventListener("keydown", function (e) {
    var m = e.ctrlKey || e.metaKey, k = e.key, lk = k.length === 1 ? k.toLowerCase() : k;
    if (m && lk === "k") { e.preventDefault(); run("palette"); }
    else if (m && lk === "l" || k === "F6" || (e.altKey && lk === "d")) { e.preventDefault(); focusOmni(); }
    else if (m && lk === "f") { e.preventDefault(); openFind(); }
    else if (m && e.shiftKey && lk === "a") { e.preventDefault(); run("sidebar.toggle"); }
    else if (m && lk === "t" && !e.shiftKey) { e.preventDefault(); run("tab.new"); }
    else if (m && lk === "w") { e.preventDefault(); run("tab.closeActive"); }
    else if (m && lk === "d") { e.preventDefault(); run("bookmark.toggle"); }
    else if (k === "Escape" && !E.find.classList.contains("hide") && document.activeElement !== E.url) closeFind();
  });
  document.addEventListener("mousedown", function () { if (cur && !cur.node.contains(document.activeElement)) { /* scrim handles outside clicks */ } });

  function renderInc() {
    var on = !!st().incognito, b = $("incb"); b.classList.toggle("hide", !on);
    if (on && !b.firstChild) { b.appendChild(icon("hat", 16)); b.appendChild(h("span", { text: S.incog.badge })); b.setAttribute("title", S.incog.tip); }
  }
  /* wiring ----------------------------------------------------------------- */
  var first = true;
  SW.on("render", function () {
    renderTabs(); renderToolbar(); applyLayout(); renderInc(); renderPerm(); renderDevtools();
    var s = st(), mx = s.window && s.window.maximized ? "1" : ""; if (mx) document.documentElement.setAttribute("data-maximized", "1"); else document.documentElement.removeAttribute("data-maximized");
    var mxb = $("wc").querySelector(".mx"); if (mxb && mxb._m !== !!mx) { mxb._m = !!mx; SW.clear(mxb); mxb.appendChild(icon(mx ? "restore" : "maxi", 14)); mxb.setAttribute("aria-label", mx ? S.win.restore : S.win.max); }
    sizeTabs();
    reportLayout();
    if (first && (s.tabs || []).length) { first = false; }
  });
  SW.ready("chrome");
  window.addEventListener("load", function () { SW.emit("render"); });
})();
