/* Preview/test harness only (NOT embedded in ui.FS): a mock window.sweef. */
window.__posts = [];
window.__errs = [];
window.sweef = { post: function (t, p) { window.__posts.push([t, p]); }, onMessage: null, mock: true };
window.__send = function (t, p) { if (window.sweef.onMessage) window.sweef.onMessage(t, p); };
window.addEventListener("error", function (e) { window.__errs.push("uncaught: " + (e && (e.message || e.error)) + " @" + (e && e.filename) + ":" + (e && e.lineno)); });
window.addEventListener("unhandledrejection", function (e) { window.__errs.push("unhandled rejection: " + (e && e.reason)); });
window.__backdrop = function (kind) {
  var vp = document.getElementById("viewport"); var r = vp ? vp.getBoundingClientRect() : { left: 0, top: 80, width: window.innerWidth, height: window.innerHeight - 80 };
  var d = document.createElement("div");
  d.id = "__bd"; d.style.cssText = "position:fixed;z-index:-1;overflow:hidden;background:#fff;color:#222;font:16px/1.6 Georgia,serif;left:" + r.left + "px;top:" + r.top + "px;width:" + r.width + "px;height:" + r.height + "px";
  var inner = "";
  if (kind === "shopband") {
    d.style.left = "0"; d.style.top = "0"; d.style.width = "100%"; d.style.height = "100%"; d.style.background = "#fff";
    var pg = '<div style="position:absolute;left:0;top:88px;width:900px;height:712px;overflow:hidden"><div style="padding:16px 28px;font:600 20px system-ui;color:#0a6b2d">grocer <span style="font-weight:400;font-size:13px;color:#666;margin-left:12px">All stores</span><span style="float:right;font-size:13px;border:1px solid #ddd;border-radius:99px;padding:6px 14px;color:#444">Search Power Sporting Goods</span></div><div style="display:flex;gap:14px;padding:12px 28px">';
    [["Mineral Sunscreen", "$9.99", "#f4a261"], ["Pickleballs 3-pack", "$9.99", "#e9c46a"], ["Beach Towel", "$29.99", "#2a9d8f"], ["Bucket Hat", "$20.00", "#264653"]].forEach(function (it) { pg += '<div style="flex:1;font:13px system-ui;color:#333"><div style="height:150px;border-radius:12px;background:' + it[2] + ';opacity:.85;margin-bottom:10px"></div><b style="font-size:15px">' + it[1] + '</b><div>' + it[0] + '</div></div>'; });
    pg += '</div></div>';
    inner = '<div style="position:absolute;left:0;top:0;right:0;height:88px;background:linear-gradient(180deg,#E3E6FF,#F4F5FF);box-shadow:inset 0 -1px 0 rgba(24,26,70,.1)"><div style="position:absolute;left:14px;top:6px;width:200px;height:32px;border-radius:10px;background:#fff;font:600 13px/32px system-ui;padding-left:14px;color:#15162b">Power Sporting Goods</div><div style="position:absolute;left:250px;right:250px;top:50px;height:30px;border-radius:99px;background:#fff;font:13px/30px system-ui;text-align:center;color:#555">grocer.example.com</div></div>' + pg + '<div style="position:absolute;left:900px;top:88px;right:0;bottom:0;background:#fff;box-shadow:inset 1px 0 0 rgba(24,26,70,.1)"></div>';
  } else if (kind === "shop") {
    inner = '<div style="padding:16px 28px;font:600 20px system-ui;color:#0a6b2d">grocer <span style="font-weight:400;font-size:13px;color:#666;margin-left:12px">All stores</span><span style="float:right;font-size:13px;border:1px solid #ddd;border-radius:99px;padding:6px 14px;color:#444">Search Power Sporting Goods</span></div><div style="display:flex;gap:14px;padding:12px 28px">';
    var items = [["Mineral Sunscreen", "$9.99", "#f4a261"], ["Pickleballs 3-pack", "$9.99", "#e9c46a"], ["Beach Towel", "$29.99", "#2a9d8f"], ["Bucket Hat", "$20.00", "#264653"]];
    items.forEach(function (it) { inner += '<div style="flex:1;font:13px system-ui;color:#333"><div style="height:150px;border-radius:12px;background:' + it[2] + ';opacity:.85;margin-bottom:10px"></div><b style="font-size:15px">' + it[1] + '</b><div>' + it[0] + '</div></div>'; });
    inner += "</div>";
  } else if (kind === "blank") { inner = ""; }
  else {
    inner = '<div style="max-width:640px;margin:0 auto;padding:36px 20px"><div style="font:600 12px system-ui;color:#999;letter-spacing:.08em;margin-bottom:8px">PERSONAL FINANCE</div><h1 style="font-size:34px;line-height:1.15;margin:0 0 14px">What the 2026 tax brackets mean for your paycheck</h1><p style="color:#555">Here are some of the key changes. The threshold for the top federal income-tax bracket will climb by about $17,000 for a married couple.</p><p>Your <a href="#" style="color:#1a56b0">effective tax rate</a> will be lower than your top rate. That is because the first slice of income is taxed at 10%, the next slice at 12%, and so on.</p><p>A married couple filing jointly could have $100,800 in taxable income, nearly $4,000 more than this year, and stay in the 12% bracket.</p><p>Not all tax parameters get inflation adjustments, although the new tax law added some more to the mix.</p></div>';
  }
  d.innerHTML = inner; document.body.appendChild(d);
};

/* previews are static frames: freeze animations at their end state */
document.addEventListener("DOMContentLoaded", function () { var st = document.createElement("style"); st.textContent = "*,*::before,*::after{animation:none!important;transition:none!important}"; document.head.appendChild(st); });
