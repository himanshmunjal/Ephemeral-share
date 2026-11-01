import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { api, ownerToken } from "../lib/api";
import { useCountdown, formatMMSS } from "../hooks/useCountdown";
import { useSessionSocket } from "../hooks/useSessionSocket";
import { deriveKeyFromCode } from "../lib/crypto";
import { getIdentity, setIdentityName, encryptIdentity, decryptIdentity, MAX_NAME_LENGTH } from "../lib/identity";
import Avatar from "../components/Avatar.jsx";
import UploadWidget from "../components/UploadWidget.jsx";
import ContentFeedItem from "../components/ContentFeedItem.jsx";
import ConfirmDialog from "../components/ConfirmDialog.jsx";
import SessionGone from "../components/SessionGone.jsx";
import { useToast } from "../components/ToastProvider.jsx";

export default function SessionRoomPage() {
  const { code } = useParams();
  const navigate = useNavigate();
  const toast = useToast();
  const [session, setSession] = useState(null);
  const [items, setItems] = useState([]);
  const [gone, setGone] = useState(null); // { kind, message } -> friendly end-state screen
  const [ending, setEnding] = useState(false);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [encKey, setEncKey] = useState(null);
  const [online, setOnline] = useState(null);
  const [people, setPeople] = useState([]); // [{ id, name, color }]
  const [me, setMe] = useState(getIdentity);
  const [who, setWho] = useState(null); // my identity, encrypted for the server
  const [editingName, setEditingName] = useState(false);
  const [reloadTick, setReloadTick] = useState(0);
  const isOwner = !!ownerToken.get(code);

  // Deadline is anchored to THIS device's clock using the server-reported
  // time-to-live, so a wrong system clock can't skew the countdown.
  const deadline = useMemo(
    () => (session ? Date.now() + session.ttl_seconds * 1000 : null),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [session?.code, session?.ttl_seconds]
  );
  const remaining = useCountdown(deadline);

  // The AES key is derived from the access code alone, so there's nothing
  // extra to carry around or paste in.
  useEffect(() => {
    deriveKeyFromCode(code).then(setEncKey);
  }, [code]);

  // The display identity is encrypted with the session key before it leaves
  // the browser, so the server only ever relays ciphertext.
  useEffect(() => {
    if (!encKey) return;
    let cancelled = false;
    encryptIdentity(encKey, me).then((w) => !cancelled && setWho(w));
    return () => {
      cancelled = true;
    };
  }, [encKey, me]);

  async function applyPresence(evt) {
    setOnline(evt.count);
    const list = await Promise.all(
      (evt.users || []).map(async (u) => ({ id: u.id, ...((await decryptIdentity(encKey, u.who)) ?? { name: "Guest", color: null }) }))
    );
    setPeople(list);
  }

  function commitName(value) {
    setEditingName(false);
    setMe(setIdentityName(value));
  }

  const refreshContent = useCallback(async () => {
    try {
      const res = await api.listContent(code);
      setItems(res.items || []);
    } catch {
      // session likely expired between actions; countdown/socket handles it
    }
  }, [code]);

  useEffect(() => {
    let cancelled = false;
    setGone(null);
    (async () => {
      try {
        // Fetch the session and its feed together: each call is a network round
        // trip to the server, so doing them one after another tripled the load
        // time. The feed is best-effort (an expired session fails getSession).
        const [s, feed] = await Promise.all([api.getSession(code), api.listContent(code).catch(() => null)]);
        if (cancelled) return;
        // getSession already reports who's online and the cap, so no separate
        // join pre-check call is needed (the socket enforces the cap atomically).
        if (s.online >= s.max_participants) {
          setGone({ kind: "full" });
          return;
        }
        setSession(s);
        setOnline(s.online);
        if (feed) setItems(feed.items || []);
      } catch (e) {
        if (cancelled) return;
        if (e.code === "not_found" || e.status === 404 || e.status === 410) setGone({ kind: "not_found" });
        else if (e.code === "full") setGone({ kind: "full" });
        else if (e.code === "locked_out") setGone({ kind: "locked_out" });
        else setGone({ kind: "error", message: e.message });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [code, refreshContent, reloadTick]);

  function endedScreen(kind) {
    ownerToken.clear(code);
    setGone({ kind });
  }

  // Live updates: new/removed content, who's online, and termination are
  // reflected for everyone in the session without a reload.
  useSessionSocket(
    session && !gone && who ? code : null,
    (evt) => {
      if (evt.event === "presence") applyPresence(evt);
      if (evt.event === "content_added") {
        refreshContent();
        toast.info("New content shared", 2000);
      }
      if (evt.event === "content_removed") setItems((list) => list.filter((i) => i.id !== evt.id));
      if (evt.event === "session_ended") endedScreen("ended");
      if (evt.event === "session_expired") endedScreen("expired");
    },
    refreshContent,
    who
  );

  useEffect(() => {
    if (session && remaining === 0 && !gone) endedScreen("expired");
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [remaining, session]);

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
      ownerToken.clear(code);
      navigate("/", { replace: true });
    } catch (e) {
      toast.error(e.message);
    } finally {
      setEnding(false);
      setConfirmOpen(false);
    }
  }

  if (gone) {
    return <SessionGone kind={gone.kind} message={gone.message} onRetry={gone.kind === "error" ? () => setReloadTick((t) => t + 1) : undefined} />;
  }

  if (!session) {
    return (
      <div className="mx-auto max-w-2xl animate-pulse space-y-6">
        <div className="h-20 rounded-xl border border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900" />
        <div className="h-32 rounded-xl border border-dashed border-slate-300 bg-white dark:border-slate-700 dark:bg-slate-900" />
      </div>
    );
  }

  const left = remaining ?? session.ttl_seconds;
  const urgent = left <= 60;

  return (
    <div className="mx-auto max-w-2xl space-y-6">
      <div className="pinned flex flex-col gap-4 px-5 pb-6 pt-4 sm:flex-row sm:items-center sm:justify-between">
        <div>
          <div className="flex items-center gap-2 text-xs uppercase tracking-wide text-slate-400 dark:text-slate-500">
            {session.type === "shared_workspace" ? "Shared Workspace" : "Quick Transfer"}
            {online != null && (
              <span
                className="flex items-center gap-1 rounded-full bg-brand-100 px-2 py-0.5 text-[11px] font-medium normal-case tracking-normal text-brand-700 dark:bg-brand-500/15 dark:text-brand-400"
                title={`${online} connected · room limit ${session.max_participants}`}
              >
                <span className="h-1.5 w-1.5 rounded-full bg-brand-500" />
                {online} online <span className="opacity-60">/ {session.max_participants}</span>
              </span>
            )}
          </div>
          <div className="mt-1.5 font-mono text-3xl font-bold tracking-[0.2em] text-slate-900 dark:text-slate-50">
            <span className="marker">{code}</span>
          </div>
          <div className="mt-1.5 flex flex-wrap gap-3 text-xs font-medium">
            <button onClick={copyCode} className="text-brand-600 hover:underline dark:text-brand-500">
              Copy code
            </button>
          </div>
          <div className="mt-2.5 flex flex-wrap items-center gap-2.5">
            {people.length > 0 && (
              <div className="flex -space-x-1.5" aria-label="People in this session">
                {people.slice(0, 6).map((p) => (
                  <Avatar key={p.id} name={p.name} color={p.color} size={24} ring />
                ))}
                {people.length > 6 && (
                  <span className="z-10 ml-2 self-center text-xs text-slate-500 dark:text-slate-400">+{people.length - 6}</span>
                )}
              </div>
            )}
            {editingName ? (
              <input
                autoFocus
                defaultValue={me.name}
                maxLength={MAX_NAME_LENGTH}
                onBlur={(e) => commitName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") e.currentTarget.blur();
                  if (e.key === "Escape") setEditingName(false);
                }}
                aria-label="Your display name"
                className="w-36 rounded-md border border-slate-300 bg-white px-2 py-0.5 text-xs text-slate-900 dark:border-slate-600 dark:bg-slate-800 dark:text-slate-100"
              />
            ) : (
              <button
                onClick={() => setEditingName(true)}
                title="Anonymous name, only for this session — click to change"
                className="text-xs text-slate-500 hover:text-slate-800 hover:underline dark:text-slate-400 dark:hover:text-slate-200"
              >
                You’re <span className="font-semibold">{me.name}</span> ✎
              </button>
            )}
          </div>
        </div>
        <div className="flex items-center gap-4">
          <div className="text-right">
            <div className={`font-mono text-2xl font-semibold ${urgent ? "text-red-600 dark:text-red-400" : "text-slate-700 dark:text-slate-200"}`}>
              {formatMMSS(left)}
            </div>
            <div className="text-[11px] uppercase tracking-wide text-slate-400 dark:text-slate-500">until auto-delete</div>
          </div>
          {isOwner && (
            <button
              onClick={() => setConfirmOpen(true)}
              className="rounded-lg border-[1.5px] border-red-700/60 px-3 py-1.5 text-sm font-semibold text-red-700 hover:bg-red-50 dark:border-red-400/60 dark:text-red-300 dark:hover:bg-red-900/20"
            >
              End now
            </button>
          )}
        </div>
        {/* The burning fuse: shrinks as the session runs out of time. */}
        <div className="fuse-track" aria-hidden="true" />
        <div
          className="fuse-fill"
          aria-hidden="true"
          style={{ width: `${Math.max(1, Math.min(100, (left / (session.ttl_total_seconds || left || 1)) * 100))}%` }}
        />
      </div>

      {urgent && (
        <div className="rounded-lg border border-red-200 bg-red-50 px-4 py-2 text-sm text-red-700 dark:border-red-900 dark:bg-red-900/30 dark:text-red-300">
          This session is about to expire — save anything you need now.
        </div>
      )}

      <UploadWidget code={code} encKey={encKey} limits={session.limits} onUploaded={refreshContent} />

      <div className="space-y-2">
        <h2 className="font-display text-base font-semibold italic text-slate-600 dark:text-slate-300">Shared content ({items.length})</h2>
        {items.length === 0 && (
          <p className="rounded-lg border border-dashed border-slate-200 py-6 text-center text-sm text-slate-400 dark:border-slate-800 dark:text-slate-500">
            Nothing shared yet — drop a file or paste a note above.
          </p>
        )}
        {items.map((item) => (
          <ContentFeedItem
            key={item.id}
            item={item}
            code={code}
            encKey={encKey}
            canDelete={item.mine}
            onDelete={(id) => setItems((list) => list.filter((i) => i.id !== id))}
          />
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
