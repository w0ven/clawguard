"use client";

import type { ReactNode } from "react";
import { createContext, useContext, useEffect, useMemo, useState } from "react";
import { usePathname, useRouter } from "next/navigation";
import { telegramWebApp } from "@/lib/telegram";

type MiniAppContextValue = {
  active: boolean;
  haptic: (tone?: "light" | "medium" | "heavy") => void;
};

const MiniAppContext = createContext<MiniAppContextValue>({
  active: false,
  haptic: () => undefined,
});

export function TelegramMiniAppProvider({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const [active, setActive] = useState(false);

  useEffect(() => {
    const app = telegramWebApp();
    const miniAppSession = sessionStorage.getItem("cg_miniapp") === "1";
    const isActive = Boolean(app?.initData || miniAppSession);
    setActive(isActive);
    document.documentElement.classList.toggle("telegram-miniapp", isActive);
    if (!isActive || !app) return;

    app.ready();
    app.expand();
    const handleBack = () => {
      // DirtyGuardProvider owns the single history confirmation. Calling
      // confirmNavigation here would ask once before router.back() and again
      // when the resulting popstate reaches the guard.
      if (pathname === "/dashboard") router.replace("/dashboard");
      else router.back();
    };
    app.BackButton.onClick(handleBack);
    if (pathname === "/dashboard" || pathname === "/miniapp") app.BackButton.hide();
    else app.BackButton.show();
    return () => app.BackButton.offClick(handleBack);
  }, [pathname, router]);

  const value = useMemo<MiniAppContextValue>(
    () => ({
      active,
      haptic: (tone = "light") =>
        telegramWebApp()?.HapticFeedback?.impactOccurred(tone),
    }),
    [active],
  );

  return <MiniAppContext.Provider value={value}>{children}</MiniAppContext.Provider>;
}

export function useTelegramMiniApp() {
  return useContext(MiniAppContext);
}
