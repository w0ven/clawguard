import { cn } from "@/lib/utils";

type SwitchProps = {
  checked: boolean;
  onChange?: (v: boolean) => void;
  onCheckedChange?: (v: boolean) => void;
  disabled?: boolean;
  className?: string;
} & React.AriaAttributes;

export function Switch({
  checked,
  onChange,
  onCheckedChange,
  disabled,
  className,
  ...ariaProps
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
      {...ariaProps}
      aria-checked={checked}
      disabled={disabled}
      onClick={handleToggle}
      className={cn(
        "relative inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full transition-colors focus-visible:ring-2 focus-visible:ring-[var(--ring)] focus-visible:ring-offset-2 focus-visible:ring-offset-[var(--bg)]",
        checked ? "bg-[var(--accent)]" : "bg-[var(--border-strong)]",
        disabled && "opacity-50 cursor-not-allowed",
        className,
      )}
    >
      <span
        className={cn(
          "inline-block h-5 w-5 transform rounded-full bg-white transition-transform shadow-sm",
          checked ? "translate-x-[22px]" : "translate-x-[2px]",
        )}
      />
    </button>
  );
}
