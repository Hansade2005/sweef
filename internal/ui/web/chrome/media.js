/* Camera, microphone and screen-share prompt and the "in use" popover. Loaded the first time a page asks or the indicator is clicked. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, T = S.media;
  var C = function () { return SW.chrome; };
  var node = null, shownId = "", pick = {};
  function media() { return SW.state.media || {}; }

  function title(p) {
    var host = p.host || p.origin;
    if (p.kind === "display") return S.f(T.wantsShare, { host: host });
    return S.f(p.video && p.audio ? T.wantsBoth : p.video ? T.wantsCam : T.wantsMic, { host: host });
  }
  function hide() { if (node && node.parentNode) node.parentNode.removeChild(node); node = null; shownId = ""; }
  function reply(p, action, extra) {
    var o = { id: p.id, action: action }, k; extra = extra || {};
    for (k in extra) o[k] = extra[k];
    post("media.answer", o); hide();
  }

  /* a device row: the chosen device, and when there are several a list to pick another */
  function deviceRow(ic, label, list, key) {
    var sel = pick[key] || (list[0] && list[0].id), box = h("div.mdev"), open = false, listEl = h("div.mlist", { role: "listbox", "aria-label": label });
    listEl.classList.add("hide");
    function cur() { for (var i = 0; i < list.length; i++) if (list[i].id === sel) return list[i]; return list[0] || { label: "" }; }
    var btn = h("button.mrow", { "aria-haspopup": list.length > 1 ? "listbox" : null, "aria-expanded": "false", "aria-label": label + ": " + cur().label, title: label,
      onclick: function () {
        if (list.length < 2) return;
        open = !open; btn.setAttribute("aria-expanded", open ? "true" : "false"); listEl.classList.toggle("hide", !open);
      } });
    function paint() {
      SW.clear(btn); btn.appendChild(icon(ic, 16)); btn.appendChild(h("span.grow.trunc", { text: cur().label })); if (list.length > 1) btn.appendChild(icon("chev", 14));
      SW.clear(listEl);
      list.forEach(function (d) {
        listEl.appendChild(h("button.mopt", { role: "option", "aria-selected": d.id === sel ? "true" : "false", onclick: function () {
          sel = d.id; pick[key] = sel; open = false; btn.setAttribute("aria-expanded", "false"); listEl.classList.add("hide"); paint(); btn.focus && btn.focus();
        } }, h("span.grow.trunc", { text: d.label }), d.id === sel ? icon("check", 14, "ck") : null));
      });
    }
    paint(); box.appendChild(btn); box.appendChild(listEl);
    box._sel = function () { return sel; };
    return box;
  }

  function buildDevices(p) {
    var body = h("div.mbody"), cam = null, mic = null;
    if (p.video && (p.cameras || []).length) { cam = deviceRow("camera", T.camera, p.cameras, "cam:" + p.id); body.appendChild(cam); }
    if (p.audio && (p.mics || []).length) { mic = deviceRow("mic", T.mic, p.mics, "mic:" + p.id); body.appendChild(mic); }
    var acts = h("div.macts");
    var chosen = function () { return { video: cam ? cam._sel() : "", audio: mic ? mic._sel() : "" }; };
    acts.appendChild(h("button.btn.primary.mbtn", { onclick: function () { reply(p, "once", chosen()); } }, T.once));
    if (p.remember) acts.appendChild(h("button.btn.soft.mbtn", { onclick: function () { reply(p, "always", chosen()); } }, T.always));
    acts.appendChild(h("button.btn.ghost.mbtn.mblock", { onclick: function () { reply(p, "block"); } }, T.block));
    body.appendChild(acts);
    if (!p.remember) body.appendChild(h("p.mnote", { text: T.priv }));
    return body;
  }

  function buildDisplay(p) {
    var body = h("div.mbody"), srcs = p.sources || [], sel = null;
    var cur = srcs.filter(function (s) { return s.current; })[0], win = srcs.filter(function (s) { return s.surface === "window"; })[0];
    sel = (p.surface === "window" && win) ? win.id : (cur ? cur.id : (srcs[0] && srcs[0].id));
    var list = h("div.msrc", { role: "radiogroup", "aria-label": T.pick }), share;
    function paint() {
      SW.clear(list);
      var groups = [[T.thisTab, srcs.filter(function (s) { return s.current; })],
        [T.otherTabs, srcs.filter(function (s) { return !s.current && s.surface === "browser"; })], [null, srcs.filter(function (s) { return s.surface === "window"; })]];
      groups.forEach(function (g) {
        if (!g[1].length) return;
        if (g[0]) list.appendChild(h("div.mgh", { text: g[0] }));
        g[1].forEach(function (s) {
          var isWin = s.surface === "window", name = isWin ? S.f(T.win) : (s.title || s.url);
          list.appendChild(h("button.mrow.msel", { role: "radio", "aria-checked": s.id === sel ? "true" : "false", onclick: function () { sel = s.id; paint(); } },
            isWin ? icon("screen", 16) : h("span.mfav", SW.favicon(s.url, s.favicon, s.title)), h("span.grow.trunc", { text: name }), s.id === sel ? icon("check", 14, "ck") : null));
        });
      });
    }
    paint(); body.appendChild(list);
    var notes = [T.noOs]; if (p.wantAudio) notes.unshift(T.noAudio);
    body.appendChild(h("p.mnote", { text: notes.join(" ") }));
    var acts = h("div.macts.mrow2");
    acts.appendChild(h("button.btn.ghost.mbtn", { onclick: function () { reply(p, "dismiss"); } }, T.cancel));
    share = h("button.btn.primary.mbtn", { onclick: function () { if (sel) reply(p, "once", { source: sel }); } }, T.share);
    acts.appendChild(share); body.appendChild(acts);
    return body;
  }

  function show(p) {
    if (!p) { hide(); return; }
    if (node && shownId === p.id) return;
    hide(); shownId = p.id;
    var ic = p.kind === "display" ? "screen" : (p.video ? "camera" : "mic");
    node = h("div.pop.mprompt", { role: "dialog", "aria-modal": "false", "aria-label": title(p), tabindex: "-1" },
      h("div.mhead", h("span.mic", icon(ic, 18)), h("b.mt", { text: title(p) }),
        h("button.ibtn.sm", { "aria-label": p.kind === "display" ? T.cancel : T.dismiss, title: p.kind === "display" ? T.cancel : T.dismiss, onclick: function () { reply(p, "dismiss"); } }, icon("close", 14))),
      p.kind === "display" ? buildDisplay(p) : buildDevices(p));
    node.addEventListener("keydown", function (e) {
      if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); reply(p, "dismiss"); }
    });
    var layer = document.getElementById("layer"), om = document.getElementById("omni"), r = om ? om.getBoundingClientRect() : { left: 150, bottom: 84 };
    node.style.visibility = "hidden"; layer.appendChild(node);
    node.style.left = Math.round(SW.clamp(r.left, 8, window.innerWidth - node.offsetWidth - 8)) + "px"; node.style.top = Math.round(r.bottom + 8) + "px"; node.style.visibility = "";
    /* focus the bubble itself, not a button: a key the person is typing into the page must never answer the prompt */
    if (node.focus) node.focus();
  }
  SW.mediaPrompt = { show: show };

  /* ---- "in use" popover: live sessions with Stop, and this site's stored choices ---- */
  var pop = null;
  function fillPop() {
    if (!pop || !pop.isConnected) return;
    var m = media(), ses = m.sessions || [], site = m.site;
    SW.clear(pop);
    pop.appendChild(h("div.mpt", h("b", { text: T.popTitle })));
    if (!ses.length) pop.appendChild(h("div.mnone", { text: "-" }));
    ses.forEach(function (s) {
      var ic = s.kind === "camera" ? "camera" : s.kind === "screen" ? "screen" : "mic";
      pop.appendChild(h("div.mses", icon(ic, 18), h("div.grow", h("b.trunc", { text: s.label || T[s.kind === "microphone" ? "mic" : s.kind] }), h("span.s", { text: s.kind === "camera" ? T.camera : s.kind === "screen" ? T.screen : T.mic })),
        h("button.btn.sm", { onclick: function () { post("media.stop", { id: s.id }); } }, T.stop)));
    });
    if (site) {
      pop.appendChild(h("div.sep"));
      pop.appendChild(h("div.mpt", h("b", { text: T.sitePerms }), h("span", { text: (site.origin || "").replace(/^https?:\/\//, "") })));
      [["camera", T.camera, true], ["microphone", T.mic, true], ["screen", T.screen, false]].forEach(function (x) {
        var seg = h("div.seg.mseg", { role: "radiogroup", "aria-label": x[1] }), cur = site[x[0]] || "ask";
        (x[2] ? [["ask", T.ask], ["allow", T.allowed], ["block", T.blocked]] : [["ask", T.ask], ["block", T.blocked]]).forEach(function (o) {
          seg.appendChild(h("button", { role: "radio", "aria-checked": cur === o[0] ? "true" : "false", onclick: function () {
            site[x[0]] = o[0]; post("site.media", { origin: site.origin, name: x[0], value: o[0] === "ask" ? "" : o[0] }); fillPop();
          } }, o[1]));
        });
        pop.appendChild(h("div.mperm", h("span.grow", { text: x[1] }), seg));
      });
    }
  }
  SW.mediaPop = {
    open: function (anchor) {
      pop = h("div.mpop", { tabindex: "-1", "aria-label": T.popTitle }); C().openPop(anchor, pop, { align: "right", noFocus: true });
      fillPop(); if (pop.focus) pop.focus();
    }
  };
  SW.on("render", function () { fillPop(); });
})();

/* ---- now playing: the controls of the page's Media Session ---- */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post, T = S.media;
  var C = function () { return SW.chrome; };
  var np = null;
  T.play = "Play"; T.pause = "Pause"; T.prev = "Previous track"; T.next = "Next track"; T.unknownTitle = "Playing media";
  function ses() { return (SW.state.media || {}).session || null; }
  function can(s, a) { return (s.actions || []).indexOf(a) >= 0; }
  function fill() {
    if (!np || !np.isConnected) return;
    var s = ses(); SW.clear(np);
    if (!s) { np.appendChild(h("div.mnone", { text: "-" })); return; }
    var sub = [s.artist, s.album].filter(Boolean).join(" - ");
    np.appendChild(h("div.nphead", h("span.mic", icon("music", 20)), h("div.grow", h("b.trunc", { text: s.title || T.unknownTitle }), sub ? h("span.s.trunc", { text: sub }) : null)));
    if (s.duration > 0) np.appendChild(h("div.npbar", { role: "progressbar", "aria-valuemin": "0", "aria-valuemax": String(Math.round(s.duration)), "aria-valuenow": String(Math.round(s.position || 0)) }, h("i", { style: "width:" + Math.max(0, Math.min(100, 100 * (s.position || 0) / s.duration)).toFixed(1) + "%" })));
    var playing = s.playbackState === "playing";
    function btn(ic, label, action, arg, on) {
      var b = h("button.ibtn", { "aria-label": label, title: label, onclick: function () { post("media.action", { action: action, arg: arg || 0 }); } }, icon(ic, 18));
      if (!on) b.disabled = true; return b;
    }
    np.appendChild(h("div.npctl",
      btn("skipprev", T.prev, "previoustrack", 0, can(s, "previoustrack")),
      btn(playing ? "pause" : "play", playing ? T.pause : T.play, playing ? "pause" : "play", 0, true),
      btn("skipnext", T.next, "nexttrack", 0, can(s, "nexttrack"))));
  }
  SW.nowPlaying = {
    open: function (anchor) { np = h("div.nppop", { tabindex: "-1", "aria-label": T.nowPlaying }); C().openPop(anchor, np, { align: "right", noFocus: true }); np.classList.add("pop"); fill(); },
    refresh: fill
  };
})();
