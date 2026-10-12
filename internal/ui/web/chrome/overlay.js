/* Agent overlay surface. Go sends: overlay {state, viewport:{x,y,w,h}, task, step, cursor:{x,y,label,pressed}}.
   state: off | acting | control | done | stopped. The overlay posts overlay.hit {rects} so Go can pass clicks through everywhere else. */
(function () {
  var SW = window.SW, S = window.S, h = SW.h, icon = SW.icon, post = SW.post;
  var $ = function (id) { return document.getElementById(id); }, glow = $("glow"), cur = $("cursor"), pill = $("pill"), vp = { x: 0, y: 0, w: window.innerWidth, h: window.innerHeight }, hideT = 0, lastHit = "";
  cur.innerHTML = '<span class="rp"></span><svg width="24" height="26" viewBox="0 0 24 26"><path d="M3 2l17 9.500-7.200 2.200L9.600 21z" fill="#5B5BFF" stroke="#fff" stroke-width="1.800" stroke-linejoin="round"/></svg><span class="lb"></span>';
  function place(el, x, y, w, hh) { el.style.left = x + "px"; el.style.top = y + "px"; if (w != null) { el.style.width = w + "px"; el.style.height = hh + "px"; } }
  function render(p) {
    p = p || {}; var s = p.state || "off"; if (p.viewport) vp = p.viewport;
    clearTimeout(hideT);
    if (s === "off") { glow.classList.add("off"); cur.classList.add("off"); pill.classList.add("off"); report(); return; }
    glow.className = s; place(glow, vp.x, vp.y, vp.w, vp.h);
    var showCur = s === "acting" && p.cursor && p.cursor.x != null; cur.classList.toggle("off", !showCur);
    if (showCur) { cur.style.transform = "translate(" + Math.round(vp.x + p.cursor.x) + "px," + Math.round(vp.y + p.cursor.y) + "px)"; cur.querySelector(".lb").textContent = p.cursor.label || S.f(S.overlay.cursorLabel); cur.classList.toggle("press", !!p.cursor.pressed); }
    SW.clear(pill); pill.className = s === "control" ? "control" : "";
    var task = p.task || "";
    if (s === "acting") {
      pill.appendChild(h("span.mk", h("span.spin"))); pill.appendChild(h("div.tx", h("b", { text: S.f(S.overlay.fulfilling, { task: task }) }), p.step ? h("small", { text: p.step }) : null));
      pill.appendChild(h("button", { onclick: function () { post("agent.takeControl"); } }, icon("hand", 14), S.overlay.takeControl)); pill.appendChild(h("button.stop", { onclick: function () { post("agent.stop"); } }, icon("stop", 12), S.overlay.stop));
    } else if (s === "control") {
      pill.appendChild(h("span.mk", icon("hand", 18, "ok"))); pill.appendChild(h("div.tx", h("b", { text: S.overlay.control }), h("small", { text: S.f(S.overlay.controlSub) })));
      pill.appendChild(h("button.go", { onclick: function () { post("agent.resume"); } }, icon("bolt", 14), S.f(S.overlay.resume))); pill.appendChild(h("button.stop", { onclick: function () { post("agent.stop"); } }, S.overlay.stop));
    } else {
      pill.appendChild(h("span.mk", icon(s === "done" ? "check" : "stop", 16, "ok"))); pill.appendChild(h("div.tx", h("b", { text: s === "done" ? S.overlay.done : S.overlay.stopped }), task ? h("small", { text: task }) : null)); pill.style.paddingRight = "16px";
      hideT = setTimeout(function () { render({ state: "off" }); }, 3200);
    }
    pill.classList.remove("off"); if (s !== "stopped" && s !== "done") pill.style.paddingRight = "";
    var pw = pill.offsetWidth || 360; place(pill, Math.round(vp.x + (vp.w - pw) / 2), Math.round(vp.y + vp.h - 44 - 24));
    report();
  }
  function report() {
    var rects = []; if (!pill.classList.contains("off")) { var r = pill.getBoundingClientRect(); rects.push({ x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) }); }
    var k = JSON.stringify(rects); if (k === lastHit) return; lastHit = k; post("overlay.hit", { rects: rects });
  }
  SW.on("overlay", render);
  SW.ready("overlay");
})();
