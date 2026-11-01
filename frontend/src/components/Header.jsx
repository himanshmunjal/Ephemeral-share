import { Link, useLocation } from "react-router-dom";
import ThemeToggle from "./ThemeToggle.jsx";

export default function Header() {
  const { pathname } = useLocation();
  const inRoom = pathname.startsWith("/session/");

  return (
    <header className="sticky top-0 z-40 border-b border-slate-300/70 bg-slate-50/85 backdrop-blur-md dark:border-slate-800 dark:bg-slate-950/85">
      <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-3.5">
        <Link to="/" className="flex items-baseline gap-1 text-slate-900 dark:text-slate-50" aria-label="ephemeral.share home">
          <span className="font-display text-xl font-semibold italic tracking-tight">ephemeral</span>
          <span aria-hidden="true" className="flex gap-[3px] text-brand-600 dark:text-brand-400">
            <span className="h-1.5 w-1.5 rounded-full bg-current" />
            <span className="h-1.5 w-1.5 rounded-full bg-current opacity-50" />
            <span className="h-1.5 w-1.5 rounded-full bg-current opacity-20" />
          </span>
        </Link>

        <div className="flex items-center gap-3">
          {!inRoom && (
            <span className="hidden text-sm text-slate-400 sm:inline dark:text-slate-500">
              Locked in your browser · gone when the timer ends
            </span>
          )}
          <ThemeToggle />
        </div>
      </div>
    </header>
  );
}
