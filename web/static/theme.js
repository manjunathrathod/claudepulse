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
