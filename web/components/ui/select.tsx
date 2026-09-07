import type React from "react";
import { cn } from "@/lib/utils";

type SelectOption = { label: string; value: string };

type SelectProps = Omit<React.SelectHTMLAttributes<HTMLSelectElement>, "children"> & {
  options?: SelectOption[];
  children?: React.ReactNode;
};

export function Select({
  className,
  options,
  children,
  ...props
}: SelectProps) {
  return (
    <select
      className={cn(
        "flex h-10 w-full rounded-xl border border-[var(--input-border)] bg-[var(--input-bg)] px-3 text-sm text-[var(--input-color)] focus:outline-none focus:border-[var(--accent)] focus:ring-2 focus:ring-[var(--ring)] disabled:opacity-50 disabled:cursor-not-allowed pr-3",
        className,
      )}
      {...props}
    >
      {options
        ? options.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {opt.label}
            </option>
          ))
        : children}
    </select>
  );
}
