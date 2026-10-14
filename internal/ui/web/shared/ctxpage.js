/* Context menus of internal pages (loaded on first right click). The menu is drawn by the chrome, so it may extend over
   the whole window; the page keeps the actions and the chrome sends the choice back (ctx.pick). */
(function () {
  var SW = window.SW, ctxActs = {}, ctxSeq = 0;
  function wire(list) {
    return list.map(function (it) {
      if (it.sep) return { sep: true };
      var o = { id: "c" + (++ctxSeq), label: it.label, kbd: it.kbd, danger: !!it.danger, disabled: !!it.disabled || (!it.on && !it.sub), checked: !!it.checked };
      if (it.on) ctxActs[o.id] = it.on; if (it.sub) o.sub = wire(it.sub); return o;
    });
  }
  SW.ctxMenu = function (e, items) { if (e && e.preventDefault) e.preventDefault(); ctxActs = {}; window.sweef.post("ctx.show", { items: wire(items) }); };
  SW.on("ctx.pick", function (p) { var f = p && ctxActs[p.id]; ctxActs = {}; if (f) f(); });
  SW.ctxInput = function (t) {
    var ro = !!t.readOnly || !!t.disabled, has = t.selectionStart !== t.selectionEnd, op = function (o) { return function () { SW.post("edit.op", { op: o }); }; }, k = SW.mod + "+";
    var es = (window.__sweefEditState ? window.__sweefEditState(t) : {}) || {};
    SW.ctxMenu(null, [{ label: "Undo", kbd: k + "Z", disabled: ro || !es.canUndo, on: op("undo") }, { label: "Redo", kbd: k + "Shift+Z", disabled: ro || !es.canRedo, on: op("redo") }, { sep: true },
      { label: "Cut", kbd: k + "X", disabled: ro || !has, on: op("cut") }, { label: "Copy", kbd: k + "C", disabled: !has, on: op("copy") }, { label: "Paste", kbd: k + "V", disabled: ro, on: op("paste") },
      { label: "Delete", disabled: ro || !has, on: op("delete") }, { sep: true }, { label: "Select all", kbd: k + "A", disabled: !t.value, on: op("selectall") }]);
  };
})();
