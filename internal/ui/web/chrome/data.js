/* Data popover: ring, this page (measured and estimated kept apart), top sites, level, per-site rule. Loaded on first click of the chip. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, T = S.data, ds = SW.ds, C = function () { return SW.chrome; };
  var node = null, last = "";
  function d() { return SW.state.data || {}; }
  function tight() { return window.innerHeight < 720; }  /* short windows get a shorter popover: no native scrolling in here */
  function priv() { return !!(d().incognito || SW.state.incognito); }  /* private windows never show the sites seen in normal ones */
  function web() { return /^https?:/i.test(SW.state.url || ""); }
  function header(b, free) {
    var used = d().used || 0, tone = SW.dataTone(b), per = ds.period(b.period);
    var left = free ? SW.fmtBytes(used) : SW.fmtBytes(Math.max(0, (b.limitMB || 0) * 1048576 - (b.usedBytes != null ? b.usedBytes : used)));
    var ring = h("div.dring2" + (tone ? "." + tone : ""), SW.dataRing(free ? 0 : Math.max(b.pct || 0, 2), 84, 3.4), h("div.rc", h("b.num", { text: left }), h("span", { text: free ? T.usedWord : T.left })));
    var txt = free
      ? h("div.ht", h("b.num", { text: S.f(T.chipTipFree, { period: per, used: SW.fmtBytes(used) }) }), h("span", { text: T.noLimit }), h("button.lnk", { text: T.setLimit, onclick: function () { go("sweef://settings#data"); } }))
      : h("div.ht", h("b.num", { text: SW.fmtBytes(used) + " " + T.usedWord }), h("span", { text: T.of + " " + ds.limitText(b.limitMB) + " " + per }), h("span.rs", { text: ds.resets(b) }));
    return h("div.dhead", ring, txt);
  }
  function go(url) { post("tab.new", { url: url }); C().closePop(true); }
  function row(label, value, pill, cls, tip, approx) {
    return h("div.drow" + (cls ? "." + cls : ""), { title: tip || "" }, h("span.dl", label, approx ? h("em", { text: " " + T.approx }) : null), h("span.dv.num", { text: value }), pill);
  }
  function pageCard(tab) {
    var u = tab.used || 0, sv = tab.saved || {}, sm = sv.measured || 0, se = sv.estimated || 0, n = tab.skipped || 0;
    var box = h("div.dcard");
    if (!u && !sm && !se && !n) { box.appendChild(h("div.dnone", { text: d().available === false ? T.noMeter : T.nothingHere })); return box; }
    box.appendChild(row(T.usedHere, SW.fmtBytes(u), ds.pill("m", T.usedHereTip), "", T.usedHereTip, tab.approx !== false));
    if (sm > 0) box.appendChild(row(T.savedM, SW.fmtBytes(sm), ds.pill("m"), ""));
    if (n > 0 || se > 0) box.appendChild(row(n === 1 ? T.notLoadedReq1 : n ? S.f(T.notLoadedReq, { n: n }) : T.notLoaded, T.about + " " + SW.fmtBytes(se), ds.pill("e"), "est"));
    return box;
  }
  function actions(tab) {
    var n = tab.canLoad || 0, box = h("div.dacts");
    if (n > 0) box.appendChild(h("button.btn.sm.soft", { onclick: function () { post("data.loadSkipped", {}); } }, icon("image", 15), S.f(T.loadImagesN, { n: n })));
    var done = !!tab.once;
    box.appendChild(h("button.btn.sm", { title: T.loadFullyTip, "aria-disabled": done ? "true" : null, onclick: function () { if (done) return; post("data.loadFully", {}); C().closePop(true); } }, icon("refresh", 15), done ? T.loadFullyDone : T.loadFully));
    if (n > 0 && !tight()) box.appendChild(h("p.dleg", { text: T.tapHint }));
    return box;
  }
  function siteRule(tab) {
    var o = tab.origin || SW.origin(SW.state.url || ""), rule = tab.rule || "", host = ds.host(o);
    var seg = h("div.seg.dseg", { role: "radiogroup", "aria-label": S.f(T.siteRule, { host: host }) });
    [["", T.ruleAuto], ["full", T.ruleFull], ["lite", T.ruleLite]].forEach(function (x) {
      seg.appendChild(h("button", { role: "radio", "aria-checked": rule === x[0] ? "true" : "false", onclick: function () {
        var dd = d(); dd.sites = dd.sites || {}; if (x[0]) dd.sites[o] = x[0]; else delete dd.sites[o]; if (dd.tab) dd.tab.rule = x[0];
        post("data.siteRule", { origin: o, rule: x[0] }); fill();
      } }, x[1]));
    });
    return h("div.dsite", { title: T.ruleSub[rule] || T.ruleSub[""] }, h("b.trunc", { text: S.f(T.siteRule, { host: host }) }), seg);
  }
  function levelSeg() {
    var lvl = d().level || "off", seg = h("div.seg.dseg", { role: "radiogroup", "aria-label": T.mode });
    [["off", T.lvl.off], ["standard", T.lvl.standard], ["strict", T.lvl.strict]].forEach(function (x) {
      seg.appendChild(h("button", { role: "radio", "aria-checked": lvl === x[0] ? "true" : "false", onclick: function () { d().level = x[0]; post("data.setLevel", { level: x[0] }); SW.emit("render"); fill(); } }, x[1]));
    });
    return h("div.dmode", h("span", { text: T.mode }), seg);
  }
  function tops() {
    var list = d().top || [], box = h("div.dtop", h("div.dth", h("b", { text: S.f(T.top, { period: ds.period((d().budget || {}).period) }) }), h("span", { text: T.topNote })));
    if (!list.length) { box.appendChild(h("div.dnone", { text: T.topNone })); return box; }
    var max = list[0].bytes || 1; list.slice(0, tight() ? 3 : 5).forEach(function (o) { box.appendChild(ds.siteRow(o.origin, o.bytes, max)); });
    return box;
  }
  function fill() {
    if (!node || !node.isConnected) return;
    var dd = d(), b = dd.budget || {}, free = !b.enabled, tab = dd.tab || {}, k = JSON.stringify([dd.used, dd.level, b, tab, dd.top, dd.sites]);
    if (k === last) return; last = k; SW.clear(node);
    node.appendChild(header(b, free)); node.appendChild(levelSeg());
    if (web()) { node.appendChild(h("div.dsec", h("b", { text: T.thisPage }), h("span", { text: ds.host(SW.origin(SW.state.url || "")) }))); node.appendChild(pageCard(tab)); node.appendChild(h("p.dleg", { text: T.legend })); node.appendChild(actions(tab)); node.appendChild(siteRule(tab)); }
    if (!priv()) node.appendChild(tops());
    if (priv()) node.appendChild(h("p.dleg", { text: S.f(T.incogNote) }));
    node.appendChild(h("div.sep")); node.appendChild(C().mi("gear", T.settings, { on: function () { post("tab.new", { url: "sweef://settings#data" }); } }));
  }
  SW.dataPop = {
    open: function (anchor) {
      node = h("div.dpop", { tabindex: "-1", "aria-label": T.name }); last = ""; C().openPop(anchor, node, { align: "right", noFocus: true });
      fill(); if (node.focus) node.focus();
    }
  };
  SW.on("render", function () { if (node && node.isConnected) fill(); });
})();
