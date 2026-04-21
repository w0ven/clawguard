import { cn } from "@/lib/utils";

type ToastViewportProps = {
  items: Array<{ id: number; title: string; tone?: "error" | "success" }>;
};

export function ToastViewport({ items }: ToastViewportProps) {
  return (
    <div className="fixed right-4 bottom-4 z-50 flex w-full max-w-sm flex-col gap-2">
      {items.map((item) => (
        <div
          key={item.id}
          className={cn(
            "rounded-md border shadow-lg px-4 py-3 text-sm backdrop-blur bg-[var(--surface)]",
            item.tone === "error"
              ? "border-[var(--danger)]/30 text-[var(--danger)]"
              : item.tone === "success"
                ? "border-[var(--success)]/30 text-[var(--success)]"
                : "border-[var(--border)] text-[var(--text)]",
          )}
        >
          {item.title}
        </div>
      ))}
    </div>
  );
}
