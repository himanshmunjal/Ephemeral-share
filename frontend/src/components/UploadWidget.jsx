import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "../lib/api";
import { encryptBytes, encryptText, encryptJSONUrlSafe } from "../lib/crypto";
import { encryptIdentity, getIdentity } from "../lib/identity";
import { formatBytes } from "../lib/format";
import { useToast } from "./ToastProvider.jsx";

let nextUploadId = 1;

const STATUS_LABEL = {
  queued: "Waiting…",
  encrypting: "Encrypting…",
  uploading: "Uploading",
  finishing: "Finishing…",
  done: "Done",
  error: "Failed",
  cancelled: "Cancelled",
};

export default function UploadWidget({ code, encKey, limits, onUploaded }) {
  const [uploads, setUploads] = useState([]);
  const [text, setText] = useState("");
  const [dragActive, setDragActive] = useState(false);
  const toast = useToast();
  const disabled = !encKey;

  // Per-upload mutable control state that must not trigger re-renders.
  const ctl = useRef(new Map()); // id -> { file, cancelled, abort, contentId }
  const chain = useRef(Promise.resolve()); // uploads run one at a time

  const patch = useCallback((id, changes) => {
    setUploads((list) => list.map((u) => (u.id === id ? { ...u, ...changes } : u)));
  }, []);
  const remove = useCallback((id) => {
    ctl.current.delete(id);
    setUploads((list) => list.filter((u) => u.id !== id));
  }, []);

  // Frees the server-side quota reserved for an upload that didn't finish.
  const abandon = useCallback(
    async (c) => {
      if (c?.contentId && !c.completed) {
        try {
          await api.deleteContent(code, c.contentId);
        } catch {
          /* session may already be gone; nothing to free */
        }
        c.contentId = null;
      }
    },
    [code]
  );

  const run = useCallback(
    async (id) => {
      const c = ctl.current.get(id);
      if (!c || c.cancelled || !encKey) return;
      const { file } = c;
      try {
        patch(id, { status: "encrypting", progress: 0, error: null });
        const plain = await file.arrayBuffer();
        const encrypted = await encryptBytes(encKey, plain);
        const name = await encryptJSONUrlSafe(encKey, { name: file.name, type: file.type || "application/octet-stream" });
        const blob = new Blob([encrypted], { type: "application/octet-stream" });
        if (c.cancelled) throw Object.assign(new Error("Upload cancelled"), { aborted: true });

        const who = await encryptIdentity(encKey, getIdentity());
        const { upload_url: url, content_id: contentId } = await api.requestUploadUrl(code, name, "application/octet-stream", blob.size, who);
        c.contentId = contentId;
        if (c.cancelled) throw Object.assign(new Error("Upload cancelled"), { aborted: true });

        patch(id, { status: "uploading" });
        const { promise, abort } = api.uploadToPresignedUrl(url, blob, (p) => patch(id, { progress: p }));
        c.abort = abort;
        await promise;

        patch(id, { status: "finishing", progress: 100 });
        await api.completeUpload(code, contentId);
        c.completed = true;
        patch(id, { status: "done" });
        onUploaded?.();
        setTimeout(() => remove(id), 2500);
      } catch (e) {
        await abandon(c);
        if (e.aborted || c.cancelled) {
          patch(id, { status: "cancelled" });
          setTimeout(() => remove(id), 1500);
        } else {
          patch(id, { status: "error", error: e.message || "Upload failed" });
        }
      } finally {
        c.abort = null;
      }
    },
    [code, encKey, patch, remove, abandon, onUploaded]
  );

  const enqueue = useCallback(
    (id) => {
      chain.current = chain.current.then(() => run(id));
    },
    [run]
  );

  const addFiles = useCallback(
    (fileList) => {
      if (!encKey) return;
      const files = Array.from(fileList || []).filter((f) => f && f.size >= 0);
      for (const file of files) {
        // Quick client-side checks for instant feedback; the server enforces
        // the same limits regardless.
        if (limits?.max_file_bytes && file.size + 28 > limits.max_file_bytes) {
          toast.error(`"${file.name}" is too large (max ${formatBytes(limits.max_file_bytes)} per file)`);
          continue;
        }
        if (file.size === 0) {
          toast.error(`"${file.name}" is empty`);
          continue;
        }
        const id = nextUploadId++;
        ctl.current.set(id, { file, cancelled: false, abort: null, contentId: null, completed: false });
        setUploads((list) => [...list, { id, name: file.name, size: file.size, status: "queued", progress: 0, error: null }]);
        enqueue(id);
      }
    },
    [encKey, limits, enqueue, toast]
  );

  function cancel(id) {
    const c = ctl.current.get(id);
    if (!c) return;
    c.cancelled = true;
    if (c.abort) c.abort(); // aborting the XHR rejects the in-flight run, which cleans up
    else {
      patch(id, { status: "cancelled" });
      setTimeout(() => remove(id), 1500);
    }
  }

  function retry(id) {
    const c = ctl.current.get(id);
    if (!c) return;
    c.cancelled = false;
    c.completed = false;
    patch(id, { status: "queued", progress: 0, error: null });
    enqueue(id);
  }

  // Paste anywhere on the page: screenshots / copied files become uploads.
  // Plain-text pastes are left alone so typing into the note box still works.
  useEffect(() => {
    function onPaste(e) {
      const files = e.clipboardData?.files;
      if (files && files.length > 0) {
        e.preventDefault();
        addFiles(files);
      }
    }
    window.addEventListener("paste", onPaste);
    return () => window.removeEventListener("paste", onPaste);
  }, [addFiles]);

  async function handleAddText(e) {
    e.preventDefault();
    if (!text.trim() || !encKey) return;
    try {
      // Multi-line content (code, logs, etc.) is shared as a "snippet" so it
      // renders monospace/preformatted; a single line stays a plain "note".
      // Only outer blank lines are trimmed — internal indentation is kept.
      // This categorization happens before encryption, so the server can
      // still tell notes from snippets for UI purposes without seeing the
      // actual text.
      const trimmed = text.replace(/^\n+|\n+$/g, "");
      const contentType = trimmed.includes("\n") ? "snippet" : "note";
      const cipherB64 = await encryptText(encKey, trimmed);
      await api.addText(code, contentType, cipherB64, await encryptIdentity(encKey, getIdentity()));
      setText("");
      onUploaded?.();
    } catch (e) {
      toast.error(e.message || "Failed to add text");
    }
  }

  function handleTextKeyDown(e) {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter") {
      handleAddText(e);
    }
  }

  function handleDrop(e) {
    e.preventDefault();
    setDragActive(false);
    addFiles(e.dataTransfer.files);
  }

  return (
    <div className="space-y-4 rounded-xl border border-dashed border-slate-300 bg-white p-4 dark:border-slate-700 dark:bg-slate-900">
      <label
        onDragOver={(e) => {
          e.preventDefault();
          setDragActive(true);
        }}
        onDragLeave={() => setDragActive(false)}
        onDrop={handleDrop}
        className={`block ${disabled ? "cursor-not-allowed opacity-60" : "cursor-pointer"}`}
      >
        <input
          type="file"
          multiple
          className="hidden"
          disabled={disabled}
          onChange={(e) => {
            addFiles(e.target.files);
            e.target.value = ""; // allow re-selecting the same file after a cancel
          }}
        />
        <div
          className={`rounded-lg py-8 text-center text-sm transition ${
            dragActive
              ? "bg-brand-50 text-brand-700 ring-2 ring-brand-400 dark:bg-brand-500/10 dark:text-brand-400"
              : "bg-slate-50 text-slate-500 hover:bg-slate-100 dark:bg-slate-800 dark:text-slate-400 dark:hover:bg-slate-800/70"
          }`}
        >
          {disabled ? (
            "Preparing encryption…"
          ) : (
            <>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" className="mx-auto mb-2 h-6 w-6">
                <path d="M12 15V3m0 0L7 8m5-5l5 5" />
                <path d="M4 15v4a2 2 0 002 2h12a2 2 0 002-2v-4" />
              </svg>
              {dragActive ? "Drop to upload" : "Click to choose files, drag them here, or paste an image"}
              {limits && (
                <div className="mt-1 text-xs text-slate-400 dark:text-slate-500">
                  Up to {formatBytes(limits.max_file_bytes)} per file · {limits.max_files} files · {formatBytes(limits.max_session_bytes)} total
                </div>
              )}
            </>
          )}
        </div>
      </label>

      {uploads.length > 0 && (
        <ul className="space-y-2" aria-label="Uploads in progress">
          {uploads.map((u) => (
            <li key={u.id} className="rounded-lg border border-slate-200 px-3 py-2 text-sm dark:border-slate-800">
              <div className="flex items-center justify-between gap-3">
                <div className="min-w-0">
                  <div className="truncate font-medium text-slate-700 dark:text-slate-200">{u.name}</div>
                  <div
                    className={`text-xs ${
                      u.status === "error"
                        ? "text-red-600 dark:text-red-400"
                        : u.status === "done"
                        ? "text-emerald-600 dark:text-emerald-400"
                        : "text-slate-400 dark:text-slate-500"
                    }`}
                  >
                    {u.status === "error" ? u.error : STATUS_LABEL[u.status]}
                    {u.status === "uploading" && ` · ${u.progress}%`} · {formatBytes(u.size)}
                  </div>
                </div>
                <div className="flex shrink-0 gap-3 text-xs font-medium">
                  {u.status === "error" && (
                    <button onClick={() => retry(u.id)} className="text-brand-600 hover:underline dark:text-brand-500">
                      Retry
                    </button>
                  )}
                  {u.status === "error" || u.status === "done" ? (
                    <button onClick={() => remove(u.id)} className="text-slate-400 hover:underline" aria-label="Dismiss">
                      Dismiss
                    </button>
                  ) : (
                    u.status !== "cancelled" && (
                      <button onClick={() => cancel(u.id)} className="text-red-600 hover:underline dark:text-red-400">
                        Cancel
                      </button>
                    )
                  )}
                </div>
              </div>
              {(u.status === "uploading" || u.status === "finishing") && (
                <div className="mt-2 h-1.5 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
                  <div className="h-1.5 rounded-full bg-brand-600 transition-all" style={{ width: `${u.progress}%` }} />
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      <form onSubmit={handleAddText} className="flex flex-col gap-2">
        <textarea
          value={text}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={handleTextKeyDown}
          disabled={disabled}
          placeholder="Paste a note, code snippet, or URL… (multi-line and indentation are preserved)"
          rows={4}
          className="flex-1 resize-y rounded-lg border border-slate-300 bg-white px-3 py-2 font-mono text-sm text-slate-900 placeholder:text-slate-400 disabled:opacity-60 dark:border-slate-700 dark:bg-slate-800 dark:text-slate-100"
        />
        <div className="flex items-center justify-between gap-3">
          <p className="text-xs text-slate-400 dark:text-slate-500">
            Don’t share illegal content. By uploading you agree to the{" "}
            <Link to="/terms" className="underline hover:text-slate-600 dark:hover:text-slate-300">
              Terms
            </Link>
            .
          </p>
          <button
            type="submit"
            disabled={disabled}
            className="shrink-0 rounded-lg bg-slate-800 px-4 py-2 text-sm text-white hover:bg-slate-900 disabled:opacity-60 dark:bg-slate-700 dark:hover:bg-slate-600"
          >
            Add
          </button>
        </div>
      </form>
    </div>
  );
}
