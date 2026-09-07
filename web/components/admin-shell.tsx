"use client";

import type { ReactNode } from "react";
import { useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import type { LucideIcon } from "lucide-react";
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
import { useDirtyNavigation } from "@/components/dirty-guard";
import { ThemeSwitcher } from "@/components/theme-switcher";
import { cn } from "@/lib/utils";

type NavItem = {
  href: string;
  label: string;
  icon: LucideIcon;
};

type NavSection = {
  label: string;
  items: readonly NavItem[];
};

const navSections: readonly NavSection[] = [
  {
    label: "工作台",
    items: [{ href: "/dashboard", label: "总览", icon: LayoutDashboard }],
  },
  {
    label: "群与策略",
    items: [
      { href: "/groups", label: "群管理", icon: Users },
      { href: "/assistant", label: "群助手", icon: Bot },
      { href: "/authorized-groups", label: "群授权", icon: Waypoints },
    ],
  },
  {
    label: "审核与处置",
    items: [
      { href: "/violations", label: "违规", icon: TriangleAlert },
      { href: "/ai-review", label: "AI 复核", icon: Bot },
      { href: "/trust", label: "信任系统", icon: UserRoundCheck },
    ],
  },
  {
    label: "AI 与资源",
    items: [
      { href: "/ai-calls", label: "AI 统计", icon: BarChart3 },
      { href: "/prompt-editor", label: "Prompt", icon: FileCode2 },
      { href: "/llm", label: "模型管理", icon: Cpu },
    ],
  },
  {
    label: "系统",
    items: [
      { href: "/audit", label: "审计", icon: ScrollText },
      { href: "/admins", label: "管理员", icon: UserCog },
    ],
  },
];

const navItems = navSections.flatMap((section) => section.items);
const primaryMobileItems = navItems.filter((item) =>
  ["/dashboard", "/groups", "/assistant", "/violations"].includes(item.href),
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
  const { confirmNavigation } = useDirtyNavigation();
  const [menuOpen, setMenuOpen] = useState(false);

  const isActive = (href: string) => pathname === href || pathname.startsWith(href + "/") || (href === "/assistant" && /\/groups\/[^/]+\/assistant(?:\/|$)/.test(pathname));

  async function handleLogout() {
    if (!confirmNavigation("当前页面有未保存的修改，确定登出吗？")) return;
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

  const navLink = (item: NavItem, compact = false) => {
    const Icon = item.icon;
    return (
      <Link
        key={item.href}
        href={item.href}
        onClick={(event) => {
          if (!confirmNavigation()) {
            event.preventDefault();
            return;
          }
          haptic();
          setMenuOpen(false);
        }}
        className={cn(
          "admin-nav-link",
          compact
            ? "flex min-h-14 flex-col items-center justify-center gap-1 px-2 text-[11px]"
            : "flex min-h-11 items-center gap-2.5 rounded-xl px-3 py-2 text-sm",
          isActive(item.href)
            ? "admin-nav-link-active bg-[var(--accent-soft)] font-semibold text-[var(--accent)]"
            : "text-[var(--text-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--text)]",
        )}
      >
        <Icon className={compact ? "h-5 w-5" : "h-4 w-4 shrink-0"} />
        <span>{item.label}</span>
      </Link>
    );
  };

  return (
    <div className={cn("admin-shell flex min-h-screen", miniApp && "miniapp-shell")}>
      <aside className="admin-sidebar fixed inset-y-3 left-3 z-30 hidden w-60 shrink-0 flex-col rounded-3xl border md:flex">
        <div className="flex h-[4.5rem] items-center gap-3 border-b border-white/10 px-5">
          <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-white/15 text-white shadow-inner">
            <Shield className="h-5 w-5" strokeWidth={2.5} />
          </div>
          <div>
            <span className="block font-semibold tracking-tight text-white">ClawGuard</span>
            <span className="block text-[10px] uppercase tracking-[0.18em] text-white/50">Control plane</span>
          </div>
        </div>
        <nav className="flex-1 overflow-y-auto px-3 py-4">
          {navSections.map((section) => (
            <section key={section.label} className="mb-4 last:mb-0">
              <p className="admin-nav-section px-3 pb-1.5">{section.label}</p>
              <ul className="flex flex-col gap-1">
                {section.items.map((item) => <li key={item.href}>{navLink(item)}</li>)}
              </ul>
            </section>
          ))}
        </nav>
        <div className="space-y-3 border-t border-white/10 p-3">
          <div className="rounded-2xl border border-white/10 bg-white/5 p-2">
            <ThemeSwitcher />
          </div>
          <button
            type="button"
            onClick={handleLogout}
            className="flex min-h-11 w-full items-center gap-2.5 rounded-xl px-3 text-sm text-white/65 hover:bg-white/10 hover:text-white"
          >
            <LogOut className="h-4 w-4" />登出
          </button>
        </div>
      </aside>

      <div className="flex min-w-0 flex-1 flex-col md:ml-[17rem]">
        <div className={cn("admin-header flex min-h-14 items-center gap-3 border-b border-[var(--border)] px-4 md:hidden", miniApp && "miniapp-topbar")}>
          <div className="flex h-8 w-8 items-center justify-center rounded-xl bg-[var(--accent)] text-white shadow-sm">
            <Shield className="h-4 w-4" strokeWidth={2.5} />
          </div>
          <span className="font-semibold tracking-tight">ClawGuard</span>
          {miniApp && <span className="text-xs text-[var(--text-muted)]">Mini App</span>}
          <div className="ml-auto"><ThemeSwitcher compact /></div>
        </div>

        {!miniApp && (
          <div className="admin-header overflow-x-auto border-b border-[var(--border)] md:hidden">
            <div className="flex min-w-max gap-1 px-4 py-2">
              {navItems.map((item) => navLink(item))}
            </div>
          </div>
        )}

        <header className={cn("admin-header sticky top-0 z-20 border-b border-[var(--border)] px-5 py-5 md:px-8", miniApp && "px-4 py-3")}>
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <p className="mb-1 text-[10px] font-semibold uppercase tracking-[0.18em] text-[var(--accent)]">ClawGuard console</p>
              <h1 className={cn("text-2xl font-semibold tracking-tight", miniApp && "text-lg")}>{title}</h1>
              {subtitle && <p className="mt-1 text-sm text-[var(--text-muted)]">{subtitle}</p>}
            </div>
            <div className="flex shrink-0 items-center gap-2">
              <div className="hidden md:block"><ThemeSwitcher compact /></div>
              {actions}
            </div>
          </div>
        </header>

        <main className={cn("admin-main flex-1 overflow-y-auto px-5 py-6 md:px-8 md:py-8", miniApp && "px-3 py-4 pb-24")}>
          <div className={cn("mx-auto flex max-w-[1420px] flex-col gap-6", miniApp && "gap-4")}>
            {children}
          </div>
        </main>
      </div>

      {miniApp && (
        <>
          <nav className="miniapp-bottom-nav fixed inset-x-0 bottom-0 z-40 grid grid-cols-5 border-t border-[var(--border)] bg-[var(--surface)]/95 shadow-[0_-10px_28px_var(--ring)] backdrop-blur-xl md:hidden">
            {primaryMobileItems.map((item) => navLink(item, true))}
            <button
              type="button"
              onClick={() => { haptic(); setMenuOpen(true); }}
              className="flex min-h-14 flex-col items-center justify-center gap-1 px-2 text-[11px] text-[var(--text-muted)]"
            >
              <Menu className="h-5 w-5" />全部
            </button>
          </nav>
          {menuOpen && (
            <div className="fixed inset-0 z-50 bg-black/45 backdrop-blur-sm md:hidden" onClick={() => setMenuOpen(false)}>
              <section className="miniapp-menu absolute inset-x-0 bottom-0 max-h-[82vh] overflow-y-auto rounded-t-3xl border-t border-[var(--border)] bg-[var(--dialog-bg)] p-4 shadow-2xl" onClick={(event) => event.stopPropagation()}>
                <div className="mb-4 flex items-center justify-between">
                  <div>
                    <p className="text-[10px] uppercase tracking-[0.16em] text-[var(--accent)]">Navigation</p>
                    <h2 className="text-base font-semibold">全部功能</h2>
                  </div>
                  <button type="button" aria-label="关闭" onClick={() => setMenuOpen(false)} className="flex h-10 w-10 items-center justify-center rounded-xl hover:bg-[var(--surface-2)]"><X className="h-5 w-5" /></button>
                </div>
                <div className="mb-4 rounded-2xl border border-[var(--border)] bg-[var(--surface-2)] p-3"><ThemeSwitcher /></div>
                <div className="grid grid-cols-2 gap-2">{navItems.map((item) => navLink(item))}</div>
                <button type="button" onClick={handleLogout} className="mt-4 flex min-h-11 w-full items-center justify-center gap-2 rounded-xl border border-[var(--border)] text-sm text-[var(--text-muted)]"><LogOut className="h-4 w-4" />登出</button>
              </section>
            </div>
          )}
        </>
      )}
    </div>
  );
}
