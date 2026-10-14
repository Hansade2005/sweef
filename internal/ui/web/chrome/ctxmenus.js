/* Context menus of the chrome's own elements: tabs, the empty tab strip, the omnibox, the bookmarks bar.
   The Sidekick builds its own in sidebar.js. Web pages and internal pages are handled in Go (ctxmenu.go). */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, post = SW.post, T = S.ctx, C = function () { return SW.chrome; };
  var $ = function (id) { return document.getElementById(id); };
  var st = function () { return SW.state; };
  var K = function (k) { return SW.mod + "+" + k; };
  function tabs() { return st().tabs || []; }
  function find(id) { return tabs().filter(function (t) { return t.id === id; })[0] || {}; }
  function hostOf(u) { return /^https?:/i.test(u || "") ? SW.host(u) : ""; }

  /* the edit group shared by the omnibox and the Sidekick input; the clipboard work happens in Go (edit.op) */
  SW.ctxEditItems = function (el, extra) {
    var es = (window.__sweefEditState ? window.__sweefEditState(el) : {}) || {}, ro = !!el.readOnly || !!el.disabled, op = function (o) { return function () { post("edit.op", { op: o }); }; };
    var items = [
      { label: T.undo, kbd: K("Z"), disabled: ro || !es.canUndo, on: op("undo") },
      { label: T.redo, kbd: K("Shift+Z"), disabled: ro || !es.canRedo, on: op("redo") }, { sep: true },
      { label: T.cut, kbd: K("X"), disabled: ro || !es.hasSel, on: op("cut") },
      { label: T.copy, kbd: K("C"), disabled: !es.hasSel, on: op("copy") },
      { label: T.paste, kbd: K("V"), disabled: ro, on: op("paste") }
    ];
    if (extra) items = items.concat(extra);
    items.push({ label: T.del, disabled: ro || !es.hasSel, on: op("delete") }, { sep: true }, { label: T.selectAll, kbd: K("A"), disabled: !!es.empty, on: op("selectall") });
    return items;
  };

  /* inputs without a menu of their own (find bar, palette, ...) */
  SW.ctxInput = function (t, e) { SW.ctx.show({ x: e.clientX, y: e.clientY, items: SW.ctxEditItems(t) }); };

  /* tabs ------------------------------------------------------------------ */
  $("tabs").addEventListener("contextmenu", function (e) {
    var t = e.target.closest ? e.target.closest(".tab") : null; if (!t) return; e.preventDefault();
    var id = t.getAttribute("data-id"), tab = find(id), all = tabs(), i = all.findIndex(function (x) { return x.id === id; });
    var site = hostOf(tab.url), siteMuted = tab.muted;
    var unpinnedOthers = all.some(function (x) { return x.id !== id && !x.pinned; }), unpinnedRight = all.slice(i + 1).some(function (x) { return !x.pinned; });
    SW.ctx.show({ x: e.clientX, y: e.clientY, items: [
      { label: T.newRight, kbd: K("T"), on: function () { post("tab.new", { url: "", after: id }); } }, { sep: true },
      { label: S.tab.reload, kbd: K("R"), on: function () { post("tab.activate", { id: id }); post("nav.reload", { tab: id }); } },
      { label: S.tab.duplicate, on: function () { post("tab.duplicate", { id: id }); } },
      { label: tab.pinned ? S.tab.unpin : S.tab.pin, on: function () { post("tab.pin", { id: id }); } },
      { label: siteMuted ? T.unmuteSite : T.muteSite, disabled: !site && !tab.audible && !tab.muted, on: function () { post("tab.muteSite", { id: id }); } }, { sep: true },
      { label: S.tab.close, kbd: K("W"), on: function () { post("tab.close", { id: id }); } },
      { label: S.tab.closeOthers, disabled: !unpinnedOthers, on: function () { post("tab.closeOthers", { id: id }); } },
      { label: S.tab.closeRight, disabled: !unpinnedRight, on: function () { post("tab.closeRight", { id: id }); } }, { sep: true },
      { label: T.reopen, kbd: K("Shift+T"), disabled: !(st().closedTabs > 0), on: function () { post("tab.reopen"); } }
    ] });
  });

  /* empty part of the tab strip */
  $("strip").addEventListener("contextmenu", function (e) {
    if (e.defaultPrevented) return; var t = e.target;
    if (t.closest && t.closest(".tab,button,input,#omni")) return;
    e.preventDefault();
    SW.ctx.show({ x: e.clientX, y: e.clientY, items: [
      { label: S.tab.new, kbd: K("T"), on: function () { post("tab.new", {}); } },
      { label: T.reopen, kbd: K("Shift+T"), disabled: !(st().closedTabs > 0), on: function () { post("tab.reopen"); } }
    ] });
  });

  /* omnibox */
  $("omni").addEventListener("contextmenu", function (e) {
    e.preventDefault(); var u = $("url");
    if (document.activeElement !== u) { u.focus(); try { u.select(); } catch (x) {} }
    SW.ctx.show({ x: e.clientX, y: e.clientY, items: SW.ctxEditItems(u, [{ label: T.pasteGo, on: function () { post("omni.pasteAndGo"); } }]) });
  });

  /* bookmarks bar */
  function editBookmark(b, at) {
    var name = h("input.field", { value: b.title || "", "aria-label": T.name }), url = h("input.field", { value: b.url || "", "aria-label": T.url });
    var go = function () { post("bookmark.update", { id: b.id, title: name.value, url: url.value }); C().closePop(true); };
    [name, url].forEach(function (i) { i.addEventListener("keydown", function (ev) { if (ev.key === "Enter") go(); }); });
    var box = h("div", { style: { width: "300px", display: "flex", flexDirection: "column", gap: "8px", padding: "8px" } },
      h("label", { text: T.name }), name, h("label", { text: T.url }), url,
      h("div", { style: { display: "flex", gap: "8px", justifyContent: "flex-end" } }, h("button.btn.sm", { text: T.cancel, onclick: function () { C().closePop(true); } }), h("button.btn.sm.primary", { text: T.save, onclick: go })));
    C().openPop(null, box, { at: at, noFocus: true }); name.focus();
  }
  $("bmbar").addEventListener("contextmenu", function (e) {
    var el = e.target.closest ? e.target.closest("button[data-id]") : null; if (!el) return; e.preventDefault();
    var id = el.getAttribute("data-id"), b = (st().bookmarksBar || []).filter(function (x) { return x.id === id; })[0]; if (!b) return;
    var at = { x: e.clientX, y: e.clientY };
    SW.ctx.show({ x: e.clientX, y: e.clientY, items: [
      { label: T.open, on: function () { post("nav.go", { tab: st().active, input: b.url }); } },
      { label: T.openNew, on: function () { post("tab.new", { url: b.url }); } }, { sep: true },
      { label: T.edit, on: function () { editBookmark(b, at); } },
      { label: T.del2, danger: true, on: function () { post("bookmark.remove", { id: b.id }); } }
    ] });
  });
})();
