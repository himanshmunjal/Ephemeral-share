import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";
import { storeKey } from "../lib/sessionKeys";

/** Accepts either a bare access code or a full shared link (code + #k=key fragment). */
function parseInput(raw) {
  const trimmed = raw.trim();
  if (trimmed.includes("/session/")) {
    try {
      const url = new URL(trimmed, window.location.origin);
      const match = url.pathname.match(/\/session\/([^/]+)/);
      const code = match?.[1]?.toUpperCase();
      const key = url.hash.startsWith("#k=") ? url.hash.slice(3) : null;
      if (code) return { code, key };
    } catch {
      // fall through to treating it as a bare code below
    }
  }
  return { code: trimmed.toUpperCase(), key: null };
}

export default function JoinSessionPage() {
  const [input, setInput] = useState("");
  const [error, setError] = useState(null);
  const [loading, setLoading] = useState(false);
  const navigate = useNavigate();

  async function handleJoin(e) {
    e.preventDefault();
    setLoading(true);
    setError(null);
    try {
      const { code, key } = parseInput(input);
      if (!code) throw new Error("Enter an access code or paste a shared link");
      await api.getSession(code); // validate it exists before navigating
      if (key) {
        storeKey(code, key);
        navigate(`/session/${code}#k=${key}`);
      } else {
        navigate(`/session/${code}`);
      }
    } catch (e) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="mx-auto max-w-sm">
      <h1 className="mb-1 text-2xl font-semibold text-slate-900 dark:text-slate-50">Join a session</h1>
      <p className="mb-8 text-slate-500 dark:text-slate-400">
        Paste the link you were sent, or enter the access code directly.
      </p>

      <form onSubmit={handleJoin}>
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="e.g. K7QX3M9P or a full link"
          maxLength={300}
          autoFocus
          className="mb-4 w-full rounded-lg border border-slate-300 bg-white px-4 py-3 text-center font-mono tracking-widest text-slate-900 uppercase placeholder:normal-case placeholder:tracking-normal dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        {error && <p className="mb-4 text-sm text-red-600 dark:text-red-400">{error}</p>}
        <button
          type="submit"
          disabled={loading || !input.trim()}
          className="w-full rounded-lg bg-brand-600 py-3 font-medium text-white transition hover:bg-brand-700 disabled:opacity-60"
        >
          {loading ? "Checking…" : "Join"}
        </button>
      </form>

      <p className="mt-6 text-center text-xs text-slate-400 dark:text-slate-500">
        Joining with only a code (no link) still works, but you'll need to paste the decryption key
        separately to view encrypted content.
      </p>
    </div>
  );
}
