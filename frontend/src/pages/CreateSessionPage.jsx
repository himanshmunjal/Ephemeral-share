import { useState } from "react";
import { useNavigate, Link } from "react-router-dom";
import { api, ownerToken } from "../lib/api";
import { useToast } from "../components/ToastProvider.jsx";

const MODES = [
  {
    id: "quick_transfer",
    title: "Quick Transfer",
    description: "One uploader, anyone with the link can view or download.",
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" className="h-5 w-5">
        <path d="M12 19V5M5 12l7-7 7 7" />
      </svg>
    ),
  },
  {
    id: "shared_workspace",
    title: "Shared Workspace",
    description: "Multiple participants share content live, in real time.",
    icon: (
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" className="h-5 w-5">
        <circle cx="9" cy="7" r="3" />
        <path d="M2 21v-2a5 5 0 015-5h4a5 5 0 015 5v2" />
        <circle cx="18" cy="7" r="2.5" />
        <path d="M17 14.5a4 4 0 013.5 4v.5" />
      </svg>
    ),
  },
];

export default function CreateSessionPage() {
  const [type, setType] = useState("quick_transfer");
  const [maxParticipants, setMaxParticipants] = useState(10);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState(null);
  const navigate = useNavigate();
  const toast = useToast();

  async function handleCreate() {
    setLoading(true);
    setError(null);
    try {
      const res = await api.createSession(type, type === "shared_workspace" ? maxParticipants : undefined);
      // The owner token authorises "end session" and deleting anyone's item.
      // It's kept only in this browser and sent as an X-Owner-Token header.
      ownerToken.set(res.code, res.owner_token);

      navigate(`/session/${res.code}`);
    } catch (e) {
      setError(e.message);
      toast.error(e.message);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="mx-auto max-w-xl">
      <h1 className="mb-2 text-3xl font-semibold text-slate-900 dark:text-slate-50">Hand something over,<br />then let it <span className="marker italic">vanish</span>.</h1>
      <p className="mb-8 text-slate-500 dark:text-slate-400">
        Pick a mode, get a code, pass it to whoever needs it. Everything is locked in your browser first, and wiped when the timer runs out.
      </p>

      <div className="mb-6 grid grid-cols-1 gap-3 sm:grid-cols-2">
        {MODES.map((mode) => (
          <button
            key={mode.id}
            onClick={() => setType(mode.id)}
            className={`rounded-xl border-[1.5px] p-5 text-left transition hover:-translate-y-0.5 ${
              type === mode.id
                ? "-rotate-[0.6deg] border-slate-900 bg-brand-50 shadow-[3px_3px_0_0_theme(colors.slate.900)] dark:border-brand-400 dark:bg-brand-500/10 dark:shadow-[3px_3px_0_0_rgba(79,163,142,0.5)]"
                : "border-slate-200 bg-white hover:border-slate-300 dark:border-slate-700 dark:bg-slate-900 dark:hover:border-slate-600"
            }`}
          >
            <div
              className={`mb-3 flex h-10 w-10 items-center justify-center rounded-lg ${
                type === mode.id ? "bg-brand-600 text-white" : "bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400"
              }`}
            >
              {mode.icon}
            </div>
            <div className="text-base font-semibold text-slate-800 dark:text-slate-100">{mode.title}</div>
            <div className="mt-1 text-sm leading-relaxed text-slate-500 dark:text-slate-400">{mode.description}</div>
          </button>
        ))}
      </div>

      {type === "shared_workspace" && (
        <label className="mb-6 block">
          <span className="text-sm text-slate-600 dark:text-slate-300">Max participants</span>
          <input
            type="number"
            min={2}
            max={10}
            value={maxParticipants}
            onChange={(e) => setMaxParticipants(Number(e.target.value))}
            className="mt-1 w-32 rounded-lg border border-slate-300 bg-white px-3 py-2 text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
          />
        </label>
      )}

      <div className="mb-6 flex items-start gap-2.5 rounded-lg border border-slate-200 bg-slate-50 px-3.5 py-2.5 text-xs text-slate-500 dark:border-slate-800 dark:bg-slate-900 dark:text-slate-400">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="mt-0.5 h-3.5 w-3.5 shrink-0">
          <rect x="5" y="11" width="14" height="9" rx="2" />
          <path d="M8 11V8a4 4 0 118 0v3" />
        </svg>
        <span>
          Files and text are encrypted in your browser and stored encrypted. The key comes from the access code, so anyone who has the code
          can open the session — share it only with people you trust. Please don’t share illegal content (see the{" "}
          <Link to="/terms" className="underline">Terms</Link>).
        </span>
      </div>

      {error && <p className="mb-4 text-sm text-red-600 dark:text-red-400">{error}</p>}

      <button
        onClick={handleCreate}
        disabled={loading}
        className="btn-primary w-full py-3 text-base disabled:opacity-60"
      >
        {loading ? "Creating…" : "Create session"}
      </button>

      <Link
        to="/join"
        className="mt-6 flex items-center justify-between gap-3 rounded-lg border border-slate-200 bg-white px-4 py-3 text-left transition hover:border-brand-300 hover:shadow-sm dark:border-slate-700 dark:bg-slate-900 dark:hover:border-brand-600"
      >
        <span className="text-sm text-slate-500 dark:text-slate-400">Have a code already?</span>
        <span className="flex items-center gap-1 text-sm font-semibold text-brand-600 dark:text-brand-400">
          Join a session
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5">
            <path d="M5 12h14M13 6l6 6-6 6" />
          </svg>
        </span>
      </Link>
    </div>
  );
}
