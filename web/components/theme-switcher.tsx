"use client";

import { Palette } from "lucide-react";
import { Select } from "@/components/ui/select";
import { useTheme } from "@/components/theme-provider";
import { THEME_LABELS, THEMES } from "@/lib/theme";

export function ThemeSwitcher({ compact = false }: { compact?: boolean }) {
  const { ready, theme, setTheme } = useTheme();
  const className = compact
    ? "theme-switcher theme-switcher-compact"
    : "theme-switcher";

  return (
    <label className={className}>
      <Palette className="h-4 w-4 shrink-0 text-[var(--text-muted)]" aria-hidden="true" />
      <span className="sr-only">界面主题</span>
      {ready ? (
        <Select
          aria-label="界面主题"
          value={theme}
          onChange={(event) => setTheme(event.target.value as typeof theme)}
          className="theme-select"
        >
          {THEMES.map((id) => (
            <option key={id} value={id}>
              {THEME_LABELS[id]}
            </option>
          ))}
        </Select>
      ) : (
        <span
          className="theme-select theme-select-placeholder"
          aria-hidden="true"
        />
      )}
    </label>
  );
}
