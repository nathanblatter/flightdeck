/*
 * Flightdeck bug widget — tiny, dependency-free, themeable embeddable reporter.
 *
 * Drop this on any public site to let visitors file bugs straight into the
 * project's flightdeck board. Minimal embed:
 *
 *   <script src="https://flightdeck.example.com/bug-widget.js"
 *           data-flightdeck-url="https://flightdeck.example.com"
 *           data-site="finforge"
 *           data-key="fd_your_ingest_only_key" defer></script>
 *
 * Theme it to match the host site with any of these optional attributes:
 *
 *   data-accent      brand color for the launcher, send button & focus ring
 *   data-accent-ink  text color on the accent (default: auto by contrast)
 *   data-surface     panel background        data-ink     panel text color
 *   data-font        CSS font-family stack    data-radius  corner radius in px
 *   data-mode        light | dark            preset surface/ink pair
 *   data-position    bottom-right | bottom-left | top-right | top-left
 *   data-launcher    pill | round | tab      shape of the always-on button
 *   data-icon        glyph for the launcher (default 🐞)
 *   data-label       launcher text (pill/tab)   data-title  panel heading
 *   data-blurb       one helper line under the heading
 *
 * The key is an ingest-only API key (scope: ingest); exposing it client-side
 * only allows creating bug items — nothing else.
 */
(function () {
  "use strict";

  var script =
    document.currentScript ||
    (function () {
      var s = document.getElementsByTagName("script");
      return s[s.length - 1];
    })();

  function attr(name, fallback) {
    var v = script.getAttribute("data-" + name);
    return v === null || v === "" ? fallback : v;
  }

  var cfg = {
    base: (attr("flightdeck-url", "") || "").replace(/\/$/, ""),
    site: attr("site", ""),
    key: attr("key", ""),
    label: attr("label", "Report a bug"),
    icon: attr("icon", "🐞"),
    title: attr("title", null),
    blurb: attr("blurb", null),
    accent: attr("accent", "#111827"),
    accentInk: attr("accent-ink", null),
    surface: attr("surface", null),
    ink: attr("ink", null),
    font: attr("font", "system-ui, -apple-system, Segoe UI, Roboto, sans-serif"),
    radius: attr("radius", "14"),
    mode: attr("mode", "light"),
    position: attr("position", "bottom-right"),
    launcher: attr("launcher", "pill"),
  };

  if (!cfg.base || !cfg.site || !cfg.key) {
    console.warn("[flightdeck] missing data-flightdeck-url, data-site, or data-key");
    return;
  }

  // --- derive a coherent theme from the few values that were supplied ---

  function parseColor(c) {
    if (!c) return null;
    c = c.trim();
    if (c[0] === "#") {
      if (c.length === 4) c = "#" + c[1] + c[1] + c[2] + c[2] + c[3] + c[3];
      return [parseInt(c.slice(1, 3), 16), parseInt(c.slice(3, 5), 16), parseInt(c.slice(5, 7), 16)];
    }
    var m = c.match(/(\d+)[,\s]+(\d+)[,\s]+(\d+)/);
    return m ? [+m[1], +m[2], +m[3]] : null;
  }

  // Relative luminance → pick black/white ink for best contrast on a color.
  function inkFor(color) {
    var rgb = parseColor(color) || [17, 24, 39];
    var f = rgb.map(function (v) {
      v /= 255;
      return v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4);
    });
    var lum = 0.2126 * f[0] + 0.7152 * f[1] + 0.0722 * f[2];
    return lum > 0.5 ? "#0b0f1a" : "#ffffff";
  }

  function withAlpha(color, a) {
    var rgb = parseColor(color);
    return rgb ? "rgba(" + rgb[0] + "," + rgb[1] + "," + rgb[2] + "," + a + ")" : color;
  }

  var dark = cfg.mode === "dark";
  var t = {
    accent: cfg.accent,
    accentInk: cfg.accentInk || inkFor(cfg.accent),
    surface: cfg.surface || (dark ? "#171a21" : "#ffffff"),
    ink: cfg.ink || (dark ? "#f4f6fb" : "#111827"),
    radius: (parseInt(cfg.radius, 10) || 0) + "px",
    font: cfg.font,
  };
  t.muted = withAlpha(t.ink, 0.6);
  t.border = withAlpha(t.ink, 0.16);
  t.field = dark ? withAlpha("#ffffff", 0.05) : "#ffffff";
  t.title = cfg.title || cfg.label;

  // --- styles, driven by CSS custom properties so the theme stays readable ---

  var vars =
    "--fd-accent:" + t.accent + ";--fd-accent-ink:" + t.accentInk +
    ";--fd-surface:" + t.surface + ";--fd-ink:" + t.ink + ";--fd-muted:" + t.muted +
    ";--fd-border:" + t.border + ";--fd-field:" + t.field + ";--fd-radius:" + t.radius +
    ";--fd-font:" + t.font.replace(/;/g, "") + ";";

  var vertical = cfg.position.indexOf("top") === 0 ? "top" : "bottom";
  var horizontal = cfg.position.indexOf("left") >= 0 ? "left" : "right";
  var anchor = vertical + ":20px;" + horizontal + ":20px;";

  var css =
    "#fd-root{" + vars + "}" +
    ".fd-launch{position:fixed;" + anchor + "z-index:2147483000;display:inline-flex;align-items:center;gap:8px;" +
      "background:var(--fd-accent);color:var(--fd-accent-ink);border:0;cursor:pointer;" +
      "font:600 13px/1 var(--fd-font);box-shadow:0 6px 20px " + withAlpha(t.accent, 0.35) + ";" +
      "transition:transform .15s ease,box-shadow .15s ease}" +
    ".fd-launch:hover{transform:translateY(-2px);box-shadow:0 10px 26px " + withAlpha(t.accent, 0.45) + "}" +
    ".fd-launch:focus-visible{outline:3px solid " + withAlpha(t.accent, 0.5) + ";outline-offset:2px}" +
    ".fd-launch .fd-ico{font-size:15px;line-height:1}" +
    ".fd-pill{padding:12px 18px;border-radius:999px}" +
    ".fd-round{padding:0;width:54px;height:54px;border-radius:999px;justify-content:center}" +
    ".fd-round .fd-txt{display:none}.fd-round .fd-ico{font-size:22px}" +
    ".fd-tab{padding:10px 16px;border-radius:var(--fd-radius) var(--fd-radius) 0 0;writing-mode:horizontal-tb}" +
    ".fd-scrim{position:fixed;inset:0;z-index:2147483001;background:rgba(8,10,16,.5);" +
      "display:flex;align-items:center;justify-content:center;padding:16px;backdrop-filter:blur(2px)}" +
    ".fd-panel{background:var(--fd-surface);color:var(--fd-ink);width:min(94vw,420px);" +
      "border-radius:var(--fd-radius);padding:22px;font:14px/1.45 var(--fd-font);" +
      "box-shadow:0 24px 60px rgba(0,0,0,.4);border:1px solid var(--fd-border)}" +
    ".fd-anim{animation:fd-in .22s cubic-bezier(.2,.8,.2,1)}" +
    "@keyframes fd-in{from{opacity:0;transform:translateY(8px) scale(.98)}to{opacity:1;transform:none}}" +
    ".fd-panel h3{margin:0;font-size:17px;font-weight:700;letter-spacing:-.01em}" +
    ".fd-blurb{margin:6px 0 0;color:var(--fd-muted);font-size:13px}" +
    ".fd-panel label{display:block;font-weight:600;margin:16px 0 6px;font-size:12px;letter-spacing:.02em;text-transform:uppercase;color:var(--fd-muted)}" +
    ".fd-panel textarea,.fd-panel select{width:100%;box-sizing:border-box;padding:10px 12px;" +
      "background:var(--fd-field);color:var(--fd-ink);border:1px solid var(--fd-border);" +
      "border-radius:calc(var(--fd-radius) * .6);font:inherit}" +
    ".fd-panel textarea{min-height:96px;resize:vertical}" +
    ".fd-panel textarea:focus,.fd-panel select:focus{outline:0;border-color:var(--fd-accent);" +
      "box-shadow:0 0 0 3px " + withAlpha(t.accent, 0.25) + "}" +
    ".fd-row{display:flex;gap:10px;justify-content:flex-end;margin-top:20px;align-items:center}" +
    ".fd-row button{padding:10px 16px;border-radius:calc(var(--fd-radius) * .6);border:0;font:600 13px/1 var(--fd-font);cursor:pointer}" +
    ".fd-cancel{background:transparent;color:var(--fd-muted)}" +
    ".fd-cancel:hover{color:var(--fd-ink)}" +
    ".fd-send{background:var(--fd-accent);color:var(--fd-accent-ink)}" +
    ".fd-send:hover{filter:brightness(1.06)}" +
    ".fd-send:disabled{opacity:.55;cursor:default}" +
    ".fd-msg{margin-right:auto;font-size:12.5px;color:var(--fd-muted)}" +
    ".fd-msg.ok{color:" + t.accent + "}.fd-msg.err{color:#e5484d}" +
    "@media (prefers-reduced-motion:reduce){.fd-launch,.fd-anim{transition:none;animation:none}}";

  function el(tag, attrs, html) {
    var e = document.createElement(tag);
    if (attrs) for (var k in attrs) e.setAttribute(k, attrs[k]);
    if (html != null) e.innerHTML = html;
    return e;
  }

  var root;

  function mount() {
    root = el("div", { id: "fd-root" });
    var style = el("style");
    style.textContent = css;
    root.appendChild(style);

    var cls = "fd-launch fd-" + (cfg.launcher === "round" || cfg.launcher === "tab" ? cfg.launcher : "pill");
    var btn = el("button", { class: cls, type: "button", "aria-label": cfg.label });
    btn.appendChild(el("span", { class: "fd-ico", "aria-hidden": "true" }, cfg.icon));
    btn.appendChild(el("span", { class: "fd-txt" }, cfg.label));
    btn.addEventListener("click", openPanel);
    root.appendChild(btn);
    document.body.appendChild(root);
  }

  function openPanel() {
    var scrim = el("div", { class: "fd-scrim" });
    var panel = el("div", {
      class: "fd-panel fd-anim",
      role: "dialog",
      "aria-modal": "true",
      "aria-label": t.title,
    });
    panel.appendChild(el("h3", null, t.title));
    if (cfg.blurb) panel.appendChild(el("p", { class: "fd-blurb" }, cfg.blurb));

    panel.appendChild(el("label", { for: "fd-message" }, "What went wrong?"));
    var msg = el("textarea", { id: "fd-message", placeholder: "Describe what you saw, and what you expected…" });
    panel.appendChild(msg);

    panel.appendChild(el("label", { for: "fd-sev" }, "How bad is it?"));
    var sev = el(
      "select",
      { id: "fd-sev" },
      '<option value="low">Minor — a small annoyance</option>' +
        '<option value="med" selected>Medium — gets in the way</option>' +
        '<option value="high">High — hard to use</option>' +
        '<option value="urgent">Urgent — completely broken</option>'
    );
    panel.appendChild(sev);

    var row = el("div", { class: "fd-row" });
    var note = el("div", { class: "fd-msg" });
    var cancel = el("button", { class: "fd-cancel", type: "button" }, "Cancel");
    var send = el("button", { class: "fd-send", type: "button" }, "Send report");
    row.appendChild(note);
    row.appendChild(cancel);
    row.appendChild(send);
    panel.appendChild(row);
    scrim.appendChild(panel);
    root.appendChild(scrim);

    function close() {
      scrim.remove();
      document.removeEventListener("keydown", onKey);
    }
    function onKey(e) {
      if (e.key === "Escape") close();
    }
    document.addEventListener("keydown", onKey);
    cancel.addEventListener("click", close);
    scrim.addEventListener("click", function (e) {
      if (e.target === scrim) close();
    });

    send.addEventListener("click", function () {
      var message = msg.value.trim();
      if (!message) {
        note.className = "fd-msg err";
        note.textContent = "Add a quick description first.";
        msg.focus();
        return;
      }
      send.disabled = true;
      note.className = "fd-msg";
      note.textContent = "Sending…";

      fetch(cfg.base + "/api/ingest/bug", {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-API-Key": cfg.key },
        body: JSON.stringify({
          site: cfg.site,
          url: location.href,
          message: message,
          severity: sev.value,
          meta: {
            userAgent: navigator.userAgent,
            referrer: document.referrer || null,
            viewport: window.innerWidth + "x" + window.innerHeight,
          },
        }),
      })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          note.className = "fd-msg ok";
          note.textContent = "Thanks — your report was filed.";
          setTimeout(close, 1200);
        })
        .catch(function (err) {
          note.className = "fd-msg err";
          note.textContent = "Could not send (" + err.message + ").";
          send.disabled = false;
        });
    });

    setTimeout(function () {
      msg.focus();
    }, 30);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", mount);
  } else {
    mount();
  }
})();
