"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Switch } from "@/components/ui/switch";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { HealthCard } from "@/components/health-card";
import { LiveFeed } from "@/components/live-feed";
import { apiFetch } from "@/lib/api";
import type { AdminStats, SystemState, Violation } from "@/lib/types";
import { useToast } from "@/components/providers";
import { Users, ShieldCheck, AlertTriangle, ArrowRight } from "lucide-react";

function formatTime(ts: string) {
  const d = new Date(ts);
  if (isNaN(d.getTime())) return "-";
  return d.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function actionTone(action: string): "danger" | "warning" | "default" {
  if (action === "ban" || action === "delete_ban") return "danger";
  if (action === "warn" || action === "mute" || action === "delete")
    return "warning";
  return "default";
}

export default function DashboardPage() {
  const { pushToast } = useToast();
  const [stats, setStats] = useState<AdminStats | null>(null);
  const [violations, setViolations] = useState<Violation[]>([]);
  const [systemState, setSystemState] = useState<SystemState | null>(null);
  const [loading, setLoading] = useState(true);
  const [updatingState, setUpdatingState] = useState(false);

  async function load() {
    setLoading(true);
    try {
      const [s, v, state] = await Promise.all([
        apiFetch<AdminStats>("/api/admin/stats"),
        apiFetch<{ violations: Violation[] }>("/api/admin/violations?limit=10"),
        apiFetch<{ state: SystemState }>("/api/admin/system-state"),
      ]);
      setStats(s);
      setViolations(v.violations ?? []);
      setSystemState(state.state);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    load();
  }, [pushToast]);

  async function updateSystemState(
    patch: Record<string, unknown>,
    message: string,
  ) {
    if (!confirm(message)) return;
    setUpdatingState(true);
    try {
      const res = await apiFetch<{ state: SystemState }>(
        "/api/admin/system-state",
        {
          method: "PUT",
          body: JSON.stringify(patch),
        },
      );
      setSystemState(res.state);
      pushToast("系统状态已更新", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    } finally {
      setUpdatingState(false);
    }
  }

  const statCards = [
    {
      label: "管理中的群",
      value: stats?.groups_count ?? "-",
      icon: Users,
      tone: "text-[var(--accent)]",
    },
    {
      label: "活跃验证",
      value: stats?.active_verifications ?? "-",
      icon: ShieldCheck,
      tone: "text-[var(--warning)]",
    },
    {
      label: "今日违规",
      value: stats?.today_violations ?? "-",
      icon: AlertTriangle,
      tone: "text-[var(--danger)]",
    },
  ];

  const hasEmergencyState =
    systemState?.ai_paused ||
    systemState?.actions_paused ||
    systemState?.frozen;

  return (
    <AdminShell title="总览" subtitle="实时群管理状态">
      <HealthCard />
      {hasEmergencyState ? (
        <Card className="border-[var(--danger)] bg-[var(--danger)]/8">
          <CardBody className="flex items-center gap-3 py-3 text-sm text-[var(--danger)]">
            <AlertTriangle className="h-4 w-4 shrink-0" />
            <span>
              当前处于紧急状态：
              {[
                systemState?.ai_paused ? "AI 已暂停" : null,
                systemState?.actions_paused ? "动作已暂停" : null,
                systemState?.frozen ? "系统已冻结" : null,
              ]
                .filter(Boolean)
                .join(" / ")}
            </span>
          </CardBody>
        </Card>
      ) : null}

      <Card>
        <CardHeader>
          <CardTitle>系统状态</CardTitle>
        </CardHeader>
        <CardBody className="grid gap-4 lg:grid-cols-[1fr_1fr_1fr_auto]">
          <div className="rounded-md border border-[var(--border)] p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="text-sm font-medium">暂停 AI 审核</p>
                <p className="mt-1 text-xs text-[var(--text-muted)]">
                  仅停 AI，关键词和正则规则继续执行
                </p>
              </div>
              <Switch
                checked={systemState?.ai_paused ?? false}
                disabled={updatingState}
                onCheckedChange={(checked) =>
                  void updateSystemState(
                    {
                      ai_paused: checked,
                      ai_paused_reason: checked ? "manual emergency pause" : "",
                    },
                    checked ? "确认暂停 AI 审核？" : "确认恢复 AI 审核？",
                  )
                }
              />
            </div>
          </div>

          <div className="rounded-md border border-[var(--border)] p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="text-sm font-medium">暂停所有封禁</p>
                <p className="mt-1 text-xs text-[var(--text-muted)]">
                  规则继续跑，但 delete/mute/ban/warn 不执行
                </p>
              </div>
              <Switch
                checked={systemState?.actions_paused ?? false}
                disabled={updatingState}
                onCheckedChange={(checked) =>
                  void updateSystemState(
                    { actions_paused: checked },
                    checked ? "确认暂停所有动作？" : "确认恢复所有动作？",
                  )
                }
              />
            </div>
          </div>

          <div className="rounded-md border border-[var(--border)] p-4">
            <div className="flex items-start justify-between gap-3">
              <div>
                <p className="text-sm font-medium">完全冻结</p>
                <p className="mt-1 text-xs text-[var(--text-muted)]">
                  所有消息处理停机，验证流程仍保留
                </p>
              </div>
              <Switch
                checked={systemState?.frozen ?? false}
                disabled={updatingState}
                onCheckedChange={(checked) =>
                  void updateSystemState(
                    { frozen: checked },
                    checked ? "确认冻结整个系统？" : "确认解除冻结？",
                  )
                }
              />
            </div>
          </div>
        </CardBody>
      </Card>

      <section className="grid gap-3 grid-cols-1 sm:grid-cols-3">
        {statCards.map(({ label, value, icon: Icon, tone }) => (
          <Card key={label} className="p-4">
            <div className="flex items-start justify-between">
              <div>
                <p className="text-xs text-[var(--text-muted)]">{label}</p>
                <p className="mt-2 text-3xl font-semibold tabular-nums">
                  {loading ? (
                    <span className="text-[var(--text-subtle)]">…</span>
                  ) : (
                    value
                  )}
                </p>
              </div>
              <div className={`${tone} opacity-80`}>
                <Icon className="h-5 w-5" strokeWidth={2} />
              </div>
            </div>
          </Card>
        ))}
      </section>

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <div>
            <CardTitle>最近违规</CardTitle>
            <p className="text-xs text-[var(--text-muted)] mt-0.5">
              最近 10 条记录
            </p>
          </div>
          <Link
            href="/violations"
            className="inline-flex items-center gap-1 text-xs text-[var(--accent)] hover:underline"
          >
            全部 <ArrowRight className="h-3 w-3" />
          </Link>
        </CardHeader>
        {violations.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "暂无违规记录"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>时间</TableHeaderCell>
                <TableHeaderCell>群</TableHeaderCell>
                <TableHeaderCell>用户</TableHeaderCell>
                <TableHeaderCell>规则</TableHeaderCell>
                <TableHeaderCell>处理</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {violations.map((v) => (
                <TableRow key={v.id}>
                  <TableCell className="text-[var(--text-muted)] tabular-nums whitespace-nowrap">
                    {formatTime(v.created_at)}
                  </TableCell>
                  <TableCell className="tabular-nums">{v.chat_id}</TableCell>
                  <TableCell className="tabular-nums">
                    {v.username ? `@${v.username}` : v.user_id}
                  </TableCell>
                  <TableCell>
                    <code className="text-xs">{v.rule}</code>
                  </TableCell>
                  <TableCell>
                    <Badge tone={actionTone(v.action)}>{v.action}</Badge>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      <div className="grid gap-4 xl:grid-cols-2">
        <LiveFeed />
      </div>
    </AdminShell>
  );
}
