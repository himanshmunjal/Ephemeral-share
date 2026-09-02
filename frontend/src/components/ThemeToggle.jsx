import { useTheme } from "../hooks/useTheme";

const ICONS = {
  light: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" className="h-4 w-4">
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </svg>
  ),
  dark: (
    <svg viewBox="0 0 24 24" fill="currentColor" className="h-4 w-4">
      <path d="M20.8 14.5A8.5 8.5 0 019.5 3.2a.75.75 0 00-.9-.98A9.75 9.75 0 1021.8 15.4a.75.75 0 00-1-.9z" />
    </svg>
  ),
  system: (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" className="h-4 w-4">
      <rect x="3" y="4" width="18" height="12" rx="2" />
      <path d="M8 20h8M12 16v4" />
    </svg>
  ),
};

const LABELS = { light: "Light", dark: "Dark", system: "System" };

export default function ThemeToggle() {
  const { theme, cycle } = useTheme();
  return (
    <button
      onClick={cycle}
      title={`Theme: ${LABELS[theme]} — click to change`}
      className="flex items-center gap-1.5 rounded-lg border border-slate-200 px-2.5 py-1.5 text-xs font-medium text-slate-600 transition hover:border-slate-300 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:border-slate-600 dark:hover:bg-slate-800"
    >
      {ICONS[theme]}
      <span className="hidden sm:inline">{LABELS[theme]}</span>
    </button>
  );
}
