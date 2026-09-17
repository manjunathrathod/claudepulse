// Theme toggle: flips data-theme, persists it, and asks charts to re-read
// their CSS variables.
(function () {
  "use strict";
  var btn = document.getElementById("theme-toggle");
  if (!btn) return;
  btn.addEventListener("click", function () {
    var next = document.documentElement.getAttribute("data-theme") === "light" ? "dark" : "light";
    document.documentElement.setAttribute("data-theme", next);
    try { localStorage.setItem("cp-theme", next); } catch (e) {}
    document.dispatchEvent(new CustomEvent("cp:theme", { detail: next }));
  });
})();

// Live "resets in" countdowns on the usage strip (re-bound after htmx swaps).
(function () {
  function tick() {
    var now = Date.now();
    document.querySelectorAll(".countdown[data-reset]").forEach(function (el) {
      var ms = Date.parse(el.getAttribute("data-reset")) - now;
      if (isNaN(ms)) return;
      if (ms <= 0) { el.textContent = "now"; return; }
      var h = Math.floor(ms / 3600000), m = Math.floor(ms % 3600000 / 60000), s = Math.floor(ms % 60000 / 1000);
      el.textContent = h > 0 ? h + "h " + (m < 10 ? "0" : "") + m + "m" : m > 0 ? m + "m " + (s < 10 ? "0" : "") + s + "s" : s + "s";
    });
  }
  tick();
  setInterval(tick, 1000);
})();
