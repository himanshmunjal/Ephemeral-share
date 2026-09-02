import { useCallback, useEffect, useState } from "react";

const STORAGE_KEY = "ephemeral-share:theme"; // "light" | "dark" | "system"

function resolveIsDark(theme) {
  return theme === "dark" || (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
}

function applyTheme(theme) {
  document.documentElement.classList.toggle("dark", resolveIsDark(theme));
}

/** Tri-state theme (light/dark/system), persisted, applied as a `dark` class on <html>. */
export function useTheme() {
  const [theme, setThemeState] = useState(() => localStorage.getItem(STORAGE_KEY) || "system");

  useEffect(() => {
    applyTheme(theme);
    if (theme !== "system") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const handler = () => applyTheme("system");
    mq.addEventListener("change", handler);
    return () => mq.removeEventListener("change", handler);
  }, [theme]);

  const setTheme = useCallback((next) => {
    localStorage.setItem(STORAGE_KEY, next);
    setThemeState(next);
  }, []);

  const cycle = useCallback(() => {
    setTheme(theme === "light" ? "dark" : theme === "dark" ? "system" : "light");
  }, [theme, setTheme]);

  return { theme, setTheme, cycle };
}
