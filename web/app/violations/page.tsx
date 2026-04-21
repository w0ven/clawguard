"use client";

import { Fragment, useEffect, useMemo, useState , Suspense} from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";
import { apiFetch } from "@/lib/api";
import type { Group, Violation, Warning } from "@/lib/types";
import { useToast } from "@/components/providers";
import { MessageTextBlock } from "@/components/message-text-block";
import { FilterBar } from "@/components/filter-bar";
import { ChevronDown, ChevronRight } from "lucide-react";

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
  if (["warn", "mute", "delete", "delete_and_warn", "delete_mute"].includes(action))
    return "warning";
  return "default";
}

function ViolationsPageInner() {
  const { pushToast } = useToast();
  const router = useRouter();
  const searchParams = useSearchParams();
  const [violations, setViolations] = useState<Violation[]>([]);
  const [warnings, setWarnings] = useState<Warning[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [filters, setFilters] = useState<Record<string, string>>({
    range: "7d",
    chat_id: "",
    user_id: "",
    rule: "",
    action: "",
  });
  const [loading, setLoading] = useState(true);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const [selected, setSelected] = useState<Record<number, Violation>>({});

  const queryString = useMemo(() => {
    const params = new URLSearchParams();
    Object.entries(filters).forEach(([key, value]) => {
      if (value) params.set(key, value);
    });
    return params.toString();
  }, [filters]);

  async function load() {
    setLoading(true);
    const vSearch = new URLSearchParams(queryString);
    vSearch.set("limit", "50");

    const wSearch = new URLSearchParams({ limit: "50" });
    if (filters.chat_id) wSearch.set("chat_id", filters.chat_id);
    if (filters.user_id) wSearch.set("user_id", filters.user_id);

    try {
      const [v, w, g] = await Promise.all([
        apiFetch<{ violations: Violation[] }>(
          `/api/admin/violations?${vSearch}`,
        ),
        apiFetch<{ warnings: Warning[] }>(
          `/api/admin/warnings?${wSearch}`,
        ),
        apiFetch<{ groups: Group[] }>("/api/admin/groups"),
      ]);
      setViolations(v.violations ?? []);
      setWarnings(w.warnings ?? []);
      setGroups(g.groups ?? []);
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    const next = { ...filters };
    searchParams.forEach((value, key) => {
      next[key] = value;
    });
    setFilters(next);
  }, [searchParams]);

  useEffect(() => {
    void load();
  }, [queryString]);

  function updateFilter(key: string, value: string) {
    setFilters((current) => ({ ...current, [key]: value }));
  }

  function applyFilters(overrides?: Record<string, string>) {
    const params = new URLSearchParams();
    Object.entries({ ...filters, ...(overrides ?? {}) }).forEach(([key, value]) => {
      if (value) params.set(key, value);
    });
    router.replace(`/violations?${params.toString()}`);
  }

  function resetFilters() {
    setFilters({ range: "7d", chat_id: "", user_id: "", rule: "", action: "" });
    router.replace("/violations");
  }

  async function batchUnban() {
    const items = Object.values(selected);
    const results = await Promise.allSettled(
      items.map((item) =>
        apiFetch<void>(`/api/admin/unban?chat_id=${item.chat_id}&user_id=${item.user_id}`, {
          method: "POST",
        }),
      ),
    );
    const success = results.filter((item) => item.status === "fulfilled").length;
    pushToast(`成功 ${success} 条，失败 ${results.length - success} 条`, success ? "success" : "error");
  }

  async function batchClearWarnings() {
    const items = Object.values(selected);
    const results = await Promise.allSettled(
      items.map((item) =>
        apiFetch<{ cleared: number }>("/api/admin/warnings/clear", {
          method: "POST",
          body: JSON.stringify({ chat_id: item.chat_id, user_id: item.user_id }),
        }),
      ),
    );
    const success = results.filter((item) => item.status === "fulfilled").length;
    pushToast(`成功 ${success} 条，失败 ${results.length - success} 条`, success ? "success" : "error");
  }

  return (
    <AdminShell title="违规记录" subtitle="查看违规和警告历史">
      <Card>
        <CardBody>
          <FilterBar groups={groups} values={filters} onChange={updateFilter} onApply={applyFilters} onReset={resetFilters} mode="violations" />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <span className="text-xs text-[var(--text-muted)]">已选 {Object.keys(selected).length} 条</span>
            <Button size="sm" variant="secondary" onClick={() => void batchUnban()}>批量 unban</Button>
            <Button size="sm" variant="ghost" onClick={() => void batchClearWarnings()}>清除警告</Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>违规 ({violations.length})</CardTitle>
        </CardHeader>
        {violations.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无记录"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell className="w-10" />
                <TableHeaderCell className="w-10" />
                <TableHeaderCell>时间</TableHeaderCell>
                <TableHeaderCell>群</TableHeaderCell>
                <TableHeaderCell>用户</TableHeaderCell>
                <TableHeaderCell>规则</TableHeaderCell>
                <TableHeaderCell>处理</TableHeaderCell>
                <TableHeaderCell>匹配内容</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {violations.map((item) => (
                <Fragment key={item.id}>
                  <TableRow
                    onClick={() =>
                      setExpandedId((current) =>
                        current === item.id ? null : item.id,
                      )
                    }
                    className="cursor-pointer"
                  >
                    <TableCell>
                      <input
                        type="checkbox"
                        checked={Boolean(selected[item.id])}
                        onChange={(e) =>
                          setSelected((current) => {
                            const next = { ...current };
                            if (e.target.checked) next[item.id] = item;
                            else delete next[item.id];
                            return next;
                          })
                        }
                      />
                    </TableCell>
                    <TableCell>
                      {expandedId === item.id ? (
                        <ChevronDown className="h-4 w-4 text-[var(--text-muted)]" />
                      ) : (
                        <ChevronRight className="h-4 w-4 text-[var(--text-muted)]" />
                      )}
                    </TableCell>
                    <TableCell className="text-[var(--text-muted)] tabular-nums whitespace-nowrap">
                      {formatTime(item.created_at)}
                    </TableCell>
                    <TableCell className="tabular-nums">{item.chat_id}</TableCell>
                    <TableCell className="tabular-nums">
                      {item.username ? `@${item.username}` : item.user_id}
                    </TableCell>
                    <TableCell>
                      <code className="text-xs">{item.rule}</code>
                    </TableCell>
                    <TableCell>
                      <Badge tone={actionTone(item.action)}>{item.action}</Badge>
                    </TableCell>
                    <TableCell className="max-w-xs truncate text-[var(--text-muted)]">
                      {item.matched ?? "—"}
                    </TableCell>
                  </TableRow>
                  {expandedId === item.id ? (
                    <TableRow className="bg-[var(--surface-2)]/40">
                      <TableCell colSpan={8}>
                        <MessageTextBlock text={item.message_text} />
                      </TableCell>
                    </TableRow>
                  ) : null}
                </Fragment>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>警告 ({warnings.length})</CardTitle>
        </CardHeader>
        {warnings.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无记录"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell>时间</TableHeaderCell>
                <TableHeaderCell>群</TableHeaderCell>
                <TableHeaderCell>用户</TableHeaderCell>
                <TableHeaderCell>原因</TableHeaderCell>
                <TableHeaderCell>来源</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {warnings.map((item) => (
                <TableRow key={item.id}>
                  <TableCell className="text-[var(--text-muted)] tabular-nums whitespace-nowrap">
                    {formatTime(item.created_at)}
                  </TableCell>
                  <TableCell className="tabular-nums">{item.chat_id}</TableCell>
                  <TableCell className="tabular-nums">{item.user_id}</TableCell>
                  <TableCell className="text-[var(--text-muted)]">
                    {item.reason ?? "—"}
                  </TableCell>
                  <TableCell className="text-[var(--text-muted)]">
                    {item.issued_by ?? "系统"}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>
    </AdminShell>
  );
}


export default function ViolationsPage() {
  return (
    <Suspense fallback={<div className="p-6 text-sm text-[var(--text-muted)]">加载中…</div>}>
      <ViolationsPageInner />
    </Suspense>
  );
}
