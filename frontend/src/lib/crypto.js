// Client-side E2E encryption. The AES key is generated in the browser,
// travels only in the URL fragment (never sent to the server or logged in
// any request), and every file/text payload is encrypted before it leaves
// the tab. The server only ever sees ciphertext.
const AES_ALGO = { name: "AES-GCM", length: 256 };
const IV_BYTES = 12;

export async function generateSessionKey() {
  return crypto.subtle.generateKey(AES_ALGO, true, ["encrypt", "decrypt"]);
}

export async function exportKey(key) {
  const raw = await crypto.subtle.exportKey("raw", key);
  return bytesToBase64Url(new Uint8Array(raw));
}

export async function importKey(base64url) {
  const raw = base64UrlToBytes(base64url);
  return crypto.subtle.importKey("raw", raw, AES_ALGO, true, ["encrypt", "decrypt"]);
}

export async function encryptBytes(key, plainBuffer) {
  const iv = crypto.getRandomValues(new Uint8Array(IV_BYTES));
  const cipher = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, key, plainBuffer);
  const out = new Uint8Array(IV_BYTES + cipher.byteLength);
  out.set(iv);
  out.set(new Uint8Array(cipher), IV_BYTES);
  return out;
}

export async function decryptBytes(key, combined) {
  const bytes = combined instanceof Uint8Array ? combined : new Uint8Array(combined);
  const iv = bytes.subarray(0, IV_BYTES);
  const cipher = bytes.subarray(IV_BYTES);
  return crypto.subtle.decrypt({ name: "AES-GCM", iv }, key, cipher);
}

// Standard base64 — used for fields that travel inside JSON bodies, where
// URL-safety doesn't matter.
export async function encryptText(key, text) {
  const bytes = await encryptBytes(key, new TextEncoder().encode(text));
  return bytesToBase64(bytes);
}

export async function decryptText(key, base64) {
  const plain = await decryptBytes(key, base64ToBytes(base64));
  return new TextDecoder().decode(plain);
}

// URL-safe base64 — used for the filename/mime envelope, which doubles as
// an S3 object-key path segment, and for the key itself in the URL fragment.
export async function encryptTextUrlSafe(key, text) {
  const bytes = await encryptBytes(key, new TextEncoder().encode(text));
  return bytesToBase64Url(bytes);
}

export async function decryptTextUrlSafe(key, base64url) {
  const plain = await decryptBytes(key, base64UrlToBytes(base64url));
  return new TextDecoder().decode(plain);
}

export async function encryptJSONUrlSafe(key, obj) {
  return encryptTextUrlSafe(key, JSON.stringify(obj));
}

export async function decryptJSONUrlSafe(key, base64url) {
  return JSON.parse(await decryptTextUrlSafe(key, base64url));
}

function bytesToBase64(bytes) {
  let binary = "";
  for (let i = 0; i < bytes.length; i++) binary += String.fromCharCode(bytes[i]);
  return btoa(binary);
}

function base64ToBytes(b64) {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return bytes;
}

function bytesToBase64Url(bytes) {
  return bytesToBase64(bytes).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function base64UrlToBytes(b64url) {
  const pad = b64url.length % 4 === 0 ? "" : "=".repeat(4 - (b64url.length % 4));
  const b64 = b64url.replace(/-/g, "+").replace(/_/g, "/") + pad;
  return base64ToBytes(b64);
}
