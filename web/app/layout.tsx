import type { Metadata } from "next";
import type { ReactNode } from "react";
import "./globals.css";
import { Providers } from "@/components/providers";

export const metadata: Metadata = {
  title: "ClawGuard",
  description: "Telegram group management console",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: ReactNode;
}>) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        <script
          dangerouslySetInnerHTML={{
            __html: `(() => { try { const allowed = ["emerald", "ocean", "graphite"]; const stored = window.localStorage.getItem("clawguard-ui-theme"); const theme = allowed.includes(stored || "") ? stored : "emerald"; document.documentElement.dataset.theme = theme; document.documentElement.style.colorScheme = theme === "graphite" ? "dark" : "light"; } catch (_) { document.documentElement.dataset.theme = "emerald"; document.documentElement.style.colorScheme = "light"; } })();`,
          }}
        />
        <link
          rel="stylesheet"
          href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap"
        />
        <script src="https://telegram.org/js/telegram-web-app.js?59" />
      </head>
      <body>
        <Providers>{children}</Providers>
      </body>
    </html>
  );
}
