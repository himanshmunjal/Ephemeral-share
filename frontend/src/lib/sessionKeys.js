// Where the per-session AES key lives on this device: sessionStorage only
// (never sent to the server, never in localStorage/cookies). Keyed by code
// so multiple tabs/sessions in one browser don't collide.
const prefix = "ephemeral-share:key:";

export function storeKey(code, base64urlKey) {
  sessionStorage.setItem(prefix + code, base64urlKey);
}

export function loadKey(code) {
  return sessionStorage.getItem(prefix + code);
}

export function clearKey(code) {
  sessionStorage.removeItem(prefix + code);
}
