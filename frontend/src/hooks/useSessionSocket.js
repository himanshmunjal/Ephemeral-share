import { useEffect, useRef } from "react";
import { wsUrl } from "../lib/api";

const TERMINAL_EVENTS = new Set(["session_expired", "session_ended"]);

/**
 * Opens a WebSocket for a session and dispatches events (content_added,
 * session_expired, session_ended, ...) to the caller. Reconnects with backoff
 * if the connection drops, and calls onReconnect so the caller can catch up
 * on anything it missed while disconnected.
 */
export function useSessionSocket(code, onEvent, onReconnect) {
  const handlersRef = useRef({ onEvent, onReconnect });
  handlersRef.current = { onEvent, onReconnect };

  useEffect(() => {
    if (!code) return;
    let socket;
    let retryTimer;
    let attempt = 0;
    let stopped = false;

    function connect() {
      socket = new WebSocket(wsUrl(code));

      socket.onopen = () => {
        if (attempt > 0) handlersRef.current.onReconnect?.();
        attempt = 0;
      };

      socket.onmessage = (msg) => {
        let evt;
        try {
          evt = JSON.parse(msg.data);
        } catch {
          return; // ignore malformed frames
        }
        if (TERMINAL_EVENTS.has(evt.event)) stopped = true;
        handlersRef.current.onEvent(evt);
      };

      socket.onclose = () => {
        if (stopped) return;
        attempt += 1;
        retryTimer = setTimeout(connect, Math.min(1000 * 2 ** (attempt - 1), 10000));
      };
    }

    connect();

    return () => {
      stopped = true;
      clearTimeout(retryTimer);
      socket?.close();
    };
  }, [code]);
}
