import { describe, it, expect } from "vitest";
import { formatBytes } from "../lib/format";
import { formatMMSS } from "../hooks/useCountdown";

describe("formatBytes", () => {
  it("formats sizes", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(2048)).toBe("2.0 KB");
    expect(formatBytes(25 * 1024 * 1024)).toBe("25 MB");
  });
});

describe("formatMMSS", () => {
  it("shows minutes:seconds, and hours when needed", () => {
    expect(formatMMSS(0)).toBe("0:00");
    expect(formatMMSS(65)).toBe("1:05");
    expect(formatMMSS(3600)).toBe("1:00:00");
    expect(formatMMSS(3725)).toBe("1:02:05");
  });
});
