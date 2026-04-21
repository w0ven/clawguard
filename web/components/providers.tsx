"use client";

import type { ReactNode } from "react";
import { createContext, useCallback, useContext, useMemo, useState } from "react";
import { ToastViewport } from "@/components/ui/toast";

type ToastItem = {
  id: number;
  title: string;
  tone?: "error" | "success";
};

type ToastContextValue = {
  pushToast: (title: string, tone?: "error" | "success") => void;
};

const ToastContext = createContext<ToastContextValue | null>(null);

export function Providers({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([]);

  const pushToast = useCallback((title: string, tone?: "error" | "success") => {
    const id = Date.now() + Math.random();
    setItems((current) => [...current, { id, title, tone }]);
    window.setTimeout(() => {
      setItems((current) => current.filter((item) => item.id !== id));
    }, 3200);
  }, []);

  const value = useMemo(() => ({ pushToast }), [pushToast]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <ToastViewport items={items} />
    </ToastContext.Provider>
  );
}

export function useToast() {
  const context = useContext(ToastContext);
  if (!context) {
    throw new Error("useToast must be used within Providers");
  }
  return context;
}
