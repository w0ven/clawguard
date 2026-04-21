import { cn } from "@/lib/utils";

type SwitchProps = {
  checked: boolean;
  onChange?: (v: boolean) => void;
  onCheckedChange?: (v: boolean) => void;
  disabled?: boolean;
  className?: string;
};

export function Switch({
  checked,
  onChange,
  onCheckedChange,
  disabled,
  className,
}: SwitchProps) {
  function handleToggle() {
    const next = !checked;
    onChange?.(next);
    onCheckedChange?.(next);
  }

  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      disabled={disabled}
      onClick={handleToggle}
      className={cn(
        "relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full transition-colors",
        checked ? "bg-[var(--accent)]" : "bg-[var(--border-strong)]",
        disabled && "opacity-50 cursor-not-allowed",
        className,
      )}
    >
      <span
        className={cn(
          "inline-block h-4 w-4 transform rounded-full bg-white transition-transform shadow-sm",
          checked ? "translate-x-[18px]" : "translate-x-[2px]",
        )}
      />
    </button>
  );
}
