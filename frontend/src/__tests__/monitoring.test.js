import { describe, it, expect } from "vitest";
import { scrub, beforeSend } from "../lib/monitoring";

describe("error-report scrubbing", () => {
  it("removes access codes from URLs and queries", () => {
    expect(scrub("https://app.example/session/K7QX3M9A?x=1")).toBe("https://app.example/session/:code?x=1");
    expect(scrub("GET /api/v1/sessions/K7QX3M9A/content")).toBe("GET /api/v1/sessions/:code/content");
    expect(scrub("wss://api/ws/sessions/K7QX3M9A?who=AbCdEf123")).not.toContain("AbCdEf123");
  });

  it("drops request data and cleans every string before sending", () => {
    const event = {
      request: { url: "https://app.example/session/K7QX3M9A" },
      user: { ip_address: "1.2.3.4" },
      transaction: "/session/K7QX3M9A",
      message: "failed /sessions/K7QX3M9A",
      exception: { values: [{ value: "fetch /sessions/K7QX3M9A/join failed" }] },
      tags: { page: "/session/K7QX3M9A" },
    };
    const out = beforeSend(event);
    expect(out.request).toBeUndefined();
    expect(out.user).toBeUndefined();
    expect(JSON.stringify(out)).not.toContain("K7QX3M9A");
  });
});
