import { useCallback, useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api } from "../lib/api";
import { useCountdown, formatMMSS } from "../hooks/useCountdown";
import { useSessionSocket } from "../hooks/useSessionSocket";
import { deriveKeyFromCode } from "../lib/crypto";
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
  const isOwner = sessionStorage.getItem(`owner:${code}`) === "1";

  const remaining = useCountdown(session?.expires_at, session?.ttl_seconds);

  // The AES key is derived from the access code alone, so there's nothing
  // extra to carry around or paste in.
  useEffect(() => {
    deriveKeyFromCode(code).then(setEncKey);
  }, [code]);

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

  // Live updates: new content appears and session termination is reflected
  // for everyone in the session without a reload.
  useSessionSocket(
    session ? code : null,
    (evt) => {
      if (evt.event === "content_added") {
        refreshContent();
        toast.info("New content shared", 2000);
      }
      if (evt.event === "session_ended") {
        if (!isOwner) toast.info("The host ended this session");
        navigate("/", { replace: true, state: { expired: true } });
      }
      if (evt.event === "session_expired") {
        toast.info("This session has expired");
        navigate("/", { replace: true, state: { expired: true } });
      }
    },
    refreshContent
  );

  useEffect(() => {
    if (session && remaining <= 0) {
      navigate("/", { replace: true, state: { expired: true } });
    }
  }, [remaining, session, navigate]);

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(code);
      toast.success("Code copied");
    } catch {
      toast.error("Couldn't copy the code");
    }
  }

  async function endSession() {
    setEnding(true);
    try {
      await api.endSession(code);
      sessionStorage.removeItem(`owner:${code}`);
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
          </div>
          <div className="mt-1 font-mono text-2xl font-semibold tracking-widest text-slate-800 dark:text-slate-100">{code}</div>
          <div className="mt-1.5 flex flex-wrap gap-3 text-xs font-medium">
            <button onClick={copyCode} className="text-brand-600 hover:underline dark:text-brand-500">
              Copy code
            </button>
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
