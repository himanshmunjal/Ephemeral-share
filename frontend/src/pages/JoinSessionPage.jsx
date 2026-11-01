import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { api } from "../lib/api";

/** Accepts either a bare access code or a full shared link. */
function parseInput(raw) {
  const trimmed = raw.trim();
  if (trimmed.includes("/session/")) {
    try {
      const url = new URL(trimmed, window.location.origin);
      const match = url.pathname.match(/\/session\/([^/]+)/);
      const code = match?.[1]?.toUpperCase();
      if (code) return { code };
    } catch {
      // fall through to treating it as a bare code below
    }
  }
  return { code: trimmed.toUpperCase() };
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
      const { code } = parseInput(input);
      if (!code) throw new Error("Enter an access code or paste a shared link");
      await api.getSession(code); // validate it exists before navigating
      navigate(`/session/${code}`);
    } catch (e) {
      setError(e.message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="mx-auto max-w-sm">
      <h1 className="mb-1 text-3xl font-semibold text-slate-900 dark:text-slate-50">Got a code?</h1>
      <p className="mb-8 text-slate-500 dark:text-slate-400">
        Paste the link you were sent, or enter the access code directly.
      </p>

      <form onSubmit={handleJoin}>
        <input
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="e.g. K7QX3M or a full link"
          maxLength={300}
          autoFocus
          className="mb-4 w-full rounded-lg border border-slate-300 bg-white px-4 py-3 text-center font-mono tracking-widest text-slate-900 uppercase placeholder:normal-case placeholder:tracking-normal dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
        />
        {error && <p className="mb-4 text-sm text-red-600 dark:text-red-400">{error}</p>}
        <button
          type="submit"
          disabled={loading || !input.trim()}
          className="btn-primary w-full py-3 text-base disabled:opacity-60"
        >
          {loading ? "Checking…" : "Join"}
        </button>
      </form>

      <p className="mt-6 text-center text-xs text-slate-400 dark:text-slate-500">
        The access code is all you need — content is decrypted automatically in your browser. Treat the code like a password.
      </p>
    </div>
  );
}
