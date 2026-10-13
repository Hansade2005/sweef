/* sweef://datastop?url=...: shown instead of a page when the budget is used up and the hard stop is on. Calm, three clear choices. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, T = S.data, TS = T.stop, $ = function (id) { return document.getElementById(id); };
  var inj = window.__sweefData || {}, target = inj.url || SW.query("url"), raising = false, pick = null;
  function info() {  // what the app injected, else the latest state (previews and tests)
    var b = (SW.state.data || {}).budget || {};
    return { limitMB: inj.limitMB || b.limitMB || 100, period: inj.period || b.period || "month", resetsInDays: inj.resetsInDays || b.resetsInDays || 1 };
  }
  function limitText(mb) { return mb >= 1024 && mb % 1024 === 0 ? (mb / 1024) + " GB" : mb + " MB"; }
  function raisePanel(i) {
    var opts = [250, 500, 1024, 2048, 5120].filter(function (m) { return m > i.limitMB; }).slice(0, 3).concat([0]);
    if (pick == null) pick = opts[0];
    var row = h("div.presets", { role: "radiogroup", "aria-label": TS.raiseTitle });
    opts.forEach(function (m) { row.appendChild(h("button.pchip", { role: "radio", "aria-checked": pick === m ? "true" : "false", onclick: function () { pick = m; render(); } }, m === 0 ? TS.noLimit : limitText(m))); });
    return h("div.raise", h("b", { text: TS.raiseTitle }), row, h("button.btn.primary", { text: TS.raiseGo, onclick: function () {
      post("data.setBudget", pick === 0 ? { enabled: false } : { enabled: true, limitMB: pick }); post("nav.reload", {});
    } }));
  }
  function render() {
    var i = info(), per = T.period[i.period] || T.period.month, when = i.resetsInDays <= 1 ? T.nt.tomorrow : S.f(T.nt.inDays, { n: i.resetsInDays }), root = $("ds"); SW.clear(root);
    var card = h("div.dcardp",
      h("div.bigring", SW.dataRing(100, 96, 3), h("span.bi", icon("drop", 30))),
      h("h1", { text: S.f(TS.title, { limit: limitText(i.limitMB), period: per }) }),
      h("p.lead", { text: S.f(TS.sub, { when: when }) }),
      target ? h("div.asked", h("span", { text: TS.host }), SW.favicon(target, "", SW.host(target)), h("b.trunc", { text: SW.host(target) })) : null,
      h("div.acts", h("button.btn.primary.lg", { text: TS.once, onclick: function () { post("data.loadOnce", { url: target }); } }),
        h("button.btn.lg", { text: TS.raise, "aria-expanded": raising ? "true" : "false", onclick: function () { raising = !raising; render(); } }),
        h("button.btn.ghost.lg", { text: TS.wait, onclick: function () { post("data.wait", {}); } })),
      raising ? raisePanel(i) : null,
      h("p.fine", { text: TS.onceNote }));
    root.appendChild(card);
  }
  SW.on("render", function () { if (!inj.limitMB) render(); });
  render(); SW.ready("datastop");
})();
