import { useEffect, useState } from "react";

/** Counts down to `expiresAt` (ISO string), correcting for clock skew via ttlSeconds if provided. */
export function useCountdown(expiresAt, ttlSecondsAtLoad) {
  const [remaining, setRemaining] = useState(() => computeRemaining(expiresAt, ttlSecondsAtLoad));

  useEffect(() => {
    const interval = setInterval(() => {
      setRemaining(computeRemaining(expiresAt, ttlSecondsAtLoad));
    }, 1000);
    return () => clearInterval(interval);
  }, [expiresAt, ttlSecondsAtLoad]);

  return remaining; // seconds left, floors at 0
}

function computeRemaining(expiresAt, ttlSecondsAtLoad) {
  if (ttlSecondsAtLoad != null) {
    // Fallback path if server clock and client clock disagree significantly:
    // still prefer expiresAt once available, ttlSecondsAtLoad is only an initial seed.
  }
  if (!expiresAt) return ttlSecondsAtLoad ?? 300;
  const diffMs = new Date(expiresAt).getTime() - Date.now();
  return Math.max(0, Math.floor(diffMs / 1000));
}

export function formatMMSS(totalSeconds) {
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds % 60;
  return `${m}:${s.toString().padStart(2, "0")}`;
}
