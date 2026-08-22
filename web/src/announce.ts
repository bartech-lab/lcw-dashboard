// Exactly two live regions. More than two and announcements collide.
export function announce(msg: string, assertive = false): void {
  const el = document.getElementById(assertive ? "live-assertive" : "live-polite");
  if (!el) return;
  // Clearing first forces a re-announcement of identical text.
  el.textContent = "";
  setTimeout(() => { el.textContent = msg; }, 30);
}
