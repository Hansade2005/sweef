/* sweef://error */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post;
  var kind = SW.query("kind") || "offline", url = SW.query("url"), host = SW.host(url) || SW.query("host") || "this site", detail = SW.query("detail"), E = S.err[kind] || S.err.offline, code = SW.query("code") || E.code;
  document.documentElement.setAttribute("data-kind", kind);
  var ico = { offline: "wifioff", dns: "globe", cert: "shield", crash: "warn", timeout: "clock" }[kind] || "alert", tone = kind === "cert" ? "bad" : kind === "crash" ? "warn" : "";
  document.title = E.title;
  var tips = { offline: ["Check that Wi-Fi or Ethernet is connected.", "Restart your router if other devices are offline too.", "A VPN or proxy can also block the connection."], dns: ["Check the address for typos.", "Try again in a minute. The site may be down.", "If other sites load, the problem is with this one."], timeout: ["The site may be busy. Try again shortly.", "Check that your connection is stable."], crash: ["Reloading usually fixes it.", "If it keeps happening, tell us what site it was."], cert: [] }[kind] || [];
  var main = document.getElementById("err");
  main.appendChild(h("div.art" + (tone ? "." + tone : ""), h("span.st"), h("span.st.b"), icon(ico, 40)));
  main.appendChild(h("h1", { text: E.title }));
  var body = h("p.body"); S.f(E.body, { host: "@@" }).split("@@").forEach(function (p, i, a) { body.appendChild(document.createTextNode(p)); if (i < a.length - 1) body.appendChild(h("b", { text: host })); }); main.appendChild(body);
  if (tips.length) main.appendChild(h("ul.tips", tips.map(function (t) { return h("li", icon("check", 16), t); })));
  var acts = h("div.acts");
  if (kind === "cert") acts.appendChild(h("button.btn.primary.lg", { onclick: function () { post("nav.back"); } }, icon("back", 18), S.err.back));
  else acts.appendChild(h("button.btn.primary.lg", { onclick: function () { post("nav.reload"); } }, icon("reload", 18), S.err.reload));
  if (url) acts.appendChild(h("button.btn.lg", { onclick: function () { post("openExternal", { url: url }); } }, icon("ext", 18), S.err.external));
  acts.appendChild(h("button.btn.lg.soft", { onclick: function () { post("agent.send", { text: S.f(S.err.fixPrompt, { code: code, url: url || host }), mode: "agent", source: "error" }); post("settings.set", { key: "sidebar.open", value: true }); } }, icon("spark", 18), S.f(S.err.fix)));
  main.appendChild(acts);
  if (kind === "cert" && url) main.appendChild(h("button.unsafe", { text: S.err.proceed, onclick: function () { post("cert.proceed", { url: url }); } }));
  var pre = h("pre.hide", [code, detail ? "\n" + S.err.cert.details + ": " + detail : "", url ? "\nURL: " + url : ""].join("")), tg = h("button", { "aria-expanded": "false", onclick: function () { var o = pre.classList.toggle("hide"); tg.setAttribute("aria-expanded", o ? "false" : "true"); } }, icon("chevr", 14), S.err.details);
  main.appendChild(h("div.det", tg, pre));
  if (SW.query("open") === "1") { pre.classList.remove("hide"); tg.setAttribute("aria-expanded", "true"); }
  SW.ready("error");
})();
