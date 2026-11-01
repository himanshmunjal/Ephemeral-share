// Optional Sentry error tracking (free tier). Enabled only when
// VITE_SENTRY_DSN is set at build time.
//
// The session URL (/session/CODE) contains the access code, which is also the
// encryption key — it must never be sent to a third party. So: no breadcrumbs
// (they record navigation URLs), no request data, no PII, no tracing/replay,
// and every string in an event is scrubbed before it leaves the browser.
import * as Sentry from "@sentry/browser";

const CODE_PATTERNS = [
  [/\/session\/[^/?#\s"']+/g, "/session/:code"],
  [/\/sessions\/[^/?#\s"']+/g, "/sessions/:code"],
  [/([?&](?:who|code)=)[^&\s"']+/gi, "$1REDACTED"],
];

export function scrub(value) {
  if (typeof value !== "string") return value;
  return CODE_PATTERNS.reduce((s, [re, rep]) => s.replace(re, rep), value);
}

function scrubDeep(obj, depth = 0) {
  if (!obj || typeof obj !== "object" || depth > 6) return;
  for (const k of Object.keys(obj)) {
    if (typeof obj[k] === "string") obj[k] = scrub(obj[k]);
    else scrubDeep(obj[k], depth + 1);
  }
}

export function beforeSend(event) {
  delete event.request; // URL, headers, referrer, cookies
  delete event.user;
  event.transaction = scrub(event.transaction);
  if (event.message) event.message = scrub(event.message);
  scrubDeep(event.exception);
  scrubDeep(event.contexts);
  scrubDeep(event.tags);
  scrubDeep(event.extra);
  return event;
}

export function initMonitoring() {
  const dsn = import.meta.env.VITE_SENTRY_DSN;
  if (!dsn) return false;
  Sentry.init({
    dsn,
    environment: import.meta.env.MODE,
    sendDefaultPii: false,
    maxBreadcrumbs: 0,
    tracesSampleRate: 0,
    // Drop the integrations that capture URLs/DOM; keep global error handlers.
    integrations: (defaults) => defaults.filter((i) => !["Breadcrumbs", "HttpContext"].includes(i.name)),
    beforeSend,
    beforeBreadcrumb: () => null,
  });
  return true;
}

export function reportError(error) {
  if (import.meta.env.VITE_SENTRY_DSN) Sentry.captureException(error);
}
