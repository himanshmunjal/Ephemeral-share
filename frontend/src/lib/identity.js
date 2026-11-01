// Anonymous display identity. There are no accounts, so nobody's "real"
// identity exists to look up — each browser just gets a friendly random name
// ("Swift Otter") and an avatar colour, editable at any time. It lives in
// localStorage, never contains personal data, and is sent to the server only
// as ciphertext (encrypted with the session key, like filenames and notes),
// so the server can't read it either.
import { encryptJSONUrlSafe, decryptJSONUrlSafe } from "./crypto";

export const ADJECTIVES = [
  "Swift", "Quiet", "Brave", "Curious", "Gentle", "Lucky", "Mellow", "Nimble", "Plucky", "Sleepy",
  "Sunny", "Witty", "Zesty", "Cosy", "Dapper", "Jolly", "Breezy", "Clever", "Humble", "Spry",
];
export const ANIMALS = [
  "Otter", "Heron", "Badger", "Finch", "Lynx", "Panda", "Gecko", "Marmot", "Puffin", "Fox",
  "Newt", "Owl", "Seal", "Wren", "Yak", "Ibex", "Koala", "Moth", "Quokka", "Tapir",
];
// Deliberately no orange; matches the ink-green palette.
export const AVATAR_COLORS = ["#1f6f5c", "#6b4e9b", "#2f6690", "#a23b5a", "#6a7d2a", "#8a5a44", "#3b7f8c", "#7a4b8c"];

const STORAGE_KEY = "ephemeral-share:identity";
export const MAX_NAME_LENGTH = 24;

function randomInt(n) {
  const buf = new Uint32Array(1);
  crypto.getRandomValues(buf);
  return buf[0] % n;
}

export function generateIdentity() {
  return {
    name: `${ADJECTIVES[randomInt(ADJECTIVES.length)]} ${ANIMALS[randomInt(ANIMALS.length)]}`,
    color: randomInt(AVATAR_COLORS.length),
  };
}

/** Trims, collapses whitespace, strips control characters, caps the length. */
export function cleanName(raw) {
  if (typeof raw !== "string") return "";
  // eslint-disable-next-line no-control-regex
  const s = raw.replace(/[\u0000-\u001f\u007f]/g, "").replace(/\s+/g, " ").trim();
  return Array.from(s).slice(0, MAX_NAME_LENGTH).join("");
}

function validColor(c) {
  return Number.isInteger(c) && c >= 0 && c < AVATAR_COLORS.length;
}

export function getIdentity() {
  try {
    const saved = JSON.parse(localStorage.getItem(STORAGE_KEY) || "null");
    const name = cleanName(saved?.name);
    if (name && validColor(saved?.color)) return { name, color: saved.color };
  } catch {
    /* fall through and create one */
  }
  const fresh = generateIdentity();
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(fresh));
  } catch {
    /* private mode: identity just won't persist across reloads */
  }
  return fresh;
}

export function setIdentityName(name) {
  const current = getIdentity();
  const next = { ...current, name: cleanName(name) || current.name };
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
  } catch {
    /* ignore */
  }
  return next;
}

export function initials(name) {
  const parts = cleanName(name).split(" ").filter(Boolean);
  if (parts.length === 0) return "?";
  const letters = parts.length === 1 ? [parts[0]] : [parts[0], parts[parts.length - 1]];
  return letters.map((p) => Array.from(p)[0]).join("").toUpperCase();
}

export function encryptIdentity(key, identity) {
  return encryptJSONUrlSafe(key, { n: identity.name, c: identity.color });
}

/** Returns { name, color } or null if it can't be decrypted. Never trusts the payload's shape. */
export async function decryptIdentity(key, who) {
  if (!key || !who) return null;
  try {
    const o = await decryptJSONUrlSafe(key, who);
    return { name: cleanName(o?.n) || "Guest", color: validColor(o?.c) ? o.c : 0 };
  } catch {
    return null;
  }
}
