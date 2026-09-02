export default function Footer() {
  return (
    <footer className="border-t border-slate-200 py-6 dark:border-slate-800">
      <div className="mx-auto flex max-w-5xl flex-col items-center gap-2 px-4 text-center text-xs text-slate-400 sm:flex-row sm:justify-between dark:text-slate-500">
        <p>No accounts. No history. No trace — everything you share is gone the moment the timer hits zero.</p>
        <p className="flex items-center gap-1.5 whitespace-nowrap font-medium text-brand-600/80 dark:text-brand-400/80">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-3.5 w-3.5">
            <rect x="5" y="11" width="14" height="9" rx="2" />
            <path d="M8 11V8a4 4 0 118 0v3" />
          </svg>
          Zero-knowledge by design
        </p>
      </div>
    </footer>
  );
}
