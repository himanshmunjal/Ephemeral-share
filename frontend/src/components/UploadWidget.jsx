import { useState } from "react";
import { api } from "../lib/api";
import { encryptBytes, encryptText, encryptJSONUrlSafe } from "../lib/crypto";
import { useToast } from "./ToastProvider.jsx";

export default function UploadWidget({ code, encKey, onUploaded }) {
  const [progress, setProgress] = useState(null);
  const [text, setText] = useState("");
  const [dragActive, setDragActive] = useState(false);
  const toast = useToast();
  const disabled = !encKey;

  async function handleFile(file) {
    if (!file || !encKey) return;
    setProgress(0);
    try {
      const plainBuffer = await file.arrayBuffer();
      const encryptedBytes = await encryptBytes(encKey, plainBuffer);
      const encryptedFilename = await encryptJSONUrlSafe(encKey, {
        name: file.name,
        type: file.type || "application/octet-stream",
      });
      const blob = new Blob([encryptedBytes], { type: "application/octet-stream" });

      const { upload_url: uploadUrl, content_id: contentId } = await api.requestUploadUrl(code, encryptedFilename, "application/octet-stream", blob.size);
      await api.uploadToPresignedUrl(uploadUrl, blob, setProgress);
      await api.completeUpload(code, contentId);
      onUploaded?.();
    } catch (e) {
      toast.error(e.message || "Upload failed");
    } finally {
      setProgress(null);
    }
  }

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
      await api.addText(code, contentType, cipherB64);
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
    handleFile(e.dataTransfer.files?.[0]);
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
        <input type="file" className="hidden" disabled={disabled} onChange={(e) => handleFile(e.target.files?.[0])} />
        <div
          className={`rounded-lg py-8 text-center text-sm transition ${
            dragActive
              ? "bg-brand-50 text-brand-700 ring-2 ring-brand-400 dark:bg-brand-500/10 dark:text-brand-400"
              : "bg-slate-50 text-slate-500 hover:bg-slate-100 dark:bg-slate-800 dark:text-slate-400 dark:hover:bg-slate-800/70"
          }`}
        >
          {disabled ? (
            "Unlock the session with a decryption key to upload"
          ) : (
            <>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" className="mx-auto mb-2 h-6 w-6">
                <path d="M12 15V3m0 0L7 8m5-5l5 5" />
                <path d="M4 15v4a2 2 0 002 2h12a2 2 0 002-2v-4" />
              </svg>
              {dragActive ? "Drop to upload" : "Click to choose a file, or drag one here"}
            </>
          )}
        </div>
      </label>

      {progress !== null && (
        <div className="h-2 w-full overflow-hidden rounded-full bg-slate-200 dark:bg-slate-700">
          <div className="h-2 rounded-full bg-brand-600 transition-all" style={{ width: `${progress}%` }} />
        </div>
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
        <button
          type="submit"
          disabled={disabled}
          className="self-end rounded-lg bg-slate-800 px-4 py-2 text-sm text-white hover:bg-slate-900 disabled:opacity-60 dark:bg-slate-700 dark:hover:bg-slate-600"
        >
          Add
        </button>
      </form>
    </div>
  );
}
