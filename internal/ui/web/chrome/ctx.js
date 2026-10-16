/* Context menu component. One menu at a time, drawn in #layer so it can cover the page viewport.
   SW.ctx.show({x, y, items, token?}):  items are {label, kbd, sep, disabled, danger, checked, sub:[...], on: fn}.
   Menus from Go (ctx.menu) carry ids instead of functions: choosing one posts ctx.pick {token, id}.
   Keyboard: arrows, Home/End, Enter/Space, Right/Left for submenus, Esc. Closes on outside click, window blur, scroll, resize. */
(function () {
  var SW = window.SW, h = SW.h, post = SW.post, cur = null;

  function enabled(it) { return !it.sep && !it.disabled; }
  function close(quiet) {
    if (!cur) return; var c = cur; cur = null;
    document.removeEventListener("keydown", c.onKey, true);
    c.levels.forEach(function (l) { if (l.el.parentNode) l.el.parentNode.removeChild(l.el); });
    if (c.scrim.parentNode) c.scrim.parentNode.removeChild(c.scrim);
    if (!quiet) post("ctx.state", { open: false, token: c.token });
  }
  function place(el, x, y, parentRect) {
    var W = window.innerWidth, H = window.innerHeight, w = el.offsetWidth, hh = el.offsetHeight, L = x, T = y;
    if (parentRect) { L = parentRect.right - 4; if (L + w > W - 4) L = parentRect.left - w + 4; T = parentRect.top - 4; }
    else if (L + w > W - 4) L = x - w;
    if (T + hh > H - 4) T = parentRect ? H - hh - 4 : y - hh;
    el.style.left = Math.round(SW.clamp(L, 4, Math.max(4, W - w - 4))) + "px";
    el.style.top = Math.round(SW.clamp(T, 4, Math.max(4, H - hh - 4))) + "px";
    el.style.visibility = "";
  }
  function highlight(lv, i) {
    lv.idx = i;
    lv.rows.forEach(function (r, k) { r.el && r.el.classList.toggle("hl", k === i); });
    if (i >= 0) lv.el.setAttribute("aria-activedescendant", lv.rows[i].el.id);
  }
  function step(lv, d) {
    var n = lv.rows.length, i = lv.idx;
    for (var k = 0; k < n; k++) { i = (i + d + n) % n; if (i < 0) i += n; if (enabled(lv.rows[i].it)) return highlight(lv, i); }
  }
  function openSub(c, lv, i) {
    var row = lv.rows[i], it = row.it; closeFrom(c, c.levels.indexOf(lv) + 1);
    if (!it.sub || !it.sub.length) return null;
    var nl = build(c, it.sub); nl.parent = lv;
    place(nl.el, 0, 0, row.el.getBoundingClientRect());
    return nl;
  }
  function closeFrom(c, n) {
    while (c.levels.length > n) { var l = c.levels.pop(); if (l.el.parentNode) l.el.parentNode.removeChild(l.el); }
  }
  function choose(c, it) {
    if (!enabled(it) || (it.sub && it.sub.length)) return;
    var tok = c.token; close();
    if (it.on) it.on(); else if (it.id != null) post("ctx.pick", { token: tok, id: it.id });
  }
  var uid = 0;
  function build(c, items) {
    var el = h("div.ctxm", { role: "menu", tabindex: "-1" }), lv = { el: el, rows: [], idx: -1 };
    el.style.visibility = "hidden"; el.style.left = "0px"; el.style.top = "0px";
    items.forEach(function (it) {
      if (it.sep) { el.appendChild(h("div.ctxsep", { role: "separator" })); lv.rows.push({ it: it }); return; }
      var i = lv.rows.length, id = "ctxi" + (++uid);
      var row = h("div.ctxi" + (it.disabled ? ".dis" : "") + (it.danger ? ".danger" : ""), { id: id, role: it.checked != null && it.checked !== false ? "menuitemcheckbox" : "menuitem", "aria-disabled": it.disabled ? "true" : null, "aria-checked": it.checked ? "true" : null, "aria-haspopup": it.sub && it.sub.length ? "menu" : null },
        h("span.ck", { text: it.checked ? "✓" : "" }), h("span.lb", { text: it.label || "" }), it.kbd ? h("span.kb", { text: it.kbd }) : null, it.sub && it.sub.length ? h("span.ar", { text: "›" }) : null);
      row.addEventListener("mousedown", function (e) { e.preventDefault(); });
      row.addEventListener("mouseover", function () {
        if (!enabled(it)) return; highlight(lv, i);
        if (it.sub && it.sub.length) { if (c.levels[c.levels.indexOf(lv) + 1] === undefined || c.levels[c.levels.indexOf(lv) + 1].src !== it) { var nl = openSub(c, lv, i); if (nl) nl.src = it; } }
        else closeFrom(c, c.levels.indexOf(lv) + 1);
      });
      row.addEventListener("click", function (e) { e.preventDefault(); e.stopPropagation(); if (it.sub && it.sub.length) { var nl = openSub(c, lv, i); if (nl) { nl.src = it; step(nl, 1); } } else choose(c, it); });
      el.appendChild(row); lv.rows.push({ it: it, el: row });
    });
    el.addEventListener("contextmenu", function (e) { e.preventDefault(); });
    document.getElementById("layer").appendChild(el); c.levels.push(lv);
    return lv;
  }
  function onKey(c) {
    return function (e) {
      var k = e.key, lv = c.levels[c.levels.length - 1];
      if (k === "Shift" || k === "Control" || k === "Alt" || k === "Meta") return;
      e.preventDefault(); e.stopPropagation();
      if (k === "Escape") { if (c.levels.length > 1) closeFrom(c, c.levels.length - 1); else close(); return; }
      if (k === "Tab") { close(); return; }
      if (k === "ArrowDown") step(lv, 1); else if (k === "ArrowUp") { if (lv.idx < 0) { lv.idx = 0; } step(lv, -1); }
      else if (k === "Home") { lv.idx = -1; step(lv, 1); } else if (k === "End") { lv.idx = 0; step(lv, -1); }
      else if (k === "ArrowRight") {
        var r = lv.rows[lv.idx]; if (r && r.it.sub && r.it.sub.length && enabled(r.it)) { var nl = openSub(c, lv, lv.idx); if (nl) { nl.src = r.it; step(nl, 1); } }
      } else if (k === "ArrowLeft") { if (c.levels.length > 1) closeFrom(c, c.levels.length - 1); }
      else if (k === "Enter" || k === " ") {
        var rr = lv.rows[lv.idx]; if (!rr || !rr.it) return;
        if (rr.it.sub && rr.it.sub.length) { var n2 = openSub(c, lv, lv.idx); if (n2) { n2.src = rr.it; step(n2, 1); } } else choose(c, rr.it);
      }
    };
  }
  function show(spec) {
    close(true);
    var items = (spec.items || []).filter(function (x) { return x; });
    if (!items.length) return;
    var c = cur = { token: spec.token || 0, levels: [], scrim: null };
    c.scrim = h("div.ctxscrim", {
      onmousedown: function () { close(); },
      oncontextmenu: function (e) { e.preventDefault(); var x = e.clientX, y = e.clientY; close(); post("ctx.rightclick", { x: x, y: y }); }
    });
    document.getElementById("layer").appendChild(c.scrim);
    var lv = build(c, items);
    place(lv.el, +spec.x || 0, +spec.y || 0);
    c.onKey = onKey(c); document.addEventListener("keydown", c.onKey, true);
    post("ctx.state", { open: true, token: c.token });
  }
  window.addEventListener("blur", function () { close(); });
  window.addEventListener("resize", function () { close(); });
  SW.ctxLoaded = true;
  SW.ctx = { show: show, close: close, isOpen: function () { return !!cur; } };
})();
