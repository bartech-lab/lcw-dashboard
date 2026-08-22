import type { Alert } from "./types";
import * as prefs from "./prefs";
import * as fmt from "./format";

/**
 * Browser notifications are an enhancement, not the delivery path. Chromium
 * buffers SSE to hidden tabs and flushes on focus, so an alert can arrive just
 * after the tab becomes visible. Gating on document.hidden here would drop
 * exactly the alerts that mattered, so it is never checked.
 *
 * The Go process notifies the desktop directly, which is what works when the tab
 * is frozen or closed.
 */
export function installNotifications(): void {
  if (!("Notification" in window)) return;

  // Permission must be requested from a user gesture, so the first click asks.
  const ask = () => {
    if (Notification.permission === "default" && !prefs.notifRequested.value) {
      prefs.notifRequested.value = true;
      void Notification.requestPermission();
    }
    document.removeEventListener("click", ask);
  };
  document.addEventListener("click", ask, { once: true });
}

export function showAlert(a: Alert): void {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  const at = fmt.clockTime(a.firedAt, prefs.locale.value);
  try {
    new Notification(a.ruleName || `${a.code} alert`, {
      // The server's fired-at time, not now: a flushed alert must not claim to
      // have just happened.
      body: `${a.message} (at ${at})`,
      tag: a.ruleId + a.code,
    });
  } catch { /* some platforms require a service worker */ }
}
