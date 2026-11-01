import { Link } from "react-router-dom";

const COPY = {
  not_found: {
    title: "Session not found",
    body: "That code doesn't match an active session. It may have expired, been ended by the host, or been typed incorrectly.",
  },
  expired: {
    title: "This session has expired",
    body: "Sessions are temporary. Everything shared in it has been permanently deleted.",
  },
  ended: {
    title: "The host ended this session",
    body: "Everything shared in it has been permanently deleted.",
  },
  full: {
    title: "This session is full",
    body: "The host set a limit on how many people can be in the room at once. Ask them to free a spot, then try again.",
  },
  locked_out: {
    title: "Too many wrong codes",
    body: "For safety, lookups from your network are paused for a few minutes. Check the code and try again shortly.",
  },
  error: {
    title: "Something went wrong",
    body: "We couldn't load this session. Check your connection and try again.",
  },
};

export default function SessionGone({ kind = "error", message, onRetry }) {
  const copy = COPY[kind] || COPY.error;
  return (
    <div className="mx-auto max-w-md rounded-2xl border border-slate-200 bg-white px-6 py-10 text-center dark:border-slate-800 dark:bg-slate-900">
      <div className="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-slate-100 text-slate-500 dark:bg-slate-800 dark:text-slate-400">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" className="h-6 w-6">
          <circle cx="12" cy="12" r="9" />
          <path d="M12 7v5l3 2" />
        </svg>
      </div>
      <h1 className="font-display text-xl font-semibold text-slate-900 dark:text-slate-50">{copy.title}</h1>
      <p className="mt-2 text-sm text-slate-500 dark:text-slate-400">{kind === "error" && message ? message : copy.body}</p>
      <div className="mt-6 flex flex-col gap-2 sm:flex-row sm:justify-center">
        <Link to="/" className="btn-primary">
          Start a new session
        </Link>
        <Link
          to="/join"
          className="rounded-lg border border-slate-300 px-4 py-2.5 text-sm font-medium text-slate-700 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-200 dark:hover:bg-slate-800"
        >
          Join with a code
        </Link>
        {onRetry && (
          <button onClick={onRetry} className="rounded-lg px-4 py-2.5 text-sm font-medium text-brand-600 hover:underline dark:text-brand-500">
            Try again
          </button>
        )}
      </div>
    </div>
  );
}
