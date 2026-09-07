"use client";

import type { ReactNode } from "react";
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from "react";
import {
  normalizeTheme,
  persistTheme,
  readStoredTheme,
  type ThemeId,
} from "@/lib/theme";

type ThemeContextValue = {
  theme: ThemeId;
  ready: boolean;
  setTheme: (theme: ThemeId) => void;
};

const ThemeContext = createContext<ThemeContextValue>({
  theme: "emerald",
  ready: false,
  setTheme: () => undefined,
});

function readInitialTheme(): ThemeId {
  if (typeof document === "undefined") {
    return "emerald";
  }
  return normalizeTheme(document.documentElement.dataset.theme || readStoredTheme());
}

function applyTheme(theme: ThemeId) {
  const root = document.documentElement;
  root.dataset.theme = theme;
  root.style.colorScheme = theme === "graphite" ? "dark" : "light";
  if (document.body) {
    document.body.dataset.theme = theme;
  }
}

export function ThemeProvider({ children }: { children: ReactNode }) {
  // SSR keeps the emerald fallback, while the layout script paints the cached
  // data-theme before hydration. Until that client value is acknowledged,
  // ThemeSwitcher renders a same-size placeholder instead of a wrong select.
  const [theme, setThemeState] = useState<ThemeId>(readInitialTheme);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const initial = readInitialTheme();
    setThemeState(initial);
    applyTheme(initial);
    setReady(true);
  }, []);

  const setTheme = useCallback((next: ThemeId) => {
    const safeTheme = normalizeTheme(next);
    setThemeState(safeTheme);
    persistTheme(safeTheme);
    applyTheme(safeTheme);
  }, []);

  const value = useMemo(
    () => ({ ready, theme, setTheme }),
    [ready, setTheme, theme],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  return useContext(ThemeContext);
}
