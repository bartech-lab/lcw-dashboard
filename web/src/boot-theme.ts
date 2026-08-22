// Runs before first paint. Auto mode sets no attribute at all, so the media
// query governs with zero JavaScript and cannot flash. Density is applied here
// too because it changes row height.
try {
  const p = JSON.parse(localStorage.getItem("lcwd:prefs") || "{}");
  if (p.theme === "light" || p.theme === "dark") {
    document.documentElement.dataset.theme = p.theme;
  }
  if (p.density) document.documentElement.dataset.density = p.density;
} catch {
  /* a corrupt blob must not stop the page loading */
}
