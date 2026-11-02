import { useEffect, useState } from "react";

const STORAGE_KEY = "ephemeral-share:free-tier-notice-seen";

function alreadySeen() {
  try {
    return localStorage.getItem(STORAGE_KEY) !== null;
  } catch {
    return false; // Storage blocked: showing it again beats never showing it.
  }
}

/** One-time notice, shown on the first visit from this browser. It stays up until closed. */
export default function FreeTierNotice() {
  const [open, setOpen] = useState(() => !alreadySeen());

  // Recorded on display, not on close, so a reload can't bring it back.
  useEffect(() => {
    if (!open) return;
    try {
      localStorage.setItem(STORAGE_KEY, "1");
    } catch {
      // Storage blocked: nothing to persist.
    }
  }, [open]);

  if (!open) return null;

  return (
    <div className="fixed inset-x-4 top-20 z-50 sm:left-auto sm:w-96 animate-[toast-in_.18s_ease-out]">
      <div role="status" className="pinned py-3 pl-4 pr-11 text-sm text-slate-700 dark:text-slate-100">
        This site is deployed on a free tier, so it may take some time to create a session. Thanks for your patience!
        <button
          type="button"
          onClick={() => setOpen(false)}
          aria-label="Dismiss notice"
          className="absolute right-2 top-2 flex h-7 w-7 items-center justify-center rounded-md text-slate-500 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800 dark:hover:text-slate-100"
        >
          <svg aria-hidden="true" viewBox="0 0 16 16" className="h-4 w-4" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <path d="M4 4l8 8M12 4l-8 8" />
          </svg>
        </button>
      </div>
    </div>
  );
}
