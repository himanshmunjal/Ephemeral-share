import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { api, ApiError, ownerToken, wsUrl } from "../lib/api";

function fakeStorage() {
  const m = new Map();
  return { getItem: (k) => m.get(k) ?? null, setItem: (k, v) => m.set(k, String(v)), removeItem: (k) => m.delete(k) };
}

describe("api client", () => {
  beforeEach(() => {
    vi.stubGlobal("localStorage", fakeStorage());
  });
  afterEach(() => vi.unstubAllGlobals());

  it("sends the participant id and owner token as headers (no cookies)", async () => {
    ownerToken.set("ABC", "tok-123");
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) });
    vi.stubGlobal("fetch", fetchMock);
    await api.endSession("ABC");
    const [, init] = fetchMock.mock.calls[0];
    expect(init.headers["X-Owner-Token"]).toBe("tok-123");
    expect(init.headers["X-Participant-ID"]).toMatch(/.{8,}/);
    expect(init.credentials).toBeUndefined();
  });

  it("attaches the encrypted identity when sharing", async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({}) });
    vi.stubGlobal("fetch", fetchMock);
    await api.addText("ABC", "note", "cipher", "WHO_TOKEN_1234567890");
    expect(fetchMock.mock.calls[0][1].headers["X-Participant-Who"]).toBe("WHO_TOKEN_1234567890");
  });

  it("surfaces machine-readable error codes", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({ ok: false, status: 409, json: async () => ({ error: "this session is full", code: "full" }) }));
    const err = await api.joinSession("ABC").catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err.code).toBe("full");
    expect(err.status).toBe(409);
  });

  it("turns a network failure into a friendly error", async () => {
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new TypeError("Failed to fetch")));
    const err = await api.getSession("ABC").catch((e) => e);
    expect(err.code).toBe("network");
    expect(err.message).toMatch(/connection/i);
  });

  it("builds a websocket URL, carrying the encrypted identity", () => {
    expect(wsUrl("ABC")).toMatch(/^ws:\/\/.+\/ws\/sessions\/ABC$/);
    expect(wsUrl("ABC", "WHO")).toMatch(/\/ws\/sessions\/ABC\?who=WHO$/);
  });
});
