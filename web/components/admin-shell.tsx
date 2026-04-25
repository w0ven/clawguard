"use client";

import type { ReactNode } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  Shield,
  Users,
  TriangleAlert,
  ScrollText,
  LogOut,
  UserCog,
  Bot,
  BarChart3,
  UserRoundCheck,
  FileCode2,
  LayoutDashboard,
  Waypoints,
  Cpu,
} from "lucide-react";
import { apiFetch } from "@/lib/api";
import { useToast } from "@/components/providers";
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
];

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

  async function handleLogout() {
    try {
      await apiFetch<void>("/api/auth/logout", { method: "POST" });
    } catch {
      // ignore
    } finally {
      pushToast("已登出", "success");
      window.location.href = "/";
    }
  }

  return (
    <div className="flex min-h-screen">
      {/* Sidebar */}
      <aside className="hidden md:flex w-56 shrink-0 flex-col border-r border-[var(--border)] bg-[var(--surface)]">
        <div className="flex h-14 items-center gap-2 border-b border-[var(--border)] px-4">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-[var(--accent)] text-white">
            <Shield className="h-4 w-4" strokeWidth={2.5} />
          </div>
          <span className="font-semibold tracking-tight">ClawGuard</span>
        </div>
        <nav className="flex-1 overflow-y-auto px-2 py-3">
          <ul className="flex flex-col gap-0.5">
            {navItems.map((item) => {
              const Icon = item.icon;
              const active =
                pathname === item.href || pathname.startsWith(item.href + "/");
              return (
                <li key={item.href}>
                  <Link
                    href={item.href}
                    className={cn(
                      "flex items-center gap-2.5 rounded-md px-2.5 py-2 text-sm transition-colors",
                      active
                        ? "bg-[var(--accent-soft)] text-[var(--accent)] font-medium"
                        : "text-[var(--text-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--text)]",
                    )}
                  >
                    <Icon className="h-4 w-4 shrink-0" />
                    <span>{item.label}</span>
                  </Link>
                </li>
              );
            })}
          </ul>
        </nav>
        <div className="border-t border-[var(--border)] p-2">
          <button
            type="button"
            onClick={handleLogout}
            className="flex w-full items-center gap-2.5 rounded-md px-2.5 py-2 text-sm text-[var(--text-muted)] hover:bg-[var(--surface-2)] hover:text-[var(--text)]"
          >
            <LogOut className="h-4 w-4" />
            登出
          </button>
        </div>
      </aside>

      {/* Main */}
      <div className="flex flex-1 flex-col min-w-0">
        {/* Mobile topbar */}
        <div className="md:hidden flex h-14 items-center gap-2 border-b border-[var(--border)] bg-[var(--surface)] px-4">
          <div className="flex h-7 w-7 items-center justify-center rounded-md bg-[var(--accent)] text-white">
            <Shield className="h-4 w-4" strokeWidth={2.5} />
          </div>
          <span className="font-semibold tracking-tight">ClawGuard</span>
        </div>

        {/* Mobile nav scroll */}
        <div className="md:hidden overflow-x-auto border-b border-[var(--border)] bg-[var(--surface)]">
          <div className="flex gap-1 px-4 py-2 min-w-max">
            {navItems.map((item) => {
              const Icon = item.icon;
              const active =
                pathname === item.href || pathname.startsWith(item.href + "/");
              return (
                <Link
                  key={item.href}
                  href={item.href}
                  className={cn(
                    "flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm whitespace-nowrap",
                    active
                      ? "bg-[var(--accent-soft)] text-[var(--accent)] font-medium"
                      : "text-[var(--text-muted)]",
                  )}
                >
                  <Icon className="h-3.5 w-3.5" />
                  {item.label}
                </Link>
              );
            })}
          </div>
        </div>

        {/* Header */}
        <header className="border-b border-[var(--border)] bg-[var(--surface)] px-5 md:px-8 py-5">
          <div className="flex items-start justify-between gap-4">
            <div className="min-w-0">
              <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
              {subtitle ? (
                <p className="mt-1 text-sm text-[var(--text-muted)]">
                  {subtitle}
                </p>
              ) : null}
            </div>
            {actions}
          </div>
        </header>

        {/* Content */}
        <main className="flex-1 overflow-y-auto bg-[var(--bg)] px-5 md:px-8 py-6">
          <div className="mx-auto max-w-7xl flex flex-col gap-6">
            {children}
          </div>
        </main>
      </div>
    </div>
  );
}
