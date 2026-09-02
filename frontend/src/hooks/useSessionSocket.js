import { useEffect, useRef } from "react";
import { wsUrl } from "../lib/api";

/**
 * Opens a WebSocket for a Shared Workspace session and dispatches events
 * (content_added, participant_joined, session_expired, ...) to the caller.
 */
export function useSessionSocket(code, onEvent) {
  const socketRef = useRef(null);

  useEffect(() => {
    if (!code) return;
    const socket = new WebSocket(wsUrl(code));
    socketRef.current = socket;

    socket.onmessage = (msg) => {
      try {
        onEvent(JSON.parse(msg.data));
      } catch {
        // ignore malformed frames
      }
    };
    socket.onerror = () => {};

    return () => socket.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code]);

  return socketRef;
}
