import { Link, useLocation } from "react-router-dom";
import ThemeToggle from "./ThemeToggle.jsx";

export default function Header() {
  const { pathname } = useLocation();
  const inRoom = pathname.startsWith("/session/");

  return (
    <header className="sticky top-0 z-40 border-b border-slate-200/80 bg-white/80 backdrop-blur-md dark:border-slate-800 dark:bg-slate-900/80">
      <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-3.5">
        <Link to="/" className="flex items-center gap-2 text-lg font-semibold text-slate-900 dark:text-slate-50">
          <span className="flex h-7 w-7 items-center justify-center rounded-md bg-brand-600 text-white">
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4">
              <path d="M12 2 3 7l9 5 9-5-9-5Z" />
              <path d="M3 12l9 5 9-5" />
              <path d="M3 17l9 5 9-5" />
            </svg>
          </span>
          ephemeral<span className="text-brand-600">.share</span>
        </Link>

        <div className="flex items-center gap-3">
          {!inRoom && (
            <span className="hidden text-sm text-slate-400 sm:inline dark:text-slate-500">
              End-to-end encrypted · gone in 5 minutes
            </span>
          )}
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}
