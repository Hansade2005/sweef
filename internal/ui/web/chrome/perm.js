/* Permission prompt: a site asks to show notifications. A card under the site button, not a modal: it does not take the
   focus by itself (the user may be typing), the page stays usable, Escape or "Not now" puts the question off, and the
   chrome command perm.focus (Alt+Shift+P) moves the focus to it. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, P = S.perm, node = null, shown = "";
  function cur() { return (SW.state.perm || {}).prompt || null; }
  function close() { if (node && node.parentNode) node.parentNode.removeChild(node); node = null; shown = ""; }
  function answer(a) {
    var p = cur(); if (!p) return;
    post("perm.answer", { id: p.id, answer: a }); SW.state.perm.prompt = null; close();
    var f = document.getElementById("url"); if (f && f.focus && a === "dismiss") f.focus();
  }
  function place() {
    if (!node) return;
    var r = document.getElementById("secbtn").getBoundingClientRect(), W = window.innerWidth, w = node.offsetWidth;
    node.style.left = Math.round(SW.clamp(r.left, 8, W - w - 8)) + "px"; node.style.top = Math.round(r.bottom + 8) + "px";
  }
  function build(p) {
    var allow = h("button.btn.sm.primary", { text: P.allow, onclick: function () { answer("allow"); } });
    var n = h("div.pop.permpop", { role: "alertdialog", "aria-labelledby": "permt", "aria-describedby": "permd", tabindex: "-1" },
      h("div.pp-h", h("span.pp-i", icon("bell", 20)), h("div", h("b", { id: "permt", text: S.f(P.title, { host: p.host }) }), h("span", { text: P.notif }))),
      h("p.pp-n", { id: "permd", text: P.note }),
      h("div.pp-b", allow, h("button.btn.sm", { text: P.block, onclick: function () { answer("block"); } }), h("button.btn.sm.ghost", { text: P.later, onclick: function () { answer("dismiss"); } })));
    n.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); answer("dismiss"); }
      else if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
        var b = SW.$$("button", n), i = b.indexOf(document.activeElement); if (i < 0) return; e.preventDefault();
        b[(i + (e.key === "ArrowRight" ? 1 : b.length - 1)) % b.length].focus();
      }
    });
    return n;
  }
  SW.permPop = {
    sync: function () {
      var p = cur(); if (!p) { close(); return; }
      if (shown === p.id && node && node.isConnected) { place(); return; }
      close(); shown = p.id; node = build(p); node.style.visibility = "hidden"; document.getElementById("layer").appendChild(node); place(); node.style.visibility = "";
      node.setAttribute("aria-label", S.f(P.title, { host: p.host }) + " " + P.notif);
    },
    focus: function () { if (node) { var b = node.querySelector(".btn"); if (b) b.focus(); } }
  };
  SW.on("ui.command", function (c) { if (c && c.name === "perm.focus") SW.permPop.focus(); });
  document.addEventListener("keydown", function (e) { if (e.altKey && e.shiftKey && !e.ctrlKey && (e.key === "P" || e.key === "p") && node) { e.preventDefault(); SW.permPop.focus(); } });
  window.addEventListener("resize", place);
})();
