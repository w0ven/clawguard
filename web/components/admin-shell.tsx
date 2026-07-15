"use client";

import type { ReactNode } from "react";
import { useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  BarChart3,
  Bot,
  Cpu,
  FileCode2,
  LayoutDashboard,
  LogOut,
  Menu,
  ScrollText,
  Shield,
  TriangleAlert,
  UserCog,
  UserRoundCheck,
  Users,
  Waypoints,
  X,
} from "lucide-react";
import { apiFetch } from "@/lib/api";
import { useToast } from "@/components/providers";
import { useTelegramMiniApp } from "@/components/telegram-miniapp-provider";
import { cn } from "@/lib/utils";

const navItems = [
  { href: "/dashboard", label: "总览", icon: LayoutDashboard },
  { href: "/groups", label: "群管理", icon: Users },
  { href: "/authorized-groups", label: "群授权", icon: Waypoints },
  { href: "/violations", label: "违规", icon: TriangleAlert },
  { href: "/ai-review", label: "AI 复核", icon: Bot },
  { href: "/ai-calls", label: "AI 统计", icon: BarChart3 },
  { href: "/trust", label: "信任系统", icon: UserRoundCheck },
  { href: "/prompt-editor", label: "Prompt", icon: FileCode2 },
  { href: "/llm", label: "模型管理", icon: Cpu },
  { href: "/audit", label: "审计", icon: ScrollText },
  { href: "/admins", label: "管理员", icon: UserCog },
] as const;

const primaryMobileItems = navItems.filter((item) =>
  ["/dashboard", "/groups", "/violations", "/trust"].includes(item.href),
);

export function AdminShell({
  title,
  subtitle,
  actions,
  children,
}: {
  title: string;
  subtitle?: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  const pathname = usePathname();
  const { pushToast } = useToast();
  const { active: miniApp, haptic } = useTelegramMiniApp();
  const [menuOpen, setMenuOpen] = useState(false);

  const isActive = (href: string) => pathname === href || pathname.startsWith(href + "/");

  async function handleLogout() {
    try {
      await apiFetch<void>("/api/auth/logout", { method: "POST" });
    } catch {
      // The local session is cleared by navigation even if revocation is unavailable.
    } finally {
      sessionStorage.removeItem("cg_miniapp");
      pushToast("已登出", "success");
      window.location.href = miniApp ? "/miniapp" : "/";
    }
  }

  const navLink = (item: (typeof navItems)[number], compact = false) => {
    const Icon = item.icon;
    return (
      <Link
        key={item.href}
        href={item.href}
        onClick={() => {
          haptic();
          setMenuOpen(false);
        }}
        className={cn(
          compact
            ? "flex min-h-12 flex-col items-center justify-center gap-1 px-2 text-[11px]"
            : "flex min-h-11 items-center gap-2.5 rounded-md px-3 py-2 text-sm",
          isActive(item.href)
            ? "bg-[var(--accent-soft)] font-medium text-[var(--accent)]"
            : "text-[var(--text-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--text)]",
        )}
      >
        <Icon className={compact ? "h-5 w-5" : "h-4 w-4 shrink-0"} />
        <span>{item.label}</span>
      </Link>
    );
  };

  return (
    <div className={cn("flex min-h-screen", miniApp && "miniapp-shell")}>
      <aside className="hidden w-56 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)] md:flex">
        <div className="flex h-14 items-center gap-2 border-b border-[var(--border)] px-4">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-[var(--accent)] text-white">
            <Shield className="h-4 w-4" strokeWidth={2.5} />
          </div>
          <span className="font-semibold">ClawGuard</span>
        </div>
        <nav className="flex-1 overflow-y-auto px-2 py-3">
          <ul className="flex flex-col gap-0.5">{navItems.map((item) => <li key={item.href}>{navLink(item)}</li>)}</ul>
        </nav>
        <div className="border-t border-[var(--border)] p-2">
          <button type="button" onClick={handleLogout} className="flex min-h-11 w-full items-center gap-2.5 rounded-md px-3 text-sm text-[var(--text-muted)] hover:bg-[var(--surface-2)]">
            <LogOut className="h-4 w-4" />登出
          </button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col">
        <div className={cn("flex h-14 items-center gap-2 border-b border-[var(--border)] bg-[var(--surface)] px-4 md:hidden", miniApp && "miniapp-topbar")}>
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-[var(--accent)] text-white">
            <Shield className="h-4 w-4" strokeWidth={2.5} />
          </div>
          <span className="font-semibold">ClawGuard</span>
          {miniApp && <span className="ml-auto text-xs text-[var(--text-muted)]">Mini App</span>}
        </div>

        {!miniApp && (
          <div className="overflow-x-auto border-b border-[var(--border)] bg-[var(--surface)] md:hidden">
            <div className="flex min-w-max gap-1 px-4 py-2">{navItems.map((item) => navLink(item))}</div>
          </div>
        )}

        <header className={cn("border-b border-[var(--border)] bg-[var(--surface)] px-5 py-5 md:px-8", miniApp && "px-4 py-3")}>
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              <h1 className={cn("text-xl font-semibold", miniApp && "text-lg")}>{title}</h1>
              {subtitle && <p className="mt-1 text-sm text-[var(--text-muted)]">{subtitle}</p>}
            </div>
            <div className="shrink-0">{actions}</div>
          </div>
        </header>

        <main className={cn("flex-1 overflow-y-auto bg-[var(--bg)] px-5 py-6 md:px-8", miniApp && "px-3 py-4 pb-24")}>
          <div className={cn("mx-auto flex max-w-7xl flex-col gap-6", miniApp && "gap-4")}>{children}</div>
        </main>
      </div>

      {miniApp && (
        <>
          <nav className="miniapp-bottom-nav fixed inset-x-0 bottom-0 z-40 grid grid-cols-5 border-t border-[var(--border)] bg-[var(--surface)] md:hidden">
            {primaryMobileItems.map((item) => navLink(item, true))}
            <button type="button" onClick={() => { haptic(); setMenuOpen(true); }} className="flex min-h-12 flex-col items-center justify-center gap-1 px-2 text-[11px] text-[var(--text-muted)]">
              <Menu className="h-5 w-5" />全部
            </button>
          </nav>
          {menuOpen && (
            <div className="fixed inset-0 z-50 bg-black/40 md:hidden" onClick={() => setMenuOpen(false)}>
              <section className="miniapp-menu absolute inset-x-0 bottom-0 max-h-[78vh] overflow-y-auto rounded-t-lg bg-[var(--surface)] p-4" onClick={(event) => event.stopPropagation()}>
                <div className="mb-3 flex items-center justify-between">
                  <h2 className="text-base font-semibold">全部功能</h2>
                  <button type="button" aria-label="关闭" onClick={() => setMenuOpen(false)} className="flex h-10 w-10 items-center justify-center rounded-md hover:bg-[var(--surface-2)]"><X className="h-5 w-5" /></button>
                </div>
                <div className="grid grid-cols-2 gap-2">{navItems.map((item) => navLink(item))}</div>
                <button type="button" onClick={handleLogout} className="mt-4 flex min-h-11 w-full items-center justify-center gap-2 rounded-md border border-[var(--border)] text-sm text-[var(--text-muted)]"><LogOut className="h-4 w-4" />登出</button>
              </section>
            </div>
          )}
        </>
      )}
    </div>
  );
}
