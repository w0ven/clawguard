"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Search } from "lucide-react";
import { AdminShell } from "@/components/admin-shell";
import { Card, CardHeader, CardTitle, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Tabs } from "@/components/ui/tabs";
import { apiFetch } from "@/lib/api";
import type { UserTrust } from "@/lib/types";
import { useToast } from "@/components/providers";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeaderCell,
  TableRow,
} from "@/components/ui/table";

const PAGE_SIZE = 50;

type TrustStatus =
  | "all"
  | "new"
  | "trusted"
  | "suspicious"
  | "banned"
  | "archived";

type TrustFilters = {
  chat_id: string;
  user_id: string;
  username: string;
  joined_since: string;
  joined_until: string;
  status: TrustStatus;
  page: string;
};

type TrustListResponse = {
  items: UserTrust[];
  total: number;
  has_more: boolean;
  counts: Record<string, number>;
};

const defaultFilters: TrustFilters = {
  chat_id: "",
  user_id: "",
  username: "",
  joined_since: "",
  joined_until: "",
  status: "all",
  page: "1",
};

function statusTone(
  status: string,
): "success" | "warning" | "danger" | "default" {
  if (status === "trusted") return "success";
  if (status === "suspicious") return "warning";
  if (status === "banned") return "danger";
  return "default";
}

const statusLabel: Record<string, string> = {
  all: "全部",
  new: "待定",
  trusted: "已毕业",
  suspicious: "可疑",
  banned: "已封禁",
  archived: "已归档",
};

const sourceLabel = (source?: string) =>
  ({
    ai: "AI 审核",
    profile_match: "入群 bio",
    cas: "CAS 黑名单",
    bio_on_message: "消息前 bio",
    manual_admin: "管理员手动",
    manual_spam_cmd: "/spam 命令",
    historical_backfill: "历史回填",
    filter_rule: "规则命中",
    warnings_threshold: "警告阈值",
  })[source ?? ""] ?? source ?? "未知";

function buildQueryString(filters: TrustFilters) {
  const params = new URLSearchParams();
  if (filters.chat_id.trim()) params.set("chat_id", filters.chat_id.trim());
  if (filters.user_id.trim()) params.set("user_id", filters.user_id.trim());
  if (filters.username.trim()) params.set("username", filters.username.trim());
  if (filters.joined_since) params.set("joined_since", filters.joined_since);
  if (filters.joined_until) params.set("joined_until", filters.joined_until);
  if (filters.status !== "all") params.set("status", filters.status);
  if (filters.page !== "1") params.set("page", filters.page);
  return params.toString();
}

function buildApiSearch(filters: TrustFilters) {
  const params = new URLSearchParams({
    limit: String(PAGE_SIZE),
    offset: String((Math.max(1, Number(filters.page) || 1) - 1) * PAGE_SIZE),
  });
  if (filters.status !== "all") params.set("status", filters.status);
  if (filters.chat_id.trim()) params.set("chat_id", filters.chat_id.trim());
  if (filters.user_id.trim()) params.set("user_id", filters.user_id.trim());
  if (filters.username.trim()) params.set("username", filters.username.trim());
  if (filters.joined_since) params.set("joined_since", filters.joined_since);
  if (filters.joined_until) params.set("joined_until", filters.joined_until);
  return params.toString();
}

function displayPrimaryName(item: UserTrust) {
  if (item.username) return `@${item.username}`;
  const fullName = [item.first_name, item.last_name].filter(Boolean).join(" ").trim();
  if (fullName) return fullName;
  return "匿名";
}

function TrustPageInner() {
  const { pushToast } = useToast();
  const router = useRouter();
  const searchParams = useSearchParams();
  const [items, setItems] = useState<UserTrust[]>([]);
  const [counts, setCounts] = useState<Record<string, number>>({});
  const [filters, setFilters] = useState<TrustFilters>(defaultFilters);
  const [draft, setDraft] = useState<TrustFilters>(defaultFilters);
  const [total, setTotal] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(true);

  const page = Math.max(1, Number(filters.page) || 1);

  async function load(activeFilters: TrustFilters) {
    setLoading(true);
    try {
      const payload = await apiFetch<TrustListResponse>(
        `/api/admin/user-trust?${buildApiSearch(activeFilters)}`,
      );
      setItems(payload.items ?? []);
      setCounts(payload.counts ?? {});
      setTotal(payload.total ?? 0);
      setHasMore(Boolean(payload.has_more));
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "加载失败", "error");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    const next: TrustFilters = { ...defaultFilters };
    const chatId = searchParams.get("chat_id");
    const userId = searchParams.get("user_id");
    const username = searchParams.get("username");
    const joinedSince = searchParams.get("joined_since");
    const joinedUntil = searchParams.get("joined_until");
    const status = searchParams.get("status");
    const pageValue = searchParams.get("page");

    if (chatId) next.chat_id = chatId;
    if (userId) next.user_id = userId;
    if (username) next.username = username;
    if (joinedSince) next.joined_since = joinedSince;
    if (joinedUntil) next.joined_until = joinedUntil;
    if (status && status in statusLabel) next.status = status as TrustStatus;
    if (pageValue) next.page = pageValue;

    setFilters(next);
    setDraft(next);
  }, [searchParams]);

  useEffect(() => {
    void load(filters);
  }, [filters]);

  function updateDraft(key: keyof TrustFilters, value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
  }

  function applyFilters(base: TrustFilters = draft, nextStatus?: TrustStatus, nextPage?: number) {
    const next: TrustFilters = {
      ...base,
      status: nextStatus ?? base.status,
      page: String(nextPage ?? 1),
    };
    const nextQuery = buildQueryString(next);
    router.replace(nextQuery ? `/trust?${nextQuery}` : "/trust");
  }

  function onChangeTab(value: string) {
    const nextStatus = value as TrustStatus;
    const nextDraft = { ...draft, status: nextStatus, page: "1" };
    setDraft(nextDraft);
    applyFilters(nextDraft, nextStatus, 1);
  }

  function goToPage(nextPage: number) {
    const nextDraft = { ...draft, page: String(nextPage) };
    setDraft(nextDraft);
    applyFilters(nextDraft, undefined, nextPage);
  }

  async function updateStatus(item: UserTrust, nextStatus: string) {
    try {
      await apiFetch(`/api/admin/user-trust/${item.chat_id}/${item.user_id}`, {
        method: "PUT",
        body: JSON.stringify({
          status: nextStatus,
          score: item.score,
          notes: item.notes,
        }),
      });
      await load(filters);
      pushToast("已更新", "success");
    } catch (e) {
      pushToast(e instanceof Error ? e.message : "更新失败", "error");
    }
  }

  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const showBannedMeta = filters.status === "banned";
  const tabs = (["all", "new", "trusted", "suspicious", "banned", "archived"] as TrustStatus[]).map(
    (value) => ({
      value,
      label: `${statusLabel[value]} (${counts[value] ?? 0})`,
    }),
  );

  return (
    <AdminShell
      title="信任系统"
      subtitle="待定 / 已毕业 / 可疑 / 已封禁 状态管理"
    >
      <Card>
        <CardBody>
          <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_180px_180px_auto]">
            <Input
              placeholder="按 chat_id 过滤"
              value={draft.chat_id}
              onChange={(e) => updateDraft("chat_id", e.target.value)}
            />
            <Input
              placeholder="按 user_id 精确搜索"
              value={draft.user_id}
              onChange={(e) => updateDraft("user_id", e.target.value)}
            />
            <Input
              placeholder="按 username / 姓名模糊搜索"
              value={draft.username}
              onChange={(e) => updateDraft("username", e.target.value)}
            />
            <Input
              type="date"
              value={draft.joined_since}
              onChange={(e) => updateDraft("joined_since", e.target.value)}
            />
            <Input
              type="date"
              value={draft.joined_until}
              onChange={(e) => updateDraft("joined_until", e.target.value)}
            />
            <Button onClick={() => applyFilters()}>
              <Search className="h-3.5 w-3.5" />
              筛选
            </Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardHeader className="gap-3 sm:flex-row sm:items-center sm:justify-between">
          <div>
            <CardTitle>用户信任状态 ({total})</CardTitle>
          </div>
          <Tabs tabs={tabs} value={filters.status} onValueChange={onChangeTab} />
        </CardHeader>
        {items.length === 0 ? (
          <div className="px-5 py-12 text-center text-sm text-[var(--text-muted)]">
            {loading ? "加载中…" : "无记录，Bot 开始处理消息后会自动记录"}
          </div>
        ) : (
          <>
            <Table>
              <TableHead>
                <TableRow>
                  <TableHeaderCell>群</TableHeaderCell>
                  <TableHeaderCell>用户</TableHeaderCell>
                  <TableHeaderCell>加入时间</TableHeaderCell>
                  <TableHeaderCell>状态</TableHeaderCell>
                  {showBannedMeta && <TableHeaderCell>封禁时间</TableHeaderCell>}
                  {showBannedMeta && <TableHeaderCell>封禁原因</TableHeaderCell>}
                  <TableHeaderCell>清洁/审核</TableHeaderCell>
                  <TableHeaderCell>得分</TableHeaderCell>
                  <TableHeaderCell>操作</TableHeaderCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {items.map((item) => (
                  <TableRow key={`${item.chat_id}-${item.user_id}`}>
                    <TableCell className="tabular-nums">{item.chat_id}</TableCell>
                    <TableCell>
                      <div className="font-medium">{displayPrimaryName(item)}</div>
                      <div className="text-xs text-[var(--text-muted)] tabular-nums">
                        ID: {item.user_id}
                      </div>
                    </TableCell>
                    <TableCell className="text-[var(--text-muted)] whitespace-nowrap">
                      {new Date(item.joined_at).toLocaleDateString("zh-CN")}
                    </TableCell>
                    <TableCell>
                      <Badge tone={statusTone(item.status)}>
                        {statusLabel[item.status] ?? item.status}
                      </Badge>
                    </TableCell>
                    {showBannedMeta && (
                      <TableCell className="text-[var(--text-muted)] whitespace-nowrap">
                        {item.banned_at ? new Date(item.banned_at).toLocaleString("zh-CN") : "—"}
                      </TableCell>
                    )}
                    {showBannedMeta && (
                      <TableCell className="min-w-[260px]">
                        <div className="flex flex-wrap items-center gap-1.5">
                          {item.banned_reason?.rule && (
                            <Badge tone="danger">{item.banned_reason.rule}</Badge>
                          )}
                          {item.banned_reason?.matched && (
                            <span
                              className="max-w-[220px] truncate text-xs text-[var(--text-muted)]"
                              title={item.banned_reason.matched}
                            >
                              {item.banned_reason.matched}
                            </span>
                          )}
                          {item.banned_reason?.source && (
                            <Badge tone="default">{sourceLabel(item.banned_reason.source)}</Badge>
                          )}
                          {!item.banned_reason && "—"}
                        </div>
                      </TableCell>
                    )}
                    <TableCell className="tabular-nums text-[var(--text-muted)]">
                      {item.messages_clean} / {item.messages_checked}
                    </TableCell>
                    <TableCell className="tabular-nums text-[var(--text-muted)]">
                      {(item.score ?? 0).toFixed(2)}
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <Button
                          size="sm"
                          variant="secondary"
                          onClick={() => updateStatus(item, "trusted")}
                        >
                          信任
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => updateStatus(item, "new")}
                        >
                          重置
                        </Button>
                        <Button
                          size="sm"
                          variant="danger"
                          onClick={() => updateStatus(item, "banned")}
                        >
                          封禁
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>

            <div className="flex items-center justify-between border-t border-[var(--border)] px-5 py-4 text-sm">
              <div className="text-[var(--text-muted)]">
                第 {page} / {totalPages} 页
              </div>
              <div className="flex gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  disabled={page <= 1 || loading}
                  onClick={() => goToPage(page - 1)}
                >
                  上一页
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  disabled={!hasMore || loading}
                  onClick={() => goToPage(page + 1)}
                >
                  下一页
                </Button>
              </div>
            </div>
          </>
        )}
      </Card>
    </AdminShell>
  );
}

export default function TrustPage() {
  return (
    <Suspense fallback={<div className="p-6 text-sm text-[var(--text-muted)]">加载中…</div>}>
      <TrustPageInner />
    </Suspense>
  );
}
