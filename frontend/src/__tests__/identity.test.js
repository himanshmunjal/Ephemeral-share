import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  generateIdentity,
  cleanName,
  initials,
  getIdentity,
  setIdentityName,
  encryptIdentity,
  decryptIdentity,
  ADJECTIVES,
  ANIMALS,
  AVATAR_COLORS,
  MAX_NAME_LENGTH,
} from "../lib/identity";
import { deriveKeyFromCode, encryptJSONUrlSafe } from "../lib/crypto";

function fakeStorage() {
  const m = new Map();
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}

describe("anonymous identity", () => {
  beforeEach(() => vi.stubGlobal("localStorage", fakeStorage()));

  it("generates friendly names with a valid colour", () => {
    for (let i = 0; i < 50; i++) {
      const { name, color } = generateIdentity();
      const [adj, animal] = name.split(" ");
      expect(ADJECTIVES).toContain(adj);
      expect(ANIMALS).toContain(animal);
      expect(color).toBeGreaterThanOrEqual(0);
      expect(color).toBeLessThan(AVATAR_COLORS.length);
    }
  });

  it("never uses an orange avatar colour", () => {
    // Guards the palette decision: hue of orange is roughly 15–45°.
    for (const hex of AVATAR_COLORS) {
      const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
      const orangey = r > 200 && g > 90 && g < 180 && b < 90;
      expect(orangey).toBe(false);
    }
  });

  it("persists one identity per browser and lets the name change", () => {
    const first = getIdentity();
    expect(getIdentity()).toEqual(first);
    const renamed = setIdentityName("  Captain   Pancake ");
    expect(renamed.name).toBe("Captain Pancake");
    expect(renamed.color).toBe(first.color);
    expect(getIdentity().name).toBe("Captain Pancake");
    expect(setIdentityName("   ").name).toBe("Captain Pancake"); // empty is ignored
  });

  it("sanitises names", () => {
    expect(cleanName("a\u0000b\nc")).toBe("abc");
    expect(Array.from(cleanName("x".repeat(100))).length).toBe(MAX_NAME_LENGTH);
    expect(cleanName(42)).toBe("");
    expect(initials("Swift Otter")).toBe("SO");
    expect(initials("Cher")).toBe("C");
    expect(initials("")).toBe("?");
  });

  it("round-trips through encryption, and the server-visible form hides the name", async () => {
    const key = await deriveKeyFromCode("K7QX3M9A");
    const who = await encryptIdentity(key, { name: "Swift Otter", color: 3 });
    expect(who).toMatch(/^[A-Za-z0-9_-]{16,300}$/); // passes the server's shape check
    expect(who).not.toContain("Swift");
    expect(await decryptIdentity(key, who)).toEqual({ name: "Swift Otter", color: 3 });
  });

  it("returns null for the wrong key and never trusts a malicious payload", async () => {
    const key = await deriveKeyFromCode("K7QX3M9A");
    const other = await deriveKeyFromCode("ZZZZZZZZ");
    const who = await encryptIdentity(key, { name: "Swift Otter", color: 3 });
    expect(await decryptIdentity(other, who)).toBeNull();
    expect(await decryptIdentity(key, "")).toBeNull();

    const evil = await encryptJSONUrlSafe(key, { n: "<img src=x onerror=alert(1)>".repeat(10), c: 999 });
    const out = await decryptIdentity(key, evil);
    expect(out.color).toBe(0); // out-of-range colour falls back
    expect(Array.from(out.name).length).toBeLessThanOrEqual(MAX_NAME_LENGTH);
  });
});
