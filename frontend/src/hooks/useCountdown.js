import { useEffect, useState } from "react";

/**
 * Counts down to a deadline. Pass `deadlineMs` (an epoch-ms timestamp computed
 * on THIS device as Date.now() + the server's ttl_seconds) so a wrong device
 * clock can't make the timer lie.
 */
export function useCountdown(deadlineMs) {
  const [remaining, setRemaining] = useState(() => compute(deadlineMs));

  useEffect(() => {
    setRemaining(compute(deadlineMs));
    const interval = setInterval(() => setRemaining(compute(deadlineMs)), 1000);
    return () => clearInterval(interval);
  }, [deadlineMs]);

  return remaining; // seconds left, floors at 0; null until a deadline is known
}

function compute(deadlineMs) {
  if (!deadlineMs) return null;
  return Math.max(0, Math.ceil((deadlineMs - Date.now()) / 1000));
}

export function formatMMSS(totalSeconds) {
  const h = Math.floor(totalSeconds / 3600);
  const m = Math.floor((totalSeconds % 3600) / 60);
  const s = totalSeconds % 60;
  const pad = (n) => n.toString().padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(s)}` : `${m}:${pad(s)}`;
}
