// Chart bootstrapping. Data comes from <script type="application/json"> blocks
// rendered by the server; nothing is computed client-side.
(function () {
  "use strict";

  function cssVar(name) {
    return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
  }

  function compact(n) {
    if (n < 1e4) return n.toLocaleString();
    if (n < 1e6) return (n / 1e3).toFixed(1).replace(/\.0$/, "") + "K";
    if (n < 1e9) return (n / 1e6).toFixed(1).replace(/\.0$/, "") + "M";
    return (n / 1e9).toFixed(2).replace(/\.?0+$/, "") + "B";
  }

  function readJSON(id) {
    var el = document.getElementById(id);
    if (!el) return null;
    try { return JSON.parse(el.textContent); } catch (e) { return null; }
  }

  function dailyChart() {
    var data = readJSON("daily-data");
    var canvas = document.getElementById("daily-chart");
    if (!data || !canvas || typeof Chart === "undefined") return;

    var text2 = cssVar("--text-2"), text3 = cssVar("--text-3"), grid = cssVar("--grid");
    var font = cssVar("--font") || "Inter, system-ui, sans-serif";
    var mono = cssVar("--mono") || "ui-monospace, monospace";
    Chart.defaults.font.family = font;
    Chart.defaults.color = text3;

    var datasets = data.datasets.map(function (d) {
      return {
        label: d.label,
        data: d.data,
        backgroundColor: d.color,
        hoverBackgroundColor: d.color,
        borderColor: cssVar("--surface-solid"),
        borderWidth: { top: 2, right: 0, bottom: 0, left: 0 }, // 2px surface gap between stacked segments
        borderSkipped: false,
        borderRadius: 4,
        maxBarThickness: 22
      };
    });

    var labels = data.labels.map(function (d) {
      var dt = new Date(d + "T00:00:00Z");
      return dt.toLocaleDateString(undefined, { month: "short", day: "numeric", timeZone: "UTC" });
    });

    new Chart(canvas, {
      type: "bar",
      data: { labels: labels, datasets: datasets },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        animation: { duration: 300 },
        interaction: { mode: "index", intersect: false },
        plugins: {
          legend: {
            display: datasets.length > 1,
            position: "top",
            align: "end",
            labels: { boxWidth: 10, boxHeight: 10, borderRadius: 3, useBorderRadius: true, padding: 14, color: text2 }
          },
          tooltip: {
            backgroundColor: cssVar("--surface-solid"),
            titleColor: cssVar("--text-1"),
            bodyColor: text2,
            borderColor: cssVar("--border-strong"),
            borderWidth: 1,
            padding: 10,
            cornerRadius: 8,
            bodyFont: { family: mono },
            callbacks: {
              label: function (ctx) { return " " + ctx.dataset.label + ": " + ctx.parsed.y.toLocaleString(); },
              footer: function (items) {
                var total = items.reduce(function (s, i) { return s + i.parsed.y; }, 0);
                return items.length > 1 ? "Total: " + total.toLocaleString() : "";
              }
            }
          }
        },
        scales: {
          x: {
            stacked: true,
            grid: { display: false },
            border: { color: grid },
            ticks: { maxTicksLimit: 10, maxRotation: 0, autoSkip: true, font: { size: 11 } }
          },
          y: {
            stacked: true,
            beginAtZero: true,
            grid: { color: grid, lineWidth: 1, drawTicks: false },
            border: { display: false },
            ticks: { maxTicksLimit: 5, padding: 8, font: { family: mono, size: 11 }, callback: function (v) { return compact(v); } }
          }
        }
      }
    });
  }

  // Session timeline: one series (output tokens per bucket), so no legend; the
  // tooltip carries prompts / replies / tool calls for the same bucket.
  function timelineChart() {
    var data = readJSON("timeline-data");
    var canvas = document.getElementById("timeline-chart");
    if (!data || !canvas || typeof Chart === "undefined") return;

    var text2 = cssVar("--text-2"), text3 = cssVar("--text-3"), grid = cssVar("--grid");
    var mono = cssVar("--mono") || "ui-monospace, monospace";
    Chart.defaults.font.family = cssVar("--font") || "Inter, system-ui, sans-serif";
    Chart.defaults.color = text3;

    var buckets = data.buckets;
    new Chart(canvas, {
      type: "bar",
      data: {
        labels: buckets.map(function (b) { return b.label; }),
        datasets: [{
          label: "Output tokens",
          data: buckets.map(function (b) { return b.output; }),
          backgroundColor: data.color,
          hoverBackgroundColor: data.color,
          borderRadius: 4,
          borderSkipped: "bottom",
          maxBarThickness: 22
        }]
      },
      options: {
        responsive: true,
        maintainAspectRatio: false,
        animation: { duration: 300 },
        interaction: { mode: "index", intersect: false },
        plugins: {
          legend: { display: false },
          tooltip: {
            backgroundColor: cssVar("--surface-solid"),
            titleColor: cssVar("--text-1"),
            bodyColor: text2,
            borderColor: cssVar("--border-strong"),
            borderWidth: 1,
            padding: 10,
            cornerRadius: 8,
            bodyFont: { family: mono },
            callbacks: {
              label: function (ctx) {
                var b = buckets[ctx.dataIndex];
                var lines = [" output " + b.output.toLocaleString()];
                if (b.prompts) lines.push(" prompts " + b.prompts);
                if (b.replies) lines.push(" replies " + b.replies);
                if (b.subagent) lines.push(" subagent replies " + b.subagent);
                if (b.tools) lines.push(" tool calls " + b.tools);
                return lines;
              }
            }
          }
        },
        scales: {
          x: { grid: { display: false }, border: { color: grid }, ticks: { maxTicksLimit: 12, maxRotation: 0, autoSkip: true, font: { size: 11 } } },
          y: { beginAtZero: true, grid: { color: grid, lineWidth: 1, drawTicks: false }, border: { display: false },
               ticks: { maxTicksLimit: 5, padding: 8, font: { family: mono, size: 11 }, callback: function (v) { return compact(v); } } }
        }
      }
    });
  }

  function init() { dailyChart(); timelineChart(); }
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
