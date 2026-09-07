"use client";

import { cn } from "@/lib/utils";

type TabsProps = {
  tabs: Array<{ value: string; label: string }>;
  value: string;
  onValueChange: (value: string) => void;
  className?: string;
};

export function Tabs({ tabs, value, onValueChange, className }: TabsProps) {
  return (
    <div
      className={cn(
        "inline-flex max-w-full items-center gap-1 overflow-x-auto rounded-xl border border-[var(--border)] bg-[var(--surface-2)] p-1",
        className,
      )}
    >
      {tabs.map((tab) => {
        const active = tab.value === value;
        return (
          <button
            key={tab.value}
            type="button"
            onClick={() => onValueChange(tab.value)}
            className={cn(
              "min-h-9 shrink-0 rounded-lg px-3 py-1.5 text-sm transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)]",
              active
                ? "bg-[var(--surface)] text-[var(--text)] shadow-sm"
                : "text-[var(--text-muted)] hover:text-[var(--text)]",
            )}
          >
            {tab.label}
          </button>
        );
      })}
    </div>
  );
}
