// Applied synchronously, before first paint, to avoid a light-mode flash
// for users whose stored/system preference is dark. Kept as a static file
// (not inline) so the production CSP can forbid inline scripts.
(function () {
  try {
    var stored = localStorage.getItem("ephemeral-share:theme") || "system";
    var isDark = stored === "dark" || (stored === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
    if (isDark) document.documentElement.classList.add("dark");
  } catch (e) {}
})();
