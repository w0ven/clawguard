"use client";

import { Fragment, Suspense, useEffect, useMemo, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardDescription, CardBody } from "@/components/ui/card";
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
import type { AIDecision, Group } from "@/lib/types";
import { useToast } from "@/components/providers";
import { MessageTextBlock } from "@/components/message-text-block";
import { FilterBar } from "@/components/filter-bar";
import { Check, AlertTriangle, ChevronDown, ChevronRight, RotateCcw } from "lucide-react";

const DANGER_VERDICTS = ["ad", "scam", "harass", "spam", "porn", "violence"];

function verdictTone(
  v: string,
): "success" | "warning" | "danger" | "default" {
  if (v === "normal" || v === "clean") return "success";
  if (v === "suspicious") return "warning";
  if (DANGER_VERDICTS.includes(v)) return "danger";
  return "default";
}

function actionLabel(a: string | undefined): {
  text: string;
  tone: "success" | "warning" | "danger" | "default";
} {
  if (!a || a === "allow" || a === "") return { text: "放行", tone: "default" };
  if (a === "flag") return { text: "仅标记", tone: "default" };
  if (a.includes("ban")) return { text: "删除+封禁", tone: "danger" };
  if (a.includes("mute")) return { text: "删除+禁言", tone: "warning" };
  if (a.includes("warn")) return { text: "删除+警告", tone: "warning" };
  if (a.includes("delete")) return { text: "删除", tone: "warning" };
  return { text: a, tone: "default" };
}

function overrideBadge(
  o: string | null | undefined,
): { text: string; tone: "success" | "warning" | "danger" | "default" } | null {
  if (!o) return null;
  if (o === "confirm") return { text: "已标 正确", tone: "success" };
  if (o === "false_positive") return { text: "已标 误封", tone: "danger" };
  if (o === "false_negative") return { text: "已标 漏判", tone: "warning" };
  return { text: o, tone: "default" };
}

function AIReviewPageInner() {
  const { pushToast } = useToast();
  const router = useRouter();
  const searchParams = useSearchParams();
  const [items, setItems] = useState<AIDecision[]>([]);
  const [groups, setGroups] = useState<Group[]>([]);
  const [loading, setLoading] = useState(true);
  const [expandedId, setExpandedId] = useState<number | null>(null);
  const [selected, setSelected] = useState<Record<number, AIDecision>>({});
  const [filters, setFilters] = useState<Record<string, string>>({
    range: "7d",
    chat_id: "",
    user_id: "",
    verdict: "",
    category: "",
  });

  const queryString = useMemo(() => {
    const params = new URLSearchParams();
    Object.entries(filters).forEach(([key, value]) => {
      if (value) params.set(key, value);
    });
    return params.toString();
  }, [filters]);

  async function load() {
    setLoading(true);
    try {
      const [p, g] = await Promise.all([
        apiFetch<{ decisions: AIDecision[] }>(`/api/admin/ai-decisions?limit=100&${queryString}`),
        apiFetch<{ groups: Group[] }>("/api/admin/groups"),
      ]);
      setItems(p.decisions ?? []);
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
    router.replace(`/ai-review?${params.toString()}`);
  }

  function resetFilters() {
    setFilters({ range: "7d", chat_id: "", user_id: "", verdict: "", category: "" });
    router.replace("/ai-review");
  }

  async function applyDecision(item: AIDecision, override: string, options?: { reload?: boolean; toast?: boolean }) {
    const shouldReload = options?.reload ?? true;
    const shouldToast = options?.toast ?? true;
    try {
      const res = await apiFetch<{
        unban_performed?: boolean;
        trust_score_changed?: number;
      }>(`/api/admin/ai-decisions/${item.id}`, {
        method: "PUT",
        body: JSON.stringify({ admin_override: override }),
      });
      if (shouldReload) await load();

      // friendly toast based on real effect
      if (!shouldToast) return;
      if (override === "false_positive") {
        if (res.unban_performed) {
          pushToast("已解封用户", "success");
        } else {
          pushToast("已标记为误封", "success");
        }
      } else if (override === "confirm") {
        pushToast("已标记为正确", "success");
      } else if (override === "false_negative") {
        pushToast("已标记为漏判", "success");
      }
    } catch (e) {
      if (shouldToast) pushToast(e instanceof Error ? e.message : "操作失败", "error");
      throw e;
    }
  }

  async function batchOverride(override: string) {
    const entries = Object.values(selected);
    const results = await Promise.allSettled(entries.map((item) => applyDecision(item, override, { reload: false, toast: false })));
    const success = results.filter((item) => item.status === "fulfilled").length;
    const failed = results.length - success;
    if (results.length > 0) await load();
    pushToast(`批量操作完成：成功 ${success} 条，失败 ${failed} 条`, failed === 0 ? "success" : "error");
  }

  return (
    <AdminShell
      title="AI 判定历史"
      subtitle="所有消息都已按阈值自动处理。这里展示历史记录，你可以标注是否准确来训练模型。"
    >
      <Card>
        <CardBody>
          <FilterBar groups={groups} values={filters} onChange={updateFilter} onApply={applyFilters} onReset={resetFilters} mode="ai" />
          <div className="mt-3 flex flex-wrap items-center gap-2">
            <span className="text-xs text-[var(--text-muted)]">已选 {Object.keys(selected).length} 条</span>
            <Button size="sm" variant="secondary" onClick={() => void batchOverride("confirm")}>批量正确</Button>
            <Button size="sm" variant="danger" onClick={() => void batchOverride("false_positive")}>批量误封</Button>
            <Button size="sm" variant="ghost" onClick={() => void batchOverride("false_negative")}>批量漏判</Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>近期 AI 判定 ({items.length})</CardTitle>
          <CardDescription>
            点击「误封」会尝试自动解封被 ban 的用户。标注「正确 / 漏判」会调整用户信任分。
          </CardDescription>
        </CardHeader>
        {items.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "暂无 AI 判定记录"}
          </div>
        ) : (
          <Table>
            <TableHead>
              <TableRow>
                <TableHeaderCell className="w-10" />
                <TableHeaderCell className="w-10" />
                <TableHeaderCell className="w-36">时间</TableHeaderCell>
                <TableHeaderCell>消息</TableHeaderCell>
                <TableHeaderCell>AI 判定</TableHeaderCell>
                <TableHeaderCell>已执行</TableHeaderCell>
                <TableHeaderCell>置信度</TableHeaderCell>
                <TableHeaderCell>标注</TableHeaderCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {items.map((item) => {
                const act = actionLabel(
                  (item as AIDecision & { action_taken?: string }).action_taken,
                );
                const badge = overrideBadge(item.admin_override);
                return (
                  <Fragment key={item.id}>
                    <TableRow
                      className="cursor-pointer"
                      onClick={() =>
                        setExpandedId((current) =>
                          current === item.id ? null : item.id,
                        )
                      }
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
                      <TableCell className="whitespace-nowrap text-xs tabular-nums text-[var(--text-muted)]">
                        {new Date(item.created_at).toLocaleString("zh-CN", {
                          year: "numeric",
                          month: "2-digit",
                          day: "2-digit",
                          hour: "2-digit",
                          minute: "2-digit",
                          second: "2-digit",
                          hour12: false,
                        })}
                      </TableCell>
                      <TableCell className="max-w-[360px]">
                        <p className="text-sm line-clamp-2">
                          {item.message_text ?? "—"}
                        </p>
                        <p className="mt-1 text-xs text-[var(--text-muted)]">
                          用户 {item.user_id}
                          {item.reason ? ` · ${item.reason}` : ""}
                        </p>
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-col gap-1">
                          <Badge tone={verdictTone(item.verdict)}>
                            {item.verdict}
                          </Badge>
                          {item.category && (
                            <span className="text-xs text-[var(--text-muted)]">
                              {item.category}
                            </span>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge tone={act.tone}>{act.text}</Badge>
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {(item.confidence * 100).toFixed(0)}%
                      </TableCell>
                      <TableCell>
                        {badge ? (
                          <div className="flex items-center gap-2">
                            <Badge tone={badge.tone}>{badge.text}</Badge>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={(e) => {
                                e.stopPropagation();
                                void applyDecision(item, "");
                              }}
                              title="清除标注"
                            >
                              <RotateCcw className="h-3.5 w-3.5" />
                            </Button>
                          </div>
                        ) : (
                          <div className="flex gap-1">
                            <Button
                              size="sm"
                              variant="secondary"
                              onClick={(e) => {
                                e.stopPropagation();
                                void applyDecision(item, "confirm");
                              }}
                            >
                              <Check className="h-3.5 w-3.5" />
                              正确
                            </Button>
                            <Button
                              size="sm"
                              variant="danger"
                              onClick={(e) => {
                                e.stopPropagation();
                                void applyDecision(item, "false_positive");
                              }}
                              title="AI 判错了，尝试解封"
                            >
                              <RotateCcw className="h-3.5 w-3.5" />
                              误封
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              onClick={(e) => {
                                e.stopPropagation();
                                void applyDecision(item, "false_negative");
                              }}
                              title="AI 放过了但应该杀"
                            >
                              <AlertTriangle className="h-3.5 w-3.5" />
                              漏判
                            </Button>
                          </div>
                        )}
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
                );
              })}
            </TableBody>
          </Table>
        )}
      </Card>
    </AdminShell>
  );
}


export default function AIReviewPage() {
  return (
    <Suspense fallback={<div className="p-6 text-sm text-[var(--text-muted)]">加载中…</div>}>
      <AIReviewPageInner />
    </Suspense>
  );
}
