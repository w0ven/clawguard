import type React from "react";
import { cn } from "@/lib/utils";

type Tone = "default" | "success" | "warning" | "danger" | "info";

const tones: Record<Tone, string> = {
  default: "bg-[var(--surface-2)] text-[var(--text-muted)] border-[var(--border)]",
  success: "bg-[var(--success-soft)] text-[var(--success)] border-[var(--success)]/20",
  warning: "bg-[var(--warning-soft)] text-[var(--warning)] border-[var(--warning)]/20",
  danger: "bg-[var(--danger-soft)] text-[var(--danger)] border-[var(--danger)]/20",
  info: "bg-[var(--accent-soft)] text-[var(--accent)] border-[var(--accent)]/20",
};

type BadgeProps = React.HTMLAttributes<HTMLSpanElement> & { tone?: Tone };

export function Badge({ className, tone = "default", ...props }: BadgeProps) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium",
        tones[tone],
        className,
      )}
      {...props}
    />
  );
}
