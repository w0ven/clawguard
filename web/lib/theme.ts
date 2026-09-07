export const THEME_STORAGE_KEY = "clawguard-ui-theme";

export const THEMES = ["emerald", "ocean", "graphite"] as const;

export type ThemeId = (typeof THEMES)[number];

export const THEME_LABELS: Record<ThemeId, string> = {
  emerald: "翠绿",
  ocean: "海蓝",
  graphite: "石墨",
};

export function isThemeId(value: unknown): value is ThemeId {
  return typeof value === "string" && (THEMES as readonly string[]).includes(value);
}

export function normalizeTheme(value: unknown): ThemeId {
  return isThemeId(value) ? value : "emerald";
}

export function readStoredTheme(): ThemeId {
  try {
    return normalizeTheme(window.localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    return "emerald";
  }
}

export function persistTheme(theme: ThemeId) {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  } catch {
    // Private browsing and restricted storage are supported with an in-memory theme.
  }
}
