/* Sweef Sidekick: the agent side panel. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, C = function () { return SW.chrome; };
  var $ = function (id) { return document.getElementById(id); };
  var E = { side: $("sidebar"), box: $("skmsgs"), msgs: $("skfeed"), input: $("skin"), send: $("sksend"), mode: $("skmode"), chips: $("skchips"), lat: $("sklat"), run: $("skrun"), chat: $("skchat"), rz: $("rz"), plus: $("skplus"), src: $("sksrc"), mic: $("skmic"), comp: $("comp") };
  var chat = { id: "", title: "" }, runs = {}, curRun = null, atts = [], stick = true, empty = null, lastUser = "", lastLat = null, busy = false;
  var st = function () { return SW.state; };
  function activeTab() { var s = st(), a = s.tabs || []; for (var i = 0; i < a.length; i++) if (a[i].id === s.active) return a[i]; return a[0] || {}; }
  function mode() { return SW.setting("agent.mode", "ask") === "agent" ? "agent" : "ask"; }

  /* header + composer chrome ---------------------------------------------- */
  function setLabel(el, t) { el.setAttribute("aria-label", t); el.setAttribute("title", t); }
  function buildStatic() {
    cK = "";
    SW.clear(E.chat); E.chat.appendChild(h("span.trunc", { text: chat.title || S.f(S.agent.title) })); E.chat.appendChild(icon("chev", 14)); setLabel(E.chat, S.agent.chats);
    var nb = $("sknew"), cb = $("skclose"); SW.clear(nb); nb.appendChild(icon("edit", 16)); setLabel(nb, S.agent.new); SW.clear(cb); cb.appendChild(icon("close", 16)); setLabel(cb, S.f(S.agent.close));
    SW.clear(E.plus); E.plus.appendChild(icon("plus", 18)); setLabel(E.plus, S.agent.attach);
    SW.clear(E.mic); E.mic.appendChild(icon("mic", 16)); setLabel(E.mic, S.agent.voice);
    SW.clear(E.src); E.src.appendChild(icon("globe", 15)); E.src.appendChild(h("span", { text: S.agent.sources })); E.src.appendChild(icon("chev", 12));
    SW.clear(E.mode);
    [["ask", S.agent.ask, S.agent.askTip, ""], ["agent", S.agent.agent, S.agent.agentTip, "ag"]].forEach(function (m) {
      var b = h("button" + (m[3] ? "." + m[3] : ""), { role: "radio", title: m[2], onclick: function () { SW.set("agent.mode", m[0]); renderComposer(); } }, icon(m[0] === "ask" ? "search" : "bolt", 13), m[1]); b.setAttribute("data-m", m[0]); E.mode.appendChild(b);
    });
    SW.clear(E.rz); setLabel(E.send, S.agent.send);
  }
  /* Runs on every key stroke of the composer: only what depends on the text or the mode is touched, and only when it changed. */
  var cK = "";
  function renderComposer() {
    var m = mode(), has = !!E.input.value.trim(), k = m + "|" + busy + "|" + has;
    if (k === cK) return; cK = k;
    SW.$$("button", E.mode).forEach(function (b) { b.setAttribute("aria-checked", b.getAttribute("data-m") === m ? "true" : "false"); });
    E.input.setAttribute("placeholder", m === "agent" ? S.agent.placeholderAgent : S.agent.placeholder);
    SW.clear(E.send);
    E.send.classList.toggle("stop", busy); E.send.classList.toggle("off", !busy && !has);
    E.send.appendChild(icon(busy ? "stop" : "send", busy ? 16 : 18)); setLabel(E.send, busy ? S.agent.stop : S.agent.send);
    E.run.classList.toggle("on", busy);
  }
  /* Fit the composer to its text. Growing needs one layout read; only shrinking has to reset the height first. */
  var gLen = 0;
  function grow() {
    var t = E.input, len = t.value.length, grew = len > gLen && t.value.indexOf("\n") < 0 && t.style.height; gLen = len;
    if (!grew) t.style.height = "auto";
    var sh = t.scrollHeight || 22 * Math.max(1, t.value.split("\n").length), hh = Math.min(176, Math.max(22, sh)) + "px";
    if (t.style.height !== hh) t.style.height = hh;
  }
  E.input.addEventListener("input", function () { grow(); renderComposer(); });
  E.input.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) { e.preventDefault(); if (busy) return; send(); }
    else if (e.key === "Escape" && busy) { post("agent.stop"); }
  });
  E.send.addEventListener("click", function () { if (busy) stop(); else send(); });
  $("sknew").addEventListener("click", newChat); $("skclose").addEventListener("click", function () { C().setSidebar(false); });
  E.mic.addEventListener("click", function () { C().toast(S.agent.voice, "warn"); });

  /* page chip + attachments ------------------------------------------------ */
  function renderChips() {
    var t = activeTab(), vis = C().pageVis(), off = vis === "blocked", k = [t.id, t.title, t.url, vis, atts.map(function (a) { return a.kind + (a.tab || ""); }).join()].join("|");
    if (E.chips._k === k) return; E.chips._k = k; SW.clear(E.chips);
    var title = t.title || SW.host(t.url || "") || S.vis.thisPage, tip = vis === "allowed" ? S.f(S.vis.chipOn) : vis === "blocked" ? S.f(S.vis.chipOff) : S.f(S.vis.chipAsk);
    var chip = h("button.chip.pagechip" + (off ? ".off" : vis === "ask" ? ".ask" : ""), { title: tip + ". " + S.vis.chipToggle, "aria-label": tip + ". " + S.vis.chipToggle, "aria-haspopup": "dialog", onclick: function () {
      var box = h("div", { style: { width: "280px" } }, h("div", { style: { padding: "6px 12px 4px", fontWeight: "700" }, text: S.f(S.vis.title) }), C().visChooser(function () { C().closePop(); })); C().openPop(chip, box, { align: "left" });
    } }, icon(off ? "eyeoff" : "eye", 15, "eye"), SW.favicon(t.url, t.favicon, t.title), h("span.trunc", { text: title, style: { maxWidth: "150px" } }));
    E.chips.appendChild(chip);
    atts.forEach(function (a, i) { E.chips.appendChild(h("span.chip", icon(a.kind === "shot" ? "image" : a.kind === "tab" ? "tab" : "list", 14), h("span.trunc", { text: a.label, style: { maxWidth: "110px" } }), h("button.x", { "aria-label": S.agent.attRemove, onclick: function () { atts.splice(i, 1); E.chips._k = ""; renderChips(); } }, icon("close", 11)))); });
  }
  E.plus.addEventListener("click", function () {
    var add = function (kind, label, tab) { return function () { atts.push({ kind: kind, label: label, tab: tab }); E.chips._k = ""; renderChips(); E.input.focus(); }; };
    var m = h("div", { style: { width: "250px" } }, C().mi("list", S.agent.attSelection, { on: add("selection", S.agent.attSelection) }), C().mi("image", S.agent.attShot, { on: add("shot", S.agent.attShot) }), h("div.sep"),
      h("div", { style: { padding: "6px 12px 2px", fontSize: "11px", fontWeight: "700", color: "var(--text-3)", textTransform: "uppercase", letterSpacing: ".06em" }, text: S.agent.attTabs }));
    (st().tabs || []).filter(function (t) { return t.id !== st().active; }).slice(0, 6).forEach(function (t) { m.appendChild(C().mi(null, t.title || SW.host(t.url), { on: add("tab", t.title || SW.host(t.url), t.id) })); });
    C().openPop(E.plus, m, { align: "left", role: "menu" });
  });
  E.src.addEventListener("click", function () {
    var row = function (key, def, ic, text) { var on = SW.setting(key, def); return C().mi(ic, text, { keep: true, on: function (e) { SW.set(key, !on); C().closePop(); } , sub: on ? S.set.on : S.set.off }); };
    var m = h("div", { style: { width: "240px" } }, row("agent.sources.page", true, "eye", S.agent.attPage), row("agent.sources.web", false, "globe", S.agent.srcWeb)); C().openPop(E.src, m, { align: "left", role: "menu" });
  });
  E.chat.addEventListener("click", function () {
    var chats = (st().agent || {}).chats || [], m = h("div", { style: { width: "280px" } }, C().mi("edit", S.agent.new, { on: newChat }), h("div.sep"));
    if (!chats.length) m.appendChild(h("div", { style: { padding: "8px 12px", color: "var(--text-3)" }, text: S.agent.noChats }));
    chats.slice(0, 8).forEach(function (c) { m.appendChild(C().mi(null, c.title || S.agent.untitled, { sub: c.when, on: function () { post("agent.chat.open", { id: c.id }); } })); });
    C().openPop(E.chat, m, { align: "left", role: "menu" });
  });

  /* messages ---------------------------------------------------------------- */
  var sc = SW.scroller(E.box, E.msgs), pinning = false;
  sc.onscroll = function () { if (!pinning) stick = sc.atBottom(); };
  function pin() { pinning = true; E.msgs.style.minHeight = E.box.clientHeight + "px"; sc.refresh(); if (stick) sc.toBottom(); pinning = false; }
  E.msgs.addEventListener("click", function (e) {
    var a = e.target.closest ? e.target.closest("a[data-href]") : null; if (a) { e.preventDefault(); post("tab.new", { url: a.getAttribute("data-href") }); }
  });
  function pageType() {
    var t = activeTab(), pt = SW.get("page.type") || t.pageType; if (pt) return pt; var u = ((t.url || "") + " " + (t.title || "")).toLowerCase();
    if (/cart|checkout|\/dp\/|\/product|shop|store|amazon\.|ebay\.|etsy\./.test(u)) return "shop"; if (/login|sign.?up|register|apply|form|survey/.test(u)) return "form";
    if (/youtube\.|youtu\.be|vimeo\.|twitch\.|watch\?/.test(u)) return "video"; if (/wikipedia|news|blog|article|medium\.|\/\d{4}\/\d\d\/|times|post|guardian|bbc\./.test(u)) return "article"; return /^https?:/.test(t.url || "") ? "any" : "";
  }
  function renderEmpty() {
    if (!empty) empty = h("div.empty");
    var pt = pageType() || "any", list = S.starters[pt] || S.starters.any, k = pt + "|" + (activeTab().title || "");
    if (empty._k !== k) {
      empty._k = k; SW.clear(empty);
      empty.appendChild(h("div.eh", SW.logo(34, "elg"), h("h2", { text: pt === "any" ? S.agent.emptyAnyTitle : S.agent.emptyTitle }), h("p", { text: S.f(S.agent.emptySub) })));
      list.forEach(function (s) {
        var ag = /do it|add to cart|fill|help me/i.test(s[0] + s[1]);
        empty.appendChild(h("button.starter" + (ag ? ".ag" : ""), { onclick: function () { send(s[1], ag ? "agent" : null); } }, h("span.si", icon(ag ? "bolt" : "spark", 16)), h("div", h("b", { text: s[0] }), h("div.d", { text: s[1] }))));
      });
    }
    if (!empty.parentNode) E.msgs.appendChild(empty);
  }
  function clearMsgs() { SW.clear(E.msgs); empty = null; runs = {}; curRun = null; setBusy(false); renderEmpty(); }
  function addUser(text, a) {
    if (empty && empty.parentNode) empty.parentNode.removeChild(empty);
    var el = h("div.um", a && a.length ? h("div", { style: { display: "flex", gap: "6px", flexWrap: "wrap", justifyContent: "flex-end" } }, a.map(function (x) { return h("span.chip", icon(x.kind === "shot" ? "image" : "list", 13), x.label); })) : null, h("div.bub", { text: text }));
    E.msgs.appendChild(el); stick = true; pin();
  }
  function newAssistant(runId) {
    if (empty && empty.parentNode) empty.parentNode.removeChild(empty);
    var el = h("div.am"), a = { id: runId, el: el, parts: [], t0: performance.now(), pend: h("span.pend", h("i"), h("i"), h("i")), dirty: false, done: false, ttft: null, thoughtT: 0 };
    el.appendChild(a.pend); E.msgs.appendChild(el); runs[runId] = a; curRun = a; stick = true; pin(); return a;
  }
  function last(a, type) { var p = a.parts[a.parts.length - 1]; return p && p.type === type ? p : null; }
  function addPart(a, p) { if (a.pend && a.pend.parentNode) a.pend.parentNode.removeChild(a.pend); if (a.live) a.el.insertBefore(p.el, a.live); else a.el.appendChild(p.el); a.parts.push(p); return p; }
  function flush(a) {
    if (!a.dirty) return; a.dirty = false;
    a.parts.forEach(function (p) { if (p.type === "text" && p.d) { p.d = false; SW.clear(p.el); p.el.appendChild(SW.md.render(p.buf)); if (!a.done) { var lastEl = p.el.lastChild; var cur = h("span.cur"); (lastEl && lastEl.nodeType === 1 ? lastEl : p.el).appendChild(cur); } } });
    pin();
  }
  function schedule(a) { a.dirty = true; if (!a.sched) { a.sched = true; SW.raf(function () { a.sched = false; flush(a); }); } }
  function mark(a) { if (a.ttft == null) { a.ttft = performance.now() - a.t0; } }
  function setLive(a, text) {
    if (!text) { if (a.live && a.live.parentNode) a.live.parentNode.removeChild(a.live); a.live = null; return; }
    if (!a.live) { a.live = h("div.stopped", h("span.spin"), h("span")); a.el.appendChild(a.live); if (a.pend && a.pend.parentNode) a.pend.parentNode.removeChild(a.pend); }
    a.live.lastChild.textContent = text;
  }
  var ST = { running: ["spin"], ok: ["check", 12], error: ["close", 12], pending: ["clock", 12] };
  function stepEl(ev) {
    var s = ev.status || "running", ic = s === "running" ? h("span.spin") : icon(s === "ok" || s === "done" ? "check" : s === "error" ? "close" : "clock", 12);
    return h("div.step." + (s === "done" ? "ok" : s), h("span.si", ic), h("div.sl", ev.label || ev.title || "", ev.detail ? h("div.sd.trunc", { text: ev.detail }) : null), ev.ms != null ? h("span.sm", { text: SW.fmtMs(ev.ms) }) : null);
  }
  function onAction(a, ev) {
    mark(a); var p = last(a, "steps");
    if (!p) { p = addPart(a, { type: "steps", el: h("div.steps", { role: "list", "aria-label": S.agent.steps }), rows: {} }); }
    var row = ev.id != null ? p.rows[ev.id] : null, n = stepEl(ev); n.setAttribute("role", "listitem");
    if (row) p.el.replaceChild(n, row); else p.el.appendChild(n);
    if (ev.id != null) p.rows[ev.id] = n; pin();
  }
  function onThought(a, ev) {
    mark(a); var p = last(a, "thought");
    if (!p) { var body = h("div.tb"), btn = h("button", { "aria-expanded": "true" }, icon("spark", 14), h("span.lb", { text: S.agent.thinking }), icon("chev", 14, "chv")); p = addPart(a, { type: "thought", el: h("div.think.live.open", btn, body), body: body, btn: btn, t: performance.now(), touched: false });
      btn.addEventListener("click", function () { p.touched = true; var o = p.el.classList.toggle("open"); btn.setAttribute("aria-expanded", o); }); }
    if (ev.text) { p.body.textContent += ev.text; pin(); }
    if (ev.done) endThought(p);
  }
  function endThought(p) { if (p.ended) return; p.ended = true; p.el.classList.remove("live"); p.btn.querySelector(".lb").textContent = S.f(S.agent.thought, { n: Math.max(1, Math.round((performance.now() - p.t) / 1000)) }); if (!p.touched) { p.el.classList.remove("open"); p.btn.setAttribute("aria-expanded", "false"); } }
  function onText(a, ev) {
    mark(a); var p = last(a, "text"); a.parts.forEach(function (x) { if (x.type === "thought") endThought(x); });
    if (!p) p = addPart(a, { type: "text", buf: "", el: h("div.txt"), d: true });
    p.buf = ev.full ? ev.text : p.buf + (ev.text || ""); p.d = true; schedule(a);
  }
  function onConfirm(a, ev) {
    mark(a); var act = ev.action || {}, pre = h("pre");
    Object.keys(act).forEach(function (k) { pre.appendChild(h("b", { text: k + " " })); pre.appendChild(document.createTextNode(String(act[k]) + "\n")); });
    if (!Object.keys(act).length && ev.detail) pre.textContent = ev.detail;
    var res = h("div.res"), card = h("div.cf", { role: "group", "aria-label": S.agent.confirmRisk }, h("div.ch", icon("shield", 18), h("span", { text: ev.title || S.agent.confirmTitle }), h("span.cr", { text: ev.risk || S.agent.confirmRisk })), pre, res);
    var decide = function (ok) { post("agent.confirm", { runId: a.id, id: ev.id, approved: ok }); card.classList.add("done"); res.textContent = ok ? S.agent.approved : S.agent.denied; };
    card.appendChild(h("div.ca", h("button.btn.primary", { text: S.agent.approve, onclick: function () { decide(true); } }), h("button.btn", { text: S.agent.deny, onclick: function () { decide(false); } })));
    addPart(a, { type: "confirm", el: card }); setLive(a, "");
    a.pendingConfirm = true; SW.emit("render"); pin();
  }
  function onSources(a, items) {
    if (!items || !items.length) return; var p = h("div.srcs", { "aria-label": S.agent.sources });
    items.slice(0, 8).forEach(function (s, i) { p.appendChild(h("button.chip", { title: s.url, onclick: function () { post("tab.new", { url: s.url }); } }, h("span.n", { text: i + 1 }), SW.favicon(s.url, s.favicon, s.title), h("span.trunc", { text: s.title || SW.host(s.url), style: { maxWidth: "140px" } }))); });
    addPart(a, { type: "sources", el: p });
  }
  function onError(a, ev) {
    var code = ev.code || "", msg = ev.message || S.agent.errGeneric, rate = code === "rate_limit", box = h("div.errc" + (rate ? ".rate" : ""), { role: "alert" }, icon(rate ? "clock" : "alert", 18), h("div.eb", h("div", { text: code === "auth" ? S.agent.errAuth : code === "network" ? S.agent.errNet : rate ? S.f(S.agent.errRate, { s: ev.retryAfter || 30 }) : msg })));
    var eb = box.querySelector(".eb");
    if (code === "auth") eb.appendChild(h("button.btn.sm.primary", { text: S.agent.errAuthBtn, onclick: function () { post("tab.new", { url: "sweef://settings#agent" }); } }));
    else if (lastUser) eb.appendChild(h("button.btn.sm", { text: S.agent.retry, onclick: function () { if (box.parentNode) box.parentNode.removeChild(box); send(lastUser); } }));
    if (rate && ev.retryAfter) { var n = ev.retryAfter, d = eb.firstChild, iv = setInterval(function () { n--; if (n <= 0 || !box.parentNode) { clearInterval(iv); d.textContent = S.agent.errRate.replace("{s}", "0").replace(/ in 0s.*/, ". You can retry now."); } else d.textContent = S.f(S.agent.errRate, { s: n }); }, 1000); }
    finish(a, true); addPart(a, { type: "error", el: box }); pin();
  }
  /* "Research used 412 KB": the real bytes of the pages the agent opened; the estimate is labelled as one */
  function dataLine(d) {
    var T = S.data, el = h("div.amdata", { title: S.f(T.researchTip) }, icon("drop", 13), h("span", h("b.num", { text: S.f(T.research, { n: SW.fmtBytes(d.bytes) }) })));
    if (d.savedEstimated > 0) el.appendChild(h("div.est", { text: S.f(T.researchNot, { n: SW.fmtBytes(d.savedEstimated) }) }));
    return el;
  }
  function finish(a, errored, ev) {
    if (a.done) return; a.done = true; setLive(a, ""); a.parts.forEach(function (p) { if (p.type === "thought") endThought(p); if (p.type === "steps") SW.$$(".step.running", p.el).forEach(function (s) { s.classList.remove("running"); }); });
    a.dirty = true; flush(a); if (a.pend && a.pend.parentNode) a.pend.parentNode.removeChild(a.pend);
    if (curRun === a) { curRun = null; setBusy(false); }
    var txt = a.parts.filter(function (p) { return p.type === "text"; }).map(function (p) { return p.buf; }).join("\n\n");
    if (!errored && ev && ev.data && ev.data.available !== false && ev.data.bytes != null) a.el.appendChild(dataLine(ev.data));
    if (!errored && txt) a.el.appendChild(h("div.amfoot", h("button.ibtn", { title: S.agent.copy, "aria-label": S.agent.copy, onclick: function () { post("clipboard.write", { text: txt }); C().toast(S.agent.copied); } }, icon("copy", 15))));
    var ttft = (ev && ev.ttft) != null ? ev.ttft : a.ttft, tps = ev && ev.tps; if (ttft != null && !errored) { lastLat = { ttft: ttft, tps: tps, ms: ev && ev.ms }; renderLat(); }
    pin();
  }
  function renderLat() {
    var on = SW.setting("agent.latencyStrip", true) && lastLat; E.lat.classList.toggle("hide", !on); SW.clear(E.lat); if (!on) return;
    var pct = Math.max(4, Math.min(100, 100 - lastLat.ttft / 20));
    E.lat.title = S.agent.latencyTip;
    E.lat.appendChild(icon("bolt", 12)); E.lat.appendChild(h("span", h("b.num", { text: SW.fmtMs(lastLat.ttft) }), " " + S.agent.latency));
    E.lat.appendChild(h("span.bar2", h("i", { style: { width: pct + "%" } })));
    if (lastLat.tps) E.lat.appendChild(h("span.num", { text: Math.round(lastLat.tps) + " " + S.agent.latencyTps }));
    E.lat.appendChild(h("button", { title: S.agent.latencyHide, "aria-label": S.agent.latencyHide, onclick: function () { SW.set("agent.latencyStrip", false); renderLat(); } }, icon("close", 11)));
  }
  function handle(ev) {
    if (!ev) return; var id = ev.runId || (curRun && curRun.id) || "r0", a = runs[id] || newAssistant(id);
    setBusy(!a.done && ev.kind !== "done" && ev.kind !== "error");
    switch (ev.kind) {
      case "text": onText(a, ev); break; case "thought": onThought(a, ev); break; case "action": onAction(a, ev); break;
      case "status": mark(a); if (ev.text) setLive(a, ev.text); if (ev.state === "idle") setLive(a, ""); break;
      case "confirm": onConfirm(a, ev); break; case "sources": onSources(a, ev.items); break;
      case "done": onSources(a, ev.sources); finish(a, false, ev); break; case "error": onError(a, ev); break;
    }
    pin();
  }
  function onEvent(ev) {
    if (ev && ev.runId && runs.pending && !runs[ev.runId]) { runs[ev.runId] = runs.pending; runs[ev.runId].id = ev.runId; delete runs.pending; }
    else if (ev && !ev.runId && runs.pending) ev = Object.assign({}, ev, { runId: "pending" });
    handle(ev);
  }
  SW.on("agent.event", onEvent);
  function loadChat(p) {
    chat = { id: p.id || "", title: p.title || "" }; clearMsgs(); buildStatic();
    (p.messages || []).forEach(function (m) { if (m.role === "user") addUser(m.text); else { var a = newAssistant("h" + Math.random()); onText(a, { text: m.text }); finish(a, false); } });
  }
  SW.on("agent.chat.load", loadChat);

  /* send / stop -------------------------------------------------------------- */
  function setBusy(b) { busy = b; renderComposer(); }
  function send(text, forceMode) {
    text = (text != null ? text : E.input.value).trim(); if (!text) return;
    if (forceMode) SW.set("agent.mode", forceMode);
    lastUser = text; var a = atts.slice(); addUser(text, a);
    E.input.value = ""; grow(); atts = []; E.chips._k = "";
    var t = activeTab();
    var ra = newAssistant("run" + Date.now()); ra.id = "pending"; runs.pending = ra; setBusy(true);
    post("agent.send", { text: text, mode: mode(), chat: chat.id, page: { url: t.url, title: t.title, tab: t.id }, attachments: a.map(function (x) { return { kind: x.kind, tab: x.tab }; }), sources: { page: SW.setting("agent.sources.page", true), web: SW.setting("agent.sources.web", false) } });
    renderChips();
  }
  /* the first event from Go carries the real runId: adopt the pending bubble */
  function stop() { post("agent.stop"); if (curRun) { var a = curRun; setLive(a, ""); finish(a, true); a.el.appendChild(h("div.stopped", icon("stop", 12), S.agent.youStopped)); } setBusy(false); }
  function newChat() { post("agent.chat.new"); chat = { id: "", title: "" }; clearMsgs(); buildStatic(); atts = []; renderChips(); E.input.focus(); }
  function ask(text, source) { C().setSidebar(true); send(text, "ask"); }

  /* resize ------------------------------------------------------------------- */
  var rz = null;
  E.rz.addEventListener("mousedown", function (e) { e.preventDefault(); rz = { x: e.clientX, w: E.side.offsetWidth }; E.rz.classList.add("drag"); window.addEventListener("mousemove", rzMove); window.addEventListener("mouseup", rzEnd); });
  function rzMove(e) { if (!rz) return; var w = SW.clamp(rz.w + (rz.x - e.clientX), 320, 560); document.documentElement.style.setProperty("--sidebar-w", w + "px"); rz.cur = w; }
  function rzEnd() { window.removeEventListener("mousemove", rzMove); window.removeEventListener("mouseup", rzEnd); E.rz.classList.remove("drag"); if (rz && rz.cur) { SW.state.sidebar = SW.state.sidebar || {}; SW.state.sidebar.width = rz.cur; SW.set("sidebar.width", rz.cur); } rz = null; }
  E.rz.addEventListener("keydown", function (e) { var d = e.key === "ArrowLeft" ? 16 : e.key === "ArrowRight" ? -16 : 0; if (!d) return; e.preventDefault(); var w = SW.clamp(E.side.offsetWidth + d, 320, 560); SW.state.sidebar = SW.state.sidebar || {}; SW.state.sidebar.width = w; SW.set("sidebar.width", w); });
  E.rz.addEventListener("dblclick", function () { SW.state.sidebar = SW.state.sidebar || {}; SW.state.sidebar.width = 380; SW.set("sidebar.width", 380); });
  E.rz.setAttribute("aria-valuemin", "320"); E.rz.setAttribute("aria-valuemax", "560");

  /* wiring ------------------------------------------------------------------- */
  buildStatic();
  SW.on("render", function () {
    var ag = st().agent || {}; if (ag.chat && ag.chat.title !== undefined && ag.chat.title !== chat.title) { chat.title = ag.chat.title; SW.clear(E.chat); E.chat.appendChild(h("span.trunc", { text: chat.title || S.f(S.agent.title) })); E.chat.appendChild(icon("chev", 14)); }
    renderComposer(); renderChips(); renderLat();
    if (empty && empty.parentNode) { renderEmpty(); }
    var ph = ag.hasKey === false; E.input.disabled = false;
  });
  SW.sidekick = { focus: function () { setTimeout(function () { E.input.focus(); }, 0); }, ask: ask, newChat: newChat, send: send, onEvent: onEvent, loadChat: loadChat };
  renderEmpty(); renderComposer(); grow();
})();
