export type TelegramWebApp = {
  initData: string;
  colorScheme: "light" | "dark";
  ready: () => void;
  expand: () => void;
  close: () => void;
  safeAreaInset?: { top: number; bottom: number };
  contentSafeAreaInset?: { top: number; bottom: number };
  onEvent?: (event: string, handler: () => void) => void;
  offEvent?: (event: string, handler: () => void) => void;
  BackButton: {
    show: () => void;
    hide: () => void;
    onClick: (handler: () => void) => void;
    offClick: (handler: () => void) => void;
  };
  HapticFeedback?: {
    impactOccurred: (style: "light" | "medium" | "heavy") => void;
    notificationOccurred: (type: "error" | "success" | "warning") => void;
  };
};

declare global {
  interface Window {
    Telegram?: { WebApp?: TelegramWebApp };
  }
}

export const TELEGRAM_WEB_APP_SCRIPT_SRC = "/vendor/telegram-web-app.js?59";

export function telegramWebApp() {
  return typeof window === "undefined" ? undefined : window.Telegram?.WebApp;
}

export function ensureTelegramWebApp(): Promise<TelegramWebApp | undefined> {
  if (typeof window === "undefined") {
    return Promise.resolve(undefined);
  }
  const existing = window.Telegram?.WebApp;
  if (existing) {
    return Promise.resolve(existing);
  }
  return new Promise((resolve, reject) => {
    const script = document.createElement("script");
    script.src = TELEGRAM_WEB_APP_SCRIPT_SRC;
    script.onload = () => resolve(window.Telegram?.WebApp);
    script.onerror = () => {
      script.remove();
      reject(new Error("telegram sdk load failed"));
    };
    document.head.appendChild(script);
  });
}
