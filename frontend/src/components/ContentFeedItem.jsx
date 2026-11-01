import { useEffect, useState } from "react";
import { api } from "../lib/api";
import { decryptText, decryptJSONUrlSafe, decryptBytes } from "../lib/crypto";
import { formatBytes } from "../lib/format";
import { decryptIdentity } from "../lib/identity";
import Avatar from "./Avatar.jsx";
import { useToast } from "./ToastProvider.jsx";

const LockIcon = (props) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" {...props}>
    <rect x="5" y="11" width="14" height="9" rx="2" />
    <path d="M8 11V8a4 4 0 118 0v3" />
  </svg>
);

const FileIcon = (props) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" {...props}>
    <path d="M14 2H6a2 2 0 00-2 2v16a2 2 0 002 2h12a2 2 0 002-2V8z" />
    <path d="M14 2v6h6" />
  </svg>
);

export default function ContentFeedItem({ item, code, encKey, canDelete, onDelete }) {
  const isFile = item.kind === "file";
  const isSnippet = item.content_type === "snippet";
  const [copied, setCopied] = useState(false);
  const [decryptedText, setDecryptedText] = useState(null);
  const [meta, setMeta] = useState(null); // { name, type } for files
  const [decryptError, setDecryptError] = useState(false);
  const [downloading, setDownloading] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [sharer, setSharer] = useState(null); // { name, color } of who shared it
  const toast = useToast();

  useEffect(() => {
    if (!encKey) return;
    let cancelled = false;
    (async () => {
      try {
        if (isFile) {
          const m = await decryptJSONUrlSafe(encKey, item.filename);
          if (!cancelled) setMeta(m);
        } else {
          const t = await decryptText(encKey, item.text_payload);
          if (!cancelled) setDecryptedText(t);
        }
      } catch {
        if (!cancelled) setDecryptError(true);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [encKey, isFile, item.filename, item.text_payload]);

  useEffect(() => {
    let cancelled = false;
    decryptIdentity(encKey, item.who).then((s) => !cancelled && setSharer(s));
    return () => {
      cancelled = true;
    };
  }, [encKey, item.who]);

  async function handleCopy() {
    if (decryptedText == null) return;
    try {
      await navigator.clipboard.writeText(decryptedText);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("Clipboard permission denied");
    }
  }

  async function handleDownload() {
    if (!encKey || !meta) return;
    setDownloading(true);
    let encrypted;
    try {
      const { download_url: url } = await api.getDownloadUrl(code, item.id);
      const res = await fetch(url);
      if (!res.ok) throw new Error(`Download failed (${res.status})`);
      encrypted = await res.arrayBuffer();
    } catch (e) {
      toast.error(e.message || "Download failed");
      setDownloading(false);
      return;
    }
    try {
      const plain = await decryptBytes(encKey, encrypted);
      const blob = new Blob([plain], { type: meta.type });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = meta.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
    } catch {
      toast.error("Couldn't decrypt this file — the key may not match.");
    } finally {
      setDownloading(false);
    }
  }

  async function handleDelete() {
    setDeleting(true);
    try {
      await api.deleteContent(code, item.id);
      onDelete?.(item.id);
    } catch (e) {
      toast.error(e.message || "Couldn't delete this item");
      setDeleting(false);
    }
  }

  const locked = !encKey;

  return (
    <div className="flex items-start justify-between gap-4 rounded-lg border border-slate-200 bg-white px-4 py-3 dark:border-slate-800 dark:bg-slate-900">
      <div className="flex min-w-0 flex-1 items-start gap-3">
        {isFile && (
          <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-md bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400">
            <FileIcon className="h-4 w-4" />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-1.5 truncate text-sm font-medium text-slate-800 dark:text-slate-100">
            {locked && <LockIcon className="h-3 w-3 shrink-0 text-slate-400" />}
            {isFile
              ? locked
                ? "Encrypted file"
                : meta?.name || (decryptError ? "Couldn't decrypt" : "Decrypting…")
              : `${item.content_type} · text`}
          </div>
          {sharer && (
            <div className="mt-0.5 flex items-center gap-1.5 text-xs text-slate-400 dark:text-slate-500">
              <Avatar name={sharer.name} color={sharer.color} size={14} />
              <span className="truncate">{item.mine ? "You" : sharer.name}</span>
            </div>
          )}
          {isFile && item.size_bytes > 0 && (
            <div className="text-xs text-slate-400 dark:text-slate-500">{formatBytes(item.size_bytes)}</div>
          )}
          {!isFile && !locked && (
            <pre
              className={`mt-1 max-h-48 overflow-auto whitespace-pre-wrap break-words rounded-md text-sm text-slate-600 dark:text-slate-300 ${
                isSnippet ? "bg-slate-50 p-2 font-mono text-xs dark:bg-slate-800" : ""
              }`}
            >
              {decryptedText ?? (decryptError ? "Couldn't decrypt this note — the key may not match." : "Decrypting…")}
            </pre>
          )}
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-3">
      {canDelete && (
        <button
          onClick={handleDelete}
          disabled={deleting}
          title="Delete this for everyone (you shared it)"
          className="text-sm font-medium text-red-600 hover:underline disabled:opacity-50 dark:text-red-400"
        >
          {deleting ? "Deleting…" : "Delete"}
        </button>
      )}
      {isFile ? (
        <button
          onClick={handleDownload}
          disabled={locked || !meta || downloading}
          className="text-sm font-medium text-brand-600 hover:underline disabled:opacity-50 disabled:no-underline dark:text-brand-500"
        >
          {downloading ? "Decrypting…" : "Download"}
        </button>
      ) : (
        <button
          onClick={handleCopy}
          disabled={locked || decryptedText == null}
          className={`text-sm font-medium hover:underline disabled:opacity-50 disabled:no-underline ${
            copied ? "text-emerald-600 dark:text-emerald-400" : "text-brand-600 dark:text-brand-500"
          }`}
        >
          {copied ? "Copied!" : "Copy"}
        </button>
      )}
      </div>
    </div>
  );
}
