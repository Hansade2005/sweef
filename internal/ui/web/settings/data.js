/* Settings -> Data. Level cards that say plainly what each level does and does not do, the budget editor, honest usage, per-site rules. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, T = S.data, TS = T.set;
  var confirmReset = false;
  function ds() { return SW.ds; }
  function d() { return SW.state.data || {}; }
  function bset(patch) {  // optimistic: show the change now, Go confirms with state.patch
    var dd = d(), b = dd.budget = dd.budget || {}; Object.keys(patch).forEach(function (k) { b[k] = patch[k]; });
    if (patch.limitMB != null || patch.enabled != null) { b.pct = b.enabled ? Math.min(999, 100 * (b.usedBytes || dd.used || 0) / (b.limitMB * 1048576)) : 0; b.exhausted = !!(b.enabled && b.pct >= 100); }
    post("data.setBudget", patch); SW.render();
  }
  function lvlSet(l) { d().level = l; post("data.setLevel", { level: l }); SW.render(); }
  function list(items, cls, ic) { return h("ul." + cls, items.map(function (t) { return h("li", icon(ic, 14), h("span", { text: t })); })); }
  function levels() {
    var cur = d().level || "off", box = h("div.lvls", { role: "radiogroup", "aria-label": TS.gLevel });
    [["off", TS.levelOff, TS.levelOffTag, TS.offDo, TS.offDont], ["standard", TS.levelStd, TS.levelStdTag, TS.stdDo, TS.stdDont], ["strict", TS.levelStrict, TS.levelStrictTag, TS.strictDo, TS.strictDont]].forEach(function (x) {
      var on = cur === x[0];
      box.appendChild(h("button.lvl", { role: "radio", "aria-checked": on ? "true" : "false", onclick: function () { lvlSet(x[0]); } },
        h("div.lh", h("span.rd"), h("div", h("b", { text: x[1] }), h("span", { text: x[2] }))), x[0] === "standard" ? h("span.rec", { text: TS.recommended }) : null,
        h("div.lk", { text: TS.does }), list(x[3], "yes", "check"), h("div.lk", { text: TS.doesnt }), list(x[4], "no", "minus")));
    });
    return h("div.group.dgroup", h("div.gh", { text: TS.gLevel }), box);
  }
  function budget(H) {
    var b = d().budget || {}, on = !!b.enabled, per = b.period || "month";
    var chips = h("div.presets", { role: "radiogroup", "aria-label": TS.gBudget });
    TS.presets.forEach(function (p) {
      var mb = +p[0], act = mb === 0 ? !on : on && b.limitMB === mb;
      chips.appendChild(h("button.pchip", { role: "radio", "aria-checked": act ? "true" : "false", onclick: function () { if (mb === 0) bset({ enabled: false }); else bset({ enabled: true, limitMB: mb }); } }, p[1]));
    });
    var custom = h("input.field.sm", { type: "number", min: "1", max: "1048576", value: on ? String(b.limitMB || "") : "", placeholder: "MB", "aria-label": TS.custom, onchange: function (e) { var n = Math.round(+e.target.value); if (n >= 1) bset({ enabled: true, limitMB: n }); } });
    var rows = [h("div.srow.col", h("div.st", h("div.d", { text: S.f(TS.budgetD) })), chips),
      H.row(TS.custom, TS.customD, custom),
      H.row(TS.period, TS.periodD, segBtns([["day", TS.day], ["week", TS.week], ["month", TS.month]], per, function (v) { bset({ period: v, resetDay: v === "week" ? 1 : 1 }); }))];
    if (per === "week") rows.push(H.row(TS.resetDayWeek, "", sel(TS.weekdays.map(function (n, i) { return [String(i), n]; }), String(b.resetDay != null ? b.resetDay : 1), function (v) { bset({ resetDay: +v }); })));
    if (per === "month") rows.push(H.row(TS.resetDayMonth, "", sel(Array.apply(null, Array(28)).map(function (_, i) { return [String(i + 1), String(i + 1)]; }), String(b.resetDay || 1), function (v) { bset({ resetDay: +v }); })));
    var w = b.warnAt && b.warnAt.length ? b.warnAt : [70, 90];
    rows.push(H.row(TS.warn, S.f(TS.warnD, { a: w[0], b: w[1] != null ? w[1] : 90 }), null));
    var hs = h("button.switch", { role: "switch", "aria-checked": b.hardStop ? "true" : "false", "aria-disabled": on ? null : "true", onclick: function () { if (!on) return; bset({ hardStop: !b.hardStop }); } });
    rows.push(H.row(TS.hardStop, TS.hardStopD, hs));
    return h("div.group", h("div.gh", { text: TS.gBudget }), rows);
  }
  function segBtns(opts, cur, on) {
    var el = h("div.seg", { role: "radiogroup" });
    opts.forEach(function (o) { el.appendChild(h("button", { role: "radio", "aria-checked": cur === o[0] ? "true" : "false", onclick: function () { on(o[0]); } }, o[1])); });
    return el;
  }
  function sel(opts, cur, on) {
    var s = h("select.field", { onchange: function () { on(s.value); } }, opts.map(function (o) { return h("option", { value: o[0], text: o[1] }); })); s.value = cur; return s;
  }
  function tile(label, value, pill, sub) { return h("div.tile", h("div.tt", h("span", { text: label }), pill), h("b.num", { text: value }), h("div.d", { text: sub })); }
  function usage() {
    var dd = d(), b = dd.budget || {}, sv = dd.saved || {}, per = SW.ds.period(b.period);
    var tiles = h("div.tiles", tile(TS.usedL, SW.fmtBytes(dd.used || 0), SW.ds.pill("m", TS.usedLD), TS.usedLD), tile(TS.savedL, SW.fmtBytes(sv.measured || 0), SW.ds.pill("m"), TS.savedLD), tile(TS.estL, T.about + " " + SW.fmtBytes(sv.estimated || 0), SW.ds.pill("e"), TS.estLD));
    var top = h("div.dtop2"), tl = dd.top || [];
    if (tl.length) { top.appendChild(h("div.dth", h("b", { text: TS.top }), h("span", { text: TS.topD }))); var max = tl[0].bytes || 1; tl.forEach(function (o) { top.appendChild(SW.ds.siteRow(o.origin, o.bytes, max)); }); }
    var reset = confirmReset
      ? h("div.ctl2", h("span", { text: TS.resetSure }), h("button.btn.sm.danger", { text: TS.resetYes, onclick: function () { confirmReset = false; post("data.reset", {}); SW.render(); } }), h("button.btn.sm", { text: TS.cancel, onclick: function () { confirmReset = false; SW.render(); } }))
      : h("button.btn.sm", { text: TS.reset, onclick: function () { confirmReset = true; SW.render(); } });
    return h("div.group", h("div.gh", { text: S.f(TS.gUse, { period: per }) }), h("div.tilewrap", tiles, top), h("div.srow", h("div.st", h("b", { text: TS.reset }), h("div.d", { text: TS.resetD })), h("div.ctl", reset)));
  }
  function sites(H) {
    var m = d().sites || {}, keys = Object.keys(m).sort(), rows = [];
    keys.forEach(function (o) {
      rows.push(H.row(SW.host(o), m[o] === "full" ? TS.ruleFull : TS.ruleLite, [segBtns([["full", TS.ruleFull], ["lite", TS.ruleLite]], m[o], function (v) { d().sites[o] = v; post("data.siteRule", { origin: o, rule: v }); SW.render(); }), H.btn(TS.forget, function () { delete d().sites[o]; post("data.siteRule", { origin: o, rule: "" }); SW.render(); })]));
    });
    if (!rows.length) rows.push(h("div.srow", h("div.st", h("div.d", { text: TS.sitesNone }))));
    return h("div.group", h("div.gh", { text: TS.gSites }), rows);
  }
  function storage(H) {
    var st = d().storage || {}, rows = [];
    (st.sites || []).forEach(function (x) {
      rows.push(H.row(SW.host(x.origin), SW.fmtBytes(x.bytes) + (x.persisted ? " · " + TS.kept : ""), H.btn(TS.clearSite, function () { x.bytes = 0; post("data.storageClear", { origin: x.origin }); SW.render(); })));
    });
    if (!rows.length) rows.push(h("div.srow", h("div.st", h("div.d", { text: TS.storageNone }))));
    return h("div.group", h("div.gh", { text: TS.gStorage }), h("div.srow", h("div.st", h("div.d", { text: S.f(TS.storageD, { used: SW.fmtBytes(st.used || 0), budget: SW.fmtBytes(st.budget || 0) }) }))), rows);
  }
  SW.dataSet = function (H) {
    var out = [h("h2", { text: TS.nav }), h("p.lead", { text: S.f(TS.lead) })];
    if (d().available === false) out.push(h("div.dnote", icon("info", 16), h("span", { text: TS.unavailable })));
    out.push(levels(), budget(H), usage(), h("div.group", h("div.gh", { text: TS.gMore }), H.row(S.f(TS.agentLite), TS.agentLiteD, H.toggle("data.agentLite", true)), H.row(TS.showMeter, TS.showMeterD, H.toggle("data.showMeter", true))), sites(H), storage(H),
      h("div.dnote.soft", icon("info", 16), h("span", { text: S.f(TS.countD) })));
    return out;
  };
})();
