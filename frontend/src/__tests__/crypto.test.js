import { describe, it, expect } from "vitest";
import {
  deriveKeyFromCode,
  encryptBytes,
  decryptBytes,
  encryptText,
  decryptText,
  encryptJSONUrlSafe,
  decryptJSONUrlSafe,
} from "../lib/crypto";

describe("client-side encryption", () => {
  it("round-trips text, bytes and JSON", async () => {
    const key = await deriveKeyFromCode("K7QX3M9A");
    expect(await decryptText(key, await encryptText(key, "héllo — 你好 🔒"))).toBe("héllo — 你好 🔒");

    const bytes = crypto.getRandomValues(new Uint8Array(5000));
    const back = new Uint8Array(await decryptBytes(key, await encryptBytes(key, bytes)));
    expect(back).toEqual(bytes);

    const meta = { name: "résumé.pdf", type: "application/pdf" };
    expect(await decryptJSONUrlSafe(key, await encryptJSONUrlSafe(key, meta))).toEqual(meta);
  });

  it("is deterministic per code, so every participant derives the same key", async () => {
    const a = await deriveKeyFromCode("SAMECODE");
    const b = await deriveKeyFromCode("SAMECODE");
    expect(await decryptText(b, await encryptText(a, "shared"))).toBe("shared");
  });

  it("cannot be decrypted with a different code", async () => {
    const right = await deriveKeyFromCode("RIGHTCODE");
    const wrong = await deriveKeyFromCode("WRONGCODE");
    const cipher = await encryptText(right, "secret");
    await expect(decryptText(wrong, cipher)).rejects.toThrow();
  });

  it("uses a fresh IV every time and detects tampering", async () => {
    const key = await deriveKeyFromCode("K7QX3M9A");
    const one = await encryptText(key, "same input");
    const two = await encryptText(key, "same input");
    expect(one).not.toBe(two);

    const tampered = Uint8Array.from(atob(one), (c) => c.charCodeAt(0));
    tampered[tampered.length - 1] ^= 0xff;
    await expect(decryptBytes(key, tampered)).rejects.toThrow();
  });

  it("emits URL-safe output for filenames (used inside object keys)", async () => {
    const key = await deriveKeyFromCode("K7QX3M9A");
    const out = await encryptJSONUrlSafe(key, { name: "a/b+c=.txt", type: "x" });
    expect(out).toMatch(/^[A-Za-z0-9_-]+$/);
  });
});
