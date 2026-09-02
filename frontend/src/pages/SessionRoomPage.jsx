import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { useCountdown, formatMMSS } from "../hooks/useCountdown";
import { useSessionSocket } from "../hooks/useSessionSocket";
import { importKey } from "../lib/crypto";
import { loadKey, storeKey, clearKey } from "../lib/sessionKeys";
import UploadWidget from "../components/UploadWidget.jsx";
import ContentFeedItem from "../components/ContentFeedItem.jsx";
import ConfirmDialog from "../components/ConfirmDialog.jsx";
import { useToast } from "../components/ToastProvider.jsx";

export default function SessionRoomPage() {
  const { code } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const [session, setSession] = useState(null);
  const [items, setItems] = useState([]);
  const [error, setError] = useState(null);
  const [ending, setEnding] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [encKey, setEncKey] = useState(null);
  const [manualKeyInput, setManualKeyInput] = useState("");
  const [keyMissing, setKeyMissing] = useState(false);
  const isOwner = sessionStorage.getItem(`owner:${code}`) === "1";

  const remaining = useCountdown(session?.expires_at, session?.ttl_seconds);

  // Pick up the AES key: from the URL fragment (freshly shared link) first,
  // falling back to sessionStorage (page reload in the same tab). The
  // fragment is stripped from the address bar immediately after capture so
  // the raw key never lingers in browser history, autofill, or a screen
  // share of the URL bar.
  useEffect(() => {
    (async () => {
      const hash = window.location.hash;
      let raw = hash.startsWith("#k=") ? hash.slice(3) : loadKey(code);
      if (hash.startsWith("#k=")) {
        storeKey(code, raw);
        window.history.replaceState(null, "", window.location.pathname);
      }
      if (!raw) {
        setKeyMissing(true);
        return;
      }
      try {
        setEncKey(await importKey(raw));
      } catch {
        setKeyMissing(true);
      }
    })();
  }, [code]);

  function submitManualKey(e) {
    e.preventDefault();
    const raw = manualKeyInput.trim();
    if (!raw) return;
    importKey(raw)
      .then((key) => {
        storeKey(code, raw);
        setEncKey(key);
        setKeyMissing(false);
      })
      .catch(() => toast.error("That doesn't look like a valid decryption key."));
  }

  const refreshContent = useCallback(async () => {
    try {
      const res = await api.listContent(code);
      setItems(res.items || []);
    } catch {
      // session likely expired between actions; countdown/redirect handles it
    }
  }, [code]);

  useEffect(() => {
    (async () => {
      try {
        const s = await api.getSession(code);
        if (s.type === "shared_workspace") {
          await api.joinSession(code); // registers a participant, enforces max_participants
        }
        setSession(s);
        await refreshContent();
      } catch (e) {
        setError(e.message);
      }
    })();
  }, [code, refreshContent]);

  // Live updates for Shared Workspace sessions.
  useSessionSocket(session?.type === "shared_workspace" ? code : null, (evt) => {
    if (evt.event === "content_added") {
      refreshContent();
      toast.info("New content shared", 2000);
    }
    if (evt.event === "session_expired") navigate("/", { replace: true, state: { expired: true } });
  });

  useEffect(() => {
    if (session && remaining <= 0) {
      navigate("/", { replace: true, state: { expired: true } });
    }
  }, [remaining, session, navigate]);

  function shareLink() {
    const key = manualKeyInput || loadKey(code);
    return key ? `${window.location.origin}/session/${code}#k=${key}` : `${window.location.origin}/session/${code}`;
  }

  async function copyLink() {
    try {
      await navigator.clipboard.writeText(shareLink());
      toast.success("Link copied — includes the decryption key");
    } catch {
      toast.error("Couldn't copy — copy the link from the address bar instead");
    }
  }

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(code);
      toast.success("Code copied");
    } catch {
      toast.error("Couldn't copy the code");
    }
  }

  async function nativeShare() {
    try {
      await navigator.share({ title: "ephemeral.share", text: "Join my session", url: shareLink() });
    } catch {
      // user cancelled the share sheet — no-op
    }
  }

  async function endSession() {
    setEnding(true);
    try {
      await api.endSession(code);
      sessionStorage.removeItem(`owner:${code}`);
      clearKey(code);
      navigate("/", { replace: true });
    } catch (e) {
      toast.error(e.message);
    } finally {
      setEnding(false);
      setConfirmOpen(false);
    }
  }

  if (error) {
    return (
      <div className="mx-auto max-w-md text-center">
        <p className="mb-4 text-red-600 dark:text-red-400">{error}</p>
        <button onClick={() => navigate("/")} className="text-brand-600 hover:underline dark:text-brand-500">
          Start a new session
        </button>
      </div>
    );
  }

  if (!session) {
    return (
      <div className="mx-auto max-w-2xl animate-pulse space-y-6">
        <div className="h-20 rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900" />
        <div className="h-32 rounded-xl border border-dashed border-slate-300 bg-white dark:border-slate-700 dark:bg-slate-900" />
      </div>
    );
  }

  const urgent = remaining <= 30;

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div className="flex flex-col gap-4 rounded-xl border border-slate-200 bg-white px-5 py-4 dark:border-slate-800 dark:bg-slate-900 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {session.type === "shared_workspace" ? "Shared Workspace" : "Quick Transfer"}
            <span className="inline-flex items-center gap-1 rounded-full bg-emerald-50 px-2 py-0.5 text-emerald-700 normal-case tracking-normal dark:bg-emerald-500/10 dark:text-emerald-400">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-2.5 w-2.5">
                <rect x="5" y="11" width="14" height="9" rx="2" />
                <path d="M8 11V8a4 4 0 118 0v3" />
              </svg>
              Encrypted
            </span>
          </div>
          <div className="mt-1 font-mono text-2xl font-semibold tracking-widest text-slate-800 dark:text-slate-100">{code}</div>
          <div className="mt-1.5 flex flex-wrap gap-3 text-xs font-medium">
            <button onClick={copyLink} className="text-brand-600 hover:underline dark:text-brand-500">
              Copy link
            </button>
            <button onClick={copyCode} className="text-slate-500 hover:underline dark:text-slate-400">
              Copy code only
            </button>
            {typeof navigator !== "undefined" && navigator.share && (
              <button onClick={nativeShare} className="text-slate-500 hover:underline dark:text-slate-400">
                Share…
              </button>
            )}
          </div>
        </div>
        <div className="flex items-center gap-4">
          <div className={`font-mono text-2xl font-semibold ${urgent ? "text-red-600 dark:text-red-400" : "text-slate-700 dark:text-slate-200"}`}>
            {formatMMSS(remaining)}
          </div>
          {isOwner && (
            <button
              onClick={() => setConfirmOpen(true)}
              className="text-sm font-medium text-red-600 hover:underline dark:text-red-400"
            >
              End session
            </button>
          )}
        </div>
      </div>

      {urgent && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/30 dark:text-red-300">
          This session is about to expire — save anything you need now.
        </div>
      )}

      {keyMissing && (
        <div className="space-y-3 rounded-xl border border-amber-200 bg-amber-50 px-4 py-3.5 text-sm dark:border-amber-900 dark:bg-amber-900/20">
          <p className="text-amber-800 dark:text-amber-300">
            No decryption key found for this session. If you were only given the code, ask the sender for the full
            link, or paste the key below.
          </p>
          <form onSubmit={submitManualKey} className="flex gap-2">
            <input
              value={manualKeyInput}
              onChange={(e) => setManualKeyInput(e.target.value)}
              placeholder="Paste decryption key"
              className="flex-1 rounded-lg border border-amber-300 bg-white px-3 py-1.5 text-sm dark:border-amber-800 dark:bg-slate-900"
            />
            <button type="submit" className="rounded-lg bg-amber-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-amber-700">
              Unlock
            </button>
          </form>
        </div>
      )}

      <UploadWidget code={code} encKey={encKey} onUploaded={refreshContent} />

      <div className="space-y-2">
        <h2 className="text-sm font-medium text-slate-500 dark:text-slate-400">Shared content ({items.length})</h2>
        {items.length === 0 && (
          <p className="rounded-lg border border-dashed border-slate-200 py-6 text-center text-sm text-slate-400 dark:border-slate-800 dark:text-slate-500">
            Nothing shared yet — drop a file or paste a note above.
          </p>
        )}
        {items.map((item) => (
          <ContentFeedItem key={item.id} item={item} code={code} encKey={encKey} />
        ))}
      </div>

      <ConfirmDialog
        open={confirmOpen}
        title="End this session now?"
        description="Everyone loses access immediately and all content is permanently deleted. This can't be undone."
        confirmLabel="End session"
        danger
        busy={ending}
        onConfirm={endSession}
        onCancel={() => setConfirmOpen(false)}
      />
    </div>
  );
}
